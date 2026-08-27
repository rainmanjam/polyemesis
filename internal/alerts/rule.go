package alerts

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/rainmanjam/polyemesis/internal/netguard"
)

// Format is the JSON shape a webhook endpoint expects. Discord and Slack are
// not integrations in any meaningful sense — they are two specific JSON bodies
// posted to a URL — but they are what this audience already has a channel for,
// so shipping the shapes is the difference between a feature that gets used and
// one that gets a "maybe later".
type Format string

const (
	FormatJSON    Format = "json"
	FormatDiscord Format = "discord"
	FormatSlack   Format = "slack"
)

// KnownFormat reports whether f can be encoded.
func KnownFormat(f Format) bool {
	switch f {
	case FormatJSON, FormatDiscord, FormatSlack:
		return true
	}
	return false
}

// Debounce and rate-limit bounds. The minimums are not zero: a rule with no
// debounce at all is how a flapping destination becomes two hundred messages,
// which is the specific outcome this package exists to prevent.
const (
	MinDebounceSeconds     = 1
	MaxDebounceSeconds     = 3600
	DefaultDebounceSeconds = 10

	MinIntervalSeconds     = 1
	MaxIntervalSeconds     = 86400
	DefaultIntervalSeconds = 30

	// MaxRuleNameLen and MaxURLLen keep a pasted mistake out of the database.
	MaxRuleNameLen = 120
	MaxURLLen      = 2048
)

// Rule is one webhook endpoint and what it wants to hear about.
type Rule struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
	// URL is never echoed back to a client or into a payload: a Slack or
	// Discord webhook URL carries its secret in the path. See RedactedURL.
	URL    string `json:"-"`
	Format Format `json:"format"`
	// Events is what this rule subscribes to. Empty means every type, which is
	// the useful default for the first rule somebody creates.
	Events []Type `json:"events"`
	// MinSeverity drops anything quieter. Empty means info, i.e. everything.
	MinSeverity Severity `json:"minSeverity"`
	// DebounceSeconds is how long an event waits for company before it is sent.
	// Everything raised about the same subject inside the window becomes one
	// message carrying a count.
	DebounceSeconds int `json:"debounceSeconds"`
	// MinIntervalSeconds is the floor between two deliveries to this endpoint.
	// Events raised while the floor is in force are not dropped: they keep
	// coalescing and ride out on the next delivery.
	MinIntervalSeconds int       `json:"minIntervalSeconds"`
	CreatedAt          time.Time `json:"createdAt"`
	UpdatedAt          time.Time `json:"updatedAt"`
	// AllowPrivateTarget is the deliberate opt-in past the SSRF guard in
	// Validate and the notifier's dial-time check. Default false, because
	// before it existed POST /alerts/rules accepted http://169.254.169.254/
	// and POST /alerts/rules/{id}/test then reported back whether the port
	// answered -- an internal port scanner and a reach into instance metadata,
	// driven from a form, with nothing in the log saying it happened (#607).
	// An operator who genuinely wants an alert delivered to something on their
	// own LAN sets this explicitly; a refusal with no escape hatch would just
	// get the whole guard disabled instead.
	AllowPrivateTarget bool `json:"allowPrivateTarget"`
}

// RedactedURL is what an API response or a log line may show.
func (r Rule) RedactedURL() string { return RedactWebhookURL(r.URL) }

// MarshalJSON adds the masked endpoint, so a handler that simply encodes a
// Rule cannot leak the real one.
func (r Rule) MarshalJSON() ([]byte, error) {
	type alias Rule
	return json.Marshal(struct {
		alias
		URL string `json:"url"`
	}{alias(r), r.RedactedURL()})
}

// Debounce is the coalescing window, defaulted.
func (r Rule) Debounce() time.Duration {
	if r.DebounceSeconds <= 0 {
		return DefaultDebounceSeconds * time.Second
	}
	return time.Duration(r.DebounceSeconds) * time.Second
}

