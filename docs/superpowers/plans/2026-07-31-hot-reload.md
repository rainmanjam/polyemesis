# Hot Configuration Reload Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Apply a settings change to a running engine without restarting a pipeline that did not need to change, and make it structurally impossible to add a setting that is stored, reported as saved, and silently ignored.

**Architecture:** Split every configurable value into two disjoint sets by one mechanical test — *does it reach an FFmpeg argv?* Values that do keep the existing signature-diff-and-respawn path, unchanged. Values that do not gain a live-apply path: `supervisor.Process` grows a mutable `Policy` so a destination's reconnect curve can be retuned without dropping its connection, and the metering throttle reads the current interval instead of the one captured when it spawned. A declarative classification table in `internal/engine/reload.go` records which class every `db.Settings` leaf is in, guarded by a reflection walk so a new field cannot land unclassified. Every reconcile records what it restarted and what it applied live, and `PUT /api/v1/settings` returns that record.

**Tech Stack:** Go 1.26 (no new dependencies). No UI work — the report is returned on the existing settings response and rendered later.

## Global Constraints

- **Nothing here re-encodes video.** No task adds, removes or alters an FFmpeg argument. The live-apply set is *defined* as the set of values that never reach an argv, so the `-c:v copy` guarantee on every destination and every playout variant is untouched by construction. The one existing re-encode (the admin preview, `engine.go:1171`) is not modified.
- **A live-applied change never reaches FFmpeg**, so "FFmpeg rejected it" is not a reachable failure mode for the live set. That is an invariant, not a hope: Task 2 Step 7 adds a reflection guard over `ffmpeg.DestSpec` that fails if a resilience field is ever added to the argv builder, and Task 4's classification table makes the rule reviewable.
- **A change that silently does nothing is worse than one that restarts.** Where the two conflict — raising a give-up threshold on a destination that has already given up — the plan restarts. That decision is stated at the call site and tested.
- **No signature loses a term without a replacement mechanism.** Removing `Resilience` from `destSpec` is only safe because Task 1 lands `SetPolicy` first. Tasks are ordered accordingly and must not be reordered.
- **Every guard must be mutation-tested.** Each task ends with a step that breaks the implementation, watches the named test fail, and restores it.
- CI gates, in the order CI runs them (`.github/workflows/ci.yml:73`–`:87`): `gofmt -l ./cmd ./internal` must print nothing; `go build ./...`; `go vet ./...`; `go test -race -timeout 15m ./...`.
- British spelling. Comments explain *why*, and name the failure that motivated the decision.

## The two lists

These are the answer to "which settings can be applied live and which genuinely require a restart", derived by reading every signature function and every settings reader. Task 4 encodes them as executable data.

### Live-applicable — no child process is replaced

| Field | Mechanism | Status today |
|---|---|---|
| `destinations[].resilience.{minBackoffSeconds,maxBackoffSeconds,giveUpAfter}` | supervisor policy; never in an argv | **Restarts a live destination.** Fixed in Task 2 |
| `meters.intervalMs` | a throttle in the Go stdout parser | **Stored and silently ignored.** Fixed in Task 3 |
| `destinations.staggerMs` | read per sweep, `engine.go:1588` | already live |
| `preview.idleTimeoutSeconds` | read by `sweepPreview`, deliberately absent from `previewSig` (`engine.go:1229`) | already live |
| `logging.{persistProcessLogs,maxFileMb,maxFiles}` | `applyLogging` swaps the `FileSink` behind `logSink`, so children already running start filling the new file (`engine.go:667`) | already live |
| `recording.{maxAgeHours,maxGb,minFreeGb}` | `recman.Run(ctx, settings func() db.RecordingSettings)` polls (`recording.go:85`) | already live |
| `failover.{graceSeconds,return,returnStableSeconds}`, `failover.backup.enabled`, `failover.slate.enabled` *as choice inputs* | `sweepSelector` re-reads `e.Settings()` every 500 ms (`engine.go:2889`) | already live |
| `playout.{maxDiskMb,sessionIdleSeconds,maxSessions,public,protection,allowCrossOrigin}` | stored on `playout.Manager` at Reconcile; read by the sweeper and the request handler (`playout.go:242`, `:485`) | already live |
| `chat.*` | `ApplyChatRetention` out of band (`handlers.go:746`) | already live |
| `automod.*` | `ApplyAutomod` rebuilds and recompiles out of band (`handlers.go:751`) | already live |
| `mqtt.*` | the runner polls and notices the password by hash (`handlers.go:684`) | already live |
| `postProd.*` | the jobs governor reads settings per admission | already live |
| destination `name`/`position`, rendition `name`/`note`, `playout.variants[].bitrate` | deliberately absent from every signature | already live |

### Genuinely requires a respawn — the value is baked into an argv or a socket bind

- `ingest.mode`; `ingest.srt.{passphrase,latencyMs}`; `ingest.rtmp.{app,streamKey}`; `ingest.pull.{url,reconnectDelayMaxSeconds,rtspTransport}` — the ingest child's argv (`reconcileIngest` sig, `engine.go:888`). Switching *to* SRT kills the child entirely; there is no ingest process in SRT mode.
- `listeners.srtPort` — a bound socket owned by the Manager. Rebound (stop + `srtserver.New` + `Start`), never adjusted (`manager.go:208`).
- `listeners.rtmpPort` — the ingest argv.
- `ingest.annotations` — recompiles every routing graph, so it changes `compiled.FilterComplex` and therefore `destSpec`.
- `recording.{segmentSeconds,stems,stemCodec}` — recorder argv, plus the probed stem plan (`engine.go:984`).
- `preview.{segmentSeconds,videoHeight,videoKbps}` — preview argv, and only when someone is actually watching.
- `meters.enabled` — start/stop, and the metering filtergraph is built from the probed channel layout.
- `playout.{segmentSeconds,playlistSegments,dvrWindowSeconds,audioKbps,format}` and `playout.variants[].{name,enabled,audioTrack,renditionId}` — muxer argv (`variantSig`, `playout.go:333`).
- `synth.silenceOnVideoOnly` — starts or stops the silence tier, which moves `silenceSig` and therefore restarts every passthrough consumer.
- `failover.enabled` (the tier itself), `failover.backup.*` ingest spec, `failover.slate.*` argv.
- Every field in `renditionSig` (`engine.go:1971`) — geometry, fps, bitrate, encoder, preset, GOP, deinterlace, overlays, text.
- Every field in `destSpec` (`engine.go:1679`) other than the three resilience ones — target URL, kind, filter graph, audio bitrate, sample rate, upstream, video delay, expert args, transport tuning, audio codec.

**Why the argv half cannot be hot-reloaded, and is not attempted here.** FFmpeg 8.1.2 (the pinned build) offers exactly two in-flight reconfiguration channels: the `zmq`/`azmq` filters and `sendcmd`/`asendcmd`. Both address *filters that were already instantiated with a command interface*, and neither can change a muxer, an encoder, an output URL, a stream mapping or a pixel format. Using them would mean pre-instrumenting every filter graph against the possibility of a future edit, accepting that only a subset of edits could be delivered, and — the decisive objection — accepting a state where FFmpeg silently declined the command and the process is running configuration nobody can read back. That is precisely the "stored and reported as applied, but not in effect" failure this plan exists to eliminate. Declined.

## What happens to a destination mid-reconnect

Named explicitly because it is the state most likely to be got wrong, and because Task 1 changes the code path.

A destination in `StateReconnecting` has **no child process**: `runOnce` has returned, `p.cmd` is nil, and the supervisor is asleep in the backoff `select` (`supervisor.go:348`). Consequences, all of which the tests in Task 1 pin:

- **Teardown is fast, not slow.** `teardownDest` → `proc.Stop(ctx)` cancels the context (breaking the sleep) and calls `terminate()`, which returns immediately on `cmd == nil` (`supervisor.go:481`). The 12 s `stopTimeout` and the 8 s `shutdownGrace` are never paid. A reconcile that removes eight reconnecting destinations does not take 96 seconds.
- **It still owns a port and a subscription.** `destination.port` and `destination.subName` are held from `startDest` until teardown regardless of process state, so `alloc.Release` and `hub.Unsubscribe` in `teardownDest` are correct for a reconnecting destination and nothing leaks.
- **Retuning shortens the wait it is already in, and never lengthens it.** `SetPolicy` pokes a `retune` channel; `waitBackoff` re-clamps its deadline down to the new `MaxBackoff` and leaves it alone otherwise. An operator who lowers the ceiling on a crawling destination expects it back sooner; one who raises it does not expect the destination they are already waiting on to wait longer still.
- **Lowering `giveUpAfter` is not retroactive.** A destination is not marked failed for exits it made under the old rules; the new limit is consulted at the next exit.
- **Raising `giveUpAfter` on a destination that already gave up *does* restart it.** Anything else would be the silent no-op. This is the one place the plan deliberately chooses a restart over a live apply, and the restart is reported.
- **A latent hazard this work surfaces:** when `supervise` returns down the give-up path it leaves `p.running == true` (`supervisor.go:334`), so a plain `Start()` on a failed process is a no-op. Only `Restart()` revives it — `Stop` returns immediately because `done` is already closed. Task 1 Step 8 pins that with a test rather than leaving it to be rediscovered.

---

### Task 1: A restart policy that can change while the process is running

**Files:**
- Modify: `internal/supervisor/supervisor.go`
- Create: `internal/supervisor/policy_test.go`

**Interfaces:**
- Consumes: `Spec.{MinBackoff,MaxBackoff,MaxRestarts}` as the *initial* policy only.
- Produces: `type Policy struct{ MinBackoff, MaxBackoff time.Duration; MaxRestarts int }`; `(*Process).Policy() Policy`; `(*Process).SetPolicy(Policy)`; `(*Process).waitBackoff(ctx, time.Duration) bool`.

- [ ] **Step 1: Write the failing tests**

Create `internal/supervisor/policy_test.go`:

