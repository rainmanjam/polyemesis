# Engineering and reliability recommendations

Reviewed 2026-08-09. Scope: the production failover path, its real-FFmpeg
acceptance coverage, CI wiring, and the uncalled upgrade subsystem. These are
ordered by ability to catch the recently observed backwards-DTS regression, not
by unit-test count.

## 1. Make failover a repeated, fault-injected media measurement gate

**What to do.** Parameterise `scripts/acceptance-failover.sh` with a switch
iteration count and a deterministic set of teardown delays/load perturbations.
For each primary -> fallback -> primary seam, preserve the output and fail on
any backwards video DTS. Run a 12-iteration version on every change that touches
`internal/engine`, `internal/ffmpeg`, relay/supervisor code, or the failover
scripts; run a longer (for example 50-iteration) scheduled reliability job.
Keep the one-iteration suite as a fast smoke test, but do not let it be the
merge evidence for timing changes.

**Why.** The current suite has the right oracle: it reads real output packet
DTS with `ffprobe` and fails on a backwards step
(`scripts/acceptance-failover.sh:576-625`). However, the CI matrix invokes each
suite once (`.github/workflows/ci.yml:532-566`), while the reverted offset change
was 3/12 failures and the prior behaviour 0/12. A single green run therefore
cannot distinguish those outcomes. Issue #126 says the mechanism remains
unknown, which makes detection by repeated observation more important than a
new timing theory.

**Impact:** high. **Effort:** medium.

## 2. Correlate every DTS seam with the selector lifecycle that created it

**What to do.** Assign each feed a generation ID and emit one structured seam
record containing: old/new source, decision time, outgoing stop result and
duration, whether the stop timed out, replacement spawn time, offset passed,
and first-packet/progress time. Expose the last N records in status and write
them beside the acceptance artifact. Have the DTS parser report the nearest
generation ID, rather than only packet ordinal and delta.

**Why.** The existing failure report usefully prints the packet location and
step size (`scripts/acceptance-failover.sh:591-616`), but it cannot say whether
the step follows a Stop timeout, a delayed first packet, or overlapping writers.
Those are real, distinct possibilities: teardown may block for `stopTimeout`
and then records that the outgoing feed may still be writing
(`internal/engine/selector.go:1488-1519`). The acceptance driver reduces live
state to four fields (`scripts/acceptance_failover_driver.go:267-293`), none of
which identify a seam. This turns Issue #126 from intermittent observations into
comparable evidence.

**Impact:** high. **Effort:** medium.

## 3. Fence the selector relay so two feed generations cannot publish together

**What to do.** Change the hand-off contract so a replacement may publish only
after the previous writer is conclusively reaped or its relay ingress has been
fenced. Prefer a generation-specific ingress/proxy that accepts packets only
from the active generation; then atomically promote the new generation and
reject the old one. Surface a failed fence as a degraded failover event and add
an acceptance case that forces `Process.Stop` past its deadline.

**Why.** Current code intentionally starts the replacement after an unsuccessful
Stop, while warning that both processes may write into the same selector input
(`internal/engine/selector.go:1495-1518`). That is explicitly a corrupted
timeline risk, not merely a cosmetic warning, and it is a credible common cause
for the timing-sensitive backwards DTS in Issue #126. Logging a known concurrent
writer does not protect a stream-copied destination.

**Impact:** high. **Effort:** large.

## 4. Stop accepting source-text guards as tests; require a compiled behaviour path

**What to do.** Delete any test that reads production `.go` files and searches
for a line/order, and reject new ones in review. Add a lightweight test-policy
check only to flag such tests for review, not as a substitute for behaviour.
For every invariant involving FFmpeg or supervision, require either a test that
starts the production construction path and inspects the spawned argv, or a
real-media assertion; timestamp changes require the latter.

**Why.** The recent reverted change was protected by two source-text selectors
and an unused helper, so all passed while the real behaviour regressed. This is
the exact failure described by Issue #107. The repository has already documented
the same anti-pattern: a test once guarded an invariant by counting source text,
then was replaced with a structural test (`docs/notes/core-review-2026-08-08.md:113-118`).
The current failover script itself states why a unit test cannot establish real
timestamps (`scripts/acceptance-failover.sh:9-23`).

**Impact:** high. **Effort:** small.

## 5. Move the feed-copy command builder into `internal/ffmpeg` and test its live wiring once

**What to do.** Move `relayFeedArgs` into `internal/ffmpeg` beside the other
command builders, add table tests for its argument shape, and add one engine
integration test that switches a real source and verifies the actual child argv
contains the offset supplied by the selector. Keep that test explicitly below
the repeated-DTS gate in the test hierarchy: it proves wiring, not continuity.

**Why.** `selector.go` acknowledges this is the only FFmpeg command assembled
outside the builder package (`internal/engine/selector.go:1529-1539`), and it is
where the offset is serialized (`internal/engine/selector.go:1540-1553`). The
seam helper that was tested after the fact was not a production call; a test of
the process construction boundary would have caught that disconnect. It will
not solve Issue #126 by itself, but it removes an easy way for the intended
mitigation to become inert again.

**Impact:** medium. **Effort:** medium.

## 6. Replace check-count floors with named required evidence for failover

**What to do.** Make the failover driver emit a machine-readable result for
each required property: all intended source transitions observed, destination
restart delta zero, output contains the expected source markers, output is
finalised/decodable, and DTS result for every iteration. Have CI validate that
schema and upload it with the media and seam records. Remove `EXPECTED_CHECKS`
as the primary completeness signal; retain it only as a secondary smoke guard if
useful.

**Why.** The current floor only says that 24 calls to `ok`/`bad` occurred
(`scripts/acceptance-failover.sh:887-915`). It cannot establish that the expected
five seam types occurred, nor that a skipped branch actually exercised a switch.
The suite already recognises this distinction in prose—bytes, restart count, and
DTS measure different promises (`scripts/acceptance-failover.sh:18-23` and
`:552-580`). Naming those as required artifacts makes an incomplete test fail
for the missing evidence rather than pass on the right count.

**Impact:** medium. **Effort:** medium.

## 7. Delete `internal/upgrade` until it has a production owner, or wire it end to end

**What to do.** Make a binary choice this release. If in-process upgrade is not
an announced feature, delete `internal/upgrade` and its test-only surface. If it
is a feature, wire it from the authenticated API through the on-air refusal and
installation-plan decision to a deliberately tested systemd hand-off, then add
an external integration test that proves the calling path rather than only file
swap mechanics.

**Why.** The package explicitly says a caller supplies the off-air decision
(`internal/upgrade/upgrade.go:11-13`), yet a repository-wide Go import search
finds no production import of `internal/upgrade`. Startup goes directly from
configuration to FFmpeg detection (`cmd/polyemesis/main.go:143-180`), and the
API's update code reports availability/on-air state (`internal/api/handlers.go:285-315`)
rather than invoking the package. Four rounds of excellent tests on an uncalled
subsystem produce false confidence and consume review capacity that the
failover path needs.

**Impact:** medium. **Effort:** small to delete; large to ship correctly.
