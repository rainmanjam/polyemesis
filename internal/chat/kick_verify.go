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

	"github.com/rainmanjam/polyemesis/internal/platform"
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
const KickPublicKeyURL = platform.KickAPIHost + "/public/v1/public-key"

// ErrKickKeyUnavailable means the signing key could not be obtained, so the
// delivery could be neither accepted nor rejected on its merits. The handler
// answers 503 for it rather than 401: Kick counts a 401 as a failed delivery
// toward its one-day auto-disable, and a 503 is the answer that says "try
// again", which is what is true. (Kick counts any failure to process, so a 503
// counts too; only the retry semantics differ.)
var ErrKickKeyUnavailable = errors.New("kick signing key unavailable")

// ErrKickStale means the signature verified but the timestamp is outside
// kickMaxSkew. The delivery is genuinely Kick's, so it is acknowledged with 200
// and not ingested (a replay must not be accepted), rather than refused: a 401
// or 503 on a legitimate late retry would count toward Kick's auto-disable.
var ErrKickStale = errors.New("kick webhook delivery is outside the freshness window")

// ErrKickDuplicate means this Message-Id was already accepted. Kick retries a
// delivery it believes failed, so a repeat is expected and the handler answers
// 200 without ingesting it a second time.
var ErrKickDuplicate = errors.New("kick webhook delivery already processed")

// kickMaxSkew bounds how far a delivery's timestamp may sit from now. The
// timestamp is signed, so a captured delivery cannot be given a fresh one; this
// is what stops a replay from being accepted indefinitely.
const kickMaxSkew = 5 * time.Minute

// kickKeyRefetchEvery rate-limits refetching the signing key after a
// signature failure, so a stream of forgeries cannot turn into a stream of
// requests to api.kick.com.
const kickKeyRefetchEvery = time.Minute

// kickReplayRing is how many accepted Message-Ids the verifier remembers. The
// timestamp window already bounds how old a replayable delivery can be, so this
// only has to span that window at chat rates.
const kickReplayRing = 4096

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
// trust where it already is -- the same host, and the same certificate chain,
// that the rest of this adapter talks to.
//
// It is cached because the key does not change between deliveries and a chat
// channel can deliver many per second. A failed fetch is NOT cached: the next
// delivery retries, so a momentary blip does not disable verification for the
// lifetime of the process. The cache is not forever either: Refresh replaces it
// after a signature failure, which is how a rotated key is picked up without a
// restart.
//
// The lock guards the cache only and is never held across the network. A fetch
// is shared by everyone who arrives while it runs, so a stalled api.kick.com
// costs each delivery its own bounded wait instead of queueing them all behind
// one.
type KickKeyFetcher struct {
	HTTP *http.Client
	URL  string
	// Now is the clock for the refetch rate limit; time.Now when nil.
	Now func() time.Time

	mu        sync.Mutex
	key       *rsa.PublicKey
	fetchedAt time.Time
	flight    *kickFlight
}

// kickFlight is one in-progress fetch that concurrent callers wait on.
type kickFlight struct {
	done chan struct{}
	key  *rsa.PublicKey
	err  error
}

func (f *KickKeyFetcher) now() time.Time {
	if f.Now != nil {
		return f.Now()
	}
	return time.Now()
}

// Key returns the cached public key, fetching it on first use.
func (f *KickKeyFetcher) Key(ctx context.Context) (*rsa.PublicKey, error) {
	f.mu.Lock()
	if f.key != nil {
		k := f.key
		f.mu.Unlock()
		return k, nil
	}
	fl := f.joinLocked(ctx)
	f.mu.Unlock()
	return f.wait(ctx, fl)
}

// Refresh refetches the key after stale failed to verify a delivery. It returns
// the key to try next: the cached one when somebody already replaced stale, or
// when a refetch ran less than a minute ago (in which case the failure is
// simply a bad signature), and the freshly fetched one otherwise.
func (f *KickKeyFetcher) Refresh(ctx context.Context, stale *rsa.PublicKey) (*rsa.PublicKey, error) {
	f.mu.Lock()
	if f.key != nil && f.key != stale {
		k := f.key
		f.mu.Unlock()
		return k, nil
	}
	if f.key != nil && f.now().Sub(f.fetchedAt) < kickKeyRefetchEvery {
		k := f.key
		f.mu.Unlock()
		return k, nil
	}
	fl := f.joinLocked(ctx)
	f.mu.Unlock()
	return f.wait(ctx, fl)
}

