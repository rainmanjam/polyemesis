package api

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/config"
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
func TestRecheckUsesTheStoredCredential(t *testing.T) {
	_, h, _ := testServer(t, config.Config{})
	sign := login(t, h)

	if code, _ := putCreds(t, h, sign, "youtube",
		"1234.apps.googleusercontent.com", "some-secret"); code != http.StatusOK {
		t.Fatalf("seed save failed with %d", code)
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
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// Same verdict as the save produced, reached from storage alone.
	if got.State != "unverified" || got.Method != "format" {
		t.Errorf("state=%q method=%q, want unverified/format from the stored value",
			got.State, got.Method)
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
