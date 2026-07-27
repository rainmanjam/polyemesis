package alerts

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"
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

	seen := map[Type]bool{}
	kept := r.Events[:0:0]
	for _, t := range r.Events {
		if !KnownType(t) || seen[t] {
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
	if !KnownFormat(r.Format) {
		return fmt.Errorf("alert rule %q has an unknown format %q", r.Name, r.Format)
	}
	switch r.MinSeverity {
	case SeverityInfo, SeverityWarning, SeverityCritical:
	default:
		return fmt.Errorf("alert rule %q has an unknown severity %q", r.Name, r.MinSeverity)
	}
	for _, t := range r.Events {
		if !KnownType(t) {
			return fmt.Errorf("alert rule %q subscribes to unknown event %q", r.Name, t)
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
