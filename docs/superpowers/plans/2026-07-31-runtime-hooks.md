# Runtime Hooks (Lifecycle Webhooks) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let an operator run a script the moment the stream starts, the moment it stops, and the moment a destination changes state — reliably enough to build on, and without a slow endpoint ever touching the media path.

**Architecture:** A new `internal/hooks` package. It is a *second consumer of the existing snapshot*, not a second way of finding out what happened: `Engine.observeLoop` builds one `alerts.Snapshot` every 2 s and hands it to both `alerts.Watcher` (incidents) and the new `hooks.Watcher` (transitions). Delivery is a separate `hooks.Dispatcher` with per-endpoint ordered queues, HMAC-signed JSON, bounded retries and a delivery log. Redaction is reused verbatim from `internal/alerts`.

**Tech Stack:** Go 1.26 (no new dependencies — `crypto/hmac`, `crypto/sha256`, `crypto/rand`, `net/http` cover all of it), SQLite via the existing `internal/db`, React + TypeScript.

---

## Why this shape, and what was rejected

Three obvious designs were considered and all three are wrong here. The plan is unreadable without knowing why.

**Rejected: subscribe to `events.Broker`.** It is the obvious "something happened" bus, and it is lossy by construction — `Publish` is `select { case s.C <- ev: default: b.dropped.Add(1) }` (internal/events/events.go:171) with a 256-deep buffer. A metering frame is meant to be droppable; "your stream just went live" is not. Worse, the broker carries no ingest lifecycle at all: ingest liveness is derived from `e.hub.RxBytes()` deltas inside `alertLoop` (engine.go:4595-4603), never published. Subscribing would mean re-deriving the edge from `TypeStatus` snapshots — a second state machine that can disagree with the first.

**Rejected: a new `alerts.Type` plus a new `alerts.Format`.** The alert path *coalesces*: `coalescer.Add` groups by `groupKey` and a `Delivery` carries `Items[]` with a count (internal/alerts/queue.go). That is exactly right for a human reading Slack and exactly wrong for a script: "destination.down ×12" is not twelve things a script can act on, and the debounce (`DefaultDebounceSeconds = 10`) plus rate floor (`DefaultIntervalSeconds = 30`) means an event can sit for half a minute. Alerts also have no signature, no sequence number and no ordering guarantee.

**Rejected: re-derive transitions inside `internal/hooks`.** `alerts.Watcher` already owns "how long has this been true", and it is the only place in the codebase where a level becomes an edge. A second sampler would need its own `e.Status()` call under the same locks, at its own cadence, and the two would drift.

**Chosen:** one snapshot, two state machines, two deliverers. `hooks.Watcher` takes the same `alerts.Snapshot` value in the same sweep, immediately after `alerts.Watcher`. It reuses the expensive and dangerous part — building a snapshot with no URL and no stream key in it — and keeps its own edges, because the questions are genuinely different:

| | `alerts.Watcher` | `hooks.Watcher` |
|---|---|---|
| Question | "is this an incident worth waking somebody?" | "did this transition happen?" |
| Starting position | assumed up; waits to be proven down | assumed off; waits to be proven on |
| Ingest dwell | 20 s down before firing | 0 s on publish, 5 s on disconnect |
| Publish edge | **cannot emit one** (see below) | the whole point |
| Delivery | coalesced, debounced, severity-filtered | 1:1, ordered, signed |

The "cannot emit one" is load-bearing and worth stating precisely. `watchIngest` (internal/alerts/watch.go:200) only emits `ingest.recovered` after it has emitted `ingest.lost`. An install whose streamer connects inside `DefaultDownFor` of boot produces neither. **There is no "the stream started" event in polyemesis today**, and that is the single largest reason this is a new subsystem rather than a new alert type.

---

## Global Constraints

- **No new Go dependencies.**
- **Video is COPIED, never re-encoded.** This feature spawns no FFmpeg, changes no argv, and touches no `Spec`. Nothing here is on a destination path. Grep-provable: Task 9 Step 3.
- **Nothing on the sweep path may block.** `Dispatcher.Publish` performs only non-blocking channel sends, exactly like `alerts.Notifier.Publish` (notify.go:172-190) and for the same stated reason. The one place a hook can block is `Dispatcher.Test`, which runs on an HTTP handler goroutine and is bounded by the hook's own timeout.
- **A stream key or token must never reach a payload.** Enforced centrally in `Dispatcher.Publish` via `alerts.Redact`, not left to callers — the same argument as internal/alerts/redact.go:179-181. `Snapshot.IngestError` and `DestState.Error` come from `supervisor.runOnce`, which returns the last three FFmpeg **stderr** lines (supervisor.go:459-461); those routinely contain a full `rtmps://host/app/KEY`.
- **Every hook is signed.** A secret is generated on create when the operator does not supply one. An unsigned webhook is one that anybody who learns the URL can forge.
- **Ordering is per endpoint, and is bought with head-of-line blocking.** One worker goroutine per hook, one bounded queue. Worst-case stall inside one endpoint is `MaxAttempts × (TimeoutSeconds + backoff)` ≤ 33 s at defaults. Other endpoints are unaffected.
- **A dropped delivery is admitted, never hidden.** Queue overflow increments a per-hook counter that rides out on the next successful envelope as `"missed": N`.
- CI gates, in CI's order: `gofmt -l ./cmd ./internal` prints nothing; `go build ./...`; `go vet ./...`; `go test -race ./...`; then `cd ui && npx tsc -b --noEmit && npm run lint && npm run build`.
- British spelling. No emoji. Comments explain *why*, and name the failure that motivated the decision.

---

### Task 1: The trigger catalogue and the Hook rule

**Files:**
- Create: `internal/hooks/hooks.go`
- Create: `internal/hooks/hooks_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `Trigger` with five constants; `AllTriggers() []Trigger`; `KnownTrigger(Trigger) bool`; `Event`; `SourceRef`; `DestinationRef`; `Hook` with `Normalized()`, `Validate()`, `Wants(Trigger) bool`, `RedactedURL()`, `MarshalJSON`.

- [ ] **Step 1: Write the failing test**

Create `internal/hooks/hooks_test.go`:

```go
package hooks

import (
	"encoding/json"
	"strings"
	"testing"
)

// The trigger strings are stored configuration: a hook subscribes by name and
// the name lives in the database. Renaming one silently unsubscribes every
// hook that used it, with no error anywhere. This pins the wire values.
func TestTriggerStringsAreFrozen(t *testing.T) {
	want := map[Trigger]string{
		TriggerIngestPublished:    "ingest.published",
		TriggerIngestDisconnected: "ingest.disconnected",
		TriggerDestinationUp:      "destination.up",
		TriggerDestinationDown:    "destination.down",
		TriggerTest:               "test",
	}
	for tr, s := range want {
		if string(tr) != s {
			t.Errorf("trigger renamed: %q, want %q -- every stored hook that "+
				"subscribed to it has just been silently unsubscribed", tr, s)
		}
	}
}

// TriggerTest must not be subscribable, for the same reason alerts.TypeTest is
// not: a test delivery that a subscription filter swallows teaches the
// operator that their endpoint is broken when it is not.
func TestAllTriggersExcludesTest(t *testing.T) {
	for _, tr := range AllTriggers() {
		if tr == TriggerTest {
			t.Fatal("TriggerTest is in AllTriggers; a test button must bypass " +
				"the subscription filter, not appear in it")
		}
	}
	if len(AllTriggers()) != 4 {
		t.Fatalf("AllTriggers has %d entries, want 4 -- add the new one to this "+
			"test and to the docs before shipping it", len(AllTriggers()))
	}
}

func TestHookWantsEmptySubscriptionMeansEverything(t *testing.T) {
	h := Hook{}.Normalized()
	for _, tr := range AllTriggers() {
		if !h.Wants(tr) {
			t.Errorf("a hook with no explicit triggers ignored %s; empty must "+
				"mean every trigger, matching alerts.Rule.Wants", tr)
		}
	}
}

func TestHookWantsAlwaysAcceptsTest(t *testing.T) {
	h := Hook{Triggers: []Trigger{TriggerDestinationUp}}.Normalized()
	if !h.Wants(TriggerTest) {
		t.Fatal("a narrow subscription swallowed a test delivery")
	}
}

func TestHookNeverMarshalsItsURLOrItsSecret(t *testing.T) {
	h := Hook{
		ID: 1, Name: "deploy",
		URL:    "https://hooks.example.com/services/T0/B1/XXXXsecretXXXX",
		Secret: "sh_ZZZZsigningZZZZ",
	}.Normalized()

	b, err := json.Marshal(h)
	if err != nil {
		t.Fatal(err)
	}
	body := string(b)
	if strings.Contains(body, "XXXXsecretXXXX") {
		t.Errorf("the endpoint path leaked: %s", body)
	}
	if strings.Contains(body, "ZZZZsigningZZZZ") {
		t.Errorf("the signing secret leaked: %s", body)
	}
	if !strings.Contains(body, "[redacted]") {
		t.Errorf("no masked endpoint; the UI has nothing to render: %s", body)
	}
	if !strings.Contains(body, `"hasSecret":true`) {
		t.Errorf("no hasSecret flag; the UI cannot tell a signed hook from an "+
			"unsigned one: %s", body)
	}
}

func TestHookValidate(t *testing.T) {
	tests := []struct {
		name string
		hook Hook
		want string // substring; "" means valid
	}{
		{"good", Hook{Name: "deploy", URL: "https://example.com/h"}, ""},
		{"no name", Hook{URL: "https://example.com/h"}, "needs a name"},
		{"no url", Hook{Name: "deploy"}, "needs a URL"},
		{"ftp", Hook{Name: "deploy", URL: "ftp://example.com/h"}, "http or https"},
		{"no host", Hook{Name: "deploy", URL: "https:///h"}, "no host"},
		{"unknown trigger", Hook{
			Name: "deploy", URL: "https://example.com/h",
			Triggers: []Trigger{"ingest.exploded"},
		}, "unknown trigger"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Validate runs on the normalized value everywhere it is called,
			// because Normalized is what drops the unknown triggers a stored
			// row might carry from an older release.
			err := tc.hook.Normalized().Validate()
			switch {
			case tc.want == "" && err != nil:
				t.Fatalf("rejected a valid hook: %v", err)
			case tc.want == "":
			case err == nil:
				t.Fatalf("accepted %s", tc.name)
			case !strings.Contains(err.Error(), tc.want):
				t.Fatalf("error = %q, want it to mention %q", err, tc.want)
			}
		})
	}
}

