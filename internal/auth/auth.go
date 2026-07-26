// Package auth provides the session layer: a JWT in an HttpOnly cookie, plus
// double-submit CSRF protection on state-changing requests.
//
// Single admin user, so there is no role model and no user table to walk —
// the token proves "you are the admin", nothing more.
package auth

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	// SessionCookie holds the JWT. HttpOnly, so XSS cannot read it.
	SessionCookie = "polyemesis_session"
	// CSRFCookie holds the double-submit token. Deliberately NOT HttpOnly:
	// the SPA must read it to echo it back in a header.
	CSRFCookie = "polyemesis_csrf"
	// CSRFHeader is where the SPA echoes the token.
	CSRFHeader = "X-CSRF-Token"

	// sessionTTL is how long a login lasts. Long enough that a streamer is not
	// logged out mid-broadcast, short enough that a stolen laptop expires.
	sessionTTL = 7 * 24 * time.Hour
)

// ErrUnauthorized is returned for any failed authentication.
var ErrUnauthorized = errors.New("unauthorized")

// Manager issues and validates sessions.
type Manager struct {
	key []byte
	// secure marks cookies Secure. Set when serving TLS directly, or when a
	// trusted proxy reports X-Forwarded-Proto: https.
	secure     bool
	trustProxy bool
}

// New creates a session manager. key should come from secrets.Box.Derive.
func New(key []byte, secure, trustProxy bool) *Manager {
	return &Manager{key: key, secure: secure, trustProxy: trustProxy}
}

// Claims is the session payload.
type Claims struct {
	jwt.RegisteredClaims
	Username string `json:"username"`
}

// Issue signs a session token for a user.
func (m *Manager) Issue(userID int64, username string) (string, error) {
	now := time.Now()
	claims := Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   fmt.Sprint(userID),
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(sessionTTL)),
			NotBefore: jwt.NewNumericDate(now.Add(-time.Minute)),
			Issuer:    "polyemesis",
		},
		Username: username,
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(m.key)
}

// Verify parses and validates a token.
func (m *Manager) Verify(token string) (*Claims, error) {
	var claims Claims
	_, err := jwt.ParseWithClaims(token, &claims, func(t *jwt.Token) (any, error) {
		// Pinning the algorithm is what stops the classic "alg: none" and
		// HMAC/RSA confusion attacks.
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method %v", t.Header["alg"])
		}
		return m.key, nil
	}, jwt.WithIssuer("polyemesis"), jwt.WithValidMethods([]string{"HS256"}))
	if err != nil {
		return nil, ErrUnauthorized
	}
	return &claims, nil
}

func (m *Manager) isSecure(r *http.Request) bool {
	if m.secure {
		return true
	}
	if r.TLS != nil {
		return true
	}
	// Only honoured behind a proxy we were told to trust; otherwise a client
	// could set the header itself and influence our cookie flags.
	if m.trustProxy && strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		return true
	}
	return false
}

// SetSession writes the session and CSRF cookies.
func (m *Manager) SetSession(w http.ResponseWriter, r *http.Request, token string) error {
	secure := m.isSecure(r)

	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookie,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   secure,
		// Lax rather than Strict: the OAuth providers redirect back to us with
		// a top-level GET, and Strict would drop the session on that hop and
		// bounce the user to the login screen mid-connect.
		SameSite: http.SameSiteLaxMode,
		Expires:  time.Now().Add(sessionTTL),
		MaxAge:   int(sessionTTL.Seconds()),
	})

	csrf, err := RandomToken()
	if err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name:     CSRFCookie,
		Value:    csrf,
		Path:     "/",
		HttpOnly: false, // the SPA must read this to echo it back
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
		Expires:  time.Now().Add(sessionTTL),
		MaxAge:   int(sessionTTL.Seconds()),
	})
	return nil
}

// ClearSession expires both cookies.
func (m *Manager) ClearSession(w http.ResponseWriter, r *http.Request) {
	secure := m.isSecure(r)
	for _, name := range []string{SessionCookie, CSRFCookie} {
		http.SetCookie(w, &http.Cookie{
			Name: name, Value: "", Path: "/",
			HttpOnly: name == SessionCookie, Secure: secure,
			SameSite: http.SameSiteLaxMode,
			Expires:  time.Unix(0, 0), MaxAge: -1,
		})
	}
}

// FromRequest extracts and verifies the session.
func (m *Manager) FromRequest(r *http.Request) (*Claims, error) {
	c, err := r.Cookie(SessionCookie)
	if err != nil || c.Value == "" {
		return nil, ErrUnauthorized
	}
	return m.Verify(c.Value)
}

// CheckCSRF validates the double-submit token on state-changing requests.
//
// SameSite=Lax already blocks cross-site POSTs in every current browser; this
// is the second layer, and the one that still holds if a future browser or a
// misconfigured proxy weakens the first.
func CheckCSRF(r *http.Request) error {
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return nil
	}
	cookie, err := r.Cookie(CSRFCookie)
	if err != nil || cookie.Value == "" {
		return errors.New("missing CSRF cookie")
	}
	header := r.Header.Get(CSRFHeader)
	if header == "" {
		return errors.New("missing " + CSRFHeader + " header")
	}
	// Constant-time compare: the token is a secret, and a timing oracle on it
	// is cheap to avoid.
	if subtleCompare(cookie.Value, header) != 1 {
		return errors.New("CSRF token mismatch")
	}
	return nil
}

// RandomToken returns a 256-bit URL-safe random string.
func RandomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func subtleCompare(a, b string) int {
	if len(a) != len(b) {
		return 0
	}
	var v byte
	for i := 0; i < len(a); i++ {
		v |= a[i] ^ b[i]
	}
	if v == 0 {
		return 1
	}
	return 0
}
