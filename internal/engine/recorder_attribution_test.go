package engine

import (
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/recording"
)

// The recorder this engine starts must name its segments after THIS engine's
// programme.
//
// recording.Scan attributes a segment by the name alone (SourceFromName), and
// the recording package's own tests prove that half for a name that is
// already tagged. This is the other half: that the name the engine hands
// FFmpeg is tagged at all. Revert reconcileRecorder to the untagged
// rec-%Y%m%d-%H%M%S.mkv and every recording package test stays green while
// every new segment is indexed with no programme -- which is how the clip
// editor came to label every clip with the default one.
//
// The engine's sourceID is set to something other than the default source on
// purpose: a pattern that hard-coded the default id would pass against it.
//
// Mutation: in reconcileRecorder, replace recording.SegmentPattern(...) with
// filepath.Join(e.cfg.RecordingsDir(), "rec-%Y%m%d-%H%M%S.mkv"). Observed to
// fail with "carries no programme".
func TestTheRecorderNamesItsSegmentsAfterItsOwnProgramme(t *testing.T) {
	e, _ := storeEngine(t)
	const programme int64 = 7
	e.sourceID = programme

	s := db.DefaultSettings()
	s.Recording.Enabled = true
	e.reconcileRecorder(s)

	e.mu.RLock()
	proc := e.recorder
	e.mu.RUnlock()
	if proc == nil {
		t.Fatal("reconcileRecorder started no recorder with recording enabled; " +
			"every assertion below would be about nothing")
	}

	// The master's output is the one argument carrying the strftime stamp.
	// Stems are off in the default settings, so there is exactly one.
	var pattern string
	for _, a := range proc.Args() {
		if strings.Contains(a, "%Y%m%d-%H%M%S") {
			if pattern != "" {
				t.Fatalf("two strftime outputs on the recorder's argv (%q, %q) with "+
					"stems off", pattern, a)
			}
			pattern = a
		}
	}
	if pattern == "" {
		t.Fatalf("no segment pattern on the recorder's argv: %q", proc.Args())
	}

	// Render it the way FFmpeg's segment muxer would, then ask the scanner.
	name := strings.Replace(pattern, "%Y%m%d-%H%M%S", "20260922-120000", 1)
	got, ok := recording.SourceFromName(name)
	if !ok {
		t.Fatalf("the recorder writes %q, which carries no programme: Scan indexes "+
			"every such segment with source_id NULL", name)
	}
	if got != programme {
		t.Fatalf("the recorder of programme %d writes %q, which Scan attributes to "+
			"programme %d", programme, name, got)
	}
}
