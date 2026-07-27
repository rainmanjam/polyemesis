package transcribe

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/ffmpeg"
	"github.com/rainmanjam/polyemesis/internal/routing"
)

func liveTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// ---------------------------------------------------------------------------
// Off by default
// ---------------------------------------------------------------------------

func TestLiveCaptionsAreOffByDefault(t *testing.T) {
	if (LiveConfig{}).Enabled {
		t.Fatal("the zero LiveConfig must not be enabled")
	}
	if DefaultLiveConfig().Enabled {
		t.Fatal("DefaultLiveConfig must be off: live captions compete with the encoders and cost the user CPU they did not ask to spend")
	}
	if DefaultLiveConfig().Normalized().Enabled {
		t.Fatal("normalizing the default must not switch it on")
	}
}

func TestLiveCostWarningNamesTheCostAndTheGuarantee(t *testing.T) {
	for _, want := range []string{"CPU", "captions stop"} {
		if !strings.Contains(LiveCost, want) {
			t.Errorf("LiveCost must mention %q so the UI cannot promise something the code does not do: %q", want, LiveCost)
		}
	}
}

// ---------------------------------------------------------------------------
// Configuration
// ---------------------------------------------------------------------------

func TestLiveConfigNormalizedClampsTheDangerousValues(t *testing.T) {
	tests := []struct {
		name       string
		in         LiveConfig
		wantWindow time.Duration
		wantStep   time.Duration
		wantTrack  int
	}{
		// Track 0 is a real track, so an unset Track means track 0 and NOT
		// auto. Only DefaultLiveConfig asks for auto, and it says so.
		{"zero fills the timing defaults", LiveConfig{}, DefaultLiveWindow, DefaultLiveStep, 0},
		{"the default asks for the mic track", DefaultLiveConfig(), DefaultLiveWindow, DefaultLiveStep, LiveTrackAuto},
		{"window clamped to the maximum", LiveConfig{Window: time.Hour}, MaxLiveWindow, DefaultLiveStep, 0},
		{"window raised to the minimum", LiveConfig{Window: time.Millisecond}, MinLiveWindow, MinLiveWindow, 0},
		{"step never exceeds the window", LiveConfig{Window: 4 * time.Second, Step: 9 * time.Second}, 4 * time.Second, 4 * time.Second, 0},
		{"step raised to the minimum", LiveConfig{Window: 4 * time.Second, Step: time.Millisecond}, 4 * time.Second, MinLiveStep, 0},
		{"a track below auto becomes auto", LiveConfig{Track: -7}, DefaultLiveWindow, DefaultLiveStep, LiveTrackAuto},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.in.Normalized()
			if got.Window != tc.wantWindow {
				t.Errorf("window = %s, want %s", got.Window, tc.wantWindow)
			}
			if got.Step != tc.wantStep {
				t.Errorf("step = %s, want %s", got.Step, tc.wantStep)
			}
			if got.Track != tc.wantTrack {
				t.Errorf("track = %d, want %d", got.Track, tc.wantTrack)
			}
			if got.Step > got.Window {
				t.Errorf("step %s exceeds window %s, which would leave audio untranscribed", got.Step, got.Window)
			}
			if err := got.Validate(); err != nil {
				t.Errorf("normalized config must validate: %v", err)
			}
		})
	}
}

func TestLiveConfigNormalizedCapsThreadsSoTheEncodersKeepCores(t *testing.T) {
	got := LiveConfig{Threads: 64}.Normalized()
	if got.Threads != MaxLiveThreads {
		t.Errorf("threads = %d, want %d", got.Threads, MaxLiveThreads)
	}
}

