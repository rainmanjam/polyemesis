# Connecting a platform without registering an application

**Status: a decision recorded, not a change made.** Nothing in this file is
built. It exists so that whoever builds it starts from what was researched on
2026-08-26 rather than from an assumption about how OBS works.

## The problem, stated as the operator's experience

To connect one platform today, an operator registers a developer application on
that platform, copies a client ID and a client secret into polyemesis, and sets
a redirect URI that must match exactly. For four platforms that is four
dashboards, four sets of credentials, and — for one of them — a review process
measured in days.

Every self-hoster does this individually. The work is identical every time and
none of it is about their broadcast.

The friction is also **wildly uneven**, which matters when choosing what to fix:

| Platform | What the operator faces |
|---|---|
| Twitch | Register app, set redirect, copy two values. Minutes |
| Kick | Same, plus one trap: the stream key is withheld unless `streamkey:read` is granted, and an account connected before that scope was requested must be disconnected and reconnected |
| YouTube | Google Cloud project, enable YouTube Data API v3, consent screen, add yourself as a **test user**. More clicks, no review |
| Facebook | Create app, add Facebook Login, and then **App Review for Advanced Access** if anyone other than the app's own developer will connect. Days |

## How OBS solves it, read from its source on 2026-08-26

OBS is the closest comparable: a desktop application, widely forked and
repackaged, that offers one-click sign-in to Twitch, YouTube and Restream.

