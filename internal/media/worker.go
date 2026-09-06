package media

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rainmanjam/polyemesis/internal/ffmpeg"
	"github.com/rainmanjam/polyemesis/internal/jobs"
)

// Registry is the part of *jobs.Queue this package needs. An interface so the
// processor can be registered against a fake in tests, and so this package does
// not depend on the queue's construction.
type Registry interface {
	Register(kind jobs.Kind, limit int, w jobs.Worker) error
}

var _ Registry = (*jobs.Queue)(nil)

// Per-kind concurrency limits.
//
// One each. These are all FFmpeg runs that saturate whatever cores they are
// given, and the point of the queue is that heavy work yields to the live
// stream — two proxies at once is not twice the throughput, it is twice the
// contention with the thing that must not stutter. The global concurrency limit
// bounds the total across kinds.
const (
	ProxyLimit      = 1
	ThumbnailLimit  = 1
	ArchiveLimit    = 1
	defaultFileMode = 0o644
	defaultDirMode  = 0o755
)

// Config is what the processor needs from the rest of the server.
type Config struct {
	// FFmpeg and FFprobe are the detected binaries. Empty means detection never
	// ran or failed, which fails the jobs with a clear message rather than
	// preventing startup — every one of these features is optional.
	FFmpeg  string
	FFprobe string
	// RecordingsDir is the directory the recorder writes segments into.
	RecordingsDir string

	// ArchiveEnabled is the first of the two switches on the destructive path.
	// Off by default, and off means an archive job fails immediately with an
	// explanation rather than sitting in the queue looking like it might run.
	ArchiveEnabled bool
	// ArchiveMinAge is how old a recording must be before it may be
	// re-encoded. Zero means DefaultArchiveMinAge.
	ArchiveMinAge time.Duration
	// ArchiveAllowReplace gates the very last step: renaming the verified copy
	// over the original. Even with it on, a job still has to ask.
	ArchiveAllowReplace bool

	// HasEncoder reports whether the FFmpeg build registers an encoder,
	// normally ffmpeg.Tools.HasEncoder. It only ever narrows the archive's
	// choice: software is preferred regardless of what hardware probed
	// successfully (see archiveEncoders), so this exists for the minimal build
	// that ships without libx265 at all. Nil means "we could not tell", which
	// keeps the software default rather than guessing at a GPU.
	HasEncoder func(string) bool
}

// Normalized fills the defaults.
func (c Config) Normalized() Config {
	if c.ArchiveMinAge <= 0 {
		c.ArchiveMinAge = DefaultArchiveMinAge
	}
	if c.ArchiveMinAge < MinArchiveMinAge {
		c.ArchiveMinAge = MinArchiveMinAge
	}
	return c
}

// Prober measures a file. A field on Processor for the same reason Execer is:
// the archive worker's refusal paths are the most important code in this
// package and they must be testable on a machine with no media on it.
type Prober func(ctx context.Context, path string) (FileSummary, error)

// Processor runs the three media jobs.
type Processor struct {
	log   *slog.Logger
	cfg   Config
	exec  Execer
	probe Prober
	now   func() time.Time
}

// Option customises a Processor, chiefly for tests.
type Option func(*Processor)

// WithExecer replaces the subprocess runner, so the workers can be exercised
// without FFmpeg on the machine.
func WithExecer(e Execer) Option {
	return func(p *Processor) { p.exec = e }
}

// WithProber replaces the file measurement.
func WithProber(pr Prober) Option {
	return func(p *Processor) { p.probe = pr }
}

// WithClock replaces the clock, so the archive age gate is testable.
func WithClock(fn func() time.Time) Option {
	return func(p *Processor) { p.now = fn }
}

// New builds a Processor.
func New(log *slog.Logger, cfg Config, opts ...Option) *Processor {
	p := &Processor{log: log, cfg: cfg.Normalized(), exec: Exec, now: time.Now}
	p.probe = p.summarize
	for _, opt := range opts {
		opt(p)
	}
	return p
}

// Config is the processor's resolved configuration.
func (p *Processor) Config() Config { return p.cfg }

