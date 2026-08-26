package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/rainmanjam/polyemesis/internal/auth"
	"github.com/rainmanjam/polyemesis/internal/chat"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/engine"
	"github.com/rainmanjam/polyemesis/internal/ffmpeg"
	"github.com/rainmanjam/polyemesis/internal/metrics"
	"github.com/rainmanjam/polyemesis/internal/oauth"
	"github.com/rainmanjam/polyemesis/internal/routing"
	"github.com/rainmanjam/polyemesis/internal/supervisor"
)

// -------------------------------------------------------------- setup & auth

func (s *Server) handleSetupStatus(w http.ResponseWriter, r *http.Request) {
	has, err := s.store.HasUser()
	if err != nil {
		writeStoreError(w, err)
		return
	}
	// sources is the second thing a browser needs before it can decide what to
	// render, and this is the one endpoint it can ask before signing in.
	//
	// An install with no programme is a NORMAL state, not a failure: nothing is
	// running, so the live endpoints answer with empty snapshots that look
	// exactly like a broadcast that has not started. Only the count separates
	// "nothing is on air" from "there is nothing to put on air", and only the
	// second one has an answer the operator can act on.
	//
	// A count that cannot be read is reported as absent rather than as zero,
	// because zero is the value the empty state keys off and inventing it from
	// a failed query would send a working install to the wrong screen.
	body := map[string]any{
		"needsSetup":       !has,
		"minPasswordChars": db.MinPasswordLength,
	}
	if n, err := s.store.CountSources(); err == nil {
		body["sources"] = n
	} else {
		s.log.Warn("cannot count sources for the setup status", "err", err)
	}
	writeJSON(w, http.StatusOK, body)
}

func (s *Server) handleSetup(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Username) == "" {
		req.Username = "admin"
	}

	// CreateUser refuses to run twice, so this endpoint cannot be used to take
	// over an existing install even though it is unauthenticated.
	user, err := s.store.CreateUser(req.Username, req.Password)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	token, err := s.sessions.Issue(user.ID, user.Username)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := s.sessions.SetSession(w, r, token); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"username": user.Username})
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	// Checked before the body is read and long before bcrypt runs: the point
	// of the throttle is that a guess must not cost us a password hash.
	ip := auth.ClientIP(r, s.cfg.TrustProxyHeaders)
	if wait := s.logins.Retry(ip); wait > 0 {
		s.log.Warn("throttled login", "remote", ip, "retryAfter", wait)
		w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(wait.Seconds()))))
		writeError(w, http.StatusTooManyRequests, "too many failed attempts, try again later")
		return
	}

	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}

	user, err := s.store.GetUser()
	if err != nil {
		writeStoreError(w, err)
		return
	}
	// One message for both wrong-user and wrong-password: no username oracle.
	if user.Username != req.Username || !user.CheckPassword(req.Password) {
		wait := s.logins.Fail(ip)
		s.log.Warn("failed login", "username", req.Username, "remote", ip, "penalty", wait)
		// Only once the throttle has started imposing a delay, which is exactly
		// when this address has spent its free allowance. A first mistyped
		// password is not an incident, and an alert that fires on one is an
		// alert the operator mutes before the first real attack. The throttled
		// branch above raises nothing at all: it answers before the password is
		// read, so publishing there would let an attacker set the event rate.
		if wait > 0 {
			s.publishAudit(auditLoginFailed(ip, s.logins.Failures(ip)))
		}
		writeError(w, http.StatusUnauthorized, "incorrect username or password")
		return
	}
	// Read BEFORE Succeed clears the counter. "Somebody signed in after nine
	// rejections from this address" is the one message here that means the
	// guessing worked, and one line later there is nothing left to read it from.
	failuresBefore := s.logins.Failures(ip)
	s.logins.Succeed(ip)

	token, err := s.sessions.Issue(user.ID, user.Username)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := s.sessions.SetSession(w, r, token); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// After the session exists, not after the password checked out. A correct
	// password that could not be turned into a session is not somebody being
	// signed in, and reporting it as one would send the operator hunting for a
	// session that never existed.
	s.publishAudit(auditLoginSucceeded(ip, failuresBefore))
	writeJSON(w, http.StatusOK, map[string]any{"username": user.Username})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	s.sessions.ClearSession(w, r)
	writeJSON(w, http.StatusOK, map[string]string{"status": "signed out"})
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	p, ok := principalFrom(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "not signed in")
		return
	}
	if p.token == nil {
		writeJSON(w, http.StatusOK, map[string]any{"username": p.username, "auth": "session"})
		return
	}
	// A token acts as the admin, so report the admin's name rather than
	// leaving a scripted caller guessing whose install it is talking to.
	username := ""
	if u, err := s.store.GetUser(); err == nil {
		username = u.Username
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"username": username, "auth": "token", "tokenName": p.token.Name,
	})
}

// tourState is the body both tour handlers return, so a caller that just wrote
// does not have to re-read to learn what the state now is.
type tourState struct {
	Completed bool `json:"completed"`
	// Unix seconds, 0 when the tour has never been finished or dismissed. The
	// zero is carried rather than omitted: a client that special-cased a missing
	// field would be reading the same thing twice.
	CompletedAt int64 `json:"completedAt"`
}

// handleTourState answers whether the onboarding tour should still be offered.
//
// It resolves the SINGLE admin account rather than the calling principal, which
// is not a shortcut -- this product has exactly one user row, enforced by
// CreateUser's WHERE NOT EXISTS, and a token principal has no user of its own to
// ask about. handleMe already resolves the admin the same way for the same
// reason.
//
// A GET on an install that has not completed first-run setup answers 404 rather
// than inventing a "not completed": there is nobody yet whose tour this would
// be, and 200 with a plausible-looking body is how a client comes to believe an
// unconfigured install is a configured one.
func (s *Server) handleTourState(w http.ResponseWriter, r *http.Request) {
	u, err := s.store.GetUser()
	if err != nil {
		writeError(w, http.StatusNotFound, "no admin user configured")
		return
	}
	at, err := s.store.TourCompletedAt(u.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, tourState{Completed: at > 0, CompletedAt: at})
}

