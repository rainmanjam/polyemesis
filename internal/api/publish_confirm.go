package api

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"sync"
	"time"
)

// confirmTTL is how long a reviewed form stays confirmable. Long enough to read
// a privacy note and fix a typo; short enough that a forgotten tab cannot
// publish an hour later.
const confirmTTL = 10 * time.Minute

// confirmMax bounds the book, so a client that only ever prepares cannot grow
// the server's memory.
const confirmMax = 256

type confirmEntry struct {
	user    int64
	hash    string
	expires time.Time
	seq     int64
}

// confirmBook issues and redeems the per-request confirmation tokens the
// publish route demands.
//
// A token is minted by POST /publish/prepare for ONE reviewed form (the hash
// covers the clip, platform, account, title, description, tags and privacy) and
// ONE person (the signed-in user), is good once, and dies after confirmTTL.
// Redeeming it with different content, or by someone else, burns it: there is no
// way to probe it with guesses.
//
// It lives in memory on purpose. A restart invalidates every token, which costs
// the person one more look at the form; persisting them would turn a click into
// a credential that outlives the process that saw it.
type confirmBook struct {
	mu  sync.Mutex
	now func() time.Time
	m   map[string]confirmEntry
	seq int64
}

func newConfirmBook(now func() time.Time) *confirmBook {
	return &confirmBook{now: now, m: map[string]confirmEntry{}}
}

func (b *confirmBook) issue(user int64, hash string) (string, time.Time) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		panic("crypto/rand failed: " + err.Error())
	}
	tok := base64.RawURLEncoding.EncodeToString(raw)
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	for k, e := range b.m {
		if !now.Before(e.expires) {
			delete(b.m, k)
		}
	}
	for len(b.m) >= confirmMax {
		var oldest string
		var oldSeq int64 = -1
		for k, e := range b.m {
			if oldSeq < 0 || e.seq < oldSeq {
				oldest, oldSeq = k, e.seq
			}
		}
		delete(b.m, oldest)
	}
	b.seq++
	exp := now.Add(confirmTTL)
	b.m[tok] = confirmEntry{user: user, hash: hash, expires: exp, seq: b.seq}
	return tok, exp
}

var errBadConfirm = errors.New("that confirmation is not valid for what is being published; review the form and click Publish again")

// redeem consumes tok. It succeeds only for the person and content it was issued for.
func (b *confirmBook) redeem(tok string, user int64, hash string) error {
	if tok == "" {
		return errBadConfirm
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	e, ok := b.m[tok]
	if !ok {
		return errBadConfirm
	}
	delete(b.m, tok) // burnt whatever happens next
	if !b.now().Before(e.expires) || e.user != user || e.hash != hash {
		return errBadConfirm
	}
	return nil
}
