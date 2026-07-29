package clipper

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The whole point of the export is that the file opens in an editor. Every
// object needs its OTIO_SCHEMA, and the versions are deliberately old ones:
// OTIO upgrades an older document on read and cannot downgrade a newer one.
func TestOTIODocumentCarriesTheSchemaVersionsEveryReaderAccepts(t *testing.T) {
	raw := marshalEDL(t, EDL{
		Name: "goal",
		Rate: 30,
		Clips: []EDLClip{{
			Name: "goal", Path: "/rec/seg0.mkv",
			In: 10 * time.Second, Duration: 4 * time.Second, Available: time.Hour,
		}},
	})

	doc := decodeJSON(t, raw)
	if got := doc["OTIO_SCHEMA"]; got != "Timeline.1" {
		t.Errorf("timeline schema = %v", got)
	}
	stack := doc["tracks"].(map[string]any)
	if got := stack["OTIO_SCHEMA"]; got != "Stack.1" {
		t.Errorf("stack schema = %v", got)
	}
	track := stack["children"].([]any)[0].(map[string]any)
	if got := track["OTIO_SCHEMA"]; got != "Track.1" {
		t.Errorf("track schema = %v", got)
	}
	if got := track["kind"]; got != "Video" {
		t.Errorf("track kind = %v", got)
	}
	clip := track["children"].([]any)[0].(map[string]any)
	if got := clip["OTIO_SCHEMA"]; got != "Clip.1" {
		t.Errorf("clip schema = %v", got)
	}
	ref := clip["media_reference"].(map[string]any)
	if got := ref["OTIO_SCHEMA"]; got != "ExternalReference.1" {
		t.Errorf("reference schema = %v", got)
	}
	if got := ref["target_url"]; got != "file:///rec/seg0.mkv" {
		t.Errorf("target url = %v", got)
	}
}

// OTIO counts in frames at a rate, so seconds have to be converted or every
// edit lands on the wrong frame.
func TestOTIOTimesAreFramesAtTheTimelineRate(t *testing.T) {
	tests := []struct {
		name          string
		rate          float64
		in, duration  time.Duration
		wantRate      float64
		wantStart     float64
		wantFrameSpan float64
	}{
		{
			name: "30fps", rate: 30,
			in: 10 * time.Second, duration: 4 * time.Second,
			wantRate: 30, wantStart: 300, wantFrameSpan: 120,
		},
		{
			name: "60fps", rate: 60,
			in: time.Second, duration: 500 * time.Millisecond,
			wantRate: 60, wantStart: 60, wantFrameSpan: 30,
		},
		{
			name: "a rate nobody supplied falls back rather than dividing by zero",
			rate: 0,
			in:   time.Second, duration: time.Second,
			wantRate: DefaultEDLRate, wantStart: DefaultEDLRate, wantFrameSpan: DefaultEDLRate,
		},
		{
			name: "a fractional frame is rounded to the nearest one", rate: 30,
			in: 1017 * time.Millisecond, duration: time.Second,
			wantRate: 30, wantStart: 31, wantFrameSpan: 30,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			raw := marshalEDL(t, EDL{
				Name:  "clip",
				Rate:  tc.rate,
				Clips: []EDLClip{{Path: "/rec/seg0.mkv", In: tc.in, Duration: tc.duration}},
			})
			clip := firstClip(t, decodeJSON(t, raw))
			rng := clip["source_range"].(map[string]any)
			start := rng["start_time"].(map[string]any)
			dur := rng["duration"].(map[string]any)

			if got := start["rate"].(float64); got != tc.wantRate {
				t.Errorf("rate = %v, want %v", got, tc.wantRate)
			}
			if got := start["value"].(float64); got != tc.wantStart {
				t.Errorf("start = %v frames, want %v", got, tc.wantStart)
			}
			if got := dur["value"].(float64); got != tc.wantFrameSpan {
				t.Errorf("duration = %v frames, want %v", got, tc.wantFrameSpan)
			}
			if got := start["OTIO_SCHEMA"]; got != "RationalTime.1" {
				t.Errorf("time schema = %v", got)
			}
		})
	}
}