// handleTourComplete records that the tour has been finished or dismissed.
//
// IDEMPOTENT, and deliberately not a toggle. The UI calls it when the operator
// finishes the tour and again when they dismiss the offer, and the two can race
// -- clicking "done" on the last step also destroys the popover, which is the
// same path a dismissal takes. A toggle would make the second call undo the
// first and the offer would come back on the next page load.
//
// The FIRST completion wins, so the timestamp answers "when did this operator
// first stop needing the tour" rather than "when did they last replay it".
func (s *Server) handleTourComplete(w http.ResponseWriter, r *http.Request) {
	u, err := s.store.GetUser()
	if err != nil {
		writeError(w, http.StatusNotFound, "no admin user configured")
		return
	}
	at, err := s.store.TourCompletedAt(u.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if at == 0 {
		at = time.Now().Unix()
		if err := s.store.SetTourCompleted(u.ID, time.Unix(at, 0)); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	writeJSON(w, http.StatusOK, tourState{Completed: true, CompletedAt: at})
}

func (s *Server) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Current string `json:"current"`
		New     string `json:"new"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	user, err := s.store.GetUser()
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if !user.CheckPassword(req.Current) {
		writeError(w, http.StatusUnauthorized, "current password is incorrect")
		return
	}
	if err := s.store.SetPassword(user.ID, req.New); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	// SetPassword bumped the user's token epoch, so every session token issued
	// before this moment — including the one that authenticated this very
	// request — is now refused. That is the point: a password change is what an
	// operator does when they think somebody else is holding their session, and
	// it has to actually end that session.
	//
	// Re-issuing here means the operator changing their own password stays
	// logged in on this device while every other copy of the old token stops
	// working. If the re-issue fails, the response still reports success,
	// because the password genuinely did change; the operator lands back at the
	// login screen on their next request, which is the safe direction to fail.
	token, err := s.sessions.Issue(user.ID, user.Username)
	if err == nil {
		_ = s.sessions.SetSession(w, r, token)
	}
	// Raised after the store call and regardless of whether the re-issue above
	// worked, because the password genuinely did change either way -- and the
	// operator who did NOT do this is the reader who needs it most, at the exact
	// moment their own session stopped working.
	s.publishAudit(auditPasswordChanged(s.clientIP(r)))
	writeJSON(w, http.StatusOK, map[string]string{"status": "password changed"})
}

// ------------------------------------------------------------------- system

// storeSettingsWithDefaultIngest is what an unscoped endpoint means by "the
// settings": the stored document, with the DEFAULT SOURCE's ingest block laid
// over the singleton one.
//
// It reproduces engine.effectiveSettings (engine.go, `settings.Ingest =
// src.Ingest`) deliberately, because that overlay is the whole difference
// between the two spellings and getting it wrong is silent. The settings
// blob's own ingest block predates sources and NO engine reads it -- an
// endpoint that reported it would be describing an ingest nothing serves,
// while the operator's encoder connects somewhere else entirely.
//
// The store rather than a running engine, which also fixes a lag: an engine's
// snapshot is whatever its last reconcile installed, so GET /system described
// the previous listener ports until something reconciled.
//
// A source that cannot be read leaves the singleton block in place, which is
// the same fallback the engine takes and for the same reason: describing the
// stale thing beats failing the whole endpoint.
func (s *Server) storeSettingsWithDefaultIngest() (db.Settings, error) {
	settings, err := s.store.GetSettings()
	if err != nil {
		return settings, err
	}
	id, err := s.defaultSourceID()
	if err != nil {
		return settings, nil
	}
	src, err := s.store.GetSource(id)
	if err != nil {
		return settings, nil
	}
	settings.Ingest = src.Ingest
	return settings, nil
}

// defaultSourceID is which source an unscoped endpoint speaks for: the one the
// default engine is RUNNING, and only the store's ordering when no engine is.
//
// The two disagree, in one direction, and the difference is an operator
// pointing an encoder at a dead port. Manager.Default() is the first source
// that BUILT an engine -- Manager.reconcile logs "cannot build engine for
// source" and carries on -- while db.DefaultSourceID() is the first row by
// position whether anything is listening for it or not. Take the row on an
// install whose lowest-positioned source failed to come up and /system
// advertises that source's ingest URL and passphrase, with no listener behind
// either; the engine's snapshot, which this replaced, never could.
//
// The store is the answer when there is no engine at all, which is the whole
// reason this endpoint stopped asking one.
func (s *Server) defaultSourceID() (int64, error) {
	if s.mgr != nil {
		// The nil check is NOT the precondition of the call any more --
		// Engine.SourceID answers 0 on a nil receiver, as of the same commit
		// this note is in -- and deleting it on those grounds would change
		// what this function means. A nil engine returning 0 would be handed
		// straight back as "the default source is id 0" and the store would
		// never be consulted, so an install whose lowest-positioned source
		// failed to build would advertise a source that does not exist
		// instead of falling through to the row that does. The check is what
		// selects between the two answers below, not armour around a deref.
		if e := s.mgr.Default(); e != nil {
			return e.SourceID(), nil
		}
	}
	return s.store.DefaultSourceID()
}

func (s *Server) handleSystem(w http.ResponseWriter, r *http.Request) {
	settings, err := s.storeSettingsWithDefaultIngest()
	if err != nil {
		writeStoreError(w, err)
		return
	}
	spec := ffmpeg.IngestSpec{
		Kind:          ffmpeg.IngestKind(settings.Ingest.Mode),
		SRTPort:       settings.Listeners.SRTPort,
		SRTPassphrase: settings.Ingest.SRT.Passphrase,
		SRTLatencyMS:  settings.Ingest.SRT.LatencyMS,
		RTMPPort:      settings.Listeners.RTMPPort,
		RTMPApp:       settings.Ingest.RTMP.App,
		// No RTMPAddress. Only PublicIngestURL is read below, and that renders
		// the server half alone -- the address is per source and belongs on the
		// Sources page, next to the button that rotates it.
		// Verbatim for an operator, masked for a read-scoped token below.
		//
		// An rtsp://user:pass@cam/ source carries a credential in its userinfo,
		// and the operator has to see which source is actually being dialled --
		// that is the entire reason this field exists. What used to stand here
		// was an argument that redacting it would be theatre "because the same
		// caller can read the identical string out of GET /settings". Scopes
		// falsified that premise: both readers are now a read-scoped bearer, so
		// the two disclosures reinforced each other rather than excusing each
		// other. Both are masked now.
		PullURL: settings.Ingest.Pull.URL,
	}
	host := r.Host
	if h, _, ok := strings.Cut(host, ":"); ok {
		host = h
	}

	// The one field on this endpoint that is a credential, and it is a
	// CONSTRUCTED one, which is why struct tags could never have covered it:
	// PublicIngestURL renders srt://host:port?...&passphrase=<cleartext> in SRT
	// mode and returns the pull URL verbatim, userinfo and all, in pull mode.
	// alerts.RedactURL masks both shapes, and it is the same function
	// supervisor already runs over every FFmpeg argv -- which is exactly why
	// GET /processes was clean through this whole review and this route was
	// not.
	ingestURL := spec.PublicIngestURL(host)
	if readScopeCannotSeePublishTokens(r) {
		ingestURL = maskURL(ingestURL)
	}

	principalVaryingResponse(w)
	writeJSON(w, http.StatusOK, map[string]any{
		"version": s.version,
		"ffmpeg":  s.tools(),
		// What the machine has, as opposed to what the FFmpeg build lists. It
		// rides on /system because the two are only meaningful together: an
		// encoder list without the hardware behind it is what made the rendition
		// editor offer NVENC on an AMD box in the first place. Cached after the
		// first scan, so this stays a cheap endpoint.
		"gpu":        machineGPUs(r.Context()),
		"ingestUrl":  ingestURL,
		"ingestMode": settings.Ingest.Mode,
		"maxTracks":  routing.MaxTracks,
		"tlsEnabled": s.cfg.ServesTLS(),
		"dataDir":    s.cfg.DataDir,
		"uiBuilt":    UIBuilt(),
	})
}

// ------------------------------------------------------------ version check

// updateFeedURL is the release feed consulted. A var so a test can point it at
// a local server; nothing reads it until an operator asks for a check.
var updateFeedURL = "https://api.github.com/repos/rainmanjam/polyemesis/releases/latest"

const (
	// updateCheckTTL keeps a bored operator clicking Check from spending the 60
	// requests an hour GitHub allows an unauthenticated client.
	updateCheckTTL = 6 * time.Hour
	// A check is a convenience, so it must never hold a request open long
	// enough for the console to look hung.
	updateCheckTimeout = 5 * time.Second
)

var updateHTTPClient = &http.Client{Timeout: updateCheckTimeout}

// updateCache lives at package scope rather than on Server because there is one
// server per process and this is the only state the feature has. Mutex-guarded
// because two open tabs will race.
var updateCache struct {
	sync.Mutex
	at     time.Time
	latest string
	url    string
	failed bool
}

// versionInfo is what both version endpoints return. Latest, ReleaseURL and
// CheckedAt stay empty until an operator has run a check at least once.
type versionInfo struct {
	Version    string `json:"version"`
	Latest     string `json:"latest,omitempty"`
	ReleaseURL string `json:"releaseUrl,omitempty"`
	// UpdateAvailable is only ever true on a confident comparison of two
	// semantic versions.
	UpdateAvailable bool `json:"updateAvailable"`
	// Comparable is false when either side is not a semantic version — a dev
	// build or a commit hash. The tag that was found is still reported: saying
	// "there is a v1.4.0, work out for yourself whether you have it" beats
	// saying nothing, which is how an operator misses a release entirely.
	Comparable  bool   `json:"comparable"`
	CheckedAt   string `json:"checkedAt,omitempty"`
	CheckFailed bool   `json:"checkFailed,omitempty"`
	// OnAir is what a restart would interrupt, reported alongside the version so
	// the answer to "should I upgrade now" arrives with the answer to "is there
	// an upgrade". Two round trips would let a UI show an enabled button while
	// a broadcast was starting between them.
	OnAir engine.OnAir `json:"onAir"`
	// OnAirSummary is the sentence to show, empty when nothing is at stake. The
	// server owns the wording because the same refusal has to reach a terminal
	// as well as a browser, and two phrasings is how they come to disagree.
	OnAirSummary string `json:"onAirSummary,omitempty"`
}

// handleVersion reports the running build plus whatever a previous check found.
// It never touches the network. A self-hosted server that phones home without
// being asked is a trust violation, so reaching GitHub is a separate endpoint
// the operator has to invoke, nothing schedules it, and startup never waits on
// it.
func (s *Server) handleVersion(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.versionInfo())
}

// handleCheckUpdate is the opt-in. Invoking it is the consent: no config flag
// to forget, and an install that never calls it never contacts GitHub.
func (s *Server) handleCheckUpdate(w http.ResponseWriter, r *http.Request) {
	updateCache.Lock()
	fresh := !updateCache.at.IsZero() && time.Since(updateCache.at) < updateCheckTTL
	updateCache.Unlock()

	if !fresh {
		// The lock is deliberately not held across the request: a five-second
		// fetch must not block the read endpoint. Two simultaneous clicks
		// costing two requests is cheaper than that.
		latest, url, err := fetchLatestRelease(r.Context())

		updateCache.Lock()
		updateCache.at = time.Now()
		updateCache.failed = err != nil
		if err == nil {
			updateCache.latest, updateCache.url = latest, url
		}
		updateCache.Unlock()

		if err != nil {
			// Quiet on purpose. A server with no outbound internet is a
			// supported deployment, and it must not grow an error banner for
			// declining to reach a service it was never promised. The operator
			// gets checkFailed; the detail is in the log.
			s.log.Debug("update check failed", "err", err)
		}
	}
	writeJSON(w, http.StatusOK, s.versionInfo())
}

func (s *Server) versionInfo() versionInfo {
	info := versionInfo{Version: s.version}
	// Surveyed on EVERY call, including the ones that return before the cache
	// is consulted. What is on air changes minute to minute; the release feed
	// changes weekly, and caching them together would let a stale "nothing is
	// live" outlive the broadcast it described.
	if s.mgr != nil {
		info.OnAir = s.mgr.OnAir()
		info.OnAirSummary = info.OnAir.Summary()
	}

	updateCache.Lock()
	defer updateCache.Unlock()
	if updateCache.at.IsZero() {
		return info
	}
	info.CheckedAt = updateCache.at.UTC().Format(time.RFC3339)
	info.CheckFailed = updateCache.failed
	info.Latest = updateCache.latest
	info.ReleaseURL = updateCache.url
	info.UpdateAvailable, info.Comparable = newerThan(info.Latest, s.version)
	return info
}

func fetchLatestRelease(ctx context.Context) (tag, url string, err error) {
	ctx, cancel := context.WithTimeout(ctx, updateCheckTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, updateFeedURL, nil)
	if err != nil {
		return "", "", err
	}
	// GitHub rejects a request with no User-Agent outright.
	req.Header.Set("User-Agent", "polyemesis")
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := updateHTTPClient.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("release feed returned %s", resp.Status)
	}

	var release struct {
		TagName string `json:"tag_name"`
		HTMLURL string `json:"html_url"`
	}
	// The real payload is a few kB; the cap stops a hijacked or redirected feed
	// from being buffered wholesale.
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&release); err != nil {
		return "", "", err
	}
	if strings.TrimSpace(release.TagName) == "" {
		return "", "", errors.New("release feed returned no tag")
	}
	return release.TagName, release.HTMLURL, nil
}

// newerThan reports whether latest is strictly newer than current, and whether
// the two could be compared at all. Both answers are needed: an unparseable
// version must not be reported as up to date, and it must not be reported as
// out of date either.
func newerThan(latest, current string) (newer, comparable bool) {
	l, ok := parseSemver(latest)
	if !ok {
		return false, false
	}
	c, ok := parseSemver(current)
	if !ok {
		return false, false
	}
	// A BUILD FROM SOURCE IS NOT A PRE-RELEASE OF THE TAG IT NAMES.
	//
	// `git describe --tags` emits `v0.6.0-167-g346af01`, meaning 167 commits
	// AFTER v0.6.0. parseSemver reads everything past the first hyphen as a
	// pre-release, and compareSemver then applies the semver rule that a
	// release outranks any pre-release of the same numbers -- correctly, for a
	// real `-rc1`. The result on a source build is the banner offering v0.6.0
	// as an upgrade over v0.6.0-167-g346af01, which is a DOWNGRADE, and
	// "Prepare update" would stage it.
	//
	// It is worst for the operators who are most current: right now a build
	// from main is the only way to have 0.7.0's security fixes, and those are
	// exactly the installs being told they are behind.
	//
	// NOT-COMPARABLE rather than "newer", because that is the truth. A source
	// build cannot be ordered against a release feed: it may be ahead of the
	// tag it names and behind some later tag, and nothing in the string says
	// which. This function already returns `comparable` for precisely this --
	// "an unparseable version must not be reported as up to date, and it must
	// not be reported as out of date either" -- and a describe build is the
	// same situation wearing a parseable shape.
	if isSourceBuild(c.pre) {
		return false, false
	}
	return compareSemver(l, c) > 0, true
}

// describeSuffix matches the `<commits>-g<sha>` tail `git describe --tags`
// appends, with the optional `-dirty` a working tree adds.
//
// Anchored at both ends so a genuine pre-release cannot match: `rc1` has no
// commit count, `beta-2-gabcdefg` would need the whole string to be the
// suffix, and `0-g1234567` -- zero commits past the tag, which git emits only
// with --long -- is still a source build and still not orderable.
var describeSuffix = regexp.MustCompile(`^[0-9]+-g[0-9a-f]{7,40}(-dirty)?$`)

func isSourceBuild(pre string) bool { return describeSuffix.MatchString(pre) }

type semver struct {
	nums [3]int
	pre  string
}

func parseSemver(s string) (semver, bool) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "v")
	// Build metadata never affects precedence.
	if i := strings.IndexByte(s, '+'); i >= 0 {
		s = s[:i]
	}

	var v semver
	if i := strings.IndexByte(s, '-'); i >= 0 {
		v.pre, s = s[i+1:], s[:i]
	}

	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return semver{}, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return semver{}, false
		}
		v.nums[i] = n
	}
	return v, true
}

