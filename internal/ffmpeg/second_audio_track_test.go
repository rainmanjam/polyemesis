package ffmpeg

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"math"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/rtmpserver"
	"github.com/rainmanjam/polyemesis/internal/testenv"
)

// TONE_A and TONE_B, spelled the way scripts/acceptance-multistream.sh spells
// them and chosen for the same reason: 300 Hz and 5000 Hz are far enough apart
// that a bandpass separates them cleanly after an AAC round trip, where
// neighbouring tones would measure the filter's skirt instead of the routing.
const (
	toneLive = 300  // the mix that goes out live
	bandLive = 100  // bandpass width at toneLive
	toneVOD  = 5000 // the second mix, standing in for a VOD-only fold
	bandVOD  = 400  // bandpass width at toneVOD
)

// toneFloor is "this band is really present", in absolute dBFS, and toneMargin
// is how far the other band must sit below it for "this track did not carry the
// other mix" to be a statement rather than a hope. Both are the multistream
// suite's figures. Measured through this path a present band reads about -24 dB
// and an absent one between -58 and -71, so neither number is tuned to one
// machine's build.
const (
	toneFloor  = -45.0
	toneMargin = 20.0
	// mixSpread is how far apart the two tracks' band BALANCES must sit for the
	// two tracks to be different audio rather than the same audio twice. Two
	// identical mixes score 0; opposite tones score the sum of both margins.
	mixSpread = 30.0
)

// twoMixGraph is a filter graph carrying TWO finished mixes, shaped like the one
// routing.Compile emits per mix -- pan to stereo, then the resample that ends
// every compiled graph -- so what is under test is the builder's handling of a
// second output rather than a filter expression invented here.
//
// liveSrc and vodSrc name the ingest tracks each mix is fed from, as FFmpeg
// stream specifiers, which is what lets the same graph express "two different
// mixes" and "the same mix twice".
func twoMixGraph(liveSrc, vodSrc string) string {
	return fmt.Sprintf(
		"[%s]pan=stereo|c0=1*c0|c1=1*c1[a_live];[a_live]aresample=48000:async=1:first_pts=0[aout];"+
			"[%s]pan=stereo|c0=1*c0|c1=1*c1[a_vod];[a_vod]aresample=48000:async=1:first_pts=0[vodout]",
		liveSrc, vodSrc)
}

// twoMixSpec is an RTMP destination that carries both of those mixes.
func twoMixSpec(target, liveSrc, vodSrc string) DestSpec {
	return DestSpec{
		Kind: DestRTMP, Target: target, RelayURL: "udp://127.0.0.1:20001",
		FilterComplex:       twoMixGraph(liveSrc, vodSrc),
		AudioOutLabel:       "aout",
		SecondAudioOutLabel: "vodout",
		AudioBitrate:        160, SampleRate: 48000, CopyVideo: true,
	}
}

// A destination that has not asked for a second mix must build exactly the
// command it always built. This is the half of the change that has to be
// invisible: every existing destination goes on emitting one audio track.
//
// Proven able to fail against the committed tree by making secondAudioMap
// return []string{"-map", "[" + s.AudioOutLabel + "]"} before its empty-label
// guard: TestASecondMixIsAbsentUnlessTheDestinationAsksForOne fails with two
// audio maps on a destination that asked for one.
func TestASecondMixIsAbsentUnlessTheDestinationAsksForOne(t *testing.T) {
	for _, kind := range []DestKind{DestRTMP, DestSRT, DestFile, DestAudio} {
		s := twoMixSpec("rtmp://a.example/live/k", "0:a:0", "0:a:1")
		s.Kind = kind
		s.SecondAudioOutLabel = ""
		if kind == DestAudio {
			s.Target = "icecast://source:pw@a.example:8000/live.mp3"
		}
		line := join(DestinationArgs(s))
		if n := strings.Count(line, "-map ["); n != 1 {
			t.Errorf("%s: %d filter-graph audio maps on a destination that asked for "+
				"one: %s", kind, n, line)
		}
	}
}

