package ffmpeg

import (
	"crypto/md5"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// copySpec is a destination that forwards tracks rather than mixing them. The
// filter graph is filled in exactly as the engine fills it in -- routing.Compile
// runs for a copy destination too -- so the tests below also demonstrate that
// the graph is dropped rather than merely unused.
func copySpec(kind DestKind, target string, tracks []int) DestSpec {
	return DestSpec{
		Kind: kind, Target: target, RelayURL: "udp://127.0.0.1:20001",
		FilterComplex: "[0:a:0]pan=stereo|c0=1*c0|c1=1*c1[a_t0];" +
			"[a_t0]aresample=48000:async=1:first_pts=0[aout]",
		AudioOutLabel: "aout",
		AudioBitrate:  160, CopyVideo: true,
		CopyAudio: true, AudioTracks: tracks,
	}
}

// The graph MUST NOT survive onto a copy command line, and this is not a
// tidiness assertion. FFmpeg 8.1.2 refuses a filtergraph whose output is never
// mapped:
//
//	Filter 'anull:default' has output 0 (aout) unconnected
//	Error binding filtergraph inputs/outputs: Invalid argument
//
// exiting 234 having written nothing. Left in as a harmless leftover it would
// be a copy destination that never starts. TestCopyPreservesTheIngestBitsExactly
// below is what actually proves the command runs; this one names the trap.
func TestCopyDropsTheFilterGraphAndTheEncoderOptions(t *testing.T) {
	for _, k := range []struct {
		kind   DestKind
		target string
	}{
		{DestSRT, "srt://a.example:9000"},
		{DestFile, "/data/archive.mkv"},
	} {
		line := join(DestinationArgs(copySpec(k.kind, k.target, []int{0})))
		if strings.Contains(line, "-filter_complex") {
			t.Errorf("%s: the compiled graph is still on a copy command line, "+
				"which FFmpeg refuses outright: %s", k.target, line)
		}
		if !strings.Contains(line, "-c:a copy") {
			t.Errorf("%s: no -c:a copy: %s", k.target, line)
		}
		// -b:a, -ac and -ar are encoder options and there is no encoder. -ac in
		// particular reads as an instruction the output will not obey.
		for _, opt := range []string{"-b:a", "-ac ", "-ar "} {
			if strings.Contains(line, opt) {
				t.Errorf("%s: encoder option %q survived onto a copy command line: %s",
					k.target, strings.TrimSpace(opt), line)
			}
		}
		// Video is untouched, exactly as on every other destination.
		if !strings.Contains(line, "-map 0:v:0 -c:v copy") {
			t.Errorf("%s: video is no longer stream-copied: %s", k.target, line)
		}
	}
}

// Copy SELECTS. `-map 0 -c copy` would have been shorter and would have
// forwarded every track the ingest carries, destroying both the profile's track
// selection and ExcludeRoles -- the switch that keeps licensed music out of the
// archive. The maps come from the compiled result, one per surviving track.
func TestCopyMapsOnlyTheTracksTheCompilerKept(t *testing.T) {
	line := join(DestinationArgs(copySpec(DestSRT, "srt://a.example:9000", []int{0, 2})))
	for _, want := range []string{"-map 0:a:0", "-map 0:a:2"} {
		if !strings.Contains(line, want) {
			t.Errorf("selected track missing from %s: %s", want, line)
		}
	}
	if strings.Contains(line, "-map 0:a:1") {
		t.Errorf("a track the compiler dropped was mapped anyway: %s", line)
	}
	if strings.Contains(line, "-map 0 ") || strings.HasSuffix(line, "-map 0") {
		t.Errorf("a blanket -map 0 appeared, which forwards excluded roles too: %s", line)
	}
	// No '?' suffix. An optional map would turn "the track the operator chose
	// is not on this ingest" into silence, and Compile has already removed
	// every track that is not in the measured layout -- so a track named here
	// and missing at runtime means the layout changed under us.
	if strings.Contains(line, "0:a:0?") || strings.Contains(line, "0:a:2?") {
		t.Errorf("the maps are optional, so a vanished track would be silent: %s", line)
	}
}

// A destination that has not opted in must emit byte-for-byte the command it
// emitted before this feature existed. That is the promise every settings
// migration in this repo makes, and the one a new branch in the builder is most
// likely to break.
func TestNotOptingInLeavesTheCommandLineAlone(t *testing.T) {
	for _, kind := range []DestKind{DestRTMP, DestSRT, DestFile, DestAudio} {
		s := copySpec(kind, "/data/out.mkv", []int{0, 1})
		s.CopyAudio = false
		line := join(DestinationArgs(s))
		if !strings.Contains(line, "-filter_complex") {
			t.Errorf("%s: a destination that did not ask for copy lost its mix: %s", kind, line)
		}
		if strings.Contains(line, "-c:a copy") {
			t.Errorf("%s: copy appeared without being asked for: %s", kind, line)
		}
		// The encoder is still there. Not asserted as "-c:a aac": the
		// audio-only kind picks its codec from the target extension, so this
		// same spec legitimately renders libmp3lame there.
		if !strings.Contains(line, "-b:a 160k") {
			t.Errorf("%s: the encoder options went missing: %s", kind, line)
		}
	}
}

// An audio-only destination cannot copy: it has no video stream to hang a track
// beside, and its codec comes from the target container rather than the ingest.
// Refused at save time; the builder must not quietly produce a copy command for
// it either, or a row written by a newer build would start something nobody
// validated.
func TestAudioOnlyIgnoresCopy(t *testing.T) {
	line := join(DestinationArgs(copySpec(DestAudio,
		"icecast://source:pw@a.example:8000/live.mp3", []int{0})))
	if strings.Contains(line, "-c:a copy") {
		t.Errorf("an audio-only destination built a copy command: %s", line)
	}
	if !strings.Contains(line, "-filter_complex") {
		t.Errorf("an audio-only destination lost its graph, which is its whole output: %s", line)
	}
}

// THE LOAD-BEARING ONE. Everything above inspects a string, and a string cannot
// tell the difference between a command that copies and one that re-encodes to
// a codec with the same name -- which is exactly the failure this feature has to
// exclude, because a silent re-encode looks identical in every place an operator
// can see: same codec_name, same channel count, same duration, plausible audio.
//
// So this runs the exact argv DestinationArgs returns against a real FFmpeg and
// compares the AUDIO BITS. The comparison is made over each track re-extracted
// to raw ADTS rather than over the muxed packets, and that detail was measured
// rather than assumed: mpegts wraps AAC access units in ADTS headers while
// matroska stores them bare, so `-f md5` over the copied packets differs between
// the two containers FOR A CORRECT COPY. Normalising to ADTS on the way out
// makes the hash a property of the samples instead of the container, and it
// matched the source byte for byte out of mpegts, matroska, flv and mp4 in the
// probe that established this.
//
// Decoding to PCM and comparing that was the other candidate and it is wrong:
// measured against ffmpeg 8.1.2 a genuine copy through mpegts decoded to a
// DIFFERENT PCM hash than the source, because the encoder-delay priming is
// carried differently. It would have failed on correct code.
//
// Mutation: replace `"-c:a", "copy"` in copyAudioArgs with `"-c:a", "aac"`.
// Observed to fail on both containers with a hash mismatch, and NOT on the
// codec_name or track-count assertions -- which is the whole point of hashing.
func TestCopyPreservesTheIngestBitsExactly(t *testing.T) {
	bin, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not installed")
	}
	probe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("ffprobe is not installed")
	}

	dir := t.TempDir()
	src := filepath.Join(dir, "multitrack.mkv")
	buildMultitrackSource(t, bin, src, 3)

	for _, tc := range []struct {
		name   string
		ext    string
		tracks []int
	}{
		// mpegts is what an SRT destination muxes, and the container whose
		// framing differs from the source's.
		{"mpegts", ".ts", []int{0, 1, 2}},
		{"matroska", ".mkv", []int{0, 1, 2}},
		// The selection case: track 1 stands in for the one a role exclusion
		// removed. It must be absent from the output entirely, and the two that
		// survive must still be bit-exact and in order.
		{"role_excluded", ".mkv", []int{0, 2}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := filepath.Join(dir, tc.name+tc.ext)
			args := DestinationArgs(copySpec(DestFile, out, tc.tracks))

			// The builder points at the relay with UDP sizing appended. Swap in
			// the file; every argument under test comes after the input.
			args = replaceRelayInput(t, args, src)
			args = append([]string{"-y"}, args...)

			if o, err := exec.Command(bin, args...).CombinedOutput(); err != nil {
				t.Fatalf("FFmpeg rejected the copy command: %v\n%s\nargv: %v",
					err, lastLines(string(o), 6), args)
			}

			// One video, and exactly the tracks that were mapped -- not the
			// three the source carries.
			gotV := streamField(t, probe, out, "v", "codec_type")
			if len(gotV) != 1 {
				t.Errorf("output has %d video streams, want 1", len(gotV))
			}
			gotA := streamField(t, probe, out, "a", "codec_name")
			if len(gotA) != len(tc.tracks) {
				t.Fatalf("output has %d audio tracks, want %d: an excluded track "+
					"reaching the output is a compliance failure, and a missing one "+
					"is a silent downgrade", len(gotA), len(tc.tracks))
			}
			for i, c := range gotA {
				if c != "aac" {
					t.Errorf("output track %d is %q, want the ingest's own aac", i, c)
				}
			}

			// And the bits. This is the assertion that separates a copy from a
			// re-encode; everything above passes for both.
			for i, srcTrack := range tc.tracks {
				want := adtsHash(t, bin, src, srcTrack)
				got := adtsHash(t, bin, out, i)
				if got != want {
					t.Errorf("output track %d does not carry the bits of source "+
						"track %d (%s vs %s): the audio was re-encoded, which is "+
						"the one thing this destination promises not to do",
						i, srcTrack, got, want)
				}
			}
		})
	}
}

