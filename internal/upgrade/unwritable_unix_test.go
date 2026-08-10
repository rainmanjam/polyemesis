//go:build unix

package upgrade

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The flavour of "unwritable" that TestSystemdRefusesAnUnwritableDirectory does
// not reach: a directory that EXISTS, holds the live binary, and still refuses a
// create. That is the stock systemd install -- deploy/polyemesis.service sets
// ProtectSystem=strict and /usr/local/bin is not in ReadWritePaths -- and it is
// the case an operator actually hits. The other test builds its directory by not
// having one, which no OS and no uid can write to but is also not what happens on
// a real box.
//
// unix and non-root on purpose. Mode bits are the only thing in the standard
// library that can build this, and they are not portable: uid 0 has
// CAP_DAC_OVERRIDE, and on Windows os.Chmod sets FILE_ATTRIBUTE_READONLY, which
// NTFS ignores when a file is created inside a directory. Rather than pretend to
// test it everywhere, this runs where it is real, and the predicate it exercises
// -- upgrade.writable -- is a create probe rather than a mode check precisely so
// that the same code is correct on the platforms this file skips.
func TestSystemdRefusesADirectoryThatExistsAndDeniesCreates(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root, which can write anywhere")
	}
	dir := tempDir(t)
	binary := filepath.Join(dir, "polyemesis")
	if err := os.WriteFile(binary, []byte("live"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	// Restored so t.TempDir's own cleanup can remove it.
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	// The premise, asserted rather than assumed: a case whose precondition
	// quietly did not hold is a case that passes for the wrong reason.
	if err := writable(dir); err == nil {
		t.Fatal("precondition not built: a 0500 directory still accepts a create here")
	}

	p := PlanFor(MethodSystemd, binary, "v0.6.0")
	if p.Automatic {
		t.Error("offered an automatic upgrade into a directory it cannot write; it would fail half way")
	}
	if !strings.Contains(p.Reason, dir) {
		t.Errorf("the refusal does not name the directory that cannot be written: %q", p.Reason)
	}
	if got, err := os.ReadFile(binary); err != nil || string(got) != "live" {
		t.Errorf("planning disturbed the live binary: %q, %v", got, err)
	}
}
