package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/config"
	"github.com/rainmanjam/polyemesis/internal/db"
)

func resetFixture(t *testing.T) (config.Config, *db.DB) {
	t.Helper()
	dir := t.TempDir()
	cfg := config.Config{DataDir: dir}
	if err := cfg.EnsureDirs(); err != nil {
		t.Fatalf("EnsureDirs: %v", err)
	}
	store, err := db.Open(cfg.DBPath())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return cfg, store
}

// The whole point: the account keeps existing, so /setup stays guarded.
//
// The route this replaces was `DELETE FROM users`, which works but disarms the
// only guard on an UNAUTHENTICATED endpoint -- between the delete and the
// operator finishing setup, anyone who can reach the port can claim the install.
func TestResetAdminNeverLeavesTheInstallUnowned(t *testing.T) {
	cfg, store := resetFixture(t)
	if _, err := store.CreateUser("admin", "the-old-password"); err != nil {
		t.Fatalf("create: %v", err)
	}
	store.Close()

	var out bytes.Buffer
	in := strings.NewReader("a-brand-new-password\na-brand-new-password\n")
	if err := resetAdmin(cfg, in, &out); err != nil {
		t.Fatalf("resetAdmin: %v", err)
	}

	reopened, err := db.Open(cfg.DBPath())
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer reopened.Close()

	// Still owned, so first-run setup is still refused.
	if has, _ := reopened.HasUser(); !has {
		t.Error("the admin account no longer exists — /setup is unauthenticated and " +
			"its only guard is that a user exists, so this would let anyone who can " +
			"reach the port claim the install")
	}
	if _, err := reopened.CreateUser("attacker", "takeover-password"); err == nil {
		t.Error("first-run setup became available after a reset; the install can be taken over")
	}
}

// The password actually changes, and the old one stops working.
func TestResetAdminChangesThePassword(t *testing.T) {
	cfg, store := resetFixture(t)
	u, err := store.CreateUser("admin", "the-old-password")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	before, _ := store.TokenEpoch(u.ID)
	store.Close()

	var out bytes.Buffer
	if err := resetAdmin(cfg, strings.NewReader("a-brand-new-password\na-brand-new-password\n"), &out); err != nil {
		t.Fatalf("resetAdmin: %v", err)
	}

	reopened, _ := db.Open(cfg.DBPath())
	defer reopened.Close()
	fresh, err := reopened.GetUser()
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !fresh.CheckPassword("a-brand-new-password") {
		t.Error("the new password does not authenticate")
	}
	if fresh.CheckPassword("the-old-password") {
		t.Error("the OLD password still authenticates after a reset")
	}

	// Sessions signed out. Someone resetting a forgotten password may be locking
	// an intruder out, and leaving their session valid defeats the exercise.
	after, _ := reopened.TokenEpoch(u.ID)
	if after <= before {
		t.Errorf("token epoch %d -> %d: existing sessions were not invalidated", before, after)
	}
	if !strings.Contains(out.String(), "signed out") {
		t.Errorf("the operator is not told sessions were ended: %q", out.String())
	}
}

func TestResetAdminRefusals(t *testing.T) {
	for _, tc := range []struct {
		name, input, want string
		withUser          bool
	}{
		{"no account yet", "whatever\nwhatever\n", "first-run setup", false},
		{"mismatch", "one-password\ntwo-password\n", "do not match", true},
		{"too short", "abc\nabc\n", "at least", true},
		{"nothing on stdin", "", "no password", true},
		{"only one line", "just-one-line\n", "twice", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, store := resetFixture(t)
			if tc.withUser {
				if _, err := store.CreateUser("admin", "the-old-password"); err != nil {
					t.Fatalf("create: %v", err)
				}
			}
			store.Close()

			var out bytes.Buffer
			err := resetAdmin(cfg, strings.NewReader(tc.input), &out)
			if err == nil {
				t.Fatalf("expected a refusal, got success")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %q, want it to mention %q", err.Error(), tc.want)
			}

			// A refusal must change nothing.
			if tc.withUser {
				reopened, _ := db.Open(cfg.DBPath())
				u, _ := reopened.GetUser()
				if u != nil && !u.CheckPassword("the-old-password") {
					t.Error("a refused reset still changed the password")
				}
				reopened.Close()
			}
		})
	}
}

// The password must not be a flag: argv is visible in ps, lands in shell
// history, and appears in any audit log that records command lines.
func TestThePasswordIsNotTakenFromArgv(t *testing.T) {
	b, err := readSource(t, "main.go")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(b, `flag.String("reset-admin`) || strings.Contains(b, `flag.String("admin-password`) {
		t.Error("the new password is a flag value; it would land in shell history, " +
			"in ps output for every other user on the box, and in argv audit logs")
	}
	if !strings.Contains(b, `flag.Bool("reset-admin"`) {
		t.Error("-reset-admin is no longer a boolean flag")
	}
}

func readSource(t *testing.T, name string) (string, error) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(".", name))
	return string(b), err
}
