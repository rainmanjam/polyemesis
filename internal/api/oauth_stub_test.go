package api

// A stub for every platform this package talks to, and the reason it replaced
// five function pointers on Server.
//
// Until internal/oauth grew WithBaseURL there was no way for a test in THIS
// package to aim a provider at anything it controlled: Providers() resolved
// against the platforms' real hosts and every base URL was unexported. So the
// tests that had to prove a call still happened -- that the composer's tags
// reach PushMetadata, that a stored COPPA declaration reaches PushCompliance,
// that refresh-key still maps the destination's Facebook options -- replaced a
// closure on Server instead, and asserted on the arguments it captured.
//
// That proved the call was made with the right arguments and nothing about what
// left the process. Everything here asserts one layer further out: the real
// provider runs, makes its real request, and this records what arrived. A
// provider that stopped sending a field now fails the test that names it, which
// the closure could not have noticed.
//
// ONE SERVER FOR ALL FOUR PLATFORMS, because oauth.WithBaseURL redirects a
// whole set at one base -- deliberately, so a partially redirected provider
// cannot exist. The paths do not collide in practice and the switch below is
// ordered longest-first where they come close.

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/oauth"
)

// stubCall is one request a provider made. The body is decoded because that is
// where YouTube, Twitch and Kick put everything; Facebook puts its parameters
// in the query string, so both are recorded for every call.
type stubCall struct {
	Method string
	Path   string
	Query  url.Values
	Body   map[string]any
	Auth   string
}

// String renders a call the way a failure message wants it.
func (c stubCall) String() string {
	s := c.Method + " " + c.Path
	if len(c.Query) > 0 {
		s += "?" + c.Query.Encode()
	}
	return s
}

// platformStub is the stub API plus the log of what reached it.
//
// Every accessor takes the lock: the metadata push fans out one goroutine per
// account, so two providers really are calling this at once and -race watches
// it.
type platformStub struct {
	URL string

	mu   sync.Mutex
	seen []stubCall

	// rejectIf, when set, is consulted for every request: a non-empty answer is
	// the message the stub refuses with instead of answering normally. It is how
	// a test makes ONE endpoint fail -- a compliance write, a broadcast write --
	// without replacing the stub for every other call the same push makes.
	rejectIf func(stubCall) string
	// onCall runs after the request is recorded and before it is answered, with
	// the stub's lock RELEASED. It is where a test that needs two platform
	// calls genuinely in flight at once does its rendezvous.
	onCall func(stubCall)
	// fbLiveID is the live video id the Facebook create returns, and the
	// pre-announce tests hand out a list so two event pages can be told apart.
	fbLiveID  string
	fbLiveIDs []string
	// fbKey is the stream key inside the ingest URL the create returns, and
	// fbBackupKey the one inside the secondary URL beside it.
	fbKey       string
	fbBackupKey string
	// fbBackups, when false, strips the secondary ingest URLs from the create
	// response so a test can watch the missing-backup warning.
	fbBackups bool
	// fbCreateErr, when set, is the Graph error the create and the reschedule
	// both answer with.
	fbCreateErr string
	// duringFacebookCreate runs inside the create request, which is where an
	// operator edit lands in production: the sweep read the destination before
	// it and writes after it.
	duringFacebookCreate func()
}

// newPlatformStub stands up the stub and returns it. Close is registered on the
// test, so a caller never has to remember it.
func newPlatformStub(t *testing.T) *platformStub {
	t.Helper()
	p := &platformStub{fbLiveID: "fb-live-1", fbKey: "sk-from-the-broadcast", fbBackupKey: "backup-key", fbBackups: true}
	srv := httptest.NewServer(http.HandlerFunc(p.serve))
	t.Cleanup(srv.Close)
	p.URL = srv.URL
	return p
}

// set is what goes into Options.Providers: every provider in it points here.
func (p *platformStub) set() oauth.Set {
	return oauth.NewSet(oauth.WithBaseURL(p.URL))
}

