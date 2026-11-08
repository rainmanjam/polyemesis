package chat

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Credentials the strict stub insists on. The webhook and subscription-list routes are app-only (the Webhooks
// introduction: "All endpoints require OAuth2 App Only Bearer Token"); the subscription create and delete carry the
// user's broadcast.read token. A call with the other credential is refused, so a test cannot pass by sending the
// wrong one.
const (
	xStubAppBearer = "Bearer app-bearer"
	xStubUserToken = "Bearer user-token"
	xStubHookURL   = "https://polyemesis.example.com/api/v1/webhooks/x"
)

type xStubWebhook struct {
	ID    string
	URL   string
	Valid bool
}

// xChatStub serves the X endpoints the chat adapter calls. It records every request so a test can assert on the
// exact path, method and body, and it answers from the fixtures below. Anything it does not know is a 418, so an
// endpoint the adapter should not be calling (the old GET /2/activity/stream among them) fails loudly. Nothing
// reaches api.x.com: the adapter's base is pointed at this server.
type xChatStub struct {
	mu    sync.Mutex
	calls []string // "METHOD path body"
	srv   *httptest.Server

	// broadcasts is what GET /2/broadcasts answers.
	broadcasts string
	// broadcastsDown makes GET /2/broadcasts fail with a 503.
	broadcastsDown bool

	// webhooks is what GET /2/webhooks lists and what POST appends to.
	webhooks []xStubWebhook
	// createValid is the "valid" a POST /2/webhooks answers; false is a webhook whose CRC failed.
	createValid bool
	// createStatus, when non-zero, makes POST /2/webhooks fail with that status and X's problem body.
	createStatus int
	// createBody is X's problem body for a refused create; empty means the generic invalid-request one.
	createBody string
	// onSubscribe runs inside POST /2/activity/subscriptions, before the answer, so a test can see what the adapter
	// can already do at the moment X would start delivering.
	onSubscribe func()
	// failSubscribeAt makes the nth POST /2/activity/subscriptions (1-based) fail with a 500.
	failSubscribeAt int
	subscribeCalls  int
	// putFailures is how many PUT /2/webhooks/{id} calls fail with a 500 before one works.
	putFailures int
	// putStatus, when non-zero, makes every PUT /2/webhooks/{id} fail with that status and a problem body.
	putStatus int
	// createRaces makes a refused create still leave the webhook behind, as another process winning the race does.
	createRaces bool
	// putFixes makes PUT /2/webhooks/{id} flip the webhook to valid, as a passing CRC does.
	putFixes bool
	// subs is what GET /2/activity/subscriptions lists, as raw JSON objects, and what POST appends to.
	subs []map[string]any
	// refuseEvent makes POST /2/activity/subscriptions for that event type fail with the given status and body.
	refuseEvent map[string]xStubRefusal
	// subscription overrides the POST /2/activity/subscriptions answer.
	subscription string
	nextID       int

	// mutePages is what GET /2/broadcasts/chat/mutes answers, by the pagination_token it was asked with ("" first).
	mutePages map[string]string
	// queries records the query string of every request, aligned with calls.
	queries []string
	// modStatus and modBody, when set, make POST /2/broadcasts/chat/moderators answer that failure.
	modStatus int
	modBody   string
	// appRevoked makes every app-bearer route answer X's probed 401, as a regenerated bearer token does.
	appRevoked atomic.Bool
	// sendBody overrides the POST /2/broadcasts/{id}/chat answer.
	sendBody string
}

// xStubRefusal is one canned failure of a subscription create.
type xStubRefusal struct {
	status int
	body   string
	// times limits it to the first n creates; 0 means every one.
	times int
	used  int
}

func newXChatStub(t *testing.T) *xChatStub {
	t.Helper()
	st := &xChatStub{
		broadcasts:  `{"data":[{"id":"B1run","state":"RUNNING"},{"id":"B0done","state":"ENDED"}]}`,
		createValid: true,
		putFixes:    true,
		nextID:      100,
	}
	st.srv = httptest.NewServer(http.HandlerFunc(st.serve))
	t.Cleanup(st.srv.Close)
	return st
}

func (st *xChatStub) refuse(w http.ResponseWriter, want string, r *http.Request) bool {
	if want == xStubAppBearer && st.appRevoked.Load() {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"title":"Unauthorized","type":"about:blank","status":401,"detail":"Unauthorized"}`)
		return true
	}
	if got := r.Header.Get("Authorization"); got != want {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"title":"Unauthorized","detail":"wrong credential for `+r.Method+` `+r.URL.Path+`"}`)
		return true
	}
	return false
}

