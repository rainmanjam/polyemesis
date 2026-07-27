package api

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rainmanjam/polyemesis/internal/auth"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/playout"
	"github.com/rainmanjam/polyemesis/internal/web"
)

// The public origin is the only part of polyemesis that answers to somebody who
// has never signed in, so the rules it follows are written out here rather than
// spread across the handlers that enforce them.
//
// A media request is served when, in order:
//
//  1. It is an authenticated administrator. That is what lets the Playout page
//     preview its own output while the stream is still private.
//  2. Playout is enabled AND Public is on AND the request proves it holds the
//     playback token — unless the operator has explicitly opened the stream.
//
// Anything else is refused, and the refusal is chosen to leak as little as
// possible: a stream that is not public answers 404, indistinguishable from one
// that does not exist, while a stream that is public but protected answers 401
// so a player can prompt for the credential it is missing.
//
// The default is PROTECTED. Turning a box into an origin the whole internet can
// pull from is a decision an operator makes on purpose, never one they arrive at
// by leaving a field alone.
const (
	// PlayoutPrefix is where the media lives. Not under /api/v1: these are not
	// API calls, they are the URLs a player and a CDN see.
	PlayoutPrefix = "/playout/"
	// WatchPath is the public player page.
	WatchPath = "/watch"
)

// playoutProtection is how an anonymous viewer proves they may watch.
type playoutProtection string

const (
	// PlayoutProtectToken requires the shared playback token, supplied in the
	// URL, in a header, or as an HTTP basic password. The default.
	PlayoutProtectToken playoutProtection = "token"
	// PlayoutProtectOpen serves anyone. Only ever reached by an operator who
	// asked for it in as many words.
	PlayoutProtectOpen playoutProtection = "open"
)

// playoutTokenCookie carries the token across the requests a player makes after
// the first one.
//
// This is the detail that makes protection work at all. A player is handed a
// playlist of RELATIVE segment URLs, so a token in the query string of the
// master playlist reaches exactly one request and nothing after it, and native
// HLS playback (Safari, a set-top box) cannot attach a header to any request.
// Trading the token for a cookie once, on the request that proved it, is what
// lets every later segment authorise itself.
const playoutTokenCookie = "polyemesis_playout"

// playoutTokenParam and playoutTokenHeader are the two ways to present the token
// on the first request. Basic auth is accepted as a third, for players and CDNs
// that only speak that.
const (
	playoutTokenParam  = "t"
	playoutTokenHeader = "X-Playout-Token"
)

// posterMaxAge is how long a rendered poster is reused.
//
// A poster is a still of a live stream, so it is stale the moment it is made;
// what matters is that it is recent, not current. Re-rendering on every request
// would let an unauthenticated page spawn an FFmpeg per hit, which is a far
// worse property than a frame that is a few seconds old.
const posterMaxAge = 10 * time.Second

// posterTimeout bounds the one-shot render. Reading a single frame off a local
// segment is milliseconds' work; anything approaching this is a wedged process,
// not a slow one.
const posterTimeout = 8 * time.Second

// ------------------------------------------------------------- publish config

// playoutPublish is the operator-facing half of the public origin: who may
// watch, and what the player page says about the stream.
//
// It is stored beside the database rather than inside db.PlayoutSettings only
// because this change does not own that file. It belongs there, and moving it
// is a schema addition plus a one-time read of this file; nothing outside this
// file depends on where it lives.
type playoutPublish struct {
	Protection playoutProtection `json:"protection"`
	// Token is the shared playback secret. Absent from every response except
	// the administrator's own, which is the only caller allowed to learn it.
	Token string `json:"token"`
	// Title and Description are shown on the player page. Free text, rendered
	// by React as text nodes, so no escaping question arises.
	Title       string `json:"title"`
	Description string `json:"description"`
}

// normalize fills in anything missing and refuses to leave the door open by
// accident: an unrecognised protection value becomes the protected one.
func (p *playoutPublish) normalize() (changed bool) {
	if p.Protection != PlayoutProtectOpen {
		if p.Protection != PlayoutProtectToken {
			p.Protection = PlayoutProtectToken
			changed = true
		}
	}
	if p.Token == "" {
		p.Token = newPlayoutToken()
		changed = true
	}
	if len(p.Title) > 200 {
		p.Title = p.Title[:200]
		changed = true
	}
	if len(p.Description) > 2000 {
		p.Description = p.Description[:2000]
		changed = true
	}
	return changed
}

