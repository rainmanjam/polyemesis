package transcribe

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/rainmanjam/polyemesis/internal/ffmpeg"
	"github.com/rainmanjam/polyemesis/internal/jobs"
	"github.com/rainmanjam/polyemesis/internal/routing"
)

// The queue processor.
//
// One job transcribes one recording. Inside it, each selected track is
// extracted to a 16 kHz mono WAV and transcribed on its own, and the results
// are written as a structured transcript plus subtitle files.
//
// Two rules from the queue are load-bearing here and both are about the live
// stream. Run returns promptly when the context is done AND kills its child
// before it does — a cancelled job that leaks a whisper process is a job that
// is still competing with the encoder. And a failure that a retry cannot fix
// comes back as jobs.Permanent: retrying "whisper is not installed" three times
// costs nothing but tells the operator nothing either.

// KindTranscribe is this processor's job kind.
const KindTranscribe jobs.Kind = "transcribe"

// TranscriptsSubdir is where output lands, a child of the recordings directory.
// The same reasoning as recording.StemsSubdir: the recordings scanner skips
// directories, so a six-track session cannot turn the recordings page into
// thirty rows of subtitle files, and the whole lot is one os.RemoveAll away.
const TranscriptsSubdir = "transcripts"

// childKillDelay is how long a child gets to die after its context is
// cancelled before its pipes are abandoned.
//
// exec.CommandContext kills the process, but Wait still blocks on the output
// pipes, and whisper.cpp's worker threads can hold them open. WaitDelay bounds
// that: after it, Wait returns and the job releases its slot. Yielding the
// machine back to the stream is more urgent than a tidy exit status.
const childKillDelay = 5 * time.Second

// Params is the JSON a caller attaches to a transcribe job.
type Params struct {
	// Recording is the segment filename, e.g. "rec-20240115-143000.mkv". It is
	// resolved inside the recordings directory; a path is refused.
	Recording string `json:"recording"`
	// RecordingID is carried through to the result so a UI can link back.
	RecordingID int64 `json:"recordingId,omitempty"`
	// Tracks are the 0-based audio tracks to transcribe. Empty means let
	// DefaultTracks choose, which is what the UI's "Transcribe" button sends.
	Tracks []int `json:"tracks,omitempty"`
	// Annotations are the operator's track roles and labels at the time the job
	// was submitted. They are copied into the job rather than read live because
	// a transcript is a record of a session, and re-running it a week later
	// after the roles were rearranged must not relabel the speakers.
	Annotations []routing.TrackAnnotation `json:"annotations,omitempty"`

	Model    string  `json:"model,omitempty"`
	Backend  Backend `json:"backend,omitempty"`
	Language string  `json:"language,omitempty"`
	// Translate asks for English output regardless of the source language.
	Translate bool `json:"translate,omitempty"`
	// Threads caps whisper's parallelism. Zero lets whisper decide, and the
	// governor's nice level is what actually keeps it out of the encoder's way.
	Threads int `json:"threads,omitempty"`
	// Formats to emit. Empty means SRT, WebVTT and the structured JSON.
	Formats []SubtitleFormat `json:"formats,omitempty"`
	// DurationMS, when known, makes the extraction phase's progress smooth. It
	// is optional: whisper reports its own percentages, which is the phase that
	// takes the time.
	DurationMS int64 `json:"durationMs,omitempty"`
}

// Result is what the job hands back, and what a UI renders without re-reading
// the transcript files.
type Result struct {
	Recording   string        `json:"recording"`
	RecordingID int64         `json:"recordingId,omitempty"`
	Model       string        `json:"model"`
	Backend     Backend       `json:"backend,omitempty"`
	Tracks      []TrackResult `json:"tracks"`
	// Files are the emitted paths, relative to the recordings directory so the
	// result survives the data directory moving.
	Files []string `json:"files"`
	// DurationMS is how long the job took, which is the number an operator uses
	// to decide whether a bigger model is affordable.
	DurationMS int64 `json:"durationMs"`
}

// TrackResult summarises one transcribed track.
type TrackResult struct {
	Track    int    `json:"track"`
	Speaker  string `json:"speaker"`
	Language string `json:"language,omitempty"`
	Segments int    `json:"segments"`
	Words    int    `json:"words"`
}

// Processor runs transcribe jobs. It implements jobs.Worker.
type Processor struct {
	log     *slog.Logger
	tools   *ffmpeg.Tools
	whisper *Tools

	recordingsDir string
	modelsDir     string

	// nice wraps a command so it runs at the governor's priority. Nil means run
	// it unwrapped; see jobs.Governor.NiceCommand.
	nice func(name string, args []string) (string, []string)

	defaultModel string
	now          func() time.Time
}