// SetConfig replaces the configuration, so a settings change reaches the
// workers without a restart. Only the next job sees it; one already running
// keeps the policy it started under, which is what makes a mid-encode settings
// change safe on the destructive path.
func (p *Processor) SetConfig(cfg Config) { p.cfg = cfg.Normalized() }

// Register wires the three workers into a queue.
func (p *Processor) Register(r Registry) error {
	if err := r.Register(KindProxy, ProxyLimit, jobs.WorkerFunc(p.RunProxy)); err != nil {
		return err
	}
	if err := r.Register(KindThumbnails, ThumbnailLimit, jobs.WorkerFunc(p.RunThumbnails)); err != nil {
		return err
	}
	return r.Register(KindArchive, ArchiveLimit, jobs.WorkerFunc(p.RunArchive))
}

// ------------------------------------------------------------------ job params

// ProxyParams is a proxy job's payload.
type ProxyParams struct {
	// Recording is the index filename of the master, not a path.
	Recording string `json:"recording"`
	// DurationMS is the master's length, used only to draw a progress bar. A
	// zero is fine: the bar then sits at zero until the job finishes.
	DurationMS int64 `json:"durationMs,omitempty"`

	// AudioTrack is which ingest track the proxy carries; nil means track 0.
	// A pointer because 0 is a meaningful value and -1 (silent) is another, so
	// the zero value cannot also mean "unset".
	AudioTrack *int `json:"audioTrack,omitempty"`
	Height     int  `json:"height,omitempty"`
	Width      int  `json:"width,omitempty"`
	CRF        int  `json:"crf,omitempty"`
	VideoKbps  int  `json:"videoKbps,omitempty"`
}

// Validate rejects params no worker could act on.
func (p ProxyParams) Validate() error {
	if !ValidRecordingName(p.Recording) {
		return fmt.Errorf("invalid recording name %q", p.Recording)
	}
	return nil
}

// ThumbnailParams is a thumbnail job's payload.
//
// The three Skip fields are negative on purpose: the zero value asks for
// everything, so a caller that submits an empty payload gets the full set
// rather than silently nothing.
type ThumbnailParams struct {
	Recording  string `json:"recording"`
	DurationMS int64  `json:"durationMs,omitempty"`

	SkipPoster       bool `json:"skipPoster,omitempty"`
	SkipContactSheet bool `json:"skipContactSheet,omitempty"`
	SkipSprites      bool `json:"skipSprites,omitempty"`

	PosterAtSeconds float64 `json:"posterAtSeconds,omitempty"`
	SpriteInterval  float64 `json:"spriteIntervalSeconds,omitempty"`
}

// Validate rejects params no worker could act on.
func (p ThumbnailParams) Validate() error {
	if !ValidRecordingName(p.Recording) {
		return fmt.Errorf("invalid recording name %q", p.Recording)
	}
	if p.SkipPoster && p.SkipContactSheet && p.SkipSprites {
		return errors.New("the job asks for no thumbnails at all")
	}
	return nil
}

// ArchiveParams is an archive job's payload.
type ArchiveParams struct {
	Recording  string `json:"recording"`
	DurationMS int64  `json:"durationMs,omitempty"`
	// RecordedAtUnix is when the recording was made. It is REQUIRED: the age
	// gate is the first line of defence and a job that does not say how old its
	// recording is cannot pass it.
	RecordedAtUnix int64 `json:"recordedAtUnix"`

	Codec   ArchiveCodec `json:"codec,omitempty"`
	Encoder string       `json:"encoder,omitempty"`
	Quality int          `json:"quality,omitempty"`
	Preset  string       `json:"preset,omitempty"`

	// AcknowledgeLossy is the second switch. The settings-level ArchiveEnabled
	// says the feature is available; this says the human who pressed the button
	// was told the re-encode is lossy and the original may be replaced.
	AcknowledgeLossy bool `json:"acknowledgeLossy"`
	// ReplaceOriginal asks for the verified copy to be renamed over the master.
	// Without it the copy is left beside the recording for inspection and
	// nothing is destroyed.
	ReplaceOriginal bool `json:"replaceOriginal,omitempty"`
}

