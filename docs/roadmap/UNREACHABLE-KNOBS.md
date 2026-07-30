# The second sweep: knobs no guard can see

**Status:** surveyed 2026-07-30. One item shipped (chat retention); the rest
recorded with a verdict each.

[UNREACHABLE-FEATURES](UNREACHABLE-FEATURES.md) found three `db.Rendition` fields
that were built, validated, compiled into FFmpeg arguments — and absent from
`ui/src`. The drift guards that came out of it now cover that shape completely.
This document is about the shape they **cannot** cover.

## Why the guards miss these

The guards compare two lists: Go's fields against the UI's type names. That
catches a setting that exists and is not reachable.

It cannot catch a knob that **is not a setting in the first place**. Every
package here takes functional options — `chat.WithRetention`, `relay.WithListenIP`
— and if production never calls one, there is no field for a guard to compare
against. The option is real, documented, tested, and dead.

Chat retention was exactly that. `chat.WithRetention` existed from the first
commit and `main.go` never called it, so every install ran a hard-coded two hours
with no way to change it short of recompiling. Nothing was wrong; nothing was
watching either.

## Method

Every `func With*` in `internal/`, counted against its non-test call sites:

```console
$ for opt in $(grep -rhoE "^func (With[A-Za-z]+)" internal/ | sed 's/func //'); do
    echo "$opt $(grep -rn "\b$opt(" internal/ cmd/ | grep -v _test | wc -l)"
  done | sort -k2 -n
```

Thirty-four came back with zero production callers. Most are **deliberate test
seams** and not gaps — `WithClock`, `WithSleep`, `WithDoer`, `WithProber`,
`WithRunner`, `WithBackoff`, `WithNiceTools`, the `*GovernorClock`/`*Tick` pairs.
A seam with no production caller is the seam working as designed.

What remains is below.

## The table

| Knob | Fixed at | What it would unlock | Verdict |
|---|---|---|---|
| `relay.WithListenIP` | IPv4 loopback | **IPv6 consumers, and a relay spanning hosts.** Every call site is `relay.New(log, 0)`, so the wildcard/dual-stack path the option documents has never run | **Highest value here.** A capability, not a tuning knob |
| `relay.WithAdvertiseIP` | derived from the bind | Correct addresses when the hub binds a wildcard or sits behind a different route than it listens on | Pairs with the above; ship together or neither |
| `transcribe.WithDefaultModel` | hardware-derived | An install-wide Whisper model choice. The API accepts a per-job `Model` ([library.go:908](../../internal/api/library.go)) and **the UI never sends it**, so model choice — the main speed/accuracy/RAM tradeoff in transcription — is unreachable by clicking and undefaultable | **High.** Small change, direct operator value |
| `chat.WithHistory` | 500 messages | The in-memory ring a late-joining browser reads before falling back to the database. Pairs directly with the retention work just shipped: retention sets how deep a user card goes, this sets how much arrives without a query | **Medium.** Natural companion to `settings.chat` |
| `alerts.WithRetry` | package default | The retry budget for a webhook that is down. Currently one fixed answer for "how hard do we chase a dead endpoint" | **Medium.** The one alerts knob with a real failure story behind it |
| `chat.WithSendTimeout` | 15s | How long one slow platform may hold up the fan-out reply that tells the operator the other three worked | **Low-medium.** 15s is defensible; only a badly-behaved platform makes it wrong |
| `alerts.WithFlushInterval` | 500ms | How often the pending alert set is examined | **Low.** Tuning without a failure story |
| `alerts.WithRulesTTL` | 5s | Rule-list cache lifetime between database reads | **Low.** Same |
| `chat.WithFlush` | 1s / 64 | SQLite write pacing for chat | **Low.** Already sized against chat's burst shape |
| `jobs.WithProgressInterval` | package default | How often a worker's progress reaches the database | **Low.** Cosmetic; affects a progress bar's smoothness |
| `scheduler.WithTick` | package default | Schedule sweep interval | **Low.** A schedule is minute-resolution anyway |

## What the survey did NOT find

**No `db.Settings` field is unreachable.** Every JSON name in the settings tree
appears somewhere in `ui/src` — checked across the whole directory rather than
just `SettingsPage.tsx`, because `postProd` is edited through the jobs policy
endpoint and a narrower search reports it as missing when it is not.

That is the drift guard having done its job, and it is worth stating plainly:
the class of bug UNREACHABLE-FEATURES documented is now closed, and this
document exists because closing it revealed a different one underneath.

## Recommendation

Two are worth doing and the rest are not, on current evidence:

1. **The relay pair.** IPv4-loopback-only is a real ceiling on what polyemesis
   can be deployed as, and the code to lift it is already written and tested —
   only the wiring is missing. Anyone who wants IPv6 or a second host hits this
   and has no way past it.
2. **`transcribe.WithDefaultModel`.** Model choice is the transcription decision,
   the API already accepts it per job, and there is no way to express a default
   or to pick one from the UI at all.

The `Low` rows should be left alone. Each is one fixed number chosen against a
measured shape, and exposing a knob nobody has a reason to turn adds a setting to
validate, document, drift-guard and support in exchange for nothing. The argument
for chat retention was never "it is configurable in code" — it was that the
moderator's user card made the number visible to an operator, and gave them a
reason to want it different. None of the Low rows has that.

## See also

- [UNREACHABLE-FEATURES](UNREACHABLE-FEATURES.md) — the first sweep, and the
  guards that came out of it
- [CHAT-MODERATION](CHAT-MODERATION.md) — why chat retention stopped being a
  constant
