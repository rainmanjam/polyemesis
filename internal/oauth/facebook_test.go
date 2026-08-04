package oauth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/routing"
)

// fbReq is one call the provider made to the stub Graph API. The Authorization
// header is recorded because "did the Page branch use the Page token" is the
// whole difference between the two targets.
type fbReq struct {
	Method string
	Path   string
	Query  string
	Auth   string
}

// fbServer points the provider at a stub Graph API and returns the request log.
func fbServer(t *testing.T, h http.HandlerFunc) *[]fbReq {
	t.Helper()
	log := &[]fbReq{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*log = append(*log, fbReq{
			Method: r.Method, Path: r.URL.Path,
			Query: r.URL.RawQuery, Auth: r.Header.Get("Authorization"),
		})
		h(w, r)
	}))
	t.Cleanup(srv.Close)

	orig := fbGraphBase
	fbGraphBase = srv.URL
	t.Cleanup(func() { fbGraphBase = orig })
	return log
}

func fbCall(log []fbReq, method, path string) *fbReq {
	for i := range log {
		if log[i].Method == method && log[i].Path == path {
			return &log[i]
		}
	}
	return nil
}

func writeJSONBody(t *testing.T, w http.ResponseWriter, status int, v any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		t.Errorf("encode stub response: %v", err)
	}
}

// graphStub answers the handful of endpoints this provider touches. A path it
// does not know 404s, which is how a test notices the provider calling
// something unexpected.
func graphStub(t *testing.T, live map[string]any) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/me":
			writeJSONBody(t, w, http.StatusOK, map[string]any{"id": "1000", "name": "Ada Lovelace"})
		case "/me/accounts":
			writeJSONBody(t, w, http.StatusOK, map[string]any{"data": []map[string]any{
				{"id": "555", "name": "Ada's Bakery", "category": "Bakery", "access_token": "page-token"},
			}})
		case "/me/live_videos", "/555/live_videos":
			writeJSONBody(t, w, http.StatusOK, live)
		default:
			http.Error(w, `{"error":{"message":"Unsupported request","code":100}}`, http.StatusNotFound)
		}
	}
}

// fbLiveResponse is what Facebook returns from a live_videos create: an id, an
// rtmp and an rtmps primary, and a backup list of each.
func fbLiveResponse(id string) map[string]any {
	return map[string]any{
		"id":                           id,
		"status":                       "LIVE_NOW",
		"stream_url":                   "rtmp://rtmp-api.facebook.com:80/rtmp/" + id + "?s_bl=1&a=plain",
		"secure_stream_url":            "rtmps://rtmp-api.facebook.com:443/rtmp/" + id + "?s_bl=1&s_psm=1&a=AbCd",
		"stream_secondary_urls":        []string{"rtmp://rtmp-api-backup.facebook.com:80/rtmp/" + id + "?a=plain2"},
		"secure_stream_secondary_urls": []string{"rtmps://rtmp-api-backup.facebook.com:443/rtmp/" + id + "?a=Ef/Gh"},
		"permalink_url":                "/ada/videos/" + id,
		"embed_html":                   "<iframe/>",
	}
}

// ------------------------------------------------------------ URL splitting

func TestFacebookIngestURLSplitsIntoAServerAndAMaskableKey(t *testing.T) {
	tests := []struct {
		name       string
		raw        string
		wantServer string
		wantKey    string
		wantErr    bool
	}{
		{
			name:       "a bare ingest splits at the last path segment",
			raw:        "rtmps://rtmp-api.facebook.com:443/rtmp/1234567890",
			wantServer: "rtmps://rtmp-api.facebook.com:443/rtmp",
			wantKey:    "1234567890",
		},
		{
			name:       "the query string belongs to the key, not to the server",
			raw:        "rtmps://rtmp-api.facebook.com:443/rtmp/999?s_bl=1&s_psm=1&s_sw=0&s_vt=api-s",
			wantServer: "rtmps://rtmp-api.facebook.com:443/rtmp",
			wantKey:    "999?s_bl=1&s_psm=1&s_sw=0&s_vt=api-s",
		},
		{
			name: "a slash inside the query does not move the split point",
			// A base64 signature contains slashes; anchoring on the last slash
			// of the whole string would cut the key in half.
			raw:        "rtmps://rtmp-api.facebook.com:443/rtmp/42?a=AbC/dEf/gHi",
			wantServer: "rtmps://rtmp-api.facebook.com:443/rtmp",
			wantKey:    "42?a=AbC/dEf/gHi",
		},
		{
			name:       "plain rtmp splits the same way",
			raw:        "rtmp://rtmp-api.facebook.com:80/rtmp/77?s_bl=1",
			wantServer: "rtmp://rtmp-api.facebook.com:80/rtmp",
			wantKey:    "77?s_bl=1",
		},
		{
			name:       "a deeper path keeps every segment on the server side",
			raw:        "rtmps://live-api-s.facebook.com:443/rtmp/live/88",
			wantServer: "rtmps://live-api-s.facebook.com:443/rtmp/live",
			wantKey:    "88",
		},
		{
			name:       "a host with no port still splits",
			raw:        "rtmps://rtmp-api.facebook.com/rtmp/1",
			wantServer: "rtmps://rtmp-api.facebook.com/rtmp",
			wantKey:    "1",
		},
		{name: "a trailing slash means no key was issued", raw: "rtmps://rtmp-api.facebook.com:443/rtmp/", wantErr: true},
		{name: "a URL with no path at all is not an ingest", raw: "rtmps://rtmp-api.facebook.com", wantErr: true},
		{name: "a URL with no scheme is not an ingest", raw: "rtmp-api.facebook.com/rtmp/1", wantErr: true},
		{name: "an empty URL is an error rather than an empty destination", raw: "", wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			server, key, err := splitIngestURL(tc.raw)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("splitIngestURL(%q) = %q / %q, want an error", tc.raw, server, key)
				}
				return
			}
			if err != nil {
				t.Fatalf("splitIngestURL(%q): %v", tc.raw, err)
			}
			if server != tc.wantServer {
				t.Errorf("server = %q, want %q", server, tc.wantServer)
			}
			if key != tc.wantKey {
				t.Errorf("key = %q, want %q", key, tc.wantKey)
			}
		})
	}
}

func TestFacebookIngestURLErrorsNeverEchoTheStreamKey(t *testing.T) {
	// The half after /rtmp/ is a credential, so a parse failure must not put it
	// into a log line or an API response.
	const secret = "12345?a=SuperSecretSignature"
	for _, raw := range []string{
		"rtmp-api.facebook.com/rtmp/" + secret,
		"rtmps://rtmp-api.facebook.com:443/rtmp/",
	} {
		_, _, err := splitIngestURL(raw)
		if err == nil {
			t.Fatalf("splitIngestURL(%q) unexpectedly succeeded", raw)
		}
		if strings.Contains(err.Error(), "SuperSecretSignature") || strings.Contains(err.Error(), secret) {
			t.Errorf("error leaked the stream key: %v", err)
		}
	}
}

