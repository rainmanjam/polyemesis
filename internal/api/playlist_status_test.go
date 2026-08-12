package api

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/playlistmedia"
	"github.com/rainmanjam/polyemesis/internal/uploads"
)

// Readiness is OBSERVED state and must not ride the settings blob.
//
// The settings document travels outward on every read and SettingsPage PUTs
// back what it GOT, so a derived read-only field in that payload is something
// the UI sends back as configuration -- which is how B1's lockout happened. Its
// own GET, for the same reason handlePutMQTTPassword has its own PUT.
//
// The mutation: add Items to what GET /settings serves and this fails.
//
// Seeds the playlist item through the store directly rather than through
// PUT /settings: the point of this test is what GET hands back, and routing
// through the PUT handler as well would let an unrelated strictness there
// (unknown-field rejection) fail the test for the wrong reason.
func TestReadinessIsNotOnTheSettingsBlob(t *testing.T) {
	h, sign, srv, _ := playlistJobServer(t)
	seedUpload(t, srv, "a.ts")

	settings, err := srv.store.GetSettings()
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	settings.Failover.Playlist.Items = []db.PlaylistItem{{Upload: "a.ts"}}
	if err := srv.store.PutSettings(settings); err != nil {
		t.Fatalf("PutSettings: %v", err)
	}

	body := string(send(t, h, sign, http.MethodGet, "/api/v1/settings", nil, http.StatusOK))
	if strings.Contains(body, `"state"`) {
		t.Error("per-item readiness is on the settings payload, which the UI PUTs " +
			"back verbatim; it belongs on GET /failover/playlist")
	}
}

// A failed item must be NAMED. "The playlist is not on air" with eleven items
// and no indication which one is the silent-never-starts failure the spec calls
// the worst outcome, moved one layer up.
//
// The mutation: return a bare boolean and this fails.
func TestAnItemNeedingAttentionIsNamedWithAReason(t *testing.T) {
	h, sign, srv, q := playlistJobServer(t)
	seedUpload(t, srv, "ready.ts")
	seedUpload(t, srv, "doomed.ts")

	// ready.ts already has its derivative -- seeded directly on disk, exactly
	// as TestAnItemThatIsAlreadyNormalisedIsNotQueuedAgain does, so this test
	// is not itself waiting on a transcode.
	derivative := playlistmedia.DerivativePath(srv.cfg.DataDir, "ready.ts")
	if err := os.MkdirAll(filepath.Dir(derivative), 0o755); err != nil {
		t.Fatalf("mkdir derivative dir: %v", err)
	}
	if err := os.WriteFile(derivative, []byte("already normalised"), 0o644); err != nil {
		t.Fatalf("seed derivative: %v", err)
	}

	savePlaylist(t, h, sign, []string{"ready.ts", "doomed.ts"}, http.StatusOK)

	// The save above queued a normalisation for doomed.ts, and nothing in this
	// test runs the queue, so it would still read as "transcoding" -- correctly,
	// per the state precedence (ready, then transcoding, then attention): a job
	// really is still active. Cancel it, the way an operator's own cancel
	// button would, so what is left to observe is the state this test is about.
	got := normaliseJobs(t, q)
	for _, j := range got {
		if j.Target == playlistmedia.NormaliseTarget("doomed.ts") {
			if err := q.Cancel(j.ID); err != nil {
				t.Fatalf("cancel job %d: %v", j.ID, err)
			}
		}
	}

	// The upload goes away OUT OF BAND -- removed from disk rather than through
	// the media endpoint.
	//
	// It used to be a DELETE, and that stopped working the moment the in-use
	// guard landed: DELETE /api/v1/media/{name} now REFUSES an upload a playlist
	// item names, with 409, precisely so an operator cannot strand a playlist
	// this way. Which leaves out-of-band removal as the only route to the state
	// this test is about -- a sweep, a tidied disk, a restore that missed a file
	// -- and that is worth knowing rather than papering over: the API can refuse
	// to CREATE this state, and can still be asked to REPORT it.
	if err := os.Remove(filepath.Join(srv.cfg.DataDir, uploads.Dir, "doomed.ts")); err != nil {
		t.Fatalf("remove the upload out of band: %v", err)
	}

	var status PlaylistStatus
	decodeInto(t, send(t, h, sign, http.MethodGet, "/api/v1/failover/playlist", nil, http.StatusOK), &status)

	if status.Ready {
		t.Error("Ready is true with an item whose upload is gone")
	}
	if len(status.Items) != 2 {
		t.Fatalf("got %d items, want 2: %+v", len(status.Items), status.Items)
	}
	if status.Items[0].State != "ready" {
		t.Errorf("items[0] (ready.ts, already normalised) state = %q, want ready: %+v",
			status.Items[0].State, status.Items[0])
	}
	if status.Items[1].State != "attention" {
		t.Errorf("items[1] (doomed.ts, upload deleted) state = %q, want attention: %+v",
			status.Items[1].State, status.Items[1])
	}
	if !strings.Contains(status.Items[1].Detail, "doomed.ts") {
		t.Errorf("items[1].Detail does not name the offending upload: %q", status.Items[1].Detail)
	}
}

