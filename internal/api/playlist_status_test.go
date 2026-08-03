package api

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/playlistmedia"
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

	// The operator deletes the second upload's file through the media page,
	// exactly as TestADeletedUploadDoesNotLockTheOperatorOutOfSettings does --
	// the item is now stale, pointing at nothing, and still on the playlist.
	send(t, h, sign, http.MethodDelete, "/api/v1/media/doomed.ts", nil, http.StatusNoContent)

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