func TestFacebookSplitRoundTripsThroughTheDestinationTarget(t *testing.T) {
	// The split is only correct if joining it back gives FFmpeg the exact URL
	// Facebook issued — db.Destination.Target() is what does the joining.
	for _, raw := range []string{
		"rtmps://rtmp-api.facebook.com:443/rtmp/1234567890",
		"rtmps://rtmp-api.facebook.com:443/rtmp/999?s_bl=1&s_psm=1&a=AbC/dEf",
		"rtmp://rtmp-api.facebook.com:80/rtmp/42",
	} {
		t.Run(raw, func(t *testing.T) {
			server, key, err := splitIngestURL(raw)
			if err != nil {
				t.Fatalf("splitIngestURL: %v", err)
			}
			d := db.Destination{
				Name: "fb", Kind: db.DestRTMP, Platform: db.PlatformFacebook,
				URL: server, StreamKey: key, AudioBitrate: 160,
				Profile: routing.DefaultProfile(),
			}
			if got := d.Target(); got != raw {
				t.Errorf("Target() = %q, want %q", got, raw)
			}
			// And the destination the split builds must be startable, or the
			// user gets a validation error on a URL they never typed.
			if err := d.Validate(); err != nil {
				t.Errorf("Validate() on a Facebook-issued ingest: %v", err)
			}
		})
	}
}

func TestFacebookLiveVideoIDIsRecoverableFromTheStoredStreamKey(t *testing.T) {
	// The id is needed to end the broadcast and to read its comments, and it is
	// already sitting in the stored key. Anything that is not a bare numeric id
	// must return empty rather than a guess.
	tests := []struct {
		name, key, want string
	}{
		{"a key with query parameters", "1234567890?s_bl=1&s_psm=1", "1234567890"},
		{"a bare key", "1234567890", "1234567890"},
		{"a key with whitespace from a paste", "  1234567890?a=b  ", "1234567890"},
		{"a non-numeric key is not an id", "abc123?s_bl=1", ""},
		{"an empty key", "", ""},
		{"a query with no id", "?s_bl=1", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := FacebookLiveVideoID(tc.key); got != tc.want {
				t.Errorf("FacebookLiveVideoID(%q) = %q, want %q", tc.key, got, tc.want)
			}
		})
	}
}

// ------------------------------------------------------------ user vs Page

func TestFacebookIngestPublishesToTheChosenTarget(t *testing.T) {
	tests := []struct {
		name      string
		ref       string
		wantPath  string
		wantAuth  string
		wantRef   string
		wantPages bool // whether /me/accounts had to be read
	}{
		{
			name:     "no target means the person's own profile",
			ref:      "",
			wantPath: "/me/live_videos",
			wantAuth: "Bearer user-token",
			wantRef:  "",
		},
		{
			name:     "an explicit user ref is still the profile",
			ref:      "user:1000",
			wantPath: "/me/live_videos",
			wantAuth: "Bearer user-token",
			wantRef:  "user:1000",
		},
		{
			name:      "a Page ref publishes to the Page with the Page's own token",
			ref:       "page:555",
			wantPath:  "/555/live_videos",
			wantAuth:  "Bearer page-token",
			wantRef:   "page:555",
			wantPages: true,
		},
		{
			name:      "a bare id is looked up rather than refused",
			ref:       "555",
			wantPath:  "/555/live_videos",
			wantAuth:  "Bearer page-token",
			wantRef:   "page:555",
			wantPages: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			log := fbServer(t, graphStub(t, fbLiveResponse("777")))
			f := &Facebook{}

			b, err := f.IngestFor(context.Background(), "cid", "user-token", tc.ref, IngestOptions{})
			if err != nil {
				t.Fatalf("IngestFor(%q): %v", tc.ref, err)
			}

			post := fbCall(*log, http.MethodPost, tc.wantPath)
			if post == nil {
				t.Fatalf("no POST to %s; calls were %+v", tc.wantPath, *log)
			}
			if post.Auth != tc.wantAuth {
				t.Errorf("Authorization = %q, want %q", post.Auth, tc.wantAuth)
			}
			if !strings.Contains(post.Query, "status=LIVE_NOW") {
				t.Errorf("POST query = %q, want status=LIVE_NOW", post.Query)
			}
			// Removed from the Graph API in v24.0; sending it is now an error.
			if strings.Contains(post.Query, "overlay_url") {
				t.Errorf("POST query sent overlay_url: %q", post.Query)
			}
			if gotPages := fbCall(*log, http.MethodGet, "/me/accounts") != nil; gotPages != tc.wantPages {
				t.Errorf("read /me/accounts = %v, want %v", gotPages, tc.wantPages)
			}

			if b.ID != "777" {
				t.Errorf("broadcast id = %q, want 777", b.ID)
			}
			if b.Target != tc.wantRef {
				t.Errorf("target = %q, want %q", b.Target, tc.wantRef)
			}
			if b.Ingest.URL != "rtmps://rtmp-api.facebook.com:443/rtmp" {
				t.Errorf("ingest URL = %q, want the rtmps server", b.Ingest.URL)
			}
			if b.Ingest.Key != "777?s_bl=1&s_psm=1&a=AbCd" {
				t.Errorf("stream key = %q", b.Ingest.Key)
			}
			// The backup ingest pairs with destination failover; it is captured
			// now because re-reading it later means creating a second broadcast.
			if len(b.Backups) != 1 {
				t.Fatalf("backups = %d, want 1", len(b.Backups))
			}
			if b.Backups[0].URL != "rtmps://rtmp-api-backup.facebook.com:443/rtmp" ||
				b.Backups[0].Key != "777?a=Ef/Gh" {
				t.Errorf("backup ingest = %+v", b.Backups[0])
			}
		})
	}
}

func TestFacebookProviderIngestUsesTheProfileAndHidesTheBroadcastID(t *testing.T) {
	// Provider.Ingest has nowhere to put the live video id, so it is the
	// convenience path; anything that needs the id calls IngestFor.
	log := fbServer(t, graphStub(t, fbLiveResponse("31337")))
	ing, err := (&Facebook{}).Ingest(context.Background(), "cid", "user-token")
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if ing.Key != "31337?s_bl=1&s_psm=1&a=AbCd" {
		t.Errorf("key = %q", ing.Key)
	}
	if fbCall(*log, http.MethodPost, "/me/live_videos") == nil {
		t.Errorf("Ingest did not publish to the profile; calls were %+v", *log)
	}
}

func TestFacebookRefusesAPageItCannotSeeRatherThanFallingBackToTheProfile(t *testing.T) {
	// Publishing a business broadcast to someone's personal timeline because we
	// could not read the Page list would be worse than refusing.
	log := fbServer(t, graphStub(t, fbLiveResponse("1")))
	_, err := (&Facebook{}).IngestFor(context.Background(), "cid", "user-token", "page:999", IngestOptions{})
	if err == nil {
		t.Fatal("IngestFor(page:999) succeeded for a Page this login does not manage")
	}
	if !strings.Contains(err.Error(), "999") {
		t.Errorf("error does not name the Page: %v", err)
	}
	if fbCall(*log, http.MethodPost, "/me/live_videos") != nil {
		t.Errorf("fell back to the profile: %+v", *log)
	}
}

