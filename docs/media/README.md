# Media

Screenshots and video for the README and the website. They are generated, not
hand-made: regenerate rather than edit. A shot that is wrong is taken again, and
a picture is never retouched.

`docs/media/shots.json` is the manifest. It lists every PNG in this folder and,
for each one, the scene that photographs it, the Playwright test that takes it,
the console screen, whether the site keeps a copy, which site files use it, its
declared exception if it has one, and the failed-state words it is allowed to
show (with a caption saying why). `usedBy` and `site` are written by
`node scripts/check-shots.mjs --write-used-by` from the site's code and never
typed; `web/src/lib/media-manifest.test.ts` fails when the manifest and the two
image folders disagree.

## Regenerating

```bash
make build                                  # compiles the UI and embeds it in the binary
./scripts/demo-seed.sh                      # the full installation (the "full" scene)
./scripts/demo-seed.sh --scene sports       # the sports installation (the "sports" scene)
./scripts/capture-lanes.sh                  # the three lane pictures
```

Each command builds what it needs, photographs, compresses and tears down or
leaves the rig running as described below. `./scripts/capture-media.sh` is the
older single-programme arrangement and is not used for any committed shot.

The binary is stamped with the latest release tag, so the header reads a release,
not `dev`. `demo-seed.sh` reads the override from `DEMO_VERSION` and
`capture-lanes.sh` from `CAPTURE_VERSION`. The PNGs are
palette-compressed. The site keeps byte-identical copies of the shots its pages
use in `web/src/assets/shots/`; `web/src/lib/features-shots.test.ts` fails if
they drift, and Astro builds avif and webp variants from them. A shot that no
page uses keeps its original here and has no site copy. The one exception is
`06-renditions.png`, which the site keeps on purpose for a later page
(`KEPT_WITHOUT_USER` in `scripts/check-shots.mjs`).

## The scenes

A scene is one prepared installation that a set of shots is taken from. The
manifest's `scene` field says which scene owns each shot.

**sports.** One match feed from Manchester with six audio tracks, sent to five
destinations labelled YouTube (English, Español, Português, Français, Arabic),
one commentary language each, with the crowd bed on every one. It owns the
shots about live routing and live measurement: `01-dashboard.png`,
`18-destinations.png`, `02-routing.png`, `03-routing-filtergraph.png`,
`04-meters.png`, `05-sources.png` and `08-mix-matrix.png`.

**full.** A fuller installation: three programmes, a rendition ladder, thirteen
destinations (YouTube, Twitch, Facebook, Kick and custom destinations), a recording library,
alert rules, schedules, automod pattern rules and a clip buffer. It owns every
other shot except the lane pictures.

The sports scene alone is not enough because it has no recordings, no
renditions, no playout, no music track and no second programme. Those pages are
photographed from the full scene.

**lanes.** `scripts/capture-lanes.sh` seeds one install three times over and
photographs the dashboard at one, two and three programmes
(`dashboard-1-programme.png`, `dashboard-2-programmes.png`,
`dashboard-3-programmes.png`, each 2880x3600 because the viewport is 1440x1800).
The lane shots and `19-lanes.png` show the "No platform accounts connected"
panel, because the lanes rig connects no platform account. The script builds on
Alpine 3.24, because the server refuses the FFmpeg in Alpine 3.20, and it
removes its work directory, which holds credentials, on every exit.

The full scene's demo content, so you know what a shot can and cannot show:
five alert rules whose addresses are all https on a `.invalid` host, chosen so
a healthy run delivers nothing; four schedules; three automod pattern rules; an
armed clip buffer and one clip; no hooks. Enabled schedules fire at 02:00,
19:00 and 21:30 UTC, so a capture that straddles one of those times can fail an
assertion; rerun it.

### What the shots really show

Plain statements, because a shot name promises more than some pages hold.

- `13-automation.png`: the alert rules, with the Delivery card reading Failed 0
  and Retried 0. The schedules are on their own tab. The test checks them before
  the shutter, but they are not pictured.
- `10-playout.png`: the player page link, the embed code and the HLS link that
  the server itself serves. The Playout page has no playlist feature.
- `12-jobs.png`: the governor and an empty queue. Nothing is queued in the demo.
- `14-clips.png`: the rolling buffer, armed, and one captured clip. There is no
  clip editor on that page.