// Validate rejects params no worker could act on. It does not check the age or
// the acknowledgement — those are runtime policy and belong in the worker,
// where the refusal is recorded on the job for an operator to read.
func (p ArchiveParams) Validate() error {
	if !ValidRecordingName(p.Recording) {
		return fmt.Errorf("invalid recording name %q", p.Recording)
	}
	if p.Codec != "" && p.Codec != ArchiveHEVC && p.Codec != ArchiveAV1 {
		return fmt.Errorf("unknown archive codec %q", p.Codec)
	}
	// poka-yoke: refuses a quality number that would encode the master into mush
	// and, when ReplaceOriginal is set, rename the mush over it.
	//
	// The name and the codec family were bounded here from the start and the
	// quality was not, which left the one knob on this payload that VerifyArchive
	// cannot check afterwards as the one knob with no bound. A CRF in the high
	// thirties encodes cleanly, keeps every audio track, decodes without an
	// error and shrinks enormously, so the verifier passes it and the rename
	// goes ahead. See archive.go's quality-bounds block for where the two
	// ceilings come from and why replacing the master gets the tighter one.
	//
	// This runs in two places and the message has to work in both. On
	// submission, NewArchiveJob returns it and the API answers 400 with this
	// text. On a job QUEUED BEFORE this bound existed, RunArchive revalidates
	// and returns jobs.Permanent, so the job goes straight to Failed with this
	// text on the row — no retries, no encode, nothing deleted. An operator
	// meeting that after an upgrade has to be able to read the row and know
	// what changed and what to do about it, which is why the refusal names the
	// ceiling, the direction of the scale, the remedy, and the fact that
	// nothing has happened to their footage.
	if p.Quality != 0 {
		limit := MaxArchiveQuality(p.Codec, p.Encoder)
		act := "an archive written beside the original"
		alternative := ""
		if p.ReplaceOriginal {
			limit = MaxReplaceArchiveQuality(p.Codec, p.Encoder)
			act = "an archive that replaces the original"
			alternative = fmt.Sprintf(
				", or leave the original in place and resubmit at %d or lower",
				MaxArchiveQuality(p.Codec, p.Encoder))
		}
		// The floor and the ceiling are refused separately because they are
		// different mistakes and the same sentence cannot explain both: below
		// the floor the number is not a quality at all, and telling someone who
		// sent -5 that a higher number is a worse picture points them further
		// the wrong way.
		if p.Quality < MinArchiveQuality {
			return fmt.Errorf(
				"archive quality %d is below %d, and there is no such quality: "+
					"the scale counts up from %d, where a lower number is a better "+
					"picture, and 0 already means take the codec's own default. "+
					"Resubmit at 0 for the default, or between %d and %d. "+
					"Nothing has been encoded and nothing has been deleted",
				p.Quality, MinArchiveQuality, MinArchiveQuality, MinArchiveQuality, limit)
		}
		if p.Quality > limit {
			return fmt.Errorf(
				"archive quality %d is outside %d-%d, the range %s may be encoded at "+
					"with this codec: a higher number is a worse picture, and past %d the "+
					"re-encode is no longer an archive of the master. "+
					"Resubmit at %d or lower%s. "+
					"Nothing has been encoded and nothing has been deleted — a job queued "+
					"before this limit existed fails here instead of running",
				p.Quality, MinArchiveQuality, limit, act, limit, limit, alternative)
		}
	}
	return nil
}

// ------------------------------------------------------------------- results

// ProxyResult is what a finished proxy job reports.
type ProxyResult struct {
	Path            string  `json:"path"`
	Bytes           int64   `json:"bytes"`
	DurationSeconds float64 `json:"durationSeconds,omitempty"`
	AudioTrack      int     `json:"audioTrack"`
}

// ThumbnailResult is what a finished thumbnail job reports.
type ThumbnailResult struct {
	Poster       string   `json:"poster,omitempty"`
	ContactSheet string   `json:"contactSheet,omitempty"`
	Sprites      []string `json:"sprites,omitempty"`
	SpriteVTT    string   `json:"spriteVtt,omitempty"`
	// SpriteIntervalSeconds is what the sheet actually used, which is not what
	// was asked for when a long recording made the interval widen.
	SpriteIntervalSeconds float64 `json:"spriteIntervalSeconds,omitempty"`
}

