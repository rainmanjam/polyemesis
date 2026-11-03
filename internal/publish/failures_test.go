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
	if err := e.d.SetPublicationSession(e.box, p.ID, e.ad[PlatformYouTube].(*YouTube).Base+"/session/old", "some-other-hash"); err != nil {
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
	if err := e.d.SetPublicationSession(e.box, p.ID, srv.URL+"/session/dead",
		metaHash(Meta{Title: "My short", Description: "d", Tags: []string{"fun"}, Privacy: "private"}, 3000)); err != nil {
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
	if err := e.d.SetPublicationRemote(p.ID, "v_pub_file~1"); err != nil {
		t.Fatal(err)
	}
	if err := e.d.SetPublicationSession(e.box, p.ID, f.srvURL+"/upload?upload_token=TTSECRET", "tiktok"); err != nil {
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
