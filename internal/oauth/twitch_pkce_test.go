package oauth

// Twitch's PKCE, which is on as of October 2026.
//
// twitch.go kept PKCE off for as long as Twitch published nothing about RFC
// 7636, on the argument that a code_challenge sent to an authorization server
// that validates its query string strictly locks every operator out of sign-in.
// That argument had a premise, and the weekly live suite
// (scripts/acceptance-oauth.sh, step 2) checked it: Twitch's discovery
// document at id.twitch.tv/oauth2/.well-known/openid-configuration now
// advertises code_challenge_methods_supported: ["S256"], and the suite went red
// on main on 2026-10-19 telling us to turn it on.
//
// The shared pkce_test.go cases already iterate every provider, so they cover
// Twitch generically once PKCE() reports true. These are the Twitch-specific
// halves they cannot see: that the challenge rides alongside force_verify on
// Twitch's own authorize path, that the verifier is sent IN ADDITION to the
// client secret rather than instead of it (Twitch's discovery document lists
// client_secret_post as its only token auth method -- this stays a
// confidential client), and that Refresh, which is what every account
// connected before this change runs on, never grew a verifier.

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

// twitchTokenStub aims a Twitch provider at a token endpoint that records the
// form it was sent and answers with a plausible token response. WithBaseURL
// moves the auth base and the Helix base together, so nothing here can reach
// the real id.twitch.tv.
func twitchTokenStub(t *testing.T) (*Twitch, *url.Values) {
	t.Helper()
	got := &url.Values{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/oauth2/token" {
			t.Errorf("token request went to %s, want /oauth2/token", r.URL.Path)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read token body: %v", err)
		}
		form, err := url.ParseQuery(string(body))
		if err != nil {
			t.Errorf("token body is not a form: %v", err)
		}
		*got = form
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"at","refresh_token":"rt","expires_in":14124,"token_type":"bearer"}`))
	}))
	t.Cleanup(srv.Close)
	return NewTwitch(WithBaseURL(srv.URL)), got
}

func TestTwitchAuthURLCarriesAnS256ChallengeAlongsideForceVerifyOnTwitchsAuthorizePath(t *testing.T) {
	tw := &Twitch{}
	if !tw.PKCE() {
		t.Fatal("Twitch.PKCE() is false; Twitch's discovery document advertises " +
			"code_challenge_methods_supported [S256], and the live suite fails while the two disagree")
	}

	const challenge = "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"
	tests := []struct {
		name          string
		challenge     string
		wantChallenge bool
	}{
		{"a challenge is forwarded with S256", challenge, true},
		// A state issued before the deploy has no verifier, so the handler
		// passes no challenge either; an empty code_challenge= would be a
		// malformed request rather than an absent one.
		{"no challenge sends no empty parameter", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			raw := tw.AuthURL("client-1", "https://polyemesis.test/api/v1/oauth/twitch/callback", "state-1", tc.challenge)
			u, err := url.Parse(raw)
			if err != nil {
				t.Fatalf("AuthURL is unparseable %q: %v", raw, err)
			}
			if got := u.Scheme + "://" + u.Host + u.Path; got != twitchIDBase+"/oauth2/authorize" {
				t.Fatalf("AuthURL endpoint = %q, want Twitch's /oauth2/authorize", got)
			}
			q := u.Query()
			// The parameters that were there before PKCE must all survive it;
			// force_verify in particular is what makes connecting a second
			// channel possible at all.
			for k, want := range map[string]string{
				"client_id":     "client-1",
				"response_type": "code",
				"state":         "state-1",
				"force_verify":  "true",
			} {
				if got := q.Get(k); got != want {
					t.Errorf("%s = %q, want %q", k, got, want)
				}
			}
			if !tc.wantChallenge {
				if q.Has("code_challenge") || q.Has("code_challenge_method") {
					t.Fatalf("an empty challenge still produced PKCE parameters: %s", u.RawQuery)
				}
				return
			}
			if got := q.Get("code_challenge"); got != tc.challenge {
				t.Errorf("code_challenge = %q, want %q", got, tc.challenge)
			}
			// S256 is the only method Twitch advertises, and the only one this
			// package ever sends.
			if got := q.Get("code_challenge_method"); got != "S256" {
				t.Errorf("code_challenge_method = %q, want S256", got)
			}
		})
	}
}

func TestTwitchExchangeSendsTheVerifierInAdditionToTheClientSecret(t *testing.T) {
	tw, got := twitchTokenStub(t)

	tok, err := tw.Exchange(context.Background(), "cid", "the-secret",
		"https://polyemesis.test/api/v1/oauth/twitch/callback", "the-code", "the-verifier")
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	if tok.AccessToken != "at" {
		t.Errorf("access token = %q, want the stub's", tok.AccessToken)
	}
	form := *got
	if v := form.Get("code_verifier"); v != "the-verifier" {
		t.Errorf("code_verifier = %q, want the verifier stored with the OAuth state", v)
	}
	// PKCE is additive here, not a switch to a public client: Twitch's token
	// documentation marks client_secret required for this grant, and
	// client_secret_post is the only auth method its discovery document lists.
	if s := form.Get("client_secret"); s != "the-secret" {
		t.Errorf("client_secret = %q, want it still sent alongside the verifier", s)
	}
	for k, want := range map[string]string{
		"client_id":    "cid",
		"code":         "the-code",
		"grant_type":   "authorization_code",
		"redirect_uri": "https://polyemesis.test/api/v1/oauth/twitch/callback",
	} {
		if v := form.Get(k); v != want {
			t.Errorf("%s = %q, want %q", k, v, want)
		}
	}
}

func TestTwitchExchangeOmitsTheVerifierForAStateIssuedBeforePKCEWasOn(t *testing.T) {
	// An operator who clicked Connect before the deploy and finished consent
	// after it holds a state stored with an empty verifier. Twitch issued that
	// code without a challenge, so an empty code_verifier= would be a verifier
	// that cannot match; omitting it is the request that still succeeds.
	tw, got := twitchTokenStub(t)
	if _, err := tw.Exchange(context.Background(), "cid", "the-secret",
		"https://polyemesis.test/cb", "the-code", ""); err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	if got.Has("code_verifier") {
		t.Errorf("Exchange sent code_verifier=%q with no verifier stored", got.Get("code_verifier"))
	}
}

func TestTwitchRefreshNeverSendsAVerifierSoAccountsConnectedBeforePKCEKeepRefreshing(t *testing.T) {
	// PKCE binds an authorization code to the request that asked for it, and
	// nothing else: RFC 7636 has no part in the refresh_token grant. Every
	// Twitch account connected before this change holds a refresh token issued
	// without a challenge, and Refresh is the only call it will ever make
	// again. This pins that it stays exactly the grant it was.
	tw, got := twitchTokenStub(t)
	tok, err := tw.Refresh(context.Background(), "cid", "the-secret", "old-refresh")
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if got.Has("code_verifier") || got.Has("code_challenge") {
		t.Errorf("Refresh sent PKCE parameters: %v", got.Encode())
	}
	for k, want := range map[string]string{
		"client_id":     "cid",
		"client_secret": "the-secret",
		"grant_type":    "refresh_token",
		"refresh_token": "old-refresh",
	} {
		if v := got.Get(k); v != want {
			t.Errorf("%s = %q, want %q", k, v, want)
		}
	}
	if tok.RefreshToken != "rt" {
		t.Errorf("refresh token = %q, want the rotated one Twitch returned", tok.RefreshToken)
	}
}
