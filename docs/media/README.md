# Media

Screenshots and video for the README and the website. **Generated, not
hand-made** — regenerate rather than edit:

```bash
make build                       # the UI must be embedded, or you photograph
                                 # the placeholder page
./scripts/capture-media.sh
```

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

Server and publisher run as two containers on one Docker network, dialling each
other by name. That sidesteps [#28](https://github.com/rainmanjam/polyemesis/issues/28),
where an SRT publisher on the host could not reach a listener bound IPv6-only.

## A note on committing these

They are binary and they regenerate, so every recapture adds another few
megabytes to history that no clone can ever drop. That is an accepted cost while
a README needs images to render on GitHub — but if this directory is
regenerated often, moving it to release assets is the better answer.