func compareSemver(a, b semver) int {
	for i := range a.nums {
		if a.nums[i] != b.nums[i] {
			if a.nums[i] > b.nums[i] {
				return 1
			}
			return -1
		}
	}
	switch {
	case a.pre == b.pre:
		return 0
	// A release outranks any pre-release of the same numbers, which is what
	// stops v1.2.0-rc1 from looking newer than the v1.2.0 already installed.
	case a.pre == "":
		return 1
	case b.pre == "":
		return -1
	}
	// Lexical order between two pre-releases. Not full semver precedence, but
	// the only case it decides is "which release candidate", and being wrong
	// there costs an operator one glance at the changelog.
	return strings.Compare(a.pre, b.pre)
}

// statusPayload is the status of the BOX, and the one place it is assembled.
//
// The destination list must not come from a single engine. s.eng() is
// mgr.Default(), whose Status is scoped to its own programme (#515), so reading
// it here gave a multi-source install the selected programme's destinations and
// nothing for the others -- in the dashboard's grouped list and in the
// WebSocket push, which is the same payload arriving by a different door.
//
// It exists as a function because those two doors drifted apart once already:
// each built the payload itself, so fixing one left the other wrong and the UI
// showed different things depending on whether it had polled or been pushed.
func (s *Server) statusPayload() engine.Status {
	st := s.eng().Status()
	st.Destinations = s.mgr.DestinationStatuses()
	return st
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.statusPayload())
}

func (s *Server) handleSource(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.eng().SourceInfo())
}

// handlePutAnnotations records what each incoming audio track is.
//
// Roles describe the feed, so they are stored once on the ingest rather than
// per destination, and every destination recompiles against them. That does
// mean a save can move audio: a role exclusion or a denoise tick changes the
// filter string, and the reconcile below restarts the destinations that
// changed. Destinations whose graph is unaffected are left running.
//
// WHICH programme is therefore not a detail: this route restarts live
// destinations, and it restarted the DEFAULT programme's whatever the operator
// was looking at (#497) -- fired by a 500 ms debounce, with no click and no
// confirmation. scopedEngine refuses rather than guessing on an install that
// runs more than one.
func (s *Server) handlePutAnnotations(w http.ResponseWriter, r *http.Request) {
	eng, ok := s.scopedEngine(w, r)
	if !ok {
		return
	}
	var req struct {
		Annotations []routing.TrackAnnotation `json:"annotations"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	// Validated before the store is touched, and with routing's own validator,
	// so the message an operator sees here is worded like every other routing
	// error they have already met.
	if err := routing.ValidateAnnotations(req.Annotations); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	// Onto the SOURCE, not the settings singleton.
	//
	// Annotations describe what the tracks of one particular feed are -- which
	// is the music, which is the mic -- so they are a property of a source, and
	// with several sources they have to be. They used to live in
	// settings.Ingest, and when the engine started reading its ingest from the
	// source row instead they silently stopped arriving: role exclusion stopped
	// dropping the music track and stems went back to being named track1,
	// track2, track3. The audio acceptance suite caught it.
	src, err := s.store.GetSource(eng.SourceID())
	if err != nil {
		writeStoreError(w, err)
		return
	}
	src.Ingest.Annotations = req.Annotations
	if err := s.store.UpdateSource(src); err != nil {
		writeStoreError(w, err)
		return
	}
	// Also mirrored into settings, so a client reading GET /settings still sees
	// the annotations it wrote and an install that later drops back to a single
	// source keeps them.
	//
	// Through UpdateSettings like every other read-modify-write of the document:
	// the mirror rewrites the whole blob, so on its own it would discard a
	// concurrent settings save entirely. A failure is still only warned about --
	// the annotations are already on the source, which is where the engine reads
	// them from, so the save itself succeeded and refusing it now would be a lie.
	//
	// NEW BEHAVIOUR worth naming: this path now validates, because
	// UpdateSettings validates. A stored document that is invalid for some
	// unrelated reason used to get the mirror written anyway by raw PutSettings;
	// now the mirror is skipped and only the log line says so. That is the safer
	// direction -- it is how a document stops being repeatedly re-stored while
	// broken -- but it means GET /settings can report annotations that differ
	// from the source row until the underlying invalidity is fixed. The engine
	// reads the SOURCE, so nothing on air is affected.
	//
	// ONLY FOR THE DEFAULT PROGRAMME, which is the only one the settings
	// singleton can describe. There is one settings document and several
	// sources, so mirroring programme 2's labels into it would overwrite
	// programme 1's for every reader of GET /settings -- the same
	// one-document-many-programmes confusion as #497, arriving through the
	// back door of a compatibility mirror.
	if eng.SourceID() == s.eng().SourceID() {
		if _, err := s.store.UpdateSettings(func(settings *db.Settings) error {
			settings.Ingest.Annotations = req.Annotations
			return nil
		}); err != nil {
			s.log.Warn("annotations saved to the source but not mirrored to settings", "err", err)
		}
	}
	if err := s.reconcile(); err != nil {
		writeError(w, http.StatusInternalServerError, "annotations saved but reconcile failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, eng.SourceInfo())
}

// handleSwitchSource puts one failover source on air by hand.
//
// Manual is the DEFAULT return mode, so without this route the only thing that
// can ever hand control back after an automatic switch is another automatic
// switch — the operator would watch the slate with no way to leave it. "auto"
// clears the pin and returns the tier to its detector.
//
// Scoped like every other member of the #497 family: a failover switch is one
// programme going to its backup feed, and asking the default engine for it put
// a DIFFERENT programme on its slate while reporting success for the one the
// operator named.
func (s *Server) handleSwitchSource(w http.ResponseWriter, r *http.Request) {
	eng, ok := s.scopedEngine(w, r)
	if !ok {
		return
	}
	var req struct {
		Source string `json:"source"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if err := eng.SwitchSource(req.Source); err != nil {
		// Every failure here is the operator asking for something this
		// configuration cannot do — an unknown name, or a tier that is off.
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, eng.Failover())
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		// The host reading comes off the manager, not off this programme: it
		// describes the box, and an install running three sources used to have
		// three samplers of it disagreeing by a tick.
		"system":  s.hostSystem(),
		"bitrate": s.ingestBitrate(),
		"relay":   s.relayStats(),
	})
}

func (s *Server) handleLevels(w http.ResponseWriter, r *http.Request) {
	levels, at := s.eng().Levels()
	writeJSON(w, http.StatusOK, map[string]any{"levels": levels, "at": at})
}

// handleMetrics renders the Prometheus exposition.
//
// The route is authenticated; it is not open from loopback. Loopback is the
// wrong gate for the way this server is actually deployed: Prometheus usually
// runs in a neighbouring container, so its scrape never arrives from
// 127.0.0.1, and with TrustProxyHeaders on, every request arrives from a proxy
// that does. The check would be too strict and too lax at once. An API token
// is neither — it is issued and revoked from the tokens page like any other,
// and Prometheus sends one natively with `authorization` or `bearer_token_file`
// in the scrape config. A session cookie is accepted as well, so an admin who
// is already signed in can just open the URL.
func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	st := s.eng().Status()
	// EVERY programme's destinations, not the default engine's. A scrape that
	// silently covers one source is worse than one that fails: a missing series
	// is indistinguishable from a destination nobody configured, so an alert
	// that should fire on a dead destination simply never evaluates.
	st.Destinations = s.mgr.DestinationStatuses()

	snap := metrics.Snapshot{
		Version:      s.version,
		Uptime:       time.Since(s.startedAt),
		Destinations: make([]metrics.Destination, 0, len(st.Destinations)),
		Relay: metrics.Relay{
			Subscribers: len(st.Relay.Subscribers),
			RxPackets:   st.Relay.RxPackets,
			RxBytes:     st.Relay.RxBytes,
			TxPackets:   st.Relay.TxPackets,
			Dropped:     st.Relay.Dropped,
		},
	}

	// A scrape says how many programmes exist, so an alert can tell a server
	// nobody has configured from one whose broadcast ended -- every other
	// series reads zero in both cases.
	//
	// A count that will not read OMITS the series, the same rule the setup
	// status follows. It is the one number here where a fabricated zero is not
	// a smaller version of the truth but its opposite: 0 means "nobody has
	// configured this install", so a sqlite hiccup would silence an outage
	// alert and fire a first-run alert at a live broadcast. Absent is a state
	// Prometheus can already ask about with absent(); a wrong 0 is not.
	if n, err := s.store.CountSources(); err == nil {
		snap.Sources = &n
	} else {
		s.log.Warn("metrics: source count unavailable", "err", err)
	}

	// The relay's own rate, not the ingest process's -progress line: this is
	// the series the dashboard graphs, so the metric cannot disagree with what
	// an operator is looking at while they read it.
	if b := s.ingestBitrate(); len(b) > 0 {
		snap.Ingest.BitrateKbps = b[len(b)-1].Kbps
	}
	snap.Ingest.State = string(supervisor.StateStopped)
	if st.Ingest != nil {
		snap.Ingest.State = string(st.Ingest.State)
		snap.Ingest.Restarts = st.Ingest.Restarts
	}

	for _, d := range st.Destinations {
		md := metrics.Destination{
			// A destination that is not running has no supervised process at
			// all; reporting it as stopped keeps its state series meaningful
			// instead of leaving every state at zero.
			Process:  metrics.Process{State: string(supervisor.StateStopped)},
			ID:       d.ID,
			Name:     d.Name,
			Kind:     string(d.Kind),
			Platform: string(d.Platform),
			Enabled:  d.Enabled,
		}
		if d.Process != nil {
			md.Process = metrics.Process{
				State:       string(d.Process.State),
				Restarts:    d.Process.Restarts,
				BitrateKbps: d.Process.Progress.BitrateKbps,
				DropFrames:  d.Process.Progress.DropFrames,
			}
		}
		snap.Destinations = append(snap.Destinations, md)
	}

	// A scrape reports what it can. Failing the whole endpoint because the
	// recordings volume is momentarily unreadable would also lose the ingest
	// and destination series, which are the ones an alert is watching.
	if u, err := s.recordings().Usage(); err == nil {
		snap.Recordings = metrics.Recordings{
			Files:      u.Count,
			UsedBytes:  u.UsedBytes,
			FreeBytes:  u.FreeBytes,
			TotalBytes: u.TotalBytes,
		}
	} else {
		s.log.Warn("metrics: recordings usage unavailable", "err", err)
	}

	sys := s.hostSystem()
	snap.Host = metrics.Host{
		CPUPercent:     sys.CPUPercent,
		MemUsedBytes:   sys.MemUsedBytes,
		MemTotalBytes:  sys.MemTotalBytes,
		ProcCPUPercent: sys.ProcCPUPercent,
		ProcMemBytes:   sys.ProcMemBytes,
		NumCPU:         sys.NumCPU,
	}

	w.Header().Set("Content-Type", metrics.ContentType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = io.WriteString(w, metrics.Render(snap))
}

// ----------------------------------------------------------------- settings