func TestFacebookIngestReadsBackTheLiveVideoWhenTheCreateOmitsIt(t *testing.T) {
	// Belt and braces: if the create response carries no ingest, one read fills
	// it in rather than failing a broadcast that already exists.
	log := fbServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/me/live_videos":
			writeJSONBody(t, w, http.StatusOK, map[string]any{"id": "808"})
		case r.URL.Path == "/808":
			writeJSONBody(t, w, http.StatusOK, fbLiveResponse("808"))
		default:
			http.Error(w, "{}", http.StatusNotFound)
		}
	})

	b, err := (&Facebook{}).IngestFor(context.Background(), "cid", "user-token", "", IngestOptions{})
	if err != nil {
		t.Fatalf("IngestFor: %v", err)
	}
	if b.Ingest.Key != "808?s_bl=1&s_psm=1&a=AbCd" {
		t.Errorf("key = %q", b.Ingest.Key)
	}
	if fbCall(*log, http.MethodGet, "/808") == nil {
		t.Errorf("no follow-up read; calls were %+v", *log)
	}
}

func TestFacebookSendsTheStoredPrivacyWhenTheBroadcastIsCreated(t *testing.T) {
	log := fbServer(t, graphStub(t, fbLiveResponse("77")))
	_, err := (&Facebook{}).IngestFor(context.Background(), "cid", "user-token", "user:1000",
		IngestOptions{Privacy: db.FBPrivacySelf})
	if err != nil {
		t.Fatalf("IngestFor: %v", err)
	}
	post := fbCall(*log, http.MethodPost, "/me/live_videos")
	if post == nil {
		t.Fatalf("no create; calls were %+v", *log)
	}
	q, err := url.ParseQuery(post.Query)
	if err != nil {
		t.Fatalf("parse query: %v", err)
	}
	// Graph wants a privacy OBJECT, not a bare value -- asserting the exact
	// envelope is what catches fbPrivacyParam degrading to plain SELF, which a
	// substring check on the raw query could not tell apart from the real thing.
	if got := q.Get("privacy"); got != `{"value":"SELF"}` {
		t.Errorf("create query carries privacy %q, want the {\"value\":\"SELF\"} object; "+
			"Facebook documents LIVE_VIDEO__PRIVACY_REQUIRED and the operator's choice never arrives", got)
	}
}

func TestFacebookSendsNoPrivacyParameterWhenNoneWasChosen(t *testing.T) {
	// ABSENT, not empty. A request carrying privacy= is a different request from
	// one that omits it, and only the second means "leave it alone". Checked by
	// key presence in the parsed query rather than a raw substring, so a future
	// field that also happens to contain "privacy" cannot make this pass for the
	// wrong reason.
	log := fbServer(t, graphStub(t, fbLiveResponse("77")))
	_, err := (&Facebook{}).IngestFor(context.Background(), "cid", "user-token", "user:1000",
		IngestOptions{})
	if err != nil {
		t.Fatalf("IngestFor: %v", err)
	}
	post := fbCall(*log, http.MethodPost, "/me/live_videos")
	if post == nil {
		t.Fatalf("no create; calls were %+v", *log)
	}
	q, err := url.ParseQuery(post.Query)
	if err != nil {
		t.Fatalf("parse query: %v", err)
	}
	if _, ok := q["privacy"]; ok {
		t.Errorf("create query %q sends a privacy nobody chose", post.Query)
	}
}

func TestAPageBroadcastCarriesNoPersonalAudienceValue(t *testing.T) {
	// The one thing standing between an operator's SELF choice and a public
	// Page broadcast is tgt.kind != fbKindPage. A Page has no personal
	// audience for Facebook to apply the value to.
	log := fbServer(t, graphStub(t, fbLiveResponse("77")))
	_, err := (&Facebook{}).IngestFor(context.Background(), "cid", "user-token", "page:555",
		IngestOptions{Privacy: db.FBPrivacySelf})
	if err != nil {
		t.Fatalf("IngestFor: %v", err)
	}
	post := fbCall(*log, http.MethodPost, "/555/live_videos")
	if post == nil {
		t.Fatalf("no create on the Page; calls were %+v", *log)
	}
	// Pin that this really reached the Page, so the test cannot pass by the
	// create silently landing on the profile instead.
	if post.Auth != "Bearer page-token" {
		t.Fatalf("create used %q, not the Page's own token", post.Auth)
	}
	if strings.Contains(post.Query, "privacy") {
		t.Errorf("create query %q sends a personal audience value to a Page", post.Query)
	}
}

func TestFacebookCrosspostingCarriesTheActionEachPageAskedFor(t *testing.T) {
	log := fbServer(t, graphStub(t, fbLiveResponse("77")))
	_, err := (&Facebook{}).IngestFor(context.Background(), "cid", "user-token", "user:1000",
		IngestOptions{Crosspost: []db.CrosspostTarget{
			{PageID: "1234", CreatePost: true},
			{PageID: "5678"},
		}})
	if err != nil {
		t.Fatalf("IngestFor: %v", err)
	}
	post := fbCall(*log, http.MethodPost, "/me/live_videos")
	if post == nil {
		t.Fatalf("no create; calls were %+v", *log)
	}
	raw, err := url.QueryUnescape(post.Query)
	if err != nil {
		t.Fatalf("unescape: %v", err)
	}
	if !strings.Contains(raw, `"page_id":"1234"`) ||
		!strings.Contains(raw, `"action":"enable_crossposting_and_create_post"`) {
		t.Errorf("query %q does not ask 1234 to create a post", raw)
	}
	if !strings.Contains(raw, `"page_id":"5678"`) ||
		!strings.Contains(raw, `"action":"enable_crossposting"`) {
		t.Errorf("query %q does not share with 5678 without posting", raw)
	}
	// The dangerous direction: a page that did NOT ask for a post must not get
	// one. Count the two action spellings rather than trusting the pair above.
	if strings.Count(raw, "enable_crossposting_and_create_post") != 1 {
		t.Errorf("query %q posts as more Pages than asked", raw)
	}
}

func TestFacebookSendsNoCrosspostingOrDonateWhenNoneIsStored(t *testing.T) {
	log := fbServer(t, graphStub(t, fbLiveResponse("77")))
	_, err := (&Facebook{}).IngestFor(context.Background(), "cid", "user-token", "user:1000",
		IngestOptions{})
	if err != nil {
		t.Fatalf("IngestFor: %v", err)
	}
	post := fbCall(*log, http.MethodPost, "/me/live_videos")
	if post == nil {
		t.Fatalf("no create; calls were %+v", *log)
	}
	q, err := url.ParseQuery(post.Query)
	if err != nil {
		t.Fatalf("parse query: %v", err)
	}
	if _, ok := q["crossposting_actions"]; ok {
		t.Errorf("create query %q crossposts to Pages nobody named", post.Query)
	}
	if _, ok := q["donate_button_charity_id"]; ok {
		t.Errorf("create query %q adds a donate button nobody asked for", post.Query)
	}
}

