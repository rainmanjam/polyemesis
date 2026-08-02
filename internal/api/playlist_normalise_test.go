package api

import (
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/jobs"
	"github.com/rainmanjam/polyemesis/internal/playlistmedia"
	"github.com/rainmanjam/polyemesis/internal/uploads"
)

// Saving a playlist is the only thing in the product that asks for a normalised
// derivative, and the engine will not put a playlist on air without one. These
// tests are about that submission, and about the existence check the spec makes
// a settings error, both of which have to happen where the uploads store is --
// which is here, not in internal/db.

// playlistJobServer is sourceServer with a real queue attached, since the
// submission is the thing under test.
func playlistJobServer(t *testing.T) (http.Handler, func(*http.Request), *Server, *jobs.Queue) {
	t.Helper()
	h, store, sign := sourceServer(t)
	srv := serverUnderTest(t, h)
	q := jobs.New(slog.New(slog.NewTextHandler(io.Discard, nil)), store)
	srv.jobq = q
	return h, sign, srv, q
}

// seedUpload writes a stored upload the settings handler will find.
func seedUpload(t *testing.T, srv *Server, name string) {
	t.Helper()
	dir := filepath.Join(srv.cfg.DataDir, uploads.Dir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir uploads: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
		t.Fatalf("seed upload %q: %v", name, err)
	}
}

// savePlaylist round-trips the settings document with the given items, exactly
// as a client has to: PUT /settings REPLACES the document, so a lone failover
// block would blank everything else.
func savePlaylist(t *testing.T, h http.Handler, sign func(*http.Request), uploadNames []string, wantStatus int) {
	t.Helper()
	var s map[string]any
	decodeInto(t, send(t, h, sign, http.MethodGet, "/api/v1/settings", nil, http.StatusOK), &s)

	fo, _ := s["failover"].(map[string]any)
	if fo == nil {
		t.Fatal("settings carried no failover block")
	}
	pl, _ := fo["playlist"].(map[string]any)
	if pl == nil {
		t.Fatal("settings carried no failover.playlist block")
	}
	items := make([]any, 0, len(uploadNames))
	for _, name := range uploadNames {
		items = append(items, map[string]any{"upload": name})
	}
	pl["items"] = items

	send(t, h, sign, http.MethodPut, "/api/v1/settings", s, wantStatus)
}

// normaliseJobs returns the queued normalisations, newest first.
func normaliseJobs(t *testing.T, q *jobs.Queue) []jobs.Job {
	t.Helper()
	all, err := q.List(jobs.Filter{})
	if err != nil {
		t.Fatalf("list jobs: %v", err)
	}
	var out []jobs.Job
	for _, j := range all {
		if j.Kind == playlistmedia.KindNormalise {
			out = append(out, j)
		}
	}
	return out
}

// TestSavingAPlaylistQueuesOneNormalisationPerUpload is the guard on the
// branch's Critical finding: internal/playlistmedia had no production
// submitter at all, so no derivative was ever written and the readiness gate
// was a permanent refusal.
//
// ONE PER UPLOAD, not per entry. The same file listed twice must produce one
// transcode -- the derivative is keyed on the upload, an hour of CPU per
// duplicate entry is an hour wasted, and B2's concat list will name the same
// derivative twice quite happily.
//
// The mutation: delete the s.enqueuePlaylistNormalisation call in
// handlePutSettings and no job exists. Drop the `seen` map and the same upload
// listed twice submits twice.
func TestSavingAPlaylistQueuesOneNormalisationPerUpload(t *testing.T) {
	h, sign, srv, q := playlistJobServer(t)
	seedUpload(t, srv, "a.ts")
	seedUpload(t, srv, "b.ts")

	// a.ts twice, deliberately.
	savePlaylist(t, h, sign, []string{"a.ts", "b.ts", "a.ts"}, http.StatusOK)

	got := normaliseJobs(t, q)
	if len(got) != 2 {
		t.Fatalf("got %d normalisation jobs for two distinct uploads across three "+
			"entries, want 2: %+v", len(got), got)
	}
	targets := map[string]bool{}
	for _, j := range got {
		targets[j.Target] = true
	}
	for _, name := range []string{"a.ts", "b.ts"} {
		if !targets[playlistmedia.NormaliseTarget(name)] {
			t.Errorf("no normalisation job for %q; its playlist can never become ready", name)
		}
	}
}