```go
package supervisor

// The live restart policy.
//
// These three values are the only part of a Spec that never reaches the child's
// argv -- they govern what the SUPERVISOR does after it exits -- so they are the
// only part that can honestly change without replacing the process. Everything
// here is about proving that a change lands on a process that is already
// running, and that it lands without granting anything a fresh set of lives.

import (
	"context"
	"testing"
	"time"
)

// A destination crawling on a 30s ceiling must come back sooner when the
// operator lowers the ceiling, not after the wait it was already in.
func TestSetPolicyShortensAnInFlightBackoff(t *testing.T) {
	p := testProcess(t, fakeExit(1), Spec{
		AutoRestart: true,
		MinBackoff:  30 * time.Second,
		MaxBackoff:  30 * time.Second,
	})
	p.Start()
	waitFor(t, "the first backoff to begin", func() bool {
		return p.Status().State == StateReconnecting
	})

	start := time.Now()
	p.SetPolicy(Policy{MinBackoff: 10 * time.Millisecond, MaxBackoff: 10 * time.Millisecond})

	waitFor(t, "a respawn under the new ceiling", func() bool { return p.Status().Restarts >= 2 })
	if took := time.Since(start); took > 10*time.Second {
		t.Fatalf("respawn took %s; the retune did not shorten the wait already in flight", took)
	}
}

// The opposite direction. Raising the ceiling is a statement about FUTURE
// waits: an operator who raises it does not expect the destination they are
// currently staring at to go quiet for longer than it already promised.
func TestSetPolicyNeverLengthensAnInFlightBackoff(t *testing.T) {
	p := testProcess(t, fakeExit(1), Spec{
		AutoRestart: true,
		MinBackoff:  50 * time.Millisecond,
		MaxBackoff:  50 * time.Millisecond,
	})
	p.Start()
	waitFor(t, "the first backoff to begin", func() bool {
		return p.Status().State == StateReconnecting
	})

	start := time.Now()
	p.SetPolicy(Policy{MinBackoff: 30 * time.Second, MaxBackoff: 30 * time.Second})

	waitFor(t, "the in-flight wait to finish on its original deadline", func() bool {
		return p.Status().Restarts >= 2
	})
	if took := time.Since(start); took > 10*time.Second {
		t.Fatalf("the in-flight wait was extended to %s by a policy change", took)
	}
}

// A settings save touches every destination. If SetPolicy reset the counters,
// saving a log level would quietly grant every destination that had been
// failing all night a fresh set of lives, and one that should have given up
// would retry for ever -- which is the exact condition GiveUpAfter exists to
// end.
func TestSetPolicyDoesNotResetTheRestartCounters(t *testing.T) {
	p := testProcess(t, fakeExit(1), Spec{
		AutoRestart: true,
		MinBackoff:  time.Millisecond,
		MaxBackoff:  time.Millisecond,
	})
	p.Start()
	waitFor(t, "three restarts", func() bool { return p.Status().Restarts >= 3 })

	before := p.Status().Restarts
	p.SetPolicy(Policy{MinBackoff: time.Millisecond, MaxBackoff: time.Millisecond, MaxRestarts: 500})

	if got := p.Status().Restarts; got < before {
		t.Fatalf("restarts went backwards: %d -> %d; a policy change must not forgive history", before, got)
	}
}

// Lowering the limit below what a process has already spent must not execute it
// where it stands. It is judged on its next exit, under the new rule.
func TestLoweringMaxRestartsAppliesFromTheNextExitRatherThanRetroactively(t *testing.T) {
	p := testProcess(t, fakeSleep(30*time.Second), Spec{
		AutoRestart: true,
		MinBackoff:  time.Millisecond,
		MaxBackoff:  time.Millisecond,
	})
	p.Start()
	waitFor(t, "the child to be running", func() bool { return p.Status().State == StateRunning })

	p.SetPolicy(Policy{MinBackoff: time.Millisecond, MaxBackoff: time.Millisecond, MaxRestarts: 1})

	if got := p.Status().State; got != StateRunning {
		t.Fatalf("state = %s, want running: a running child must not be failed by a policy change", got)
	}
}

// A reconciling teardown of a reconnecting destination has no child to signal,
// so it must not pay the 8s grace or the 12s stop budget. Eight of them in one
// reconcile is the case that makes this matter.
func TestStoppingAReconnectingProcessReturnsPromptly(t *testing.T) {
	p := testProcess(t, fakeExit(1), Spec{
		AutoRestart: true,
		MinBackoff:  30 * time.Second,
		MaxBackoff:  30 * time.Second,
	})
	p.Start()
	waitFor(t, "the backoff to begin", func() bool { return p.Status().State == StateReconnecting })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	start := time.Now()
	p.Stop(ctx)

	if took := time.Since(start); took > 2*time.Second {
		t.Fatalf("Stop took %s on a process with no child; teardown of a reconnecting "+
			"destination must not wait out its backoff", took)
	}
	if got := p.Status().State; got != StateStopped {
		t.Fatalf("state = %s, want stopped", got)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/supervisor/ -run 'TestSetPolicy|TestLoweringMaxRestarts|TestStoppingAReconnecting' -v`
Expected: FAIL to build — `undefined: Policy`, `p.SetPolicy undefined`.

- [ ] **Step 3: Add the Policy type and the fields**

In `internal/supervisor/supervisor.go`, immediately after the `Spec` struct, add:

```go
// Policy is the part of a Spec that can change while the process is running.
//
// It is separated out because everything else in a Spec ends up in an argv, and
// FFmpeg has no channel for changing an argv in flight. These three never reach
// the child at all -- they describe what the SUPERVISOR does once it has exited
// -- so retuning them is a memory write rather than a restart.
//
// Before this existed the only way to apply "be more patient with this
// destination" was to drop its connection: the three values rode in destSpec,
// so editing them tore the destination down and built it again. An operator
// raising a give-up threshold because a platform was flapping got a guaranteed
// outage as the price of asking for fewer of them.
type Policy struct {
	MinBackoff  time.Duration
	MaxBackoff  time.Duration
	MaxRestarts int
}
```

Add to the `Process` struct, after the `runMu` block:

```go
	// policyMu guards pol. Deliberately NOT p.mu: setState takes p.mu and then
	// calls OnState, which fans out to the WebSocket, and a reconcile applying
	// a policy must never end up waiting behind a browser.
	policyMu sync.Mutex
	pol      Policy
	// retune wakes a supervisor that is sleeping out a backoff, so a lowered
	// ceiling takes effect now rather than after the wait it was already in.
	// Buffered by one and written non-blocking: applying a policy must never
	// block on a supervisor that is mid-spawn.
	retune chan struct{}
```

In `New`, after the two defaulting `if` blocks, and inside the returned literal:

```go
	return &Process{
		spec:   spec,
		pol:    Policy{MinBackoff: spec.MinBackoff, MaxBackoff: spec.MaxBackoff, MaxRestarts: spec.MaxRestarts},
		retune: make(chan struct{}, 1),
		log:    log.With("process", spec.Name),
		state:  StateStopped,
		logs:   newRing(logRingSize),
	}
```

Also amend the doc comments on `Spec.MinBackoff`/`MaxBackoff`/`MaxRestarts` to say they are the INITIAL policy, and that `SetPolicy` is what changes them afterwards. Add above `MaxRestarts`:

```go
	// MaxRestarts, MinBackoff and MaxBackoff seed the process's Policy. After
	// Start they are read only through Policy()/SetPolicy(); reading spec here
	// would mean a retune applied to a running destination had no effect.
```

- [ ] **Step 4: Add Policy, SetPolicy and waitBackoff**

Append to `internal/supervisor/supervisor.go`:

```go
// Policy returns the live restart policy.
func (p *Process) Policy() Policy {
	p.policyMu.Lock()
	defer p.policyMu.Unlock()
	return p.pol
}

// SetPolicy retunes a process that is already running.
//
// It deliberately does NOT reset the restart counter or the consecutive-failure
// count. A settings save touches every destination, so resetting them would
// mean that saving a log level silently granted every destination that had been
// failing all night a fresh set of lives -- and one that should have given up
// would retry for ever, which is the condition GiveUpAfter exists to end.
//
// A lowered MaxRestarts applies from the NEXT exit and never retroactively: a
// process is not failed for exits it made under the old rules.
func (p *Process) SetPolicy(pol Policy) {
	if pol.MinBackoff <= 0 {
		pol.MinBackoff = defaultMinBackoff
	}
	if pol.MaxBackoff <= 0 {
		pol.MaxBackoff = defaultMaxBackoff
	}
	// A ceiling below the floor would make backoff *= 2 clamp downwards for
	// ever, pinning the retry curve at the floor. The API validates the pair,
	// so this only catches a caller that constructed a Policy by hand.
	if pol.MaxBackoff < pol.MinBackoff {
		pol.MaxBackoff = pol.MinBackoff
	}

	p.policyMu.Lock()
	changed := p.pol != pol
	p.pol = pol
	p.policyMu.Unlock()
	if !changed {
		return
	}

	select {
	case p.retune <- struct{}{}:
	default:
	}
}

// waitBackoff sleeps out a retry delay, returning false when the process was
// stopped during it.
//
// A policy change during the wait shortens it to the new ceiling and never
// lengthens it. Both halves are deliberate: an operator who lowers MaxBackoff
// while a destination is crawling expects it back sooner, and one who raises it
// does not expect the destination they are already watching to go quiet for
// longer than it had already promised.
func (p *Process) waitBackoff(ctx context.Context, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for {
		wait := time.Until(deadline)
		if wait <= 0 {
			return true
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return false
		case <-timer.C:
			return true
		case <-p.retune:
			timer.Stop()
			if max := p.Policy().MaxBackoff; time.Until(deadline) > max {
				deadline = time.Now().Add(max)
				// Status.NextRetryIn is what the dashboard counts down, so it
				// has to move with the deadline or the card lies for the rest
				// of the wait.
				p.mu.Lock()
				p.nextRetry = deadline
				p.mu.Unlock()
			}
		}
	}
}
```

- [ ] **Step 5: Make supervise read the live policy**

In `supervise` (`internal/supervisor/supervisor.go:266`), make four edits.

Replace `backoff := p.spec.MinBackoff` with:

```go
	// From the policy, not the spec: a retune that landed between New and
	// Start must be the curve this run starts on.
	backoff := p.Policy().MinBackoff
```

Replace the stability reset:

```go
		if ranFor > stableAfter {
			backoff = p.Policy().MinBackoff
			consecutive = 0
		}
```

Replace the give-up test:

```go
		// Read here rather than at the top of supervise: the limit an exit is
		// judged against is the one in force when it exited, which is what
		// makes a lowered limit apply from the next exit rather than to the
		// history that preceded it.
		if pol := p.Policy(); pol.MaxRestarts > 0 && consecutive > pol.MaxRestarts {
```

Replace the sleep and the doubling:

```go
		if !p.waitBackoff(ctx, backoff) {
			p.setState(StateStopped, "")
			return
		}

		backoff *= 2
		if max := p.Policy().MaxBackoff; backoff > max {
			backoff = max
		}
```

- [ ] **Step 6: Run the tests**

Run: `go test ./internal/supervisor/ -race -count=1 -v`
Expected: PASS — the whole package, including the pre-existing lifecycle tests.

- [ ] **Step 7: Mutation-test the retune**

Temporarily delete the `case <-p.retune:` arm from `waitBackoff` (leaving the ctx and timer arms).
Run: `go test ./internal/supervisor/ -run TestSetPolicyShortensAnInFlightBackoff -count=1`
Expected: FAIL with "the retune did not shorten the wait already in flight".
Restore the arm by hand, re-run, confirm PASS.

- [ ] **Step 8: Pin the failed-process revival hazard**

`supervise` returns down the give-up path without clearing `p.running`, so `Start()` on a failed process is a no-op and only `Restart()` revives it. Task 2 depends on that. Append to `internal/supervisor/policy_test.go`:

