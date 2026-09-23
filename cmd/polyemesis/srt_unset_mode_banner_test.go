package main

import (
	"bytes"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/config"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/ffmpeg"
	"github.com/rainmanjam/polyemesis/internal/tlsx"
)

// THE UPGRADING BOOT SAYS WHICH SOURCES IT SET TO SRT, AND ONLY THAT BOOT.
//
// db.MigrateUnsetSourceIngestMode changes a source on the operator's behalf. It
// is right for every console-made source that ran SRT, and wrong for the rare
// one an operator meant for RTMP and never got round to choosing. The warning
// is the only place that names them, so it has to name them -- and it has to
// stay quiet on every later boot, or it becomes a line people learn to skip.
const srtGivenWarning = "sources with no ingest mode were set to SRT"

func TestTheUpgradingBootNamesTheSourcesItSetToSRT(t *testing.T) {
	provider, err := tlsx.New(tlsx.Options{Mode: tlsx.ModeOff})
	if err != nil {
		t.Fatalf("tlsx.New(off): %v", err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "polyemesis.db")

	// The old install: a console-made source, stored exactly as v0.10.0 wrote it.
	old, err := db.Open(path)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	if _, err := old.SQL().Exec(
		`INSERT INTO sources (name, enabled, ingest, token, position, created_at, updated_at)
		 VALUES ('console-made', 1, '{"mode":""}', 'console-made-source-streamid', 1, 1700000000, 1700000000)`); err != nil {
		t.Fatalf("insert the v0.10.0 source: %v", err)
	}
	if err := old.Close(); err != nil {
		t.Fatalf("close the old install: %v", err)
	}

	cfg := config.Default()
	cfg.Addr = "127.0.0.1:8080"
	cfg.DataDir = dir
	boot := func() string {
		t.Helper()
		store, err := db.Open(path)
		if err != nil {
			t.Fatalf("db.Open: %v", err)
		}
		defer store.Close()
		var logs bytes.Buffer
		log := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
		captureStdout(t, func() {
			if err := reportStartup(log, cfg, provider, store, &ffmpeg.Tools{Version: "test-ffmpeg"}); err != nil {
				t.Errorf("reportStartup: %v", err)
			}
		})
		return logs.String()
	}

	first := boot()
	if !strings.Contains(first, srtGivenWarning) || !strings.Contains(first, "console-made") {
		t.Errorf("the boot that set an unset source to SRT did not say so, naming it.\n"+
			"An operator who meant that source for RTMP has no other way to learn "+
			"which card to change.\n\nlog:\n%s", first)
	}

	// The negative control: the migration found nothing to do, so neither may
	// the warning.
	if second := boot(); strings.Contains(second, srtGivenWarning) {
		t.Errorf("a boot that changed nothing still warned %q; a warning on every "+
			"boot is one nobody reads.\n\nlog:\n%s", srtGivenWarning, second)
	}
}
