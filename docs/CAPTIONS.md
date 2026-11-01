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
| YouTube | Caption ingestion (HTTP POST), or in-stream CEA-608 | Both are built. The POST route is provisional (YouTube does not publish its body format), needs a 30 s or 60 s delay, and needs one Refresh stream key to fetch its caption URL. See *Routes in detail*. |
| Facebook | In-stream CEA-608 | Provisional. Off until the server is started with `POLYEMESIS_CAPTIONS_FACEBOOK=1`. |
| Kick | None | Kick documents no caption support for live streams, so captions are not offered. |

Every destination starts with captions **off**. An upgrade never turns them on.

**Per programme: the caption track.** Open a programme in *Sources* and choose
*Caption track*. The captioner listens to that audio track. *Automatic* is the
choice the captioner makes today, and it is the default.

## Which routes reach the stream

The route and the track are stored, and the console and the API show them.

A Twitch route on *In-stream CEA-608 (SEI)* reaches the stream. Saving that
route restarts the destination, the same as a delay change, so the captions
start with the next picture of the restarted feed, and they follow the
destination's broadcast delay. Twitch's display of the captions on your channel
is checked live, not in this build.

A YouTube route on *caption ingestion (HTTP POST)* posts each caption to the
caption ingestion URL of the destination's own YouTube stream. The video path is
not touched, so saving or changing this route never restarts the destination.
Facebook's route is not sent to the stream yet; saving it changes nothing on air.

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
to a signed caption ingestion URL that belongs to the destination's YouTube
stream. This route is provisional: YouTube does not publish the body format, so
it is checked against a live ingest before you rely on it. Setting it up takes
four steps:

1. Choose *YouTube caption ingestion* for the destination, set its delay to 30
   or 60 seconds, and save.
2. Press **Refresh stream key**. YouTube publishes the caption URL only when a
   stream is created, so this creates the destination's own YouTube stream and
   stores its URL. **The destination's stream key changes once.** If your
   encoder publishes to YouTube directly, paste the new key into it. The console
   says *A caption ingestion URL is stored* when this has worked.
3. Schedule the show from polyemesis (a schedule with pre-announce, or a
   recurring event). Polyemesis creates the broadcast for captions: caption type
   *HTTP POST*, monitor stream on, and the destination's 30 s or 60 s delay.
   YouTube allows these only before a broadcast goes to testing or live, so they
   are set when the broadcast is created.
4. Go live. Each caption is posted when its delay has passed, which is when the
   matching video reaches YouTube.

What polyemesis refuses, and why:

- **Starting the destination with no caption URL.** A destination on this route
  will not start until a URL is stored for its current stream key (the API
  answers 409 and names Refresh stream key). A key typed over by hand belongs to
  a different stream, so its old URL no longer counts. Turning the route off
  deletes the stored URL.
- **Going live on a broadcast that is not set for POST captions.** Before a
  scheduled broadcast is moved to testing or live, polyemesis reads it. If its
  caption type is not *HTTP POST*, its monitor stream is off, or its delay is not
  30 or 60 seconds, the broadcast is left where it is and the destination card
  shows the reason. The stream keeps going out. Schedule the show again from
  polyemesis, or set captions to off or embedded.
- **Ultra-low latency** does not apply. YouTube does not allow captions with
  ultra-low latency, but polyemesis never sets a latency preference, so the
  broadcasts it creates use YouTube's default.

What polyemesis does not check: a show started without a scheduled broadcast
(YouTube's default *Stream now* event, or a broadcast made in YouTube Studio)
has no broadcast id for polyemesis to read. Captions are still posted, but they
appear only if that broadcast is set for HTTP POST captions in YouTube Studio.

The caption URL is a credential: anyone holding it can post captions to the
broadcast. It is stored encrypted, apart from the destination, and it is never
shown in the console, the API, logs or events. A refused post is logged at most
once a minute per destination, without the URL. If the ingest is slow, each
destination keeps its newest 16 captions and drops the oldest.

The broadcast delay counts twice on this route: once in polyemesis (the
destination's own delay, which keeps the captions with the picture) and once on
YouTube (the broadcast delay YouTube needs for this route). Viewers can see the
show up to about two minutes after it happens at a 60 s setting.

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

## How the captioner is driven

The captioner runs for a programme when at least one of its enabled destinations
has a caption mode that its platform accepts. It transcribes the track the
programme chose (automatic when none is chosen). Turning every captioned
destination off stops it. A destination's broadcast delay decides when its
captions are released, so a destination set to 30 seconds receives each caption
30 seconds after it was spoken, which keeps the words with the picture.

Three safety rules apply:

- If the stream health guard stops captioning to protect the broadcast, it stays
  stopped through later reconciles. It starts again only after you save a
  caption-relevant setting (a mode, a delay or the track).
- If the captioner stops on its own (the audio tap or the relay goes away, or
  speech recognition crashes), the dashboard shows it stopped straight away and
  it is started again. If it stops within 30 seconds of starting three times in
  a row, captions are switched off with a warning instead of being restarted
  over and over. A run that lasts longer than 30 seconds resets the count, and
  so does turning captions on again.
- Stopping the captioner cancels every caption still waiting for a destination's
  delay. Nothing is sent after the stop.

An automatic track stays automatic while captions run, so a source that changes
its audio layout gets the track the new layout offers. The status shows the
track, speaker and model the captioner is actually using. Changing any part of
the caption configuration, including the thread count or the health limits,
restarts the captioner with the new values. Nothing else restarts it.

Each new caption replaces the one on screen. A caption that is still showing when
the next one starts is not erased or shown again later.

**Current state:** the engine releases each caption to every captioned
destination at its delay. A Twitch destination carries it in the video as SEI,
and a YouTube destination on the HTTP POST route posts it to its caption URL.
The custom vocabulary and the profanity filter exist
as a tested policy in code, but no operator setting stores them yet, so the
policy is empty and captions are shown as recognised.

## Verifying a route on your channel

Each route marked provisional or live-check needs one test on a real channel
before you rely on it: start a short stream with a test key, confirm the captions
appear on the platform's page, and stop. Do this with a key minted for the test,
not your production key.
