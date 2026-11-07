package oauth

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

// The bodies below are what X answered on a live probe (2026-11-07) with a fake user token and fake credentials.
const (
	// X parses a garbage user bearer as an app-only one, and answers 403 rather than 401.
	xUnsupportedAuthBody = `{"title":"Unsupported Authentication","detail":"Authenticating with OAuth 2.0 Application-Only is forbidden for this endpoint.  Supported authentication types are [OAuth 1.0a User Context, OAuth 2.0 User Context].","type":"https://api.twitter.com/2/problems/unsupported-authentication","status":403}`
	xInvalidClientBody   = `{"error":"invalid_client","error_description":"Value passed for the client id was invalid."}`
)

// A refused token is a reconnect, not a paid-tier refusal, and its text names neither a URL nor the raw body.
func TestXCheckEntitlementTreatsARefusedTokenAsReconnectNotATier(t *testing.T) {
	x, _ := xStubLive(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, xUnsupportedAuthBody)
	})
	err := x.CheckEntitlement(context.Background(), "client", "tok")
	if err == nil {
		t.Fatal("CheckEntitlement succeeded on a refused token")
	}
	if errors.Is(err, ErrNotEntitled) {
		t.Errorf("a refused token reads as ErrNotEntitled: %v", err)
	}
	if !errors.Is(err, ErrReconnectRequired) || !XCredentialRejected(err) {
		t.Errorf("err = %v, want ErrReconnectRequired and XCredentialRejected", err)
	}
	if !strings.Contains(err.Error(), "reconnect the X account") || !strings.Contains(err.Error(), "Unsupported Authentication") {
		t.Errorf("err = %q, want X's title and the reconnect instruction", err)
	}
	if strings.Contains(err.Error(), "://") {
		t.Errorf("err = %q carries a URL", err)
	}
}

// The documented 403s stay what they were: about:blank is a tier or eligibility refusal, missing-scopes asks for a
// reconnect to grant the permission and is not a paid-tier claim.
func TestXCheckEntitlementKeepsTheDocumented403s(t *testing.T) {
	for _, tc := range []struct {
		name, body    string
		notEntitled   bool
		wantReconnect bool
	}{
		{"about:blank", `{"title":"Forbidden","detail":"Livestream access is not enabled","type":"about:blank","status":403}`, true, false},
		{"missing scopes", `{"title":"Forbidden","detail":"The token lacks broadcast.read","type":"https://api.x.com/2/problems/missing-scopes","status":403}`, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			x, _ := xStubLive(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusForbidden)
				_, _ = io.WriteString(w, tc.body)
			})
			err := x.CheckEntitlement(context.Background(), "client", "tok")
			if got := errors.Is(err, ErrNotEntitled); got != tc.notEntitled {
				t.Errorf("ErrNotEntitled = %v, want %v (%v)", got, tc.notEntitled, err)
			}
			if got := errors.Is(err, ErrReconnectRequired); got != tc.wantReconnect {
				t.Errorf("ErrReconnectRequired = %v, want %v (%v)", got, tc.wantReconnect, err)
			}
			if err != nil && strings.Contains(err.Error(), "://") {
				t.Errorf("err = %q carries a URL", err)
			}
		})
	}
}

// A go-live refused for a bad token must not become the terminal "not eligible"; the documented 403s still do.
func TestXGoLiveSeparatesARefusedTokenFromARefusedAccount(t *testing.T) {
	for _, tc := range []struct {
		name        string
		status      int
		body        string
		wantRefusal TransitionRefusal
		refused     bool
		reconnect   bool
	}{
		{"unsupported authentication", 403, xUnsupportedAuthBody, "", false, true},
		{"401", 401, `{"title":"Unauthorized","type":"about:blank","status":401,"detail":"Unauthorized"}`, "", false, true},
		{"not eligible", 403, `{"title":"Forbidden","detail":"The account isn't eligible to go live","type":"about:blank","status":403}`, RefusalNotEligible, true, false},
		{"missing scopes", 403, `{"title":"Forbidden","detail":"The token lacks broadcast.write","type":"https://api.x.com/2/problems/missing-scopes","status":403}`, RefusalMissingScopes, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			x, _ := xStubLive(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			})
			_, err := x.PublishBroadcast(context.Background(), "tok", xPendingHandle(xSourceID), XPublish{Title: "T"})
			var tr *TransitionRefused
			if got := errors.As(err, &tr); got != tc.refused {
				t.Fatalf("refused = %v, want %v (%v)", got, tc.refused, err)
			}
			if tc.refused && tr.Refusal != tc.wantRefusal {
				t.Errorf("refusal = %v, want %v", tr.Refusal, tc.wantRefusal)
			}
			if got := errors.Is(err, ErrReconnectRequired); got != tc.reconnect {
				t.Errorf("ErrReconnectRequired = %v, want %v (%v)", got, tc.reconnect, err)
			}
		})
	}
}

// The token endpoint's answers reach the operator as sentences, never as raw JSON, and only a dead grant is a reconnect.
func TestXRefreshWordsTheTokenEndpointsRefusals(t *testing.T) {
	for _, tc := range []struct {
		name      string
		status    int
		body      string
		want      string
		reconnect bool
	}{
		{"invalid_client", 400, xInvalidClientBody, "X rejected the app credentials (invalid_client): check the client id and secret", false},
		{"invalid_grant", 400, `{"error":"invalid_grant","error_description":"Value passed for the token was invalid."}`, "reconnect the X account in Settings", true},
		{"dead refresh token as invalid_request", 400, `{"error":"invalid_request","error_description":"Value passed for the token was invalid."}`, "reconnect the X account in Settings", true},
		{"a missing parameter is not a dead grant", 400, `{"error":"invalid_request","error_description":"Missing required parameter [grant_type]."}`, "X would not refresh the token (HTTP 400, invalid_request)", false},
		{"503", 503, `{"error":"temporarily_unavailable","access_token":"LEAK-ME"}`, "X would not refresh the token (HTTP 503, temporarily_unavailable)", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			x, _ := xStub(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			})
			_, err := x.Refresh(context.Background(), "client", "secret", "refresh-1")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to contain %q", err, tc.want)
			}
			if strings.ContainsAny(err.Error(), "{}") || strings.Contains(err.Error(), "LEAK-ME") {
				t.Errorf("err = %q carries the raw body", err)
			}
			if got := errors.Is(err, ErrReconnectRequired); got != tc.reconnect {
				t.Errorf("ErrReconnectRequired = %v, want %v", got, tc.reconnect)
			}
		})
	}
}

// A problem type is a URL; it identifies the problem to a program and stays out of operator text.
func TestXProblemTextKeepsTypeURLsOut(t *testing.T) {
	got := XProblemText([]byte(xUnsupportedAuthBody))
	if strings.Contains(got, "://") || !strings.Contains(got, "Unsupported Authentication") {
		t.Errorf("XProblemText = %q, want X's words and no URL", got)
	}
	if types := XProblemTypes([]byte(xUnsupportedAuthBody)); !strings.Contains(types, "unsupported-authentication") {
		t.Errorf("XProblemTypes = %q, want the type kept for classifiers", types)
	}
}
