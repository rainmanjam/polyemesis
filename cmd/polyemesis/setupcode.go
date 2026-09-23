package main

import (
	"fmt"
	"io"
	"log/slog"
	"os"

	"github.com/rainmanjam/polyemesis/internal/auth"
	"github.com/rainmanjam/polyemesis/internal/config"
	"github.com/rainmanjam/polyemesis/internal/db"
)

// prepareSetupCode readies the one-time code POST /api/v1/setup requires, or
// clears a leftover one when the install already has an admin. See
// auth.SetupCode for why the code exists at all.
//
// Before the listener opens, not after: a server that could answer setup for
// even a moment without a code would reopen the race the code closes.
func prepareSetupCode(cfg config.Config, store *db.DB) (*auth.SetupCode, error) {
	has, err := store.HasUser()
	if err != nil {
		return nil, err
	}
	return auth.PrepareSetupCode(cfg.DataDir, has, os.Getenv(auth.SetupCodeEnv))
}

// reportSetupCode prints the code, once, under the startup banner.
//
// Stdout rather than the structured log, because stdout is what journalctl and
// docker logs show an operator scrolling up to the banner -- and so the code
// sits in one place in the log, not in every structured line a log shipper
// copies somewhere else. The structured log gets the file's path only.
func reportSetupCode(w io.Writer, log *slog.Logger, code *auth.SetupCode) {
	if !code.Pending() {
		return
	}
	fmt.Fprintf(w, "  FIRST RUN: no admin account exists yet. Creating it needs this setup code:\n\n")
	fmt.Fprintf(w, "      %s\n\n", code.Code())
	fmt.Fprintf(w, "  It is also saved in %s (readable by this service's user only).\n", code.Path())
	fmt.Fprintf(w, "  Open the web UI, enter it with a username and password, and it is used up.\n")
	switch {
	case code.Preset():
		fmt.Fprintf(w, "  (Set by %s.)\n", auth.SetupCodeEnv)
	case code.Reused():
		fmt.Fprintf(w, "  (The same code as before the last restart.)\n")
	}
	fmt.Fprintln(w)
	log.Info("first run: creating the admin account needs the setup code", "file", code.Path())
}