// ArchiveResult is what a finished archive job reports.
type ArchiveResult struct {
	Path         string       `json:"path"`
	Encoder      string       `json:"encoder"`
	SourceBytes  int64        `json:"sourceBytes"`
	ArchiveBytes int64        `json:"archiveBytes"`
	SavedBytes   int64        `json:"savedBytes"`
	SavedPercent float64      `json:"savedPercent"`
	Verification Verification `json:"verification"`
	// ReplacedOriginal records whether the master was actually overwritten,
	// which is the one fact anyone will come back to this row for.
	ReplacedOriginal bool `json:"replacedOriginal"`
}

// -------------------------------------------------------------- job builders

// NewProxyJob builds the queue entry for a proxy.
//
// Unique, so clicking "generate proxy" twice does not encode twice. Normal
// priority: nobody is watching it, but the timeline stays unusable until it
// lands, so it should not sit behind a bulk sweep.
func NewProxyJob(recordingID int64, p ProxyParams) (jobs.Job, error) {
	return newJob(KindProxy, recordingID, p, p.Validate(), jobs.PriorityNormal)
}

// NewThumbnailJob builds the queue entry for the thumbnail pass.
func NewThumbnailJob(recordingID int64, p ThumbnailParams) (jobs.Job, error) {
	return newJob(KindThumbnails, recordingID, p, p.Validate(), jobs.PriorityNormal)
}

// NewArchiveJob builds the queue entry for an archive re-encode.
//
// PriorityBulk: this is the definition of work nobody is waiting on, and it is
// the most expensive job in the product. It goes behind everything.
func NewArchiveJob(recordingID int64, p ArchiveParams) (jobs.Job, error) {
	return newJob(KindArchive, recordingID, p, p.Validate(), jobs.PriorityBulk)
}

func newJob(kind jobs.Kind, recordingID int64, params any, verr error, pri jobs.Priority) (jobs.Job, error) {
	if verr != nil {
		return jobs.Job{}, verr
	}
	if recordingID <= 0 {
		return jobs.Job{}, fmt.Errorf("recording id %d is not a recording", recordingID)
	}
	raw, err := json.Marshal(params)
	if err != nil {
		return jobs.Job{}, fmt.Errorf("encode %s params: %w", kind, err)
	}
	return jobs.Job{
		Kind:     kind,
		Target:   jobs.RecordingTarget(recordingID),
		Params:   raw,
		Priority: pri,
		Unique:   true,
	}.Normalized(), nil
}

// --------------------------------------------------------------- proxy worker

// RunProxy generates the scrubbing proxy for one recording.
func (p *Processor) RunProxy(ctx context.Context, job jobs.Job, rep jobs.Reporter) error {
	var params ProxyParams
	if err := decodeParams(job, &params); err != nil {
		return err
	}
	if err := params.Validate(); err != nil {
		return jobs.Permanent(err)
	}
	input, layout, err := p.prepare(params.Recording)
	if err != nil {
		return err
	}

	track := 0
	if params.AudioTrack != nil {
		track = *params.AudioTrack
	}
	spec := ProxySpec{
		Input:      input,
		Output:     layout.Proxy + PartialSuffix,
		Width:      params.Width,
		Height:     params.Height,
		CRF:        params.CRF,
		VideoKbps:  params.VideoKbps,
		AudioTrack: track,
	}

	rep.Logf("encoding a %s proxy from audio track %d", proxySize(spec), track)
	if err := p.run(ctx, rep, Command{Name: p.cfg.FFmpeg, Args: ProxyArgs(spec)},
		params.DurationMS, 0, 1); err != nil {
		p.discard(spec.Output)
		return err
	}
	if err := publish(spec.Output, layout.Proxy); err != nil {
		return err
	}

	res := ProxyResult{Path: layout.Proxy, AudioTrack: track,
		DurationSeconds: float64(params.DurationMS) / 1000}
	if info, err := os.Stat(layout.Proxy); err == nil {
		res.Bytes = info.Size()
	}
	rep.SetResult(res)
	rep.Logf("proxy written to %s", filepath.Base(layout.Proxy))
	return nil
}

