// Package session opens SSM sessions and hands them to AWS's
// session-manager-plugin.
package session

import (
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"strconv"
	"strings"

	"github.com/Vishnu-Rr/ssmate/internal/exitcode"
)

const (
	// PluginBinary is the executable AWS ships.
	PluginBinary = "session-manager-plugin"

	// MinimumVersion is the oldest plugin AWS documents as supported. It is
	// also the release in which signing of the channel-open request was
	// reapplied, which is why AWS drew the line there.
	MinimumVersion = "1.2.764.0"

	// EnvTokenSince is the last version that could NOT read the session
	// response from an environment variable. Anything newer can, which keeps
	// the session token out of the process table where other users could
	// read it with ps.
	EnvTokenSince = "1.2.497.0"

	// EnvTokenVar is the variable name the plugin looks for.
	EnvTokenVar = "AWS_SSM_START_SESSION_RESPONSE"
)

// PluginInfo describes the plugin we found on this machine.
type PluginInfo struct {
	Path             string
	Version          string
	TooOld           bool
	SupportsEnvToken bool
}

// FindPlugin locates the plugin and asks it for its version.
//
// It distinguishes "not installed" from "installed but built for the wrong
// CPU". The AWS CLI conflates them by checking only for a missing file, which
// is why installing the Intel build on Apple silicon produces a baffling
// error there. Ours names the actual problem.
func FindPlugin() (*PluginInfo, error) {
	path, err := exec.LookPath(PluginBinary)
	if err != nil {
		return nil, exitcode.New(
			exitcode.Prereq,
			PluginBinary+" is not installed",
			InstallHint(runtime.GOOS, runtime.GOARCH),
			nil,
		)
	}

	out, err := exec.Command(path, "--version").Output()
	if err != nil {
		// An exec format error means the binary exists but cannot run on
		// this CPU: almost always an x86 build on an arm64 machine.
		var execErr *exec.Error
		if errors.As(err, &execErr) || strings.Contains(err.Error(), "exec format") {
			return nil, exitcode.New(
				exitcode.Prereq,
				fmt.Sprintf("%s at %s cannot run on %s/%s", PluginBinary, path, runtime.GOOS, runtime.GOARCH),
				"Reinstall the build for your CPU: "+InstallHint(runtime.GOOS, runtime.GOARCH),
				err,
			)
		}
		return nil, exitcode.New(
			exitcode.Prereq,
			fmt.Sprintf("could not run %s --version", path),
			InstallHint(runtime.GOOS, runtime.GOARCH),
			err,
		)
	}

	version := strings.TrimSpace(string(out))
	return &PluginInfo{
		Path:             path,
		Version:          version,
		TooOld:           CompareVersions(version, MinimumVersion) < 0,
		SupportsEnvToken: CompareVersions(version, EnvTokenSince) > 0,
	}, nil
}

// CompareVersions compares dotted numeric versions, returning -1, 0 or 1.
// Missing trailing components count as zero and unparseable components count
// as zero, so this never panics on unexpected plugin output.
func CompareVersions(a, b string) int {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	n := len(as)
	if len(bs) > n {
		n = len(bs)
	}
	for i := 0; i < n; i++ {
		x, y := 0, 0
		if i < len(as) {
			x, _ = strconv.Atoi(strings.TrimSpace(as[i]))
		}
		if i < len(bs) {
			y, _ = strconv.Atoi(strings.TrimSpace(bs[i]))
		}
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	return 0
}

// InstallHint returns a command the user can run to install the plugin on
// their platform. It returns a command, never only a link, because a link is
// one more step between a broken tool and a working one.
func InstallHint(goos, goarch string) string {
	const base = "https://s3.amazonaws.com/session-manager-downloads/plugin/latest"
	switch goos {
	case "darwin":
		return "brew install --cask session-manager-plugin"
	case "linux":
		pkg := "ubuntu_64bit"
		if goarch == "arm64" {
			pkg = "ubuntu_arm64"
		}
		return fmt.Sprintf(
			"curl -o /tmp/session-manager-plugin.deb %s/%s/session-manager-plugin.deb "+
				"&& sudo dpkg -i /tmp/session-manager-plugin.deb",
			base, pkg,
		)
	case "windows":
		return fmt.Sprintf("Download and run %s/windows/SessionManagerPluginSetup.exe", base)
	default:
		return fmt.Sprintf("See %s for your platform", base)
	}
}
