package auth

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func testManager(t *testing.T, fill byte) *Manager {
	t.Helper()
	return New(bytes.Repeat([]byte{fill}, 32), false, false)
}

func TestIssueThenVerifyReturnsTheIssuedIdentity(t *testing.T) {
	m := testManager(t, 0x2a)

	token, err := m.Issue(7, "admin")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	claims, err := m.Verify(token)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if claims.Username != "admin" {
		t.Errorf("Username = %q, want %q", claims.Username, "admin")
	}
	if claims.Subject != "7" {
		t.Errorf("Subject = %q, want %q", claims.Subject, "7")
	}
	if claims.Issuer != "polyemesis" {
		t.Errorf("Issuer = %q, want %q", claims.Issuer, "polyemesis")
	}
}

func TestVerifyRejects(t *testing.T) {
	m := testManager(t, 0x2a)

	// Signed with the right algorithm but the wrong key: a forger who guessed
	// the claim shape but not the secret.
	foreign := testManager(t, 0x99)
	foreignToken, err := foreign.Issue(1, "attacker")
	if err != nil {
		t.Fatalf("issue foreign token: %v", err)
	}

	signedWith := func(t *testing.T, key []byte, mutate func(*Claims)) string {
		t.Helper()
		now := time.Now()
		claims := Claims{
			RegisteredClaims: jwt.RegisteredClaims{
				Subject:   "1",
				IssuedAt:  jwt.NewNumericDate(now),
				ExpiresAt: jwt.NewNumericDate(now.Add(time.Hour)),
				NotBefore: jwt.NewNumericDate(now.Add(-time.Minute)),
				Issuer:    "polyemesis",
			},
			Username: "admin",
		}
		mutate(&claims)
		s, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(key)
		if err != nil {
			t.Fatalf("sign: %v", err)
		}
		return s
	}

	key := bytes.Repeat([]byte{0x2a}, 32)

	tests := []struct {
		name  string
		token func(t *testing.T) string
	}{
		{
			name:  "a token signed with a different key is rejected",
			token: func(t *testing.T) string { return foreignToken },
		},
		{
			name: "an alg:none token with no signature is rejected",
			// The classic downgrade: strip the signature and claim the token
			// needs none.
			token: func(t *testing.T) string { return unsignedToken(t, "none") },
		},
		{
			name:  "an alg:None token is rejected despite the casing",
			token: func(t *testing.T) string { return unsignedToken(t, "None") },
		},
		{
			name: "an expired token is rejected",
			token: func(t *testing.T) string {
				return signedWith(t, key, func(c *Claims) {
					c.IssuedAt = jwt.NewNumericDate(time.Now().Add(-2 * time.Hour))
					c.NotBefore = jwt.NewNumericDate(time.Now().Add(-2 * time.Hour))
					c.ExpiresAt = jwt.NewNumericDate(time.Now().Add(-time.Hour))
				})
			},
		},
		{
			name: "a token from a different issuer is rejected",
			token: func(t *testing.T) string {
				return signedWith(t, key, func(c *Claims) { c.Issuer = "somewhere-else" })
			},
		},
		{
			name:  "an empty token is rejected",
			token: func(t *testing.T) string { return "" },
		},
		{
			name:  "a garbage token is rejected",
			token: func(t *testing.T) string { return "not.a.jwt" },
		},
		{
			name: "a token with a tampered payload is rejected",
			token: func(t *testing.T) string {
				good, err := m.Issue(1, "admin")
				if err != nil {
					t.Fatalf("Issue: %v", err)
				}
				// Flip a byte in the payload segment; the HMAC must no longer match.
				b := []byte(good)
				dot := bytes.IndexByte(b, '.')
				b[dot+5] ^= 0x01
				return string(b)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			claims, err := m.Verify(tt.token(t))
			if err == nil {
				t.Fatalf("Verify() accepted the token and returned %+v", claims)
			}
			if err != ErrUnauthorized {
				t.Errorf("Verify() = %v, want ErrUnauthorized", err)
			}
		})
	}
}

// unsignedToken hand-builds a JWT with the given alg header and no signature,
// which is what an "alg: none" confusion attack looks like on the wire.
func unsignedToken(t *testing.T, alg string) string {
	t.Helper()

	header, err := json.Marshal(map[string]string{"alg": alg, "typ": "JWT"})
	if err != nil {
		t.Fatalf("marshal header: %v", err)
	}
	now := time.Now()
	payload, err := json.Marshal(Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   "1",
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(time.Hour)),
			NotBefore: jwt.NewNumericDate(now.Add(-time.Minute)),
			Issuer:    "polyemesis",
		},
		Username: "attacker",
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	enc := base64.RawURLEncoding.EncodeToString
	return enc(header) + "." + enc(payload) + "."
}