**It runs a broker.** From
[`frontend/oauth/TwitchAuth.cpp`](https://github.com/obsproject/obs-studio/blob/master/frontend/oauth/TwitchAuth.cpp):

```c
#define TWITCH_AUTH_URL  OAUTH_BASE_URL "v1/twitch/redirect"
#define TWITCH_TOKEN_URL OAUTH_BASE_URL "v1/twitch/token"
```

and from `frontend/CMakeLists.txt`:

```cmake
if(NOT OAUTH_BASE_URL)
  set(OAUTH_BASE_URL "https://auth.obsproject.com/" CACHE STRING "Default OAuth base URL")
```

OBS does not call Twitch's token endpoint. The exchange goes through a service
the OBS project operates. The client **ID** is compiled in and lightly
obfuscated (`deobfuscate_str`, `frontend/utility/obf.h`); the client **secret**
is not in the binary at all, because the broker holds it.

**YouTube is the exception, and the exception is instructive.**
`frontend/cmake/templates/ui-config.h.in` defines:

```c
#define TWITCH_CLIENTID "@TWITCH_CLIENTID@"
#define YOUTUBE_CLIENTID "@YOUTUBE_CLIENTID@"
#define YOUTUBE_SECRET   "@YOUTUBE_SECRET@"
```

Twitch ships an ID only; YouTube ships an ID *and* a secret. Google's installed-
application model treats that secret as non-confidential — it is a public client
in the RFC 8252 sense — while Twitch requires a confidential client. **The
platform's own rules decide whether a broker is needed at all**, and they differ
per platform. Any design here that treats "OAuth" as one thing will be wrong for
at least one of the four.

**A build without credentials loses the feature**, from
`frontend/cmake/feature-twitch.cmake`:

```cmake
if(TWITCH_CLIENTID AND TWITCH_HASH MATCHES "^(0|[a-fA-F0-9]+)$" AND TARGET OBS::browser-panels)
  target_enable_feature(obs-studio "Twitch API connection" TWITCH_ENABLED)
else()
  target_disable_feature(obs-studio "Twitch API connection")
```

So a distribution rebuilding OBS from source ships an OBS with no Twitch
integration. That is the honest cost of the model and OBS accepts it. **We would
be accepting it too**, and for a project whose users build from source more often
than OBS's do, it lands harder.

## The options

| | Operator effort | What it costs the project |
|---|---|---|
| **Today** — every operator registers their own apps | High, and repeated per platform | Nothing. No service, no secrets held, no availability promise |
| **Broker**, OBS's model | Near zero | A service to run, secure and keep up. It sees every token in transit. Every install depends on its uptime |
| **Ship a client ID, no secret** | Zero, where the platform permits a public client | Only lawful where PKCE-without-secret is allowed. Already true for Kick |
| **Hybrid** — broker by default, bring-your-own always supported | Zero by default, full control when wanted | Two code paths, and both must be tested |

## What is recommended, and the reasoning

**The hybrid, defaulting to bring-your-own credentials.**

The argument against a mandatory broker is not operational, it is what the
product claims to be. `README.md` sells a single static binary with no runtime
dependencies, and `docs/COMPARISON.md` sells self-hosting as the thing that
distinguishes polyemesis from restream.io. A required call to a server we operate
makes every self-hosted install depend on our uptime and routes every operator's
tokens through us. That is a change to the promise, not a convenience feature,
and it should never be the only path.

As an **option**, it is straightforwardly good: the operator who wants one click
gets one click, and the operator who wants nothing to phone home keeps that.

### Two cheaper wins to take first

1. **Kick needs no broker at all.** It is OAuth 2.1 with PKCE — a public client,
   no secret. A client ID could ship today and one of the four platforms becomes
   one-click with no infrastructure and no secret held anywhere. This is the
   highest ratio of benefit to risk on the list and it is available now.

2. **Facebook cannot be fixed by any of this.** App Review gates the
   *permissions* — `publish_video`, `pages_manage_posts` — not the credentials.
   A broker using our client ID would still require every operator's use to be
   covered by *our* App Review, which is a materially different and larger
   commitment than relaying a token. Facebook stays bring-your-own.

So the realistic end state is: **Kick free, Twitch and YouTube one-click via the
broker, Facebook unchanged.** Anyone estimating this work should price it as
"two and a half platforms", not four.

## The broker, if it is built

Cloudflare Workers fits, and the shape is small: two routes, four secrets, no
database.

| Need | Mechanism |
|---|---|
| Hold the client secrets | Workers Secrets (encrypted bindings, read from `env`) |
| `/v1/<platform>/redirect` and `/v1/<platform>/token` | one Worker, path-routed |
| Call the platform's token endpoint | one `fetch()` subrequest |
| Stable redirect URI | custom domain on a Worker route |
| Cost | free plan allows 100,000 requests/day |

Traffic is proportional to **account connections**, not to streaming, so the free
tier is not a constraint in any realistic deployment.

Two platform limits are worth writing down before someone meets them:

* The free plan allows **50 external subrequests per invocation**. A token
  exchange is one. Not a constraint.
* Exceeding the daily request limit returns **Error 1027**, and the route's fail
  mode is configurable. This must be **fail closed**. Fail open bypasses the
  Worker, and for a security-critical route that is worse than refusing.

### The broker must be stateless, and that is the point

The obvious implementation stores the PKCE verifier and state in KV for the few
seconds between redirect and callback. **Do not.** Sign the state parameter with
a Worker secret and carry what is needed inside it.

This removes KV, removes expiry sweeping, and removes the only reason the broker
would have a datastore. What it buys is a sentence that can be written in the
documentation and be true: **the broker stores nothing about anyone.**

For a product whose distinguishing claim is that nothing phones home, the
difference between "a service that holds your data" and "a service that relays
one exchange and forgets" is worth more than the engineering it saves.

**It still sees every token in transit.** That is unavoidable in this design and
must be stated plainly wherever the broker is offered, not buried. An operator
who objects has the bring-your-own path, which is exactly why that path stays.

## What would have to be true before building it

* The broker's client IDs are registered to the **project**, which means the
  project accepts each platform's developer terms on behalf of every operator
  who uses the default path. Read them first; at least one will have something
  to say about it.
* A rate limit per operator, or one abusive install exhausts a shared quota for
  everyone. Trovo, for reference, publishes 1200 requests/minute **per client
  id** — shared across every user of that id.
* An answer to "the broker is down and I cannot connect an account", which is
  bring-your-own, and that answer only exists because the hybrid keeps it.
* A decision on what happens to tokens already issued through the broker if the
  broker is ever retired.

## The rule this leaves

**Bring-your-own credentials must always work, and must always be documented as
the path that depends on nothing.** Anything built on top of that is a
convenience, and a convenience may be switched off.
