package main

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/uploads"
)

// seedLeftover writes a file into the uploads directory and backdates it past
// the sweep's cutoff.
func seedLeftover(t *testing.T, dataDir, name, body string, old bool) string {
	t.Helper()
	dir := filepath.Join(dataDir, uploads.Dir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir uploads: %v", err)
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatalf("seed %s: %v", name, err)
	}
	if old {
		when := time.Now().Add(-24 * time.Hour)
		if err := os.Chtimes(p, when, when); err != nil {
			t.Fatalf("backdate %s: %v", name, err)
		}
	}
	return p
}

// #185. Nothing in the product ever swept <dataDir>/uploads, so a ".partial-"
// file stranded by a process killed mid-upload stayed forever -- invisible,
// unreferenceable, and occupying up to MaxUploadBytes of the volume the
// database, the recorder and the HLS preview share, with the free-space floor
// unaware of it.
//
// THE CONTROL IS IN THE SAME TEST: a sweep that removed everything and a sweep
// that removed the right things look identical to any assertion that only
// checks the leftover is gone.
func TestStartupClearsStrandedUploadsAndKeepsRealOnes(t *testing.T) {
	dataDir := t.TempDir()
	stranded := seedLeftover(t, dataDir, ".partial-1234567.ts", "stranded bytes", true)
	kept := seedLeftover(t, dataDir, "show-abcd1234.ts", "real media", true)
	arriving := seedLeftover(t, dataDir, ".partial-7654321.ts", "still landing", false)

	var buf bytes.Buffer
	sweepUploadLeftovers(dataDir, slog.New(slog.NewTextHandler(&buf, nil)))

	if _, err := os.Stat(stranded); err == nil {
		t.Error("a staged file left by a killed process survived startup; nothing " +
			"else in the product will ever remove it")
	}
	if _, err := os.Stat(kept); err != nil {
		t.Errorf("startup deleted a published upload: %v", err)
	}
	if _, err := os.Stat(arriving); err != nil {
		t.Errorf("startup deleted a staged file younger than the cutoff: %v", err)
	}

	// IT HAS TO SAY SO. The whole reason this runs at boot is the boot after
	// the crash that stranded gigabytes, and an operator wondering where their
	// disk went has no other way to find out that it came back.
	log := buf.String()
	for _, want := range []string{"uploads", "stagedFiles=1", "stagedBytes=14"} {
		if !strings.Contains(log, want) {
			t.Errorf("the startup log does not report %q: %s", want, log)
		}
	}
}

// A boot that removed nothing must say nothing. A line every restart saying
// "nothing to do" is a line operators learn to skip, and this one has to be
// readable on the boot where it matters.
func TestStartupIsSilentWhenThereIsNothingToClear(t *testing.T) {
	dataDir := t.TempDir()
	seedLeftover(t, dataDir, "show-abcd1234.ts", "real media", true)

	var buf bytes.Buffer
	sweepUploadLeftovers(dataDir, slog.New(slog.NewTextHandler(&buf, nil)))

	if buf.Len() != 0 {
		t.Errorf("a boot with nothing to clear logged: %s", buf.String())
	}
}

// A data directory that cannot be used is a warning, never a refusal to boot.
// The uploads path carries no ingest and no recording, and taking a live
// broadcast off air over a tidy-up would be the worse outcome by a wide margin.
func TestStartupSurvivesAnUnusableUploadsDirectory(t *testing.T) {
	dataDir := t.TempDir()
	// A FILE where the uploads directory belongs, which uploads.New's MkdirAll
	// cannot turn into one.
	if err := os.WriteFile(filepath.Join(dataDir, uploads.Dir), []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	var buf bytes.Buffer
	sweepUploadLeftovers(dataDir, slog.New(slog.NewTextHandler(&buf, nil)))

	if !strings.Contains(buf.String(), "level=WARN") {
		t.Errorf("an unusable uploads directory produced no warning: %s", buf.String())
	}
}