// newPlayoutToken mints a playback secret. 32 bytes because this is a bearer
// credential that will sit in a URL somebody pastes into a chat window and then
// forgets about.
func newPlayoutToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand does not fail on any platform polyemesis runs on, and a
		// predictable token would be worse than no playout at all. The empty
		// string is refused by every check below, so playout stays shut.
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// playoutStore is the publish config on disk plus the caches that must not be
// rebuilt per request.
type playoutStore struct {
	path string

	mu     sync.Mutex
	cfg    *playoutPublish
	poster []byte
	// posterAt is when poster was rendered; zero means never.
	posterAt time.Time
	// posterErr is why the last render failed, kept so the endpoint can answer
	// consistently instead of re-running a command that is going to fail again
	// within the same cache window.
	posterErr error
}

// playoutStores keys one store per config path.
//
// Keyed by path rather than by *Server so two servers over one data directory
// share a cache — and, more usefully, so a test with its own temp directory is
// isolated from every other test without any teardown. In a running process
// there is exactly one entry.
var playoutStores sync.Map

func playoutStoreFor(path string) *playoutStore {
	if v, ok := playoutStores.Load(path); ok {
		return v.(*playoutStore)
	}
	v, _ := playoutStores.LoadOrStore(path, &playoutStore{path: path})
	return v.(*playoutStore)
}

// playoutConfigPath is the sidecar's location: beside polyemesis.db, in the one
// directory operators are told to back up.
func (s *Server) playoutConfigPath() string {
	return filepath.Join(s.cfg.DataDir, "playout.json")
}

func (s *Server) playoutStore() *playoutStore { return playoutStoreFor(s.playoutConfigPath()) }

// load returns the publish config, reading it from disk once and seeding a
// protected default the first time.
func (st *playoutStore) load() playoutPublish {
	st.mu.Lock()
	defer st.mu.Unlock()
	return *st.loadLocked()
}

func (st *playoutStore) loadLocked() *playoutPublish {
	if st.cfg != nil {
		return st.cfg
	}
	cfg := &playoutPublish{}
	if b, err := os.ReadFile(st.path); err == nil {
		// A corrupt file is not a reason to serve the stream to everyone, and
		// normalize below turns the zero value into the protected default.
		_ = json.Unmarshal(b, cfg)
	}
	if cfg.normalize() {
		_ = writePlayoutConfig(st.path, cfg)
	}
	st.cfg = cfg
	return cfg
}

// save replaces the config, normalising first so nothing unrepresentable is
// ever persisted.
func (st *playoutStore) save(cfg playoutPublish) (playoutPublish, error) {
	cfg.normalize()

	st.mu.Lock()
	defer st.mu.Unlock()
	if err := writePlayoutConfig(st.path, &cfg); err != nil {
		return cfg, err
	}
	st.cfg = &cfg
	return cfg, nil
}

