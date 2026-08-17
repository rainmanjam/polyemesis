package oauth

// The post-broadcast pair: changing an archive's privacy, and filing it in a
// playlist.
//
// These assert on the REQUEST BODY rather than on the returned value wherever
// the body is the hazard, because the body is the only thing YouTube sees. The
// privacy test is the one that matters most in this file: a videos.update that
// carries a snippet deletes the title, description and tags off a broadcast
// somebody has already finished, and no assertion on a return value can notice
// that.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// ytReply is one scripted answer to a playlistItems insert.
type ytReply struct {
	status int
	body   string
}

// ytRefusal builds a YouTube error body carrying a named reason.
//
// The message is DELIBERATELY LONG and sits ahead of the reason, so the reason
// lands well past the 300th byte. A refusal from Google runs past 300
// characters routinely, and 300 is where statusError truncates the body it
// SHOWS -- so a reason parsed out of the truncated copy is not merely missing,
// it is unparseable, because the cut lands mid-string. That is a bug this
// repository has already shipped once (see statusError.full in metadata.go),
// and the short fixtures in place at the time did not catch it.
func ytRefusal(status int, reason string) ytReply {
	long := strings.Repeat("The request cannot be completed as submitted. ", 9)
	b, err := json.Marshal(map[string]any{"error": map[string]any{
		"code":    status,
		"message": long,
		"errors": []map[string]any{{
			"domain":  "youtube.playlistItem",
			"reason":  reason,
			"message": long,
		}},
	}})
	if err != nil {
		panic(err)
	}
	return ytReply{status: status, body: string(b)}
}

