package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/rainmanjam/polyemesis/internal/eventstream"
)

// eventsHeartbeat is how often an idle stream sends a comment line. Under the
// 60-second read timeout nginx and most load balancers default to, so a quiet
// install's stream is not cut by a proxy; and it is the tick the revocation
// checks ride on, so it is also the bound on how long a revoked credential's
// stream survives.
const eventsHeartbeat = 15 * time.Second

// eventsRetryMS is the reconnect delay the stream asks EventSource for.
const eventsRetryMS = 3000

// heartbeatEvery is eventsHeartbeat, or the test override /ws already uses.
func (s *Server) heartbeatEvery() time.Duration {
	s.revokedMu.RLock()
	d := s.wsPingEvery
	s.revokedMu.RUnlock()
	if d > 0 {
		return d
	}
	return eventsHeartbeat
}

// handleEvents serves GET /api/v1/events: the real-time events API, as
// Server-Sent Events.
//
// AUTH IS THE ROUTE GROUP'S, NOT THIS HANDLER'S: requireAuth and requireScope
// have run before it, so a session cookie (what EventSource sends) and a bearer
// token (what curl, Node and Python send) are both accepted and nothing else
// is. THERE IS NO TOKEN QUERY PARAMETER and there must never be one: a URL
// carrying a credential ends up in proxy logs, browser history and the
// Referer header, and the browser case it would serve is the cookie's.
//
// What the principal changes is what it is sent: a read-scoped token is
// withheld every type the catalogue marks Withheld (chat), decided per event
// by the hub over a closed table that fails closed.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	if s.stream == nil {
		writeError(w, http.StatusServiceUnavailable, "the events stream is not available in this build")
		return
	}
	// CROSS-ORIGIN READS ARE REFUSED. No Access-Control-Allow-Origin is ever
	// set here, so a browser would not let another site read the body anyway;
	// refusing the request outright as well means another site cannot even
	// hold a stream open on the operator's cookie. Same rule as /ws's
	// CheckOrigin. A request with no Origin is not a browser's cross-site
	// request and is allowed.
	if origin := r.Header.Get("Origin"); origin != "" && !sameHost(origin, r.Host) {
		writeError(w, http.StatusForbidden, "cross-origin event streams are not allowed")
		return
	}
	topics, err := parseTopics(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	filter := eventstream.Filter{Topics: topics, ReadScoped: isReadScopedToken(r)}

	// The principal, captured now for the revocation checks on the heartbeat
	// -- the same two levers handleWS answers to (#159), for the same reason:
	// revoking a token, or changing the password, must end a stream that is
	// already open, not only the next request.
	var tokenID, sessionUser, sessionEpoch int64
	if p, ok := principalFrom(r.Context()); ok {
		if p.token != nil {
			tokenID = p.token.ID
		} else {
			sessionUser, sessionEpoch = p.userID, p.epoch
		}
	}
	s.serveEventStream(w, r, filter, streamGuard{
		every: s.heartbeatEvery(),
		valid: func() bool {
			if s.tokenRevoked(tokenID) {
				return false
			}
			return sessionUser == 0 || !s.sessionEpochChanged(sessionUser, sessionEpoch)
		},
	})
}

// streamGuard is how an open stream learns its credential is gone.
type streamGuard struct {
	// valid is asked every `every`; false ends the stream.
	valid func() bool
	every time.Duration
	// kicked, when closed, ends the stream at once: rotation or revocation
	// told us directly rather than waiting for the next check.
	kicked <-chan struct{}
}

