package transcribe

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rainmanjam/polyemesis/internal/ffmpeg"
	"github.com/rainmanjam/polyemesis/internal/routing"
)

// Realtime captions: the one job in this workstream that deliberately competes
// with the live stream.
//
// Everything else here is a queued job that yields to the broadcast, because a
// transcript arriving an hour later costs nothing. Live captions cannot be that
// — a caption an hour late is not a caption — so this is the exception, and the
// exception is dangerous. A caption feature that starves the destination
// encoders and drops frames on somebody's live broadcast is not a degraded
// feature, it is a catastrophic one.
//
// Three rules follow, and all three are enforced in code rather than in
// documentation:
//
//  1. OFF BY DEFAULT. DefaultLiveConfig().Enabled is false and nothing in this
//     package turns it on. See TestLiveCaptionsAreOffByDefault.
//  2. BOUNDED WORK. Exactly one inference is ever in flight. When a step comes
//     round and the previous one has not finished, the step is DROPPED, not
//     queued. Lag therefore cannot accumulate; the captions get worse instead,
//     which is the correct direction to fail.
//  3. VISIBLE DEGRADATION. When the drops or the lag say the machine cannot do
//     this, the caption stream STOPS and says so. Limping along invisibly is
//     how a stream ends up dropping frames with nobody knowing why.
//
// The whisper child also runs under the governor's nice wrapper, so the OS
// scheduler prefers the encoders whenever both want the CPU.

// LiveCost is the plain-language warning a UI must show next to the switch.
// It is a constant here rather than a string in the frontend so that the honest
// version of it is the one that ships, and so it cannot drift away from what
// the code actually does.
const LiveCost = "Live captions run speech recognition on this machine while you are streaming. " +
	"That competes with your encoders for CPU. If the machine cannot keep up, captions stop and " +
	"you are told — polyemesis will never let captions cost you frames on air. Use the smallest " +
	"model that is good enough, and transcribe the recording afterwards if you want quality."

// LiveTrackAuto asks for the mic-roled track, the same choice DefaultTracks
// would make.
const LiveTrackAuto = -1

// Windowing defaults.
//
// A rolling window rather than a growing one: whisper's cost is linear in the
// audio it is given, so a window that grows is a job that gets slower forever.
// Eight seconds is roughly the context whisper needs to punctuate a sentence
// sensibly, and a three-second step means each inference re-hears five seconds
// it has already heard, which is what stops a word being cut in half at the
// boundary.
const (
	DefaultLiveWindow = 8 * time.Second
	DefaultLiveStep   = 3 * time.Second

	MinLiveWindow = 2 * time.Second
	MaxLiveWindow = 30 * time.Second
	MinLiveStep   = 500 * time.Millisecond

	// MaxLiveThreads caps whisper's parallelism regardless of core count. The
	// encoders need those cores more than the captions do.
	MaxLiveThreads = 4
)

// Health defaults. See LiveHealth for what each one means.
const (
	DefaultLiveMaxLag        = 15 * time.Second
	DefaultLiveMaxRealtime   = 0.9
	DefaultLiveGraceWindows  = 2
	DefaultLiveMinDropStreak = 2
	liveReadChunk            = 16 << 10
	liveChildKillDelay       = 3 * time.Second
	// liveShutdownTimeout bounds the wait for an in-flight inference on stop.
	// It is generous because the alternative is returning while a whisper child
	// still holds the scratch directory the caller is about to remove.
	liveShutdownTimeout = 2 * time.Minute
)

// bytesPerSample is s16le mono.
const bytesPerSample = 2

// LiveSubdir is where the captioner keeps its scratch WAVs.
const LiveSubdir = "captions"

// LiveVTTName is the sidecar caption file's name inside the playout directory.
const LiveVTTName = "captions.vtt"

// LiveWorkDir is the scratch directory for live captioning under the data dir.
func LiveWorkDir(dataDir string) string { return filepath.Join(dataDir, LiveSubdir) }

// LiveConfig is everything the operator chose.
type LiveConfig struct {
	// Enabled is false in the zero value and false in DefaultLiveConfig, and
	// that is load-bearing. See the package comment above.
	Enabled bool `json:"enabled"`

	// Track is the 0-based audio track to caption, or LiveTrackAuto. Exactly
	// one track: this is a live budget, not a transcript, and captioning four
	// microphones at once is four times the cost for a caption bar that can
	// only show one line anyway. The recording still gets every track
	// transcribed properly, afterwards, by the queued job.
	Track int `json:"track"`
	// Speaker labels the captions from that track, resolved by LiveTrack.
	Speaker string `json:"speaker,omitempty"`
	// Denoise carries the track annotation's noise suppression through.
	Denoise bool `json:"denoise,omitempty"`

	Model    string  `json:"model,omitempty"`
	Backend  Backend `json:"backend,omitempty"`
	Language string  `json:"language,omitempty"`
	// Threads is whisper's -t. Zero means LiveThreads decides from the core
	// count, which is the answer that leaves the encoders room.
	Threads int `json:"threads,omitempty"`

	Window time.Duration `json:"window"`
	Step   time.Duration `json:"step"`

	Health LiveHealth `json:"health"`

	// VTTPath, when set, also appends captions to a WebVTT file. See LiveVTT
	// for what that file is and is not.
	VTTPath string `json:"vttPath,omitempty"`
}

// DefaultLiveConfig is the shape of the feature switched off.
func DefaultLiveConfig() LiveConfig {
	return LiveConfig{
		Enabled: false,
		Track:   LiveTrackAuto,
		Window:  DefaultLiveWindow,
		Step:    DefaultLiveStep,
		Health:  DefaultLiveHealth(),
	}
}