// joinLocked returns the fetch in progress, starting one if there is none. The
// caller holds f.mu.
func (f *KickKeyFetcher) joinLocked(ctx context.Context) *kickFlight {
	if f.flight != nil {
		return f.flight
	}
	fl := &kickFlight{done: make(chan struct{})}
	f.flight = fl
	// Detached from the first caller's context: that caller may give up while
	// others are still waiting on this fetch. Bounded on its own instead.
	fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	go func() {
		defer cancel()
		fl.key, fl.err = f.fetch(fctx)
		f.mu.Lock()
		if fl.err == nil {
			f.key = fl.key
			f.fetchedAt = f.now()
		}
		f.flight = nil
		f.mu.Unlock()
		close(fl.done)
	}()
	return fl
}

func (f *KickKeyFetcher) wait(ctx context.Context, fl *kickFlight) (*rsa.PublicKey, error) {
	select {
	case <-fl.done:
		return fl.key, fl.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (f *KickKeyFetcher) fetch(ctx context.Context) (*rsa.PublicKey, error) {
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

	return ParseKickPublicKey([]byte(envelope.Data.PublicKey))
}

// kickReplayGuard remembers the Message-Ids it has accepted.
type kickReplayGuard struct {
	mu   sync.Mutex
	seen map[string]struct{}
	ring []string
	next int
}

// firstSight records id and reports whether it was new.
func (g *kickReplayGuard) firstSight(id string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, dup := g.seen[id]; dup {
		return false
	}
	if g.seen == nil {
		g.seen = make(map[string]struct{}, kickReplayRing)
		g.ring = make([]string, kickReplayRing)
	}
	delete(g.seen, g.ring[g.next])
	g.ring[g.next] = id
	g.next = (g.next + 1) % kickReplayRing
	g.seen[id] = struct{}{}
	return true
}

// KickVerifier returns the Verify function KickConfig expects, backed by a
// lazily fetched public key.
//
// Verification is not made conditional on the fetch succeeding. If the key
// cannot be retrieved the delivery is refused, because "we could not check this
// one" and "this one is fine" must not produce the same outcome -- that
// equivalence is exactly what left this check switched off before. The refusal
// is ErrKickKeyUnavailable (503, retry) rather than ErrKickSignature (401),
// because the delivery was not found to be wrong.
//
// After the signature, and only then, the delivery must be recent and new: a
// timestamp more than kickMaxSkew from now is refused, and a Message-Id already
// accepted is ErrKickDuplicate. Doing these after the signature means a forger
// cannot fill the replay ring with ids of their choosing.
func KickVerifier(f *KickKeyFetcher) func(r *http.Request, body []byte) error {
	return kickVerifier(f, time.Now)
}

func kickVerifier(f *KickKeyFetcher, now func() time.Time) func(r *http.Request, body []byte) error {
	guard := &kickReplayGuard{}
	return func(r *http.Request, body []byte) error {
		// A request without the three headers is not a Kick delivery, whatever
		// the key is. Turned away before the key is wanted, so a stranger
		// probing the URL gets 401 (not a 503 that reads as an outage) and costs
		// no fetch.
		if r.Header.Get(KickHeaderMessageID) == "" || r.Header.Get(KickHeaderTimestamp) == "" || r.Header.Get(KickHeaderSignature) == "" {
			return fmt.Errorf("%w: missing one of %s, %s, %s",
				ErrKickSignature, KickHeaderMessageID, KickHeaderTimestamp, KickHeaderSignature)
		}

		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()

		key, err := f.Key(ctx)
		if err != nil {
			return fmt.Errorf("%w: %v", ErrKickKeyUnavailable, err)
		}
		if err := VerifyKickSignature(key, r, body); err != nil {
			// A rotated key fails every genuine delivery the same way a forgery
			// fails. Refetch once (rate-limited) and judge again before saying no.
			fresh, rerr := f.Refresh(ctx, key)
			if rerr != nil {
				return fmt.Errorf("%w: %v", ErrKickKeyUnavailable, rerr)
			}
			if fresh == key {
				return err
			}
			if err := VerifyKickSignature(fresh, r, body); err != nil {
				return err
			}
		}

		ts, perr := time.Parse(time.RFC3339, r.Header.Get(KickHeaderTimestamp))
		if perr != nil {
			return fmt.Errorf("%w: timestamp is not RFC 3339: %v", ErrKickSignature, perr)
		}
		if skew := now().Sub(ts); skew > kickMaxSkew || skew < -kickMaxSkew {
			return fmt.Errorf("%w: timestamp %s is outside the %s window", ErrKickStale, ts.Format(time.RFC3339), kickMaxSkew)
		}
		if !guard.firstSight(r.Header.Get(KickHeaderMessageID)) {
			return ErrKickDuplicate
		}
		return nil
	}
}