func (p *platformStub) serve(w http.ResponseWriter, r *http.Request) {
	body := map[string]any{}
	if raw, err := io.ReadAll(r.Body); err == nil && len(raw) > 0 {
		if json.Unmarshal(raw, &body) != nil {
			body = map[string]any{}
		}
	}
	p.mu.Lock()
	p.seen = append(p.seen, stubCall{
		Method: r.Method, Path: r.URL.Path, Query: r.URL.Query(),
		Body: body, Auth: r.Header.Get("Authorization"),
	})
	call := p.seen[len(p.seen)-1]
	reject, onCall, liveID, createErr := p.rejectIf, p.onCall, p.nextLiveIDLocked(r), p.fbCreateErr
	key, backupKey := p.fbKey, p.fbBackupKey
	backups, during := p.fbBackups, p.duringFacebookCreate
	p.mu.Unlock()

	if onCall != nil {
		onCall(call)
	}
	if reject != nil {
		if msg := reject(call); msg != "" {
			writeStubError(w, msg)
			return
		}
	}

	switch {
	// ------------------------------------------------------------- YouTube
	case r.URL.Path == "/liveBroadcasts" && r.Method == http.MethodGet:
		writeStubJSON(w, map[string]any{"items": []map[string]any{{
			"id": "yt-broadcast-1",
			"snippet": map[string]any{
				"title":              "Tonight's broadcast",
				"scheduledStartTime": "2030-01-01T20:00:00Z",
			},
			"status": map[string]any{"lifeCycleStatus": "ready"},
		}}})
	case r.URL.Path == "/liveBroadcasts", r.URL.Path == "/videos":
		writeStubJSON(w, map[string]any{"id": "yt-broadcast-1"})
	case r.URL.Path == "/videoCategories":
		writeStubJSON(w, map[string]any{"items": []map[string]any{
			{"id": "20", "snippet": map[string]any{"title": "Gaming"}},
		}})

	// -------------------------------------------------------------- Twitch
	case r.URL.Path == "/games", r.URL.Path == "/search/categories":
		writeStubJSON(w, map[string]any{"data": []map[string]any{{"id": "509658", "name": "Just Chatting"}}})
	case r.URL.Path == "/channels":
		// Helix answers a successful PATCH with 204 and no body.
		w.WriteHeader(http.StatusNoContent)

	// ---------------------------------------------------------------- Kick
	case strings.HasPrefix(r.URL.Path, "/public/v1/"):
		writeStubJSON(w, map[string]any{"data": []any{}})

	// ------------------------------------------------------------ Facebook
	case r.URL.Path == "/search":
		// The ad-interest lookup resolveTags makes, one word at a time. The id
		// echoes the word so a test can assert which words were looked up and
		// which ids reached content_tags in one read.
		writeStubJSON(w, map[string]any{"data": []map[string]any{
			{"id": "interest-" + r.URL.Query().Get("q"), "name": r.URL.Query().Get("q")},
		}})
	case r.URL.Path == "/me":
		writeStubJSON(w, map[string]any{"id": "1000", "name": "Ada Lovelace"})
	case r.URL.Path == "/me/accounts":
		// No Pages, so resolveTarget falls through to the profile and every
		// Facebook call in this package addresses the "me" node.
		writeStubJSON(w, map[string]any{"data": []any{}})
	case strings.HasSuffix(r.URL.Path, "/live_videos") && r.Method == http.MethodGet:
		writeStubJSON(w, map[string]any{"data": []map[string]any{
			{"id": liveID, "status": "LIVE", "title": "Tonight's broadcast"},
		}})
	case strings.HasSuffix(r.URL.Path, "/live_videos"):
		if during != nil {
			during()
		}
		if createErr != "" {
			writeStubError(w, createErr)
			return
		}
		writeStubJSON(w, fbLiveVideo(liveID, key, backupKey, backups))
	default:
		// Anything else is a Facebook live video NODE: a read of one, or the
		// POST that reschedules or edits it. A path this stub genuinely does
		// not know cannot be told apart from those, so the reschedule error is
		// applied here too rather than 404ing and reading as a Graph refusal.
		if createErr != "" {
			writeStubError(w, createErr)
			return
		}
		writeStubJSON(w, fbLiveVideo(strings.TrimPrefix(r.URL.Path, "/"), key, backupKey, backups))
	}
}