// Option configures a Processor.
type Option func(*Processor)

// WithNice hands the processor the governor's command wrapper, so every child
// starts niced and on idle IO. This is how the workstream's governing principle
// reaches the actual processes: the policy is useless if the transcoder runs at
// the same priority as the encoder.
func WithNice(fn func(name string, args []string) (string, []string)) Option {
	return func(p *Processor) { p.nice = fn }
}

// WithClock replaces time.Now, for tests.
func WithClock(fn func() time.Time) Option {
	return func(p *Processor) {
		if fn != nil {
			p.now = fn
		}
	}
}

// WithDefaultModel sets the model used when a job does not name one. Empty
// leaves the hardware-derived default in place.
func WithDefaultModel(name string) Option {
	return func(p *Processor) { p.defaultModel = name }
}

// NewProcessor builds the worker.
//
// whisper may be nil or unavailable — that is the normal state of an install
// without whisper.cpp, and it must not stop the processor being constructed and
// registered. Jobs then fail with a message naming the fix, which is a far
// better experience than a "transcribe" button that does not exist and no
// explanation of why.
func NewProcessor(log *slog.Logger, tools *ffmpeg.Tools, whisper *Tools, recordingsDir, modelsDir string, opts ...Option) *Processor {
	p := &Processor{
		log:           log,
		tools:         tools,
		whisper:       whisper,
		recordingsDir: recordingsDir,
		modelsDir:     modelsDir,
		now:           time.Now,
	}
	for _, o := range opts {
		o(p)
	}
	if p.defaultModel == "" {
		p.defaultModel = DefaultModel(HintFromTools(tools)).Name
	}
	return p
}

// Available reports whether this processor can actually do anything, which is
// what a UI asks before offering the button.
func (p *Processor) Available() bool { return p != nil && p.whisper.Available() }

// TranscriptsDir is where output is written.
func (p *Processor) TranscriptsDir() string { return filepath.Join(p.recordingsDir, TranscriptsSubdir) }

// Run implements jobs.Worker.
func (p *Processor) Run(ctx context.Context, job jobs.Job, rep jobs.Reporter) error {
	started := p.now()

	params, input, modelPath, err := p.prepare(job)
	if err != nil {
		return err
	}

	plan, err := p.plan(ctx, input, params)
	if err != nil {
		return err
	}
	if len(plan) == 0 {
		return jobs.Permanent(errors.New("this recording has no audio tracks to transcribe"))
	}
	rep.Logf("transcribing %d track(s) of %s with model %s", len(plan), filepath.Base(input), ModelNameFromFile(modelPath))

	scratch, err := os.MkdirTemp("", "polyemesis-transcribe-")
	if err != nil {
		return fmt.Errorf("create scratch directory: %w", err)
	}
	// The WAVs are large and worthless once transcribed. Removing them on every
	// exit path, including cancellation, keeps a cancelled job from leaving a
	// gigabyte behind on a volume the recorder is also writing to.
	defer os.RemoveAll(scratch)

	transcript := Transcript{
		Recording:   filepath.Base(input),
		RecordingID: params.RecordingID,
		CreatedAt:   started,
		Model:       ModelNameFromFile(modelPath),
		Backend:     params.Backend,
	}

	for i, choice := range plan {
		if err := ctx.Err(); err != nil {
			return err
		}
		base := float64(i) / float64(len(plan))
		span := 1 / float64(len(plan))
		tt, err := p.transcribeTrack(ctx, trackRun{
			input:     input,
			scratch:   scratch,
			modelPath: modelPath,
			choice:    choice,
			params:    params,
		}, rep, func(f float64) { rep.Progress(base + span*jobs.ClampProgress(f)) })
		if err != nil {
			return err
		}
		transcript.Tracks = append(transcript.Tracks, tt)
		rep.Logf("track %d (%s): %d segments", choice.Track, choice.Speaker, len(tt.Segments))
	}

	files, err := p.write(transcript, params.Formats)
	if err != nil {
		return fmt.Errorf("write transcript: %w", err)
	}
	rep.Progress(1)

	res := Result{
		Recording:   transcript.Recording,
		RecordingID: params.RecordingID,
		Model:       transcript.Model,
		Backend:     transcript.Backend,
		Files:       files,
		DurationMS:  p.now().Sub(started).Milliseconds(),
	}
	for _, tt := range transcript.Tracks {
		res.Tracks = append(res.Tracks, TrackResult{
			Track:    tt.Track,
			Speaker:  tt.Speaker,
			Language: tt.Language,
			Segments: len(tt.Segments),
			Words:    countWords(tt),
		})
	}
	rep.SetResult(res)
	return nil
}

