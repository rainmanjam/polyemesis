package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/config"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/ffmpeg"
	"github.com/rainmanjam/polyemesis/internal/tlsx"
)

// captureStdout runs fn with os.Stdout redirected to a pipe and returns what it
// printed.
//
// THIS PACKAGE MUST NOT USE t.Parallel(), ANYWHERE, AND THAT IS NOT NEGOTIABLE
// WHILE THIS HELPER EXISTS. os.Stdout is one process-wide variable. Two tests
// swapping it at once interleave their output, and the failure is not a clean
// error -- it is one test reading another's banner and asserting happily on it,
// or a flake that appears only under -race on a loaded machine. There is no
// t.Parallel() in cmd/polyemesis today; adding one anywhere in the package
// breaks these tests non-deterministically, which is the worst way to find out.
//
// The alternative -- an io.Writer parameter on reportStartup and reportTLS --
// is a production seam invented for a test, on functions whose entire job is to
// write to the terminal an operator is looking at. It is available if a
// reviewer prefers it; it was not taken unasked.
//
// The pipe is drained in a goroutine because a pipe has a finite buffer (64 KiB
// on Linux, 8 KiB on some BSDs): a banner larger than it would block the
// WRITER, which is the function under test, and the test would hang rather than
// fail. The banner is nowhere near that size; the goroutine costs nothing and
// removes the failure mode entirely.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	orig := os.Stdout
	os.Stdout = w
	// Restore FIRST (cleanups are LIFO, so this runs last) -- a test that fails
	// mid-fn must not leave the whole package writing into a closed pipe.
	t.Cleanup(func() { os.Stdout = orig })

	done := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()

	fn()

	if err := w.Close(); err != nil {
		t.Fatalf("closing the capture pipe: %v", err)
	}
	out := <-done
	_ = r.Close()
	return out
}

// bannerFixture builds the four arguments reportStartup needs.
//
// ORDER MATTERS ON WINDOWS. t.TempDir() is registered FIRST and store.Close is
// registered SECOND, so the LIFO cleanup closes the database before the
// directory is removed. Reversed, Windows refuses to delete a directory holding
// an open file and the test fails in cleanup with an error that names neither
// the test nor the reason.
func bannerFixture(t *testing.T, mode db.IngestMode) (config.Config, *db.DB, *ffmpeg.Tools) {
	t.Helper()

	dir := t.TempDir()
	store, err := db.Open(filepath.Join(dir, "polyemesis.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	s := db.DefaultSettings()
	s.Ingest.Mode = mode
	// PutSettings is the raw write and does NOT validate, which is what makes it
	// the right fixture door: a pull mode with no URL yet is a state the banner
	// must survive, and going through UpdateSettings would refuse to arrange it.
	if err := store.PutSettings(s); err != nil {
		t.Fatalf("PutSettings: %v", err)
	}
	// A SOURCE, because the ingest line is now a statement about one.
	//
	// db.Open stopped seeding one -- MigrateSources only builds "Main" for an
	// install upgrading from single-ingest -- so a fresh database has none, and
	// the banner says so rather than naming a mode nothing is running. Every
	// case in the table below is about which mode a RUNNING install prints, so
	// each one needs the programme that makes that question meaningful. The
	// zero-source case has its own test, which is where the absence belongs.
	//
	// The source carries the DEFAULT ingest rather than this case's mode, and
	// the two are unrelated on purpose: the banner reads the settings singleton,
	// never a source row, so what this row ingests changes nothing it prints.
	// Copying the mode across would only matter for one case and would break it
	// -- CreateSource validates, and "pull with no URL yet" is precisely the
	// state PutSettings is used above to arrange.
	if err := store.CreateSource(&db.Source{
		Name: db.DefaultSourceName, Enabled: true,
		Ingest: db.DefaultSettings().Ingest, Position: 1,
	}); err != nil {
		t.Fatalf("create the fixture's source: %v", err)
	}

	cfg := config.Default()
	cfg.Addr = "127.0.0.1:8080"
	cfg.DataDir = dir

	return cfg, store, &ffmpeg.Tools{Version: "test-ffmpeg"}
}

// bannerIngestLine returns the one line of the banner that names the ingest.
func bannerIngestLine(t *testing.T, out string) string {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "ingest") {
			return line
		}
	}
	t.Fatalf("the startup banner has no ingest line at all:\n%s", out)
	return ""
}

