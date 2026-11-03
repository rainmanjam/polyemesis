package publish

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/jobs"
)

func TestYouTubeFailuresAreNamedAndTheRightOnesRetry(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		cause  Cause
		retry  bool
	}{
		{"insufficient permissions", 403, `{"error":{"code":403,"message":"m","errors":[{"reason":"insufficientPermissions"}]}}`, CauseNeedsReconnect, false},
		{"no channel", 403, `{"error":{"code":403,"message":"m","errors":[{"reason":"youtubeSignupRequired"}]}}`, CauseRefused, false},
		{"bad video", 400, `{"error":{"code":400,"message":"The video is invalid"}}`, CauseClipRejected, false},
		{"revoked token", 401, `{}`, CauseTokenRejected, false},
		{"rate limited", 429, `{}`, CauseRateLimited, true},
		{"server error", 503, `{}`, CauseUnavailable, true},
		{"other refusal", 404, `{"error":{"message":"gone"}}`, CauseRefused, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			srv, _ := serve(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(c.status)
				fmt.Fprint(w, c.body)
			})
			e.ad[PlatformYouTube] = &YouTube{Base: srv.URL}
			p, params := e.claim(PlatformYouTube)
			_, err := e.run(params, 1)
			if err == nil {
				t.Fatal("expected a failure")
			}
			if jobs.IsPermanent(err) == c.retry {
				t.Fatalf("permanent = %v, want retry = %v (%v)", jobs.IsPermanent(err), c.retry, err)
			}
			row := e.row(p.ID)
			if c.retry {
				if row.State != db.PublicationUploading {
					t.Fatalf("a retryable failure left state %q", row.State)
				}
				return
			}
			if row.FailCause != string(c.cause) || row.State != db.PublicationFailed {
				t.Fatalf("row = %+v", row)
			}
		})
	}
}

func TestAGoneYouTubeSessionIsReplacedAndAChangedFormOpensANewOne(t *testing.T) {
	e, f, cnt := ytEnv(t)
	p, params := e.claim(PlatformYouTube)
	// A session stored for DIFFERENT words: the old one holds nothing final, so a
	// fresh one is opened for what the person confirmed.
	if err := e.d.SetPublicationSession(e.box, p.ID, "nonce-click-1", e.ad[PlatformYouTube].(*YouTube).Base+"/session/old", "some-other-hash"); err != nil {
		t.Fatal(err)
	}
	f.uploaded = 0
	if _, err := e.run(params, 1); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if cnt.count("POST /upload/youtube/v3/videos") != 1 {
		t.Fatalf("a session for different words was reused")
	}
}

func TestAYouTubeSessionThatIsGoneIsReplaced(t *testing.T) {
	e := newEnv(t)
	var url string
	f := &ytFake{rowID: 1}
	h := f.handler(func() string { return url })
	srv, cnt := serve(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut && strings.HasSuffix(r.URL.Path, "/session/dead") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		h(w, r)
	})
	url = srv.URL
	e.ad[PlatformYouTube] = &YouTube{Base: srv.URL}
	p, params := e.claim(PlatformYouTube)
	if err := e.d.SetPublicationSession(e.box, p.ID, "nonce-click-1", srv.URL+"/session/dead",
		metaHash(Meta{Title: "My short", Description: "d", Tags: []string{"fun"}, Privacy: "private"}, e.clip)); err != nil {
		t.Fatal(err)
	}
	if _, err := e.run(params, 1); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if cnt.count("POST /upload/youtube/v3/videos") != 1 || e.row(p.ID).State != db.PublicationPosted {
		t.Fatalf("a dead session was not replaced")
	}
}