// Normalized fills in the blanks and clamps the dangerous values.
func (c LiveConfig) Normalized() LiveConfig {
	if c.Track < LiveTrackAuto {
		c.Track = LiveTrackAuto
	}
	if c.Window <= 0 {
		c.Window = DefaultLiveWindow
	}
	c.Window = min(max(c.Window, MinLiveWindow), MaxLiveWindow)
	if c.Step <= 0 {
		c.Step = DefaultLiveStep
	}
	if c.Step < MinLiveStep {
		c.Step = MinLiveStep
	}
	// A step longer than the window would leave gaps of audio nobody ever
	// transcribed, which reads to the operator as whisper silently missing
	// speech rather than as a misconfiguration.
	if c.Step > c.Window {
		c.Step = c.Window
	}
	if c.Threads < 0 {
		c.Threads = 0
	}
	if c.Threads > MaxLiveThreads {
		c.Threads = MaxLiveThreads
	}
	c.Health = c.Health.normalized(c.Window, c.Step)
	c.Model = strings.TrimSpace(c.Model)
	c.Language = strings.TrimSpace(c.Language)
	return c
}

// Validate rejects a configuration that cannot run. It is deliberately thin:
// the model choice, the backend and the machine's speed are all judged
// elsewhere and all of those judgements fail open.
func (c LiveConfig) Validate() error {
	if c.Window < MinLiveWindow || c.Window > MaxLiveWindow {
		return fmt.Errorf("caption window %s out of range (%s-%s)", c.Window, MinLiveWindow, MaxLiveWindow)
	}
	if c.Step < MinLiveStep {
		return fmt.Errorf("caption step %s is below the %s minimum", c.Step, MinLiveStep)
	}
	if c.Step > c.Window {
		return fmt.Errorf("caption step %s is longer than the %s window, which would skip audio", c.Step, c.Window)
	}
	if c.Track < LiveTrackAuto {
		return fmt.Errorf("caption track %d is not a track index", c.Track)
	}
	return nil
}

// LiveThreads picks whisper's thread count for live captioning.
//
// A quarter of the cores, at least one and at most MaxLiveThreads. Whisper will
// happily take every core it is offered and the encoders will not get them
// back; this is the number that keeps the exception to the governing principle
// from swallowing the machine.
func LiveThreads(cores int) int {
	if cores <= 0 {
		return 1
	}
	return min(max(cores/4, 1), MaxLiveThreads)
}

// LiveDefaultModel is the model a fresh install should caption with.
//
// Smaller than DefaultModel picks for a queued job, and deliberately so. A
// queued transcription can take ten minutes over a ten-minute recording and
// nobody notices; a live caption that takes ten seconds over eight seconds of
// audio is already broken. Promising "large, live, on CPU" would be a lie, so
// this never offers it as a default.
func LiveDefaultModel(h HardwareHint) Model {
	if h.GPU || h.CPUCores >= 8 {
		return mustModel("base")
	}
	return mustModel("tiny")
}

// LiveOffer reports whether live captions are worth putting in front of this
// operator at all, and says why not when they are not.
//
// It fails open in both directions that matter. No whisper binary is the one
// hard no — there is nothing to run. Everything else is a warning attached to a
// switch that still works, because a check that is wrong in the restrictive
// direction removes a feature from a machine that could have run it, with no
// way for the user to find out.
func LiveOffer(w *Tools, m Model, h HardwareHint) (bool, string) {
	if !w.Available() {
		return false, w.Unavailable()
	}
	if ok, why := RealtimeCapable(m, h); !ok {
		return true, why
	}
	return true, ""
}

// LiveHealth is the budget that decides when captions stop.
//
// This is the guard the package comment calls catastrophic to get wrong. Every
// field is a way of asking the same question — "is this machine actually
// keeping up?" — from a different direction, because each direction misses a
// case the others catch.
type LiveHealth struct {
	// MaxLag is how far behind the live edge the caption stream may fall before
	// it is abandoned. Zero uses DefaultLiveMaxLag.
	MaxLag time.Duration `json:"maxLag"`
	// MaxDropStreak is how many consecutive steps may be skipped because the
	// previous inference was still running. Normalized derives it from
	// window/step: that many drops in a row is exactly the point at which audio
	// starts rolling out of the window untranscribed, i.e. the point at which
	// the captions stop being a record of what was said.
	MaxDropStreak int `json:"maxDropStreak"`
	// MaxRealtimeFactor is inference wall time as a fraction of the step. Above
	// 1.0 the machine cannot finish one window before the next is due, so it is
	// losing ground on every step; the default sits just under that to catch it
	// before it does.
	MaxRealtimeFactor float64 `json:"maxRealtimeFactor"`
	// GraceWindows exempts the first few inferences. The first one loads a
	// gigabyte of model off disk and every measurement of it is a measurement
	// of the disk, not of the machine's ability to caption.
	GraceWindows int `json:"graceWindows"`
}

// DefaultLiveHealth is the budget for the default window and step.
func DefaultLiveHealth() LiveHealth {
	return LiveHealth{
		MaxLag:            DefaultLiveMaxLag,
		MaxRealtimeFactor: DefaultLiveMaxRealtime,
		GraceWindows:      DefaultLiveGraceWindows,
	}
}