func (st *xChatStub) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	st.mu.Lock()
	st.calls = append(st.calls, r.Method+" "+r.URL.Path+" "+string(body))
	st.queries = append(st.queries, r.URL.RawQuery)
	st.mu.Unlock()
	path := r.URL.Path

	switch {
	case path == "/2/users/me":
		_, _ = io.WriteString(w, `{"data":{"id":"1234567890","username":"sundaypractice"}}`)
	case r.Method == http.MethodGet && path == "/2/broadcasts":
		st.mu.Lock()
		b, down := st.broadcasts, st.broadcastsDown
		st.mu.Unlock()
		if down {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, `{"title":"Service Unavailable","detail":"try again"}`)
			return
		}
		_, _ = io.WriteString(w, b)

	// ---- webhooks: app bearer
	case r.Method == http.MethodGet && path == "/2/webhooks":
		if st.refuse(w, xStubAppBearer, r) {
			return
		}
		// `valid` is only guaranteed when asked for (webhook_config.fields), so the adapter must ask.
		if f := r.URL.Query().Get("webhook_config.fields"); !strings.Contains(f, "valid") || !strings.Contains(f, "url") || !strings.Contains(f, "id") {
			http.Error(w, "webhook_config.fields must name id,url,valid", http.StatusTeapot)
			return
		}
		st.mu.Lock()
		list := make([]map[string]any, 0, len(st.webhooks))
		for _, h := range st.webhooks {
			list = append(list, map[string]any{"id": h.ID, "url": h.URL, "valid": h.Valid, "created_at": "2026-11-01T00:00:00.000Z"})
		}
		st.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"data": list, "meta": map[string]any{"result_count": len(list)}})
	case r.Method == http.MethodPost && path == "/2/webhooks":
		if st.refuse(w, xStubAppBearer, r) {
			return
		}
		var req struct {
			URL string `json:"url"`
		}
		_ = json.Unmarshal(body, &req)
		st.mu.Lock()
		defer st.mu.Unlock()
		if st.createStatus != 0 {
			if st.createRaces {
				st.nextID++
				st.webhooks = append(st.webhooks, xStubWebhook{ID: fmt.Sprint(st.nextID), URL: req.URL, Valid: true})
			}
			w.WriteHeader(st.createStatus)
			b := st.createBody
			if b == "" {
				b = `{"title":"Invalid Request","detail":"refused","type":"https://api.x.com/2/problems/invalid-request"}`
			}
			_, _ = io.WriteString(w, b)
			return
		}
		st.nextID++
		h := xStubWebhook{ID: fmt.Sprint(st.nextID), URL: req.URL, Valid: st.createValid}
		st.webhooks = append(st.webhooks, h)
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"id": h.ID, "url": h.URL, "valid": h.Valid, "created_at": "2026-11-06T00:00:00.000Z"}})
	case r.Method == http.MethodPut && strings.HasPrefix(path, "/2/webhooks/"):
		if st.refuse(w, xStubAppBearer, r) {
			return
		}
		id := strings.TrimPrefix(path, "/2/webhooks/")
		st.mu.Lock()
		if st.putStatus != 0 {
			code := st.putStatus
			st.mu.Unlock()
			w.WriteHeader(code)
			_, _ = io.WriteString(w, `{"title":"Too Many Requests","detail":"Too Many Requests","type":"about:blank","status":429}`)
			return
		}
		if st.putFailures > 0 {
			st.putFailures--
			st.mu.Unlock()
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, `{"title":"Internal Error"}`)
			return
		}
		for i := range st.webhooks {
			if st.webhooks[i].ID == id && st.putFixes {
				st.webhooks[i].Valid = true
			}
		}
		st.mu.Unlock()
		// The documented answer says only that the CRC was TRIGGERED, which is why the adapter re-lists.
		_, _ = io.WriteString(w, `{"data":{"valid":true}}`)

	// ---- activity subscriptions
	case r.Method == http.MethodGet && path == "/2/activity/subscriptions":
		if st.refuse(w, xStubAppBearer, r) {
			return
		}
		st.mu.Lock()
		list := append([]map[string]any(nil), st.subs...)
		st.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"data": list})
	case r.Method == http.MethodPost && path == "/2/activity/subscriptions":
		if st.refuse(w, xStubUserToken, r) {
			return
		}
		var req struct {
			EventType string            `json:"event_type"`
			Filter    map[string]string `json:"filter"`
			WebhookID string            `json:"webhook_id"`
			Tag       string            `json:"tag"`
		}
		_ = json.Unmarshal(body, &req)
		if st.onSubscribe != nil {
			st.onSubscribe()
		}
		st.mu.Lock()
		defer st.mu.Unlock()
		if rf, ok := st.refuseEvent[req.EventType]; ok && (rf.times == 0 || rf.used < rf.times) {
			rf.used++
			st.refuseEvent[req.EventType] = rf
			w.WriteHeader(rf.status)
			_, _ = io.WriteString(w, rf.body)
			return
		}
		st.subscribeCalls++
		if st.failSubscribeAt != 0 && st.subscribeCalls == st.failSubscribeAt {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, `{"title":"Internal Error"}`)
			return
		}
		if st.subscription != "" {
			_, _ = io.WriteString(w, st.subscription)
			return
		}
		st.nextID++
		sub := map[string]any{"subscription_id": fmt.Sprint(st.nextID), "event_type": req.EventType,
			"filter": xStubFilter(req.Filter), "webhook_id": req.WebhookID, "tag": req.Tag}
		st.subs = append(st.subs, sub)
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"subscription": sub}})
	case r.Method == http.MethodDelete && strings.HasPrefix(path, "/2/activity/subscriptions/"):
		if st.refuse(w, xStubUserToken, r) {
			return
		}
		id := strings.TrimPrefix(path, "/2/activity/subscriptions/")
		st.mu.Lock()
		kept := st.subs[:0:0]
		for _, s := range st.subs {
			if s["subscription_id"] != id {
				kept = append(kept, s)
			}
		}
		st.subs = kept
		st.mu.Unlock()
		_, _ = io.WriteString(w, `{"data":{"deleted":true}}`)

	// ---- the broadcast chat calls
	case r.Method == http.MethodGet && path == "/2/broadcasts/chat/mutes":
		st.mu.Lock()
		page, ok := st.mutePages[r.URL.Query().Get("pagination_token")]
		st.mu.Unlock()
		if !ok {
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, `{"title":"Forbidden","detail":"only the broadcast owner can list mutes"}`)
			return
		}
		_, _ = io.WriteString(w, page)
	case r.Method == http.MethodGet && path == "/2/broadcasts/chat/moderators":
		_, _ = io.WriteString(w, `{"data":[{"id":"777","username":"helper","name":"Helper"}],"meta":{"result_count":1}}`)
	case r.Method == http.MethodPost && path == "/2/broadcasts/chat/moderators":
		if st.modStatus != 0 {
			w.WriteHeader(st.modStatus)
			_, _ = io.WriteString(w, st.modBody)
			return
		}
		_, _ = io.WriteString(w, `{"data":{"moderator_user_ids":["777","888"]}}`)
	case r.Method == http.MethodDelete && strings.HasPrefix(path, "/2/broadcasts/chat/moderators/"):
		_, _ = io.WriteString(w, `{"data":{"moderator_user_ids":["777"]}}`)
	case r.Method == http.MethodPost && strings.HasSuffix(path, "/chat"):
		if st.sendBody != "" {
			_, _ = io.WriteString(w, st.sendBody)
			return
		}
		_, _ = io.WriteString(w, `{"data":{"success":true,"timestamp":"1761904800000000000"}}`)
	case r.Method == http.MethodPost && strings.HasSuffix(path, "/chat/mutes"):
		_, _ = io.WriteString(w, `{"data":{"muted":true}}`)
	case r.Method == http.MethodDelete && strings.Contains(path, "/chat/mutes/"):
		_, _ = io.WriteString(w, `{"data":{"muted":false}}`)
	case r.Method == http.MethodDelete && strings.Contains(path, "/chat/"):
		_, _ = io.WriteString(w, `{"data":{"deleted":true}}`)
	default:
		http.Error(w, "unexpected "+r.Method+" "+path, http.StatusTeapot)
	}
}

