package playout

import (
	"testing"

	"github.com/rainmanjam/polyemesis/internal/ffmpeg"
)

// A rung restarting with a declared track that never carries data writes no
// segment for as long as a relay reader's probe can run; the live grace must
// outlast that or the restart shows a watching viewer "ended".
func TestTheLiveGraceOutlastsTheReadersProbeCeiling(t *testing.T) {
	if minLiveGrace <= ffmpeg.RelayProbeWindow {
		t.Errorf("minLiveGrace %v does not exceed the relay probe window %v", minLiveGrace, ffmpeg.RelayProbeWindow)
	}
}
