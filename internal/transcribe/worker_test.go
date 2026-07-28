package transcribe

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/ffmpeg"
	"github.com/rainmanjam/polyemesis/internal/jobs"
	"github.com/rainmanjam/polyemesis/internal/routing"
)

// NOTE ON WHAT IS AND IS NOT TESTED HERE.
//
// Real transcription cannot be tested offline: it needs a multi-hundred-megabyte
// model, an FFmpeg build and minutes of CPU, and its output is a probabilistic
// function of an acoustic model. So the accuracy of a transcript is not
// exercised anywhere in this package and cannot be.
//
// What IS exercised is everything around it, which is where the bugs live: the
// command lines, the parsing of whisper's output, the subtitle formatting, the
// track selection, the failure classification, and — through the stub binaries
// below — the full job pipeline from params to written files. The stubs stand
// in for ffprobe, ffmpeg and whisper-cli; they speak the same protocols on
// stdout and stderr and write the same files, so a change that breaks the way
// this package DRIVES those tools fails here.

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// recordingReporter captures what a worker reported, so progress ordering and
// log content can be asserted.
type recordingReporter struct {
	mu       sync.Mutex
	progress []float64
	logs     []string
	result   any
}

func (r *recordingReporter) Progress(f float64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.progress = append(r.progress, jobs.ClampProgress(f))
}

func (r *recordingReporter) Logf(format string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.logs = append(r.logs, format)
	_ = args
}

func (r *recordingReporter) SetResult(v any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.result = v
}

func (r *recordingReporter) last() float64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.progress) == 0 {
		return -1
	}
	return r.progress[len(r.progress)-1]
}

