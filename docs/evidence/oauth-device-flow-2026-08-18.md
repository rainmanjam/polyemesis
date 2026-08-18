# OAuth without a registrable callback, checked 2026-08-18

Every claim below traces to the provider's own reference page, fetched on
2026-08-18, with the operative sentence quoted verbatim. Where a capability is
**absent**, the page that was read and does not contain it is named — an absence
with no page behind it is a guess, not a finding.

Two claims are marked **UNVERIFIED** and must not be built on until closed.

## The problem this is about

polyemesis builds its callback from the request it is answering:

```go
// internal/api/oauth_handlers.go:56
func (s *Server) redirectURI(r *http.Request, platform db.Platform) string {
	return fmt.Sprintf("%s/api/v1/oauth/%s/callback", s.origin(r), platform)
}
```

and `origin(r)` is `scheme + "://" + r.Host` — the address the operator typed
into their browser. Every provider requires the redirect to **exactly match** one
registered in their developer console. A box reachable only as `192.168.1.50`,
or on self-signed TLS, has no address any provider will accept.

Client credentials are already per-install and sealed
(`db.PutPlatformCreds(box, platform, clientID, clientSecret)`), so polyemesis is
bring-your-own-app today.

## Device flow, per provider

| Provider | Device flow | Ceiling that decides it |
|---|---|---|
| **Twitch** | **Yes** | Full scopes. Their own example uses `channel:manage:broadcast` |
| **YouTube** | **Yes, but** | Only `youtube` and `youtube.readonly`. No `youtube.upload` |
| **Facebook** | **Yes** | Permission set **UNVERIFIED** — see below |
| **Kick** | **No** | Token endpoint documents only `authorization_code` |

### Twitch — supported, with three consequences

<https://dev.twitch.tv/docs/authentication/getting-tokens-oauth/>

```
POST https://id.twitch.tv/oauth2/device
  client_id=<id>&scopes=<scopes>
→ { device_code, expires_in, interval, user_code, verification_uri }
```

then poll `https://id.twitch.tv/oauth2/token` with
`grant_type=urn:ietf:params:oauth:grant-type:device_code`.

The page's own worked example requests `channel:manage:broadcast`, which is the
scope the capability-expansion plan needs, so **the scope we care about is
demonstrated by the vendor**, not merely assumed.

Three properties that change the design, quoted:

> "Are only limited to the usage of device authorization grant flow to obtain
> OAuth tokens and cannot use any of the other flows"

A *public* client (no secret) can do device flow **and nothing else**. If
polyemesis wants both device flow and the existing code flow from one Twitch
app, it must stay a *confidential* client and keep a secret.

> "These tokens are for **one time use only**, meaning if they are used in
> refreshing a token they will become invalid after use."

Refresh is single-use — the store must persist the replacement atomically or the
account is orphaned on a crash between refresh and write.

> "There is an expiry on the refresh token which is inactive, which is set to
> **30 days**."

An install idle for 30 days must re-run the whole flow. Access tokens last
4 hours.

### YouTube — supported, but the scope ceiling is the finding

<https://developers.google.com/identity/protocols/oauth2/limited-input-device>

> "The OAuth 2.0 flow for devices is supported only for the following scopes:"

and under **YouTube API**, that list is exactly:

* `https://www.googleapis.com/auth/youtube`
* `https://www.googleapis.com/auth/youtube.readonly`

plus `email`, `openid`, `profile` for OpenID Connect.

**`youtube.upload` is not on the list.** The capability-expansion plan sequences
thumbnail upload last and alone because it may need `youtube.upload`; if it
does, that feature **cannot be reached through device flow at all**. Everything
else the plan wants from YouTube — `liveBroadcasts.insert`, `transition`,
`videos.update`, `playlistItems` — is covered by the single `youtube` scope, so
device flow is sufficient for the whole plan *except* thumbnails.

It also needs a **different OAuth client**:

> "Select the **TVs and Limited Input devices** application type."

So an operator using device flow for YouTube registers a second client, not the
web one. `invalid_client` with HTTP 401 is the error when the type is wrong.

### Facebook — flow exists; the permissions do not

<https://developers.facebook.com/docs/facebook-login/for-devices> (page updated
2026-06-30, so it is current, not an archived feature)

```
POST https://graph.facebook.com/<API_VERSION>/device/login_status
  access_token=<APP_ID|CLIENT_TOKEN>&code=<code>
```

with documented `error_subcode` values for pending authorization (`1349174`),
polling too fast (`1349172`), and so on.

**UNVERIFIED: which permissions Device Login can grant.** The page demonstrates
`public_profile`. polyemesis needs live-video publishing permissions, and
nothing on this page states whether those are grantable through Device Login or
gated to the standard flow. **Do not plan Facebook device flow until a
per-permission page is read.** Facebook's permission model already caught us
once — `platform-lifecycle-apis-2026-08-16.md` records that a page-level
permissions list is not a source for a per-endpoint scope.