// writePlayoutConfig writes through a temp file so a crash mid-write cannot
// leave a truncated config that the next start would read as "no token".
func writePlayoutConfig(path string, cfg *playoutPublish) error {
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	// 0600: this file holds a bearer credential.
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ------------------------------------------------------------------- plumbing

// playoutManager resolves the engine's playout manager.
//
// The nil check is not defensive habit: the API test server is built without an
// engine, and every playout route has to answer "unavailable" rather than
// panic when it is.
func (s *Server) playoutManager() *playout.Manager {
	if s.eng == nil {
		return nil
	}
	return s.eng.Playout()
}

// playoutSettings is the stored configuration, which is authoritative for the
// access decision even when no manager is running yet.
func (s *Server) playoutSettings() db.PlayoutSettings {
	if m := s.playoutManager(); m != nil {
		return m.Settings()
	}
	if st, err := s.store.GetSettings(); err == nil {
		return st.Playout
	}
	return db.PlayoutSettings{}
}

// ------------------------------------------------------------ access decision

// playoutAccess is why a media request was allowed, or how it must be refused.
type playoutAccess int

const (
	// playoutDenyHidden refuses without admitting anything exists.
	playoutDenyHidden playoutAccess = iota
	// playoutDenyChallenge refuses but names the credential that would work.
	playoutDenyChallenge
	// playoutAllowAdmin is a signed-in operator.
	playoutAllowAdmin
	// playoutAllowToken presented the playback token.
	playoutAllowToken
	// playoutAllowOpen is an origin the operator opened to everyone.
	playoutAllowOpen
)

func (a playoutAccess) ok() bool { return a >= playoutAllowAdmin }

// authorizePlayout decides whether one media request may be served.
func (s *Server) authorizePlayout(r *http.Request) playoutAccess {
	set := s.playoutSettings()

	// An administrator can always watch their own output, including while it is
	// private — otherwise the Playout page could not preview what it is about
	// to publish. Checked before Enabled so a disabled stream still 404s for
	// everyone rather than 401ing for an admin, which would be a confusing way
	// to say "playout is off".
	admin := s.authenticatedPlayout(r)

	if !set.Enabled {
		return playoutDenyHidden
	}
	if admin {
		return playoutAllowAdmin
	}
	if !set.Public {
		return playoutDenyHidden
	}

	cfg := s.playoutStore().load()
	if cfg.Protection == PlayoutProtectOpen {
		return playoutAllowOpen
	}
	if playoutTokenMatches(r, cfg.Token) {
		return playoutAllowToken
	}
	return playoutDenyChallenge
}

// authenticatedPlayout reports whether the request carries an administrator's
// credential. CSRF is not consulted: these are read-only GETs, and a cross-site
// forgery whose entire effect is that somebody else's browser fetches a video
// segment is not a threat worth a token exchange a player cannot perform.
func (s *Server) authenticatedPlayout(r *http.Request) bool {
	_, err := s.authenticate(r)
	return err == nil
}

// playoutTokenMatches checks every channel a token may arrive on.
//
// Constant-time throughout, and an empty configured token never matches, so a
// failure to mint one shuts the door rather than opening it.
func playoutTokenMatches(r *http.Request, want string) bool {
	if want == "" {
		return false
	}
	eq := func(got string) bool {
		return got != "" && subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
	}
	if eq(r.URL.Query().Get(playoutTokenParam)) {
		return true
	}
	if eq(strings.TrimSpace(r.Header.Get(playoutTokenHeader))) {
		return true
	}
	if c, err := r.Cookie(playoutTokenCookie); err == nil && eq(c.Value) {
		return true
	}
	// The username is ignored: there is one stream and one secret, and insisting
	// on a particular name would only be one more thing to get wrong when
	// pasting a URL into a set-top box.
	if _, pass, ok := r.BasicAuth(); ok && eq(pass) {
		return true
	}
	return false
}

// setPlayoutTokenCookie hands the player a credential its later requests can
// carry on their own. See playoutTokenCookie for why this is necessary.
func (s *Server) setPlayoutTokenCookie(w http.ResponseWriter, token string, crossOrigin bool) {
	secure := s.cfg.ServesTLS()
	sameSite := http.SameSiteLaxMode
	// A player embedded on another site makes cross-site requests, and a Lax
	// cookie is not sent on those — the embed would authorise its first request
	// and then 401 on every segment. None requires Secure, so on a plain-HTTP
	// box the cookie stays Lax and cross-site embedding falls back to the token
	// the player appends to each URL itself.
	if crossOrigin && secure {
		sameSite = http.SameSiteNoneMode
	}
	http.SetCookie(w, &http.Cookie{
		Name:  playoutTokenCookie,
		Value: token,
		// Scoped to the media prefix: the admin API never sees it.
		Path:     PlayoutPrefix,
		HttpOnly: true,
		Secure:   secure,
		SameSite: sameSite,
		MaxAge:   int((12 * time.Hour).Seconds()),
	})
}

// ------------------------------------------------------------- media handler

// playoutHandler serves the public origin: the access check above, then the
// manager's own file handler.
func (s *Server) playoutHandler() http.Handler {
	// Built once. The inner handler consults settings per request, so a
	// manager that is nil at route-build time is the only thing that has to be
	// re-checked here.
	var (
		once  sync.Once
		inner http.Handler
	)
	resolve := func() http.Handler {
		once.Do(func() {
			if m := s.playoutManager(); m != nil {
				inner = m.Handler(PlayoutPrefix)
			}
		})
		return inner
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A preflight carries no credentials by definition, so it is answered
		// before the access check. It reveals nothing: the browser still has to
		// make the real request, and that one is checked.
		if r.Method == http.MethodOptions {
			if h := resolve(); h != nil {
				h.ServeHTTP(w, r)
				return
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}

		switch access := s.authorizePlayout(r); access {
		case playoutAllowToken:
			cfg := s.playoutStore().load()
			s.setPlayoutTokenCookie(w, cfg.Token, s.playoutSettings().AllowCrossOrigin)
		case playoutDenyChallenge:
			// Basic is named because it is the only one of the three channels a
			// browser can satisfy on its own, which is what turns a bare
			// /playout/ URL pasted into an address bar into a password prompt
			// rather than a dead end.
			w.Header().Set("WWW-Authenticate", `Basic realm="polyemesis playout", charset="UTF-8"`)
			writeError(w, http.StatusUnauthorized, "this stream requires a playback token")
			return
		default:
			if !access.ok() {
				http.NotFound(w, r)
				return
			}
		}

		h := resolve()
		if h == nil {
			writeError(w, http.StatusServiceUnavailable, "playout is not running")
			return
		}
		h.ServeHTTP(w, r)
	})
}

// ------------------------------------------------------------- player page

// watchHandler serves the SPA for /watch, relaxing the frame-blocking headers
// when the operator has allowed embedding.
//
// The global security middleware sends X-Frame-Options: DENY and a CSP with
// frame-ancestors 'none', which is right for an admin console and fatal for a
// page whose entire purpose is to be put in an iframe on somebody's website.
// Both are rewritten here rather than weakened globally, so the admin UI keeps
// the strict policy and only this one page opts out — and only when
// AllowCrossOrigin says the operator meant to publish it that way.
func (s *Server) watchHandler() http.Handler {
	spa, err := web.Handler()
	if err != nil {
		s.log.Error("playout: embedded UI unavailable for /watch", "err", err)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			writeError(w, http.StatusNotFound, "UI is not built")
		})
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.playoutSettings().AllowCrossOrigin {
			h := w.Header()
			h.Del("X-Frame-Options")
			// Replaced wholesale rather than edited: the middleware already set
			// the strict policy, and Set overwrites it. Everything else is
			// copied from cspDirectives; only frame-ancestors differs.
			h.Set("Content-Security-Policy", watchCSP)
		}
		// index.html, never cached, so a redeploy does not strand a viewer on a
		// bundle whose asset hashes no longer exist.
		spa.ServeHTTP(w, r)
	})
}

