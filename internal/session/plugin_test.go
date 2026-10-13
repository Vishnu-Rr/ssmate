package session

import (
	"strings"
	"testing"
)

func TestCompareVersionsOrdersNumerically(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"1.2.764.0", "1.2.764.0", 0},
		{"1.2.835.0", "1.2.764.0", 1},
		{"1.2.764.0", "1.2.835.0", -1},
		{"1.2.100.0", "1.2.99.0", 1}, // numeric, not lexicographic
		{"1.10.0.0", "1.9.0.0", 1},   // ditto
		{"1.2.835", "1.2.835.0", 0},  // missing trailing components are zero
		{"2.0.0.0", "1.99.99.99", 1},
	}
	for _, tc := range cases {
		if got := CompareVersions(tc.a, tc.b); got != tc.want {
			t.Errorf("CompareVersions(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestCompareVersionsHandlesGarbage(t *testing.T) {
	// Must not panic on unparseable input; treat junk components as zero.
	if got := CompareVersions("", "1.2.3.4"); got != -1 {
		t.Errorf("CompareVersions(empty, v) = %d, want -1", got)
	}
	if got := CompareVersions("not.a.version", "0.0.0.0"); got != 0 {
		t.Errorf("CompareVersions(garbage, zero) = %d, want 0", got)
	}
}

func TestInstallHintIsAConcreteCommandPerPlatform(t *testing.T) {
	cases := []struct{ goos, goarch, want string }{
		{"darwin", "arm64", "brew install --cask session-manager-plugin"},
		{"darwin", "amd64", "brew install --cask session-manager-plugin"},
		{"linux", "arm64", "ubuntu_arm64"},
		{"linux", "amd64", "ubuntu_64bit"},
		{"windows", "amd64", "SessionManagerPluginSetup.exe"},
	}
	for _, tc := range cases {
		got := InstallHint(tc.goos, tc.goarch)
		if got == "" {
			t.Errorf("InstallHint(%s,%s) is empty", tc.goos, tc.goarch)
			continue
		}
		if !strings.Contains(got, tc.want) {
			t.Errorf("InstallHint(%s,%s) = %q, want it to mention %q", tc.goos, tc.goarch, got, tc.want)
		}
	}
}

func TestInstallHintNeverReturnsOnlyADocumentationLink(t *testing.T) {
	// The whole point is to hand the user something they can run.
	for _, goos := range []string{"darwin", "linux", "windows"} {
		got := InstallHint(goos, "amd64")
		if strings.HasPrefix(strings.TrimSpace(got), "http") {
			t.Errorf("InstallHint(%s) returned a bare URL: %q", goos, got)
		}
	}
}

func TestSupportsEnvTokenBoundary(t *testing.T) {
	// Versions after 1.2.497.0 read the session token from an environment
	// variable instead of argv, which keeps it out of the process table.
	if CompareVersions("1.2.498.0", EnvTokenSince) <= 0 {
		t.Error("1.2.498.0 should be newer than the env-token boundary")
	}
	if CompareVersions("1.2.497.0", EnvTokenSince) != 0 {
		t.Error("the boundary version should compare equal to itself")
	}
}

func TestMinimumVersionIsTheSigningRelease(t *testing.T) {
	// AWS documents 1.2.764.0 as the minimum supported plugin, and that is
	// the release where OpenDataChannel signing was reapplied. If this
	// constant drifts, the doctor check stops matching AWS's own floor.
	if MinimumVersion != "1.2.764.0" {
		t.Fatalf("MinimumVersion = %q, want 1.2.764.0", MinimumVersion)
	}
}
