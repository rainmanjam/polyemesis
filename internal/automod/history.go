package automod

import (
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// The history checker: a SEQUENCE from one author.
//
// This is the layer neither of the others can replace. Rate and repetition are
// properties of a sequence, not a message, so no per-message classifier --
// regex or model -- can see them. Ten identical messages are individually
// innocuous and collectively the commonest form of chat abuse there is; a model
// shown one of them in isolation has no basis to object.
//
// It runs against a small in-memory ring per author rather than the database.
// The hot path is every message of a live chat, and a query per message would
// make the defence the bottleneck.

// HistoryLimits are the thresholds. All configurable, all with defaults chosen
// to be quiet: a detector that fires on ordinary enthusiasm trains an operator
// to ignore it, which is worse than not having it.
type HistoryLimits struct {
	// Window is how far back the detectors look.
	Window time.Duration `json:"window"`
	// MaxMessages in Window before it counts as flooding.
	MaxMessages int `json:"maxMessages"`
	// MaxRepeats of the same normalised text in Window.
	MaxRepeats int `json:"maxRepeats"`
	// MaxLinks in Window.
	MaxLinks int `json:"maxLinks"`
	// MaxMentionsPerMessage before it counts as mention spam.
	MaxMentionsPerMessage int `json:"maxMentionsPerMessage"`
	// MinLengthForCaps is the length below which a shouty message is just a
	// short one. "OK" and "WHAT" are not shouting.
	MinLengthForCaps int `json:"minLengthForCaps"`
	// MaxCapsRatio before it counts as shouting, 0..1.
	MaxCapsRatio float64 `json:"maxCapsRatio"`
	// Action is what a history finding asks for. Timeout by default: flooding
	// is usually a person getting carried away rather than an attack, and a
	// timeout expires on its own where a ban needs a human to undo.
	Action Action `json:"action"`
	// TimeoutSeconds for that action.
	TimeoutSeconds int `json:"timeoutSeconds"`
	// Retain caps how many messages are kept per author.
	Retain int `json:"retain"`
	// IdleEviction is how long an author is kept after their last message. A
	// raid is thousands of new authors in a minute, so the ring has to forget
	// or the defence becomes the denial of service.
	IdleEviction time.Duration `json:"idleEviction"`
}

// DefaultHistoryLimits are deliberately forgiving.
func DefaultHistoryLimits() HistoryLimits {
	return HistoryLimits{
		Window:                30 * time.Second,
		MaxMessages:           8,
		MaxRepeats:            3,
		MaxLinks:              3,
		MaxMentionsPerMessage: 5,
		MinLengthForCaps:      12,
		MaxCapsRatio:          0.8,
		Action:                ActionTimeout,
		TimeoutSeconds:        60,
		Retain:                24,
		IdleEviction:          10 * time.Minute,
	}
}

type entry struct {
	at   time.Time
	norm string
}

type authorLog struct {
	entries []entry
	last    time.Time
}

// History tracks recent messages per author, per platform.
type History struct {
	mu     sync.Mutex
	limits HistoryLimits
	// Keyed on platform+author_id, never on display name: the same name on two
	// platforms is not the same person, and author_id is the stable identifier
	// on all four.
	authors map[string]*authorLog
	now     func() time.Time
}

// NewHistory returns a tracker.
func NewHistory(limits HistoryLimits) *History {
	return &History{
		limits:  limits,
		authors: map[string]*authorLog{},
		now:     time.Now,
	}
}

func authorKey(p db.Platform, authorID string) string {
	return string(p) + "\x00" + authorID
}

// Observe records a message and returns what the sequence now looks like.
//
// Recording and checking are one call because they must not race: two
// goroutines checking before either records would each see a count one short.
func (h *History) Observe(p db.Platform, authorID, text string) []Finding {
	if h == nil || authorID == "" {
		return nil
	}
	now := h.now()
	norm := Normalise(text)

	h.mu.Lock()
	defer h.mu.Unlock()

	h.evictLocked(now)

	key := authorKey(p, authorID)
	log := h.authors[key]
	if log == nil {
		log = &authorLog{}
		h.authors[key] = log
	}
	log.entries = append(log.entries, entry{at: now, norm: norm})
	log.last = now
	if len(log.entries) > h.limits.Retain {
		log.entries = log.entries[len(log.entries)-h.limits.Retain:]
	}

	cutoff := now.Add(-h.limits.Window)
	var inWindow []entry
	for _, e := range log.entries {
		if e.at.After(cutoff) {
			inWindow = append(inWindow, e)
		}
	}

	var out []Finding
	add := func(reason string) {
		out = append(out, Finding{
			Checker:        CheckerHistory,
			Action:         h.limits.Action,
			TimeoutSeconds: h.limits.TimeoutSeconds,
			Reason:         reason,
		})
	}

	if n := len(inWindow); h.limits.MaxMessages > 0 && n > h.limits.MaxMessages {
		add(fmt.Sprintf("%d messages in %s", n, h.limits.Window))
	}

	if h.limits.MaxRepeats > 0 && norm != "" {
		repeats := 0
		for _, e := range inWindow {
			if e.norm == norm {
				repeats++
			}
		}
		if repeats > h.limits.MaxRepeats {
			add(fmt.Sprintf("the same message %d times in %s", repeats, h.limits.Window))
		}
	}

	if h.limits.MaxLinks > 0 {
		links := 0
		for _, e := range inWindow {
			links += countLinks(e.norm)
		}
		if links > h.limits.MaxLinks {
			add(fmt.Sprintf("%d links in %s", links, h.limits.Window))
		}
	}

	if h.limits.MaxMentionsPerMessage > 0 {
		if n := strings.Count(text, "@"); n > h.limits.MaxMentionsPerMessage {
			add(fmt.Sprintf("%d mentions in one message", n))
		}
	}

	if r, ok := capsRatio(text, h.limits.MinLengthForCaps); ok &&
		h.limits.MaxCapsRatio > 0 && r > h.limits.MaxCapsRatio {
		add(fmt.Sprintf("%.0f%% upper case", r*100))
	}

	return sortByConsequence(out)
}

// evictLocked drops authors who have gone quiet. Called on every observation
// rather than on a timer: a raid is exactly when a timer goroutine is least
// likely to get scheduled, and it is exactly when this matters.
func (h *History) evictLocked(now time.Time) {
	if h.limits.IdleEviction <= 0 {
		return
	}
	cutoff := now.Add(-h.limits.IdleEviction)
	for k, log := range h.authors {
		if log.last.Before(cutoff) {
			delete(h.authors, k)
		}
	}
}

// Tracked reports how many authors are held, so a test can assert the ring does
// not grow without bound and an operator can see it.
func (h *History) Tracked() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.authors)
}

func countLinks(s string) int {
	n := strings.Count(s, "http://") + strings.Count(s, "https://")
	if n == 0 {
		// A bare domain is still a link to anybody reading it, and dropping the
		// scheme is the obvious way past a filter that only counts "http".
		n = strings.Count(s, "www.")
	}
	return n
}

// capsRatio reports the proportion of cased letters that are upper case, and
// whether the message is long enough for the question to mean anything.
func capsRatio(s string, minLength int) (float64, bool) {
	var upper, letters int
	for _, r := range s {
		if !unicode.IsLetter(r) {
			continue
		}
		letters++
		if unicode.IsUpper(r) {
			upper++
		}
	}
	if letters < minLength {
		return 0, false
	}
	return float64(upper) / float64(letters), true
}
