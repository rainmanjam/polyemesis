package main

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// AN UNKNOWN --log VALUE USED TO MEAN info, SILENTLY.
//
// `--log warning` -- the spelling syslog, Python and half the tools an operator
// has used all accept -- started a server logging at info, and `--log trace`
// one logging at less than debug. Nothing said the value had been ignored.
// Staging-readiness row 35.
//
// Mutations: a `default: return slog.LevelInfo, nil` in levelFromFlag's
// switch, observed to fail here on every refused value; and disabling the
// check in run(), observed to fail the test below when the child starts
// serving and runServer's deadline kills it.
func TestLevelFromFlagRefusesWhatItDoesNotKnow(t *testing.T) {
	for in, want := range map[string]slog.Level{
		"debug": slog.LevelDebug, "info": slog.LevelInfo, "warn": slog.LevelWarn,
		"error": slog.LevelError, "WARN": slog.LevelWarn, " info ": slog.LevelInfo,
	} {
		got, err := levelFromFlag(in)
		if err != nil || got != want {
			t.Errorf("levelFromFlag(%q) = %v, %v; want %v, nil", in, got, err, want)
		}
	}
	for _, in := range []string{"warning", "trace", "verbose", "", "0"} {
		_, err := levelFromFlag(in)
		if err == nil {
			t.Errorf("levelFromFlag(%q) was accepted; it would have run at info unasked", in)
			continue
		}
		if !strings.Contains(err.Error(), "debug, info, warn, error") {
			t.Errorf("levelFromFlag(%q): the refusal does not list the accepted values: %v", in, err)
		}
	}
}

// The refusal is where it matters: before the server opens anything.
func TestAnUnknownLogLevelStopsTheServerBeforeItStarts(t *testing.T) {
	cwd := t.TempDir()
	// -addr on an ephemeral port: if the refusal regresses, the child starts a
	// real server, and it must not take a port something else is using.
	out, ok := runServer(t, cwd, "-log", "warning", "-addr", "127.0.0.1:0", "-data", filepath.Join(cwd, "data"))
	if ok {
		t.Fatalf("the server accepted --log warning and exited 0:\n%s", out)
	}
	if !strings.Contains(out, `--log "warning" is not a log level`) {
		t.Errorf("the server stopped, but not with the log-level refusal:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(cwd, "data")); err == nil {
		t.Error("the server created its data directory before refusing the log level")
	}
}
