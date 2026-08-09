# Security Policy

## Reporting a vulnerability

**Please do not open a public issue for a security problem.**

Report it through GitHub's private vulnerability reporting: go to the
[Security tab](https://github.com/rainmanjam/polyemesis/security/advisories/new)
and open a draft advisory. That keeps the report private until there is a fix.

What helps most in a report:

- the version (`polyemesis -version`) and how it is deployed — binary, Docker,
  behind a reverse proxy;
- whether the instance is exposed to the internet, a LAN, or localhost only;
- what an attacker needs to start with: no access at all, a network route to the
  ingest port, an authenticated session, a valid API token;
- a reproduction, ideally against a fresh install.

You should get an acknowledgement within a few days. If a fix is warranted it
will ship with an advisory crediting you, unless you would rather not be named.

## Supported versions

This is a young project with a single maintained line. Security fixes land on
`main` and in the next release. There are no long-term support branches.

## The threat model, stated plainly

Being explicit about this is more useful than a list of features, because most
real incidents come from a mismatch between what an operator assumed and what
the software actually promises.

### What polyemesis defends

- **The admin session.** bcrypt password hashing, a JWT in an `HttpOnly`,
  `SameSite=Lax` cookie, CSRF double-submit on every state-changing request.
- **Revocation that actually revokes.** Each user row carries a token epoch that
  every session token is signed against and every request re-checks. Changing
  the password bumps it in the same statement, so every session issued before
  the change stops working immediately — a stolen cookie does not outlive the
  password it was obtained under. The check fails closed: a token whose epoch
  cannot be established is refused rather than admitted.
- **Scoped API tokens.** A token is created `read` or `admin`, and omitting the
  choice gives `read`. A `read` token reaches `GET` and `HEAD` plus three POSTs
  that compute an answer and write nothing; anything else is refused with a 403.
  The rule is by HTTP method rather than by a list of routes, so a route added
  later is refused by construction instead of by somebody remembering to
  classify it, and the small allowlist is additive — forgetting to extend it
  denies, never permits.

  No token of either scope can manage tokens, change the password, upload or
  delete media, or complete an OAuth connect flow. Those are session-only.

  Tokens that predate scopes are `admin`, because they already could do
  everything and narrowing them on upgrade would break a running script without
  telling anyone. Revoke and re-mint to narrow one.
- **Login brute force.** Five free attempts per client address, then a delay
  doubling from 2s, capped at 5 minutes, with `Retry-After` on the 429. The cap
  and the one-hour idle reset are deliberate: an uncapped lockout is a
  denial-of-service against the operator's own server. Counters are in memory
  only, so a restart never strands you outside your own instance.
- **Secrets at rest.** Platform OAuth tokens and client secrets are encrypted
  with NaCl secretbox. API tokens are stored as hashes. TLS private keys live in
  `<dataDir>/tls/`, restricted to the account the server runs as — `0700` on the
  directory and `0600` on the keys on Linux and macOS, and an explicit DACL
  granting only that account and `SYSTEM` on Windows.

  The Windows half is stated separately because it is not the same mechanism.
  Go's `os.FileMode` is a Unix concept that the Windows syscall layer discards,
  so `os.MkdirAll(dir, 0700)` succeeds there and restricts nothing. polyemesis
  sets a **protected** DACL instead (see `internal/fsperm`), which detaches the
  directory from the inheritable permissions it would otherwise pick up from its
  parent — under the default ACL on a system drive those include `BUILTIN\Users`.
  The grant is marked inheritable so that files written into the directory later
  are covered too, which is what protects the ACME account key: `autocert`
  creates that file through its own code path, so inheritance is the only
  mechanism that can reach it.
- **Secrets in transit to you.** No stream key, client secret, API token or TLS
  private key is ever returned by an API or written to a log. Webhook URLs — which
  carry their credential in the path — are masked in every response.
- **Path confinement.** File destinations, `file://` pull sources, slate images,
  recording and clip downloads are all confined to the data directory. Paths
  that come from the database are never trusted as filesystem paths.
- **Uploads name themselves.** `POST /api/v1/media` is the one endpoint where a
  caller supplies both the bytes and something filename-shaped, so the supplied
  name is treated as a hint and thrown away: the server generates the stored
  name with a random suffix, which also means an upload cannot overwrite an
  existing one by guessing it. The separator check tests both `/` and `\` on
  every platform rather than `os.PathSeparator`, whose meaning changes with the
  build target — that exact bug once let a forward slash through on Windows.
  Uploading and deleting media are session-only, so an API token cannot write
  bytes to the disk; a token can still `GET /api/v1/media` to list what is
  stored. An oversized, empty or cancelled upload leaves nothing behind.

  This paragraph claimed the session requirement before anything enforced it.
  The routes sat in the ordinary authenticated group, and the CSRF middleware
  passes token-authenticated requests through on purpose — nothing attaches an
  `Authorization` header on its own, so there is no cross-site forgery left to
  stop — which meant a token-only `POST` stored a file for as long as the claim
  went unchallenged. The routes now sit in a session-only router group, so the
  route table states the rule instead of the prose doing it alone.
- **Inbound webhooks.** Kick's chat webhook is verified against Kick's published
  RSA public key before its body is read, and the handler refuses the request
  outright when no verifier is configured. An unverifiable webhook endpoint is
  an unauthenticated write path, so it fails closed rather than open.
- **Browser-side hardening.** Responses carry a CSP, `X-Frame-Options: DENY`,
  `nosniff`, `Referrer-Policy: no-referrer`, and a `Permissions-Policy`
  disabling camera, microphone and geolocation.

  The one exception is `/watch`, and only when you have turned on
  `allowCrossOrigin` for playout: a page whose purpose is to sit in an iframe on
  somebody else's site cannot also refuse to be framed. That page alone drops
  `X-Frame-Options` and opens `frame-ancestors`; the admin console keeps the
  strict policy. Leaving `allowCrossOrigin` off keeps the exception closed.

### What it does NOT defend

These are design decisions, not oversights. Read them as operating instructions.

- **There is one user.** No multi-user model, no roles, no per-destination
  permissions. **Access to the UI is full control of the server's streaming** —
  and, through file destinations and expert mode, meaningful control of the
  machine.
- **Expert mode is arbitrary FFmpeg arguments.** It exists because operators
  need it, it is guarded by a confirmation showing exactly what will be spliced
  in, and it is still a way to make FFmpeg do things. Treat the ability to reach
  it as equivalent to shell access.
- **RTMP ingest is only as protected as its stream key.** Both protocols are now
  authenticated by construction — the publish token *is* the address, over SRT as
  `streamid` and over RTMP as the stream key in the URL path, so a publisher that
  cannot present a valid one is refused. The difference that remains is the wire:
  SRT can additionally encrypt with a passphrase, RTMP cannot unless you put it
  behind RTMPS. Subscribing to the RTMP listener is restricted to loopback, so a
  publish credential never doubles as a viewing one.
- **Nothing is encrypted on the wire unless you say so.** Plain HTTP means the
  password and session cookie cross the network in clear text. The server warns
  about this at startup; the warning is not decorative.
- **There is no audit log.** Nothing records which change was made when, or from
  where. With one operator that is a gap rather than a hazard, but do not deploy
  this expecting to reconstruct events after an incident.
- **The ingest port is reachable even when the UI is not.** Binding the web UI
  to loopback does not move the ingest listener, which must stay reachable for
  your encoder. A valid token is the only thing standing in front of it, plus
  the SRT passphrase if you set one.

### One thing automod sends outward

If you enable the model checker, **chat messages leave your server** for
whichever endpoint you configure. That is the whole point of it, and it is worth
saying plainly rather than leaving in a settings tooltip.

It is off by default. The endpoint is yours to choose, so a locally hosted
OpenAI-compatible model keeps everything on your own hardware. The API key is
sealed with the same NaCl secretbox as your platform tokens and is never
returned by any endpoint — the settings blob carries only `hasApiKey`.

The rules and history checkers send nothing anywhere. They are local, and they
are what catches most abuse.

**The endpoint is not filtered against private addresses.** A private address is
the *recommended* configuration — Ollama or vLLM on `127.0.0.1` or a LAN host —
so blocking those would reject the deployment we suggest. Only an authenticated
admin can set the field, and an admin can already read your tokens and edit your
destinations, so it grants no privilege they did not already have. Treat it the
way you treat every other admin-only setting: if untrusted people can reach your
admin UI, this field is not your biggest problem.

### Deploying it safely

- **Do not expose it to the internet, or to a LAN you do not control, without
  TLS.** `tls.mode: auto` is the one-line fix. Binding to `127.0.0.1` and
  reaching it over an SSH tunnel is the zero-configuration alternative.
- Put it behind a reverse proxy if you want anything resembling access control,
  and set `trustProxyHeaders: true` so throttling sees real client addresses.
- Set an SRT passphrase or an RTMP stream key even on a private network.
- Rotate the source token if it has ever been in a chat message, a screenshot or
  a support thread. `POST /api/v1/sources/{id}/token` does it with a five-minute
  grace period so a live encoder is not cut off.
- Back up `<dataDir>`, and treat it as secret material — it holds the database,
  the encryption key and the TLS private keys.

## A note on HSTS

HSTS is opt-in and is never sent over a self-signed certificate. This is
deliberate: an HSTS header from a self-signed instance pins the browser to HTTPS
for that host for months, and if the certificate is later lost or the instance
moves, the operator is locked out of their own tool with no obvious way back.
[docs/TLS.md](docs/TLS.md#hsts-is-opt-in-and-here-is-why) has the full reasoning.