// buildMultitrackSource writes a real n-track file, each track a distinct tone
// so a mis-ordered map is a hash mismatch rather than a coincidence.
func buildMultitrackSource(t *testing.T, bin, path string, n int) {
	t.Helper()
	args := []string{"-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=size=160x120:rate=10:duration=2"}
	maps := []string{"-map", "0:v"}
	for i := range n {
		args = append(args, "-f", "lavfi", "-i",
			fmt.Sprintf("sine=frequency=%d:duration=2:sample_rate=48000", 440+i*220))
		maps = append(maps, "-map", fmt.Sprintf("%d:a", i+1))
	}
	args = append(args, maps...)
	args = append(args, "-c:v", "libx264", "-preset", "ultrafast", "-c:a", "aac",
		"-shortest", path)
	if o, err := exec.Command(bin, args...).CombinedOutput(); err != nil {
		t.Skipf("could not build a %d-track source: %s", n, o)
	}
}

// adtsHash is one audio track's access units, re-framed as ADTS so the hash is
// a property of the samples rather than of the container they arrived in. See
// the long note on TestCopyPreservesTheIngestBitsExactly for why the obvious
// alternatives do not work.
// The hash is taken in Go rather than with FFmpeg's own `-f md5` muxer,
// because the two would be a pair of -f options on one output and the LAST one
// wins: `-f adts -f md5` silently drops the re-framing and hashes the container's
// own packets again. That mistake passed on matroska and failed on mpegts, which
// is precisely the false negative the re-framing exists to remove.
func adtsHash(t *testing.T, bin, path string, track int) string {
	t.Helper()
	raw, err := exec.Command(bin, "-v", "error", "-i", path,
		"-map", fmt.Sprintf("0:a:%d", track), "-c", "copy", "-f", "adts", "-").Output()
	if err != nil {
		t.Fatalf("extracting track %d of %s: %v", track, path, err)
	}
	if len(raw) == 0 {
		t.Fatalf("track %d of %s extracted to nothing; an empty hash would "+
			"match an empty hash and this test would prove nothing", track, path)
	}
	return fmt.Sprintf("%x", md5.Sum(raw))
}

