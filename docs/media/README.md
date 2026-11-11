# Media

Screenshots and video for the README and the website. **Generated, not
hand-made** — regenerate rather than edit:

```bash
make build                       # the UI must be embedded, or you photograph
                                 # the placeholder page
./scripts/demo-seed.sh           # the full demo install: three programmes,
                                 # renditions, thirteen destinations, recordings
./scripts/capture-media.sh       # the older single-programme arrangement
```

Both stamp the binary with the latest release tag (override with
`DEMO_VERSION` / `CAPTURE_VERSION`), so the header reads a release, not `dev`,
and both palette-compress the PNGs. The site keeps byte-identical copies in
`web/src/assets/shots/`; `web/src/lib/features-shots.test.ts` fails if they
drift, and Astro serves avif and webp variants built from them.

## What the capture does

It brings up a real polyemesis in a container, seeds it with a three-track
source feeding three destinations whose track selections all differ, pushes a
synthetic stream through the **actual SRT ingest**, and only then photographs
it.

None of that is incidental. Screenshots of an empty install show a product that
does nothing, and the one claim polyemesis makes — a different audio mix per
destination — is invisible unless audio is genuinely flowing. An earlier version
injected into the relay hub instead, which left the dashboard reading *"Ingest
Offline"* with every track *"no signal"*: the routing was correct and the
screenshots said the product was broken.

The script **refuses to capture** if the ingest never goes live, rather than
producing a plausible-looking set that quietly claims failure.

### The exceptions: `09-chat.png` and `22-platforms-connected.png`

Every other shot photographs a real polyemesis doing real work. The chat
timeline cannot be one of them: a message arrives only from a platform account
attached to the install, which needs live OAuth credentials for a real channel
— and real viewers' messages are neither reproducible nor ours to publish.

So that one capture serves fixture messages to the real page. **The interface
is genuine and the messages are not.** What the shot demonstrates — four
platforms merged into one timeline with a toggle each, per-platform attribution,
a long message wrapping under its author's name, polyemesis's own poll result
marked with the megaphone, and a send box whose chips show where a message goes
(the live platforms by default, Kick connected but not live) — is real behaviour
of the real component, rendered from data the harness supplies. Besides
`/chat`, the harness serves `/chat/live`, which is what decides the chip state.
The shot is clipped to the timeline and composer at a 1440×720 viewport, so the
site's small display of it stays legible.

Before this it photographed the empty state, *"Chat is running but no platform
account is attached yet"*, under a test named "four platforms in one hub". The
picture argued the opposite of its own title, which is its own kind of
dishonesty.

Which platforms the send box may name is not part of the fixture: the capture
reads it from `GET /platforms/capabilities`, the same matrix the server uses,
so Facebook (chat send "Not possible") shows as receive-only.

It is written down here because an undeclared exception is how a policy stops
meaning anything.

### `21-platforms.png` and `22-platforms-connected.png`

`21-platforms.png` is Settings → Platform credentials in the state every new
install starts in: the "Optional" banner, an empty Connected channels card, and
YouTube's row opened with **Set up** to show its one-time setup, where the
Client ID and Client secret fields are empty. Nothing types into those fields.

`22-platforms-connected.png` is the same real page after that one-time setup,
and it is the second declared exception, on the same terms as chat. Connecting
needs live OAuth credentials for a real channel, so the capture serves fixture
responses for `/platforms/credentials` and `/platforms/accounts`: a client id
reading `demo-…-client-not-real`, and accounts named "Demo Studio". The viewer
readout says Offline, because no platform is carrying the demo stream. Since
the Platform credentials redesign the shot is the Connected channels card, one
row per connected platform with its Connected pill, above the platforms not yet
set up. The page and its logic are genuine; whose channel it is, is not.

The website's "Platform sign-in" section shows the connected state, with the
empty form beneath it as the one-time setup.

### `02-routing-hero.png` is a clip; `07-monitoring.png` is framed on the processes

`02-routing-hero.png` is a clip of the routing screen, the track rows and the
music-rights switch for the selected destination, cropped so it stays legible
at a small size. It was taken for the home page hero, which is now the split diagram (the
earlier animated hero, #971, was retired); the clip is kept for any page that needs the routing screen at that size. It is
taken at 1024×1800, because the music-rights card sits below the four track
rows and a clip is cut from the viewport: at 900px tall it ended on Track 4 and
the switch was not in it. On the demo's three programmes the level frames name
no programme, so tracks 3 and 4 can read "not measured" when another
programme's two-track frame arrived last; the committed clip is a capture
checked by eye to show all four meters. `07-monitoring.png` is framed on the process
list, with the FFmpeg log filtered to one process (the preview, when it still
has lines). Every child that decodes the relay joins it mid-GOP and logs a burst
of h264 decoder errors until the first keyframe; the supervisor logs that burst
as info until the process's first output frame, so the shot shows it with
errors and warnings at zero. The capture asserts no per-process error chip and
no `--:--:--` uptime before it fires.

`tour.webm` is the tour test's own recording, re-encoded to VP9 at CRF 42 to
keep it small; `tour.mp4` is the H.264 copy GitHub can inline.

Server and publisher run as two containers on one Docker network, dialling each
other by name. That sidesteps [#28](https://github.com/rainmanjam/polyemesis/issues/28),
where an SRT publisher on the host could not reach a listener bound IPv6-only.

## A note on committing these

They are binary and they regenerate, so every recapture adds another few
megabytes to history that no clone can ever drop. That is an accepted cost while
a README needs images to render on GitHub — but if this directory is
regenerated often, moving it to release assets is the better answer.
