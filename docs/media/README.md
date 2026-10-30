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
platforms merged into one timeline, per-platform attribution, a send box that
knows which platforms can currently accept a message — is real behaviour of the
real component, rendered from data the harness supplies.

Before this it photographed the empty state, *"Chat is running but no platform
account is attached yet"*, under a test named "four platforms in one hub". The
picture argued the opposite of its own title, which is its own kind of
dishonesty.

It is written down here because an undeclared exception is how a policy stops
meaning anything.

### `21-platforms.png` and `22-platforms-connected.png`

`21-platforms.png` is Settings → Platform credentials in the state every new
install starts in: no developer app registered and no account connected, so the
Client ID and Client secret fields are empty. Nothing types into those fields.

`22-platforms-connected.png` is the same real page after that one-time setup,
and it is the second declared exception, on the same terms as chat. Connecting
needs live OAuth credentials for a real channel, so the capture serves fixture
responses for `/platforms/credentials` and `/platforms/accounts`: a client id
reading `demo-…-client-not-real`, and accounts named "Demo Studio". The viewer
readout says Offline, because no platform is carrying the demo stream. The page
and its logic are genuine; whose channel it is, is not.

The website's "Platform sign-in" section shows the connected state, with the
empty form beneath it as the one-time setup.

### `02-routing-hero.png` and `07-monitoring.png` are clips

`02-routing-hero.png` is a clip of the routing screen, the track rows and the
music-rights switch for the selected destination, taken for the home page hero
so it can be read at that size. `07-monitoring.png` stops above the process
list. Every child that decodes the relay joins it mid-GOP and logs a burst of
h264 decoder errors until the first keyframe. The supervisor counts that burst
as errors, so a healthy install wears an "N ERRORS" chip. The comment in
`ui/e2e/capture.spec.ts` says when to photograph the whole page again.

Server and publisher run as two containers on one Docker network, dialling each
other by name. That sidesteps [#28](https://github.com/rainmanjam/polyemesis/issues/28),
where an SRT publisher on the host could not reach a listener bound IPv6-only.

## A note on committing these

They are binary and they regenerate, so every recapture adds another few
megabytes to history that no clone can ever drop. That is an accepted cost while
a README needs images to render on GitHub — but if this directory is
regenerated often, moving it to release assets is the better answer.
