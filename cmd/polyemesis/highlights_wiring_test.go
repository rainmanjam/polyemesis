package main

import (
	"context"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/clipper"
	"github.com/rainmanjam/polyemesis/internal/config"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/ffmpeg"
	"github.com/rainmanjam/polyemesis/internal/highlights"
	"github.com/rainmanjam/polyemesis/internal/jobs"
	"github.com/rainmanjam/polyemesis/internal/testenv"
)

type wiringReporter struct{ result any }

func (r *wiringReporter) Progress(float64)    {}
func (r *wiringReporter) Logf(string, ...any) {}
func (r *wiringReporter) SetResult(v any)     { r.result = v }

// Both new kinds are registered, unconditionally, by the same mechanism the
// normaliser's guard uses: a second Register of an already-registered kind is
// refused.
func TestTheHighlightAndClipKindsAreRegistered(t *testing.T) {
	store, dir := wiringStore(t)
	log := wiringLogger()
	q := jobs.New(log, store)
	gov := jobs.NewGovernor(log, q)
	tools := &ffmpeg.Tools{FFmpeg: filepath.Join(dir, "no-ffmpeg"), FFprobe: filepath.Join(dir, "no-ffprobe")}
	registerProcessors(log, config.Config{DataDir: dir}, store, nil, gov, q, tools, nil, db.PostProdSettings{})
	for _, k := range []jobs.Kind{highlights.JobKind, clipper.JobKind} {
		if err := q.Register(k, 1, jobs.WorkerFunc(func(context.Context, jobs.Job, jobs.Reporter) error { return nil })); err == nil {
			t.Errorf("%s is not registered", k)
		}
	}
}

// A scan through the REAL wiring: a real store with a recording, its
// transcript and its chat; a real FFmpeg measuring a real file; suggestions
// saved through the real Save into clip_highlights. What the unit tests stub,
// this runs.
func TestAScanThroughTheRealWiringStoresSuggestions(t *testing.T) {
	bin := testenv.FFmpegBinary(t, "ffmpeg", "without ffmpeg the scan's loudness pass cannot run")
	store, dir := wiringStore(t)
	log := wiringLogger()

	// Twenty seconds: a quiet tone with a shout at 10..13 s.
	src := filepath.Join(dir, "rec-20261102-100000.mkv")
	if out, err := exec.Command(bin, "-hide_banner", "-loglevel", "error", "-y", "-f", "lavfi",
		"-i", "sine=f=440:r=48000:d=20,volume=volume=0.05,volume=enable='between(t,10,13)':volume=16",
		"-c:a", "flac", src).CombinedOutput(); err != nil {
		t.Fatalf("fixture: %v\n%s", err, out)
	}
	started := time.UnixMilli(1_800_000_000_000)
	if err := store.UpsertRecording(&db.Recording{Filename: filepath.Base(src), StartedAt: started, DurationMS: 20_000}); err != nil {
		t.Fatal(err)
	}
	recs, _ := store.ListRecordings()
	rec := recs[0].ID
	if err := store.SaveTranscript(&db.Transcript{RecordingID: rec, Tracks: []db.TranscriptTrack{{Track: 0,
		Segments: []db.TranscriptSegment{{Track: 0, StartMS: 9_500, EndMS: 12_000, Text: "here it comes"}}}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendChatMessages([]db.ChatMessage{{Platform: db.PlatformTwitch, MessageID: "m1", Text: "hi",
		At: started.Add(2 * time.Second)}}); err != nil {
		t.Fatal(err)
	}

	deps := highlightDeps(log, store, nil, nil, &ffmpeg.Tools{FFmpeg: bin})
	rep := &wiringReporter{}
	res, err := highlights.Scan(context.Background(), deps, highlights.JobParams{RecordingID: rec, Parts: []highlights.Part{
		{RecordingID: rec, Path: src, DurationMS: 20_000, StartedAtMS: started.UnixMilli()},
	}}, rep)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if !res.LoudnessMeasured || !res.ChatAvailable || res.ChatMessages != 1 || res.Stored != 1 {
		t.Fatalf("result %+v", res)
	}
	list, err := store.ListHighlights(rec)
	if err != nil || len(list) != 1 {
		t.Fatalf("stored %+v, %v", list, err)
	}
	h := list[0]
	if h.Excerpt != "here it comes" || !strings.Contains(string(h.Signals), `"loudness"`) || h.State != db.HighlightPending {
		t.Errorf("stored suggestion %+v", h)
	}
	// The model was not asked for, and none is configured.
	if c, why := deps.Model(); c != nil || why == "" {
		t.Errorf("no Automod model is configured: %v %q", c != nil, why)
	}

	// A measurement that fails says why, from FFmpeg's own last line.
	if err := deps.Run(context.Background(), bin, []string{"-hide_banner", "-i", filepath.Join(dir, "missing.mkv")},
		func(r io.Reader) error { return nil }); err == nil ||
		!strings.Contains(err.Error(), "exit status") || strings.HasSuffix(strings.TrimSpace(err.Error()), ":") {
		t.Errorf("a failed run = %v, want the exit status and FFmpeg's reason after it", err)
	}
	if got := lastLine("first\nsecond\n"); got != "second" {
		t.Errorf("lastLine = %q", got)
	}
}

// The production cutter is the governor's: every FFmpeg it starts is wrapped
// exactly as the governor's NiceCommand wraps it.
func TestTheProductionCutterGoesThroughTheGovernor(t *testing.T) {
	store, dir := wiringStore(t)
	log := wiringLogger()
	gov := jobs.NewGovernor(log, jobs.New(log, store))
	var got []string
	rec := clipper.WithRunner(func(_ context.Context, name string, args []string) ([]byte, error) {
		got = append([]string{name}, args...)
		return nil, nil
	})
	c := newCutter(log, &ffmpeg.Tools{FFmpeg: "ffmpeg", FFprobe: "ffprobe"}, gov, rec)
	tl, _ := clipper.NewTimeline([]clipper.Segment{{Path: filepath.Join(dir, "a.mkv"), Duration: time.Minute}})
	_, _ = c.Cut(context.Background(), tl, clipper.Request{In: time.Second, Out: 2 * time.Second, OutPath: filepath.Join(dir, "o.mkv")}, nil)
	if len(got) == 0 {
		t.Fatal("the cutter ran nothing")
	}
	wantName, _ := gov.NiceCommand("ffmpeg")
	if got[0] != wantName {
		t.Errorf("the cutter started %q, want the governor's %q", got[0], wantName)
	}
	if wantName == "ffmpeg" {
		t.Log("this machine has no priority tool, so the wrapper is the identity here")
	}
}

// The resolver a job falls back to names each segment's recording, so a job
// whose params carry no segments can still be captioned at run time.
//
// MUTATION: recordingTimeline drops the id. Observed: FAIL, RecordingID 0.
func TestTheRecordingResolverNamesTheRecording(t *testing.T) {
	store, dir := wiringStore(t)
	if err := store.UpsertRecording(&db.Recording{Filename: "r.mkv", DurationMS: 60_000}); err != nil {
		t.Fatal(err)
	}
	recs, _ := store.ListRecordings()
	tl, err := recordingTimeline(store, dir)(context.Background(), jobs.RecordingTarget(recs[0].ID))
	if err != nil {
		t.Fatal(err)
	}
	if segs := tl.Segments(); len(segs) != 1 || segs[0].RecordingID != recs[0].ID {
		t.Errorf("segments %+v do not name recording %d", segs, recs[0].ID)
	}
}