// writeScript drops an executable shell script and returns its path. Windows
// has no /bin/sh, so every test that uses one skips there; the argument
// builders, which are the platform-independent half, are tested everywhere.
func writeScript(t *testing.T, dir, name, body string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("stub binaries need a POSIX shell")
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// stubProbe answers with the given number of audio tracks.
const stubProbeBody = `cat <<'EOF'
{"streams":[
  {"codec_type":"video","codec_name":"h264","width":1920,"height":1080},
  {"codec_type":"audio","codec_name":"aac","channels":2,"sample_rate":"48000","tags":{"language":"eng"}},
  {"codec_type":"audio","codec_name":"aac","channels":2,"sample_rate":"48000"}
]}
EOF
`

// stubFFmpeg pretends to extract: it writes the output file named last.
const stubFFmpegBody = `for last; do :; done
printf 'RIFF' > "$last"
exit 0
`

// stubWhisper writes the JSON whisper.cpp would write at its -of prefix, prints
// the same segments on stdout, and reports progress and a system_info line on
// stderr — the three streams the worker consumes.
const stubWhisperBody = `prefix=""
next=""
for a in "$@"; do
  if [ "$next" = "of" ]; then prefix="$a"; next=""; continue; fi
  if [ "$a" = "-of" ]; then next="of"; fi
done
echo "system_info: n_threads = 4 / 8 | AVX = 1 | METAL = 1 | CUDA = 0 |" >&2
echo "whisper_print_progress_callback: progress =  50%" >&2
echo "whisper_print_progress_callback: progress = 100%" >&2
echo "[00:00:00.000 --> 00:00:02.000]   Hello there"
echo "[00:00:02.000 --> 00:00:04.000]   General Kenobi"
cat > "$prefix.json" <<'EOF'
{"result":{"language":"en"},"transcription":[
 {"offsets":{"from":0,"to":2000},"text":" Hello there",
  "tokens":[{"text":"[_BEG_]","p":0.1},{"text":" Hello","p":0.9},{"text":" there","p":0.9}]},
 {"offsets":{"from":2000,"to":4000},"text":" General Kenobi"}]}
EOF
exit 0
`

// harness wires a Processor onto stub binaries, a fake recording and a fake
// model.
type harness struct {
	proc          *Processor
	recordingsDir string
	modelsDir     string
	whisper       *Tools
}

func newHarness(t *testing.T, whisperBody string) *harness {
	t.Helper()
	bin := t.TempDir()
	ffprobe := writeScript(t, bin, "ffprobe", stubProbeBody)
	ffmpegBin := writeScript(t, bin, "ffmpeg", stubFFmpegBody)
	whisperBin := writeScript(t, bin, "whisper-cli", whisperBody)

	recordings := t.TempDir()
	if err := os.WriteFile(filepath.Join(recordings, "rec-20240115-143000.mkv"), []byte("not really an mkv"), 0o644); err != nil {
		t.Fatal(err)
	}
	models := t.TempDir()
	body := make([]byte, minModelBytes+64)
	copy(body, ggmlMagic)
	if err := os.WriteFile(filepath.Join(models, "ggml-testmodel.bin"), body, 0o644); err != nil {
		t.Fatal(err)
	}

	w := &Tools{Binary: whisperBin, Flags: []string{"model", "file", "output-json", "output-json-full", "print-progress", "no-gpu"}}
	p := NewProcessor(testLogger(), &ffmpeg.Tools{FFmpeg: ffmpegBin, FFprobe: ffprobe}, w, recordings, models,
		WithDefaultModel("testmodel"))
	return &harness{proc: p, recordingsDir: recordings, modelsDir: models, whisper: w}
}

func job(t *testing.T, p Params) jobs.Job {
	t.Helper()
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	return jobs.Job{ID: 1, Kind: KindTranscribe, Target: jobs.RecordingTarget(7), Params: raw}
}

func TestTheJobTranscribesEachTrackSeparatelyAndWritesEveryFormat(t *testing.T) {
	h := newHarness(t, stubWhisperBody)
	rep := &recordingReporter{}

	err := h.proc.Run(context.Background(), job(t, Params{
		Recording:   "rec-20240115-143000.mkv",
		RecordingID: 7,
		Annotations: []routing.TrackAnnotation{
			{Track: 0, Role: routing.RoleMic, Label: "Host"},
			{Track: 1, Role: routing.RoleMic, Label: "Guest"},
		},
	}), rep)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if rep.last() != 1 {
		t.Errorf("final progress = %v, want 1", rep.last())
	}

	res, ok := rep.result.(Result)
	if !ok {
		t.Fatalf("result = %T, want a Result", rep.result)
	}
	if len(res.Tracks) != 2 {
		t.Fatalf("transcribed %d tracks, want both mics separately", len(res.Tracks))
	}
	if res.Tracks[0].Speaker != "Host" || res.Tracks[1].Speaker != "Guest" {
		t.Errorf("speakers = %q / %q", res.Tracks[0].Speaker, res.Tracks[1].Speaker)
	}
	if res.Tracks[0].Language != "en" {
		t.Errorf("language = %q, want the language whisper reported", res.Tracks[0].Language)
	}
	if res.Tracks[0].Words != 4 {
		t.Errorf("words = %d, want 4", res.Tracks[0].Words)
	}

	dir := h.proc.TranscriptsDir()
	want := []string{
		"rec-20240115-143000-host.srt",
		"rec-20240115-143000-guest.srt",
		"rec-20240115-143000-all.srt",
		"rec-20240115-143000-host.vtt",
		"rec-20240115-143000-guest.vtt",
		"rec-20240115-143000-all.vtt",
		"rec-20240115-143000.json",
	}
	for _, name := range want {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("missing output %s: %v", name, err)
		}
	}
	if len(res.Files) != len(want) {
		t.Errorf("result lists %d files, want %d: %v", len(res.Files), len(want), res.Files)
	}

	// The merged file is the free-diarization output, so it must carry speaker
	// prefixes; the per-track files must not, or every line reads the same.
	all, err := os.ReadFile(filepath.Join(dir, "rec-20240115-143000-all.srt"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(all), "Host: Hello there") || !strings.Contains(string(all), "Guest: Hello there") {
		t.Errorf("merged SRT is missing speaker attribution:\n%s", all)
	}
	one, err := os.ReadFile(filepath.Join(dir, "rec-20240115-143000-host.srt"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(one), "Host:") {
		t.Errorf("a single-track SRT should not prefix every line with the same speaker:\n%s", one)
	}
	// And the separators are still right after a full round trip.
	if !strings.Contains(string(one), "00:00:00,000 --> 00:00:02,000") {
		t.Errorf("SRT timings wrong after the full pipeline:\n%s", one)
	}
	vtt, err := os.ReadFile(filepath.Join(dir, "rec-20240115-143000-host.vtt"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(vtt), "WEBVTT") || !strings.Contains(string(vtt), "00:00:00.000 --> 00:00:02.000") {
		t.Errorf("VTT wrong after the full pipeline:\n%s", vtt)
	}
}

func TestTheJobLearnsTheBuildsBackendsFromARunItWasGoingToDoAnyway(t *testing.T) {
	h := newHarness(t, stubWhisperBody)
	if h.whisper.Probed {
		t.Fatal("backends should not be known before a run")
	}
	if err := h.proc.Run(context.Background(), job(t, Params{
		Recording: "rec-20240115-143000.mkv", Tracks: []int{0},
	}), &recordingReporter{}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !h.whisper.Probed || h.whisper.BestBackend() != BackendMetal {
		t.Errorf("backends = %v (probed %v), want metal read from the run's system_info line",
			h.whisper.Backends, h.whisper.Probed)
	}
}

func TestTheStructuredTranscriptCarriesTrackIdentityAndConfidence(t *testing.T) {
	h := newHarness(t, stubWhisperBody)
	if err := h.proc.Run(context.Background(), job(t, Params{
		Recording:   "rec-20240115-143000.mkv",
		Annotations: []routing.TrackAnnotation{{Track: 1, Role: routing.RoleMic, Label: "Guest"}},
	}), &recordingReporter{}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(h.proc.TranscriptsDir(), "rec-20240115-143000.json"))
	if err != nil {
		t.Fatal(err)
	}
	var tr Transcript
	if err := json.Unmarshal(raw, &tr); err != nil {
		t.Fatalf("the transcript we wrote does not round-trip: %v", err)
	}
	if len(tr.Tracks) != 1 || tr.Tracks[0].Track != 1 {
		t.Fatalf("tracks = %+v, want only the mic track", tr.Tracks)
	}
	if tr.Tracks[0].Role != "mic" || tr.Tracks[0].Speaker != "Guest" {
		t.Errorf("track identity lost: %+v", tr.Tracks[0])
	}
	seg := tr.Tracks[0].Segments[0]
	if seg.Track != 1 || seg.Speaker != "Guest" {
		t.Errorf("segment lost its speaker: %+v", seg)
	}
	if !seg.ConfidenceKnown || seg.Confidence < 0.85 {
		t.Errorf("confidence = %v (known %v), want the token probabilities to survive",
			seg.Confidence, seg.ConfidenceKnown)
	}
	if tr.Model != "testmodel" {
		t.Errorf("model = %q, want the model that was actually run recorded", tr.Model)
	}
}

// A forty-minute run that produced segments must not be thrown away because the
// JSON file could not be read. The stdout lines carry the same segments.
func TestAMissingJSONFileFallsBackToTheStreamedStdoutSegments(t *testing.T) {
	noJSON := `echo "[00:00:00.000 --> 00:00:01.500]   Only on stdout"
exit 0
`
	h := newHarness(t, noJSON)
	rep := &recordingReporter{}
	if err := h.proc.Run(context.Background(), job(t, Params{
		Recording: "rec-20240115-143000.mkv", Tracks: []int{0},
	}), rep); err != nil {
		t.Fatalf("Run: %v", err)
	}
	res := rep.result.(Result)
	if len(res.Tracks) != 1 || res.Tracks[0].Segments != 1 {
		t.Fatalf("result = %+v, want the stdout segment kept", res)
	}
}

func TestFailuresARetryCannotFixAreMarkedPermanent(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*harness)
		params  Params
		wantSub string
	}{
		{
			name:    "whisper is not installed",
			mutate:  func(h *harness) { h.proc.whisper = nil },
			params:  Params{Recording: "rec-20240115-143000.mkv"},
			wantSub: "whisper.cpp is not installed",
		},
		{
			name:    "the recording does not exist",
			params:  Params{Recording: "nope.mkv"},
			wantSub: "does not exist",
		},
		{
			name:    "a path is not a recording filename",
			params:  Params{Recording: "../../etc/passwd"},
			wantSub: "not a recording filename",
		},
		{
			name:    "an absolute path is refused too",
			params:  Params{Recording: "/etc/passwd"},
			wantSub: "not a recording filename",
		},
		{
			name:    "no recording named at all",
			params:  Params{},
			wantSub: "no recording named",
		},
		{
			name:    "the model has not been downloaded",
			params:  Params{Recording: "rec-20240115-143000.mkv", Model: "large-v3"},
			wantSub: "not downloaded",
		},
		{
			name: "the model on disk is corrupt, which would otherwise produce fluent nonsense",
			mutate: func(h *harness) {
				os.WriteFile(filepath.Join(h.modelsDir, "ggml-testmodel.bin"), []byte("<html>"), 0o644)
			},
			params:  Params{Recording: "rec-20240115-143000.mkv"},
			wantSub: "ggml",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, stubWhisperBody)
			if tc.mutate != nil {
				tc.mutate(h)
			}
			err := h.proc.Run(context.Background(), job(t, tc.params), &recordingReporter{})
			if err == nil {
				t.Fatal("expected a failure")
			}
			if !jobs.IsPermanent(err) {
				t.Errorf("err = %v, want it marked Permanent: retrying will not help", err)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("err = %v, want it to mention %q", err, tc.wantSub)
			}
		})
	}
}