// TestStartupBannerNamesTheIngestModeItActuallyRuns pins what an operator reads
// when the server comes up.
//
// This banner is the entire user interface of a headless first run. Nobody has
// opened the web UI yet -- they cannot, they have no password -- so these eight
// lines are the only thing telling them where to point an encoder. A wrong port
// here does not produce an error; it produces an operator at their firewall,
// opening a port that was never in the path.
//
// THE DEFECT THIS FIXES. In pull mode the banner printed `ingest pull (port
// 6000)`, because ingestPort returns the SRT port for everything that is not
// RTMP. Pull DIALS OUT -- there is no inbound port and no encoder to aim
// anywhere. The project already knew this and said so in code:
// engine.Manager.ListenerBound(db.IngestPull) returns false, its comment
// explains that saying otherwise "would tell an operator a token gates an
// ingest that no publisher ever reaches", and manager_test.go pins it. The
// banner was the last place still telling the operator the opposite.
//
// ingestPort itself is deliberately NOT changed. 6000 genuinely is bound in
// pull mode -- both listeners bind unconditionally (engine/manager.go, "BOTH
// LISTENERS BIND, ALWAYS") -- so its return value is not wrong. What was wrong
// is attributing that port to this ingest, and that decision lives here.
func TestStartupBannerNamesTheIngestModeItActuallyRuns(t *testing.T) {
	provider, err := tlsx.New(tlsx.Options{Mode: tlsx.ModeOff})
	if err != nil {
		t.Fatalf("tlsx.New(off): %v", err)
	}

	for _, tc := range []struct {
		name string
		mode db.IngestMode
		want string
		// absent is asserted against the ingest LINE, not the whole banner.
		absent []string
	}{
		{
			name: "a fresh install says so rather than naming a mode nobody chose",
			mode: db.IngestUnset,
			want: "not chosen yet",
			// An empty mode beside a port number reads as "srt on 6000" to
			// anyone skimming, and that is the one impression a fresh install
			// must not give.
			absent: []string{"(port"},
		},
		{
			name: "srt names the SRT port",
			mode: db.IngestSRT,
			want: "srt (port 6000)",
		},
		{
			name: "rtmp names the RTMP port, not the SRT one",
			mode: db.IngestRTMP,
			want: "rtmp (port 1935)",
		},
		{
			name: "pull names no port at all, because it dials out",
			mode: db.IngestPull,
			want: "pull (dials out; no inbound port)",
			// THE ASSERTION THE FIX EXISTS FOR. Not merely "says pull" -- the
			// old banner said pull too, and then printed a port beside it.
			absent: []string{"(port", "6000", "1935"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, store, tools := bannerFixture(t, tc.mode)
			out := captureStdout(t, func() {
				if err := reportStartup(newLogger("error"), cfg, provider, store, tools); err != nil {
					t.Errorf("reportStartup: %v", err)
				}
			})
			line := bannerIngestLine(t, out)

			if !strings.Contains(line, tc.want) {
				t.Errorf("ingest banner line for mode %q =\n  %q\nwant it to contain %q",
					tc.mode, strings.TrimSpace(line), tc.want)
			}
			for _, bad := range tc.absent {
				if strings.Contains(line, bad) {
					t.Errorf("ingest banner line for mode %q =\n  %q\nwhich contains %q.\n\n"+
						"In pull mode there is no inbound port: polyemesis DIALS the source. "+
						"Printing one sends the operator to their firewall to open a port "+
						"that was never in the path, and it contradicts "+
						"engine.Manager.ListenerBound(db.IngestPull), which returns false "+
						"and is pinned by manager_test.go.",
						tc.mode, strings.TrimSpace(line), bad)
				}
			}
		})
	}
}

// TestStartupBannerDoesNotAdvertiseAnIngestWithNoProgrammeBehindIt is the A7
// half, and it is a lie rather than a crash.
//
// settings.ingest is not read by anything any more: the engine takes its ingest
// from the source row, and the only thing that ever wrote the block through was
// a settings save on an install that had a source. So on a fresh install the
// block can say srt on 6000 while there is no programme to admit anybody to --
// and 6000 really is bound, because both listeners bind unconditionally, so
// nothing downstream contradicts the banner. The operator points an encoder at
// a port that answers and refuses them, and the only place that could have told
// them why is this line.
//
// The mode is set to SRT on PURPOSE. An unset mode already prints "not chosen
// yet" for its own reason, so a fixture that left it unset would pass this test
// with the source count doing nothing at all.
func TestStartupBannerDoesNotAdvertiseAnIngestWithNoProgrammeBehindIt(t *testing.T) {
	provider, err := tlsx.New(tlsx.Options{Mode: tlsx.ModeOff})
	if err != nil {
		t.Fatalf("tlsx.New(off): %v", err)
	}

	dir := t.TempDir()
	store, err := db.Open(filepath.Join(dir, "polyemesis.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	// The premise, asserted rather than assumed: if db.Open ever starts seeding
	// a source again this test would be describing an ordinary install and
	// would pass or fail for reasons that have nothing to do with the banner.
	if n, err := store.CountSources(); err != nil || n != 0 {
		t.Fatalf("a freshly opened database has %d sources (err %v); this test is about "+
			"the install that has none", n, err)
	}

	s := db.DefaultSettings()
	s.Ingest.Mode = db.IngestSRT
	if err := store.PutSettings(s); err != nil {
		t.Fatalf("PutSettings: %v", err)
	}

	cfg := config.Default()
	cfg.Addr = "127.0.0.1:8080"
	cfg.DataDir = dir

	out := captureStdout(t, func() {
		if err := reportStartup(newLogger("error"), cfg, provider, store,
			&ffmpeg.Tools{Version: "test-ffmpeg"}); err != nil {
			t.Errorf("reportStartup: %v", err)
		}
	})
	line := bannerIngestLine(t, out)

	for _, bad := range []string{"srt", "(port", "6000"} {
		if strings.Contains(line, bad) {
			t.Errorf("the ingest banner line on an install with no source =\n  %q\nwhich "+
				"contains %q.\n\nsettings.ingest describes a programme that does not exist: "+
				"nothing reads that block, the engine reads the source row, and there is no "+
				"source row. The port named here is bound and will refuse the encoder aimed "+
				"at it, so this line is the only thing that could have told the operator "+
				"what to do instead.", strings.TrimSpace(line), bad)
		}
	}
	// And it has to say what to do, not merely stop saying the wrong thing. A
	// blank where the ingest was reads as a build problem.
	if !strings.Contains(line, "source") {
		t.Errorf("the ingest banner line =\n  %q\nwhich never names a source. The operator "+
			"is at a terminal with no web UI open and no account yet; if this line does not "+
			"name the next step, nothing does.", strings.TrimSpace(line))
	}
}