// nextLiveIDLocked hands out the create ids in order when a test supplied a
// list, so two event pages created in one sweep can be told apart. Called with
// the lock held; only a create consumes an id.
func (p *platformStub) nextLiveIDLocked(r *http.Request) string {
	if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/live_videos") {
		return p.fbLiveID
	}
	if len(p.fbLiveIDs) > 0 {
		id := p.fbLiveIDs[0]
		p.fbLiveIDs = p.fbLiveIDs[1:]
		return id
	}
	return p.fbLiveID
}

// fbLiveVideo is what Graph returns from a live_videos create: an id and both
// the plain and secure ingest URLs, with the backup pair when the test wants
// one.
func fbLiveVideo(id, key, backupKey string, backups bool) map[string]any {
	out := map[string]any{
		"id":                id,
		"status":            "LIVE_NOW",
		"title":             "Tonight's broadcast",
		"secure_stream_url": "rtmps://live.example/rtmp/" + key,
		// The read-back UpdateLiveVideoPrivacy confirms against. EVERYONE
		// deliberately disagrees with anything a test asks for, so the default
		// answer is "Facebook did not confirm the change" -- the failure
		// channel that carries no error, which is the one worth defaulting to.
		"privacy": map[string]any{"value": "EVERYONE"},
	}
	if backups {
		out["secure_stream_secondary_urls"] = []string{"rtmps://backup.example/rtmp/" + backupKey}
	}
	return out
}

func writeStubJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// writeStubError answers in Graph's own envelope, so the provider's error text
// carries the message a test asserts on rather than a decode complaint.
func writeStubError(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadRequest)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{"message": msg, "type": "OAuthException", "code": 100},
	})
}

// ----------------------------------------------------------------- reading

// calls returns a copy of the log.
func (p *platformStub) calls() []stubCall {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]stubCall(nil), p.seen...)
}

// matching returns every call to method and path.
func (p *platformStub) matching(method, path string) []stubCall {
	var out []stubCall
	for _, c := range p.calls() {
		if c.Method == method && c.Path == path {
			out = append(out, c)
		}
	}
	return out
}

// first returns the first call to method and path, or nil.
func (p *platformStub) first(method, path string) *stubCall {
	m := p.matching(method, path)
	if len(m) == 0 {
		return nil
	}
	return &m[0]
}

// reset drops the log. Used where a test drives two sweeps and only the second
// one's calls are the subject.
func (p *platformStub) reset() {
	p.mu.Lock()
	p.seen = nil
	p.mu.Unlock()
}

// set replaces the mutable knobs under the lock, because a sweep running in
// another goroutine reads them.
func (p *platformStub) setCreateErr(msg string) {
	p.mu.Lock()
	p.fbCreateErr = msg
	p.mu.Unlock()
}

func (p *platformStub) setDuringCreate(fn func()) {
	p.mu.Lock()
	p.duringFacebookCreate = fn
	p.mu.Unlock()
}

func (p *platformStub) setReject(fn func(stubCall) string) {
	p.mu.Lock()
	p.rejectIf = fn
	p.mu.Unlock()
}

func (p *platformStub) setOnCall(fn func(stubCall)) {
	p.mu.Lock()
	p.onCall = fn
	p.mu.Unlock()
}

// stubCallWith finds the first call to method and path whose query carries
// key=value. YouTube distinguishes its writes by the `part` parameter and
// Twitch by `broadcaster_id`, so "which write was this" is a query question
// rather than a path one.
func stubCallWith(p *platformStub, method, path, key, value string) *stubCall {
	for _, c := range p.matching(method, path) {
		if c.Query.Get(key) == value {
			found := c
			return &found
		}
	}
	return nil
}

// nestedAny reads body[outer][inner], or nil. JSON decodes every object to
// map[string]any, so a missing level is an absent value rather than a panic.
func nestedAny(body map[string]any, outer, inner string) any {
	m, ok := body[outer].(map[string]any)
	if !ok {
		return nil
	}
	return m[inner]
}

func nestedString(body map[string]any, outer, inner string) string {
	s, _ := nestedAny(body, outer, inner).(string)
	return s
}
