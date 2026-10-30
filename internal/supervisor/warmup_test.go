package supervisor

import (
	"testing"
	"time"
)

// The shapes a video decoder's join-time burst takes. warmupSliceError is the
// line that made "189 ERRORS": on the seeded demo install every process that
// decodes the relay (preview, renditions, destinations, loudness probes, the
// meters) logged about 190 of it, alternating with "non-existing PPS 0
// referenced", all between 0.5 s and 2 s after its spawn. The others are the
// same burst as other decoders and FFmpeg versions print it.
const (
	warmupSliceError = "[h264 @ 0x7f3a2c004b80] decode_slice_header error"
	warmupMMCO       = "[h264 @ 0x7f3a2c004b80] mmco: unref short failure"
	warmupDecoding   = "[vist#0:0/h264 @ 0x7f3a2c0012c0] [dec:h264 @ 0x7f3a2c003a40] Decoding error: Invalid data found when processing input"
	warmupHEVC       = "[hevc @ 0x55d0c8a1e2c0] Error constructing the frame RPS."
)

// The join-time decoder burst is warm-up, not a fault. Every child that decodes
// video from the relay joins it wherever the relay happens to be, which is
// almost never a keyframe, and until the next one arrives the decoder complains
// once a frame. Counted as errors, a healthy install showed "189 ERRORS" on the
// process that started last.
//
// Through the real scan loop: the line a just-spawned child prints must reach
// the log ring as info.
func TestDecoderBurstAtJoinIsNotAnError(t *testing.T) {
	p := testProcess(t, fakeExitSaying(0, warmupSliceError), Spec{})
	p.Start()
	waitFor(t, "the decoder line to reach the log", func() bool { return len(p.Logs()) > 0 })

	got := p.Logs()[0]
	if got.Text != warmupSliceError {
		t.Fatalf("logged %q, want the decoder line", got.Text)
	}
	if got.Level != "info" {
		t.Fatalf("a decoder line printed at join was logged at %q, want info: the "+
			"mid-GOP warm-up burst is counted as errors on a healthy stream", got.Level)
	}
}

// The other half: the window closes. The same line from a child past warm-up is
// a real decode error -- corrupt input, a publisher fault -- and must be counted.
func TestDecoderErrorAfterWarmupStillCounts(t *testing.T) {
	p := testProcess(t, fakeExitSaying(0, warmupSliceError), Spec{})
	p.warmup = 0 // every line is past the window
	p.Start()
	waitFor(t, "the decoder line to reach the log", func() bool { return len(p.Logs()) > 0 })

	if got := p.Logs()[0].Level; got != "error" {
		t.Fatalf("a decoder error after warm-up was logged at %q, want error", got)
	}
}

func TestDecoderWarmupWindow(t *testing.T) {
	const notDecoder = "rtmp://a.rtmp.example/live: Connection refused"
	tests := []struct {
		name   string
		line   string
		since  time.Duration // since the spawn
		frames int64         // output frames reported so far
		want   bool
	}{
		{"a slice error right after the spawn", warmupSliceError, 200 * time.Millisecond, 0, true},
		{"an mmco failure right after the spawn", warmupMMCO, time.Second, 0, true},
		{"FFmpeg 7+'s decoder-wrapped error", warmupDecoding, time.Second, 0, true},
		{"hevc joins mid-GOP the same way", warmupHEVC, time.Second, 0, true},
		{"still waiting for a keyframe late in the window", warmupSliceError, decoderWarmup - time.Millisecond, 0, true},

		{"the first output frame ends it: a keyframe has been decoded", warmupSliceError, time.Second, 1, false},
		{"the time cap ends it when no frame count is reported", warmupSliceError, decoderWarmup, 0, false},
		{"long after the spawn", warmupSliceError, time.Minute, 0, false},
		{"a non-decoder error is never warm-up", notDecoder, 100 * time.Millisecond, 0, false},
		{"an output muxer's error is not a decoder's", "[flv @ 0x55d0c8a1e2c0] Failed to update header", 100 * time.Millisecond, 0, false},
		{"an audio decoder is not joining a GOP", "[aac @ 0x55d0c8a1e2c0] Error decoding AAC frame", 100 * time.Millisecond, 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := inDecoderWarmup(tt.line, tt.since, tt.frames, decoderWarmup); got != tt.want {
				t.Errorf("inDecoderWarmup(%q, %v, %d) = %v, want %v", tt.line, tt.since, tt.frames, got, tt.want)
			}
		})
	}
}
