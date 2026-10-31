package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/eventstream"
	"github.com/rainmanjam/polyemesis/internal/overlay"
	"github.com/rainmanjam/polyemesis/internal/web"
)

// Chat overlays: OBS browser sources and docks that show chat on stream.
//
// THE OVERLAY KEY IS THE NARROWEST CREDENTIAL IN THE PRODUCT, and its whole
// security argument is about where it is NOT accepted:
//
//   - It is not an API token. It lives in its own table with its own prefix
//     (pov_), and requireAuth only ever looks up pmk_ bearers, so on every
//     route behind requireAuth -- the REST API, /ws, /events -- an overlay key
//     is exactly as good as no credential at all.
//   - It is accepted by exactly two handlers, both registered OUTSIDE every
//     auth group: the overlay's config and its display stream. Nothing else
//     reads OverlayKeyHeader.
//   - What those two return is the display projection (eventstream.DisplayLine
//     via the overlay filter) and the overlay's own style. A leaked key shows
//     its holder the public chat the overlay was already putting on stream,
//     after automod, without author ids -- and nothing else.
//
// It travels to these handlers in a header, never a query string. The OBS URL
// carries it in the FRAGMENT (#key=...), which a browser never sends to a
// server, never writes into a Referer and no proxy logs; the overlay page reads
// it there and sends it as OverlayKeyHeader.
//
// Some OBS builds drop the fragment, so the same pages are also served at
// OverlayPath+OverlayKeyPath+"{key}" and its dock twin. The handler never
// reads {key} -- the page reads it from location.pathname and sends the same
// header, so the credential check above is unchanged. What the path form
// gives up is the proxy: a path reaches the server, and a reverse proxy in
// front of it logs paths. Inside polyemesis it is covered twice: a matched
// request logs its route PATTERN ("/overlay/k/{key}", see loggedPath), and
// loggedPath scrubs pov_ keys from any raw path it does log. Referrer-Policy:
// no-referrer keeps it out of a Referer. The settings page offers the
// fragment form first for the proxy's sake.

// OverlayKeyHeader is the one place an overlay key is read from.
const OverlayKeyHeader = "X-Overlay-Key"

// OverlayPath is the overlay page; OverlayPath+"/dock" is the dock.
const OverlayPath = "/overlay"

// OverlayKeyPath is the path-key form's segment: OverlayPath+OverlayKeyPath+key
// and OverlayPath+"/dock"+OverlayKeyPath+key serve the same pages as the
// fragment URLs, for an OBS that drops the fragment. See the header above.
const OverlayKeyPath = "/k/"

// overlayRecheck is how often an open display stream re-reads its key from the
// store. Rotation and revocation end streams at once through overlayKicker;
// this is the net under that for any path that changes the row without
// telling it.
const overlayRecheck = 2 * time.Second

// overlayKicker ends open display streams when their key changes.
//
// It is also the one place that knows which display streams are open, so it
// answers "is OBS connected?" (app-council A98). seen is the last instant each
// overlay had a stream open: stamped when one opens and again when one closes,
// so a source that dropped five minutes ago reads as "last connected five
// minutes ago" rather than as one that never connected. In memory only: a
// restart forgets it, and every source reconnects within seconds anyway.
type overlayKicker struct {
	mu   sync.Mutex
	subs map[int64]map[chan struct{}]struct{}
	seen map[int64]time.Time
}

// status reports how many display streams one overlay has open, and when it
// last had one. Zero and the zero time for an overlay nothing ever opened.
func (k *overlayKicker) status(id int64) (int, time.Time) {
	k.mu.Lock()
	defer k.mu.Unlock()
	n := len(k.subs[id])
	if n > 0 {
		return n, time.Now()
	}
	return 0, k.seen[id]
}

// stampLocked records that id had a stream open just now. Caller holds k.mu.
func (k *overlayKicker) stampLocked(id int64) {
	if k.seen == nil {
		k.seen = map[int64]time.Time{}
	}
	k.seen[id] = time.Now()
}

func (k *overlayKicker) register(id int64) (<-chan struct{}, func()) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.subs == nil {
		k.subs = map[int64]map[chan struct{}]struct{}{}
	}
	if k.subs[id] == nil {
		k.subs[id] = map[chan struct{}]struct{}{}
	}
	ch := make(chan struct{})
	k.subs[id][ch] = struct{}{}
	k.stampLocked(id)
	return ch, func() {
		k.mu.Lock()
		defer k.mu.Unlock()
		if _, ok := k.subs[id][ch]; ok {
			delete(k.subs[id], ch)
			k.stampLocked(id)
		}
	}
}

// kick closes every open stream for one overlay.
func (k *overlayKicker) kick(id int64) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if len(k.subs[id]) > 0 {
		k.stampLocked(id)
	}
	for ch := range k.subs[id] {
		close(ch)
	}
	delete(k.subs, id)
}