func TestYouTubeWithoutALocationHeaderIsRetried(t *testing.T) {
	e := newEnv(t)
	srv, _ := serve(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	e.ad[PlatformYouTube] = &YouTube{Base: srv.URL}
	_, params := e.claim(PlatformYouTube)
	if _, err := e.run(params, 1); err == nil || jobs.IsPermanent(err) {
		t.Fatalf("err = %v, want a retryable error", err)
	}
}

func TestRangeEndReadsYouTubesResumeHeader(t *testing.T) {
	for in, want := range map[string]int64{"bytes=0-999": 1000, "": 0, "garbage": 0, "bytes=0-x": 0} {
		if got := rangeEnd(in); got != want {
			t.Errorf("rangeEnd(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestTikTokErrorsAreNamed(t *testing.T) {
	cases := []struct {
		code  string
		cause Cause
		retry bool
	}{
		{"access_token_invalid", CauseNeedsReconnect, false},
		{"scope_not_authorized", CauseNeedsReconnect, false},
		{"rate_limit_exceeded", CauseRateLimited, true},
		{"spam_risk_too_many_posts", CauseQuota, false},
		{"unaudited_client_can_only_post_to_private_accounts", CauseRefused, false},
		{"privacy_level_option_mismatch", CausePrivacyUnavailable, false},
		{"something_new", CauseRefused, false},
	}
	for _, c := range cases {
		t.Run(c.code, func(t *testing.T) {
			e := newEnv(t)
			srv, _ := serve(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusBadRequest)
				fmt.Fprintf(w, `{"data":{},"error":{"code":%q,"message":"m"}}`, c.code)
			})
			e.ad[PlatformTikTok] = &TikTok{Base: srv.URL}
			p, params := e.claim(PlatformTikTok)
			_, err := e.run(params, 1)
			if err == nil {
				t.Fatal("expected a failure")
			}
			if c.retry {
				if jobs.IsPermanent(err) {
					t.Fatalf("a retryable cause was permanent: %v", err)
				}
				return
			}
			if row := e.row(p.ID); row.FailCause != string(c.cause) {
				t.Fatalf("cause = %q, want %q", row.FailCause, c.cause)
			}
		})
	}
}

func TestTikTokReportsAFailedProcessingAndForgetsTheDeadPublishID(t *testing.T) {
	e, f, _ := tkEnv(t)
	f.status, f.failReason = "FAILED", "file_format_check_failed"
	p, params := e.claim(PlatformTikTok)
	if _, err := e.run(params, 1); !jobs.IsPermanent(err) {
		t.Fatalf("err = %v", err)
	}
	row := e.row(p.ID)
	if row.FailCause != string(CauseRefused) || !strings.Contains(row.FailDetail, "file_format_check_failed") {
		t.Fatalf("row = %+v", row)
	}
	if row.PlatformPostID != "" || row.HasSession {
		t.Fatalf("a dead publish id / upload address survived: %+v", row)
	}
}

func TestTikTokResumesFromAChunkBoundaryAndSkipsTheInitWhenTheIDIsAlive(t *testing.T) {
	e, f, cnt := tkEnv(t)
	e.clip.Size = 25 << 20 // three chunks of 10 MB, the last carrying the tail
	big := make([]byte, e.clip.Size)
	if err := writeFile(e.clip.Path, big); err != nil {
		t.Fatal(err)
	}
	p, params := e.claim(PlatformTikTok)
	if err := e.d.SetPublicationRemote(p.ID, "nonce-click-1", "v_pub_file~1"); err != nil {
		t.Fatal(err)
	}
	if err := e.d.SetPublicationSession(e.box, p.ID, "nonce-click-1", f.srvURL+"/upload?upload_token=TTSECRET",
		metaHash(Meta{Title: "My short", Description: "d", Tags: []string{"fun"}, Privacy: "private"}, e.clip)); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.status, f.uploadedSet = "PROCESSING_UPLOAD", 10<<20
	f.mu.Unlock()
	// The status says 10 MB are in; after the resumed chunks it reports complete.
	go func() {
		for cnt.count("PUT /upload") < 1 {
			time.Sleep(time.Millisecond)
		}
		f.mu.Lock()
		f.status = "PUBLISH_COMPLETE"
		f.mu.Unlock()
	}()
	if _, err := e.run(params, 2); err != nil {
		t.Fatalf("resume: %v", err)
	}
	if cnt.count("POST /v2/post/publish/video/init") != 0 {
		t.Fatal("a live publish id was initialised again")
	}
	f.mu.Lock()
	chunks := append([]string(nil), f.chunks...)
	f.mu.Unlock()
	if len(chunks) != 1 || !strings.HasPrefix(chunks[0], "bytes 10485760-26214399/26214400") {
		t.Fatalf("chunks sent = %v, want only the final chunk from the 10 MB boundary", chunks)
	}
}

func TestAnExpiredTikTokUploadAddressIsForgottenNotRetriedBlindly(t *testing.T) {
	e := newEnv(t)
	srv, _ := serve(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/post/publish/creator_info/query/":
			fmt.Fprint(w, `{"data":{"privacy_level_options":["SELF_ONLY"],"max_video_post_duration_sec":300},"error":{"code":"ok"}}`)
		case "/v2/post/publish/video/init/":
			fmt.Fprintf(w, `{"data":{"publish_id":"p1","upload_url":"http://%s/upload"},"error":{"code":"ok"}}`, r.Host)
		case "/upload":
			w.WriteHeader(http.StatusForbidden)
		}
	})
	e.ad[PlatformTikTok] = &TikTok{Base: srv.URL}
	p, params := e.claim(PlatformTikTok)
	if _, err := e.run(params, 1); err == nil {
		t.Fatal("expected a failure")
	}
	if e.row(p.ID).HasSession {
		t.Fatal("an expired upload address was kept")
	}
}