- `17-settings-full.png`: the whole Settings page. Settings shows one section at
  a time, so the whole page is only slightly taller than the window: this shot is
  2880x1834 and `16-settings.png` is 2880x1800.
- `07-monitoring.png`: the process table only, framed so the log pane is outside
  the window. No process has a clean log at the default level: destinations
  print nothing and decoders print warm-up lines.
- `08-mix-matrix.png`: taken last in the sports scene, after the test puts the
  English destination into matrix mode through the console's own API. The
  window is 1800 px wide so all six tracks fit.
- `20-automod.png`: a tall frame (2880x5902) from the pattern rules down to the
  grant matrix.
- `02-routing.png` and `04-meters.png` are taller than 1440x900 at 2x.
- `05-sources.png` shows a 172.x address on its PUBLISHING line. That is
  Docker's private range and is the same on every machine.

### Two shots that show console defects as they are

These two pictures are accurate and unflattering. Retake them when the defects
are fixed; do not edit them.

- `06-renditions.png` reads "NOT ENCODING" on every tier while the encodes run.
  Any install with two or more programmes where only one has renditions shows
  this. In `ui/src/components/LiveDataProvider.tsx`, the `status` case lets a
  status frame from another programme replace the rendition list.
- `14-clips.png` lists its clip as "ROUGH START, 0.0s" although the capture call
  reported 16 s, keyframe-aligned. In `internal/clips/clips.go`, `List` builds
  its rows from the directory only and never sets the length or the keyframe
  flag.

## The theme

Every shot is dark, at 1440x900 and 2x. `capture.config.ts` and
`lanes.config.ts` (in `ui/e2e/`) set `colorScheme: "dark"`, and the capture
reads `html[data-theme]` at every shutter and refuses a page that is not
`dark`. A machine set to light mode therefore cannot photograph the other theme
by accident. The chat shot is dark as well.

## What the capture refuses

The capture would rather stop than write a plausible picture of a product that
looks broken. It refuses when:

- the ingest never goes live (the seeder stops with "these programmes never went
  live"), so an install that is not receiving a stream is never photographed;
- the header would read `dev` (no release tag, for example a shallow clone);
  `git fetch --tags`, or set `DEMO_VERSION` (`CAPTURE_VERSION` for
  `capture-lanes.sh`);
- the page is not in the dark theme;
- a secret is legible on screen: the stream keys and publish tokens of the
  fixture, the admin password and the setup code. The capture is given them as
  a file (`SHOT_SECRETS_FILE`) and reports only the kind of secret it found,
  never its value;
- the page shows a failed-state word (failed, crashed, unreachable, offline,
  degraded, reconnecting, errors, stopped), counted after zero counts such as
  "0 failed" are removed. Where a page carries one of these words as static
  text, that shot's `allowFailedWords` in `shots.json` lists it and a `caption`
  explains it. The rules are in `ui/src/lib/captureGuards.ts`, which has its own
  tests;
- the sports destinations have not settled: every one must be running for at
  least 30 s, at a speed between 0.97x and 1.03x, with no dropped or duplicated
  frames and a measured loudness verdict of pass. The speed window is the
  capture's own gate (`MIN_SETTLED_SPEED` and `MAX_SETTLED_SPEED` in
  `captureGuards.ts`); it is stricter than the console, whose destination card
  turns its speed amber only below 0.95x;
- the feed cannot hold its frame rate: once the publisher has run 20 s, the
  script reads its last three speed readings and its dropped-frame count, and
  stops with a hint to rerun with `DEMO_FPS=30` unless all three readings are at
  least 0.98x and no frame was dropped;
- the installation is short of what the fixture describes.

## The declared exceptions

Every other shot photographs a real polyemesis doing real work. Four are
different and each is declared in the manifest's `exception` field, because an
undeclared exception is how a policy stops meaning anything.

### `09-chat.png` and `22-platforms-connected.png`

A chat message arrives only from a platform account attached to the install,
which needs live OAuth credentials for a real channel, and real viewers'
messages are neither reproducible nor ours to publish. So the chat capture
serves fixture messages to the real page. **The interface is genuine and the
messages are not.** What the shot demonstrates is real behaviour of the real
component, rendered from data the harness supplies: four platforms merged into
one timeline with a toggle each, per-platform attribution, a long message
wrapping under its author's name, polyemesis's own poll result marked with the
megaphone, and a send box whose chips show where a message goes (the live
platforms by default, Kick connected but not live). Besides `/chat`, the harness
serves `/chat/live`, which decides the chip state, and an empty moderator list,
because the demo has no chat account. The shot is clipped to the timeline and
composer at a 1440x720 viewport, so the site's small display of it stays
legible.