// An unparseable URL must not be quoted back. A webhook URL carries its secret
// in the path, so an error message that echoes it puts the secret in the log
// the operator then pastes into an issue.
func TestValidateNeverQuotesTheURL(t *testing.T) {
	h := Hook{Name: "deploy", URL: "ftp://example.com/SECRETPATH"}.Normalized()
	err := h.Normalized().Validate()
	if err == nil {
		t.Fatal("accepted an ftp endpoint")
	}
	if strings.Contains(err.Error(), "SECRETPATH") {
		t.Fatalf("the error quoted the URL path: %v", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/hooks/ -count=1`
Expected: FAIL to build — the package does not exist.

- [ ] **Step 3: Write the implementation**

Create `internal/hooks/hooks.go`:

```go
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
```

Note: `Validate` is called on the **normalized** value everywhere, and `Normalized` drops unknown triggers — so the `unknown trigger` branch is unreachable through the normal path and exists for a caller that skips normalisation. Keep it; the test drives it directly.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/hooks/ -count=1 -v`
Expected: PASS, seven tests.

- [ ] **Step 5: Mutation-test the URL guard**

Temporarily change `MarshalJSON` to embed `h.URL` instead of `h.RedactedURL()`.
Run: `go test ./internal/hooks/ -run TestHookNeverMarshals -count=1`
Expected: FAIL naming the leaked path. Restore by hand, re-run, confirm PASS.

- [ ] **Step 6: Commit**

```bash
gofmt -w internal/hooks/
git add internal/hooks/
git commit -m "feat(hooks): the trigger catalogue and the Hook rule

Four subscribable triggers plus a test one that bypasses the filter. The
strings are stored configuration, so a rename silently unsubscribes every hook
that used it -- pinned by a test rather than a comment.

A Hook marshals a masked URL and a hasSecret boolean and nothing else. The
endpoint carries its credential in the path and the signing key has no form a
UI needs, so neither has a marshalled representation to leak."
```

---

### Task 2: The watcher — publish, disconnect, destination edges

**Files:**
- Create: `internal/hooks/watch.go`
- Create: `internal/hooks/watch_test.go`

**Interfaces:**
- Consumes: `alerts.Snapshot`, `alerts.DestState` from `internal/alerts/watch.go:114`.
- Produces: `WatchConfig`; `NewWatcher(SourceRef, WatchConfig) *Watcher`; `(*Watcher).Observe(alerts.Snapshot) []Event`; `edgeState` with `observe(on bool, now time.Time, offAfter time.Duration) (turnedOn, turnedOff bool)`.

- [ ] **Step 1: Write the failing test**

Create `internal/hooks/watch_test.go`:

```go
package hooks

import (
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/alerts"
)

func at(sec int) time.Time {
	return time.Date(2026, 7, 31, 12, 0, sec, 0, time.UTC)
}

func triggers(evs []Event) []Trigger {
	out := make([]Trigger, 0, len(evs))
	for _, e := range evs {
		out = append(out, e.Trigger)
	}
	return out
}

func same(got, want []Trigger) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// THE test for this whole feature. alerts.Watcher cannot produce this event:
// watchIngest only emits ingest.recovered after it has emitted ingest.lost, so
// an install whose streamer connects inside DownFor of boot produces neither.
func TestFirstBytesFireIngestPublishedWithNoDwell(t *testing.T) {
	w := NewWatcher(SourceRef{ID: 1, Name: "Main"}, WatchConfig{})

	if got := w.Observe(alerts.Snapshot{At: at(0), IngestConfigured: true}); len(got) != 0 {
		t.Fatalf("a server that has never seen a stream announced %v", triggers(got))
	}
	got := w.Observe(alerts.Snapshot{At: at(2), IngestConfigured: true, IngestLive: true})
	if !same(triggers(got), []Trigger{TriggerIngestPublished}) {
		t.Fatalf("triggers = %v, want [ingest.published] on the very first bytes",
			triggers(got))
	}
	if got[0].Source.Name != "Main" {
		t.Errorf("source = %+v; a script told the stream started but not which "+
			"programme cannot act on it", got[0].Source)
	}
	// Still live: no repeats. A hook that fires every two seconds for the
	// duration of a broadcast is a hook nobody keeps enabled.
	if again := w.Observe(alerts.Snapshot{At: at(4), IngestConfigured: true, IngestLive: true}); len(again) != 0 {
		t.Fatalf("repeated while still live: %v", triggers(again))
	}
}

func TestDisconnectWaitsForTheDwellAndOnlyAfterAPublish(t *testing.T) {
	w := NewWatcher(SourceRef{ID: 1}, WatchConfig{DisconnectAfter: 5 * time.Second})

	w.Observe(alerts.Snapshot{At: at(0), IngestConfigured: true, IngestLive: true})
	if got := w.Observe(alerts.Snapshot{At: at(2), IngestConfigured: true}); len(got) != 0 {
		t.Fatalf("fired at 2s into a 5s dwell: %v", triggers(got))
	}
	if got := w.Observe(alerts.Snapshot{At: at(4), IngestConfigured: true}); len(got) != 0 {
		t.Fatalf("fired at 4s into a 5s dwell: %v", triggers(got))
	}
	got := w.Observe(alerts.Snapshot{At: at(6), IngestConfigured: true})
	if !same(triggers(got), []Trigger{TriggerIngestDisconnected}) {
		t.Fatalf("triggers = %v, want [ingest.disconnected] once the dwell elapsed",
			triggers(got))
	}
	// And not again. The stream is not disconnecting once every two seconds.
	if again := w.Observe(alerts.Snapshot{At: at(20), IngestConfigured: true}); len(again) != 0 {
		t.Fatalf("repeated while still disconnected: %v", triggers(again))
	}
}

func TestAnIdleServerNeverAnnouncesADisconnection(t *testing.T) {
	// A server that has been up for an hour with nobody streaming has not lost
	// anything. Without the "only after a publish" rule this fires once, five
	// seconds after boot, on every install that is not currently live.
	w := NewWatcher(SourceRef{ID: 1}, WatchConfig{DisconnectAfter: 5 * time.Second})
	for s := 0; s <= 60; s += 2 {
		if got := w.Observe(alerts.Snapshot{At: at(s), IngestConfigured: true}); len(got) != 0 {
			t.Fatalf("at %ds an idle server announced %v", s, triggers(got))
		}
	}
}

func TestAFlappingDestinationDoesNotStorm(t *testing.T) {
	// The supervisor reconnecting an RTMP destination is normal operation. The
	// dwell on the DOWN edge alone is what suppresses it: a destination that
	// comes back inside the window never goes down, so it never comes up again
	// either.
	w := NewWatcher(SourceRef{ID: 1}, WatchConfig{DestinationDownAfter: 10 * time.Second})
	dest := func(running bool) alerts.Snapshot {
		return alerts.Snapshot{
			At: time.Time{}, IngestConfigured: true, IngestLive: true,
			Destinations: []alerts.DestState{{ID: 3, Name: "Twitch", Enabled: true, Running: running}},
		}
	}
	first := dest(true)
	first.At = at(0)
	got := w.Observe(first)
	if !same(triggers(got), []Trigger{TriggerIngestPublished, TriggerDestinationUp}) {
		t.Fatalf("triggers = %v, want ingest.published then destination.up", triggers(got))
	}
	for i, running := range []bool{false, true, false, true, false, true} {
		s := dest(running)
		s.At = at(2 + i*2)
		if evs := w.Observe(s); len(evs) != 0 {
			t.Fatalf("flap %d produced %v; the 10s dwell should have absorbed it",
				i, triggers(evs))
		}
	}
}

func TestDisablingADestinationIsADownWithAReason(t *testing.T) {
	// Deliberately different from alerts, which treats a disabled destination
	// as "not down". A hook is a fact, not an incident: a script mirroring
	// "what are we live to" needs the edge whoever caused it.
	w := NewWatcher(SourceRef{ID: 1}, WatchConfig{DestinationDownAfter: 0})
	up := alerts.Snapshot{
		At: at(0), IngestConfigured: true, IngestLive: true,
		Destinations: []alerts.DestState{{ID: 3, Name: "Twitch", Enabled: true, Running: true}},
	}
	w.Observe(up)

	off := up
	off.At = at(2)
	off.Destinations = []alerts.DestState{{ID: 3, Name: "Twitch", Enabled: false}}
	got := w.Observe(off)
	if !same(triggers(got), []Trigger{TriggerDestinationDown}) {
		t.Fatalf("triggers = %v, want [destination.down]", triggers(got))
	}
	if got[0].Reason != "disabled" {
		t.Errorf("reason = %q, want \"disabled\" so a script can tell an "+
			"operator's decision from a failure", got[0].Reason)
	}
}

func TestARemovedDestinationGoesDownRatherThanVanishing(t *testing.T) {
	w := NewWatcher(SourceRef{ID: 1}, WatchConfig{DestinationDownAfter: 0})
	up := alerts.Snapshot{
		At: at(0), IngestConfigured: true, IngestLive: true,
		Destinations: []alerts.DestState{{ID: 3, Name: "Twitch", Enabled: true, Running: true}},
	}
	w.Observe(up)

	gone := alerts.Snapshot{At: at(2), IngestConfigured: true, IngestLive: true}
	got := w.Observe(gone)
	if !same(triggers(got), []Trigger{TriggerDestinationDown}) {
		t.Fatalf("triggers = %v, want [destination.down]; a deleted destination "+
			"that was live leaves a script believing it still is", triggers(got))
	}
	if got[0].Reason != "removed" {
		t.Errorf("reason = %q, want \"removed\"", got[0].Reason)
	}
	if got[0].Destination == nil || got[0].Destination.Name != "Twitch" {
		t.Errorf("the removed destination lost its identity: %+v", got[0].Destination)
	}
}

func TestDestinationEventsAreOrderedByID(t *testing.T) {
	// Map iteration order must never reach the wire: a receiver correlating
	// deliveries by sequence sees a different order on every run otherwise.
	w := NewWatcher(SourceRef{ID: 1}, WatchConfig{DestinationDownAfter: 0})
	s := alerts.Snapshot{
		At: at(0), IngestConfigured: true, IngestLive: true,
		Destinations: []alerts.DestState{
			{ID: 9, Name: "c", Enabled: true, Running: true},
			{ID: 2, Name: "a", Enabled: true, Running: true},
			{ID: 5, Name: "b", Enabled: true, Running: true},
		},
	}
	got := w.Observe(s)
	var names []string
	for _, e := range got {
		if e.Destination != nil {
			names = append(names, e.Destination.Name)
		}
	}
	if len(names) != 3 || names[0] != "a" || names[1] != "b" || names[2] != "c" {
		t.Fatalf("destination order = %v, want a b c (ascending id)", names)
	}
}

func TestUnconfiguredIngestResetsRatherThanFiring(t *testing.T) {
	w := NewWatcher(SourceRef{ID: 1}, WatchConfig{DisconnectAfter: 5 * time.Second})
	w.Observe(alerts.Snapshot{At: at(0), IngestConfigured: true, IngestLive: true})

	// Ingest removed from the configuration entirely. There is nothing to lose,
	// so say nothing -- and forget the session, so re-adding it publishes
	// afresh rather than continuing a session that ended.
	if got := w.Observe(alerts.Snapshot{At: at(2)}); len(got) != 0 {
		t.Fatalf("an unconfigured ingest announced %v", triggers(got))
	}
	got := w.Observe(alerts.Snapshot{At: at(4), IngestConfigured: true, IngestLive: true})
	if !same(triggers(got), []Trigger{TriggerIngestPublished}) {
		t.Fatalf("triggers = %v, want a fresh publish after reconfiguration",
			triggers(got))
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/hooks/ -run 'TestFirstBytes|TestDisconnect|TestAnIdle|TestAFlapping|TestDisabling|TestARemoved|TestDestinationEvents|TestUnconfigured' -v`
Expected: FAIL to build, `undefined: NewWatcher`.

- [ ] **Step 3: Write the implementation**

Create `internal/hooks/watch.go`:

```go
package hooks

import (
	"sort"
	"time"

	"github.com/rainmanjam/polyemesis/internal/alerts"
)

// Watcher defaults.
const (
	// DefaultDisconnectAfter is how long the ingest must be silent before a
	// disconnection is announced. Much shorter than alerts.DefaultDownFor (20s)
	// because the questions differ: an alert waits to be sure this is an
	// incident and not a reconnect, while a hook is reporting a fact somebody
	// asked to be told about. Three sweeps at the engine's 2s cadence.
	DefaultDisconnectAfter = 5 * time.Second
	// DefaultDestinationDownAfter absorbs a supervisor reconnect. It only
	// applies to the DOWN edge; the UP edge is immediate, which is what stops a
	// flapping destination producing a storm -- it never goes down, so it never
	// comes up again either.
	DefaultDestinationDownAfter = 10 * time.Second
)

// WatchConfig tunes the dwell times. A zero value takes every default.
type WatchConfig struct {
	DisconnectAfter      time.Duration
	DestinationDownAfter time.Duration
}

func (c WatchConfig) normalized() WatchConfig {
	if c.DisconnectAfter < 0 {
		c.DisconnectAfter = 0
	} else if c.DisconnectAfter == 0 {
		c.DisconnectAfter = DefaultDisconnectAfter
	}
	if c.DestinationDownAfter < 0 {
		c.DestinationDownAfter = 0
	}
	return c
}

// edgeState is the on/off detector both the ingest and every destination use.
//
// It is NOT alerts.downState with different numbers, and the difference is the
// starting position. alerts starts things UP and waits to be proven down,
// because an incident is a departure from working. This starts everything OFF
// and waits to be proven on, because "the stream started" is the event a script
// is written against and there is no such thing as a stream that was always
// running.
//
// That asymmetry is the whole reason this package exists: alerts.watchIngest
// only emits ingest.recovered after emitting ingest.lost, so an install whose
// streamer connects inside DownFor of boot produces no publish edge at all.
type edgeState struct {
	on       bool
	offSince time.Time
}

// observe folds one observation and reports which edge was crossed. The dwell
// applies to the OFF direction only.
func (s *edgeState) observe(on bool, now time.Time, offAfter time.Duration) (turnedOn, turnedOff bool) {
	if on {
		s.offSince = time.Time{}
		if !s.on {
			s.on = true
			return true, false
		}
		return false, false
	}
	if !s.on {
		return false, false
	}
	if s.offSince.IsZero() {
		s.offSince = now
	}
	if now.Before(s.offSince.Add(offAfter)) {
		return false, false
	}
	s.on, s.offSince = false, time.Time{}
	return false, true
}

// Watcher turns a stream of snapshots into lifecycle events for one source.
//
// One per engine, because the state it holds is per programme. Given the same
// sequence of snapshots it emits the same sequence of events, which is what
// makes every dwell above a table test rather than a stopwatch.
type Watcher struct {
	src  SourceRef
	cfg  WatchConfig
	ing  edgeState
	dest map[int64]*edgeState
	// names remembers what a destination was called, so a row that disappears
	// between two snapshots can still be identified in the event that says so.
	names map[int64]DestinationRef
}

// NewWatcher creates a watcher for one source.
func NewWatcher(src SourceRef, cfg WatchConfig) *Watcher {
	return &Watcher{
		src:   src,
		cfg:   cfg.normalized(),
		dest:  map[int64]*edgeState{},
		names: map[int64]DestinationRef{},
	}
}

// SetSource refreshes the programme label. The engine re-reads the source row on
// every reconcile, and a hook payload naming a programme by its old name after a
// rename is the kind of small lie that costs an operator an afternoon.
func (w *Watcher) SetSource(src SourceRef) { w.src = src }

// Observe judges one snapshot and returns every transition in it.
func (w *Watcher) Observe(s alerts.Snapshot) []Event {
	now := s.At
	if now.IsZero() {
		now = time.Now()
	}
	out := w.watchIngest(s, now)
	return append(out, w.watchDestinations(s, now)...)
}

func (w *Watcher) watchIngest(s alerts.Snapshot, now time.Time) []Event {
	if !s.IngestConfigured {
		// Nothing to lose, and nothing to continue. Forgetting the session
		// means re-adding an ingest publishes afresh rather than resuming one
		// that ended while it was not configured.
		w.ing = edgeState{}
		return nil
	}
	published, disconnected := w.ing.observe(s.IngestLive, now, w.cfg.DisconnectAfter)
	switch {
	case published:
		return []Event{{
			Trigger: TriggerIngestPublished, At: now, Source: w.src,
			Reason: "data is arriving on the ingest",
		}}
	case disconnected:
		return []Event{{
			Trigger: TriggerIngestDisconnected, At: now, Source: w.src,
			Reason: "no data for " + w.cfg.DisconnectAfter.String(),
			Error:  s.IngestError,
		}}
	}
	return nil
}

func (w *Watcher) watchDestinations(s alerts.Snapshot, now time.Time) []Event {
	// Sorted so map iteration order can never reach the wire. A receiver
	// correlating deliveries by sequence would otherwise see a different order
	// on every run.
	rows := append([]alerts.DestState(nil), s.Destinations...)
	sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })

	live := make(map[int64]bool, len(rows))
	var out []Event
	for _, d := range rows {
		live[d.ID] = true
		ref := DestinationRef{ID: d.ID, Name: d.Name, Platform: d.Platform}
		w.names[d.ID] = ref

		st := w.dest[d.ID]
		if st == nil {
			st = &edgeState{}
			w.dest[d.ID] = st
		}
		// A disabled destination is OFF rather than exempt. That is the
		// deliberate divergence from alerts, which treats it as "not down"
		// because nobody should be woken for a switch somebody flipped. A hook
		// is a fact: a script mirroring what is live needs the edge whoever
		// caused it.
		up, down := st.observe(d.Enabled && d.Running, now, w.cfg.DestinationDownAfter)
		switch {
		case up:
			out = append(out, Event{
				Trigger: TriggerDestinationUp, At: now, Source: w.src,
				Destination: &ref, Reason: "delivering",
			})
		case down:
			reason := "stopped"
			if !d.Enabled {
				reason = "disabled"
			}
			out = append(out, Event{
				Trigger: TriggerDestinationDown, At: now, Source: w.src,
				Destination: &ref, Reason: reason, Error: d.Error,
			})
		}
	}

	// Rows that disappeared. A destination that was live and has been deleted
	// must not simply vanish: the last thing a script heard was that it came
	// up, and nothing would ever correct that.
	var gone []int64
	for id := range w.dest {
		if !live[id] {
			gone = append(gone, id)
		}
	}
	sort.Slice(gone, func(i, j int) bool { return gone[i] < gone[j] })
	for _, id := range gone {
		if w.dest[id].on {
			ref := w.names[id]
			out = append(out, Event{
				Trigger: TriggerDestinationDown, At: now, Source: w.src,
				Destination: &ref, Reason: "removed",
			})
		}
		delete(w.dest, id)
		delete(w.names, id)
	}
	return out
}
```

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/hooks/ -count=1 -v`
Expected: PASS, all of Task 1's and all eight here.

- [ ] **Step 5: Mutation-test the "only after a publish" rule**

In `edgeState.observe`, temporarily delete the `if !s.on { return false, false }` guard.
Run: `go test ./internal/hooks/ -run TestAnIdleServerNever -count=1`
Expected: FAIL — an idle server announces a disconnection five seconds after boot.
Restore by hand, re-run, confirm PASS.

- [ ] **Step 6: Mutation-test the destination ordering**

Temporarily delete the `sort.Slice(rows, ...)` line.
Run: `go test ./internal/hooks/ -run TestDestinationEventsAreOrderedByID -count=1 -count=1`
Expected: FAIL on most runs (Go randomises map order, but `s.Destinations` is a slice here — so instead verify by reordering the literal in the test's snapshot to 9,2,5 and confirming the assertion still demands a,b,c). Restore, re-run, confirm PASS.

- [ ] **Step 7: Commit**

```bash
gofmt -w internal/hooks/
go test ./internal/hooks/ -count=1
git add internal/hooks/watch.go internal/hooks/watch_test.go
git commit -m "feat(hooks): the publish/disconnect/destination edge detector

Takes the same alerts.Snapshot the alert watcher judges, in the same sweep, and
derives different edges from it. One sampler, two state machines.

edgeState is not alerts.downState with different numbers. It starts everything
OFF and waits to be proven on, where alerts starts things up and waits to be
proven down. That asymmetry is why alerts cannot emit a publish edge at all:
watchIngest only emits ingest.recovered after emitting ingest.lost, so an
install whose streamer connects inside DownFor of boot produces neither.

A disabled destination reports down with reason=disabled, unlike the alert of
the same name. A hook is a fact, not an incident: a script mirroring what is
live needs the edge whoever caused it."
```

---

### Task 3: The envelope, redaction and the signature

**Files:**
- Create: `internal/hooks/payload.go`
- Create: `internal/hooks/payload_test.go`

**Interfaces:**
- Consumes: `alerts.Redact`, `alerts.SecretName` from `internal/alerts/redact.go`.
- Produces: `SpecVersion`; `Envelope`; `Encode(Envelope) ([]byte, error)`; `Sign(secret string, ts int64, body []byte) string`; `NewSecret() (string, error)`; `SignatureHeader`, `TimestampHeader`, `TriggerHeader`, `DeliveryHeader`, `SequenceHeader` constants; `(Event).redacted() Event`.

- [ ] **Step 1: Write the failing test**

Create `internal/hooks/payload_test.go`:

```go
package hooks

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/alerts"
)

const plantedKey = "live_9999_PLANTEDSTREAMKEY"

// The single most important test in this package. Snapshot.IngestError and
// DestState.Error come from supervisor.runOnce, which returns the last three
// FFmpeg STDERR lines -- and an FFmpeg failing to publish prints the whole
// rtmps:// URL, stream key included. Anything that reaches an envelope has to
// go through the same redaction the alert path applies centrally.
func TestNoStreamKeyReachesAnEnvelope(t *testing.T) {
	dirty := "rtmps://live.twitch.tv/app/" + plantedKey + ": Connection refused"

	for _, ev := range []Event{
		{Trigger: TriggerIngestDisconnected, Source: SourceRef{ID: 1, Name: "Main"}, Error: dirty},
		{Trigger: TriggerIngestDisconnected, Source: SourceRef{ID: 1, Name: "Main"}, Reason: dirty},
		{
			Trigger:     TriggerDestinationDown,
			Source:      SourceRef{ID: 1, Name: "Main"},
			Destination: &DestinationRef{ID: 3, Name: "Twitch", Platform: "twitch"},
			Error:       dirty,
		},
	} {
		env := Envelope{
			SpecVersion: SpecVersion, ID: "d1", Sequence: 1,
			Trigger: ev.Trigger, At: time.Unix(0, 0).UTC(),
			Source: ev.Source, Destination: ev.Destination,
			Reason: ev.redacted().Reason, Error: ev.redacted().Error,
		}
		body, err := Encode(env)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(body), plantedKey) {
			t.Fatalf("%s carried a stream key to the wire:\n%s", ev.Trigger, body)
		}
		if !strings.Contains(string(body), alerts.Mask) {
			t.Errorf("%s dropped the error entirely instead of masking it; the "+
				"receiver needs to know something went wrong:\n%s", ev.Trigger, body)
		}
	}
}

// A structural guard, so a field added later cannot smuggle a credential out by
// being named after one. Walks the marshalled object rather than the Go struct,
// because the JSON tag is what actually ships.
func TestNoEnvelopeFieldIsNamedAfterASecret(t *testing.T) {
	body, err := Encode(Envelope{
		SpecVersion: SpecVersion, ID: "d1", Sequence: 1,
		Trigger: TriggerDestinationDown, At: time.Unix(0, 0).UTC(),
		Source:      SourceRef{ID: 1, Name: "Main"},
		Destination: &DestinationRef{ID: 3, Name: "Twitch", Platform: "twitch"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var tree any
	if err := json.Unmarshal(body, &tree); err != nil {
		t.Fatal(err)
	}
	var walk func(path string, v any)
	walk = func(path string, v any) {
		switch node := v.(type) {
		case map[string]any:
			for k, child := range node {
				if alerts.SecretName(k) {
					t.Errorf("envelope field %s%s is named after a credential; "+
						"a hook payload must never carry one", path, k)
				}
				walk(path+k+".", child)
			}
		case []any:
			for _, child := range node {
				walk(path, child)
			}
		}
	}
	walk("", tree)
}

// The signature covers the timestamp AND the body. Signing the body alone lets
// a request captured off the wire be replayed an hour later against a receiver
// that only compares digests.
func TestSignCoversTheTimestamp(t *testing.T) {
	const secret = "topsecret"
	body := []byte(`{"a":1}`)

	got := Sign(secret, 1700000000, body)

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte("1700000000."))
	mac.Write(body)
	want := "v1=" + hex.EncodeToString(mac.Sum(nil))
	if got != want {
		t.Fatalf("Sign = %q, want %q", got, want)
	}
	if other := Sign(secret, 1700000001, body); other == got {
		t.Fatal("the timestamp does not change the signature; a captured body " +
			"can be replayed forever")
	}
}

func TestNewSecretIsLongAndDistinct(t *testing.T) {
	a, err := NewSecret()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := NewSecret()
	if a == b {
		t.Fatal("two generated secrets are identical")
	}
	if len(a) != SecretBytes*2 {
		t.Fatalf("secret is %d hex characters, want %d", len(a), SecretBytes*2)
	}
}

// The envelope is a contract. A receiver written against v1 must keep working,
// so the field names are pinned here and specVersion is bumped if they change.
func TestEnvelopeWireShape(t *testing.T) {
	body, err := Encode(Envelope{
		SpecVersion: SpecVersion, ID: "abc", Sequence: 7, Missed: 2,
		Trigger: TriggerIngestPublished,
		At:      time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC),
		Source:  SourceRef{ID: 1, Name: "Main"},
		Reason:  "data is arriving on the ingest",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"specVersion":"1"`, `"id":"abc"`, `"sequence":7`, `"missed":2`,
		`"trigger":"ingest.published"`, `"at":"2026-07-31T12:00:00Z"`,
		`"source":{"id":1,"name":"Main"}`,
	} {
		if !strings.Contains(string(body), want) {
			t.Errorf("envelope is missing %s:\n%s", want, body)
		}
	}
	// Absent fields must be absent, not null: a receiver switching on the
	// presence of "destination" should not have to also check for null.
	if strings.Contains(string(body), `"destination"`) {
		t.Errorf("an ingest event carried a destination key:\n%s", body)
	}
	if strings.Contains(string(body), `"test"`) {
		t.Errorf("a real delivery is marked as a test:\n%s", body)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/hooks/ -run 'TestNoStreamKey|TestNoEnvelope|TestSign|TestNewSecret|TestEnvelopeWire' -v`
Expected: FAIL to build, `undefined: Envelope`.

- [ ] **Step 3: Write the implementation**

Create `internal/hooks/payload.go`:

```go
package hooks

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"time"

	"github.com/rainmanjam/polyemesis/internal/alerts"
)

// SpecVersion is the envelope contract version. A receiver is written against
// it, so any change to a field's name or meaning bumps this rather than
// silently breaking somebody's script at 3am.
const SpecVersion = "1"

// Delivery headers. Prefixed rather than bare, because a hook endpoint is very
// often a shared automation runner receiving from several systems.
const (
	SignatureHeader = "X-Polyemesis-Signature"
	TimestampHeader = "X-Polyemesis-Timestamp"
	TriggerHeader   = "X-Polyemesis-Trigger"
	DeliveryHeader  = "X-Polyemesis-Delivery"
	SequenceHeader  = "X-Polyemesis-Sequence"
)

// Envelope is the JSON body of every delivery.
//
// Flat on purpose. A hook is consumed by a shell script with jq as often as by
// a program, and `.destination.name` is the deepest anybody should have to
// reach.
type Envelope struct {
	SpecVersion string  `json:"specVersion"`
	ID          string  `json:"id"`
	Trigger     Trigger `json:"trigger"`
	// Sequence counts deliveries to THIS endpoint, from 1, and resets when the
	// process restarts. A receiver that sees it go backwards knows polyemesis
	// restarted -- which matters, because a restarted process has observed
	// nothing and republishes the current state as fresh events.
	Sequence uint64    `json:"sequence"`
	At       time.Time `json:"at"`
	// Missed is how many deliveries to this endpoint were dropped because its
	// queue was full since the last successful one. Zero is omitted. A gap
	// admitted is a gap a receiver can go and reconcile; a gap hidden is a
	// receiver quietly out of date.
	Missed uint64 `json:"missed,omitempty"`
	// Test marks a delivery raised by the test button rather than by anything
	// that happened, so a script can refuse to act on it.
	Test        bool            `json:"test,omitempty"`
	Source      SourceRef       `json:"source"`
	Destination *DestinationRef `json:"destination,omitempty"`
	Reason      string          `json:"reason,omitempty"`
	Error       string          `json:"error,omitempty"`
}

// Encode marshals an envelope. The bytes it returns are exactly what is signed
// and exactly what is sent -- re-marshalling between signing and sending is how
// a signature scheme quietly stops verifying.
func Encode(e Envelope) ([]byte, error) { return json.Marshal(e) }

// Sign returns the value for SignatureHeader.
//
// The timestamp is signed WITH the body rather than merely sent beside it. A
// digest over the body alone means a request captured off the wire can be
// replayed an hour later against a receiver that only compares digests; with
// the timestamp inside the MAC, a receiver can reject anything older than its
// own tolerance and the attacker cannot re-stamp it.
func Sign(secret string, ts int64, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(strconv.FormatInt(ts, 10)))
	mac.Write([]byte("."))
	mac.Write(body)
	return "v1=" + hex.EncodeToString(mac.Sum(nil))
}

// NewSecret mints a signing key. Called whenever a hook is created without one,
// because an unsigned webhook is one that anybody who learns the URL can forge
// -- and a URL leaks through proxy logs, browser history and screenshots.
func NewSecret() (string, error) {
	buf := make([]byte, SecretBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// redacted scrubs the free text on an event.
//
// Applied by Dispatcher.Publish rather than left to the watcher, for the reason
// stated at alerts/redact.go:179 -- the strings arrive from a dozen places and
// exactly one careless one puts a stream key in somebody's automation log.
// Error in particular is FFmpeg stderr, which prints the full publish URL.
func (e Event) redacted() Event {
	e.Reason = alerts.Redact(e.Reason)
	e.Error = alerts.Redact(e.Error)
	if e.Destination != nil {
		d := *e.Destination
		d.Name = alerts.Redact(d.Name)
		e.Destination = &d
	}
	e.Source.Name = alerts.Redact(e.Source.Name)
	return e
}
```

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/hooks/ -count=1 -v`
Expected: PASS.

- [ ] **Step 5: Mutation-test the redaction**

In `redacted`, temporarily change `e.Error = alerts.Redact(e.Error)` to `e.Error = e.Error`.
Run: `go test ./internal/hooks/ -run TestNoStreamKeyReachesAnEnvelope -count=1`
Expected: FAIL naming the planted key. Restore, re-run, confirm PASS.

- [ ] **Step 6: Commit**

```bash
gofmt -w internal/hooks/
git add internal/hooks/payload.go internal/hooks/payload_test.go
git commit -m "feat(hooks): the signed envelope, and the redaction that guards it

The body is a flat versioned envelope with a per-endpoint sequence number and a
'missed' counter, so a receiver can detect both a restart and a dropped
delivery rather than being quietly out of date.

The HMAC covers the timestamp AND the body. A digest over the body alone lets a
request captured off the wire be replayed an hour later.

Redaction is applied centrally in the publish path, reusing alerts.Redact.
Snapshot errors are FFmpeg stderr, and an FFmpeg that cannot publish prints the
whole rtmps:// URL with the stream key on the end."
```

---

### Task 4: The dispatcher — ordered, non-blocking, bounded

**Files:**
- Create: `internal/hooks/dispatch.go`
- Create: `internal/hooks/dispatch_test.go`

**Interfaces:**
- Consumes: `Hook`, `Event`, `Envelope`, `Encode`, `Sign` from Tasks 1 and 3.
- Produces: `Doer`; `Source` interface with `Hooks() ([]Hook, error)`; `SourceFunc`; `Option` (`WithDoer`, `WithClock`, `WithSleep`, `WithReloadInterval`); `NewDispatcher(*slog.Logger, Source, ...Option) *Dispatcher`; `(*Dispatcher).Publish(Event)`, `.Run(context.Context)`, `.HasHooks() bool`, `.Test(context.Context, Hook) (TestResult, error)`, `.Deliveries(int64) []DeliveryRecord`, `.Stats() Stats`.

- [ ] **Step 1: Write the failing test**

Create `internal/hooks/dispatch_test.go`:

```go
package hooks

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// recorder is a Doer that records every request and can be made arbitrarily
// slow, which is how the "never blocks" property is proved without a socket.
type recorder struct {
	mu     sync.Mutex
	bodies []string
	seqs   []string
	block  chan struct{}
	status int
}

func (r *recorder) Do(req *http.Request) (*http.Response, error) {
	if r.block != nil {
		<-r.block
	}
	body, _ := io.ReadAll(req.Body)
	r.mu.Lock()
	r.bodies = append(r.bodies, string(body))
	r.seqs = append(r.seqs, req.Header.Get(SequenceHeader))
	r.mu.Unlock()
	code := r.status
	if code == 0 {
		code = 200
	}
	return &http.Response{
		StatusCode: code,
		Body:       io.NopCloser(strings.NewReader("ok")),
		Header:     http.Header{},
	}, nil
}

func (r *recorder) seen() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.bodies...)
}

func oneHook() []Hook {
	return []Hook{Hook{
		ID: 1, Name: "deploy", Enabled: true,
		URL: "https://example.com/h", Secret: "s3cr3t",
	}.Normalized()}
}

func runDispatcher(t *testing.T, d *Dispatcher) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); d.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("Run did not return within 5s of cancellation")
		}
	})
}