```go
// Reviving a process that gave up.
//
// supervise returns down the give-up path WITHOUT clearing p.running, so
// Start() on a failed process is a silent no-op -- it takes the `if p.running`
// early return. Only Restart() revives it, and Stop() inside Restart returns
// immediately because done is already closed. The engine's "you raised the
// give-up limit, so this destination comes back" path is built on this, so it
// is pinned here rather than left to be rediscovered.
func TestAFailedProcessIsRevivedByRestartAndNotByStart(t *testing.T) {
	p := testProcess(t, fakeExit(1), Spec{
		AutoRestart: true,
		MinBackoff:  time.Millisecond,
		MaxBackoff:  time.Millisecond,
		MaxRestarts: 1,
	})
	p.Start()
	waitFor(t, "the process to give up", func() bool { return p.Status().State == StateFailed })

	p.Start()
	if got := p.Status().State; got != StateFailed {
		t.Fatalf("state = %s after Start on a failed process, want it unchanged at failed", got)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	p.SetPolicy(Policy{MinBackoff: time.Millisecond, MaxBackoff: time.Millisecond})
	p.Restart(ctx)

	waitFor(t, "the revived process to run again", func() bool { return p.Status().Restarts >= 2 })
}
```

Run: `go test ./internal/supervisor/ -run TestAFailedProcessIsRevived -race -count=1 -v`
Expected: PASS.

- [ ] **Step 9: Commit**

```bash
gofmt -w internal/supervisor/
go test ./internal/supervisor/ -race -count=1
git add internal/supervisor/
git commit -m "feat(supervisor): a restart policy that can change while the process runs

MinBackoff, MaxBackoff and MaxRestarts are the only part of a Spec that never
reaches the child's argv -- they describe what the supervisor does after it
exits -- so they are the only part that can honestly be retuned without
replacing the process. They move behind Policy()/SetPolicy().

SetPolicy does not reset the restart or consecutive-failure counters. A settings
save touches every destination, so resetting them would mean saving a log level
silently granted every destination that had been failing all night a fresh set
of lives, and one that should have given up would retry for ever.

A retune shortens a backoff already in flight and never lengthens it. An
operator lowering the ceiling on a crawling destination expects it back sooner;
one raising it does not expect the destination they are watching to go quiet for
longer than it had already promised.

Also pins two behaviours the engine work depends on: Stop on a reconnecting
process returns immediately because there is no child to signal, and a process
that gave up is revived by Restart and not by Start -- supervise returns without
clearing p.running, so Start takes its idempotence early return."
```

---

### Task 2: Destination resilience becomes a live apply

**Files:**
- Modify: `internal/engine/engine.go` (`destSpec` :1679, `startDest` :1866, `startDestinations` :1578)
- Create: `internal/engine/hotreload_test.go`

**Interfaces:**
- Consumes: `supervisor.Policy`, `(*supervisor.Process).SetPolicy/Policy/Restart` from Task 1.
- Produces: `destPolicy(row *db.Destination) supervisor.Policy`; `(*Engine).applyDestPolicy(d *destination, row *db.Destination)`; `moreForgiving(before, want supervisor.Policy) bool`. `destSpec` loses its three resilience terms.

- [ ] **Step 1: Write the failing tests**

Create `internal/engine/hotreload_test.go`:

```go
package engine

// Hot reload: the settings that reach a running process without replacing it.
//
// The dividing line is mechanical -- does the value end up in an FFmpeg argv?
// Everything that does keeps the signature-diff-and-respawn path. Everything
// that does not must be pushed into the running process, because the
// alternative is an operator being told a change applied while the process goes
// on running the old one.

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/ffmpeg"
	"github.com/rainmanjam/polyemesis/internal/routing"
	"github.com/rainmanjam/polyemesis/internal/supervisor"
)

// The reconnect policy governs what the supervisor does AFTER ffmpeg exits. It
// never reaches the command line, so editing it must not tear down a live
// output. Until this landed, the only way to tell polyemesis "be more patient
// with this platform" was to drop the connection to it.
func TestChangingOnlyTheResiliencePolicyDoesNotChangeTheDestinationSpec(t *testing.T) {
	compiled := routing.Result{FilterComplex: "[0:a:0]anull[out]", OutLabel: "[out]"}
	base := destSpec(testDestination(7, nil), compiled, "")

	tests := []struct {
		name   string
		mutate func(*db.Destination)
	}{
		{"raising the minimum backoff", func(d *db.Destination) { d.Resilience.MinBackoffSeconds = 5 }},
		{"raising the maximum backoff", func(d *db.Destination) { d.Resilience.MaxBackoffSeconds = 120 }},
		{"raising the give-up threshold", func(d *db.Destination) { d.Resilience.GiveUpAfter = 40 }},
		{"setting all three at once", func(d *db.Destination) {
			d.Resilience = db.DestResilience{MinBackoffSeconds: 2, MaxBackoffSeconds: 90, GiveUpAfter: 12}
		}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			row := testDestination(7, nil)
			tc.mutate(row)
			if got := destSpec(row, compiled, ""); got != base {
				t.Errorf("spec changed; the destination would be torn down to apply a "+
					"value that never reaches its command line")
			}
		})
	}
}

// The other half. Leaving destSpec must not mean the setting stopped working:
// it has to arrive somewhere, and destPolicy is where.
func TestResilienceStillReachesTheSupervisorPolicy(t *testing.T) {
	row := testDestination(7, nil)
	row.Resilience = db.DestResilience{MinBackoffSeconds: 3, MaxBackoffSeconds: 90, GiveUpAfter: 12}

	got := destPolicy(row)
	want := supervisor.Policy{
		MinBackoff:  3 * time.Second,
		MaxBackoff:  90 * time.Second,
		MaxRestarts: 12,
	}
	if got != want {
		t.Fatalf("destPolicy = %+v, want %+v", got, want)
	}
}

// The zero value must stay exactly what every destination ran on before the
// policy was configurable: the supervisor's own defaults, which secondsOr
// spells as a zero Duration.
func TestAnUnsetResiliencePolicyLeavesTheSupervisorDefaultsInPlace(t *testing.T) {
	if got := destPolicy(testDestination(7, nil)); got != (supervisor.Policy{}) {
		t.Fatalf("destPolicy = %+v, want the zero Policy so supervisor.New's defaults apply", got)
	}
}

// The invariant that makes the whole live-apply set safe: a live-applied value
// cannot be rejected by FFmpeg because it is never shown to FFmpeg. If a
// resilience field is ever added to the argv builder, this fails and the
// classification in reload.go becomes a lie.
func TestNoResilienceFieldExistsOnTheDestinationArgvSpec(t *testing.T) {
	forbidden := []string{"backoff", "giveup", "restart", "resilience"}
	rt := reflect.TypeOf(ffmpeg.DestSpec{})
	for i := range rt.NumField() {
		name := strings.ToLower(rt.Field(i).Name)
		for _, bad := range forbidden {
			if strings.Contains(name, bad) {
				t.Errorf("ffmpeg.DestSpec.%s looks like a reconnect-policy field. "+
					"The policy is applied live precisely because it never reaches an "+
					"argv; putting it in one makes that claim false and makes editing "+
					"it a silent no-op on every running destination.", rt.Field(i).Name)
			}
		}
	}
}

// The survivor branch of startDestinations is the only place a running
// destination is touched without being replaced, so it is the only place a
// retune can land.
func TestRefreshingARunningDestinationRetunesItsSupervisorPolicy(t *testing.T) {
	proc := supervisor.New(testLogger(), supervisor.Spec{Name: "dest:7"})
	running := &destination{
		row:     &db.Destination{ID: 7, Name: "twitch"},
		proc:    proc,
		port:    9001,
		subName: "dest:7",
		spec:    "unchanged",
	}
	e := &Engine{dests: map[int64]*destination{7: running}}

	updated := &db.Destination{ID: 7, Name: "twitch"}
	updated.Resilience = db.DestResilience{MinBackoffSeconds: 4, MaxBackoffSeconds: 60, GiveUpAfter: 9}
	e.startDestinations(map[int64]destPlan{7: {row: updated, spec: "unchanged"}})

	want := supervisor.Policy{MinBackoff: 4 * time.Second, MaxBackoff: 60 * time.Second, MaxRestarts: 9}
	if got := proc.Policy(); got != want {
		t.Fatalf("policy = %+v, want %+v: the edit was saved and never reached the "+
			"supervisor that governs the reconnect", got, want)
	}
}

// moreForgiving decides whether a destination that gave up should be revived. 0
// is unlimited, so it is the MOST forgiving value rather than the least -- read
// as a plain number it sorts the wrong way round.
func TestMoreForgivingTreatsZeroAsUnlimited(t *testing.T) {
	tests := []struct {
		name   string
		before supervisor.Policy
		want   supervisor.Policy
		revive bool
	}{
		{"raising a finite limit", supervisor.Policy{MaxRestarts: 5}, supervisor.Policy{MaxRestarts: 20}, true},
		{"lowering a finite limit", supervisor.Policy{MaxRestarts: 20}, supervisor.Policy{MaxRestarts: 5}, false},
		{"finite to unlimited", supervisor.Policy{MaxRestarts: 5}, supervisor.Policy{MaxRestarts: 0}, true},
		{"unlimited to finite", supervisor.Policy{MaxRestarts: 0}, supervisor.Policy{MaxRestarts: 5}, false},
		{"unchanged", supervisor.Policy{MaxRestarts: 5}, supervisor.Policy{MaxRestarts: 5}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := moreForgiving(tc.before, tc.want); got != tc.revive {
				t.Errorf("moreForgiving = %v, want %v", got, tc.revive)
			}
		})
	}
}
```

- [ ] **Step 2: Add the test logger helper if it is missing**

Run: `grep -rn "func testLogger" internal/engine/`
If it prints nothing, append to `internal/engine/hotreload_test.go`:

```go
// testLogger discards output. The engine's constructors all want a *slog.Logger
// and none of these tests assert on log lines.
func testLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }
```

and add `"io"` and `"log/slog"` to the import block. If it already exists, use the existing one and do not add a second.

- [ ] **Step 3: Run tests to verify they fail**

Run: `go test ./internal/engine/ -run 'TestChangingOnlyTheResilience|TestResilienceStill|TestAnUnsetResilience|TestNoResilienceField|TestRefreshingARunningDestinationRetunes|TestMoreForgiving' -v`
Expected: FAIL to build — `undefined: destPolicy`, `undefined: moreForgiving`; and once those exist, `TestChangingOnlyTheResiliencePolicyDoesNotChangeTheDestinationSpec` fails on all four subtests because `destSpec` still hashes them.

- [ ] **Step 4: Take resilience out of destSpec**

In `internal/engine/engine.go`, in `destSpec`, delete these three lines and the comment above them:

```go
		// The reconnect policy is a property of the SUPERVISOR, not of the
		// command line, so it does not show up in the argv -- which is exactly
		// why it has to be named here. Without it, raising a give-up threshold
		// would be stored and never reach the process it governs.
		strconv.Itoa(row.Resilience.MinBackoffSeconds),
		strconv.Itoa(row.Resilience.MaxBackoffSeconds),
		strconv.Itoa(row.Resilience.GiveUpAfter),
```

Replace them with:

```go
		// Resilience is deliberately ABSENT. It is a property of the
		// supervisor, not of the command line, and supervisor.SetPolicy now
		// carries it into a process that is already running -- see
		// applyDestPolicy. The reasoning that first put it here was right about
		// the danger (a setting stored and never reaching the process it
		// governs) and wrong about the remedy: the remedy was to deliver it,
		// not to drop the operator's connection in order to deliver it.
```

- [ ] **Step 5: Add destPolicy, applyDestPolicy and moreForgiving**

Append to `internal/engine/engine.go`, next to `secondsOr`:

```go
// destPolicy is the reconnect policy for one destination row.
//
// The zero value must map to the zero Policy, not to an explicit 1s/30s: that
// is what leaves supervisor.New's own defaults in place, which is what every
// destination ran on before the policy was configurable.
func destPolicy(row *db.Destination) supervisor.Policy {
	return supervisor.Policy{
		MinBackoff:  secondsOr(row.Resilience.MinBackoffSeconds, 0),
		MaxBackoff:  secondsOr(row.Resilience.MaxBackoffSeconds, 0),
		MaxRestarts: row.Resilience.GiveUpAfter,
	}
}

// applyDestPolicy carries a changed reconnect policy into a destination that is
// already running, and revives one that had given up under a stricter rule.
//
// The revival is the one place this plan chooses a restart over a live apply,
// and it is chosen deliberately. Raising GiveUpAfter on a destination that has
// already exhausted the old limit and would otherwise sit in StateFailed for
// ever is exactly the "stored, reported as applied, and does nothing" failure
// this whole file is littered with warnings about. Lowering it is NOT
// retroactive: a destination is not executed for exits it made under the old
// rules.
//
// Start() cannot do the revival -- supervise returns down the give-up path
// without clearing p.running, so Start takes its idempotence early return.
// Restart() is the only door, and its Stop returns immediately because the
// supervise goroutine has already closed done.
func (e *Engine) applyDestPolicy(d *destination, row *db.Destination) {
	if d == nil || d.proc == nil {
		return
	}
	before := d.proc.Policy()
	want := destPolicy(row)
	if before == want {
		return
	}
	d.proc.SetPolicy(want)
	e.log.Info("destination reconnect policy retuned without a restart",
		"dest", row.Name, "minBackoff", want.MinBackoff, "maxBackoff", want.MaxBackoff,
		"giveUpAfter", want.MaxRestarts)

	if d.proc.Status().State != supervisor.StateFailed || !moreForgiving(before, want) {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), stopTimeout)
	defer cancel()
	d.proc.Restart(ctx)
	e.log.Info("destination revived: it had given up under the previous limit",
		"dest", row.Name, "giveUpAfter", want.MaxRestarts)
}

// moreForgiving reports whether want allows more attempts than before.
//
// 0 means unlimited, so it is the MOST forgiving value rather than the least.
// Compared as a plain number it sorts exactly the wrong way round, which would
// revive a destination the operator had just told to give up sooner.
func moreForgiving(before, want supervisor.Policy) bool {
	if want.MaxRestarts == 0 {
		return before.MaxRestarts != 0
	}
	if before.MaxRestarts == 0 {
		return false
	}
	return want.MaxRestarts > before.MaxRestarts
}
```

- [ ] **Step 6: Use destPolicy in startDest and apply it in the survivor branch**

In `startDest`, replace the three policy lines in the `supervisor.Spec` literal:

```go
		// Per-destination reconnect policy. Zero values leave the supervisor's
		// own defaults in place, which is what every destination ran on before
		// this was configurable. The same three values are re-applied without a
		// restart by applyDestPolicy when they change.
		MinBackoff:  destPolicy(row).MinBackoff,
		MaxBackoff:  destPolicy(row).MaxBackoff,
		MaxRestarts: destPolicy(row).MaxRestarts,
```

In `startDestinations`, replace the survivor branch:

```go
		e.mu.Lock()
		if cur := e.dests[id]; cur != nil {
			// Survived the stop phase, so it is running with the right
			// arguments; refresh the row for cosmetic fields like the name.
			//
			// Replaced wholesale rather than mutated in place: Status hands out
			// these pointers and then reads their fields after dropping the
			// lock, which is only safe while a published destination never
			// changes again.
			next := *cur
			next.row = p.row
			e.dests[id] = &next
			e.mu.Unlock()
			// AFTER the unlock. SetPolicy itself is a memory write, but the
			// revival path calls Restart, which blocks for up to stopTimeout.
			// Holding e.mu across that would stall every Status() the dashboard
			// asks for and every other tier's reconcile behind it.
			e.applyDestPolicy(&next, p.row)
			continue
		}
		e.mu.Unlock()
```

- [ ] **Step 7: Run the tests**

Run: `go test ./internal/engine/ -race -count=1`
Expected: PASS — the whole package. `TestDestSpec` in `rendition_test.go` is unaffected because none of its cases mutate `Resilience`.

- [ ] **Step 8: Mutation-test both halves**

First, the removal. Temporarily re-add `strconv.Itoa(row.Resilience.GiveUpAfter),` to `destSpec`.
Run: `go test ./internal/engine/ -run TestChangingOnlyTheResiliencePolicy -count=1`
Expected: FAIL on "raising the give-up threshold" and "setting all three at once". Remove the line again and confirm PASS.

Second, the delivery. Temporarily comment out the `e.applyDestPolicy(&next, p.row)` call.
Run: `go test ./internal/engine/ -run TestRefreshingARunningDestinationRetunes -count=1`
Expected: FAIL with "the edit was saved and never reached the supervisor". Restore and confirm PASS.

- [ ] **Step 9: Commit**

```bash
gofmt -w internal/engine/
go test ./internal/engine/ -race -count=1
git add internal/engine/engine.go internal/engine/hotreload_test.go
git commit -m "feat(engine): retune a destination's reconnect policy without dropping it

The three resilience values leave destSpec and are delivered by SetPolicy to the
process that is already running. They govern what the supervisor does after
ffmpeg exits and never reach the command line, so tearing the destination down
to apply them meant the only way to say 'be more patient with this platform' was
to drop the connection to it.

The comment that put them in destSpec was right about the danger -- a setting
stored and never reaching the process it governs -- and wrong about the remedy.
The remedy was to deliver it, not to cause an outage in order to deliver it.

One exception, chosen deliberately: raising the give-up threshold on a
destination that has ALREADY given up restarts it, because leaving it in
StateFailed for ever would be the exact silent no-op this is fixing. Lowering
the threshold is not retroactive.

TestNoResilienceFieldExistsOnTheDestinationArgvSpec is the guard that keeps the
claim true: a live-applied value cannot be rejected by FFmpeg because it is
never shown to FFmpeg, and that stops being true the moment one of these appears
in the argv builder."
```

---

### Task 3: The metering throttle reads the current interval

**Files:**
- Modify: `internal/engine/engine.go` (Engine struct, `reconcileMeters` :1249)
- Modify: `internal/engine/hotreload_test.go`

**Interfaces:**
- Produces: `Engine.meterInterval atomic.Int64`; `type meterThrottle struct`; `(*meterThrottle).allow(now time.Time) bool`; `(*Engine).applyMeterInterval(m db.MeterSettings)`.

- [ ] **Step 1: Write the failing tests**

Append to `internal/engine/hotreload_test.go`:

```go
// meters.intervalMs was captured by value into the StdoutHandler closure when
// the metering sidecar spawned, and it is not in metersSig -- so changing it
// stored cleanly, returned 200, and did absolutely nothing until some unrelated
// change happened to restart the meters. It is a throttle in a Go parser and
// never reaches an argv, so it must apply to the running process.
func TestTheMeterThrottleReadsTheCurrentIntervalRatherThanTheOneItSpawnedWith(t *testing.T) {
	e := &Engine{}
	e.applyMeterInterval(db.MeterSettings{Enabled: true, IntervalMS: 1000})
	th := &meterThrottle{e: e}

	base := time.Now()
	if !th.allow(base) {
		t.Fatal("the first frame must always be published")
	}
	if th.allow(base.Add(50 * time.Millisecond)) {
		t.Fatal("a frame 50ms into a 1000ms window was published; the throttle is not throttling")
	}

	e.applyMeterInterval(db.MeterSettings{Enabled: true, IntervalMS: 10})

	if !th.allow(base.Add(50 * time.Millisecond)) {
		t.Fatal("the same frame was still suppressed after the interval was lowered to 10ms; " +
			"the throttle is reading a value captured when the sidecar spawned")
	}
}

// The interval has to be stored before every early return in reconcileMeters,
// or lowering it on a box whose ingest has not probed yet would be lost -- and
// the operator would have to change it twice.
func TestTheMeterIntervalIsAppliedEvenWhenTheMetersCannotRun(t *testing.T) {
	e := &Engine{log: testLogger(), source: routing.DefaultSource()}
	e.reconcileMeters(db.Settings{Meters: db.MeterSettings{Enabled: false, IntervalMS: 250}})

	if got := e.meterInterval.Load(); got != int64(250*time.Millisecond) {
		t.Fatalf("meterInterval = %d, want %d: the interval must be stored before the "+
			"early returns, or a change made while the meters are down is lost",
			got, int64(250*time.Millisecond))
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/engine/ -run 'TestTheMeter' -v`
Expected: FAIL to build — `undefined: meterThrottle`, `e.applyMeterInterval undefined`, `e.meterInterval undefined`.

- [ ] **Step 3: Add the field and the throttle**

In `internal/engine/engine.go`, add to the `Engine` struct beside `sink`:

```go
	// meterInterval is the metering throttle, in nanoseconds.
	//
	// Atomic rather than under e.mu because it is read on the metering child's
	// stdout goroutine for every astats frame -- up to fifty a second -- and
	// taking the engine lock at that rate to answer a question that changes
	// once a month would be absurd. It exists at all because the value used to
	// be captured into the StdoutHandler closure at spawn time, which made
	// editing it a silent no-op.
	meterInterval atomic.Int64
```

Add, next to `reconcileMeters`:

```go
// applyMeterInterval publishes a changed metering throttle to the sidecar that
// is already running.
//
// Called before every early return in reconcileMeters, deliberately: a change
// made while the meters are down (no probe yet, or metering switched off) has
// to survive until they come back, or the operator has to make it twice.
func (e *Engine) applyMeterInterval(m db.MeterSettings) {
	e.meterInterval.Store(int64(time.Duration(m.IntervalMS) * time.Millisecond))
}

// meterThrottle rate-limits metering frames on their way to the WebSocket.
//
// It holds the ENGINE rather than a Duration. That is the whole point: astats
// prints far faster than any UI can draw, so the frames have to be shed, but
// the rate at which they are shed is an operator setting that must not require
// respawning the sidecar to change. A captured Duration is what made
// meters.intervalMs a setting that stored and did nothing.
type meterThrottle struct {
	e    *Engine
	last time.Time
}

func (t *meterThrottle) allow(now time.Time) bool {
	if now.Sub(t.last) < time.Duration(t.e.meterInterval.Load()) {
		return false
	}
	t.last = now
	return true
}
```

- [ ] **Step 4: Wire it into reconcileMeters**

In `reconcileMeters`, insert as the very first statement of the function body:

```go
	// Before every early return below. See applyMeterInterval.
	e.applyMeterInterval(s.Meters)
```

Delete the line `interval := time.Duration(s.Meters.IntervalMS) * time.Millisecond` and replace the `StdoutHandler` closure with:

```go
		// astats prints far faster than any UI can draw; throttle here rather
		// than flooding every WebSocket client with 50 frames a second. The
		// rate is read per frame from the engine, so lowering it applies to
		// this sidecar without respawning it.
		StdoutHandler: func(r io.Reader) error {
			th := &meterThrottle{e: e}
			return ffmpeg.ParseLevels(r, channels, func(l ffmpeg.Levels) {
				now := time.Now()
				if !th.allow(now) {
					return
				}
				e.mu.Lock()
				e.levels = l
				e.levelsAt = now
				e.mu.Unlock()
				e.bus.Publish(events.TypeLevels, l)
			})
		},
```

- [ ] **Step 5: Run the tests**

Run: `go test ./internal/engine/ -race -count=1`
Expected: PASS.

- [ ] **Step 6: Mutation-test the throttle**

Temporarily change `meterThrottle` to capture the interval once:

```go
type meterThrottle struct {
	e    *Engine
	iv   time.Duration
	last time.Time
}
```

with `allow` reading `t.iv` and `reconcileMeters` setting `iv` at construction.
Run: `go test ./internal/engine/ -run TestTheMeterThrottleReadsTheCurrentInterval -count=1`
Expected: FAIL with "the throttle is reading a value captured when the sidecar spawned".
Restore by hand, re-run, confirm PASS.

- [ ] **Step 7: Commit**

```bash
gofmt -w internal/engine/
go test ./internal/engine/ -race -count=1
git add internal/engine/engine.go internal/engine/hotreload_test.go
git commit -m "fix(engine): the meter interval applies to the sidecar already running

meters.intervalMs was captured by value into the StdoutHandler closure when the
metering child spawned, and it is not in metersSig -- so changing it stored
cleanly, returned 200, and did nothing at all until some unrelated change
happened to restart the meters. Lowering the interval to watch a quiet channel
appeared to work and did not.

The throttle now holds the Engine and reads an atomic per frame. Atomic rather
than e.mu because it is consulted up to fifty times a second on the child's
stdout goroutine to answer a question that changes once a month.

applyMeterInterval runs before every early return in reconcileMeters, so a
change made while the meters are down -- switched off, or waiting on a first
probe -- survives until they come back rather than being lost."
```

---

### Task 4: The classification table and its drift guard

**Files:**
- Create: `internal/engine/reload.go`
- Create: `internal/engine/reload_test.go`

**Interfaces:**
- Consumes: `db.Settings` for the reflection walk.
- Produces: `type ReloadClass string` with `ClassLive`/`ClassRespawn`/`ClassRebind`/`ClassOnDemand`; `type ReloadRule struct{ Class ReloadClass; Applies, Why string }`; `var settingsReload map[string]ReloadRule`; `ClassOf(path string) (ReloadRule, bool)`.

- [ ] **Step 1: Write the failing tests**

Create `internal/engine/reload_test.go`:

```go
package engine

// The classification guard.
//
// Every silent no-op this repo has shipped had the same shape: a field was
// added to db.Settings, validated, stored, returned by the API, and never
// wired to anything that was already running. meters.intervalMs was the most
// recent. The remedy is not vigilance; it is that adding a field to
// db.Settings without saying what happens when it changes must not compile
// green.

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
)

func TestEverySettingsFieldHasAReloadRule(t *testing.T) {
	var missing []string
	walkSettings(reflect.TypeOf(db.Settings{}), "", func(path string) {
		if _, ok := settingsReload[path]; !ok {
			missing = append(missing, path)
		}
	})

	for _, p := range missing {
		t.Errorf("db.Settings.%s has no entry in settingsReload. Say what happens "+
			"when an operator changes it while the stream is up: ClassLive (and name "+
			"the function that pushes it into the running process), ClassRespawn (and "+
			"name the signature function that notices), ClassRebind, or ClassOnDemand. "+
			"A field with no answer is a field that will be stored, reported as saved, "+
			"and ignored.", p)
	}
}

func TestNoReloadRuleNamesAFieldThatNoLongerExists(t *testing.T) {
	known := map[string]bool{}
	walkSettings(reflect.TypeOf(db.Settings{}), "", func(path string) { known[path] = true })

	for path := range settingsReload {
		if !known[path] {
			t.Errorf("settingsReload names %q, which is not a field of db.Settings. "+
				"A stale rule is worse than none: it reads as a decision somebody made "+
				"about behaviour that no longer exists.", path)
		}
	}
}

// A rule's Applies field is a claim about the code, so it has to be checkable.
// A ClassLive entry that names a function nobody wrote is the same lie as no
// entry at all, dressed up as a decision.
func TestEveryReloadRuleNamesAFunctionThatExists(t *testing.T) {
	// The packages a settings value can be delivered by. internal/api carries
	// the two out-of-band appliers (chat retention, automod).
	dirs := []string{
		filepath.Join("."),
		filepath.Join("..", "playout"),
		filepath.Join("..", "recording"),
		filepath.Join("..", "api"),
		filepath.Join("..", "supervisor"),
		filepath.Join("..", "srtserver"),
		filepath.Join("..", "mqtt"),
		filepath.Join("..", "jobs"),
		filepath.Join("..", "chat"),
	}
	src := readGoSources(t, dirs)

	for path, rule := range settingsReload {
		if rule.Applies == "" {
			t.Errorf("settingsReload[%q] names no function. Every rule must say which "+
				"function carries the change, or it is documentation rather than a "+
				"decision anybody can check.", path)
			continue
		}
		if rule.Why == "" {
			t.Errorf("settingsReload[%q] has no reason. The reason is what a reviewer "+
				"reads when they disagree with the class.", path)
		}
		re := regexp.MustCompile(`func (\([^)]*\) )?` + regexp.QuoteMeta(rule.Applies) + `\b`)
		if !re.MatchString(src) {
			t.Errorf("settingsReload[%q] names %s, which is not defined in any package "+
				"a settings value can be delivered by. Either the function was renamed "+
				"and the rule was not, or the delivery was never written.",
				path, rule.Applies)
		}
	}
}

// The two fields this work moved. Pinned by name so a future edit that quietly
// puts either back into a signature has to argue with a test.
func TestTheFieldsThisWorkMadeLiveAreClassifiedLive(t *testing.T) {
	for _, path := range []string{"meters.intervalMs", "destinations.staggerMs"} {
		if got := settingsReload[path].Class; got != ClassLive {
			t.Errorf("settingsReload[%q].Class = %q, want %q", path, got, ClassLive)
		}
	}
}

// walkSettings visits every leaf json path in a struct tree.
//
// Modelled on walk() in internal/db/settings_drift_test.go rather than shared
// with it: that one is a test helper in another package, and exporting it to
// reach across would make a production package depend on a test one.
func walkSettings(rt reflect.Type, prefix string, visit func(path string)) {
	for rt.Kind() == reflect.Pointer {
		rt = rt.Elem()
	}
	if rt.Kind() != reflect.Struct {
		return
	}
	for i := range rt.NumField() {
		f := rt.Field(i)
		if !f.IsExported() {
			continue
		}
		tag := f.Tag.Get("json")
		if tag == "-" {
			continue
		}
		name, _, _ := strings.Cut(tag, ",")
		if name == "" {
			name = f.Name
		}
		path := name
		if prefix != "" {
			path = prefix + "." + name
		}

		ft := f.Type
		for ft.Kind() == reflect.Pointer || ft.Kind() == reflect.Slice || ft.Kind() == reflect.Map {
			ft = ft.Elem()
		}
		if ft.Kind() == reflect.Struct && ft.PkgPath() != "time" && ft.Name() != "Time" {
			walkSettings(ft, path, visit)
			continue
		}
		visit(path)
	}
}

func readGoSources(t *testing.T, dirs []string) string {
	t.Helper()
	var b strings.Builder
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("cannot read %s: %v", dir, err)
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
				continue
			}
			raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
			if err != nil {
				t.Fatalf("cannot read %s: %v", e.Name(), err)
			}
			b.Write(raw)
			b.WriteByte('\n')
		}
	}
	return b.String()
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/engine/ -run 'TestEverySettingsField|TestNoReloadRule|TestEveryReloadRule|TestTheFieldsThisWork' -v`
Expected: FAIL to build — `undefined: settingsReload`, `undefined: ClassLive`.

- [ ] **Step 3: Write the table**

Create `internal/engine/reload.go`:

```go
package engine

// What happens when an operator changes a setting while the stream is up.
//
// This file is the answer, written down as data rather than as prose, because
// prose does not fail a build. Every silent no-op this repo has shipped had the
// same shape: a field added to db.Settings, validated, stored, returned by the
// API, and never wired to anything already running. The settings page said
// saved; the process went on doing what it was doing.
//
// The dividing line is mechanical, and it is the only reason any of this is
// safe: DOES THE VALUE REACH AN FFMPEG ARGV? If it does, the child is replaced,
// because FFmpeg 8.1.2 has no way to change a muxer, an encoder, an output URL
// or a stream mapping in flight, and the two channels it does offer -- zmq and
// sendcmd -- address only filters that were instantiated with a command
// interface and give no readable confirmation that a command was accepted. A
// half-applied graph nobody can read back is worse than a restart.
//
// If it does not reach an argv, it must be delivered to the running process,
// and the rule names the function that delivers it.

// ReloadClass is what a change to one settings field costs.
type ReloadClass string

const (
	// ClassLive is applied to whatever is already running. No child process is
	// replaced and no viewer or platform connection is dropped.
	ClassLive ReloadClass = "live"
	// ClassRespawn is baked into a child's argv. The signature named in the
	// rule notices, and the child is replaced.
	ClassRespawn ReloadClass = "respawn"
	// ClassRebind is a bound socket. The listener is stopped and rebound; every
	// publisher on it reconnects.
	ClassRebind ReloadClass = "rebind"
	// ClassOnDemand is read at the moment it is needed and never held by
	// anything. Nothing has to be applied because nothing has a copy.
	//
	// This is the class most likely to be claimed falsely, so a rule using it
	// still has to name the reader.
	ClassOnDemand ReloadClass = "on_demand"
)

// ReloadRule records the decision for one settings field.
type ReloadRule struct {
	Class ReloadClass
	// Applies names the function that carries the change: the one that pushes
	// the value into a running process for ClassLive, the signature that
	// notices for ClassRespawn, the reader for ClassOnDemand. Checked against
	// the source by TestEveryReloadRuleNamesAFunctionThatExists, so it is a
	// claim rather than a comment.
	Applies string
	Why     string
}

// ClassOf reports the rule for a dotted json path, e.g. "meters.intervalMs".
func ClassOf(path string) (ReloadRule, bool) {
	r, ok := settingsReload[path]
	return r, ok
}

// settingsReload is keyed by the dotted json path of every leaf in db.Settings.
// TestEverySettingsFieldHasAReloadRule fails when a field is added without one.
var settingsReload = map[string]ReloadRule{
	// ---------------------------------------------------------------- ingest
	"ingest.mode":                          {ClassRespawn, "reconcileIngest", "chooses the listener; SRT has no child at all, so the mode change is a spawn or a kill"},
	"ingest.srt.passphrase":                {ClassRespawn, "reconcileIngest", "an SRT socket option, fixed at bind"},
	"ingest.srt.latencyMs":                 {ClassRespawn, "reconcileIngest", "an SRT socket option, fixed at bind"},
	"ingest.rtmp.app":                      {ClassRespawn, "reconcileIngest", "part of the listener's URL"},
	"ingest.rtmp.streamKey":                {ClassRespawn, "reconcileIngest", "matched against the publisher's playpath at connect"},
	"ingest.pull.url":                      {ClassRespawn, "reconcileIngest", "the input FFmpeg dials"},
	"ingest.pull.reconnectDelayMaxSeconds": {ClassRespawn, "reconcileIngest", "an FFmpeg input option, not a supervisor one"},
	"ingest.pull.rtspTransport":            {ClassRespawn, "reconcileIngest", "an FFmpeg input option"},
	"ingest.annotations":                   {ClassRespawn, "planDestinations", "recompiles every routing graph, so it moves compiled.FilterComplex and therefore destSpec"},

	// ------------------------------------------------------------- listeners
	"listeners.srtPort":  {ClassRebind, "reconcileSharedIngest", "one bound socket for every source; the Manager stops it and binds the new number"},
	"listeners.rtmpPort": {ClassRespawn, "reconcileIngest", "part of the ingest child's listen URL"},

	// ------------------------------------------------------------- recording
	"recording.enabled":        {ClassRespawn, "reconcileRecorder", "starts or stops the child"},
	"recording.segmentSeconds": {ClassRespawn, "reconcileRecorder", "the segment muxer's argv"},
	"recording.maxAgeHours":    {ClassLive, "ScanAndSweep", "a retention rule the sweeper re-reads through the settings func it was handed"},
	"recording.maxGb":          {ClassLive, "ScanAndSweep", "a retention rule, re-read per sweep"},
	"recording.minFreeGb":      {ClassLive, "CheckFreeSpace", "the free-space floor, re-read per sweep"},
	"recording.stems":          {ClassRespawn, "reconcileRecorder", "adds output mappings to the recorder's argv"},
	"recording.stemCodec":      {ClassRespawn, "reconcileRecorder", "the stem encoder in the argv"},

	// --------------------------------------------------------------- preview
	"preview.enabled":            {ClassRespawn, "reconcilePreview", "starts or stops the on-demand encoder"},
	"preview.segmentSeconds":     {ClassRespawn, "previewSig", "the HLS muxer's argv, and the keyframe expression derived from it"},
	"preview.videoHeight":        {ClassRespawn, "previewSig", "the scale filter"},
	"preview.videoKbps":          {ClassRespawn, "previewSig", "the encoder's rate control"},
	"preview.idleTimeoutSeconds": {ClassLive, "previewIdleWindow", "read by sweepPreview each tick and deliberately absent from previewSig, so changing it never cycles a live preview"},

	// --------------------------------------------------------------- playout
	"playout.enabled":            {ClassRespawn, "Reconcile", "starts or stops every variant"},
	"playout.public":             {ClassOnDemand, "playoutHandler", "evaluated per request, because a route table is built once at startup and this is a runtime setting"},
	"playout.protection":         {ClassOnDemand, "playoutHandler", "evaluated per request"},
	"playout.allowCrossOrigin":   {ClassOnDemand, "setCORS", "a response header, decided per request"},
	"playout.format":             {ClassRespawn, "variantSig", "chooses the HLS or DASH muxer in the argv"},
	"playout.segmentSeconds":     {ClassRespawn, "variantSig", "-hls_time / -seg_duration"},
	"playout.playlistSegments":   {ClassRespawn, "variantSig", "-hls_list_size"},
	"playout.dvrWindowSeconds":   {ClassRespawn, "variantSig", "widens -hls_list_size"},
	"playout.maxDiskMb":          {ClassLive, "Sweep", "the sweeper reads m.settings, which Reconcile has already replaced"},
	"playout.audioKbps":          {ClassRespawn, "variantSig", "the AAC encoder's bitrate"},
	"playout.sessionIdleSeconds": {ClassLive, "SetLimits", "a viewer-table bound, pushed in by Reconcile before any variant is touched"},
	"playout.maxSessions":        {ClassLive, "SetLimits", "a viewer-table bound"},
	"playout.variants.name":      {ClassRespawn, "variantSig", "names the variant's own output directory"},
	"playout.variants.enabled":   {ClassRespawn, "Reconcile", "starts or stops one muxer"},
	"playout.variants.renditionId": {ClassRespawn, "variantSig", "changes which relay the muxer reads"},
	"playout.variants.audioTrack":  {ClassRespawn, "variantSig", "a stream mapping"},
	"playout.variants.bitrate":     {ClassLive, "writeMaster", "advertised in the master playlist, which Go writes; deliberately absent from variantSig so re-tuning a rendition does not restart the variant twice"},

	// -------------------------------------------------------------- failover
	"failover.enabled":              {ClassRespawn, "wantSelector", "starts or stops the selector tier, which moves every consumer's upstream signature"},
	"failover.graceSeconds":         {ClassLive, "failoverGrace", "read by sweepSelector every 500ms"},
	"failover.return":               {ClassLive, "applySourceChoice", "read by sweepSelector every 500ms"},
	"failover.returnStableSeconds":  {ClassLive, "applySourceChoice", "read by sweepSelector every 500ms"},
	"failover.backup.enabled":       {ClassRespawn, "reconcileBackupIngest", "starts or stops the second listener"},
	"failover.backup.mode":          {ClassRespawn, "backupIngestSig", "the backup ingest's argv"},
	"failover.backup.srt.passphrase": {ClassRespawn, "backupIngestSig", "an SRT socket option"},
	"failover.backup.srt.latencyMs":  {ClassRespawn, "backupIngestSig", "an SRT socket option"},
	"failover.backup.rtmp.app":       {ClassRespawn, "backupIngestSig", "part of the listener URL"},
	"failover.backup.rtmp.streamKey": {ClassRespawn, "backupIngestSig", "matched at connect"},
	"failover.backup.pull.url":       {ClassRespawn, "backupIngestSig", "the input FFmpeg dials"},
	"failover.backup.pull.reconnectDelayMaxSeconds": {ClassRespawn, "backupIngestSig", "an FFmpeg input option"},
	"failover.backup.pull.rtspTransport":            {ClassRespawn, "backupIngestSig", "an FFmpeg input option"},
	"failover.slate.enabled":   {ClassLive, "applySourceChoice", "whether the slate is an eligible choice is re-read every 500ms; the slate's own argv is not"},
	"failover.slate.imagePath": {ClassRespawn, "feedUpstreamSig", "the input file in the slate feed's argv"},
	"failover.slate.text":      {ClassRespawn, "feedUpstreamSig", "a drawtext filter argument"},

	// ----------------------------------------------------------------- synth
	"synth.silenceOnVideoOnly": {ClassRespawn, "wantSilence", "starts or stops the silence tier, which moves silenceSig and therefore every passthrough consumer"},

	// ---------------------------------------------------------------- meters
	"meters.enabled":    {ClassRespawn, "reconcileMeters", "starts or stops the sidecar"},
	"meters.intervalMs": {ClassLive, "applyMeterInterval", "a throttle in the Go stdout parser; it has never reached an argv, and capturing it at spawn made editing it a silent no-op"},

	// --------------------------------------------------------------- logging
	"logging.persistProcessLogs": {ClassLive, "applyLogging", "swaps the FileSink behind logSink, so children already running start filling the new file"},
	"logging.maxFileMb":          {ClassLive, "applyLogging", "re-opens the sink; no child is touched"},
	"logging.maxFiles":           {ClassLive, "applyLogging", "re-opens the sink; no child is touched"},

	// -------------------------------------------------------------- postProd
	"postProd.enabled":               {ClassOnDemand, "Admit", "read by the jobs governor when a job asks to run"},
	"postProd.concurrency":           {ClassOnDemand, "Admit", "read per admission"},
	"postProd.defaultMode":           {ClassOnDemand, "Admit", "read per admission"},
	"postProd.yieldToStream":         {ClassOnDemand, "Admit", "read per admission"},
	"postProd.cpuCeilingPercent":     {ClassOnDemand, "Admit", "read per admission"},
	"postProd.cpuResumePercent":      {ClassOnDemand, "Admit", "read per admission"},
	"postProd.cpuSustainedSeconds":   {ClassOnDemand, "Admit", "read per admission"},
	"postProd.cpuSettleSeconds":      {ClassOnDemand, "Admit", "read per admission"},
	"postProd.avoidGpuWhenStreaming": {ClassOnDemand, "Admit", "read per admission"},
	"postProd.gpuBusy":               {ClassOnDemand, "Admit", "read per admission"},
	"postProd.batteryFloorPercent":   {ClassOnDemand, "Admit", "read per admission"},
	"postProd.thermalCeilingC":       {ClassOnDemand, "Admit", "read per admission"},
	"postProd.niceLevel":             {ClassOnDemand, "Admit", "applied to the job process at spawn"},
	"postProd.idleIo":                {ClassOnDemand, "Admit", "applied to the job process at spawn"},
	"postProd.ingestLingerSeconds":   {ClassOnDemand, "Admit", "read per admission"},

	// ------------------------------------------------------------------ mqtt
	"mqtt.enabled":         {ClassLive, "ApplyAutomod", "the MQTT runner polls settings and notices by hash; see handlePutMQTTPassword"},
	"mqtt.brokerUrl":       {ClassLive, "ApplyAutomod", "polled by the runner"},
	"mqtt.username":        {ClassLive, "ApplyAutomod", "polled by the runner"},
	"mqtt.hasPassword":     {ClassLive, "ApplyAutomod", "reported by the runner, set through its own endpoint"},
	"mqtt.prefix":          {ClassLive, "ApplyAutomod", "polled by the runner"},
	"mqtt.instance":        {ClassLive, "ApplyAutomod", "polled by the runner"},
	"mqtt.clientId":        {ClassLive, "ApplyAutomod", "polled by the runner"},
	"mqtt.intervalSeconds": {ClassLive, "ApplyAutomod", "polled by the runner"},
	"mqtt.keepAliveSeconds": {ClassLive, "ApplyAutomod", "polled by the runner"},
	"mqtt.tlsSkipVerify":   {ClassLive, "ApplyAutomod", "polled by the runner"},
	"mqtt.discovery":       {ClassLive, "ApplyAutomod", "polled by the runner"},

	// ---------------------------------------------------------- destinations
	"destinations.staggerMs": {ClassLive, "startDestinations", "read per sweep and applied only to processes started in that sweep; it can never affect one already running"},

	// ------------------------------------------------------------ chat + automod
	"chat.retentionHours": {ClassLive, "ApplyChatRetention", "pushed into the Hub out of band by handlePutSettings"},
	"chat.keepMessages":   {ClassLive, "ApplyChatRetention", "pushed into the Hub out of band"},
	"chat.purgeMinutes":   {ClassLive, "ApplyChatRetention", "pushed into the Hub out of band"},

	"automod.enabled":         {ClassLive, "ApplyAutomod", "rebuilds the automod engine out of band"},
	"automod.platformEnabled": {ClassLive, "ApplyAutomod", "rebuilds the automod engine"},
	"automod.on":              {ClassLive, "ApplyAutomod", "rebuilds the matrix"},
	"automod.rules.id":        {ClassLive, "ApplyAutomod", "recompiles the patterns"},
	"automod.rules.name":      {ClassLive, "ApplyAutomod", "recompiles the patterns"},
	"automod.rules.enabled":   {ClassLive, "ApplyAutomod", "recompiles the patterns"},
	"automod.rules.pattern":   {ClassLive, "ApplyAutomod", "recompiles the patterns"},
	"automod.rules.action":    {ClassLive, "ApplyAutomod", "recompiles the patterns"},
	"automod.rules.timeoutSeconds": {ClassLive, "ApplyAutomod", "recompiles the patterns"},
}
```