// proxySize describes the proxy's dimensions for the job log, in whichever of
// the three forms the caller actually specified.
func proxySize(s ProxySpec) string {
	switch {
	case s.Width > 0 && s.Height > 0:
		return fmt.Sprintf("%dx%d", s.Width, s.Height)
	case s.Width > 0:
		return fmt.Sprintf("%dpx wide", s.Width)
	case s.Height > 0:
		return fmt.Sprintf("%dp", s.Height)
	default:
		return fmt.Sprintf("%dp", DefaultProxyHeight)
	}
}

// ----------------------------------------------------------- thumbnail worker

// RunThumbnails generates the poster, contact sheet, sprite sheets and VTT in a
// single pass. See ThumbnailArgs for why it is one pass.
func (p *Processor) RunThumbnails(ctx context.Context, job jobs.Job, rep jobs.Reporter) error {
	var params ThumbnailParams
	if err := decodeParams(job, &params); err != nil {
		return err
	}
	if err := params.Validate(); err != nil {
		return jobs.Permanent(err)
	}
	input, layout, err := p.prepare(params.Recording)
	if err != nil {
		return err
	}

	seconds := float64(params.DurationMS) / 1000
	spec := ThumbnailSpec{Input: input, DurationSeconds: seconds}
	if !params.SkipPoster {
		spec.Poster = PosterSpec{Output: layout.Poster + PartialSuffix, AtSeconds: params.PosterAtSeconds}
	}
	if !params.SkipContactSheet {
		spec.ContactSheet = ContactSheetSpec{Output: layout.ContactSheet + PartialSuffix}
	}
	if !params.SkipSprites {
		// The sprite sheets are the one output written under their final names
		// during the run: the count is not known up front, so there is nothing
		// to rename afterwards. A partially written run leaves sheets that the
		// VTT — written last, only on success — never points at.
		spec.Sprites = SpriteSpec{
			OutputPattern:   layout.SpritePattern,
			IntervalSeconds: params.SpriteInterval,
		}
	}

	args := ThumbnailArgs(spec)
	if len(args) == 0 {
		// Validate already rejects "nothing at all", so this is unreachable
		// today. Treated as done rather than as an error, because a job that
		// asked for nothing has nothing left to do.
		return nil
	}

	if err := p.run(ctx, rep, Command{Name: p.cfg.FFmpeg, Args: args}, params.DurationMS, 0, 0.95); err != nil {
		p.cleanupThumbs(spec)
		return err
	}

	res := ThumbnailResult{}
	if !params.SkipPoster {
		if err := publish(spec.Poster.Output, layout.Poster); err != nil {
			return err
		}
		res.Poster = layout.Poster
	}
	if !params.SkipContactSheet {
		if err := publish(spec.ContactSheet.Output, layout.ContactSheet); err != nil {
			return err
		}
		res.ContactSheet = layout.ContactSheet
	}
	if !params.SkipSprites {
		sprites := spec.Normalized().Sprites
		if vtt := sprites.VTT(); vtt != "" {
			if err := os.WriteFile(layout.SpriteVTT, []byte(vtt), defaultFileMode); err != nil {
				return fmt.Errorf("write sprite index: %w", err)
			}
			res.SpriteVTT = layout.SpriteVTT
			res.SpriteIntervalSeconds = sprites.Interval()
		} else {
			// No duration, so no cue list can be built. The sheets are still on
			// disk and still useful to a human; only the scrub preview is lost.
			rep.Logf("sprite sheets written but the recording's duration is unknown, " +
				"so no WebVTT index could be built")
		}
		res.Sprites = sheetsOnDisk(layout)
	}
	rep.Progress(1)
	rep.SetResult(res)
	return nil
}

func (p *Processor) cleanupThumbs(spec ThumbnailSpec) {
	if spec.Poster.Output != "" {
		p.discard(spec.Poster.Output)
	}
	if spec.ContactSheet.Output != "" {
		p.discard(spec.ContactSheet.Output)
	}
	// Guarded, not merely convenient: filepath.Dir("") is ".", so an unguarded
	// cleanup on a job that skipped sprites would sweep sprite-*.jpg out of the
	// process's working directory.
	if spec.Sprites.OutputPattern == "" {
		return
	}
	for _, name := range sheetsOnDisk(Layout{Dir: filepath.Dir(spec.Sprites.OutputPattern)}) {
		p.discard(name)
	}
}