// The property the whole design exists to protect. Publish is called from the
// engine's sweep goroutine; if a webhook endpoint that never answers can hold
// it up, one dead URL stalls the loop that also raises every alert.
func TestPublishNeverBlocksOnADeadEndpoint(t *testing.T) {
	rec := &recorder{block: make(chan struct{})}
	d := NewDispatcher(testLogger(t), SourceFunc(func() ([]Hook, error) { return oneHook(), nil }),
		WithDoer(rec), WithReloadInterval(10*time.Millisecond))
	runDispatcher(t, d)
	waitFor(t, func() bool { return d.HasHooks() })

	done := make(chan struct{})
	go func() {
		defer close(done)
		// Far more than the queue depth, so the drop path is exercised too.
		for i := 0; i < queueDepth*4; i++ {
			d.Publish(Event{Trigger: TriggerIngestPublished, At: time.Now(), Source: SourceRef{ID: 1}})
		}
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Publish blocked while the endpoint hung; the engine sweep " +
			"would be stalled behind one dead webhook")
	}
	close(rec.block)

	waitFor(t, func() bool { return d.Stats().Dropped > 0 })
}

// Ordering is the promise a script depends on. "disconnected" arriving before
// "published" makes an automation believe the stream is down while it is live.
func TestDeliveriesToOneEndpointKeepTheirOrder(t *testing.T) {
	rec := &recorder{}
	d := NewDispatcher(testLogger(t), SourceFunc(func() ([]Hook, error) { return oneHook(), nil }),
		WithDoer(rec), WithReloadInterval(10*time.Millisecond))
	runDispatcher(t, d)
	waitFor(t, func() bool { return d.HasHooks() })

	want := []Trigger{
		TriggerIngestPublished, TriggerDestinationUp,
		TriggerDestinationDown, TriggerIngestDisconnected,
	}
	for _, tr := range want {
		d.Publish(Event{Trigger: tr, At: time.Now(), Source: SourceRef{ID: 1}})
	}
	waitFor(t, func() bool { return len(rec.seen()) == len(want) })

	for i, tr := range want {
		if !strings.Contains(rec.seen()[i], `"trigger":"`+string(tr)+`"`) {
			t.Fatalf("delivery %d = %s, want trigger %s", i, rec.seen()[i], tr)
		}
	}
	// And the sequence numbers count from one, so a receiver can spot a gap.
	rec.mu.Lock()
	defer rec.mu.Unlock()
	for i, s := range rec.seqs {
		if s != itoa(i+1) {
			t.Errorf("delivery %d carried sequence %q, want %d", i, s, i+1)
		}
	}
}

// A dropped delivery is admitted on the next one. A receiver that is told it
// missed two events can go and reconcile; one that is not told is quietly wrong
// and has no way to find out.
func TestADroppedDeliveryIsReportedOnTheNextOne(t *testing.T) {
	rec := &recorder{block: make(chan struct{})}
	d := NewDispatcher(testLogger(t), SourceFunc(func() ([]Hook, error) { return oneHook(), nil }),
		WithDoer(rec), WithReloadInterval(10*time.Millisecond))
	runDispatcher(t, d)
	waitFor(t, func() bool { return d.HasHooks() })

	for i := 0; i < queueDepth*3; i++ {
		d.Publish(Event{Trigger: TriggerIngestPublished, At: time.Now(), Source: SourceRef{ID: 1}})
	}
	waitFor(t, func() bool { return d.Stats().Dropped > 0 })
	close(rec.block)

	waitFor(t, func() bool {
		for _, b := range rec.seen() {
			if strings.Contains(b, `"missed":`) {
				return true
			}
		}
		return false
	})
}