func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	settings, err := s.store.GetSettings()
	if err != nil {
		writeStoreError(w, err)
		return
	}
	// HasPassword is derived, never stored in the settings blob.
	//
	// The blob is served straight to the settings page, so a password in it
	// would be handed to every browser that opened Settings. The page gets a
	// boolean instead: enough to render "a password is set" and to offer to
	// clear it, and nothing more.
	if has, err := s.store.HasMQTTPassword(); err == nil {
		settings.MQTT.HasPassword = has
	} else {
		s.log.Warn("cannot tell whether an MQTT password is stored", "err", err)
	}
	// Same for the automod model key, and for the same reason: the page needs
	// to know one is set without ever being handed it.
	if has, err := s.store.HasAutomodKey(); err == nil {
		settings.Automod.Model.HasAPIKey = has
	} else {
		s.log.Warn("cannot tell whether an automod model key is stored", "err", err)
	}
	// The ingest credentials are sealed the same way the MQTT password and the
	// automod key already were, except that they cannot be moved out of the
	// blob -- the settings page reads and writes them -- so they are blanked on
	// the way out for a read-scoped token instead. See internal/api/redact.go.
	if readScopeCannotSeePublishTokens(r) {
		settings = readSafeSettings(settings)
	}
	principalVaryingResponse(w)
	writeJSON(w, http.StatusOK, settings)
}

// handlePutMQTTPassword sets or clears the broker password.
//
// Its own endpoint rather than a field on the settings blob, for the reason
// above: the blob travels outward on every settings read, and a write-only
// field in a read-write payload is a trap -- a client that PUT back what it
// GOT would blank the password every time.
//
// An empty password CLEARS the stored one. That is the only way to move to an
// anonymous broker without leaving a stale credential behind to be sent to it.
func (s *Server) handlePutMQTTPassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if err := s.store.PutMQTTPassword(s.box, req.Password); err != nil {
		writeStoreError(w, err)
		return
	}
	// No reconcile call: the MQTT runner polls the settings and notices the
	// password changed by its hash, which is also what makes a rotation to a
	// different password of the same length take effect.
	//
	// Raised here rather than left to PUT /settings, which this endpoint does
	// not go through: the password is sealed straight into the store, so
	// changedSections can never see it. Without this line the channel would
	// report a cosmetic settings tweak and stay silent about a credential
	// rotation, which is exactly backwards. Only the section name travels.
	s.publishAudit(auditSettingsChanged([]string{"mqtt"}, s.clientIP(r)))
	writeJSON(w, http.StatusOK, map[string]bool{"hasPassword": req.Password != ""})
}

func (s *Server) handlePutSettings(w http.ResponseWriter, r *http.Request) {
	// Buffered before EITHER lock, and the order of these two statements is
	// the whole point. The decode has to happen inside the store's settings
	// mutex -- decoding over the stored document is what makes a partial
	// payload safe -- so reading the network there would hold that lock at the
	// speed of the client's connection, and the server sets no body timeout
	// (ReadHeaderTimeout bounds headers only). The same argument applies to
	// settingsMu below, whose whole job is to make a media delete wait: a save
	// from a stalled connection must not hold it either. See readJSONBody.
	body, ok := readJSONBody(w, r)
	if !ok {
		return
	}

	// settingsMu -- see its declaration on Server. Held for the whole
	// handler rather than trimmed to just the store call: the only cost of
	// the wider scope is a concurrent DELETE /api/v1/media/{name} waiting a
	// little longer, and that is simpler to keep correct than re-deriving
	// the exact validate-and-store boundary by hand at every future edit to
	// this function.
	s.settingsMu.Lock()
	defer s.settingsMu.Unlock()

	// Everything from reading the stored document to storing the new one is one
	// span inside db.UpdateSettings, which holds the STORE's settings lock
	// across it. s.settingsMu above is a different boundary and cannot serve
	// this one: the engine's scheduled playlist flip writes settings too and has
	// no way to reach a mutex that lives on the API server, so a save that read,
	// merged and wrote around it would silently discard whatever the scheduler
	// had just changed -- and, because the settings are one JSON blob, every
	// other field with it.
	//
	// The closure decodes the request over the stored document, so a partial
	// payload still cannot blank fields the client did not send.
	// The stored document as bytes, taken inside the closure below and read
	// after it returns. Bytes rather than a db.Settings copy for the same
	// reason the playlist is copied there: decoding into settings can rewrite
	// slices in place, so a struct copy would not be a snapshot of anything.
	// Marshalling is the cheapest thing that genuinely freezes the value.
	//
	// It never leaves this handler. changedSections compares it and returns
	// section NAMES; nothing derived from these bytes reaches an alert.
	var storedJSON []byte
	// The stored ingest, and whether the save asked to change it on an install
	// that has no source to change. Both are written inside the closure and
	// read after it, which is the only place both the submitted document and
	// the stored one exist at once.
	var storedIngest db.IngestSettings
	ingestRefused := false
	settings, err := s.store.UpdateSettings(func(settings *db.Settings) error {
		storedJSON, _ = json.Marshal(settings)
		// The stored playlist, copied BEFORE the decode overwrites it.
		//
		// The copy is not defensive tidiness: json.Unmarshal reuses a slice's
		// backing array when the capacity allows, so decoding into settings can
		// rewrite the stored items IN PLACE and leave "what was already saved"
		// and "what was just sent" as the same memory. playlistUploadProblems
		// needs them to be two different values or it cannot tell an item the
		// operator is introducing from one they inherited.
		storedPlaylist := db.PlaylistSettings{
			Enabled: settings.Failover.Playlist.Enabled,
			Items:   append([]db.PlaylistItem(nil), settings.Failover.Playlist.Items...),
		}
		// The two stored pull URLs, for the same reason and taken at the same
		// moment. Plain strings need no copy -- a string value IS a snapshot,
		// where the playlist above is a slice whose backing array the decode
		// can rewrite in place. See pullSourceUploadProblems.
		storedPullURL := settings.Ingest.Pull.URL
		storedBackupPullURL := settings.Failover.Backup.Pull.URL
		// The whole stored ingest block, copied the same way and for the same
		// reason -- Annotations is a slice the decode can rewrite in place. It
		// is what the write-through below compares against, and what it puts
		// back when there is nothing to write it through TO.
		storedIngest = settings.Ingest
		storedIngest.Annotations = append(
			[]routing.TrackAnnotation(nil), settings.Ingest.Annotations...)
		if err := decodeJSONInto(body, settings); err != nil {
			return err
		}
		// Validated HERE as well as inside UpdateSettings, and the order is the
		// point: the filesystem check below must never run on a document the
		// shape rules have already refused. Returning the typed error rather
		// than writing the response keeps one 400 mapping below for both
		// validation failures, whichever of the two found it.
		if err := settings.Validate(); err != nil {
			return db.InvalidSettingsError{Err: err}
		}
		// A save that touches the ingest section may not leave the mode unset.
		//
		// db.IngestUnset is storable on purpose — it is what a fresh install is
		// before anyone has chosen, and the migration writes it during DB open,
		// so the store cannot refuse it. But it is a starting state, never a
		// choice: nobody opens this form and decides on "none". Refusing it here
		// is what turns "no default" into "you have to pick", without which the
		// unset state would just be a silent no-ingest.
		//
		// Scoped to CLEARING a mode that was already chosen, not to every save
		// made before one is. The settings page PUTs the whole document, so
		// refusing any unset ingest would fail the first unrelated change an
		// operator makes on a fresh install — for a reason that has nothing to
		// do with what they touched. What forces the choice on a fresh install
		// is that nothing ingests until it is made, and the UI says so.
		if settings.Ingest.Mode == db.IngestUnset {
			var stored struct {
				Ingest struct {
					Mode db.IngestMode `json:"mode"`
				} `json:"ingest"`
			}
			if json.Unmarshal(storedJSON, &stored) == nil && stored.Ingest.Mode != db.IngestUnset {
				return badRequestError{"choose an ingest mode: srt, rtmp or pull"}
			}
		}
		// The half of playlist validation that needs a filesystem.
		// Settings.Validate checks an item's SHAPE and cannot check its
		// existence -- internal/db has no uploads store and must not grow one --
		// so the "a missing upload is a settings error" rule is enforced here,
		// where the store already exists.
		//
		// Scoped to items this save INTRODUCES. An unrelated save must not be
		// refused because an upload some earlier item named has since been
		// deleted: the operator has no control that can clear it, and the
		// readiness gate keeps the playlist off air regardless. See
		// playlistUploadProblems.
		//
		// This is the one thing left inside the store's lock that touches
		// something outside the process: a MkdirAll and a Stat per introduced
		// item, on local disk. Bounded and short, unlike a network read, and
		// moving it out would mean either checking uploads against a document
		// that is not the one about to be stored or holding the check open
		// across the write. Recorded rather than claimed away.
		if err := s.playlistUploadProblems(settings.Failover.Playlist, storedPlaylist); err != nil {
			return badRequestError{err.Error()}
		}
		// The same rule for the OTHER way a stored upload reaches an FFmpeg: a
		// pull source naming one. That path bypasses ffmpeg.ProbeFile's format
		// allowlist entirely, so a file this server was never allowed to inspect
		// must not be routed to air through it. #201.
		if err := s.pullSourceUploadProblems(*settings, storedPullURL, storedBackupPullURL); err != nil {
			return badRequestError{err.Error()}
		}
		// AND THE INGEST BLOCK ON AN INSTALL WITH NOWHERE TO PUT IT.
		//
		// settings.ingest is not read by anything any more: the engine takes
		// its ingest from the source row, and the write-through below is the
		// only thing that gives this half of the form an effect. With no source
		// there is nothing to write it through to, so an edit here would store,
		// answer 200, and change nothing -- on the DEFAULT tab of the settings
		// page, on a first-time operator's first screen. That is the exact bug
		// the write-through was added to fix, arriving again from the other
		// side, and it is the one this handler must never re-commit.
		//
		// So the stored block is PUT BACK and the handler refuses below. Two
		// things follow from doing it here rather than at the top:
		//
		//   - every other setting in the same document is saved. Recording,
		//     chat, automod, alerts and the listeners are all things an
		//     operator legitimately configures before creating a source, which
		//     is also why this route carries no requireSource.
		//   - nothing that has no effect is stored. Refusing without this line
		//     would leave the operator's dead ingest in the blob, and the form
		//     would redisplay it on the next load as though it had taken.
		//
		// Scoped to a CHANGE. The settings page PUTs the whole document, so a
		// save of anything else carries the ingest block along untouched, and
		// refusing that would make every unrelated setting unreachable until a
		// source exists.
		if !ingestEqual(storedIngest, settings.Ingest) {
			switch _, err := s.store.DefaultSourceID(); {
			case err == nil:
			case errors.Is(err, db.ErrSourceNotFound):
				settings.Ingest = storedIngest
				ingestRefused = true
			default:
				return err
			}
		}
		return nil
	})
	var invalid db.InvalidSettingsError
	var badRequest badRequestError
	switch {
	case errors.As(err, &badRequest):
		writeError(w, http.StatusBadRequest, badRequest.Error())
		return
	case errors.As(err, &invalid):
		writeError(w, http.StatusBadRequest, invalid.Error())
		return
	case err != nil:
		writeStoreError(w, err)
		return
	}
	// Raised here rather than at the end of the handler: the document is stored
	// by this point, so every remaining exit -- a source update that fails, a
	// reconcile that fails -- is one where the configuration really did change
	// and the operator asking "was that me?" still deserves an answer.
	//
	// Only when something actually moved. Opening the settings page and pressing
	// Save without touching anything is the most common way to reach this
	// handler, and an alert for it is the noise that gets the channel muted.
	//
	// Both marshal errors are swallowed on purpose. changedSections reads
	// unparseable input as "cannot tell" and answers with no alert, which is the
	// right direction: the alternative is guessing that everything changed and
	// sending the noisiest possible message on the least informed possible
	// basis. UpdateSettings has just serialised this same document into SQLite,
	// so neither call can realistically fail anyway.
	savedJSON, _ := json.Marshal(settings)
	if sections := changedSections(storedJSON, savedJSON); len(sections) > 0 {
		s.publishAudit(auditSettingsChanged(sections, s.clientIP(r)))
	}
	// Ask for the derivative every playlist item needs. AFTER the save, so a
	// rejected settings document never queues a transcode, and before the
	// reconcile below only because a job that is instantly runnable may as well
	// already be queued when the engine looks. See enqueuePlaylistNormalisation
	// for why this is not done from inside the engine.
	s.enqueuePlaylistNormalisation(settings.Failover.Playlist)
	// The ingest block ALSO has to reach the default source, or saving it does
	// nothing at all.
	//
	// Before sources existed, settings.ingest WAS the ingest. Afterwards the
	// engine reads its ingest from the source row -- effectiveSettings does
	// `settings.Ingest = src.Ingest` -- so this endpoint kept accepting an
	// ingest block, storing it, returning 200, and having no effect whatever.
	// The entire ingest editor on the settings page was dead, and an operator
	// changing an SRT port there got a success toast and an unchanged server.
	//
	// Writing it through to the default source restores the old meaning:
	// settings.ingest edits the programme an unscoped request acts on, which is
	// exactly what it edited when there was only one.
	//
	// The three answers DefaultSourceID can give are now three branches, where
	// this used to be one `err == nil` that treated all of them as "skip".
	//
	//   a source        write the block through, as before.
	//   no source       nothing to write it through to. Silent HERE, because
	//                   reaching this line at all means the ingest was
	//                   unchanged -- a change was already refused in the
	//                   closure -- so there is genuinely nothing to do and
	//                   nothing to say. Every other setting in the document has
	//                   just been saved and must not be undone by a 404 about a
	//                   source the operator never asked about.
	//   anything else   a store that cannot be read. Reported: swallowing it
	//                   answered 200 for a save that silently never reached the
	//                   pipeline, which is the failure this whole block exists
	//                   to have stopped happening.
	switch id, err := s.store.DefaultSourceID(); {
	case err == nil:
		if src, err := s.store.GetSource(id); err == nil && !ingestEqual(src.Ingest, settings.Ingest) {
			src.Ingest = settings.Ingest
			if err := s.store.UpdateSource(src); err != nil {
				writeError(w, http.StatusBadRequest, "ingest settings: "+err.Error())
				return
			}
		}
	case errors.Is(err, db.ErrSourceNotFound):
	default:
		writeStoreError(w, err)
		return
	}
	// The MANAGER, not the default engine.
	//
	// Settings are install-wide, and some of them are owned by the manager
	// rather than by any one engine -- the one-port SRT listener above all,
	// which is a single listener serving every source. Reconciling only the
	// default engine saved the setting and changed nothing: enabling shared
	// ingest returned 200 while no listener ever bound, which is exactly the
	// kind of silent no-op that is worse than an error.
	if err := s.reconcile(); err != nil {
		writeError(w, http.StatusInternalServerError, "settings saved but reconcile failed: "+err.Error())
		return
	}
	// Chat retention is not the manager's to reconcile -- the Hub owns it -- and
	// it has to be applied HERE rather than at the next restart. A retention
	// setting that stores, returns 200 and keeps sweeping on the old numbers is
	// the same silent no-op the ingest block above documents.
	ApplyChatRetention(s.chat, settings.Chat)
	// Same argument for automod: a matrix that stores, returns 200 and keeps
	// deciding on the old cells is the silent no-op this file already warns
	// about twice. Rebuilding the engine here is also what recompiles a changed
	// rule -- without it a new pattern would not apply until the next restart.
	ApplyAutomod(s.chat, s.store, s.box, s.log, settings.Automod)
	// And the same for alert delivery: a retry budget that stores, returns 200
	// and keeps chasing on the old count is the third instance of the silent
	// no-op this handler now guards against three times.
	ApplyAlertSettings(s.mgr, settings.Alerts)

	// The refusal, LAST, after everything the same document asked for has
	// actually been applied.
	//
	// Refusing earlier would have been tidier to read and wrong: the rest of
	// the document is already stored by then, so returning above would leave a
	// chat retention window in the database that the Hub is not sweeping on and
	// an automod matrix that is not deciding on -- the silent no-op this
	// handler warns about three times, reintroduced by the guard against a
	// fourth one.
	//
	// codeNoSource, and the same sentence as every other refusal: this is the
	// fresh-install empty state, not a fault, and the dashboard branches on the
	// code rather than reading the English.
	if ingestRefused {
		writeNoSource(w)
		return
	}

	// The EFFECT, not just the intent. "Saved" is a statement about the
	// database; an operator whose destination card just went grey needs to know
	// their edit did that, and one whose card did NOT move needs to know the
	// edit landed somewhere rather than being stored and ignored.
	//
	// The settings stay at the TOP LEVEL rather than nested under a key. The UI
	// types this response as Settings and assigns it straight into state
	// (`setSettings(await api.putSettings(next))` in three pages), so nesting
	// would silently blank every form on the page. Embedding inlines the
	// settings fields and puts reload alongside them, which is additive.
	writeJSON(w, http.StatusOK, settingsResponse{
		Settings: settings,
		Reload:   s.mgr.LastReload(),
	})
}

