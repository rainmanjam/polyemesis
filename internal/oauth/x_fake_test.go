package oauth

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// The ids and key the fake X holds. The source id is 19 digits and is NOT the stream key, as on the /2/sources routes.
const (
	xSourceID    = "9123456789012345678"
	xKey         = "k3y0ne"
	xBroadcastID = "1ynJOZVeajOJR"
)

// xOldRoute matches the routes X removes on 2027-01-01: /2/users/{id}/sources and /2/users/{id}/broadcasts and every
// sub-route. /2/users/me is deliberately not matched.
var xOldRoute = regexp.MustCompile(`^/2/users/[^/]+/(sources|broadcasts)(/|$)`)

// xFake is a strict fake of X's Livestream routes as the migration guide
// (https://docs.x.com/livestream-api/migration-guide.md) describes them. It serves ONLY the new routes: any request to a
// user-scoped source or broadcast route fails the test, bodies with unknown fields are 400s, and a source_id that is not
// digits is a 400, exactly as X answers. It records "METHOD PATH BODY" for every request except /2/users/me.
type xFake struct {
	x   *X
	srv *httptest.Server
	t   *testing.T

	mu   sync.Mutex
	reqs []string
	// meHits counts /2/users/me reads, the one user-scoped route that stays.
	meHits atomic.Int32

	// sources is the GET /2/sources body.
	sources string
	// active is the is_stream_active the source routes report.
	active bool
	// broadcastState is the state GET /2/broadcasts/{id} reports.
	broadcastState string
	// list is the GET /2/broadcasts body.
	list string
	// liveStatus and liveBody, when set, make POST /2/broadcasts answer that status and body.
	liveStatus int
	liveBody   string
	// dateSkew is how far the fake's clock (its Date header) is from the real one.
	dateSkew time.Duration
	// createdAtMS is created_at_ms on GET /2/broadcasts/{id}; empty means xFakeCreatedMS.
	createdAtMS string
	// liveExisting makes POST /2/broadcasts answer 200 (the source already has a live broadcast) instead of 201.
	liveExisting bool
}

// xFakeCreatedMS is when the fake's broadcast was created, and the list fixture's created_at_ms is 10s before it.
const xFakeCreatedMS = "1755302400000"

// xEmptyBroadcastList is GET /2/broadcasts for an account with nothing on air.
const xEmptyBroadcastList = `{"data":[],"meta":{"result_count":0}}`

func newXFake(t *testing.T) *xFake {
	t.Helper()
	f := &xFake{t: t, sources: xSourcesFixture, broadcastState: "RUNNING", list: xBroadcastListFixture}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	f.x = NewX(WithBaseURL(f.srv.URL)).WithLivestreamAccess()
	return f
}

func (f *xFake) requests() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string{}, f.reqs...)
}

func (f *xFake) reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reqs = nil
}

func (f *xFake) problem(w http.ResponseWriter, status int, detail string) {
	w.WriteHeader(status)
	b, _ := json.Marshal(map[string]any{"type": "https://api.x.com/2/problems/invalid-request", "title": "Invalid Request", "detail": detail, "status": status})
	_, _ = w.Write(b)
}

// strictBody decodes body into a map and refuses any key outside allowed.
func (f *xFake) strictBody(w http.ResponseWriter, body []byte, allowed ...string) (map[string]any, bool) {
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		f.problem(w, 400, "body is not JSON")
		return nil, false
	}
	for k := range m {
		ok := false
		for _, a := range allowed {
			ok = ok || a == k
		}
		if !ok {
			f.problem(w, 400, "unknown field "+k)
			return nil, false
		}
	}
	return m, true
}

var xInactive = regexp.MustCompile(`"is_stream_active":\s*false`)

var xDigitID = regexp.MustCompile(`^[0-9]{1,19}$`)