// A clip that spans segment files becomes several clips butted together, which
// is the truth of what is on disk: the editor gets the same two files, and a
// user can still trim across the seam.
func TestFromPlanSplitsABoundarySpanningCutIntoOneClipPerFile(t *testing.T) {
	tl := hourlyTimeline(t, 3, 10*time.Second)
	p, err := PlanCut(tl, gop(60, time.Second), req(9*time.Second, 21*time.Second))
	if err != nil {
		t.Fatalf("PlanCut: %v", err)
	}

	e := FromPlan(p, 30)
	if len(e.Clips) != 3 {
		t.Fatalf("got %d clips, want one per source file", len(e.Clips))
	}
	want := []struct {
		path     string
		in       time.Duration
		duration time.Duration
	}{
		{"/seg0.mkv", 9 * time.Second, time.Second},
		{"/seg1.mkv", 0, 10 * time.Second},
		{"/seg2.mkv", 0, time.Second},
	}
	for i, w := range want {
		got := e.Clips[i]
		if got.Path != w.path || got.In != w.in || got.Duration != w.duration {
			t.Errorf("clip %d = %s in=%s dur=%s, want %s in=%s dur=%s",
				i, got.Path, got.In, got.Duration, w.path, w.in, w.duration)
		}
		if got.Available != 10*time.Second {
			t.Errorf("clip %d advertises %s of media, want the whole 10s file", i, got.Available)
		}
	}
}

// The EDL frequently outlives the conversation about drift. It has to say the
// in-point moved rather than leaving that to be rediscovered in an edit suite.
func TestFromPlanMarksTheRequestedInPointWhenTheCutDrifted(t *testing.T) {
	p := planFast(t, 5*time.Second, 15*time.Second)
	if p.InDrift != -time.Second {
		t.Fatalf("precondition: drift = %s, want -1s", p.InDrift)
	}

	e := FromPlan(p, 30)
	if len(e.Clips) != 1 || len(e.Clips[0].Markers) != 1 {
		t.Fatalf("got %d clips with %d markers, want one marker", len(e.Clips), markerCount(e))
	}
	m := e.Clips[0].Markers[0]
	if m.At != 5*time.Second {
		t.Errorf("marker at %s, want the requested 5s", m.At)
	}
	if !strings.Contains(m.Comment, "keyframe") {
		t.Errorf("marker comment does not explain itself: %q", m.Comment)
	}

	clip := firstClip(t, decodeJSON(t, marshalEDL(t, e)))
	markers := clip["markers"].([]any)
	if len(markers) != 1 {
		t.Fatalf("the marker did not survive serialisation: %v", markers)
	}
	got := markers[0].(map[string]any)
	if got["OTIO_SCHEMA"] != "Marker.2" {
		t.Errorf("marker schema = %v", got["OTIO_SCHEMA"])
	}
	if got["color"] != MarkerRed {
		t.Errorf("marker colour = %v", got["color"])
	}
	// A point marker: zero length, at one frame.
	rng := got["marked_range"].(map[string]any)
	if v := rng["duration"].(map[string]any)["value"].(float64); v != 0 {
		t.Errorf("marker spans %v frames, want a point", v)
	}
	if v := rng["start_time"].(map[string]any)["value"].(float64); v != 150 {
		t.Errorf("marker at frame %v, want 150", v)
	}
}

func TestFromPlanLeavesAnUndriftedCutUnmarked(t *testing.T) {
	p := planFast(t, 6*time.Second, 15*time.Second)
	e := FromPlan(p, 30)
	if markerCount(e) != 0 {
		t.Fatalf("a cut that did not drift was marked anyway")
	}
}

func TestFromPlanRecordsWhatTheCutActuallyDid(t *testing.T) {
	p := planPrecise(t, 5*time.Second, 15*time.Second)
	e := FromPlan(p, 30)
	meta := e.Clips[0].Metadata["polyemesis"].(map[string]any)

	if meta["mode"] != string(ModePrecise) {
		t.Errorf("mode = %v", meta["mode"])
	}
	if meta["reEncodedSec"].(float64) != 1 {
		t.Errorf("re-encoded = %v seconds, want 1", meta["reEncodedSec"])
	}
}

