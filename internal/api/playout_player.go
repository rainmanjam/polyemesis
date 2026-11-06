package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"hash/maphash"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rainmanjam/polyemesis/internal/auth"
	"github.com/rainmanjam/polyemesis/internal/clipper"
	"github.com/rainmanjam/polyemesis/internal/jobs"
	"github.com/rainmanjam/polyemesis/internal/metrics"
	"github.com/rainmanjam/polyemesis/internal/playout"
	"github.com/rainmanjam/polyemesis/internal/scheduler"
)

// The public player's second half: what it reports back (QoE beacons) and what
// it shows when there is nothing live (the offline card). Both sit behind the
// same gate as the media -- see the access rules at the top of playout.go.

// --------------------------------------------------------------- QoE beacons

// playoutBeaconPath is the beacon endpoint. A constant because the router and
// the request logger (which keeps beacons out of the log) must agree on it.
const playoutBeaconPath = "/api/v1/playout/beacon"

// beaconGate is the endpoint's own state: the rate limiter, the salt its keys
// are hashed with, and how many beacons it has refused. All in memory.
type beaconGate struct {
	once     sync.Once
	limiter  *playout.BeaconLimiter
	seed     maphash.Seed
	invalid  atomic.Int64
	limited  atomic.Int64
	clockFor func() time.Time
}

func (s *Server) beaconGate() *beaconGate {
	g := &s.beacons
	g.once.Do(func() {
		g.limiter = playout.NewBeaconLimiter()
		// A per-process salt: the key a limiter bucket is filed under is not
		// the address and cannot be turned back into one by anybody who reads
		// the process memory without also having the seed.
		g.seed = maphash.MakeSeed()
		if g.clockFor == nil {
			g.clockFor = time.Now
		}
	})
	return g
}

