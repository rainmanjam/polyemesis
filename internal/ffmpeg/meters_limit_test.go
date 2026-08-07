package ffmpeg

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/routing"
)

/* The meters process merges every channel of every track into one amerge, and
 * amerge refuses beyond 64 channels. Before MaxTracks was raised from 6 that
 * was unreachable -- 6 x 8 = 48. At 32 it is reachable, so it is now a case
 * that has to be handled rather than argued away.
 *
 * What must hold: a too-wide ingest still meters what fits, still produces a
 * command FFmpeg accepts, and says how many tracks it left out. Silently
 * metering a prefix is the failure to avoid: an operator would read an
 * unmetered track as a silent one. */

func TestMeterChannelLimitMatchesRouting(t *testing.T) {
	// MeterChannelLimit is duplicated rather than imported, because this
	// package builds command lines and does not depend on routing. That is
	// only safe while the two agree.
	if MeterChannelLimit != routing.MaxMeterChannels {
		t.Fatalf("MeterChannelLimit = %d, routing.MaxMeterChannels = %d: the duplication has drifted",
			MeterChannelLimit, routing.MaxMeterChannels)
	}
}

func TestMaxTracksFitsTheMeters(t *testing.T) {
	// The point of choosing 32: a full-width stereo ingest must meter whole.
	stereo := make([]int, routing.MaxTracks)
	for i := range stereo {
		stereo[i] = 2
	}
	if got := MetersDropped(stereo); got != 0 {
		t.Errorf("%d stereo tracks dropped %d; MaxTracks and MeterChannelLimit disagree",
			routing.MaxTracks, got)
	}
}

func TestMetersArgsCap(t *testing.T) {
	tests := []struct {
		name        string
		chans       []int
		wantMetered int
	}{
		{"six stereo, the ordinary case", rep(6, 2), 6},
		{"exactly at the limit", rep(32, 2), 32},
		{"one track past the limit", rep(33, 2), 32},
		{"eight-channel tracks reach it sooner", rep(8, 8), 8},
		{"nine eight-channel tracks do not fit", rep(9, 8), 8},
		{"mixed widths stop on the track that overflows", []int{8, 8, 8, 8, 8, 8, 8, 6, 8}, 8},
		{"a single track is never merged, so the limit does not apply", []int{200}, 1},
		// Only the FIRST track is exempt. Without this case, widening the
		// exemption to the first two passes every other assertion here while
		// compiling an amerge FFmpeg rejects.
		{"the exemption does not extend to the second track", []int{200, 200}, 1},
		{"unprobed widths still occupy a leg", rep(70, 0), 64},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			args := MetersArgs(MetersSpec{TrackChannels: tc.chans, RelayURL: "udp://127.0.0.1:5000"})
			fc := filterComplexOf(t, args)

			// Count the input legs actually compiled.
			legs := strings.Count(fc, "aresample=")
			if legs != tc.wantMetered {
				t.Errorf("compiled %d legs, want %d\n%s", legs, tc.wantMetered, fc)
			}

			if want := len(tc.chans) - tc.wantMetered; MetersDropped(tc.chans) != want {
				t.Errorf("MetersDropped = %d, want %d", MetersDropped(tc.chans), want)
			}

			// Whatever it emitted must be within amerge's range, or FFmpeg
			// rejects the command and the meters crash-loop.
			if tc.wantMetered > 1 {
				if !strings.Contains(fc, fmt.Sprintf("amerge=inputs=%d", tc.wantMetered)) {
					t.Errorf("amerge input count does not match the legs compiled\n%s", fc)
				}
				total := 0
				for _, c := range tc.chans[:tc.wantMetered] {
					total += max(c, 1)
				}
				if total > MeterChannelLimit {
					t.Errorf("compiled %d channels into amerge, over the %d limit", total, MeterChannelLimit)
				}
			}

			// It must never reference a track it decided to skip.
			if d := len(tc.chans) - tc.wantMetered; d > 0 {
				if strings.Contains(fc, fmt.Sprintf("[0:a:%d]", len(tc.chans)-1)) {
					t.Errorf("references dropped track %d\n%s", len(tc.chans)-1, fc)
				}
			}
		})
	}
}

func rep(n, v int) []int {
	out := make([]int, n)
	for i := range out {
		out[i] = v
	}
	return out
}

func filterComplexOf(t *testing.T, args []string) string {
	t.Helper()
	for i, a := range args {
		if a == "-filter_complex" && i+1 < len(args) {
			return args[i+1]
		}
	}
	t.Fatalf("no -filter_complex in %v", args)
	return ""
}

// TestMetersArgsCompileInFFmpeg is the one that matters: the assertions above
// check what MetersArgs emits, and only FFmpeg can say whether it accepts it.
//
// The 33-track case is the point. Uncapped it compiles a 66-channel amerge,
// which FFmpeg rejects with "Too many channels (max 64)" -- and because the
// meters are supervised and respawned, a rejected command does not fail once,
// it crash-loops.
func TestMetersArgsCompileInFFmpeg(t *testing.T) {
	bin, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not installed")
	}

	for _, n := range []int{6, 32, 33, 40} {
		t.Run(fmt.Sprintf("%d_tracks", n), func(t *testing.T) {
			dir := t.TempDir()
			src := filepath.Join(dir, "wide.ts")

			// A real n-track MPEG-TS, the shape the relay carries.
			gen := []string{"-hide_banner", "-loglevel", "error", "-y",
				"-f", "lavfi", "-i", "testsrc2=size=160x120:rate=10:duration=1"}
			maps := []string{"-map", "0:v"}
			for i := 0; i < n; i++ {
				gen = append(gen, "-f", "lavfi", "-i",
					fmt.Sprintf("sine=frequency=%d:duration=1:sample_rate=48000", 200+i*7))
				maps = append(maps, "-map", fmt.Sprintf("%d:a", i+1))
			}
			gen = append(gen, maps...)
			gen = append(gen, "-c:v", "libx264", "-preset", "ultrafast", "-c:a", "aac",
				"-f", "mpegts", src)
			if out, err := exec.Command(bin, gen...).CombinedOutput(); err != nil {
				t.Skipf("could not build a %d-track source: %s", n, out)
			}

			chans := rep(n, 2)
			args := MetersArgs(MetersSpec{TrackChannels: chans, RelayURL: src})

			// MetersArgs targets the relay via RelayInputURL, which appends UDP
			// sizing. Point it at the file instead; the filtergraph -- the part
			// under test -- is untouched.
			for i, a := range args {
				if strings.HasPrefix(a, src) {
					args[i] = src
				}
			}
			args = append([]string{"-t", "0.5"}, args...)

			out, err := exec.Command(bin, args...).CombinedOutput()
			if err != nil {
				t.Fatalf("FFmpeg rejected the meters command for %d tracks (dropped %d): %v\n%s",
					n, MetersDropped(chans), err, lastLines(string(out), 4))
			}
		})
	}
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
