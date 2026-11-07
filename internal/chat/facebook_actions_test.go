package chat

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/oauth"
)

// fbPage is the PageAccess a Page-connected account would carry.
func fbPage(id, tok string) func(context.Context) (string, string, error) {
	return func(context.Context) (string, string, error) { return id, tok, nil }
}

type fbReq struct {
	method, path, auth string
	query              url.Values
}

// fbServer records every request and answers each from reply.
func fbServer(t *testing.T, reply func(r *http.Request) (int, string)) (*httptest.Server, func() []fbReq) {
	t.Helper()
	var mu sync.Mutex
	var got []fbReq
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		got = append(got, fbReq{r.Method, r.URL.Path, r.Header.Get("Authorization"), r.URL.Query()})
		mu.Unlock()
		code, body := reply(r)
		w.WriteHeader(code)
		fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv, func() []fbReq {
		mu.Lock()
		defer mu.Unlock()
		return append([]fbReq(nil), got...)
	}
}

func TestFacebookBanBlocksThePersonFromThePageWithThePageToken(t *testing.T) {
	srv, reqs := fbServer(t, func(*http.Request) (int, string) { return 200, `{"u9":true}` })
	a := fbAdapter(t, srv.URL, func(c *FacebookConfig) { c.PageAccess = fbPage("123", "page-tok") })

	if err := a.Ban(context.Background(), "u9", 0, "spam"); err != nil {
		t.Fatal(err)
	}
	got := reqs()
	if len(got) != 1 {
		t.Fatalf("requests = %+v", got)
	}
	r := got[0]
	if r.method != http.MethodPost || r.path != "/123/blocked" || r.query.Get("user") != "u9" {
		t.Errorf("request = %+v, want POST /123/blocked?user=u9", r)
	}
	if r.auth != "Bearer page-tok" {
		t.Errorf("auth = %q; blocking must use the Page token, not the user token", r.auth)
	}
}

func TestFacebookBanRefusesATimeoutBeforeAnyRequest(t *testing.T) {
	srv, reqs := fbServer(t, func(*http.Request) (int, string) { return 200, `{}` })
	a := fbAdapter(t, srv.URL, func(c *FacebookConfig) { c.PageAccess = fbPage("123", "page-tok") })

	err := a.Ban(context.Background(), "u9", 10*time.Minute, "")
	if err == nil || !strings.Contains(err.Error(), "cannot time anyone out") {
		t.Fatalf("err = %v, want the no-timeout sentence", err)
	}
	if n := len(reqs()); n != 0 {
		t.Errorf("a timeout sent %d requests; it must not become a permanent block", n)
	}
}

func TestFacebookBanAndUnbanRefuseWithoutAPage(t *testing.T) {
	srv, reqs := fbServer(t, func(*http.Request) (int, string) { return 200, `{}` })
	ctx := context.Background()

	// No PageAccess at all.
	a := fbAdapter(t, srv.URL, nil)
	if err := a.Ban(ctx, "u9", 0, ""); err == nil || !strings.Contains(err.Error(), "not connected as a Page") {
		t.Errorf("no page access: %v", err)
	}
	// A profile.
	a = fbAdapter(t, srv.URL, func(c *FacebookConfig) {
		c.PageAccess = func(context.Context) (string, string, error) { return "", "", oauth.ErrNotAPage }
	})
	for name, err := range map[string]error{"ban": a.Ban(ctx, "u9", 0, ""), "unban": a.Unban(ctx, "u9")} {
		if err == nil || !strings.Contains(err.Error(), "personal profile") {
			t.Errorf("%s on a profile: %v", name, err)
		}
	}
	// A resolver that fails, and one that answers nothing.
	a = fbAdapter(t, srv.URL, func(c *FacebookConfig) {
		c.PageAccess = func(context.Context) (string, string, error) { return "", "", errors.New("boom") }
	})
	if err := a.Ban(ctx, "u9", 0, ""); err == nil || err.Error() != "boom" {
		t.Errorf("resolver error: %v", err)
	}
	a = fbAdapter(t, srv.URL, func(c *FacebookConfig) { c.PageAccess = fbPage("", "") })
	if err := a.Unban(ctx, "u9"); err == nil || !strings.Contains(err.Error(), "no Page access") {
		t.Errorf("empty access: %v", err)
	}
	// A blank id.
	if err := a.Ban(ctx, "  ", 0, ""); err == nil {
		t.Error("blank ban id accepted")
	}
	if err := a.Unban(ctx, ""); err == nil {
		t.Error("blank unban id accepted")
	}
	if n := len(reqs()); n != 0 {
		t.Errorf("%d requests were sent for refused calls", n)
	}
}