func TestATokenErrorThatSaysReconnectIsPermanent(t *testing.T) {
	e, _, cnt := ytEnv(t)
	_, params := e.claim(PlatformYouTube)
	w := NewWorker(WorkerDeps{
		Ledger: LedgerFor(e.d, e.box),
		Tokens: func(context.Context, int64) (Credential, error) {
			return Credential{}, fmt.Errorf("the youtube token has expired and there is no refresh token; reconnect the account")
		},
		Clips: func(context.Context, int64) (File, error) { return e.clip, nil }, Adapters: e.ad,
	})
	raw := mustParams(params)
	err := w.Run(context.Background(), jobs.Job{ID: 1, Params: raw, Attempts: 1, MaxAttempts: 3}, &rep{})
	if !jobs.IsPermanent(err) || cnt.n() != 0 {
		t.Fatalf("err = %v requests = %d", err, cnt.n())
	}
}

func TestShutdownMidUploadLeavesTheRowInFlight(t *testing.T) {
	e := newEnv(t)
	started := make(chan struct{}, 1)
	srv, _ := serve(t, func(w http.ResponseWriter, r *http.Request) {
		// The body must be read for the server to notice a client that went away.
		_, _ = io.Copy(io.Discard, r.Body)
		select {
		case started <- struct{}{}:
		default:
		}
		<-r.Context().Done()
	})
	e.ad[PlatformYouTube] = &YouTube{Base: srv.URL}
	p, params := e.claim(PlatformYouTube)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- e.worker().Run(ctx, jobs.Job{ID: 1, Params: mustParams(params), Attempts: 1, MaxAttempts: 3}, &rep{})
	}()
	<-started
	cancel()
	if err := <-done; err == nil || jobs.IsPermanent(err) {
		t.Fatalf("err = %v, want a plain context error the queue can requeue", err)
	}
	if got := e.row(p.ID).State; got != db.PublicationUploading {
		t.Fatalf("a shutdown failed the publication: state %q", got)
	}
}

func TestAJobWithGarbageParamsIsPermanent(t *testing.T) {
	e := newEnv(t)
	err := e.worker().Run(context.Background(), jobs.Job{ID: 1, Params: []byte(`{"publicationId":0}`)}, &rep{})
	if !jobs.IsPermanent(err) {
		t.Fatalf("err = %v", err)
	}
	if err := e.worker().Run(context.Background(), jobs.Job{ID: 1, Params: []byte(`nope`)}, &rep{}); !jobs.IsPermanent(err) {
		t.Fatalf("err = %v", err)
	}
}

func TestAClipThatIsNotAnExportIsRefusedForGood(t *testing.T) {
	e, _, _ := ytEnv(t)
	w := NewWorker(WorkerDeps{
		Ledger: LedgerFor(e.d, e.box), Adapters: e.ad,
		Tokens: func(context.Context, int64) (Credential, error) { return e.cred, nil },
		Clips:  func(context.Context, int64) (File, error) { return File{}, ErrNotAClip },
	})
	p, params := e.claim(PlatformYouTube)
	if err := w.Run(context.Background(), jobs.Job{ID: 1, Params: mustParams(params), Attempts: 1, MaxAttempts: 3}, &rep{}); !jobs.IsPermanent(err) {
		t.Fatalf("err = %v", err)
	}
	if e.row(p.ID).FailCause != string(CauseClipRejected) {
		t.Fatalf("cause = %q", e.row(p.ID).FailCause)
	}
}

