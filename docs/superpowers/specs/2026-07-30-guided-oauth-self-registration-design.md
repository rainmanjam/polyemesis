# Guided OAuth self-registration

**Date:** 2026-07-30 · **Status:** approved, not yet implemented
**Scope:** credential validation and redirect-URI preflight in `internal/oauth`,
`internal/api` and `ui/src/pages/SettingsPage.tsx`.

---

## Why this exists

polyemesis is being prepared for general distribution: anyone should be able to
install it and stream, authenticating either by pasting a stream URL and key or
through OAuth.

The pasted-key half already works. `destinations.account_id` is nullable and the
`TierManual` presets (X, Rumble, DLive) stream today with nothing but a URL and
a key. No work is needed there.

The OAuth half requires every operator to register a developer application with
each platform, and that is where new installs will fail. Not because the steps
are hard — `oauth.go` already carries good prose for all four platforms — but
because **every mistake is silent until it is expensive**. `handlePutCreds`
checks only that two strings are non-empty:

```go
if strings.TrimSpace(req.ClientID) == "" || strings.TrimSpace(req.ClientSecret) == "" {
    writeError(w, http.StatusBadRequest, "both a client ID and a client secret are required")
}
```

A typo, a wrong redirect URI, or a forgotten API enablement is accepted without
comment and surfaces much later as an opaque platform error, at the moment the
operator is trying to go live.

This design attacks the silence, not the step count.

## Relationship to the OAuth broker

A second design (spec A+B) will add a hosted broker, modelled on OBS Studio's
`auth.obsproject.com`, so that Twitch, Kick and Facebook need no per-install
registration at all.

This spec is not made redundant by that work, for two reasons:

1. **YouTube can never use the broker.** `DefaultQuotaUnits = 10000` is a limit
   per *Google Cloud project*, and at `QuotaCostListMessages = 5` a shared
   project affords roughly 1,960 chat polls per day across every install in the
   world — about one and a third concurrent broadcasts. YouTube self-registration
   is permanent.
2. **It is the broker's fallback.** When the broker is unreachable, or an
   operator declines to use it, this is the path they land on.

## Goals

- An operator learns a credential is wrong **when they paste it**, not at go-live.
- An operator learns the redirect URI they are about to register will not work
  **before** they register it.
- Where a credential cannot be verified, the UI says so plainly rather than
  implying a check happened.

## Non-goals

- The broker (spec A+B).
- Persisting verification state. A stale "verified" badge is worse than none.
- Rate-limiting the check. It is bounded by operator clicks.
- Any change to the OAuth authorization flow itself.
- A multi-step wizard route. The existing panel is the surface.

---

## 1. Where validation lives

A new interface in `internal/oauth`, implemented only by providers that can
prove a credential pair without a user consent round-trip:

```go
// CredentialChecker is implemented by providers whose client credentials can be
// proven correct without a user consent round-trip.
type CredentialChecker interface {
    CheckCredentials(ctx context.Context, clientID, clientSecret string) error
}
```

Twitch, Kick and Facebook implement it. YouTube deliberately does not.

**This must not be an optional, nil-able hook.** That exact shape produced the
Medium finding in the 2026-07-30 security review: `KickConfig.Verify` was
declared, nil-checked, and never assigned at its one construction site, so
webhook signature verification silently never ran for two releases. An optional
security control is an absent one.

The structure that prevents a repeat:

- **Compile-time assertions** — `var _ CredentialChecker = (*Twitch)(nil)` and
  the same for Kick and Facebook. Dropping the method breaks the build.
- **An explicit exclusion set** — `unverifiableProviders` names YouTube *and its
  reason*, in code, not in a comment somewhere else.
- **A drift test** asserting every registered provider is in exactly one of the
  two categories. A provider added later cannot quietly default to "unverified";
  the author has to state which it is.

### Implementation notes per provider

| Provider | Call |
|---|---|
| Twitch | `POST https://id.twitch.tv/oauth2/token`, `grant_type=client_credentials` |
| Kick | `POST https://id.kick.com/oauth/token`, `grant_type=client_credentials` |
| Facebook | `GET /oauth/access_token?grant_type=client_credentials` — a **GET**, unlike the others |

No new dependency. `golang.org/x/oauth2/clientcredentials` was considered and
rejected: polyemesis has no OAuth dependency today, `internal/oauth` is 7,649
lines of deliberate platform-specific handling, and Facebook's GET-shaped
endpoint falls outside what that package models. Adding a library for three
functions that must then be special-cased would fight the existing design.

## 2. What a check means, stated honestly

| Platform | Method | What a pass proves |
|---|---|---|
| Twitch | `client_credentials` | ID and secret are both correct |
| Kick | `client_credentials` | ID and secret are both correct |
| Facebook | `client_credentials` | App ID and secret are both correct |
| YouTube | format only | Only that it resembles `*.apps.googleusercontent.com` |

