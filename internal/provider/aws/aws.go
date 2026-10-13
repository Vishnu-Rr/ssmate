package aws

import (
	"context"
	"fmt"
	"os"

	"github.com/Vishnu-Rr/ssmate/internal/awsconf"
	"github.com/Vishnu-Rr/ssmate/internal/doctor"
	"github.com/Vishnu-Rr/ssmate/internal/inventory"
	"github.com/Vishnu-Rr/ssmate/internal/provider"
	"github.com/Vishnu-Rr/ssmate/internal/session"
	sdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/aws/aws-sdk-go-v2/service/sts"
)

// Provider reaches machines through Amazon's Systems Manager.
type Provider struct{}

// Compile-time proof that this satisfies the seam. A provider that drifts out
// of shape must fail the build, not the user.
var _ provider.Provider = Provider{}

// Handler opens Session Manager sessions. Session Manager reaches any machine
// whose agent registered with it - an EC2 instance, or a machine elsewhere
// registered through a hybrid activation - so the handler works from the
// route's target alone and never assumes the node is an EC2 instance.
type Handler struct{}

var _ provider.Handler = Handler{}

func init() {
	provider.Register(Provider{})
	provider.RegisterHandler(Handler{})
}

// Kind is the route kind this handler serves.
func (Handler) Kind() string { return inventory.RouteSSM }

// New returns the Amazon provider. Registration happens in init, so importing
// this package is enough; New exists for tests that want one directly.
func New() Provider { return Provider{} }

func (Provider) Cloud() string { return inventory.CloudAWS }

// Detect reports whether Amazon is usable here.
//
// It deliberately does not call the API. Detection runs on every invocation,
// including for clouds you never use, so it must be a local file and
// environment check. Whether those credentials actually work is a question for
// the diagnosis, which names the fix.
func (Provider) Detect(context.Context) (bool, string) {
	if awsConfigured() {
		return true, ""
	}
	return false, "no AWS credentials found; run ssmate doctor for how to set them up"
}

// Inventory sweeps every selected profile and region.
func (p Provider) Inventory(ctx context.Context, o provider.Options) ([]inventory.Node, error) {
	profiles := []string{o.Account}
	if o.AllAccounts {
		cfgPath, credPath := awsconf.DefaultPaths()
		cf, _ := os.Open(cfgPath)
		crf, _ := os.Open(credPath)
		defer func() {
			if cf != nil {
				cf.Close()
			}
			if crf != nil {
				crf.Close()
			}
		}()
		profiles = nil
		for _, pr := range awsconf.ParseProfiles(cf, crf) {
			profiles = append(profiles, pr.Name)
		}
	}

	// Resolve each profile's credentials exactly once. Doing it inside the
	// per-region fetch re-resolves them, and on a single-sign-on profile
	// re-exchanges tokens, once per region.
	var targets []inventory.Target
	for _, profile := range profiles {
		cfg, err := Credentials(ctx, profile, o.Region)
		if err != nil {
			return nil, err
		}
		regions := []string{cfg.Region}
		if o.Region == "" {
			if all, err := awsconf.EnabledRegions(ctx, cfg); err == nil {
				regions = all
			}
		}
		for _, r := range regions {
			targets = append(targets, inventory.Target{Profile: profile, Region: r})
		}
	}

	fetch := func(ctx context.Context, profile, region string) ([]inventory.Node, error) {
		cfg, err := awsconf.Load(ctx, profile, region)
		if err != nil {
			return nil, err
		}
		return inventory.FetchRegion(ctx, ssm.NewFromConfig(cfg), ec2.NewFromConfig(cfg), profile, region)
	}

	conc := o.Concurrency
	if conc <= 0 {
		// These are paginated read-only describes, far below the describe
		// token bucket. Concurrency was never what made this fast, though:
		// the cost was one TLS handshake per region per service, which a
		// shared connection pool removes.
		conc = 24
	}
	o.Report("Searching %d region(s) across %d profile(s)...", len(targets), len(profiles))
	nodes, errs := inventory.FanOut(ctx, targets, conc, fetch)
	for _, err := range errs {
		// One unreachable region must never cost the other twenty. Accounts
		// routinely have regions that are enabled but not reachable.
		o.Skipped("skipped: %v", err)
	}
	return nodes, nil
}

// Shell opens an interactive session along a Session Manager route.
func (Handler) Shell(ctx context.Context, _ inventory.Node, r inventory.Route, o provider.SessionOptions) error {
	req := session.ShellRequest(r.Target, o.Raw)
	if len(o.Command) > 0 {
		req = session.CommandRequest(r.Target, o.Command)
	}
	req.Region, req.Profile, req.Reason = r.Region, r.Account, o.Reason
	if o.Print {
		fmt.Println(session.PrintCommand(req))
		return nil
	}
	cfg, err := Credentials(ctx, r.Account, r.Region)
	if err != nil {
		return err
	}
	plugin, err := Plugin()
	if err != nil {
		return err
	}
	return session.Run(ctx, ssm.NewFromConfig(cfg), plugin, req, endpoint(r.Region))
}

// Forward opens a tunnel along a Session Manager route.
func (Handler) Forward(ctx context.Context, _ inventory.Node, r inventory.Route, f session.Forward, o provider.SessionOptions) error {
	req := session.ForwardRequest(r.Target, f)
	req.Region, req.Profile, req.Reason = r.Region, r.Account, o.Reason
	if o.Print {
		fmt.Println(session.PrintCommand(req))
		return nil
	}
	cfg, err := Credentials(ctx, r.Account, r.Region)
	if err != nil {
		return err
	}
	plugin, err := Plugin()
	if err != nil {
		return err
	}
	return session.RunForwarding(ctx, ssm.NewFromConfig(cfg), plugin, req, endpoint(r.Region), o.OnReconnect)
}

// Checks returns Amazon's diagnosis.
func (p Provider) Checks(ctx context.Context, n *inventory.Node, query string) doctor.Registry {
	profile, region := "", ""
	if n != nil {
		profile, region = n.Account, n.Region
	}
	reg := doctor.Registry{
		doctor.PluginCheck(),
		doctor.CredentialsCheck(func(c context.Context) (string, error) {
			cfg, err := Credentials(c, profile, region)
			if err != nil {
				return "", err
			}
			id, err := sts.NewFromConfig(cfg).GetCallerIdentity(c, &sts.GetCallerIdentityInput{})
			if err != nil {
				return "", err
			}
			return "account " + sdk.ToString(id.Account), nil
		}),
	}
	if query != "" {
		reg = append(reg, doctor.TargetChecks(n, query)...)
	}
	return reg
}

func endpoint(region string) string {
	return fmt.Sprintf("https://ssm.%s.amazonaws.com", region)
}