func TestFacebookDonateButtonReachesTheCreateWhenStored(t *testing.T) {
	// The mirror of the absence test above: absence was covered, presence was
	// not, and a stored charity id could have been silently dropped forever.
	log := fbServer(t, graphStub(t, fbLiveResponse("77")))
	_, err := (&Facebook{}).IngestFor(context.Background(), "cid", "user-token", "user:1000",
		IngestOptions{DonateCharityID: "999"})
	if err != nil {
		t.Fatalf("IngestFor: %v", err)
	}
	post := fbCall(*log, http.MethodPost, "/me/live_videos")
	if post == nil {
		t.Fatalf("no create; calls were %+v", *log)
	}
	q, err := url.ParseQuery(post.Query)
	if err != nil {
		t.Fatalf("parse query: %v", err)
	}
	if got := q.Get("donate_button_charity_id"); got != "999" {
		t.Errorf("create query carries donate_button_charity_id %q, want 999", got)
	}
}

func TestARefusedCreateFailsTheKeyFetchRatherThanReturningAKey(t *testing.T) {
	// A broadcast created with the wrong privacy is the unrecoverable case this
	// whole sub-project exists for. Handing back a stream key for one is worse
	// than handing back an error: the operator streams to it.
	fbServer(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/live_videos") {
			http.Error(w, `{"error":{"message":"(#100) Invalid privacy setting"}}`,
				http.StatusBadRequest)
			return
		}
		http.Error(w, "{}", http.StatusNotFound)
	})
	b, err := (&Facebook{}).IngestFor(context.Background(), "cid", "user-token", "user:1000",
		IngestOptions{Privacy: db.FBPrivacySelf})
	if err == nil {
		t.Fatalf("a refused create returned a broadcast %+v instead of an error", b)
	}
	if b != nil {
		t.Errorf("broadcast = %+v, want nil alongside the error", b)
	}
}

func TestFacebookAccountNamesTheTargetItConnected(t *testing.T) {
	tests := []struct {
		name     string
		ref      string
		wantName string
		wantRef  string
		wantErr  bool
	}{
		{"the default target is the person's profile", "", "Ada Lovelace", "user:1000", false},
		{"a Page is labelled so it cannot be confused with the person", "page:555", "Ada's Bakery (Page)", "page:555", false},
		{"a bare id resolves to the Page it names", "555", "Ada's Bakery (Page)", "page:555", false},
		{"a Page this login does not manage is an error", "page:999", "", "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fbServer(t, graphStub(t, fbLiveResponse("1")))
			acct, err := (&Facebook{}).AccountFor(context.Background(), "cid", "user-token", tc.ref)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("AccountFor(%q) = %+v, want an error", tc.ref, acct)
				}
				return
			}
			if err != nil {
				t.Fatalf("AccountFor(%q): %v", tc.ref, err)
			}
			if acct.Name != tc.wantName || acct.Ref != tc.wantRef {
				t.Errorf("account = %q/%q, want %q/%q", acct.Name, acct.Ref, tc.wantName, tc.wantRef)
			}
		})
	}
}

func TestFacebookTargetsOffersEveryPageAndSurvivesHavingNone(t *testing.T) {
	tests := []struct {
		name     string
		pages    func(w http.ResponseWriter)
		wantRefs []string
	}{
		{
			name: "a token that can list Pages offers the profile and the Pages",
			pages: func(w http.ResponseWriter) {
				writeJSONBody(t, w, http.StatusOK, map[string]any{"data": []map[string]any{
					{"id": "555", "name": "Ada's Bakery", "access_token": "page-token"},
					{"id": "556", "name": "Ada's Other Bakery", "access_token": "page-token-2"},
				}})
			},
			wantRefs: []string{"user:1000", "page:555", "page:556"},
		},
		{
			name: "a token without pages_show_list is a profile-only connection, not a failure",
			pages: func(w http.ResponseWriter) {
				http.Error(w, `{"error":{"message":"(#200) Requires pages_show_list permission","type":"OAuthException","code":200}}`,
					http.StatusForbidden)
			},
			wantRefs: []string{"user:1000"},
		},
		{
			name: "a login that manages nothing offers only the profile",
			pages: func(w http.ResponseWriter) {
				writeJSONBody(t, w, http.StatusOK, map[string]any{"data": []map[string]any{}})
			},
			wantRefs: []string{"user:1000"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fbServer(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/me":
					writeJSONBody(t, w, http.StatusOK, map[string]any{"id": "1000", "name": "Ada Lovelace"})
				case "/me/accounts":
					tc.pages(w)
				default:
					http.Error(w, "{}", http.StatusNotFound)
				}
			})

			got, err := (&Facebook{}).Targets(context.Background(), "cid", "user-token")
			if err != nil {
				t.Fatalf("Targets: %v", err)
			}
			if len(got) != len(tc.wantRefs) {
				t.Fatalf("Targets returned %d entries (%+v), want %d", len(got), got, len(tc.wantRefs))
			}
			for i, want := range tc.wantRefs {
				if got[i].Ref != want {
					t.Errorf("target %d ref = %q, want %q", i, got[i].Ref, want)
				}
			}
			if got[0].Kind != "user" {
				t.Errorf("the profile must come first, got kind %q", got[0].Kind)
			}
		})
	}
}

func TestFacebookIsDiscoverableAsATargetedProviderAndOthersAreNot(t *testing.T) {
	tests := []struct {
		name     string
		platform db.Platform
		want     bool
	}{
		{"facebook publishes to a profile or a Page", db.PlatformFacebook, true},
		{"youtube has one channel per account", db.PlatformYouTube, false},
		{"twitch has one channel per account", db.PlatformTwitch, false},
		{"an unknown platform is absent rather than an error", db.Platform("mystery"), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, ok := TargetsFor(tc.platform); ok != tc.want {
				t.Errorf("TargetsFor(%s) = %v, want %v", tc.platform, ok, tc.want)
			}
		})
	}
}

// --------------------------------------------------------------- metadata

func TestFacebookPushMetadataEditsTheBroadcastThatIsOnAir(t *testing.T) {
	log := fbServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/me/live_videos":
			// Newest first, and the finished VOD must never be the one edited.
			writeJSONBody(t, w, http.StatusOK, map[string]any{"data": []map[string]any{
				{"id": "3", "status": "VOD", "title": "Last week"},
				{"id": "2", "status": "LIVE", "title": "On air"},
				{"id": "1", "status": "UNPUBLISHED", "title": "Staged"},
			}})
		case "/2":
			writeJSONBody(t, w, http.StatusOK, map[string]any{"success": true})
		default:
			http.Error(w, "{}", http.StatusNotFound)
		}
	})

	res, err := (&Facebook{}).PushMetadata(context.Background(), "cid", "user-token", "user:1000",
		Metadata{Title: "Tonight's show", Description: "  with guests  ", Category: "Gaming"})
	if err != nil {
		t.Fatalf("PushMetadata: %v", err)
	}

	post := fbCall(*log, http.MethodPost, "/2")
	if post == nil {
		t.Fatalf("did not edit the live broadcast; calls were %+v", *log)
	}
	if !strings.Contains(post.Query, "title=Tonight%27s+show") {
		t.Errorf("POST query = %q, want the new title", post.Query)
	}
	if !strings.Contains(post.Query, "description=with+guests") {
		t.Errorf("POST query = %q, want the trimmed description", post.Query)
	}
	if len(res.Applied) != 2 {
		t.Errorf("applied = %v, want title and description", res.Applied)
	}
	// Facebook Live has no category, and saying so up front is what keeps it
	// out of the failure list.
	if len(res.Skipped) != 1 || res.Skipped[0] != FieldCategory {
		t.Errorf("skipped = %v, want category", res.Skipped)
	}
	if len(res.Warnings) == 0 {
		t.Error("a skipped field with no warning tells the operator nothing")
	}
}

