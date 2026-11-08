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
| Facebook | In-stream CEA-608 | Provisional. Off until the server is started with `POLYEMESIS_CAPTIONS_FACEBOOK=1` (or `true`). |
| Kick | None | Kick documents no caption support for live streams, so captions are not offered. |

Every destination starts with captions **off**. An upgrade never turns them on.

**Per programme: the caption track.** Open a programme in *Sources* and choose
*Caption track*. The captioner listens to that audio track. *Automatic* is the
choice the captioner makes today, and it is the default.

**Per programme: caption words.** Under *Caption words* on the programme, list
words one per line (or comma-separated) and press *Save caption words*:

- *Spell these words my way.* A recognised word that matches one, ignoring
  case, is written in your spelling (`polyemesis` becomes `Polyemesis`). It
  fixes case and accents only; it cannot make the recogniser hear a word it
  missed.
- *Mask these words.* A masked word keeps its first letter and the rest become
  asterisks (`darn` shows as `d***`).

Both lists hold single words: letters, digits and apostrophes inside a word.
A phrase, a hyphenated term, more than 200 entries or an entry over 48
characters is refused with the reason, because the captioner matches word by
word and such an entry could never match. The words are applied once, where
captions leave the captioner, so the dashboard, the player track, SEI, YouTube
POST, burned-in captions and the exports all show the same text. A save reaches
captions that are already running from the next caption, without restarting
speech recognition.

**Per rendition: burned-in captions.** A rendition (*Renditions*, edit a tier)
can draw the programme's live captions into its picture, *At the bottom* or *At
the top*, for a platform or player that shows no caption track. See
*Burned-in captions* below.

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
Facebook's in-stream route goes through the same SEI stage as Twitch's, but only while the
server runs with `POLYEMESIS_CAPTIONS_FACEBOOK=1` (or `true`); without it the route cannot be saved.

The save is refused, with a reason, in three cases:

- the platform does not take that route (for example, any route on Kick);
- YouTube's caption POST is chosen at a delay other than 30 s or 60 s;
- a route changes while the destination is live. Stop the destination first.

A change that adds or removes the in-stream (SEI) route changes the
destination's video path, so it restarts an enabled destination that is not
live yet. A change between off and YouTube's HTTP POST touches no video and
restarts nothing, unless the destination has a delay: then the delay moves
between polyemesis and YouTube, and the output restarts to gain or lose its
buffer. The picker says which applies.

## The player and the recordings