// ytVODStub serves the two post-broadcast endpoints.
//
// replies is the script for playlistItems.insert, consumed in order; once it
// runs out the LAST entry repeats, so "refused forever" needs one entry and
// "refused then accepted" needs two. The provider comes back with a zero retry
// wait: the retry path has to be driven, and driving it at the production
// cadence would cost this test the better part of a minute.
func ytVODStub(t *testing.T, log *[]capture, replies ...ytReply) *YouTube {
	t.Helper()
	var n int
	srv := httptest.NewServer(recordAll(t, log, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPut && r.URL.Path == "/videos":
			io.WriteString(w, `{"id":"vid"}`)
		case r.Method == http.MethodPost && r.URL.Path == "/playlistItems":
			if len(replies) == 0 {
				io.WriteString(w, `{"id":"item-1"}`)
				return
			}
			rep := replies[min(n, len(replies)-1)]
			n++
			w.WriteHeader(rep.status)
			io.WriteString(w, rep.body)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	y := NewYouTube(WithBaseURL(srv.URL))
	y.vodRetry = []time.Duration{0, 0}
	return y
}

func ytCount(log []capture, method, path string) int {
	var n int
	for _, c := range log {
		if c.Method == method && c.Path == path {
			n++
		}
	}
	return n
}

// THE TEST THIS FILE EXISTS FOR.
//
// videos.update is destructive: "If your request does not specify a value for a
// property that already has a value, the property's existing value will be
// deleted." A privacy-only change must therefore send part=status ALONE and
// must not carry a snippet -- a snippet in the body makes snippet.title and
// snippet.categoryId required, and wipes every snippet field left out of it off
// somebody's finished broadcast.
//
// MUTATION M1, internal/oauth/youtube_vod.go in SetVODPrivacy:
// add `"snippet": map[string]any{"title": "x"}` to the body.
// Observed: FAIL -- "the request carries a snippet".
//
// MUTATION M2, same function: `?part=status` -> `?part=status,snippet`.
// Observed: FAIL -- part = "part=status,snippet", want part=status alone.
func TestAVODPrivacyChangeSendsPartStatusAloneAndCarriesNoSnippet(t *testing.T) {
	var log []capture
	y := ytVODStub(t, &log)

	if err := y.SetVODPrivacy(context.Background(), "tok", "vid", db.PrivacyUnlisted); err != nil {
		t.Fatalf("SetVODPrivacy: %v", err)
	}

	c := find(log, http.MethodPut, "/videos")
	if c == nil {
		t.Fatal("no videos.update happened at all")
	}
	if c.Query != "part=status" {
		t.Errorf("part = %q, want exactly \"part=status\": any other part is one whose fields "+
			"YouTube resets to their defaults when the body does not carry them", c.Query)
	}
	if _, ok := c.Body["snippet"]; ok {
		t.Errorf("the request carries a snippet: %v\n\n"+
			"videos.update deletes every property of a part it is sending that the body does not "+
			"specify, so a snippet here removes the title, the description and the tags from a "+
			"broadcast that has already finished.", c.Body)
	}
	if c.Body["id"] != "vid" {
		t.Errorf("id = %v, want the video id", c.Body["id"])
	}
	st, _ := c.Body["status"].(map[string]any)
	if st == nil || st["privacyStatus"] != string(db.PrivacyUnlisted) {
		t.Errorf("status = %v, want privacyStatus %q; part=status carrying no privacyStatus is the "+
			"request that removes the setting entirely", c.Body["status"], db.PrivacyUnlisted)
	}
	if len(st) != 1 {
		t.Errorf("status carries %v; only the one property the evidence documents as writable "+
			"belongs here", st)
	}
}

// "Leave it alone" is expressible only as sending nothing. A bare part=status
// is the exact request that reverts the video to the default privacy, so an
// unchanged privacy must produce no call rather than an empty one.
func TestAnUnchangedPrivacyMakesNoRequestAtAll(t *testing.T) {
	var log []capture
	y := ytVODStub(t, &log)

	if err := y.SetVODPrivacy(context.Background(), "tok", "vid", db.PrivacyUnchanged); err != nil {
		t.Fatalf("SetVODPrivacy: %v", err)
	}
	if len(log) != 0 {
		t.Fatalf("an unchanged privacy sent %d request(s): %v", len(log), log)
	}
}

func TestAPrivacyChangeRefusesWhatYouTubeWouldRefuseWithoutSpendingTheRequest(t *testing.T) {
	tests := []struct {
		name    string
		videoID string
		privacy db.PrivacyStatus
		want    string
	}{
		{"an unrecorded video id", "", db.PrivacyPrivate, "no YouTube video id"},
		{"a privacy value YouTube does not have", "vid", db.PrivacyStatus("hidden"), "not a YouTube privacy status"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var log []capture
			y := ytVODStub(t, &log)

			err := y.SetVODPrivacy(context.Background(), "tok", tc.videoID, tc.privacy)
			if err == nil {
				t.Fatal("accepted a call YouTube would refuse")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not say %q", err, tc.want)
			}
			if len(log) != 0 {
				t.Errorf("spent %d request(s) to learn what was knowable here: %v", len(log), log)
			}
		})
	}
}

// The insert shape, asserted on the body for the same reason as above.
//
// snippet.position is absent deliberately: it is optional, and manualSortRequired
// is documented to be fixed "by removing the snippet.position element", so the
// element that is never sent is the refusal that can never happen.
func TestAPlaylistInsertSendsTheDocumentedShapeAndNoPosition(t *testing.T) {
	var log []capture
	y := ytVODStub(t, &log)

	res, err := y.AddVODToPlaylist(context.Background(), "tok", "PL123", "vid")
	if err != nil {
		t.Fatalf("AddVODToPlaylist: %v", err)
	}
	if !res.Added || res.ItemID != "item-1" || res.Attempts != 1 {
		t.Errorf("result = %+v, want a single-attempt add carrying the item id", res)
	}

	c := find(log, http.MethodPost, "/playlistItems")
	if c == nil {
		t.Fatal("no playlistItems insert happened at all")
	}
	if c.Query != "part=snippet" {
		t.Errorf("part = %q, want part=snippet", c.Query)
	}
	snip, _ := c.Body["snippet"].(map[string]any)
	if snip == nil {
		t.Fatalf("no snippet on the insert: %v", c.Body)
	}
	if snip["playlistId"] != "PL123" {
		t.Errorf("playlistId = %v, want the playlist asked for", snip["playlistId"])
	}
	if _, ok := snip["position"]; ok {
		t.Errorf("the insert sends a position: %v — manualSortRequired is fixed by removing that "+
			"element, so it is never added", snip)
	}
	rid, _ := snip["resourceId"].(map[string]any)
	if rid == nil || rid["kind"] != "youtube#video" || rid["videoId"] != "vid" {
		t.Errorf("resourceId = %v, want kind youtube#video and the video id", snip["resourceId"])
	}
}

// playlistItems.insert IS NOT IDEMPOTENT: a second insert of a video the
// playlist already holds is refused with duplicate/videoAlreadyInPlaylist. The
// playlist nonetheless contains what was asked for, so this is reported as an
// outcome rather than a failure -- otherwise a retried request, or an operator
// pressing the button twice, sends somebody to fix something that is fine.
//
// This also drives the truncation guard: ytRefusal's reason sits well past the
// 300 characters statusError shows, so an implementation reading the truncated
// body instead of the full one decodes nothing, finds no reason, and fails here.
//
// MUTATION M3, internal/oauth/youtube_vod.go in ytHasReason:
// `se.payload()` -> `se.Body`.
// Observed: FAIL -- AddVODToPlaylist returned an error for a duplicate.
func TestADuplicateInsertIsAnOutcomeRatherThanAFailure(t *testing.T) {
	var log []capture
	y := ytVODStub(t, &log, ytRefusal(http.StatusConflict, "videoAlreadyInPlaylist"))

	res, err := y.AddVODToPlaylist(context.Background(), "tok", "PL123", "vid")
	if err != nil {
		t.Fatalf("a duplicate was reported as a failure: %v", err)
	}
	if !res.AlreadyPresent || res.Added {
		t.Errorf("result = %+v, want AlreadyPresent and not Added", res)
	}
	if n := ytCount(log, http.MethodPost, "/playlistItems"); n != 1 {
		t.Errorf("sent %d inserts for a duplicate; retrying one costs quota and cannot succeed", n)
	}
}

// Nothing documents how soon after a broadcast completes its archive becomes a
// valid playlistItems target, and an eager call is expected to be refused with
// videoNotFound. The wait is polyemesis's, not YouTube's -- but it has to
// actually happen, or the file-it-away step is lost for every prompt caller.
func TestAPlaylistAddWaitsForTheArchiveToBecomeAddressable(t *testing.T) {
	var log []capture
	y := ytVODStub(t, &log,
		ytRefusal(http.StatusNotFound, "videoNotFound"),
		ytReply{status: http.StatusOK, body: `{"id":"item-9"}`})

	res, err := y.AddVODToPlaylist(context.Background(), "tok", "PL123", "vid")
	if err != nil {
		t.Fatalf("AddVODToPlaylist gave up on a video that appeared on the second try: %v", err)
	}
	if !res.Added || res.Attempts != 2 || res.ItemID != "item-9" {
		t.Errorf("result = %+v, want an add on the second attempt", res)
	}
}

// And the wait ends. The message must own the retry as an engineering decision
// rather than implying YouTube promised the archive would show up.
func TestAnArchiveThatNeverAppearsEndsWithAnHonestMessage(t *testing.T) {
	var log []capture
	y := ytVODStub(t, &log, ytRefusal(http.StatusNotFound, "videoNotFound"))

	_, err := y.AddVODToPlaylist(context.Background(), "tok", "PL123", "vid")
	if err == nil {
		t.Fatal("a video that never appeared was reported as added")
	}
	// One first attempt plus one per delay in the stub's schedule.
	if n := ytCount(log, http.MethodPost, "/playlistItems"); n != 3 {
		t.Errorf("sent %d inserts, want 3 — first attempt plus the two scheduled waits", n)
	}
	for _, want := range []string{"nothing documents", "polyemesis waits", "vid", "PL123"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the give-up message does not mention %q: %v", want, err)
		}
	}
}