Which platforms the send box may name is not part of the fixture: the capture
reads it from `GET /platforms/capabilities`, the same matrix the server uses, so
Facebook (chat send "Not possible") shows as receive-only.

`21-platforms.png` is Settings, Platform credentials, in the state every new
install starts in: the "Optional" banner, an empty Connected channels card, and
YouTube's row opened with **Set up**, where the Client ID and Client secret
fields are empty. Nothing types into those fields.

`22-platforms-connected.png` is the same real page after that one-time setup,
and it is the second exception on the same terms as chat. Connecting needs live
OAuth credentials for a real channel, so the capture serves fixture responses
for `/platforms/credentials` and `/platforms/accounts`: a client id reading
`demo-…-client-not-real`, and accounts named "Demo Studio". The viewer readout
says Offline, because no platform is carrying the demo stream. The shot is the
Connected channels card, one row per connected platform with its Connected
pill, above the platforms not yet set up. The page and its logic are genuine;
whose channel it is, is not. The website's "Platform sign-in" section shows the
connected state, with the empty form beneath it as the one-time setup.

### `02-routing-hero.png` is a clip; `07-monitoring.png` is framed

`02-routing-hero.png` is a clip of the routing screen, the track rows and the
music-rights switch for the selected destination, cropped so it stays legible at
a small size. It was taken for the home page hero, which is now the split
diagram (the earlier animated hero, #971, was retired); the clip is kept for any
page that needs the routing screen at that size. It is taken at 1024x1800,
because the music-rights card sits below the four track rows and a clip is cut
from the viewport: at 900 px tall it ended on Track 4 and the switch was not in
it. On the demo's three programmes the level frames name no programme, so
tracks 3 and 4 can read "not measured" when another programme's two-track frame
arrived last; the committed clip is a capture checked by eye to show all four
meters.

`07-monitoring.png` is framed on the process table, with the log pane outside
the window (see above). The capture asserts no per-process error chip and no
`--:--:--` uptime before it fires.

### Docker network

On the full scene the server and one publisher container per programme (three)
run on one Docker network, dialling each other by name. That sidesteps
[#28](https://github.com/rainmanjam/polyemesis/issues/28), where an SRT
publisher on the host could not reach a listener bound IPv6-only.

## The sports rig

`./scripts/demo-seed.sh --scene sports` stands the sports scene up. The code is
in `scripts/lib-demo-sports.sh` (sourced by `demo-seed.sh`), the destinations
and settings are in `scripts/demo-seed.sports.fixture.json`, and the media
generator is in `scripts/demo-sports/`.

**The story.** One match feed, "Match feed — Manchester", reaches the server
through the real SRT ingest and fans out to five destinations, each carrying its
own commentary language plus the crowd. It is the product's one claim, a
different audio mix per destination, with real audio flowing.

**The media is synthetic and shows nothing real.** The picture is a stylised
pitch with moving players, a ball and a scorebug. The audio is five synthetic
voices, each at its own pitch and rhythm, plus a crowd bed. No real footage,
speech or club is used. `scripts/demo-sports/gen-filter.py` writes the two
FFmpeg filter graphs (`match.filter`, `pitch.filter`) from the voice table in
`voices.py`; `python3 scripts/demo-sports/gen-filter.py --check` exits non-zero
when the committed graphs are not what the generator writes.

**Nothing reaches a real platform.** Three layers, all kept:

1. The fixture's destination hosts are checked against an allow-list of one host
   (the receiver container) before any container exists
   (`internal/demorig/hosts.go`, run by the driver's `guardfixture`).
2. The same check runs against what the server stored, after seeding and before
   every start.
3. The server container sits on a Docker network created with `--internal`,
   which has no route off the machine, and publishes no port. The console is
   reached through a small relay container that alone binds `127.0.0.1`. The
   script asks Docker whether the network is internal rather than assuming it.

The destinations publish to a receiver container (MediaMTX) on that network. No
platform account, no OAuth and no stream key other than invented ones for the
local receiver is involved. The full scene's alert addresses are held to the
same standard (`CheckWebhookURLs`), checked before any container exists and
again before they are sent.

**Credentials are files, never output.** The work directory
(`${TMPDIR:-/tmp}/polyemesis-demo-sports`, or `DEMO_WORK`) is created private
(mode 600 files). The admin password is in `admin.pw` there and the list of
secrets the capture must refuse to photograph is in `secrets.list`. Nothing
prints them. The sign-in line names the file, not its content.

**States.** With the rig running, `--state` acts on it and exits:

```bash
./scripts/demo-seed.sh --scene sports --state routing-initial   # every destination takes all six tracks
./scripts/demo-seed.sh --scene sports --state routing-final     # each destination takes its own language and the crowd
./scripts/demo-seed.sh --scene sports --state start             # start the five destinations
./scripts/demo-seed.sh --scene sports --state stop
./scripts/demo-seed.sh --scene sports --state status
./scripts/demo-seed.sh --scene sports --state guard             # re-run the allow-list check on what the server stored
./scripts/demo-seed.sh --scene sports --state waitlive          # wait until all are live with measured loudness
```

`--no-capture` stands the rig up and leaves it running without photographing.
`--down` removes everything the script created. `--reset` discards a leftover
work directory. A rerun over a crashed run refuses and names the command that
clears it. The console is on port 8097 for the sports rig (`DEMO_PORT`).

**The optional receiver proof.** `./scripts/demo-seed.sh --scene sports --proof`
reads each destination's path back from the receiver and checks that it carries
its own language. It needs Python 3 with numpy (`scripts/demo-sports/proof.py`).
It passes when each path's own voice is at least 15 dB above the strongest other
voice on that path. It also prints a loudness figure for each path; that figure
is an independent reading by FFmpeg's `ebur128` filter of the stream read back
from the receiver, not the console's own figure.
It is manual, never part of a capture. Measured on the maintainer's machine on
2026-11-13, after the last change to the rig, the own-voice margins were between
23.4 and 26.8 dB across the five paths, and each path's integrated loudness was
between -14.1 and -13.8 LUFS in that reading.

The tour's order of steps, prerequisites, frame check and caption rules are in
`scripts/tour/README.md`, not here.

## Timing, for a maintainer

Measured on the maintainer's machine on 2026-11-13, with the Docker image
already built and cached (a first run also builds the image and takes longer):

- the sports scene took about 92 s from start to finish, including about 52 s of
  capture. The publisher held 59.80 fps at speed 0.994x with 0 dropped frames,
  so `DEMO_FPS=30` was not needed. The destinations settled about 35 s after
  their checks began;
- the full scene's capture took about 1.3 minutes (16 tests passed) after the
  build, in a clean run. No clean end-to-end wall time for the full scene was
  recorded;
- the lanes script took about 138 s for the three shots.

None of this runs in CI. The host needs Docker, Go (the version in `go.mod`),
Node and npm (to build the UI and run the capture), system Google Chrome and
`openssl` (to generate a password when none is supplied). FFmpeg runs inside the
containers, not on the host. Python 3 is needed only for the optional palette
compression pass (with Pillow) and for `--proof` (with numpy).

## The tour

The published tour is `web/public/media/tour.mp4`, with its poster
`web/public/media/tour-poster.jpg`. Its source lives in `scripts/tour/`, and
`scripts/tour/publish.sh` is the one script that regenerates those two files
from the master render and, with `--check`, verifies them against
`web/src/data/tour.mjs`. The full order of steps, the prerequisites and the rules
for captions are in `scripts/tour/README.md`. The older tour files in this
folder are gone.

## The Stream Deck + model

The homepage shows a 3D Stream Deck +. The model is
`web/public/models/stream-deck-plus.glb`, derived from Elgato's free model; it
is not covered by this repository's licence and the original file is not
included. `web/scripts/stream-deck-model/README.md` says what the scripts there
do, what is not committed and why.

## A note on committing these

The images are binary and they regenerate, so every recapture adds another few
megabytes to history that no clone can ever drop. On 2026-11-13 this folder's 26
PNGs total 4,372,339 bytes (the largest is `20-automod.png` at 540,150 bytes),
and the nine site copies in `web/src/assets/shots/` total 1,386,044 bytes. A full
refresh adds about the same again to history. That is an accepted cost while a
README needs images to render on GitHub, but if this directory is regenerated
often, moving it to release assets is the better answer.