// handlePlayoutBeacon accepts one QoE beacon from the public player.
//
// THE ORDER IS THE CONTRACT, and each step is placed where it is so the
// endpoint cannot be used to learn more than /playout/public would tell:
//
//  1. The gate, with the same verdicts /playout/public gives: 404 for a stream
//     that is not published, 401 for a published one without the token. A 429
//     answered first would tell a stranger that a hidden stream exists.
//  2. The rate limit.
//  3. The body: size-capped, unknown fields refused, trailing data refused,
//     every value checked against the schema -- and then, once a manager is
//     known to exist, against the running ladder.
//
// It never sets the playback cookie: a beacon is not a viewer arriving, and
// trading the token for a cookie here would make this a second way to mint the
// credential the gate exists to check.
func (s *Server) handlePlayoutBeacon(w http.ResponseWriter, r *http.Request) {
	playoutVaryingResponse(w)
	w.Header().Set(headerCacheControl, "no-store")

	switch access := s.authorizePlayout(r); access {
	case playoutDenyChallenge:
		writeError(w, http.StatusUnauthorized, "this stream requires a playback token")
		return
	default:
		if !access.ok() {
			http.NotFound(w, r)
			return
		}
	}

	g := s.beaconGate()
	key := maphash.String(g.seed, auth.ClientIP(r, s.proxies))
	if !g.limiter.Allow(key, g.clockFor()) {
		g.limited.Add(1)
		w.Header().Set("Retry-After", "15")
		writeError(w, http.StatusTooManyRequests, "too many beacons")
		return
	}

	b, err := decodeBeacon(w, r)
	if err == nil {
		err = playout.ValidateBeaconSchema(b)
	}
	if err != nil {
		g.invalid.Add(1)
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	m := s.playoutManager()
	if m == nil {
		writeError(w, http.StatusServiceUnavailable, "playout is not running")
		return
	}
	// The ladder half of validation needs the manager: a variant name is only
	// valid if a player could have loaded it.
	if err := m.RecordBeacon(b); err != nil {
		g.invalid.Add(1)
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// decodeBeacon reads exactly one beacon object. Anything else -- an oversized
// body, a field the schema does not name, a second value after the first -- is
// a refusal, so the schema in docs/API.md is the whole of what can arrive.
func decodeBeacon(w http.ResponseWriter, r *http.Request) (playout.Beacon, error) {
	var b playout.Beacon
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, playout.MaxBeaconBytes))
	if err != nil {
		return b, errors.New("invalid beacon: body too large or unreadable")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&b); err != nil {
		return b, errors.New("invalid beacon: " + err.Error())
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return b, errors.New("invalid beacon: trailing data after the object")
	}
	return b, nil
}

// playoutMetrics is the scrape's playout block, nil without a manager.
func (s *Server) playoutMetrics() *metrics.Playout {
	m := s.playoutManager()
	if m == nil {
		return nil
	}
	a := m.Analytics()
	q := a.QoE
	g := s.beaconGate()
	p := &metrics.Playout{
		Viewers:         a.Viewers,
		Beacons:         q.Beacons,
		Untracked:       q.Untracked,
		Startups:        q.Startups,
		StartupSeconds:  float64(q.StartupMs) / 1000,
		Rebuffers:       q.Rebuffers,
		RebufferSeconds: float64(q.RebufferMs) / 1000,
		Errors:          make(map[string]int64, len(q.Errors)),
		ErrorCodes:      make([]string, 0, len(playout.BeaconErrors)),
		Rejected: map[string]int64{
			"invalid":      g.invalid.Load(),
			"rate_limited": g.limited.Load(),
		},
		ActiveSessions: q.Active,
		ByVariant:      q.ByVariant,

		StartupBuckets:     q.StartupHistogram,
		SessionsWithErrors: q.SessionsWithErrors,
	}
	for _, ms := range playout.StartupBucketsMs {
		p.StartupBounds = append(p.StartupBounds, float64(ms)/1000)
	}
	for _, c := range playout.BeaconErrors {
		p.ErrorCodes = append(p.ErrorCodes, string(c))
	}
	for c, n := range q.Errors {
		p.Errors[string(c)] = n
	}
	if q.LatencyMsMedian != nil {
		v := float64(*q.LatencyMsMedian) / 1000
		p.LatencySeconds = &v
	}
	return p
}

// -------------------------------------------------------------- offline card

// playoutOfflineClipPath is the offline card's looping clip, as the public
// view names it. A constant so the view and the route cannot disagree.
const playoutOfflineClipPath = "/api/v1/playout/offline-clip"

// Bounds on what the operator may put on the offline card.
const (
	maxOfflineMessage = 500
	maxVODURL         = 2048
)

// validVODURL accepts an absolute http(s) URL and nothing else. It is rendered
// as a link on a page strangers open, so javascript: and data: are refused
// here rather than trusted to the renderer.
func validVODURL(raw string) bool {
	if raw == "" {
		return true
	}
	if len(raw) > maxVODURL {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return false
	}
	return u.Scheme == "http" || u.Scheme == "https"
}

// offlineClipExts are the containers a browser's <video> plays on its own. A
// buffer clip is MPEG-TS, which Chrome will not, so only exports qualify.
var offlineClipExts = map[string]bool{".mp4": true, ".m4v": true, ".mov": true, ".webm": true}

// offlineClipPath resolves the operator's chosen clip export to a file, or
// explains why it cannot be used.
func (s *Server) offlineClipPath(jobID int64) (string, error) {
	job, err := s.store.GetJob(jobID)
	if err != nil {
		return "", errors.New("no such clip export")
	}
	if job.Kind != clipper.JobKind || job.State != jobs.StateDone || len(job.Result) == 0 {
		return "", errors.New("that job is not a finished clip export")
	}
	var res clipper.JobResult
	if err := json.Unmarshal(job.Result, &res); err != nil || res.Path == "" {
		return "", errors.New("that job produced no file")
	}
	if res.Vertical {
		// The card plays in the 16:9 player, where a 9:16 short is a narrow
		// strip between black bars.
		return "", errors.New("that export is a 9:16 short; the offline card needs a landscape clip")
	}
	path, _, err := s.clipExportPath(res.Path)
	if err != nil {
		return "", err
	}
	if !offlineClipExts[strings.ToLower(filepath.Ext(path))] {
		return "", errors.New("the offline clip must be an MP4, MOV or WebM export")
	}
	// Exports can be deleted after they are chosen; a clip that is gone must
	// not be named to a viewer whose page would then load nothing.
	if st, err := os.Stat(path); err != nil || !st.Mode().IsRegular() {
		return "", errors.New("that clip is not on disk")
	}
	return path, nil
}

// handlePlayoutOfflineClip serves the looping clip the offline card plays.
//
// Gated exactly like the poster: the same decision, and the same deliberate
// absence of a basic-auth challenge -- a <video> tag on somebody's page must
// not raise a password prompt. Like the poster it is an /api/v1 URL, which the
// playback cookie (scoped to /playout/) does not reach, so the page puts the
// token in the query.
func (s *Server) handlePlayoutOfflineClip(w http.ResponseWriter, r *http.Request) {
	playoutVaryingResponse(w)
	if !s.authorizePlayout(r).ok() {
		http.NotFound(w, r)
		return
	}
	cfg := s.playoutStore().load()
	if cfg.OfflineClipJob <= 0 {
		http.NotFound(w, r)
		return
	}
	path, err := s.offlineClipPath(cfg.OfflineClipJob)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	f, err := os.Open(path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "clip unreadable")
		return
	}
	// Private: the answer depends on the credential, and a shared cache keyed
	// on the bare URL is exactly what the Vary above is there to stop.
	w.Header().Set(headerCacheControl, "private, max-age=300")
	// ServeContent answers Range, which is how a browser seeks and loops.
	http.ServeContent(w, r, filepath.Base(path), st.ModTime(), f)
}

// nextStartCache holds the next scheduled start for a short while: the public
// view is polled by every viewer, and the schedule table changes when an
// operator edits it, not per request.
type nextStartCache struct {
	mu   sync.Mutex
	at   time.Time
	when *time.Time
}

const nextStartTTL = 30 * time.Second

// nextScheduledStart is the earliest upcoming start across every enabled
// schedule that starts something -- a destination start or a playlist start.
// Only the instant leaves this function: schedule names and targets are the
// operator's, not the audience's.
func (s *Server) nextScheduledStart(now time.Time) *time.Time {
	c := &s.nextStart
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.at.IsZero() && now.Sub(c.at) < nextStartTTL && (c.when == nil || c.when.After(now)) {
		return c.when
	}
	c.at, c.when = now, nil
	list, err := s.store.ListSchedules()
	if err != nil {
		return nil
	}
	c.when = earliestStart(list, now)
	return c.when
}

// earliestStart is nextScheduledStart's decision as a pure function.
func earliestStart(list []scheduler.Schedule, now time.Time) *time.Time {
	var best *time.Time
	for _, sc := range list {
		if !sc.Enabled {
			continue
		}
		// An empty action is a start: Validate fills it in, and a row read
		// back from before the field existed carries none.
		if a := sc.Action; a != "" && a != scheduler.ActionStart && a != scheduler.ActionPlaylistStart {
			continue
		}
		at, ok := scheduler.Next(sc, now)
		if !ok {
			continue
		}
		if best == nil || at.Before(*best) {
			t := at.UTC()
			best = &t
		}
	}
	return best
}