// A 4xx is the endpoint saying the request is wrong. Retrying it four times
// only delays every delivery queued behind it -- and behind it is the whole
// point, because ordering means one endpoint's retries are its own head-of-line
// blocking.
func TestA404IsNotRetried(t *testing.T) {
	rec := &recorder{status: 404}
	d := NewDispatcher(testLogger(t), SourceFunc(func() ([]Hook, error) { return oneHook(), nil }),
		WithDoer(rec), WithReloadInterval(10*time.Millisecond),
		WithSleep(func(context.Context, time.Duration) bool { return true }))
	runDispatcher(t, d)
	waitFor(t, func() bool { return d.HasHooks() })

	d.Publish(Event{Trigger: TriggerIngestPublished, At: time.Now(), Source: SourceRef{ID: 1}})
	waitFor(t, func() bool { return d.Stats().Failed == 1 })

	if n := len(rec.seen()); n != 1 {
		t.Fatalf("attempted %d times for a 404, want 1", n)
	}
}

func TestA503IsRetried(t *testing.T) {
	rec := &recorder{status: 503}
	d := NewDispatcher(testLogger(t), SourceFunc(func() ([]Hook, error) { return oneHook(), nil }),
		WithDoer(rec), WithReloadInterval(10*time.Millisecond),
		WithSleep(func(context.Context, time.Duration) bool { return true }))
	runDispatcher(t, d)
	waitFor(t, func() bool { return d.HasHooks() })

	d.Publish(Event{Trigger: TriggerIngestPublished, At: time.Now(), Source: SourceRef{ID: 1}})
	waitFor(t, func() bool { return d.Stats().Failed == 1 })

	if n := len(rec.seen()); n != DefaultAttempts {
		t.Fatalf("attempted %d times for a 503, want %d", n, DefaultAttempts)
	}
}

func TestASubscriptionFilterIsHonoured(t *testing.T) {
	rec := &recorder{}
	narrow := Hook{
		ID: 1, Name: "deploy", Enabled: true, URL: "https://example.com/h",
		Secret: "s", Triggers: []Trigger{TriggerDestinationDown},
	}.Normalized()
	d := NewDispatcher(testLogger(t), SourceFunc(func() ([]Hook, error) { return []Hook{narrow}, nil }),
		WithDoer(rec), WithReloadInterval(10*time.Millisecond))
	runDispatcher(t, d)
	waitFor(t, func() bool { return d.HasHooks() })

	d.Publish(Event{Trigger: TriggerIngestPublished, At: time.Now(), Source: SourceRef{ID: 1}})
	d.Publish(Event{Trigger: TriggerDestinationDown, At: time.Now(), Source: SourceRef{ID: 1}})
	waitFor(t, func() bool { return len(rec.seen()) == 1 })

	time.Sleep(100 * time.Millisecond)
	if n := len(rec.seen()); n != 1 {
		t.Fatalf("delivered %d, want only the subscribed trigger", n)
	}
	if !strings.Contains(rec.seen()[0], `"trigger":"destination.down"`) {
		t.Fatalf("delivered the wrong one: %s", rec.seen()[0])
	}
}

// Every delivery is signed with the value a receiver will verify.
func TestEveryDeliveryCarriesAVerifiableSignature(t *testing.T) {
	var gotSig, gotTS string
	var gotBody []byte
	verify := doerFunc(func(req *http.Request) (*http.Response, error) {
		gotBody, _ = io.ReadAll(req.Body)
		gotSig = req.Header.Get(SignatureHeader)
		gotTS = req.Header.Get(TimestampHeader)
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("")), Header: http.Header{}}, nil
	})
	d := NewDispatcher(testLogger(t), SourceFunc(func() ([]Hook, error) { return oneHook(), nil }),
		WithDoer(verify), WithReloadInterval(10*time.Millisecond))
	runDispatcher(t, d)
	waitFor(t, func() bool { return d.HasHooks() })

	d.Publish(Event{Trigger: TriggerIngestPublished, At: time.Now(), Source: SourceRef{ID: 1}})
	waitFor(t, func() bool { return gotSig != "" })

	ts := atoi64(t, gotTS)
	if want := Sign("s3cr3t", ts, gotBody); gotSig != want {
		t.Fatalf("signature = %q, want %q -- a receiver verifying this would "+
			"reject every delivery", gotSig, want)
	}
}
```

Also append the small helpers to the same file:

```go
type doerFunc func(*http.Request) (*http.Response, error)

func (f doerFunc) Do(r *http.Request) (*http.Response, error) { return f(r) }

func testLogger(t *testing.T) *slog.Logger {
	t.Helper()
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// waitFor polls rather than sleeping a fixed interval, so the suite is neither
// flaky on a loaded machine nor slow on an idle one.
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("condition not reached within 3s")
}

func itoa(i int) string { return strconv.Itoa(i) }

func atoi64(t *testing.T, s string) int64 {
	t.Helper()
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		t.Fatalf("timestamp header %q is not a number: %v", s, err)
	}
	return v
}
```

Add `"log/slog"` and `"strconv"` to the test file's imports.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/hooks/ -run TestPublishNeverBlocks -v`
Expected: FAIL to build, `undefined: NewDispatcher`.

- [ ] **Step 3: Write the implementation**

Create `internal/hooks/dispatch.go`:

```go
package hooks

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rainmanjam/polyemesis/internal/alerts"
)

const (
	// queueDepth is how many deliveries one endpoint may fall behind before
	// they start being dropped. Deep enough to absorb a slow endpoint through a
	// go-live burst (one publish plus one up per destination), shallow enough
	// that a dead endpoint cannot hold megabytes of history.
	queueDepth = 64
	// intakeDepth buffers the hand-off from Publish to the fan-out goroutine.
	intakeDepth = 256
	// reloadEvery is how often the hook list is re-read, so a hook added or
	// edited takes effect without a restart. Same idea as the notifier's rule
	// cache; the interval is longer because nothing here is on a hot path.
	reloadEvery = 5 * time.Second
	// backoffBase and backoffMax bound a retry. Deliberately small: a retry
	// blocks the endpoint's own queue, because ordering is the promise.
	backoffBase = time.Second
	backoffMax  = 8 * time.Second
	// logRing is how many recent deliveries per hook the operator can inspect.
	// This is the answer to "did my hook fire" without a packet capture.
	logRing = 50
	// bodySnippet bounds what a response body contributes to the delivery log.
	bodySnippet = 512
)

// Doer is the HTTP client, narrowed so a test can count attempts without a
// listening socket. Same shape as alerts.Doer, and deliberately a second
// declaration rather than an import: internal/hooks must not depend on
// internal/alerts for anything but redaction and the Snapshot type.
type Doer interface {
	Do(req *http.Request) (*http.Response, error)
}

// Source supplies the enabled hooks, with their secrets decrypted. An interface
// rather than *db.DB so the package is testable without a database, and so an
// edited hook is picked up without a restart.
type Source interface {
	Hooks() ([]Hook, error)
}

// SourceFunc adapts a function to a Source.
type SourceFunc func() ([]Hook, error)

func (f SourceFunc) Hooks() ([]Hook, error) { return f() }

// Stats is what the dispatcher will admit to.
type Stats struct {
	Queued  int64 `json:"queued"`
	Dropped int64 `json:"dropped"`
	Sent    int64 `json:"sent"`
	Failed  int64 `json:"failed"`
	Retries int64 `json:"retries"`
	// Endpoints is how many workers are running, i.e. enabled hooks.
	Endpoints int       `json:"endpoints"`
	LastSent  time.Time `json:"lastSent,omitempty"`
	// LastError has already been through alerts.Redact.
	LastError string `json:"lastError,omitempty"`
}

// DeliveryRecord is one attempt's outcome, kept in memory so an operator can
// answer "did my hook fire, and what did the endpoint say" without a packet
// capture. Not persisted: this is a debugging aid, and a table of every
// delivery on a busy install is a database that grows without an operator ever
// choosing it.
type DeliveryRecord struct {
	HookID     int64     `json:"hookId"`
	Trigger    Trigger   `json:"trigger"`
	Sequence   uint64    `json:"sequence"`
	ID         string    `json:"id"`
	At         time.Time `json:"at"`
	Attempts   int       `json:"attempts"`
	Status     int       `json:"status,omitempty"`
	DurationMS int64     `json:"durationMs"`
	// Error and Response are both redacted; an endpoint that echoes the request
	// back would otherwise put the payload's own free text into the log twice.
	Error    string `json:"error,omitempty"`
	Response string `json:"response,omitempty"`
}

// TestResult is what the test button reports. It is deliberately richer than
// the alert equivalent's "sent": the operator is testing a machine contract, so
// the status code and the body are the answer, not a green tick.
type TestResult struct {
	Status     int    `json:"status"`
	DurationMS int64  `json:"durationMs"`
	Response   string `json:"response,omitempty"`
	Body       string `json:"body"`
	Signature  string `json:"signature"`
}

// Option configures a Dispatcher.
type Option func(*Dispatcher)

// WithDoer replaces the HTTP client.
func WithDoer(d Doer) Option { return func(x *Dispatcher) { x.doer = d } }

// WithClock replaces time.Now, for tests.
func WithClock(fn func() time.Time) Option { return func(x *Dispatcher) { x.now = fn } }

// WithSleep replaces the retry wait. Returning false means the context ended.
func WithSleep(fn func(context.Context, time.Duration) bool) Option {
	return func(x *Dispatcher) { x.sleep = fn }
}

// WithReloadInterval sets how often the hook list is re-read.
func WithReloadInterval(d time.Duration) Option {
	return func(x *Dispatcher) {
		if d > 0 {
			x.reloadEvery = d
		}
	}
}

// worker owns one endpoint: its queue, its goroutine and its sequence number.
//
// One goroutine per hook rather than a shared pool, because ORDER is the
// promise. A pool would deliver "ingest.disconnected" before "ingest.published"
// whenever the first attempt at the earlier one was slower, and an automation
// told the stream is down while it is live is worse than no automation.
type worker struct {
	ch     chan Event
	stop   func()
	done   chan struct{}
	seq    atomic.Uint64
	missed atomic.Uint64

	mu   sync.Mutex
	hook Hook
	log  []DeliveryRecord
}

// Dispatcher delivers events to every subscribed endpoint.
//
// One per process, not one per engine: sequence numbers, the delivery log and
// the worker set all belong to the endpoint, and an endpoint is shared by every
// source. Compare relay.PortAllocator, which is shared for the same class of
// reason.
type Dispatcher struct {
	log         *slog.Logger
	src         Source
	doer        Doer
	now         func() time.Time
	sleep       func(context.Context, time.Duration) bool
	reloadEvery time.Duration

	intake chan Event

	mu      sync.Mutex
	workers map[int64]*worker
	stats   Stats
}

// NewDispatcher builds a dispatcher. It does nothing until Run is called, which
// is what lets main wire it before it has a context.
func NewDispatcher(log *slog.Logger, src Source, opts ...Option) *Dispatcher {
	d := &Dispatcher{
		log:         log,
		src:         src,
		doer:        &http.Client{},
		now:         time.Now,
		sleep:       sleepCtx,
		reloadEvery: reloadEvery,
		intake:      make(chan Event, intakeDepth),
		workers:     map[int64]*worker{},
	}
	for _, o := range opts {
		o(d)
	}
	return d
}

// Publish accepts a transition. It never blocks and never returns an error.
//
// The caller is the engine's sweep goroutine, which also raises every alert. A
// webhook endpoint that never answers must not be able to stall it -- that is
// the same argument alerts.Notifier.Publish makes, and it is the reason both
// are a non-blocking send onto a bounded queue rather than an HTTP call.
func (d *Dispatcher) Publish(ev Event) {
	if d == nil {
		return
	}
	if ev.At.IsZero() {
		ev.At = d.now()
	}
	select {
	case d.intake <- ev.redacted():
		d.bump(func(s *Stats) { s.Queued++ })
	default:
		d.bump(func(s *Stats) { s.Dropped++ })
	}
}

// HasHooks reports whether anybody is listening, so the engine can skip work
// nothing would read.
func (d *Dispatcher) HasHooks() bool {
	if d == nil {
		return false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.workers) > 0
}

// Stats reports what has happened so far.
func (d *Dispatcher) Stats() Stats {
	if d == nil {
		return Stats{}
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	out := d.stats
	out.Endpoints = len(d.workers)
	return out
}

// Deliveries returns one hook's recent attempts, newest last.
func (d *Dispatcher) Deliveries(hookID int64) []DeliveryRecord {
	if d == nil {
		return nil
	}
	d.mu.Lock()
	w := d.workers[hookID]
	d.mu.Unlock()
	if w == nil {
		return []DeliveryRecord{}
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]DeliveryRecord(nil), w.log...)
}

// Run drives the dispatcher until ctx ends.
func (d *Dispatcher) Run(ctx context.Context) {
	d.reload()
	tick := time.NewTicker(d.reloadEvery)
	defer tick.Stop()

	for {
		select {
		case <-ctx.Done():
			d.stopAll()
			return
		case <-tick.C:
			d.reload()
		case ev := <-d.intake:
			d.fanOut(ev)
		}
	}
}

// fanOut hands one event to every subscribed endpoint's queue. Non-blocking per
// endpoint: a full queue means that endpoint is too slow, and the count rides
// out on its next successful delivery as "missed".
func (d *Dispatcher) fanOut(ev Event) {
	d.mu.Lock()
	targets := make([]*worker, 0, len(d.workers))
	for _, w := range d.workers {
		w.mu.Lock()
		wants := w.hook.Wants(ev.Trigger)
		w.mu.Unlock()
		if wants {
			targets = append(targets, w)
		}
	}
	d.mu.Unlock()

	for _, w := range targets {
		select {
		case w.ch <- ev:
		default:
			w.missed.Add(1)
			d.bump(func(s *Stats) { s.Dropped++ })
		}
	}
}

// reload diffs the stored hooks against the running workers.
//
// An EDITED hook keeps its worker, its queue and its sequence number: only the
// value is swapped. Restarting the worker would throw away whatever is queued
// and reset the sequence, so renaming a hook would look to the receiver exactly
// like polyemesis restarting.
func (d *Dispatcher) reload() {
	rows, err := d.src.Hooks()
	if err != nil {
		// Keep the current set. A database hiccup must not be the reason a
		// go-live went unannounced.
		d.log.Warn("cannot read hooks; keeping the running set", "err", err)
		return
	}
	want := make(map[int64]Hook, len(rows))
	for _, h := range rows {
		n := h.Normalized()
		if n.Enabled && n.Validate() == nil {
			want[n.ID] = n
		}
	}

	d.mu.Lock()
	var stopping []*worker
	for id, w := range d.workers {
		if _, keep := want[id]; !keep {
			stopping = append(stopping, w)
			delete(d.workers, id)
		}
	}
	for id, h := range want {
		if w := d.workers[id]; w != nil {
			w.mu.Lock()
			w.hook = h
			w.mu.Unlock()
			continue
		}
		d.workers[id] = d.startWorker(h)
	}
	d.mu.Unlock()

	for _, w := range stopping {
		w.stop()
	}
}

// startWorker spawns one endpoint's goroutine. Called with d.mu held.
func (d *Dispatcher) startWorker(h Hook) *worker {
	ctx, cancel := context.WithCancel(context.Background())
	w := &worker{
		ch:   make(chan Event, queueDepth),
		stop: cancel,
		done: make(chan struct{}),
		hook: h,
	}
	go func() {
		defer close(w.done)
		for {
			select {
			case <-ctx.Done():
				return
			case ev := <-w.ch:
				d.deliver(ctx, w, ev)
			}
		}
	}()
	return w
}

func (d *Dispatcher) stopAll() {
	d.mu.Lock()
	all := make([]*worker, 0, len(d.workers))
	for id, w := range d.workers {
		all = append(all, w)
		delete(d.workers, id)
	}
	d.mu.Unlock()
	for _, w := range all {
		w.stop()
	}
}

// deliver builds, signs and posts one envelope, with bounded retries.
func (d *Dispatcher) deliver(ctx context.Context, w *worker, ev Event) {
	w.mu.Lock()
	hook := w.hook
	w.mu.Unlock()

	id, err := deliveryID()
	if err != nil {
		d.log.Warn("cannot mint a delivery id", "err", err)
		return
	}
	env := Envelope{
		SpecVersion: SpecVersion,
		ID:          id,
		Trigger:     ev.Trigger,
		Sequence:    w.seq.Add(1),
		At:          ev.At.UTC(),
		Missed:      w.missed.Swap(0),
		Source:      ev.Source,
		Destination: ev.Destination,
		Reason:      ev.Reason,
		Error:       ev.Error,
	}
	body, err := Encode(env)
	if err != nil {
		d.log.Warn("cannot encode a hook payload", "trigger", ev.Trigger, "err", err)
		return
	}

	started := d.now()
	rec := DeliveryRecord{
		HookID: hook.ID, Trigger: env.Trigger, Sequence: env.Sequence,
		ID: env.ID, At: started,
	}
	for attempt := 1; attempt <= hook.MaxAttempts; attempt++ {
		if attempt > 1 {
			d.bump(func(s *Stats) { s.Retries++ })
		}
		rec.Attempts = attempt
		status, snippet, retry, err := d.attempt(ctx, hook, body, env)
		rec.Status, rec.Response = status, snippet
		if err == nil {
			rec.DurationMS = d.now().Sub(started).Milliseconds()
			sent := d.now()
			d.bump(func(s *Stats) { s.Sent++; s.LastSent = sent; s.LastError = "" })
			w.record(rec)
			return
		}
		rec.Error = alerts.Redact(err.Error())
		if !retry || attempt == hook.MaxAttempts {
			break
		}
		if !d.sleep(ctx, backoffFor(attempt)) {
			break
		}
	}
	rec.DurationMS = d.now().Sub(started).Milliseconds()
	lastErr := rec.Error
	d.bump(func(s *Stats) { s.Failed++; s.LastError = lastErr })
	d.log.Warn("hook delivery failed",
		"hook", hook.Name, "url", hook.RedactedURL(),
		"trigger", env.Trigger, "err", lastErr)
	w.record(rec)
}

// attempt performs one POST. retry reports whether another try could help.
//
// The classification is the same one alerts/notify.go:423 makes, and for the
// same reason: a 404 from a deleted endpoint is permanent, and retrying it only
// delays everything behind it -- which here means everything queued for THIS
// endpoint, because ordering is preserved by never overtaking.
func (d *Dispatcher) attempt(ctx context.Context, h Hook, body []byte, env Envelope) (status int, snippet string, retry bool, err error) {
	reqCtx, cancel := context.WithTimeout(ctx, time.Duration(h.TimeoutSeconds)*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, h.URL, bytes.NewReader(body))
	if err != nil {
		return 0, "", false, err
	}
	ts := d.now().Unix()
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "polyemesis")
	req.Header.Set(TimestampHeader, strconv.FormatInt(ts, 10))
	req.Header.Set(TriggerHeader, string(env.Trigger))
	req.Header.Set(DeliveryHeader, env.ID)
	req.Header.Set(SequenceHeader, strconv.FormatUint(env.Sequence, 10))
	if h.Secret != "" {
		req.Header.Set(SignatureHeader, Sign(h.Secret, ts, body))
	}

	resp, err := d.doer.Do(req)
	if err != nil {
		// Nothing was delivered and nothing said it never would be.
		return 0, "", true, err
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
		_ = resp.Body.Close()
	}()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, bodySnippet))
	snippet = alerts.Redact(string(raw))

	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return resp.StatusCode, snippet, false, nil
	case resp.StatusCode == http.StatusTooManyRequests,
		resp.StatusCode == http.StatusRequestTimeout,
		resp.StatusCode >= 500:
		return resp.StatusCode, snippet, true, statusError(resp.StatusCode)
	default:
		return resp.StatusCode, snippet, false, statusError(resp.StatusCode)
	}
}

// Test delivers one synthetic envelope to a single endpoint immediately,
// skipping the queue and the subscription filter, and reports what the endpoint
// said.
//
// It returns the body and the signature it sent, not just a verdict. The
// operator is testing a machine contract: "sent" tells them nothing about
// whether their verification code agrees, and this is the whole answer to
// "how do I test a hook without going live".
func (d *Dispatcher) Test(ctx context.Context, h Hook, tr Trigger) (TestResult, error) {
	hook := h.Normalized()
	id, err := deliveryID()
	if err != nil {
		return TestResult{}, err
	}
	if !KnownTrigger(tr) {
		tr = TriggerIngestPublished
	}
	env := Envelope{
		SpecVersion: SpecVersion, ID: id, Trigger: tr, Sequence: 0,
		At: d.now().UTC(), Test: true,
		Source: SourceRef{ID: 0, Name: "test"},
		Reason: "test delivery from polyemesis",
	}
	if tr == TriggerDestinationUp || tr == TriggerDestinationDown {
		env.Destination = &DestinationRef{ID: 0, Name: "Example destination", Platform: "custom"}
	}
	body, err := Encode(env)
	if err != nil {
		return TestResult{}, err
	}
	started := d.now()
	status, snippet, _, err := d.attempt(ctx, hook, body, env)
	res := TestResult{
		Status:     status,
		DurationMS: d.now().Sub(started).Milliseconds(),
		Response:   snippet,
		Body:       string(body),
	}
	if hook.Secret != "" {
		res.Signature = Sign(hook.Secret, d.now().Unix(), body)
	}
	return res, err
}

func (d *Dispatcher) bump(fn func(*Stats)) {
	d.mu.Lock()
	fn(&d.stats)
	d.mu.Unlock()
}

func (w *worker) record(r DeliveryRecord) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.log = append(w.log, r)
	if len(w.log) > logRing {
		w.log = append(w.log[:0:0], w.log[len(w.log)-logRing:]...)
	}
}

// backoffFor is exponential and capped. No jitter: one worker posts to one
// endpoint, so there is no herd to spread out and a deterministic schedule is
// testable.
func backoffFor(attempt int) time.Duration {
	d := backoffBase
	for i := 1; i < attempt; i++ {
		d *= 2
		if d >= backoffMax {
			return backoffMax
		}
	}
	return d
}

func deliveryID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

type statusError int

func (e statusError) Error() string { return "endpoint returned HTTP " + strconv.Itoa(int(e)) }

func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// sortedIDs exists so the meta endpoint and the tests can list endpoints in a
// stable order.
func sortedIDs(m map[int64]*worker) []int64 {
	out := make([]int64, 0, len(m))
	for id := range m {
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
```

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/hooks/ -count=1 -race -v`
Expected: PASS, with no race reports. `-race` is not optional here: the dispatcher is the only part of this feature with concurrent state.

- [ ] **Step 5: Mutation-test the non-blocking guarantee**

In `Publish`, temporarily replace the `select` with a plain `d.intake <- ev.redacted()`.
Run: `go test ./internal/hooks/ -run TestPublishNeverBlocksOnADeadEndpoint -count=1 -timeout 30s`
Expected: FAIL with "Publish blocked while the endpoint hung". Restore, re-run, confirm PASS.

- [ ] **Step 6: Mutation-test the retry classification**

In `attempt`, temporarily move `resp.StatusCode >= 400` into the retryable branch.
Run: `go test ./internal/hooks/ -run TestA404IsNotRetried -count=1`
Expected: FAIL with "attempted 3 times for a 404". Restore, re-run, confirm PASS.

- [ ] **Step 7: Commit**

```bash
gofmt -w internal/hooks/
go test ./internal/hooks/ -race -count=1
git add internal/hooks/dispatch.go internal/hooks/dispatch_test.go
git commit -m "feat(hooks): the dispatcher -- ordered per endpoint, never blocking

