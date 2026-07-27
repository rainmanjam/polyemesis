package oauth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
)

// 32 random bytes encode to a 43-character verifier: the shortest length
// RFC 7636 §4.1 permits, and already 256 bits of entropy. Raw base64url is
// used because its alphabet is a subset of the unreserved characters the RFC
// requires, so the verifier survives a form encoding untouched.
const verifierBytes = 32

// NewPKCE returns a fresh code_verifier and the S256 code_challenge derived
// from it. The verifier stays server-side with the state row; only the
// challenge travels through the user's browser.
func NewPKCE() (verifier, challenge string, err error) {
	b := make([]byte, verifierBytes)
	if _, err := rand.Read(b); err != nil {
		return "", "", err
	}
	verifier = base64.RawURLEncoding.EncodeToString(b)
	return verifier, Challenge(verifier), nil
}

// Challenge derives the RFC 7636 S256 code_challenge for a verifier.
func Challenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}