// serveEventStream is the SSE half shared by GET /events and the overlay's
// display stream: subscribe, replay, then relay until the client leaves, falls
// behind, or the guard says the credential is gone.
func (s *Server) serveEventStream(w http.ResponseWriter, r *http.Request, filter eventstream.Filter, guard streamGuard) {
	// EventSource resends the last id it saw in this header on reconnect. The
	// query form is for a client that cannot set headers on its first
	// connection; it is an event id, not a credential.
	lastID := strings.TrimSpace(r.Header.Get("Last-Event-ID"))
	if lastID == "" {
		lastID = strings.TrimSpace(r.URL.Query().Get("lastEventId"))
	}

	rc := http.NewResponseController(w)
	h := w.Header()
	h.Set("Content-Type", "text/event-stream; charset=utf-8")
	// no-transform as well as no-cache: a compressing proxy that buffers to
	// compress delivers nothing until its buffer fills.
	h.Set("Cache-Control", "no-cache, no-transform")
	// nginx buffers proxied responses by default and honours this header to
	// stop; docs/EVENTS.md also gives the proxy_buffering line for proxies
	// that do not.
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	sub, replay, gap := s.stream.Subscribe(filter, lastID)
	defer sub.Close()

	write := func(b []byte) bool {
		// A deadline per write, because the server has no WriteTimeout (it
		// would end every stream) and a client that stops reading must not
		// hold this goroutine forever.
		_ = rc.SetWriteDeadline(time.Now().Add(writeWait))
		if _, err := w.Write(b); err != nil {
			return false
		}
		if err := rc.Flush(); err != nil && !errors.Is(err, http.ErrNotSupported) {
			return false
		}
		return true
	}
	if !write([]byte(fmt.Sprintf("retry: %d\n: polyemesis events\n\n", eventsRetryMS))) {
		return
	}
	if gap && !write(frame(s.stream.Gap(lastID))) {
		return
	}
	for _, env := range replay {
		if !write(frame(env)) {
			return
		}
	}

	beat := time.NewTicker(s.heartbeatEvery())
	defer beat.Stop()
	every := guard.every
	if every <= 0 {
		every = s.heartbeatEvery()
	}
	check := time.NewTicker(every)
	defer check.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-guard.kicked:
			return
		case env, ok := <-sub.C:
			if !ok {
				// The hub closed it: this client fell too far behind. Ending
				// the response is the whole of the remedy -- EventSource
				// reconnects with Last-Event-ID and replays the gap from the
				// ring. Logged, because an operator whose dashboard keeps
				// reconnecting should be able to find out why.
				if sub.Overflowed() {
					s.log.Info("events stream: disconnected a client that fell behind",
						"buffer", eventstream.DefaultClientBuffer)
				}
				return
			}
			if !write(frame(env)) {
				return
			}
		case <-check.C:
			if guard.valid != nil && !guard.valid() {
				return
			}
		case <-beat.C:
			if guard.valid != nil && !guard.valid() {
				return
			}
			if !write([]byte(": heartbeat\n\n")) {
				return
			}
		}
	}
}

// frame renders one event in SSE wire form. JSON from encoding/json contains no
// raw newline, so the payload is always exactly one data line.
func frame(env eventstream.Envelope) []byte {
	body, err := json.Marshal(env)
	if err != nil {
		// Unencodable (a NaN in a payload). Sent as a comment rather than
		// dropped silently or allowed to end the stream -- the lesson
		// writeEvent learned on /ws.
		return []byte(": an event of type " + string(env.Type) + " could not be encoded\n\n")
	}
	var b strings.Builder
	if env.ID != "" {
		b.WriteString("id: ")
		b.WriteString(env.ID)
		b.WriteByte('\n')
	}
	b.WriteString("event: ")
	b.WriteString(string(env.Type))
	b.WriteString("\ndata: ")
	b.Write(body)
	b.WriteString("\n\n")
	return []byte(b.String())
}

// parseTopics reads ?topics=a,b (or repeated). Unknown names are a 400 that
// lists the valid ones: a typo that silently subscribed to nothing would look
// exactly like a quiet install.
func parseTopics(r *http.Request) ([]eventstream.Topic, error) {
	valid := map[eventstream.Topic]bool{}
	var names []string
	for _, t := range eventstream.Topics() {
		valid[t] = true
		names = append(names, string(t))
	}
	var out []eventstream.Topic
	for _, raw := range r.URL.Query()["topics"] {
		for _, part := range strings.Split(raw, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			t := eventstream.Topic(part)
			if !valid[t] {
				sort.Strings(names)
				return nil, fmt.Errorf("unknown topic %q; topics are %s", part, strings.Join(names, ", "))
			}
			out = append(out, t)
		}
	}
	return out, nil
}