func TestFacebookMetadataNeverBlanksAFieldTheOperatorLeftEmpty(t *testing.T) {
	log := fbServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/9" {
			writeJSONBody(t, w, http.StatusOK, map[string]any{"success": true})
			return
		}
		http.Error(w, "{}", http.StatusNotFound)
	})

	res, err := (&Facebook{}).UpdateLiveVideo(context.Background(), "cid", "user-token", "user:1000", "9",
		Metadata{Title: "Only the title"})
	if err != nil {
		t.Fatalf("UpdateLiveVideo: %v", err)
	}
	post := fbCall(*log, http.MethodPost, "/9")
	if post == nil {
		t.Fatalf("no edit; calls were %+v", *log)
	}
	if strings.Contains(post.Query, "description") {
		t.Errorf("sent an empty description, which would blank a live broadcast: %q", post.Query)
	}
	if len(res.Applied) != 1 || res.Applied[0] != FieldTitle {
		t.Errorf("applied = %v, want only the title", res.Applied)
	}
}

func TestFacebookMetadataSaysSoWhenThereIsNoBroadcastToEdit(t *testing.T) {
	fbServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/me/live_videos" {
			writeJSONBody(t, w, http.StatusOK, map[string]any{"data": []map[string]any{
				{"id": "3", "status": "VOD", "title": "Last week"},
			}})
			return
		}
		http.Error(w, "{}", http.StatusNotFound)
	})

	_, err := (&Facebook{}).PushMetadata(context.Background(), "cid", "user-token", "", Metadata{Title: "x"})
	if err == nil {
		t.Fatal("PushMetadata edited something when only a finished VOD existed")
	}
	if !strings.Contains(err.Error(), "no live or staged broadcast") {
		t.Errorf("error does not explain what to do: %v", err)
	}
}

func TestFacebookMetadataCapsDoNotInventLimits(t *testing.T) {
	caps := (&Facebook{}).MetadataCaps()
	if caps.Accepts(FieldCategory) {
		t.Error("Facebook Live has no category field")
	}
	if !caps.Accepts(FieldTitle) || !caps.Accepts(FieldDescription) {
		t.Errorf("fields = %v, want title and description", caps.Fields)
	}
	// A limit we cannot cite is a limit that rejects input the platform would
	// have accepted. Zero means "no published limit".
	if caps.TitleMax != 0 || caps.DescriptionMax != 0 {
		t.Errorf("caps claim undocumented limits: title %d, description %d", caps.TitleMax, caps.DescriptionMax)
	}
	if caps.Scope == "" {
		t.Error("caps name no scope, so a 403 cannot be turned into advice")
	}
}

// ----------------------------------------------------------- error mapping

func TestFacebookErrorsBecomeAdviceTheOperatorCanAct(t *testing.T) {
	tests := []struct {
		name     string
		status   int
		body     string
		wantAny  []string // every phrase must be present
		wantSame bool     // the error passes through untouched
	}{
		{
			name:    "a missing permission names the permission and the fix",
			status:  http.StatusForbidden,
			body:    `{"error":{"message":"(#200) Requires publish_video permission to manage the object","type":"OAuthException","code":200}}`,
			wantAny: []string{"permission", "publish_video", "reconnect the account", "Requires publish_video"},
		},
		{
			name:    "an unreviewed app is named as App Review, not as a missing permission",
			status:  http.StatusForbidden,
			body:    `{"error":{"message":"(#200) The app must undergo App Review before it can publish live video","type":"OAuthException","code":200}}`,
			wantAny: []string{"App Review", "Advanced Access"},
		},
		{
			name:    "development mode is the same problem under another name",
			status:  http.StatusForbidden,
			body:    `{"error":{"message":"This app is in development mode and can only be used by app roles","code":200}}`,
			wantAny: []string{"App Review"},
		},
		{
			name:    "an expired token says reconnect rather than repeating Meta's wording",
			status:  http.StatusBadRequest,
			body:    `{"error":{"message":"Error validating access token: Session has expired","type":"OAuthException","code":190,"error_subcode":463}}`,
			wantAny: []string{"expire", "Reconnect", "Settings → Platforms"},
		},
		{
			name:    "a de-authorised app is the same 190 and the same cure",
			status:  http.StatusBadRequest,
			body:    `{"error":{"message":"The user has not authorized application","type":"OAuthException","code":190,"error_subcode":458}}`,
			wantAny: []string{"Reconnect"},
		},
		{
			name:    "a user-facing message is preferred over the developer one",
			status:  http.StatusForbidden,
			body:    `{"error":{"message":"(#200) permissions error","code":200,"error_user_msg":"Ada needs to be an admin of this Page."}}`,
			wantAny: []string{"Ada needs to be an admin"},
		},
		{
			name:     "an error we do not recognise is passed through rather than guessed at",
			status:   http.StatusInternalServerError,
			body:     `{"error":{"message":"An unknown error occurred","code":1}}`,
			wantSame: true,
		},
		{
			name:     "a body that is not Meta's envelope is passed through",
			status:   http.StatusBadGateway,
			body:     `<html>502 Bad Gateway</html>`,
			wantSame: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			orig := &statusError{Status: tc.status, URL: "https://graph.example/me/live_videos", Body: tc.body}
			got := fbAdvice(orig, "start a Facebook broadcast", []string{"publish_video"})

			if tc.wantSame {
				if got != error(orig) {
					t.Errorf("error was rewritten on a guess: %v", got)
				}
				return
			}
			if got == error(orig) {
				t.Fatalf("error was passed through unmapped: %v", got)
			}
			for _, want := range tc.wantAny {
				if !strings.Contains(got.Error(), want) {
					t.Errorf("advice is missing %q: %v", want, got)
				}
			}
		})
	}
}