One goroutine and one bounded queue per endpoint. A pool would deliver
ingest.disconnected before ingest.published whenever the earlier attempt was
slower, and an automation told the stream is down while it is live is worse
than no automation.

Ordering is bought with head-of-line blocking inside one endpoint, bounded at
MaxAttempts x (timeout + backoff) = 33s at defaults. Other endpoints are
unaffected, which is why the queue is per hook and not global.

Publish is a non-blocking send. It is called from the engine sweep that also
raises every alert, so one dead webhook must not be able to stall it.

An edited hook keeps its worker, queue and sequence number: restarting it would
make a rename look to the receiver exactly like polyemesis restarting."
```

---

### Task 5: Persistence — the hooks table and the sealed secret

**Files:**
- Modify: `internal/db/schema.sql` (after the `alert_rules` block, ~line 230, and the index block ~line 454)
- Create: `internal/db/hooks.go`
- Create: `internal/db/hooks_test.go`

**Interfaces:**
- Consumes: `hooks.Hook`, `secrets.Box` (`Seal`/`Open`, internal/secrets/secrets.go:98/:110).
- Produces: `(*DB).ListHooks(*secrets.Box) ([]hooks.Hook, error)`; `.EnabledHooks(*secrets.Box)`; `.GetHook(*secrets.Box, int64)`; `.CreateHook(*secrets.Box, *hooks.Hook) (*hooks.Hook, string, error)`; `.UpdateHook(*secrets.Box, *hooks.Hook)`; `.DeleteHook(int64)`.

- [ ] **Step 1: Add the table**

In `internal/db/schema.sql`, immediately after the `alert_rules` table (which ends at line 230):

```sql
-- Lifecycle webhooks. Distinct from alert_rules, which are for a human reading
-- Slack: an alert coalesces ("12 times") and debounces, and a hook must not,
-- because a script cannot act on eleven events it was never given.
--
-- secret is SEALED, not plaintext, unlike alert_rules.url. It is an HMAC key
-- rather than a capability URL: it is used to prove a payload came from here,
-- so anybody who reads the database file can forge deliveries with it, and
-- unlike a webhook URL it is never displayed and so never needs to be
-- recovered in plaintext by a human.
CREATE TABLE IF NOT EXISTS hooks (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    name            TEXT    NOT NULL,
    enabled         INTEGER NOT NULL DEFAULT 1,
    url             TEXT    NOT NULL,
    secret          BLOB    NOT NULL,
    triggers        TEXT    NOT NULL DEFAULT '[]',   -- JSON array; empty = every trigger
    timeout_seconds INTEGER NOT NULL DEFAULT 10,
    max_attempts    INTEGER NOT NULL DEFAULT 3,
    created_at      INTEGER NOT NULL,
    updated_at      INTEGER NOT NULL
);
```

And with the other indexes (near line 454):

```sql
CREATE INDEX IF NOT EXISTS idx_hooks_enabled ON hooks(enabled, id);
```

No `Migrate*` function is needed. `CREATE TABLE IF NOT EXISTS` covers a NEW table; the migration helpers in `db.Open` exist only because that statement cannot add a COLUMN to a table that already exists (db.go:48-49).

- [ ] **Step 2: Write the failing test**

Create `internal/db/hooks_test.go`:

```go
package db

import (
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/hooks"
	"github.com/rainmanjam/polyemesis/internal/secrets"
)

func testBox(t *testing.T) *secrets.Box {
	t.Helper()
	box, err := secrets.New(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	return box
}

func validHook() *hooks.Hook {
	return &hooks.Hook{
		Name: "deploy", Enabled: true,
		URL:      "https://hooks.example.com/services/T0/B1/XXXXsecretXXXX",
		Triggers: []hooks.Trigger{hooks.TriggerIngestPublished},
	}
}

func TestHookRoundTrips(t *testing.T) {
	d := testDB(t)
	box := testBox(t)

	created, plaintext, err := d.CreateHook(box, validHook())
	if err != nil {
		t.Fatalf("CreateHook: %v", err)
	}
	if plaintext == "" {
		t.Fatal("no plaintext secret returned; the operator can never see it " +
			"again, so create is the only chance to hand it over")
	}
	got, err := d.GetHook(box, created.ID)
	if err != nil {
		t.Fatalf("GetHook: %v", err)
	}
	if got.URL != validHook().URL {
		t.Errorf("url = %q, want the stored one", got.URL)
	}
	if got.Secret != plaintext {
		t.Errorf("secret did not survive the round trip; every signature this "+
			"hook sends would be unverifiable")
	}
	if len(got.Triggers) != 1 || got.Triggers[0] != hooks.TriggerIngestPublished {
		t.Errorf("triggers = %v, want [ingest.published]", got.Triggers)
	}
}

// The secret is sealed, not stored in the clear. A database file copied off a
// backup drive must not hand somebody the ability to forge deliveries.
func TestHookSecretIsSealedOnDisk(t *testing.T) {
	d := testDB(t)
	box := testBox(t)

	created, plaintext, err := d.CreateHook(box, validHook())
	if err != nil {
		t.Fatal(err)
	}
	var raw []byte
	if err := d.SQL().QueryRow(`SELECT secret FROM hooks WHERE id = ?`, created.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), plaintext) {
		t.Fatal("the signing secret is stored in the clear")
	}
}

// An operator supplying their own secret must get theirs, not a generated one:
// they are pasting it into the receiver at the same moment.
func TestASuppliedSecretIsKept(t *testing.T) {
	d := testDB(t)
	box := testBox(t)

	h := validHook()
	h.Secret = "my-own-key"
	created, plaintext, err := d.CreateHook(box, h)
	if err != nil {
		t.Fatal(err)
	}
	if plaintext != "my-own-key" {
		t.Fatalf("plaintext = %q, want the supplied secret", plaintext)
	}
	got, _ := d.GetHook(box, created.ID)
	if got.Secret != "my-own-key" {
		t.Fatalf("stored secret = %q, want the supplied one", got.Secret)
	}
}

func TestEnabledHooksReturnsOnlyTheEnabledOnes(t *testing.T) {
	d := testDB(t)
	box := testBox(t)

	on, _, err := d.CreateHook(box, validHook())
	if err != nil {
		t.Fatal(err)
	}
	off := validHook()
	off.Name, off.Enabled = "off", false
	if _, _, err := d.CreateHook(box, off); err != nil {
		t.Fatal(err)
	}

	all, err := d.ListHooks(box)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("ListHooks = %d, want both", len(all))
	}
	live, err := d.EnabledHooks(box)
	if err != nil {
		t.Fatal(err)
	}
	if len(live) != 1 || live[0].ID != on.ID {
		t.Fatalf("EnabledHooks = %+v, want only the enabled one", live)
	}
}

func TestUpdateHookWithNoSecretKeepsTheStoredOne(t *testing.T) {
	// The UI never renders the secret, so an edit form submits an empty one.
	// Overwriting the stored key with "" would silently unsign every future
	// delivery, and the receiver would start rejecting them with no error here.
	d := testDB(t)
	box := testBox(t)

	created, plaintext, err := d.CreateHook(box, validHook())
	if err != nil {
		t.Fatal(err)
	}
	edit := *created
	edit.Name, edit.Secret = "renamed", ""
	if _, err := d.UpdateHook(box, &edit); err != nil {
		t.Fatalf("UpdateHook: %v", err)
	}
	got, _ := d.GetHook(box, created.ID)
	if got.Secret != plaintext {
		t.Fatalf("secret = %q after an edit that did not mention it, want the "+
			"stored one", got.Secret)
	}
	if got.Name != "renamed" {
		t.Errorf("name = %q, want renamed", got.Name)
	}
}

func TestHookValidationRunsOnWrite(t *testing.T) {
	d := testDB(t)
	box := testBox(t)

	bad := validHook()
	bad.URL = "ftp://example.com/x"
	if _, _, err := d.CreateHook(box, bad); err == nil {
		t.Fatal("CreateHook accepted an ftp endpoint")
	}
}
```

Reuse whatever the package's existing `testDB(t)` helper is — `internal/db/db_test.go` already defines one; do not add a second.

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./internal/db/ -run TestHook -v`
Expected: FAIL to build, `d.CreateHook undefined`.

- [ ] **Step 4: Write the implementation**

Create `internal/db/hooks.go`:

