package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/automod"
	"github.com/rainmanjam/polyemesis/internal/clipper"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/ffmpeg"
	"github.com/rainmanjam/polyemesis/internal/highlights"
	"github.com/rainmanjam/polyemesis/internal/jobs"
)

// highlightFixture is a server with a job queue (no workers, so nothing runs),
// one ten-minute recording on disk with a transcript, and the embedded fonts.
func highlightFixture(t *testing.T) (http.Handler, func(*http.Request), *Server, int64) {
	t.Helper()
	h, sign, srv, _ := playlistJobServer(t)
	dir := srv.eng().Recordings().Dir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	const name = "rec-20261102-090000.mkv"
	if err := os.WriteFile(filepath.Join(dir, name), []byte("MASTER"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := srv.store.UpsertRecording(&db.Recording{Filename: name, StartedAt: time.Now().Add(-time.Hour),
		DurationMS: 600_000, Tracks: 2}); err != nil {
		t.Fatal(err)
	}
	recs, err := srv.store.ListRecordings()
	if err != nil || len(recs) == 0 {
		t.Fatalf("ListRecordings: %v", err)
	}
	rec := recs[0].ID
	if err := srv.store.SaveTranscript(&db.Transcript{RecordingID: rec, Tracks: []db.TranscriptTrack{
		{Track: 0, Segments: []db.TranscriptSegment{
			{Track: 0, StartMS: 12_000, EndMS: 14_500, Text: "hello there"},
			{Track: 0, StartMS: 90_000, EndMS: 92_000, Text: "much later"},
		}},
		{Track: 1, Segments: []db.TranscriptSegment{{Track: 1, StartMS: 13_000, EndMS: 15_000, Text: "the other mic"}}},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := ffmpeg.EnsureFonts(srv.cfg.FontsDir()); err != nil {
		t.Fatal(err)
	}
	return h, sign, srv, rec
}

func jobsOfKind(t *testing.T, store *db.DB, k jobs.Kind) []jobs.Job {
	t.Helper()
	list, err := store.ListJobs(jobs.Filter{Kinds: []jobs.Kind{k}})
	if err != nil {
		t.Fatal(err)
	}
	return list
}

func plantHighlight(t *testing.T, store *db.DB, rec int64, start, end int64) int64 {
	t.Helper()
	if _, err := store.ReplacePendingHighlights(rec, []db.Highlight{{StartMS: start, EndMS: end, Score: 0.7,
		Signals: []byte(`[{"kind":"chat","startMs":0,"endMs":1,"strength":0.7,"peak":9,"baseline":1}]`),
		Excerpt: "hello there"}}); err != nil {
		t.Fatal(err)
	}
	list, _ := store.ListHighlights(rec)
	for _, h := range list {
		if h.StartMS == start && h.State == db.HighlightPending {
			return h.ID
		}
	}
	t.Fatal("planted highlight not found")
	return 0
}

func recPath(rec int64, tail string) string {
	return "/api/v1/clipper/recordings/" + strconv.FormatInt(rec, 10) + tail
}

// NOTHING IS CLIPPED BY A SCAN. Scanning queues one highlights.scan job, folds
// a second press into it, carries the parts it will measure -- and creates no
// clip.export job at all.
//
// MUTATION: handleScanHighlights also submits a clip export. Observed: FAIL.
// MUTATION: the scan is not Unique. Observed: FAIL, two scan jobs.
func TestAHighlightScanQueuesAScanAndNoClip(t *testing.T) {
	h, sign, srv, rec := highlightFixture(t)
	send(t, h, sign, http.MethodPost, recPath(rec, "/highlights/scan"), map[string]any{"useModel": true}, http.StatusAccepted)
	send(t, h, sign, http.MethodPost, recPath(rec, "/highlights/scan"), map[string]any{}, http.StatusAccepted)
	scans := jobsOfKind(t, srv.store, highlights.JobKind)
	if len(scans) != 1 {
		t.Fatalf("%d scan jobs, want one (Scan twice is one scan)", len(scans))
	}
	var p highlights.JobParams
	if err := json.Unmarshal(scans[0].Params, &p); err != nil {
		t.Fatal(err)
	}
	if p.RecordingID != rec || len(p.Parts) != 1 || p.Parts[0].Path == "" || p.Parts[0].StartedAtMS == 0 || !p.UseModel {
		t.Errorf("scan params %+v", p)
	}
	if clips := jobsOfKind(t, srv.store, clipper.JobKind); len(clips) != 0 {
		t.Errorf("a scan created %d clip exports; nothing is clipped without an accept", len(clips))
	}
}

// DISMISSING CLIPS NOTHING, and the queue shows the decision.
func TestDismissingAHighlightClipsNothing(t *testing.T) {
	h, sign, srv, rec := highlightFixture(t)
	id := plantHighlight(t, srv.store, rec, 10_000, 40_000)
	var view highlightDecisionView
	decodeInto(t, send(t, h, sign, http.MethodPost, "/api/v1/clipper/highlights/"+strconv.FormatInt(id, 10)+"/dismiss",
		nil, http.StatusOK), &view)
	if view.Highlight.State != db.HighlightDismissed {
		t.Errorf("state %q", view.Highlight.State)
	}
	if clips := jobsOfKind(t, srv.store, clipper.JobKind); len(clips) != 0 {
		t.Errorf("a dismiss created %d clip exports", len(clips))
	}
	send(t, h, sign, http.MethodPost, "/api/v1/clipper/highlights/"+strconv.FormatInt(id, 10)+"/dismiss", nil, http.StatusConflict)

	var list highlightsView
	decodeInto(t, send(t, h, sign, http.MethodGet, recPath(rec, "/highlights"), nil, http.StatusOK), &list)
	if len(list.Suggestions) != 1 || list.Suggestions[0].State != db.HighlightDismissed ||
		len(list.Suggestions[0].Signals) != 1 || list.Suggestions[0].Signals[0].Kind != highlights.KindChat {
		t.Errorf("review queue %+v", list)
	}
	if list.ModelAvailable || !strings.Contains(list.ModelReason, "switched off") {
		t.Errorf("default settings have no model: %+v", list)
	}
}

// ACCEPT IS THE ONLY WAY A SUGGESTION BECOMES A CLIP, AND IT DOES SO ONCE. The
// accepted suggestion's span is the clip's; a second accept is refused and
// queues nothing; the suggestion records the job it made.
//
// MUTATION: the accept skips DecideHighlight. Observed: FAIL, two clip exports.
// MUTATION: the accept ignores the suggestion's span. Observed: FAIL, 0..0.
func TestAcceptingAHighlightQueuesExactlyOneClip(t *testing.T) {
	h, sign, srv, rec := highlightFixture(t)
	id := plantHighlight(t, srv.store, rec, 10_000, 40_000)
	path := "/api/v1/clipper/highlights/" + strconv.FormatInt(id, 10) + "/accept"

	var view highlightDecisionView
	decodeInto(t, send(t, h, sign, http.MethodPost, path, map[string]any{"title": "the moment"}, http.StatusAccepted), &view)
	send(t, h, sign, http.MethodPost, path, map[string]any{}, http.StatusConflict)

	clips := jobsOfKind(t, srv.store, clipper.JobKind)
	if len(clips) != 1 {
		t.Fatalf("%d clip exports after two accepts, want exactly one", len(clips))
	}
	var params clipper.JobParams
	if err := json.Unmarshal(clips[0].Params, &params); err != nil {
		t.Fatal(err)
	}
	if params.Request.In != 10*time.Second || params.Request.Out != 40*time.Second {
		t.Errorf("clip %v..%v, want the suggestion's 10s..40s", params.Request.In, params.Request.Out)
	}
	got, _ := srv.store.GetHighlight(id)
	if got.State != db.HighlightAccepted || got.JobID != clips[0].ID || view.Highlight.JobID != clips[0].ID {
		t.Errorf("accepted highlight %+v, job %d", got, clips[0].ID)
	}
}

// An accept that cannot be planned is a 400 that leaves the suggestion
// pending: a typo in the layout must not eat the moment.
//
// MUTATION: the claim moves before the plan. Observed: FAIL, the suggestion is
// accepted with no clip.
func TestAnImpossibleAcceptLeavesTheSuggestionPending(t *testing.T) {
	h, sign, srv, rec := highlightFixture(t)
	id := plantHighlight(t, srv.store, rec, 10_000, 40_000)
	send(t, h, sign, http.MethodPost, "/api/v1/clipper/highlights/"+strconv.FormatInt(id, 10)+"/accept",
		map[string]any{"vertical": map[string]any{"layout": "diagonal"}}, http.StatusBadRequest)
	if got, _ := srv.store.GetHighlight(id); got.State != db.HighlightPending {
		t.Errorf("state %q after a refused accept", got.State)
	}
	if clips := jobsOfKind(t, srv.store, clipper.JobKind); len(clips) != 0 {
		t.Errorf("%d clips from a refused accept", len(clips))
	}
	send(t, h, sign, http.MethodPost, "/api/v1/clipper/highlights/999999/accept", map[string]any{}, http.StatusNotFound)
}

// A vertical export is the ordinary export job with a Vertical attached: the
// layout from the body, captions from the TRANSCRIPT over the clip's span (in
// timeline milliseconds, filtered to the tracks the short carries), the font
// from the server's own fonts directory, and a -vertical.mp4 name.
func TestAVerticalExportCarriesTheLayoutAndTheTranscriptsCaptions(t *testing.T) {
	h, sign, srv, rec := highlightFixture(t)
	body := map[string]any{
		"inMs": 10_000, "outMs": 40_000, "title": "short", "audioMode": "tracks", "tracks": []int{0},
		"vertical": map[string]any{
			"layout": "stacked", "face": map[string]any{"x": 0.7, "y": 0.05, "w": 0.25, "h": 0.3},
			"captions": map[string]any{"enabled": true, "style": map[string]any{"uppercase": true}},
		},
	}
	var job struct {
		ID int64 `json:"id"`
	}
	decodeInto(t, send(t, h, sign, http.MethodPost, recPath(rec, "/export"), body, http.StatusAccepted), &job)
	j, err := srv.store.GetJob(job.ID)
	if err != nil {
		t.Fatal(err)
	}
	var params clipper.JobParams
	if err := json.Unmarshal(j.Params, &params); err != nil {
		t.Fatal(err)
	}
	v := params.Request.Vertical
	if v == nil || v.Layout != clipper.LayoutStacked || v.Face.X != 0.7 || v.Focus != 0.5 {
		t.Fatalf("vertical %+v", v)
	}
	if !strings.HasSuffix(params.Request.OutPath, "-vertical.mp4") || params.Request.Mode != clipper.ModePrecise {
		t.Errorf("out %q mode %q", params.Request.OutPath, params.Request.Mode)
	}
	c := v.Captions
	if c == nil || len(c.Cues) != 1 || c.Cues[0].StartMS != 12_000 || c.Cues[0].Text != "hello there" || !c.Style.Uppercase {
		t.Fatalf("captions %+v (want only track 0's line inside 10s..40s)", c)
	}
	if filepath.Dir(c.FontFile) != mustAbs(t, srv.cfg.FontsDir()) || filepath.Base(c.FontFile) != clipper.DefaultCaptionFont {
		t.Errorf("font %q is not the fonts directory's %s", c.FontFile, clipper.DefaultCaptionFont)
	}

	// Planned at 70 seconds: still 200, still a short, and warned.
	body["outMs"] = 80_000
	var plan clipPlanView
	decodeInto(t, send(t, h, sign, http.MethodPost, recPath(rec, "/plan"), body, http.StatusOK), &plan)
	if !plan.Vertical || !strings.Contains(strings.Join(plan.Warnings, " "), "Shorts") {
		t.Errorf("a 70s short plans with a warning: %+v", plan)
	}
}

func mustAbs(t *testing.T, p string) string {
	t.Helper()
	a, err := filepath.Abs(p)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// The caption font is chosen from the fonts directory's listing; anything else
// is refused rather than becoming part of a path.
func TestACaptionFontMustBeOneTheServerHas(t *testing.T) {
	h, sign, _, rec := highlightFixture(t)
	for _, font := range []string{"Missing.ttf", "../../etc/passwd", "Inter-Bold.ttf/../x.ttf"} {
		send(t, h, sign, http.MethodPost, recPath(rec, "/plan"), map[string]any{
			"inMs": 0, "outMs": 10_000,
			"vertical": map[string]any{"layout": "blur", "captions": map[string]any{"enabled": true,
				"style": map[string]any{"font": font}}},
		}, http.StatusBadRequest)
	}
}

func TestClipTemplatesAreSavedValidatedAndListedWithTheEditorsOptions(t *testing.T) {
	h, sign, _, _ := highlightFixture(t)
	good := map[string]any{"name": "Face top", "vertical": map[string]any{
		"layout": "stacked", "face": map[string]any{"x": 0.7, "y": 0.05, "w": 0.25, "h": 0.3},
		"captions": map[string]any{"enabled": true, "style": map[string]any{"color": "#ffcc00"}}}}
	var saved clipTemplateView
	decodeInto(t, send(t, h, sign, http.MethodPost, "/api/v1/clipper/templates", good, http.StatusOK), &saved)
	send(t, h, sign, http.MethodPost, "/api/v1/clipper/templates",
		map[string]any{"name": "bad", "vertical": map[string]any{"layout": "diagonal"}}, http.StatusBadRequest)
	send(t, h, sign, http.MethodPost, "/api/v1/clipper/templates",
		map[string]any{"name": "  ", "vertical": map[string]any{"layout": "blur"}}, http.StatusBadRequest)
	send(t, h, sign, http.MethodPost, "/api/v1/clipper/templates",
		map[string]any{"name": "bad colour", "vertical": map[string]any{"layout": "blur",
			"captions": map[string]any{"enabled": true, "style": map[string]any{"color": "red"}}}}, http.StatusBadRequest)

	var list clipTemplatesView
	decodeInto(t, send(t, h, sign, http.MethodGet, "/api/v1/clipper/templates", nil, http.StatusOK), &list)
	if len(list.Templates) != 1 || list.Templates[0].Name != "Face top" ||
		list.Templates[0].Vertical.Captions.Style.Color != "#ffcc00" {
		t.Errorf("templates %+v", list.Templates)
	}
	if strings.Join(list.Layouts, ",") != "center,stacked,blur" || list.MaxShortSeconds != 60 || len(list.Fonts) == 0 {
		t.Errorf("editor options %+v", list)
	}
	path := "/api/v1/clipper/templates/" + strconv.FormatInt(saved.ID, 10)
	send(t, h, sign, http.MethodDelete, path, nil, http.StatusNoContent)
	send(t, h, sign, http.MethodDelete, path, nil, http.StatusNotFound)
}

// The scan's model is the Automod PRIMARY connector, when it is on and speaks
// the OpenAI-compatible protocol, and it is reached through automod.Complete --
// here against a fake provider, never a real one.
func TestTheHighlightModelIsTheAutomodPrimaryConnector(t *testing.T) {
	_, _, srv, _ := highlightFixture(t)
	model := HighlightModel(srv.store, srv.box, automod.NewBudget(), nil)
	if c, why := model(); c != nil || !strings.Contains(why, "switched off") {
		t.Errorf("default settings: %v %q", c != nil, why)
	}

	var got string
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		got, _ = body["model"].(string)
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{{"message": map[string]any{"content": `{"scores":[]}`}}}})
	}))
	defer fake.Close()

	set := func(m db.AutomodModel) {
		if _, err := srv.store.UpdateSettings(func(s *db.Settings) error { s.Automod.Model = m; return nil }); err != nil {
			t.Fatal(err)
		}
	}
	set(db.AutomodModel{Enabled: true, Protocol: db.AutomodProtocolJev, Endpoint: fake.URL, Model: "jev-1", MinConfidence: 0.8})
	if c, why := model(); c != nil || !strings.Contains(why, "Jev") {
		t.Errorf("a Jev primary: %v %q", c != nil, why)
	}
	set(db.AutomodModel{Enabled: true, Endpoint: fake.URL, Model: "fake-model", MinConfidence: 0.8})
	c, why := model()
	if c == nil {
		t.Fatalf("an OpenAI-compatible primary is a model: %q", why)
	}
	if out, err := c(context.Background(), "s", "u"); err != nil || out != `{"scores":[]}` || got != "fake-model" {
		t.Errorf("Complete through the connector = %q, %v (model %q)", out, err, got)
	}
}
