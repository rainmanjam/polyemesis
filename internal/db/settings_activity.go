package db

import (
	"encoding/json"
	"fmt"
	"math/big"
	"slices"
	"strings"

	"github.com/rainmanjam/polyemesis/internal/activity"
	"github.com/rainmanjam/polyemesis/internal/platform"
)

// ActivitySettings is how viewer activity (internal/activity: follows, subs,
// tips, raids) is filtered. It lives in the settings JSON and nowhere else:
// activity itself is held in memory and sent outward, never stored, so there is
// no table and no migration. A blob written before this field existed reads as
// DefaultActivitySettings, as Chat and Automod did when they were added.
type ActivitySettings struct {
	// Enabled is the master switch. Off, the hub drops everything -- the
	// console feed, the events stream and hooks included -- and says so.
	Enabled bool `json:"enabled"`
	// Kinds is, per platform, the kinds that are on. A platform with no entry
	// uses the defaults (every kind except the reaction tally). An entry is
	// exactly the list: [] means that platform's activity is off.
	Kinds map[string][]string `json:"kinds,omitempty"`
	// MinTip limits tips on the OVERLAY ONLY: the console and hooks always see
	// every tip. One row per currency or unit, because amounts are never
	// converted -- a US$5 floor says nothing about a KICK or a yen.
	MinTip []MinAmount `json:"minTip,omitempty"`
	// OverlayMessages puts the viewer's own words on the overlay, after automod
	// has passed them. Off, an alert shows the name and the amount only.
	OverlayMessages bool `json:"overlayMessages"`
	// Reactions turns on the aggregate reaction tally (Facebook). Off by
	// default: it is a poll of the platform that no streamer asked for.
	Reactions bool `json:"reactions"`
}

// MinAmount is one minimum: money in a currency OR a platform unit, never both.
type MinAmount struct {
	Currency string `json:"currency,omitempty"` // ISO 4217
	Minor    int64  `json:"minor,omitempty"`
	Unit     string `json:"unit,omitempty"` // "bits" | "kicks"
	Units    int64  `json:"units,omitempty"`
}

// Activity settings bounds.
const (
	MaxActivityMinAmounts = 32
	MaxActivityUnitRunes  = 16
)

// DefaultActivitySettings is what an install runs with until somebody changes
// it: on, every kind but the reaction tally, messages on the overlay (gated by
// automod). Nothing flows until a source exists, so on by default is harmless.
func DefaultActivitySettings() ActivitySettings {
	return ActivitySettings{Enabled: true, OverlayMessages: true}
}

// UnmarshalJSON replaces Kinds when the document names it. A JSON object
// decoded over an existing map MERGES, so without this a platform could never
// be put back on the defaults: its key would survive every save.
func (a *ActivitySettings) UnmarshalJSON(b []byte) error {
	type plain ActivitySettings
	var probe struct {
		Kinds json.RawMessage `json:"kinds"`
	}
	if err := json.Unmarshal(b, &probe); err != nil {
		return err
	}
	if probe.Kinds != nil {
		a.Kinds = nil
	}
	return json.Unmarshal(b, (*plain)(a))
}

// DefaultActivityKinds are the kinds on for a platform with no entry in Kinds: all of
// them except the reaction tally, which Reactions controls.
func DefaultActivityKinds() []activity.Kind {
	var out []activity.Kind
	for _, k := range activity.AllKinds() {
		if k != activity.KindReaction {
			out = append(out, k)
		}
	}
	return out
}

// KindEnabled reports whether a platform's kind is on. The reaction tally also
// needs Reactions, wherever the list stands.
func (a ActivitySettings) KindEnabled(platform string, kind activity.Kind) bool {
	if !a.Enabled {
		return false
	}
	if kind == activity.KindReaction && !a.Reactions {
		return false
	}
	if list, ok := a.Kinds[platform]; ok {
		return slices.Contains(list, string(kind))
	}
	return kind != activity.KindReaction || a.Reactions
}

// BelowMinimum reports whether a tip is under the configured minimum for its
// own currency or unit. A tip in a currency or unit with no row is never below
// one: there is nothing to compare it with, and amounts are not converted.
func (a ActivitySettings) BelowMinimum(amt *activity.Amount) bool {
	if amt == nil {
		return false
	}
	for _, m := range a.MinTip {
		switch {
		case m.Currency != "" && amt.Currency == m.Currency:
			return belowScaled(amt, m)
		case m.Unit != "" && amt.Unit == m.Unit:
			return amt.Units < m.Units
		}
	}
	return false
}