// prepare validates everything that can be decided before any process is
// spawned.
//
// Every failure it can produce is Permanent, which is the point of grouping
// them: none of "params will not parse", "whisper is not installed", "that file
// is not in the recordings directory" and "the model on disk is corrupt" gets
// better by waiting fifteen seconds and trying again, and a job that retries
// three times before showing the operator a message they could have acted on
// immediately is a worse product.
func (p *Processor) prepare(job jobs.Job) (Params, string, string, error) {
	var params Params
	if len(job.Params) > 0 {
		if err := json.Unmarshal(job.Params, &params); err != nil {
			return params, "", "", jobs.Permanent(fmt.Errorf("bad transcribe params: %w", err))
		}
	}
	if !p.whisper.Available() {
		return params, "", "", jobs.Permanent(errors.New(p.whisper.Unavailable()))
	}
	if p.tools == nil || p.tools.FFmpeg == "" {
		return params, "", "", jobs.Permanent(errors.New("no FFmpeg available to extract audio with"))
	}

	input, err := p.resolveRecording(params.Recording)
	if err != nil {
		return params, "", "", jobs.Permanent(err)
	}

	modelName := params.Model
	if modelName == "" {
		modelName = p.defaultModel
	}
	modelPath, err := ResolveModel(p.modelsDir, modelName)
	if err != nil {
		return params, "", "", jobs.Permanent(fmt.Errorf("%w. Download it from Settings first", err))
	}
	// A corrupt model produces fluent nonsense rather than an error, so this is
	// the one place it can be caught. An uncatalogued model — somebody's own
	// fine-tune — is still checked for the ggml magic; only the size comparison
	// needs a catalogue entry to compare against.
	m, _ := FindModel(ModelNameFromFile(modelPath))
	if err := VerifyModelFile(modelPath, m); err != nil {
		return params, "", "", jobs.Permanent(fmt.Errorf("%w. Delete it and download it again", err))
	}
	return params, input, modelPath, nil
}

// plan decides which tracks to transcribe.
//
// A probe failure is NOT fatal: the operator's requested tracks are honoured
// verbatim, and if they asked for nothing the job would have nothing to do, so
// that case alone is an error. Refusing to transcribe because ffprobe could not
// read a file FFmpeg will happily decode is the restrictive-direction mistake.
func (p *Processor) plan(ctx context.Context, input string, params Params) ([]TrackChoice, error) {
	var src routing.Source
	probe, err := ffmpeg.Probe(ctx, p.tools.FFprobe, input, 30)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if len(params.Tracks) == 0 {
			return nil, jobs.Permanent(fmt.Errorf("probe %s: %w", filepath.Base(input), err))
		}
		p.log.Warn("transcribe: probe failed, using the requested tracks as given",
			"recording", filepath.Base(input), "err", err)
	} else {
		src = SourceFromProbe(probe)
	}
	src = src.WithAnnotations(params.Annotations)
	return PlanTracks(src, params.Tracks), nil
}

type trackRun struct {
	input     string
	scratch   string
	modelPath string
	choice    TrackChoice
	params    Params
}

// transcribeTrack extracts one track and transcribes it alone.
//
// The progress split is 15/85. Extraction is a stream copy through a resampler
// and finishes in a fraction of the time whisper takes, so giving it a big
// slice of the bar makes the bar lie.
func (p *Processor) transcribeTrack(ctx context.Context, r trackRun, rep jobs.Reporter, progress func(float64)) (TrackTranscript, error) {
	const extractShare = 0.15

	wav := filepath.Join(r.scratch, WAVName(r.input, r.choice.Track))
	extract := ExtractSpec{
		FFmpeg:     p.tools.FFmpeg,
		Input:      r.input,
		Track:      r.choice.Track,
		Output:     wav,
		Denoise:    r.choice.Denoise,
		DurationMS: r.params.DurationMS,
		Progress:   r.params.DurationMS > 0,
	}
	if err := p.extract(ctx, extract, func(f float64) { progress(f * extractShare) }); err != nil {
		return TrackTranscript{}, err
	}
	progress(extractShare)

	lang := r.params.Language
	if lang == "" {
		lang = r.choice.Language
	}
	spec := WhisperSpec{
		Model:        r.modelPath,
		Input:        wav,
		OutputPrefix: strings.TrimSuffix(wav, ".wav"),
		Language:     lang,
		Translate:    r.params.Translate,
		Threads:      r.params.Threads,
		Backend:      r.params.Backend,
		JSON:         true,
		FullJSON:     true,
		Progress:     true,
		Flags:        p.whisper,
	}
	segs, detected, err := p.whisperRun(ctx, spec, rep, func(f float64) {
		progress(extractShare + (1-extractShare)*f)
	})
	if err != nil {
		return TrackTranscript{}, err
	}
	progress(1)

	if detected == "" {
		detected = normalizeLanguage(lang)
		if detected == "auto" {
			detected = ""
		}
	}
	for i := range segs {
		segs[i].Track = r.choice.Track
		segs[i].Speaker = r.choice.Speaker
	}
	return TrackTranscript{
		Track:    r.choice.Track,
		Speaker:  r.choice.Speaker,
		Role:     string(r.choice.Role),
		Language: detected,
		Model:    ModelNameFromFile(r.modelPath),
		Segments: segs,
	}, nil
}