// The lift itself: a second mix becomes a second mapped, encoded audio track on
// an RTMP destination.
//
// Proven able to fail against the committed tree by making secondAudioMap
// return nil before its guards: this test fails with "the second mix was not
// mapped after the first".
func TestASecondMixIsMappedAsASecondAudioTrack(t *testing.T) {
	line := join(DestinationArgs(twoMixSpec("rtmp://a.example/live/k", "0:a:0", "0:a:1")))
	if !strings.Contains(line, "-map [aout] -map [vodout]") {
		t.Errorf("the second mix was not mapped after the first: %s", line)
	}
	// Both tracks are encoded by the one encoder block, which is the documented
	// limitation as much as it is the behaviour: no per-track bitrate exists.
	if !strings.Contains(line, "-c:a aac") || !strings.Contains(line, "-b:a 160k") {
		t.Errorf("the encoder options went missing from a two-track destination: %s", line)
	}
	if !strings.Contains(line, "-f flv") {
		t.Errorf("a two-track RTMP destination lost its muxer: %s", line)
	}
	// The video half of the promise is untouched.
	if !strings.Contains(line, "-map 0:v:0 -c:v copy") {
		t.Errorf("video is no longer stream-copied on a two-track destination: %s", line)
	}
}

// THE SAME MIX TWICE IS NOT A SECOND TRACK, and FFmpeg agrees violently: mapping
// one filter output twice is refused outright --
//
//	Output with label 'aout' does not exist in any defined filter graph, or was
//	already used elsewhere.
//
// exit 234, nothing published. So a spec naming the same label twice would be a
// destination that never starts, and the second map is dropped instead.
//
// Proven able to fail against the committed tree by deleting the
// `s.SecondAudioOutLabel == s.AudioOutLabel` guard from secondAudioMap: this
// test fails with "-map [aout] -map [aout]".
func TestNamingTheSameMixTwiceDoesNotBuildATwoTrackCommand(t *testing.T) {
	s := twoMixSpec("rtmp://a.example/live/k", "0:a:0", "0:a:1")
	s.SecondAudioOutLabel = s.AudioOutLabel
	line := join(DestinationArgs(s))
	if n := strings.Count(line, "-map ["); n != 1 {
		t.Errorf("a spec naming one mix twice built %d maps of it, which FFmpeg "+
			"refuses outright: %s", n, line)
	}
}

// An audio-only destination is one stream: an Icecast mount, or a file whose
// codec comes from its extension. A copy destination has no graph to take a
// second mix from at all. Neither may quietly grow a second track from a field a
// newer build wrote, because a row nobody validated would start a process
// nobody designed.
//
// Proven able to fail against the committed tree by deleting `|| s.Kind ==
// DestAudio` from secondAudioMap's first guard: the audio-only case fails with
// two maps. Removing the early `if s.CopyAudio` return in DestinationArgs is not
// the mutation for the copy case -- copyAudioArgs never calls secondAudioMap, so
// the copy case is structural and is asserted here to keep it that way.
func TestTheSecondMixIsIgnoredWhereThereIsNoSecondTrackToCarryIt(t *testing.T) {
	audio := twoMixSpec("icecast://source:pw@a.example:8000/live.mp3", "0:a:0", "0:a:1")
	audio.Kind = DestAudio
	if line := join(DestinationArgs(audio)); strings.Count(line, "-map [") != 1 {
		t.Errorf("an audio-only destination built a second audio track: %s", line)
	}

	cp := twoMixSpec("srt://a.example:9000", "0:a:0", "0:a:1")
	cp.Kind = DestSRT
	cp.CopyAudio = true
	cp.AudioTracks = []int{0, 1}
	line := join(DestinationArgs(cp))
	if strings.Contains(line, "-map [") {
		t.Errorf("a copy destination mapped a filter graph it does not have: %s", line)
	}
	if !strings.Contains(line, "-map 0:a:0 -map 0:a:1") {
		t.Errorf("the copy destination lost its track selection: %s", line)
	}
}

