package clipper

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/jobs"
)

func testLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// fakeFFmpeg records every invocation and creates the file each one claims to
// write, which is what makes the multi-step precise path testable end to end
// without an encoder.
type fakeFFmpeg struct {
	calls [][]string
	fail  func(args []string) error
}

func (f *fakeFFmpeg) run(_ context.Context, _ string, args []string) ([]byte, error) {
	f.calls = append(f.calls, args)
	if f.fail != nil {
		if err := f.fail(args); err != nil {
			return nil, err
		}
	}
	out := args[len(args)-1]
	if err := os.WriteFile(out, []byte("video"), 0o644); err != nil {
		return nil, err
	}
	return nil, nil
}

func newTestCutter(t *testing.T, f *fakeFFmpeg, kf Keyframes) *Cutter {
	t.Helper()
	return New(testLog(), "ffmpeg", "ffprobe",
		WithRunner(f.run),
		WithProber(proberFunc(func(_ context.Context, _ string, _, _ time.Duration) (Keyframes, error) {
			return kf, nil
		})),
	)
}

// The clip only exists at its final path once every step has succeeded, so a
// download that arrives mid-cut can never find a half-written file.
func TestExecutePublishesTheClipOnlyOnceEveryStepSucceeded(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "clip.mkv")
	f := &fakeFFmpeg{}
	c := newTestCutter(t, f, gop(60, 2*time.Second))

	res, err := c.Cut(context.Background(), oneHourTimeline(t), req(5*time.Second, 15*time.Second,
		func(r *Request) { r.OutPath = out }), nil)
	if err != nil {
		t.Fatalf("Cut: %v", err)
	}
	if res.Path != out {
		t.Errorf("path = %s, want %s", res.Path, out)
	}
	if res.Bytes != int64(len("video")) {
		t.Errorf("bytes = %d", res.Bytes)
	}
	if res.Plan.In != 4*time.Second || res.Plan.InDrift != -time.Second {
		t.Errorf("the result does not carry the drift: in=%s drift=%s", res.Plan.In, res.Plan.InDrift)
	}
	if _, err := os.Stat(out); err != nil {
		t.Fatalf("the clip was not published: %v", err)
	}
	// The work directory, its intermediates and its concat lists are gone.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "clip.mkv" {
		t.Fatalf("the directory holds %v, want only the clip", entryNames(entries))
	}
}

func TestExecuteRunsThePreciseStepsInOrderAndWritesTheirSidecars(t *testing.T) {
	dir := t.TempDir()
	f := &fakeFFmpeg{}
	c := newTestCutter(t, f, gop(60, 2*time.Second))

	var seen []string
	_, err := c.Cut(context.Background(), oneHourTimeline(t),
		req(5*time.Second, 15*time.Second, precise, func(r *Request) {
			r.OutPath = filepath.Join(dir, "clip.mkv")
		}),
		func(_ float64, msg string) { seen = append(seen, msg) })
	if err != nil {
		t.Fatalf("Cut: %v", err)
	}

	if len(f.calls) != 3 {
		t.Fatalf("ran %d commands, want head, tail, join", len(f.calls))
	}
	if !strings.Contains(strings.Join(f.calls[0], " "), "libx264") {
		t.Errorf("the first step is not the encode: %v", f.calls[0])
	}
	if !strings.Contains(strings.Join(f.calls[2], " "), "-f concat") {
		t.Errorf("the last step is not the join: %v", f.calls[2])
	}
	if !equalStrings(seen, []string{"head", "tail", "join", "done"}) {
		t.Errorf("progress reported %v", seen)
	}
}

// A failed step must not leave a clip behind. The user retries and gets a
// clean run, rather than finding a file that plays for two seconds.
func TestExecuteLeavesNothingBehindWhenAStepFails(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "clip.mkv")
	f := &fakeFFmpeg{fail: func(args []string) error {
		if strings.Contains(strings.Join(args, " "), "-f concat") {
			return errors.New("Invalid data found when processing input")
		}
		return nil
	}}
	c := newTestCutter(t, f, gop(60, 2*time.Second))

	_, err := c.Cut(context.Background(), oneHourTimeline(t),
		req(5*time.Second, 15*time.Second, precise, func(r *Request) { r.OutPath = out }), nil)
	if err == nil {
		t.Fatal("Cut succeeded despite a failing step")
	}
	if !strings.Contains(err.Error(), "join") {
		t.Errorf("the error does not name the step that failed: %v", err)
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Errorf("a clip was published from a failed cut")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Errorf("the directory holds %v, want nothing", entryNames(entries))
	}
}

