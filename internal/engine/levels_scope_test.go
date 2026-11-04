package engine

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/events"
	"github.com/rainmanjam/polyemesis/internal/relay"
	"github.com/rainmanjam/polyemesis/internal/routing"
)

// THE LEVELS FRAME NAMES ITS PROGRAMME.
//
// Every engine publishes onto the one bus, and /ws relays the whole bus to every
// console. Status frames say which programme they describe (status.source.id),
// so the console can keep the selected programme's; levels frames said nothing,
// and on a multi-programme install the console drew whichever programme metered
// last. On the three-programme demo, tracks 3 and 4 read "not measured" in about
// 40% of samples: a two-track programme's frame, drawn over a four-track one.
//
// The frame must carry source.id and source.name, the same accessor status uses,
// and keep peak and rms at the top level so a client that ignores the tag reads
// what it always read.
func TestLevelsFramesNameTheirProgramme(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatalf("locate the test binary to re-execute as a stand-in ffmpeg: %v", err)
	}
	t.Setenv(engineFakeChildAstats, "1")

	e, _ := storeEngine(t)
	e.alloc = relay.NewPortAllocator(freeUDPPort(t), 4)
	e.tools.FFmpeg = self
	e.mu.Lock()
	e.sourceName = "Studio B"
	e.source = routing.Source{Tracks: []routing.Track{{Index: 0, Channels: 2, Codec: "aac", Layout: "stereo"}}}
	e.measured = true
	e.measuredMode = db.IngestRTMP
	e.probed = true
	want := e.sourceID
	e.mu.Unlock()

	sub := e.bus.Subscribe(events.TypeLevels)
	defer sub.Close()

	s := db.DefaultSettings()
	s.Meters.Enabled = true
	e.reconcileMeters(s)

	var ev events.Event
	select {
	case ev = <-sub.C:
	case <-time.After(10 * time.Second):
		t.Fatal("no levels frame was published: the stand-in meters never fed the handler, so this test proves nothing")
	}

	raw, err := json.Marshal(ev.Data)
	if err != nil {
		t.Fatalf("marshal levels frame: %v", err)
	}
	var frame struct {
		Peak   [][]float64 `json:"peak"`
		RMS    [][]float64 `json:"rms"`
		Source *struct {
			ID   int64  `json:"id"`
			Name string `json:"name"`
		} `json:"source"`
	}
	if err := json.Unmarshal(raw, &frame); err != nil {
		t.Fatalf("unmarshal levels frame %s: %v", raw, err)
	}
	if frame.Source == nil {
		t.Fatalf("levels frame %s names no programme: on a multi-programme install the console "+
			"cannot tell this engine's meters from another's", raw)
	}
	if frame.Source.ID != want || frame.Source.Name != "Studio B" {
		t.Errorf("levels frame names programme %d %q, want %d %q", frame.Source.ID, frame.Source.Name, want, "Studio B")
	}
	if len(frame.Peak) != 1 || len(frame.RMS) != 1 {
		t.Errorf("peak/rms are not at the top level of the frame, where existing clients read them: %s", raw)
	}
}