### Kick — not supported

<https://docs.kick.com/getting-started/generating-tokens-oauth2-flow> is the
page that documents Kick's OAuth in full. Its token endpoint lists, as required:

| Field | Value |
|---|---|
| `grant_type` | `authorization_code` |
| `client_secret` | Yes (required) |
| `code_verifier` | Yes (required) |

The page documents an authorization endpoint, a token endpoint, and an app
access token (client credentials). **There is no device authorization endpoint
and no `urn:ietf:params:oauth:grant-type:device_code`.** Kick is OAuth 2.1 with
mandatory PKCE and a mandatory client secret.

## The fourth option: loopback redirect

This is not a workaround; it is the standard pattern for native and desktop
apps (RFC 8252), and two of the four providers document it explicitly.

**Google** — <https://developers.google.com/identity/protocols/oauth2/native-app>

> "Loopback IP address (macOS, Linux, Windows desktop) ... if your platform
> supports it, this is the recommended mechanism for obtaining the authorization
> code."

with `redirect_uri=http://127.0.0.1:9004` and application type **Desktop app**.
Note the caveat, quoted: "It is also possible to use `localhost` in place of the
loopback IP, but this configuration may cause issues with client firewalls."

**Kick** — <https://docs.kick.com/getting-started/generating-tokens-oauth2-flow>

> "If developing your authorization to only happen on an app running locally, we
> recommend using `localhost` as your redirect URI/callback URL e.g.
> `http://localhost/auth/callback`."

Kick also documents a live bug worth knowing before anyone debugs it blind:
their frontend rewrites the **first** `127.0.0.1` in a URL to `localhost`, so a
`127.0.0.1` redirect fails exact-match unless a sacrificial query parameter
containing `127.0.0.1` is placed *before* `redirect_uri`.

**Twitch** — <https://dev.twitch.tv/docs/authentication/register-app/> takes
arbitrary "OAuth Redirect URLs"; nothing on the page restricts the host.

### This already works in polyemesis, with no code change

Because `origin(r)` is derived from `r.Host`, an operator who reaches the UI at
`http://localhost:8080` — on the box itself, or through
`ssh -L 8080:localhost:8080` — gets a callback of

```
http://localhost:8080/api/v1/oauth/twitch/callback
```

which is exactly the string they paste into the developer console. **The
capability is present today and undocumented**, which is the cheapest gap in
this file to close.

The limit is honest and should be stated wherever this is documented: the
browser must reach that loopback address, so this serves an operator working on
the box or willing to open a tunnel. It does nothing for someone administering
a headless box from a phone.

## The fifth option: a real hostname

`internal/tlsx/acme.go` already runs `autocert.Manager` with a `HostPolicy`
pinned to one configured name. An operator who points a DNS name at the box gets
a trusted certificate and a callback of
`https://stream.example.com/api/v1/oauth/twitch/callback` — registrable
everywhere, with no relay and no device flow.

This is the only option that needs neither new code nor a compromise, and it is
the one a public-IP install should be told about first. It costs a domain name
and a port 80/443 reachable for the ACME challenge.

## Where that leaves the five options

| | Needs new code | Custody risk | Works headless + remote | Scope ceiling |
|---|---|---|---|---|
| **A** BYO + public address | none | none | yes | none |
| **B** hosted relay | relay service | **you hold every user's client secret** | yes | none |
| **C** device flow | per-provider flow + polling UI | none | yes | YouTube capped; Kick impossible |
| **D** loopback | **none — works today** | none | no (needs local browser or tunnel) | none |
| **E** hostname + ACME | none — already built | none | yes | none |

**Recommendation: document E, then D, then implement C for Twitch only.**

E and D cost nothing but words and close the case for most operators. C is worth
building for Twitch, where the vendor demonstrates the exact scope we need. C
for YouTube buys everything except thumbnails and costs a second client
registration. C for Kick is impossible. B remains the worst trade: it is the
only option that makes a polyemesis outage able to stop someone else's
broadcast, and the only one where a single compromise reaches every user's
channel.

## Open items

1. **UNVERIFIED** — Facebook Device Login permission set. Needs a
   per-permission page, not the device-login guide.
2. **UNVERIFIED** — whether YouTube thumbnail upload actually requires
   `youtube.upload`. If it does not, device flow covers the whole plan.
3. Kick's `127.0.0.1` rewrite bug should be re-checked before anyone relies on
   loopback there; it is a live third-party defect, not a stable property.

## The matrix

Cells carry their confidence, because the useful thing here is knowing which
squares were *read* and which were reasoned. Legend:

* **Works** — a page read on 2026-08-18 says so, or it follows by construction
  from the provider accepting an arbitrary registered HTTPS redirect.
* **Limited** — supported, with a stated ceiling that changes the design.
* **No** — the page that documents this provider's OAuth does not contain it.
* **Unchecked** — plausible, not read. Do not build on it.