// Every other refusal is answered the same way by a second attempt as by the
// first, so retrying one spends 50 units to reproduce it.
func TestARefusalThatIsNotVideoNotFoundIsNotRetried(t *testing.T) {
	var log []capture
	y := ytVODStub(t, &log, ytRefusal(http.StatusForbidden, "playlistOperationUnsupported"))

	_, err := y.AddVODToPlaylist(context.Background(), "tok", "UU_uploads", "vid")
	if err == nil {
		t.Fatal("an unsupported playlist operation was reported as an add")
	}
	if n := ytCount(log, http.MethodPost, "/playlistItems"); n != 1 {
		t.Errorf("sent %d inserts for a refusal a retry cannot fix", n)
	}
	if !strings.Contains(err.Error(), "Uploads") {
		t.Errorf("the message does not name the Uploads playlist, which is the one thing that "+
			"explains this refusal: %v", err)
	}
}

// YouTube documents THAT a playlist fills up and never says at what size. The
// message must therefore carry no figure -- an invented limit in front of an
// operator is the most-repeated mistake in this repository's history.
//
// The wrapped platform error is removed before looking, because its own status
// code and URL legitimately contain digits; what is under test is polyemesis's
// prose.
func TestTheFullPlaylistRefusalQuotesNoNumberBecauseYouTubePublishesNone(t *testing.T) {
	var log []capture
	y := ytVODStub(t, &log, ytRefusal(http.StatusForbidden, "playlistContainsMaximumNumberOfVideos"))

	_, err := y.AddVODToPlaylist(context.Background(), "tok", "PL123", "vid")
	if err == nil {
		t.Fatal("a full playlist was reported as an add")
	}
	ours := err.Error()
	if inner := errors.Unwrap(err); inner != nil {
		ours = strings.Replace(ours, inner.Error(), "", 1)
	}
	if got := regexp.MustCompile(`[0-9]+`).FindString(ours); got != "" {
		t.Errorf("the full-playlist message quotes %q. YouTube publishes no limit for this, so any "+
			"number here was invented: %s", got, ours)
	}
	if !strings.Contains(ours, "full") {
		t.Errorf("the message does not say the playlist is full: %s", ours)
	}
}

// A playlist add with nothing to address is refused before it becomes a request
// whose failure reads as YouTube's fault.
func TestAPlaylistAddWithNothingToAddressIsRefusedWithoutACall(t *testing.T) {
	tests := []struct {
		name                string
		playlistID, videoID string
	}{
		{"no playlist chosen", "", "vid"},
		{"no archive recorded", "PL123", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var log []capture
			y := ytVODStub(t, &log)

			if _, err := y.AddVODToPlaylist(context.Background(), "tok", tc.playlistID, tc.videoID); err == nil {
				t.Fatal("accepted an add with nothing to address")
			}
			if len(log) != 0 {
				t.Errorf("spent %d request(s): %v", len(log), log)
			}
		})
	}
}
