# Stream Deck hardware check

A short owner checklist for the packaged Stream Deck plugin on real hardware. CI
validates the manifest and images with Elgato's CLI and packs the
`.streamDeckPlugin`, but only a device shows whether a face reads at key size, a
hold feels right, and a dial turns smoothly. Run it before a release that changes
`integrations/streamdeck/`, on a Stream Deck (keys) and a Stream Deck+ (dials).

Tick every box. Note the device, the Stream Deck app version, the OS and the
plugin version (Stream Deck app, **Preferences > Plugins**) next to the result.

> **Check this first: does every state switch on a real device?** Elgato's
> keys guide says Stream Deck supports up to two states: more are allowed in
> the manifest, but their functionality "is not fully supported". This
> plugin gives each key action eleven states (`KEY_STATE` in
> `src/render.ts`), each with its own image and its own `TitleColor`. Warn,
> state 2, is the only one with a near-black title. Nothing in CI can show
> the app honours them; only a device can.
>
> - [ ] On a Destination key, drive it through idle, live, warn
>       (reconnecting), failed (error), held and unbound. Do the same on a
>       Mute track key for muted, restarted and unavailable, on Record for
>       halted, and on any write key with a read token for needs-admin. Each
>       time, the key shows **that state's image** and **its title colour**:
>       near-black on warn's amber, white on every other state.
> - [ ] Switch back and forth (e.g. live → warn → live) a few times. The image
>       and the title colour change together every time, and the key never
>       sticks on an earlier state's face or title colour.
>
> If any state shows the wrong image or title colour, stop and record it.
> That outcome decides whether the plugin keeps eleven states.

## 0. Get the file

- [ ] Download `com.polyemesis.streamdeck.streamDeckPlugin` from the GitHub
      release, or from the `com.polyemesis.streamdeck.streamDeckPlugin` artifact
      of the pull request's `streamdeck typecheck, test, build` CI job. A CI
      artifact downloads as a `.zip`: unzip it first, the plugin file is inside.
      To build it yourself:
      `cd integrations/streamdeck && npm ci && npm run build && npm run pack`
      writes it to `dist/`.
- [ ] For a release download, hash it and compare the result with its line in
      `SHA256SUMS`:
      - macOS: `shasum -a 256 com.polyemesis.streamdeck.streamDeckPlugin`
      - Windows (PowerShell):
        `Get-FileHash -Algorithm SHA256 com.polyemesis.streamdeck.streamDeckPlugin`
        (it prints the hash in upper case; the comparison ignores case)
      - Linux: `sha256sum -c SHA256SUMS --ignore-missing`

## 1. Install

- [ ] Stream Deck app 7.1 or later is running.
- [ ] Double-click the file. The app offers to install **Polyemesis**. Accept.
- [ ] **Preferences > Plugins** lists Polyemesis at the expected version: the
      release tag as x.y.z.0 (v0.12.3 is 0.12.3.0). A CI or local build shows the
      committed manifest's placeholder version instead.
- [ ] The action list has a **Polyemesis** category with the white bars icon, holding all
      eleven actions: Destination, Go live, Stop all, Failover, Stop destination,
      Record, Routing preset, Chat poll, Mute track, Track gain, Meter.
- [ ] Each action's icon in that list is a crisp white glyph, not a blank or a
      missing-image square.

## 2. Pair (property inspector)

- [ ] Drag a Destination key onto the deck and select it. The property inspector
      opens with server address, token and certificate fields.
- [ ] Paste the address and a **read** token, press **Test connection**. It
      reports success.
- [ ] For a self-signed install, paste the CA from `GET /api/v1/tls/ca` into
      **Certificate**. Test connection succeeds; without the CA it reports the
      certificate as untrusted (it never silently skips verification).
- [ ] Pick a programme and a destination. The lists come from the install.

## 3. Token is in global settings only

- [ ] Set the token once on one key. Add a second polyemesis key of another
      type: it is already paired, without pasting the token again.
- [ ] Export the profile (**Preferences > Profiles**, the profile's menu,
      **Export**). Unzip the `.streamDeckProfile` and search it for the token:
      `unzip -o profile.streamDeckProfile -d p && grep -rF '<12 characters from the middle of the token>' p`.
      **No match.** A match means the token travelled with the profile.
- [ ] Delete the unzipped folder and the export afterwards.

## 4. Key faces at size

For each key action, on the hardware rather than the app's preview:

- [ ] Destination, Go live, Stop all, Stop destination, Failover, Record, Routing
      preset, Chat poll and Mute track each show a face that is legible from
      normal operating distance: the label is not clipped and the corner glyph does
      not collide with the title.
- [ ] Idle, live and error faces are told apart by colour **and** label, not by
      colour alone.