func TestLiveConfigValidateRejectsUnrunnableSettings(t *testing.T) {
	tests := []struct {
		name string
		cfg  LiveConfig
		ok   bool
	}{
		{"normalized default", DefaultLiveConfig().Normalized(), true},
		{"window below the minimum", LiveConfig{Window: time.Second, Step: MinLiveStep}, false},
		{"window above the maximum", LiveConfig{Window: time.Hour, Step: MinLiveStep}, false},
		{"step below the minimum", LiveConfig{Window: DefaultLiveWindow, Step: time.Millisecond}, false},
		{"step longer than the window skips audio", LiveConfig{Window: 4 * time.Second, Step: 5 * time.Second}, false},
		{"track below auto", LiveConfig{Window: DefaultLiveWindow, Step: DefaultLiveStep, Track: -2}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.cfg.Validate()
			if tc.ok != (err == nil) {
				t.Fatalf("Validate() err = %v, want ok=%v", err, tc.ok)
			}
		})
	}
}

func TestLiveThreadsLeavesCoresForTheEncoders(t *testing.T) {
	tests := []struct {
		cores int
		want  int
	}{
		{0, 1}, {-4, 1}, {1, 1}, {2, 1}, {4, 1}, {8, 2}, {12, 3}, {16, 4}, {64, MaxLiveThreads},
	}
	for _, tc := range tests {
		if got := LiveThreads(tc.cores); got != tc.want {
			t.Errorf("LiveThreads(%d) = %d, want %d", tc.cores, got, tc.want)
		}
	}
}

func TestLiveDefaultModelNeverPromisesRealtimeItCannotDeliver(t *testing.T) {
	tests := []struct {
		name string
		hint HardwareHint
	}{
		{"two cores, no gpu", HardwareHint{CPUCores: 2}},
		{"sixteen cores", HardwareHint{CPUCores: 16}},
		{"gpu", HardwareHint{GPU: true, CPUCores: 4}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := LiveDefaultModel(tc.hint)
			if m.Size != SizeTiny && m.Size != SizeBase {
				t.Fatalf("live default model %q is %q; only tiny and base are honest live defaults", m.Name, m.Size)
			}
			if ok, why := RealtimeCapable(m, tc.hint); !ok {
				t.Fatalf("the live default must be one this machine can actually run live: %s", why)
			}
		})
	}
}

