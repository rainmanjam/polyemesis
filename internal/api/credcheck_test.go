package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/config"
	"github.com/rainmanjam/polyemesis/internal/db"
)

// The credential check on save, from the API's side.
//
// The property under test throughout is that CHECKING AND STORING ARE
// SEPARATE. An operator is usually part-way through a platform console when
// they paste these, and a save that refuses a credential they are three clicks
// from making valid is obstructive rather than protective.

type credCheckResponse struct {
	Platform  string `json:"platform"`
	HasSecret bool   `json:"hasSecret"`
	Check     struct {
		State  string `json:"state"`
		Method string `json:"method"`
		Detail string `json:"detail"`
	} `json:"check"`
}

func putCreds(t *testing.T, h http.Handler, sign func(*http.Request), platform, id, secret string) (int, credCheckResponse) {
	t.Helper()
	r := jsonRequest(t, http.MethodPut, "/api/v1/platforms/credentials/"+platform,
		map[string]string{"clientId": id, "clientSecret": secret})
	sign(r)
	w := do(t, h, r)

	var got credCheckResponse
	if w.Body.Len() > 0 {
		_ = json.Unmarshal(w.Body.Bytes(), &got)
	}
	return w.Code, got
}

// A rejected credential must STILL BE STORED. See the note above: refusing the
// save is the obstructive half of a feature whose useful half is the verdict.
func TestSaveStoresEvenWhenTheCheckRejects(t *testing.T) {
	srv, h, store := testServer(t, config.Config{})
	sign := login(t, h)

	code, got := putCreds(t, h, sign, "youtube", "nope", "also-nope")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200: a failed check must not block saving", code)
	}
	if got.Check.State != "rejected" {
		t.Errorf("state = %q, want rejected: a Google client ID must end in "+
			".apps.googleusercontent.com", got.Check.State)
	}
	if got.Check.Detail == "" {
		t.Error("no detail; the operator needs to know what to fix")
	}

	// The half that matters, and the reason this test is not just about the
	// verdict: it is on disk regardless of what the verdict said.
	creds, err := store.GetPlatformCreds(srv.box, "youtube")
	if err != nil {
		t.Fatalf("the rejected credential was not stored: %v", err)
	}
	if creds.ClientID != "nope" {
		t.Errorf("stored clientId = %q, want the value that was rejected", creds.ClientID)
	}
}

// YouTube cannot be asked, so the honest answer is "unverified" and the method
// is "format". Reporting a format check as verified would be a lie the operator
// would only discover during a live broadcast.
func TestSaveReportsUnverifiableHonestly(t *testing.T) {
	_, h, _ := testServer(t, config.Config{})
	sign := login(t, h)

	code, got := putCreds(t, h, sign, "youtube",
		"1234.apps.googleusercontent.com", "some-secret")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if got.Check.State != "unverified" {
		t.Fatalf("state = %q, want unverified: Google cannot be asked, and "+
			"reporting a format check as verified would be a lie", got.Check.State)
	}
	if got.Check.Method != "format" {
		t.Errorf("method = %q, want format", got.Check.Method)
	}
}

// An operator who fixes something in the console must be able to retest without
// pasting the secret again -- which they often cannot, since most consoles show
// a secret exactly once.
//
// Asserts JSON, NOT "did not 404". Unknown paths fall through to the SPA
// handler, which answers 200 with index.html, so a not-404 assertion here
// passes with no route registered at all -- it did exactly that on the first
// run of this file, and the giveaway was a sibling test failing to parse the
// body with "invalid character '<'".
func TestCheckRouteIsAJSONEndpoint(t *testing.T) {
	_, h, _ := testServer(t, config.Config{})
	sign := login(t, h)

	r := jsonRequest(t, http.MethodPost, "/api/v1/platforms/credentials/youtube/check", nil)
	sign(r)
	w := do(t, h, r)

	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("body is not JSON (%v); the SPA fallback answered instead of a "+
			"re-check route: %.60s", err, w.Body.String())
	}
	// Nothing is stored for youtube in this test, so the handler must say so
	// rather than invent a verdict. 404 with a JSON error is the proof that the
	// route reached the handler AND that the handler consulted the store --
	// neither of which the SPA fallback's 200-and-index.html could produce.
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 when no credential is stored yet: %v", w.Code, got)
	}
	if _, ok := got["error"]; !ok {
		t.Errorf("no error field explaining the 404: %v", got)
	}
}