func (f *xFake) serve(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Date", time.Now().Add(f.dateSkew).UTC().Format(http.TimeFormat))
	path := r.URL.Path
	if path == "/2/users/me" {
		f.meHits.Add(1)
		_, _ = io.WriteString(w, xMeFixture)
		return
	}
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	rec := r.Method + " " + path
	if len(body) > 0 {
		rec += " " + string(body)
	}
	f.reqs = append(f.reqs, rec)
	f.mu.Unlock()

	if xOldRoute.MatchString(path) {
		f.t.Errorf("request %s %s hit a user-scoped route X removes on 2027-01-01", r.Method, path)
		http.Error(w, "gone", http.StatusNotFound)
		return
	}
	active := "false"
	if f.active {
		active = "true"
	}
	switch {
	case r.Method == http.MethodGet && path == "/2/region":
		_, _ = io.WriteString(w, `{"region":"us-east-1"}`)
	case r.Method == http.MethodGet && path == "/2/sources":
		_, _ = io.WriteString(w, xInactive.ReplaceAllString(f.sources, `"is_stream_active":`+active))
	case r.Method == http.MethodPost && path == "/2/sources":
		m, ok := f.strictBody(w, body, "name", "region")
		if !ok {
			return
		}
		if m["name"] == nil || m["region"] == nil {
			f.problem(w, 400, "name and region are required")
			return
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"data":{"id":"`+xSourceID+`","name":"polyemesis","region":"us-east-1",`+
			`"rtmp_url":"rtmp://ingest.example.invalid/live","rtmps_url":"rtmps://ingest.example.invalid:443/live",`+
			`"stream_key":"minted-key","is_stream_active":false,"created_at_ms":"1772029802844"}}`)
	case r.Method == http.MethodGet && strings.HasPrefix(path, "/2/sources/") && strings.HasSuffix(path, "/stream_key"):
		id := strings.TrimSuffix(strings.TrimPrefix(path, "/2/sources/"), "/stream_key")
		if !xDigitID.MatchString(id) {
			f.problem(w, 400, "The `id` value is not a source id.")
			return
		}
		if id != xSourceID {
			f.problem(w, 404, "no such source")
			return
		}
		_, _ = io.WriteString(w, `{"data":{"id":"`+xSourceID+`","stream_key":"`+xKey+`"}}`)
	case r.Method == http.MethodPost && path == "/2/broadcasts":
		m, ok := f.strictBody(w, body, "source_id", "title", "locale", "chat_option", "should_not_tweet")
		if !ok {
			return
		}
		if sid, _ := m["source_id"].(string); !xDigitID.MatchString(sid) {
			f.problem(w, 400, "The `source_id` value is not a source id.")
			return
		}
		if v, present := m["chat_option"]; present {
			if n, isNum := v.(float64); !isNum || n < 0 || n > 6 || n != float64(int(n)) {
				f.problem(w, 400, "chat_option must be an integer from 0 to 6")
				return
			}
		}
		if v, present := m["should_not_tweet"]; present {
			if _, isBool := v.(bool); !isBool {
				f.problem(w, 400, "should_not_tweet must be a boolean")
				return
			}
		}
		if f.liveStatus != 0 {
			w.WriteHeader(f.liveStatus)
			_, _ = io.WriteString(w, f.liveBody)
			return
		}
		if f.liveExisting {
			w.WriteHeader(http.StatusOK)
		} else {
			w.WriteHeader(http.StatusCreated)
		}
		_, _ = io.WriteString(w, `{"data":{"id":"`+xBroadcastID+`","broadcast_id":"`+xBroadcastID+`","state":"RUNNING",`+
			`"share_url":"https://x.com/i/broadcasts/`+xBroadcastID+`"}}`)
	case r.Method == http.MethodGet && path == "/2/broadcasts":
		_, _ = io.WriteString(w, f.list)
	case r.Method == http.MethodGet && strings.HasPrefix(path, "/2/broadcasts/"):
		id := strings.TrimPrefix(path, "/2/broadcasts/")
		if id != xBroadcastID && id != "legacyId1234" {
			f.problem(w, 404, "no such broadcast")
			return
		}
		created := f.createdAtMS
		if created == "" {
			created = xFakeCreatedMS
		}
		_, _ = io.WriteString(w, `{"data":{"id":"`+id+`","broadcast_id":"`+id+`","state":"`+f.broadcastState+
			`","source_id":"`+xSourceID+`","title":"Sunday practice","created_at_ms":"`+created+`"}}`)
	case r.Method == http.MethodPut && strings.HasPrefix(path, "/2/broadcasts/") && strings.HasSuffix(path, "/state"):
		m, ok := f.strictBody(w, body, "state")
		if !ok {
			return
		}
		if m["state"] != "END" {
			f.problem(w, 400, "only END is supported")
			return
		}
		id := strings.TrimSuffix(strings.TrimPrefix(path, "/2/broadcasts/"), "/state")
		_, _ = io.WriteString(w, `{"data":{"id":"`+id+`","state":"ENDED"}}`)
	default:
		f.t.Errorf("unexpected request %s %s", r.Method, path)
		http.Error(w, "unexpected", http.StatusTeapot)
	}
}