The API returns the **method**, not merely a boolean, and the UI renders three
distinct states:

- **Verified** — the platform accepted the pair.
- **Unverified** — this platform offers no way to check; the verdict comes at
  first connect.
- **Rejected** — the platform refused, with its own message shown.

A check that cannot fail must never look like one that can. Rendering YouTube's
format check as the same green tick Twitch earns would be a lie told by a
progress indicator.

Validation runs on save and is also exposed as an explicit re-check, so an
operator can retest after fixing something in the console without re-pasting.

### API shape

The existing routes, verified against `internal/api/api.go:375-377`:

```http
GET    /api/v1/platforms/credentials
PUT    /api/v1/platforms/credentials/{platform}
DELETE /api/v1/platforms/credentials/{platform}
POST   /api/v1/platforms/credentials/{platform}/check
```

`PUT` gains validation and reports the result. `POST .../check` is new. The
other two are unchanged.

The re-check is a `POST` despite reading nothing: it makes an outbound call to a
third party, so it is not safe or idempotent in the HTTP sense, and `POST` puts
it behind `requireCSRF` with the rest of the state-changing group.

Response:

```json
{
  "platform": "twitch",
  "state":    "verified" | "unverified" | "rejected" | "unreachable",
  "method":   "client_credentials" | "format",
  "detail":   "human-readable, safe to display"
}
```

Saving is **not** blocked by a `rejected` result. An operator may be mid-way
through console setup, and refusing to store a credential they are about to make
valid would be obstructive. The state is reported, not enforced.

## 3. Redirect-URI preflight

`GET /api/v1/oauth/guides` already returns `origin + RedirectPath`. It gains a
`redirectWarnings []string`, computed server-side where the configuration
actually lives (`cfg.TLS.Hostname`, `ServesTLS()`, `TrustProxyHeaders`, `r.Host`).

| Condition | Why it matters |
|---|---|
| Scheme is `http://` and host is not loopback | Google rejects non-HTTPS redirect URIs; Twitch too, except localhost |
| Host is a bare IP literal | Google will not accept an IP for a web application client |
| `tls.hostname` is set and differs from the browsed host | The operator would register the URI they can see, then serve on another name, and get `redirect_uri_mismatch` |
| `X-Forwarded-Host` present while `trustProxyHeaders` is off | polyemesis is behind a proxy it cannot see, so the URI it displays is probably not the one the browser used |

Every warning names the exact URI to register. A warning that says only "this
may be wrong" moves the problem rather than solving it.

**Warnings never block.** A reverse proxy terminating TLS upstream is
indistinguishable, from inside the process, from a misconfiguration. Refusing to
save would trap a working deployment to protect a hypothetical broken one.

## 4. UI

Additive to the existing panel in `SettingsPage.tsx`, which already renders the
redirect URI with copy-to-clipboard around line 1504.

- Warnings render **above** the credential fields. Registering the correct URI
  has to happen before the credentials matter, so it comes first on screen.
- After save, a status chip carrying one of the four states. On `rejected`, the
  platform's own message is shown: an operator can act on "invalid client
  secret" and cannot act on "validation failed".

No new route, no wizard, no new components.

## 5. Errors

**"Could not reach the platform" is never rendered as "your credentials are
wrong."** These are different facts and collapsing them ships a frightening,
incorrect message. Hence the distinct `unreachable` state.

- Checks are bounded at 5 seconds so a hanging platform cannot hang the
  settings page.
- Secrets are never logged, never echoed in a response. The check returns a
  verdict and a reason, never its input. `internal/alerts/redact.go` already
  establishes the pattern.
- A check failing for any reason never prevents the credential being stored.

## 6. Testing

- **Per provider:** `httptest` servers returning real-shaped success and error
  payloads, including Facebook's differently-shaped GET endpoint.
- **Preflight:** a table with a positive *and* a negative case per warning. A
  check that always fires passes a table of bad input exactly as happily as a
  correct one, so each warning must be shown to stay silent when it should.
- **Categorisation drift:** every registered provider is either a
  `CredentialChecker` or named in `unverifiableProviders`, never both, never
  neither.
- **Mutation testing:** each guard removed in turn, the corresponding test
  confirmed to fail, then restored. A guard that does not fail when the fix is
  removed proves nothing.
- **UI:** `tsc -b` and `oxlint` clean; no Playwright coverage, because the
  existing suite cannot reach a real platform and a mocked round-trip would
  assert only that the mock was called.

## Risks

| Risk | Response |
|---|---|
| A platform changes its client-credentials endpoint | The check fails `unreachable`, not `rejected`; saving still works, so nobody is locked out |
| Preflight warns on a valid proxy deployment | Warnings never block; the text names the proxy case explicitly |
| YouTube's weaker check is read as equivalent | Three distinct states, different wording, different colour — never one tick |
| Scope creep toward a wizard | Explicit non-goal; the surface is the existing panel |
