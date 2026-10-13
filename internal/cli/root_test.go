package cli

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

func TestRootHasTheExpectedSubcommands(t *testing.T) {
	root := NewRoot("test")
	want := map[string]bool{"connect": false, "ls": false, "doctor": false}
	for _, c := range root.Commands() {
		if _, ok := want[c.Name()]; ok {
			want[c.Name()] = true
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("subcommand %q is missing", name)
		}
	}
}

func TestRootDeclaresGlobalFlags(t *testing.T) {
	root := NewRoot("test")
	for _, name := range []string{"profile", "region", "all-profiles", "refresh", "json", "no-input", "print"} {
		if root.PersistentFlags().Lookup(name) == nil {
			t.Errorf("global flag --%s is missing", name)
		}
	}
}

func TestHelpMentionsWhatTheToolIsFor(t *testing.T) {
	root := NewRoot("test")
	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetArgs([]string{"--help"})
	if err := root.Execute(); err != nil {
		t.Fatalf("--help returned an error: %v", err)
	}
	out := strings.ToLower(buf.String())
	if !strings.Contains(out, "ssm") {
		t.Errorf("help should say what this connects to:\n%s", buf.String())
	}
}

func TestVersionIsReported(t *testing.T) {
	root := NewRoot("1.2.3-test")
	if !strings.Contains(root.Version, "1.2.3-test") {
		t.Fatalf("root.Version = %q, want it to carry the build version", root.Version)
	}
}

func TestFileIsTerminalRejectsNonTerminals(t *testing.T) {
	// Regression guard. chooseNode used to open the picker regardless of
	// whether anyone could answer it, which crashed against a pipe AND
	// exited zero, so a script could not tell it had failed.
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	if fileIsTerminal(r) {
		t.Error("a pipe must not be reported as a terminal")
	}

	f, err := os.CreateTemp(t.TempDir(), "stdin")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if fileIsTerminal(f) {
		t.Error("a regular file must not be reported as a terminal")
	}

	if fileIsTerminal(nil) {
		t.Error("a nil file must not be reported as a terminal")
	}

	// The case that defeated the previous implementation. /dev/null IS a
	// character device, so a char-device test called it a terminal and the
	// picker was opened against it.
	devnull, err := os.Open(os.DevNull)
	if err != nil {
		t.Skip("no /dev/null on this platform")
	}
	defer devnull.Close()
	if fileIsTerminal(devnull) {
		t.Error("/dev/null must not be reported as a terminal")
	}
}
