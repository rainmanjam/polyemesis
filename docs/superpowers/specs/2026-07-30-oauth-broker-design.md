# The OAuth broker

**Date:** 2026-07-30 · **Status:** design, not yet implemented
**Scope:** a new `cmd/broker` deployable, a shared `internal/brokerproto`
package, and broker-aware OAuth in `internal/oauth` and `internal/api`.

**Depends on:** [guided OAuth self-registration](2026-07-30-guided-oauth-self-registration-design.md).
That design is the fallback this one degrades to, and the permanent path for
YouTube. Build it first.

---

## Why this exists

polyemesis is being distributed so anyone can install it and stream. Pasting a
stream URL and key already works for every platform. OAuth does not, because it
requires each operator to register a developer application per platform — around
ten minutes of console work each, with several ways to get it silently wrong.

The goal is that a self-hoster clicks **Connect** and registers nothing.

## What makes this hard, in one line

OAuth requires the redirect URI to be registered in advance and matched exactly,
but every self-hosted install has a different hostname.

OBS Studio hit the same wall and solved it by running a broker. From their build
configuration:

```cmake
set(OAUTH_BASE_URL "https://auth.obsproject.com/" CACHE STRING "Default OAuth base URL")
```

Their Twitch token exchange posts to `auth.obsproject.com/v1/twitch/token`. This
design follows that shape, with one difference: OBS is a desktop application and
can use `http://127.0.0.1:<port>` as a redirect URI, because the browser and the
app are on the same machine. polyemesis is a **server the operator browses to**,
often on another machine, so loopback is unavailable and the broker is required
for all three platforms rather than just the secret-bearing ones.

## Scope: three platforms, not four

| Platform | Via broker | Why |
|---|---|---|
| Twitch | yes | Needs a confidential client; no PKCE |
| Facebook | yes | Needs an app secret; no PKCE |
| Kick | yes | PKCE, but still needs a fixed redirect URI |
| **YouTube** | **never** | Quota, below |

`DefaultQuotaUnits = 10000` in `internal/chat/quota.go` is a limit **per Google
Cloud project**, not per user. At `QuotaCostListMessages = 5` and a 200-unit
reserve, a shared project affords roughly:

> 9,800 ÷ 5 = **~1,960 chat polls per day across every install in the world**

A single four-hour broadcast polling every ten seconds spends 7,200 units. That
is about one and a third concurrent broadcasts, globally. Worse, each install's
pacer divides the remaining budget by time-to-reset believing it owns the whole
10,000, so they would collectively exhaust it before noon and then all take 403s
at once — "chat silently dead until midnight Pacific", the exact outcome
`quota.go` says it was written to design out.

YouTube self-registration is therefore permanent, not a stopgap.

## Non-goals

- Storing tokens on the broker. It passes them through and remembers nothing.
- Being an identity provider. Nobody logs into the broker.
- Multi-tenancy inside polyemesis. Each install still has one administrator.
- Per-install revocation, rate limiting, or usage analytics on day one. See Risks.

---

## 1. Deployment shape

One Go binary, `cmd/broker`, on Cloud Run with `min-instances=0`, behind a
custom domain — `auth.polyemesis.dev` for the sake of argument.

Four values in Secret Manager: the Twitch, Kick and Facebook client secrets, and
the broker's own HMAC signing key.

**The property that makes the whole design work:** each platform application
registers exactly one redirect URI, the broker's. Platforms never see an
install's hostname, so "every install is different" stops being a problem.

## 2. No database

A broker normally needs to remember which install a callback belongs to. This
one does not, because every fact it would store is carried in a signed token
instead.

| Normally stored | Carried instead |
|---|---|
| instance → return URL | Signed enrolment token, presented per request |
| Pending authorisation | Signed `state`, five-minute expiry |
| Tokens | Never held |
| PKCE verifier | Stays on the install |

Enrolment is a **binding** gate, not an **authorisation** gate — anyone may
enrol, because every self-hoster is legitimate. Its job is to stop the return
address being swapped mid-flow, and a signature does that as well as a database
row.

This is what keeps the service stateless, which is what makes Cloud Run's
scale-to-zero and horizontal scaling free rather than a problem to solve.

### What statelessness costs

- **No nonce replay prevention.** Mitigated by the five-minute `state` expiry
  and the platforms' own single-use authorization codes: a replayed code fails
  at the platform.
- **No per-install revocation.** Rotating the signing key re-enrols everyone. A
  blunt break-glass, but adequate for the threat.

Both are accepted deliberately in exchange for deleting an entire datastore.

## 3. Enrolment

```http
POST /v1/enrol            {"baseURL": "https://stream.example.com"}
200                       {"enrolment": "<signed token>"}
```

The token is `{instanceID, returnURL, iat}` signed HMAC-SHA256 with the broker
key. The install seals it into its settings blob alongside its other secrets.

**There is deliberately no proof-of-control check.** The obvious hardening —
have the broker call the submitted URL back and look for a nonce — *cannot work*
here: most self-hosters run on a LAN behind NAT, and the broker cannot reach
them. A check that only the publicly-reachable minority could pass would reject
the majority of legitimate installs.

Enrolling somebody else's URL is not useful anyway. The broker returns the
authorization code to the enrolled return URL, and that endpoint on the victim's
install sits behind `requireAuth` (`api.go:431-435`), so an attacker's browser
receives a 401 rather than anything they can use.