// THE LOAD-BEARING ONE. Everything above reads a string, and a string cannot say
// whether two tracks reached a far end -- which is the entire question, because
// RTMP egress was capped at one track on the belief that a second would not
// survive the wire.
//
// So this runs the exact argv DestinationArgs returns, through a real FFmpeg,
// into POLYEMESIS'S OWN RTMP SERVER (internal/rtmpserver, the same listener an
// encoder publishes to), pulls the stream back out with a real subscriber, and
// reads the TONES off each received track.
//
// TONES, NOT A TRACK COUNT. Two tracks carrying the same audio is a failure mode
// a count cannot see: it is two streams, two codecs, two plausible bitrates, and
// the second one is worthless. So each mix is fed from a different ingest tone
// and the assertion is on which band each RECEIVED track carries.
//
// AND THE CHECK IS PROVEN ABLE TO SEE THAT. The "duplicated" subtest publishes
// both mixes from the SAME ingest track -- two real tracks, identical audio --
// and asserts the spread the distinctness check demands is absent. Without it,
// "the tracks differ by 30 dB of balance" would be a sentence nobody had ever
// watched fail.
//
// Proven able to fail against the committed tree TWICE, once at each end.
//
// SENDING END: secondAudioMap returning nil before its guards. Both subtests
// fail with "the built command exited (exit status 234) without ever
// publishing", FFmpeg naming the now-unmapped 'vodout' output -- the same trap
// documented on the copy path, and a louder failure than the one-track stream
// that was expected.
//
// RECEIVING END, which is what makes this a measurement of the far end rather
// than of an argv: `case *message.AudioExMultitrack` in internal/rtmpserver's
// isSetup returning false, so the second track's decoder configuration is never
// replayed to a subscriber. Both subtests fail with "the subscriber never
// finished identifying the stream and was killed after 30s".
// NO `testing.Short()` GUARD, deliberately. It would be a new skip site on the
// #161 ratchet -- a free pass in the shape the census exists to count -- and
// nothing in this repository runs `go test -short`, so it would be a pass nobody
// ever took. Two subtests of about four seconds each is the price of the only
// evidence that egress carries two tracks.
func TestTwoDistinctMixesReachAnRTMPFarEnd(t *testing.T) {
	ffmpeg := testenv.FFmpegBinary(t, "ffmpeg",
		"ffmpeg is not installed, so nothing can publish the two-track stream this measures")
	ffprobe := testenv.FFmpegBinary(t, "ffprobe",
		"ffprobe is not installed, so the received tracks cannot be counted")

	dir := t.TempDir()
	src := filepath.Join(dir, "twotone.mkv")
	buildTwoToneSource(t, ffmpeg, src)

	for _, tc := range []struct {
		name    string
		liveSrc string
		vodSrc  string
		// distinct says the two received tracks must be different audio. The
		// false case is the harness proving it can tell, not a product claim.
		distinct bool
	}{
		{"two_mixes", "0:a:0", "0:a:1", true},
		{"the_same_mix_twice", "0:a:0", "0:a:0", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recv := filepath.Join(dir, tc.name+".mkv")
			publishTwoMixes(t, ffmpeg, src, recv, tc.liveSrc, tc.vodSrc)

			got := streamField(t, ffprobe, recv, "a", "codec_name")
			if len(got) != 2 {
				t.Fatalf("%d audio track(s) arrived at the far end, want 2: RTMP egress "+
					"carrying a second track is the whole subject of this test", len(got))
			}

			// balance is how far a track leans towards the live tone. The live
			// mix scores strongly positive; a VOD mix fed the other tone scores
			// strongly negative.
			bal := make([]float64, 2)
			for i := range bal {
				live := bandRMS(t, ffmpeg, recv, i, toneLive, bandLive)
				vod := bandRMS(t, ffmpeg, recv, i, toneVOD, bandVOD)
				bal[i] = live - vod
				t.Logf("received track %d: %d Hz %.1f dB, %d Hz %.1f dB (balance %.1f)",
					i, toneLive, live, toneVOD, vod, bal[i])
			}

			if !tc.distinct {
				// BOTH TRACKS REAL FIRST. Two silent tracks also score zero
				// apart, and "the check cannot tell duplicated audio from
				// distinct audio" would then be demonstrated by a measurement
				// of nothing.
				for i := range bal {
					assertBand(t, fmt.Sprintf("duplicated track %d", i), ffmpeg, recv, i,
						toneLive, bandLive, toneVOD, bandVOD)
				}
				if d := math.Abs(bal[0] - bal[1]); d >= mixSpread {
					t.Fatalf("two tracks carrying the SAME mix scored %.1f dB apart, and "+
						"the distinctness check below only demands %.1f. The check would "+
						"pass on duplicated audio, which is the failure it exists to "+
						"catch", d, mixSpread)
				}
				return
			}

			// Track 0 is the live mix: the live tone present, the VOD tone not.
			assertBand(t, "received track 0", ffmpeg, recv, 0,
				toneLive, bandLive, toneVOD, bandVOD)
			// Track 1 is the second mix, and carries the other tone.
			assertBand(t, "received track 1", ffmpeg, recv, 1,
				toneVOD, bandVOD, toneLive, bandLive)

			if d := bal[0] - bal[1]; d < mixSpread {
				t.Errorf("the two received tracks are only %.1f dB apart in band balance, "+
					"want at least %.1f: two tracks arrived but they are not two "+
					"different mixes, which is a second track worth nothing",
					d, mixSpread)
			}
		})
	}
}