// A queued-but-not-yet-run item is "transcoding", not "attention" -- the
// operator asked for exactly this and it is in progress, not broken.
func TestAQueuedItemIsTranscodingNotAttention(t *testing.T) {
	h, sign, srv, q := playlistJobServer(t)
	seedUpload(t, srv, "big.ts")

	savePlaylist(t, h, sign, []string{"big.ts"}, http.StatusOK)

	got := normaliseJobs(t, q)
	if len(got) != 1 {
		t.Fatalf("got %d normalisation jobs, want 1 (the save above should have queued one): %+v", len(got), got)
	}

	var status PlaylistStatus
	decodeInto(t, send(t, h, sign, http.MethodGet, "/api/v1/failover/playlist", nil, http.StatusOK), &status)

	if status.Ready {
		t.Error("Ready is true while an item is still transcoding")
	}
	if len(status.Items) != 1 || status.Items[0].State != "transcoding" {
		t.Errorf("items = %+v, want a single transcoding entry", status.Items)
	}
	if status.Items[0].Detail != "" {
		t.Errorf("a transcoding item carries a Detail, which reads as a problem: %q", status.Items[0].Detail)
	}
}

// A ready playlist -- every item normalised -- reports Ready at the top
// level, not just per item.
func TestAFullyNormalisedPlaylistIsReady(t *testing.T) {
	h, sign, srv, _ := playlistJobServer(t)
	seedUpload(t, srv, "one.ts")
	seedUpload(t, srv, "two.ts")

	for _, name := range []string{"one.ts", "two.ts"} {
		derivative := playlistmedia.DerivativePath(srv.cfg.DataDir, name)
		if err := os.MkdirAll(filepath.Dir(derivative), 0o755); err != nil {
			t.Fatalf("mkdir derivative dir: %v", err)
		}
		if err := os.WriteFile(derivative, []byte("already normalised"), 0o644); err != nil {
			t.Fatalf("seed derivative %s: %v", name, err)
		}
	}

	savePlaylist(t, h, sign, []string{"one.ts", "two.ts"}, http.StatusOK)

	var status PlaylistStatus
	decodeInto(t, send(t, h, sign, http.MethodGet, "/api/v1/failover/playlist", nil, http.StatusOK), &status)

	if !status.Ready {
		t.Errorf("Ready is false with every item already normalised: %+v", status.Items)
	}
	for _, it := range status.Items {
		if it.State != "ready" {
			t.Errorf("item %+v is not ready", it)
		}
	}
}

// An empty playlist is not ready -- there is nothing for the selector to put
// on air, the same reading playlistSig gives an enabled playlist with no
// items.
func TestAnEmptyPlaylistIsNotReady(t *testing.T) {
	h, sign, _, _ := playlistJobServer(t)

	var status PlaylistStatus
	decodeInto(t, send(t, h, sign, http.MethodGet, "/api/v1/failover/playlist", nil, http.StatusOK), &status)

	if status.Ready {
		t.Error("an empty playlist reported Ready")
	}
	if len(status.Items) != 0 {
		t.Errorf("an empty playlist reported items: %+v", status.Items)
	}
}

// seedDerivative writes a non-empty derivative, which is the ONE question
// readiness asks. Non-empty matters: all four readers of this path treat
// zero-length as absent.
func seedDerivative(t *testing.T, srv *Server, name string) {
	t.Helper()
	p := playlistmedia.DerivativePath(srv.cfg.DataDir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatalf("mkdir derivative dir: %v", err)
	}
	if err := os.WriteFile(p, []byte("already normalised"), 0o644); err != nil {
		t.Fatalf("seed derivative %s: %v", name, err)
	}
}

