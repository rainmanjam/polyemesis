// Package hooks turns pipeline lifecycle transitions into signed webhook
// deliveries an operator can script against.
//
// It is deliberately NOT internal/alerts with different defaults, and the
// difference is worth stating because the two look alike from a distance.
// An alert is for a human: it is coalesced ("12 times"), debounced, filtered by
// severity, and formatted for Slack. A hook is for a script: exactly one
// delivery per transition, in order, with a stable machine-readable body and an
// HMAC over it. Coalescing an alert is a kindness; coalescing a hook loses the
// eleven events the script needed.
//
// It is also not a subscriber of internal/events. That broker drops silently at
// 256 queued events by design -- correct for a metering frame, wrong for "your
// stream just went live" -- and it carries no ingest lifecycle at all: ingest
// liveness is derived from relay hub byte counters inside the engine's sweep and
// never published.
//
// What it DOES reuse is the snapshot. Engine.observeLoop builds one
// alerts.Snapshot every two seconds and hands it to both watchers, so there is
// one sampler, one set of locks, and one place where a stream key is kept out
// of a payload.
package hooks

import (
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/rainmanjam/polyemesis/internal/alerts"
)

// Trigger names a lifecycle transition. These strings are stored configuration
// -- a hook subscribes by name -- so renaming one silently unsubscribes every
// hook that used it, with no error anywhere. Do not rename them.
type Trigger string

const (
	// TriggerIngestPublished fires the first time data arrives on the ingest
	// after a period of nothing. There is no dwell: an operator scripting "go
	// live" wants the event now, and a spurious pair from a handshake blip is
	// cheaper than a five-second delay on every broadcast.
	TriggerIngestPublished Trigger = "ingest.published"
	// TriggerIngestDisconnected fires when nothing has arrived for
	// DefaultDisconnectAfter. It can only fire after a publish, so a server
	// sitting idle since boot never announces a disconnection that never
	// happened.
	TriggerIngestDisconnected Trigger = "ingest.disconnected"
	// TriggerDestinationUp fires when a destination starts delivering.
	TriggerDestinationUp Trigger = "destination.up"
	// TriggerDestinationDown fires when a destination stops -- because it
	// failed, because the operator disabled it, or because it was deleted. The
	// reason field says which. Unlike the alert of the same name this is a
	// FACT, not an incident, which is why a deliberate disable reports it: a
	// script mirroring "what are we live to" needs both edges.
	TriggerDestinationDown Trigger = "destination.down"
	// TriggerBroadcastFault fires when a platform refused to move a broadcast's
	// state and the refusal is one somebody has to act on -- the channel is at
	// its concurrent-broadcast ceiling, the broadcast has already been
	// completed, the token expired.
	//
	// A SEPARATE TRIGGER RATHER THAN A destination.down WITH A REASON, because
	// it is the opposite fact. The stream is FINE: bytes are flowing, FFmpeg is
	// healthy, the destination is delivering. What has failed is the platform's
	// idea of the broadcast, so a script that mirrors "what are we live to"
	// must not tear anything down when it hears this, and a script subscribed
	// to destination.down must not start hearing about it.
	//
	// It never means the stream was stopped. Nothing in polyemesis stops a
	// stream because a transition failed -- see internal/api/lifecycle.go, where
	// that rule is the one invariant with a test of its own.
	TriggerBroadcastFault Trigger = "broadcast.fault"
	// TriggerDestinationRolledOver fires when a file destination's respawn was
	// given a DIFFERENT output path than the one configured, because the
	// configured one already holds footage.
	//
	// A SEPARATE TRIGGER RATHER THAN A destination.down, for the same reason
	// broadcast.fault is separate: nothing went down. The destination is
	// delivering, the recording is continuing, and the only thing that changed
	// is which file it is continuing into. A script mirroring "what are we live
	// to" must not react, and a script subscribed to destination.down must not
	// start hearing about this.
	//
	// IT EXISTS BECAUSE THE ROLLOVER WAS OTHERWISE INVISIBLE. The child that
	// stopped is respawned, and the supervisor logs a clean exit at Info with no
	// entry in the process log ring; LastError is empty because -loglevel warning
	// suppresses the lines classify would have kept. So the configured filename
	// held a header and nothing else, the footage was in a sibling nobody had
	// been told about, and the only trace anywhere was a restart counter moving
	// from 0 to 1. "My recording is empty" is what that looks like from outside.
	//
	// Reason carries the path actually written. It is a filename, not a
	// credential, but it goes through the same redaction as every other Reason.
	TriggerDestinationRolledOver Trigger = "destination.rolledover"
	// TriggerTest is what the test button sends. Never subscribable: a test
	// that a subscription filter swallows teaches the operator that their
	// endpoint is broken when it is not.
	TriggerTest Trigger = "test"
)