// belowScaled compares a tip with a minimum in the same currency at the same
// scale. A minimum's Minor is in the currency's own standard minor unit
// (activity.ExponentFor); the tip carries its exponent, so both are brought to
// a common scale before comparing -- a 500 JPY tip against a US-style 2-digit
// minimum is otherwise off by a factor of a hundred. A currency with no standard
// exponent is compared as written.
func belowScaled(amt *activity.Amount, m MinAmount) bool {
	std, known := activity.ExponentFor(amt.Currency)
	if !known || std == amt.Exponent {
		return amt.Minor < m.Minor
	}
	// tip = amt.Minor / 10^amt.Exponent, floor = m.Minor / 10^std.
	// tip < floor  <=>  amt.Minor * 10^std < m.Minor * 10^amt.Exponent.
	l := new(big.Int).Mul(big.NewInt(amt.Minor), pow10(std))
	r := new(big.Int).Mul(big.NewInt(m.Minor), pow10(amt.Exponent))
	return l.Cmp(r) < 0
}

func pow10(n int) *big.Int {
	return new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(max(n, 0))), nil)
}

func (a ActivitySettings) problems() []string {
	probs := a.kindProblems()
	return append(probs, a.minTipProblems()...)
}

// kindProblems checks the activity kinds each platform is listed for.
func (a ActivitySettings) kindProblems() []string {
	var probs []string
	known := map[string]bool{}
	for _, r := range platform.ChatOverlays() {
		known[r.Enum] = true
	}
	for p, list := range a.Kinds {
		if !known[p] {
			probs = append(probs, fmt.Sprintf("activity kinds name %q, which is not a platform with chat", p))
		}
		seen := map[string]bool{}
		for _, k := range list {
			if _, ok := activity.ParseKind(k); !ok {
				probs = append(probs, fmt.Sprintf("activity kind %q (%s) is not one this build knows", k, p))
			}
			if seen[k] {
				probs = append(probs, fmt.Sprintf("activity kind %q is listed twice for %s", k, p))
			}
			seen[k] = true
		}
	}
	return probs
}

// minTipProblems checks the minimum-amount rows, their count and their
// duplicates.
func (a ActivitySettings) minTipProblems() []string {
	var probs []string
	if len(a.MinTip) > MaxActivityMinAmounts {
		probs = append(probs, fmt.Sprintf("activity minimums: %d rows is more than %d", len(a.MinTip), MaxActivityMinAmounts))
	}
	seen := map[string]bool{}
	for i, m := range a.MinTip {
		problem, skipDup := minTipShapeProblem(i, m)
		if problem != "" {
			probs = append(probs, problem)
		}
		if skipDup {
			continue
		}
		id := m.Currency + "|" + m.Unit
		if seen[id] {
			probs = append(probs, fmt.Sprintf("activity minimum %d repeats %s", i+1, strings.Trim(id, "|")))
		}
		seen[id] = true
	}
	return probs
}

// minTipShapeProblem checks one minimum row on its own. It returns the problem
// (empty when the row is fine) and whether the row is too malformed for a
// duplicate check to mean anything.
func minTipShapeProblem(i int, m MinAmount) (problem string, skipDup bool) {
	money := m.Currency != ""
	unit := m.Unit != "" || m.Units != 0
	switch {
	case money && unit:
		return fmt.Sprintf("activity minimum %d is money or a unit, not both", i+1), true
	case !money && !unit:
		return fmt.Sprintf("activity minimum %d needs a currency or a unit", i+1), true
	case money && (len(m.Currency) != 3 || m.Currency != strings.ToUpper(m.Currency)):
		return fmt.Sprintf("activity minimum %d: %q is not an upper-case ISO 4217 code", i+1, m.Currency), false
	case money && m.Minor < 0, !money && m.Units < 0:
		return fmt.Sprintf("activity minimum %d cannot be negative", i+1), false
	case !money && (m.Unit == "" || len([]rune(m.Unit)) > MaxActivityUnitRunes || m.Unit != strings.ToLower(m.Unit)):
		return fmt.Sprintf("activity minimum %d: the unit must be a lower-case name of at most %d characters", i+1, MaxActivityUnitRunes), false
	}
	return "", false
}