**Public player, live.** The captioner writes `captions.vtt` beside the HLS
window, and the public player (`/watch`) adds it as a *Captions* track that is
on by default (the viewer turns it off in the player's controls). The file
names its epoch in a header line, `X-POLYEMESIS-EPOCH-MS`, and places each
caption at the moment its words were spoken. The player maps that onto the
picture through the playlist's `EXT-X-PROGRAM-DATE-TIME`, so the captions line
up live and after a seek back through the DVR window. The player fetches the
file every 4 seconds, and every 15 seconds while it is missing. It is not an
HLS subtitle rendition: a player other than polyemesis's own sees the captions
only if it reads the file itself.

**Recordings.** Every live caption is also kept in the programme's cue log,
`<data>/captions/cues-source-<id>.jsonl`, with the wall-clock time it was
spoken. The log is private to the server's user and rolls over once past
32 MiB, so at most 64 MiB is on disk per programme. In the *Library*, a
recording's player shows a *Captions* track: the stored transcript when there
is one, otherwise the live captions lined up with the recording's own timeline.
A recording's start is stored to the second, so live captions on a recording
can be up to a second early or late.

**Exports.** Beside the transcript, *Export captions* offers SRT, VTT and JSON:

- *Transcript*: the transcript made from the recording, with the programme's
  caption words applied;
- *As shown live*: the captions viewers saw during the broadcast, lined up with
  this recording. A caption that straddles the start or the end is clipped to
  the recording.

Every file names the speaker on each cue. The API is
`GET /api/v1/library/recordings/{id}/transcript/export?format=srt|vtt|json`,
with `&source=live` for the live captions. A read-only token is refused, as for
the transcript itself.

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
4. Go live. YouTube holds the broadcast back by its delay, and polyemesis sends
   this destination's video without a delay of its own. Each caption is posted
   as soon as it is recognised, stamped with the moment its words were spoken,
   which is when the matching video reached YouTube. YouTube lines the two up
   inside its delay (PROVISIONAL: see below).

What polyemesis refuses, and why:

- **Starting the destination with no caption URL.** A destination on this route
  will not start until a URL is stored for its current stream key (the API
  answers 409 and names Refresh stream key). A key typed over by hand belongs to
  a different stream, so its old URL no longer counts. Turning the route off
  deletes the stored URL.
- **Going live on a broadcast that is not set for POST captions.** Before a
  scheduled broadcast is moved to testing or live, polyemesis reads it. If its
  caption type is not *HTTP POST*, its monitor stream is off, its delay is not
  30 or 60 seconds, or it is set to ultra-low latency, the broadcast is left
  where it is and the destination card shows the reason. The stream keeps going
  out. While the broadcast is still upcoming, fix those settings in YouTube
  Studio, or set captions to off or embedded. Rescheduling does not fix it: polyemesis keeps the broadcast's
  caption settings as they are when it moves the start time.
- **A show announced before the first Refresh stream key** stays bound to the
  stream it was announced on, and the refresh moves the destination to a new
  stream. Do the refresh before you schedule shows for this destination.
- **Creating a destination already switched on with this route.** It has no URL
  yet, so create it switched off, press Refresh stream key, then start it. The
  console always creates destinations switched off.
- **Ultra-low latency.** YouTube does not allow captions with ultra-low
  latency. polyemesis never sets a latency preference, so the broadcasts it
  creates use YouTube's default. A broadcast made in YouTube Studio can be set
  to ultra-low latency, and the go-live check above holds it back with that
  reason. Choose normal or low latency in Studio.

A destination polyemesis opens a broadcast for at go-live (the default for a
YouTube destination with a connected account) is created with the HTTP POST
caption settings, the monitor stream and the 30 s or 60 s delay, so it passes
the check above.

What polyemesis does not check: a show started without a broadcast of its own
(a destination switched to YouTube's *Stream now*, or a broadcast made in
YouTube Studio) has no broadcast id for polyemesis to read. Captions are still posted, but they
appear only if that broadcast is set for HTTP POST captions in YouTube Studio.

The caption URL is a credential: anyone holding it can post captions to the
broadcast. It is stored encrypted, apart from the destination, and it is never
shown in the console, the API, logs or events. A refused post is logged at most
once a minute per destination, without the URL. If the ingest is slow, each
destination keeps its newest 16 captions and drops the oldest.

**The delay on this route is YouTube's.** The destination's 30 s or 60 s delay
is sent as the broadcast's delay, and YouTube holds viewers back by it.
polyemesis does not hold this destination's output back as well, so viewers are
about 30 or 60 seconds behind, not twice that. Other destinations of the same
programme keep their own delays. The card still shows the delay, because it is
how far behind viewers are. Some consequences:

- Changing the delay between 30 s and 60 s does not restart the output. It
  takes effect on the next broadcast polyemesis creates, because YouTube fixes
  a broadcast's delay once it is testing or live.
- Switching a delayed YouTube destination to or from this route moves its delay
  between YouTube and polyemesis, so the output restarts to gain or lose its
  buffer. The editor says so before you save.
- Stop does not air a delayed tail from polyemesis on this route, because there
  is no buffer here. YouTube still holds its last 30 or 60 seconds when the
  broadcast ends. That was already true before this change.

PROVISIONAL: YouTube does not document the POST body. polyemesis stamps each
caption with the wall-clock moment its words were spoken, which the third-party
description says YouTube matches against the media it ingested. If the live
check shows YouTube expects a different clock, only the stamp changes.

**Facebook in-stream.** Facebook documents that CEA-608 can be added to a live
stream, but not the carriage in the SEI. It stays off until a live check passes.

## Limits you should know

- Captions are timed to picture, so they lag speech by about two to five
  seconds. A broadcast delay (see [Broadcast delay](BROADCAST-DELAY.md)) keeps
  them aligned with the picture.
- A viewer who joins a platform's HLS stream after a caption began would miss
  it, because a CEA-608 pop-on caption is shown at its End Of Caption. The SEI
  route re-sends the caption on screen every 2 seconds (one Twitch segment), up
  to 32 times per caption, so a joiner sees it within a segment.
- Burned-in captions lag speech by the same two to five seconds as every other
  route.
- The SEI route captions H.264 only. An HEVC source passes through unchanged,
  without captions, and the server log says so for that destination.
- A YouTube broadcast taking captions by HTTP POST keeps them through a planned
  rollover: see [ROLLOVER.md](ROLLOVER.md).
- The HTTP POST sequence number (`seq`) starts from the clock rather than from 1,
  so captions posted after a restart mid-broadcast never reuse a number already
  sent (PROVISIONAL: YouTube does not document the parameter).
- The player's caption track reads only what was added to `captions.vtt` since
  its last poll (an HTTP range request), and the whole file once a minute.

## Burned-in captions

A rendition set to burn captions in draws the programme's live captions into its
picture with FFmpeg's `drawtext`, in the built-in Inter font on a translucent
box, two lines at most, the newest words last. Each caption stays for as long
as its words took (at least 2 seconds) and is then cleared.

How it works: the captioner keeps the programme's burn file,
`<data>/captions/burn-source-<id>.txt`, holding the caption on screen, and
replaces it whole on every caption. The rendition's `drawtext` reads it with
`reload=1`, which re-reads it on every frame. FFmpeg's `subtitles` filter cannot
do this: it reads its whole file once, when it starts. Measured against the
shipped image's FFmpeg 8.1.3, a caption added to a WebVTT file after the encode
began was never drawn, while `drawtext` drew a burn file replaced mid-encode.
The shipped image has no system fonts, so the burn-in uses the font polyemesis
writes to `<data>/fonts`.

Rules:

- A rendition always re-encodes, so burn-in costs no extra encode. Changing the
  setting restarts the tier.
- The tier needs both a width and a height, because the captions are sized
  against the output picture. The console disables the setting otherwise, and
  the API refuses it.
- The rendition sits before any destination's broadcast delay, so the burned
  caption is delayed with its picture and stays in step with it.
- An FFmpeg without `drawtext` runs the tier without captions and logs why.
- A tier that starts while no captioner runs starts from an empty burn file, so
  a caption left by a crash is never drawn.
- The built-in font is Inter, which has no Chinese, Japanese or Korean glyphs.
  Captions in those languages burn in as missing glyphs; use the caption track
  or a platform route for them.


## How the captioner is driven

The captioner runs for a programme when at least one of its enabled destinations
has a caption mode that its platform accepts, or uses a rendition that burns
captions in. Burned-in captions are how a platform with no caption route (Kick,
a custom RTMP server) gets them, so such a tier is enough on its own. The
player's live track and the cue log fill only while the captioner runs. It transcribes the track the
programme chose (automatic when none is chosen). Turning every captioned
destination off stops it. A destination's broadcast delay decides when its
captions are released, so a destination set to 30 seconds receives each caption
30 seconds after it was spoken, which keeps the words with the picture. The
exception is YouTube's HTTP POST route, whose delay YouTube applies: its
captions are posted at once, with the time they were spoken.

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
Every caption also goes, undelayed, to the player's `captions.vtt`, the
programme's cue log and its burn file, after the programme's caption words are
applied.

## Verifying a route on your channel

Each route marked provisional or live-check needs one test on a real channel
before you rely on it: start a short stream with a test key, confirm the captions
appear on the platform's page, and stop. Do this with a key minted for the test,
not your production key.
