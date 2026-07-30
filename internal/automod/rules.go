package automod

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
)

// The rules checker: one message, in isolation, deterministically.
//
// Go's regexp is RE2 -- linear time, no backtracking -- so a crafted message
// cannot turn a filter into a denial of service. That is why rules can be spent
// freely here where in a backtracking engine an operator-supplied pattern would
// be a loaded gun.
//
// Deterministic matters beyond speed: a rule hit is reproducible and
// explainable after the fact, which is what makes it the evidence an operator
// can reasonably let take an automatic action.

// Rule is one operator-defined pattern.
type Rule struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
	// Pattern is a regular expression, matched case-insensitively against the
	// normalised message text.
	Pattern string `json:"pattern"`
	// Action is what a match asks for. Whether it HAPPENS is the matrix's
	// decision, not the rule's -- a rule proposes, the matrix disposes.
	Action Action `json:"action"`
	// TimeoutSeconds applies to ActionTimeout. Seconds here on purpose: Kick
	// counts minutes and the adapters convert at the last moment, so anything
	// upstream of them speaks one unit.
	TimeoutSeconds int `json:"timeoutSeconds,omitempty"`

	re *regexp.Regexp
}

// Compile prepares a rule for matching and reports a bad pattern to the
// operator rather than at match time, where it would be a silent no-match.
func (r *Rule) Compile() error {
	if strings.TrimSpace(r.Pattern) == "" {
		return fmt.Errorf("rule %q has an empty pattern", r.Name)
	}
	if !KnownActions(r.Action) {
		return fmt.Errorf("rule %q asks for unknown action %q", r.Name, r.Action)
	}
	// (?i) rather than lowercasing the subject: an operator writing a pattern
	// with a character class should not have to think about case folding, and
	// folding the subject would break any pattern that deliberately matches
	// upper case.
	re, err := regexp.Compile("(?i)" + r.Pattern)
	if err != nil {
		return fmt.Errorf("rule %q: %w", r.Name, err)
	}
	r.re = re
	return nil
}

// Match reports whether the rule fires on this text.
func (r *Rule) Match(text string) bool {
	if !r.Enabled || r.re == nil {
		return false
	}
	return r.re.MatchString(text)
}

// RuleSet is the compiled collection.
type RuleSet struct {
	rules []Rule
}

// NewRuleSet compiles every rule, refusing the whole set if any pattern is bad.
//
// All-or-nothing on purpose. Silently dropping the one rule that failed to
// compile leaves an operator believing a protection is in place when it is not
// -- the same silent-failure shape the capability gate exists to prevent.
func NewRuleSet(rules []Rule) (*RuleSet, error) {
	out := make([]Rule, 0, len(rules))
	for _, r := range rules {
		if err := r.Compile(); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return &RuleSet{rules: out}, nil
}

// Check returns the findings for one message, worst action first.
func (s *RuleSet) Check(text string) []Finding {
	if s == nil {
		return nil
	}
	norm := Normalise(text)
	despaced := ""
	if deliberatelySpaced(norm) {
		despaced = strings.ReplaceAll(norm, " ", "")
	}
	var out []Finding
	for i := range s.rules {
		r := &s.rules[i]
		// Three forms, each for a reason. Raw, because a pattern may
		// deliberately target punctuation normalisation removes. Normalised,
		// which defeats case, padding, homoglyphs and zero-width characters.
		// Despaced, but ONLY when the spacing looks deliberate -- see
		// deliberatelySpaced.
		if r.Match(norm) || r.Match(text) || (despaced != "" && r.Match(despaced)) {
			out = append(out, Finding{
				Checker:        CheckerRules,
				Action:         r.Action,
				TimeoutSeconds: r.TimeoutSeconds,
				Reason:         fmt.Sprintf("matched rule %q", r.Name),
				RuleID:         r.ID,
			})
		}
	}
	return sortByConsequence(out)
}

// Normalise folds a message down to the form the filters compare.
//
// Every step here exists because it is how a term gets past a naive filter:
// spacing it out, doubling letters, swapping in a homoglyph, or hiding it in
// zero-width characters. None of this is about tidiness.
func Normalise(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	var lastRune rune
	for _, r := range s {
		// Zero-width and other formatting characters are invisible to a reader
		// and split a word for a matcher, which is exactly why they get used.
		if unicode.Is(unicode.Cf, r) || r == '​' || r == '‌' || r == '‍' {
			continue
		}
		r = unicode.ToLower(foldHomoglyph(r))
		if unicode.IsSpace(r) {
			r = ' '
		}
		// Collapse runs of the same character: "sssspam" and "s p a m" both
		// reduce toward the term they are hiding.
		if r == lastRune && (r == ' ' || unicode.IsLetter(r)) {
			continue
		}
		b.WriteRune(r)
		lastRune = r
	}
	return strings.TrimSpace(b.String())
}

// deliberatelySpaced reports whether a message looks letter-spaced to evade a
// filter -- "b a d w o r d" -- rather than merely containing short words.
//
// The distinction matters and is the whole reason this is a heuristic rather
// than "strip all spaces". Stripping unconditionally turns "a bad wordsmith"
// into a match for "badword": the Scunthorpe problem, arriving by a slightly
// different road. A detector that fires on ordinary text trains an operator to
// ignore it, which is worse than not having it.
//
// So it triggers only on the shape the evasion actually takes: several tokens,
// most of them a single character.
func deliberatelySpaced(s string) bool {
	fields := strings.Fields(s)
	if len(fields) < 4 {
		return false
	}
	single := 0
	for _, f := range fields {
		if len([]rune(f)) == 1 {
			single++
		}
	}
	return single*2 > len(fields)
}

// foldHomoglyph maps the handful of look-alike characters actually used to
// evade filters back to ASCII. Deliberately small: a full confusables table is
// large, slow, and folds characters that legitimately differ in languages this
// product is translated into.
func foldHomoglyph(r rune) rune {
	switch r {
	case 'а': // Cyrillic a
		return 'a'
	case 'е': // Cyrillic e
		return 'e'
	case 'о': // Cyrillic o
		return 'o'
	case 'р': // Cyrillic r
		return 'p'
	case 'с': // Cyrillic s
		return 'c'
	case 'х': // Cyrillic h
		return 'x'
	case 'і': // Ukrainian i
		return 'i'
	case '0':
		return 'o'
	case '1', '|', '!':
		return 'i'
	case '3':
		return 'e'
	case '4':
		return 'a'
	case '$', '5':
		return 's'
	case '@':
		return 'a'
	}
	return r
}