// watchCSP is the admin policy with frame-ancestors opened up. Written as a
// derivation of cspDirectives so a directive added there is not silently
// missing here.
var watchCSP = func() string {
	out := make([]string, 0, len(cspDirectives))
	for _, d := range cspDirectives {
		if strings.HasPrefix(d, "frame-ancestors") {
			// The operator has already declared this origin embeddable by
			// turning AllowCrossOrigin on, which sends Access-Control-Allow-
			// Origin: * on the media itself. Restricting the frame to a list we
			// do not have would only break the embed while the media stayed
			// readable, which protects nobody.
			d = "frame-ancestors *"
		}
		out = append(out, d)
	}
	return strings.Join(out, "; ")
}()

// ----------------------------------------------------------------- poster

// handlePlayoutPoster returns a recent keyframe as JPEG.
//
// The frame is pulled from a segment already on disk, not from a new relay
// subscription: the packager has written keyframe-aligned segments regardless,
// so a poster costs one short-lived FFmpeg read of a local file and no port, no
// subscription and nothing on the destination path.
func (s *Server) handlePlayoutPoster(w http.ResponseWriter, r *http.Request) {
	if !s.authorizePlayout(r).ok() {
		// Deliberately no basic-auth challenge: a poster is decoration, and a
		// password prompt fired by an <img> tag on somebody's blog would be a
		// hostile thing to do. The player page prompts, the poster just 404s.
		http.NotFound(w, r)
		return
	}
	m := s.playoutManager()
	if m == nil || s.eng == nil {
		http.NotFound(w, r)
		return
	}
	ffmpegPath := ""
	if tools := s.eng.Tools(); tools != nil {
		ffmpegPath = tools.FFmpeg
	}

	jpeg, err := s.playoutStore().posterJPEG(m.Dir(), ffmpegPath, time.Now())
	if err != nil || len(jpeg) == 0 {
		// No segment on disk yet is the normal state of a stream nobody is
		// feeding, so this is a 404 rather than a 500 the operator would go
		// looking for a cause for.
		http.NotFound(w, r)
		return
	}

	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "public, max-age="+strconv.Itoa(int(posterMaxAge.Seconds())))
	if s.playoutSettings().AllowCrossOrigin {
		w.Header().Set("Access-Control-Allow-Origin", "*")
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(jpeg)))
	_, _ = w.Write(jpeg)
}