func (h LiveHealth) normalized(window, step time.Duration) LiveHealth {
	if h.MaxLag <= 0 {
		h.MaxLag = DefaultLiveMaxLag
	}
	if h.MaxRealtimeFactor <= 0 {
		h.MaxRealtimeFactor = DefaultLiveMaxRealtime
	}
	if h.GraceWindows < 0 {
		h.GraceWindows = 0
	}
	if h.GraceWindows == 0 {
		h.GraceWindows = DefaultLiveGraceWindows
	}
	if h.MaxDropStreak <= 0 && step > 0 {
		h.MaxDropStreak = int(window / step)
	}
	if h.MaxDropStreak < DefaultLiveMinDropStreak {
		h.MaxDropStreak = DefaultLiveMinDropStreak
	}
	return h
}

// Normalized fills the blanks using the default window and step.
func (h LiveHealth) Normalized() LiveHealth {
	return h.normalized(DefaultLiveWindow, DefaultLiveStep)
}

// Check judges a stats snapshot. It returns false and an operator-facing reason
// when the caption stream should be abandoned.
func (h LiveHealth) Check(st LiveStats) (bool, string) {
	h = h.Normalized()
	// Everything below is a measurement of a machine mid-inference; before the
	// grace windows are done, the measurements are of a cold model load.
	if st.Windows < int64(h.GraceWindows) && st.DropStreak <= h.MaxDropStreak {
		return true, ""
	}
	if st.DropStreak > h.MaxDropStreak {
		return false, fmt.Sprintf(
			"speech recognition skipped %d windows in a row, so audio is going untranscribed. "+
				"This machine cannot caption live while it is streaming — try a smaller model, or "+
				"transcribe the recording afterwards.", st.DropStreak)
	}
	if h.MaxLag > 0 && st.LagMS > h.MaxLag.Milliseconds() {
		return false, fmt.Sprintf(
			"captions fell %.1fs behind the live stream, past the %.0fs limit. Stopping them rather "+
				"than letting the delay grow.", float64(st.LagMS)/1000, h.MaxLag.Seconds())
	}
	if st.RealtimeFactor > h.MaxRealtimeFactor {
		return false, fmt.Sprintf(
			"speech recognition is taking %.0f%% of the time it has available, so it cannot keep up. "+
				"Try a smaller model.", st.RealtimeFactor*100)
	}
	return true, ""
}

// LiveStats is what the dashboard shows and what LiveHealth judges.
type LiveStats struct {
	Running bool    `json:"running"`
	Track   int     `json:"track"`
	Speaker string  `json:"speaker,omitempty"`
	Model   string  `json:"model,omitempty"`
	Backend Backend `json:"backend,omitempty"`

	// Windows is completed inferences, Captions the lines actually published
	// (far fewer: overlap and silence are both discarded).
	Windows  int64 `json:"windows"`
	Captions int64 `json:"captions"`
	// Dropped counts steps skipped because an inference was still running, and
	// DropStreak how many of those were consecutive. The streak is the number
	// that matters — scattered drops are the design working, a run of them is
	// audio being lost.
	Dropped    int64 `json:"dropped"`
	DropStreak int   `json:"dropStreak"`

	// LagMS is how far behind the live edge the last published caption was.
	LagMS int64 `json:"lagMs"`
	// RealtimeFactor is the last inference's wall time over the step. Under 1.0
	// means there is headroom.
	RealtimeFactor float64 `json:"realtimeFactor"`

	// Degraded records that the guard fired, and Warning says what an operator
	// should do about it. Both survive the session stopping: the whole point is
	// that the user finds out.
	Degraded bool   `json:"degraded"`
	Warning  string `json:"warning,omitempty"`

	StartedAt time.Time `json:"startedAt,omitzero"`
}

// LiveCaption is one published caption line.
type LiveCaption struct {
	Segment
	// At is when it was produced, and LagMS how far behind the live edge it
	// was. Both are on the wire because a caption bar that is four seconds
	// behind and honest about it is far more usable than one that is four
	// seconds behind and pretending otherwise.
	At    time.Time `json:"at"`
	LagMS int64     `json:"lagMs"`
}

// LiveWindow is the rolling PCM buffer between the audio and whisper.
//
// It is an io.Writer that never blocks and never grows: once full, the oldest
// audio is overwritten. That is the whole backpressure story on the audio side
// — the FFmpeg child feeding this can never be stalled by a slow inference,
// because there is nothing here for it to wait on.
type LiveWindow struct {
	mu   sync.Mutex
	buf  []byte
	head int
	n    int
	// total is whole samples committed since the start, which is the stream
	// clock every caption timestamp is rebased onto.
	total int64
	// carry holds a trailing odd byte so the sample alignment survives a read
	// that split a sample. Without it every short read shifts the audio by half
	// a sample and the result is white noise.
	carry []byte
}

// NewLiveWindow sizes a window to hold d of 16 kHz mono PCM.
func NewLiveWindow(d time.Duration) *LiveWindow {
	if d <= 0 {
		d = DefaultLiveWindow
	}
	samples := int(d.Seconds() * float64(WhisperSampleRate))
	if samples < WhisperSampleRate {
		samples = WhisperSampleRate
	}
	return &LiveWindow{buf: make([]byte, samples*bytesPerSample)}
}