func TestFromPlanNamesTheTimelineAfterTheClip(t *testing.T) {
	tests := []struct {
		name  string
		title string
		out   string
		want  string
	}{
		{name: "an explicit title wins", title: "The goal", out: filepath.Join(testClipDir, "a.mkv"), want: "The goal"},
		{name: "otherwise the filename", out: filepath.Join(testClipDir, "clip-20240115.mkv"), want: "clip-20240115"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tl := oneHourTimeline(t)
			p, err := PlanCut(tl, gop(60, 2*time.Second), req(0, 10*time.Second, func(r *Request) {
				r.Title, r.OutPath = tc.title, tc.out
			}))
			if err != nil {
				t.Fatalf("PlanCut: %v", err)
			}
			if got := FromPlan(p, 30).Name; got != tc.want {
				t.Fatalf("name = %q, want %q", got, tc.want)
			}
		})
	}
}

// A media reference with no available_range reads as unbounded to some
// importers. Advertising the cut's own span is the conservative truth.
func TestAClipOfUnknownMediaAdvertisesOnlyItsOwnSpan(t *testing.T) {
	raw := marshalEDL(t, EDL{
		Name:  "clip",
		Rate:  30,
		Clips: []EDLClip{{Path: "/rec/seg0.mkv", In: 10 * time.Second, Duration: 4 * time.Second}},
	})
	ref := firstClip(t, decodeJSON(t, raw))["media_reference"].(map[string]any)
	rng := ref["available_range"].(map[string]any)
	if v := rng["duration"].(map[string]any)["value"].(float64); v != 420 {
		t.Fatalf("available = %v frames, want 14s at 30fps", v)
	}
}

func TestFileURLSurvivesAWindowsPath(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{in: "/rec/seg0.mkv", want: "file:///rec/seg0.mkv"},
		{in: `C:\media\rec.mkv`, want: "file:///C:/media/rec.mkv"},
		{in: "/rec/a b.mkv", want: "file:///rec/a%20b.mkv"},
		// A backslash is a legal character in a POSIX filename. Treating it as a
		// separator would turn one file into two directories.
		{in: `/rec/my\clip.mkv`, want: `file:///rec/my%5Cclip.mkv`},
		{in: "", want: ""},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			if got := fileURL(tc.in); got != tc.want {
				t.Fatalf("fileURL(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestWriteOTIOPublishesTheFileWhole(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "clip"+OTIOExt)

	p := planFast(t, 5*time.Second, 15*time.Second)
	if err := WriteOTIO(path, FromPlan(p, 30)); err != nil {
		t.Fatalf("WriteOTIO: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if doc := decodeJSON(t, raw); doc["OTIO_SCHEMA"] != "Timeline.1" {
		t.Fatalf("the written file is not an OTIO timeline")
	}
	// Nothing half-written is left where a user or an importer could find it.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("the directory holds %d files, want only the timeline", len(entries))
	}
}

func marshalEDL(t *testing.T, e EDL) []byte {
	t.Helper()
	raw, err := e.MarshalOTIO()
	if err != nil {
		t.Fatalf("MarshalOTIO: %v", err)
	}
	return raw
}

func decodeJSON(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("the OTIO document is not valid JSON: %v\n%s", err, raw)
	}
	return doc
}

func firstClip(t *testing.T, doc map[string]any) map[string]any {
	t.Helper()
	stack := doc["tracks"].(map[string]any)
	track := stack["children"].([]any)[0].(map[string]any)
	clips := track["children"].([]any)
	if len(clips) == 0 {
		t.Fatal("the timeline has no clips")
	}
	return clips[0].(map[string]any)
}

func markerCount(e EDL) int {
	n := 0
	for _, c := range e.Clips {
		n += len(c.Markers)
	}
	return n
}