// The re-check reads what is stored rather than taking a body, so the secret
// never has to leave the operator's console twice.
//
// The verdict has to VARY WITH THE STORED VALUE, which is the half this used to
// miss. It seeded one valid-format client ID and asserted unverified/format --
// but YouTube's check is format-only, so replacing `creds.ClientID` in
// handleCheckCreds with any hard-coded valid-format Google id produced the same
// verdict, and the stored secret was never consulted at all. The table below
// makes each field load-bearing: a client ID the format check rejects, and a
// stored secret that is empty.
//
// Mutations, each one line in oauth_handlers.go's handleCheckCreds:
//   - `creds.ClientID` -> `"1234.apps.googleusercontent.com"` -- observed FAIL
//     on the "a stored client ID that fails the format check" case.
//   - `creds.ClientSecret` -> `"some-secret"` -- observed FAIL on the "a stored
//     secret that went missing" case.
func TestRecheckUsesTheStoredCredential(t *testing.T) {
	for _, tc := range []struct {
		name         string
		clientID     string
		clientSecret string
		wantState    string
		wantDetail   string
	}{
		{
			name:     "a stored client ID the platform would accept",
			clientID: "1234.apps.googleusercontent.com", clientSecret: "some-secret",
			wantState: "unverified",
		},
		{
			// The operator pasted the project name instead of the client ID.
			// Re-checking has to tell them so, from storage alone.
			name:     "a stored client ID that fails the format check",
			clientID: "my-streaming-project", clientSecret: "some-secret",
			wantState: "rejected", wantDetail: "apps.googleusercontent.com",
		},
		{
			// Written straight to the store: the PUT route refuses a blank
			// secret, and the point here is what the re-check makes of a row
			// that holds one anyway -- a paste that picked up only the
			// surrounding whitespace. A single space rather than "" because the
			// column is NOT NULL, and this is the shortest value that still
			// trims to nothing.
			name:     "a stored secret that is blank",
			clientID: "1234.apps.googleusercontent.com", clientSecret: " ",
			wantState: "rejected", wantDetail: "client secret",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, h, store := testServer(t, config.Config{})
			sign := login(t, h)
			if err := store.PutPlatformCreds(srv.box, db.PlatformYouTube,
				tc.clientID, tc.clientSecret); err != nil {
				t.Fatalf("seed: %v", err)
			}

			r := jsonRequest(t, http.MethodPost, "/api/v1/platforms/credentials/youtube/check", nil)
			sign(r)
			w := do(t, h, r)
			if w.Code != http.StatusOK {
				t.Fatalf("re-check status = %d, want 200: body %s", w.Code, w.Body.String())
			}

			var got struct {
				State  string `json:"state"`
				Method string `json:"method"`
				Detail string `json:"detail"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if got.State != tc.wantState {
				t.Errorf("state = %q, want %q: the verdict has to come from the "+
					"stored credential, not from a constant. detail=%q",
					got.State, tc.wantState, got.Detail)
			}
			if got.Method != "format" {
				t.Errorf("method = %q, want format: Google cannot be asked", got.Method)
			}
			if tc.wantDetail != "" && !strings.Contains(got.Detail, tc.wantDetail) {
				t.Errorf("detail = %q, want it to mention %q so the operator knows "+
					"what to fix", got.Detail, tc.wantDetail)
			}
		})
	}
}

// The re-check is state-changing enough to sit behind CSRF: it spends an
// outbound request to a third party on whoever asks.
func TestRecheckRequiresCSRF(t *testing.T) {
	_, h, _ := testServer(t, config.Config{})
	_ = login(t, h) // establishes the session, but the request goes unsigned

	r := jsonRequest(t, http.MethodPost, "/api/v1/platforms/credentials/youtube/check", nil)
	if w := do(t, h, r); w.Code == http.StatusOK {
		t.Fatal("an unsigned POST reached the re-check route; it makes an " +
			"outbound call and must sit behind requireCSRF")
	}
}

// An unknown platform is rejected before anything is stored or dialled.
func TestCheckRejectsAnUnknownPlatform(t *testing.T) {
	_, h, _ := testServer(t, config.Config{})
	sign := login(t, h)

	r := jsonRequest(t, http.MethodPost, "/api/v1/platforms/credentials/nonesuch/check", nil)
	sign(r)
	if w := do(t, h, r); w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for an unknown platform", w.Code)
	}
}
