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

### 3. Transcribe — 7,661 lines, 2 external hosts — DONE, and the ranking was wrong about why

`scripts/acceptance-transcribe.sh` covers this now, but the entry above was
misleading and the correction is worth keeping.

The "2 external hosts" came from grepping for URLs. One of the two is a
`github.com` link inside an error message that is never fetched. Transcription
is **local** — a whisper.cpp binary and an ffmpeg command line — so the ranking
metric, "has a real external far end", scored this package for the wrong
reason and nearly produced a network-shaped suite for a local-shaped package.

The untested surface was real, just a different shape: ten hardcoded claims
about a Hugging Face repo (a name per model, from which a URL is composed, and
a byte count that `VerifyModelFile` gates on), plus two argument builders that
were pinned by exhaustive unit tests and had never been shown to ffmpeg or
whisper.cpp.

On its first run it found three defects, all in the #310/#312 class — each half
individually correct, the composition wrong:

- `ggmlMagic` was byte-reversed, so `looksLikeGGML` rejected every genuine
  model: no download could succeed and `InstalledModels` hid hand-copied
  models. Invisible offline because every test fixture is built with
  `copy(buf, ggmlMagic)` — the same wrong constant.
- The SHA-256 check compared against Hugging Face's **xetHash** rather than the
  content hash, because the checksum is on the redirect and the CDN it points
  at now sets a different-but-same-shaped hash. Every download was rejected.
- whisper-cli exits **0** after refusing an argument list. `worker.go` treats a
  zero exit as success, finds no JSON, falls back to stdout segments it never
  got, and returns an empty transcript with a nil error. **Not fixed** — see
  the PR.

The lesson for the entries below: "external hosts" counts URLs, not
dependencies. Read the package before choosing the shape of its suite.

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

## Recommended order

1. **Chat, refusal-only** — no credentials, largest untested surface, technique
   already proven in this repo.
2. **Hooks** — no credentials, self-contained.
3. **Chat, authenticated** and **OAuth refresh** — once an account is connected.