// posterJPEG returns the cached poster, rendering a new one when the cached one
// has aged out. Serialised so a burst of viewers produces one FFmpeg, not one
// each.
func (st *playoutStore) posterJPEG(dir, ffmpegPath string, now time.Time) ([]byte, error) {
	st.mu.Lock()
	defer st.mu.Unlock()

	if !st.posterAt.IsZero() && now.Sub(st.posterAt) < posterMaxAge {
		return st.poster, st.posterErr
	}

	seg := newestPlayoutSegment(dir)
	if seg == "" {
		st.poster, st.posterErr, st.posterAt = nil, nil, now
		return nil, nil
	}

	jpeg, err := renderPoster(ffmpegPath, seg)
	// The previous poster is kept on failure. A transient decode error on one
	// segment should not blank a page that was showing a perfectly good frame a
	// moment ago.
	if err != nil && len(st.poster) > 0 {
		st.posterAt = now
		return st.poster, nil
	}
	st.poster, st.posterErr, st.posterAt = jpeg, err, now
	return jpeg, err
}

// newestPlayoutSegment picks the segment to grab a frame from.
//
// MPEG-TS only, because independent_segments guarantees every one of them opens
// on a keyframe and a lone DASH .m4s is undecodable without its init segment.
// The most recent file is skipped when there is an older one to use: the newest
// segment is usually the one FFmpeg is still appending to, and half a segment
// decodes to a broken frame or to nothing.
func newestPlayoutSegment(dir string) string {
	type entry struct {
		path string
		mod  time.Time
	}
	var found []entry
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if !strings.EqualFold(filepath.Ext(d.Name()), ".ts") {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		found = append(found, entry{path: path, mod: info.ModTime()})
		return nil
	})
	if len(found) == 0 {
		return ""
	}
	sort.Slice(found, func(i, j int) bool {
		if !found[i].mod.Equal(found[j].mod) {
			return found[i].mod.After(found[j].mod)
		}
		return found[i].path > found[j].path
	})
	if len(found) > 1 {
		return found[1].path
	}
	return found[0].path
}

