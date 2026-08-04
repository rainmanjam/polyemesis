# The second sweep: knobs no guard can see

**Status:** surveyed 2026-07-30. Every item rated worth doing has since
shipped — chat retention, then the Whisper default model, then the chat history
ring and the alert retry budget. The Low rows are recorded with a verdict each
and remain deliberately unexposed.

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
| `relay.WithListenIP` | IPv4 loopback | Nothing, today — see [Why the relay pair must stay unwired](#why-the-relay-pair-must-stay-unwired) | **Do not wire.** I ranked this highest and was wrong |
| `relay.WithAdvertiseIP` | derived from the bind | Only meaningful alongside a non-loopback bind | **Do not wire.** Same |
| `transcribe.WithDefaultModel` | hardware-derived | An install-wide Whisper model choice. The API accepts a per-job `Model` ([library.go:908](../../internal/api/library.go)) and **the UI never sends it**, so model choice — the main speed/accuracy/RAM tradeoff in transcription — is unreachable by clicking and undefaultable | ✅ **SHIPPED.** Live via `settings.postprod.whisperModel`, offered as a picker of installed models with "Automatic" first. The per-JOB override is still unreachable — the library page calls `submitRecordingJob(id, kind)` with no body, so `model`, `language`, `translate`, `threads` and `formats` are all accepted by the API and never sent. That is a feature request, not an unreachable knob: the install-wide choice is reachable now |
| `chat.WithHistory` | ~~500 messages~~ `settings.chat.historyMessages` | The in-memory ring a late-joining browser reads before falling back to the database. Pairs directly with retention: that sets how deep a user card goes, this sets how much arrives without a query | ✅ **SHIPPED.** Live via `Hub.SetHistory`, bounded 1–50,000 |
| `alerts.WithRetry` | ~~package default~~ `settings.alerts.retryAttempts` | The retry budget for a webhook that is down. Was one fixed answer for "how hard do we chase a dead endpoint" | ✅ **SHIPPED.** Attempts only; the backoff curve stays unexposed |
| `chat.WithSendTimeout` | 15s | How long one slow platform may hold up the fan-out reply that tells the operator the other three worked | **Do not wire.** 15s is defensible, and shortening it has a harm the first pass missed: the timeout cancels OUR wait, not the platform's processing, so a shorter one converts "slow" into "reported failed but actually delivered" — and the operator's fix for that is to re-send, which duplicates the message in their live chat. Nothing connects the setting to the duplicates |
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

## Why the relay pair must stay unwired

This section is a correction. The first draft of this document called the relay
pair the highest-value item here, on the reasoning that IPv4-loopback-only is a
ceiling on what polyemesis can be deployed as. That reasoning does not survive
reading the code, and the conclusion inverts.

**The hub does not know who sent a datagram.** `run` calls `h.conn.Read(buf)`,
not `ReadFrom`: there is no source address to check and nothing checks one. The
socket reads, and whatever arrives is fanned out to every subscriber.

That is entirely safe on loopback, where the only writer is polyemesis's own
ingest. Bind it wider and it becomes an **unauthenticated injection path into the
live programme** — anything that can reach that UDP port lands in every
destination, the recorder, and the meters, with no way for an operator to see
where it came from.

**And it buys nothing today.** Every subscriber is a local FFmpeg child process:

    recorder   preview   meters   playout   silence   selector

each handed a `udp://127.0.0.1:port` URL and spawned on this host. The
cross-host entry point that would justify a wider bind is `SubscribeAddr`, and
that has **no production caller either** — so the remote-consumer case the option
was written for does not exist anywhere in the product.

Wiring this would therefore add a setting, a validator, a UI control and a
drift-guard entry whose principal effect is to let an operator open a hole in
their own broadcast, in exchange for a capability nothing uses. The option should
stay exactly where it is: available to a future caller that has a remote consumer
AND a source-validation story, and reachable by nobody until then.

If that future arrives, the order is: add source filtering to `run` first, then
the bind option, then the setting. Not the reverse.

## Recommendation

**All three worth doing are now done.** `transcribe.WithDefaultModel` shipped
first; `chat.WithHistory` and `alerts.WithRetry` followed as
`settings.chat.historyMessages` and `settings.alerts.retryAttempts`.

1. ✅ **`transcribe.WithDefaultModel`.** Model choice is the transcription
   decision, the API already accepted it per job, and there was no way to
   express a default or pick one from the UI at all.
2. ✅ **`chat.WithHistory`.** Applied live by resizing the ring rather than at
   the next restart, for the reason retention was: a setting that needs a
   restart is one an operator changes, sees nothing happen, and changes again.
3. ✅ **`alerts.WithRetry`.** The attempt count only. The backoff curve
   underneath was chosen against measured behaviour and no failure story argues
   for moving it, which is the same test the Low rows below fail.

Building the last two turned up something this document had not looked for: the
comment on `db.ChatSettings` cited a guard, `TestChatDefaultsMatchTheChatPackage`,
that kept the package default and the settings default in step. **No such test
existed, in any package, under any name.** Exposing a knob is the moment that
stops being free — until then there is one number, and afterwards there are two
that can disagree. Both pairs are now genuinely pinned, in `internal/api`
because `internal/db` cannot import either package without a cycle.

`chat.WithSendTimeout` was the closest call, and it went the same way for a
reason worth writing down. By this document's own test it looked like a
candidate: the delay IS visible to an operator — a slow platform holds up the
reply telling them the other three worked — and that is exactly the "gave them
a reason to want it different" argument that carried chat retention.

**What that test misses is whether the operator can predict what turning it
does.** `sendTimeout` bounds the context handed to the adapter, so it cancels
the request polyemesis made; it does not cancel what the platform has already
accepted. Shorten it and a platform that would have succeeded at seven seconds
is reported as failed at five — while the message goes out. The operator sees a
failure, re-sends, and their live chat carries the message twice. Nothing in the
setting, its label, or the failure says that is what happened.

So the rule this row adds to the survey: **a knob whose effect the operator can
see is not the same as a knob whose effect they can predict.** Visibility was
the right test for retention because a longer scrollback does exactly what it
says. It is the wrong test here.

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
