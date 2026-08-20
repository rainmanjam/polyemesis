package oauth

// postFormJSON, the transport half of device flow.
//
// It exists because the device authorization endpoint answers with
// device_code/user_code/verification_uri and no access_token, so postForm
// rejects the body before a caller ever sees it. The three properties asserted
// here are the ones a caller depends on and cannot recover for itself:
//
//   - a refusal arrives as *tokenStatusError, because "still waiting for the
//     operator" is an HTTP 400 and classifyTwitchDeviceError has to tell it
//     apart from every other 400 by comparing an int and reading a body;
//   - a 2xx that is not the JSON we asked for is an error rather than a
//     zero-valued struct, since a zero DeviceAuth is a blank code on screen;
//   - a caller that wants no body decoded gets no decode error.
//
// Driven through postFormJSON directly rather than through Twitch, because
// these are the helper's guarantees and a provider-level test would only prove
// them for the one provider that happens to use it today.

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// jsonEndpoint serves one canned answer and records the form it was posted.
func jsonEndpoint(t *testing.T, status int, body string) (string, *url.Values) {
	t.Helper()
	var got url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("endpoint could not parse the request form: %v", err)
		}
		got = r.PostForm
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv.URL, &got
}

// TestPostFormJSONHandsARefusalBackInAFormAClassifierCanRead is the reason
// postFormJSON returns a typed error instead of a formatted string.
//
// Twitch answers a pending authorization with HTTP 400 and its own envelope.
// The only thing separating that from "invalid client" -- also a 400 -- is the
// body, and the only thing separating both from a 500 that happens to contain
// the same phrase is the status. A caller handed `fmt.Errorf("...400: %s")`
// would have to parse the number back out of a sentence to make either
// distinction.
func TestPostFormJSONHandsARefusalBackInAFormAClassifierCanRead(t *testing.T) {
	endpoint, form := jsonEndpoint(t, http.StatusBadRequest,
		`{"status":400,"message":"authorization_pending"}`)

	err := postFormJSON(context.Background(), endpoint, url.Values{"client_id": {"cid"}}, &struct{}{})
	if err == nil {
		t.Fatal("postFormJSON accepted a 400 as a device authorization")
	}
	var se *tokenStatusError
	if !errors.As(err, &se) {
		t.Fatalf("postFormJSON returned %v, which no caller can classify by status", err)
	}
	if se.code != http.StatusBadRequest {
		t.Errorf("the error carries status %d, want the 400 the endpoint answered with", se.code)
	}
	if !strings.Contains(se.body, "authorization_pending") {
		t.Errorf("the error carries body %q, and the token that decides whether the caller "+
			"keeps polling is only in the body", se.body)
	}
	if got := form.Get("client_id"); got != "cid" {
		t.Errorf("the endpoint received client_id=%q, want the form the caller passed", got)
	}
}

// TestPostFormJSONRefusesA200ThatIsNotTheAnswerItAskedFor. An authorization
// server behind a proxy answers maintenance pages with a 200 more often than
// anyone would like, and decoding one into a DeviceAuth silently yields empty
// strings -- which reach the operator as a dialog with a blank code and a blank
// address, and reach a support ticket as "it just doesn't work".
func TestPostFormJSONRefusesA200ThatIsNotTheAnswerItAskedFor(t *testing.T) {
	endpoint, _ := jsonEndpoint(t, http.StatusOK, `<html><body>maintenance</body></html>`)

	var out struct {
		DeviceCode string `json:"device_code"`
	}
	err := postFormJSON(context.Background(), endpoint, url.Values{}, &out)
	if err == nil {
		t.Fatalf("postFormJSON decoded an HTML page into %#v and called it a success", out)
	}
	// Not a refusal: the server said 200. A caller that reported this as "the
	// platform rejected your credentials" would send the operator to the
	// Settings page over a proxy's error page.
	var se *tokenStatusError
	if errors.As(err, &se) {
		t.Errorf("an undecodable 200 was reported as status %d, which it was not", se.code)
	}
	if out.DeviceCode != "" {
		t.Errorf("device code %q survived a body that was never JSON", out.DeviceCode)
	}
}

// TestPostFormJSONAcceptsAResponseTheCallerDoesNotWantToRead covers the caller
// that posts for the effect rather than the answer. Passing no destination has
// to mean "do not decode", not "decode into nothing and fail", or the only way
// to ignore a body would be to invent a struct to throw away.
func TestPostFormJSONAcceptsAResponseTheCallerDoesNotWantToRead(t *testing.T) {
	endpoint, _ := jsonEndpoint(t, http.StatusOK, `this was never going to be JSON`)

	if err := postFormJSON(context.Background(), endpoint, url.Values{}, nil); err != nil {
		t.Errorf("postFormJSON failed on a body it was asked not to decode: %v", err)
	}
}

// TestPostFormJSONSurfacesAnEndpointItCannotBuildARequestFor. The endpoint is
// assembled from the base URL WithBaseURL supplied, so a garbled one is
// reachable from a caller's mistake rather than from a platform's behaviour. It
// has to arrive as an error the caller can print, not as a panic in a request
// handler.
func TestPostFormJSONSurfacesAnEndpointItCannotBuildARequestFor(t *testing.T) {
	err := postFormJSON(context.Background(), "http://bro\nken.invalid/oauth2/device",
		url.Values{}, nil)
	if err == nil {
		t.Fatal("postFormJSON accepted an endpoint that cannot be turned into a request")
	}
}