| | Twitch | YouTube | Facebook | Kick |
|---|---|---|---|---|
| **A** — BYO app, registrable address | **Works** (ships today) | **Works** (ships today) | **Works** (ships today) | **Works** (ships today) |
| **B** — hosted relay (`auth.polyemesis.com`) | Works mechanically; **ToS unchecked** | Works mechanically; **ToS unchecked** | Works mechanically; **ToS unchecked** | Works mechanically; **ToS unchecked** |
| **C** — device flow | **Works** — vendor example uses `channel:manage:broadcast` | **Limited** — only `youtube`, `youtube.readonly`; needs a *TVs and Limited Input devices* client | **Limited** — flow current, **permissions unverified** | **No** — token endpoint documents only `authorization_code` |
| **D** — loopback redirect | **Works** — register-app page places no host restriction | **Works** — "recommended mechanism" for desktop; *Desktop app* client type | **Unchecked** | **Works** — vendor recommends `http://localhost/auth/callback`; see the `127.0.0.1` rewrite bug |
| **E** — real hostname + ACME | **Works** | **Works** | **Works** | **Works** |

### What OBS Studio does, per platform

Read from the OBS sources on 2026-08-18, because "OBS uses a relay" is the
argument most often made for option B and it turns out to be **only two thirds
true**. OBS picks a *different* strategy for Google than for the others.

| Platform | OBS's strategy | Evidence |
|---|---|---|
| **Twitch** | **B1** — shared credentials, obsproject.com relays *and exchanges* | `#define TWITCH_AUTH_URL "https://obsproject.com/app-auth/twitch?action=redirect"` / `#define TWITCH_TOKEN_URL "https://obsproject.com/app-auth/twitch-token"`, with `client_id = TWITCH_CLIENTID; deobfuscate_str(&client_id[0], TWITCH_HASH);` |
| **Restream** | **B1** — identical shape | `RESTREAM_AUTH_URL ".../app-auth/restream?action=redirect"`, `RESTREAM_TOKEN_URL ".../app-auth/restream-token"`, `deobfuscate_str(&client_id[0], RESTREAM_HASH)` |
| **YouTube** | **D-shaped** — a *local listener*, and OBS exchanges the code itself | `auth-youtube.cpp` includes `auth-listener.hpp`, and calls `auth->GetToken(YOUTUBE_TOKEN_URL, clientid, secret, redirect_uri, ...)` — note it passes a **secret** and its **own** `redirect_uri` |
| **Facebook** | Not integrated in the sources read | — |
| **Kick** | Not integrated in the sources read | — |

Three things follow, and they matter for the polyemesis decision.

**The client ID is obfuscated in the binary, not secret.** `deobfuscate_str` is
obfuscation, not encryption — the value ships to every user and is recoverable
by anyone who cares. OBS is not pretending otherwise; it is accepting that a
desktop app cannot hold a secret, and putting the *exchange* somewhere that can.

**That is the whole point of the relay.** For Twitch and Restream the token
exchange happens at `obsproject.com/…-token`, not in OBS. The relay exists so
the client secret never ships. Any polyemesis relay that only forwards the
redirect and lets each box exchange (**B2**) is solving a different problem —
and only works because each polyemesis operator has their *own* secret to
exchange with, which OBS's users do not.

**Google got a different answer.** For YouTube, OBS runs a local listener and
exchanges directly, passing a `secret` from the binary. It did not route Google
through obsproject.com the way it routes Twitch. Whatever the reason — Google's
rules on redirect URIs, or its verification model attaching to the client — the
lesson for us is that **a relay design cannot be assumed to be acceptable to
every provider**, and Google is the one that most often differs.

**UNVERIFIED:** the host `YOUTUBE_TOKEN_URL` points at, and the loopback port
the listener binds. Both were inferred from the call shape and the include, not
read.

### What each option costs polyemesis

| | New code | Custody risk | Headless + remote admin | Extra registration |
|---|---|---|---|---|
| **A** | none | none | yes | — |
| **B** | a relay service, run forever | **one secret for every user** | yes | none for the operator |
| **C** | device endpoint + polling UI, per provider | none | **yes** | YouTube needs a 2nd client |
| **D** | **none — works today** | none | no — needs a local browser or a tunnel | — |
| **E** | none — `tlsx/acme.go` already does it | none | yes | a DNS name |

### Reading the matrix

**No single option covers all four providers for a headless, remotely
administered box except B and E.** C fails on Kick outright and is capped on
YouTube; D needs a browser that can reach loopback.

**E is the only row that is all-Works, needs no code, and carries no custody
risk.** It costs a DNS name. That makes it the answer to recommend first, and
the fact that it is already implemented and undocumented is the cheapest thing
on this page to fix.

**C earns its build on Twitch alone.** That is the one cell where the vendor
demonstrates the exact scope the capability plan needs, and where no second
client registration is required.

**B is the only row with a red cell that is not about capability.** It is
all-Works on capability and the sole entry whose failure mode is *your outage
stops someone else's broadcast*.
