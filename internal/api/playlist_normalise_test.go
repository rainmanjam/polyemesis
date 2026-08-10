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
// Nothing in the handler deduplicates: the queue does, by folding a Unique
// submission onto an active job with the same Target, which
// playlistmedia.NormaliseTarget keys on the upload name. That is what this test
// pins -- a Target that stopped naming the upload, or a job that stopped being
// Unique, would show up here as three jobs.
//
// The mutations: delete the s.enqueuePlaylistNormalisation call in
// handlePutSettings and no job exists at all; drop `Unique: true` from
// NewNormaliseJob and a.ts is transcoded twice.
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

// The two halves of "a missing upload is a settings error" are tested against
// each other on purpose, because the rule is a boundary rather than a check:
// an item the operator is INTRODUCING must be refused, and an item they
// INHERITED must not be, or a deleted file locks them out of the settings page
// entirely. Either test alone would be satisfied by a check that is simply
// always on or always off.

// TestSavingAPlaylistItemThatNamesNoUploadIsRefused is the spec's rule, in the
// direction the spec states it.
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

// A playlist item may only name something the Library shows.
//
// The uploads directory holds two kinds of file the Library deliberately never
// lists: ".partial-" files, which are uploads whose bytes have landed and which
// are being probed right now and may be deleted in a second, and ".probe-"
// sidecars, which are JSON. Both exist on disk, so the os.Stat this validation
// used to be made of said yes to both -- and a playlist item naming one is a
// reference to something the operator cannot see in the Library and therefore
// cannot remove from it.
//
// The staged case is the one that matters most, because uploading is what
// creates it: for the length of a probe there is a real file on disk under a
// name that is about to stop existing.
//
// The mutation: drop the uploads.Listable call in playlistUploadProblems and
// both refusals below become 200.
func TestSavingAPlaylistItemNamingAStagedOrSidecarFileIsRefused(t *testing.T) {
	h, sign, srv, q := playlistJobServer(t)
	seedUpload(t, srv, "real.ts")

	// THE CONTROL FIRST. If an ordinary upload were refused too, the two
	// refusals below would prove nothing at all.
	savePlaylist(t, h, sign, []string{"real.ts"}, http.StatusOK)

	seedUpload(t, srv, ".partial-1234567.ts")
	seedUpload(t, srv, ".probe-real.ts.json")

	savePlaylist(t, h, sign, []string{"real.ts", ".partial-1234567.ts"}, http.StatusBadRequest)
	savePlaylist(t, h, sign, []string{"real.ts", ".probe-real.ts.json"}, http.StatusBadRequest)

	// The control save queued one normalisation for real.ts and the two refused
	// saves queued nothing more: a refused save must have no side effects.
	if got := normaliseJobs(t, q); len(got) != 1 {
		t.Errorf("normalise jobs = %d, want the 1 from the control save: %+v", len(got), got)
	}
}

// TestADeletedUploadDoesNotLockTheOperatorOutOfSettings is the other half, and
// it is about a check that was too strict rather than too loose.
//
// The sequence used to be entirely ordinary: save a playlist, delete the file
// through DELETE /api/v1/media/{name}. Task 5 closed that path -- the in-use
// guard on that endpoint now refuses exactly this case, see
// TestDeletingAnUploadAPlaylistNamesIsRefused in media_test.go -- so the file
// here is removed straight off disk instead, standing in for every OTHER way
// an upload can still disappear out from under a stored item (moved, pruned
// by something outside this process, or gone from before the guard shipped).
// The stored item now names nothing, exactly as it would have before Task 5.
//
// With existence checked on every item, EVERY subsequent settings save 400s
// -- with the playlist disabled, and for a change with nothing to do with the
// playlist -- and the operator cannot clear it, because the settings UI has
// no playlist control yet and the page GETs the whole document and PUTs it
// back, so the stale item round-trips untouched. That would brick the
// settings page with no in-product recovery.
//
// VALIDATION REJECTS WHAT THE OPERATOR IS INTRODUCING; IT MUST NOT PUNISH THEM
// FOR PRE-EXISTING STATE THEY HAVE NO CONTROL TO EDIT. The safety property is
// unaffected: engine.playlistItemsReady stats the resolved upload on every
// reconcile, so the playlist still cannot go on air.
//
// The mutation: drop the `inherited` skip in playlistUploadProblems -- so every
// item is checked again, which is what shipped -- and the unrelated save below
// comes back 400.
func TestADeletedUploadDoesNotLockTheOperatorOutOfSettings(t *testing.T) {
	h, sign, srv, _ := playlistJobServer(t)
	seedUpload(t, srv, "doomed.ts")

	// A perfectly good save, which is what makes the item pre-existing.
	savePlaylist(t, h, sign, []string{"doomed.ts"}, http.StatusOK)

	// The upload vanishes some way other than the API -- see the comment
	// above for why this is no longer a DELETE call. Nothing rewrites the
	// settings row.
	if err := os.Remove(filepath.Join(srv.cfg.DataDir, uploads.Dir, "doomed.ts")); err != nil {
		t.Fatalf("remove upload from disk: %v", err)
	}

	// An entirely unrelated change: the recorder's segment length. The playlist
	// block travels along untouched because the settings page always PUTs the
	// whole document.
	var s map[string]any
	decodeInto(t, send(t, h, sign, http.MethodGet, "/api/v1/settings", nil, http.StatusOK), &s)
	rec, _ := s["recording"].(map[string]any)
	if rec == nil {
		t.Fatal("settings carried no recording block")
	}
	rec["segmentSeconds"] = 900

	fo, _ := s["failover"].(map[string]any)
	pl, _ := fo["playlist"].(map[string]any)
	items, _ := pl["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("the stale item is not in the document, so this proves nothing: %v", pl["items"])
	}

	send(t, h, sign, http.MethodPut, "/api/v1/settings", s, http.StatusOK)

	// And the unrelated change really landed, rather than the 200 coming from
	// somewhere that skipped the write.
	var after map[string]any
	decodeInto(t, send(t, h, sign, http.MethodGet, "/api/v1/settings", nil, http.StatusOK), &after)
	got, _ := after["recording"].(map[string]any)
	if got == nil || got["segmentSeconds"] != float64(900) {
		t.Errorf("the unrelated setting did not persist: %v", got["segmentSeconds"])
	}
}

// TestANewItemIsStillRefusedWhenAnOldOneIsAlreadyBroken is the boundary itself:
// inheriting a broken item must not buy an operator the right to add another.
//
// Without this, "skip what is already stored" could be read as "skip
// everything once anything is broken", and the spec's rule would quietly stop
// applying to the very list it matters most for.
func TestANewItemIsStillRefusedWhenAnOldOneIsAlreadyBroken(t *testing.T) {
	h, sign, srv, _ := playlistJobServer(t)
	seedUpload(t, srv, "doomed.ts")

	savePlaylist(t, h, sign, []string{"doomed.ts"}, http.StatusOK)
	// Off disk directly, not through DELETE /api/v1/media/{name}: that
	// endpoint now refuses to remove an upload a stored item names. See
	// TestADeletedUploadDoesNotLockTheOperatorOutOfSettings above.
	if err := os.Remove(filepath.Join(srv.cfg.DataDir, uploads.Dir, "doomed.ts")); err != nil {
		t.Fatalf("remove upload from disk: %v", err)
	}

	// The stale item rides along, and the operator adds a second one that names
	// nothing either. The NEW one must still be refused.
	savePlaylist(t, h, sign, []string{"doomed.ts", "also-missing.ts"}, http.StatusBadRequest)
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
