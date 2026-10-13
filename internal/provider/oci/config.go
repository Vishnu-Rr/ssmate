// Package oci reaches machines through Oracle's Bastion service.
//
// The shape differs from Amazon's in one way that drives everything here:
// there is no agent to ask. A bastion is a thing you provision, it binds
// permanently to one VCN when created, and a machine is reachable if and only
// if some bastion sits in its VCN. So reachability is a join across the
// network topology rather than a health report, and a machine can be perfectly
// healthy and still unreachable because nobody built the door.
package oci

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/oracle/oci-go-sdk/v65/common"
)

// DefaultProfile is the profile OCI's own tooling uses when none is named. It
// is a literal name in the file rather than an absence, which is why an empty
// Account has to become this rather than staying empty.
const DefaultProfile = "DEFAULT"

// ConfigPath is where OCI keeps its config. OCI_CLI_CONFIG_FILE overrides it,
// the same variable the vendor CLI honours.
func ConfigPath() string {
	if p := os.Getenv("OCI_CLI_CONFIG_FILE"); p != "" {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".oci", "config")
}

// Profile is one stanza of the config file.
type Profile struct {
	Name   string
	Region string
	// Token is true when this profile authenticates with a session token
	// rather than an API key. The two need different credential providers and
	// fail differently: a token expires every hour or so and is re-obtained
	// with `oci session authenticate`, while an API key does not expire at
	// all. Telling the user to re-run the wrong one wastes their time.
	Token bool
}

// Profiles lists what the config file declares, in file order.
//
// It parses rather than delegating because the SDK offers no way to enumerate
// profiles, and because choosing a credential provider requires knowing which
// kind each profile is before building one.
func Profiles(path string) ([]Profile, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var out []Profile
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			out = append(out, Profile{Name: strings.TrimSpace(line[1 : len(line)-1])})
			continue
		}
		if len(out) == 0 {
			continue // a key before any stanza: not ours to interpret
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		cur := &out[len(out)-1]
		switch strings.TrimSpace(key) {
		case "region":
			cur.Region = strings.TrimSpace(value)
		case "security_token_file":
			cur.Token = strings.TrimSpace(value) != ""
		}
	}
	return out, sc.Err()
}

// Configured reports whether this machine has any OCI config at all. Like the
// Amazon check it touches only the filesystem: detection runs on every
// invocation, including for clouds the user does not have, so it must not
// make a network call or read a key.
func Configured() bool {
	path := ConfigPath()
	if path == "" {
		return false
	}
	st, err := os.Stat(path)
	return err == nil && !st.IsDir()
}

// credentials builds a configuration provider for one profile.
//
// Which constructor is correct depends on how the profile authenticates, and
// picking the wrong one fails with a message about a missing field rather
// than about the real problem.
func credentials(profile string) (common.ConfigurationProvider, error) {
	if profile == "" {
		profile = DefaultProfile
	}
	path := ConfigPath()
	if path == "" {
		return nil, fmt.Errorf("cannot locate an OCI config file")
	}
	profiles, err := Profiles(path)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	for _, p := range profiles {
		if p.Name != profile {
			continue
		}
		if p.Token {
			return common.CustomProfileSessionTokenConfigProvider(path, profile), nil
		}
		return common.CustomProfileConfigProvider(path, profile), nil
	}
	return nil, fmt.Errorf("no profile %q in %s", profile, path)
}

// regionOf reports the region a profile is configured for, which is where a
// sweep starts when the user names no region.
func regionOf(profile string) string {
	if profile == "" {
		profile = DefaultProfile
	}
	profiles, err := Profiles(ConfigPath())
	if err != nil {
		return ""
	}
	for _, p := range profiles {
		if p.Name == profile {
			return p.Region
		}
	}
	return ""
}

// credentialProblem turns the SDK's twelve-line authentication failure into
// one line naming the fix.
//
// The two credential kinds fail identically on the wire and differently in
// reality: a session token expires roughly hourly and is renewed by signing in
// again, while an API key does not expire and a 401 means the key or its
// fingerprint is wrong. Telling someone to re-run the wrong one wastes their
// afternoon.
func credentialProblem(profile string, err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	authFailed := strings.Contains(msg, "NotAuthenticated") ||
		strings.Contains(msg, "Http Status Code: 401")
	if !authFailed {
		return err
	}
	for _, p := range mustProfiles() {
		if p.Name != profile {
			continue
		}
		if p.Token {
			return fmt.Errorf("session token for profile %s has expired; run: oci session authenticate --profile-name %s", profile, profile)
		}
		return fmt.Errorf("API key for profile %s was rejected; check key_file and fingerprint in %s", profile, ConfigPath())
	}
	return err
}

// mustProfiles reads the config and swallows the error, because it is only
// ever used to improve a message that is already being returned.
func mustProfiles() []Profile {
	p, err := Profiles(ConfigPath())
	if err != nil {
		return nil
	}
	return p
}
