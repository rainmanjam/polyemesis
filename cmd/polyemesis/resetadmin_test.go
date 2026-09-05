package main

import (
	"bytes"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/config"
	"github.com/rainmanjam/polyemesis/internal/db"

	_ "modernc.org/sqlite"
)

// breakTheTokenTable makes both halves of the token disclosure fail, through a
// connection of its own, in a way that SURVIVES db.Open.
//
// Dropping the table does not work: resetAdmin opens the database itself, and
// Open runs the schema, which puts the table straight back. So this leaves the
// schema exactly as it is and breaks the two operations instead --
//
//   - a BEFORE DELETE trigger that aborts, so DeleteAllAPITokens fails; and
//   - a row whose created_at holds text, so ListAPITokens fails on Scan.
//     SQLite is dynamically typed and stores it happily; the Go driver cannot
//     hand it to an int64.
//
// The alternative was a fake store, which would have tested the fake.
func breakTheTokenTable(t *testing.T, path string) {
	t.Helper()
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	defer raw.Close()
	for _, stmt := range []string{
		`CREATE TRIGGER no_token_deletes BEFORE DELETE ON api_tokens
		 BEGIN SELECT RAISE(ABORT, 'the token table refuses deletes'); END`,
		`INSERT INTO api_tokens (name, prefix, token_hash, created_at, last_used_at, scope)
		 VALUES ('unreadable', 'poly_xx', 'x', 'not-a-timestamp', 0, 'admin')`,
	} {
		if _, err := raw.Exec(stmt); err != nil {
			t.Fatalf("breaking the token table: %v", err)
		}
	}
}

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
	if err := resetAdmin(cfg, in, &out, false); err != nil {
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
	if err := resetAdmin(cfg, strings.NewReader("a-brand-new-password\na-brand-new-password\n"), &out, false); err != nil {
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
			err := resetAdmin(cfg, strings.NewReader(tc.input), &out, false)
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

// A PASSWORD CHANGE DOES NOT END API TOKENS, AND THE OPERATOR IS TOLD SO. #718.
//
// This command is the one an operator reaches for when they cannot sign in,
// which is the compromise case. It printed "every existing session has been
// signed out" and stopped there -- true, and incomplete: bumping token_epoch
// ends SESSIONS, while API tokens are resolved by hash alone, carry no epoch,
// and live on. Nothing listed them, so the sentence read as "access has ended"
// when it had not.
//
// The HTTP handler for the same gesture already reads the surviving tokens back
// and discloses them. This pins that the CLI does too.
func TestResetAdminNamesTheTokensThatSurviveIt(t *testing.T) {
	cfg, store := resetFixture(t)
	if _, err := store.CreateUser("admin", "the-old-password"); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, _, err := store.CreateAPIToken("ci-runner", string(db.ScopeAdmin)); err != nil {
		t.Fatalf("create token: %v", err)
	}
	store.Close()

	var out bytes.Buffer
	in := strings.NewReader("a-brand-new-password\na-brand-new-password\n")
	if err := resetAdmin(cfg, in, &out, false); err != nil {
		t.Fatalf("resetAdmin: %v", err)
	}

	got := out.String()
	if !strings.Contains(got, "ci-runner") {
		t.Errorf("the surviving token is not named in the output, so an operator who "+
			"has just locked out an intruder is not told what still reaches the "+
			"install:\n%s", got)
	}
	if !strings.Contains(got, "STILL WORK") {
		t.Errorf("the output does not say the tokens still work. Naming them is only "+
			"half of it -- the sentence above them says sessions were signed out, and "+
			"a list under that reads as a list of things that were ended:\n%s", got)
	}
	if !strings.Contains(got, "--revoke-api-tokens") {
		t.Errorf("the output does not say how to end them. An operator told there is a "+
			"problem and not told the remedy is worse off than one told nothing:\n%s", got)
	}
}

// And the flag actually revokes, or the sentence above is advice that does not work.
func TestResetAdminCanRevokeTheTokensItWarnsAbout(t *testing.T) {
	cfg, store := resetFixture(t)
	if _, err := store.CreateUser("admin", "the-old-password"); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, _, err := store.CreateAPIToken("ci-runner", string(db.ScopeAdmin)); err != nil {
		t.Fatalf("create token: %v", err)
	}
	store.Close()

	var out bytes.Buffer
	in := strings.NewReader("a-brand-new-password\na-brand-new-password\n")
	if err := resetAdmin(cfg, in, &out, true); err != nil {
		t.Fatalf("resetAdmin: %v", err)
	}

	reopened, err := db.Open(cfg.DBPath())
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer reopened.Close()
	tokens, err := reopened.ListAPITokens()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(tokens) != 0 {
		t.Errorf("--revoke-api-tokens left %d token(s) in the database", len(tokens))
	}
	if got := out.String(); !strings.Contains(got, "1 API token(s) revoked") {
		t.Errorf("the output does not report what was revoked:\n%s", got)
	}
}

// THE DISCLOSURE MUST SURVIVE ITS OWN FAILURE. #718.
//
// The device is that an operator running this command is always told what still
// reaches the install. Its two failure arms are the ones that matter most: they
// fire exactly when the command cannot answer the question, and an operator who
// is not told that silence means "unknown" will read it as "nothing".
//
// Reached by dropping the api_tokens table out from under the command, which is
// the only way to make both the delete and the list fail without a fake store.
// sqlite lets a second connection do that while resetAdmin holds its own.
func TestResetAdminSaysSoWhenItCannotReadTheSurvivingTokens(t *testing.T) {
	cfg, store := resetFixture(t)
	if _, err := store.CreateUser("admin", "the-old-password"); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, _, err := store.CreateAPIToken("ci-runner", string(db.ScopeAdmin)); err != nil {
		t.Fatalf("create token: %v", err)
	}
	store.Close()
	breakTheTokenTable(t, cfg.DBPath())

	var out bytes.Buffer
	in := strings.NewReader("a-brand-new-password\na-brand-new-password\n")
	// --revoke-api-tokens as well, so BOTH failure arms run in one command: the
	// revoke that could not revoke, and the read-back that could not read.
	if err := resetAdmin(cfg, in, &out, true); err != nil {
		t.Fatalf("resetAdmin returned an error rather than reporting the trouble: %v", err)
	}

	got := out.String()
	// The password change itself must still have happened and still be reported.
	// A token table that will not read is not a reason to leave the operator
	// locked out, which is the situation they ran this from.
	if !strings.Contains(got, "password reset") {
		t.Errorf("the password reset is not reported:\n%s", got)
	}
	if !strings.Contains(got, "could not be") || !strings.Contains(got, "still work") {
		t.Errorf("a revoke that failed does not say the tokens still work. An "+
			"operator who asked for a revoke and was not told it failed believes "+
			"the credentials are dead:\n%s", got)
	}
	if !strings.Contains(got, "could not read the API token list") {
		t.Errorf("a token list that could not be read is passed over in silence, "+
			"which an operator reads as 'no tokens exist' -- the opposite of "+
			"what is known:\n%s", got)
	}
	if !strings.Contains(got, "NOT") {
		t.Errorf("the warning does not say tokens are not ended by a password "+
			"change, which is the fact the whole disclosure exists to carry:\n%s", got)
	}
}

// And the ordinary quiet case still says something rather than nothing: an
// install with no tokens gets a sentence, not silence. Silence is what the two
// arms above must be distinguishable from.
func TestResetAdminSaysSoWhenNoTokensExist(t *testing.T) {
	cfg, store := resetFixture(t)
	if _, err := store.CreateUser("admin", "the-old-password"); err != nil {
		t.Fatalf("create: %v", err)
	}
	store.Close()

	var out bytes.Buffer
	in := strings.NewReader("a-brand-new-password\na-brand-new-password\n")
	if err := resetAdmin(cfg, in, &out, false); err != nil {
		t.Fatalf("resetAdmin: %v", err)
	}
	if got := out.String(); !strings.Contains(got, "no API tokens exist") {
		t.Errorf("an install with no tokens is told nothing, so it cannot be "+
			"told apart from one whose token list could not be read:\n%s", got)
	}
}
