package publish

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/jobs"
)

// ytFake is a fake YouTube upload host. It records what reached it.
type ytFake struct {
	uploaded              int64
	privacy               string
	sessionIn             *db.DB
	rowID                 int64
	sawSessionBeforeBytes bool
	dropFinalResponse     atomic.Bool
	finished              bool
}

func (f *ytFake) handler(srvURL func() string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/upload/youtube/v3/videos"):
			var body struct {
				Status struct {
					PrivacyStatus string `json:"privacyStatus"`
				} `json:"status"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.privacy = body.Status.PrivacyStatus
			w.Header().Set("Location", srvURL()+"/session/abc?upload_id=SESSIONSECRET")
		case r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/session/"):
			cr := r.Header.Get("Content-Range")
			if strings.HasPrefix(cr, "bytes */") { // status probe
				if f.finished {
					fmt.Fprintf(w, `{"id":"vid1","status":{"privacyStatus":"private"}}`)
					return
				}
				if f.uploaded > 0 {
					w.Header().Set("Range", fmt.Sprintf("bytes=0-%d", f.uploaded-1))
				}
				w.WriteHeader(http.StatusPermanentRedirect)
				return
			}
			if f.sessionIn != nil {
				p, _ := f.sessionIn.GetPublication(f.rowID)
				f.sawSessionBeforeBytes = p.HasSession
			}
			n, _ := io.Copy(io.Discard, r.Body)
			f.uploaded += n
			f.finished = true
			if f.dropFinalResponse.Load() {
				hj, _ := w.(http.Hijacker)
				c, _, _ := hj.Hijack()
				c.Close()
				return
			}
			fmt.Fprintf(w, `{"id":"vid1","status":{"privacyStatus":"private"}}`)
		default:
			http.NotFound(w, r)
		}
	}
}

func ytEnv(t *testing.T) (*env, *ytFake, *counting) {
	e := newEnv(t)
	f := &ytFake{sessionIn: e.d, rowID: 1}
	var url string
	srv, cnt := serve(t, f.handler(func() string { return url }))
	url = srv.URL
	e.ad[PlatformYouTube] = &YouTube{Base: srv.URL}
	return e, f, cnt
}

// ---------------------------------------------------------------- the click

// THE GUARANTEE: a job whose ledger row no click armed sends nothing at all --
// no token fetched, no clip read, no request.
func TestAJobNoClickArmedMakesNoRequestAndFetchesNoToken(t *testing.T) {
	e, _, cnt := ytEnv(t)
	p, params := e.claim(PlatformYouTube)

	for name, bad := range map[string]Params{
		"forged nonce": {PublicationID: p.ID, Nonce: "guess"},
		"empty nonce":  {PublicationID: p.ID},
		"no row":       {PublicationID: 9999, Nonce: "x"},
	} {
		_, err := e.run(bad, 1)
		if err == nil {
			t.Errorf("%s: the worker ran", name)
		}
	}
	if cnt.n() != 0 || e.tokens != 0 {
		t.Fatalf("an unarmed job reached the platform: %d requests, %d token fetches", cnt.n(), e.tokens)
	}
	// And the genuine job still works afterwards.
	if _, err := e.run(params, 1); err != nil {
		t.Fatalf("armed job: %v", err)
	}
}

// A failed row is dead to every job until a NEW click re-arms it, so a generic
// job retry cannot post it.
func TestARetriedJobCannotPostAFailedPublication(t *testing.T) {
	e, _, cnt := ytEnv(t)
	p, params := e.claim(PlatformYouTube)
	if err := e.d.FailPublication(p.ID, "quota_exceeded", "x"); err != nil {
		t.Fatal(err)
	}
	_, err := e.run(params, 1)
	if !jobs.IsPermanent(err) {
		t.Fatalf("err = %v, want permanent", err)
	}
	if cnt.n() != 0 || e.tokens != 0 {
		t.Fatalf("a failed publication reached the platform")
	}
}

// ------------------------------------------------------------- double posts

func TestAnArmedPublishPostsOnceAndAReplayPostsNothing(t *testing.T) {
	e, f, cnt := ytEnv(t)
	p, params := e.claim(PlatformYouTube)
	r, err := e.run(params, 1)
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	row := e.row(p.ID)
	if row.State != db.PublicationPosted || row.PlatformPostID != "vid1" || row.ActualPrivacy != "private" {
		t.Fatalf("row = %+v", row)
	}
	if !strings.Contains(row.PostURL, "/shorts/vid1") {
		t.Errorf("url = %q", row.PostURL)
	}
	if !f.sawSessionBeforeBytes {
		t.Errorf("the upload session was not stored before the first byte was sent")
	}
	if got := r.final.(Result); got.PostID != "vid1" {
		t.Errorf("result = %+v", got)
	}
	before := cnt.n()
	// The same job run again -- a generic retry, a crash recovery -- is success
	// and sends nothing.
	if _, err := e.run(params, 2); err != nil {
		t.Fatalf("replay: %v", err)
	}
	if cnt.n() != before {
		t.Fatalf("a replay of a posted publication made %d more requests", cnt.n()-before)
	}
	// A second click on the same clip+platform+account does not even claim.
	if _, out, _ := e.d.ClaimPublication(db.Publication{ClipJobID: 11, Platform: PlatformYouTube, AccountID: 5, Nonce: "n2"}); out != db.ClaimPosted {
		t.Errorf("second click outcome = %v, want ClaimPosted", out)
	}
}

// The dangerous case: the last response is lost. The retry must ASK the session,
// find the video, and record it -- not open a second session.
func TestALostFinalResponseIsResolvedByAskingTheSessionNotByUploadingAgain(t *testing.T) {
	e, f, cnt := ytEnv(t)
	f.dropFinalResponse.Store(true)
	p, params := e.claim(PlatformYouTube)
	if _, err := e.run(params, 1); err == nil {
		t.Fatal("first attempt should have failed with a dropped connection")
	}
	if e.row(p.ID).State == db.PublicationPosted {
		t.Fatal("row posted without an answer")
	}
	f.dropFinalResponse.Store(false)
	if _, err := e.run(params, 2); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if got := cnt.count("POST /upload/youtube/v3/videos"); got != 1 {
		t.Fatalf("sessions opened = %d, want exactly 1", got)
	}
	if got := cnt.count("PUT /session/"); got != 2 { // the upload and the probe, no second upload
		t.Fatalf("PUTs = %d, want 2 (upload + status probe)", got)
	}
	if row := e.row(p.ID); row.State != db.PublicationPosted || row.PlatformPostID != "vid1" {
		t.Fatalf("row = %+v", row)
	}
}

func TestAnInterruptedUploadResumesFromTheByteYouTubeHolds(t *testing.T) {
	e, f, _ := ytEnv(t)
	p, params := e.claim(PlatformYouTube)
	// A session exists and holds 1000 bytes.
	if err := e.d.SetPublicationSession(e.box, p.ID, "nonce-click-1", e.ad[PlatformYouTube].(*YouTube).Base+"/session/abc?upload_id=SESSIONSECRET",
		metaHash(Meta{Title: "My short", Description: "d", Tags: []string{"fun"}, Privacy: "private"}, e.clip)); err != nil {
		t.Fatal(err)
	}
	f.uploaded = 1000
	if _, err := e.run(params, 2); err != nil {
		t.Fatalf("resume: %v", err)
	}
	if f.uploaded != 3000 {
		t.Errorf("total uploaded = %d, want 3000 (only the missing 2000 bytes sent)", f.uploaded)
	}
}

func TestSessionAndUploadURLNeverReachTheLedgerLogsOrErrors(t *testing.T) {
	e, f, _ := ytEnv(t)
	f.dropFinalResponse.Store(true)
	p, params := e.claim(PlatformYouTube)
	r, err := e.run(params, 3) // last attempt: the failure is recorded on the row
	if err == nil {
		t.Fatal("expected a failure")
	}
	row := e.row(p.ID)
	blob, _ := json.Marshal(row)
	all := string(blob) + err.Error() + strings.Join(r.logs, "\n") + row.FailDetail
	for _, secret := range []string{"SESSIONSECRET", "upload_id", fakeToken, "/session/abc"} {
		if strings.Contains(all, secret) {
			t.Errorf("%q leaked into the ledger row, log or error: %s", secret, all)
		}
	}
	if row.State != db.PublicationFailed {
		t.Errorf("an exhausted retry left the row %q, want failed", row.State)
	}
}

func TestRetriesBackOffThenGiveUpOnlyOnTheLastAttempt(t *testing.T) {
	e, _, _ := ytEnv(t)
	e.ad[PlatformYouTube] = &YouTube{Base: "http://127.0.0.1:1"} // refused
	p, params := e.claim(PlatformYouTube)
	_, err := e.run(params, 1)
	if err == nil || jobs.IsPermanent(err) {
		t.Fatalf("attempt 1 err = %v, want a retryable error", err)
	}
	if got := e.row(p.ID).State; got != db.PublicationUploading {
		t.Fatalf("after a retryable failure state = %q, want uploading", got)
	}
	if _, err = e.run(params, 3); err == nil {
		t.Fatal("last attempt should fail")
	}
	if row := e.row(p.ID); row.State != db.PublicationFailed || row.FailCause != string(CauseNetwork) {
		t.Fatalf("row = %+v", row)
	}
}

// ---------------------------------------------------------------- grants

func TestAGrantWithoutUploadMakesNoRequestAndAsksToReconnect(t *testing.T) {
	e, _, cnt := ytEnv(t)
	e.cred.Scopes = "https://www.googleapis.com/auth/youtube.readonly"
	p, params := e.claim(PlatformYouTube)
	if _, err := e.run(params, 1); !jobs.IsPermanent(err) {
		t.Fatalf("err = %v", err)
	}
	if cnt.n() != 0 {
		t.Errorf("requests made despite a missing scope")
	}
	if row := e.row(p.ID); row.FailCause != string(CauseNeedsReconnect) {
		t.Errorf("cause = %q", row.FailCause)
	}
}

func TestTheExistingYouTubeScopeAlreadyAllowsUploads(t *testing.T) {
	e, _, _ := ytEnv(t)
	e.cred.Scopes = "https://www.googleapis.com/auth/youtube"
	_, params := e.claim(PlatformYouTube)
	if _, err := e.run(params, 1); err != nil {
		t.Fatalf("an account connected before uploads existed was refused: %v", err)
	}
}

func TestQuotaExhaustionIsNamedAndNotRetried(t *testing.T) {
	e := newEnv(t)
	srv, _ := serve(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, `{"error":{"code":403,"message":"quota","errors":[{"reason":"quotaExceeded"}]}}`)
	})
	e.ad[PlatformYouTube] = &YouTube{Base: srv.URL}
	p, params := e.claim(PlatformYouTube)
	if _, err := e.run(params, 1); !jobs.IsPermanent(err) {
		t.Fatalf("err = %v", err)
	}
	if row := e.row(p.ID); row.FailCause != string(CauseQuota) || row.State != db.PublicationFailed {
		t.Fatalf("row = %+v", row)
	}
}

func TestAMissingClipFileIsPermanent(t *testing.T) {
	e, _, _ := ytEnv(t)
	e.clip.Path = e.clip.Path + ".gone"
	p, params := e.claim(PlatformYouTube)
	_, err := e.run(params, 1)
	if err == nil {
		t.Fatal("expected failure")
	}
	row := e.row(p.ID)
	if row.State != db.PublicationFailed || row.FailCause != string(CauseClipMissing) {
		t.Fatalf("row = %+v", row)
	}
	_ = errors.New
}

func TestPublishJobKindIsStable(t *testing.T) {
	if JobKind != "clip.publish" {
		t.Fatalf("JobKind changed: %q", JobKind)
	}
}
