package awsconf

import (
	"strings"
	"testing"
)

const sampleConfig = `
[default]
region = us-west-2

[profile prod]
region = eu-west-1
sso_session = corp

[profile no-region]
output = json

[sso-session corp]
sso_start_url = https://example.awsapps.com/start
sso_region = us-east-1

; a comment
# another comment
`

const sampleCredentials = `
[default]
aws_access_key_id = EXAMPLE_KEY_ID_1

[legacy]
aws_access_key_id = EXAMPLE_KEY_ID_2
`

func names(ps []Profile) []string {
	out := make([]string, 0, len(ps))
	for _, p := range ps {
		out = append(out, p.Name)
	}
	return out
}

func find(t *testing.T, ps []Profile, name string) Profile {
	t.Helper()
	for _, p := range ps {
		if p.Name == name {
			return p
		}
	}
	t.Fatalf("profile %q not found in %v", name, names(ps))
	return Profile{}
}

func TestParseProfilesFindsAllProfiles(t *testing.T) {
	got := ParseProfiles(strings.NewReader(sampleConfig), strings.NewReader(sampleCredentials))
	want := map[string]bool{"default": true, "prod": true, "no-region": true, "legacy": true}
	if len(got) != len(want) {
		t.Fatalf("got %d profiles %v, want %d", len(got), names(got), len(want))
	}
	for _, p := range got {
		if !want[p.Name] {
			t.Errorf("unexpected profile %q", p.Name)
		}
	}
}

func TestParseProfilesSkipsSSOSessionSections(t *testing.T) {
	for _, p := range ParseProfiles(strings.NewReader(sampleConfig), nil) {
		if p.Name == "corp" {
			t.Fatal("sso-session section was parsed as a profile")
		}
	}
}

func TestParseProfilesReadsRegion(t *testing.T) {
	got := ParseProfiles(strings.NewReader(sampleConfig), nil)
	if p := find(t, got, "prod"); p.Region != "eu-west-1" {
		t.Errorf("prod region = %q, want eu-west-1", p.Region)
	}
	if p := find(t, got, "no-region"); p.Region != "" {
		t.Errorf("no-region region = %q, want empty", p.Region)
	}
}

func TestParseProfilesReadsSSOSession(t *testing.T) {
	got := ParseProfiles(strings.NewReader(sampleConfig), nil)
	if p := find(t, got, "prod"); p.SSOSession != "corp" {
		t.Errorf("prod sso_session = %q, want corp", p.SSOSession)
	}
}

func TestParseProfilesDeduplicatesAcrossFiles(t *testing.T) {
	got := ParseProfiles(strings.NewReader(sampleConfig), strings.NewReader(sampleCredentials))
	seen := 0
	for _, p := range got {
		if p.Name == "default" {
			seen++
		}
	}
	if seen != 1 {
		t.Fatalf("default appeared %d times, want 1", seen)
	}
}

func TestParseProfilesHandlesNilReaders(t *testing.T) {
	if got := ParseProfiles(nil, nil); len(got) != 0 {
		t.Fatalf("got %d profiles from nil readers, want 0", len(got))
	}
}

func TestParseProfilesResultIsSorted(t *testing.T) {
	got := names(ParseProfiles(strings.NewReader(sampleConfig), strings.NewReader(sampleCredentials)))
	for i := 1; i < len(got); i++ {
		if got[i-1] > got[i] {
			t.Fatalf("profiles not sorted: %v", got)
		}
	}
}