// renderPoster decodes one frame out of a segment and encodes it as JPEG on
// stdout.
func renderPoster(ffmpegPath, segment string) ([]byte, error) {
	if ffmpegPath == "" {
		ffmpegPath = "ffmpeg"
	}
	ctx, cancel := context.WithTimeout(context.Background(), posterTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, ffmpegPath,
		"-hide_banner", "-nostdin", "-loglevel", "error",
		"-i", segment,
		"-frames:v", "1",
		// Scaled down: this is a poster behind a play button, and a full-size
		// still off a 1080p ladder is a quarter-megabyte nobody looks at.
		"-vf", "scale=854:-2",
		"-q:v", "5",
		"-f", "image2",
		"-c:v", "mjpeg",
		"-",
	)
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ------------------------------------------------------------------ admin API

// playoutURLs are the addresses an operator copies out of the page.
type playoutURLs struct {
	// Master is the HLS ladder, relative to this origin.
	Master string `json:"master"`
	// Watch is the player page.
	Watch string `json:"watch"`
	// Embed is a ready-made iframe snippet.
	Embed string `json:"embed"`
}

// playoutAdminView is the Playout page's single read.
type playoutAdminView struct {
	Status playout.Status `json:"status"`
	// Settings is echoed so the page can render the variant editor without a
	// second call to /settings.
	Settings   db.PlayoutSettings `json:"settings"`
	Protection playoutProtection  `json:"protection"`
	// Token is the playback secret in the clear. This endpoint is behind the
	// administrator's session; there is exactly one person entitled to see it,
	// and hiding it from them would only mean they could not share the link.
	Token       string      `json:"token"`
	Title       string      `json:"title"`
	Description string      `json:"description"`
	URLs        playoutURLs `json:"urls"`
	// Exposed states plainly whether this stream is reachable by anyone on the
	// internet who has the URL and nothing else. The UI leads with it.
	Exposed bool `json:"exposed"`
	// Running reports whether the playout manager exists at all, so the page can
	// say "not wired up" rather than showing an empty ladder.
	Running bool `json:"running"`
}

func (s *Server) handleGetPlayout(w http.ResponseWriter, r *http.Request) {
	set := s.playoutSettings()
	cfg := s.playoutStore().load()

	view := playoutAdminView{
		Settings:    set,
		Protection:  cfg.Protection,
		Token:       cfg.Token,
		Title:       cfg.Title,
		Description: cfg.Description,
		Exposed:     set.Enabled && set.Public && cfg.Protection == PlayoutProtectOpen,
		URLs:        playoutURLsFor(r, set, cfg),
	}
	if m := s.playoutManager(); m != nil {
		view.Status = m.Status()
		view.Running = true
	} else {
		// Without a manager the settings are still real, so the page shows the
		// ladder it would run rather than nothing at all.
		view.Status = playout.Status{
			Enabled: set.Enabled,
			Public:  set.Public,
			Master:  playout.MasterPlaylist,
			Format:  set.Format,
		}
	}
	writeJSON(w, http.StatusOK, view)
}

// playoutURLsFor builds absolute URLs from the request, because the operator is
// going to paste them somewhere this server cannot see. The Host header is what
// they typed to get here, which is the best available guess at what a viewer
// should type too.
func playoutURLsFor(r *http.Request, set db.PlayoutSettings, cfg playoutPublish) playoutURLs {
	base := requestOrigin(r)
	q := ""
	// The token is only useful in a link when the stream is actually protected;
	// appending it to an open stream's URL would teach the operator to paste a
	// secret around for no reason.
	if set.Public && cfg.Protection == PlayoutProtectToken {
		q = "?" + playoutTokenParam + "=" + cfg.Token
	}

	watch := base + WatchPath
	if q != "" {
		watch = base + WatchPath + "/" + cfg.Token
	}
	return playoutURLs{
		Master: base + PlayoutPrefix + playout.MasterPlaylist + q,
		Watch:  watch,
		Embed: `<iframe src="` + watch + `" width="854" height="480" frameborder="0" ` +
			`allow="autoplay; fullscreen; picture-in-picture" allowfullscreen></iframe>`,
	}
}

// requestOrigin reconstructs the scheme and host the client used.
func requestOrigin(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	// Behind a terminating proxy r.TLS is nil even though the viewer is on
	// https, and a link that downgrades them is worse than one that guesses.
	if p := r.Header.Get("X-Forwarded-Proto"); p != "" {
		if i := strings.IndexByte(p, ','); i > 0 {
			p = p[:i]
		}
		if p = strings.TrimSpace(p); p == "http" || p == "https" {
			scheme = p
		}
	}
	return scheme + "://" + r.Host
}

// playoutPublishRequest is what the Playout page sends. Pointers so an omitted
// field is left alone rather than blanked.
type playoutPublishRequest struct {
	Protection  *playoutProtection `json:"protection,omitempty"`
	Title       *string            `json:"title,omitempty"`
	Description *string            `json:"description,omitempty"`
}

func (s *Server) handlePutPlayoutPublish(w http.ResponseWriter, r *http.Request) {
	var req playoutPublishRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	st := s.playoutStore()
	cfg := st.load()
	if req.Protection != nil {
		switch *req.Protection {
		case PlayoutProtectToken, PlayoutProtectOpen:
			cfg.Protection = *req.Protection
		default:
			writeError(w, http.StatusBadRequest,
				`protection must be "token" or "open"`)
			return
		}
	}
	if req.Title != nil {
		cfg.Title = strings.TrimSpace(*req.Title)
	}
	if req.Description != nil {
		cfg.Description = strings.TrimSpace(*req.Description)
	}

	saved, err := st.save(cfg)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "save playout publishing: "+err.Error())
		return
	}
	if saved.Protection == PlayoutProtectOpen {
		set := s.playoutSettings()
		if set.Enabled && set.Public {
			// Loud on purpose. This is the one setting in the product that puts
			// a stream in front of the whole internet, and the log is where an
			// operator looks when they are asked to prove when that happened.
			s.log.Warn("playout: stream is now PUBLIC and UNPROTECTED — anyone with the URL can watch",
				"client", auth.ClientIP(r, s.cfg.TrustProxyHeaders))
		}
	}
	writeJSON(w, http.StatusOK, saved)
}

