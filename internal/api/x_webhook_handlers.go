package api

import (
	"io"
	"math"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/rainmanjam/polyemesis/internal/auth"
	"github.com/rainmanjam/polyemesis/internal/chat"
)

// xFailLimiter limits FAILED signatures per client address, and nothing else. It is deliberately not auth.Throttle:
// that one shares a global budget across every address and is spent before the credential is checked, so one
// address alternating bad requests could starve X's own deliveries. Here a failure is counted only after the HMAC
// has said no, a valid request never consults the limiter, and there is no shared budget to drain.
type xFailLimiter struct {
	mu  sync.Mutex
	now func() time.Time
	m   map[string]*xFailCount
}

type xFailCount struct {
	n     int
	since time.Time
}

// xNotLiveGrace bounds how long an X adapter that is not registered keeps being answered 503. Past it (or at once,
// after a Fatal Run) the route answers 200 and drops, so X stops retrying for chat that is not coming. A variable so
// a test can shorten it.
var xNotLiveGrace = 10 * time.Minute

const (
	// xFailBurst failures are answered 401 per address per window; the next ones are answered 429.
	xFailBurst  = 20
	xFailWindow = time.Minute
	// xFailMaxAddrs bounds the table.
	xFailMaxAddrs = 4096
)

func newXFailLimiter() *xFailLimiter {
	return &xFailLimiter{now: time.Now, m: map[string]*xFailCount{}}
}

// fail records one rejected signature from ip and returns how long to ask the caller to wait, or 0 while the
// address is still within its allowance.
func (l *xFailLimiter) fail(ip string) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	key := auth.PeerKey(ip)
	now := l.now()
	c := l.m[key]
	if c == nil || now.Sub(c.since) >= xFailWindow {
		if len(l.m) >= xFailMaxAddrs {
			for k, v := range l.m {
				if now.Sub(v.since) >= xFailWindow {
					delete(l.m, k)
				}
			}
			if len(l.m) >= xFailMaxAddrs {
				l.m = map[string]*xFailCount{} // an address pool bigger than the table: start over rather than grow
			}
		}
		c = &xFailCount{since: now}
		l.m[key] = c
	}
	c.n++
	if c.n <= xFailBurst {
		return 0
	}
	return xFailWindow - now.Sub(c.since)
}

// handleXWebhookCRC answers X's challenge. A missing, empty, or unsignable crc_token is 400; no saved client secret
// is 503, because there is nothing to answer with. It is never throttled: it holds nothing worth guarding (see
// xCRCTokenOK) and X must always get its answer.
func (s *Server) handleXWebhookCRC(w http.ResponseWriter, r *http.Request) {
	tok := r.URL.Query().Get("crc_token")
	if !xCRCTokenOK(tok) {
		writeError(w, http.StatusBadRequest, "crc_token is missing or not acceptable")
		return
	}
	secret := s.xClientSecret()
	if secret == "" {
		writeError(w, http.StatusServiceUnavailable, "no X client secret is saved, so the check cannot be answered; add it in Settings → Platforms")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]string{"response_token": xHMAC(secret, []byte(tok))})
}

// xIngestStatus is the answer for what became of a verified delivery. Only an accepted event or one already
// delivered is 200. An event that could not be taken (the adapter is not live yet, or its inbox is full) is 503 with
// Retry-After, so X can send it again: docs.x.com/x-api/webhooks/introduction lists "Retry and recovery support"
// (and a replay endpoint, POST /2/webhooks/replay) but does not state the schedule or which statuses it retries. If
// X does not retry a 503, nothing is lost that a 200 would have kept, since a 200 here would drop the event too.
func xIngestStatus(res chat.XIngest) int {
	switch res {
	case chat.XIngestNotRunning, chat.XIngestFull:
		return http.StatusServiceUnavailable
	case chat.XIngestMalformed:
		return http.StatusBadRequest
	}
	return http.StatusOK // queued, duplicate, ignored type, or another user's event
}

// handleXWebhookEvent receives one delivery. The order matters: size, then signature over the raw bytes, and only
// then is anything parsed. A verified body is queued without blocking. Nothing here answers 429 to a validly signed
// request: the per-address limit applies to failed signatures only, after the HMAC.
func (s *Server) handleXWebhookEvent(w http.ResponseWriter, r *http.Request) {
	ip := auth.ClientIP(r, s.proxies)
	secret := s.xClientSecret()
	if secret == "" {
		// 401, not 503: with no key there is nothing a delivery could be verified against, which is the same
		// refusal as a bad signature.
		writeError(w, http.StatusUnauthorized, "signature rejected: no X client secret is saved to verify it against")
		return
	}
	// One byte past the cap tells "exactly at the cap" from "over it"; a plain LimitReader would truncate silently
	// and surface as a signature failure.
	body, err := io.ReadAll(io.LimitReader(r.Body, xWebhookMaxBody+1))
	if err != nil {
		writeError(w, http.StatusBadRequest, "could not read the request body")
		return
	}
	if len(body) > xWebhookMaxBody {
		writeError(w, http.StatusRequestEntityTooLarge, "request body too large")
		return
	}
	got := r.Header.Get(xSignatureHeader)
	if got == "" || !secretEqual(got, xHMAC(secret, body)) {
		if wait := s.xhooks.fail(ip); wait > 0 {
			w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(wait.Seconds()))))
			writeError(w, http.StatusTooManyRequests, "too many rejected requests from this address")
			return
		}
		writeError(w, http.StatusUnauthorized, "signature rejected")
		return
	}

	if s.chat == nil {
		w.WriteHeader(http.StatusOK) // 200 so X does not retry events a chat-less server will never want
		return
	}
	user := chat.XWebhookUser(body)
	if user == "" {
		writeError(w, http.StatusBadRequest, "not an X activity event")
		return
	}
	xa := s.xAdapterFor(user)
	if xa == nil {
		w.WriteHeader(http.StatusOK) // no account of this install: not ours to keep
		return
	}
	res := xa.IngestWebhook(body)
	if res == chat.XIngestNotRunning && xa.Unavailable(xNotLiveGrace) {
		// Terminal (a Fatal Run) or not live for longer than the grace: no live adapter is coming, so a 503 would only
		// make X retry for nothing. The status the operator sees already says why.
		s.log.Warn("x webhook: chat is not running for this account and is not expected to; event dropped", "user", user)
		w.WriteHeader(http.StatusOK)
		return
	}
	switch code := xIngestStatus(res); code {
	case http.StatusServiceUnavailable:
		s.log.Warn("x webhook: event not taken, asking X to redeliver", "user", user, "result", res.String())
		w.Header().Set("Retry-After", "5")
		writeError(w, code, "chat is not ready to take this event; try again")
	case http.StatusBadRequest:
		writeError(w, code, "not an X activity event")
	default:
		w.WriteHeader(http.StatusOK)
	}
}