// Cancellation that leaks an FFmpeg is cancellation that still competes with
// the live stream, so a cancelled cut must stop before the next step starts.
func TestExecuteStopsBetweenStepsWhenTheContextIsCancelled(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	f := &fakeFFmpeg{fail: func([]string) error { cancel(); return nil }}
	c := newTestCutter(t, f, gop(60, 2*time.Second))

	_, err := c.Cut(ctx, oneHourTimeline(t),
		req(5*time.Second, 15*time.Second, precise, func(r *Request) {
			r.OutPath = filepath.Join(dir, "clip.mkv")
		}), nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Cut: got %v, want a cancellation", err)
	}
	if len(f.calls) != 1 {
		t.Fatalf("ran %d commands after cancellation, want 1", len(f.calls))
	}
}

// FFmpeg exiting zero without writing anything is a real failure mode, and
// renaming a file that is not there produces an error nobody can read.
func TestExecuteSaysSoWhenFFmpegSucceedsButWritesNothing(t *testing.T) {
	dir := t.TempDir()
	c := New(testLog(), "ffmpeg", "ffprobe",
		WithRunner(func(context.Context, string, []string) ([]byte, error) { return nil, nil }),
		WithProber(proberFunc(func(context.Context, string, time.Duration, time.Duration) (Keyframes, error) {
			return gop(60, 2*time.Second), nil
		})))

	_, err := c.Cut(context.Background(), oneHourTimeline(t),
		req(5*time.Second, 15*time.Second, func(r *Request) { r.OutPath = filepath.Join(dir, "clip.mkv") }), nil)
	if err == nil || !strings.Contains(err.Error(), "wrote no clip") {
		t.Fatalf("Cut: got %v, want a complaint about the missing output", err)
	}
}

func TestPlanDoesNotTouchTheFilesystem(t *testing.T) {
	dir := t.TempDir()
	f := &fakeFFmpeg{}
	c := newTestCutter(t, f, gop(60, 2*time.Second))

	if _, err := c.Plan(context.Background(), oneHourTimeline(t),
		req(5*time.Second, 15*time.Second, func(r *Request) { r.OutPath = filepath.Join(dir, "clip.mkv") })); err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(f.calls) != 0 {
		t.Errorf("planning ran FFmpeg %d times", len(f.calls))
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Errorf("planning wrote %v", entryNames(entries))
	}
}

// ------------------------------------------------------------------- the job

type fakeReporter struct {
	fraction float64
	lines    []string
	result   any
}

func (r *fakeReporter) Progress(f float64) { r.fraction = f }
func (r *fakeReporter) SetResult(v any)    { r.result = v }

func (r *fakeReporter) Logf(format string, args ...any) {
	r.lines = append(r.lines, fmt.Sprintf(format, args...))
}

func (r *fakeReporter) has(substr string) bool {
	for _, l := range r.lines {
		if strings.Contains(l, substr) {
			return true
		}
	}
	return false
}

func jobFor(t *testing.T, params JobParams) jobs.Job {
	t.Helper()
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	return jobs.Job{Kind: JobKind, Target: "recording:1", Params: raw}
}

// The drift goes into the job log before the work starts, because a job
// somebody checks on tomorrow still has to say the in-point moved.
func TestTheJobLogsTheDriftBeforeItCuts(t *testing.T) {
	dir := t.TempDir()
	f := &fakeFFmpeg{}
	c := newTestCutter(t, f, gop(60, 2*time.Second))
	w := NewWorker(c, nil)
	rep := &fakeReporter{}

	job := jobFor(t, JobParams{
		Segments: []Segment{{Path: "/rec/seg0.mkv", Duration: time.Hour}},
		Request:  req(5*time.Second, 15*time.Second, func(r *Request) { r.OutPath = filepath.Join(dir, "clip.mkv") }),
	})
	if err := w.Run(context.Background(), job, rep); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !rep.has("earlier than you asked") {
		t.Fatalf("the job log does not mention the drift: %v", rep.lines)
	}

	res, ok := rep.result.(JobResult)
	if !ok {
		t.Fatalf("result = %T, want a JobResult", rep.result)
	}
	if res.InDriftMS != -1000 || !res.DriftKnown {
		t.Errorf("result drift = %dms known=%v, want -1000ms known", res.InDriftMS, res.DriftKnown)
	}
	if res.Mode != ModeFast || res.Sources != 1 {
		t.Errorf("result = %+v", res)
	}
	if rep.fraction != 1 {
		t.Errorf("progress ended at %v, want 1", rep.fraction)
	}
}

