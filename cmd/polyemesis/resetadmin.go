package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"

	"github.com/rainmanjam/polyemesis/internal/config"
	"github.com/rainmanjam/polyemesis/internal/db"
)

// resetAdmin sets a new password for the admin account, from the box the
// database lives on.
//
// WHY THIS EXISTS. Before it, an operator who lost the password had exactly one
// route: stop the service, `DELETE FROM users`, and let first-run setup run
// again. That works — HasUser is a row count and needsSetup is its negation —
// but it is a bad instruction to give people, for a reason worth stating
// plainly.
//
// POST /api/v1/setup is UNAUTHENTICATED. Its only guard is CreateUser refusing
// to run when a user exists. Deleting the row removes that guard, so between the
// DELETE and the operator finishing setup, anyone who can reach the port can
// claim the account. The window is small and easy to close by stopping the
// service first — and it is also entirely avoidable, which is what this is.
//
// Setting the password directly never opens it: the account keeps existing, the
// guard on /setup stays armed the whole time, and there is no moment at which
// the install is unowned.
//
// SESSIONS ARE INVALIDATED. Bumping the token epoch is the half that makes this
// a security operation rather than a convenience. Someone resetting a forgotten
// password may be locking an intruder out, and leaving that intruder's existing
// session valid would defeat the whole exercise.
func resetAdmin(cfg config.Config, in io.Reader, out io.Writer, revokeTokens bool) error {
	store, err := db.Open(cfg.DBPath())
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer store.Close()

	user, err := store.GetUser()
	if err != nil {
		if errors.Is(err, db.ErrNoUser) {
			// Not an error worth a stack trace: an install nobody has set up yet
			// has no password to reset, and the answer is the setup screen.
			return errors.New("this install has no admin account yet — open the web UI and complete first-run setup")
		}
		return err
	}

	pw, err := readNewPassword(in, out)
	if err != nil {
		return err
	}
	if err := store.SetPassword(user.ID, pw); err != nil {
		return err
	}
	// AFTER the password is changed, not before. If SetPassword fails, the old
	// password still works and the operator's sessions are still theirs; bumping
	// first would have logged everyone out to achieve nothing.
	if err := store.BumpTokenEpoch(user.ID); err != nil {
		return fmt.Errorf("password changed, but existing sessions could not be invalidated: %w", err)
	}

	// WHAT SURVIVES, ALWAYS, AND NOT ONLY WHEN IT IS EMPTY. #718.
	//
	// This printed "every existing session has been signed out" and stopped
	// there. That sentence is true and incomplete: bumping the epoch ends
	// SESSIONS, and API tokens are resolved by hash alone and carry no epoch, so
	// they live on. An operator reaching for this command is usually locked out,
	// which is the compromise case -- and this is the one path that could not
	// tell them what still has access.
	//
	// The HTTP handler for the same gesture reads the surviving tokens back and
	// discloses them. Mirroring that here is the device: the operator is told,
	// every time, rather than left to assume. Deliberately NOT a forced revoke,
	// for the reason that handler's own comment gives -- routine rotation is the
	// common case, and destroying every integration's credential is the wrong
	// default for it. --revoke-api-tokens is the explicit ask.
	fmt.Fprintf(out, "password reset for %q; every existing session has been signed out\n", user.Username)

	if revokeTokens {
		n, rerr := store.DeleteAllAPITokens()
		if rerr != nil {
			fmt.Fprintf(out, "WARNING: the password changed, but the API tokens could not be\n"+
				"  revoked (%v). They still work.\n", rerr)
		} else {
			fmt.Fprintf(out, "%d API token(s) revoked.\n", n)
		}
	}

	tokens, terr := store.ListAPITokens()
	switch {
	case terr != nil:
		fmt.Fprintf(out, "WARNING: could not read the API token list (%v). Tokens are NOT\n"+
			"  ended by a password change, and this command could not tell you what survives.\n", terr)
	case len(tokens) == 0:
		fmt.Fprintln(out, "no API tokens exist, so nothing else can reach this install.")
	default:
		fmt.Fprintf(out, "\n%d API TOKEN(S) STILL WORK. A password change does not end them:\n", len(tokens))
		for _, t := range tokens {
			fmt.Fprintf(out, "  - %s (%s, created %s)\n", t.Name, t.Scope, t.CreatedAt.Format("2006-01-02"))
		}
		fmt.Fprintln(out, "Re-run with --revoke-api-tokens to delete them, or revoke them\n"+
			"individually in Settings once you can sign in.")
	}
	return nil
}

// readNewPassword asks twice and compares, without echoing to a terminal.
//
// Not taken as a flag value on purpose. A password on the command line lands in
// the shell history, in `ps` output for every other user on the box, and in any
// audit log that records argv — three places it is harder to remove from than it
// was to put in.
//
// Reading from a pipe is supported so this can be scripted (`printf '%s\n%s\n' …
// | polyemesis -reset-admin`), which is a deliberate choice and not an accident
// of the implementation: an operator automating a rebuild should not be pushed
// back towards editing the database by hand.
func readNewPassword(in io.Reader, out io.Writer) (string, error) {
	if f, ok := in.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		fmt.Fprint(out, "New password: ")
		first, err := term.ReadPassword(int(f.Fd()))
		fmt.Fprintln(out)
		if err != nil {
			return "", err
		}
		fmt.Fprint(out, "Repeat: ")
		second, err := term.ReadPassword(int(f.Fd()))
		fmt.Fprintln(out)
		if err != nil {
			return "", err
		}
		if string(first) != string(second) {
			return "", errors.New("the two passwords do not match")
		}
		return validated(string(first))
	}

	// Piped. Two lines, so a script cannot silently set a password it did not
	// mean to by feeding a single line twice.
	r := bufio.NewReader(in)
	first, err := r.ReadString('\n')
	if err != nil && first == "" {
		return "", errors.New("no password on standard input")
	}
	second, err := r.ReadString('\n')
	if err != nil && second == "" {
		return "", errors.New("expected the password twice on standard input, one per line")
	}
	if strings.TrimRight(first, "\r\n") != strings.TrimRight(second, "\r\n") {
		return "", errors.New("the two passwords do not match")
	}
	return validated(strings.TrimRight(first, "\r\n"))
}

func validated(pw string) (string, error) {
	if len(pw) < db.MinPasswordLength {
		return "", fmt.Errorf("password must be at least %d characters", db.MinPasswordLength)
	}
	return pw, nil
}