// MinInterval is the rate-limit floor, defaulted.
func (r Rule) MinInterval() time.Duration {
	if r.MinIntervalSeconds <= 0 {
		return DefaultIntervalSeconds * time.Second
	}
	return time.Duration(r.MinIntervalSeconds) * time.Second
}

// Wants reports whether this rule should hear about ev.
//
// A test event always passes: a "send test" button that a subscription filter
// silently swallows tells the operator their webhook is broken when it is not.
func (r Rule) Wants(ev Event) bool {
	if ev.Type == TypeTest {
		return true
	}
	if !ev.Severity.AtLeast(r.MinSeverity) {
		return false
	}
	if len(r.Events) == 0 {
		return true
	}
	for _, t := range r.Events {
		if t == ev.Type {
			return true
		}
	}
	return false
}

// Normalized fills the defaults and clamps the knobs into range. It clamps
// rather than refuses, so a value that drifted out of bounds costs a bounded
// message rate and not a rule that has silently stopped alerting.
func (r Rule) Normalized() Rule {
	r.Name = strings.TrimSpace(r.Name)
	r.URL = strings.TrimSpace(r.URL)
	if r.Format == "" {
		r.Format = FormatJSON
	}
	if r.MinSeverity == "" {
		r.MinSeverity = SeverityInfo
	}
	if r.DebounceSeconds <= 0 {
		r.DebounceSeconds = DefaultDebounceSeconds
	}
	r.DebounceSeconds = clampInt(r.DebounceSeconds, MinDebounceSeconds, MaxDebounceSeconds)
	if r.MinIntervalSeconds <= 0 {
		r.MinIntervalSeconds = DefaultIntervalSeconds
	}
	r.MinIntervalSeconds = clampInt(r.MinIntervalSeconds, MinIntervalSeconds, MaxIntervalSeconds)

	// Duplicates go; UNKNOWN NAMES STAY. Dropping them here is what made
	// POST /alerts/rules with events:["nope.notreal"] return 201 and store an
	// empty list -- and an empty list means "every type". The narrowest rule an
	// operator could write became the loudest thing on the install, while they
	// believed they had subscribed to one event, because Normalized had already
	// deleted the evidence by the time Validate looked for it. Keeping the name
	// is what lets Validate refuse it BY NAME at save.
	//
	// This is also why the load path is safe. db.scanAlertRule runs Normalized
	// and never Validate, so a rule stored by a newer release, naming an event
	// this build has never heard of, still loads: the unknown name simply never
	// matches anything in Wants, and the rule keeps alerting on the events it
	// does name. Under the old code that same rule lost its only subscription
	// on read and started firing on everything -- the widening happened to an
	// install that had touched nothing.
	seen := map[Type]bool{}
	kept := r.Events[:0:0]
	for _, t := range r.Events {
		if seen[t] {
			continue
		}
		seen[t] = true
		kept = append(kept, t)
	}
	r.Events = kept
	return r
}