func TestFacebookErrorsNeverCarryTokenMaterial(t *testing.T) {
	// Graph accepts ?access_token=, and using it would put the token into every
	// error string and every log line. The header is what keeps it out.
	log := fbServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"message":"(#200) Requires publish_video permission","code":200}}`, http.StatusForbidden)
	})

	_, err := (&Facebook{}).IngestFor(context.Background(), "cid", "super-secret-token", "", IngestOptions{})
	if err == nil {
		t.Fatal("IngestFor succeeded against a 403")
	}
	if strings.Contains(err.Error(), "super-secret-token") {
		t.Errorf("error leaked the access token: %v", err)
	}
	for _, c := range *log {
		if strings.Contains(c.Query, "super-secret-token") {
			t.Errorf("token travelled in the query string: %s?%s", c.Path, c.Query)
		}
	}
}

// ---------------------------------------------------------------- OAuth flow

func TestFacebookAuthURLAsksForBothTargetsAndCanRerequest(t *testing.T) {
	f := &Facebook{}
	raw := f.AuthURL("cid", "https://example.test/cb", "state-1", "unused-challenge")
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("AuthURL returned an unparseable URL %q: %v", raw, err)
	}
	q := u.Query()

	// Facebook's scope parameter is comma-delimited, unlike everyone else's.
	scope := q.Get("scope")
	for _, want := range f.Scopes() {
		if !strings.Contains(scope, want) {
			t.Errorf("scope %q is missing %q", scope, want)
		}
	}
	if strings.Contains(scope, " ") {
		t.Errorf("scope %q is space-delimited; Facebook wants commas", scope)
	}
	// Without rerequest, a user who declined a permission is bounced straight
	// back with the same partial grant and no way to fix it.
	if q.Get("auth_type") != "rerequest" {
		t.Errorf("auth_type = %q, want rerequest", q.Get("auth_type"))
	}
	if q.Get("state") != "state-1" || q.Get("response_type") != "code" {
		t.Errorf("query = %v", u.RawQuery)
	}
}

func TestFacebookTokensRefreshThroughTheLongLivedExchange(t *testing.T) {
	// Facebook issues no refresh token. The access token is its own refresh
	// credential via fb_exchange_token, so both halves of the Token must be
	// populated or the connection dies at the first expiry.
	tests := []struct {
		name string
		run  func(f *Facebook) (*Token, error)
		want map[string]string
	}{
		{
			name: "the code exchange stores the access token as its own refresh credential",
			run: func(f *Facebook) (*Token, error) {
				return f.Exchange(context.Background(), "cid", "secret", "https://example.test/cb", "the-code", "")
			},
			want: map[string]string{"code": "the-code", "client_id": "cid", "redirect_uri": "https://example.test/cb"},
		},
		{
			name: "a refresh re-exchanges the stored token for a longer-lived one",
			run: func(f *Facebook) (*Token, error) {
				return f.Refresh(context.Background(), "cid", "secret", "old-token")
			},
			want: map[string]string{"grant_type": "fb_exchange_token", "fb_exchange_token": "old-token"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var form map[string]string
			fbServer(t, func(w http.ResponseWriter, r *http.Request) {
				if err := r.ParseForm(); err != nil {
					t.Errorf("parse form: %v", err)
				}
				form = map[string]string{}
				for k := range r.PostForm {
					form[k] = r.PostForm.Get(k)
				}
				writeJSONBody(t, w, http.StatusOK, map[string]any{
					"access_token": "fresh-token", "token_type": "bearer", "expires_in": 5183944,
				})
			})

			tok, err := tc.run(&Facebook{})
			if err != nil {
				t.Fatalf("token call: %v", err)
			}
			for k, want := range tc.want {
				if form[k] != want {
					t.Errorf("form[%s] = %q, want %q", k, form[k], want)
				}
			}
			if tok.RefreshToken != tok.AccessToken || tok.RefreshToken == "" {
				t.Errorf("refresh token = %q, want it to mirror the access token", tok.RefreshToken)
			}
			if tok.ExpiresAt.IsZero() {
				t.Error("expiry was not recorded, so the refresh loop will never renew this token")
			}
		})
	}
}

func TestFacebookPKCEDecisionIsRecorded(t *testing.T) {
	// Pinned deliberately. Meta's manual login flow documents its parameters and
	// says nothing about RFC 7636; sending a code_challenge on a hunch would not
	// weaken a defence, it would break sign-in outright. Flip this only with a
	// documented citation in hand.
	f := &Facebook{}
	if f.PKCE() {
		t.Fatal("Facebook claims PKCE support; Meta does not document it for this flow")
	}
	if strings.Contains(f.AuthURL("cid", "https://example.test/cb", "s", "challenge"), "code_challenge") {
		t.Error("AuthURL leaked a code_challenge for a provider that does not opt in")
	}
}

func TestFacebookIsRegisteredAndGuidedAsASupportedPlatform(t *testing.T) {
	if _, err := Get(db.PlatformFacebook); err != nil {
		t.Fatalf("Get(facebook): %v", err)
	}
	var guide *SetupGuide
	for i, g := range Guides() {
		if g.Platform == db.PlatformFacebook {
			guide = &Guides()[i]
		}
	}
	if guide == nil {
		t.Fatal("Facebook has a provider but no setup guide, so nobody can configure it")
	}
	if !guide.Supported || guide.RedirectPath == "" {
		t.Errorf("guide claims %v/%q", guide.Supported, guide.RedirectPath)
	}
	// App Review is the thing that stops an operator dead after they have built
	// everything else, so it has to be in the part they read first.
	if !strings.Contains(guide.Note, "App Review") {
		t.Errorf("the guide buries the App Review requirement: %q", guide.Note)
	}
	if len(guide.Steps) < 5 {
		t.Errorf("guide has %d steps; it needs the real ones", len(guide.Steps))
	}
}

func TestFacebookResolvesTagWordsIntoContentTagIDs(t *testing.T) {
	log := fbServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/search":
			writeJSONBody(t, w, http.StatusOK, map[string]any{"data": []map[string]any{
				{"id": "6003", "name": "Cooking"},
			}})
		case r.URL.Path == "/9":
			writeJSONBody(t, w, http.StatusOK, map[string]any{"success": true})
		default:
			http.Error(w, "{}", http.StatusNotFound)
		}
	})
	res, err := (&Facebook{}).UpdateLiveVideo(context.Background(), "cid", "user-token", "user:1000", "9",
		Metadata{Tags: []string{"cooking"}})
	if err != nil {
		t.Fatalf("UpdateLiveVideo: %v", err)
	}
	post := fbCall(*log, http.MethodPost, "/9")
	if post == nil {
		t.Fatalf("no edit; calls were %+v", *log)
	}
	if !strings.Contains(post.Query, "content_tags") || !strings.Contains(post.Query, "6003") {
		t.Errorf("edit query %q carries no resolved tag id", post.Query)
	}
	if !slices.Contains(res.Applied, FieldTags) {
		t.Errorf("applied = %v, want FieldTags", res.Applied)
	}
}

func TestATagWordThatMatchesNothingIsNamedInAWarning(t *testing.T) {
	// A tag that vanishes without comment is indistinguishable from one that
	// worked, and the operator has no way to find out which happened.
	fbServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/search":
			writeJSONBody(t, w, http.StatusOK, map[string]any{"data": []map[string]any{}})
		case r.URL.Path == "/9":
			writeJSONBody(t, w, http.StatusOK, map[string]any{"success": true})
		default:
			http.Error(w, "{}", http.StatusNotFound)
		}
	})
	res, err := (&Facebook{}).UpdateLiveVideo(context.Background(), "cid", "user-token", "user:1000", "9",
		Metadata{Title: "t", Tags: []string{"zzzznotathing"}})
	if err != nil {
		t.Fatalf("UpdateLiveVideo: %v", err)
	}
	joined := strings.Join(res.Warnings, " | ")
	if !strings.Contains(joined, "zzzznotathing") {
		t.Errorf("warnings %q do not name the tag that matched nothing", joined)
	}
}

func TestARefusedTagSearchStillAppliesTheTitle(t *testing.T) {
	// The tag lookup is an ads-surface endpoint and may not be reachable with
	// publish_video. A 403 there must never cost the operator a title change
	// seconds before air.
	fbServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/search":
			http.Error(w, `{"error":{"message":"(#10) permission"}}`, http.StatusForbidden)
		case r.URL.Path == "/9":
			writeJSONBody(t, w, http.StatusOK, map[string]any{"success": true})
		default:
			http.Error(w, "{}", http.StatusNotFound)
		}
	})
	res, err := (&Facebook{}).UpdateLiveVideo(context.Background(), "cid", "user-token", "user:1000", "9",
		Metadata{Title: "Tonight", Tags: []string{"cooking"}})
	if err != nil {
		t.Fatalf("a refused tag search failed the whole push: %v", err)
	}
	if !slices.Contains(res.Applied, FieldTitle) {
		t.Errorf("applied = %v, want the title to have landed anyway", res.Applied)
	}
	if !slices.Contains(res.Skipped, FieldTags) {
		t.Errorf("skipped = %v, want FieldTags", res.Skipped)
	}
}

func TestFacebookSendsNoContentTagsWhenTheOperatorTypedNone(t *testing.T) {
	log := fbServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/9" {
			writeJSONBody(t, w, http.StatusOK, map[string]any{"success": true})
			return
		}
		http.Error(w, "{}", http.StatusNotFound)
	})
	_, err := (&Facebook{}).UpdateLiveVideo(context.Background(), "cid", "user-token", "user:1000", "9",
		Metadata{Title: "Only a title"})
	if err != nil {
		t.Fatalf("UpdateLiveVideo: %v", err)
	}
	if fbCall(*log, http.MethodGet, "/search") != nil {
		t.Error("searched for tags nobody typed, on the path that runs seconds before air")
	}
	post := fbCall(*log, http.MethodPost, "/9")
	if post != nil && strings.Contains(post.Query, "content_tags") {
		t.Errorf("edit query %q sends empty content_tags", post.Query)
	}
}

// ------------------------------------------------------------- privacy push

func TestAConfirmedPrivacyChangeIsReportedAsApplied(t *testing.T) {
	log := fbServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/9":
			writeJSONBody(t, w, http.StatusOK, map[string]any{"success": true})
		case r.Method == http.MethodGet && r.URL.Path == "/9":
			writeJSONBody(t, w, http.StatusOK, map[string]any{
				"id": "9", "privacy": map[string]any{"value": "ALL_FRIENDS"},
			})
		default:
			http.Error(w, "{}", http.StatusNotFound)
		}
	})
	res, err := (&Facebook{}).UpdateLiveVideoPrivacy(context.Background(), "cid", "user-token",
		"user:1000", "9", db.FBPrivacyFriends)
	if err != nil {
		t.Fatalf("UpdateLiveVideoPrivacy: %v", err)
	}
	if !slices.Contains(res.Applied, FieldPrivacy) {
		t.Errorf("applied = %v, want FieldPrivacy after Facebook confirmed the change", res.Applied)
	}
	// The create path pins the exact {"value":"..."} envelope and says why:
	// Graph wants an object, and a bare value is a different, malformed
	// request. This path sends the same fbPrivacyParam encoding and had no
	// assertion of its own -- unlike the create path, nothing here inspected
	// the POST it actually sent.
	post := fbCall(*log, http.MethodPost, "/9")
	if post == nil {
		t.Fatalf("no POST to /9; calls were %+v", *log)
	}
	q, err := url.ParseQuery(post.Query)
	if err != nil {
		t.Fatalf("parse query: %v", err)
	}
	if got := q.Get("privacy"); got != `{"value":"ALL_FRIENDS"}` {
		t.Errorf("update POST carries privacy %q, want the {\"value\":\"ALL_FRIENDS\"} object", got)
	}
}

func TestAPrivacyFacebookAcceptedButDidNotApplyIsNotReportedAsApplied(t *testing.T) {
	// THE CASE THIS TASK EXISTS FOR. Graph documents no update surface for
	// LiveVideo, so a 200 means "request accepted", not "field changed". If the
	// read-back still says EVERYONE, the operator must not be told their
	// broadcast is now friends-only.
	fbServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/9":
			writeJSONBody(t, w, http.StatusOK, map[string]any{"success": true})
		case r.Method == http.MethodGet && r.URL.Path == "/9":
			writeJSONBody(t, w, http.StatusOK, map[string]any{
				"id": "9", "privacy": map[string]any{"value": "EVERYONE"},
			})
		default:
			http.Error(w, "{}", http.StatusNotFound)
		}
	})
	res, err := (&Facebook{}).UpdateLiveVideoPrivacy(context.Background(), "cid", "user-token",
		"user:1000", "9", db.FBPrivacyFriends)
	if err != nil {
		t.Fatalf("UpdateLiveVideoPrivacy: %v", err)
	}
	if slices.Contains(res.Applied, FieldPrivacy) {
		t.Fatal("reported a privacy change Facebook did not make; the operator believes " +
			"the broadcast is friends-only and it is public")
	}
	if !slices.Contains(res.Skipped, FieldPrivacy) {
		t.Errorf("skipped = %v, want FieldPrivacy", res.Skipped)
	}
	if !strings.Contains(strings.Join(res.Warnings, " | "), "EVERYONE") {
		t.Errorf("warnings %v do not say what the privacy actually is", res.Warnings)
	}
}

func TestAPrivacyReadBackThatOmitsTheFieldIsNotTreatedAsConfirmation(t *testing.T) {
	// Whether privacy is even readable on a LiveVideo is unverified -- the node
	// reference 404s. Absence must read as "not confirmed", never as agreement.
	fbServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/9":
			writeJSONBody(t, w, http.StatusOK, map[string]any{"success": true})
		case r.Method == http.MethodGet && r.URL.Path == "/9":
			writeJSONBody(t, w, http.StatusOK, map[string]any{"id": "9"})
		default:
			http.Error(w, "{}", http.StatusNotFound)
		}
	})
	res, err := (&Facebook{}).UpdateLiveVideoPrivacy(context.Background(), "cid", "user-token",
		"user:1000", "9", db.FBPrivacyFriends)
	if err != nil {
		t.Fatalf("UpdateLiveVideoPrivacy: %v", err)
	}
	if slices.Contains(res.Applied, FieldPrivacy) {
		t.Fatal("treated a read-back with no privacy field as confirmation")
	}
	if !slices.Contains(res.Skipped, FieldPrivacy) {
		t.Errorf("skipped = %v, want FieldPrivacy", res.Skipped)
	}
}

func TestAnUnreadablePrivacyConfirmationIsSkippedRatherThanTrusted(t *testing.T) {
	// The fourth not-confirmed path, named in the method's own doc comment: "an
	// unreadable response". A GET that fails outright is a different failure
	// from one that succeeds but omits the field (tested above) -- both must
	// land as Skipped, but only this one exercises getErr != nil rather than
	// confirm.Privacy == nil. Until this test existed, that branch could be
	// rewritten to report Applied unconditionally with the suite still green.
	fbServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/9":
			writeJSONBody(t, w, http.StatusOK, map[string]any{"success": true})
		case r.Method == http.MethodGet && r.URL.Path == "/9":
			http.Error(w, `{"error":{"message":"(#1) internal error"}}`, http.StatusInternalServerError)
		default:
			http.Error(w, "{}", http.StatusNotFound)
		}
	})
	res, err := (&Facebook{}).UpdateLiveVideoPrivacy(context.Background(), "cid", "user-token",
		"user:1000", "9", db.FBPrivacyFriends)
	if err != nil {
		t.Fatalf("UpdateLiveVideoPrivacy: %v", err)
	}
	if slices.Contains(res.Applied, FieldPrivacy) {
		t.Fatal("reported a privacy change confirmed by a read-back that could not be read at all")
	}
	if !slices.Contains(res.Skipped, FieldPrivacy) {
		t.Errorf("skipped = %v, want FieldPrivacy", res.Skipped)
	}
	if !strings.Contains(strings.Join(res.Warnings, " | "), "could not be confirmed") {
		t.Errorf("warnings %v do not say the read-back itself failed", res.Warnings)
	}
}

func TestARefusedPrivacyPushIsSkippedRatherThanAnError(t *testing.T) {
	// The stored value was already applied when the broadcast was created, so a
	// refusal here costs a convenience, not the setting.
	fbServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"message":"(#100) unsupported"}}`, http.StatusBadRequest)
	})
	res, err := (&Facebook{}).UpdateLiveVideoPrivacy(context.Background(), "cid", "user-token",
		"user:1000", "9", db.FBPrivacyFriends)
	if err != nil {
		t.Fatalf("a refused privacy push became an error: %v", err)
	}
	if !slices.Contains(res.Skipped, FieldPrivacy) {
		t.Errorf("skipped = %v, want FieldPrivacy", res.Skipped)
	}
}