## 4. Connect: two channels, deliberately

**Channel one — through the browser, carrying only a short-lived code:**

```
operator clicks Connect
  install  ──302──▶  broker  /v1/twitch/start?enrolment=…&state=…&challenge=…
                     verifies the enrolment signature, reads returnURL
  broker   ──302──▶  platform authorize endpoint
                       redirect_uri = the broker's one fixed URI
                       state = signed{returnURL, instanceID, callerState, exp:5m}
  platform ──302──▶  broker  /v1/twitch/callback?code=…&state=…
                     verifies the state signature
  broker   ──302──▶  https://stream.example.com/api/v1/oauth/twitch/callback?code=…&state=<callerState>
```

`callerState` is returned intact, so polyemesis's existing `state` CSRF defence
for that route — the one its own comment describes as "the CSRF defence for
these two routes" — keeps working unchanged.

**Channel two — server-to-server, initiated by the install:**

```http
POST /v1/twitch/exchange  {"code": "...", "enrolment": "...", "verifier": "..."}
200                       {"accessToken": "...", "refreshToken": "...", "expiresIn": 3600, "scopes": [...]}
```

The broker adds the client secret, calls the platform, returns the result, and
forgets it.

### Why the exchange is a second call rather than a redirect payload

Two obvious alternatives were rejected:

- **Return tokens on the browser redirect.** URLs end up in browser history,
  proxy logs and `Referer` headers. Tokens must not travel that way.
- **Have the broker push tokens to the install.** Most installs are on a LAN
  with no inbound reachability. The broker cannot call them.

Having the install originate the call solves both at once: tokens travel over a
direct TLS response to a request the install made, so no inbound port is needed
and nothing sensitive touches a URL. The code that does ride through the browser
is single-use, expires in seconds, and is worthless without the client secret,
which only the broker holds.

## 5. Refresh: the same shape, for the life of the token

```http
POST /v1/twitch/refresh   {"refreshToken": "...", "enrolment": "..."}
```

Every provider's `Refresh` signature in `internal/oauth` takes `clientSecret`:

```go
func (t *Twitch) Refresh(ctx context.Context, clientID, clientSecret, refreshToken string) (*Token, error)
```

So the broker is on the path for **every refresh, for every connected account,
for the life of every install** — not merely at connect. This is the single most
important operational consequence of the design and it drives the availability
requirement below.

## 6. Degradation

When the broker is unreachable:

- **New connects** fail immediately, with a message naming the broker and
  offering the self-registration path from the companion spec.
- **Existing accounts** keep working until their access token expires, then stop.

polyemesis must therefore warn **before** that bites, not after: when a refresh
fails and the token has less than a configurable margin left, the account list
says so plainly. An operator discovering mid-broadcast that chat has stopped is
the failure this design must not produce.

The escape hatch is per platform: an operator may supply their own client ID and
secret for any platform, which bypasses the broker entirely and uses the flow the
companion spec describes. YouTube always uses that path.

## 7. Code layout

`cmd/broker` alongside `cmd/polyemesis`, with the wire types in a shared
`internal/brokerproto` package imported by both sides.

The contract is then enforced by the compiler rather than by documentation. That
matters here more than usual: this repository has been bitten repeatedly by two
copies of one fact drifting apart — the Go/TypeScript capability matrices, and
the setup guide that told operators to paste a Kick stream key polyemesis had
been fetching for months. A duplicated wire protocol would be the same bug with
a longer feedback loop.

## 8. What the broker logs

Method, path, platform, outcome, latency. Never a code, token, secret,
enrolment token, or return URL. `internal/alerts/redact.go` already establishes
the redaction pattern and should be reused rather than reinvented.

## 9. Testing

- **Protocol:** table tests over `internal/brokerproto` for signing, verification,
  expiry and tamper detection. Every field flipped in turn must fail verification.
- **Broker:** `httptest` platforms returning real-shaped payloads; assert the
  broker refuses an expired `state`, a forged enrolment token, a `state` whose
  return URL disagrees with the enrolment, and a missing PKCE verifier for Kick.
- **Client:** polyemesis against a fake broker, including the unreachable case
  and the pre-expiry warning.
- **End to end:** the broker and an install in one test process, exercising
  enrol → start → callback → exchange → refresh.
- **Mutation testing:** every guard removed in turn, the matching test confirmed
  to fail, then restored.

## 10. Deployment and cost

| | |
|---|---|
| Scaling | `min-instances=0`; a Go binary cold-starts in ~200–500 ms |
| Secrets | Secret Manager → environment at deploy; never in the image |
| Domain | Cloud Run domain mapping with a Google-managed certificate; this *is* the registered redirect URI |
| Networking | No VPC connector, no egress configuration |
| Cost | Expected to sit inside the 2M-request free tier |

## Risks

| Risk | Response |
|---|---|
| Broker outage breaks refresh for everyone | Warn before expiry, not after; per-platform escape hatch to own credentials |
| Shared client credentials are abused | Inherent to the model — OBS has the same exposure. Cloud Armor rate limiting if it materialises |
| A platform bans the shared app | Every install falls back to self-registration; the fallback must therefore stay first-class, not vestigial |
| Signing key compromise | Rotate; all installs re-enrol. Key lives only in Secret Manager |
| Replayed authorization code | Five-minute `state` expiry plus single-use codes at the platform |
| Maintainer owns the ToS relationship for all users | Accepted, and the same position OBS Project occupies |
