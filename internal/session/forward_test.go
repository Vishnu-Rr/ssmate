package session

import "testing"

func TestParseForwardLocalToInstancePort(t *testing.T) {
	got, err := ParseForward("8080:80")
	if err != nil {
		t.Fatalf("ParseForward: %v", err)
	}
	if got.LocalPort != 8080 || got.RemotePort != 80 || got.RemoteHost != "" {
		t.Fatalf("got %+v", got)
	}
}

func TestParseForwardThroughInstanceToAnotherHost(t *testing.T) {
	got, err := ParseForward("5432:db.internal:5432")
	if err != nil {
		t.Fatalf("ParseForward: %v", err)
	}
	if got.LocalPort != 5432 || got.RemoteHost != "db.internal" || got.RemotePort != 5432 {
		t.Fatalf("got %+v", got)
	}
}

func TestParseForwardRejectsBadInput(t *testing.T) {
	for _, spec := range []string{"", "8080", "8080:", ":80", "abc:80", "8080:abc",
		"0:80", "70000:80", "8080:0", "8080:70000", "a:b:c:d", "5432::5432"} {
		if _, err := ParseForward(spec); err == nil {
			t.Errorf("ParseForward(%q) should have failed", spec)
		}
	}
}

func TestParseForwardRejectsIPv6RatherThanMisreadingIt(t *testing.T) {
	// Splitting on ':' would turn this into nonsense. Refusing is honest.
	if _, err := ParseForward("5432:[::1]:5432"); err == nil {
		t.Fatal("IPv6 literal should be refused, not silently mis-parsed")
	}
}

func TestForwardRequestPicksTheRightDocument(t *testing.T) {
	local := ForwardRequest("i-0abc", Forward{LocalPort: 8080, RemotePort: 80})
	if local.DocumentName != "AWS-StartPortForwardingSession" {
		t.Errorf("local forward document = %q", local.DocumentName)
	}
	if _, ok := local.Parameters["host"]; ok {
		t.Error("a local forward must not carry a host parameter")
	}

	remote := ForwardRequest("i-0abc", Forward{LocalPort: 5432, RemoteHost: "db", RemotePort: 5432})
	if remote.DocumentName != "AWS-StartPortForwardingSessionToRemoteHost" {
		t.Errorf("remote forward document = %q", remote.DocumentName)
	}
	if remote.Parameters["host"][0] != "db" {
		t.Errorf("host parameter = %v", remote.Parameters["host"])
	}
}

func TestForwardRoundTripsThroughString(t *testing.T) {
	for _, spec := range []string{"8080:80", "5432:db.internal:5432"} {
		f, err := ParseForward(spec)
		if err != nil {
			t.Fatal(err)
		}
		if f.String() != spec {
			t.Errorf("String() = %q, want %q", f.String(), spec)
		}
	}
}

func TestDescribeSaysWhichDirectionTrafficFlows(t *testing.T) {
	f, _ := ParseForward("5432:db.internal:5432")
	got := f.Describe("i-0abc")
	if got != "localhost:5432 -> db.internal:5432 (via i-0abc)" {
		t.Fatalf("Describe() = %q", got)
	}
}