// discard removes a file a failed job left behind. It never fails the job: the
// encode already failed, and a leftover .partial is a disk-space problem the
// next run cleans up, not a reason to lose the error the operator needs. It
// goes to the server log rather than the job log for the same reason — the job
// log should carry why the work failed, not the housekeeping.
func (p *Processor) discard(path string) {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		p.log.Warn("could not remove a partial file", "path", path, "err", err)
	}
}

// sheetsOnDisk lists the sprite sheets that actually exist, rather than the
// count the arithmetic predicted. The two can disagree — a recording whose
// duration was mis-reported produces a different number of sheets — and the
// filesystem is the one that is right.
func sheetsOnDisk(layout Layout) []string {
	entries, err := os.ReadDir(layout.Dir)
	if err != nil {
		return nil
	}
	prefix, suffix := spriteAffixes()
	var out []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, suffix) {
			continue
		}
		out = append(out, filepath.Join(layout.Dir, name))
	}
	return out
}

// spriteAffixes splits SpritePattern around its number, so the only place that
// knows what a sprite sheet is called stays the constant itself.
func spriteAffixes() (prefix, suffix string) {
	prefix, rest, ok := strings.Cut(SpritePattern, "%")
	if !ok {
		return SpritePattern, ""
	}
	if i := strings.IndexAny(rest, "dxXov"); i >= 0 {
		return prefix, rest[i+1:]
	}
	return prefix, ""
}

// ------------------------------------------------------------- archive worker