// streamField reads one ffprobe field for every stream of a type, in stream
// order.
//
// The index is requested alongside the field and used to DEDUPE, which is not
// defensive tidiness: ffprobe lists an MPEG-TS stream twice, once inside the
// program it belongs to and once at the top level, so the naive form reports
// six audio tracks for a file that carries three. Counting those would have
// failed this test on correct code -- and, worse, could have passed it on a
// command that mapped every track by reporting a plausible-looking multiple.
func streamField(t *testing.T, probe, path, kind, field string) []string {
	t.Helper()
	o, err := exec.Command(probe, "-v", "error", "-select_streams", kind,
		"-show_entries", "stream=index,"+field, "-of", "csv=p=0", path).CombinedOutput()
	if err != nil {
		t.Fatalf("ffprobe %s: %v\n%s", path, err, o)
	}
	seen := map[string]bool{}
	var out []string
	for _, l := range strings.Split(strings.TrimSpace(string(o)), "\n") {
		idx, val, ok := strings.Cut(strings.TrimSpace(l), ",")
		if !ok || seen[idx] {
			continue
		}
		seen[idx] = true
		out = append(out, strings.TrimSpace(val))
	}
	return out
}

// replaceRelayInput points the built command at a file instead of the relay,
// leaving every argument under test exactly where the builder put it.
func replaceRelayInput(t *testing.T, args []string, src string) []string {
	t.Helper()
	for i, a := range args {
		if a == "-i" && i+1 < len(args) {
			out := append([]string{}, args...)
			out[i+1] = src
			return out
		}
	}
	t.Fatalf("no -i in the built command: %v", args)
	return nil
}