// A refusal recorded AFTER a derivative exists does not take the item off air.
//
// This is #273's on-air half, and the decision it records is deliberate: the
// derivative was transcoded from those bytes and is itself intact, so pulling
// it off air would black out a running programme to report a fact the operator
// can act on whenever they like. The finding still has to reach them, which is
// what Warning is for.
//
// The three assertions are separable on purpose, because the bug this guards
// has two distinct wrong answers: reporting nothing (the state before this
// change), and reporting it by breaking readiness.
//
// The mutation: delete the `if refused` block in playlistItemStatus's ready
// branch and the Warning assertion fails while the other two still pass. Set
// `st.State = playlistItemAttention` inside that block instead and the State
// and Ready assertions fail while Warning still passes.
func TestARefusalRecordedAfterNormalisationLeavesTheItemOnAir(t *testing.T) {
	h, sign, srv, _ := playlistJobServer(t)
	seedUpload(t, srv, "onair-abcd1234.ts")
	seedDerivative(t, srv, "onair-abcd1234.ts")
	savePlaylist(t, h, sign, []string{"onair-abcd1234.ts"}, http.StatusOK)

	// Recorded AFTER the save, which is the whole shape of this bug: the item
	// passed playlistUploadProblems when it was introduced, and the re-verify
	// job refused it later. Seeding the refusal first would test the
	// introduction gate instead, which already refuses and is not this.
	seedVerdict(t, srv, "onair-abcd1234.ts", uploads.RefusedVerdict("not media"))

	var status PlaylistStatus
	decodeInto(t, send(t, h, sign, http.MethodGet, "/api/v1/failover/playlist", nil, http.StatusOK), &status)

	if len(status.Items) != 1 {
		t.Fatalf("want 1 item, got %+v", status.Items)
	}
	it := status.Items[0]
	if it.State != playlistItemReady {
		t.Errorf("a refused upload with a derivative left the item %q; it must stay ready and keep playing", it.State)
	}
	if !status.Ready {
		t.Error("Ready went false over a refusal whose derivative is intact; the programme would go off air")
	}
	if !strings.Contains(it.Warning, "refused") || !strings.Contains(it.Warning, "not media") {
		t.Errorf("Warning does not carry the refusal or its reason: %q", it.Warning)
	}
}

// A refusal with NO derivative is the item's cause, and it outranks the
// queue-state sentences that would otherwise be reported.
//
// "not yet queued for normalisation" is true and useless here: normalisation
// cannot fix a refusal, and an operator told to wait will wait for ever. The
// remedy differs from the on-air case above precisely because there is nothing
// playing to keep.
//
// The mutation: delete the `if refused` block above the failed-job lookup and
// Detail falls back to "not yet queued for normalisation", failing both the
// substring assertions below.
func TestARefusedItemWithNoDerivativeSaysSoRatherThanBlamingTheQueue(t *testing.T) {
	h, sign, srv, _ := playlistJobServer(t)
	seedUpload(t, srv, "refused-abcd1234.ts")
	savePlaylist(t, h, sign, []string{"refused-abcd1234.ts"}, http.StatusOK)
	seedVerdict(t, srv, "refused-abcd1234.ts", uploads.RefusedVerdict("not media"))

	var status PlaylistStatus
	decodeInto(t, send(t, h, sign, http.MethodGet, "/api/v1/failover/playlist", nil, http.StatusOK), &status)

	if len(status.Items) != 1 {
		t.Fatalf("want 1 item, got %+v", status.Items)
	}
	it := status.Items[0]
	if it.State != playlistItemAttention {
		t.Errorf("a refused upload with no derivative reported %q, want attention", it.State)
	}
	if !strings.Contains(it.Detail, "refused") || !strings.Contains(it.Detail, "not media") {
		t.Errorf("Detail does not name the refusal or its reason: %q", it.Detail)
	}
	if strings.Contains(it.Detail, "not yet queued") {
		t.Errorf("Detail blamed the queue for a refusal the queue cannot fix: %q", it.Detail)
	}
	if it.Warning != "" {
		t.Errorf("an item that cannot play carries a Warning as well as a Detail: %q", it.Warning)
	}
}

// An UNCHECKED upload is not a refusal and gets no warning.
//
// #255's asymmetry, one layer up: an upload nobody managed to inspect is a
// fact about THIS SERVER -- on a box with no ffprobe every upload looks
// unchecked -- while a refusal exists only where an inspection ran and read
// the bytes. Without this distinction every item on such a box would carry a
// warning accusing the operator's files.
//
// The mutation: relax uploadRefusal's `v.Outcome != uploads.OutcomeRefused`
// check to `!v.Verified()` and this fails while both tests above still pass.
func TestAnUncheckedUploadIsNotReportedAsRefused(t *testing.T) {
	h, sign, srv, _ := playlistJobServer(t)
	seedUpload(t, srv, "unchecked-abcd1234.ts")
	seedDerivative(t, srv, "unchecked-abcd1234.ts")
	savePlaylist(t, h, sign, []string{"unchecked-abcd1234.ts"}, http.StatusOK)
	seedVerdict(t, srv, "unchecked-abcd1234.ts", uploads.UnverifiedVerdict("nobody read this"))

	var status PlaylistStatus
	decodeInto(t, send(t, h, sign, http.MethodGet, "/api/v1/failover/playlist", nil, http.StatusOK), &status)

	if len(status.Items) != 1 {
		t.Fatalf("want 1 item, got %+v", status.Items)
	}
	if w := status.Items[0].Warning; w != "" {
		t.Errorf("an unchecked upload was reported as refused: %q", w)
	}
}