func TestNetworkErrorsNeverCarryTheRequestURL(t *testing.T) {
	f := netFailure(fmt.Errorf("Put %q: dial tcp: connection refused", "https://up.example/s?token=SECRET"))
	// A plain error is passed through, but a *url.Error is reduced to its cause.
	_ = f
	e := newEnv(t)
	e.ad[PlatformYouTube] = &YouTube{Base: "http://127.0.0.1:1/?token=SECRETQUERY"}
	p, params := e.claim(PlatformYouTube)
	e.run(params, 3)
	row := e.row(p.ID)
	if strings.Contains(row.FailDetail, "SECRETQUERY") || strings.Contains(row.FailDetail, "http://") {
		t.Fatalf("the request URL leaked into the failure detail: %q", row.FailDetail)
	}
}

func TestRedactStripsLinksAndTokenPairs(t *testing.T) {
	in := `Put "https://up.example/s?upload_id=ABC123&x=1": 403 token=SECRETVALUE and key=K1 then http://h/p`
	out := Redact(in)
	for _, leak := range []string{"https://", "http://", "ABC123", "SECRETVALUE", "K1", "up.example"} {
		if strings.Contains(out, leak) {
			t.Errorf("Redact left %q in %q", leak, out)
		}
	}
}

func TestAPlatformErrorBodyThatQuotesTheUploadURLIsRedacted(t *testing.T) {
	e := newEnv(t)
	srv, _ := serve(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/post/publish/creator_info/query/":
			fmt.Fprint(w, `{"data":{"privacy_level_options":["SELF_ONLY"],"max_video_post_duration_sec":300},"error":{"code":"ok"}}`)
		case "/v2/post/publish/video/init/":
			fmt.Fprintf(w, `{"data":{"publish_id":"p1","upload_url":"http://%s/upload?upload_token=LEAKME"},"error":{"code":"ok"}}`, r.Host)
		case "/upload":
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprintf(w, "<Error>denied for http://%s/upload?upload_token=LEAKME</Error>", r.Host)
		}
	})
	e.ad[PlatformTikTok] = &TikTok{Base: srv.URL}
	p, params := e.claim(PlatformTikTok)
	_, err := e.run(params, 3)
	row := e.row(p.ID)
	if all := row.FailDetail + fmt.Sprint(err); strings.Contains(all, "LEAKME") || strings.Contains(all, "upload_token") {
		t.Fatalf("an upload capability leaked: %q", all)
	}
}

