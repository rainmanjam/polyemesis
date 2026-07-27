package playout

import (
	"crypto/rand"
	"encoding/binary"
	"hash/fnv"
	"sort"
	"sync"
	"time"
)

// RequestKind is what a viewer just asked for. Both kinds keep a session alive,
// unlike the dashboard preview where only playlist polls count.
//
// The preview can ignore segments because it is one admin watching a live edge
// and a stalled player stops polling within seconds. Playout cannot: a viewer
// seeking inside a DVR window pulls a burst of segments off a manifest they
// already hold, and counting them as idle would report an empty stream while
// hundreds of people were watching it.
type RequestKind string

const (
	RequestPlaylist RequestKind = "playlist"
	RequestSegment  RequestKind = "segment"
)

// defaultPruneInterval bounds how often the expiry walk runs. The walk is O(n)
// over a table capped at MaxSessions, and running it on every request would put
// that walk in front of every segment on a busy origin.
const defaultPruneInterval = 2 * time.Second

// Viewer is one counted session: a client address watching one variant.
//
// The address is stored as a salted hash and never as text. Counting viewers
// needs identity to be stable, not legible, and an in-memory table of the IP
// addresses of everyone watching a public stream is a liability nobody asked
// for. The salt is per-process, so the hashes do not even survive a restart.
type viewerKey struct {
	addr    uint64
	variant string
}

type viewer struct {
	first time.Time
	last  time.Time
	hits  int64
}

// Analytics is the viewer picture the API reports. Counts only: there is
// deliberately nothing here that identifies a viewer.
type Analytics struct {
	// Viewers is the number of sessions currently inside the idle window.
	Viewers int `json:"viewers"`
	// ByVariant breaks that down by which rung they are watching, which is the
	// number that tells an operator whether the ladder is earning its CPU.
	ByVariant map[string]int `json:"byVariant"`
	// Peak is the high-water mark since the process started, and PeakAt when it
	// was set.
	Peak   int        `json:"peak"`
	PeakAt *time.Time `json:"peakAt,omitempty"`
	// Sessions is how many sessions have been opened in total, counting a
	// viewer who went away and came back as two.
	Sessions int64 `json:"sessions"`
	// Requests is every playlist and segment request that was counted.
	Requests int64 `json:"requests"`
	// Uncounted is requests from new viewers that arrived with the table full.
	// Non-zero means the reported numbers are a floor, not a total, and the cap
	// wants raising.
	Uncounted int64 `json:"uncounted"`
	// IdleSeconds and Capacity are echoed so a reader can tell whether a
	// surprising number is a real one or a configuration artefact.
	IdleSeconds int `json:"idleSeconds"`
	Capacity    int `json:"capacity"`
}

// Sessions counts active viewers by client address, in memory and bounded.
//
// Bounded is the whole design constraint: this is fed directly by unauthenticated
// public requests, so an attacker choosing addresses must not be able to grow it.
// Past the cap, new viewers are served exactly as before and simply go
// uncounted — accounting is never allowed to be the reason a stream stops.
type Sessions struct {
	mu       sync.Mutex
	idle     time.Duration
	max      int
	salt     uint64
	viewers  map[viewerKey]*viewer
	lastPr   time.Time
	peak     int
	peakAt   time.Time
	sessions int64
	requests int64
	skipped  int64
}

// NewSessions creates a tracker. Zero or negative arguments take the defaults
// rather than disabling the tracker, so a settings blob written before playout
// existed still produces something that counts.
func NewSessions(idle time.Duration, max int) *Sessions {
	s := &Sessions{viewers: map[viewerKey]*viewer{}, salt: randomSalt()}
	s.SetLimits(idle, max)
	return s
}

func randomSalt() uint64 {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		// A predictable salt weakens the hash against someone who already has
		// the address they want to test for. That is a far smaller problem than
		// refusing to serve video, so this falls open.
		return uint64(time.Now().UnixNano())
	}
	return binary.LittleEndian.Uint64(b[:])
}

