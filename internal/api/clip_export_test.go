package api

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/clipper"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/jobs"
)

// TestClipExportSubmitsAJobCarryingTheSegments is about the SUBMISSION, not the
// cut. No worker is registered: executing a clip export is internal/clipper's
// subject and needs a real FFmpeg.
//
// What is asserted is the three decisions handleClipExport makes that nothing
// downstream can recover if it gets them wrong:
//
//   - the SEGMENTS travel with the job. The comment on the params says why: a
//     re-index between submission and execution must not change what gets cut
//     out from under the user. A job submitted without them resolves its
//     timeline again an hour later, from a different index.
//   - the priority is PriorityUser, because a human is watching this one.
//   - it is deliberately NOT Unique. Two different in-points out of one
//     recording are two different clips, and folding the second into the first
//     silently throws away the export somebody just asked for.
func TestClipExportSubmitsAJobCarryingTheSegments(t *testing.T) {
	h, sign, store := jobsFixture(t)
	srv := serverUnderTest(t, h)

	dir := srv.eng().Recordings().Dir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir recordings: %v", err)
	}
	const name = "rec-20240115-090000.mkv"
	if err := os.WriteFile(filepath.Join(dir, name), []byte("MASTER"), 0o644); err != nil {
		t.Fatalf("seed master: %v", err)
	}
	if err := store.UpsertRecording(&db.Recording{
		Filename:   name,
		StartedAt:  time.Now().Add(-time.Hour),
		DurationMS: 600_000,
		Tracks:     2,
	}); err != nil {
		t.Fatalf("index master: %v", err)
	}
	recs, err := store.ListRecordings()
	if err != nil || len(recs) == 0 {
		t.Fatalf("ListRecordings: %v", err)
	}
	rec := recs[0]

	export := func(inMS, outMS int64, title string) *jobs.Job {
		t.Helper()
		var view struct {
			ID int64 `json:"id"`
		}
		decodeInto(t, send(t, h, sign, http.MethodPost,
			"/api/v1/clipper/recordings/"+strconv.FormatInt(rec.ID, 10)+"/export",
			map[string]any{"inMs": inMS, "outMs": outMS, "title": title},
			http.StatusAccepted), &view)
		j, err := store.GetJob(view.ID)
		if err != nil {
			t.Fatalf("the export answered with job %d, which does not exist: %v", view.ID, err)
		}
		return j
	}

	job := export(10_000, 25_000, "first")

	if job.Kind != clipper.JobKind {
		t.Errorf("submitted kind %q, want %q", job.Kind, clipper.JobKind)
	}
	if job.Priority != jobs.PriorityUser {
		t.Errorf("submitted at priority %d, want PriorityUser (%d): a human is "+
			"watching this one", job.Priority, jobs.PriorityUser)
	}

	var params clipper.JobParams
	if err := json.Unmarshal(job.Params, &params); err != nil {
		t.Fatalf("the job's params are not clipper.JobParams: %v (%s)", err, job.Params)
	}
	if len(params.Segments) == 0 {
		t.Fatalf("the job carries no segments, so the worker will re-resolve the "+
			"timeline from the index an hour later and may cut something else: %s",
			job.Params)
	}
	if got := filepath.Base(params.Segments[0].Path); got != name {
		t.Errorf("the job's first segment is %q, want the recording it was cut from (%q)",
			got, name)
	}
	if params.Request.In != 10*time.Second || params.Request.Out != 25*time.Second {
		t.Errorf("the job asks for %v..%v, want 10s..25s", params.Request.In, params.Request.Out)
	}

	// A second export of a DIFFERENT range out of the same recording must be a
	// second job. Unique would fold it into the first and lose it.
	second := export(40_000, 55_000, "second")
	if second.ID == job.ID {
		t.Fatalf("a second export of a different range was folded into job %d; "+
			"two in-points out of one recording are two clips", job.ID)
	}
	var secondParams clipper.JobParams
	if err := json.Unmarshal(second.Params, &secondParams); err != nil {
		t.Fatalf("decode second params: %v", err)
	}
	if secondParams.Request.In != 40*time.Second {
		t.Errorf("the second job asks for in=%v, want 40s", secondParams.Request.In)
	}
}
