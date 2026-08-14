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

### 2. OAuth — 10,693 lines, 19 external hosts — **covered, with one gap named below**

The largest external-host count in the codebase. Token refresh is the classic
silent failure: correct on day one, broken at hour four when the first token
expires, and invisible until then.

It also populates destination URLs and keys automatically — the exact field
whose hand-typed equivalent produced #312. A composition bug here would look
identical and be harder to see, because no operator typed anything.

`scripts/acceptance-oauth.sh` now covers this, and the split is worth recording
because it was not what this note predicted. The note assumed OAuth had to wait
for a connected account. Most of it did not: **twenty-eight of its forty-six
checks need no credential**, because every provider's OAuth surface is public.
Discovery documents, authorization and token endpoints, advertised grant types
and PKCE methods, and the Graph API version Facebook actually serves are all
fetchable by anybody and comparable against what `internal/oauth` hardcodes.

The gap that remains is narrow and real: **nothing proves a refresh SUCCEEDS.**
Step 8 does exactly that and skips without `POLY_OAUTH_<PLATFORM>_*`, so until
an account is connected the hour-four failure is still only bounded from one
side — the suite proves every token endpoint is present and refuses a bad
grant, which is not the same claim.

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

That framing turned out to understate what was reachable. Both suites were
built anyway, and between them **forty-three checks run with no credential at
all** — because a platform's refusals, its certificate chain, its published
public key and its whole OAuth surface are things anyone can ask for. The rule
this yielded, worth carrying to the three packages below: *ask what the far end
will tell a stranger before assuming the test needs an account.*

## Recommended order

1. ~~**Chat, refusal-only**~~ — done, `scripts/acceptance-chat.sh` (#316).
2. ~~**OAuth, credential-free**~~ — done, `scripts/acceptance-oauth.sh` (#322).
   Needed no credential at all: a provider's OAuth surface is public.
3. ~~**Automod**~~ — done. Needed none either, for a different reason: the
   fail-open contract is about refusal, and an unauthenticated request to a real
   API is a real refusal.
4. ~~**Transcribe**~~ — done, though the ranking was wrong about it. It is a
   LOCAL package; see the correction above.
5. ~~**Hooks**~~ — done. No defect found, and the reason it was worth doing
   turned out not to be "no socket" but "no `*url.Error`".
6. **Chat, authenticated** and **OAuth refresh** — still open, and the only ones
   that need an account connected. Both suites already have the step written and
   skipping.

## A sixth kind of gap: a suite that runs nowhere

`scripts/acceptance-obs-multitrack.sh` was not on the list above, because it
already existed and had already found something — that OBS 30.2.3 sends no
multitrack audio at all, which six documents now depend on. It ran nowhere. A
suite that exists and is never executed is indistinguishable from one that was
never written, except that it reads as covered.

It is scheduled now (`.github/workflows/obs-multitrack.yml`), and getting it
there surfaced two defects of its own, both of the kind this note keeps finding
— composition, not logic:

- **The observer had no floor.** The suite asserts OBS sends exactly ONE audio
  track. Below FFmpeg 7.1 multitrack FLV does not demux, so on Ubuntu's stock
  6.1.1 it would report one track whatever OBS sent — a green run on a host that
  could not have produced any other answer. Both halves were correct: the
  assertion was right, the FFmpeg was a supported version. The composition was
  vacuous. It now refuses below 7.1, measured with a round trip.
- **It was the last host suite not using the shared teardown.** Running it twice
  back to back — which is the standard here, and the only reason this was seen —
  bound `:1935` while the previous run still held it, and reported "the RTMP
  listener never bound": a teardown bug wearing the costume of the product
  failure the suite exists to detect. `lib-cleanup.sh` was written for exactly
  this symptom and twelve suites had adopted it.

The rule from the top of this note holds in a new direction. *Ask what the far
end will tell a stranger* found coverage nobody expected; this one adds: **ask
whether the thing doing the asking could have heard a different answer.**

WHAT THE FIVE FOUND, since it was not what this note predicted. Two credential
leaks (#310 in chat, and automod's endpoint in its own error text) sat in the
same place: not in the request, not in the response, but in what an ERROR was
allowed to say afterwards. `*url.Error` carries the full URL, and a test that
stubs the HTTP client can never produce one — which is why five packages' worth
of unit tests had never seen the string their scrubs existed for.

Transcribe's two were a different shape and worth naming separately: a magic
constant spelled the wrong way round, and a checksum compared against the wrong
header. Both were invisible offline because the fixtures were generated from the
same wrong constants. A closed loop that agrees with itself.
