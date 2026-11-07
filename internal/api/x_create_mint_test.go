package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/oauth"
)

// AN X DESTINATION LINKED TO AN ACCOUNT MAY BE CREATED WITH NO URL AND NO KEY.
//
// With X Livestream on, polyemesis creates the stream source itself, so the
// create fills both from the account. With it off the paste is the only way in,
// and an empty one stays refused. Validate still demands an RTMP URL, so the
// fill has to happen before the row is stored.

// xSourceFake answers the three calls oauth.X.Ingest makes for an account with
// no source yet, and counts them.
func xSourceFake(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/2/region":
			_, _ = io.WriteString(w, `{"region":"us-east-1"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/2/sources":
			_, _ = io.WriteString(w, `{"data":[],"meta":{"result_count":0}}`)
		case r.Method == http.MethodPost && r.URL.Path == "/2/sources":
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"data":{"id":"9123456789012345678","name":"polyemesis","region":"us-east-1",`+
				`"rtmp_url":"rtmp://ingest.example.invalid/live","rtmps_url":"rtmps://ingest.example.invalid:443/live",`+
				`"stream_key":"minted1","is_stream_active":false,"created_at_ms":"1772029802844"}}`)
		default:
			http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

func xCreateBody(accountID *int64, url, key string) map[string]any {
	b := map[string]any{"name": "X", "kind": "rtmp", "platform": "x", "url": url, "streamKey": key}
	if accountID != nil {
		b["accountId"] = *accountID
	}
	return b
}

// MUTATION, internal/api/handlers.go, mintXSourceOnCreate: return false at the
// top. Observed: FAIL -- the create answers 400 "an RTMP URL is required".
func TestALinkedXDestinationWithNoUrlOrKeyIsFilledFromTheAccountWhenLivestreamIsOn(t *testing.T) {
	srv, _ := xSourceFake(t)
	s, h, store, sign := engineServer(t, defaultTools(), Options{
		Providers: oauth.NewSet().With(db.PlatformX, oauth.NewX(oauth.WithBaseURL(srv.URL)).WithLivestreamAccess()),
	})
	acct := connectAccount(t, store, s.box, db.PlatformX, "rainman")
	if err := store.PutPlatformCreds(s.box, db.PlatformX, "cid", "placeholder-secret"); err != nil {
		t.Fatalf("creds: %v", err)
	}

	var out struct {
		Destination db.Destination `json:"destination"`
	}
	decodeInto(t, send(t, h, sign, http.MethodPost, "/api/v1/destinations", xCreateBody(&acct, "", ""), http.StatusCreated), &out)

	got, err := store.GetDestination(out.Destination.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.URL != "rtmps://ingest.example.invalid:443/live" || got.StreamKey != "minted1" {
		t.Errorf("stored URL %q key %q, want the minted source's", got.URL, got.StreamKey)
	}
	if got.AccountID == nil || *got.AccountID != acct {
		t.Errorf("stored account = %v, want %d", got.AccountID, acct)
	}
}

// MUTATION, same function: delete the gate check. Observed: FAIL -- the fake is
// asked, or the refusal loses the gate's name.
func TestALinkedXDestinationWithNoUrlOrKeyIsRefusedWhenLivestreamIsOff(t *testing.T) {
	srv, hits := xSourceFake(t)
	s, h, store, sign := engineServer(t, defaultTools(), Options{
		Providers: oauth.NewSet().With(db.PlatformX, oauth.NewX(oauth.WithBaseURL(srv.URL))),
	})
	acct := connectAccount(t, store, s.box, db.PlatformX, "rainman")
	if err := store.PutPlatformCreds(s.box, db.PlatformX, "cid", "placeholder-secret"); err != nil {
		t.Fatalf("creds: %v", err)
	}

	raw := send(t, h, sign, http.MethodPost, "/api/v1/destinations", xCreateBody(&acct, "", ""), http.StatusBadRequest)

	if !strings.Contains(string(raw), "POLYEMESIS_X_LIVESTREAM") {
		t.Errorf("the refusal does not name the switch that opens the gate:\n%s", raw)
	}
	if n := hits.Load(); n != 0 {
		t.Errorf("X was asked %d times with the gate closed", n)
	}
	if all, _ := store.ListDestinations(); len(all) != 0 {
		t.Errorf("a refused create left %d destinations behind", len(all))
	}
}

// With no account there is nothing to mint from, gate open or not.
func TestAnUnlinkedXDestinationWithNoUrlOrKeyIsStillRefused(t *testing.T) {
	srv, hits := xSourceFake(t)
	_, h, _, sign := engineServer(t, defaultTools(), Options{
		Providers: oauth.NewSet().With(db.PlatformX, oauth.NewX(oauth.WithBaseURL(srv.URL)).WithLivestreamAccess()),
	})

	raw := send(t, h, sign, http.MethodPost, "/api/v1/destinations", xCreateBody(nil, "", ""), http.StatusBadRequest)

	if !strings.Contains(string(raw), "RTMP URL is required") {
		t.Errorf("the refusal is not the missing-URL one:\n%s", raw)
	}
	if n := hits.Load(); n != 0 {
		t.Errorf("X was asked %d times for a destination with no account", n)
	}
}

// A pasted URL and key are the operator's, and are stored as typed: the mint
// must not run over them.
func TestAPastedXUrlAndKeyAreNotReplacedByAMintedSource(t *testing.T) {
	srv, hits := xSourceFake(t)
	s, h, store, sign := engineServer(t, defaultTools(), Options{
		Providers: oauth.NewSet().With(db.PlatformX, oauth.NewX(oauth.WithBaseURL(srv.URL)).WithLivestreamAccess()),
	})
	acct := connectAccount(t, store, s.box, db.PlatformX, "rainman")

	var out struct {
		Destination db.Destination `json:"destination"`
	}
	decodeInto(t, send(t, h, sign, http.MethodPost, "/api/v1/destinations",
		xCreateBody(&acct, "rtmps://pasted.example.invalid:443/live", "pasted9"), http.StatusCreated), &out)

	if out.Destination.URL != "rtmps://pasted.example.invalid:443/live" || out.Destination.StreamKey != "pasted9" {
		t.Errorf("stored URL %q key %q, want the pasted pair", out.Destination.URL, out.Destination.StreamKey)
	}
	if n := hits.Load(); n != 0 {
		t.Errorf("X was asked %d times although both fields were typed", n)
	}
}

// The mint's failures each answer the create and store nothing: a half-made X
// destination with no URL would fail Validate on every later edit.
func TestALinkedXDestinationIsNotStoredWhenTheMintCannotRun(t *testing.T) {
	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"title":"Service Unavailable"}`, http.StatusServiceUnavailable)
	}))
	t.Cleanup(failing.Close)

	cases := []struct {
		name     string
		baseURL  string
		creds    bool
		platform db.Platform
		account  func(acct int64) int64
		status   int
		want     string
	}{
		{"no developer credentials", "", false, db.PlatformX, nil, http.StatusPreconditionFailed, "developer credentials are missing"},
		{"the linked account is not X", "", true, db.PlatformTwitch, nil, http.StatusBadRequest, "linked account is twitch"},
		{"the linked account does not exist", "", true, db.PlatformX, func(acct int64) int64 { return acct + 99 }, http.StatusBadGateway, ""},
		{"X refuses the source", failing.URL, true, db.PlatformX, nil, http.StatusBadGateway, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, _ := xSourceFake(t)
			base := srv.URL
			if tc.baseURL != "" {
				base = tc.baseURL
			}
			s, h, store, sign := engineServer(t, defaultTools(), Options{
				Providers: oauth.NewSet().With(db.PlatformX, oauth.NewX(oauth.WithBaseURL(base)).WithLivestreamAccess()),
			})
			acct := connectAccount(t, store, s.box, tc.platform, "rainman")
			if tc.account != nil {
				acct = tc.account(acct)
			}
			if tc.creds {
				if err := store.PutPlatformCreds(s.box, db.PlatformX, "cid", "placeholder-secret"); err != nil {
					t.Fatalf("creds: %v", err)
				}
			}

			raw := send(t, h, sign, http.MethodPost, "/api/v1/destinations", xCreateBody(&acct, "", ""), tc.status)

			if tc.want != "" && !strings.Contains(string(raw), tc.want) {
				t.Errorf("refusal %q does not say %q", raw, tc.want)
			}
			if all, _ := store.ListDestinations(); len(all) != 0 {
				t.Errorf("a refused create left %d destinations behind", len(all))
			}
		})
	}
}