// A lost final response followed by a vanished session cannot say whether the
// video landed. Opening a new session could be a second video.
func TestAVanishedSessionAfterBytesWentOutIsOutcomeUnknownNotANewUpload(t *testing.T) {
	e, f, cnt := ytEnv(t)
	f.dropFinalResponse.Store(true)
	p, params := e.claim(PlatformYouTube)
	if _, err := e.run(params, 1); err == nil {
		t.Fatal("expected the dropped response to fail")
	}
	// The session is now gone on YouTube's side.
	srv2, cnt2 := serve(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	_ = cnt
	// Re-aim the adapter at a host that answers 404 to the stored session URL.
	sess, _, _ := e.d.GetPublicationSession(e.box, p.ID)
	if sess == "" {
		t.Fatal("no session was stored")
	}
	if err := e.d.SetPublicationSession(e.box, p.ID, "nonce-click-1", srv2.URL+"/session/abc",
		metaHash(Meta{Title: "My short", Description: "d", Tags: []string{"fun"}, Privacy: "private"}, e.clip)); err != nil {
		t.Fatal(err)
	}
	e.ad[PlatformYouTube] = &YouTube{Base: srv2.URL}
	if _, err := e.run(params, 2); !jobs.IsPermanent(err) {
		t.Fatalf("err = %v, want a permanent outcome-unknown", err)
	}
	if cnt2.count("POST /upload/youtube/v3/videos") != 0 {
		t.Fatal("a new upload session was opened after bytes may have been sent")
	}
	if row := e.row(p.ID); row.FailCause != string(CauseOutcomeUnknown) {
		t.Fatalf("cause = %q", row.FailCause)
	}
}

type brokenSessionLedger struct{ Ledger }

func (brokenSessionLedger) Session(int64) (string, string, error) {
	return "", "", fmt.Errorf("disk error")
}

func TestAFailedSessionReadStopsTheAttemptInsteadOfOpeningASecondUpload(t *testing.T) {
	e, _, cnt := ytEnv(t)
	_, params := e.claim(PlatformYouTube)
	w := NewWorker(WorkerDeps{
		Ledger: brokenSessionLedger{LedgerFor(e.d, e.box)}, Adapters: e.ad,
		Tokens: func(context.Context, int64) (Credential, error) { return e.cred, nil },
		Clips:  func(context.Context, int64) (File, error) { return e.clip, nil },
	})
	err := w.Run(context.Background(), jobs.Job{ID: 1, Params: mustParams(params), Attempts: 1, MaxAttempts: 3}, &rep{})
	if err == nil || cnt.n() != 0 {
		t.Fatalf("err = %v, requests = %d: an unreadable session must stop the attempt before any request", err, cnt.n())
	}
}

// Worker A is mid-request when the click is cancelled and a new click re-arms the
// row. When A's request returns, its writes are refused and the new run's row is
// untouched.
func TestAStaleWorkerCannotTouchTheRunThatReplacedIt(t *testing.T) {
	e := newEnv(t)
	started, release := make(chan struct{}, 1), make(chan struct{})
	srv, _ := serve(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		select {
		case started <- struct{}{}:
		default:
		}
		<-release
		w.Header().Set("Location", "http://"+r.Host+"/session/late?upload_id=LATE")
	})
	e.ad[PlatformYouTube] = &YouTube{Base: srv.URL}
	p, params := e.claim(PlatformYouTube)
	done := make(chan error, 1)
	go func() {
		done <- e.worker().Run(context.Background(), jobs.Job{ID: 1, Params: mustParams(params), Attempts: 1, MaxAttempts: 3}, &rep{})
	}()
	<-started
	// Cancel, then a new click re-arms the row with a new nonce.
	if err := e.d.FailPublication(p.ID, "cancelled", "Publishing was cancelled."); err != nil {
		t.Fatal(err)
	}
	re := db.Publication{ClipJobID: 11, Platform: PlatformYouTube, AccountID: 5, Title: "t", Nonce: "nonce-click-2", Privacy: "private"}
	if _, out, _ := e.d.ClaimPublication(re); out != db.ClaimRearmed {
		t.Fatalf("outcome = %v", out)
	}
	close(release)
	if err := <-done; err == nil {
		t.Fatal("the stale worker carried on")
	}
	row := e.row(p.ID)
	if row.State != db.PublicationConfirmed || row.HasSession || row.FailCause != "" {
		t.Fatalf("the stale worker disturbed the new run: %+v", row)
	}
}

func TestXRetryAfterAnAppendErrorRedoesTheUploadInsteadOfPostingUnfinishedMedia(t *testing.T) {
	e := newEnv(t)
	appends := 0
	srv, cnt := serve(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/2/media/upload/initialize":
			fmt.Fprint(w, `{"data":{"id":"m1"}}`)
		case "/2/media/upload/m1/append":
			appends++
			if appends == 1 {
				w.WriteHeader(http.StatusBadGateway)
				return
			}
			fmt.Fprint(w, `{"data":{"expires_at":1}}`)
		case "/2/media/upload/m1/finalize":
			fmt.Fprint(w, `{"data":{"id":"m1"}}`)
		case "/2/tweets":
			fmt.Fprint(w, `{"data":{"id":"tw1"}}`)
		}
	})
	e.ad[PlatformX] = &X{Enabled: true, Base: srv.URL, PollEvery: time.Millisecond}
	e.cred.Scopes = "tweet.write media.write"
	e.gates = Gates{XPublish: true}
	p, params := e.claim(PlatformX)
	if _, err := e.run(params, 1); err == nil {
		t.Fatal("the first attempt should fail on the append")
	}
	if _, err := e.run(params, 2); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if cnt.count("POST /2/media/upload/initialize") != 1 || appends != 2 || cnt.count("POST /2/media/upload/m1/finalize") != 1 {
		t.Fatalf("init=%d appends=%d finalize=%d", cnt.count("POST /2/media/upload/initialize"), appends, cnt.count("POST /2/media/upload/m1/finalize"))
	}
	if e.row(p.ID).State != db.PublicationPosted || cnt.count("POST /2/tweets") != 1 {
		t.Fatalf("row = %+v", e.row(p.ID))
	}
}

