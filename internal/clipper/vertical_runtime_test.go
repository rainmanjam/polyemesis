package clipper

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/jobs"
)

// CaptionCues rebases each segment's transcript onto the timeline through the
// segment's own start, keeps the lines overlapping the clip, filters by track,
// reads each recording once, and skips segments that name none.
func TestCaptionCuesRebasesEachSegmentsTranscript(t *testing.T) {
	reads := map[int64]int{}
	src := func(id int64) ([]TranscriptLine, error) {
		reads[id]++
		switch id {
		case 1:
			return []TranscriptLine{{Track: 0, StartMS: 3_590_000, EndMS: 3_595_000, Text: "end of the first"},
				{Track: 1, StartMS: 3_591_000, EndMS: 3_592_000, Text: "other mic"}}, nil
		case 2:
			return []TranscriptLine{{Track: 0, StartMS: 1_000, EndMS: 3_000, Text: "start of the second"},
				{Track: 0, StartMS: 600_000, EndMS: 601_000, Text: "far later"}}, nil
		}
		return nil, nil
	}
	segs := []Segment{
		{RecordingID: 1, Path: "/a", Start: 0, Duration: time.Hour},
		{RecordingID: 2, Path: "/b", Start: time.Hour, Duration: time.Hour},
		{RecordingID: 2, Path: "/b2", Start: time.Hour, Duration: time.Hour},
		{Path: "/unknown", Start: 2 * time.Hour, Duration: time.Hour},
	}
	cues, err := CaptionCues(segs, src, time.Hour-20*time.Second, time.Hour+20*time.Second, []int{0})
	if err != nil {
		t.Fatal(err)
	}
	if len(cues) != 2 || cues[0].Text != "end of the first" || cues[1].Text != "start of the second" ||
		cues[1].StartMS != 3_601_000 {
		t.Errorf("cues %+v", cues)
	}
	if reads[2] != 1 {
		t.Errorf("recording 2 read %d times", reads[2])
	}
	if _, err := CaptionCues(segs, func(int64) ([]TranscriptLine, error) { return nil, errors.New("disk") }, 0, time.Hour, nil); err == nil {
		t.Error("a transcript that cannot be read is an error")
	}
	v := &Vertical{Layout: LayoutBlur, Captions: &Captions{Cues: cues, Tracks: []int{0}}}
	if w := v.ForJob(); len(w.Captions.Cues) != 0 || len(v.Captions.Cues) != 2 || w.Captions.Tracks[0] != 0 {
		t.Errorf("WithoutCues must drop the words from a copy, not the original: %+v / %+v", w.Captions, v.Captions)
	}
}

type workerReporter struct{ logs []string }

func (r *workerReporter) Progress(float64)        {}
func (r *workerReporter) Logf(f string, a ...any) { r.logs = append(r.logs, fmt.Sprintf(f, a...)) }
func (r *workerReporter) SetResult(any)           {}

// captionRunner stands in for FFmpeg: it reads the caption sidecars the
// command was given and writes the output file, so the worker sees success.
func captionRunner(drawn *[]string) func(context.Context, string, []string) ([]byte, error) {
	return func(_ context.Context, _ string, args []string) ([]byte, error) {
		for i, a := range args {
			if a == "-/filter_complex" {
				files, _ := filepath.Glob(filepath.Join(filepath.Dir(args[i+1]), "caption-*.txt"))
				sort.Strings(files)
				for _, f := range files {
					b, _ := os.ReadFile(f)
					*drawn = append(*drawn, string(b))
				}
			}
		}
		return nil, os.WriteFile(args[len(args)-1], []byte("short"), 0o644)
	}
}

// THE WORKER READS THE WORDS WHEN IT RUNS. A captioned short's job carries the
// tracks and the style; the worker asks the transcript source for the lines and
// draws them. Without a source the short is still made, and the log says why it
// has no captions.
//
// MUTATION: captionsAtRunTime returns v unchanged. Observed: FAIL, nothing drawn.
func TestTheWorkerCaptionsAShortFromTheTranscriptAtRunTime(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "exports", "short.mp4")
	params := JobParams{
		Request: Request{In: 10 * time.Second, Out: 20 * time.Second, OutPath: out, Mode: ModePrecise,
			Vertical: &Vertical{Layout: LayoutBlur, Captions: &Captions{Tracks: []int{0},
				FontFile: filepath.Join(dir, "font.ttf"), DrawText: true}}},
		Segments: []Segment{{RecordingID: 7, Path: filepath.Join(dir, "src.mkv"), Start: 0, Duration: time.Minute}},
	}
	raw, _ := json.Marshal(params)
	src := func(id int64) ([]TranscriptLine, error) {
		return []TranscriptLine{{Track: 0, StartMS: 12_000, EndMS: 14_000, Text: "spoken at twelve"},
			{Track: 1, StartMS: 12_000, EndMS: 14_000, Text: "the other mic"}}, nil
	}

	params.Request.Vertical.Captions.FontFile = ""
	params.Request.Vertical.Captions.DrawText = false
	params.Request.Vertical.Captions.Style.Font = "Inter-Bold.ttf"
	raw, _ = json.Marshal(params)
	fonts := func(name string) (string, error) { return filepath.Join(dir, "fonts", name), nil }

	var drawn []string
	c := New(testLog(), "ffmpeg", "ffprobe", WithRunner(captionRunner(&drawn)), WithProber(stubProber{}))
	if err := NewWorker(c, nil, WithTranscript(src), WithCaptionFonts(fonts), WithDrawText(true)).Run(context.Background(),
		jobs.Job{Kind: JobKind, Params: raw}, &workerReporter{}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if strings.Join(drawn, "|") != "spoken at twelve" {
		t.Errorf("drew %q, want only track 0's line", drawn)
	}

	drawn = nil
	rep := &workerReporter{}
	if err := NewWorker(c, nil).Run(context.Background(), jobs.Job{Kind: JobKind, Params: raw}, rep); err != nil {
		t.Fatalf("Run without a source: %v", err)
	}
	if len(drawn) != 0 || !strings.Contains(strings.Join(rep.logs, " "), "no transcript source") {
		t.Errorf("without a source: drew %q, logs %q", drawn, rep.logs)
	}
}