Note on completeness: the map above is written from `db.Settings` as it stands. Run the guard in Step 4 and **add whatever it names** — including every remaining `automod.history.*` and `automod.model.*` leaf, all of which are `{ClassLive, "ApplyAutomod", ...}`. Do not delete the guard to make it pass.

- [ ] **Step 4: Run the tests and close the gaps**

Run: `go test ./internal/engine/ -run 'TestEverySettingsField|TestNoReloadRule|TestEveryReloadRule|TestTheFieldsThisWork' -count=1 -v`
Expected on the first run: FAIL, naming the leaves the map does not yet carry. Add each one with a class, a function and a reason. Repeat until PASS.

If `TestEveryReloadRuleNamesAFunctionThatExists` names a function it cannot find, the rule is wrong, not the test. Fix the rule to name the function that actually delivers the value.

- [ ] **Step 5: Mutation-test the drift guard**

Temporarily delete the `"meters.intervalMs"` entry from `settingsReload`.
Run: `go test ./internal/engine/ -run TestEverySettingsFieldHasAReloadRule -count=1`
Expected: FAIL naming `db.Settings.meters.intervalMs`.
Restore it, re-run, confirm PASS.

Then temporarily change that entry's `Applies` to `"applyMeterIntervalThatDoesNotExist"`.
Run: `go test ./internal/engine/ -run TestEveryReloadRuleNamesAFunctionThatExists -count=1`
Expected: FAIL. Restore and confirm PASS.

- [ ] **Step 6: Commit**

```bash
gofmt -w internal/engine/
go test ./internal/engine/ -race -count=1
git add internal/engine/reload.go internal/engine/reload_test.go
git commit -m "feat(engine): classify every settings field by what changing it costs

Every silent no-op this repo has shipped had one shape: a field added to
db.Settings, validated, stored, returned by the API, and never wired to anything
already running. meters.intervalMs was the most recent. The remedy is not
vigilance -- it is that adding a field without saying what happens when it
changes must not compile green.

The dividing line is mechanical and is the only reason live application is safe:
does the value reach an FFmpeg argv? If it does, the child is replaced, because
FFmpeg 8.1.2 cannot change a muxer, an encoder, an output URL or a stream
mapping in flight, and zmq/sendcmd address only pre-instrumented filters and
give no readable confirmation that a command was taken. A half-applied graph
nobody can read back is worse than a restart.

Each rule names the function that delivers the change, and that name is checked
against the source. A rule naming a function nobody wrote is the same lie as no
rule at all, dressed up as a decision."
```

---

### Task 5: Say what restarted and what applied live

**Files:**
- Modify: `internal/engine/reload.go` (recorder, event type)
- Modify: `internal/engine/engine.go` (`Reconcile`, `teardownDest`, `startDest`, `applyDestPolicy`, `teardownRendition`, `startRendition`)
- Modify: `internal/engine/manager.go` (`Reconcile`)
- Modify: `internal/api/handlers.go` (`handlePutSettings`)
- Modify: `internal/engine/reload_test.go`

**Interfaces:**
- Consumes: `events.Broker` from `Engine.bus`.
- Produces: `type ReloadNote struct{ Tier, Name, Action, Reason string }`; `type ReloadReport struct{ SourceID int64; SourceName string; Notes []ReloadNote }`; `(*Engine).LastReload() ReloadReport`; `(*Manager).LastReload() []ReloadReport`; `eventReload events.Type = "reload"`; `PUT /api/v1/settings` response gains `"reload"`.

- [ ] **Step 1: Write the failing tests**

Append to `internal/engine/reload_test.go`:

```go
// A reconcile that restarts a live output must say so. "Saved" is a statement
// about the database; an operator watching a card go grey needs to know it was
// their edit that did it, and an operator whose card did NOT go grey needs to
// know the edit still landed.
func TestAReconcileRecordsWhatItRestartedAndWhatItAppliedLive(t *testing.T) {
	rec := newReloadRecorder()
	rec.note("destination", "twitch", reloadRestart, "the routing graph changed")
	rec.note("destination", "youtube", reloadLive, "reconnect policy retuned")

	got := rec.snapshot()
	if len(got) != 2 {
		t.Fatalf("notes = %d, want 2", len(got))
	}
	if got[0].Action != reloadRestart || got[0].Name != "twitch" {
		t.Errorf("first note = %+v, want the restart of twitch", got[0])
	}
	if got[1].Action != reloadLive {
		t.Errorf("second note = %+v, want a live apply", got[1])
	}
}

// Notes raised outside a reconcile -- the preview idling out, a storage guard
// halting the recorder -- must not accumulate into the next settings save's
// report. They are not consequences of anything the operator just did.
func TestNotesRaisedOutsideAReconcileAreDropped(t *testing.T) {
	e := &Engine{log: testLogger()}
	e.noteReload("preview", "preview", reloadRestart, "idle timeout")

	if got := e.LastReload().Notes; len(got) != 0 {
		t.Fatalf("notes = %+v, want none: nothing was reconciling, so nothing an "+
			"operator did caused this", got)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/engine/ -run 'TestAReconcileRecords|TestNotesRaisedOutside' -v`
Expected: FAIL to build — `undefined: newReloadRecorder`, `undefined: reloadRestart`.

- [ ] **Step 3: Add the recorder**

Append to `internal/engine/reload.go`:

```go
import (
	"sync"
	"sync/atomic"

	"github.com/rainmanjam/polyemesis/internal/events"
)

const (
	// reloadRestart means a child process was replaced to apply the change.
	reloadRestart = "restart"
	// reloadLive means the change reached a process that kept running.
	reloadLive = "live"

	// eventReload announces what a reconcile moved. Declared here rather than
	// in internal/events because it is only meaningful to a system that has a
	// reconciler; the broker takes any type. Same precedent as eventFailover.
	eventReload events.Type = "reload"
)

// ReloadNote is one thing a reconcile did.
type ReloadNote struct {
	Tier   string `json:"tier"`
	Name   string `json:"name"`
	Action string `json:"action"`
	Reason string `json:"reason"`
}

// ReloadReport is everything one engine's reconcile did.
type ReloadReport struct {
	SourceID   int64        `json:"sourceId"`
	SourceName string       `json:"sourceName"`
	Notes      []ReloadNote `json:"notes"`
}

// reloadRecorder collects notes for one reconcile.
//
// It carries its own mutex rather than riding on e.mu because notes are raised
// from teardown paths that already hold, or are about to take, e.mu -- and
// because a note must never be the thing that deadlocks a reconcile.
type reloadRecorder struct {
	mu    sync.Mutex
	notes []ReloadNote
}

func newReloadRecorder() *reloadRecorder { return &reloadRecorder{} }

func (r *reloadRecorder) note(tier, name, action, reason string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.notes = append(r.notes, ReloadNote{Tier: tier, Name: name, Action: action, Reason: reason})
}

func (r *reloadRecorder) snapshot() []ReloadNote {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]ReloadNote(nil), r.notes...)
}

// noteReload records something this reconcile did, if a reconcile is what is
// doing it.
//
// A nil recorder is the normal case outside Reconcile and the note is dropped
// on purpose: the preview idling out and the storage guard halting the recorder
// are real events, but they are not consequences of anything the operator just
// saved, and folding them into a settings response would tell somebody their
// edit stopped a recording it had nothing to do with. They already reach the
// operator as TypeStatus and as alerts.
func (e *Engine) noteReload(tier, name, action, reason string) {
	if r := e.reloadRec.Load(); r != nil {
		r.note(tier, name, action, reason)
	}
}

// LastReload is what the most recent reconcile did.
//
// Honest limitation: concurrent reconciles interleave into one recorder, so two
// handlers saving at the same moment each see the union. That is the truth
// about what moved, which is more useful than a per-caller fiction, but it is
// not a per-request audit log and must not be read as one.
func (e *Engine) LastReload() ReloadReport {
	rep := e.lastReload.Load()
	if rep == nil {
		return ReloadReport{SourceID: e.sourceID, SourceName: e.SourceName()}
	}
	return *rep
}
```

Add to the `Engine` struct in `engine.go`, beside `sink`:

```go
	// reloadRec is the note collector for the reconcile currently in flight,
	// nil the rest of the time. Atomic because it is read from teardown paths
	// that hold e.mu.
	reloadRec  atomic.Pointer[reloadRecorder]
	lastReload atomic.Pointer[ReloadReport]
```

- [ ] **Step 4: Instrument Reconcile**

In `Engine.Reconcile` (`engine.go:775`), wrap the body:

```go
func (e *Engine) Reconcile() error {
	settings, err := e.effectiveSettings()
	if err != nil {
		return err
	}
	// Installed before the first sub-reconcile and cleared after the last, so a
	// note raised by previewLoop or by the storage guard -- neither of which is
	// a consequence of anything an operator just saved -- is dropped rather
	// than reported as one.
	rec := newReloadRecorder()
	e.reloadRec.Store(rec)
	defer func() {
		e.reloadRec.Store(nil)
		rep := ReloadReport{SourceID: e.sourceID, SourceName: e.SourceName(), Notes: rec.snapshot()}
		e.lastReload.Store(&rep)
		if len(rep.Notes) > 0 {
			e.bus.Publish(eventReload, rep)
		}
	}()

	e.mu.Lock()
	...
```

Leave the rest of the function unchanged.

Guard the `e.bus.Publish` with a nil check if the package's other publishers do; check with `grep -n "if e.bus != nil" internal/engine/engine.go` and match whatever the neighbours do.

- [ ] **Step 5: Raise notes at the four tier boundaries**

In `teardownDest`, as the first statement after the nil guard:

```go
	e.noteReload("destination", d.row.Name, reloadRestart,
		"its command line changed, or it was disabled or removed")
```

In `startDest`, immediately before `return nil`:

```go
	e.noteReload("destination", row.Name, reloadRestart, "started")
```

In `teardownRendition`, after its nil guard (use the same field the function already reads for logging):

```go
	e.noteReload("rendition", r.row.Name, reloadRestart,
		"its encode signature changed, or nothing selects it any more")
```

