package oauth

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
)

func TestChallengeIsTheBase64URLSHA256OfTheVerifier(t *testing.T) {
	tests := []struct {
		name     string
		verifier string
		want     string
	}{
		{
			// RFC 7636 appendix B: if this vector ever fails, every S256
			// exchange we make is being rejected by the platform.
			name:     "rfc 7636 appendix B vector",
			verifier: "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk",
			want:     "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM",
		},
		{
			name:     "empty verifier still hashes",
			verifier: "",
			want:     "47DEQpj8HBSa-_TImW-5JCeuQeRkm5NMpJWZG3hSuFU",
		},
		{
			name:     "a single differing character changes the challenge",
			verifier: "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXj",
			want:     "8AuWQe2Sg66Pu1SExiKweDeww7b3MY2_Ktkgbbb2tA0",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Challenge(tt.verifier); got != tt.want {
				t.Errorf("Challenge(%q) = %q, want %q", tt.verifier, got, tt.want)
			}
		})
	}
}

func TestNewPKCEReturnsAnUnreservedVerifierMatchingItsChallenge(t *testing.T) {
	const unreserved = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-._~"

	seen := make(map[string]bool, 64)
	for range 64 {
		verifier, challenge, err := NewPKCE()
		if err != nil {
			t.Fatalf("NewPKCE: %v", err)
		}
		// RFC 7636 §4.1 bounds the verifier at 43..128 unreserved characters;
		// a platform rejects anything outside that range outright.
		if len(verifier) < 43 || len(verifier) > 128 {
			t.Fatalf("verifier length = %d, want 43..128", len(verifier))
		}
		if i := strings.IndexFunc(verifier, func(r rune) bool {
			return !strings.ContainsRune(unreserved, r)
		}); i >= 0 {
			t.Fatalf("verifier %q contains a reserved character %q at %d", verifier, verifier[i], i)
		}
		if challenge != Challenge(verifier) {
			t.Fatalf("NewPKCE challenge = %q, want %q", challenge, Challenge(verifier))
		}
		if seen[verifier] {
			t.Fatalf("NewPKCE repeated the verifier %q; verifiers must be single-use", verifier)
		}
		seen[verifier] = true
	}
}

func TestAuthURLCarriesPKCEParamsOnlyForProvidersThatOptIn(t *testing.T) {
	const challenge = "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"

	for platform, p := range Providers() {
		t.Run(string(platform), func(t *testing.T) {
			// Feed a challenge regardless of the opt-in: a provider that says
			// no must not leak the parameter even if a caller gets it wrong.
			raw := p.AuthURL("cid", "https://example.test/cb", "state-1", challenge)
			u, err := url.Parse(raw)
			if err != nil {
				t.Fatalf("AuthURL returned an unparseable URL %q: %v", raw, err)
			}
			q := u.Query()

			if !p.PKCE() {
				if q.Has("code_challenge") || q.Has("code_challenge_method") {
					t.Fatalf("%s does not opt into PKCE but AuthURL sent %v", platform, u.RawQuery)
				}
				return
			}
			if got := q.Get("code_challenge"); got != challenge {
				t.Errorf("code_challenge = %q, want %q", got, challenge)
			}
			// Plain is permitted by the RFC but pointless over a redirect the
			// attacker can read; S256 is the only method we ever send.
			if got := q.Get("code_challenge_method"); got != "S256" {
				t.Errorf("code_challenge_method = %q, want S256", got)
			}
		})
	}
}

func TestAuthURLOmitsPKCEParamsWhenNoChallengeIsIssued(t *testing.T) {
	for platform, p := range Providers() {
		t.Run(string(platform), func(t *testing.T) {
			u, err := url.Parse(p.AuthURL("cid", "https://example.test/cb", "state-1", ""))
			if err != nil {
				t.Fatalf("parse AuthURL: %v", err)
			}
			if u.Query().Has("code_challenge") {
				t.Errorf("%s sent a code_challenge without one being issued: %v", platform, u.RawQuery)
			}
		})
	}
}

