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
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/rainmanjam/polyemesis/internal/alerts"
	"github.com/rainmanjam/polyemesis/internal/netguard"
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
	// SecretUnreadable is the reason this hook's stored signing secret could
	// not be decrypted on this machine, empty for every hook whose secret was
	// read normally -- which is all of them on a healthy install. #715.
	//
	// It is set by db.scanHook and NEVER by a column: it is a fact about this
	// process's key file, not about the row, so it is recomputed on every read
	// rather than remembered. Restore the right key file and it goes away by
	// itself, with no repair step and nothing to un-set. The same shape, and
	// for the same reasons, as db.Destination.KeyUnreadable.
	//
	// WHEN IT IS SET, Secret IS EMPTY AND NOTHING IS DELIVERED. That is the
	// whole point. The old behaviour was to load the row with an empty secret
	// and go on posting, which signs with nothing and sends the delivery
	// UNSIGNED -- and at the far end an unsigned delivery is indistinguishable
	// from a forgery. The signing secret was the only thing that made the
	// webhook trustworthy, and its absence is invisible to the receiver.
	//
	// The row itself is not touched. enabled is still 1 in the database, so
	// this is reversible by restoring the key file rather than by re-enabling
	// every hook by hand.
	SecretUnreadable string `json:"secretUnreadable,omitempty"`
}

// secretUnreadableReason is what the operator is shown. It names the fix,
// because "decryption failed" tells somebody staring at a hook that has stopped
// firing nothing they can act on, and the fix really is this small.
const secretUnreadableReason = "the signing secret could not be read on this machine — " +
	"re-enter it to enable this hook"

// SecretUnreadableReason is secretUnreadableReason for the storage layer, which
// is what discovers the condition and therefore what has to set it.
func SecretUnreadableReason() string { return secretUnreadableReason }

// ErrSecretUnreadable is the sentinel behind Hook.SecretUnreadable.
//
// Deliberately never returned by the load path: a hook whose secret will not
// open must still appear in the list, with its name and endpoint intact, or an
// operator whose key file went missing opens the hooks page to an empty one and
// has nothing to act on. It is an error only so Validate has something to
// return and attempt has something to match on.
var ErrSecretUnreadable = errors.New(secretUnreadableReason)

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

	// Duplicates go; UNKNOWN NAMES STAY, for the reason spelled out in
	// alerts.Rule.Normalized. Dropping them here meant a hook saved with a
	// mistyped trigger stored an empty Triggers list, and an empty list means
	// EVERY trigger -- the script got the whole firehose while its author
	// believed it was subscribed to one transition, and Validate's unknown
	// trigger error below could never fire because the name was already gone.
	//
	// Keeping the name also fixes the load path rather than breaking it.
	// db.scanHook runs Normalized and never Validate, so a row written by a
	// newer release survives: the trigger this build cannot honour simply never
	// matches in Wants, and the hook keeps delivering the ones it can. Under the
	// old code that row lost its only subscription on read and started
	// delivering everything.
	seen := map[Trigger]bool{}
	kept := h.Triggers[:0:0]
	for _, tr := range h.Triggers {
		if seen[tr] {
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
	// FIRST, and before the name check, because it is the one refusal here that
	// is about the MACHINE rather than about what the operator typed. #715.
	//
	// reload() starts a worker only for a hook that validates, so this is what
	// keeps an unsigned delivery from having a queue to sit in at all. The
	// refusal at the moment of signing (Dispatcher.attempt) is the one that
	// makes it unreachable; this one is what makes it VISIBLE, in the list and
	// in the log, rather than a hook that quietly stopped firing.
	if h.SecretUnreadable != "" {
		return fmt.Errorf("hook %q: %w", h.Name, ErrSecretUnreadable)
	}
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
	// Reached at SAVE only. Every write path runs Normalized then Validate; the
	// read path runs Normalized alone. A typo is refused by name while the
	// operator is still looking at the form, and a hook already stored against a
	// trigger a later version removed still loads and still delivers. Refusing
	// at load would stop every hook on the install to punish one stale name.
	for _, tr := range h.Triggers {
		if !KnownTrigger(tr) {
			return fmt.Errorf("hook %q subscribes to %q, which is not a trigger this "+
				"build fires; check the spelling against the trigger list, because a "+
				"hook that keeps no valid subscription at all means every trigger",
				h.Name, tr)
		}
	}
	return nil
}

// ErrDuplicateHookName is what CheckNameUnique wraps, so an HTTP layer can
// answer 409 for this and 400 for everything else Validate refuses.
var ErrDuplicateHookName = errors.New("a hook with that name already exists")

// CheckNameUnique refuses a name another hook already answers to.
//
// Two hooks called "deploy" are indistinguishable in the list, so the one an
// operator disables may not be the one that is firing, and nothing tells them
// they disabled the wrong one. Comparison folds case and surrounding space
// because "Deploy" and "deploy " are just as indistinguishable on screen.
//
// existing is the current set INCLUDING candidate itself when this is an
// update; candidate.ID is how its own row is excluded, so re-saving a hook
// without renaming it is not a conflict with itself.
//
// MUST BE CALLED FROM THE WRITE PATH. A name check that only the tests reach
// prevents nothing; see the report accompanying this change for the two call
// sites (db.CreateHook and db.UpdateHook) it belongs in.
func CheckNameUnique(candidate Hook, existing []Hook) error {
	name := strings.TrimSpace(candidate.Name)
	for _, other := range existing {
		if other.ID == candidate.ID && candidate.ID != 0 {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(other.Name), name) {
			return fmt.Errorf("%w: hook %d is already called %q, and two hooks with "+
				"the same name cannot be told apart in the list -- the one you disable "+
				"may not be the one that is firing",
				ErrDuplicateHookName, other.ID, other.Name)
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

// isPublicAddr answers, for this package, whether ip may be dialed. It is a
// one-line delegation on purpose: the real list lives in internal/netguard so
// that internal/alerts checks the SAME ranges rather than a copy of them that
// stops matching the first time somebody remembers a range here and not there
// (#607). Keep it delegating -- a range added below and not in netguard is a
// range alerts would still let a webhook reach.
func isPublicAddr(ip net.IP) bool { return netguard.IsPublicAddr(ip) }
