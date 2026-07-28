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
- **Login brute force.** Five free attempts per client address, then a delay
  doubling from 2s, capped at 5 minutes, with `Retry-After` on the 429. The cap
  and the one-hour idle reset are deliberate: an uncapped lockout is a
  denial-of-service against the operator's own server. Counters are in memory
  only, so a restart never strands you outside your own instance.
- **Secrets at rest.** Platform OAuth tokens and client secrets are encrypted
  with NaCl secretbox. API tokens are stored as hashes. TLS private keys live in
  `<dataDir>/tls/` with the directory `0700` and keys `0600`.
- **Secrets in transit to you.** No stream key, client secret, API token or TLS
  private key is ever returned by an API or written to a log. Webhook URLs — which
  carry their credential in the path — are masked in every response.
- **Path confinement.** File destinations, `file://` pull sources, slate images,
  recording and clip downloads are all confined to the data directory. Paths
  that come from the database are never trusted as filesystem paths.
- **Browser-side hardening.** Every response carries a CSP,
  `X-Frame-Options: DENY`, `nosniff`, `Referrer-Policy: no-referrer`, and a
  `Permissions-Policy` disabling camera, microphone and geolocation.

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
- **The ingest port is not authenticated by default.** An SRT or RTMP listener
  accepts whoever reaches it unless you set a passphrase or stream key. With
  one-port ingest the per-source token *is* the address, which closes this — but
  only when it is enabled.
- **Nothing is encrypted on the wire unless you say so.** Plain HTTP means the
  password and session cookie cross the network in clear text. The server warns
  about this at startup; the warning is not decorative.

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
The README section *TLS and certificates* explains the full reasoning.