// overlayKeyInPath matches an overlay key anywhere in a logged string.
var overlayKeyInPath = regexp.MustCompile(db.OverlayKeyPrefix + `[A-Za-z0-9_\-]+`)

// redactOverlayKeys scrubs overlay keys from a path about to be logged.
func redactOverlayKeys(p string) string {
	if !strings.Contains(p, db.OverlayKeyPrefix) {
		return p
	}
	return overlayKeyInPath.ReplaceAllString(p, db.OverlayKeyPrefix+"[redacted]")
}

// ------------------------------------------------------------ management

// overlayView is a stored overlay as the settings page sees it: the row, with
// its style parsed (so a row written by a newer build reads as the default
// rather than failing the list).
type overlayView struct {
	db.ChatOverlay
	Style overlay.Style `json:"style"`
	// Connected is how many display streams (an OBS browser source, a dock)
	// have this overlay open right now, and LastSeen when one last did
	// (app-council A98). Filled by the list only; create and update answer
	// with the row, where 0 and no time are simply not reported yet.
	Connected int       `json:"connected"`
	LastSeen  time.Time `json:"lastSeen,omitzero"`
}

func viewOverlay(o db.ChatOverlay) overlayView {
	st, err := overlay.ParseStyle(o.Style)
	if err != nil {
		st = overlay.DefaultStyle()
	}
	return overlayView{ChatOverlay: o, Style: st}
}

type overlayRequest struct {
	Name      string          `json:"name"`
	Platforms []string        `json:"platforms"`
	Style     json.RawMessage `json:"style"`
}

// validate checks a create/update body and returns the canonical style JSON.
func (req overlayRequest) validate() (json.RawMessage, error) {
	if err := overlay.ValidPlatforms(req.Platforms); err != nil {
		return nil, err
	}
	st, err := overlay.ParseStyle(req.Style)
	if err != nil {
		return nil, err
	}
	b, err := json.Marshal(st)
	return b, err
}

func (s *Server) handleListOverlays(w http.ResponseWriter, r *http.Request) {
	list, err := s.store.ListChatOverlays()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	views := make([]overlayView, 0, len(list))
	for _, o := range list {
		v := viewOverlay(o)
		v.Connected, v.LastSeen = s.overlayKicks.status(o.ID)
		views = append(views, v)
	}
	var stats overlay.Stats
	if s.overlayGate != nil {
		stats = s.overlayGate.Stats()
	}
	presets := map[string]overlay.Style{}
	for _, n := range overlay.PresetNames {
		presets[n] = overlay.PresetStyle(n)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"overlays":    views,
		"presets":     presets,
		"presetOrder": overlay.PresetNames,
		"platforms":   overlay.Platforms,
		"stats":       stats,
		"maxHoldMs":   overlay.MaxHold.Milliseconds(),
	})
}

func (s *Server) handleCreateOverlay(w http.ResponseWriter, r *http.Request) {
	var req overlayRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	style, err := req.validate()
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	o, err := s.store.CreateChatOverlay(req.Name, req.Platforms, style)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.log.Info("chat overlay created", "id", o.ID, "name", o.Name)
	writeJSON(w, http.StatusCreated, viewOverlay(*o))
}

func (s *Server) handleUpdateOverlay(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	var req overlayRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	style, err := req.validate()
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	o, err := s.store.UpdateChatOverlay(id, req.Name, req.Platforms, style)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			writeStoreError(w, err)
			return
		}
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	// A changed platform filter must apply to streams already open: the
	// filter is fixed at subscribe, so end them and let the page reconnect.
	s.overlayKicks.kick(id)
	writeJSON(w, http.StatusOK, viewOverlay(*o))
}

func (s *Server) handleRotateOverlayKey(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	o, err := s.store.RotateChatOverlayKey(id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	s.overlayKicks.kick(id)
	s.log.Info("chat overlay key rotated; the old URL no longer works", "id", id)
	writeJSON(w, http.StatusOK, viewOverlay(*o))
}

func (s *Server) handleRevokeOverlayKey(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	o, err := s.store.RevokeChatOverlayKey(id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	s.overlayKicks.kick(id)
	s.log.Info("chat overlay key revoked", "id", id)
	writeJSON(w, http.StatusOK, viewOverlay(*o))
}

func (s *Server) handleDeleteOverlay(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	if err := s.store.DeleteChatOverlay(id); err != nil {
		writeStoreError(w, err)
		return
	}
	s.overlayKicks.kick(id)
	s.log.Info("chat overlay deleted", "id", id)
	w.WriteHeader(http.StatusNoContent)
}

// ------------------------------------------------------------ the key's side

// overlayFromRequest resolves the overlay key, or writes 401. The same answer
// for a missing, malformed, revoked and unknown key, so the response says
// nothing about which.
func (s *Server) overlayFromRequest(w http.ResponseWriter, r *http.Request) (*db.ChatOverlay, bool) {
	// No caching anywhere between here and OBS, for a response about a
	// credential.
	w.Header().Set("Cache-Control", "no-store")
	if s.store == nil {
		writeError(w, http.StatusServiceUnavailable, "overlays are not available in this build")
		return nil, false
	}
	key := strings.TrimSpace(r.Header.Get(OverlayKeyHeader))
	o, err := s.store.ChatOverlayByKey(key)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "this overlay key is not valid; copy the overlay URL from Settings again")
		return nil, false
	}
	return o, true
}