// SetLimits applies changed settings without discarding the sessions already
// counted; a viewer must not disappear from the dashboard because the idle
// timeout was nudged.
func (s *Sessions) SetLimits(idle time.Duration, max int) {
	if idle <= 0 {
		idle = 20 * time.Second
	}
	if max <= 0 {
		max = 5000
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.idle, s.max = idle, max
}

// Observe records one request from addr for variant.
//
// addr is whatever the caller decided identifies a client — normally the remote
// IP, or the forwarded one where a proxy is trusted. An empty addr is still
// counted, under one shared bucket, because the alternative is silently
// under-reporting every viewer behind a proxy we could not read.
func (s *Sessions) Observe(addr, variant string, kind RequestKind, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.requests++
	key := viewerKey{addr: s.hash(addr), variant: variant}

	if v, ok := s.viewers[key]; ok {
		// An entry that has already expired but has not been swept yet is a
		// returning viewer, not a continuing one: counting it as continuing
		// would hide every reconnect from the session total.
		if now.Sub(v.last) >= s.idle {
			s.sessions++
			v.first = now
			v.hits = 0
		}
		v.last = now
		v.hits++
		return
	}

	s.pruneLocked(now, false)
	if len(s.viewers) >= s.max {
		// Full is exactly when a stale entry costs something, so pay for the
		// exact sweep rather than turning away a viewer who could be counted.
		s.pruneLocked(now, true)
	}
	if len(s.viewers) >= s.max {
		s.skipped++
		return
	}
	s.viewers[key] = &viewer{first: now, last: now, hits: 1}
	s.sessions++
	s.markPeakLocked(now)
}

// pruneLocked drops expired sessions. force skips the rate limit, for the paths
// that need an exact count rather than a cheap one.
func (s *Sessions) pruneLocked(now time.Time, force bool) {
	if !force && now.Sub(s.lastPr) < defaultPruneInterval {
		return
	}
	s.lastPr = now
	for k, v := range s.viewers {
		if now.Sub(v.last) >= s.idle {
			delete(s.viewers, k)
		}
	}
}

// markPeakLocked may only be called when the table has just been pruned, so the
// high-water mark is a count of live viewers and not of unswept ones.
func (s *Sessions) markPeakLocked(now time.Time) {
	if len(s.viewers) > s.peak {
		s.peak = len(s.viewers)
		s.peakAt = now
	}
}

// Snapshot prunes and reports. It is the exact count, which is why it forces
// the sweep the request path rate-limits.
func (s *Sessions) Snapshot(now time.Time) Analytics {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.pruneLocked(now, true)
	s.markPeakLocked(now)

	a := Analytics{
		Viewers:     len(s.viewers),
		ByVariant:   map[string]int{},
		Peak:        s.peak,
		Sessions:    s.sessions,
		Requests:    s.requests,
		Uncounted:   s.skipped,
		IdleSeconds: int(s.idle / time.Second),
		Capacity:    s.max,
	}
	if !s.peakAt.IsZero() {
		at := s.peakAt
		a.PeakAt = &at
	}
	for k := range s.viewers {
		a.ByVariant[k.variant]++
	}
	return a
}

// Variants lists the variants with at least one viewer, busiest first. Used by
// the status endpoint, and kept separate from Snapshot so the map above stays a
// map rather than becoming an ordered structure nothing else needs.
func (s *Sessions) Variants(now time.Time) []string {
	counts := s.Snapshot(now).ByVariant
	out := make([]string, 0, len(counts))
	for name := range counts {
		out = append(out, name)
	}
	sort.Slice(out, func(i, j int) bool {
		if counts[out[i]] != counts[out[j]] {
			return counts[out[i]] > counts[out[j]]
		}
		return out[i] < out[j]
	})
	return out
}

// Reset clears the table and the counters, for a playout that has been switched
// off: leaving yesterday's peak on the dashboard of a stopped origin reads as a
// live number.
func (s *Sessions) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.viewers = map[viewerKey]*viewer{}
	s.peak, s.sessions, s.requests, s.skipped = 0, 0, 0, 0
	s.peakAt = time.Time{}
	s.lastPr = time.Time{}
}

func (s *Sessions) hash(addr string) uint64 {
	h := fnv.New64a()
	var salt [8]byte
	binary.LittleEndian.PutUint64(salt[:], s.salt)
	_, _ = h.Write(salt[:])
	_, _ = h.Write([]byte(addr))
	return h.Sum64()
}
