package chat

// Kick webhook signature verification.
//
// Kick signs every webhook delivery. This file implements the check, and it is
// worth recording why it did not exist until now: KickConfig carried an
// optional `Verify` hook whose comment read "the hook for Kick's signature
// header once its scheme is documented. Nothing here invents one." That was an
// honest position — the code refused to guess at a scheme rather than shipping
// a check that only looked like one.
//
// It was also out of date. The scheme is documented, at
// https://docs.kick.com/events/webhook-security, and this file follows it
// exactly rather than approximating it:
//
//   - The signed message is the three values `Kick-Event-Message-Id`,
//     `Kick-Event-Message-Timestamp` and the raw request body, joined with "."
//   - It is signed RSASSA-PKCS1-v1_5 over SHA-256 with Kick's private key.
//   - `Kick-Event-Signature` carries the signature, standard base64.
//   - The matching public key is PKIX-encoded PEM, served from
//     https://api.kick.com/public/v1/public-key
//
// The same correction happened once before in this repo, with Kick's stream
// key: recorded as unavailable, then found riding on a response already being
// fetched. "We looked and there was nothing" is worth re-checking whenever the
// thing it justifies is a disabled security control.

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

// The header names Kick sends. Spelled once, here, because a typo in any of
// them produces a verifier that rejects everything and a chat pane that stays
// empty for a reason nobody can see.
const (
	KickHeaderMessageID = "Kick-Event-Message-Id"
	KickHeaderTimestamp = "Kick-Event-Message-Timestamp"
	KickHeaderSignature = "Kick-Event-Signature"
)

// KickPublicKeyURL is where Kick publishes the key that matches the private one
// it signs with.
const KickPublicKeyURL = "https://api.kick.com/public/v1/public-key"

// ErrKickSignature is returned for every verification failure.
//
// One error for all of them on purpose: "bad signature", "no such header" and
// "malformed base64" are the same answer to the sender — no — and
// distinguishing them in a reply tells whoever is probing which part of their
// forgery to fix next. The detail goes in the wrapped message, which is logged
// locally and never returned over the wire.
var ErrKickSignature = errors.New("kick webhook signature rejected")

// VerifyKickSignature checks one delivery against a public key.
//
// body must be the bytes exactly as received. Kick signs the raw octets, so a
// body that has been through a JSON decode and re-encode will not verify even
// when it is semantically identical — which is why the handler reads the body
// once, verifies those bytes, and only then parses them.
func VerifyKickSignature(pub *rsa.PublicKey, r *http.Request, body []byte) error {
	if pub == nil {
		return fmt.Errorf("%w: no public key", ErrKickSignature)
	}

	messageID := r.Header.Get(KickHeaderMessageID)
	timestamp := r.Header.Get(KickHeaderTimestamp)
	sig := r.Header.Get(KickHeaderSignature)
	if messageID == "" || timestamp == "" || sig == "" {
		return fmt.Errorf("%w: missing one of %s, %s, %s",
			ErrKickSignature, KickHeaderMessageID, KickHeaderTimestamp, KickHeaderSignature)
	}

	raw, err := base64.StdEncoding.DecodeString(sig)
	if err != nil {
		return fmt.Errorf("%w: signature is not base64: %v", ErrKickSignature, err)
	}

	// The order and the separator are the specification, not a convention. A
	// verifier that concatenated these differently would reject every genuine
	// delivery, so this exact line is what the tests pin.
	signed := fmt.Sprintf("%s.%s.%s", messageID, timestamp, body)
	digest := sha256.Sum256([]byte(signed))

	if err := rsa.VerifyPKCS1v15(pub, crypto.SHA256, digest[:], raw); err != nil {
		return fmt.Errorf("%w: %v", ErrKickSignature, err)
	}
	return nil
}

// ParseKickPublicKey decodes Kick's PEM public key.
func ParseKickPublicKey(pemBytes []byte) (*rsa.PublicKey, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, errors.New("kick public key: not PEM")
	}
	if block.Type != "PUBLIC KEY" {
		return nil, fmt.Errorf("kick public key: PEM block is %q, want PUBLIC KEY", block.Type)
	}
	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("kick public key: %w", err)
	}
	pub, ok := parsed.(*rsa.PublicKey)
	if !ok {
		return nil, fmt.Errorf("kick public key: got %T, want *rsa.PublicKey", parsed)
	}
	return pub, nil
}

// KickKeyFetcher fetches and caches Kick's public key.
//
// The key is fetched rather than compiled in. An embedded key is one key
// rotation away from rejecting every delivery with no way for an operator to
// fix it short of a new binary; fetching it over TLS from api.kick.com puts the
// trust where it already is — the same host, and the same certificate chain,
// that the rest of this adapter talks to.
//
// It is cached because the key does not change between deliveries and a chat
// channel can deliver many per second. A failed fetch is NOT cached: the next
// delivery retries, so a momentary blip does not disable verification for the
// lifetime of the process.
type KickKeyFetcher struct {
	HTTP *http.Client
	URL  string

	mu  sync.Mutex
	key *rsa.PublicKey
}

// Key returns the cached public key, fetching it on first use.
func (f *KickKeyFetcher) Key(ctx context.Context) (*rsa.PublicKey, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.key != nil {
		return f.key, nil
	}

	url := f.URL
	if url == "" {
		url = KickPublicKeyURL
	}
	hc := f.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch kick public key: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch kick public key: HTTP %d", resp.StatusCode)
	}
	// Generous but bounded: a PEM RSA public key is well under a kilobyte, and
	// an unbounded read from a host that has started returning something else
	// is a memory bug waiting for a bad day.
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 16<<10))
	if err != nil {
		return nil, fmt.Errorf("fetch kick public key: %w", err)
	}

	var envelope struct {
		Data struct {
			PublicKey string `json:"public_key"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, fmt.Errorf("fetch kick public key: %w", err)
	}
	if envelope.Data.PublicKey == "" {
		return nil, errors.New("fetch kick public key: response carried no key")
	}

	key, err := ParseKickPublicKey([]byte(envelope.Data.PublicKey))
	if err != nil {
		return nil, err
	}
	f.key = key
	return key, nil
}

// KickVerifier returns the Verify function KickConfig expects, backed by a
// lazily fetched public key.
//
// Verification is not made conditional on the fetch succeeding. If the key
// cannot be retrieved the delivery is refused, because "we could not check this
// one" and "this one is fine" must not produce the same outcome — that
// equivalence is exactly what left this check switched off before.
func KickVerifier(f *KickKeyFetcher) func(r *http.Request, body []byte) error {
	return func(r *http.Request, body []byte) error {
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()

		key, err := f.Key(ctx)
		if err != nil {
			return fmt.Errorf("%w: %v", ErrKickSignature, err)
		}
		return VerifyKickSignature(key, r, body)
	}
}
