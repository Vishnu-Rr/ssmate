package awsconf

import (
	"context"
	"fmt"

	"github.com/Vishnu-Rr/ssmate/internal/exitcode"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
)

// Load builds SDK configuration for one profile and region.
//
// The SDK handles sso-session sections and token refresh natively, which is
// exactly what the older generation of SSM wrappers could not do and the
// single most common complaint against them.
func Load(ctx context.Context, profile, region string) (aws.Config, error) {
	opts := []func(*config.LoadOptions) error{
		// Share one connection pool across every region. See SharedHTTPClient.
		config.WithHTTPClient(SharedHTTPClient()),
	}
	if profile != "" {
		opts = append(opts, config.WithSharedConfigProfile(profile))
	}
	if region != "" {
		opts = append(opts, config.WithRegion(region))
	}
	cfg, err := config.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return aws.Config{}, exitcode.New(
			exitcode.Creds,
			fmt.Sprintf("could not load AWS configuration for profile %q", profile),
			loginHint(profile),
			err,
		)
	}
	return cfg, nil
}

func loginHint(profile string) string {
	if profile == "" {
		return "aws sso login"
	}
	return "aws sso login --profile " + profile
}

// ForRegion returns a copy of cfg pointed at another region.
//
// Use this instead of loading a fresh config per region. Reloading re-resolves
// credentials every time, which on an IAM Identity Center profile can mean a
// token exchange per region, and it discards the connection pool.
func ForRegion(cfg aws.Config, region string) aws.Config {
	out := cfg.Copy()
	out.Region = region
	return out
}

// EnabledRegions lists the regions this account can use.
func EnabledRegions(ctx context.Context, cfg aws.Config) ([]string, error) {
	out, err := ec2.NewFromConfig(cfg).DescribeRegions(ctx, &ec2.DescribeRegionsInput{
		AllRegions: aws.Bool(false),
	})
	if err != nil {
		return nil, fmt.Errorf("list regions: %w", err)
	}
	regions := make([]string, 0, len(out.Regions))
	for _, r := range out.Regions {
		if name := aws.ToString(r.RegionName); name != "" {
			regions = append(regions, name)
		}
	}
	return regions, nil
}