func TestUpdateLiveVideoPrivacyRefusesAnUnknownValueRatherThanSendingIt(t *testing.T) {
	// Entry guard, not a Graph round trip: an unknown value must never reach
	// the wire at all. fbServer's 404-everything handler is the tripwire --
	// if this guard were dropped, the request would hit it and the test would
	// fail on the wrong assertion instead of silently passing.
	log := fbServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "{}", http.StatusNotFound)
	})
	_, err := (&Facebook{}).UpdateLiveVideoPrivacy(context.Background(), "cid", "user-token",
		"user:1000", "9", db.FacebookPrivacy("NOT_A_REAL_VALUE"))
	if err == nil {
		t.Fatal("UpdateLiveVideoPrivacy accepted a privacy value Facebook does not define")
	}
	if len(*log) != 0 {
		t.Errorf("an invalid value still reached Facebook: %+v", *log)
	}
}

func TestUpdateLiveVideoPrivacyRefusesAnEmptyLiveVideoID(t *testing.T) {
	// Same shape: no broadcast id means there is nothing to address, and the
	// method must say so rather than POSTing to "/".
	log := fbServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "{}", http.StatusNotFound)
	})
	_, err := (&Facebook{}).UpdateLiveVideoPrivacy(context.Background(), "cid", "user-token",
		"user:1000", "", db.FBPrivacySelf)
	if err == nil {
		t.Fatal("UpdateLiveVideoPrivacy accepted an empty live video id")
	}
	if len(*log) != 0 {
		t.Errorf("a request with no live video id still reached Facebook: %+v", *log)
	}
}