func TestFacebookUnbanLiftsTheBlock(t *testing.T) {
	srv, reqs := fbServer(t, func(*http.Request) (int, string) { return 200, `{"success":true}` })
	a := fbAdapter(t, srv.URL, func(c *FacebookConfig) { c.PageAccess = fbPage("123", "page-tok") })
	if err := a.Unban(context.Background(), "u9"); err != nil {
		t.Fatal(err)
	}
	r := reqs()[0]
	if r.method != http.MethodDelete || r.path != "/123/blocked" || r.query.Get("user") != "u9" {
		t.Errorf("request = %+v, want DELETE /123/blocked?user=u9", r)
	}
}

func TestFacebookBlockAnswersThatSayNoAreErrors(t *testing.T) {
	ctx := context.Background()
	srv, _ := fbServer(t, func(r *http.Request) (int, string) {
		if r.Method == http.MethodDelete {
			return 200, `{"success":false}`
		}
		return 200, `{"u9":false}`
	})
	a := fbAdapter(t, srv.URL, func(c *FacebookConfig) { c.PageAccess = fbPage("123", "t") })
	if err := a.Ban(ctx, "u9", 0, ""); err == nil || !strings.Contains(err.Error(), "did not block") {
		t.Errorf("ban false: %v", err)
	}
	if err := a.Unban(ctx, "u9"); err == nil || !strings.Contains(err.Error(), "did not unblock") {
		t.Errorf("unban false: %v", err)
	}
}

func TestFacebookBlockRefusalsAreMappedToWhatToDo(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   string
		fatal  bool
	}{
		{"block list full", 400, `{"error":{"code":3801,"message":"max"}}`, "maximum number of people", false},
		{"reblock too soon", 400, `{"error":{"code":3802,"message":"soon"}}`, "so soon after being unblocked", false},
		{"abusive", 400, `{"error":{"code":368,"message":"abusive"}}`, "abusive or disallowed", false},
		{"not visible", 400, `{"error":{"code":210,"message":"nv"}}`, "not visible to this Page", false},
		{"permission 200", 403, `{"error":{"code":200,"message":"perm"}}`, "lack of a permission", false},
		{"permission 283", 403, `{"error":{"code":283,"message":"ext"}}`, "pages_read_user_content", false},
		{"expired token", 401, `{"error":{"code":190,"message":"tok"}}`, "rejected the access token", true},
		{"forbidden without a code", 403, `nope`, "Page admin", false},
		{"unknown", 500, `{"error":{"code":1,"message":"x"}}`, "returned 500", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv, _ := fbServer(t, func(*http.Request) (int, string) { return c.status, c.body })
			a := fbAdapter(t, srv.URL, func(cfg *FacebookConfig) { cfg.PageAccess = fbPage("123", "t") })
			err := a.Ban(context.Background(), "u9", 0, "")
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want it to say %q", err, c.want)
			}
			if IsFatal(err) != c.fatal {
				t.Errorf("fatal = %v, want %v", IsFatal(err), c.fatal)
			}
		})
	}
}

func TestFacebookIsABannerThatCannotTimeOut(t *testing.T) {
	pr, ok := ProfileOf("facebook")
	if !ok || !pr.Ban || !pr.NoTimeout {
		t.Fatalf("profile = %+v, want a Banner marked NoTimeout", pr)
	}
	for _, p := range []string{"youtube", "twitch", "kick"} {
		if pr, _ := ProfileOf(db.Platform(p)); pr.NoTimeout {
			t.Errorf("%s is marked NoTimeout", p)
		}
	}
}

// ------------------------------------------------------------------- polls

func TestFacebookPollLifecycle(t *testing.T) {
	ctx := context.Background()
	srv, reqs := fbServer(t, func(r *http.Request) (int, string) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/live-1/polls":
			return 200, `{"id":"poll-7","option_ids":["o1","o2"]}`
		case r.Method == http.MethodPost && r.URL.Path == "/poll-7":
			return 200, `{"success":true}`
		case r.Method == http.MethodGet && r.URL.Path == "/poll-7":
			// Out of order on the wire; the order field decides.
			return 200, `{"poll_options":{"data":[{"order":2,"total_votes":5},{"order":1,"total_votes":3}]}}`
		}
		return 404, `{}`
	})
	a := fbAdapter(t, srv.URL, func(c *FacebookConfig) { c.PageAccess = fbPage("123", "page-tok") })

	id, err := a.CreatePoll(ctx, "Next map?", []string{"Dust", "Mirage"})
	if err != nil || id != "poll-7" {
		t.Fatalf("create = %q, %v", id, err)
	}
	votes, err := a.PollVotes(ctx, id)
	if err != nil || len(votes) != 2 || votes[0] != 3 || votes[1] != 5 {
		t.Fatalf("votes = %v, %v; want [3 5] in option order", votes, err)
	}
	final, err := a.ClosePoll(ctx, id)
	if err != nil || len(final) != 2 || final[1] != 5 {
		t.Fatalf("close = %v, %v", final, err)
	}

	got := reqs()
	create := got[0]
	if create.query.Get("question") != "Next map?" || strings.Join(create.query["options[]"], ",") != "Dust,Mirage" {
		t.Errorf("create query = %v", create.query)
	}
	if create.auth != "Bearer page-tok" {
		t.Errorf("create auth = %q; the Page's live video takes the Page token", create.auth)
	}
	// Close is POST /{poll}?action=CLOSE, and the count is read AFTER it.
	var closeAt, readAfter int
	for i, r := range got {
		if r.method == http.MethodPost && r.path == "/poll-7" && r.query.Get("action") == "CLOSE" {
			closeAt = i
		}
	}
	readAfter = len(got) - 1
	if closeAt == 0 || got[readAfter].method != http.MethodGet || readAfter < closeAt {
		t.Errorf("requests = %+v; want CLOSE then a read", got)
	}
}