// RunArchive re-encodes an old recording smaller, verifies the result against
// the original, and only then — if asked twice — replaces it.
//
// Read archive.go's header before changing anything here. Every early return
// below is a refusal to destroy something.
func (p *Processor) RunArchive(ctx context.Context, job jobs.Job, rep jobs.Reporter) error {
	var params ArchiveParams
	if err := decodeParams(job, &params); err != nil {
		return err
	}
	if err := params.Validate(); err != nil {
		return jobs.Permanent(err)
	}
	if !p.cfg.ArchiveEnabled {
		return jobs.Permanent(errors.New(
			"archive compression is switched off. It re-encodes recordings with a lossy codec " +
				"and can replace the multitrack master, so it has to be enabled in Settings first"))
	}
	if !params.AcknowledgeLossy {
		return jobs.Permanent(errors.New(
			"this archive job was submitted without acknowledging that the re-encode is lossy"))
	}

	var recordedAt time.Time
	if params.RecordedAtUnix > 0 {
		recordedAt = time.Unix(params.RecordedAtUnix, 0)
	}
	// Deliberately Permanent rather than deferred. A too-young recording is a
	// submission mistake, and a job that quietly waits a month is a job whose
	// eventual destruction nobody remembers authorising.
	if err := CheckArchiveAge(recordedAt, p.now(), p.cfg.ArchiveMinAge); err != nil {
		return jobs.Permanent(err)
	}

	input, layout, err := p.prepare(params.Recording)
	if err != nil {
		return err
	}
	if p.cfg.FFprobe == "" {
		return jobs.Permanent(errors.New(
			"ffprobe was not detected, and without it the archive copy cannot be verified " +
				"against the original"))
	}

	// The source is measured BEFORE the encode. Measuring it afterwards would
	// mean measuring a file the same job may be about to overwrite.
	src, err := p.probe(ctx, input)
	if err != nil {
		return fmt.Errorf("probe the original: %w", err)
	}
	rep.Logf("original: %.1fs, %d audio track(s), %d bytes", src.DurationSeconds, len(src.Audio), src.Bytes)

	spec := ArchiveSpec{
		Input:   input,
		Output:  layout.Archive + PartialSuffix,
		Codec:   params.Codec,
		Encoder: params.Encoder,
		Quality: params.Quality,
		Preset:  params.Preset,
	}
	if spec.Codec == "" {
		spec.Codec = DefaultArchiveCodec
	}
	if spec.Encoder == "" {
		spec.Encoder = ArchiveEncoder(spec.Codec, p.cfg.HasEncoder)
	}

	rep.Logf("re-encoding with %s; every audio track is copied, not re-encoded", spec.Encoder)
	if err := p.run(ctx, rep, Command{Name: p.cfg.FFmpeg, Args: ArchiveArgs(spec)},
		params.DurationMS, 0, 0.75); err != nil {
		p.discard(spec.Output)
		return err
	}

	out, err := p.probe(ctx, spec.Output)
	if err != nil {
		p.discard(spec.Output)
		return fmt.Errorf("probe the archive copy: %w", err)
	}
	rep.Progress(0.78)

	rep.Logf("decoding the archive copy in full to check it")
	decodeErrs, err := p.decodeCheck(ctx, rep, spec.Output, params.DurationMS)
	if err != nil && len(decodeErrs) == 0 {
		// The decoder itself failed to run. Treated as a decode failure, not as
		// a pass: an unverifiable copy never authorises a delete.
		decodeErrs = []string{err.Error()}
	}
	rep.Progress(0.95)

	v := VerifyArchive(src, out, decodeErrs, VerifyOptions{})
	res := ArchiveResult{
		Path: layout.Archive, Encoder: spec.Encoder,
		SourceBytes: src.Bytes, ArchiveBytes: out.Bytes,
		SavedBytes: v.SavedBytes, SavedPercent: v.SavedPercent,
		Verification: v,
	}
	if !v.OK {
		p.discard(spec.Output)
		for _, r := range v.Reasons {
			rep.Logf("verification failed: %s", r)
		}
		rep.SetResult(res)
		return jobs.Permanent(fmt.Errorf(
			"the archive copy did not verify against the original, so nothing was changed: %s",
			strings.Join(v.Reasons, "; ")))
	}
	if err := publish(spec.Output, layout.Archive); err != nil {
		return err
	}
	rep.Logf("verified: %.1f%% smaller, all %d audio track(s) present", v.SavedPercent, len(out.Audio))

	if params.ReplaceOriginal {
		if !p.cfg.ArchiveAllowReplace {
			rep.Logf("the job asked to replace the original but replacement is switched off; " +
				"the archive copy has been left beside the recording")
		} else if err := replaceOriginal(layout.Archive, input); err != nil {
			// The copy survives, the original survives, and the operator is
			// told. Failing here loses nothing.
			return fmt.Errorf("replace the original with the archive copy: %w", err)
		} else {
			res.Path = input
			res.ReplacedOriginal = true
			rep.Logf("the original has been replaced by the archive copy")
		}
	}

	rep.Progress(1)
	rep.SetResult(res)
	return nil
}

// replaceOriginal renames the verified copy over the master.
//
// A rename, not a copy-then-delete: within the recordings tree it is one
// filesystem operation, so there is no window in which neither file is at the
// master's path. The extension has to match, because the recordings index keys
// off the filename and a .ts segment holding Matroska is a file nothing can
// open.
func replaceOriginal(archive, original string) error {
	if !strings.EqualFold(filepath.Ext(archive), filepath.Ext(original)) {
		return fmt.Errorf("the archive copy is %s and the original is %s; "+
			"replacing one with the other would leave a file whose name lies about its contents",
			filepath.Ext(archive), filepath.Ext(original))
	}
	return os.Rename(archive, original)
}

// decodeCheck runs the full decode pass and returns what it complained about.
func (p *Processor) decodeCheck(ctx context.Context, rep jobs.Reporter, path string, durationMS int64) ([]string, error) {
	var lines []string
	sink := Sink{
		Line: func(l string) { lines = append(lines, l) },
	}
	if durationMS > 0 {
		sink.Progress = func(pr ffmpeg.Progress) {
			rep.Progress(0.78 + 0.17*float64(pr.OutTimeMS)/float64(durationMS))
		}
	}
	err := p.exec(ctx, Command{Name: p.cfg.FFmpeg, Args: DecodeCheckArgs(path)}, sink)
	return DecodeErrors(strings.Join(lines, "\n")), err
}