func (st *xChatStub) recorded() []string {
	st.mu.Lock()
	defer st.mu.Unlock()
	return append([]string(nil), st.calls...)
}

// countCalls counts recorded requests that start with prefix ("METHOD path").
func (st *xChatStub) countCalls(prefix string) int {
	n := 0
	for _, c := range st.recorded() {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

func (st *xChatStub) setValid(id string, v bool) {
	st.mu.Lock()
	defer st.mu.Unlock()
	for i := range st.webhooks {
		if st.webhooks[i].ID == id {
			st.webhooks[i].Valid = v
		}
	}
}

func (st *xChatStub) webhookList() []xStubWebhook {
	st.mu.Lock()
	defer st.mu.Unlock()
	return append([]xStubWebhook(nil), st.webhooks...)
}

func (st *xChatStub) subList() []map[string]any {
	st.mu.Lock()
	defer st.mu.Unlock()
	return append([]map[string]any(nil), st.subs...)
}

func (st *xChatStub) adapter(t *testing.T, now time.Time) *XAdapter {
	t.Helper()
	return st.adapterFor(t, "1234567890", now)
}

func (st *xChatStub) adapterFor(t *testing.T, account string, now time.Time) *XAdapter {
	t.Helper()
	a, err := NewX(XConfig{
		AccountRef: account,
		Channel:    "sundaypractice",
		Token:      StaticToken("user-token"),
		AppBearer:  "app-bearer",
		APIBase:    st.srv.URL,
		WebhookURL: xStubHookURL,
		Now:        func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("NewX: %v", err)
	}
	return a
}

// xStubFilter is the filter X lists back: the user id, and the direction when the subscription asked for one.
func xStubFilter(f map[string]string) map[string]any {
	out := map[string]any{"user_id": f["user_id"]}
	if d := f["direction"]; d != "" {
		out["direction"] = d
	}
	return out
}
