package engine

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/hooks"
	"github.com/rainmanjam/polyemesis/internal/recording"
)

// rolloverEngine is an engine with just enough to answer noteRollover: a
// recordings directory and somewhere to record what it found.
func rolloverEngine(t *testing.T) (*Engine, string) {
	t.Helper()
	dir := t.TempDir()
	return &Engine{
		log:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		recman:     recording.New(slog.New(slog.NewTextHandler(io.Discard, nil)), nil, dir, nil),
		rolledOver: map[int64]string{},
	}, dir
}

// TestARecordingThatContinuesElsewhereIsSaidOutLoud is the observability half of
// #398.
//
// A file destination never overwrites footage: a respawn whose configured name
// already holds bytes is given a timestamped sibling. That is correct. What was
// missing is that nobody was told -- the child can exit CLEANLY (FFmpeg 8.1 exits
// 0 on a demuxer-side failure), a clean exit is logged at Info with nothing in
// the process log ring, and LastError stays empty. So the operator's configured
// filename held a Matroska header and no video, the footage was in a file nobody
// had mentioned, and the only trace was a restart counter moving.
func TestARecordingThatContinuesElsewhereIsSaidOutLoud(t *testing.T) {
	e, dir := rolloverEngine(t)
	row := &db.Destination{ID: 7, Name: "archive", Kind: db.DestFile, URL: "show.mkv"}

	// The ordinary case first: the name is free, so the respawn gets exactly
	// what the operator asked for and there is nothing to report.
	want, err := e.recman.Resolve(row.URL)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	e.noteRollover(row, want)
	if got, rolled := e.RolledOver(7); rolled {
		t.Fatalf("a destination writing to its configured name reported a rollover to %q", got)
	}

	// Now the name holds real footage, which is the state a restart finds.
	if err := os.WriteFile(want, []byte("not empty"), 0o600); err != nil {
		t.Fatalf("seed footage: %v", err)
	}
	actual, err := e.recman.ResolveForWrite(row.URL)
	if err != nil {
		t.Fatalf("ResolveForWrite: %v", err)
	}
	if actual == want {
		t.Fatalf("fixture: ResolveForWrite reused %q over existing footage, so this "+
			"test cannot prove anything", want)
	}
	e.noteRollover(row, actual)

	got, rolled := e.RolledOver(7)
	if !rolled {
		t.Fatal("the recording continued in a different file and nothing recorded it; " +
			"the operator's configured filename now holds a header and no video, and " +
			"the only remaining trace is a restart counter")
	}
	if got != actual {
		t.Errorf("RolledOver = %q, want the path actually written, %q", got, actual)
	}
	if filepath.Dir(got) != dir {
		t.Errorf("the reported path %q is not in the recordings directory %q", got, dir)
	}
}

// A rollover must not be reported for a destination that does not write a file
// at all -- an RTMP target's URL is not a path, and Resolve refusing it must
// read as "nothing to say" rather than as a rollover.
func TestANonFileDestinationNeverReportsARollover(t *testing.T) {
	e, _ := rolloverEngine(t)
	row := &db.Destination{ID: 9, Name: "twitch", Kind: db.DestRTMP,
		URL: "rtmp://live.twitch.tv/app"}

	e.noteRollover(row, "/somewhere/else.mkv")

	if got, rolled := e.RolledOver(9); rolled {
		t.Errorf("an RTMP destination reported a rollover to %q; only a file "+
			"destination has an output path that can move", got)
	}
}

// The hook is published on the same path that records the status, so a
// dispatcher that is present must not change what is recorded — and a
// dispatcher with no hooks configured must not be a special case.
//
// PUBLISHING IS ASSERTED THROUGH ITS EFFECT ON THE CALLER, not by intercepting
// the event. Dispatcher.Publish fans out to configured endpoints over HTTP; a
// test that stood one up would be testing internal/hooks, which has its own
// tests for exactly that. What is this package's business is that wiring a
// dispatcher in cannot break the rollover record, and that the nil-hooks case
// and the no-hooks-configured case behave the same.
func TestARolloverIsRecordedWhetherOrNotHooksAreWired(t *testing.T) {
	for _, tc := range []struct {
		name  string
		hooks func() *hooks.Dispatcher
	}{
		{"no dispatcher at all", func() *hooks.Dispatcher { return nil }},
		{"a dispatcher with no hooks configured", func() *hooks.Dispatcher {
			return hooks.NewDispatcher(slog.New(slog.NewTextHandler(io.Discard, nil)),
				hooks.SourceFunc(func() ([]hooks.Hook, error) { return nil, nil }))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, _ := rolloverEngine(t)
			e.hooks = tc.hooks()
			row := &db.Destination{ID: 3, Name: "archive", Kind: db.DestFile, URL: "show.mkv"}

			want, err := e.recman.Resolve(row.URL)
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if err := os.WriteFile(want, []byte("existing footage"), 0o600); err != nil {
				t.Fatalf("seed footage: %v", err)
			}
			actual, err := e.recman.ResolveForWrite(row.URL)
			if err != nil {
				t.Fatalf("ResolveForWrite: %v", err)
			}

			e.noteRollover(row, actual)

			got, rolled := e.RolledOver(3)
			if !rolled || got != actual {
				t.Errorf("RolledOver = (%q, %v), want (%q, true) — whether a hook "+
					"endpoint exists is not this destination's business, and the "+
					"operator-facing record must not depend on it", got, rolled, actual)
			}
		})
	}
}
