# Chat overlay and OBS dock

**One chat overlay for every platform, as an OBS browser source, plus a dock
for reading chat inside OBS. Both are display-only.**

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

Restyling an overlay doesn't change its URL. An open overlay picks up the new
style the next time it reconnects; you can force that by refreshing the source
in OBS.

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
| `GET` | `/api/v1/overlay/events` | Server-Sent Events: `chat.display` and `chat.undisplay` only |

The event payloads are described in [EVENTS.md](EVENTS.md#overlay).

## Limitations

- A newly opened overlay starts empty. It shows messages that arrive after it
  connects.
- Alerts and polls aren't part of the overlay.
- The overlay was tested in headless Chromium, the engine OBS's browser source
  uses, with a transparent background at 1920 × 1080. It hasn't been tested in
  OBS itself.