// settingsResponse is the stored settings plus what saving them just did.
//
// db.Settings is EMBEDDED rather than named, so its fields marshal at the top
// level exactly as they did before this existed. That is not a style choice:
// three UI pages do `setSettings(await api.putSettings(next))` and type the
// response as Settings, so moving the settings under a key would blank every
// form on the page the moment somebody saved. Adding a sibling field is
// additive and older clients ignore it.
type settingsResponse struct {
	db.Settings
	Reload []engine.ReloadReport `json:"reload"`
}

// ApplyChatRetention pushes the stored bounds into a running Hub.
//
// Shared by the settings handler and startup so the two cannot disagree about
// what "0 hours" means, which is the sort of difference that only shows up as
// "my chat history vanished after a restart".
func ApplyChatRetention(hub *chat.Hub, c db.ChatSettings) {
	if hub == nil {
		return
	}
	// 0 hours is keep-forever, and a negative Duration is how the Hub spells
	// that. Converting through time.Duration(0) would mean "purge everything
	// older than now", which is the exact opposite.
	age := time.Duration(c.RetentionHours) * time.Hour
	if c.RetentionHours <= 0 {
		age = -1
	}
	hub.SetRetention(age, c.KeepMessages, time.Duration(c.PurgeMinutes)*time.Minute)
	// The in-memory ring is a separate bound from the stored one and is applied
	// here for the same reason: it decides what a browser connecting one second
	// after the save receives.
	hub.SetHistory(c.HistoryMessages)
}

// ApplyAlertSettings pushes the stored delivery policy into the running engines.
//
// Takes the Manager rather than a Notifier because there is one Notifier PER
// ENGINE and engines come and go with sources. Handing this a single Notifier
// would apply the setting to one programme and leave every other one — and
// every source added afterwards — on the old budget, which is a disagreement an
// operator has no way to see.
//
// Shared with startup, like ApplyChatRetention, so a fresh boot and a settings
// save cannot end up chasing a dead endpoint for different lengths of time.
func ApplyAlertSettings(mgr *engine.Manager, a db.AlertSettings) {
	if mgr == nil {
		return
	}
	mgr.SetAlertRetry(a.RetryAttempts)
}

// ------------------------------------------------------------- destinations

// engineForSource is the engine that owns one programme, and it exists because
// s.eng() is s.mgr.Default() -- engines[0] -- which is the RIGHT answer only on
// an install with one source.
//
// applyDestinationEnabled already carries the finding this generalises:
// "EVERY ENGINE, NOT THE DEFAULT ONE ... The bug is invisible on a single-source
// install, which is every development box." That fix swept every engine looking
// for a destination. This one goes straight to the owner, which is possible
// because db.Destination.SourceID names it and Manager.Engine keys the map by
// exactly that id.
//
// Returns nil when the source has no engine -- a row whose programme was
// deleted under a request, or a manager that has not reconciled yet. Callers
// must handle nil rather than dereference, which is why this hands back the
// engine instead of a value read off it.
func (s *Server) engineForSource(id *int64) *engine.Engine {
	if s == nil || s.mgr == nil || id == nil {
		return nil
	}
	return s.mgr.Engine(*id)
}

// sourceForDestination is the TRACK LAYOUT a destination's routing must be
// compiled against: its own programme's, never the default programme's.
//
// This is not a tidy-up. routing.Source carries Tracks, so compiling a
// destination's profile against another source's layout answers about an ingest
// that destination does not read. Two sources are two ingests and routinely
// differ -- a six-track OBS feed and a stereo camera -- so on a multi-source
// install the "Tracks 1, 2, 4 -> stereo" summary the dashboard renders was
// describing the wrong programme, or failing as a routingError for tracks that
// are perfectly present in the source that destination actually carries.
//
// The bool is SourceKnown's: false means nothing has been measured yet and the
// compile came off the placeholder layout, which the caller flags as
// provisional rather than withholding.
func (s *Server) sourceForDestination(sourceID *int64) (routing.Source, bool) {
	// Falls back to the nil-engine answer -- the placeholder layout, not known
	// -- which is exactly what s.eng() returned for an absent engine before.
	return s.engineForSource(sourceID).SourceKnown()
}

// ------------------------------------------------- naming the programme

// sourceParam is the query parameter a programme-scoped route reads, written
// once so that six routes cannot drift into six spellings of it.
const sourceParam = "source"