// Validate rejects what cannot be delivered.
func (r Rule) Validate() error {
	if r.Name == "" {
		return fmt.Errorf("alert rule needs a name")
	}
	if len(r.Name) > MaxRuleNameLen {
		return fmt.Errorf("alert rule name is longer than %d characters", MaxRuleNameLen)
	}
	if r.URL == "" {
		return fmt.Errorf("alert rule %q needs a webhook URL", r.Name)
	}
	if len(r.URL) > MaxURLLen {
		return fmt.Errorf("alert rule %q has a webhook URL longer than %d characters", r.Name, MaxURLLen)
	}
	u, err := url.Parse(r.URL)
	if err != nil {
		return fmt.Errorf("alert rule %q has an unparseable webhook URL", r.Name)
	}
	// Scheme and host only: the path is where the secret lives, so nothing
	// below the host is ever quoted back in an error message.
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
	default:
		return fmt.Errorf("alert rule %q must post to http or https, not %q", r.Name, u.Scheme)
	}
	if u.Host == "" {
		return fmt.Errorf("alert rule %q has a webhook URL with no host", r.Name)
	}
	// SSRF guard, part one of two. If the operator wrote a literal IP -- the
	// cloud metadata address, a loopback, a LAN range -- catch it here with no
	// network call, at the moment they save it. A HOSTNAME is deliberately NOT
	// resolved in this path: DNS from inside a save request is slow, flaky in
	// an offline test or sandbox, and its answer can legitimately change by the
	// time the alert is delivered anyway. The notifier's dial-time guard (see
	// notify.go) is what actually enforces this for a hostname, because it runs
	// at the one point that cannot be lied to by a DNS answer that changed
	// after Validate ran -- otherwise known as DNS rebinding. This literal-IP
	// check exists in addition because refusing an obviously bad rule at save
	// time, rather than letting the operator find out from a "send test" that
	// quietly told them which internal ports are open, is worth the
	// duplication. Same two points, same reason, as internal/hooks.
	if !r.AllowPrivateTarget {
		if ip := net.ParseIP(u.Hostname()); ip != nil && !netguard.IsPublicAddr(ip) {
			return fmt.Errorf("alert rule %q targets a non-public address; set "+
				"allowPrivateTarget to permit a self-hosted endpoint on purpose",
				r.Name)
		}
	}
	if !KnownFormat(r.Format) {
		return fmt.Errorf("alert rule %q has an unknown format %q", r.Name, r.Format)
	}
	switch r.MinSeverity {
	case SeverityInfo, SeverityWarning, SeverityCritical:
	default:
		return fmt.Errorf("alert rule %q has an unknown severity %q", r.Name, r.MinSeverity)
	}
	// Reached at SAVE only, and that placement is the whole point. Every write
	// path runs Normalized then Validate; the read path runs Normalized alone.
	// So a typo is refused by name at the moment the operator makes it, and a
	// rule already in the database naming an event a later version removed still
	// loads and still alerts. Refusing at load would take alerting down across
	// an upgrade to fix a typo -- the same trade #607 settled by guarding at the
	// dial rather than at the load.
	for _, t := range r.Events {
		if !KnownType(t) {
			return fmt.Errorf("alert rule %q subscribes to %q, which is not an event "+
				"this build raises; check the spelling against the event list, because "+
				"a rule that keeps no valid subscription at all means every event",
				r.Name, t)
		}
	}
	return nil
}

// ErrDuplicateRuleName is what CheckNameUnique wraps, so an HTTP layer can
// answer 409 for this and 400 for everything else Validate refuses.
var ErrDuplicateRuleName = errors.New("an alert rule with that name already exists")

// CheckNameUnique refuses a name another rule already answers to.
//
// Two rules called "disk" are indistinguishable in the list, so the one the
// operator disables during an incident may not be the one that is firing -- and
// nothing tells them they disabled the wrong one. Comparison folds case and
// surrounding space because "Disk" and "disk " are just as indistinguishable on
// screen as an exact match is.
//
// existing is the current set INCLUDING candidate itself when this is an
// update; candidate.ID is how its own row is excluded, so re-saving a rule
// without renaming it is not a conflict with itself.
//
// MUST BE CALLED FROM THE WRITE PATH. A name check that only the tests reach
// prevents nothing; see the report accompanying this change for the two call
// sites (db.CreateAlertRule and db.UpdateAlertRule) it belongs in.
func CheckNameUnique(candidate Rule, existing []Rule) error {
	name := strings.TrimSpace(candidate.Name)
	for _, other := range existing {
		if other.ID == candidate.ID && candidate.ID != 0 {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(other.Name), name) {
			return fmt.Errorf("%w: rule %d is already called %q, and two rules with "+
				"the same name cannot be told apart in the list -- the one you disable "+
				"may not be the one that is firing",
				ErrDuplicateRuleName, other.ID, other.Name)
		}
	}
	return nil
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
