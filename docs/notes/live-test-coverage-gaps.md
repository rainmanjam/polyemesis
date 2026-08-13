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

### 4. Automod — 2,454 lines, 2 external hosts

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
2. ~~**OAuth, credential-free**~~ — done, `scripts/acceptance-oauth.sh`.
3. **Hooks** — no credentials, self-contained. Now the cheapest one left.
4. **Chat, authenticated** and **OAuth refresh** — once an account is
   connected. Both suites already have the step written and skipping.