func TestAPagePrivacyPushMakesNoRequestAtAll(t *testing.T) {
	// A Page broadcast is public by nature. Sending a personal audience value is
	// a request Facebook has no meaning for, and the operator is told why.
	log := fbServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSONBody(t, w, http.StatusOK, map[string]any{"success": true})
	})
	res, err := (&Facebook{}).UpdateLiveVideoPrivacy(context.Background(), "cid", "page-token",
		"page:5000", "9", db.FBPrivacySelf)
	if err != nil {
		t.Fatalf("UpdateLiveVideoPrivacy: %v", err)
	}
	if fbCall(*log, http.MethodPost, "/9") != nil {
		t.Error("sent a personal privacy value to a Page broadcast")
	}
	if !slices.Contains(res.Skipped, FieldPrivacy) {
		t.Errorf("skipped = %v, want FieldPrivacy", res.Skipped)
	}
}

// A scheduled broadcast is a DIFFERENT status, not LIVE_NOW plus a field.
// Asserted on the request Facebook actually receives: a test that only checked
// IngestFor returned no error would pass with the status unchanged.
func TestAScheduledBroadcastIsCreatedUnpublishedWithItsStartTime(t *testing.T) {
	log := fbServer(t, graphStub(t, fbLiveResponse("777")))

	at := time.Unix(1800000000, 0)
	if _, err := (&Facebook{}).IngestFor(context.Background(), "cid", "user-token", "",
		IngestOptions{ScheduledFor: at}); err != nil {
		t.Fatalf("IngestFor: %v", err)
	}

	post := fbCall(*log, http.MethodPost, "/me/live_videos")
	if post == nil {
		t.Fatalf("no create call; calls were %+v", *log)
	}
	q, err := url.ParseQuery(post.Query)
	if err != nil {
		t.Fatalf("parse query %q: %v", post.Query, err)
	}
	if s := q.Get("status"); s != "SCHEDULED_UNPUBLISHED" {
		t.Errorf("status = %q, want SCHEDULED_UNPUBLISHED", s)
	}
	if ep := q.Get("event_params"); ep != "1800000000" {
		t.Errorf("event_params = %q, want the unix start time 1800000000", ep)
	}
}

// The zero value is the whole existing world. Every current caller passes
// IngestOptions{}, and turning those into scheduled creates would produce
// broadcasts that never go live.
func TestAnUnscheduledBroadcastIsStillLiveNowAndSendsNoEventParams(t *testing.T) {
	log := fbServer(t, graphStub(t, fbLiveResponse("777")))

	if _, err := (&Facebook{}).IngestFor(context.Background(), "cid", "user-token", "",
		IngestOptions{}); err != nil {
		t.Fatalf("IngestFor: %v", err)
	}

	post := fbCall(*log, http.MethodPost, "/me/live_videos")
	if post == nil {
		t.Fatalf("no create call; calls were %+v", *log)
	}
	q, err := url.ParseQuery(post.Query)
	if err != nil {
		t.Fatalf("parse query %q: %v", post.Query, err)
	}
	if s := q.Get("status"); s != "LIVE_NOW" {
		t.Errorf("status = %q, want LIVE_NOW", s)
	}
	// ABSENT, not empty. Facebook reads a present-but-empty parameter as a
	// value, so this asserts the key does not exist at all -- the same
	// "empty means leave alone" rule the rest of this file runs on.
	if _, ok := q["event_params"]; ok {
		t.Error("event_params was sent for an unscheduled broadcast")
	}
}