// AllTriggers is every subscribable trigger, in the order a settings page
// should list them.
func AllTriggers() []Trigger {
	return []Trigger{
		TriggerIngestPublished, TriggerIngestDisconnected,
		TriggerDestinationUp, TriggerDestinationDown,
		// APPENDED, so no row of the settings picker moves under an operator
		// who has learned where the existing four are. Same reasoning as
		// alerts.AllTypes, and the same cost stated the same way: a hook with an
		// empty Triggers list means "everything", so an install that never
		// narrowed its subscription starts receiving this one on upgrade.
		TriggerBroadcastFault,
		// APPENDED, same as broadcast.fault above and for the same two reasons:
		// no existing row moves under an operator who has learned the order, and
		// an install whose hook never narrowed its Triggers list starts
		// receiving this one on upgrade.
		TriggerDestinationRolledOver,
	}
}

// KnownTrigger reports whether tr can be subscribed to.
func KnownTrigger(tr Trigger) bool {
	for _, k := range AllTriggers() {
		if k == tr {
			return true
		}
	}
	return false
}

// SourceRef identifies the programme a transition belongs to. Always present:
// an install can run several sources, and a script told "the stream started"
// without being told which one cannot act.
type SourceRef struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// DestinationRef identifies a destination. It carries no URL and no stream key,
// and it never will -- see the guard in payload_test.go.
type DestinationRef struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Platform string `json:"platform"`
}

// Event is one transition, before it is addressed to any particular endpoint.
type Event struct {
	Trigger     Trigger
	At          time.Time
	Source      SourceRef
	Destination *DestinationRef
	// Reason is why, in the operator's words: "no data for 5s", "disabled",
	// "removed". Free text, and therefore redacted on the way in.
	Reason string
	// Error is the child process's last words, which is FFmpeg stderr and
	// routinely contains a full rtmps:// URL with the stream key on the end.
	// Redacted on the way in, in Dispatcher.Publish, never here.
	Error string
}

// Delivery bounds. The retry budget is short on purpose: retries block the
// endpoint's own queue, because ordering is the promise. Three attempts at ten
// seconds each plus backoff is the worst-case stall inside one hook.
const (
	MinTimeoutSeconds     = 1
	MaxTimeoutSeconds     = 30
	DefaultTimeoutSeconds = 10

	MinAttempts     = 1
	MaxAttempts     = 5
	DefaultAttempts = 3

	MaxHookNameLen = 120
	MaxURLLen      = 2048
	// SecretBytes is the generated signing key length. 32 bytes of crypto/rand
	// is a 256-bit HMAC key, matching the digest.
	SecretBytes = 32
)