// extract runs FFmpeg to produce whisper's input WAV.
func (p *Processor) extract(ctx context.Context, spec ExtractSpec, progress func(float64)) error {
	name, args := p.command(spec.FFmpeg, ExtractArgs(spec))
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.WaitDelay = childKillDelay

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	var stderr strings.Builder
	cmd.Stderr = &tailWriter{w: &stderr, max: 4 << 10}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start ffmpeg: %w", err)
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		if spec.DurationMS <= 0 {
			io.Copy(io.Discard, stdout)
			return
		}
		ffmpeg.ParseProgress(stdout, func(pr ffmpeg.Progress) {
			progress(float64(pr.OutTimeMS) / float64(spec.DurationMS))
		})
	}()
	err = cmd.Wait()
	wg.Wait()
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return fmt.Errorf("extract track %d: %w: %s", spec.Track, err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// whisperRun runs whisper.cpp over one WAV and returns its segments.
//
// Both streams are read. stdout carries the segments as they are produced,
// which is the fallback when the JSON file is missing or unreadable; stderr
// carries the progress percentages, the system_info line the backend probe
// wants, and any error worth putting in the job log.
func (p *Processor) whisperRun(ctx context.Context, spec WhisperSpec, rep jobs.Reporter, progress func(float64)) ([]Segment, string, error) {
	name, args := p.command(p.whisper.Binary, WhisperArgs(spec))
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.WaitDelay = childKillDelay

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, "", err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, "", err
	}
	if err := cmd.Start(); err != nil {
		return nil, "", fmt.Errorf("start whisper: %w", err)
	}

	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		streamed []Segment
		tail     strings.Builder
	)
	wg.Add(2)
	go func() {
		defer wg.Done()
		scanLines(stdout, func(line string) {
			if seg, ok := ParseSegmentLine(line); ok {
				mu.Lock()
				streamed = append(streamed, seg)
				mu.Unlock()
			}
		})
	}()
	go func() {
		defer wg.Done()
		var info strings.Builder
		scanLines(stderr, func(line string) {
			if f, ok := ParseProgressLine(line); ok {
				progress(f)
				return
			}
			if strings.Contains(line, "system_info") {
				info.WriteString(line)
				info.WriteByte('\n')
			}
			if IsNoteworthy(line) {
				rep.Logf("whisper: %s", strings.TrimSpace(line))
				mu.Lock()
				tail.WriteString(strings.TrimSpace(line))
				tail.WriteByte('\n')
				mu.Unlock()
			}
		})
		// The build's capabilities, learned for free from a run that was going
		// to happen anyway. This is why detection does not load a model.
		if b := ParseSystemInfo(info.String()); len(b) > 0 {
			p.whisper.SetBackends(b)
		}
	}()

	err = cmd.Wait()
	wg.Wait()
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, "", ctxErr
		}
		return nil, "", fmt.Errorf("whisper failed: %w: %s", err, strings.TrimSpace(tail.String()))
	}

	var detected string
	if raw, rerr := os.ReadFile(JSONPath(spec.OutputPrefix)); rerr == nil {
		if segs, perr := ParseJSON(raw, &detected); perr == nil {
			return segs, detected, nil
		} else {
			p.log.Warn("transcribe: unreadable whisper json, falling back to stdout", "err", perr)
		}
	}
	// No JSON, or JSON we could not read. The stdout lines carry the same
	// segments without the confidences, and a transcript without confidences is
	// worth far more than a failed job after a run this long.
	mu.Lock()
	defer mu.Unlock()
	return NormalizeSegments(streamed), detected, nil
}

