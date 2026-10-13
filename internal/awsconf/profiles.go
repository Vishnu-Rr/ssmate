// Package awsconf discovers AWS profiles and turns them into loaded SDK
// configuration.
package awsconf

import (
	"bufio"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Profile is one entry from the shared AWS config or credentials files.
type Profile struct {
	Name       string
	Region     string
	SSOSession string
}

// DefaultPaths returns the shared config and credentials file paths,
// honouring the standard environment overrides.
func DefaultPaths() (configPath, credentialsPath string) {
	home, _ := os.UserHomeDir()
	configPath = os.Getenv("AWS_CONFIG_FILE")
	if configPath == "" {
		configPath = filepath.Join(home, ".aws", "config")
	}
	credentialsPath = os.Getenv("AWS_SHARED_CREDENTIALS_FILE")
	if credentialsPath == "" {
		credentialsPath = filepath.Join(home, ".aws", "credentials")
	}
	return configPath, credentialsPath
}

// ParseProfiles reads profile names out of the shared config and credentials
// files. Either reader may be nil. The result is sorted by name and
// deduplicated, with config-file values winning over credentials-file ones.
//
// The AWS SDK for Go v2 has no exported API for enumerating profiles, so this
// parses the INI itself. It deliberately understands only what we need:
// section headers, `region` and `sso_session`.
func ParseProfiles(configFile, credentialsFile io.Reader) []Profile {
	byName := map[string]Profile{}

	// Credentials first, so config values overwrite them.
	scanSections(credentialsFile, false, byName)
	scanSections(configFile, true, byName)

	out := make([]Profile, 0, len(byName))
	for _, p := range byName {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// scanSections walks an INI file. In the config file every section except
// "default" is written as "[profile name]"; in the credentials file sections
// are bare "[name]". "[sso-session name]" sections are not profiles.
func scanSections(r io.Reader, isConfig bool, into map[string]Profile) {
	if r == nil {
		return
	}
	var current string
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			current = ""
			header := strings.TrimSpace(line[1 : len(line)-1])
			switch {
			case strings.HasPrefix(header, "sso-session "):
				// Not a profile.
				continue
			case isConfig && header == "default":
				current = "default"
			case isConfig && strings.HasPrefix(header, "profile "):
				current = strings.TrimSpace(strings.TrimPrefix(header, "profile "))
			case isConfig:
				// A bare section in the config file is not a valid profile.
				continue
			default:
				current = header
			}
			if current != "" {
				p := into[current]
				p.Name = current
				into[current] = p
			}
			continue
		}
		if current == "" {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		p := into[current]
		switch key {
		case "region":
			p.Region = value
		case "sso_session":
			p.SSOSession = value
		}
		into[current] = p
	}
}
