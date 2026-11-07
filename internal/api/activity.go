package api

import (
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/rainmanjam/polyemesis/internal/activity"
	"github.com/rainmanjam/polyemesis/internal/activityhub"
	"github.com/rainmanjam/polyemesis/internal/overlay"
)

// Viewer activity (internal/activity): the console's feed, the health of each
// platform's source, and the "Send test alert" button.
//
// NAMED "activity", NOT "alerts": GET /alerts-style paths and the `alert` event
// type are the operator's alerts (internal/alerts) and stay that.
//
// WHO SEES WHAT. An activity names viewers and carries text they wrote, so it
// is chat content: both GETs are on the readDenied flag, like /chat/messages.
// The two POSTs are admin by the method rule -- a test alert reaches the
// overlay, which is on air, and a replay does too.

const activityUnavailable = "activity is not available: this server was started without the activity hub"

const (
	activityDefaultLimit = 200
	// activityTestBurst and activityTestWindow are the "Send test alert" limit:
	// two a second. The button reaches the overlay, which is on air, and a
	// held-down key should not machine-gun it.
	activityTestBurst  = 2
	activityTestWindow = time.Second
)

// activityTestLimiter is a sliding window: at most activityTestBurst in
// activityTestWindow.
type activityTestLimiter struct {
	mu    sync.Mutex
	stamp []time.Time
}

func (l *activityTestLimiter) allow(now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	cut := now.Add(-activityTestWindow)
	kept := l.stamp[:0]
	for _, t := range l.stamp {
		if t.After(cut) {
			kept = append(kept, t)
		}
	}
	l.stamp = kept
	if len(l.stamp) >= activityTestBurst {
		return false
	}
	l.stamp = append(l.stamp, now)
	return true
}

func (s *Server) requireActivity(w http.ResponseWriter) bool {
	if s.activity == nil {
		writeError(w, http.StatusServiceUnavailable, activityUnavailable)
		return false
	}
	return true
}

// activityKindInfo describes one kind to the console: whether an overlay can
// ever draw it, so the settings page can grey the rest.
type activityKindInfo struct {
	Kind    activity.Kind `json:"kind"`
	Overlay bool          `json:"overlay"`
}

func activityKinds() []activityKindInfo {
	out := make([]activityKindInfo, 0, len(activity.AllKinds()))
	overlayKinds := map[activity.Kind]bool{}
	for _, k := range activity.OverlayKinds() {
		overlayKinds[k] = true
	}
	for _, k := range activity.AllKinds() {
		out = append(out, activityKindInfo{Kind: k, Overlay: overlayKinds[k]})
	}
	return out
}

// activityFeed is GET /activity.
type activityFeed struct {
	Enabled bool             `json:"enabled"`
	Events  []activity.Event `json:"events"`
}

// handleActivity is GET /api/v1/activity: the in-memory feed, newest first, for
// the console to seed itself before it goes live on /ws. A restart empties it.
func (s *Server) handleActivity(w http.ResponseWriter, r *http.Request) {
	if !s.requireActivity(w) {
		return
	}
	q := r.URL.Query()
	limit := activityDefaultLimit
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			writeError(w, http.StatusBadRequest, "limit must be a positive whole number")
			return
		}
		limit = n
	}
	var kind activity.Kind
	if v := q.Get("kind"); v != "" {
		k, ok := activity.ParseKind(v)
		if !ok {
			writeError(w, http.StatusBadRequest, "unknown kind "+strconv.Quote(v))
			return
		}
		kind = k
	}
	platform := q.Get("platform")
	if platform != "" && overlay.ValidPlatforms([]string{platform}) != nil {
		writeError(w, http.StatusBadRequest, "unknown platform "+strconv.Quote(platform))
		return
	}
	writeJSON(w, http.StatusOK, activityFeed{
		Enabled: s.activity.Settings().Enabled,
		Events:  s.activity.Recent(limit, platform, kind),
	})
}