// scopedEngine is the engine a request NAMES, and the point of it is what it
// refuses.
//
// #497: editing a track label while looking at programme 2 rewrote programme
// 1's ingest and restarted its live destinations. Every route in this family
// resolved its engine through s.eng() -- s.mgr.Default(), Engines()[0] --
// which is the right answer only on an install with one source. There was no
// click and no confirmation: a 500 ms debounce in the routing editor fired it,
// and the compile preview beside it was reading the same default engine, so
// the page showed a plausible answer for the wrong saved state.
//
// THE FALLBACK IS THE DEFECT, so it is removed exactly where it can be wrong.
// An install with one source (or none) has no other programme to hit and keeps
// the default, which is what every existing client sends -- the monitoring
// page, a script, a UI bundle older than this one. An install with two or more
// must say which, and is refused with source_required until it does. A route
// that cannot work out the programme now says "pick one" instead of picking
// programme 1.
//
// A QUERY PARAMETER rather than `sourceId` in the body, which is what
// requireNamedSource reads on a create: this has to serve GETs (/processes)
// and one POST whose body is a full-replacement routing.PresetOpts with no
// room for a field that is not a preset option. One spelling for every method
// is one thing to remember instead of two.
//
// Returns the engine, never nil when ok is true except on an install with no
// source at all -- the zero-source reads (/processes) answer for that state on
// purpose and must keep doing so, which is why the count of zero takes the
// same branch as the count of one rather than being refused here.
func (s *Server) scopedEngine(w http.ResponseWriter, r *http.Request) (*engine.Engine, bool) {
	raw := strings.TrimSpace(r.URL.Query().Get(sourceParam))
	if raw != "" {
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || id <= 0 {
			writeErrorCode(w, http.StatusBadRequest, codeSourceRequired,
				`"`+sourceParam+`" must be a source id.`+s.availableSources())
			return nil, false
		}
		eng := s.engineForSource(&id)
		if eng == nil {
			// Named a programme that is not running: deleted under this
			// request, never created, or the manager has not reconciled yet.
			// Saying so beats acting on somebody else's programme, which is
			// the whole of #497.
			writeError(w, http.StatusConflict, fmt.Sprintf(
				"source %d is not running, so there is nothing here to answer for it", id))
			return nil, false
		}
		return eng, true
	}

	n, err := s.store.CountSources()
	if err != nil {
		// The count is what makes the omission safe. Without it the choice
		// cannot be shown to be unambiguous, so it is not made.
		writeStoreError(w, err)
		return nil, false
	}
	if n > 1 {
		writeErrorCode(w, http.StatusBadRequest, codeSourceRequired,
			"this install runs several programmes, so this request must say which one it is "+
				`for: add "?`+sourceParam+`=<id>".`+s.availableSources())
		return nil, false
	}
	return s.eng(), true
}

// availableSources is the "Available: 1 (Main), 2 (Studio B)." tail both
// source_required refusals carry, or "" when the list cannot be read.
//
// An operator meeting "say which programme" with no way to see the ids has been
// told to make a choice and not shown the options.
func (s *Server) availableSources() string {
	rows, err := s.store.ListSources()
	if err != nil || len(rows) == 0 {
		return ""
	}
	names := make([]string, 0, len(rows))
	for _, src := range rows {
		names = append(names, fmt.Sprintf("%d (%s)", src.ID, src.Name))
	}
	return " Available: " + strings.Join(names, ", ") + "."
}