// Hook is one endpoint and what it wants to hear about.
type Hook struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
	// URL never marshals. Like an alert rule's, a webhook URL carries its
	// credential in the path.
	URL string `json:"-"`
	// Secret is the HMAC-SHA256 signing key. It marshals as nothing at all --
	// not even masked -- because unlike the URL there is no version of it the
	// UI has any use for. It is shown once, at create, and never again; see
	// handleCreateHook and the same pattern in token_handlers.go:54.
	Secret string `json:"-"`
	// Triggers is the subscription. Empty means every trigger, which is the
	// useful default for the first hook somebody creates.
	Triggers       []Trigger `json:"triggers"`
	TimeoutSeconds int       `json:"timeoutSeconds"`
	MaxAttempts    int       `json:"maxAttempts"`
	CreatedAt      time.Time `json:"createdAt"`
	UpdatedAt      time.Time `json:"updatedAt"`
	// AllowPrivateTarget is the deliberate opt-in past the SSRF guard in
	// Validate and Dispatcher's dial-time check. Default false, because the
	// ordinary webhook targets a public service and a hook aimed at
	// 169.254.169.254 or a LAN address with nobody having decided that on
	// purpose is a pivot from the operator console into the metadata service or
	// the rest of the network, not a feature. An operator who genuinely wants a
	// hook to hit something on their own LAN sets this explicitly; a refusal
	// with no escape hatch would just get the whole feature disabled instead.
	AllowPrivateTarget bool `json:"allowPrivateTarget"`
}

// RedactedURL is what a response or a log line may show.
func (h Hook) RedactedURL() string { return alerts.RedactWebhookURL(h.URL) }

// MarshalJSON adds the masked endpoint and a boolean for the secret, so a
// handler that simply encodes a Hook cannot leak either one.
func (h Hook) MarshalJSON() ([]byte, error) {
	type alias Hook
	return json.Marshal(struct {
		alias
		URL       string `json:"url"`
		HasSecret bool   `json:"hasSecret"`
	}{alias(h), h.RedactedURL(), h.Secret != ""})
}

// Wants reports whether this hook should hear about tr.
func (h Hook) Wants(tr Trigger) bool {
	if tr == TriggerTest {
		return true
	}
	if len(h.Triggers) == 0 {
		return true
	}
	for _, k := range h.Triggers {
		if k == tr {
			return true
		}
	}
	return false
}

// Normalized fills the defaults and clamps the knobs. It clamps rather than
// refuses for the reason alerts.Rule.Normalized does: a value that drifted out
// of bounds should cost a bounded timeout, not a hook that has silently stopped
// firing.
func (h Hook) Normalized() Hook {
	h.Name = strings.TrimSpace(h.Name)
	h.URL = strings.TrimSpace(h.URL)
	h.Secret = strings.TrimSpace(h.Secret)
	if h.TimeoutSeconds <= 0 {
		h.TimeoutSeconds = DefaultTimeoutSeconds
	}
	h.TimeoutSeconds = clampInt(h.TimeoutSeconds, MinTimeoutSeconds, MaxTimeoutSeconds)
	if h.MaxAttempts <= 0 {
		h.MaxAttempts = DefaultAttempts
	}
	h.MaxAttempts = clampInt(h.MaxAttempts, MinAttempts, MaxAttempts)

	seen := map[Trigger]bool{}
	kept := h.Triggers[:0:0]
	for _, tr := range h.Triggers {
		if !KnownTrigger(tr) || seen[tr] {
			continue
		}
		seen[tr] = true
		kept = append(kept, tr)
	}
	h.Triggers = kept
	return h
}