// assertBand is "this track carries want and not other".
func assertBand(t *testing.T, who, bin, path string, track int, want, wantW, other, otherW float64) {
	t.Helper()
	got := bandRMS(t, bin, path, track, want, wantW)
	if got < toneFloor {
		t.Errorf("%s: %g Hz reads %.1f dB, below the %.1f dB presence floor: the mix "+
			"this track was supposed to carry is not on it", who, want, got, toneFloor)
	}
	if leak := bandRMS(t, bin, path, track, other, otherW); leak > got-toneMargin {
		t.Errorf("%s: %g Hz reads %.1f dB against %g Hz at %.1f dB, less than the %.1f dB "+
			"margin apart: this track carries the other mix too", who, other, leak,
			want, got, toneMargin)
	}
}

// buildTwoToneSource writes the ingest this destination is fed from: video plus
// two STEREO tone tracks, stereo because routing's pan matrix addresses c0 and
// c1 and a mono source would make the graph under test unbuildable.
func buildTwoToneSource(t *testing.T, bin, path string) {
	t.Helper()
	args := []string{"-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=size=160x120:rate=15:duration=6"}
	for _, hz := range []int{toneLive, toneVOD} {
		args = append(args, "-f", "lavfi", "-i",
			fmt.Sprintf("sine=frequency=%d:duration=6:sample_rate=48000", hz))
	}
	args = append(args, "-map", "0:v", "-map", "1:a", "-map", "2:a",
		"-ac", "2", "-c:v", "libx264", "-preset", "ultrafast", "-g", "15",
		"-c:a", "aac", "-shortest", path)
	if o, err := exec.Command(bin, args...).CombinedOutput(); err != nil {
		t.Fatalf("could not build the two-tone source: %v\n%s", err, lastLines(string(o), 6))
	}
}