func TestAnXAdapterThatIsSwitchedOffSendsNothingEvenIfReached(t *testing.T) {
	e, cnt := xEnv(t, 200)
	e.ad[PlatformX] = &X{Base: e.ad[PlatformX].(*X).Base} // Enabled false
	e.gates = Gates{XPublish: true}                       // the worker's own gate is open
	p, params := e.claim(PlatformX)
	if _, err := e.run(params, 1); !jobs.IsPermanent(err) {
		t.Fatalf("err = %v", err)
	}
	if cnt.n() != 0 || e.row(p.ID).FailCause != string(CauseGated) {
		t.Fatalf("requests = %d, cause = %q", cnt.n(), e.row(p.ID).FailCause)
	}
}

func TestRedactCatchesBearerJSONAndColonForms(t *testing.T) {
	for _, in := range []string{
		`Authorization: Bearer abcDEF123.xyz`,
		`{"access_token":"SECRETJSON","x":1}`,
		`upload_token: SECRETCOLON`,
		`refresh_token=SECRETEQ&other=1`,
		`client_secret = SECRETSPACED`,
	} {
		out := Redact(in)
		if strings.Contains(out, "SECRET") || strings.Contains(out, "abcDEF123") {
			t.Errorf("Redact(%q) = %q", in, out)
		}
	}
}

// Words changed after a TikTok post was opened but before its final chunk: the old
// post can never complete, so the latest confirmation gets a fresh one.
func TestTikTokOpensAFreshPostWhenTheWordsChangedBeforeTheFinalChunk(t *testing.T) {
	e, f, cnt := tkEnv(t)
	p, params := e.claim(PlatformTikTok)
	if err := e.d.SetPublicationRemote(p.ID, "nonce-click-1", "old_publish_id"); err != nil {
		t.Fatal(err)
	}
	if err := e.d.SetPublicationSession(e.box, p.ID, "nonce-click-1", f.srvURL+"/upload?upload_token=OLD", "hash-of-other-words"); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.status = "PROCESSING_UPLOAD"
	f.mu.Unlock()
	go func() {
		for cnt.count("PUT /upload") < 1 {
			time.Sleep(time.Millisecond)
		}
		f.mu.Lock()
		f.status = "PUBLISH_COMPLETE"
		f.mu.Unlock()
	}()
	if _, err := e.run(params, 2); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if cnt.count("POST /v2/post/publish/video/init") != 1 {
		t.Fatal("the stale post was reused instead of opening one for the confirmed words")
	}
}

// A 2xx from X whose body is not JSON is not an empty answer.
func TestXAnUnreadableFinalizeIsNotTreatedAsReady(t *testing.T) {
	e := newEnv(t)
	srv, cnt := serve(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/2/media/upload/initialize":
			fmt.Fprint(w, `{"data":{"id":"m1"}}`)
		case "/2/media/upload/m1/append":
			fmt.Fprint(w, `{"data":{"expires_at":1}}`)
		case "/2/media/upload/m1/finalize":
			fmt.Fprint(w, `<html>not json</html>`)
		}
	})
	e.ad[PlatformX] = &X{Enabled: true, Base: srv.URL, PollEvery: time.Millisecond}
	e.cred.Scopes = "tweet.write media.write"
	e.gates = Gates{XPublish: true}
	_, params := e.claim(PlatformX)
	if _, err := e.run(params, 1); err == nil {
		t.Fatal("expected a retryable failure")
	}
	if cnt.count("POST /2/tweets") != 0 {
		t.Fatal("a post was created from unfinished media")
	}
}