// handleRotatePlayoutToken mints a new playback secret, which invalidates every
// link already handed out. That is the point: it is the revocation mechanism.
func (s *Server) handleRotatePlayoutToken(w http.ResponseWriter, r *http.Request) {
	st := s.playoutStore()
	cfg := st.load()
	cfg.Token = newPlayoutToken()
	if cfg.Token == "" {
		writeError(w, http.StatusInternalServerError, "could not generate a token")
		return
	}
	saved, err := st.save(cfg)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "save playout publishing: "+err.Error())
		return
	}
	s.log.Info("playout: playback token rotated; existing links no longer work")
	writeJSON(w, http.StatusOK, map[string]any{
		"token": saved.Token,
		"urls":  playoutURLsFor(r, s.playoutSettings(), saved),
	})
}

func (s *Server) handleResetPlayoutAnalytics(w http.ResponseWriter, r *http.Request) {
	m := s.playoutManager()
	if m == nil {
		writeError(w, http.StatusServiceUnavailable, "playout is not running")
		return
	}
	m.ResetAnalytics()
	writeJSON(w, http.StatusOK, m.Analytics())
}

// ----------------------------------------------------------------- public API

// playoutPublicView is everything the player page needs, and nothing more. In
// particular it never carries the token: a caller who reached this endpoint
// either already holds one or is not entitled to it.
type playoutPublicView struct {
	// Enabled is whether there is anything to watch. False makes the page say
	// "offline" rather than spin on a playlist that will never appear.
	Enabled     bool   `json:"enabled"`
	Title       string `json:"title"`
	Description string `json:"description"`
	// Master and Poster are relative, so the page works under whatever host or
	// proxy prefix the viewer reached it through.
	Master string `json:"master"`
	Poster string `json:"poster"`
	// Variants names the rungs, so the player can offer a quality picker.
	Variants []playoutPublicVariant `json:"variants"`
	// Viewers is the live count; the operator may find it flattering and a
	// viewer may find it reassuring. Derived from the same table the admin page
	// reads, so the two never disagree.
	Viewers int `json:"viewers"`
}

type playoutPublicVariant struct {
	Name     string `json:"name"`
	Playlist string `json:"playlist"`
	Width    int    `json:"width,omitempty"`
	Height   int    `json:"height,omitempty"`
}

// handlePlayoutPublic answers the player page.
//
// It runs the same access check the media does, so a page loaded without a valid
// token learns nothing — not the title, not whether anyone is watching, not even
// whether the stream is live.
func (s *Server) handlePlayoutPublic(w http.ResponseWriter, r *http.Request) {
	set := s.playoutSettings()
	if set.AllowCrossOrigin {
		w.Header().Set("Access-Control-Allow-Origin", "*")
	}

	switch access := s.authorizePlayout(r); access {
	case playoutAllowToken:
		cfg := s.playoutStore().load()
		s.setPlayoutTokenCookie(w, cfg.Token, set.AllowCrossOrigin)
	case playoutDenyChallenge:
		// No WWW-Authenticate here. The page's own fetch would trigger the
		// browser's credential dialog on top of the React UI, which is a worse
		// experience than the page explaining that the link needs a token.
		writeError(w, http.StatusUnauthorized, "this stream requires a playback token")
		return
	default:
		if !access.ok() {
			http.NotFound(w, r)
			return
		}
	}

	cfg := s.playoutStore().load()
	view := playoutPublicView{
		Enabled:     set.Enabled,
		Title:       cfg.Title,
		Description: cfg.Description,
		Master:      PlayoutPrefix + playout.MasterPlaylist,
		Poster:      "/api/v1/playout/poster.jpg",
		Variants:    []playoutPublicVariant{},
	}
	if m := s.playoutManager(); m != nil {
		st := m.Status()
		view.Viewers = st.Analytics.Viewers
		for _, v := range st.Variants {
			// Only rungs a player could actually load. A variant that failed to
			// start would be a quality option that plays nothing.
			if !v.Running {
				continue
			}
			view.Variants = append(view.Variants, playoutPublicVariant{
				Name:     v.Name,
				Playlist: PlayoutPrefix + v.Playlist,
				Width:    v.Width,
				Height:   v.Height,
			})
		}
	}
	writeJSON(w, http.StatusOK, view)
}
