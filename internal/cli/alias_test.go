package cli

import "testing"

func TestSSHIsConnect(t *testing.T) {
	cmd, rest, err := NewRoot("test").Find([]string{"ssh", "web-1", "uptime"})
	if err != nil {
		t.Fatal(err)
	}
	if cmd.Name() != "connect" {
		t.Fatalf("ssmate ssh ran %q, want connect; the root would read ssh as a machine name", cmd.Name())
	}
	if len(rest) != 2 || rest[0] != "web-1" || rest[1] != "uptime" {
		t.Fatalf("arguments after ssh = %v, want [web-1 uptime]", rest)
	}
}
