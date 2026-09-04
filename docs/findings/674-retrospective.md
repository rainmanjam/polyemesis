# #674 — what would have found this faster

Measured from the session log: first mention 2026-08-27T06:31, still open
2026-09-03T03:43. **34 full `acceptance-docker` runs on the last two days
alone** (~12 min each, ~6.8 h of pure run time), 3,164 shell calls, 34
revert/restore operations.

Ordered by hours they would have saved.

## 1. The dump never printed per-stream packet counts

`Input stream #0:2 (audio): N packets read` is the ONE line separating

  - "no audio reached the reader"  (fix at the writer), from
  - "audio reached it, the graph failed"  (fix at the reader)

Two faults, opposite fixes, identical outward symptom: "published nothing".
This bug was misdiagnosed **twice** for want of that line, and each
misdiagnosis cost a wrong fix plus a 12-minute run to disprove it. It prints at
the default loglevel and needed no instrumentation at all.

**Rule:** when two candidate causes present identically, find the cheapest
number that separates them and print it *before* theorising. A failure dump
should answer "which half?", not merely "it failed".

## 2. A 12-minute loop was the primary instrument for six hours

An 8-second unit reproduction (`internal/rtmpserver/ingest_remux_test.go`)
existed but was written late, and then could not run: `ffmpegMajor` cut
"n8.1.2-34-g9b6c8969e0" at the first "." and fataled on every runner.

**Rule:** the first response to "this takes 12 minutes to test" is to build the
30-second reproduction, not to run it 34 times. Budget the reproduction
against the loop: at 3 runs it has already paid for itself.

## 3. Local FFmpeg was 9.0.1; the bug only exists on 8.1.2

Every local run silently SKIPPED the one test that reproduces the defect. The
version asymmetry was discovered late and explains why local green meant
nothing.

**Rule:** when a defect is version-dependent, pin the dev loop to the shipped
version (run it in the container) before starting.

## 4. Guards that printed "ok" without gating

At least one run reported `gated ok: trace enabled ... three extraction blocks
present` when the file contained none of it: the edit had failed and `set -e`
did not abort as assumed. An empty output section reads identically to "looked,
found nothing wrong".

**Rule:** a gate must assert the END STATE of the artefact, and be watched to
fail once. Same discipline the repo already applies to skips and vacuous checks.

## 5. My own instrumentation masked the fix

`guardSilentPublish` restarted the destination every 30s, so it never settled.
The interleave fix's real effect (3 -> 1 -> 0 unresolved audio streams) was
invisible until the guard was removed. It also broke `acceptance-failover`.

**Rule:** never leave a self-healing mechanism enabled while measuring the thing
it heals. Land diagnosis and remediation in separate runs.

## 6. Two changes shipped in one commit

The 45s probe window and the watchdog went together. When the run got worse
(2 failures -> 4) it took another cycle to attribute which one did it.

**Rule:** one variable per acceptance run. The loop is too expensive to
confound.

## 7. A subagent's flag value was nearly applied unchecked

Fable's mechanism was right and unblocked the bug. Its concrete recommendation,
`-max_interleave_delta 0`, was inverted: 0 means "buffer indefinitely", the
opposite of flushing early. Caught by reading `ffmpeg -h full`.

**Rule:** take a subagent's MECHANISM as a lead; verify every literal value
against primary documentation before it reaches a command line.

## 8. The code comment encoded the wrong model

`-flush_packets 1` carried "without this the muxer holds partial TS packets".
It flushes the AVIO buffer, NOT the interleaver, which is what was holding
them. A confident, wrong comment steered attention away for hours.

**Rule:** when a comment explains why a flag prevents X and X is happening
anyway, suspect the comment.

## 9. Stale code index

`impact()` returned "not found" for symbols added the same night, so the
mandated analysis silently degraded to text search.

**Rule:** re-index before relying on graph analysis in a long session.

## The shape of the whole miss

Every wrong theory was reasoned from real measurements. What was missing was
not rigour, it was a DISCRIMINATOR: a cheap observation whose two possible
values point at different halves of the system. Sparsity counts, byte totals,
and PID traces all described the symptom in more detail without ever splitting
the space. One line of exit statistics would have.