// command applies the governor's nice wrapper, when there is one.
func (p *Processor) command(name string, args []string) (string, []string) {
	if p.nice == nil {
		return name, args
	}
	return p.nice(name, args)
}

// write emits the transcript files and returns their paths relative to the
// recordings directory.
func (p *Processor) write(t Transcript, formats []SubtitleFormat) ([]string, error) {
	if len(formats) == 0 {
		formats = []SubtitleFormat{FormatSRT, FormatVTT, FormatJSON}
	}
	dir := p.TranscriptsDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	base := strings.TrimSuffix(t.Recording, filepath.Ext(t.Recording))

	var out []string
	emit := func(name, content string) error {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			return err
		}
		out = append(out, filepath.Join(TranscriptsSubdir, name))
		return nil
	}

	for _, f := range formats {
		if !ValidFormat(f) {
			continue
		}
		if f == FormatJSON {
			raw, err := json.MarshalIndent(t, "", "  ")
			if err != nil {
				return nil, err
			}
			if err := emit(base+FormatJSON.Ext(), string(raw)+"\n"); err != nil {
				return nil, err
			}
			continue
		}
		// One file per track, named after the speaker, so a video editor sees
		// which subtitle track is whose without opening them.
		for _, tt := range t.Tracks {
			name := fmt.Sprintf("%s-%s%s", base, fileSafe(tt.Speaker, tt.Track), f.Ext())
			if err := emit(name, render(f, tt.Segments, SubtitleOptions{})); err != nil {
				return nil, err
			}
		}
		// And one merged file with speaker prefixes, which is the transcript a
		// human reads. This is the free-diarization output.
		if len(t.Tracks) > 1 {
			name := base + "-all" + f.Ext()
			if err := emit(name, render(f, t.Merged(), SubtitleOptions{Speakers: true})); err != nil {
				return nil, err
			}
		}
	}
	return out, nil
}

func render(f SubtitleFormat, segs []Segment, opt SubtitleOptions) string {
	switch f {
	case FormatSRT:
		return SRT(segs, opt)
	case FormatVTT:
		return VTT(segs, opt)
	case FormatText:
		return PlainText(segs, opt)
	}
	return ""
}

// resolveRecording turns a filename into an absolute path inside the recordings
// directory.
//
// Job params are data, and data that names a path is a directory traversal
// waiting to happen. Only a bare filename is accepted, and the resolved path is
// re-checked against the directory afterwards so a symlink cannot smuggle the
// job somewhere else.
func (p *Processor) resolveRecording(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", errors.New("no recording named")
	}
	if name != filepath.Base(name) || strings.Contains(name, "..") {
		return "", fmt.Errorf("%q is not a recording filename", name)
	}
	path := filepath.Join(p.recordingsDir, name)
	resolved, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	dir, err := filepath.Abs(p.recordingsDir)
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(resolved, dir+string(filepath.Separator)) {
		return "", fmt.Errorf("%q is outside the recordings directory", name)
	}
	if st, err := os.Stat(resolved); err != nil || st.IsDir() {
		return "", fmt.Errorf("recording %q does not exist", name)
	}
	return resolved, nil
}

// fileSafe reduces a speaker label to a filename component, falling back to the
// track number when nothing survives. Same whitelist approach as the stem
// namer: the result reaches a path, so anything not positively known to be
// harmless is dropped.
func fileSafe(s string, track int) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	out := b.String()
	for strings.Contains(out, "--") {
		out = strings.ReplaceAll(out, "--", "-")
	}
	out = strings.Trim(out, "-")
	if out == "" || strings.Trim(out, "0123456789") == "" {
		return fmt.Sprintf("track%d", track+1)
	}
	if len(out) > 32 {
		out = strings.Trim(out[:32], "-")
	}
	return out
}

func countWords(t TrackTranscript) int {
	n := 0
	for _, s := range t.Segments {
		n += len(strings.Fields(s.Text))
	}
	return n
}

// scanLines reads a stream line by line with a buffer large enough for
// whisper's longest segment. The default bufio.Scanner buffer is 64 KiB, which
// a single long segment can exceed, and a scanner that stops early silently
// truncates a transcript.
func scanLines(r io.Reader, fn func(string)) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		fn(sc.Text())
	}
}

// tailWriter keeps the last max bytes written to it, so a chatty child cannot
// grow an error message without bound.
type tailWriter struct {
	w   *strings.Builder
	max int
}

func (t *tailWriter) Write(p []byte) (int, error) {
	t.w.Write(p)
	if t.w.Len() > t.max {
		s := t.w.String()
		t.w.Reset()
		t.w.WriteString(s[len(s)-t.max:])
	}
	return len(p), nil
}