func TestCheckCSRF(t *testing.T) {
	const token = "kZ8pQ2r_TokenValue"

	tests := []struct {
		name    string
		method  string
		cookie  string
		header  string
		wantErr bool
	}{
		{name: "GET is exempt even with no token at all", method: http.MethodGet},
		{name: "HEAD is exempt even with no token at all", method: http.MethodHead},
		{name: "OPTIONS is exempt even with no token at all", method: http.MethodOptions},
		{
			name:   "POST with a matching cookie and header is allowed",
			method: http.MethodPost, cookie: token, header: token,
		},
		{
			name:   "DELETE with a matching cookie and header is allowed",
			method: http.MethodDelete, cookie: token, header: token,
		},
		{
			name:   "POST with no cookie is rejected",
			method: http.MethodPost, header: token, wantErr: true,
		},
		{
			name:   "POST with an empty cookie is rejected",
			method: http.MethodPost, cookie: "", header: token, wantErr: true,
		},
		{
			name:   "POST with no header is rejected",
			method: http.MethodPost, cookie: token, wantErr: true,
		},
		{
			name:   "POST where the header does not match the cookie is rejected",
			method: http.MethodPost, cookie: token, header: "kZ8pQ2r_TokenValu3", wantErr: true,
		},
		{
			name:   "POST where the header is a prefix of the cookie is rejected",
			method: http.MethodPost, cookie: token, header: token[:8], wantErr: true,
		},
		{
			name:   "PUT with neither cookie nor header is rejected",
			method: http.MethodPut, wantErr: true,
		},
		{
			name:   "PATCH with a matching cookie and header is allowed",
			method: http.MethodPatch, cookie: token, header: token,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(tt.method, "/api/destinations", nil)
			if tt.cookie != "" {
				r.AddCookie(&http.Cookie{Name: CSRFCookie, Value: tt.cookie})
			}
			if tt.header != "" {
				r.Header.Set(CSRFHeader, tt.header)
			}

			err := CheckCSRF(r)
			if tt.wantErr && err == nil {
				t.Fatal("CheckCSRF() = nil, want an error")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("CheckCSRF() = %v, want nil", err)
			}
		})
	}
}

func TestFromRequestReadsTheSessionCookieSetBySetSession(t *testing.T) {
	m := testManager(t, 0x2a)

	token, err := m.Issue(1, "admin")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	w := httptest.NewRecorder()
	if err := m.SetSession(w, httptest.NewRequest(http.MethodPost, "/api/login", nil), token); err != nil {
		t.Fatalf("SetSession: %v", err)
	}

	r := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	var sawCSRF bool
	for _, c := range w.Result().Cookies() {
		r.AddCookie(c)
		if c.Name == CSRFCookie {
			sawCSRF = true
			if c.HttpOnly {
				t.Error("the CSRF cookie is HttpOnly; the SPA cannot echo a token it cannot read")
			}
		}
		if c.Name == SessionCookie && !c.HttpOnly {
			t.Error("the session cookie is not HttpOnly; XSS could read the JWT")
		}
	}
	if !sawCSRF {
		t.Fatal("SetSession did not set a CSRF cookie")
	}

	claims, err := m.FromRequest(r)
	if err != nil {
		t.Fatalf("FromRequest: %v", err)
	}
	if claims.Username != "admin" {
		t.Errorf("Username = %q, want %q", claims.Username, "admin")
	}
}

func TestFromRequestWithoutASessionCookieIsUnauthorized(t *testing.T) {
	m := testManager(t, 0x2a)
	r := httptest.NewRequest(http.MethodGet, "/api/status", nil)

	if _, err := m.FromRequest(r); err != ErrUnauthorized {
		t.Fatalf("FromRequest() = %v, want ErrUnauthorized", err)
	}
}

func TestRandomTokenIsUnpredictable(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 64; i++ {
		tok, err := RandomToken()
		if err != nil {
			t.Fatalf("RandomToken: %v", err)
		}
		if len(tok) != 43 {
			t.Fatalf("RandomToken() has length %d, want 43 (256 bits, base64url unpadded)", len(tok))
		}
		if seen[tok] {
			t.Fatalf("RandomToken() repeated %q within 64 draws", tok)
		}
		seen[tok] = true
	}
}
