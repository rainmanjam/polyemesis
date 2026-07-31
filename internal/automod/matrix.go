// Package automod decides what to do about a chat message.
//
// Only the deciding half. The acting half already exists: internal/chat's Hub
// exposes Delete, Hide, HideLocally, Ban and Unban across four platforms, and
// upstream retraction works. Nothing here performs an action itself — it
// returns a Verdict and the caller acts, which is what keeps the audit trail
// in one place and this package testable without a network.
//
// Three checkers, cheapest first, each able to settle a message before the next
// one costs anything:
//
//	rules   one message in isolation, regex and predicates. Free.
//	history a SEQUENCE from one author. Free, and the only one that can see
//	        rate and repetition -- ten identical messages are individually
//	        innocuous and collectively the commonest abuse there is.
//	model   an external API, on what the first two could not settle. Paid,
//	        slow, and never on the hot path.
//
// See docs/roadmap/CHAT-AUTOMOD.md for the design and why it is shaped this way.
package automod

import (
	"fmt"
	"sort"
	"strings"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// Action is something that can be done about a message or its author.
type Action string

const (
	// ActionFlag records it for a human and does nothing else. The only action
	// that is on by default, because it cannot surprise anybody.
	ActionFlag Action = "flag"
	// ActionHideLocal hides the message in polyemesis only. Reversible, and
	// invisible to the platform.
	ActionHideLocal Action = "hide_local"
	// ActionHide hides it upstream. Only Facebook can do this reversibly.
	ActionHide Action = "hide"
	// ActionDelete removes it upstream. Not reversible.
	ActionDelete Action = "delete"
	// ActionTimeout silences the author for a duration. Expires on its own.
	ActionTimeout Action = "timeout"
	// ActionBan removes the author until a human lifts it.
	ActionBan Action = "ban"
)

// Actions is every action, in ascending order of consequence. The order is not
// decorative: the UI renders the matrix in it, so the most destructive row is
// always last and never the one an operator ticks by muscle memory.
var Actions = []Action{
	ActionFlag, ActionHideLocal, ActionHide, ActionDelete, ActionTimeout, ActionBan,
}

// Checker is the kind of evidence behind a decision.
type Checker string

const (
	// CheckerRules is deterministic: regex and predicates over one message.
	// Reproducible and explainable after the fact.
	CheckerRules Checker = "rules"
	// CheckerHistory is deterministic too, over a sequence from one author.
	CheckerHistory Checker = "history"
	// CheckerModel is an external model. Probabilistic, and cannot be replayed.
	CheckerModel Checker = "model"
)

// Checkers is every checker, cheapest first.
var Checkers = []Checker{CheckerRules, CheckerHistory, CheckerModel}

// Platforms is every platform automod can act on.
var Platforms = []db.Platform{
	db.PlatformYouTube, db.PlatformTwitch, db.PlatformKick, db.PlatformFacebook,
}

// Cell is one switch: may THIS checker take THIS action on THIS platform.
type Cell struct {
	Platform db.Platform `json:"platform"`
	Action   Action      `json:"action"`
	Checker  Checker     `json:"checker"`
	// Auto is the operator's choice. Meaningless when Available is false.
	Auto bool `json:"auto"`
	// Available reports whether the platform can perform the action at all.
	// Derived from capability, never stored: a cell the platform cannot do is
	// rendered inert with the reason rather than as an unticked box, because a
	// switch that silently does nothing is worse than no switch.
	Available bool `json:"available"`
	// Reason explains an unavailable cell, in the operator's terms.
	Reason string `json:"reason,omitempty"`
}

// Key identifies a cell.
type Key struct {
	Platform db.Platform
	Action   Action
	Checker  Checker
}

func (k Key) String() string {
	return fmt.Sprintf("%s/%s/%s", k.Platform, k.Action, k.Checker)
}

// Matrix is the whole switch grid plus the master switches over it.
//
// Stored as a set of the cells that are ON rather than a dense grid. A dense
// grid has to be migrated whenever an action, checker or platform is added, and
// the migration has to decide a default for cells nobody has ever seen. A
// sparse set answers that by construction: absent means off, which is the
// default anyway.
type Matrix struct {
	// Enabled is the global kill switch. False means automod decides nothing at
	// all, whatever any cell says.
	Enabled bool `json:"enabled"`
	// PlatformEnabled is the per-platform kill switch. An absent platform is
	// treated as enabled, so adding a platform does not silently disable it;
	// the global switch above is the one that fails closed.
	PlatformEnabled map[db.Platform]bool `json:"platformEnabled,omitempty"`
	// On holds the cells the operator has switched on.
	On map[string]bool `json:"on,omitempty"`
}

// DefaultMatrix is what a fresh install gets: automod on, but the only
// automatic action anywhere is flagging for review.
//
// Automod that acts on first install is automod that surprises somebody during
// a broadcast. Flagging cannot surprise anyone -- it changes nothing an
// audience sees -- so it is the one thing that starts on.
func DefaultMatrix() Matrix {
	on := map[string]bool{}
	for _, p := range Platforms {
		for _, c := range Checkers {
			on[Key{Platform: p, Action: ActionFlag, Checker: c}.String()] = true
		}
	}
	return Matrix{Enabled: true, On: on}
}

// Allows reports whether a checker may take an action on a platform.
//
// Every gate is consulted, and the capability gate is consulted LAST and is not
// overridable. A stored setting written before a capability changed must never
// become an action nobody can explain.
func (m Matrix) Allows(caps Capabilities, k Key) bool {
	if !m.Enabled {
		return false
	}
	if on, ok := m.PlatformEnabled[k.Platform]; ok && !on {
		return false
	}
	if !m.On[k.String()] {
		return false
	}
	ok, _ := caps.Can(k.Platform, k.Action)
	return ok
}

// Set switches one cell on or off.
func (m *Matrix) Set(k Key, auto bool) {
	if m.On == nil {
		m.On = map[string]bool{}
	}
	if auto {
		m.On[k.String()] = true
		return
	}
	// Deleted rather than stored false, so the stored form stays sparse and
	// "absent means off" remains the only rule a reader needs.
	delete(m.On, k.String())
}

// Cells renders the full grid for the UI, in a stable order.
func (m Matrix) Cells(caps Capabilities) []Cell {
	out := make([]Cell, 0, len(Platforms)*len(Actions)*len(Checkers))
	for _, p := range Platforms {
		for _, a := range Actions {
			available, reason := caps.Can(p, a)
			for _, c := range Checkers {
				k := Key{Platform: p, Action: a, Checker: c}
				out = append(out, Cell{
					Platform:  p,
					Action:    a,
					Checker:   c,
					Auto:      m.On[k.String()],
					Available: available,
					Reason:    reason,
				})
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Platform != out[j].Platform {
			return out[i].Platform < out[j].Platform
		}
		return actionRank(out[i].Action) < actionRank(out[j].Action)
	})
	return out
}

func actionRank(a Action) int {
	for i, x := range Actions {
		if x == a {
			return i
		}
	}
	return len(Actions)
}

// Summary is the collapsed per-platform view: how many automatic actions are
// armed. Sixty cells is a table nobody reads, so this is what the UI shows
// until an operator expands it.
func (m Matrix) Summary(caps Capabilities) map[db.Platform]int {
	out := map[db.Platform]int{}
	for _, p := range Platforms {
		n := 0
		for _, a := range Actions {
			// Flagging is not "an automatic action" in the sense an operator
			// means when they ask what this thing will do to their chat.
			if a == ActionFlag {
				continue
			}
			for _, c := range Checkers {
				if m.Allows(caps, Key{Platform: p, Action: a, Checker: c}) {
					n++
				}
			}
		}
		out[p] = n
	}
	return out
}

// ParseKey turns a stored key back into its parts.
func ParseKey(s string) (Key, error) {
	parts := strings.Split(s, "/")
	if len(parts) != 3 {
		return Key{}, fmt.Errorf("malformed automod key %q", s)
	}
	return Key{
		Platform: db.Platform(parts[0]),
		Action:   Action(parts[1]),
		Checker:  Checker(parts[2]),
	}, nil
}