```go
package db

import (
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/rainmanjam/polyemesis/internal/hooks"
	"github.com/rainmanjam/polyemesis/internal/secrets"
)

// Hooks are stored as hooks.Hook rather than a row struct of their own, the
// same way alert rules are stored as alerts.Rule: the domain package owns the
// validation and the defaults, and a second copy here would be one more place
// for them to disagree.
//
// The one asymmetry with alert_rules is the secret. A webhook URL is stored in
// the clear because it must be, being the thing the request is sent to and
// something an operator occasionally has to recover. An HMAC key is never
// displayed and never recovered, so it is sealed -- a database file on a backup
// drive should not hand anybody the ability to forge deliveries.

const hookColumns = `id, name, enabled, url, secret, triggers,
	timeout_seconds, max_attempts, created_at, updated_at`

const (
	hooksQuery        = `SELECT ` + hookColumns + ` FROM hooks ORDER BY id`
	hooksEnabledQuery = `SELECT ` + hookColumns + ` FROM hooks WHERE enabled = 1 ORDER BY id`
	hookByIDQuery     = `SELECT ` + hookColumns + ` FROM hooks WHERE id = ?`
)

func scanHook(box *secrets.Box, s interface{ Scan(...any) error }) (*hooks.Hook, error) {
	var (
		h                hooks.Hook
		enabled          int
		sealed           []byte
		triggersJSON     string
		created, updated int64
	)
	if err := s.Scan(&h.ID, &h.Name, &enabled, &h.URL, &sealed, &triggersJSON,
		&h.TimeoutSeconds, &h.MaxAttempts, &created, &updated); err != nil {
		return nil, err
	}
	h.Enabled = enabled != 0
	// A secret that will not open leaves the hook UNSIGNED rather than
	// unreadable. The alternative -- failing the whole read -- would take every
	// other hook down with it, and an unsigned delivery that arrives is more
	// useful to an operator than a signed one that never does. The API reports
	// hasSecret:false, which is how they find out.
	if len(sealed) > 0 {
		if plain, err := box.Open(sealed); err == nil {
			h.Secret = plain
		}
	}
	if triggersJSON != "" && triggersJSON != "[]" {
		var list []hooks.Trigger
		// A subscription list that will not parse subscribes to everything, for
		// the reason spelled out in alerts.go:33 -- the alternative is a hook
		// that has silently stopped firing.
		if err := json.Unmarshal([]byte(triggersJSON), &list); err == nil {
			h.Triggers = list
		}
	}
	h.CreatedAt = time.Unix(created, 0)
	h.UpdatedAt = time.Unix(updated, 0)
	out := h.Normalized()
	return &out, nil
}

func (d *DB) queryHooks(box *secrets.Box, q string) ([]hooks.Hook, error) {
	rows, err := d.sql.Query(q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []hooks.Hook{}
	for rows.Next() {
		h, err := scanHook(box, rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *h)
	}
	return out, rows.Err()
}

// ListHooks returns every hook, oldest first.
func (d *DB) ListHooks(box *secrets.Box) ([]hooks.Hook, error) {
	return d.queryHooks(box, hooksQuery)
}

// EnabledHooks returns the enabled hooks. It is what satisfies hooks.Source, so
// the dispatcher never sees one that is switched off.
func (d *DB) EnabledHooks(box *secrets.Box) ([]hooks.Hook, error) {
	return d.queryHooks(box, hooksEnabledQuery)
}

// GetHook loads one hook.
func (d *DB) GetHook(box *secrets.Box, id int64) (*hooks.Hook, error) {
	h, err := scanHook(box, d.sql.QueryRow(hookByIDQuery, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return h, err
}

// CreateHook stores a new hook and returns the plaintext signing secret.
//
// The plaintext is returned exactly once, here, and never again -- the same
// contract as an API token (see CreateAPIToken and its handler at
// api/token_handlers.go:54). An operator pasting the key into their receiver
// needs it at this moment and at no other.
func (d *DB) CreateHook(box *secrets.Box, h *hooks.Hook) (*hooks.Hook, string, error) {
	norm := h.Normalized()
	if err := norm.Validate(); err != nil {
		return nil, "", err
	}
	// Generated when the operator did not supply one, because an unsigned
	// webhook is one that anybody who learns the URL can forge, and a URL leaks
	// through proxy logs, browser history and screenshots.
	if norm.Secret == "" {
		s, err := hooks.NewSecret()
		if err != nil {
			return nil, "", err
		}
		norm.Secret = s
	}
	sealed, err := box.Seal(norm.Secret)
	if err != nil {
		return nil, "", err
	}
	triggers, err := marshalTriggers(norm.Triggers)
	if err != nil {
		return nil, "", err
	}
	now := time.Now().Unix()
	res, err := d.sql.Exec(`INSERT INTO hooks
		(name, enabled, url, secret, triggers, timeout_seconds, max_attempts, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?)`,
		norm.Name, boolToInt(norm.Enabled), norm.URL, sealed, triggers,
		norm.TimeoutSeconds, norm.MaxAttempts, now, now)
	if err != nil {
		return nil, "", err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, "", err
	}
	out, err := d.GetHook(box, id)
	return out, norm.Secret, err
}

// UpdateHook replaces a hook in place. An empty Secret means "unchanged": the
// UI never renders it, so every edit form submits an empty one, and storing
// that would silently unsign every future delivery.
func (d *DB) UpdateHook(box *secrets.Box, h *hooks.Hook) (*hooks.Hook, error) {
	norm := h.Normalized()
	if err := norm.Validate(); err != nil {
		return nil, err
	}
	if norm.Secret == "" {
		existing, err := d.GetHook(box, norm.ID)
		if err != nil {
			return nil, err
		}
		norm.Secret = existing.Secret
	}
	sealed, err := box.Seal(norm.Secret)
	if err != nil {
		return nil, err
	}
	triggers, err := marshalTriggers(norm.Triggers)
	if err != nil {
		return nil, err
	}
	res, err := d.sql.Exec(`UPDATE hooks SET
		name=?, enabled=?, url=?, secret=?, triggers=?,
		timeout_seconds=?, max_attempts=?, updated_at=? WHERE id=?`,
		norm.Name, boolToInt(norm.Enabled), norm.URL, sealed, triggers,
		norm.TimeoutSeconds, norm.MaxAttempts, time.Now().Unix(), norm.ID)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, ErrNotFound
	}
	return d.GetHook(box, norm.ID)
}

// DeleteHook removes a hook.
func (d *DB) DeleteHook(id int64) error {
	res, err := d.sql.Exec(`DELETE FROM hooks WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// marshalTriggers encodes a subscription list. An empty list is stored as "[]",