func TestLiveOfferFailsOpenExceptWhenThereIsNothingToRun(t *testing.T) {
	tests := []struct {
		name      string
		tools     *Tools
		model     Model
		hint      HardwareHint
		wantOffer bool
		wantWarn  bool
	}{
		{"no whisper at all", nil, mustModel("tiny"), HardwareHint{CPUCores: 8}, false, true},
		{"tiny on a small box", &Tools{Binary: "/usr/bin/whisper-cli"}, mustModel("tiny"), HardwareHint{CPUCores: 2}, true, false},
		{"large on a cpu box is offered with a warning", &Tools{Binary: "/usr/bin/whisper-cli"}, mustModel("large-v3"), HardwareHint{CPUCores: 4}, true, true},
		{"large on a gpu box gets the benefit of the doubt", &Tools{Binary: "/usr/bin/whisper-cli"}, mustModel("large-v3"), HardwareHint{GPU: true, CPUCores: 8}, true, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			offer, warn := LiveOffer(tc.tools, tc.model, tc.hint)
			if offer != tc.wantOffer {
				t.Errorf("offer = %v, want %v (warning %q)", offer, tc.wantOffer, warn)
			}
			if (warn != "") != tc.wantWarn {
				t.Errorf("warning = %q, want present=%v", warn, tc.wantWarn)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Windowing
// ---------------------------------------------------------------------------

func TestLiveWindowKeepsSampleAlignmentAcrossSplitReads(t *testing.T) {
	w := NewLiveWindow(time.Second)

	// Three bytes is one whole sample plus half of the next. The half must be
	// carried, not committed, or every subsequent sample is shifted by a byte
	// and the audio becomes noise.
	if _, err := w.Write([]byte{1, 2, 3}); err != nil {
		t.Fatal(err)
	}
	if got := w.Len(); got != 2 {
		t.Fatalf("after a 3-byte write the window holds %d bytes, want 2 with 1 carried", got)
	}
	if _, err := w.Write([]byte{4, 5, 6}); err != nil {
		t.Fatal(err)
	}
	if got := w.Len(); got != 6 {
		t.Fatalf("after 3+3 bytes the window holds %d bytes, want 6", got)
	}
	pcm, _, _ := w.Snapshot()
	if !bytes.Equal(pcm, []byte{1, 2, 3, 4, 5, 6}) {
		t.Fatalf("window contents = %v, want the bytes in order", pcm)
	}
}

func TestLiveWindowWriteReportsTheFullLengthAndNeverBlocks(t *testing.T) {
	w := NewLiveWindow(MinLiveWindow)
	big := make([]byte, 10*len(w.buf))
	done := make(chan int, 1)
	go func() {
		n, err := w.Write(big)
		if err != nil {
			t.Error(err)
		}
		done <- n
	}()
	select {
	case n := <-done:
		if n != len(big) {
			t.Fatalf("Write returned %d, want %d: a rolling window discards, it does not short-write", n, len(big))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Write blocked; the audio tap must never be stalled by the caption path")
	}
}

func TestLiveWindowRollsTheOldestAudioOutAndTracksTheStreamClock(t *testing.T) {
	w := NewLiveWindow(time.Second) // 16000 samples == 32000 bytes
	capBytes := WhisperSampleRate * bytesPerSample

	if _, err := w.Write(make([]byte, 30000)); err != nil {
		t.Fatal(err)
	}
	fresh := bytes.Repeat([]byte{0xAB}, 4000)
	if _, err := w.Write(fresh); err != nil {
		t.Fatal(err)
	}

	pcm, startMS, endMS := w.Snapshot()
	if len(pcm) != capBytes {
		t.Fatalf("window holds %d bytes, want the capacity %d", len(pcm), capBytes)
	}
	if !bytes.Equal(pcm[len(pcm)-4000:], fresh) {
		t.Fatal("the newest audio is missing from the end of the window; the ring wrapped wrong")
	}
	if !bytes.Equal(pcm[:len(pcm)-4000], make([]byte, capBytes-4000)) {
		t.Fatal("the window kept the wrong prefix after wrapping")
	}
	// 34000 bytes == 17000 samples == 1062ms of stream, of which the last
	// second is still in the window.
	if endMS != 1062 {
		t.Errorf("endMS = %d, want 1062", endMS)
	}
	if startMS != 62 {
		t.Errorf("startMS = %d, want 62", startMS)
	}
	if got := w.TotalMS(); got != endMS {
		t.Errorf("TotalMS = %d, want the window end %d", got, endMS)
	}
}

// ---------------------------------------------------------------------------
// Overlap de-duplication
// ---------------------------------------------------------------------------

func TestLiveMergePublishesEachUtteranceOnce(t *testing.T) {
	tests := []struct {
		name        string
		segs        []Segment
		windowStart int64
		emitted     int64
		wantTexts   []string
		wantEnd     int64
	}{
		{
			name:      "everything new on the first window",
			segs:      []Segment{{StartMS: 0, EndMS: 900, Text: "hello"}, {StartMS: 1000, EndMS: 1800, Text: "world"}},
			wantTexts: []string{"hello", "world"},
			wantEnd:   1800,
		},
		{
			name:        "the overlap whisper re-heard is dropped",
			segs:        []Segment{{StartMS: 0, EndMS: 900, Text: "hello"}, {StartMS: 1000, EndMS: 1800, Text: "world"}},
			windowStart: 0,
			emitted:     900,
			wantTexts:   []string{"world"},
			wantEnd:     1800,
		},
		{
			name:        "segments are rebased onto the stream clock",
			segs:        []Segment{{StartMS: 100, EndMS: 900, Text: "later"}},
			windowStart: 30000,
			wantTexts:   []string{"later"},
			wantEnd:     30900,
		},
		{
			name:        "a segment more than half new survives the boundary",
			segs:        []Segment{{StartMS: 0, EndMS: 1000, Text: "straddles"}},
			windowStart: 0,
			emitted:     400,
			wantTexts:   []string{"straddles"},
			wantEnd:     1000,
		},
		{
			name:        "a segment mostly already said is dropped",
			segs:        []Segment{{StartMS: 0, EndMS: 1000, Text: "straddles"}},
			windowStart: 0,
			emitted:     600,
			wantTexts:   nil,
			wantEnd:     600,
		},
		{
			name:        "a silence marker does not advance the watermark over real speech",
			segs:        []Segment{{StartMS: 0, EndMS: 5000, Text: "[BLANK_AUDIO]"}, {StartMS: 5000, EndMS: 6000, Text: "speech"}},
			windowStart: 0,
			wantTexts:   []string{"speech"},
			wantEnd:     6000,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, end := LiveMerge(tc.segs, tc.windowStart, tc.emitted)
			var texts []string
			for _, s := range got {
				texts = append(texts, s.Text)
			}
			if strings.Join(texts, "|") != strings.Join(tc.wantTexts, "|") {
				t.Errorf("texts = %v, want %v", texts, tc.wantTexts)
			}
			if end != tc.wantEnd {
				t.Errorf("watermark = %d, want %d", end, tc.wantEnd)
			}
		})
	}
}

func TestIsNonSpeechDropsAnnotationsAndKeepsWords(t *testing.T) {
	tests := []struct {
		text string
		want bool
	}{
		{"", true},
		{"   ", true},
		{"[BLANK_AUDIO]", true},
		{"(silence)", true},
		{"[ Music ]", true},
		{"♪♪♪", true},
		{"*laughs*", true},
		{"...", true},
		{"hello", false},
		{"Hello (laughs) again", false},
		{"R&D is fine", false},
	}
	for _, tc := range tests {
		if got := IsNonSpeech(tc.text); got != tc.want {
			t.Errorf("IsNonSpeech(%q) = %v, want %v", tc.text, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// The health guard
// ---------------------------------------------------------------------------

func TestLiveHealthCheckStopsCaptionsWhenTheMachineCannotKeepUp(t *testing.T) {
	h := LiveHealth{MaxLag: 10 * time.Second, MaxDropStreak: 3, MaxRealtimeFactor: 0.9, GraceWindows: 2}
	tests := []struct {
		name string
		st   LiveStats
		ok   bool
	}{
		{"the cold first window is forgiven", LiveStats{Windows: 0, RealtimeFactor: 12, LagMS: 60000}, true},
		{"healthy", LiveStats{Windows: 100, RealtimeFactor: 0.4, LagMS: 1200}, true},
		{"drop streak at the limit is still allowed", LiveStats{Windows: 100, DropStreak: 3}, true},
		{"drop streak past the limit means audio is being lost", LiveStats{Windows: 100, DropStreak: 4}, false},
		{"a drop streak overrides the grace window", LiveStats{Windows: 0, DropStreak: 9}, false},
		{"lag past the limit", LiveStats{Windows: 100, LagMS: 10001}, false},
		{"inference slower than the step it has", LiveStats{Windows: 100, RealtimeFactor: 1.4}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ok, why := h.Check(tc.st)
			if ok != tc.ok {
				t.Fatalf("Check = %v (%q), want %v", ok, why, tc.ok)
			}
			if !ok && why == "" {
				t.Fatal("a stopped caption stream must always carry a reason the operator can act on")
			}
		})
	}
}

func TestLiveHealthDropStreakIsDerivedFromTheWindowAndStep(t *testing.T) {
	// Four steps of 500ms fill the 2s window; the fifth consecutive drop is the
	// point at which audio rolls out untranscribed.
	cfg := LiveConfig{Window: 2 * time.Second, Step: 500 * time.Millisecond}.Normalized()
	if got := cfg.Health.MaxDropStreak; got != 4 {
		t.Fatalf("MaxDropStreak = %d, want 4 (window/step)", got)
	}
	// A short window still keeps a floor, so a pathological configuration
	// cannot turn every scattered drop into a hard stop.
	cfg = LiveConfig{Window: 2 * time.Second, Step: 2 * time.Second}.Normalized()
	if got := cfg.Health.MaxDropStreak; got < DefaultLiveMinDropStreak {
		t.Fatalf("MaxDropStreak = %d, want at least %d", got, DefaultLiveMinDropStreak)
	}
}

// ---------------------------------------------------------------------------
// The session: backpressure and visible degradation
// ---------------------------------------------------------------------------

// chunkReader hands out fixed-size chunks of silence, optionally waiting for a
// tick before each one so a test can drive the loop one step at a time.
type chunkReader struct {
	chunk  []byte
	left   int
	tick   <-chan struct{}
	first  bool
	closed bool
}

func newChunkReader(bytesPerChunk, chunks int, tick <-chan struct{}) *chunkReader {
	return &chunkReader{chunk: make([]byte, bytesPerChunk), left: chunks, tick: tick, first: true}
}

func (r *chunkReader) Read(p []byte) (int, error) {
	if r.left == 0 {
		return 0, io.EOF
	}
	if r.tick != nil && !r.first {
		select {
		case <-r.tick:
		case <-time.After(5 * time.Second):
			return 0, io.EOF
		}
	}
	r.first = false
	r.left--
	return copy(p, r.chunk), nil
}

// stepBytes is one Step's worth of 16 kHz mono PCM.
func stepBytes(d time.Duration) int {
	return int(d.Seconds()*float64(WhisperSampleRate)) * bytesPerSample
}

func TestLiveSessionDropsStepsRatherThanQueueingThem(t *testing.T) {
	cfg := LiveConfig{Window: 2 * time.Second, Step: 500 * time.Millisecond}.Normalized()

	started := make(chan struct{}, 1)
	release := make(chan struct{})
	tr := LiveTranscriberFunc(func(ctx context.Context, pcm []byte) ([]Segment, error) {
		select {
		case started <- struct{}{}:
		default:
		}
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return []Segment{{StartMS: 0, EndMS: 400, Text: "one"}}, nil
	})

	s := NewLiveSession(liveTestLogger(), cfg, tr)
	// Five chunks: one dispatch and four steps that arrive while it is still
	// running. Four is the limit, so this must NOT degrade — it must simply
	// skip the work, which is the whole backpressure design.
	r := newChunkReader(stepBytes(cfg.Step), 5, nil)

	done := make(chan error, 1)
	go func() { done <- s.Run(context.Background(), r) }()

	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("no inference was ever dispatched")
	}
	waitFor(t, func() bool { return s.Stats().Dropped == 4 })

	st := s.Stats()
	if st.DropStreak != 4 {
		t.Errorf("DropStreak = %d, want 4", st.DropStreak)
	}
	if st.Degraded {
		t.Errorf("session degraded at the drop limit; scattered drops are the design working, not a failure: %q", st.Warning)
	}

	close(release)
	if err := waitErr(t, done); err != nil {
		t.Fatalf("Run = %v", err)
	}
	if got := s.Stats().Windows; got != 1 {
		t.Errorf("Windows = %d, want exactly 1: dropped steps must not be queued and run later", got)
	}
}

func TestLiveSessionDegradesVisiblyInsteadOfAccumulatingLag(t *testing.T) {
	cfg := LiveConfig{Window: 2 * time.Second, Step: 500 * time.Millisecond}.Normalized()

	release := make(chan struct{})
	defer close(release)
	tr := LiveTranscriberFunc(func(ctx context.Context, pcm []byte) ([]Segment, error) {
		select {
		case <-release:
		case <-ctx.Done():
		}
		return nil, ctx.Err()
	})

	var (
		mu      sync.Mutex
		reasons []string
	)
	s := NewLiveSession(liveTestLogger(), cfg, tr, WithLiveDegrade(func(reason string) {
		mu.Lock()
		reasons = append(reasons, reason)
		mu.Unlock()
	}))

	// Seven chunks: one dispatch, then six consecutive drops. The fifth is past
	// the limit and the caption stream must give up there and say so.
	r := newChunkReader(stepBytes(cfg.Step), 7, nil)
	done := make(chan error, 1)
	go func() { done <- s.Run(context.Background(), r) }()

	if err := waitErr(t, done); err != nil {
		t.Fatalf("Run = %v", err)
	}

	st := s.Stats()
	if !st.Degraded {
		t.Fatal("the session kept running past its drop budget; a caption feature that quietly steals CPU from the encoders is the failure this guard exists to prevent")
	}
	if st.Warning == "" {
		t.Fatal("degrading must be visible: no warning was recorded")
	}
	mu.Lock()
	n := len(reasons)
	mu.Unlock()
	if n != 1 {
		t.Fatalf("degrade callback fired %d times, want exactly 1", n)
	}
	if st.Running {
		t.Error("a degraded session must report itself stopped")
	}
}

func TestLiveSessionPublishesRebasedStampedCaptionsOnce(t *testing.T) {
	cfg := LiveConfig{
		Window:  2 * time.Second,
		Step:    500 * time.Millisecond,
		Track:   3,
		Speaker: "Guest",
	}.Normalized()

	tick := make(chan struct{}, 1)
	tr := LiveTranscriberFunc(func(ctx context.Context, pcm []byte) ([]Segment, error) {
		// The same utterance every window, which is exactly what an overlapping
		// window does in practice: whisper re-hears the audio it was already
		// given and reports it again.
		return []Segment{{StartMS: 0, EndMS: 400, Text: "hello there"}}, nil
	})

	var (
		mu   sync.Mutex
		caps []LiveCaption
	)
	s := NewLiveSession(liveTestLogger(), cfg, tr,
		WithLiveEmit(func(c LiveCaption) {
			mu.Lock()
			caps = append(caps, c)
			mu.Unlock()
		}),
		WithLiveStats(func(LiveStats) {
			select {
			case tick <- struct{}{}:
			default:
			}
		}),
	)

	r := newChunkReader(stepBytes(cfg.Step), 4, tick)
	if err := s.Run(context.Background(), r); err != nil {
		t.Fatalf("Run = %v", err)
	}

	st := s.Stats()
	if st.Windows != 4 {
		t.Fatalf("Windows = %d, want 4 (the reader is paced, so nothing should be dropped)", st.Windows)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(caps) != 1 {
		t.Fatalf("published %d captions, want 1: the overlap between windows must not be published twice", len(caps))
	}
	c := caps[0]
	if c.Track != 3 || c.Speaker != "Guest" {
		t.Errorf("caption = track %d speaker %q, want 3/Guest", c.Track, c.Speaker)
	}
	if c.Text != "hello there" {
		t.Errorf("text = %q", c.Text)
	}
	if c.At.IsZero() {
		t.Error("a caption must carry when it was produced")
	}
}

func TestLiveSessionEndsCleanlyWhenTheAudioStops(t *testing.T) {
	cfg := DefaultLiveConfig().Normalized()
	s := NewLiveSession(liveTestLogger(), cfg, LiveTranscriberFunc(
		func(context.Context, []byte) ([]Segment, error) { return nil, nil }))

	if err := s.Run(context.Background(), bytes.NewReader(nil)); err != nil {
		t.Fatalf("Run over an empty tap = %v, want nil", err)
	}
	st := s.Stats()
	if st.Windows != 0 || st.Running {
		t.Fatalf("no audio must mean no work: %+v", st)
	}
}

func TestLiveSessionStopsWhenItsContextIsCancelled(t *testing.T) {
	cfg := LiveConfig{Window: 2 * time.Second, Step: 500 * time.Millisecond}.Normalized()
	tr := LiveTranscriberFunc(func(ctx context.Context, pcm []byte) ([]Segment, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	s := NewLiveSession(liveTestLogger(), cfg, tr)

	ctx, cancel := context.WithCancel(context.Background())
	// A reader that never ends, so only the cancellation can stop the loop.
	r := newChunkReader(stepBytes(cfg.Step), 1<<20, nil)
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx, r) }()

	waitFor(t, func() bool { return s.Stats().Dropped > 0 })
	cancel()
	if err := waitErr(t, done); err != nil {
		t.Fatalf("Run after cancel = %v, want nil", err)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition not met within 5s")
}

func waitErr(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return")
		return nil
	}
}

// ---------------------------------------------------------------------------
// Command line and container plumbing
// ---------------------------------------------------------------------------

func TestLiveExtractArgsTapsOneTrackAtWhispersRate(t *testing.T) {
	args := LiveExtractArgs(LiveExtractSpec{FFmpeg: "ffmpeg", Input: "udp://127.0.0.1:21001", Track: 2})
	joined := strings.Join(args, " ")

	for _, want := range []string{
		"-map 0:a:2",       // audio-relative, or every track is off by one
		"-ac 1",            // whisper is trained on mono
		"-ar 16000",        // ...at 16 kHz
		"-f s16le",         // raw, because a live stream has no length for a header
		"pipe:1",           // straight into the session
		"-fflags nobuffer", // input latency lands directly on the caption delay
		"-flags low_delay", // ...and then on the health budget
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("args missing %q: %s", want, joined)
		}
	}
	if strings.Contains(joined, "afftdn") {
		t.Error("denoise must be off unless the operator annotated the track")
	}
	if !strings.Contains(strings.Join(LiveExtractArgs(LiveExtractSpec{Track: 0, Denoise: true}), " "), "afftdn") {
		t.Error("denoise must reach the command line when asked for")
	}
}

func TestWAVHeaderDescribesSixteenKilohertzMono(t *testing.T) {
	h := WAVHeader(32000, WhisperSampleRate, WhisperChannels)
	if len(h) != 44 {
		t.Fatalf("header is %d bytes, want the canonical 44", len(h))
	}
	if string(h[0:4]) != "RIFF" || string(h[8:12]) != "WAVE" || string(h[36:40]) != "data" {
		t.Fatalf("header chunks are wrong: %q", h)
	}
	tests := []struct {
		name string
		at   int
		want uint32
		size int
	}{
		{"riff size", 4, 36 + 32000, 4},
		{"fmt size", 16, 16, 4},
		{"sample rate", 24, WhisperSampleRate, 4},
		{"byte rate", 28, WhisperSampleRate * bytesPerSample, 4},
		{"data size", 40, 32000, 4},
	}
	for _, tc := range tests {
		got := uint32(h[tc.at]) | uint32(h[tc.at+1])<<8 | uint32(h[tc.at+2])<<16 | uint32(h[tc.at+3])<<24
		if got != tc.want {
			t.Errorf("%s = %d, want %d", tc.name, got, tc.want)
		}
	}
}

func TestWriteWAVProducesAReadableFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "w.wav")
	pcm := bytes.Repeat([]byte{0x11, 0x22}, 100)
	if err := WriteWAV(path, pcm); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) != 44+len(pcm) {
		t.Fatalf("file is %d bytes, want %d", len(raw), 44+len(pcm))
	}
	if !bytes.Equal(raw[44:], pcm) {
		t.Fatal("the samples did not survive the write")
	}
}

func TestLiveVTTAppendsCuesWithASingleHeader(t *testing.T) {
	path := filepath.Join(t.TempDir(), "live.vtt")
	v, err := OpenLiveVTT(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []LiveCaption{
		{Segment: Segment{StartMS: 0, EndMS: 1500, Text: "first", Speaker: "Host"}},
		{Segment: Segment{StartMS: 1500, EndMS: 3000, Text: "second & <third>", Speaker: "Host"}},
	} {
		if err := v.Append(c); err != nil {
			t.Fatal(err)
		}
	}
	if got := v.Cues(); got != 2 {
		t.Fatalf("Cues = %d, want 2", got)
	}
	if err := v.Close(); err != nil {
		t.Fatal(err)
	}
	if err := v.Close(); err != nil {
		t.Fatalf("Close must be safe twice: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	if n := strings.Count(body, "WEBVTT"); n != 1 {
		t.Fatalf("found %d WEBVTT headers, want exactly 1: a second one mid-file makes the whole cue list unparseable", n)
	}
	if !strings.Contains(body, "00:00:01.500 --> 00:00:03.000") {
		t.Errorf("second cue timing missing (WebVTT uses a period, not a comma):\n%s", body)
	}
	if !strings.Contains(body, "1\n") || !strings.Contains(body, "2\n") {
		t.Errorf("cues must be numbered in sequence as they are appended:\n%s", body)
	}
	if !strings.Contains(body, "Host: second &amp; &lt;third&gt;") {
		t.Errorf("cue payload must be escaped and speaker-prefixed:\n%s", body)
	}
	if strings.Contains(body, "<third>") {
		t.Error("unescaped markup reached the cue payload")
	}
}

func TestLiveVTTIsSafeOnANilReceiver(t *testing.T) {
	var v *LiveVTT
	if err := v.Append(LiveCaption{}); err != nil {
		t.Fatal(err)
	}
	if err := v.Close(); err != nil {
		t.Fatal(err)
	}
	if v.Cues() != 0 || v.Path() != "" {
		t.Fatal("nil receiver must answer empty")
	}
}

// ---------------------------------------------------------------------------
// Track selection
// ---------------------------------------------------------------------------

func TestLiveTrackPrefersTheMicrophone(t *testing.T) {
	src := routing.Source{
		Tracks: []routing.Track{{Index: 0, Channels: 2}, {Index: 1, Channels: 2}, {Index: 2, Channels: 2}},
		Annotations: []routing.TrackAnnotation{
			{Track: 0, Role: routing.RoleMusic, Label: "Music bed"},
			{Track: 1, Role: routing.RoleMic, Label: "Host mic", Denoise: true},
		},
	}
	tests := []struct {
		name    string
		want    int
		track   int
		speaker string
		ok      bool
	}{
		{name: "auto picks the mic", want: LiveTrackAuto, track: 1, speaker: "Host mic", ok: true},
		{name: "an explicit track is honoured", want: 2, track: 2, ok: true},
		{name: "a track that is not there falls back rather than failing", want: 9, track: 1, speaker: "Host mic", ok: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := LiveTrack(src, tc.want)
			if ok != tc.ok {
				t.Fatalf("ok = %v, want %v", ok, tc.ok)
			}
			if got.Track != tc.track {
				t.Fatalf("track = %d, want %d", got.Track, tc.track)
			}
			if tc.speaker != "" && got.Speaker != tc.speaker {
				t.Errorf("speaker = %q, want %q", got.Speaker, tc.speaker)
			}
		})
	}
}

func TestLiveTrackReportsWhenThereIsNoAudioAtAll(t *testing.T) {
	if _, ok := LiveTrack(routing.Source{}, LiveTrackAuto); ok {
		t.Fatal("a source with no tracks cannot be captioned")
	}
}

// ---------------------------------------------------------------------------
// Wiring
// ---------------------------------------------------------------------------

func TestNewLiveCaptionerRefusesWhatItCannotRun(t *testing.T) {
	tests := []struct {
		name    string
		whisper *Tools
		ffmpeg  string
		cfg     LiveConfig
		wantErr bool
	}{
		{"no whisper installed", nil, "/usr/bin/ffmpeg", DefaultLiveConfig(), true},
		{"no ffmpeg", &Tools{Binary: "/w"}, "", DefaultLiveConfig(), true},
		{"a step longer than the window", &Tools{Binary: "/w"}, "/usr/bin/ffmpeg", LiveConfig{Window: 4 * time.Second, Step: 30 * time.Second}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// The last case is not an error because Normalized clamps it first:
			// a caller that asks for something impossible gets the nearest
			// runnable thing, not a dead feature.
			_, err := NewLiveCaptioner(liveTestLogger(), &ffmpeg.Tools{FFmpeg: tc.ffmpeg}, tc.whisper, tc.cfg)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, want error=%v", err, tc.wantErr)
			}
		})
	}
}
