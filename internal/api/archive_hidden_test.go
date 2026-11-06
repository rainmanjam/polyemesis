package api

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/jobs"
	"github.com/rainmanjam/polyemesis/internal/media"
)

// Archive compression is registered but has no way to be switched on, so the
// product hides it: no new job may be queued, the jobs page does not offer it,
// and a job stored by an earlier build still lists under its proper name.
func TestArchiveCompressionIsRefusedAtSubmit(t *testing.T) {
	h, sign, srv, _ := playlistJobServer(t)
	if err := srv.store.UpsertRecording(&db.Recording{Filename: "rec-1.mkv", StartedAt: time.Now().Add(-90 * 24 * time.Hour), DurationMS: 60000}); err != nil {
		t.Fatal(err)
	}
	recs, _ := srv.store.ListRecordings()
	path := "/api/v1/library/recordings/" + itoa(recs[0].ID) + "/jobs/" + string(media.KindArchive)
	msg := mustJSONError(t, h, sign, http.MethodPost, path,
		map[string]any{"acknowledgeLossy": true}, http.StatusBadRequest)
	if !strings.Contains(msg, "archive compression is not available yet") {
		t.Errorf("refusal %q does not say archive compression is not available yet", msg)
	}
	list, err := srv.store.ListJobs(jobs.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Errorf("a refused archive submit still queued %d job(s)", len(list))
	}
}

func TestArchiveCompressionIsNotOnTheJobsPageCatalogue(t *testing.T) {
	for _, info := range kindInfo(db.PostProdSettings{}, nil) {
		if info.Kind == string(media.KindArchive) {
			t.Errorf("the jobs page catalogue still offers %q", info.Kind)
		}
	}
	for _, k := range CataloguedKinds() {
		if k == media.KindArchive {
			t.Errorf("CataloguedKinds still lists %q", k)
		}
	}
}

func TestAStoredArchiveJobStillListsUnderItsOwnLabel(t *testing.T) {
	if got := kindLabel(media.KindArchive); got != "Archive compression" {
		t.Errorf("a stored archive job is labelled %q", got)
	}
}
