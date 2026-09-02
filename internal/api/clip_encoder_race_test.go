package api

import (
	"context"
	"sync"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/clipper"
	"github.com/rainmanjam/polyemesis/internal/db"
)

// THE RACE IS REAL AND THE DETECTOR IS THE ONLY WITNESS.
//
// clipRequest used to name the head encoder with
// `clipper.HeadEncoder(tools, tools.HWEncoders)` -- a bare read of a field that
// ffmpeg.Tools.RefreshEncoderCapabilities sets to nil and then appends to under
// Tools' own write lock. POST /api/v1/encoders?redetect=1 calls that refresh on
// a live server, so a clip export planned at the same moment reads a slice
// header while another goroutine is rewriting it.
//
// Nothing about the OUTCOME is asserted, on purpose: both interleavings produce
// a perfectly reasonable encoder name, which is precisely why this survived
// review. Under -race the unsynchronised access itself is the failure, and
// under a plain `go test` this is a cheap concurrency smoke test that passes
// either way. Run it with -race or it proves nothing.
//
// The refresh does real work here even though the fixture's ffmpeg path does
// not exist: ProbeEncoders spawns a process per candidate, every spawn fails
// with an *exec.Error, and a full set of verdicts is still written back under
// the lock. The write is what matters, not what it says.
func TestPlanningAClipDoesNotRaceAnEncoderRedetect(t *testing.T) {
	tools := probedTools(
		[]string{string(db.EncoderX264), string(db.EncoderNVENCH264)},
		worked(string(db.EncoderX264), 40),
		worked(string(db.EncoderNVENCH264), 60),
	)
	srv, _, _, _ := engineServer(t, tools, Options{})

	// The reader runs FOR AS LONG AS THE WRITER DOES rather than for a fixed
	// count. A refresh spawns a process per candidate and takes tens of
	// milliseconds; a clipRequest takes microseconds. A reader with its own
	// count finishes before the first write lands, and the two never overlap --
	// which is how a version of this test can pass against the racing code and
	// prove nothing at all.
	const rounds = 20
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		defer close(done)
		for i := 0; i < rounds; i++ {
			tools.RefreshEncoderCapabilities(context.Background())
		}
	}()
	go func() {
		defer wg.Done()
		for {
			select {
			case <-done:
				return
			default:
			}
			// Precise mode is the branch that names an encoder at all; fast
			// mode copies the bitstream and never asks.
			if _, err := srv.clipRequest(clipTimeline{}, clipRequestBody{
				Mode: string(clipper.ModePrecise), OutMS: 1000,
			}); err != nil {
				t.Errorf("clipRequest: %v", err)
				return
			}
		}
	}()
	wg.Wait()
}
