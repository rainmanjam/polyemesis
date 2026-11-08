# Connecting your platforms

From a self-hosted server that is installed to streaming and reading chat on
X, Twitch, Kick, YouTube and Facebook. Do the server part once, then each
platform in turn; every platform is optional and independent.

[PLATFORMS.md](PLATFORMS.md) says what each platform's API allows.
[QUICKSTART.md](QUICKSTART.md) gets the first stream out. This page is the
path between them: what to put on the server, what to register at each
platform, and how to tell it worked.

> **Upgrading?** After upgrading to this release, reconnect Twitch and X once:
> both ask for new permissions, and a granted scope never reaches a login that
> was issued earlier.

- [Server prerequisites](#server-prerequisites)
- [The same four steps on every platform](#the-same-four-steps-on-every-platform)
- [X](#x)
- [Twitch](#twitch)
- [Kick](#kick)
- [YouTube](#youtube)
- [Facebook (being verified)](#facebook-being-verified)
- [Chat actions by platform](#chat-actions-by-platform)
- [Alerts and chat export](#alerts-and-chat-export)
- [When something is empty or refused](#when-something-is-empty-or-refused)

---

## Server prerequisites

Do these once, before you register an app anywhere.

**Use a real hostname with a trusted certificate.** Point a DNS name at the
server and set `tls.mode: acme` with `tls.hostname` and `tls.acmeEmail`, so
Let's Encrypt issues the certificate:

```yaml
tls:
  mode: acme
  hostname: polyemesis.example.com
  acmeEmail: you@example.com
```

Ports **80 and 443** must be reachable from the internet. 443 serves the
dashboard and the platform callbacks; 80 answers the ACME challenge and
redirects plain HTTP to HTTPS. [TLS.md](TLS.md) and
[CONFIGURATION.md](CONFIGURATION.md#the-part-that-pays-for-itself-platform-sign-in)
have the long version, including what `auto` resolves to.

**Open the dashboard by its hostname, never its IP.** polyemesis builds every
OAuth callback from the address you browsed to, and a platform accepts only
the redirect URLs you registered. Browsing by IP produces a callback nobody can
register, and the bare IP has no valid certificate either.

**Let the platforms reach you.** X and Kick deliver chat by webhook to your
server, so it must be publicly reachable over HTTPS:

- A provider firewall or edge filter in front of the host (a cloud provider's
  network firewall, for example) must allow inbound 80 and 443.
- Keep the host firewall (UFW, for example) to 22, 80 and 443 plus the ingest
  ports you use: RTMP 1935 and SRT 6000/udp.
- Test from outside. Ubuntu maps the server's own hostname to `127.0.1.1` in
  `/etc/hosts`, so a reachability check run on the box itself proves nothing.
  From another machine, `https://polyemesis.example.com` should load with a
  padlock.

**Know your redirect URL.** Every platform's redirect URL has the same shape:

```
https://polyemesis.example.com/api/v1/oauth/<platform>/callback
```

where `<platform>` is `x`, `twitch`, `kick`, `youtube` or `facebook`. For
example `https://polyemesis.example.com/api/v1/oauth/x/callback`. **Settings →
Platform credentials** shows the exact string for each platform, built from the
address you are browsing, with a copy button.

**Keep the admin password to hand.** If you lose it, `-reset-admin` sets a new
one from the server's shell; see
[the FAQ](FAQ.md#i-forgot-the-admin-password-how-do-i-get-back-in).

## The same four steps on every platform

1. **Register an app** in the platform's developer console, with your redirect
   URL.
2. Paste its credentials into **Settings → Platform credentials**.
3. Press **Connect account** and approve the sign-in.
4. Under **Destinations → Add**, choose the platform and pick the connected
   account under **Connected account**.

Credentials are the platform's own client ID and secret (App ID and App secret
on Facebook). They are stored encrypted and never returned by the API. A tick
beside them means the platform accepted the pair, for every platform but
YouTube, which cannot be checked before sign-in; see
[Connecting an account](PLATFORMS.md#connecting-an-account).

---

## X

X gives you everything polyemesis can do with it through its Livestream API:
polyemesis creates the stream source, takes the broadcast live with your title,
reads chat, and ends the broadcast when you stop.

### 1. Register the app

In the X developer console create an app with **OAuth 2.0** as a
**confidential client**. Set:

- **Callback URL** to `https://polyemesis.example.com/api/v1/oauth/x/callback`
- **Website URL** to `https://polyemesis.example.com`

polyemesis requests these scopes, so the app must allow them: `tweet.read`,
`tweet.write`, `users.read`, `offline.access`, `media.write`, `broadcast.read`
and `broadcast.write`.

### 2. Paste the credentials and connect

Copy the app's **OAuth 2.0 Client ID** and **Client Secret** into **Settings →
Platform credentials → X**, then press **Connect account**. The account must be
allowed to go live (not protected).

### 3. Turn on live chat

X delivers live chat only to a webhook, and the calls that register it need the
app's **app-only bearer token**. Set it as `POLYEMESIS_X_APP_BEARER`, from the
app's keys in the X developer console. It is a secret, so keep it in a systemd
`EnvironmentFile` with mode 600 rather than inline in the unit:

```sh
sudo install -m 600 -o root -g root /dev/null /etc/polyemesis/polyemesis.env
sudoedit /etc/polyemesis/polyemesis.env     # add: POLYEMESIS_X_APP_BEARER=<your bearer token>
sudo systemctl edit polyemesis              # add under [Service]:
                                            #   EnvironmentFile=/etc/polyemesis/polyemesis.env
sudo systemctl restart polyemesis
```

When chat starts, polyemesis registers `https://polyemesis.example.com/api/v1/webhooks/x`
with X and subscribes the account to `broadcast.chat` and
`broadcast.chat.remove`. X checks the webhook with a challenge that polyemesis
answers using the OAuth 2.0 client secret you pasted in step 2, so the address
must be publicly reachable on 443 with a trusted certificate (see [Server
prerequisites](#server-prerequisites)). Without the bearer or the address the
chat pane stays empty and says what is missing; sending is unaffected.
[CONFIGURATION.md](CONFIGURATION.md#x-live-chat) has the reference.

### 4. Add the destination

**Destinations → Add**, choose **X**, then pick the account under **Connected
account**. **Leave RTMP URL and stream key empty**: polyemesis creates the
stream source on X when you save. X Livestream is on by default;
`POLYEMESIS_X_LIVESTREAM=0` turns it off. Pasting the server URL and stream key
from X's producer tooling stays the fallback, and is how an account that cannot
use the API streams.

On the destination's **Video and advanced** step, the **X Livestream** card has
two settings:

- **Go-live title** is sent to X when polyemesis takes the broadcast live,
  which is the only moment X takes a title.
- **Go-live announcement** chooses whether X posts about the broadcast:
  **On the first go-live of a show only**, **On every go-live**, or
  **Never**.

When the destination starts, polyemesis waits for your video, takes the
broadcast live, and ends it on X when the destination stops.

### What has been checked live

Verified against a real X account: connecting, the go-live title, the
announcement settings ("first" posted the announcement with the title, "never"
posted nothing), chat read over the webhook, and ending the broadcast. Not yet
verified live: sending chat, moderation, and viewer counts.

---

## Twitch

1. At <https://dev.twitch.tv/console/apps>, **Register Your Application** with
   OAuth Redirect URL `https://polyemesis.example.com/api/v1/oauth/twitch/callback`,
   category *Broadcasting Suite* and client type **Confidential**.
2. Press **Manage**, then **New Secret**. Paste the **Client ID** and **Client
   Secret** into **Settings → Platform credentials → Twitch**.
3. **Connect account**, then add the destination and pick the account.

Chat is an IRC connection, so it needs nothing public. polyemesis validates the
token at startup and every hour, as Twitch requires, and re-attaches chat when
you connect; if Twitch revokes the token the account shows as needing a
reconnect. Scopes and the moderation notes are in
[PLATFORMS.md](PLATFORMS.md#twitch), and
[the token validation section](PLATFORMS.md#twitch-token-validation) has the
detail.

---

## Kick

1. In Kick, open **Settings → Developer** and create an app with redirect URL
   `https://polyemesis.example.com/api/v1/oauth/kick/callback`.
2. Request the scopes polyemesis uses: `user:read`, `channel:read`,
   `channel:write`, `chat:write`, `moderation:chat_message:manage`,
   `moderation:ban`, `events:subscribe` and `streamkey:read`. In Kick's wording
   that is user read, channel read and write, chat write, events subscribe,
   moderation, and stream key.
3. Paste the **Client ID** and **Client Secret** into **Settings → Platform
   credentials → Kick**, then **Connect account**. The stream key is fetched
   for you.

### Chat needs webhooks

Kick delivers chat to your server, so chat has one more step:

1. In **Settings → Platform credentials → Kick**, open **How to register the
   app** and press **Copy** beside **Webhook URL to paste into your Kick app**.
2. In your Kick app press **Edit**, switch on **Enable webhooks**, paste the
   Webhook URL, and save.
3. Reload the Kick app page and check the switch stayed on.

That URL contains a secret: treat it like a password. polyemesis shows it only
to a signed-in operator.

polyemesis then subscribes the app to chat messages itself and re-checks every
30 minutes. If webhooks are off, Kick refuses the subscription and the Kick
chat card says "Kick webhook not enabled for your app".

**If you switched webhooks off and on again,** restart polyemesis, or
disconnect and reconnect Kick. polyemesis trusts an existing Kick subscription
for up to 6 hours, so it will not re-subscribe sooner on its own, and Kick
drops the subscriptions when webhooks go off. Messages sent while webhooks were
off are not redelivered.

---

## YouTube

Connecting is verified end to end on a live server. The go-live broadcast is
checked after each deploy; see [YouTube go-live
broadcasts](PLATFORMS.md#youtube-go-live-broadcasts). The steps follow
Google's console and [PLATFORMS.md](PLATFORMS.md#youtube-google).

1. In a [Google Cloud project](https://console.cloud.google.com/apis/credentials),
   enable **YouTube Data API v3** under **APIs & Services → Library**.
2. Configure the **OAuth consent screen** as **External**. While the app is in
   **Testing**, add your Google account as a **test user**.
3. Create an OAuth client of type **Web application** with redirect URL
   `https://polyemesis.example.com/api/v1/oauth/youtube/callback`.
4. Paste the **Client ID** and **Client secret** into **Settings → Platform
   credentials → YouTube**, press **Connect account**, and pick the channel.

Google's sign-in shows an "unverified app" warning while the app is in
Testing; continue past it, since you wrote the app.

**In Testing, Google's refresh tokens expire after 7 days,** so the account
must be reconnected weekly. Setting the publishing status to **In production**
removes the expiry, and an app only you sign in to does not need Google's
verification review for that. YouTube chat is metered; see [What YouTube chat
costs](PLATFORMS.md#what-youtube-chat-costs).

---

## Facebook (being verified)

This setup is also being verified on a live server, and two points are open
(below).

1. At <https://developers.facebook.com/apps> create an app with the **Live
   Video API** use case.
2. Add the redirect URL
   `https://polyemesis.example.com/api/v1/oauth/facebook/callback` to the app's
   Facebook Login settings.
3. Copy the **App ID** and **App secret** from **App settings → Basic** into
   **Settings → Platform credentials → Facebook**.
4. Press **Connect account** and pick your profile or Page.

Open points: polyemesis signs in with scope-based Facebook Login
(`public_profile`, `publish_video`, `pages_show_list`, `pages_manage_posts`,
`pages_read_engagement`). An app set up with **Facebook Login for Business**
may need a configuration instead of scopes; that is not yet verified. Meta
also requires App Review before anyone but you can connect an account; your own
account works at once as a developer or tester of your own app. See
[PLATFORMS.md](PLATFORMS.md#facebook-live-meta).

---

## Chat actions by platform

What you can do in a platform's chat from polyemesis, checked against the
code and against each platform's published API on 2026-11-07.

| Action | Twitch | Kick | X | YouTube | Facebook | Rumble |
|---|---|---|---|---|---|---|
| Read chat | ✅ | ✅ (needs webhooks) | ✅ (webhook + app bearer) | ✅ | ✅ (comments) | ✅ |
| Send | ✅ 500 chars | ✅ 500 chars | ✅ 140 chars ⚠️ | ✅ 200 chars | ❓ | 🚫 |
| Reply to a message | ❓ | ✅ (as you, or as the bot) | ❓ | ❓ | ❓ | 🚫 |
| Delete message | ✅ | ✅ | ✅ ⚠️ | ✅ | ✅ | 🚫 |
| Hide on platform | 🚫 | 🚫 | 🚫 | 🚫 | ✅ | 🚫 |
| Time out | ✅ | ✅ | ✅ timed mute ⚠️ | ✅ temporary ban | 🚫 | 🚫 |
| Ban / unban | ✅ | ✅ | ✅ mute / unmute ⚠️ | ✅ | ✅ ⚠️ block / unblock the Page's | 🚫 |
| Mute and delete a message in one step | ❓ | ❓ | ✅ ⚠️ | ❓ | ❓ | 🚫 |
| Native poll | ✅ | 🚫 | 🚫 | ✅ | ✅ ⚠️ | 🚫 |
| Pin a message | ✅ ⚠️ | 🚫 | 🚫 | 🚫 | 🚫 | 🚫 |
| Announcement, shoutout, warn | ✅ ⚠️ | 🚫 | 🚫 | 🚫 | 🚫 | 🚫 |
| Moderators | ✅ ⚠️ (owner only) | 🚫 | ✅ ⚠️ | ✅ ⚠️ (channel owner only) | 🚫 | 🚫 |
| VIPs | ✅ ⚠️ (owner only) | 🚫 | 🚫 | 🚫 | 🚫 | 🚫 |
| Banned and muted lists | ✅ ⚠️ banned list | ❓ | ✅ ⚠️ muted users | ❓ | ❓ | 🚫 |
| Chat settings | ✅ read + change (slow, follower, subscriber, emote, unique, moderator delay) | 🚫 | ❓ | 🚫 | 🚫 | 🚫 |
| Shield Mode, blocked terms, AutoMod, unban requests, chatters | ✅ ⚠️ | 🚫 | 🚫 | 🚫 | 🚫 | 🚫 |

- ✅ built in polyemesis.
- ⚠️ built, not yet tested live. As of 2026-11-07 that is X send, delete and
  mute (and mute-and-delete, muted users, moderators); the Twitch channel tools
  (pin, announcement, shoutout, warn, Shield Mode, blocked terms, chatters,
  AutoMod, moderators and VIPs, banned list, unban requests); YouTube moderators;
  and Facebook block, unblock and native polls. Twitch and Kick moderation and
  all of YouTube and Facebook are still to be live-tested.
- 🔧 the platform's API offers it and polyemesis does not do it yet.
- 🚫 the platform's API doesn't offer it.
- ❓ the platform's docs don't say, or polyemesis does not do it and the docs
  leave open whether it could. Facebook send is one: the live-video comments
  edge says "You can't perform this operation on this endpoint", and the
  generic Video comments edge documents a POST nobody has shown works on a
  live video, so polyemesis does not send to Facebook chat.

Not every row is a button in the console. The Twitch channel tools are served
from `/chat/twitch/{resource}` in the [API](API.md); what each needs, and the
Twitch scopes behind them, is in [PLATFORMS.md](PLATFORMS.md#twitch).

How the table plays out in practice:

- **Hide locally works everywhere.** It removes a message from the console and
  the overlays only; the platform's own chat still shows it. Where the table
  says 🚫 for *Hide on platform*, local hiding is the way to keep a message off
  your stream.
- **A send to several platforms uses the strictest limit.** With X connected
  that is 140 characters, so the same text goes out everywhere.
- **Automod acts only where the table shows ✅.** Anywhere else it flags the
  message or hides it locally. That includes the recommended-protection pack:
  it is on by default and only flags and holds lines off the overlays, and each
  platform's delete, time-out and ban is a switch you arm yourself (Settings,
  Pipeline, Recommended protection). A rung the platform cannot perform is shown
  inert with the reason. Only Twitch also has its own filters the pack can fill
  (blocked terms and the AutoMod level), because Twitch's filters see only
  Twitch chat. See [Automatic moderation](AUTOMOD.md#the-recommended-protection-pack),
  including the staging runbook for a first live test on a platform that is not
  yet verified.
- **Where there is no native poll, polls run from chat votes.** Twitch and
  YouTube get a native poll; every other chat is asked to type a number.
- **An X mute belongs to the broadcaster**, so it applies to all of that
  account's broadcasts, and a timed mute ends on its own.
- **Kick timeouts run from 1 minute to 7 days.**
- **Facebook can block but not time out.** The Page's block list has no
  duration, so a ban there is permanent until you lift it.
- **A refused action says so.** A platform's refusal or outage is reported
  with its reason rather than dropped, and a revoked login shows the account as
  needing a reconnect.
- **Twitch, X and YouTube moderator changes are the owner's.** Twitch refuses
  moderators and VIPs from anyone but the channel's owner, and YouTube lets only
  the chat's channel owner manage its moderators.

Sources checked on 2026-11-07:

- Twitch: [Helix reference](https://dev.twitch.tv/docs/api/reference/)
- Kick: [Chat](https://docs.kick.com/apis/chat) and
  [Moderation](https://docs.kick.com/apis/moderation) in the Kick public API docs
- X: [Livestream API](https://docs.x.com/livestream-api/introduction), chat
  endpoints
- YouTube: [Live Streaming API reference](https://developers.google.com/youtube/v3/live/docs)
- Facebook: Graph API reference for
  [Comment](https://developers.facebook.com/docs/graph-api/reference/comment/),
  [Page blocked](https://developers.facebook.com/docs/graph-api/reference/page/blocked/),
  [Live Video polls](https://developers.facebook.com/docs/graph-api/reference/live-video/polls/)
  and [Page settings](https://developers.facebook.com/docs/graph-api/reference/page/settings/)
- Rumble: [Live Stream API](https://rumblefaq.groovehq.com/help/how-to-use-rumble-s-live-stream-api)

---

## Alerts and chat export

Once chat is connected, two more things draw on it. **Alerts** (follows,
subscriptions, tips and the like, from all six platforms) are covered in
[ACTIVITY.md](ACTIVITY.md), including what each platform needs for them and
which are not yet checked against a live account. **Chat export** saves each
recording's chat beside it as JSON and WebVTT; see
[CHAT-EXPORT.md](CHAT-EXPORT.md).

---

## When something is empty or refused

| You see | Likely cause | Fix |
|---|---|---|
| X chat pane empty | `POLYEMESIS_X_APP_BEARER` not set, the server is not reachable on 443, or the webhook shows as invalid in the X console | Set the bearer and restart; test the hostname from outside; the pane's message says which is missing |
| Kick chat pane empty | Webhooks off in the Kick app, or the pasted URL differs from the one polyemesis shows | Re-copy the Webhook URL, switch **Enable webhooks** on, save, then restart polyemesis |
| "redirect mismatch" at sign-in | You browsed by IP or by a different hostname than the one registered | Open the dashboard at the hostname in your redirect URL |
| YouTube disconnects every 7 days | The Google app is in Testing | Reconnect, or publish the app to production |

Anything else: [TROUBLESHOOTING.md](TROUBLESHOOTING.md).