func TestMalformedParamsAreNotRetried(t *testing.T) {
	h := newHarness(t, stubWhisperBody)
	j := jobs.Job{ID: 1, Kind: KindTranscribe, Params: json.RawMessage(`{"recording":`)}
	err := h.proc.Run(context.Background(), j, &recordingReporter{})
	if err == nil || !jobs.IsPermanent(err) {
		t.Fatalf("err = %v, want a permanent failure: params do not become valid on a retry", err)
	}
}

// A whisper that exits non-zero for a transient reason — a busy GPU, a
// transient OOM — is NOT permanent. Anything unclassified is retried, which is
// the queue's default and the right one.
func TestATransientWhisperFailureStaysRetryable(t *testing.T) {
	failing := `echo "error: failed to initialise GPU context" >&2
exit 1
`
	h := newHarness(t, failing)
	rep := &recordingReporter{}
	err := h.proc.Run(context.Background(), job(t, Params{
		Recording: "rec-20240115-143000.mkv", Tracks: []int{0},
	}), rep)
	if err == nil {
		t.Fatal("expected a failure")
	}
	if jobs.IsPermanent(err) {
		t.Errorf("err = %v, want it left retryable", err)
	}
	if !strings.Contains(err.Error(), "GPU context") {
		t.Errorf("err = %v, want whisper's own message carried through", err)
	}
}