// Write implements io.Writer. It always reports the full length written: a
// rolling window discarding old audio is the design, not a short write.
func (w *LiveWindow) Write(p []byte) (int, error) {
	n := len(p)
	w.mu.Lock()
	defer w.mu.Unlock()

	if len(w.carry) > 0 {
		p = append(append(make([]byte, 0, len(w.carry)+len(p)), w.carry...), p...)
		w.carry = w.carry[:0]
	}
	if odd := len(p) % bytesPerSample; odd != 0 {
		w.carry = append(w.carry[:0], p[len(p)-odd:]...)
		p = p[:len(p)-odd]
	}
	if len(p) == 0 {
		return n, nil
	}
	w.total += int64(len(p) / bytesPerSample)
	w.push(p)
	return n, nil
}

func (w *LiveWindow) push(b []byte) {
	c := len(w.buf)
	if len(b) >= c {
		copy(w.buf, b[len(b)-c:])
		w.head, w.n = 0, c
		return
	}
	tail := (w.head + w.n) % c
	k := copy(w.buf[tail:], b)
	if k < len(b) {
		copy(w.buf, b[k:])
	}
	if w.n+len(b) > c {
		w.head = (w.head + (w.n + len(b) - c)) % c
		w.n = c
		return
	}
	w.n += len(b)
}

// Snapshot copies out the whole window plus the stream-clock span it covers.
func (w *LiveWindow) Snapshot() (pcm []byte, startMS, endMS int64) {
	w.mu.Lock()
	defer w.mu.Unlock()

	out := make([]byte, w.n)
	k := copy(out, w.buf[w.head:min(w.head+w.n, len(w.buf))])
	if k < w.n {
		copy(out[k:], w.buf[:w.n-k])
	}
	end := samplesToMS(w.total)
	return out, samplesToMS(w.total - int64(w.n/bytesPerSample)), end
}

// TotalMS is the stream clock: how much audio has arrived since the start.
func (w *LiveWindow) TotalMS() int64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	return samplesToMS(w.total)
}

// Len is how many bytes the window currently holds.
func (w *LiveWindow) Len() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.n
}

func samplesToMS(s int64) int64 { return s * 1000 / WhisperSampleRate }

// LiveTranscriber turns one window of 16 kHz mono PCM into segments whose
// offsets are relative to the START of that window. The session rebases them
// onto the stream clock; keeping the contract window-relative is what lets a
// test drive the whole loop with no whisper binary anywhere.
type LiveTranscriber interface {
	Transcribe(ctx context.Context, pcm []byte) ([]Segment, error)
}

// LiveTranscriberFunc adapts a function to LiveTranscriber.
type LiveTranscriberFunc func(ctx context.Context, pcm []byte) ([]Segment, error)

// Transcribe implements LiveTranscriber.
func (f LiveTranscriberFunc) Transcribe(ctx context.Context, pcm []byte) ([]Segment, error) {
	return f(ctx, pcm)
}

// LiveOption configures a session or a captioner.
type LiveOption func(*liveOptions)

type liveOptions struct {
	now       func() time.Time
	emit      func(LiveCaption)
	onDegrade func(reason string)
	onStats   func(LiveStats)
	nice      func(name string, args []string) (string, []string)
}

// WithLiveClock replaces time.Now, for tests.
func WithLiveClock(fn func() time.Time) LiveOption {
	return func(o *liveOptions) {
		if fn != nil {
			o.now = fn
		}
	}
}

// WithLiveEmit registers the caption sink. It is called from the inference
// goroutine and MUST NOT block — the engine publishes onto the event broker,
// which drops rather than waits, for exactly this reason.
func WithLiveEmit(fn func(LiveCaption)) LiveOption {
	return func(o *liveOptions) { o.emit = fn }
}

// WithLiveDegrade registers the visible-failure callback. It fires once, with
// the reason, immediately before the caption stream gives up.
func WithLiveDegrade(fn func(reason string)) LiveOption {
	return func(o *liveOptions) { o.onDegrade = fn }
}

// WithLiveStats registers a callback fired after every completed inference.
func WithLiveStats(fn func(LiveStats)) LiveOption {
	return func(o *liveOptions) { o.onStats = fn }
}

// WithLiveNice hands over the governor's command wrapper, so the whisper child
// starts niced and the OS scheduler prefers the encoders. This is the only
// reason it is defensible to run speech recognition next to a live broadcast at
// all, so a captioner built without it logs the fact.
func WithLiveNice(fn func(name string, args []string) (string, []string)) LiveOption {
	return func(o *liveOptions) { o.nice = fn }
}

func newLiveOptions(opts []LiveOption) liveOptions {
	o := liveOptions{now: time.Now}
	for _, fn := range opts {
		if fn != nil {
			fn(&o)
		}
	}
	return o
}

// LiveSession is the windowing, dispatch and backpressure loop.
//
// It knows nothing about FFmpeg or whisper: it reads PCM from an io.Reader and
// hands windows to a LiveTranscriber. That split is what makes the dangerous
// half of this feature — the half that must never accumulate lag — testable
// without either binary present.
type LiveSession struct {
	log *slog.Logger
	cfg LiveConfig
	tr  LiveTranscriber
	win *LiveWindow
	opt liveOptions

	mu   sync.Mutex
	st   LiveStats
	busy bool
	// emittedMS is the stream time up to which captions have been published.
	// It is what stops the five seconds of overlap in each window being
	// published five times.
	emittedMS int64
	lastText  string
	stopped   bool
	cancel    context.CancelFunc
}

// NewLiveSession builds the loop. cfg is normalized here so a caller cannot
// hand it a step longer than its window.
func NewLiveSession(log *slog.Logger, cfg LiveConfig, tr LiveTranscriber, opts ...LiveOption) *LiveSession {
	cfg = cfg.Normalized()
	return &LiveSession{
		log: log,
		cfg: cfg,
		tr:  tr,
		win: NewLiveWindow(cfg.Window),
		opt: newLiveOptions(opts),
		st: LiveStats{
			Track:   cfg.Track,
			Speaker: cfg.Speaker,
			Model:   cfg.Model,
			Backend: cfg.Backend,
		},
	}
}