func TestTheJobWritesAnEDLBesideTheClipWhenAsked(t *testing.T) {
	dir := t.TempDir()
	edl := filepath.Join(dir, "clip"+OTIOExt)
	f := &fakeFFmpeg{}
	c := newTestCutter(t, f, gop(60, 2*time.Second))
	rep := &fakeReporter{}

	job := jobFor(t, JobParams{
		Segments: []Segment{{Path: "/rec/seg0.mkv", Duration: time.Hour}},
		Request:  req(5*time.Second, 15*time.Second, func(r *Request) { r.OutPath = filepath.Join(dir, "clip.mkv") }),
		EDLPath:  edl,
		EDLRate:  30,
	})
	if err := NewWorker(c, nil).Run(context.Background(), job, rep); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if _, err := os.Stat(edl); err != nil {
		t.Fatalf("no EDL: %v", err)
	}
	if got := rep.result.(JobResult).EDLPath; got != edl {
		t.Errorf("result EDL path = %q", got)
	}
}

// The clip is already published and correct by the time the sidecar is written.
// Losing the sidecar must not fail the job that produced the clip.
func TestAFailedEDLDoesNotFailTheJobThatCutTheClip(t *testing.T) {
	dir := t.TempDir()
	f := &fakeFFmpeg{}
	c := newTestCutter(t, f, gop(60, 2*time.Second))
	rep := &fakeReporter{}

	job := jobFor(t, JobParams{
		Segments: []Segment{{Path: "/rec/seg0.mkv", Duration: time.Hour}},
		Request:  req(5*time.Second, 15*time.Second, func(r *Request) { r.OutPath = filepath.Join(dir, "clip.mkv") }),
		// A directory that does not exist, so the write fails.
		EDLPath: filepath.Join(dir, "nope", "clip"+OTIOExt),
	})
	if err := NewWorker(c, nil).Run(context.Background(), job, rep); err != nil {
		t.Fatalf("Run failed because a sidecar did: %v", err)
	}
	if got := rep.result.(JobResult).EDLPath; got != "" {
		t.Errorf("result claims an EDL at %q", got)
	}
	if !rep.has("its EDL was not") {
		t.Errorf("the failure was not logged: %v", rep.lines)
	}
}

// A retry cannot fix a request that is wrong on its own terms, and a queue that
// retries one three times has just done nothing three times.
func TestTheJobMarksUnfixableFailuresPermanent(t *testing.T) {
	dir := t.TempDir()
	c := newTestCutter(t, &fakeFFmpeg{}, gop(60, 2*time.Second))
	segs := []Segment{{Path: "/rec/seg0.mkv", Duration: time.Hour}}

	tests := []struct {
		name          string
		job           jobs.Job
		wantPermanent bool
	}{
		{
			name:          "params that are not JSON",
			job:           jobs.Job{Kind: JobKind, Target: "recording:1", Params: json.RawMessage("{oh no")},
			wantPermanent: true,
		},
		{
			name: "a range that is not a range",
			job: jobFor(t, JobParams{Segments: segs, Request: Request{
				In: time.Second, Out: time.Second, OutPath: filepath.Join(dir, "clip.mkv"),
			}}),
			wantPermanent: true,
		},
		{
			name: "an in-point past the end of the recording",
			job: jobFor(t, JobParams{Segments: segs, Request: req(2*time.Hour, 2*time.Hour+time.Minute,
				func(r *Request) { r.OutPath = filepath.Join(dir, "clip.mkv") })}),
			wantPermanent: true,
		},
		{
			name:          "a job with no segments and no resolver",
			job:           jobFor(t, JobParams{Request: req(0, time.Second, func(r *Request) { r.OutPath = filepath.Join(dir, "clip.mkv") })}),
			wantPermanent: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := NewWorker(c, nil).Run(context.Background(), tc.job, &fakeReporter{})
			if err == nil {
				t.Fatal("Run succeeded")
			}
			if got := jobs.IsPermanent(err); got != tc.wantPermanent {
				t.Fatalf("permanent = %v, want %v (%v)", got, tc.wantPermanent, err)
			}
		})
	}
}

func TestTheJobFallsBackToItsResolverWhenItCarriesNoSegments(t *testing.T) {
	dir := t.TempDir()
	c := newTestCutter(t, &fakeFFmpeg{}, gop(60, 2*time.Second))
	var asked string
	resolve := func(_ context.Context, target string) (Timeline, error) {
		asked = target
		return NewTimeline([]Segment{{Path: "/rec/seg0.mkv", Duration: time.Hour}})
	}

	job := jobFor(t, JobParams{Request: req(5*time.Second, 15*time.Second,
		func(r *Request) { r.OutPath = filepath.Join(dir, "clip.mkv") })})
	if err := NewWorker(c, resolve).Run(context.Background(), job, &fakeReporter{}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if asked != "recording:1" {
		t.Fatalf("the resolver was asked for %q", asked)
	}
}

func entryNames(entries []os.DirEntry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}