// The queue's first rule: return promptly on cancellation, and kill the child
// before you do. A cancelled job that leaks a whisper is still competing with
// the encoder.
func TestCancellationReturnsPromptlyAndKillsTheChild(t *testing.T) {
	slow := `echo "whisper_print_progress_callback: progress =  10%" >&2
sleep 30
exit 0
`
	h := newHarness(t, slow)
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		done <- h.proc.Run(ctx, job(t, Params{Recording: "rec-20240115-143000.mkv", Tracks: []int{0}}), &recordingReporter{})
	}()
	time.Sleep(200 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want context.Canceled", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return within ten seconds of cancellation")
	}
}

func TestTheScratchWAVsAreRemovedEvenWhenTheJobFails(t *testing.T) {
	before, _ := filepath.Glob(filepath.Join(os.TempDir(), "polyemesis-transcribe-*"))
	h := newHarness(t, "exit 1\n")
	_ = h.proc.Run(context.Background(), job(t, Params{
		Recording: "rec-20240115-143000.mkv", Tracks: []int{0},
	}), &recordingReporter{})
	after, _ := filepath.Glob(filepath.Join(os.TempDir(), "polyemesis-transcribe-*"))
	if len(after) > len(before) {
		t.Errorf("a failed job left %d scratch directories behind", len(after)-len(before))
	}
}