// Stats snapshots the counters.
func (s *LiveSession) Stats() LiveStats {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.st
}

// Run reads PCM until the reader ends, the context is cancelled, or the health
// guard fires. It returns nil for all three: a caption stream that stopped
// because the machine could not keep up is a reported condition, not an error
// the caller should retry.
func (s *LiveSession) Run(ctx context.Context, pcm io.Reader) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	s.mu.Lock()
	s.cancel = cancel
	s.st.Running = true
	s.st.StartedAt = s.opt.now()
	s.mu.Unlock()

	var inflight sync.WaitGroup
	defer func() {
		// Wait for the outstanding inference before returning, so a caller that
		// tears down the work directory on the next line does not pull a WAV
		// out from under a running child.
		inflight.Wait()
		s.mu.Lock()
		s.st.Running = false
		s.mu.Unlock()
	}()

	stepMS := s.cfg.Step.Milliseconds()
	buf := make([]byte, liveReadChunk)
	var lastStepMS int64
	for {
		n, err := pcm.Read(buf)
		if n > 0 {
			_, _ = s.win.Write(buf[:n])
		}
		if ctx.Err() != nil {
			return nil
		}
		if total := s.win.TotalMS(); total-lastStepMS >= stepMS && total > 0 {
			lastStepMS = total
			s.step(ctx, &inflight)
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
}

// step dispatches one inference, or records a drop.
//
// The drop is the load-bearing behaviour of this whole file. Queueing the step
// instead would let the backlog — and therefore the lag, and therefore the CPU
// spent on audio nobody will see captioned in time — grow without limit while
// the encoders next door starve.
func (s *LiveSession) step(ctx context.Context, inflight *sync.WaitGroup) {
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return
	}
	if s.busy {
		s.st.Dropped++
		s.st.DropStreak++
		st := s.st
		s.mu.Unlock()
		s.guard(st)
		return
	}
	s.busy = true
	s.st.DropStreak = 0
	s.mu.Unlock()

	pcm, startMS, endMS := s.win.Snapshot()
	began := s.opt.now()

	inflight.Add(1)
	go func() {
		defer inflight.Done()
		segs, err := s.tr.Transcribe(ctx, pcm)
		wall := s.opt.now().Sub(began)

		s.mu.Lock()
		s.busy = false
		s.mu.Unlock()

		if err != nil {
			if ctx.Err() != nil {
				return
			}
			// One failed window is not a reason to abandon the stream: whisper
			// can fail a window on a transient and recover on the next one, and
			// killing the feature on the first hiccup is the restrictive
			// direction this repo has learned to distrust.
			s.log.Warn("live captions: window failed", "err", err)
			return
		}
		s.complete(segs, startMS, endMS, wall)
	}()
}

// complete publishes a finished window and re-judges the budget.
func (s *LiveSession) complete(segs []Segment, startMS, endMS int64, wall time.Duration) {
	now := s.opt.now()
	// How much newer audio arrived while that inference ran. This, not the wall
	// clock, is the honest measure of how far behind the captions are.
	lag := s.win.TotalMS() - endMS
	if lag < 0 {
		lag = 0
	}

	s.mu.Lock()
	emit, newEnd := LiveMerge(segs, startMS, s.emittedMS)
	// Whisper re-hearing the overlap sometimes re-emits the same sentence with
	// slightly different boundaries. Identical consecutive text is the one case
	// the midpoint rule cannot catch, so it is caught here.
	if len(emit) > 0 && emit[0].Text == s.lastText {
		emit = emit[1:]
	}
	if len(emit) > 0 {
		s.lastText = emit[len(emit)-1].Text
	}
	s.emittedMS = newEnd
	s.st.Windows++
	s.st.Captions += int64(len(emit))
	s.st.LagMS = lag
	if s.cfg.Step > 0 {
		s.st.RealtimeFactor = float64(wall) / float64(s.cfg.Step)
	}
	st := s.st
	sink := s.opt.emit
	track, speaker := s.cfg.Track, s.cfg.Speaker
	s.mu.Unlock()

	if sink != nil {
		for _, seg := range emit {
			seg.Track = track
			seg.Speaker = speaker
			sink(LiveCaption{Segment: seg, At: now, LagMS: lag})
		}
	}
	if s.opt.onStats != nil {
		s.opt.onStats(st)
	}
	s.guard(st)
}

// guard stops the caption stream, visibly, when the budget says the machine
// cannot do this.
//
// Stopping is the point. The alternative — carrying on with captions that fall
// further behind every window while whisper keeps taking CPU from the encoders
// — risks dropped frames on a live broadcast, which is unrecoverable, to
// protect a caption that is already useless.
func (s *LiveSession) guard(st LiveStats) {
	ok, reason := s.cfg.Health.Check(st)
	if ok {
		return
	}

	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return
	}
	s.stopped = true
	s.st.Degraded = true
	s.st.Warning = reason
	cancel := s.cancel
	s.mu.Unlock()

	s.log.Warn("live captions stopped: this machine cannot keep up", "reason", reason)
	if s.opt.onDegrade != nil {
		s.opt.onDegrade(reason)
	}
	if cancel != nil {
		cancel()
	}
}