// activitySources is GET /activity/sources.
type activitySources struct {
	Enabled bool `json:"enabled"`
	// Kinds is the whole vocabulary, so the settings page lists every row.
	Kinds []activityKindInfo `json:"kinds"`
	// Platforms are the platforms activity can come from, in the overlay's order.
	Platforms []string                   `json:"platforms"`
	Sources   []activityhub.SourceStatus `json:"sources"`
	Stats     activityhub.Stats          `json:"stats"`
	// Overlay is the alerts gate's counters, absent without one.
	Overlay *overlay.ActivityStats `json:"overlay,omitempty"`
}

// handleActivitySources is GET /api/v1/activity/sources: per platform account,
// whether its activity is flowing and, kind by kind, which are on, unavailable
// to this account (not an Affiliate, not monetised) or need a reconnect.
func (s *Server) handleActivitySources(w http.ResponseWriter, r *http.Request) {
	if !s.requireActivity(w) {
		return
	}
	out := activitySources{
		Enabled:   s.activity.Settings().Enabled,
		Kinds:     activityKinds(),
		Platforms: overlay.Platforms,
		Sources:   s.activity.Statuses(),
		Stats:     s.activity.Stats(),
	}
	if s.activityGate != nil {
		st := s.activityGate.Stats()
		out.Overlay = &st
	}
	writeJSON(w, http.StatusOK, out)
}

// activityTestRequest is the body of POST /activity/test.
type activityTestRequest struct {
	Kind      string             `json:"kind"`
	Platform  string             `json:"platform"`
	Overrides activity.Overrides `json:"overrides"`
}

// activityTestResult is its answer. Accepted false is a 200: the button worked
// and the settings said no, which the console words for the operator.
type activityTestResult struct {
	Accepted bool           `json:"accepted"`
	Reason   string         `json:"reason,omitempty"`
	Detail   string         `json:"detail,omitempty"`
	Event    activity.Event `json:"event"`
}

// handleActivityTest is POST /api/v1/activity/test: a canned event of any kind,
// sent through the hub like a real one -- the settings filter, the overlay
// gate, /ws, the events stream and opt-in hooks -- so a streamer with no
// Affiliate status, or no account connected at all, can style the overlay.
// Admin, two a second.
func (s *Server) handleActivityTest(w http.ResponseWriter, r *http.Request) {
	if !s.requireActivity(w) {
		return
	}
	var req activityTestRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	kind, ok := activity.ParseKind(req.Kind)
	if !ok {
		writeError(w, http.StatusBadRequest, "unknown kind "+strconv.Quote(req.Kind))
		return
	}
	if req.Platform != "" && overlay.ValidPlatforms([]string{req.Platform}) != nil {
		writeError(w, http.StatusBadRequest, "unknown platform "+strconv.Quote(req.Platform))
		return
	}
	if !s.activityTests.allow(time.Now()) {
		w.Header().Set("Retry-After", "1")
		writeErrorCode(w, http.StatusTooManyRequests, "rate_limited", "test alerts are limited to two a second")
		return
	}
	ev := activity.Sample(kind, req.Platform, req.Overrides, time.Now())
	res := s.activity.Submit(ev)
	if res.Reason == activityhub.ReasonInvalid {
		// The overrides made an event the system refuses (a negative amount,
		// a currency that is not a code). The caller's mistake.
		writeError(w, http.StatusBadRequest, res.Detail)
		return
	}
	writeJSON(w, http.StatusOK, activityTestResult{
		Accepted: res.Accepted, Reason: res.Reason, Detail: res.Detail, Event: res.Event,
	})
}

// handleActivityReplay is POST /api/v1/activity/{id}/replay: draw a remembered
// event on the overlay again. Admin. 404 when the hub no longer holds it, or
// when no overlay could ever draw it (a moderation action).
func (s *Server) handleActivityReplay(w http.ResponseWriter, r *http.Request) {
	if !s.requireActivity(w) {
		return
	}
	// The same limit as Send test alert: a replay reaches the overlay, which is
	// on air, and an id in the URL is no reason to allow a machine gun.
	if !s.activityTests.allow(time.Now()) {
		w.Header().Set("Retry-After", "1")
		writeErrorCode(w, http.StatusTooManyRequests, "rate_limited", "replays are limited to two a second")
		return
	}
	if !s.activity.Replay(chi.URLParam(r, "id")) {
		writeError(w, http.StatusNotFound, "that activity is no longer held, or is not one an overlay shows")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"replayed": true})
}
