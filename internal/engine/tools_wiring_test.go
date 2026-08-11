package engine

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/config"
	"github.com/rainmanjam/polyemesis/internal/db/dbtest"
	"github.com/rainmanjam/polyemesis/internal/events"
	"github.com/rainmanjam/polyemesis/internal/ffmpeg"
)

// Issue #183: nothing covered the production path that supplies ffprobe to the
// upload gate.
//
// internal/api/media.go probeUpload does, in order: s.mgr == nil, s.eng() ==
// nil, eng.Tools() == nil, tools.FFprobe == "". Any one of them accepts the
// upload UNCHECKED -- a survivable outcome by design, and precisely why a
// wiring bug here is silent. Every api test in the package leaves s.mgr nil and
// so exercises the first arm and never the rest.
//
// This is the engine half: that an engine built the way the manager builds one
// hands back the tools the manager was constructed with, ffprobe included. It
// does not assert what the API does with them; it asserts that what the API
// reaches for is there.
func TestTheEngineHandsBackTheFFprobeTheManagerWasBuiltWith(t *testing.T) {
	dir := t.TempDir()
	store := dbtest.OpenAt(t, filepath.Join(dir, "polyemesis.db"))
	cfg := config.Config{DataDir: dir}
	if err := cfg.EnsureDirs(); err != nil {
		t.Fatalf("EnsureDirs: %v", err)
	}

	// Distinct paths, so an assertion cannot be satisfied by ffmpeg's.
	tools := &ffmpeg.Tools{
		FFmpeg:  filepath.Join(dir, "ffmpeg-under-test"),
		FFprobe: filepath.Join(dir, "ffprobe-under-test"),
	}
	m := NewManager(slog.New(slog.NewTextHandler(io.Discard, nil)), cfg, store, tools, events.NewBroker())
	t.Cleanup(m.Stop)

	// A second source, because the upload gate reaches the DEFAULT engine and a
	// wiring bug that only fed the first one would otherwise be invisible.
	addSource(t, store, "Vertical")
	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	eng := m.Default()
	if eng == nil {
		t.Fatal("the manager has no default engine, so probeUpload takes the unchecked " +
			"path and every upload is stored without being probed (#183)")
	}

	got := eng.Tools()
	if got == nil {
		t.Fatal("Engine.Tools() is nil on an engine the manager built. probeUpload reads " +
			"this to find ffprobe; nil means every upload is accepted unchecked, and the " +
			"only sign is a WARN line (#183)")
	}
	if got.FFprobe != tools.FFprobe {
		t.Fatalf("Engine.Tools().FFprobe = %q, want %q. An empty or wrong path is the "+
			"silently-open upload gate #183 is about", got.FFprobe, tools.FFprobe)
	}
	if got.FFmpeg != tools.FFmpeg {
		t.Errorf("Engine.Tools().FFmpeg = %q, want %q", got.FFmpeg, tools.FFmpeg)
	}

	// Every engine, not only the default: /api/v1/media on a scoped source
	// reaches its own.
	engines := m.Engines()
	if len(engines) < 2 {
		t.Fatalf("running engines = %d, want the default plus the second source", len(engines))
	}
	for _, e := range engines {
		tl := e.Tools()
		if tl == nil || tl.FFprobe != tools.FFprobe {
			t.Errorf("engine for source %d reports ffprobe %v, want %q",
				e.SourceID(), tl, tools.FFprobe)
		}
	}
}