// LiveMerge rebases one window's segments onto the stream clock and drops the
// ones already published.
//
// The rule is the midpoint: a segment is new when more than half of it lies
// after everything already emitted. Overlapping windows mean whisper re-hears
// several seconds each time and re-segments them slightly differently, so a
// boundary test on the start or the end alone either duplicates a sentence or
// swallows one. The midpoint is the one test that is stable under that jitter,
// and it is a single sentence to explain to whoever reads this next.
//
// Non-speech markers are dropped here rather than at the edge: "[BLANK_AUDIO]"
// advancing the emitted watermark would suppress the real speech that follows
// it inside the same window.
func LiveMerge(segs []Segment, windowStartMS, emittedMS int64) ([]Segment, int64) {
	out := make([]Segment, 0, len(segs))
	end := emittedMS
	for _, seg := range segs {
		seg.StartMS += windowStartMS
		seg.EndMS += windowStartMS
		if seg.EndMS < seg.StartMS {
			seg.EndMS = seg.StartMS
		}
		if IsNonSpeech(seg.Text) {
			continue
		}
		if (seg.StartMS+seg.EndMS)/2 < emittedMS {
			continue
		}
		seg.Text = strings.TrimSpace(seg.Text)
		out = append(out, seg)
		if seg.EndMS > end {
			end = seg.EndMS
		}
	}
	return out, end
}

// bracketRE matches whisper's non-lexical annotations, in all three styles it
// emits them in.
var bracketRE = regexp.MustCompile(`\[[^\]]*\]|\([^)]*\)|\*[^*]*\*|♪[^♪]*♪`)

// IsNonSpeech reports whether a caption line carries no actual speech.
//
// The test is "does anything survive removing the annotations", which correctly
// keeps "Hello (laughs) again" and correctly drops "[BLANK_AUDIO]", "(silence)"
// and a line of music notes — without a keyword list that would have to be
// maintained per language.
func IsNonSpeech(text string) bool {
	t := strings.TrimSpace(text)
	if t == "" {
		return true
	}
	t = bracketRE.ReplaceAllString(t, " ")
	t = strings.Trim(t, " \t.,;:-–—…!?\"'♪")
	return strings.TrimSpace(t) == ""
}

// LiveTrack picks the single track to caption.
//
// want may be LiveTrackAuto, in which case the same preference order the queued
// job uses applies and the mic-roled track wins. A requested track the source
// does not have falls back to the automatic choice rather than failing: an
// operator whose second microphone did not come back should get captions from
// the one that did.
func LiveTrack(src routing.Source, want int) (TrackChoice, bool) {
	if want >= 0 {
		if plan := PlanTracks(src, []int{want}); len(plan) > 0 {
			return plan[0], true
		}
	}
	plan := PlanTracks(src, nil)
	if len(plan) == 0 {
		return TrackChoice{}, false
	}
	return plan[0], true
}

// LiveExtractSpec describes pulling one live audio track off the relay.
type LiveExtractSpec struct {
	FFmpeg string
	// Input is the relay URL the hub handed out.
	Input string
	// Track is the 0-based audio track: the N in -map 0:a:N.
	Track int
	// Denoise applies the spectral denoiser. It costs CPU on the live path, so
	// it is only ever on when the operator has already flagged that track as a
	// bad room.
	Denoise bool
}

// LiveExtractArgs builds the FFmpeg command line that feeds the captioner.
//
// It writes raw s16le to stdout rather than a WAV: there is no length to put in
// a header on a stream that has not ended, and the session wraps each window in
// its own header anyway.
//
// The buffering flags are the reason this is not ExtractArgs with a different
// output. FFmpeg's default input buffering is tuned for throughput on a file
// and adds seconds of latency on a live input — seconds that would land
// directly on the caption delay and then on the health budget, so a purely
// cosmetic default would look exactly like a machine too slow to caption.
func LiveExtractArgs(s LiveExtractSpec) []string {
	args := []string{
		"-nostdin", "-hide_banner", "-loglevel", "error",
		"-fflags", "nobuffer", "-flags", "low_delay",
		"-i", s.Input,
		"-map", "0:a:" + strconv.Itoa(s.Track),
		"-vn", "-sn", "-dn",
	}
	if s.Denoise {
		args = append(args, "-af", "afftdn=nf=-25")
	}
	args = append(args,
		"-ac", strconv.Itoa(WhisperChannels),
		"-ar", strconv.Itoa(WhisperSampleRate),
		"-c:a", "pcm_s16le",
		"-f", "s16le",
		"pipe:1",
	)
	return args
}