func TestExchangeSendsCodeVerifierOnlyForProvidersThatOptIn(t *testing.T) {
	for platform, p := range Providers() {
		t.Run(string(platform), func(t *testing.T) {
			form := captureTokenForm(t, func() (*Token, error) {
				return p.Exchange(context.Background(), "cid", "secret",
					"https://example.test/cb", "the-code", "the-verifier")
			})

			if got := form.Get("code"); got != "the-code" {
				t.Errorf("code = %q, want %q", got, "the-code")
			}
			want := ""
			if p.PKCE() {
				want = "the-verifier"
			}
			if got := form.Get("code_verifier"); got != want {
				t.Errorf("%s Exchange sent code_verifier = %q, want %q", platform, got, want)
			}
		})
	}
}

func TestExchangeOmitsCodeVerifierWhenNoneWasStored(t *testing.T) {
	// States issued before PKCE was switched on carry no verifier; sending an
	// empty one would fail the exchange where omitting it succeeds.
	for platform, p := range Providers() {
		t.Run(string(platform), func(t *testing.T) {
			form := captureTokenForm(t, func() (*Token, error) {
				return p.Exchange(context.Background(), "cid", "secret",
					"https://example.test/cb", "the-code", "")
			})
			if form.Has("code_verifier") {
				t.Errorf("%s sent code_verifier with no verifier stored", platform)
			}
		})
	}
}

func TestProvidersOnlyClaimPKCEWhereItIsDocumented(t *testing.T) {
	// Pinned deliberately: flipping one of these on without confirming the
	// platform accepts RFC 7636 breaks sign-in for that platform entirely.
	want := map[db.Platform]bool{
		db.PlatformYouTube: true,
		db.PlatformTwitch:  false,
		// Meta's Login dialog does not document code_challenge; sending one is
		// the lock-everyone-out risk Provider.PKCE exists to avoid.
		db.PlatformFacebook: false,
		// Kick speaks OAuth 2.1, which folds RFC 7636 into the grant itself.
		db.PlatformKick: true,
		// Vimeo's authentication guide documents four grant types -- client
		// credentials, authorization code, implicit and device code -- and
		// mentions no PKCE parameter in any of them; the code flow is a
		// confidential client using client_secret over HTTP Basic. Vimeo's own
		// account of a malformed authorize request is that "the request fails,
		// and the standard Vimeo 404 page loads", which is this flag's
		// lock-everyone-out risk in its least diagnosable form.
		db.PlatformVimeo: false,
	}
	for platform, p := range Providers() {
		w, ok := want[platform]
		if !ok {
			t.Errorf("provider %q has no recorded PKCE decision; verify the platform's docs and add one", platform)
			continue
		}
		if p.PKCE() != w {
			t.Errorf("%s PKCE() = %v, want %v", platform, p.PKCE(), w)
		}
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// captureTokenForm runs a token request against a stub transport and returns
// the parameters the provider posted, whichever way it encoded them.
//
// IT USED TO ASSUME A FORM, and the two tests above are about PARAMETERS rather
// than about encodings -- "did this provider send code, and did it send
// code_verifier only if it opted into PKCE". Vimeo is the first provider here
// whose token endpoint takes a JSON body with HTTP Basic client authentication
// (its Table 8 is explicit about all three headers), and url.ParseQuery on a
// JSON object does not fail -- it returns a single garbage key. So the guard
// would have gone on printing ok for a provider it could no longer read,
// leaking a code_verifier past it unnoticed.
//
// Content-Type decides, because that is what the SERVER uses to decide.
func captureTokenForm(t *testing.T, do func() (*Token, error)) url.Values {
	t.Helper()

	orig := httpClient.Transport
	t.Cleanup(func() { httpClient.Transport = orig })

	var got url.Values
	httpClient.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, err
		}
		if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
			var fields map[string]any
			if err := json.Unmarshal(body, &fields); err != nil {
				return nil, err
			}
			got = url.Values{}
			for k, v := range fields {
				if s, ok := v.(string); ok {
					got.Set(k, s)
				}
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"access_token":"at","expires_in":3600}`)),
			}, nil
		}
		if got, err = url.ParseQuery(string(body)); err != nil {
			return nil, err
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"access_token":"at","expires_in":3600}`)),
		}, nil
	})

	if _, err := do(); err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	return got
}