// summarize probes a file into the shape the verifier compares.
func (p *Processor) summarize(ctx context.Context, path string) (FileSummary, error) {
	var size int64
	if info, err := os.Stat(path); err == nil {
		size = info.Size()
	}
	raw, stderr, err := output(ctx, Command{Name: p.cfg.FFprobe, Args: ProbeArgs(path)})
	if err != nil {
		if s := strings.TrimSpace(stderr); s != "" {
			return FileSummary{}, fmt.Errorf("ffprobe %s: %s", filepath.Base(path), s)
		}
		return FileSummary{}, fmt.Errorf("ffprobe %s: %w", filepath.Base(path), err)
	}
	return ParseSummary(raw, path, size)
}

// ------------------------------------------------------------------- plumbing

func decodeParams(job jobs.Job, into any) error {
	if len(job.Params) == 0 {
		return jobs.Permanent(fmt.Errorf("%s job %d has no parameters", job.Kind, job.ID))
	}
	if err := json.Unmarshal(job.Params, into); err != nil {
		// Params that will not parse will not parse on the next attempt either.
		return jobs.Permanent(fmt.Errorf("%s job %d has unreadable parameters: %w", job.Kind, job.ID, err))
	}
	return nil
}

// prepare resolves the master, checks it is there, and creates the derived
// directory.
func (p *Processor) prepare(recording string) (input string, layout Layout, err error) {
	if p.cfg.FFmpeg == "" {
		return "", Layout{}, jobs.Permanent(errors.New(
			"FFmpeg was not detected, so derived media cannot be generated"))
	}
	input, err = p.masterPath(recording)
	if err != nil {
		return "", Layout{}, jobs.Permanent(err)
	}
	if _, err := os.Stat(input); err != nil {
		// A recording that is not on disk is not coming back; retrying would
		// burn attempts on a file the user deleted.
		return "", Layout{}, jobs.Permanent(fmt.Errorf("recording %s: %w", recording, err))
	}
	layout = LayoutFor(p.cfg.RecordingsDir, recording)
	if err := os.MkdirAll(layout.Dir, defaultDirMode); err != nil {
		return "", Layout{}, fmt.Errorf("create %s: %w", layout.Dir, err)
	}
	return input, layout, nil
}

// masterPath resolves a recording's filename against the recordings directory,
// refusing anything that escapes it. The name comes from a job's params, which
// came from an HTTP request.
func (p *Processor) masterPath(name string) (string, error) {
	if !ValidRecordingName(name) {
		return "", fmt.Errorf("invalid recording name %q", name)
	}
	base, err := filepath.Abs(p.cfg.RecordingsDir)
	if err != nil {
		return "", err
	}
	full := filepath.Join(base, name)
	if !strings.HasPrefix(full, base+string(os.PathSeparator)) {
		return "", fmt.Errorf("recording %q escapes the recordings directory", name)
	}
	return full, nil
}

// run executes one command, mapping FFmpeg's progress onto the slice of the
// job's bar between lo and hi.
func (p *Processor) run(ctx context.Context, rep jobs.Reporter, cmd Command, durationMS int64, lo, hi float64) error {
	sink := Sink{
		Line: func(l string) { rep.Logf("%s", l) },
	}
	if durationMS > 0 {
		sink.Progress = func(pr ffmpeg.Progress) {
			rep.Progress(lo + (hi-lo)*float64(pr.OutTimeMS)/float64(durationMS))
		}
	}
	if err := p.exec(ctx, cmd, sink); err != nil {
		// Cancellation is not a failure worth retrying — the operator or the
		// governor asked for it — but it is also not this package's call, so it
		// is returned unwrapped and the queue decides.
		return err
	}
	rep.Progress(hi)
	return nil
}

// publish renames a finished .partial file into place.
//
// Nothing in this package writes to a final path directly, so a half-written
// proxy can never be served and a half-written archive can never be measured by
// a verifier. Same convention as clips.Capture.
func publish(partial, final string) error {
	if err := os.Rename(partial, final); err != nil {
		_ = os.Remove(partial)
		return fmt.Errorf("publish %s: %w", filepath.Base(final), err)
	}
	return nil
}