type stubProber struct{}

func (stubProber) Keyframes(context.Context, string, time.Duration, time.Duration) (Keyframes, error) {
	return NewKeyframes(nil), nil
}

// THE CUTTER YIELDS TO THE LIVE STREAM. WithNice routes every child -- the cut
// and the keyframe probe alike -- through the priority wrapper the governor
// hands out, and a vertical export with no thread cap gets one.
//
// MUTATION: WithNice ignored. Observed: FAIL, the runner sees "ffmpeg".
// MUTATION: no default thread cap. Observed: FAIL, no -threads.
func TestTheCutterRunsItsChildrenThroughTheNiceWrapper(t *testing.T) {
	var names []string
	rec := func(_ context.Context, name string, args []string) ([]byte, error) {
		names = append(names, name)
		return nil, os.WriteFile(args[len(args)-1], []byte("x"), 0o644)
	}
	nice := func(name string, args []string) (string, []string) {
		return "nice", append([]string{"-n", "10", name}, args...)
	}
	dir := t.TempDir()
	c := New(testLog(), "ffmpeg", "ffprobe", WithRunner(rec), WithNice(nice), WithProber(stubProber{}))
	tl, _ := NewTimeline([]Segment{{Path: filepath.Join(dir, "src.mkv"), Duration: time.Minute}})
	res, err := c.Cut(context.Background(), tl, Request{In: time.Second, Out: 5 * time.Second,
		OutPath: filepath.Join(dir, "short.mp4"), Mode: ModePrecise, Vertical: &Vertical{Layout: LayoutBlur}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(names) == 0 || names[0] != "nice" {
		t.Errorf("children ran as %q, want them under the nice wrapper", names)
	}
	if res.Plan.HeadThreads != VerticalThreads() || VerticalThreads() < 1 {
		t.Errorf("a vertical export's threads = %d, want the default cap %d", res.Plan.HeadThreads, VerticalThreads())
	}
}

// A finished short says so in its job result, which is how the offline-card
// picker and the playout server tell it from a landscape export.
func TestAShortsJobResultSaysItIsVertical(t *testing.T) {
	v := Plan{Vertical: &Vertical{Layout: LayoutBlur}, Sources: []Segment{{Path: "/a"}}}
	if r := jobResult(v, Result{Path: "/x.mp4"}, JobParams{}, &workerReporter{}); !r.Vertical {
		t.Error("a short's job result does not say it is vertical")
	}
	if r := jobResult(Plan{Sources: []Segment{{Path: "/a"}}}, Result{Path: "/x.mkv"}, JobParams{}, &workerReporter{}); r.Vertical {
		t.Error("a straight cut claims to be vertical")
	}
}

// A SHORT NEVER REPLACES A FILE. Two jobs that ended up with one name, or an
// export that finds a file already there, fail rather than overwrite what the
// first one made. (A straight cut keeps its old behaviour.)
//
// MUTATION: publish with os.Rename. Observed: FAIL, the first short replaced.
func TestAShortNeverReplacesAnExistingFile(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "short.mp4")
	if err := os.WriteFile(out, []byte("the first short"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := New(testLog(), "ffmpeg", "ffprobe", WithProber(stubProber{}), WithRunner(func(_ context.Context, _ string, args []string) ([]byte, error) {
		return nil, os.WriteFile(args[len(args)-1], []byte("the second short"), 0o644)
	}))
	tl, _ := NewTimeline([]Segment{{Path: filepath.Join(dir, "src.mkv"), Duration: time.Minute}})
	_, err := c.Cut(context.Background(), tl, Request{In: time.Second, Out: 5 * time.Second, OutPath: out,
		Mode: ModePrecise, Vertical: &Vertical{Layout: LayoutBlur}}, nil)
	if !errors.Is(err, ErrOutputExists) {
		t.Errorf("Cut = %v, want ErrOutputExists", err)
	}
	if b, _ := os.ReadFile(out); string(b) != "the first short" {
		t.Errorf("the existing short was replaced: %q", b)
	}
	if left, _ := filepath.Glob(filepath.Join(dir, ".clip-*")); len(left) != 0 {
		t.Errorf("work directories left behind: %v", left)
	}
}