// WAVHeader builds a 44-byte canonical RIFF/WAVE header for s16le PCM.
//
// Whisper reads files, not pipes, so each window is written out as a real WAV.
// Hand-rolling the header rather than shelling out to FFmpeg again saves a
// process spawn every three seconds on the one code path that is explicitly
// competing with the encoders for CPU.
func WAVHeader(dataBytes, sampleRate, channels int) []byte {
	if channels <= 0 {
		channels = WhisperChannels
	}
	if sampleRate <= 0 {
		sampleRate = WhisperSampleRate
	}
	const bitsPerSample = 8 * bytesPerSample
	blockAlign := channels * bytesPerSample
	byteRate := sampleRate * blockAlign

	h := make([]byte, 44)
	copy(h[0:], "RIFF")
	binary.LittleEndian.PutUint32(h[4:], uint32(36+dataBytes))
	copy(h[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(h[16:], 16)
	binary.LittleEndian.PutUint16(h[20:], 1) // PCM
	binary.LittleEndian.PutUint16(h[22:], uint16(channels))
	binary.LittleEndian.PutUint32(h[24:], uint32(sampleRate))
	binary.LittleEndian.PutUint32(h[28:], uint32(byteRate))
	binary.LittleEndian.PutUint16(h[32:], uint16(blockAlign))
	binary.LittleEndian.PutUint16(h[34:], bitsPerSample)
	copy(h[36:], "data")
	binary.LittleEndian.PutUint32(h[40:], uint32(dataBytes))
	return h
}

// WriteWAV writes one window of 16 kHz mono PCM as a WAV file.
func WriteWAV(path string, pcm []byte) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if _, err := f.Write(WAVHeader(len(pcm), WhisperSampleRate, WhisperChannels)); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(pcm); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// whisperLive is the real LiveTranscriber: one whisper.cpp run per window.
type whisperLive struct {
	tools   *Tools
	cfg     LiveConfig
	model   string
	workDir string
	nice    func(name string, args []string) (string, []string)

	mu  sync.Mutex
	seq int64
}

// Transcribe implements LiveTranscriber.
func (w *whisperLive) Transcribe(ctx context.Context, pcm []byte) ([]Segment, error) {
	if len(pcm) == 0 {
		return nil, nil
	}
	w.mu.Lock()
	w.seq++
	seq := w.seq
	w.mu.Unlock()

	// The scratch WAV is named by sequence, not by timestamp, so a directory
	// left behind by a crash reads as an obvious sequence rather than as a pile
	// of anonymous files.
	wav := filepath.Join(w.workDir, "live-"+strconv.FormatInt(seq, 10)+".wav")
	if err := WriteWAV(wav, pcm); err != nil {
		return nil, fmt.Errorf("live captions: write window: %w", err)
	}
	defer os.Remove(wav)

	spec := WhisperSpec{
		Model:    w.model,
		Input:    wav,
		Language: w.cfg.Language,
		Threads:  w.cfg.Threads,
		Backend:  w.cfg.Backend,
		Flags:    w.tools,
	}
	name, args := w.command(w.tools.Binary, WhisperArgs(spec))

	// No -of and no -oj: stdout carries the same timestamped segments, and a
	// live window does not need the confidences enough to pay for a JSON file
	// written and re-read every step.
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.WaitDelay = liveChildKillDelay

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("live captions: start whisper: %w", err)
	}

	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		segs  []Segment
		tail  strings.Builder
		info  strings.Builder
		lines int
	)
	wg.Add(2)
	go func() {
		defer wg.Done()
		scanLines(stdout, func(line string) {
			if seg, ok := ParseSegmentLine(line); ok {
				mu.Lock()
				segs = append(segs, seg)
				mu.Unlock()
			}
		})
	}()
	go func() {
		defer wg.Done()
		scanLines(stderr, func(line string) {
			if strings.Contains(line, "system_info") {
				info.WriteString(line)
				info.WriteByte('\n')
			}
			if IsNoteworthy(line) && lines < 8 {
				lines++
				tail.WriteString(strings.TrimSpace(line))
				tail.WriteByte('\n')
			}
		})
		if b := ParseSystemInfo(info.String()); len(b) > 0 {
			w.tools.SetBackends(b)
		}
	}()

	err = cmd.Wait()
	wg.Wait()
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, fmt.Errorf("live captions: whisper failed: %w: %s", err, strings.TrimSpace(tail.String()))
	}

	mu.Lock()
	defer mu.Unlock()
	return NormalizeSegments(segs), nil
}

func (w *whisperLive) command(name string, args []string) (string, []string) {
	if w.nice == nil {
		return name, args
	}
	return w.nice(name, args)
}

// LiveCaptioner wires an FFmpeg audio tap and a whisper.cpp child onto a
// LiveSession. It is the thing the engine starts and stops.
type LiveCaptioner struct {
	log     *slog.Logger
	tools   *ffmpeg.Tools
	whisper *Tools
	cfg     LiveConfig
	opts    []LiveOption
	opt     liveOptions

	mu      sync.Mutex
	sess    *LiveSession
	cancel  context.CancelFunc
	done    chan struct{}
	last    LiveStats
	running bool
}

// NewLiveCaptioner validates what can be validated up front and builds the
// captioner. It does not start anything.
func NewLiveCaptioner(log *slog.Logger, tools *ffmpeg.Tools, whisper *Tools, cfg LiveConfig, opts ...LiveOption) (*LiveCaptioner, error) {
	cfg = cfg.Normalized()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if tools == nil || tools.FFmpeg == "" {
		return nil, errors.New("live captions need FFmpeg to tap the audio track")
	}
	if !whisper.Available() {
		return nil, errors.New(whisper.Unavailable())
	}
	o := newLiveOptions(opts)
	if o.nice == nil {
		// Not fatal — a captioner with no wrapper still works — but it is the
		// difference between "yields to the stream" and "competes with it", so
		// it does not happen silently.
		log.Warn("live captions: no nice wrapper, speech recognition will compete with the encoders at equal priority")
	}
	return &LiveCaptioner{log: log, tools: tools, whisper: whisper, cfg: cfg, opts: opts, opt: o}, nil
}

// Config is the normalized configuration this captioner will run.
func (c *LiveCaptioner) Config() LiveConfig { return c.cfg }

// Stats reports the live counters, or the final ones after it stopped. The
// degraded flag and its warning deliberately survive the stop.
func (c *LiveCaptioner) Stats() LiveStats {
	c.mu.Lock()
	sess, last := c.sess, c.last
	c.mu.Unlock()
	if sess != nil {
		return sess.Stats()
	}
	return last
}

