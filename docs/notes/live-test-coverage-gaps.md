# What is still only tested offline

Written 2026-08-13, after the live multistream suite (#141) closed and, in
doing so, found two defects nothing else had: a stream key written to
`server.log` on every retry (#310) and a shipped Kick preset that could not
publish (#312).

## The headline

polyemesis has 17 acceptance suites. **Exactly one of them talks to a real
external service**: `scripts/acceptance-multistream.sh`. Every other suite runs
entirely against loopback, fakes, or files.

That one suite, on its first live runs, found two real defects — one a
credential leak, one a shipped-and-wrong default. Neither was reachable by unit
tests, because on each side of the boundary both halves were individually
correct. What was wrong was the composition, and only a real far end refuses a
bad composition.

Everything below is a place where the same thing could be true and nobody
would know.

## Ranked

Ranked by (has a real external far end) × (size) × (how quietly it fails).

### 1. Chat — 10,063 lines, 8 external hosts, no acceptance suite

The package's own doc comment is the clearest statement of the gap:

> On testing: the IRC transport cannot be exercised against Twitch offline, and
> this package does not pretend otherwise — there is no test here that proves
> polyemesis can talk to irc.chat.twitch.tv. […] The parts that need a socket to
> a real platform are verified by connecting to one.

That last sentence describes a practice, and nothing currently performs it. The
4,494 lines of chat tests cover the parser, the handshake over an in-memory
pipe, and the Hub's failure isolation — all real, none of it a socket.

Untested against reality: the Twitch IRC TLS handshake and `PASS`/`NICK`
sequence, PING/PONG keepalive over hours, Kick webhook signature verification
against `api.kick.com`'s published public key, YouTube polling and its quota
pacer.

**Cheapest useful version needs no credentials.** Connect to
`irc.chat.twitch.tv:6697` with a deliberately wrong token and assert the
specific refusal. That proves DNS, TLS with the right SNI, the IRC line
protocol and the failure path — everything except a valid login. It is the same
shape as the multistream suite's "a wrong key is refused" check (63c61a4), so
the technique is already established here.

### 2. OAuth — 10,693 lines, 19 external hosts, no acceptance suite

The largest external-host count in the codebase. Token refresh is the classic
silent failure: correct on day one, broken at hour four when the first token
expires, and invisible until then.

It also populates destination URLs and keys automatically — the exact field
whose hand-typed equivalent produced #312. A composition bug here would look
identical and be harder to see, because no operator typed anything.

### 3. Transcribe — 7,661 lines, 2 external hosts

### 4. Automod — 2,454 lines, 2 external hosts

### 5. Hooks — 2,150 lines, outbound webhooks — **CLOSED**

Cheapest of all to test: it needs a listener we control, not a platform
account. No credentials, no rate limits, no account state.

Closed by `scripts/acceptance-hooks.sh`. The prediction above was right about
the cost and wrong about the reason it was worth doing. The interesting gap
turned out not to be "no socket" but "no `*url.Error`": the package's unit
tests all pass a `WithDoer` that returns a hand-built response, so the one
string that has ever leaked a webhook credential in this codebase — net/http's
`Post "https://host/PATH-IS-THE-SECRET": dial tcp ...` — was a string no test
had ever constructed, and the whole three-pass scrub in `dispatch.go` existed
for an input nothing had fed it. `WithSleep` had the same shape: the retry test
stubs the wait out, so `backoffBase` could have been a nanosecond.

31 checks, 29 of which need nothing outside the machine, wired into the `go`
job in `ci.yml`. No defect found — every scrub, both layers of it, holds
against real transport errors, and the backoff is the documented 1s then 2s on
a wall clock.

### Not in this list

`clipper` (5,609 lines) and `playout` (3,289) have **no** external hosts, so
unit and acceptance tests can genuinely cover them. Their gaps, if any, are
ordinary coverage gaps rather than this class.

## What blocks the top two today

Nothing is connected on the OVH server:

    platform_accounts  0 rows
    platform_creds     0 rows
    automod_creds      0 rows
    chat_messages      0 rows

So the full chat and OAuth suites need an account connected first. The
credential-free Twitch IRC refusal check does not.

## Recommended order

1. ~~**Chat, refusal-only**~~ — done, `scripts/acceptance-chat.sh`.
2. ~~**Hooks**~~ — done, `scripts/acceptance-hooks.sh`.
3. **Chat, authenticated** and **OAuth refresh** — once an account is connected.
4. **Transcribe** and **Automod**, still untouched, and both still need a
   credential before the interesting half of either is reachable.