// TestAnItemThatIsAlreadyNormalisedIsNotQueuedAgain keeps the job history from
// growing by one row per playlist item on every unrelated settings save.
//
// The queue's Unique fold does not cover this: it looks only at ACTIVE jobs, so
// a normalisation that finished last week suppresses nothing.
//
// The mutation: delete the os.Stat on DerivativePath in
// enqueuePlaylistNormalisation and a job appears for work already done.
func TestAnItemThatIsAlreadyNormalisedIsNotQueuedAgain(t *testing.T) {
	h, sign, srv, q := playlistJobServer(t)
	seedUpload(t, srv, "done.ts")

	derivative := playlistmedia.DerivativePath(srv.cfg.DataDir, "done.ts")
	if err := os.MkdirAll(filepath.Dir(derivative), 0o755); err != nil {
		t.Fatalf("mkdir derivative dir: %v", err)
	}
	if err := os.WriteFile(derivative, []byte("already normalised"), 0o644); err != nil {
		t.Fatalf("seed derivative: %v", err)
	}

	savePlaylist(t, h, sign, []string{"done.ts"}, http.StatusOK)

	if got := normaliseJobs(t, q); len(got) != 0 {
		t.Errorf("an item whose derivative is already on disk was queued again: %+v", got)
	}
}

// TestSavingAPlaylistItemThatNamesNoUploadIsRefused is the spec's "a missing
// upload is a settings error", which nothing implemented.
//
// Without it the save returns 200, the playlist never becomes available, and
// the only trace anywhere is an Info log line saying "not every item has been
// normalised yet" -- the wrong sentence entirely for a file that does not
// exist. The spec names that outcome as the worst one the feature has.
//
// The mutation: delete the s.playlistUploadProblems call in handlePutSettings
// and this returns 200.
func TestSavingAPlaylistItemThatNamesNoUploadIsRefused(t *testing.T) {
	h, sign, srv, q := playlistJobServer(t)
	seedUpload(t, srv, "real.ts")

	savePlaylist(t, h, sign, []string{"real.ts", "never-uploaded.ts"}, http.StatusBadRequest)

	// And nothing was queued for the half of the list that was fine: a refused
	// save must not have side effects.
	if got := normaliseJobs(t, q); len(got) != 0 {
		t.Errorf("a refused settings save still queued work: %+v", got)
	}
}

// The refusal has to name the item, or an operator with a twelve-item playlist
// is told only that something is wrong.
func TestARefusedPlaylistItemIsNamedInTheError(t *testing.T) {
	h, sign, srv, _ := playlistJobServer(t)
	seedUpload(t, srv, "real.ts")

	var s map[string]any
	decodeInto(t, send(t, h, sign, http.MethodGet, "/api/v1/settings", nil, http.StatusOK), &s)
	fo, _ := s["failover"].(map[string]any)
	pl, _ := fo["playlist"].(map[string]any)
	pl["items"] = []any{
		map[string]any{"upload": "real.ts"},
		map[string]any{"upload": "ghost.ts"},
	}

	body := string(send(t, h, sign, http.MethodPut, "/api/v1/settings", s, http.StatusBadRequest))
	if !strings.Contains(body, "ghost.ts") {
		t.Errorf("the error does not name the offending upload: %s", body)
	}
	if !strings.Contains(body, "item 1") {
		t.Errorf("the error does not say WHICH item is wrong: %s", body)
	}
}

// A server with no queue wired still has to save settings. Every part of the
// post-production tier is optional by construction, and a playlist that cannot
// be normalised is a feature that does not start -- not a settings page that
// 500s.
func TestSavingAPlaylistWithNoQueueWiredStillSaves(t *testing.T) {
	h, store, sign := sourceServer(t)
	srv := serverUnderTest(t, h)
	srv.jobq = nil
	seedUpload(t, srv, "lonely.ts")

	savePlaylist(t, h, sign, []string{"lonely.ts"}, http.StatusOK)

	s, err := store.GetSettings()
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	if len(s.Failover.Playlist.Items) != 1 {
		t.Fatalf("the playlist was not stored: %+v", s.Failover.Playlist.Items)
	}
}