- [ ] A warning face (e.g. a destination reconnecting) is bright amber with a
      **near-black** title and corner glyph; every other face has a white title.
      The burnt-orange **Applied by restart** face (white title, circular arrow)
      is plainly not the warning amber. The title colour comes from the manifest's
      per-state `TitleColor`. If a key that was already on the deck before an
      upgrade still shows a white title on amber, remove it and drag it on again,
      and note that here: it means the app kept the old state style.
- [ ] With the read token, admin-only keys that have no state of their own (Go
      live, Stop all, Stop destination, Routing preset, Chat poll) show the violet padlock
      **Needs admin token** face at rest. Keys that show state (Destination,
      Failover, Record, Mute track) keep showing it, and switch to the padlock
      face with an alert when pressed. Either way a press sends nothing (check
      the install's log: no write request).
- [ ] Record shows **Recording** / **Record off** / **Not running** correctly. If
      you can trigger the free-space floor on a test install, it shows
      **HALTED: DISK** on crimson with a drive.
- [ ] Mute track shows **MUTED** (red, crossed speaker) and **Unmuted**; on an
      audio-copy destination it shows **Not available** (grey, barred circle).

Then switch the token to an **admin** token and continue.

## 5. Taps

- [ ] Destination: tap on an off destination starts it.
- [ ] Destination: tap on a **Failed** destination (e.g. a test destination with a
      wrong stream key) retries it: the key goes amber **Starting**, then **Failed**
      again with its reason. A quick double tap sends one retry (one restart in the
      server log). Tap on a **Live** destination does nothing.
- [ ] Go live: tap starts every destination on the key's programme.
- [ ] Failover: shows the source on air; tap hands control back to automatic.
- [ ] Record: tap switches install-wide recording. It is a tap, not a hold.
- [ ] Mute track: tap mutes, tap again unmutes; the key briefly says **Applied
      live** or, on its orange restart face, **Applied by restart** with a reason.
      A mute made in the web UI shows on the key within about two seconds.
- [ ] Chat poll: set a question, options and duration in the inspector, tap to
      start. The key shows **Poll on** with votes and time left, then **Counting**.

## 6. Hold-to-confirm on destructive keys

The hold is 1.5 seconds (`HOLD_MS` in `src/runtime.ts`). Test on a throwaway
destination, never a real broadcast: a YouTube stop ends the broadcast for good.

- [ ] Stop all: press shows **Hold to stop** and how many YouTube broadcasts the
      stop would end. Release before 1.5 s: **nothing stops**. Hold the full 1.5 s:
      every destination on the programme stops, once.
- [ ] Stop destination: same, for one destination. With the server unable to
      load the count (e.g. a deleted destination), it refuses instead of stopping.
- [ ] Routing preset: press shows **Hold to apply**. A short press applies
      nothing; a full hold applies the preset once.
- [ ] Chat poll, while running: **Hold to end**. A short press does not end it; a
      full hold ends it early.
- [ ] A long hold (5 s or more) still fires exactly once, not repeatedly.
- [ ] Stop all, Stop destination, Routing preset and Chat poll cannot be added
      inside a Multi Action (the app refuses them).

## 7. Dials (Stream Deck+)

- [ ] Track gain: the strip shows destination, track, the level in dB and a bar.
      Each detent is 0.5 dB. A fast spin moves the strip at once and lands on one
      final level on the install, not a burst of requests.
- [ ] Track gain range stops at -40 dB and +6 dB.
- [ ] Track gain: push mutes / unmutes; touching the strip resets to 0 dB.
- [ ] Track gain on a matrix mix shows **matrix: use UI** and a turn sends
      nothing; a push still mutes.
- [ ] The dial's trigger descriptions in the app read Gain / Mute / Reset.
- [ ] Meter: shows the loudest channel for the key's programme and moves with the
      audio. With no feed (stop the source) it shows **NO FEED**.

## 8. Reconnect after a polyemesis restart

- [ ] With keys showing live state, restart polyemesis (`systemctl restart
      polyemesis`, the Windows service, or the container).
- [ ] While it is down, keys show **Unreachable** and the Meter shows
      **NO FEED**. Nothing crashes; the plugin does not need restarting.
- [ ] Within a few seconds of it coming back, every key returns to its true state
      and the dials resume, with no re-pairing and no Stream Deck app restart.
- [ ] Quit and relaunch the Stream Deck app: keys come back paired, with the
      token still set, and show state on the first tick.

## Recording the result

Note any failure with the device, step number and a photo of the key. A face that
fails at size is fixed in `integrations/streamdeck/scripts/icons.mjs`; the PNGs
are regenerated with `node scripts/icons.mjs` and committed.