// publishTwoMixes runs the built command against a real RTMP server and records
// what arrives.
//
// The far end is internal/rtmpserver rather than `ffmpeg -listen 1`, and the
// difference matters: an FFmpeg listener takes almost any bytes offered to it,
// on any playpath, from anyone. rtmpserver is the server this product ships --
// it performs the handshake, addresses the stream BY KEY, and its subscriber
// side has to carry the E-RTMP multitrack messages a second audio track arrives
// as. "Two tracks reached a real RTMP server" is the claim; a permissive sink
// could not support it.
func publishTwoMixes(t *testing.T, bin, src, recv, liveSrc, vodSrc string) {
	t.Helper()

	const key = "twotrack"
	tg := rtmpserver.Target{SourceID: 1, Name: "Main", Enabled: true, Ready: true}
	res := testenv.ReserveTCP(t)
	addr := "127.0.0.1:" + strconv.Itoa(res.Port())
	res.Release()
	srv := rtmpserver.New(slog.New(slog.NewTextHandler(io.Discard, nil)), addr,
		rtmpserver.ConstantTimeLookup(map[string]rtmpserver.Target{key: tg}))
	if err := srv.Start(); err != nil {
		t.Fatalf("could not start the far end: %v", err)
	}
	defer srv.Stop()
	target := "rtmp://" + addr + "/live/" + key

	args := DestinationArgs(twoMixSpec(target, liveSrc, vodSrc))
	// The relay the builder points at does not exist here; the file stands in
	// for it, and every argument under test is left exactly where the builder
	// put it. -re paces the file in real time so there is a LIVE stream for the
	// subscriber to attach to rather than a burst that is over before it dials.
	args = replaceRelayInput(t, args, src)
	args = spliceBefore(t, args, "-i", "-re")

	pubCtx, stopPub := context.WithCancel(context.Background())
	defer stopPub()
	pub := exec.CommandContext(pubCtx, bin, args...)
	var pubErr strings.Builder
	pub.Stderr = &pubErr
	if err := pub.Start(); err != nil {
		t.Fatalf("publisher: %v", err)
	}
	// Kill AND Wait: an unreaped child is a zombie holding a slot in this
	// process's table for the rest of the package's run. Wait runs in one place
	// only -- a second call returns an error about the first -- so the loop
	// below reads the result and puts it back for the cleanup to drain.
	waited := make(chan error, 1)
	go func() { waited <- pub.Wait() }()
	defer func() { _ = pub.Process.Kill(); <-waited }()

	// A COMMAND FFMPEG REFUSED IS NOT A SLOW ONE. Waiting out the deadline for a
	// publisher that has already exited turns "the argv is invalid" into "the
	// handshake did not happen", which reads as a far-end fault and hides the
	// message that says what is actually wrong.
	deadline := time.Now().Add(25 * time.Second)
	for !srv.Publishing(tg.SourceID) {
		select {
		case err := <-waited:
			waited <- err
			t.Fatalf("the built command exited (%v) without ever publishing. FFmpeg "+
				"said:\n%s\nargv: %v", err, lastLines(pubErr.String(), 8), args)
		default:
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("the built command never completed the RTMP handshake, so nothing "+
				"below is about a stream that was published. FFmpeg said:\n%s\nargv: %v",
				lastLines(pubErr.String(), 8), args)
		}
		time.Sleep(50 * time.Millisecond)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// -map 0 -c copy so a track that arrived is recorded whether or not this
	// test expected it: recording only the first would make "it sent one" and
	// "it sent two" look identical.
	sub := exec.CommandContext(ctx, bin, "-nostdin", "-hide_banner", "-loglevel", "error",
		"-rw_timeout", "15000000", "-i", target, "-map", "0", "-c", "copy",
		"-t", "3", "-y", recv)
	o, err := sub.CombinedOutput()
	if ctx.Err() != nil {
		// The shape a far end that dropped one track's decoder configuration
		// takes: the subscriber has the data, has no configuration for it, and
		// waits to identify a stream forever rather than failing. Named, because
		// "signal: killed" reads as a flake.
		t.Fatalf("the subscriber never finished identifying the stream and was killed "+
			"after %s. A track whose decoder configuration the far end did not replay "+
			"looks exactly like this. It said:\n%s\npublisher said:\n%s",
			30*time.Second, lastLines(string(o), 6), lastLines(pubErr.String(), 8))
	}
	if err != nil {
		t.Fatalf("the subscriber could not record what arrived: %v\n%s\npublisher said:\n%s",
			err, lastLines(string(o), 6), lastLines(pubErr.String(), 8))
	}
}

// spliceBefore inserts one argument immediately before the first occurrence of
// another, which is where FFmpeg reads an input option from.
func spliceBefore(t *testing.T, args []string, before, arg string) []string {
	t.Helper()
	for i, a := range args {
		if a == before {
			out := append([]string{}, args[:i]...)
			out = append(out, arg)
			return append(out, args[i:]...)
		}
	}
	t.Fatalf("no %s in the built command: %v", before, args)
	return nil
}

// bandRMS is one received track's RMS level inside one band, in dBFS. The same
// bandpass-and-astats measurement scripts/acceptance-multistream.sh makes of
// each platform's far end.
func bandRMS(t *testing.T, bin, path string, track int, freq, width float64) float64 {
	t.Helper()
	o, err := exec.Command(bin, "-hide_banner", "-nostats", "-i", path,
		"-map", fmt.Sprintf("0:a:%d", track),
		"-af", fmt.Sprintf("bandpass=f=%g:width_type=h:w=%g,astats=metadata=1:reset=0", freq, width),
		"-f", "null", "-").CombinedOutput()
	if err != nil {
		t.Fatalf("measuring track %d of %s at %g Hz: %v\n%s", track, path, freq, err,
			lastLines(string(o), 6))
	}
	var got string
	for _, line := range strings.Split(string(o), "\n") {
		if i := strings.Index(line, "RMS level dB:"); i >= 0 {
			got = strings.TrimSpace(line[i+len("RMS level dB:"):])
		}
	}
	if got == "" {
		t.Fatalf("astats reported no RMS level for track %d of %s at %g Hz; an "+
			"unparsed measurement must not read as silence:\n%s",
			track, path, freq, lastLines(string(o), 10))
	}
	// A band with nothing in it reads -inf, which is a real measurement and the
	// one the absence checks want; it is floored rather than left unparseable.
	if strings.EqualFold(got, "-inf") {
		return -200
	}
	v, err := strconv.ParseFloat(got, 64)
	if err != nil {
		t.Fatalf("astats reported %q as the RMS level for track %d at %g Hz, which is "+
			"neither a number nor -inf", got, track, freq)
	}
	return v
}
