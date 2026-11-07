package chat

import (
	"errors"
	"math/rand/v2"
	"sync"
	"time"
)

// helixLimit remembers that Twitch answered 429, so the calls that follow
// inside its stated window are refused locally rather than sent to fail the
// same way. Twitch's 429 carries Ratelimit-Reset (an epoch), not Retry-After.
type helixLimit struct {
	mu    sync.Mutex
	until time.Time
	// why is Twitch's own words from the 429 that started the window, repeated on
	// the calls refused locally so their errors still carry the real reason.
	why string
}

// maxLocalBackoff caps the local hold-off, so a far-off reset cannot silence
// moderation for the whole of it; the next call after the cap simply tries.
const maxLocalBackoff = 30 * time.Second

func (h *helixLimit) note(err error) {
	var ae *apiError
	if !errors.As(err, &ae) {
		return
	}
	d := ae.RetryAfter
	if d <= 0 {
		d = 2 * time.Second
	}
	d = min(d, maxLocalBackoff)
	// Jitter, so several accounts rate-limited together do not all resume in the
	// same instant.
	d += time.Duration(rand.Int64N(int64(500 * time.Millisecond)))
	h.mu.Lock()
	h.until = time.Now().Add(d)
	h.why = ae.Message
	h.mu.Unlock()
}

// wait is how long calls are still held off, and Twitch's reason for it.
func (h *helixLimit) wait() (time.Duration, string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return max(0, time.Until(h.until)), h.why
}