func (s *Server) handleListDestinations(w http.ResponseWriter, r *http.Request) {
	rows, err := s.store.ListDestinations()
	if err != nil {
		writeStoreError(w, err)
		return
	}
	// No single `src` here on purpose: it is resolved PER ROW below, because
	// each destination's routing must be compiled against the programme that
	// destination carries.

	// A read-scoped token gets the destination with its publish credentials
	// blanked. db.Destination has no MarshalJSON, so `{"destination": row}` is
	// a raw dump of every leaf -- streamKey, backupStreamKey, and for an audio
	// destination a url whose icecast://user:pass@ userinfo IS the credential.
	// See internal/api/redact.go for why this is a copy at the handler rather
	// than a property of the type.
	hide := readScopeCannotSeePublishTokens(r)

	// Each row is returned with its compiled routing, so the UI can render the
	// "Tracks 1, 2, 4 → stereo" summary and the generated filter string
	// without a second round trip.
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		shown := row
		if hide {
			safe := readSafeDestination(*row)
			shown = &safe
		}
		item := map[string]any{"destination": shown}
		src, srcKnown := s.sourceForDestination(row.SourceID)
		if c, err := routing.Compile(row.Profile, src); err == nil {
			item["routing"] = c
			// PROVISIONAL until something has been measured. Until then this is
			// compiled from the placeholder -- six stereo tracks that exist so
			// the editor has something to draw -- and reconcileOutputs refuses
			// to run that very graph. Handing it over unlabelled made the screen
			// and the process disagree, in the direction that makes the
			// placeholder look authoritative.
			//
			// Flagged, not withheld: configuring a destination before going live
			// is when most people configure them (see refuseIfSilent below).
			if !srcKnown {
				item["routingProvisional"] = true
			}
		} else {
			item["routingError"] = err.Error()
		}
		out = append(out, item)
	}
	principalVaryingResponse(w)
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleGetDestination(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	row, err := s.store.GetDestination(id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	shown := row
	if readScopeCannotSeePublishTokens(r) {
		safe := readSafeDestination(*row)
		shown = &safe
	}
	resp := map[string]any{"destination": shown}
	getSrc, getKnown := s.sourceForDestination(row.SourceID)
	// Compiled off the ORIGINAL row, not the redacted copy: the routing profile
	// carries no credential and compiling the copy would only invite a future
	// reader to wonder whether it differs.
	if c, err := routing.Compile(row.Profile, getSrc); err == nil {
		resp["routing"] = c
		if !getKnown {
			resp["routingProvisional"] = true
		}
	} else {
		resp["routingError"] = err.Error()
	}
	principalVaryingResponse(w)
	writeJSON(w, http.StatusOK, resp)
}

// ingestEqual compares two ingest blocks by value.
//
// Cheap rather than clever: the blocks are small and this runs once per
// settings save. It exists so an unrelated settings change -- a recording cap,
// a log level -- does not rewrite the source row and restart a live ingest for
// nothing.
func ingestEqual(a, b db.IngestSettings) bool {
	x, errA := json.Marshal(a)
	y, errB := json.Marshal(b)
	if errA != nil || errB != nil {
		// Cannot tell, so treat as different. A needless restart is visible and
		// recoverable; a change that silently did nothing is neither.
		return false
	}
	return bytes.Equal(x, y)
}

// refuseIfSilent rejects a profile that compiles to no audio against the ingest
// currently arriving.
//
// routing.Compile has always returned ErrNoAudio for this -- "every selected
// track is excluded by this destination's role policy" -- and every caller here
// wrote `if c, err := Compile(...); err == nil`, using the result and throwing
// the error away. So a destination that would publish silence saved cleanly and
// failed later, on air, where the operator cannot hear it.
//
// Only refuses when the source has actually been PROBED. With no stream
// arriving there is nothing to evaluate the profile against, and refusing then
// would make it impossible to configure a destination before going live --
// which is when most people configure them. Prove it wrong or allow it; never
// guess.
//
// It answers for an absent programme rather than assuming its caller's route
// carries requireSource. POST /destinations does; the point is that this is a
// SECOND read of the engine set, after the middleware's, and one read cannot
// speak for the other -- see engOrNil. The refusal is the middleware's, word
// for word, so a create that loses its programme between the two answers the
// same thing it would have answered an instant earlier rather than storing a
// destination and reporting a reconcile it never performed.
func (s *Server) refuseIfSilent(w http.ResponseWriter, profile routing.Profile) bool {
	e := s.engOrNil()
	if e == nil {
		writeNoSource(w)
		return true
	}
	src := e.Source()
	// No tracks means nothing has been probed yet, so there is nothing to
	// evaluate the profile against.
	if len(src.Tracks) == 0 {
		return false
	}
	if _, err := routing.Compile(profile, src); errors.Is(err, routing.ErrNoAudio) {
		writeError(w, http.StatusBadRequest,
			"this destination would carry no audio against the stream now arriving: "+
				err.Error()+". Streaming silence is the one failure this product exists to prevent, "+
				"so it is refused rather than saved.")
		return true
	}
	return false
}

// dropUnsendableSettings zeroes the platform-specific settings a destination
// cannot act on, and returns one line per thing it dropped.
//
// WHY IT CLEARS RATHER THAN REFUSES. A destination could reach this state
// through the dialog: picking YouTube, declaring COPPA, then switching the
// preset to Kick left the declaration in the form state, hidden -- the
// compliance panel renders only for the three platforms that have one -- and
// save() sent it anyway. So rows already carry compliance their platform will
// never transmit, and a 400 would make every one of them uneditable: there is
// no control in the dialog to clear a field it does not show.
//
// The reason this matters beyond tidiness is REACTIVATION. Compliance stored
// against a Kick destination is inert, because ComplianceFor(kick) is absent
// and the push skips it. Point that same destination back at YouTube and the
// declaration is live again -- a legal statement about who a programme is for,
// sent on behalf of an operator who last saw it in a form they abandoned.
//
// Clearing is therefore the repair, and the warning is what keeps it from
// being a silent one. Nothing here refuses a write.
func (s *Server) dropUnsendableSettings(row *db.Destination) []string {
	var warnings []string

	// Discovered through ComplianceFor, never a list of platform names: the
	// capability is what decides whether a value can be sent, and a second copy
	// of that knowledge here would be the copy that goes stale.
	//
	// A method on Server rather than a free function for one reason: the
	// capability has to be resolved through the SAME provider set the push
	// resolves it through, or a test can aim the push at a stub and leave this
	// answering for production.
	if _, ok := s.providers.ComplianceFor(row.Platform); !ok && !row.Compliance.Empty() {
		row.Compliance = db.Compliance{}
		warnings = append(warnings, fmt.Sprintf(
			"Compliance settings were removed: %s has no compliance surface, so a "+
				"privacy or COPPA declaration stored here would never be sent — and "+
				"would apply again if this destination were pointed back at a platform "+
				"that has one.", row.Platform))
	}

	// The same shape, one platform wide. Crossposting and the donate button are
	// arguments to Facebook's live-video create call and mean nothing anywhere
	// else, so a destination that is not Facebook cannot act on them either.
	if row.Platform != db.PlatformFacebook && !row.Facebook.Empty() {
		row.Facebook = db.FacebookSettings{}
		warnings = append(warnings, fmt.Sprintf(
			"Facebook crossposting and donate settings were removed: they are "+
				"arguments to Facebook's broadcast create call and %s does not have one.",
			row.Platform))
	}

	return warnings
}

func (s *Server) handleCreateDestination(w http.ResponseWriter, r *http.Request) {
	var row db.Destination
	if !decodeJSON(w, r, &row) {
		return
	}
	row.ID = 0
	if !s.requireNamedSource(w, row.SourceID) {
		return
	}
	// Expert mode is reachable only through its own routes. See
	// clearExpertArgs.
	clearExpertArgs(&row)
	if s.refuseIfSilent(w, row.Profile) {
		return
	}
	// Before the write, so what is stored is what the response describes.
	warnings := s.dropUnsendableSettings(&row)
	// What the platform registry knows and this destination contradicts --
	// most often an RTMP URL with no application path, which the far end
	// refuses by dropping the connection rather than by saying anything. The
	// destination is still created: this is advice, and Validate owns refusal.
	for _, w := range row.Warnings() {
		warnings = append(warnings, w.Detail)
	}
	created, err := s.store.CreateDestination(&row)
	if err != nil {
		// Not a plain 400: a body that names no source is the NORMAL shape,
		// and resolving it is the store's job, so "there is no source to
		// resolve" is the install's condition rather than a fault in what was
		// sent. See writeCreateError.
		writeCreateError(w, err)
		return
	}
	if err := s.reconcile(); err != nil {
		s.log.Warn("reconcile after destination create", "err", err)
	}
	resp := map[string]any{"destination": created}
	if len(warnings) > 0 {
		resp["warnings"] = warnings
	}
	writeJSON(w, http.StatusCreated, resp)
}

func (s *Server) handleUpdateDestination(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	existing, err := s.store.GetDestination(id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	// Decode over the existing row so a client sending only a routing profile
	// does not wipe the URL.
	saved := expertArgsOf(existing)
	if !decodeJSON(w, r, existing) {
		return
	}
	existing.ID = id
	// Whatever the body said about expert arguments, the stored ones win. Only
	// the expert routes enforce the confirm step and the guard acknowledgement,
	// and decodeJSON's DisallowUnknownFields means these fields are accepted
	// here the moment they exist on the model — so a plain PUT carrying
	// extraOutputArgs would otherwise be a way around both.
	existing.ExtraInputArgs = saved.InputArgs
	existing.ExtraOutputArgs = saved.OutputArgs
	existing.ExpertAckReencode = saved.AckReencode

	// Deliberately AFTER the decode-over-existing above, because that is the
	// path that produces the state worth catching: a body carrying nothing but
	// {"platform":"kick"} leaves the stored compliance untouched, so the row
	// ends up on a platform that cannot send what it is holding without the
	// client ever mentioning compliance. Checking the request body instead of
	// the merged row would see nothing at all.
	warnings := s.dropUnsendableSettings(existing)
	// Same registry check create does. An edit is the more likely place for a
	// URL to acquire this problem, not the less: creation usually starts from
	// a preset, and editing is where somebody pastes a fresh address in.
	for _, w := range existing.Warnings() {
		warnings = append(warnings, w.Detail)
	}

	updated, err := s.store.UpdateDestination(existing)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	// Reconcile restarts only this destination, and only if the change
	// actually affects its command line.
	if err := s.reconcile(); err != nil {
		s.log.Warn("reconcile after destination update", "err", err)
	}

	resp := map[string]any{"destination": updated}
	updSrc, updKnown := s.sourceForDestination(updated.SourceID)
	if c, err := routing.Compile(updated.Profile, updSrc); err == nil {
		resp["routing"] = c
		if !updKnown {
			resp["routingProvisional"] = true
		}
	}
	if len(warnings) > 0 {
		resp["warnings"] = warnings
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleReorderDestinations persists dashboard order. It deliberately does not
// reconcile: position is display only and is not part of a destination's spec
// hash, so rearranging cards must never interrupt a live output.
func (s *Server) handleReorderDestinations(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IDs []int64 `json:"ids"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if err := s.store.ReorderDestinations(req.IDs); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	rows, err := s.store.ListDestinations()
	if err != nil {
		writeStoreError(w, err)
		return
	}
	ids := make([]int64, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ids": ids})
}

func (s *Server) handleDeleteDestination(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	// READ BEFORE THE DELETE, because the question is about the row and about its
	// siblings, and after DeleteDestination neither the key nor the id ordering
	// that answers it exists any more. A failed read is not worth a failed
	// delete: the notice is a courtesy and the deletion is what was asked for.
	//
	// It does NOT remove the platform's ingest stream, and the long comment at
	// noteOrphanedIngestStream says why that is the answer rather than a gap.
	if dest, err := s.store.GetDestination(id); err == nil {
		s.noteOrphanedIngestStream(dest)
	}
	if err := s.store.DeleteDestination(id); err != nil {
		writeStoreError(w, err)
		return
	}
	if err := s.reconcile(); err != nil {
		s.log.Warn("reconcile after destination delete", "err", err)
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// destEffect is what one press of start or stop actually did to one row: the
// process state read back afterwards, and the two things that state cannot say
// on its own.
//
// It exists so the bulk pair in destinations_bulk.go can report per destination
// without a second implementation of the sentence below. See
// applyDestinationEnabled.
type destEffect struct {
	// State is the supervisor's word for the process, and HasProcess says
	// whether there was a process to ask.
	//
	// The two are separate because a zero supervisor.Status has an empty State,
	// so "" cannot distinguish "no process" from "a process whose state has not
	// been set" -- and the single-destination response has always emitted the
	// key for the second case.
	State      string
	HasProcess bool
	// Error is the destination's own fault text, empty when there is none.
	Error string
	// Warning is DestStatus.StopWarning: set only on a stop, and only when the
	// stop ended on the deadline arm. Empty means the child was reaped.
	Warning string
	// Found reports that the row appeared in the engine's snapshot. False for a
	// row the engine is not carrying, which is not a failure -- it is what a
	// destination on another source's programme looks like from here.
	Found bool
}

// destControl is the result of applyDestinationEnabled, carrying the two
// failures apart because they answer with different status codes: a store
// failure is about the row (404 for a missing one), a reconcile failure is
// about the pipeline (500).
type destControl struct {
	Effect       destEffect
	StoreErr     error
	ReconcileErr error
}

// applyDestinationEnabled is the whole of what POST /destinations/{id}/start and
// POST /destinations/{id}/stop do to one destination: write the run/stop intent,
// reconcile the pipeline, and read the process state back.
//
// Factored out so the bulk routes are exactly N presses of the button an
// operator already has, driven from the same code, rather than a parallel
// implementation that can drift from it. Nothing here is bulk-specific; the
// pacing and the per-row reporting live entirely in destinations_bulk.go.
func (s *Server) applyDestinationEnabled(id int64, enabled bool) destControl {
	if err := s.store.SetDestinationEnabled(id, enabled); err != nil {
		return destControl{StoreErr: err}
	}
	if err := s.reconcile(); err != nil {
		return destControl{ReconcileErr: err}
	}
	// The EFFECT, not just the intent.
	//
	// This used to answer {"enabled": true} the moment the row was written,
	// which is a statement about the database rather than about the stream. A
	// destination whose URL the platform refuses is enabled and not running,
	// and the UI, told only "enabled", drew it as started. Reading the process
	// state back means a success response is evidence that something happened.
	//
	// AND WHICH EFFECT (#209). "Reading the process state back" is only evidence
	// that something happened -- it is not evidence of WHICH thing, because
	// supervisor.stop() sets StateStopped on BOTH of its arms: the one where the
	// child was reaped, and the one where the deadline expired, SIGKILL was
	// issued and nobody waited. Those have different consequences for whoever
	// asked: the second means a process that may still be running is still
	// holding, and still publishing to, what this response has just declared
	// free. So a stop reports `reaped`, and says why when it is false.
	// EVERY ENGINE, NOT THE DEFAULT ONE. s.eng() is s.mgr.Default(), so on an
	// install with more than one source this read looked for the destination in
	// the wrong programme's engine, found nothing, and reported Found=false --
	// which the caller renders as "started" for an enable that failed and as a
	// clean stop for one that left a process holding the port. The bug is
	// invisible on a single-source install, which is every development box.
	var eff destEffect
	for _, d := range s.mgr.DestinationStatuses() {
		if d.ID != id {
			continue
		}
		eff.Found = true
		if d.Process != nil {
			eff.State, eff.HasProcess = string(d.Process.State), true
		}
		eff.Error = d.Error
		if !enabled {
			eff.Warning = d.StopWarning
		}
		break
	}
	return destControl{Effect: eff}
}

func (s *Server) setDestinationEnabled(w http.ResponseWriter, r *http.Request, enabled bool) {
	id, err := idParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	ctl := s.applyDestinationEnabled(id, enabled)
	if ctl.StoreErr != nil {
		writeStoreError(w, ctl.StoreErr)
		return
	}
	if ctl.ReconcileErr != nil {
		writeError(w, http.StatusInternalServerError, ctl.ReconcileErr.Error())
		return
	}
	resp := map[string]any{"enabled": enabled}
	if ctl.Effect.Found {
		if ctl.Effect.HasProcess {
			resp["state"] = ctl.Effect.State
		}
		if ctl.Effect.Error != "" {
			resp["error"] = ctl.Effect.Error
		}
		if !enabled {
			resp["reaped"] = ctl.Effect.Warning == ""
			if ctl.Effect.Warning != "" {
				resp["warning"] = ctl.Effect.Warning
			}
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleStartDestination(w http.ResponseWriter, r *http.Request) {
	s.setDestinationEnabled(w, r, true)
}

func (s *Server) handleStopDestination(w http.ResponseWriter, r *http.Request) {
	s.setDestinationEnabled(w, r, false)
}

func (s *Server) handleRestartDestination(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	// THE OWNING ENGINE, NOT THE DEFAULT ONE. s.eng() is s.mgr.Default(), so a
	// restart of a destination belonging to any programme but the first was
	// asked of an engine that has never heard of it: it answered "no such
	// destination", this handler turned that into a 500, and the destination
	// the operator was trying to restart carried on exactly as it was. On a
	// one-source install the two engines are the same object and nothing is
	// wrong, which is why this survived -- see applyDestinationEnabled, which
	// found the same shape on the enable path.
	row, err := s.store.GetDestination(id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	eng := s.engineForSource(row.SourceID)
	if eng == nil {
		// The programme has no engine: deleted under this request, or the
		// manager has not reconciled yet. Saying so beats a nil dereference and
		// beats restarting somebody else's destination.
		writeError(w, http.StatusConflict, "the source this destination carries is not running")
		return
	}
	if err := eng.RestartDestination(id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "restarting"})
}

// -------------------------------------------------------------- routing API

// handleCompileRouting compiles a candidate profile without saving it.
//
// SCOPED, and #497 said this preview already was -- it was not. It compiled
// against the default engine's track layout like everything else in that
// family, which is worse here than a wrong number: the preview is the page's
// claim that the graph shown is the graph that will run, and on a
// multi-source install it was describing a different programme's ingest.
func (s *Server) handleCompileRouting(w http.ResponseWriter, r *http.Request) {
	eng, ok := s.scopedEngine(w, r)
	if !ok {
		return
	}
	var req struct {
		Profile routing.Profile `json:"profile"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	req.Profile.ApplyDefaults()

	// This backs the live filter-string preview in the routing editor: the
	// user sees exactly what their checkboxes compile to, before saving.
	res, err := routing.Compile(req.Profile, eng.Source())
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error":   err.Error(),
			"profile": req.Profile,
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"routing": res, "profile": req.Profile})
}

func (s *Server) handleListPresets(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"presets":  routing.Presets(),
		"defaults": routing.DefaultPresetOpts(),
	})
}

// handleApplyPreset compiles one of the catalogue's mixes against this
// programme's ingest.
//
// Scoped through the query rather than the body: the body is a
// FULL-REPLACEMENT routing.PresetOpts (see below) and has no room for a field
// that is not a preset option. Same family as #497 -- a preset applied against
// the default programme's track layout picks track indices out of an ingest the
// destination does not read.
func (s *Server) handleApplyPreset(w http.ResponseWriter, r *http.Request) {
	eng, ok := s.scopedEngine(w, r)
	if !ok {
		return
	}
	// A body is optional: without one the OBS-convention defaults apply --
	// which is what GET /routing/presets advertises under "defaults", and what
	// this handler's own comment has always claimed.
	//
	// It was not true. The old code started from `var opts routing.PresetOpts`,
	// so a bodyless apply ran with every track index at Go's zero value, and
	// "mic only" compiled [0:a:0] -- the full mix -- while the catalogue two
	// routes away advertised micTrack 2. The advertised value and the applied
	// value disagreed, and only a client that sent a body ever got the one it
	// was promised.
	opts := routing.DefaultPresetOpts()

	// Read the body unconditionally rather than gating on Content-Length.
	// A chunked request has ContentLength -1 WHATEVER it is carrying, so the
	// old `r.ContentLength > 0` guard silently discarded a preset body that
	// was really there and applied the defaults instead.
	body, ok := readJSONBody(w, r)
	if !ok {
		return
	}
	if len(bytes.TrimSpace(body)) > 0 {
		// FULL REPLACEMENT, not a patch over the defaults. A body means the
		// client is stating every option it cares about, and an omitted field
		// has always meant zero: {"micTrack":1} selects track 2 and leaves
		// cleanTrack at 0, not at the advertised 1. Decoding on top of the
		// defaults would quietly change what every existing partial body
		// means, which is a larger break than the one being fixed.
		opts = routing.PresetOpts{}
		if err := decodeJSONInto(body, &opts); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	profile, err := routing.ApplyPreset(chi.URLParam(r, "preset"), eng.Source(), opts)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	res, err := routing.Compile(profile, eng.Source())
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"profile": profile, "routing": res})
}

