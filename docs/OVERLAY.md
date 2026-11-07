# Chat overlay and OBS dock

**One chat overlay for every platform, as an OBS browser source, plus a dock
for reading chat inside OBS and an [alerts overlay](#alerts-overlay) for
follows, subs, tips and raids. All are display-only.**

Set them up under **Settings → Chat overlays**.

## Create an overlay

1. Give the overlay a name and select **Create overlay**.
2. Choose the platforms it shows. With none selected, it shows chat from every
   connected platform.
3. Pick a style preset, adjust the settings, and watch the live preview.
4. Select **Save overlay**.
5. Select **Copy OBS URL**. In OBS, add a **Browser** source, paste the URL, and
   set the size to your canvas (for example 1920 × 1080).

The page background is transparent, so only the chat lines show over your
scene. You don't need any custom CSS in OBS.

For the dock, select **Copy dock URL**. In OBS, open **Docks → Custom Browser
Docks**, then paste the URL. The dock uses a denser layout on a solid
background and keeps the last 200 lines.

If the overlay stays empty in OBS, your OBS may be dropping the part of the
URL after `#`, which is where the key is. Use **Copy OBS URL (key in path)** or
**Copy dock URL (key in path)** instead. See
[The key in the path](#the-key-in-the-path) for the trade-off.

Restyling an overlay doesn't change its URL. An open overlay picks up the new
style the next time it reconnects; you can force that by refreshing the source
in OBS.

## The poll widget

Each overlay also has a poll widget: a second OBS browser source, on the same
key and style, that draws the running [chat poll](POLLS.md) and nothing else.
Select **Copy poll widget URL** and add it as its own Browser source, so you
can place it apart from the chat lines. Its path is `/overlay/poll`, or
`/overlay/poll/k/{key}` with the key in the path.

The widget shows totals only, with no platform logos or per-platform
breakdown, so it appears on every overlay key whatever its platform filter.
It never shows who voted.

## Alerts overlay

A third OBS browser source on the same key draws **viewer activity**: a new
follower, a sub, a gift, a tip, a raid, a channel-points redemption, a hype
train, a shoutout. Select **Copy alerts overlay URL** and add it as its own
Browser source, so you can place it apart from the chat lines. Its path is
`/overlay/alerts`, or `/overlay/alerts/k/{key}` with the key in the path. What
the events are and where they come from is in [ACTIVITY.md](ACTIVITY.md).

It follows the same rules as the chat overlay:

- **The page is transparent.** Only the alert shows over your scene.
- **The platform filter applies.** A Twitch-only overlay is never sent a Kick
  follow. The platform name is shown as text.
- **Words and names are judged before they go on air.** A viewer's message is
  held until automod has passed it. If automod removes it, or gives no answer
  within four seconds, the alert **still shows, without the message**. A name
  that matches your blocked terms is shown as "Someone". The names are checked
  against the blocked terms only; the model checker is asked about messages, not
  names. You can also turn viewer messages off for the overlay entirely under
  **Activity → Settings**.
- **Nothing replays on reload.** If OBS reloads the source, it is not played the
  last few minutes of alerts again: an alert is replayed to a reconnecting
  overlay only if it is under 10 seconds old.
- **Only the overlay kinds are drawn:** follows, subs, resubs, gifts, tips,
  raids in, redemptions, hype trains and shoutouts. A moderation action, the
  platform's stream online/offline report, a native poll or prediction, a
  reaction tally, and a raid you sent *out* never reach an overlay.
- **No ids.** The overlay is sent a name, an amount and a few numbers, never a
  platform account id, an actor id or a source id.

Alerts queue on the page and play one at a time, each for the overlay's
**Seconds** (3 to 15). The queue holds 50; when it is full the oldest follow
goes first, and a tip is never dropped. More than five follows in ten seconds
are shown as one "N new followers" alert.

### Alerts style

The overlay's style gains an optional `alerts` block (the chat style fields are
unchanged):

| Field | Values |
|---|---|
| `layout` | `banner`, `card`, `minimal` |
| `kinds` | the kinds this overlay draws, from the nine above |
| `seconds` | 3 to 15 |
| `showMessage` | draw the viewer's message, if it was released |
| `showAmount` | draw the amount of a tip |
| `sound` | `none`, `chime`, `pop` or `bell`: tones the page makes itself. There is no sound URL. |
| `accent` | `#rrggbb`, lower case |

With no `alerts` block the overlay draws every kind as a banner for six seconds,
silently. **Downgrading:** an older polyemesis refuses to read an overlay saved
with an `alerts` block, because it rejects style fields it does not know; remove
the block, or restore the overlay from before the upgrade, if you go back.

### Try it without a platform

**Send test alert** (on the overlay editor beside **Copy OBS URL**, and under
**Activity → Settings**) sends a canned alert of any kind through the same path
a real one takes, with no account connected and no Affiliate or monetisation
tier needed. It obeys your settings, so a kind you switched off does not show,
and it carries a `TEST` mark in the console. Use it to choose a layout and a
sound, and to check the platform filter.

## Platform filter: a Twitch-safe scene

Each overlay has its own platform filter. Some platforms don't want other
platforms' chat or logos on their stream, so make one overlay per audience:

- a **Twitch** overlay with only Twitch selected, for the scene you send to
  Twitch;
- an **All** overlay with nothing selected, for everywhere else.

Then use one in each OBS scene. The filter is applied on the server, so a
filtered overlay never receives another platform's messages. The platform
mark (TTV, YT, KICK and so on) is text, not a logo.

## Styles

There are four presets:

| Preset | Look |
|---|---|
| **Classic** | Plain lines with a soft shadow, names in each viewer's colour |
| **Bubbles** | Each line in a rounded, semi-transparent bubble |
| **Bold outline** | Large heavy text with a black outline, readable over anything |
| **Compact strip** | Small monospace lines in one translucent strip |

Choosing a preset resets the settings below it. You can then adjust:

| Setting | Range |
|---|---|
| Font | Sans serif, serif, monospace or rounded |
| Font size | 10–72 px |
| Text and background colour | any colour |
| Background opacity | 0–100 % |
| Fade after | 0–600 seconds; 0 keeps a line until newer ones push it off |
| Maximum lines | 1–50 |
| Name colours, platform mark, role dots | on or off |

## What an overlay never shows

An overlay shows a message only after automatic moderation has judged it:

- **Automod off, or only the free checkers:** a message is judged as it
  arrives, so it appears immediately. A message the rules or history checkers
  will hide, delete, time out or ban never appears at all.
- **The model checker on:** each message waits for the model's verdict for up
  to **4 seconds**. A message the model removes never appears. If the verdict
  hasn't arrived after 4 seconds, the overlay leaves that message out. The
  console still shows it. If the model endpoint fails, messages pass, just as
  they do in the console.

If you hide or delete a message, or ban its author, after it's on screen, it
leaves every overlay straight away. An overlay that reconnects, for example
after OBS reloads the source, is never sent a removed message again.

Each line carries the viewer's display name, colour, role dots and text. It
carries no user IDs, account names or moderation data. Emote images aren't
drawn; their names stay in the text.

**Settings → Chat overlays** shows counters since the server started: lines
shown, lines held back by moderation, and lines dropped while waiting for
automod.

## The overlay key

An OBS URL looks like this:

```
https://your-server/overlay#key=pov_…
```

The key is the part after `#`. Browsers never send that part to a server, so
it never appears in the server's logs, a proxy's logs or a `Referer` header.
The overlay page sends it to the server in a request header instead.

### The key in the path

Some OBS browser sources drop the part after `#`, which leaves the URL above
with no key, and the overlay stays empty. For those, Settings also offers the
key in the path:

```
https://your-server/overlay/k/pov_…
https://your-server/overlay/dock/k/pov_…
```

The page reads the key from its own address and sends it in the same request
header, so the key works exactly as it does in the `#` form. What changes is
who sees the address:

- **Polyemesis's own log never records it.** A request that matches the route
  is logged as `/overlay/k/{key}`, and any other path has `pov_` keys replaced
  with `pov_[redacted]` before it is logged.
- **No `Referer` carries it.** The page is sent with
  `Referrer-Policy: no-referrer`.
- **A reverse proxy in front of polyemesis can log it.** nginx, Caddy and
  Traefik write the request path to their own access logs by default, and
  polyemesis can't reach those. This is why the `#` form stays the default.

Use the path form only if the `#` form doesn't work in your OBS. If you run a
proxy and use it, treat the proxy's access log as holding the key, or rotate
the key if that log is shared.

### What the key can reach

The key can read only two things: the overlay's own style and its filtered
display stream. It isn't an API token. It can't call any other API route, open
the WebSocket or read `/api/v1/events`. If it leaks, it shows the holder the
chat that overlay was already putting on your stream, and nothing else.

You can't manage overlays with an API token: listing, creating and changing
them, and rotating keys, need a signed-in session.

### Rotate or revoke

- **Rotate key** issues a new key. The old URL stops working within seconds
  and any open overlay goes blank. Paste the new URL into OBS.
- **Revoke key** removes the key, keeping the overlay and its style. To use it
  again, select **Issue a new key**.
- **Delete overlay** removes the overlay and its key.

Changing your password doesn't revoke overlay keys. Rotate them separately if
you need to.

## Endpoints

Overlays are managed with these routes, which need a signed-in session:

| Method | Path | Does |
|---|---|---|
| `GET` | `/api/v1/overlays` | Lists overlays, presets and counters |
| `POST` | `/api/v1/overlays` | Creates one: `{name, platforms, style}` |
| `PUT` | `/api/v1/overlays/{id}` | Replaces its name, platforms and style |
| `DELETE` | `/api/v1/overlays/{id}` | Deletes it |
| `POST` | `/api/v1/overlays/{id}/key` | Rotates its key |
| `DELETE` | `/api/v1/overlays/{id}/key` | Revokes its key |

The overlay key reaches these two routes, sent in `X-Overlay-Key`:

| Method | Path | Returns |
|---|---|---|
| `GET` | `/api/v1/overlay/config` | `{name, platforms, style}` |
| `GET` | `/api/v1/overlay/events` | Server-Sent Events: `chat.display`, `chat.undisplay`, `poll.display` and `activity.display` only |

The event payloads are described in [EVENTS.md](EVENTS.md#overlay).

## Limitations

- A newly opened overlay starts empty. It shows messages that arrive after it
  connects.
- Polls are drawn by the separate [poll widget](#the-poll-widget), and viewer
  activity by the separate [alerts overlay](#alerts-overlay), not on the chat
  overlay.
- Activity is kept in memory and not stored. An alert that happened while no
  overlay was connected is not shown later, and a restart forgets the console's
  activity feed.
- The overlay was tested in headless Chromium, the engine OBS's browser source
  uses, with a transparent background at 1920 × 1080. It hasn't been tested in
  OBS itself.
