package eventsub

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/oauth"
)

// The scope drift guard: every scope the catalogue relies on must be one the
// Twitch provider actually requests, or the kind would report "reconnect"
// forever with nothing the operator could do about it.
func TestEveryTypeIsReachableWithTheScopesTheProviderRequests(t *testing.T) {
	granted := (&oauth.Twitch{}).Scopes()
	for _, s := range Catalogue() {
		if !s.Holds(granted) {
			t.Errorf("%s needs one of %v, none of which the Twitch provider requests", s.Type, s.Scopes)
		}
	}
	if (&oauth.Twitch{}).Platform() != db.PlatformTwitch {
		t.Fatal("wrong provider")
	}
}

func TestCatalogueIsWellFormed(t *testing.T) {
	seen := map[string]bool{}
	for _, s := range Catalogue() {
		key := s.Type + "/" + s.Direction
		if seen[key] {
			t.Errorf("%s listed twice", key)
		}
		seen[key] = true
		if s.Type == "" || s.Version == "" || s.Kind == "" {
			t.Errorf("incomplete spec %+v", s)
		}
		c := s.Condition("42")
		if len(c) == 0 {
			t.Errorf("%s has no condition", s.Type)
		}
		for k, v := range c {
			if v != "42" {
				t.Errorf("%s condition %s = %q", s.Type, k, v)
			}
		}
	}
	// A raid pair names exactly one of from/to in each half, never both
	// (the reference: passing both fails the request).
	for _, s := range Catalogue() {
		if s.Type != "channel.raid" {
			continue
		}
		c := s.Condition("42")
		if len(c) != 1 {
			t.Errorf("raid %s condition = %v", s.Direction, c)
		}
		if _, ok := c["to_broadcaster_user_id"]; ok != (s.Direction == "in") {
			t.Errorf("raid %s condition = %v", s.Direction, c)
		}
	}
}

func TestSpecHolds(t *testing.T) {
	poll := Spec{Scopes: []string{"channel:read:polls", "channel:manage:polls"}}
	if !poll.Holds([]string{"channel:manage:polls"}) || poll.Holds([]string{"chat:read"}) {
		t.Error("either scope should allow the type, and neither should not")
	}
	if !(Spec{}).Holds(nil) {
		t.Error("a type with no scope is always allowed")
	}
	if got := ScopeList("a b,c  d"); strings.Join(got, "|") != "a|b|c|d" {
		t.Errorf("ScopeList = %v", got)
	}
}

func TestParseMessage(t *testing.T) {
	m, err := ParseMessage([]byte(`{"metadata":{"message_id":"84c1e79a","message_type":"session_reconnect","message_timestamp":"2022-11-18T09:10:11.634234626Z"},` +
		`"payload":{"session":{"id":"AQoQexAWVYKSTIu4ec_2VAxyuhAB","status":"reconnecting","keepalive_timeout_seconds":null,"reconnect_url":"wss://eventsub.wss.twitch.tv?...","connected_at":"2022-11-16T10:11:12.634234626Z"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	s, err := m.Session()
	if err != nil || s.ReconnectURL == nil || s.KeepaliveTimeoutSeconds != nil || m.Type() != TypeReconnect {
		t.Errorf("session = %+v err=%v", s, err)
	}
	if m.Metadata.Time().IsZero() {
		t.Error("timestamp did not parse")
	}
	for _, bad := range []string{``, `nope`, `[]`, `{}`, `{"metadata":{"message_type":"x"}}`} {
		if _, err := ParseMessage([]byte(bad)); err == nil {
			t.Errorf("%q parsed", bad)
		}
	}
	// The error never echoes the frame.
	_, err = ParseMessage([]byte(`VIEWER-TEXT`))
	if err == nil || strings.Contains(err.Error(), "VIEWER-TEXT") {
		t.Errorf("err = %v", err)
	}
}

func helixAt(t *testing.T, h http.HandlerFunc) *API {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return &API{Base: srv.URL, ClientID: "cid", Token: func(context.Context) (string, error) { return "tok", nil }}
}

func TestBroadcasterType(t *testing.T) {
	a := helixAt(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("id") != "7" || r.Header.Get("Client-Id") != "cid" {
			w.WriteHeader(400)
			return
		}
		w.Write([]byte(`{"data":[{"id":"7","broadcaster_type":"partner"}]}`))
	})
	got, err := a.BroadcasterType(context.Background(), "7")
	if err != nil || got != TierPartner {
		t.Errorf("got %q err=%v", got, err)
	}
}

func TestAPIErrorCarriesTheStatusAndNeverTheBody(t *testing.T) {
	a := helixAt(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Ratelimit-Reset", "1900000000")
		w.WriteHeader(429)
		w.Write([]byte(`{"message":"VIEWER-TEXT"}`))
	})
	_, err := a.Subscribe(context.Background(), "s", Catalogue()[0], "7")
	if !IsStatus(err, 429) || strings.Contains(err.Error(), "VIEWER-TEXT") {
		t.Fatalf("err = %v", err)
	}
	var ae *APIError
	if !asAPI(err, &ae) || ae.RetryAt.Unix() != 1900000000 {
		t.Errorf("RetryAt = %v", ae)
	}
}

func asAPI(err error, out **APIError) bool {
	ae, ok := err.(*APIError)
	*out = ae
	return ok
}

func TestA401IsRefreshedOnceThenGivenUp(t *testing.T) {
	calls := 0
	a := helixAt(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(401)
	})
	refreshes := 0
	a.Refresh = func(context.Context, string) (string, error) { refreshes++; return "tok2", nil }
	_, err := a.BroadcasterType(context.Background(), "7")
	if !IsStatus(err, 401) || calls != 2 || refreshes != 1 {
		t.Errorf("err=%v calls=%d refreshes=%d", err, calls, refreshes)
	}
}