func TestFacebookPollOnAProfileUsesTheUserToken(t *testing.T) {
	srv, reqs := fbServer(t, func(*http.Request) (int, string) { return 200, `{"id":"p"}` })
	a := fbAdapter(t, srv.URL, func(c *FacebookConfig) {
		c.PageAccess = func(context.Context) (string, string, error) { return "", "", oauth.ErrNotAPage }
	})
	if _, err := a.CreatePoll(context.Background(), "q", []string{"a", "b"}); err != nil {
		t.Fatal(err)
	}
	if got := reqs()[0].auth; got != "Bearer fb-token" {
		t.Errorf("auth = %q, want the user token for a profile", got)
	}
	// And with no resolver at all.
	a = fbAdapter(t, srv.URL, nil)
	if _, err := a.CreatePoll(context.Background(), "q", []string{"a", "b"}); err != nil {
		t.Fatal(err)
	}
}

func TestFacebookPollRefusals(t *testing.T) {
	ctx := context.Background()
	status, body := 200, `{}`
	srv, _ := fbServer(t, func(*http.Request) (int, string) { return status, body })
	a := fbAdapter(t, srv.URL, func(c *FacebookConfig) { c.PageAccess = fbPage("123", "t") })

	// No id back.
	if _, err := a.CreatePoll(ctx, "q", []string{"a", "b"}); err == nil || !strings.Contains(err.Error(), "no poll id") {
		t.Errorf("no id: %v", err)
	}
	// Not live yet: a 400.
	status, body = 400, `{"error":{"code":100,"message":"bad"}}`
	if _, err := a.CreatePoll(ctx, "q", []string{"a", "b"}); err == nil || !strings.Contains(err.Error(), "live right now") {
		t.Errorf("not live: %v", err)
	}
	// Permission.
	status, body = 403, `{"error":{"code":200,"message":"perm"}}`
	_, err := a.CreatePoll(ctx, "q", []string{"a", "b"})
	if err == nil || !strings.Contains(err.Error(), "pages_manage_posts") {
		t.Errorf("permission: %v", err)
	}
	if _, err := a.ClosePoll(ctx, "p"); err == nil || !strings.Contains(err.Error(), "lack of a permission") {
		t.Errorf("close permission: %v", err)
	}
	// Token.
	status, body = 401, `{"error":{"code":190,"message":"tok"}}`
	if _, err := a.PollVotes(ctx, "p"); !IsFatal(err) {
		t.Errorf("expired token should be fatal: %v", err)
	}
	// No options in the read.
	status, body = 200, `{"poll_options":{"data":[]}}`
	if _, err := a.PollVotes(ctx, "p"); err == nil || !strings.Contains(err.Error(), "no options") {
		t.Errorf("no options: %v", err)
	}
	// No live video, and a resolver that fails.
	a2 := fbAdapter(t, srv.URL, func(c *FacebookConfig) { c.LiveVideoID = "" })
	if _, err := a2.CreatePoll(ctx, "q", []string{"a", "b"}); err == nil || !strings.Contains(err.Error(), "no live video") {
		t.Errorf("no live video: %v", err)
	}
	a3 := fbAdapter(t, srv.URL, func(c *FacebookConfig) {
		c.PageAccess = func(context.Context) (string, string, error) { return "", "", errors.New("boom") }
	})
	for name, err := range map[string]error{
		"create": func() error { _, e := a3.CreatePoll(ctx, "q", []string{"a", "b"}); return e }(),
		"votes":  func() error { _, e := a3.PollVotes(ctx, "p"); return e }(),
		"close":  func() error { _, e := a3.ClosePoll(ctx, "p"); return e }(),
	} {
		if err == nil || err.Error() != "boom" {
			t.Errorf("%s with a failing resolver: %v", name, err)
		}
	}
}