// Running reports whether the loop is up.
func (c *LiveCaptioner) Running() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.running
}

// Start taps relayURL and begins captioning. modelPath is a resolved ggml file
// and workDir a scratch directory the captioner may write windows into.
//
// It returns as soon as the child is up; the loop runs until ctx ends, the
// relay stops, or the health guard fires.
func (c *LiveCaptioner) Start(ctx context.Context, relayURL, modelPath, workDir string) error {
	c.mu.Lock()
	if c.running {
		c.mu.Unlock()
		return errors.New("live captions are already running")
	}
	c.mu.Unlock()

	if err := os.MkdirAll(workDir, 0o755); err != nil {
		return fmt.Errorf("live captions: %w", err)
	}

	args := LiveExtractArgs(LiveExtractSpec{
		FFmpeg:  c.tools.FFmpeg,
		Input:   relayURL,
		Track:   max(c.cfg.Track, 0),
		Denoise: c.cfg.Denoise,
	})

	ctx, cancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(ctx, c.tools.FFmpeg, args...)
	cmd.WaitDelay = liveChildKillDelay
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		cancel()
		return err
	}
	if err := cmd.Start(); err != nil {
		cancel()
		return fmt.Errorf("live captions: start audio tap: %w", err)
	}

	tr := &whisperLive{
		tools:   c.whisper,
		cfg:     c.cfg,
		model:   modelPath,
		workDir: workDir,
		nice:    c.opt.nice,
	}
	sess := NewLiveSession(c.log, c.cfg, tr, c.opts...)

	done := make(chan struct{})
	c.mu.Lock()
	c.sess, c.cancel, c.done, c.running = sess, cancel, done, true
	c.mu.Unlock()

	go func() {
		scanLines(stderr, func(line string) {
			if IsNoteworthy(line) {
				c.log.Warn("live captions: audio tap", "line", strings.TrimSpace(line))
			}
		})
	}()

	go func() {
		defer close(done)
		defer cancel()
		if err := sess.Run(ctx, stdout); err != nil && ctx.Err() == nil {
			c.log.Warn("live captions: stopped", "err", err)
		}
		// The tap is killed by the context, but the wait is what reaps it. A
		// captioner that leaves an FFmpeg child behind on every stop would
		// accumulate one per toggle, each still decoding the relay.
		_ = cmd.Wait()

		c.mu.Lock()
		c.last = sess.Stats()
		c.last.Running = false
		c.sess, c.running = nil, false
		c.mu.Unlock()
	}()

	c.log.Info("live captions started",
		"track", c.cfg.Track, "model", c.cfg.Model, "window", c.cfg.Window, "step", c.cfg.Step)
	return nil
}

// Stop ends the loop and waits for the children to be reaped.
func (c *LiveCaptioner) Stop() {
	c.mu.Lock()
	cancel, done := c.cancel, c.done
	c.cancel, c.done = nil, nil
	c.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if done != nil {
		select {
		case <-done:
		case <-time.After(liveShutdownTimeout):
			c.log.Error("live captions: shutdown timed out waiting for whisper")
		}
	}
}

// LiveVTT appends captions to a WebVTT file beside the HLS playout.
//
// Be clear about what this is: a growing sidecar file a player or the /watch
// page can poll, NOT a conformant HLS subtitle rendition. A real one needs its
// own segments and an X-TIMESTAMP-MAP tying cue time to the media timeline, and
// producing that correctly for a live window is a playout-workstream problem,
// not a transcription one. Writing this file is cheap and useful; claiming it
// is HLS subtitles would not be.
type LiveVTT struct {
	mu   sync.Mutex
	f    *os.File
	path string
	n    int
}

// OpenLiveVTT creates or truncates the sidecar and writes its header.
func OpenLiveVTT(path string) (*LiveVTT, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	if _, err := f.WriteString("WEBVTT\n\n"); err != nil {
		f.Close()
		return nil, err
	}
	return &LiveVTT{f: f, path: path}, nil
}

// Path is where the file lives.
func (v *LiveVTT) Path() string {
	if v == nil {
		return ""
	}
	return v.path
}

// Append writes one cue.
//
// Deliberately not fsynced. A reader on this host sees the bytes through the
// page cache the moment the write returns, and the only thing a sync would buy
// is durability across a power cut — for a caption sidecar that is regenerated
// next session, at the price of a blocking disk operation on the live path,
// next to a recorder writing multitrack video.
func (v *LiveVTT) Append(c LiveCaption) error {
	if v == nil {
		return nil
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.f == nil {
		return nil
	}
	v.n++
	// The cue is built here rather than through VTT(), which writes a header
	// and restarts its cue numbering from 1 on every call. Both are right for a
	// standalone file and wrong for one that is appended to: a second WEBVTT
	// line mid-file makes the whole thing unparseable.
	body := cueBody(c.Segment, SubtitleOptions{Speakers: c.Speaker != ""}, true)
	cue := fmt.Sprintf("%d\n%s --> %s\n%s\n\n",
		v.n, FormatVTTTime(c.StartMS), FormatVTTTime(c.EndMS), body)
	_, err := v.f.WriteString(cue)
	return err
}

// Cues is how many have been written.
func (v *LiveVTT) Cues() int {
	if v == nil {
		return 0
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.n
}

// Close closes the file. It is safe on a nil receiver and safe twice.
func (v *LiveVTT) Close() error {
	if v == nil {
		return nil
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.f == nil {
		return nil
	}
	err := v.f.Close()
	v.f = nil
	return err
}
