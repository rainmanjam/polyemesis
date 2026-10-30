package engine

import (
	"os"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/relay"
	"github.com/rainmanjam/polyemesis/internal/routing"
)

// The meters process showed uptime "--:--:--" on the monitoring page for as long
// as it ran. Uptime runs from the first media, and the supervisor learns of
// media from FFmpeg's -progress block -- but the meters' stdout is level data,
// read by their own handler, so the supervisor never heard of any. Levels
// arriving IS media flowing, and the handler has to say so.
func TestMetersReportUptimeOnceLevelsArrive(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatalf("locate the test binary to re-execute as a stand-in ffmpeg: %v", err)
	}
	t.Setenv(engineFakeChildAstats, "1")

	e, _ := storeEngine(t)
	e.alloc = relay.NewPortAllocator(freeUDPPort(t), 4)
	e.tools.FFmpeg = self
	e.mu.Lock()
	e.source = routing.Source{Tracks: []routing.Track{{Index: 0, Channels: 2, Codec: "aac", Layout: "stereo"}}}
	e.measured = true
	e.measuredMode = db.IngestRTMP
	e.probed = true
	e.mu.Unlock()

	s := db.DefaultSettings()
	s.Meters.Enabled = true
	e.reconcileMeters(s)

	e.mu.RLock()
	proc := e.meters
	e.mu.RUnlock()
	if proc == nil {
		t.Fatal("reconcileMeters started no meters process")
	}

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		e.mu.RLock()
		got := !e.levelsAt.IsZero()
		e.mu.RUnlock()
		if got {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	e.mu.RLock()
	gotLevels := !e.levelsAt.IsZero()
	e.mu.RUnlock()
	if !gotLevels {
		t.Fatal("no levels reached the engine: the stand-in meters never fed the handler, so this test proves nothing")
	}
	time.Sleep(50 * time.Millisecond)

	if st := proc.Status(); st.UptimeSec <= 0 {
		t.Fatalf("meters are delivering levels but report UptimeSec %v: the monitoring page shows "+
			"--:--:-- for a process that is plainly working", st.UptimeSec)
	}
}