// ------------------------------------------------------- destination presets

// handlePlatformPresets returns the platform destination catalogue: the
// transport, the ingest URL template and the notes for every platform we can
// describe, grouped for a picker that has to stay navigable at thirty entries.
//
// It is a static catalogue served over HTTP rather than baked into the UI so
// that scripted setups and any other client get the same answer the dialog
// gets, and so a preset can be corrected without shipping a new bundle.
//
// Presets carry no bitrate or resolution numbers — those belong to renditions,
// where they are already offered with a disclaimer. What they do carry is a
// URL that is either documented or an obvious {placeholder} template, and an
// empty URL wherever the platform issues its ingest per account or per event.
// The disclaimer travels with the payload because a preset the operator was
// not warned about is a preset they will trust further than we can.
func (s *Server) handlePlatformPresets(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"presets":    db.DestinationPresets(),
		"groups":     db.PresetGroups(),
		"disclaimer": db.PlatformPresetDisclaimer,
	})
}

// handlePlatformCapabilities serves the honest capability matrix: per platform,
// whether polyemesis can sign in, fetch the stream key, push metadata, read and
// send chat, moderate, and report viewers.
//
// It sits beside the preset catalogue rather than inside it because the two
// answer different questions. A preset says "here is the URL to type"; the
// matrix says "here is what you get after you have typed it", which is what
// somebody deciding whether to spend an evening on Meta's App Review actually
// needs. Joining them by preset id lets the destination dialog show both.
//
// Nothing on this endpoint gates anything. Unverified capabilities are reported
// as unverified and still attempted — see the sourcing rule in
// internal/oauth/capabilities.go.
func (s *Server) handlePlatformCapabilities(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"platforms": oauth.PlatformCapabilities(),
		"columns":   oauth.CapabilityColumns(),
		"support":   oauth.SupportLegend(),
		"tiers":     oauth.TierLegend(),
	})
}

// ---------------------------------------------------------------- recordings

func (s *Server) handleListRecordings(w http.ResponseWriter, r *http.Request) {
	recs, err := s.store.ListRecordings()
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, recs)
}

func (s *Server) handleRecordingUsage(w http.ResponseWriter, r *http.Request) {
	usage, err := s.recordings().Usage()
	if err != nil {
		writeStoreError(w, err)
		return
	}
	usage.Storage = s.storageVerdict()
	writeJSON(w, http.StatusOK, usage)
}

func (s *Server) handleDeleteRecording(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	if err := s.recordings().Delete(id); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (s *Server) handleDownloadRecording(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	rec, err := s.store.GetRecording(id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	// Resolve confines the path to the recordings directory; the filename
	// originates from a database row and is never trusted as a path.
	path, err := s.recordings().Resolve(rec.Filename)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	f, err := os.Open(path)
	if err != nil {
		writeError(w, http.StatusNotFound, "recording file is missing from disk")
		return
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	w.Header().Set("Content-Type", "video/x-matroska")
	w.Header().Set("Content-Disposition",
		fmt.Sprintf("attachment; filename=%q", filepath.Base(rec.Filename)))
	// ServeContent gives us range requests, so a partially downloaded
	// multi-gigabyte segment can be resumed.
	http.ServeContent(w, r, rec.Filename, stat.ModTime(), f)
}

// ---------------------------------------------------------------- processes

// processSummary and processDetail are what these two endpoints say about a
// process. They are named rather than inlined so that what leaves the API can
// be asserted directly, without standing up an engine to own a process first --
// which is why this was the one egress path where nobody noticed the redaction
// policy was not being applied.
//
// Neither reads Args(). CommandString and Logs are the masked renderings;
// Args() is the raw argv, and on a destination it carries the stream key and,
// with backup ingest on, the backup key as well.
func processSummary(p *supervisor.Process) map[string]any {
	return map[string]any{
		"status":  p.Status(),
		"command": p.CommandString(),
	}
}

func processDetail(name string, p *supervisor.Process) map[string]any {
	return map[string]any{
		"name":    name,
		"command": p.CommandString(),
		"lines":   p.Logs(),
	}
}

// handleListProcesses and handleProcessLogs are the #497 sibling with no id of
// its own to be scoped by.
//
// A process is named "ingest", "recorder", "preview", "meters" -- IDENTICALLY
// in every engine, because the name says what the child does and not which
// programme it does it for. So a request for programme 2's FFmpeg log matched
// programme 1's process and served it, and nothing in the answer said which
// programme it came from: an operator diagnosing a failing broadcast was
// reading a healthy one's log. Naming them per source would fix it at the
// source but the names are the engine's and the supervisor's, not this
// package's; scoping the request is the half that fits here, and it refuses
// rather than answering for the wrong programme.
func (s *Server) handleListProcesses(w http.ResponseWriter, r *http.Request) {
	eng, ok := s.scopedEngine(w, r)
	if !ok {
		return
	}
	procs := eng.Processes()
	out := make([]map[string]any, 0, len(procs))
	for _, p := range procs {
		out = append(out, processSummary(p))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleProcessLogs(w http.ResponseWriter, r *http.Request) {
	eng, ok := s.scopedEngine(w, r)
	if !ok {
		return
	}
	// Unescaped by hand because chi routes on RawPath whenever it differs from
	// Path, so URLParam hands back the still-encoded segment. Every process
	// whose name contains a colon — "dest:1", "rendition:2", "playout:source" —
	// arrives here as "dest%3A1" and matches nothing. A name that will not
	// unescape is compared as it arrived rather than rejected: the answer is a
	// 404 either way, and there is no reason to have two ways of saying it.
	name := chi.URLParam(r, "name")
	if decoded, err := url.PathUnescape(name); err == nil {
		name = decoded
	}
	for _, p := range eng.Processes() {
		if p.Name() == name {
			writeJSON(w, http.StatusOK, processDetail(name, p))
			return
		}
	}
	writeError(w, http.StatusNotFound, "no such process")
}

// ---------------------------------------------------------------------- HLS

// hlsSourceHandler serves ONE source's preview, at /hls/{source}/...
//
// Source-qualified because the preview is a filesystem resource and there is one
// engine per source: while every engine shared a directory, the one that became
// default deleted the live playlist of the one it replaced. See Config.HLSDirFor.
//
// The engine is resolved per request rather than captured, because which engines
// exist changes as sources are created and removed.
func (s *Server) hlsSourceHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(chi.URLParam(r, "source"), 10, 64)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		e := s.mgr.Engine(id)
		if e == nil {
			// A source that does not exist, or one whose engine has not been
			// built yet. 404 rather than 400: to a player this is the same
			// "nothing here yet" it already retries through, and a deleted
			// source should not read as a client error to a tab left open.
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		if strings.HasSuffix(r.URL.Path, ".m3u8") {
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			e.PreviewRequested()
		}
		http.StripPrefix("/hls/"+strconv.FormatInt(id, 10)+"/",
			http.FileServer(http.Dir(s.cfg.HLSDirFor(id)))).ServeHTTP(w, r)
	})
}

func (s *Server) hlsHandler() http.Handler {
	return http.StripPrefix("/hls/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The playlist is rewritten every segment; caching it would freeze the
		// preview a few seconds after it starts.
		w.Header().Set("Cache-Control", "no-store")

		// READ ONCE. Two calls to engOrNil are two different engine sets -- the
		// last source can be deleted between them -- so the playlist poke and
		// the directory must come from the same answer, or they can disagree
		// about which source this request is for.
		//
		// The nil branch is not hypothetical: /hls is registered outside
		// /api/v1, and Dashboard.tsx mounts PreviewPlayer before GET /settings
		// has answered, so on an install with no source this is one of the first
		// requests the browser makes. PreviewRequested is a mutation and must
		// keep panicking on a nil receiver, so the check is here.
		e := s.engOrNil()

		if strings.HasSuffix(r.URL.Path, ".m3u8") {
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			// The encoder is on-demand, and playlist polling is what keeps it
			// alive. Only the playlist counts: a player can keep pulling
			// segments from a manifest it already holds, so segment requests
			// are not evidence anyone is still watching a live stream.
			//
			// A request that starts the encoder is answered 404 below, since
			// ffmpeg has not written the playlist yet. hls.js retries a failed
			// manifest load, so the player recovers within a segment or two.
			if e != nil {
				e.PreviewRequested()
			}
		}

		// Resolved per request rather than captured: the default source can
		// change under a running server, and a directory fixed at construction
		// would go on serving the source it happened to start with.
		//
		// Falling through to the file server rather than refusing when there is
		// no engine: the directory is real and possibly non-empty, and a 404 for
		// a playlist nothing is writing is the same answer a live install gives
		// in the seconds before the encoder starts.
		dir := s.cfg.HLSDir()
		if e != nil {
			dir = s.cfg.HLSDirFor(e.SourceID())
		}
		http.FileServer(http.Dir(dir)).ServeHTTP(w, r)
	}))
}

// requireNamedSource refuses a create whose body does not say which programme
// the new thing belongs to.
//
// THE SERVER USED TO CHOOSE, AND THAT IS THE BUG THIS CLOSES.
// db.CreateDestination and db.CreateRendition fill an omitted source_id with
// DefaultSourceID(), which is the first source by id. On a one-source install
// that is the only possible answer and no choice was ever made. On an install
// with several it is a coin flip the operator neither made nor saw: the
// destination is created, attached to a programme nobody picked, and nothing on
// screen says which. It then carries the wrong feed, or sits idle while its
// intended programme streams without it.
//
// The store keeps its fallback -- it is reached by a hundred test fixtures that
// legitimately mean "on the default source", and by nothing else once this
// guard is in front of the two handlers that serve real clients. The rule
// belongs HERE, at the boundary where a human or a client omits a choice,
// rather than in the store, where the callers are our own and have already
// decided.
//
// The refusal names the programmes to choose from. A 400 that says "sourceId is
// required" tells an operator what they failed to type; one that lists the
// sources tells them what to type instead, which is the difference between a
// validation message and a usable one.
func (s *Server) requireNamedSource(w http.ResponseWriter, id *int64) bool {
	if id != nil && *id != 0 {
		return true
	}
	msg := "this create must say which source it belongs to: set \"sourceId\"." + s.availableSources()
	writeErrorCode(w, http.StatusBadRequest, codeSourceRequired, msg)
	return false
}
