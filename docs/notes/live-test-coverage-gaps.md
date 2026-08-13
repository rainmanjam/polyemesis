# What is still only tested offline

Written 2026-08-13, after the live multistream suite (#141) closed and, in
doing so, found two defects nothing else had: a stream key written to
`server.log` on every retry (#310) and a shipped Kick preset that could not
publish (#312).

## The headline

polyemesis has 17 acceptance suites. **Exactly one of them talks to a real
external service**: `scripts/acceptance-multistream.sh`. Every other suite runs
entirely against loopback, fakes, or files.

> Since written: `acceptance-chat.sh` (#316) and `acceptance-automod.sh` make
> three. Each of the three found a defect on its first live run — a stream key
> in `server.log` (#310), a shipped Kick preset that could not publish (#312),
> and the model endpoint's own key in `server.log` and in the operator's spend
> panel. The hit rate is now three for three, which is the argument for the
> remaining items rather than a coincidence.

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

### 4. Automod — 2,454 lines, 0 external hosts — **covered, and the count above was wrong**

`scripts/acceptance-automod.sh` now covers this. Two things it settled that this
ranking had assumed:

**The "2 external hosts" were not hosts.** They are the string literals
`"http://"` and `"https://"` in `history.go`'s link counter, which exist to
count links inside a chat message. `internal/automod` has no compiled-in
endpoint at all: the model endpoint is a settings field, and the deployment
`model.go` names first is a local one. Whatever produced these counts matched
URL-shaped literals, so every figure in this document is an upper bound rather
than a measurement — treat the chat and OAuth numbers the same way.

**It found a credential leak anyway**, which is the third one a live suite has
found here. `net/http` puts the request URL verbatim into `*url.Error`; the
endpoint is free text an operator pastes, and `internal/api/redact.go` already
masks it out of `GET /settings` because a proxied inference endpoint most often
arrives carrying `?api_key=sk-...`. That reasoning had been applied to the
settings blob and nowhere else, so a failed call put the key in
`ModelStats.LastError` and in `server.log` — once per message, with no backoff,
because failing open means trying again on the next one. #310's shape exactly.
No unit test could reach it: both halves were individually correct.

### 5. Hooks — 2,150 lines, outbound webhooks

Cheapest of all to test: it needs a listener we control, not a platform
account. No credentials, no rate limits, no account state.

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

1. ~~**Chat, refusal-only**~~ — done, #316.
2. ~~**Automod**~~ — done. Needed no credentials either, for the same reason:
   what the fail-open contract is about is refusal, and an unauthenticated
   request to a real API is a real refusal.
3. **Hooks** — no credentials, self-contained.
4. **Chat, authenticated** and **OAuth refresh** — once an account is connected.

Both suites so far found their defect in the same place: not in the request, and
not in the response, but in what an error was allowed to say afterwards. Worth
looking there first in the remaining three.
