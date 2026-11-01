# Live captions

Live captions are text shown on a stream as it goes out. Polyemesis transcribes
one audio track of a programme on this machine. The text is meant to reach each
destination by the route that destination accepts. This page covers the two
settings you choose today, what each route is, and what is not yet sent.

## What you choose

**Per destination: the caption route.** Open a destination and choose a route
under *Live captions*. The choices depend on the platform:

| Platform | Routes offered | Status |
|---|---|---|
| Twitch | In-stream CEA-608 (SEI) | Built. Twitch documents the route; display on your channel is checked live. |
| YouTube | Caption ingestion (HTTP POST), or in-stream CEA-608 | The in-stream route is built. The POST route is provisional, and needs a 30 s or 60 s broadcast delay on that destination. |
| Facebook | In-stream CEA-608 | Provisional. Off until the server is started with `POLYEMESIS_CAPTIONS_FACEBOOK=1`. |
| Kick | None | Kick documents no caption support for live streams, so captions are not offered. |

Every destination starts with captions **off**. An upgrade never turns them on.

**Per programme: the caption track.** Open a programme in *Sources* and choose
*Caption track*. The captioner listens to that audio track. *Automatic* is the
choice the captioner makes today, and it is the default.

## Settings are saved; the stream is not yet changed

The route and the track are stored, and the console and the API show them. The
engine does not send captions yet, so choosing a route changes nothing on air at
this stage. The console says so beside the picker.

The save is refused, with a reason, in three cases:

- the platform does not take that route (for example, any route on Kick);
- YouTube's caption POST is chosen at a delay other than 30 s or 60 s;
- a route changes while the destination is live. Stop the destination first.

## Routes in detail

**In-stream CEA-608 (SEI).** The captions are carried inside the video, as
CEA-608 data in an H.264 SEI message. The platform reads them from the stream,
so no separate upload is needed. Polyemesis writes the data into the copied
video without re-encoding it.

**YouTube caption ingestion (HTTP POST).** YouTube takes captions as text posted
to a signed ingestion URL that belongs to each broadcast. YouTube documents that
the broadcast needs a 30 s or 60 s delay for this route. The exact body format
is not published, so this route is provisional until it is checked against a
live ingest.

**Facebook in-stream.** Facebook documents that CEA-608 can be added to a live
stream, but not the carriage in the SEI. It stays off until a live check passes.

## Limits you should know

- Captions are timed to picture, so they lag speech by about two to five
  seconds. A broadcast delay (see [Broadcast delay](BROADCAST-DELAY.md)) keeps
  them aligned with the picture.
- A viewer who joins an HLS stream mid-caption can miss the caption that was
  already on screen. Re-sending the caption periodically reduces that gap.
- Burn-in (captions drawn into the picture of a rendition) needs an FFmpeg build
  with `libass`. It is not wired yet.

## Verifying a route on your channel

Each route marked provisional or live-check needs one test on a real channel
before you rely on it: start a short stream with a test key, confirm the captions
appear on the platform's page, and stop. Do this with a key minted for the test,
not your production key.
