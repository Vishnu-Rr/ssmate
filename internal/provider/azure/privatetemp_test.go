package azure

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/Vishnu-Rr/ssmate/internal/session"
)

// fakeAz puts an az on PATH that behaves like the bastion command does for
// Entra sign-in: it writes a key into a fresh directory under its temporary
// directory, records where that was, and exits with code.
func fakeAz(t *testing.T, code string) (record string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake az is a shell script")
	}
	bin := t.TempDir()
	record = filepath.Join(t.TempDir(), "tmpdir")
	script := "#!/bin/sh\n" +
		"d=\"${TMPDIR:-/tmp}/aadsshcertTEST\"\n" +
		"mkdir -p \"$d\" && echo key > \"$d/id_rsa\"\n" +
		"echo \"$TMPDIR\" > \"" + record + "\"\n" +
		"exit " + code + "\n"
	if err := os.WriteFile(filepath.Join(bin, "az"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return record
}

func TestPrivateTempRemovesTheSignInKey(t *testing.T) {
	record := fakeAz(t, "0")

	err := runAzPrivateTemp(context.Background(), []string{"network", "bastion", "ssh"})
	if !errors.Is(err, session.ErrClosedByUser) {
		t.Fatalf("a clean az exit = %v, want ErrClosedByUser", err)
	}
	raw, rerr := os.ReadFile(record)
	if rerr != nil {
		t.Fatal(rerr)
	}
	dir := strings.TrimSpace(string(raw))
	if !strings.HasPrefix(filepath.Base(dir), "ssmate-az-") {
		t.Fatalf("az ran with TMPDIR %q, want a ssmate-az- directory of its own", dir)
	}
	if _, serr := os.Stat(dir); !os.IsNotExist(serr) {
		t.Errorf("%s still exists after the session; the key az wrote there was left behind", dir)
	}
}

func TestPrivateTempStillReportsAFailedSession(t *testing.T) {
	fakeAz(t, "255")

	err := runAzPrivateTemp(context.Background(), []string{"network", "bastion", "ssh"})
	if err == nil || errors.Is(err, session.ErrClosedByUser) {
		t.Fatalf("az exiting 255 = %v, want a failure", err)
	}
}
