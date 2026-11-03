package publish

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/jobs"
)

type tkFake struct {
	mu          sync.Mutex
	options     []string
	maxSec      int
	initBodies  []map[string]any
	chunks      []string
	status      string
	uploadedSet int64
	failReason  string
	srvURL      string
}

func (f *tkFake) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		switch r.URL.Path {
		case "/v2/post/publish/creator_info/query/":
			fmt.Fprintf(w, `{"data":{"privacy_level_options":%s,"max_video_post_duration_sec":%d},"error":{"code":"ok"}}`,
				mustJSON(f.options), f.maxSec)
		case "/v2/post/publish/video/init/":
			var b map[string]any
			_ = json.NewDecoder(r.Body).Decode(&b)
			f.initBodies = append(f.initBodies, b)
			fmt.Fprintf(w, `{"data":{"publish_id":"v_pub_file~1","upload_url":"%s/upload?upload_token=TTSECRET"},"error":{"code":"ok"}}`, f.srvURL)
		case "/upload":
			io.Copy(io.Discard, r.Body)
			f.chunks = append(f.chunks, r.Header.Get("Content-Range"))
			w.WriteHeader(http.StatusCreated)
		case "/v2/post/publish/status/fetch/":
			fmt.Fprintf(w, `{"data":{"status":%q,"fail_reason":%q,"uploaded_bytes":%d},"error":{"code":"ok"}}`,
				f.status, f.failReason, f.uploadedSet)
		default:
			http.NotFound(w, r)
		}
	}
}

func mustJSON(v any) string { b, _ := json.Marshal(v); return string(b) }

func tkEnv(t *testing.T) (*env, *tkFake, *counting) {
	e := newEnv(t)
	f := &tkFake{options: []string{"PUBLIC_TO_EVERYONE", "SELF_ONLY"}, maxSec: 300, status: "PUBLISH_COMPLETE"}
	srv, cnt := serve(t, f.handler())
	f.srvURL = srv.URL
	e.ad[PlatformTikTok] = &TikTok{Base: srv.URL, PollEvery: time.Millisecond, PollFor: time.Second}
	return e, f, cnt
}

// TikTok posts are private whatever the request says. This is the test that
// pins the constant: change SELF_ONLY and it fails.
func TestTikTokIsAlwaysPostedPrivateNoMatterWhatWasAskedFor(t *testing.T) {
	e, f, _ := tkEnv(t)
	p, params := e.claim(PlatformTikTok)
	// A database row carrying a public request, as a hostile or buggy caller might leave one.
	if _, err := e.d.SQL().Exec(`UPDATE clip_publications SET privacy = 'PUBLIC_TO_EVERYONE' WHERE id = ?`, p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.run(params, 1); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if len(f.initBodies) != 1 {
		t.Fatalf("inits = %d", len(f.initBodies))
	}
	info := f.initBodies[0]["post_info"].(map[string]any)
	if info["privacy_level"] != "SELF_ONLY" {
		t.Fatalf("privacy_level = %v, want SELF_ONLY", info["privacy_level"])
	}
	if got := e.row(p.ID); got.ActualPrivacy != "SELF_ONLY" || got.State != db.PublicationPosted {
		t.Fatalf("row = %+v", got)
	}
}

func TestNormalizeForcesTikTokPrivacy(t *testing.T) {
	m, err := Normalize(PlatformTikTok, Meta{Title: "t", Privacy: "PUBLIC_TO_EVERYONE"})
	if err != nil || m.Privacy != "SELF_ONLY" {
		t.Fatalf("Normalize = %+v, %v", m, err)
	}
}

// If TikTok offers no private option, nothing is posted and no other level is tried.
func TestTikTokWithoutAPrivateOptionFailsInsteadOfFallingBack(t *testing.T) {
	e, f, cnt := tkEnv(t)
	f.options = []string{"PUBLIC_TO_EVERYONE", "MUTUAL_FOLLOW_FRIENDS"}
	p, params := e.claim(PlatformTikTok)
	if _, err := e.run(params, 1); !jobs.IsPermanent(err) {
		t.Fatalf("err = %v", err)
	}
	if cnt.count("POST /v2/post/publish/video/init") != 0 {
		t.Fatalf("init was sent although no private option exists")
	}
	if row := e.row(p.ID); row.FailCause != string(CausePrivacyUnavailable) {
		t.Fatalf("cause = %q", row.FailCause)
	}
}

func TestTikTokRefusesAClipLongerThanTheAccountMayPost(t *testing.T) {
	e, f, cnt := tkEnv(t)
	f.maxSec = 10
	p, params := e.claim(PlatformTikTok)
	if _, err := e.run(params, 1); !jobs.IsPermanent(err) {
		t.Fatalf("err = %v", err)
	}
	if cnt.count("PUT /upload") != 0 || e.row(p.ID).FailCause != string(CauseClipRejected) {
		t.Fatalf("a too-long clip was uploaded or mislabelled")
	}
}

// The publish_id is stored before any byte, and a retry polls it instead of
// starting a second post.
func TestTikTokRetryPollsTheStoredPublishIDAndNeverInitialisesTwice(t *testing.T) {
	e, f, cnt := tkEnv(t)
	f.status = "PROCESSING_UPLOAD"
	p, params := e.claim(PlatformTikTok)
	if _, err := e.run(params, 1); err == nil {
		t.Fatal("expected a retryable failure while TikTok is still processing")
	}
	if row := e.row(p.ID); row.PlatformPostID != "v_pub_file~1" || !row.HasSession {
		t.Fatalf("publish id / session not stored before the end: %+v", row)
	}
	f.mu.Lock()
	f.status, f.uploadedSet = "PUBLISH_COMPLETE", 3000
	f.mu.Unlock()
	if _, err := e.run(params, 2); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if got := cnt.count("POST /v2/post/publish/video/init"); got != 1 {
		t.Fatalf("inits = %d, want 1", got)
	}
	if row := e.row(p.ID); row.State != db.PublicationPosted || row.PlatformPostID != "v_pub_file~1" {
		t.Fatalf("row = %+v", row)
	}
}

func TestTikTokUploadURLIsNeverStoredInTheClear(t *testing.T) {
	e, f, _ := tkEnv(t)
	f.status = "PROCESSING_UPLOAD"
	p, params := e.claim(PlatformTikTok)
	r, err := e.run(params, 3)
	if err == nil {
		t.Fatal("expected failure")
	}
	row := e.row(p.ID)
	b, _ := json.Marshal(row)
	if all := string(b) + err.Error() + strings.Join(r.logs, ""); strings.Contains(all, "TTSECRET") {
		t.Fatalf("upload token leaked: %s", all)
	}
}

func TestTikTokChunksFollowTheDocumentedRules(t *testing.T) {
	for _, tc := range []struct{ size, chunk, count int64 }{
		{3 << 20, 3 << 20, 1},
		{12 << 20, 10 << 20, 1},
		{70 << 20, 10 << 20, 7},
	} {
		c, n := tikTokChunks(tc.size)
		if c != tc.chunk || n != tc.count {
			t.Errorf("size %d -> chunk %d count %d, want %d/%d", tc.size, c, n, tc.chunk, tc.count)
		}
	}
}

func TestTikTokScopeIsChecked(t *testing.T) {
	e, _, cnt := tkEnv(t)
	e.cred.Scopes = "user.info.basic"
	_, params := e.claim(PlatformTikTok)
	if _, err := e.run(params, 1); !jobs.IsPermanent(err) || cnt.n() != 0 {
		t.Fatalf("err = %v, requests = %d", err, cnt.n())
	}
}