func TestProgressAdvancesMonotonicallyAcrossTracks(t *testing.T) {
	h := newHarness(t, stubWhisperBody)
	rep := &recordingReporter{}
	if err := h.proc.Run(context.Background(), job(t, Params{
		Recording: "rec-20240115-143000.mkv", Tracks: []int{0, 1},
	}), rep); err != nil {
		t.Fatalf("Run: %v", err)
	}
	rep.mu.Lock()
	defer rep.mu.Unlock()
	prev := -1.0
	for i, f := range rep.progress {
		if f < prev {
			t.Fatalf("progress went backwards at %d: %v after %v (%v)", i, f, prev, rep.progress)
		}
		if f < 0 || f > 1 {
			t.Fatalf("progress %v is outside 0..1", f)
		}
		prev = f
	}
	// Track one must not report the whole bar: the split across tracks is what
	// makes a two-hour, six-track job legible.
	if len(rep.progress) < 3 {
		t.Fatalf("only %d progress reports for two tracks", len(rep.progress))
	}
}

func TestTheNiceWrapperIsAppliedToEveryChild(t *testing.T) {
	h := newHarness(t, stubWhisperBody)
	var mu sync.Mutex
	var wrapped []string
	h.proc.nice = func(name string, args []string) (string, []string) {
		mu.Lock()
		wrapped = append(wrapped, filepath.Base(name))
		mu.Unlock()
		return name, args
	}
	if err := h.proc.Run(context.Background(), job(t, Params{
		Recording: "rec-20240115-143000.mkv", Tracks: []int{0},
	}), &recordingReporter{}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(wrapped) != 2 {
		t.Fatalf("wrapped %v, want both the extraction and the transcription niced", wrapped)
	}
}

func TestAvailableFollowsTheDetectedBinary(t *testing.T) {
	var nilProc *Processor
	if nilProc.Available() {
		t.Error("a nil Processor reported itself as available")
	}
	p := NewProcessor(testLogger(), nil, nil, t.TempDir(), t.TempDir())
	if p.Available() {
		t.Error("a Processor with no whisper reported itself as available")
	}
	// Constructing it must still work: registration happens at startup, before
	// anyone knows whether whisper is installed, and it must not be conditional.
	if p.TranscriptsDir() == "" {
		t.Error("TranscriptsDir is empty")
	}
	if p.defaultModel == "" {
		t.Error("no default model was chosen from the hardware hint")
	}
}

func TestFileSafeNamesSurviveOperatorFreeText(t *testing.T) {
	tests := []struct {
		name, in string
		track    int
		want     string
	}{
		{name: "an ordinary label", in: "Host", want: "host"},
		{name: "spaces and punctuation collapse", in: "Guest mic (Zoom)", want: "guest-mic-zoom"},
		{name: "a path separator cannot escape the directory", in: "../../etc/passwd", track: 0, want: "etc-passwd"},
		{name: "a leading dot cannot hide the file", in: ".hidden", want: "hidden"},
		{name: "punctuation alone falls back to the track number", in: "!!!", track: 2, want: "track3"},
		{name: "a numeric label reads as a track number it is not", in: "12", track: 4, want: "track5"},
		{name: "emoji are dropped", in: "🎤 mic", want: "mic"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := fileSafe(tc.in, tc.track); got != tc.want {
				t.Errorf("fileSafe(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestKindIsStableAndTargetsUseTheCanonicalSpelling(t *testing.T) {
	if KindTranscribe != "transcribe" {
		t.Errorf("KindTranscribe = %q: changing it orphans every queued job", KindTranscribe)
	}
	if len(string(KindTranscribe)) > jobs.MaxKindLen {
		t.Error("the kind is longer than the queue stores")
	}
	// Two spellings of a target silently match nothing, which is why the queue
	// exports the constructor.
	if got := jobs.RecordingTarget(7); got != "recording:7" {
		t.Errorf("RecordingTarget = %q", got)
	}
}

// The processor must satisfy the queue's interface, checked at compile time so
// a signature drift is a build failure rather than a runtime registration
// error.
var _ jobs.Worker = (*Processor)(nil)