// Validate rejects what cannot be delivered.
//
// Nothing below the host is ever quoted back into an error: the path is where
// the secret lives, and an error message is the first thing an operator pastes
// into a bug report.
func (h Hook) Validate() error {
	if h.Name == "" {
		return fmt.Errorf("hook needs a name")
	}
	if len(h.Name) > MaxHookNameLen {
		return fmt.Errorf("hook name is longer than %d characters", MaxHookNameLen)
	}
	if h.URL == "" {
		return fmt.Errorf("hook %q needs a URL", h.Name)
	}
	if len(h.URL) > MaxURLLen {
		return fmt.Errorf("hook %q has a URL longer than %d characters", h.Name, MaxURLLen)
	}
	u, err := url.Parse(h.URL)
	if err != nil {
		return fmt.Errorf("hook %q has an unparseable URL", h.Name)
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
	default:
		return fmt.Errorf("hook %q must post to http or https, not %q", h.Name, u.Scheme)
	}
	if u.Host == "" {
		return fmt.Errorf("hook %q has a URL with no host", h.Name)
	}
	// SSRF guard, part one of two. If the operator wrote a literal IP -- the
	// cloud metadata address, a loopback, a LAN range -- catch it here with no
	// network call, at the moment they save it. A HOSTNAME is deliberately NOT
	// resolved in this path: DNS from inside a save request is slow, flaky in
	// an offline test or sandbox, and its answer can legitimately change by the
	// time the hook is dispatched anyway. Dispatcher's dial-time guard (see
	// dispatch.go) is what actually enforces this for a hostname, because it
	// runs at the one point that cannot be lied to by a DNS answer that changed
	// after Validate ran -- otherwise known as DNS rebinding. This literal-IP
	// check exists in addition because rejecting an obviously bad hook at save
	// time, rather than only discovering it three retries into a delivery
	// attempt, is worth the duplication.
	if !h.AllowPrivateTarget {
		if ip := net.ParseIP(u.Hostname()); ip != nil && !isPublicAddr(ip) {
			return fmt.Errorf("hook %q targets a non-public address; set "+
				"allowPrivateTarget to permit a self-hosted endpoint on purpose",
				h.Name)
		}
	}
	for _, tr := range h.Triggers {
		if !KnownTrigger(tr) {
			return fmt.Errorf("hook %q subscribes to unknown trigger %q", h.Name, tr)
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

// isPublicAddr reports whether ip is safe to let a webhook actually reach:
// not loopback, not link-local (which covers 169.254.169.254, the cloud
// metadata address, and IPv6 fe80::/10 alike), not a private range (RFC1918,
// and IPv6 ULA fc00::/7 -- both covered by net.IP.IsPrivate), not the
// unspecified address, and not multicast. Used at two points that must agree:
// Validate's literal-IP check above and Dispatcher's dial-time guard.
func isPublicAddr(ip net.IP) bool {
	if ip == nil {
		return false
	}
	switch {
	case ip.IsLoopback(),
		ip.IsLinkLocalUnicast(),
		ip.IsLinkLocalMulticast(),
		ip.IsPrivate(),
		ip.IsUnspecified(),
		ip.IsMulticast():
		return false
	}
	// net.IP.IsPrivate is RFC1918 and IPv6 ULA and NOTHING ELSE, which leaves
	// ranges that are unroutable on the public internet but very much reachable
	// from the host. 100.64.0.0/10 is the practical one: carrier NAT, and the
	// range Tailscale hands out -- so without this a hook to http://100.64.0.1
	// was accepted and dialed, which is the overlay network the guard most
	// needs to keep a webhook out of.
	for _, cidr := range nonPublicRanges {
		if cidr.Contains(ip) {
			return false
		}
	}
	return true
}

// nonPublicRanges are reachable-but-not-globally-routable networks that
// net.IP.IsPrivate does not know about. Parsed once; a bad constant here would
// panic at init rather than silently letting a range through.
var nonPublicRanges = func() []*net.IPNet {
	out := make([]*net.IPNet, 0, 8)
	for _, s := range []string{
		"100.64.0.0/10",   // RFC6598 shared address space (CGNAT, Tailscale)
		"192.0.0.0/24",    // RFC6890 IETF protocol assignments
		"198.18.0.0/15",   // RFC2544 benchmarking
		"192.0.2.0/24",    // RFC5737 TEST-NET-1
		"198.51.100.0/24", // TEST-NET-2
		"203.0.113.0/24",  // TEST-NET-3
		"240.0.0.0/4",     // RFC1112 reserved
		"64:ff9b::/96",    // RFC6052 IPv4/IPv6 translation -- an embedded v4 target
	} {
		_, n, err := net.ParseCIDR(s)
		if err != nil {
			panic("hooks: bad non-public CIDR " + s + ": " + err.Error())
		}
		out = append(out, n)
	}
	return out
}()