// overlayConfig is everything the overlay page needs to draw itself. Built
// field by field: no key, no id, no timestamps.
type overlayConfig struct {
	Name      string        `json:"name"`
	Platforms []string      `json:"platforms"`
	Style     overlay.Style `json:"style"`
}

func (s *Server) handleOverlayConfig(w http.ResponseWriter, r *http.Request) {
	o, ok := s.overlayFromRequest(w, r)
	if !ok {
		return
	}
	v := viewOverlay(*o)
	writeJSON(w, http.StatusOK, overlayConfig{Name: v.Name, Platforms: v.Platforms, Style: v.Style})
}

// handleOverlayEvents is the display stream: the events hub, through the
// overlay filter, which admits chat.display and chat.undisplay for this
// overlay's platforms and nothing else whatever is asked for.
func (s *Server) handleOverlayEvents(w http.ResponseWriter, r *http.Request) {
	// The key first, so a caller without one learns nothing -- not even
	// whether this build has a stream to offer.
	o, ok := s.overlayFromRequest(w, r)
	if !ok {
		return
	}
	if s.stream == nil {
		writeError(w, http.StatusServiceUnavailable, "the events stream is not available in this build")
		return
	}
	if origin := r.Header.Get("Origin"); origin != "" && !sameHost(origin, r.Host) {
		writeError(w, http.StatusForbidden, "cross-origin event streams are not allowed")
		return
	}
	kicked, release := s.overlayKicks.register(o.ID)
	defer release()
	key := o.OBSKey
	id := o.ID
	s.serveEventStream(w, r, eventstream.Filter{
		Overlay: &eventstream.OverlayView{Platforms: o.Platforms},
	}, streamGuard{
		every:  overlayRecheck,
		kicked: kicked,
		valid: func() bool {
			cur, err := s.store.ChatOverlayByKey(key)
			return err == nil && cur.ID == id
		},
	})
}

// overlayPageHandler serves the SPA for the overlay and dock pages, which
// render without a session: the page reads its key from the URL fragment, or
// from the path on the path-key routes.
//
// index.html is served with its background made TRANSPARENT before the first
// paint. The shell paints the body dark inline (so the console never flashes
// white), and in an OBS browser source that dark would be the first frame on
// stream, every time the source is refreshed, until the bundle loaded and
// cleared it. color-scheme is reset for the same reason: a dark scheme gives
// the canvas an opaque default.
func (s *Server) overlayPageHandler() http.HandlerFunc {
	spa, err := web.Handler()
	if err != nil {
		s.log.Error("overlay: embedded UI unavailable", "err", err)
		return func(w http.ResponseWriter, r *http.Request) {
			writeError(w, http.StatusNotFound, "UI is not built")
		}
	}
	return func(w http.ResponseWriter, r *http.Request) {
		// Referrer-Policy: no-referrer is already set by securityHeaders on
		// every response; it is restated here so the overlay page's promise
		// does not depend on a middleware nobody reading this file can see.
		h := w.Header()
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cache-Control", "no-store")
		if page, ok := transparentIndex(); ok {
			// No Content-Type set here: net/http sniffs the doctype, the same
			// type the SPA handler's file server sends for this file.
			_, _ = w.Write(page)
			return
		}
		spa.ServeHTTP(w, r)
	}
}

// bodyStyle matches the shell's opening body tag, whatever its inline style.
var bodyStyle = regexp.MustCompile(`<body[^>]*>`)

// transparentIndex is the embedded index.html with a transparent page.
func transparentIndex() ([]byte, bool) {
	sub, err := web.FS()
	if err != nil {
		return nil, false
	}
	raw, err := fs.ReadFile(sub, "index.html")
	if err != nil || len(raw) == 0 {
		return nil, false
	}
	out := bodyStyle.ReplaceAll(raw, []byte(`<body style="background:transparent">`))
	out = bytes.Replace(out, []byte("<html"), []byte(`<html style="background:transparent;color-scheme:normal"`), 1)
	return out, true
}