In `startRendition`, on the success path:

```go
	e.noteReload("rendition", row.Name, reloadRestart, "started")
```

In `applyDestPolicy` (Task 2), replace the two `e.log.Info` calls' *neighbourhood* by adding notes beside them — keep the log lines:

```go
	e.noteReload("destination", row.Name, reloadLive,
		fmt.Sprintf("reconnect policy retuned to %s..%s, giving up after %d",
			want.MinBackoff, want.MaxBackoff, want.MaxRestarts))
```

and, on the revival path:

```go
	e.noteReload("destination", row.Name, reloadRestart,
		"it had given up under the previous limit and the new one is more forgiving")
```

In `applyMeterInterval` (Task 3), append:

```go
	e.noteReload("meters", "meters", reloadLive, "metering interval applied without a respawn")
```

Guard against a nil `e.log` in `applyMeterInterval`'s callers is unnecessary — `noteReload` touches only the atomic.

- [ ] **Step 6: Expose it from the Manager and the API**

Append to `internal/engine/manager.go`:

```go
// LastReload is what each engine's most recent reconcile did, in display order.
//
// One report per engine rather than a merged list: a settings save is
// install-wide, and an operator with three programmes needs to know which one
// lost a destination.
func (m *Manager) LastReload() []ReloadReport {
	engines := m.Engines()
	out := make([]ReloadReport, 0, len(engines))
	for _, eng := range engines {
		out = append(out, eng.LastReload())
	}
	return out
}
```

In `internal/api/handlers.go`, change the final line of `handlePutSettings` from `writeJSON(w, http.StatusOK, settings)` to:

```go
	// The EFFECT, not just the intent -- the same argument setDestinationEnabled
	// already makes. "Saved" is a statement about the database. An operator
	// whose destination card just went grey needs to know their edit did that,
	// and one whose card did NOT move needs to know the edit still landed
	// somewhere rather than being stored and ignored.
	writeJSON(w, http.StatusOK, map[string]any{
		"settings": settings,
		"reload":   s.mgr.LastReload(),
	})
```

**This changes the response shape.** Check the UI's caller before committing:

Run: `grep -rn "putSettings\|/settings" ui/src/lib/api.ts`

If the UI consumes the settings object directly, keep the old shape at the top level and nest only the new field:

```go
	resp := map[string]any{"reload": s.mgr.LastReload()}
	raw, _ := json.Marshal(settings)
	_ = json.Unmarshal(raw, &resp) // settings fields at the top level, reload alongside
	writeJSON(w, http.StatusOK, resp)
```

Prefer the flat shape if the UI reads fields off the response; prefer the nested one if it ignores the body. Do not guess — read `ui/src/lib/api.ts` and follow what is there. Whichever you pick, run `cd ui && npx tsc -b --noEmit` before committing.

- [ ] **Step 7: Run the tests**

Run: `go test ./internal/engine/ ./internal/api/ -race -count=1`
Expected: PASS.

- [ ] **Step 8: Mutation-test the drop rule**

Temporarily change `noteReload` to append to a package-level slice instead of checking the atomic.
Run: `go test ./internal/engine/ -run TestNotesRaisedOutsideAReconcileAreDropped -count=1`
Expected: FAIL.
Restore, re-run, confirm PASS.

- [ ] **Step 9: Commit**

```bash
gofmt -w internal/engine/ internal/api/
go test ./internal/engine/ ./internal/api/ -race -count=1
git add internal/engine/ internal/api/handlers.go
git commit -m "feat(engine): a reconcile reports what it restarted and what applied live

Saving settings answered with the settings, which is a statement about the
database. An operator whose destination card just went grey needs to know their
edit did that; one whose card did not move needs to know the edit landed
somewhere rather than being stored and ignored. Both are now in the response.

Notes raised outside a reconcile are dropped on purpose. The preview idling out
and the storage guard halting the recorder are real events, but they are not
consequences of anything the operator just saved, and folding them into a
settings response would tell somebody their edit stopped a recording it had
nothing to do with. They already reach the operator as status and as alerts.

Concurrent reconciles interleave into one recorder, so two handlers saving at
the same moment each see the union. That is the truth about what moved, and it
is stated in LastReload's comment rather than papered over. It is not a
per-request audit log."
```

---

### Task 6: Operator documentation and full verification

**Files:**
- Create: `docs/HOT-RELOAD.md`
- Modify: none

- [ ] **Step 1: Write the operator document**

Create `docs/HOT-RELOAD.md` containing, in this order:

1. The one-sentence rule: a setting that reaches an FFmpeg command line replaces the process that runs it; a setting that does not is delivered to the process still running.
2. The two tables from this plan's "The two lists" section, verbatim, with a line saying the authority is `settingsReload` in `internal/engine/reload.go` and that this document is a rendering of it.
3. A "what a restart costs" paragraph: a destination restart drops the platform connection and the platform's own reconnect behaviour decides how visible that is; a rendition restart cycles every destination and playout variant downstream of it; a silence-tier or selector transition restarts every passthrough destination.
4. A "what is not hot-reloadable, and why" section: the FFmpeg zmq/sendcmd finding, stated as in the Global Constraints above.
5. A "mid-reconnect" section reproducing the six bullets from this plan's "What happens to a destination mid-reconnect".
6. An explicit **Not covered** section:
   - No overlay, geometry, bitrate, routing-graph or transport change is applied live. All still respawn.
   - The ingest is never hot-reloaded. Any ingest edit drops the publisher.
   - `listeners.srtPort` rebinds a shared socket, so it interrupts *every* programme, not just the one being edited.
   - `LastReload` is best-effort telemetry, not an audit log; concurrent saves interleave.
   - The classification table is a *claim* checked for shape (the field exists, the function exists, the reason is non-empty). It is not a proof that every already-live entry is genuinely live. Only the two fields this work moved — `destinations[].resilience.*` and `meters.intervalMs` — have behavioural tests.
   - A settings save still calls `Manager.Reconcile`, so every engine walks its whole tier tree and re-hashes every signature. That cost is unchanged and is O(engines x tiers) per save.

- [ ] **Step 2: Run every gate CI runs, in CI's order**

```bash
gofmt -l ./cmd ./internal | tee /tmp/fmt && test ! -s /tmp/fmt
go build ./...
go vet ./...
go test -race -timeout 15m ./...
```

Expected: gofmt prints nothing; build and vet silent; every package `ok`.

- [ ] **Step 3: Confirm the copy promise is untouched**

```bash
git diff --stat main -- internal/ffmpeg/
```

Expected: **empty**. No task in this plan modifies `internal/ffmpeg`. If it is not empty, an argv changed and the central claim of this plan — that nothing live-applied reaches FFmpeg — is false.

```bash
grep -rn "c:v copy" internal/ffmpeg/build.go internal/playout/args.go | wc -l
```

Expected: unchanged from `git stash`-ing the branch and re-running. The rendition, destination and playout video paths still copy.

- [ ] **Step 4: Confirm no live-applied value reached an argv**

```bash
grep -rn "Resilience\|MaxRestarts\|MinBackoff\|MaxBackoff\|IntervalMS" internal/ffmpeg/
```

Expected: no matches.

- [ ] **Step 5: Confirm every guard is reachable**

```bash
go test ./internal/supervisor/ ./internal/engine/ -run 'Policy|Reload|Resilience|Meter' -count=1 -v | grep -c '^--- PASS'
```

Expected: at least 14 passing test functions and subtests across the two packages.

- [ ] **Step 6: Commit**

```bash
git add docs/HOT-RELOAD.md
git commit -m "docs: what a settings change costs, and what it does not

A rendering of settingsReload for operators, plus the parts that are not
covered: no overlay, geometry, bitrate, routing or transport change is applied
live; the ingest is never hot-reloaded; changing the shared SRT port interrupts
every programme rather than the one being edited; and the classification table
is checked for shape rather than proven field by field -- only the two settings
this work moved have behavioural tests."
```

---

## Self-Review

**Coverage:**

| Requirement from the brief | Where |
|---|---|
| Which settings can be applied live — enumerated | "The two lists", table 1; encoded in Task 4 |
| Which genuinely require a restart — enumerated | "The two lists", table 2; encoded in Task 4 |
| How to diff old and new config for the minimum restart set | Unchanged: the existing per-tier signature functions already compute it. This plan removes a term that should never have been in one (Task 2) and adds none. |
| What happens to a destination mid-reconnect | Dedicated section; pinned by `TestStoppingAReconnectingProcessReturnsPromptly`, `TestSetPolicyShortensAnInFlightBackoff`, `TestSetPolicyNeverLengthensAnInFlightBackoff`, `TestLoweringMaxRestartsAppliesFromTheNextExitRatherThanRetroactively` |
| Failure mode if a live-applied change is rejected by FFmpeg | Not reachable by construction, and guarded: `TestNoResilienceFieldExistsOnTheDestinationArgvSpec` plus the "no diff in internal/ffmpeg" check in Task 6 Step 3 |
| A change that silently does nothing is worse than one that restarts | Task 3 fixes an existing one; Task 4 prevents the next; Task 5 makes both visible; the revival path in Task 2 chooses a restart over a no-op |

**Honesty about how invasive this is.** Task 1 changes the read path of the most safety-critical loop in the codebase — `supervise` now reads a mutable policy on four lines that were previously immutable-by-construction. That is a real risk and it is why Task 1 is standalone, lands first, and ends with the whole supervisor suite green under `-race`. Task 5 touches six call sites across two files for telemetry alone; if it has to be dropped for schedule, Tasks 1–4 stand without it.

**What could go wrong.**
- `waitBackoff` is a new loop. A `retune` that arrives repeatedly (many concurrent saves) spins the loop, but each iteration re-arms a timer and the loop only exits on the deadline or ctx, so it cannot busy-wait; worst case it re-arms once per save.
- Removing three terms from `destSpec` shortens every destination's hash input. Existing specs are compared only within one process lifetime, so no stored value goes stale, but every destination *is* re-hashed on the first reconcile after deploy — and because the old and new hashes differ, **the first reconcile after upgrading restarts every destination once.** That is unavoidable when a signature's inputs change and it is the same cost every previous signature change paid. It must be in the release notes.
- The revival path calls `Restart` inside `startDestinations`, which is inside `reconcileOutputs`, which is inside `Reconcile`. It runs without `e.mu` and is bounded by `stopTimeout`, but a reconcile that revives eight failed destinations can take up to 96 seconds in the worst case. In practice `Stop` on a failed process returns immediately (`done` is closed), so the real cost is the spawn.
- `TestEveryReloadRuleNamesAFunctionThatExists` matches a function name anywhere in a set of packages. A rule naming `Reconcile` matches many. It catches an absent function, not a wrong one.

**Placeholder scan:** no TBD, no TODO, no "add appropriate error handling". Every code step carries real code. Three steps direct the implementer to read a specific file before choosing (Task 2 Step 2 `testLogger`, Task 4 Step 4 the remaining automod leaves, Task 5 Step 6 the settings response shape) and each says exactly which file to read and what decides the answer.