// meaning "every trigger", so the column is never NULL.
func marshalTriggers(list []hooks.Trigger) (string, error) {
	if len(list) == 0 {
		return "[]", nil
	}
	b, err := json.Marshal(list)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
```

- [ ] **Step 5: Run the tests**

Run: `go test ./internal/db/ -count=1`
Expected: PASS, the whole package.

- [ ] **Step 6: Mutation-test the "empty secret means unchanged" rule**

In `UpdateHook`, temporarily delete the `if norm.Secret == "" { ... }` block.
Run: `go test ./internal/db/ -run TestUpdateHookWithNoSecretKeepsTheStoredOne -count=1`
Expected: FAIL. Restore, re-run, confirm PASS.

- [ ] **Step 7: Commit**

```bash
gofmt -w internal/db/
go test ./internal/db/ -count=1
git add internal/db/schema.sql internal/db/hooks.go internal/db/hooks_test.go
git commit -m "feat(db): the hooks table, with a sealed signing secret

A new table, so CREATE TABLE IF NOT EXISTS is enough -- the Migrate* helpers in
Open exist only because that statement cannot add a COLUMN to an existing
table.

The secret is sealed where alert_rules.url is not, and the asymmetry is
deliberate: a webhook URL is the thing the request is sent to and an operator
occasionally has to recover it, while an HMAC key is never displayed and never
recovered. A database file on a backup drive should not hand somebody the
ability to forge deliveries.

An empty secret on update means unchanged. The UI never renders it, so every
edit form submits an empty one, and storing that would silently unsign every
future delivery with no error anywhere."
```

---

### Task 6: Wiring — one snapshot, two watchers, one shared dispatcher

**Files:**
- Modify: `internal/engine/engine.go` (struct fields near :175-180; `Start` near :505-514; `alertLoop` at :4568, renamed; a new `observeWanted` helper; `SetHooks`)
- Create: `internal/engine/observe_test.go`
- Modify: `internal/engine/manager.go` (struct near :33; `Sync` near :140; new `SetHooks`)
- Modify: `cmd/polyemesis/main.go` (construct and run the dispatcher; hand it to the manager and the API)

**Interfaces:**
- Consumes: `hooks.NewDispatcher`, `hooks.NewWatcher`, `hooks.SourceRef`, `(*db.DB).EnabledHooks`.
- Produces: `(*Engine).SetHooks(*hooks.Dispatcher)`; `(*Manager).SetHooks(*hooks.Dispatcher)`; `observeWanted(alertRules, hookRules bool) bool`; `Engine.observeLoop` (renamed from `alertLoop`).

- [ ] **Step 1: Write the failing test**

Create `internal/engine/observe_test.go`:

```go
package engine

import "testing"

// The gate on the sweep, extracted so it is testable without an engine.
//
// The failure it guards is specific and silent: alertLoop used to skip the
// whole sweep when no ALERT rules existed. A hook is a second consumer of the
// same snapshot, so an install with hooks configured and no alert rules would
// have built no snapshot, observed no transitions, and fired nothing -- with a
// perfectly healthy hook listed as enabled in the UI.
func TestObserveWanted(t *testing.T) {
	tests := []struct {
		name              string
		alerts, hooks     bool
		want              bool
	}{
		{"neither", false, false, false},
		{"alerts only", true, false, true},
		{"hooks only", false, true, true},
		{"both", true, true, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := observeWanted(tc.alerts, tc.hooks); got != tc.want {
				t.Fatalf("observeWanted(%v, %v) = %v, want %v",
					tc.alerts, tc.hooks, got, tc.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/engine/ -run TestObserveWanted -v`
Expected: FAIL to build, `undefined: observeWanted`.

- [ ] **Step 3: Add the engine fields**

In `internal/engine/engine.go`, beside `alerter`/`alertWatch` (the block at :175-180), add:

```go
	// hooks delivers lifecycle webhooks and hookWatch derives their edges.
	//
	// The dispatcher is SHARED across every engine -- it is handed in by the
	// manager, not built here -- because a sequence number, a delivery log and
	// a retry queue all belong to the endpoint, and an endpoint is subscribed
	// to by the whole install rather than by one programme. The watcher is
	// per engine, because "has this source been publishing" is per source.
	//
	// Both may be nil on an Engine assembled field by field, which is how the
	// tests build one; every use is nil-safe.
	hooks     *hooks.Dispatcher
	hookWatch *hooks.Watcher
```

Add `"github.com/rainmanjam/polyemesis/internal/hooks"` to the import block.

In `New` (engine.go:294), immediately after `e.alertWatch = alerts.NewWatcher(alerts.WatchConfig{})` at :353:

```go
	// Named from the source row on the first reconcile; SourceRef starts with
	// the id alone so an event raised before that still identifies its
	// programme.
	e.hookWatch = hooks.NewWatcher(hooks.SourceRef{ID: sourceID}, hooks.WatchConfig{})
```

- [ ] **Step 4: Add SetHooks**

Append to `internal/engine/engine.go`, beside `Alerts()` (:4524):

```go
// Hooks exposes the dispatcher so the API can report its counters, list recent
// deliveries and send a test. Nil when no dispatcher was wired.
func (e *Engine) Hooks() *hooks.Dispatcher { return e.hooks }

// SetHooks attaches the shared dispatcher.
//
// A setter rather than a New parameter, matching SetTranscriber: engines are
// created whenever a source is added, long after main built the dispatcher, and
// a programme whose hooks silently never fire is a bug nobody reports.
func (e *Engine) SetHooks(d *hooks.Dispatcher) {
	e.mu.Lock()
	e.hooks = d
	e.mu.Unlock()
}
```

- [ ] **Step 5: Rename alertLoop and fold in the hook fan-out**

In `internal/engine/engine.go`, rename `alertLoop` to `observeLoop` (one call site, at :513) and replace its body's guard and tail. The renamed function:

```go
// observeWanted reports whether a sweep is worth building.
//
// Extracted so the gate is testable without an engine. The failure it guards is
// silent: this loop used to skip everything when no ALERT rules existed, and a
// hook is a second consumer of the same snapshot, so an install with hooks and
// no alert rules would have observed nothing at all.
func observeWanted(alertRules, hookRules bool) bool { return alertRules || hookRules }

// observeLoop samples the pipeline and hands each snapshot to both watchers.
//
// One sweep raises everything rather than Publish calls scattered through the
// reconcile, because everything worth reporting is a TRANSITION -- "has been
// down for twenty seconds", "data is arriving now" -- and a transition needs
// somewhere to remember the previous state. Sweeping also guarantees nothing is
// raised while e.mu is held by the thing it is about.
//
// Two watchers over ONE snapshot. They answer different questions -- "is this
// an incident worth waking somebody" against "did this transition happen" --
// and so they cross different edges at different times: a destination failing
// raises a hook at 10s and an alert at 20s. Deriving the second set from a
// second sampler would mean two Status() calls at two cadences that could
// disagree about what they saw.
func (e *Engine) observeLoop(ctx context.Context) {
	if e.alerter == nil || e.alertWatch == nil {
		return
	}
	tick := time.NewTicker(alertSweep)
	defer tick.Stop()

	var (
		lastRx   uint64
		firstRx  = true
		disk     alerts.DiskState
		diskAt   time.Time
		haveDisk bool
	)
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-tick.C:
			// Liveness from bytes on the hub, not from process state: an SRT or
			// RTMP listener sits in "running" for as long as it waits for a
			// publisher, which is a different question from "is the source
			// arriving".
			rx := e.hub.RxBytes()
			live := !firstRx && rx > lastRx
			lastRx, firstRx = rx, false

			// A server with neither an alert rule nor a hook pays for two
			// cached lookups and nothing else -- no status snapshot, no
			// queries, no disk read. Adding the first one starts the timers
			// from that moment, which is the only honest thing it could do.
			if !observeWanted(e.alerter.HasRules(), e.hooks.HasHooks()) {
				haveDisk = false
				continue
			}

			if !haveDisk || now.Sub(diskAt) >= alertDiskEvery {
				disk, haveDisk, diskAt = e.diskState(), true, now
			}
			snap := e.alertSnapshot(now, live)
			snap.Disk = disk

			for _, ev := range e.alertWatch.Observe(snap) {
				e.alerter.Publish(ev)
			}
			if e.hookWatch != nil {
				e.hookWatch.SetSource(hooks.SourceRef{ID: e.sourceID, Name: e.SourceName()})
				for _, ev := range e.hookWatch.Observe(snap) {
					e.hooks.Publish(ev)
				}
			}
		}
	}
}
```

Update the call site at engine.go:513 from `e.alertLoop(e.ctx)` to `e.observeLoop(e.ctx)`.

Note `e.hooks.HasHooks()` and `e.hooks.Publish(...)` are called on a possibly-nil pointer. Both methods begin with `if d == nil` — the same nil-receiver discipline `alerts.Notifier` uses (notify.go:173, :294) and `transcribe.Tools` uses throughout. Do not add a nil check at the call site; that is what makes the two diverge.

- [ ] **Step 6: Add the manager wiring**

In `internal/engine/manager.go`, add to the `Manager` struct beside the transcriber block:

```go
	// hooks is remembered for the same reason the transcriber is: engines are
	// created after Start whenever a source is added, and a programme whose
	// hooks silently never fire is a bug nobody reports.
	hooks *hooks.Dispatcher
```

Add the import, then a setter beside `SetTranscriber` (:407):

```go
// SetHooks attaches the shared lifecycle-hook dispatcher to every engine, now
// and to any created later.
func (m *Manager) SetHooks(d *hooks.Dispatcher) {
	m.mu.Lock()
	m.hooks = d
	engines := make([]*Engine, 0, len(m.engines))
	for _, eng := range m.engines {
		engines = append(engines, eng)
	}
	m.mu.Unlock()
	for _, eng := range engines {
		eng.SetHooks(d)
	}
}
```

And in `Sync`, immediately after the `eng.SetTranscriber(tw, dir, nice)` line (:150):

```go
		if hd := m.hooks; hd != nil {
			eng.SetHooks(hd)
		}
```

Match the surrounding code's locking: read `m.hooks` where `m.tw` is read, under the same lock.

- [ ] **Step 7: Wire main**

In `cmd/polyemesis/main.go`, after `box` is created and after `eng.Start(ctx)` succeeds (around line 200), add:

```go
	// Lifecycle webhooks. One dispatcher for the whole process, handed to every
	// engine: a sequence number and a delivery log belong to the endpoint, and
	// an endpoint is subscribed to by the install rather than by one programme.
	//
	// Started unconditionally and inert until an operator adds a hook. The
	// dispatcher re-reads the table on a ticker, so creating one takes effect
	// without a restart -- the same shape as the alert notifier's rule cache.
	hookd := hooks.NewDispatcher(log, hooks.SourceFunc(func() ([]hooks.Hook, error) {
		return store.EnabledHooks(box)
	}))
	go hookd.Run(ctx)
	eng.SetHooks(hookd)
```

Then add `Hooks: hookd` to the `api.Options` literal further down.

- [ ] **Step 8: Run the tests**

Run: `go build ./... && go test ./internal/engine/ ./internal/hooks/ -count=1`
Expected: build clean, both packages PASS.

- [ ] **Step 9: Mutation-test the gate**

In `observeWanted`, temporarily change the body to `return alertRules`.
Run: `go test ./internal/engine/ -run TestObserveWanted -count=1`
Expected: FAIL on "hooks only". Restore, re-run, confirm PASS.

- [ ] **Step 10: Commit**

```bash
gofmt -w ./cmd ./internal
go build ./... && go test ./internal/engine/ -count=1
git add cmd/polyemesis/main.go internal/engine/
git commit -m "feat(engine): fan the sweep snapshot out to hooks as well as alerts

alertLoop is now observeLoop, because it is no longer only about alerts. One
snapshot every two seconds, two watchers over it. They answer different
questions and cross different edges at different times -- a destination failing
raises a hook at 10s and an alert at 20s -- and deriving the second set from a
second sampler would mean two Status() calls at two cadences that could
disagree about what they saw.

The gate is extracted as observeWanted so it is testable without an engine. The
bug it guards is silent: the loop used to skip everything when no ALERT rules
existed, so an install with hooks and no alert rules would have observed
nothing while the UI showed a healthy enabled hook.

The dispatcher is shared and handed down by the manager, matching
SetTranscriber -- engines are created whenever a source is added, long after
main built it."
```

---

### Task 7: The HTTP surface

**Files:**
- Modify: `internal/api/api.go` (Server struct ~:44-80, Options ~:83-110, New ~:113, routes after :363)
- Create: `internal/api/hooks.go`
- Create: `internal/api/hooks_test.go`

**Interfaces:**
- Consumes: `(*db.DB).ListHooks/GetHook/CreateHook/UpdateHook/DeleteHook`, `(*hooks.Dispatcher).Test/Deliveries/Stats`.
- Produces: `GET|POST /api/v1/hooks`, `GET|PUT|DELETE /api/v1/hooks/{id}`, `POST /api/v1/hooks/{id}/test`, `GET /api/v1/hooks/{id}/deliveries`, `GET /api/v1/hooks/meta`.

- [ ] **Step 1: Write the failing test**

Create `internal/api/hooks_test.go`:

```go
package api

import (
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/alerts"
)

// A hook endpoint holds two secrets where an alert rule holds one: the URL
// (whose path is a capability) and the signing key (which lets anybody who has
// it forge deliveries). Both are easy to regress with an innocent change to a
// response struct, so every route that can return a hook is checked.

const hookURL = "https://ci.example.com/build/XXXXsecretXXXX"

func createHook(t *testing.T, h http.Handler, sign func(*http.Request), body map[string]any) map[string]any {
	t.Helper()
	var out map[string]any
	decodeInto(t, send(t, h, sign, http.MethodPost, "/api/v1/hooks", body, http.StatusCreated), &out)
	return out
}

func TestCreateReturnsThePlaintextSecretExactlyOnce(t *testing.T) {
	h, _, sign := sourceServer(t)

	created := createHook(t, h, sign, map[string]any{"name": "deploy", "url": hookURL})
	secret, _ := created["secret"].(string)
	if secret == "" {
		t.Fatal("no plaintext secret in the create response; the operator has " +
			"nothing to paste into their receiver and no way to get it later")
	}
	id := int64(created["id"].(float64))

	// And never again, on any route.
	for _, p := range []string{
		"/api/v1/hooks",
		"/api/v1/hooks/" + strconv.FormatInt(id, 10),
	} {
		body := string(send(t, h, sign, http.MethodGet, p, nil, http.StatusOK))
		if strings.Contains(body, secret) {
			t.Errorf("%s re-issued the signing secret:\n%s", p, body)
		}
		if strings.Contains(body, "XXXXsecretXXXX") {
			t.Errorf("%s echoed the endpoint path:\n%s", p, body)
		}
		if !strings.Contains(body, alerts.Mask) {
			t.Errorf("%s returned no masked endpoint; the UI has nothing to show:\n%s", p, body)
		}
		if !strings.Contains(body, `"hasSecret":true`) {
			t.Errorf("%s does not say whether the hook is signed:\n%s", p, body)
		}
	}
}

func TestUpdatingAHookWithTheMaskedURLKeepsTheRealOne(t *testing.T) {
	// The same trap alert rules have: every form renders the only URL it was
	// given -- the masked one -- and submits it back untouched. Storing that
	// would point the hook at a URL that has never existed, and firing would
	// stop with no error anywhere.
	h, _, sign := sourceServer(t)

	created := createHook(t, h, sign, map[string]any{"name": "deploy", "url": hookURL})
	id := int64(created["id"].(float64))
	path := "/api/v1/hooks/" + strconv.FormatInt(id, 10)

	var shown map[string]any
	decodeInto(t, send(t, h, sign, http.MethodGet, path, nil, http.StatusOK), &shown)
	masked, _ := shown["url"].(string)
	if !strings.Contains(masked, alerts.Mask) {
		t.Fatalf("url = %q, expected a masked one", masked)
	}
	send(t, h, sign, http.MethodPut, path, map[string]any{
		"name": "renamed", "url": masked,
	}, http.StatusOK)

	// Proven through behaviour rather than by reading the column back: a test
	// delivery goes to the stored URL, and a hook pointed at "[redacted]" would
	// fail to build a request at all.
	var res map[string]any
	raw := send(t, h, sign, http.MethodPost, path+"/test", nil, http.StatusBadGateway)
	decodeInto(t, raw, &res)
	if msg, _ := res["error"].(string); strings.Contains(msg, alerts.Mask) {
		t.Fatalf("the stored URL became the mask: %v", res)
	}
}

func TestTestDeliveryReturnsWhatTheEndpointSaid(t *testing.T) {
	// An operator testing a hook is testing a machine contract. "sent" tells
	// them nothing about whether their signature verification agrees, so the
	// response carries the exact body and signature that were sent.
	h, _, sign := sourceServer(t)
	created := createHook(t, h, sign, map[string]any{"name": "deploy", "url": hookURL})
	id := strconv.FormatInt(int64(created["id"].(float64)), 10)

	// hookURL does not resolve, so this is the unreachable path -- and the
	// response must still say what was attempted rather than only "failed".
	var out map[string]any
	decodeInto(t, send(t, h, sign, http.MethodPost, "/api/v1/hooks/"+id+"/test", nil,
		http.StatusBadGateway), &out)
	if _, ok := out["error"]; !ok {
		t.Fatalf("no error explaining the failure: %v", out)
	}
}

func TestHooksMetaListsEveryTrigger(t *testing.T) {
	h, _, sign := sourceServer(t)
	body := string(send(t, h, sign, http.MethodGet, "/api/v1/hooks/meta", nil, http.StatusOK))
	for _, want := range []string{
		"ingest.published", "ingest.disconnected",
		"destination.up", "destination.down",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("meta is missing %s; the editor cannot offer a trigger it "+
				"is not told about:\n%s", want, body)
		}
	}
	if strings.Contains(body, `"test"`) {
		t.Errorf("meta offers the test trigger as subscribable:\n%s", body)
	}
}

func TestDeliveriesRouteExists(t *testing.T) {
	h, _, sign := sourceServer(t)
	created := createHook(t, h, sign, map[string]any{"name": "deploy", "url": hookURL})
	id := strconv.FormatInt(int64(created["id"].(float64)), 10)

	send(t, h, sign, http.MethodGet, "/api/v1/hooks/"+id+"/deliveries", nil, http.StatusOK)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/api/ -run 'TestCreateReturnsThePlaintext|TestUpdatingAHook|TestTestDelivery|TestHooksMeta|TestDeliveriesRoute' -v`
Expected: FAIL — every route 404s.

- [ ] **Step 3: Add the Server field and Option**

In `internal/api/api.go`, add to `Server`:

```go
	// hooks is the shared lifecycle-webhook dispatcher. Optional like
	// everything else in this block: a build with none wired still serves the
	// hooks page, listing stored hooks and reporting that nothing is delivering
	// -- which is the difference an operator needs to see.
	hooks *hooks.Dispatcher
```

To `Options`:

```go
	// Hooks is the lifecycle-webhook dispatcher. Optional.
	Hooks *hooks.Dispatcher
```

And to the `New` literal: `hooks: o.Hooks,`.

- [ ] **Step 4: Write the handlers**

Create `internal/api/hooks.go`:

```go
package api

import (
	"net/http"
	"strings"

	"github.com/rainmanjam/polyemesis/internal/alerts"
	"github.com/rainmanjam/polyemesis/internal/hooks"
)

// The HTTP surface for lifecycle webhooks.
//
// Shaped after the alert-rule handlers next door, deliberately: the two are
// different subsystems but they present the same operator problem -- an
// endpoint URL whose path is a credential -- and a hook that behaved
// differently about it would be a second thing to get right.

// hookRequest is a PATCH-shaped body: every field is a pointer, and an omitted
// one leaves the stored value alone.
//
// It exists instead of decoding straight into hooks.Hook because of the two
// secrets. A Hook marshals a MASKED url and no secret at all, so the only way a
// client can change either is a field the API defines itself.
type hookRequest struct {
	Name           *string           `json:"name"`
	Enabled        *bool             `json:"enabled"`
	URL            *string           `json:"url"`
	Secret         *string           `json:"secret"`
	Triggers       *[]hooks.Trigger  `json:"triggers"`
	TimeoutSeconds *int              `json:"timeoutSeconds"`
	MaxAttempts    *int              `json:"maxAttempts"`
}

func (q hookRequest) applyTo(h hooks.Hook) hooks.Hook {
	if q.Name != nil {
		h.Name = *q.Name
	}
	if q.Enabled != nil {
		h.Enabled = *q.Enabled
	}
	if q.URL != nil {
		// The client was shown "https://host/[redacted]" and every form hands
		// it back untouched, because the field it renders is the only URL it
		// has. Storing that would point the hook at a URL that has never
		// existed and stop it firing silently, so a value still carrying the
		// mask means "unchanged".
		if u := strings.TrimSpace(*q.URL); !strings.Contains(u, alerts.Mask) {
			h.URL = u
		}
	}
	if q.Secret != nil {
		// Empty means unchanged, handled in db.UpdateHook: the UI never renders
		// the secret, so every edit form submits an empty one.
		h.Secret = strings.TrimSpace(*q.Secret)
	}
	if q.Triggers != nil {
		h.Triggers = append([]hooks.Trigger(nil), *q.Triggers...)
	}
	if q.TimeoutSeconds != nil {
		h.TimeoutSeconds = *q.TimeoutSeconds
	}
	if q.MaxAttempts != nil {
		h.MaxAttempts = *q.MaxAttempts
	}
	return h
}

func (s *Server) handleListHooks(w http.ResponseWriter, r *http.Request) {
	rows, err := s.store.ListHooks(s.box)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rows)
}

func (s *Server) handleGetHook(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	h, err := s.store.GetHook(s.box, id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, h)
}

// handleCreateHook stores a hook and returns its signing secret ONCE.
//
// The plaintext appears in exactly this response and nowhere else, matching the
// API-token handler (token_handlers.go:54). An operator pasting the key into
// their receiver needs it at this moment and never again, and a key that can be
// read back from a list endpoint is a key that leaks through every screenshot.
func (s *Server) handleCreateHook(w http.ResponseWriter, r *http.Request) {
	var req hookRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	// A new hook with no explicit enabled flag is on: somebody who just typed a
	// URL in wants it to fire.
	h := req.applyTo(hooks.Hook{Enabled: true}).Normalized()
	if err := h.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	out, plaintext, err := s.store.CreateHook(s.box, &h)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	// Encoded through the Hook's own MarshalJSON for the masked url and
	// hasSecret, with the plaintext added beside it rather than inside it, so
	// no future encoding of a Hook can accidentally carry it.
	writeJSON(w, http.StatusCreated, map[string]any{
		"id": out.ID, "hook": out,
		"secret": plaintext,
		"secretNote": "This signing key is shown once. Store it in your " +
			"receiver now; polyemesis cannot show it again.",
	})
}

func (s *Server) handleUpdateHook(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	existing, err := s.store.GetHook(s.box, id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	var req hookRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	// The stored secret is not carried onto the request value: db.UpdateHook
	// re-reads it when the request left it empty. Copying it here would mean
	// two places that both have to remember, and one of them eventually will
	// not.
	updated := req.applyTo(*existing)
	updated.Secret = ""
	if req.Secret != nil {
		updated.Secret = strings.TrimSpace(*req.Secret)
	}
	updated = updated.Normalized()
	if err := updated.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	out, err := s.store.UpdateHook(s.box, &updated)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleDeleteHook(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	if err := s.store.DeleteHook(id); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// handleTestHook sends one fully-formed delivery to the stored endpoint, right
// now, and reports what came back.
//
// This is the whole answer to "how does an operator test a hook without going
// live". It reads the hook from the store rather than from the body, so the URL
// under test is the URL that will really be used, and it returns the exact body
// and signature that were sent so the operator can check their verification
// code against real bytes rather than against the documentation.
func (s *Server) handleTestHook(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	h, err := s.store.GetHook(s.box, id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if s.hooks == nil {
		writeError(w, http.StatusServiceUnavailable, "the hook dispatcher is not running")
		return
	}
	trigger := hooks.Trigger(r.URL.Query().Get("trigger"))
	res, err := s.hooks.Test(r.Context(), *h, trigger)
	if err != nil {
		// 502, not 500: the failure is the operator's endpoint, and the message
		// says which. Errors out of the dispatcher are already redacted.
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// handleHookDeliveries returns one hook's recent attempts, so "did it fire, and
// what did my endpoint say" is answerable from the console.
func (s *Server) handleHookDeliveries(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	if s.hooks == nil {
		writeJSON(w, http.StatusOK, []any{})
		return
	}
	writeJSON(w, http.StatusOK, s.hooks.Deliveries(id))
}

// handleHooksMeta is the catalogue the hook editor builds its pickers from, so
// a new trigger is added in exactly one place.
func (s *Server) handleHooksMeta(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"triggers":    hooks.AllTriggers(),
		"specVersion": hooks.SpecVersion,
		"headers": map[string]string{
			"signature": hooks.SignatureHeader,
			"timestamp": hooks.TimestampHeader,
			"trigger":   hooks.TriggerHeader,
			"delivery":  hooks.DeliveryHeader,
			"sequence":  hooks.SequenceHeader,
		},
		"bounds": map[string]int{
			"minTimeoutSeconds": hooks.MinTimeoutSeconds,
			"maxTimeoutSeconds": hooks.MaxTimeoutSeconds,
			"minAttempts":       hooks.MinAttempts,
			"maxAttempts":       hooks.MaxAttempts,
			"maxNameLen":        hooks.MaxHookNameLen,
			"maxUrlLen":         hooks.MaxURLLen,
		},
		"stats": s.hooks.Stats(),
	})
}
```

- [ ] **Step 5: Register the routes**

In `internal/api/api.go`, immediately after the alert-rule block (line 363):

```go
			// Lifecycle webhooks. Same authenticated group as the alert rules,
			// and /meta before /{id} so chi cannot read "meta" as an id.
			r.Get("/hooks/meta", s.handleHooksMeta)
			r.Get("/hooks", s.handleListHooks)
			r.Post("/hooks", s.handleCreateHook)
			r.Get("/hooks/{id}", s.handleGetHook)
			r.Put("/hooks/{id}", s.handleUpdateHook)
			r.Delete("/hooks/{id}", s.handleDeleteHook)
			// POST despite reading nothing: it makes an outbound call to a
			// third party, so it is neither safe nor idempotent, and POST puts
			// it behind requireCSRF with the rest of the state-changing group.
			r.Post("/hooks/{id}/test", s.handleTestHook)
			r.Get("/hooks/{id}/deliveries", s.handleHookDeliveries)
```

Verify `/alerts/meta` sits before `/alerts/rules/{id}` in the existing block — it does (:357 before :360) — so this ordering matches the neighbour.

- [ ] **Step 6: Run the tests**

Run: `go test ./internal/api/ -count=1`
Expected: PASS, the whole package.

If `sourceServer` does not construct the API with a dispatcher, add one to its `api.Options` literal in `internal/api/sources_test.go` using an in-memory `hooks.SourceFunc` backed by the test store, and start it with a `t.Cleanup`-cancelled context. Do not invent a second fixture shape.

- [ ] **Step 7: Mutation-test the secret-echo guard**

In `handleListHooks`, temporarily wrap the rows so `secret` is included in the response.
Run: `go test ./internal/api/ -run TestCreateReturnsThePlaintextSecretExactlyOnce -count=1`
Expected: FAIL naming the route. Restore, re-run, confirm PASS.

- [ ] **Step 8: Commit**

```bash
gofmt -w internal/api/
go test ./internal/api/ -count=1
git add internal/api/api.go internal/api/hooks.go internal/api/hooks_test.go
git commit -m "feat(api): CRUD, test delivery and a delivery log for lifecycle hooks

Shaped after the alert-rule handlers next door on purpose: different
subsystems, same operator problem -- an endpoint URL whose path is a credential
-- and a hook that behaved differently about it would be a second thing to get
right.

The signing key is returned exactly once, at create, matching the API-token
handler. A key readable from a list endpoint is a key that leaks through every
screenshot.

The test route returns the exact body and signature that were sent, not a
verdict. An operator testing a hook is testing a machine contract: 'sent' tells
them nothing about whether their verification code agrees."
```

---

### Task 8: The UI

**Files:**
- Modify: `ui/src/lib/types.ts`
- Modify: `ui/src/lib/api.ts`
- Modify: `ui/src/pages/AutomationPage.tsx`

**Interfaces:**
- Consumes: every route from Task 7.
- Produces: `Hook`, `HookMeta`, `HookDelivery`, `HookTestResult` TypeScript types; `api.hooks.*`.

- [ ] **Step 1: Add the types**

In `ui/src/lib/types.ts`, beside the alert-rule types:

```ts
export type HookTrigger =
  | "ingest.published"
  | "ingest.disconnected"
  | "destination.up"
  | "destination.down";

/** A stored lifecycle webhook. `url` is always masked and `secret` is never
 *  present: the plaintext key is returned once, by the create call, and cannot
 *  be read back. */
export interface Hook {
  id: number;
  name: string;
  enabled: boolean;
  url: string;
  hasSecret: boolean;
  triggers: HookTrigger[];
  timeoutSeconds: number;
  maxAttempts: number;
  createdAt: string;
  updatedAt: string;
}

export interface HookMeta {
  triggers: HookTrigger[];
  specVersion: string;
  headers: Record<string, string>;
  bounds: Record<string, number>;
  stats: {
    queued: number;
    dropped: number;
    sent: number;
    failed: number;
    retries: number;
    endpoints: number;
    lastSent?: string;
    lastError?: string;
  };
}

export interface HookDelivery {
  hookId: number;
  trigger: HookTrigger | "test";
  sequence: number;
  id: string;
  at: string;
  attempts: number;
  status?: number;
  durationMs: number;
  error?: string;
  response?: string;
}

/** What the test button gets back. It carries the exact body and signature that
 *  were sent, because the operator is checking their own verification code
 *  against real bytes rather than against the documentation. */
export interface HookTestResult {
  status: number;
  durationMs: number;
  response?: string;
  body: string;
  signature: string;
}

export interface HookCreated {
  id: number;
  hook: Hook;
  secret: string;
  secretNote: string;
}
```

- [ ] **Step 2: Add the API calls**

In `ui/src/lib/api.ts`, beside the alert-rule calls, matching the surrounding helper style (`get`/`post`/`put`/`del` — check the neighbours rather than assuming):

```ts
  hooks: {
    meta: () => get<HookMeta>("/hooks/meta"),
    list: () => get<Hook[]>("/hooks"),
    create: (body: Partial<Hook> & { url: string; secret?: string }) =>
      post<HookCreated>("/hooks", body),
    update: (id: number, body: Partial<Hook> & { secret?: string }) =>
      put<Hook>(`/hooks/${id}`, body),
    remove: (id: number) => del<{ status: string }>(`/hooks/${id}`),
    test: (id: number, trigger?: HookTrigger) =>
      post<HookTestResult>(
        `/hooks/${id}/test${trigger ? `?trigger=${trigger}` : ""}`,
        {},
      ),
    deliveries: (id: number) => get<HookDelivery[]>(`/hooks/${id}/deliveries`),
  },
```

- [ ] **Step 3: Verify types compile**

Run: `cd ui && npx tsc -b --noEmit`
Expected: clean.

- [ ] **Step 4: Add the card**

In `ui/src/pages/AutomationPage.tsx`, below the alert-rules card, add a `HooksCard` section. Model it on the alert-rules card in the same file — same `Card`, `Badge`, `Button` imports and the same list/edit shape — and add the three things the alert card does not need:

```tsx
{/* Shown once, and only once: the server cannot re-issue it. Rendered as a
    copyable block rather than a toast, because a toast that disappears after
    four seconds is how an operator loses a key they now have to regenerate. */}
{newSecret && (
  <div className="rounded border border-warn/40 bg-warn/10 px-3 py-2 text-xs">
    <p className="mb-1 font-medium text-warn">
      Signing key — copy it now, it cannot be shown again
    </p>
    <code className="block break-all font-mono text-[11px]">{newSecret}</code>
  </div>
)}
```

```tsx
{/* The test result, in full. The operator is verifying a machine contract, so
    the exact bytes and the exact signature are the answer. */}
{testResult && (
  <div className="space-y-1 rounded border border-border bg-muted/40 px-3 py-2 text-[11px]">
    <div className="flex items-center gap-2">
      <Badge variant={testResult.status >= 200 && testResult.status < 300 ? "live" : "destructive"}>
        HTTP {testResult.status || "no response"}
      </Badge>
      <span className="text-muted-foreground">{testResult.durationMs} ms</span>
    </div>
    <pre className="overflow-x-auto whitespace-pre-wrap font-mono">{testResult.body}</pre>
    <p className="break-all font-mono text-muted-foreground">
      {meta?.headers.signature}: {testResult.signature}
    </p>
  </div>
)}
```

```tsx
{/* Recent deliveries. A hook that fires into a black hole is indistinguishable
    from one that does not fire at all, and this is the difference. */}
<ul className="space-y-1 text-[11px]">
  {deliveries.map((d) => (
    <li key={d.id} className="flex items-center gap-2">
      <Badge variant={d.error ? "destructive" : "outline"}>{d.trigger}</Badge>
      <span className="text-muted-foreground">#{d.sequence}</span>
      <span className="text-muted-foreground">{d.status || "—"}</span>
      <span className="text-muted-foreground">{d.durationMs} ms</span>
      {d.error && <span className="truncate text-destructive">{d.error}</span>}
    </li>
  ))}
</ul>
```

Use whatever `Badge` variants the neighbouring alert card uses — the goal is that a hook looks like every other automation in the product, not like a new feature bolted on.

- [ ] **Step 5: Run the UI gates**

Run: `cd ui && npx tsc -b --noEmit && npm run lint && npm run build`
Expected: all three clean.

- [ ] **Step 6: Commit**

```bash
git add ui/src/lib/types.ts ui/src/lib/api.ts ui/src/pages/AutomationPage.tsx
git commit -m "feat(ui): the lifecycle hooks card

The signing key renders as a copyable block rather than a toast. A toast that
disappears after four seconds is how an operator loses a key they now have to
regenerate.

The test result shows the exact body and the exact signature that were sent.
The operator is verifying a machine contract, and a green tick tells them
nothing about whether their verification code agrees.

Recent deliveries are listed because a hook firing into a black hole is
indistinguishable from one that never fires."
```

---

### Task 9: Documentation and full verification

**Files:**
- Create: `docs/HOOKS.md`
- Modify: `docs/README.md` (add it to the index alongside MONITORING.md and MQTT.md)

- [ ] **Step 1: Write the operator documentation**

Create `docs/HOOKS.md`, covering, with real examples and no hand-waving:

1. **What fires, and when** — the four triggers, the dwell on each edge, and the table from this plan comparing hooks with alerts.
2. **The envelope** — a complete `specVersion: "1"` example for each trigger, copy-pasteable.
3. **Verifying a signature** — a working receiver in ~15 lines of Node and ~15 of Python, including the timestamp tolerance check. State that verification is `hmac(secret, "<timestamp>.<raw body>")` and that the raw body must be used, not a re-serialised object.
4. **Ordering, retries and gaps** — per-endpoint ordering, three attempts, and what `missed` and a `sequence` reset mean.
5. **What is deliberately NOT in a payload** — no destination URL, no stream key, no token, no ingest passphrase. Point at `internal/hooks/payload_test.go`.
6. **Testing without going live** — the test button, the `?trigger=` parameter, and the deliveries list.
7. **Known limitations**, verbatim from the section below.

- [ ] **Step 2: Run every gate CI runs, in CI's order**

```bash
gofmt -l ./cmd ./internal | tee /tmp/fmt && test ! -s /tmp/fmt
go build ./...
go vet ./...
go test -race ./...
cd ui && npx tsc -b --noEmit && npm run lint && npm run build
```

Expected: gofmt prints nothing; build and vet silent; every package `ok`; all three UI gates clean.

- [ ] **Step 3: Prove the copy promise is untouched**

```bash
git diff main --stat -- internal/ffmpeg internal/playout internal/relay internal/supervisor
grep -rn "c:v\|libx264\|nvenc\|-vf \|filter_complex" internal/hooks/ internal/api/hooks.go internal/db/hooks.go
```

Expected: **no diff** in the four packages that build or supervise argv, and **no hits** for any encoder flag in the new files. This feature spawns no process, alters no argv, and touches no destination path. Video is still copied.

- [ ] **Step 4: Prove no secret reaches a payload**

```bash
go test ./internal/hooks/ -run 'TestNoStreamKey|TestNoEnvelopeField|TestHookNeverMarshals' -count=1 -v
go test ./internal/api/ -run TestCreateReturnsThePlaintextSecretExactlyOnce -count=1 -v
grep -rn "Secret" internal/api/hooks.go
```

Expected: all tests PASS; `Secret` appears in `internal/api/hooks.go` only in `hookRequest`, in `applyTo`, in the `updated.Secret` handling, and in the single `"secret": plaintext` of `handleCreateHook`.

- [ ] **Step 5: Confirm the trigger set is complete in all four places**

```bash
grep -rn "ingest.published" internal/hooks/hooks.go internal/api/hooks.go docs/HOOKS.md ui/src/lib/types.ts
```

Expected: one hit in each. A trigger that exists in Go but not in `types.ts` cannot be subscribed to from the UI, and one missing from `docs/HOOKS.md` is one nobody knows about.

- [ ] **Step 6: Commit**

```bash
git add docs/HOOKS.md docs/README.md
git commit -m "docs: lifecycle hooks

Includes a working signature verifier in Node and Python, because a signing
scheme documented in prose is one every receiver implements slightly
differently and then disables."
```

---

## What is NOT covered, and what could go wrong

Written out rather than discovered later.

**A restarted server replays the current state as fresh events.** Both the ingest and every destination start in the OFF position, so a process restarted mid-broadcast fires `ingest.published` and a `destination.up` per live destination within two seconds. This is deliberate — the alternative is a script that never runs because the transition happened while the server was down — but it means **every receiver must be idempotent**. The `sequence` field resetting to 1 is the signal, and `docs/HOOKS.md` says so; nothing enforces it.

**Adding the first hook mid-broadcast does the same.** The sweep does not run when nothing is subscribed, so the watcher's first observation is whatever is true at that moment. Same choice `alerts` made ("Adding the first rule starts the timers from that moment, which is the only honest thing it could do"), same consequence.

**Whether that happens depends on your alert rules.** If alert rules already exist the sweep has been running, so the hook watcher is warm and adding a hook fires nothing. If they do not, it is cold and adding a hook fires the current state. That inconsistency is real, it is not fixed here, and it is documented rather than papered over.

**A one-sample handshake blip produces a publish/disconnect pair.** `ingest.published` has zero dwell by design. An SRT connection that delivers a few bytes and dies will announce a stream that never happened, followed five seconds later by a disconnection. Adding a dwell would delay every real go-live to protect against a rare one; the trade is stated, not hidden.

**Dropped deliveries are counted, not recovered.** A queue that overflows loses those events for good; the `missed` counter on the next successful envelope tells the receiver to go and reconcile, but nothing reconciles for it. There is no persistent outbox, no at-least-once guarantee, and no delivery ordering across a restart.

**Retries block their own endpoint.** Ordering is bought with head-of-line blocking, bounded at `MaxAttempts × (TimeoutSeconds + backoff)` — 33 s at defaults, 165 s if an operator sets the maximums. A slow endpoint delays only its own deliveries, but it delays all of them.

**No SSRF protection.** A hook URL is operator-supplied and may point at `127.0.0.1`, a cloud metadata service or an internal admin panel, and polyemesis will POST to it with a signature header. This is exactly the position `alerts.Rule` is already in, so it is not a new exposure, but it is not a defended one either. Anyone who can create a hook can already reconfigure the streaming pipeline.

**No per-source subscription filter.** A hook receives from every source. An install with three programmes gets three `ingest.published` events, distinguishable only by `source.id` in the payload. Filtering belongs in a later revision; the payload already carries what it would filter on.

**No `destination.enabled`/`disabled` as separate triggers.** A deliberate disable arrives as `destination.down` with `reason: "disabled"`. A script that cares about the distinction reads the reason string, which is free text and therefore weaker than a trigger name.

**The delivery log is in memory and per process.** Fifty entries per hook, gone on restart. It is a debugging aid, not an audit trail, and it is not persisted because a table of every delivery on a busy install grows without an operator ever choosing it.

**The engine wiring is only shallowly tested.** `observeWanted` has a unit test and a stated mutation, but "the snapshot actually reaches `hookWatch`" is proved by nothing except the build. An end-to-end engine test would need a live relay hub and a real FFmpeg, which this repository does not do for the alert path either. If a step in Task 6 is skipped, the tests still pass.

**`hooks` importing `alerts` is a real coupling.** `hooks.Watcher.Observe` takes an `alerts.Snapshot`, and `payload.go` uses `alerts.Redact`. If `alerts.Snapshot` gains a field carrying a URL, the hook payload inherits the risk. The structural guard in `payload_test.go` catches a field *named* after a secret; it does not catch one merely *containing* one.
