# Changelog

All notable changes to polyemesis are recorded here.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and
the project intends to follow [Semantic Versioning](https://semver.org/) from
its first tagged release.

## [Unreleased]

### Added

- **Viewer card: platform tools and private notes.** The viewer card now has a row of tools where the
  platform supports them: on Twitch warn, VIP and moderator (with current status) and a link to
  Twitch's own moderator card; on X and YouTube make or remove a chat moderator. Role changes ask
  again inline and show the platform's own refusal (Build a Community for VIPs, owner-only roles). It also
  keeps a private note and tags per viewer (`GET`/`PUT`/`DELETE /api/v1/chat/users/notes`, admin-only,
  refused to a read token, stored in the new `viewer_notes` table with no schema-version change and
  never sent to overlays, events or hooks). A tagged viewer shows a tag badge in the chat list.

- **"Reconnect to enable" banner.** When a connected Twitch (version 5 to 6) or X (1 to 2) account was
  granted under an older scope version, the console names the platform and what a reconnect unlocks,
  with a one-click Reconnect; dismissible per account and version. `GET /api/v1/platforms/accounts`
  gains `currentScopeVersion` and `scopeUnlocks` (additive).
- **X: choose who can chat and the broadcast language per destination.** The X card in the destination dialog (step 4) has a **Who can chat** setting (off, everyone, verified accounts, accounts you follow, your subscribers, or accounts you follow and theirs) and a **Language** setting. They are sent with the go-live as `chat_option` and `locale`, and left unset they send nothing, so X decides. They are stored in `compliance.x` with no schema change.

- **Viewer activity: one event for follows, subs, gifts, tips, raids and the rest (the shared core).**
  A new Activity tab on the Chat page, an **alerts overlay** for OBS (`/overlay/alerts`, the same
  overlay key and platform filter as the chat overlay, with layout, kinds, duration, sound and
  accent), the `activity` and `activity.display` event types on `GET /api/v1/events`, and an opt-in
  `activity` webhook trigger. Every kind arrives as one shape (`docs/ACTIVITY.md`); amounts are money
  or a platform unit and are never converted, so a minimum tip is one row per currency or unit and
  applies to the overlay only. **Send test alert** (Activity → Settings, and beside Copy OBS URL)
  sends a canned event of any kind through the real path with no account, tier or monetisation. This
  release carries the core only: the per-platform sources (Twitch EventSub, Kick, X, YouTube, Rumble,
  Facebook) are added one at a time, and until one is in your build nothing real arrives from it.
  Viewer words reach the overlay only after automod passes them (removed or unjudged: the alert shows
  without the message), names are checked against your blocked terms, the overlay is never sent an
  id, and a reloaded OBS source is not replayed old alerts. Activity is held in memory (a 500-event
  feed) and is not stored: no new table, and the settings live in the existing settings JSON
  (`settings.activity`).
  - New routes: `GET /api/v1/activity`, `GET /api/v1/activity/sources` (both refused to read tokens, as
    chat is), `POST /api/v1/activity/test` (two a second) and `POST /api/v1/activity/{id}/replay`
    (admin). The word *alert* is not reused on any wire: it stays the operator alert.
  - **Webhooks:** the `activity` trigger is opt-in, so a hook with an empty trigger list does not
    start receiving every follow; `GET /api/v1/hooks/meta` lists it under `optInTriggers`. The
    envelope gains an optional `activity` field and `specVersion` stays `"1"`.
  - **Downgrading:** an overlay saved with an `alerts` style block is refused by an older polyemesis
    (it rejects style fields it does not know). Remove the block or restore the overlay before going
    back.
- **Chat is saved with each recording and can be exported.** When a recording finishes, polyemesis
  writes `<recording>.chat.json` (every viewer message with its offset from the recording start,
  absolute UTC time, platform, account, author, roles and badges, under a small header) and
  `<recording>.chat.vtt` (WebVTT cues, `[PLATFORM] name: text`) beside it, so post-stream analytics
  survives the chat purge and lines up with the video. They are deleted with their recording by the
  delete button and by the age and size sweeps, and offload copies them along. New routes:
  `GET /recordings/{id}/chat?format=json|vtt` (the saved file, or built from the chat history if it is
  still kept) and `GET /chat/export?from&to&format=json|csv|ndjson&platform` (a streamed window); both
  are refused to `read` tokens like `GET /chat/messages`, and CSV cells beginning `= + - @` are
  neutralised. `GET /recordings` reports `chatSaved`. The Recordings page has a Chat menu per
  recording, the Chat page an Export chat button, and Settings → Pipeline → Chat history says chat is
  saved with each recording. Times are the stored message time (`at_ms`) the highlight scanner uses; see
  `docs/CHAT-EXPORT.md`. No schema change.
- **Kick chat now sets itself up, and the console shows the webhook URL Kick needs.** Staging found
  Kick chat could not work: nothing showed the operator the webhook URL to paste into their Kick app,
  and nothing created the event subscription. Settings → Platform credentials → Kick now shows
  "Webhook URL to paste into your Kick app" with a copy button (or the reason there is none, such as
  no public HTTPS hostname), and the Kick setup guide has the step to enable webhooks. The URL
  carries a secret, so it is sent only to the signed-in operator and admin tokens (a read token gets
  the reason instead), is masked by the log redactor, and no longer appears in chat health text. When
  Kick chat attaches, polyemesis lists the app's event subscriptions and creates `chat.message.sent`
  only if missing, re-checks every 30 minutes (Kick auto-disables a subscription after a day of
  failed deliveries, and disabling webhooks drops them all) and says so on the Kick chat card if Kick
  refuses ("Kick webhook not enabled for your app"). Subscriptions are not deleted on shutdown, and an empty listing is not taken as proof one is gone (Kick issue #419). Guides responses are `private, no-store`, and chat state sent to read-only sockets is redacted.
- **Twitch tokens are validated at startup and hourly, as Twitch requires.** Each connected
  account's token is checked with `GET id.twitch.tv/oauth2/validate`; on a 401 one refresh is tried,
  and if Twitch still refuses the account's IRC session is stopped and the account shows as needing a
  reconnect. A Twitch revoked refresh token ("Invalid refresh token") is now recognised as a
  reconnect-required grant.
- **X live chat is read from a webhook.** X delivers `broadcast.chat` only to a registered webhook, and
  the old route (a subscription with no webhook, read from `GET /2/activity/stream`) answered 503 on
  every attempt against live X, so it is removed. polyemesis now serves `GET`/`POST
  /api/v1/webhooks/x` (the CRC check and signed event deliveries), registers
  `https://<tls.hostname>/api/v1/webhooks/x` with X when chat starts, and subscribes
  `broadcast.chat` and `broadcast.chat.remove`; a removed message disappears from the pane. Needs
  `POLYEMESIS_X_APP_BEARER`, a public trusted HTTPS hostname without a port (`tls.mode: acme`),
  inbound 443, and the X client secret saved in Settings. Without them the chat pane says what is
  missing. See `docs/PLATFORMS.md` and `docs/CONFIGURATION.md`.

- **A light theme for the operator console, and a Light / Dark / System control in the header.** The
  console follows the operating system's `prefers-color-scheme` until the operator picks, then
  remembers the pick in that browser (no server setting; it still works when storage is blocked).
  "System" tracks the OS live, with no reload. The light palette is a counterpart rather than an
  inversion: the same roles, with the broadcast tally lamp unchanged, `--down` moved deeper so it
  still cannot be mistaken for it, and every contrast rule the dark palette holds (4.5:1 ink, 3:1
  shapes and focus ring) enforced for both by `palette-contrast.test.ts`. The theme is set before
  first paint by an external `/theme-init.js`, so neither theme flashes the other and the
  Content-Security-Policy is not loosened. The meters and the clip timeline repaint when the theme
  changes. The public player (`/watch`) and the OBS overlay pages are never themed.

### Changed

- **X Livestream is now on by default.** `POLYEMESIS_X_LIVESTREAM` unset or empty switches the X
  Livestream family on (stream key, go-live, chat read and send, moderation); `POLYEMESIS_X_LIVESTREAM=0`
  (also `false`, `off`, `no`) switches it off. A value that is neither a recognised on nor a recognised
  off is read as before (`1` or `true` is on, anything else is off). The paste-the-stream-key fallback
  for an account without Livestream API access is unchanged, and `POLYEMESIS_X_PUBLISH` and
  `POLYEMESIS_CAPTIONS_FACEBOOK` stay off by default. **Upgrading:** an operator who relied on X
  Livestream being off must now set `POLYEMESIS_X_LIVESTREAM=0`. See `docs/CONFIGURATION.md` and
  `docs/PLATFORMS.md`.

- **X Livestream was verified live on 2026-11-07.** On staging against a real X account, two runs through
  polyemesis with X Livestream on: the destination linked to the account and the stream source was created
  through the API on save; the go-live published the broadcast with the configured title; the announcement
  setting `first` made X post and `never` made no post; chat read delivered 2 of 2 viewer messages over
  the webhook; and polyemesis ended both broadcasts. In the capability matrix the X row's Metadata, Chat
  read and Start / end cells now read Works. Chat send, moderation (delete, ban) and viewer stats were not
  exercised and stay Unverified.

### Fixed

- **X chat reaches "live", Kick's loopback self-probe no longer reads as degraded, and the X step-2
  hint stops telling you to paste.** Staging found three small things. The chat status for X stayed
  "connecting" for as long as chat was arriving: the hub only marks an adapter live through its
  health report, and X's reported none, so it now says live once its webhook and subscriptions are
  in place (degraded while X lists the webhook as invalid). Kick chat showed "degraded: could not
  reach its own callback URL (dial tcp 127.0.1.1:443: connection refused)" on a server whose own
  hostname resolves to 127.0.1.1 in `/etc/hosts`; a self-probe that never left the machine proves
  nothing about outside reachability, so a dial to a loopback address is now treated as
  inconclusive. And in Add/Edit destination, with an X account linked and X Livestream on, step 2
  still said to paste the ingest URL and stream key; it now says polyemesis creates the stream
  source on save and the fields can stay empty.

- **An X destination can be linked to the connected X account.** Staging found Add destination →
  "X (Twitter) Live" showed only Transport, RTMP URL and Stream key, with no "Connected account"
  picker, so the Livestream features had no account to act on. The X row in the destination
  dialog's capability table declared no sign-in, and the dialog also showed the picker only when
  the row's sign-in cell read "yes"; X's read "unverified". Sign-in has now run live (an account
  connected on 2026-11-06, reconnected on 2026-11-07, and the token refreshes), so that cell is
  "yes"; the other X cells stay unverified. The picker now follows whether the platform has a sign-in
  flow, not how far it has been verified. With X Livestream on and an account linked, the URL and
  stream key may be left empty: creating the destination makes the stream source and stores its URL
  and key. With Livestream off, or no account, they are still required, and the refusal names the
  switch. The step-2 and preset copy no longer says X has no endpoint to fetch an ingest from.
- **Kick chat messages are limited the way Kick limits them.** Kick counts 500 grapheme clusters
  and also caps the content at 2048 bytes of UTF-8; polyemesis counted runes and had no byte cap,
  so emoji-heavy text passed the check and was refused by Kick. Both limits are enforced and a refusal says which was exceeded.
- **Kick category search uses `GET /public/v2/categories`** (the v1 `?q=` search is deprecated).
  Terms under three characters are not sent, as v2 rejects them.
- **docs/PLATFORMS.md** no longer claims Kick's Get Channels page does not list `streamkey:read`.
- **X Livestream moves off the routes X removes on 2027-01-01, and the whitelist copy is gone.** X deprecated
  `/2/users/{id}/sources` and `/2/users/{id}/broadcasts` and removes them on 2027-01-01, after which they
  return 404. polyemesis now uses `/2/sources`, `GET /2/sources/{id}/stream_key`, `POST /2/broadcasts` (create
  and go live in one call) and `PUT /2/broadcasts/{id}/state`, which take the owner from the access token. A
  source now has a separate, non-secret id, so the stored stream key is resolved to it. X's go-live is public as
  soon as it returns, so a destination records a pending handle while its source is silent and the real
  broadcast id is recorded when it goes live. X's Livestream docs no longer ask for a whitelist or an access
  form, so the operator copy says what the switch is; it is now on by default (see the entry above) and
  pasting the key still works. These routes ran against a live X account on 2026-11-07 (see the entry below). While a destination waits for video its
  `lifecycle.broadcastId` is empty and `lifecycle.pendingSource` names the source. A go-live whose answer
  was lost is adopted on the next read, a deleted row right after go-live still ends the broadcast, a source
  deleted on X raises a fault, an account X will not let go live is a terminal fault, and a 409 for a source
  that is not receiving video yet costs nothing. Adoption compares X's `created_at_ms` with the go-live
  attempt in X's own time (from the `Date` header), inside a two-sided window, so a wrong clock on the host
  neither blocks nor misfires it. A reconnect leaves a destination with an unresolved go-live alone, a late
  result from an older row cannot restore a cleared source, a deleted or switched-off destination keeps looking
  for a broadcast a lost go-live may have made, and a non-terminal X fault that has used its budget is retried
  every two minutes instead of waiting for a manual toggle.

  **Known rare edge cases, all under the `POLYEMESIS_X_LIVESTREAM` switch (on by default).** A go-live is not sent
  unless its marker was written first. Where tracking has to give up on a broadcast a go-live may have made
  (a list that never shows it within two minutes, or an ending that keeps failing) the destination gets a
  durable fault, "polyemesis may have left a broadcast live on X", and a deleted destination raises it as an
  alert, so nothing is let go of silently. The go-live attempt time is not refreshed after a lost answer, so a
  second attempt that creates the broadcast more than about 25 seconds after the first is refused as not
  started by polyemesis and shows as a fault. A reconnect leaves a destination with an unresolved go-live
  alone. How X's `created_at_ms`, `Date` header and 200-versus-201 answers behave is modelled from X's
  documentation and has not been confirmed against a live account.

- **The console Dashboard crashed as soon as an X account was connected.** X writes no metadata fields,
  and its capabilities carried a nil list that the API served as `"fields": null`; the Dashboard then
  called `.includes` on it ("Cannot read properties of null"). `MetadataCaps` now always serialises
  `fields` as an array for every provider (enforced on the type, with a test over every registered
  provider), X reports an explicit empty list, and the Dashboard's field filters tolerate a null list.

- **SonarCloud reliability and security findings.** A fetch chain in the clip editor, the process poll
  and the routing page's first redirect no longer end as unhandled promises; six regular expressions that
  could take quadratic time on a hostile paste (endpoint and ingest URLs, timecodes, stream-health values,
  Mermaid fences, the translation script) are plain scans; the chat message row, face box and a few
  delegating containers got proper keyboard roles or a documented reason; the Stream Deck inspector's
  issue list is labelled for screen readers; and the deprecated React types (`ElementRef`, `FormEvent`,
  `MutableRefObject`) are replaced with their current forms. No behaviour changes.

- **Re-detecting hardware reaches the next VAAPI rendition start of a running programme.** A VAAPI
  rendition pinned the render node it found at its first start, for as long as the programme's engine
  lived, so a GPU passed through or a driver upgraded afterwards showed `h264_vaapi` working in the
  encoder list while every later rendition start kept using the old node. The render node now has one
  owner, refreshed by `GET /api/v1/encoders?redetect=1` and read at every rendition start (a rendition
  that is already running keeps its node until it restarts).
- **Deleting a destination no longer skips the "this ends a live broadcast" confirmation when its
  row cannot be read.** A store error on that read used to turn the refusal into an unconfirmed delete.
  It now asks for `{"confirm": true}`, since whether the row is carrying a broadcast is unknown; a row
  with unreadable data can still be deleted that way.
- **`GET /api/v1/system` no longer serves a stale ingest block when the source cannot be read.** It fell
  back to the settings blob's own ingest copy while `GET /api/v1/settings` failed on the same fault.
  Both now return the store error.
- **`playout.json` no longer holds the playback token in plaintext.** The token is sealed with the key
  the database seals stream keys with (`secret.key`); an existing plaintext file is read and rewritten
  sealed when the server starts, so watch links keep working. A `playout.json` that is corrupt, or whose
  token this install's key cannot open (a restored backup without its `secret.key`), is replaced with
  protected defaults and now logs a warning saying so, instead of silently losing the title, offline
  card and token.
- **Copy buttons say so when the clipboard is unavailable.** On a plain-http install the browser has no
  clipboard, and the ingest URL copy in Settings, the API token, ACME, TLS and redirect-path copies,
  and Pull URL on a media file did nothing and said nothing. They now show "Clipboard is unavailable on this origin".
  The clip, automation and recording pages now show a plain-text server message in their errors, as
  every other page does, and a change the server saved but could not apply raises the same warning
  toast there. A freshly minted API token is no longer shown as
  "Copied" because the previous one was copied a moment ago.
- **The Dashboard and the header agree on how many programmes exist.** The Dashboard kept its own count
  and refreshed it on its own timer, so for up to ten seconds the two could scope the ingest figures
  differently. The count now has one owner and a console left open notices the last source going away.
- **The Dashboard's lanes, Go live rows and setup checklist follow programmes added, renamed or deleted
  elsewhere.** The page kept its own one-time read of the programme list, so they held the old list until
  the page was reloaded. They now read the same list the header does, which refreshes every ten seconds.
- **Saving a destination refuses a routing profile that would stream silence, as creating one
  does.** `PUT /destinations/{id}` skipped the check `POST` ran, so a save could store a profile that
  selected nothing the programme's tracks carry. A save that changes neither the profile nor the
  programme is still not refused. The compile preview (`POST /routing/presets/{preset}`) and OBS
  scene rules with no options now pick tracks from the programme's roles, as applying a preset
  already did; `GET /routing/presets` adds `roleDefaults` beside `defaults`.
- **The default programme means one thing.** The Settings page and the settings save used the first
  source row while `/system`, transcripts and the OBS bridge used the first source with a running
  engine, so with the first source failing to start they described different programmes. All of them
  (and the Sources list's default marker) now use the running one, and the first row only when none
  is running.
- **A read-only API token no longer receives chat retractions on the live socket.** `/api/v1/ws`
  sent `chatRetract` (platform account, author id) to read tokens while `/api/v1/events` withheld it.
  Both now follow one table.
- **Deleting a programme names the YouTube ingest streams it leaves on the channel**, as deleting a
  destination does. A request that deletes the last source can no longer reach a handler that finds
  no programme.
- **`POLYEMESIS_FFMPEG_LOGLEVEL` reaches every FFmpeg child, as the documentation says.** Eight
  children ignored it and ran at a level written into their command line: the selector's copy
  hops, the playlist feed, the HLS/DASH playout variants, the loudness monitor, the proxy, thumbnail
  and archive jobs, the clip exporter, the playlist transcode and the transcription audio extract. With the variable
  unset, nothing changes: each keeps its own default (`warning` for the live ones and for the proxy,
  thumbnail, archive, clip and playlist-transcode jobs; `error` for the transcription extract and the other
  short probes, as docs/CONFIGURATION.md lists). The variable, `POLYEMESIS_INGEST_CAPTURE` and `POLYEMESIS_FFMPEG_ASSUME_MAJOR` are
  now read once at startup with the rest of the environment.
- **Every relay reader opens the relay the same way, and starts faster.** The rendition encoder, the
  silence synthesiser, the HLS/DASH playout variants, the loudness monitor and the selector's copy hops
  read the relay without the probe limits, `-scan_all_pmts 0` and the forward-jump
  `-dts_delta_threshold` that the destinations, recorder, preview and ingest meter already used. They now
  carry the same input options, so they stop spending the whole probe window as dead air on start, join
  in well under a second, tolerate a late track, and no longer swap track offsets after a long outage.
  A declared track that never carries data can hold a reader for up to 15 s (it used to be about 5 s),
  so the playout page waits 16 s before calling a restarting rung ended. Visible as: those children
  start faster, and their command lines (on the Monitoring page) carry the extra input options.
- **Every writer into the relay carries the interleave-stall fix the ingest has.** The silence
  synthesiser, the slate, the rendition encoder and the selector's copy hops (the failover and playlist
  feeds) muxed with FFmpeg's default 10 s interleave buffer, so a multitrack stream whose later audio
  tracks resolved late could write nothing, video included, for up to ten seconds. They now carry
  `-max_interleave_delta 100000 -muxdelay 0 -muxpreload 0`, as the ingest always has, so their output
  timestamps start near 0 instead of 1.4 s. Audio/video spacing is unchanged; only the shared starting
  offset moved, and every writer into a hub moved with it.
- **A backup destination now restarts when FFmpeg cannot characterise its audio,** at most 3 times with a
  30 s cooldown between attempts, as the primary already did.
- **Stopping the preview, a rendition, a playout variant, the meters or a failover feed on a quiet feed no
  longer waits out the grace to be killed.** Only the recorder and the destinations were woken when
  stopped after their publisher left; every other relay reader blocked in its read, ignored the
  stop signal and was killed at the end of the 8 s grace. They are all built one way now, and a
  stopped preview returns in under a second instead of eight.
- **A redundant (backup) output now re-probes like its primary.** It lacked the publishing signal and
  the re-probe handler, so a backup that started before the stream's audio existed sat at running and
  published nothing for ever, where the primary restarts to probe again. It restarts on the demuxer's
  own complaint, and again when the ingest layout is first measured if it has never published.
- **The expert-mode command preview of a stopped destination shows the command that would run.** It
  omitted the muxer and socket tuning (`-flvflags`, `-rw_timeout`, the mux queue sizes), so a
  destination using them was previewed without them. A running destination's preview is built by the
  engine from the pieces it was started with instead of by peeling the saved arguments off its live
  command line.
- **Burning a recorded caption file in no longer loses whitespace at the edge of its path.** FFmpeg trims
  unescaped leading and trailing spaces, so a path ending in one named a different file; the burn-in
  now uses the same filter escaper as every other filter argument, which also converts Windows
  separators.
- **The console agrees with itself about what is live.** The header said "1 live" while the on-air bar
  said 0 and the card said Starting, because a running process that had carried no media counted as
  live in some places and not others. The header, bar, across-the-room view, Dashboard lane heading and
  delete confirm, routing overview and go-live sheet now share one rule. A waiting ingest listener
  reads Waiting, not Running.
- **MQTT reads a destination's delay the way alerts and webhooks do.** A stopped destination still
  playing its delayed tail out is `enabled` on the retained topic until the tail ends, instead of
  reading disabled while it was still on the air. The audience sampler likewise no longer treats a
  destination that carries an error as running.
- **A saved MQTT publish interval takes effect without a restart.** The interval was read once when
  the broker connection opened, and the "unchanged settings" check left it out on purpose, so a new
  value did nothing until a restart or some other reconnect. The running loop now moves to it at
  once, and the connection is not cycled to do so.
- **`POLYEMESIS_CAPTIONS_FACEBOOK` accepts `true`, like the X switches.** It accepted only the exact
  string `1`, so `=true` silently left Facebook captions off. Every on/off switch in the environment
  (`POLYEMESIS_CAPTIONS_FACEBOOK`, `POLYEMESIS_X_PUBLISH`, `POLYEMESIS_X_LIVESTREAM`) now reads `1` or
  `true`, ignoring case and surrounding space; anything else is off. The switches, the X app bearer
  and the Rumble chat key and channel are read once at startup and handed to the parts that use them.
- **Times on every page follow the display time zone from the first paint.** The zone was applied only
  once a page that reads settings (Dashboard, Settings, Sources, Recordings, Playout) had loaded, so a
  cold load onto Jobs, Clips, Monitoring, Automation or Library drew every time in UTC. The console
  now reads it before it draws anything. The failover banner's "since" time, the output strip and
  track meter times, the OBS bridge times, API token dates and the account expiry date used the
  browser's own zone; they now use the display zone too (UTC on a default install) and show 24-hour
  time.
- **A display time zone the server cannot load is reported on a settings save as well as at start-up.**
  Start-up warned and a save silently logged in UTC. Both paths now run the same list of what stored
  settings configure in a running process, so a setting added to it is applied at both.
- **An unreadable settings row stops the server from starting.** Start-up handled it four ways
  (built-in defaults for the job tier, skipping the chat, automod and alert settings, and an abort
  after the engines and API were already running). It now stops once, before anything is built
  from guessed values, with an error that names the settings.

- **With two Kick accounts connected, the second channel's chat is no longer credited to the first.**
  Kick posts every connected channel to one webhook URL, and every delivery went to the first Kick
  adapter, which stamped its own account on it. Each delivery now goes to the adapter for the
  broadcaster it names; one for a channel that is not connected here is acknowledged and dropped.
- **A spent YouTube quota is read the same way everywhere.** Deleting or banning in YouTube chat
  recognised only `quotaExceeded`, so `dailyLimitExceeded` (and `dailyLimitExceededUnreg`) read as
  "you are not a moderator". Chat, clip publishing and the quota meter now share one set of reasons,
  and clip publishing tells `dailyLimitExceededUnreg` (unauthenticated use) apart, advising a reconnect
  rather than waiting for midnight.
- **X no longer shows a stale broadcast id.** The connected-account list and planned rollover fell
  back to the announcement mirror for X, which X never writes from a schedule, so an ended
  broadcast could reappear as the one on air. They now use the lifecycle's recorded id only.

- **Deleting the last recorded segment works once no recorder is running, as 0.11.0 said it did.**
  The exemption that lets `DELETE /recordings/{id}` take the stopped recorder's own segment
  recognised only the name earlier releases wrote (`rec-YYYYMMDD-HHMMSS.mkv`). Since 0.11.0 the
  recorder names each segment after its programme (`rec-s<id>-YYYYMMDD-HHMMSS.mkv`), so every
  segment it had just written was still refused with `409` for up to one segment length plus two
  minutes after recording was turned off or the free-space floor stopped it, telling the operator
  to stop a recording that was no longer running. Both names are recognised now. A file
  destination's output keeps the guard, including one whose rolled-over name looks like a recorder
  segment, and so does a segment whose recorder is still finalising it: a recorder that is
  stopping, or whose source is being deleted, now counts as running until its FFmpeg has exited,
  so `GET /recording` can answer `"state": "recording"` for those few seconds.
- **The session cookie's `Secure` flag, the OAuth redirect URI and the client address now trust
  forwarded headers by one rule.** With `trustProxyHeaders` on, `X-Forwarded-For` was believed only
  from loopback and the addresses in `trustedProxies`, while `X-Forwarded-Proto` (cookie flag) and
  `X-Forwarded-Proto` / `X-Forwarded-Host` (OAuth redirect URI) were believed from any peer. All
  four headers are now believed only from loopback and `trustedProxies`, and the playout viewer
  links follow the same rule once `trustProxyHeaders` is on. A proxy in another container or on
  another host, including nginx or Caddy on the Docker host (`172.17.0.1`), must be listed in
  `trustedProxies`, or the cookie loses its `Secure` flag and OAuth redirect URIs fall back to the
  internal `http://` address; the startup log now says so.
- **Security: the RTMP and SRT wrong-credential backoff folds an IPv6 peer to its /64**, as the web
  login throttle already does. Keyed on the full address, a client holding one /64 could rotate
  addresses and never reach the limit.
- **Security: the Streamer.bot pack no longer sends the API token in the clear.** `Polyemesis.cs`
  sent the bearer token to whatever `polyemesisUrl` held, `http://` to a remote host included.
  It now applies the rule the Stream Deck plugin and the OBS script already had: `https://` to
  any host, `http://` only to this machine, anything else refused with a warning in the
  Streamer.bot log and no request made. A pack pointed at a plain-`http://` remote host stops
  working until its URL is changed to `https://`.
- **The Streamer.bot Status action counts running destinations.** It read `enabled` off each item of
  `GET /destinations`, whose items nest the destination, so `%polyemesisLive%` was always 0 and the
  summary read "0 of N destinations running"; and `enabled` is configuration, not state. It now reads
  `GET /status?source=<polyemesisSource>` (which a multi-programme install requires) and counts
  destinations whose process is `running`, the field the console and the Stream Deck use. The server
  lists every programme's destinations whatever `?source=` says, so the action also filters them by
  each destination's programme itself; the manifest lists the path the code calls.

- **Poll announcements reach X.** The poll's start and end texts were sent unchanged to every chat, and
  X, which stops a chat message at 140 characters, refused any of 141 to 200. X's chat now gets a
  shortened text (an ellipsis where the question or a label was cut, the voting instructions kept; the
  question is shortened before any option label, and a cut never splits an emoji or a flag); every other
  chat keeps the full text.
- **Instagram, LinkedIn, Dailymotion, Owncast and Rumble destinations now get their platform's encoder
  guidance.** Each of those saves as a custom destination, so the console and the engine never found the
  figures the catalogue holds for them and a rendition outside them raised no warning. A destination now
  remembers the preset the operator picked for it (`presetId`, optional on create and update, checked
  against the destination's platform, and cleared by sending it empty), and the guidance is looked up by it.
  The dialog sends the preset only when one was picked, never the guess it uses to label an older destination. Destinations that already exist are matched once, at upgrade, by their
  ingest URL where exactly one preset's fixed host fits; the rest stay unlabelled until they are edited
  and saved from a preset.
- **YouTube titles are counted the same way everywhere.** The metadata push counted characters, the clip
  publisher and the console counted UTF-16 units (an emoji outside the Basic Multilingual Plane is
  two), so a title could pass one check and fail another. The metadata push and recurring-broadcast
  titles now use UTF-16 units too, and so does the title polyemesis gives a YouTube stream it creates (128).
- **Rumble chat cannot be moderated, and the moderation matrix now says so.** Delete, timeout, ban and
  upstream hide read as available on Rumble although it has no moderation API; they are now shown
  as unavailable with the reason. Flagging and the local hide still work.
- **A failure cause newer than the console no longer reads blank.** The destination card, on-air bar,
  Go live sheet and process table looked the cause up by name and showed nothing for one they had no
  label for; they now show "Unknown failure", as the remedy already did.
- **Refresh stream key is offered where the platform can hand a key over.** It was hidden for Kick,
  which can, and shown for Vimeo, X, Rumble and TikTok, which have no key API (Vimeo and X answered every press with a 400).
- **Chat names X and Rumble properly.** The moderation menu printed "on x" in lower case, and Rumble
  chat was labelled "Custom", and X chat lines carried no platform mark on an overlay. Every place the console names a platform now reads one list, taken from
  the server's platform registry.
- **The settings form's failover fallback matches the server.** Shown only when a server omits the
  failover block, it read 10 s grace and 60 s stable return; it is now 5 s and 30 s, the server's defaults.
- **The Go live sheet warns with the same YouTube count as the Stop dialog.** It counted every enabled
  YouTube destination, so it could promise a broadcast would end when the Stop dialog, which asks the
  server, said none would.
- **Cancelling a clip export or a highlight scan returns promptly.** The clip cutter, the editor's
  keyframe probe and the highlight loudness pass started FFmpeg without a kill grace, so a child that left
  a worker holding its output pipe could keep a cancelled job waiting until that worker exited. They now
  start through the shared FFmpeg runner, which gives up on the pipes five seconds after the kill.
- **`polyemesis mcp` accepts `http://LOCALHOST`.** The stdio relay matched `localhost` case-sensitively
  and refused a mixed-case loopback URL that the API treats as loopback.

### Added

- **`GET /api/v1/platforms/registry`** serves the platform registry (names, the stored platform value,
  which platforms carry destinations and chat, and the IVS and X title rules) for scripted clients.
  `GET /platforms/accounts` gains `isPage` on a Facebook account that is a Page.

### Changed

- **New Twitch destinations connected through an account default to the RTMPS ingest.** Fetching the
  stream key hands back `rtmps://live.twitch.tv:443/app` (the same ingest and key over TLS, which also
  passes networks that block port 1935) for a destination with no URL yet. Refreshing a key keeps the
  transport an existing destination already uses: the "Twitch" preset stays on plain RTMP, "Twitch (RTMPS)"
  on RTMPS, and an unlabelled one keeps its stored scheme, so the plain-RTMP fallback for networks that
  refuse TLS survives a refresh. The "Twitch (RTMPS)" preset now spells out `:443`, and the destination
  dialog recognises an RTMPS Twitch URL with or without the port.
- **Archive compression is hidden until it can be switched on.** It had no setting, so every archive job
  failed. `POST /library/recordings/{id}/jobs/media.archive` now answers 400 "archive compression is not
  available yet", and the Jobs page no longer lists the kind (`GET /jobs/overview`).
  A job of that kind stored by an earlier version still lists, as "Archive compression", and fails with
  a message that says so instead of pointing at a Settings switch that does not exist.
- **Audio and video ceilings follow the platforms' own pages.** Twitch's audio ceiling is 160 kbps (its
  help page), not OBS's 320, so a destination sending more than 160 kbps to Twitch now shows the
  audio-bitrate warning. Kick has no audio ceiling (no source gives one), so the 160 kbps warning for Kick
  is gone. Twitch's 6000 kbps video figure is advice and no longer a ceiling. YouTube keeps 12000 kbps as
  the recommended figure and 51000 as the maximum. `GET /services` reflects these.
- **Connecting X now asks for `tweet.write` and `media.write`** so clips can be posted to X. An X account
  connected before this release shows as needing a reconnect, once, before it can publish clips; the
  prompt says that only posting clips needs it and that livestreaming and chat are unaffected.
- **A destination's `presetId` appears in destination responses** when it has one, and
  `GET /renditions/{id}/concerns` accepts an optional `preset` query parameter.

### Documentation

- **A guide for connecting your platforms end to end.** `docs/CONNECTING-PLATFORMS.md` walks an operator
  from an installed server to streaming and reading chat on X, Twitch, Kick, YouTube and Facebook:
  the server prerequisites (a real hostname with a trusted certificate, ports 80 and 443 reachable from
  outside, opening the dashboard by hostname so OAuth callbacks match), each platform's app setup and
  scopes, the X bearer token and webhook, the Kick **Enable webhooks** step, and a short troubleshooting
  table. YouTube and Facebook are marked as being verified. It is linked from the README, the Quickstart
  and `docs/PLATFORMS.md`. `docs/PLATFORMS.md` and `docs/CONFIGURATION.md` now name the settings tab
  "Platform credentials" (they said "Platforms"), say where the X go-live title and announcement settings
  live (**Video and advanced**), and no longer carry a duplicated edge-case paragraph.

## [0.15.0] — 2026-11-06

### Changed

- **A Kick or Twitch ingest URL without `/app` is refused when it is saved.** Kick's dashboard
  prints its Amazon IVS ingest as `rtmps://<host>.global-contribute.live-video.net/`, with no
  application path, and pasted as it is the stream key becomes the RTMP application name, which
  IVS answers by dropping every connection. Saving one only raised a ten-second toast beside the
  success toast as the dialog closed. `POST /destinations` and a `PUT /destinations/{id}` that
  changes the URL to one now answer `400`, naming the URL that works
  (`rtmps://<host>:443/app`). A destination that already holds such a URL can still be renamed,
  switched off and saved, and is warned about on every save. Every other URL without an
  application path is still only a warning, because some services really have none. The
  destination dialog says so under the URL field as it is typed, with a **Use the corrected URL**
  button; it never rewrites the URL on its own. **Refresh stream key** stores the corrected URL
  if a platform answers with an IVS URL without `/app`, and its response says so.

### Fixed

- **A Kick destination with no `/app` in its URL no longer reads as a TLS failure.** IVS opens TLS,
  completes the RTMP handshake, then resets the connection when the stream key arrives as the
  application name. FFmpeg's only TLS-level line is then the encrypted socket's read failing, and
  any `[tls @` line was classified as **Secure (TLS) connection failed**, advising a look at ports
  and certificates. An `rtmps://` failure is now TLS only when the TLS connection could not be
  opened (FFmpeg prints `Cannot open connection tls://…` then) or a certificate failed. A connection
  dropped after it opened is a refusal (`key_rejected`), the same verdict as an unknown RTMP
  application, so repeated quick refusals now hold the destination instead of restarting it for
  ever. For an IVS URL without `/app`, the failure reason also carries a new `urlFix` field with
  the URL that works. The card then shows **Server URL is missing /app** with that URL, the alert
  sentence names it, and both replace the stream-key advice. Three new FFmpeg 9
  fixtures, captured against a local listener that drops the connection after the RTMP connect,
  pin this (`internal/failreason/testdata`).
- **A destination card no longer shows LIVE before anything is on air.** The server reports a
  destination's process as `running` from the moment FFmpeg starts, before it has connected, and
  `uptimeSec` stays 0 until media moves. The card, the YouTube vertical row and the Renditions
  page drew any `running` destination as on air, and the on-air bar counted it as live, so a
  destination being refused on every connect showed a LIVE lamp at 0 kbps with uptime `--:--:--`
  beside its failure. They now show **Starting**, and the on-air bar counts it as trying, until
  the run has carried media. The API's `state` is unchanged.
- **Instagram Live is a working RTMPS destination.** The Instagram preset used to be marked
  unsupported, on the understanding that Instagram had no streaming-software path for most
  accounts. Instagram's desktop flow (**+ Create → Live video → Go live with streaming software**)
  now hands out a stream URL and key, including named persistent keys, so the preset is a
  manual-key destination like DLive or LinkedIn Live:
  - The stream URL is left for you to paste, because the host Instagram shows carries a region
    (`rtmps://edgetee-upload-<region>.xx.fbcdn.net:443/rtmp/`). The preset's notes give that
    shape, recommend a persistent key so the destination stays ready between live videos, and
    say to click **Go live** in Instagram once the preview appears and to end the live video in
    Instagram before stopping the destination.
  - Video guidance is vertical 720×1280 at 30 fps, 2,250–6,000 kbps, from Restream's Instagram
    help (Meta publishes no encoder page of its own).
  - The capability matrix moves Instagram to **Manual key**. Reading live comments is allowed by
    Meta's API and is not built yet, so it reads *Unverified*; creating a broadcast, fetching a
    key, Go live / end, viewer counts and comment moderation have no public endpoint and read
    *Not possible*.
  - The rollover limit table records Instagram's one-hour cap, as *reported* (the source is
    Restream's help, not Meta's). It drives no countdown: Instagram destinations save as custom,
    and a limit row with no platform is now never matched to a destination.
  - A test pins that a query-string key such as Instagram's or Facebook's
    (`<digits>?s_bl=1&…`) reaches FFmpeg as one argument with its `?` and `&` intact.

## [0.14.0] — 2026-11-05

### Added

- **The Stream Deck plugin and the server are checked against each other in CI.** The plugin calls
  about 25 routes with an API token, and nothing compiled the two together: a route renamed, moved
  to the session-only group, or given a different scope, or a field renamed in an answer, left both
  test suites green. Three checks now fail instead:
  - **Routes.** The plugin's routes and the scope each needs are a table in the plugin
    (`integrations/streamdeck/src/apiContract.json`), and the plugin uses it: a call it does not
    list is refused before any request, and on a read token an admin route is refused in the plugin
    and never sent (the Viewers key's analytics read is the one exception, kept so the key can show
    **Needs admin token**). A Go test parses every call in the plugin's client and drives each route
    through the real router: it must exist, take an API token rather than only a signed-in session,
    and need exactly the scope the table says.
  - **Answers.** A Go test records the real answers of every route the plugin reads, and the
    answers and refusals it acts on, from a seeded install with a destination in each state a key
    paints (live, off, failed, held, buffering a delay), into
    `integrations/streamdeck/test/contract/`. It fails when an answer changes; `-update-streamdeck-contract`
    rewrites them. The plugin's tests parse every recorded answer and paint every key kind from
    them, and every hand-written fake in the plugin's tests is checked against the recorded schema,
    so a renamed field fails there too.
  - **End to end.** A new CI job, `streamdeck end to end`, builds the binary, starts it on a
    temporary data directory, creates the admin and the tokens through the API, and runs the
    plugin's runtime against it over HTTP with a local RTMP sink: pair, read, start a destination
    and see it at the sink, kill the sink and see the key show **Failed** with its reason, retry from
    the key, stop it. It skips when a pull request cannot reach the plugin or the server.
    `./scripts/streamdeck-e2e.sh` runs it locally.
- **`PUT /destinations/{id}` reports how a save took effect.** The response
  carries `applied` (`live` or `restart`) and `reason`, the same fields as
  `POST /destinations/{id}/tracks/{track}/gain`. The server reads them back from
  the reconcile; they are not a prediction. `applied` describes the primary
  output, and a backup feed that was started, stopped or restarted is named in
  `reason`. Both are absent when nothing was running, including a delayed
  destination whose buffer is still filling. See [docs/API.md](docs/API.md).
- **`POST /routing/presets/{preset}/apply` applies a routing preset to a
  programme.** It saves the preset on every destination of the programme, or on
  the one `?destination=` names, and follows the same rules as
  `PUT /destinations/{id}`. A destination whose mix changes only in levels gets
  the change live. A destination whose audio graph changes restarts. Each
  destination's row answers `applied` and `reason`. Muted tracks stay muted and
  a music guard (`excludeRoles`) stays on. A destination that copies its audio
  is skipped. Only the two mixes are written, in one transaction, so a stop or
  a key refresh that lands meanwhile is kept, and if one destination cannot
  take the preset, nothing is saved. `?mix=vod` replaces the second mix
  where there is one, and `?dryRun=true` previews without saving. Without a
  body, the track pickers follow the programme's track roles, as the routing
  editor's do. It needs an admin token. `POST /routing/presets/{preset}` still
  compiles a preset and saves nothing. See [docs/API.md](docs/API.md).

### Changed

These follow from the lingering-state fixes below and are visible on purpose.

- **Signing out ends every session of the account, on every browser and phone.** Sign-out used to
  clear only the current browser's cookie, and a copy of that cookie kept working for the rest of
  its seven days. It now revokes the user's sessions the way a password change does: other tabs
  are told the session ended, and their live sockets close. API tokens, the Stream Deck, the OBS
  script and MCP clients are unaffected. A Bearer-token call to `POST /auth/logout` signs nobody
  out.
- **Removing a platform's app credentials while accounts still use them needs a confirmation.**
  `DELETE /api/v1/platforms/credentials/{platform}` without `{"confirm": true}` now answers `409`
  with `code: "creds_in_use"` and the accounts, because removing the app does not disconnect
  them: they keep working until their tokens expire. The console's two removal dialogs are typed
  confirmations already and send the confirmed request. With no account on the app, nothing
  changes. Accounts are never deleted by this route.
- **Deleting the programme published on the public playout page takes the page down.** It
  answers `503 playout is not running` until another programme is published in Playout >
  Programme for viewers, instead of silently showing the audience a different programme. The
  delete says so in its `warnings`, and the Playout page shows the programme picker with a
  **Use the default programme** button whenever the published programme no longer exists,
  including on a one-programme install, where the picker is otherwise hidden. A published
  programme that exists but is not running yet still falls back to the default, as before.
- **Deleting a destination cancels its upcoming scheduled events on the platform.** YouTube
  events created by a recurring template or a pre-announcement are deleted (this cannot be
  undone, and is what deleting the destination asks for). A Facebook pre-announcement cannot be
  deleted from here and is named in the response for removal on Facebook. A show that has
  started, or a destination that is on air, is left alone and named. A platform error never
  blocks the delete.
- **A schedule whose only destinations were deleted is switched off** rather than failing at
  every occurrence; one that names other destinations simply loses the deleted one. The delete
  response says which.
- **`DELETE /api/v1/sources/{id}` answers `200` with `{"warnings": [...]}`** instead of `204`
  when it has something to report: an event it could not cancel, a schedule it switched off, the
  public playout page losing the programme it was published from, or a public playout rung
  losing its rendition (which used to go only to the log). The response to
  `DELETE /api/v1/destinations/{id}` gains the same additive `warnings` array.
- **A programme delete is checked against the destinations it was confirmed for, by id.** One
  destination deleted and another created on the programme while the delete was cancelling
  platform events now refuses it with `409`, as an added or removed one already did, instead of
  taking the new destination with the programme because the count still matched.
- **Switching MQTT off deletes the retained state topics** it was publishing, so other consumers
  stop reading a stale "running"; the status stays `offline` and the Home Assistant device stays
  (its entities show unavailable). Changing the topic prefix deletes the old tree's state and
  status; changing the instance also deletes the old Home Assistant device. Shutdown and every
  other settings change delete nothing.
- **The Automation page shows a schedule's warnings when it is saved, switched or deleted**: an
  event page the change could not withdraw (Facebook has no call to remove one), and the existing
  note that a one-time schedule more than seven days out gets no Facebook event page, which the
  console never showed before.
- **Deleting one track of a transcript also removes the merged transcript files** (the JSON and
  the `-all` subtitles, which carry that track's words). The next transcription writes them
  again; the database, which captions and exports read, is unaffected.

### Fixed

- **Refresh stream key, the expert save and turning on a Facebook backup no longer undo a change
  made while they ran.** Each one read a destination, waited (for the platform, up to 30 seconds,
  or for a re-read), then wrote the whole destination back. A stop or start, a rename, a routing
  change (a save, or a routing preset applied from the Stream Deck) or an expert save that landed
  in that window was silently reverted, and a reverted stop was a destination the server then
  started again.
  - **Refresh stream key** now writes only what the platform answered: the ingest URL, the stream
    key, the backup endpoint and its key. If the destination was linked to a different account
    while the key was being fetched, the key is not saved and the request answers `409` with a
    sentence saying so. A YouTube caption ingestion URL is now stored in the same write as its key,
    so a destination is never left with the new key and no URL. If the URL cannot be stored for
    any reason other than a missing secret key, the refresh now fails as a whole.
  - **The expert save** (`PUT` and `DELETE /destinations/{id}/expert`) writes only the expert
    arguments.
  - **`PUT /destinations/{id}`** stores the edit first, then asks Facebook for the backup ingest it
    has just turned on, and writes only the backup endpoint it gets back.
  - Each write reads the destination again in the same transaction, runs the same checks as
    before (validation, stream key sealing, and the repair of settings the platform cannot use),
    and leaves every other column alone. Request and response shapes are unchanged.
- **Deleting things now takes what referred to them** (the 2026-11-04 lingering-state audit):
  - A destination or programme delete removes the destination's id from schedules, its planned
    rollover settings, and its place in the OBS bridge configuration. A scene rule left naming
    only deleted destinations is removed rather than widened to every destination. An OBS
    configuration that already named a deleted destination, which could not be saved at all,
    is cleaned at startup. A save naming a missing programme now gets its own `400` sentence
    instead of a bare `404`.
  - The recurring sweep drops, once and with an error in the log, an event whose destination is
    gone, instead of retrying a cancel it can never make until the row ages out.
  - Deleting, switching off, retargeting or turning a template-less schedule into a stop withdraws
    the events it pre-announced (YouTube cancelled, Facebook named), and its marker no longer
    hides a replacement show at the same time.
  - Deleting a schedule deletes its recurring-broadcast template and thumbnail; orphaned
    templates are removed at startup.
  - Deleting a recording removes its stems and transcript files straight away, including when it
    was the last recording. Stems are matched to the nearest master of the same programme, and a
    neighbour's files are never taken.
  - Deleting a transcript removes its files from `recordings/transcripts`, where the offloader
    would otherwise upload them. Objects already uploaded stay in the bucket.
  - Removing platform credentials drops the sign-ins begun with them, and a device-code sign-in
    started with them (or with credentials since replaced) answers `expired` instead of storing
    an account for an app that is gone.
  - A storage target delete names, in `warning` and `unabortedUploads`, any unfinished upload it
    could not abort on the service; the console keeps the warning and each upload's key and
    upload id on the card, copyable, until it is closed. The delete still always succeeds.
  - Deleting a destination leaves a show that already aired alone, as it leaves an old
    pre-announcement, instead of reporting each recent show as an event "left on YouTube (start
    time has passed)".
  - Deleting one track of a transcript keeps a per-speaker file another track also writes to:
    two labels that reduce to the same file name (`Host` and `host!`), or a label that is
    another track's fallback name (`track2`).
  - A destination delete that fails on the Dashboard no longer also says "Destination deleted".
  - Switching MQTT off or moving it while the broker is unreachable says in the log which
    retained tree was left, and removing the old Home Assistant device after an instance move
    has its own time budget rather than what the state clear left of a shared one.
  - A rendition delete reports public playout rungs that named it (`playoutVariants`,
    `enabledPlayoutVariants`, and the `warning`), and such a rung's error now says the rendition
    no longer exists.
  - Every account disconnect says the platform still lists the connection and where to remove
    it; the disconnect dialog says the same in every language.
  - Home Assistant discovery is republished when the entity set changes, and a removed entity
    is removed from the device rather than left behind.
- **The Stream Deck's Routing preset key applies the preset.** The key sent
  `POST /routing/presets/{preset}`, which only compiles a preset, and showed OK
  while nothing changed. It now sends the apply route above to every destination
  on its programme. It shows **Applied to N** with the OK mark when the preset
  was saved, and an alert when no destination took it. If the preset was saved
  but the running pipeline could not be updated, the key shows both the count
  and the alert. See
  [docs/STREAM-DECK.md](docs/STREAM-DECK.md#routing-preset).
- **Platform routing presets are no longer refused as unknown.** A platform
  preset's id has a colon (`platform:youtube`). The Stream Deck plugin and the
  console escape it in the path, and both preset routes read the escaped form,
  so they refused the preset as unknown. Both routes now unescape the id. The
  Stream Deck end-to-end run found this.
- **The MCP tool `apply_routing_preset` uses the same route.** It no longer has
  its own compile-then-`PUT`, so its preview and its apply follow the server's
  rules. The confirmed answer carries the server's `applied` and `reason`, and a
  music guard now stays on, as muted tracks already did. One behaviour changed:
  a destination with no second mix is refused with `mix: vod`. Before, the tool
  created a second mix there. A destination that copies its audio is refused
  too. The preview's `wouldRestart` now covers a change to the second mix, which
  restarts a running destination as a live-mix change does. A row the server
  did not save is a failed call, and a reconcile that failed is audited as one.
  The tool also accepts platform preset ids such as `platform:youtube`, which
  its argument check used to refuse even though `list_routing_presets` lists
  them. See [docs/MCP.md](docs/MCP.md).

- **Routing Overview shows music that is routed live.** The Music rights column
  used to say "No music routed" beside a filled Music cell. In the 2026-11-04
  demo, Twitch sent track 4 at 100% and Kick sent it at 25%, and both rows showed
  that label. The column now reads "Music routed" whenever a music-role track is
  in a destination's live mix and no guard keeps it out. "No music routed" now
  means no music track is in any mix and no guard is set. A destination whose VOD mix keeps music
  out, but whose live mix still sends it, now shows "Music routed" instead of
  "Music blocked".
- **The toast after a routing save reports what the server did.** It used to
  predict the result itself, so turning on music rights for YouTube after its
  music track had already left the mix said "“YouTube” is restarting with the
  new mix". The server had applied the change without restarting anything. The
  toast now follows the server's answer. When the destination's output kept
  running without a level going out, it says it kept running without a restart.
- **The save bar no longer flashes "Saving restarts" while a fader is dragged.**
  Each step of a drag starts a new compile, and until it answered, the bar showed
  the restart warning in amber. While a compile is out the bar now claims neither
  a restart nor a live change. Once the compile answers, it applies the engine's
  own rule to the live mix: the argv fields and masked levels the engine
  compares, and only the track list for an audio-copy destination. It also
  reports a change that moves nothing the destination sends as "Saving does not
  restart". A change to the second (VOD) mix is still warned of as a restart,
  because the page does not have its stored compile.
- **A disconnected account leaves the chat pane at once.** Disconnecting an account, or removing a
  platform's developer credentials in Settings → Platform credentials, left that platform's chat
  running until the next restart: the Dashboard's chat card went on showing its chip ("Connected
  and waiting") and Settings showed nothing connected. A Facebook page with no live video never
  noticed, because it never asked for its token. Disconnecting an account now ends that account's
  chat immediately, and removing credentials ends the chat of every account on that platform (Rumble,
  which is keyed by an environment variable, is untouched). A restart agrees: an account whose
  platform has no developer credentials stored no longer starts reading chat.
- **A newly connected account reads chat at once.** Connecting Twitch (or any chat platform) in
  Settings → Platforms left the Dashboard's chat card without it until polyemesis restarted, because
  chat was only started at startup. Connecting an account, by sign-in or by device code, now starts
  its chat immediately. Connecting it again restarts its chat with the new token, which is also what
  brings back a Twitch chat whose login was refused (its own message says to reconnect, and before
  this the chip went on reading Failed). The same rules apply as at startup: no chat without the
  platform's developer credentials, and X chat only with `POLYEMESIS_X_LIVESTREAM` set. A connect and
  a disconnect that overlap leave the chat matching whether the account is still connected, and the
  connect no longer fails with a bare "not found" when the disconnect lands in the middle of it: it
  completes, and the disconnect takes effect before or after it.

## [0.13.0] — 2026-11-04

### Added

- **Stream Deck destination keys show which destination they are.** A Destination key showed only
  its state ("Live", "Failed / Timed out"), and the plugin sets the title, so nothing on the key said
  which of five destinations it was. The key now draws the destination's platform mark (YouTube,
  Twitch, Kick, Facebook or X; a neutral transmitter for custom RTMP and every other platform) and
  the destination's own name at the top, in the state's colour and title ink, with the state and its
  reason below as before. The image is an SVG the plugin draws itself (`setImage`); a key that is not
  showing that destination's own state (not found, other programme, unreachable, the read-token
  padlock) goes back to the plain face. A long name shrinks, then is cut; with four or more state
  lines the name is left off. On X, whose mark is its name, a destination named just "X" shows the
  mark alone. The marks are Simple Icons (CC0 1.0), listed in the plugin's new
  `third-party-licenses.txt`. Action UUIDs, settings and the API are unchanged.

- **A tap on a failed Stream Deck destination key retries it.** The Destination key started a
  destination that was off and ignored every other tap, so a key showing **Failed** or **Held**
  (after repeated quick rejections) could only be fixed from the console. A tap on it now sends the
  console's Retry (`POST /api/v1/destinations/{id}/restart`, admin token), and the key re-reads
  the server straight away rather than waiting for the next poll, so it follows the real
  transition: amber **Starting**, then **Live**, or **Failed** again with its reason. A refused
  retry shows the key's alert. Taps on the same destination while a retry is out, or within
  4 seconds of one, are ignored (from any key), so a double tap does not restart the attempt the
  first began. Off still starts; Live, Starting and the other
  states still ignore the tap, and Go live is unchanged.

### Fixed

- **The rest of the console's counts say one as one too.** A second pass over every number in the
  catalogues, not only `{count}`: the Stop all toast ("1 destination stopped."), track and line
  counts ("1 track", "1 line"), the host's cores, the offload badges ("Upload failed on 1 target"
  rather than "target(s)"), the setup guide ("1 step"), the platforms tile, retried uploads, the
  automod call count, the Across the Room count, and the partial-revoke and partial-hide messages.
  Strings that read the same in English but not elsewhere are fixed as well: Spanish, French,
  Italian and Portuguese "received", "sent", "verified", "shown", the viewer count, automod's
  fallback lines and the checklist progress, and German "1 läuft". A line that carries several
  counts at once (the player's sessions and rebuffers, the overlay's shown and held lines, the
  regroup result, the ingest's TS loss, the Go live and on-air summaries) now names each count
  with a label ("sessions: 1 · rebuffers: 2") so it reads right whatever the numbers are. Four
  catalogue keys nothing referenced are gone.

- **Go live and Stop all say one as one, in every language.** The Go live button read "1 listos"
  in Spanish (and "1 prêts", "1 pronti", "1 prontos" in French, Italian and Portuguese). It now
  reads "1 listo", and Polish uses a form that is right at any count ("gotowe: 2"). The Stop all
  dialog said "Stop all 1 destinations?" and "1 of these are YouTube broadcasts". It now says
  "Stop 1 destination?" and "1 of these is a YouTube broadcast". The Go live sheet's YouTube
  vertical-pair reminder no longer says "1 YouTube vertical pairs have not had…". Each of these
  has its own key for one, the same approach as the YouTube warning.

- **Every other count in the console says one as one too.** Poll votes ("1 vote", on the panel,
  the history and the on-stream widget), automod's answered messages and failed calls, the log
  verdict ("1 error"), relay subscribers, shortened debug lines, the purge report ("Purged 1
  finished job."), recording tracks and stem files, the on-air health line ("1 running destination
  healthy", "1 of 3 destinations needs attention"), the clip retention limit, the failed count,
  Spanish, French, Italian and Portuguese "new messages", and the Polish live count. Where Polish,
  Russian or Ukrainian were also wrong at 2 to 4 or 21 they now use a form that reads right at any
  count, and Italian agrees with the feminine *destinazioni*.

- **The meters follow the programme selected in the header.** On an install with several
  programmes, every programme's audio levels reach the console over one live connection, and the
  console drew whichever programme metered last. Tracks a smaller programme lacks read "not
  measured" (tracks 3 and 4 in about 40% of samples on the three-programme demo). Levels frames
  now name their programme, as status frames do, and the Meters and Routing pages keep only the
  selected programme's. Switching programme clears the old programme's meters until the new one
  reports, and the routing editor's "last peak" for a quiet track is kept per programme.
- **Go live counts up through the press you made.** The sheet starts destinations one at a time,
  and its button used to recount "Go live · 4 ready", then "3 ready", as each one came on, so one
  click looked as if it had started a single destination. After the click it counts what is
  actually on air, from each destination's live status: "Starting… 2 of 4 live", then "4 of 4
  live", or "3 of 4 live · 1 failed" when one is refused or its process fails. A destination is
  live once its output is moving and not stalled, not the moment FFmpeg starts, so one that
  the platform is about to refuse is not counted (the row's LIVE chip follows the same rule).
  The press is over when every destination it set out to start is live or failed, not when the
  last request returns, and the final count stays on the button for a second before it goes back to Go live.
  A destination still starting after a minute (longer than the server takes to hold one that
  keeps being refused) gives the button back, and the count says how many are still starting.
  The sheet can be closed while it waits, though not while the starts are going out. Screen
  readers hear the first count and the final one at once, and changes in between at most every
  two seconds. The YouTube warning also says "1 YouTube broadcast", not "1 YouTube broadcasts",
  in every language.

### Security

- **Expert mode is session-only, and refuses any argument that touches a file on the server.**
  An admin API token could save FFmpeg arguments that overwrote a file (`-progress <db path>`),
  created one (a second output) or read one out to a platform (`movie=`, `textfile=`). The five
  expert routes (`/destinations/{id}/expert`, its `preview` and `dry-run`) now answer `403` to an
  API token of any scope; nothing that ships uses them with a token. For the signed-in operator,
  options are an allowlist generated from FFmpeg's own option table, with every option whose help
  mentions a file, path, URL, log, dump, stats or device left out. Filter graphs may use only
  filters vetted to open no file, the `/option=` form is refused, and so is any encoder-param key
  outside an allowlist of tuning keys (`pass=`, `stats=`, `dump-yuv=` and their aliases are out), nested option lists such as `-format_opts`, `-f` values
  other than streaming formats (`flv`, `mpegts`, `matroska`, `mp4`, ...), `-i`, and stray output
  tokens. Arguments
  saved before this are dropped when the destination starts, with a log line saying why. See
  docs/API.md and docs/TROUBLESHOOTING.md.
- **A publisher whose token is rotated, or whose source is disabled, is disconnected.** The SRT
  and RTMP listeners check every live publisher again every five seconds. They close one whose
  token no longer resolves, whose source is off, or (SRT) whose engine is gone. One failed lookup
  is not enough: the check must fail twice in a row, so a busy database never drops a healthy
  encoder.
- **The OBS bridge script sends its token only over HTTPS**, or over plain HTTP only to the same
  machine, which is the rule the Stream Deck plugin already applies. Any other address is
  refused, and nothing is sent.

### Fixed

- **An unmounted recordings volume no longer wipes the archive's metadata.** A scan that lists no
  recordings while the index has some now leaves the index alone and logs once. It used to
  remove every recording with its transcripts, highlights and sessions. The offload ledger
  likewise stays when every file it tracks is missing at once, so the archive is not uploaded
  again in full when the volume returns.
- **Deleting a job refuses one a worker has just started.** The check now happens in the same
  database statement as the delete, so the job's row cannot vanish while it runs.
- **A schedule that fired, but could not record that it fired, no longer fires again** on every
  pass for the rest of its grace window.
- **A replaced RTMP publisher's disconnect no longer cuts off its replacement's viewers.**
- **Twitch chat no longer goes dark about four hours after it connects.** Each reconnect now
  signs in with a fresh token, not the one captured when chat started.
- **Automod never acts on the broadcaster, a moderator or the operator's own messages.**
- **A revoked platform sign-in ends a publish with "reconnect the account"** right away. It used
  to retry five times and report a token error.
- **A YouTube rate limit is retried.** It used to be reported as the daily quota, and the clip was
  dropped until midnight Pacific. YouTube's per-channel upload limit now has its own cause,
  `upload_limited`, instead of the daily-quota one.
- **The chat box keeps your message when no platform accepted it.** When only some accepted it,
  **Put the message back** restores it.
- **The chat rules card shows the room's real rules.** It reads them from Twitch first, and Apply
  sets every rule as shown, off included. New route `GET /chat/settings`.
- **A click outside, or Esc, no longer discards a half-filled destination or storage form.**
  Cancel still closes it.
- **rollback.sh refuses while this install is on the air** (unless you pass `--force`). Before
  it starts, it says which changes it will lose and asks on a terminal.
- **The website redeploys only after a release that actually published.** It used to redeploy
  after failed runs and rehearsals too.

### Changed

- **Expert arguments saved earlier may stop applying.** The stricter expert-mode rules (see
  Security) also run when a destination starts. Saved arguments that now fail them are left out of
  the command, and the destination starts on its generated command: for example, a `loudnorm` or
  `drawtext` filter, an `-f` other than a streaming format, or an option FFmpeg's own option list
  does not include. The expert editor names what was refused and says the destination starts
  without those arguments, and the server log says it on every start.
- **MQTT gets an instance of its own when you first set it up**, so two installs on one broker
  cannot share a client ID or topic tree. Installs that were ever configured keep theirs, even after
  switching MQTT off and on. The settings
  page and the server log now warn when credentials go over `mqtt://`, `tcp://` or `ws://` to a
  broker that is not on your network. They also say when **Accept a self-signed certificate** is
  on but has nothing to act on.

- **The home page hero is now an interactive signal path.** "One stream in. Your mix, everywhere."
  sits above a diagram of one upload from OBS going through polyemesis to Twitch, YouTube and a
  local archive, each with its own audio mix: wires and packets in the track colours, the
  polyemesis mark as five faders that move to the mix of whichever destination you hover or tab to,
  and a switch that sends the music to YouTube. It replaces the routing screenshot. The diagram is
  real HTML first, so it reads without JavaScript, and its animation (Three.js, vendored, no CDN)
  loads lazily after the page has painted. It pauses offscreen and in a hidden tab, and is a static
  frame with `prefers-reduced-motion`. Each destination card is a keyboard focus stop with the same
  2px ring as the rest of the site. The packet glow is softer (its bloom is half as strong and only the
  brightest packet heads feed it), so the wires keep their track colour on phones. The clip in it is a fictional person and an unbranded car, generated
  with Google Veo 3.1.

### Documentation

- **X's stream-key path was tested live, and a server that cannot reach Cloudflare cannot reach X.**
  On 2026-11-04 a build of `main` on a residential connection published to X with a pasted server
  URL and key, and the picture showed in X Producer's preview. docs/PLATFORMS.md records the test;
  the X row of the capability table is unchanged (the stream key stays *By hand*, everything else
  *Unverified*). The same destination timed out from an OVH VPS that could not reach Cloudflare's
  IPv4 network at all, and X's ingest sits behind Cloudflare; the cause is likely routing on the
  provider's side, and other hosts are untested. docs/PLATFORMS.md says how to check that a server
  can reach Cloudflare and X's ingest first, and docs/TROUBLESHOOTING.md has a new entry for
  "Connection timed out" on an X destination.

## [0.12.0] — 2026-11-03

### Added

- **Publish a clip or short to YouTube Shorts or TikTok, from the clip editor (Wave 4 #5c).** Open
  **Publish** on a finished clip export (in the clip editor or on the Jobs page), pick the
  platform and the connected account, review the title, description and hashtags (filled in from
  the clip) and the privacy level, and click **Publish**. The dialog follows the upload to a link
  to the post, or to a plain-words reason it did not complete (the same cause-and-line shape as a
  destination's failure reason), and says when the governor is holding the upload back so a live
  stream keeps its bandwidth. **Nothing posts without that click:** every publish route is
  session-only (no API token of any scope, no MCP tool), `POST /publish` needs a one-time
  confirmation minted for exactly the reviewed form, and the queued job is useless without the
  nonce the click created. Each clip, platform and account has one ledger row that records the
  platform's post id, so a second click, a second tab or a retry never posts twice; YouTube
  resumes from its stored session and TikTok polls its stored `publish_id`. YouTube reuses the
  existing connection (the `youtube` scope already allows uploads; **Reconnect to allow uploads**
  appears if a grant lacks every upload scope) and costs 1,600 quota units per upload. TikTok has
  a new sign-in for posting only (Settings, Platforms) and is **always posted private** until
  TikTok audits the app, which the dialog says; X publishing is built and **off**
  (`POLYEMESIS_X_PUBLISH`, separate from the Livestream switch). New routes `GET /publish/targets`,
  `POST /publish/prepare`, `POST /publish`, `GET /publish/{id}`; a request whose last step may have landed (`outcome_unknown`) is only re-armed once the person confirms they checked the account; new job kind `clip.publish`; one
  new table `clip_publications`; the TikTok capability row now reads Works for sign-in. In all 15
  languages. See docs/CLIP-PUBLISHING.md.

- **The installer sets up caption fonts, CJK included.** In binary mode `install.sh` installs
  fontconfig, DejaVu (Latin, Greek, Cyrillic, Arabic, Hebrew) and Noto CJK with the host's own
  package manager (apt-get, dnf, apk or pacman) and runs `fc-cache -f`, so burned-in captions in
  Chinese, Japanese and Korean render out of the box. The plan shows what it will install and the
  download size (CJK is about 60 MB on apt, up to about 190 MB on pacman); `--no-cjk-fonts` leaves
  CJK out and keeps the rest. Fonts already present are left alone, and a package that will not
  install is a warning, never a failed install. The generated `update.sh` installs any that have
  gone missing before it stops the service, keeping the install-time choice. See
  docs/INSTALL.md, "Caption fonts".

- **Broadcast delay: Dump (#11).** A delayed destination that is on the air can drop the newest
  part of its delay buffer, so what just happened never reaches that platform, and it stays on the
  air. Choose everything since the last dump or a number of seconds, from the card's delay panel,
  a Stream Deck **Dump delay** key (one destination or every delayed one, hold to confirm),
  `POST /destinations/{id}/delay/dump`, `POST /destinations/delay/dump-all` (needs
  `{"confirm": true}`), or the MCP tools `dump_destination_delay` and `dump_all_delays`. The cut and
  the resume are at IDR keyframes and the timestamps run on unbroken, so the platform sees no
  corrupt frame, no gap and no jump. A dump never leaves less than 2 s plus one GOP of delay. The
  delay stays shorter until the destination next starts: the video is copied, so it cannot be
  stretched back, and a slate or freeze to refill it is not safe on every platform without an
  encoder per destination. The status payload's delay gains `effectiveSeconds`, `dumpedSeconds`
  and `dumpable`, `destination.delay` gains the `dumped` reason, every dump raises the new
  `delay.dumped` alert, and `polyemesis_destination_delay_effective_seconds` joins the metrics.
  The Stream Deck **Stop destination** key gains a **Cut the delayed tail** option and shows the
  tail left while it airs. In all 15 languages.

- **S3-compatible offload of finished recordings, and a pull-CDN-ready player origin (Wave 4 #10).**
  Storage targets (AWS S3, Cloudflare R2, Backblaze B2, MinIO, Wasabi) on the Recordings page and
  under `/api/v1/storage/targets`: endpoint, bucket, region, a key-prefix template, path-style
  addressing and a write-only secret sealed with the install's key, never returned, and used only
  for the address it was saved for. **Test** writes, reads back and deletes a small object and names
  the step that failed. Offload is **mirror mode**: finished segments are copied -- each checked on
  receipt (signed SHA-256 and Content-MD5) and verified after upload (size, ETag, SHA-256 metadata,
  optionally a read-back), files above 64 MiB in resumable parts -- and no local file is ever moved
  or deleted; retention is unchanged. Stems, transcripts and clips can be copied too. Repeated or
  permanent failures make an upload **stuck**, raised as the `offload.stuck` alert, webhook and
  event and counted per target over MQTT (a Home Assistant sensor each), with **Retry now** per
  target and per recording; the Library shows each recording's state. Moving a target to another
  address re-uploads its files, and an upload still running against the old address cannot record
  its result. The player
  origin now sends `public` caching only for an open stream (HLS segments numbered from the wall
  clock, a year in the browser and the seekable window at the edge; one-second playlists),
  `private`/`no-store` for anything token-gated or signed in and for every refusal, and an `ETag`
  on every file. See docs/STORAGE.md and docs/CDN.md. In all 15 languages.
- **Highlight suggestions in the clip editor (Wave 4 #5a).** *Find highlights* queues a
  `highlights.scan` job that looks for chat bursts over the stream's own baseline and loudness
  peaks (EBU R128, measured from the recording), and, if you tick it and switch on **Use AI for
  highlight scoring** in Automod settings (off by default), has the Automod model's provider score
  the transcript excerpts in one call. Results land in a review queue with each
  suggestion's span, score, the signals that fired and the words spoken; you accept (an ordinary
  clip export, with the editor's settings) or dismiss. Nothing is clipped until you accept, and a
  rescan does not offer a moment you already decided again. In all 15 languages.
- **Vertical shorts (Wave 4 #5b).** The clip editor exports a 1080x1920 H.264/AAC MP4 for Shorts,
  TikTok and Reels: centre crop with a pan, face cam stacked over gameplay (a box you draw on a
  still of the frame), or the whole frame over a blurred copy. Captions are burned in from the
  transcript with the bundled font, in a style you choose, and layouts can be saved as templates.
  A short over 60 seconds exports with a warning rather than being refused. Clip encodes run
  under the job governor's priority wrapper so they yield to a live broadcast, and the playout
  offline card does not offer a 9:16 short. In all 15 languages.
- **X go-live announcement: once per show by default (#3, PROVISIONAL).** An X destination has a
  **Go-live announcement** setting in the X console (`xGoLiveTweet`): *on the first go-live of a
  show only* (the default), *on every go-live*, or *never*. A show lasts while the destination is
  on. The broadcasts polyemesis opens again within it, after X ends or times out the previous one,
  are published with `should_not_tweet`, so a show no longer posts a fresh announcement on every
  renewal. Switching the destination off ends the show. `lifecycle.renewed` marks a renewed
  broadcast. Any other value is refused, and a destination that is not X has the setting removed
  on save, with a warning. PROVISIONAL: `should_not_tweet` is a boolean in the PUBLISH body per
  X's documentation, and X Livestream stays gated until the app is whitelisted. In all 15
  languages.
- **X console: a go-live title, the broadcast's state, and the Livestream gate (#3).** An X
  destination's editor shows whether X Livestream access is switched on (`POLYEMESIS_X_LIVESTREAM`;
  "needs X Livestream access" while it is off), where its X broadcast stands, and a go-live title.
  X takes a title only when the broadcast is published, so polyemesis now sends that title on the
  publish step (or a recurring show's rendered title during its first 30 minutes). The field is
  read-only while the broadcast is live, and the server refuses a change then, because X cannot
  change a running broadcast's title. The title is at most 280 characters (PROVISIONAL: X gives no
  number). After publishing, polyemesis tells the X chat adapter the new broadcast straight away
  instead of waiting for the adapter's 15-second check, which stays as the fallback. The setup
  guide now names `POLYEMESIS_X_LIVESTREAM` and `POLYEMESIS_X_APP_BEARER`. All of it is built from
  X's documentation and waits for a whitelisted app to verify. X's scheduled broadcasts are
  documented by X but not created yet.

- **Live captions: custom vocabulary, masked words, a live cue log and VOD alignment (#8 step E).**
  Each programme can hold a caption policy: words spelled the way you want (a recognised word
  that matches one, ignoring case, is written in your spelling) and words masked on screen
  (`d***`). It is applied once, where captions leave the captioner, so the dashboard, the player
  track, SEI, YouTube POST and the exports all show the same words, and a save reaches captions
  that are already running without restarting speech recognition. Entries are single words
  (letters, digits, inner apostrophes), at most 200 per list and 48 characters each. Every live
  caption is also kept in the programme's cue log (`<data>/captions/cues-source-<id>.jsonl`, at
  most 64 MiB on disk) with the wall-clock moment it was spoken, and the caption export takes
  `source=live` to serve what viewers saw during a recording, lined up with the recording's own
  timeline. The stored-transcript export now applies the programme's policy too. The player
  sidecar `captions.vtt` names its epoch (`X-POLYEMESIS-EPOCH-MS`) and places each cue when it
  was spoken, so a player can line it up through HLS program date-time.
- **Live captions on the public player, in the Library, as downloads and burned in (#8 step E).**
  The public player shows the live captions as a *Captions* track, placed on the picture through
  HLS program date-time, live and after a DVR seek. A recording's player in the Library shows its
  transcript, or else the captions viewers saw live, and *Export captions* downloads either as SRT,
  VTT or JSON. A programme's *Caption words* (spellings and masked words) are edited under
  *Sources*. A rendition can burn the live captions into its picture, at the bottom or the top:
  FFmpeg's `subtitles` filter reads its file only once (measured on the shipped FFmpeg 8.1.3), so
  the burn-in is `drawtext` re-reading a burn file the captioner replaces on every caption, in the
  built-in font. It needs a fixed width and height, and a change restarts the tier.
- **SEI captions are re-sent every 2 s while on screen (#8).** A viewer who joins a Twitch
  stream after a caption's End Of Caption now sees that caption within one segment instead of
  waiting for the next one.

- **Live captions to YouTube by HTTP POST (#8, PROVISIONAL).** A YouTube destination on
  *caption ingestion (HTTP POST)* now posts each caption, after its delay, to the caption
  ingestion URL of its own YouTube stream, through a bounded per-destination queue that never
  holds up the other outputs. Press **Refresh stream key** once after choosing the route: it
  creates the destination's own stream and stores its caption URL, so **the stream key changes
  once**. The pre-announce and recurring sweeps never do this; they only reuse a stored URL.
  Broadcasts polyemesis schedules for such a destination are created with caption type HTTP
  POST, the monitor stream on and the destination's 30 s or 60 s delay. The destination will
  not start without a caption URL for its current key (409 naming the button), and a scheduled
  broadcast that is not set for POST captions is not taken to testing or live (the card says
  why; the stream keeps going out). The URL is stored encrypted, apart from the destination,
  and is never in the console, the API, logs or events. PROVISIONAL: YouTube does not publish
  the POST body format, so it is isolated in one function and needs a live check on a test
  channel. Ultra-low latency is not checked because polyemesis never sets it.

- **X go-live lifecycle (#3, coordinator slice).** With the X Livestream gate open,
  enabling an X destination opens its broadcast on the stream source, unpublished. The
  broadcast goes live once bytes arrive, and disabling the destination while it is on air
  ends it. A finished broadcast on a destination that is still enabled is replaced by a new
  one for the next show, so a second show needs no manual step. The coordinator reads all six
  X states explicitly. The gate closed means nothing is opened and nothing is faulted. The
  stream source key stays out of faults and logs. PROVISIONAL: no live X account has run this
  path yet.
- **Live captions on Twitch as SEI, behind the broadcast delay (#8 step D).**
  A Twitch destination whose caption mode is SEI now reads through a caption
  stage between the stream and its FFmpeg. The stage adds one CEA-608 caption
  SEI per picture on the copied H.264, so Twitch receives the captions inside
  the video, and video is still copied. Captions are placed on the pictures live
  at delivery time, so a delayed destination's captions follow its delay (#11).
  Turning SEI captions on or off for a running destination restarts that
  destination, as a delay change does. Kick is still off, and Facebook SEI stays
  behind its provisional flag. Twitch has not been checked against a live
  channel yet. The RTMP `onCaptionInfo` fallback is not built.
- **Live captions: engine wiring (#8 step B).** The captioner now runs for a
  programme when one of its enabled destinations has a caption mode its platform
  accepts, on the track the programme chose. Each captioned destination receives
  its captions at its broadcast delay (#11), and stopping the captioner cancels
  every caption still waiting. The health guard's switch-off stays latched until
  an operator saves a caption setting. Captions pass one text policy at the emit
  boundary. No output consumes the delayed captions yet (the SEI injector and the
  YouTube caption POST are later steps), and no operator setting stores the
  custom vocabulary or profanity filter yet, so the policy is empty.
- **X (Twitter) Livestream, behind X's whitelist (wave 3, #3).** X sign-in
  (OAuth 2.0 with PKCE, `broadcast.read`, `broadcast.write`, `offline.access`,
  `users.read`, `tweet.read`), stream-source minting, publish and end of an existing broadcast, chat read
  over the Activity API, chat send (140 characters), and mute, unmute and delete.
  **Switched off until X whitelists the app.** Set `POLYEMESIS_X_LIVESTREAM=1`
  once it is approved; until then the operator sees "needs X Livestream access
  (app must be whitelisted by X)" and pasting the key works as before. X has no
  hide action, so moderation offers delete and mute only. Metadata is NOT at
  parity with YouTube: X has no endpoint that changes a running broadcast's title
  or chat option, so polyemesis refuses every metadata push to X. PROVISIONAL until a
  whitelisted app has run it live: the PKCE acceptance, the broadcast.chat
  payload, and the activity stream's app bearer, which is supplied as
  `POLYEMESIS_X_APP_BEARER`. Titles are set at go-live only, so a live title
  cannot change. **Not built in this PR (deviation from #3):** creating the
  broadcast itself. Create it in X's producer tooling; polyemesis can then
  publish and end it.
- **Twitch VOD track (#15): what is verified and what is not.** The second
  (VOD) mix is now checked against this FFmpeg before it is sent: a binary
  that cannot write two audio tracks into FLV drops the VOD mix with its
  reason on the card, and the live stream goes out alone. A new second mix
  defaults to everything except music. Live mute reaches the VOD mix's copy of
  the same track, and so does live gain except in a matrix VOD mix (gain there
  is per cell). Twitch acceptance of the two-track publish is PROVISIONAL until
  a live test on a Twitch test channel.
- **Twitch VOD track (#15): a meter for each mix, and matrix gain says it
  restarts.** Every destination with a VOD mix now has a second loudness
  reading on the Meters page, labelled "VOD mix", beside its live one. Each
  mix has its own analyser process reading the destination's relay, so the
  command line that publishes is unchanged. The analyser for a paired graph
  previously could not start at all, because FFmpeg refused the unused VOD
  output; it now terminates the other mix. A gain change on a matrix VOD mix
  restarts the destination for a moment, which interrupts the live mix too, and
  the VOD editor says so. A live fader gain does not reach a matrix VOD mix, and
  the response now says so. Alerts stay on the live mix. The startup FLV probe
  fails open: a probe that times out, cannot create its temporary directory, or
  cannot start FFmpeg records "probe inconclusive" and keeps the VOD mix; only a
  real refusal by this FFmpeg drops it.
- **Live captions, part 1: the in-band path (#8, not yet switched on).** New
  `internal/captions` package. It builds CEA-608 pop-on captions (RCL, row
  addressing, 32-column wrapping, EOC and EDM, the basic and special character
  sets), and a Go SEI injector that rewrites the H.264 PID of an MPEG-TS stream
  so each picture carries the caption pair due at its presentation time. Pairs
  are keyed on picture timestamps, not arrival order, so B-frames read back in
  order; a short reorder window is the only added latency. Verified by FFmpeg's
  own CEA-608 decoder (host FFmpeg 9.0.2) reading the injected captions back.
  **PROVISIONAL:** the Twitch path needs a live check on a test channel before
  it is offered. Nothing calls it yet: no API route, no UI control, and no
  destination path runs the injector.
  **Not built in this PR (deviation from #8, which is agreed in full):** the
  transcriber is not wired to a routed caption track; the per-destination mode is stored
  but not read by the engine; the player WebVTT track and the burn-in are
  not wired into renditions; and the YouTube caption POST is not called. The
  pieces above are library code, not a delivered feature.
- Caption text policy: vocabulary spellings and profanity masking, applied once
  at the emit boundary so every output shows the same words. Timing, track and
  speaker are unchanged.
- Live captions settings (#8): a per-destination caption route, and a per-programme
  caption track. The route is chosen on a destination (off, Twitch in-stream CEA-608,
  YouTube caption ingestion, or Facebook in-stream, which is provisional and needs
  `POLYEMESIS_CAPTIONS_FACEBOOK=1`). Kick offers no route. YouTube caption ingestion
  needs a 30 s or 60 s broadcast delay, and a route cannot change while the destination
  is live. The track is chosen on a programme, and "automatic" is the default. Both are
  stored and shown, and the engine does not send captions from them yet. See
  `docs/CAPTIONS.md`.
- Per-destination caption mode (`destinations.caption_mode`, default `off`).
  The console has a picker for it and the API stores it, but the engine does not
  read it yet, so the choice changes nothing on air. Twitch SEI is marked go,
  and its on-stream display still needs a live check. YouTube's embedded
  CEA-608 route is go. YouTube's caption POST is provisional and needs a 30 s
  or 60 s broadcast delay. Facebook SEI stays off behind a flag. Kick is
  skipped: Kick documents no caption route for live streams.
- Burn-in filter builder for the `subtitles` filter. The shipped image's FFmpeg
  (8.1.3-r0 on the pinned `alpine:3.24` digest) has `subtitles` (libass) and
  `drawtext`; checked in a local container on 2026-10-31, amd64 only. Not yet
  wired into renditions.
- The injector drops video packets whose adaptation field runs past the packet
  and counts them. It bounds the units it holds, releasing units over the cap
  unchanged, and it restarts its caption schedule on a clock jump in either
  direction.

- **YouTube vertical half (API, #7; PROVISIONAL).** A YouTube destination can now
  pair with a 9:16 vertical destination. `POST /api/v1/destinations/{id}/youtube-vertical`
  creates the vertical liveStream through the API and stores it as its own destination
  with a portrait rendition. The pair is recorded in polyemesis, not in YouTube:
  the Live Streaming API cannot attach a vertical companion, so the one-time Live
  Control Room step (choosing this key as the vertical "second stream key") stays a
  human action, recorded by `POST .../youtube-vertical/confirm`. The horizontal
  broadcast never binds to the vertical stream, and the vertical stream is kept out of
  the first-RTMP fallback. `GET .../youtube-vertical` reads its health by stream id,
  and a failed read stays on that half. The status read is paced to one YouTube call a
  minute per pair, and only while one of its halves is live (correction 6 of the YouTube
  truth sheet). Each status read is charged to the chat quota budget and stops at its send reserve.
- **YouTube vertical in the console (#7; PROVISIONAL).** A YouTube destination offers a
  "YouTube vertical" section in its dialog. It picks one of two 9:16 tiers, adds the
  vertical half, and walks the one-time Live Control Room steps with a confirm button. The
  pair shows as one dashboard card with a vertical sub-row. That sub-row has its own
  start, stop and restart, and a failed half shows there without stopping the other. The
  card and the go-live sheet keep a reminder until the step is confirmed. polyemesis records
  the confirmation and does not verify it. The Stream Deck key for a pair covers both halves
  (red "Vertical half failed", amber "Vertical half down").
- **Planned rollover (preview).** A destination can be replaced before its
  platform's cap: a live YouTube broadcast is swapped for a new one on the same
  stream, and a Twitch destination is reconnected. Triggers are a safety margin
  against a stated cap, a daily quiet window, or an OBS scene. Other platforms
  show a countdown only. Built against fakes; the YouTube auto-start and Twitch
  reset behaviour are provisional until checked on a live account. See
  [docs/ROLLOVER.md](docs/ROLLOVER.md).
  **Not built in this PR (deviation from #19):** the countdown card and the
  settings control in the console. The API serves the same data. A manual run
  that the platform refuses answers 502 with the reason.
- **Recurring broadcast events (preview).** A start schedule can carry a broadcast
  template: a title pattern with `{date}` and `{time}`, a description, privacy,
  category and thumbnail. The next occurrences (default 2, up to 10) are created as
  upcoming YouTube events, and the go-live uses the first one that is not skipped.
  One occurrence can be skipped or retitled, and deleting the template cancels its
  upcoming events, but never one whose start has passed, one on a destination that is
  delivering (kept and retried after it stops), or one YouTube reports as no longer
  upcoming; a skip of such a show is refused with a 409. Facebook, Twitch, Kick and X
  are not given template events yet,
  and the calendar screen and any console UI are not built in this PR (deviation
  from #18); the API serves the data. See
  upcoming events. Facebook and X are not given template events yet; Twitch and Kick
  take the title and category at go-live (see the entry below). The calendar screen
  and any console UI are not built in this PR (deviation from #18); the API serves
  the data. See
  [docs/RECURRING-EVENTS.md](docs/RECURRING-EVENTS.md).
- **Recurring events: Twitch and Kick go-live, skip backfill, safer cancels (#18).**
  Twitch and Kick take the template's title and category when the show starts,
  once per show, through the go-live metadata write. A skipped occurrence no longer
  uses up a look-ahead slot: the next occurrence fills it. A skip is refused with
  409 for a show that has started or a destination that is on air. A template event
  whose title and cancel both fail is kept as stranded and its cancel is retried,
  rather than shown as an upcoming show with no title. YouTube template events are
  deleted only while `created` or `ready`. A started show's platform event is never
  removed by an orphan sweep.
- **Shared console kit.** One empty state (`EmptyState`), one status pill and
  vocabulary (`StatusPill`, `lib/status.ts`), one disabled-control reason
  (`DisabledReason`), and one save model with a save status line (`useSaveModel`,
  `SaveStatus`). Pages pick these up as they are touched. See
  [docs/DESIGN-SYSTEM.md](docs/DESIGN-SYSTEM.md).
- **OBS bridge: go live and stop when OBS does.** Polyemesis now hears
  about OBS in one of two ways. It can connect to obs-websocket 5 on your LAN
  or over Tailscale: the password is stored sealed, the link reconnects on its
  own, and its state shows in Settings. Or the new OBS Python script
  (`integrations/obs`) can post to `POST /api/v1/obs/events` when polyemesis
  cannot reach OBS. When OBS starts streaming, the destinations you picked
  start. When it stops, they stop too, if you have turned that on. Turning it
  on asks you to accept that stopping ends a YouTube broadcast for good, and
  you can set a grace period that a quick restart of OBS cancels. A dropped
  connection or OBS reconnecting never stops anything. Scene rules map an OBS
  scene to a routing preset. A switch is applied as live track mutes and never
  restarts a destination; anything that would need a restart is skipped, and
  the status says why. New `obs` token scope: it reaches
  `POST /api/v1/obs/events` and nothing else. New events `obs.connection`,
  `obs.stream` and `obs.scene`. See [docs/OBS-BRIDGE.md](docs/OBS-BRIDGE.md).
- **Streamer.bot actions.** `integrations/streamerbot` adds actions that call
  the REST API with a token you keep in a Streamer.bot global: go live, stop
  all, start or stop a destination, mute or unmute a track, recording on or
  off, and a status line. Mix It Up can make the same calls from
  `actions.json`. It is source plus setup steps, not an import string. See
  [integrations/streamerbot/README.md](integrations/streamerbot/README.md).
- **Broadcast delay per destination.** One destination can hold its output
  back by up to 15 minutes (`delaySeconds`, 0 to 900) so you can cut
  something before it airs there. Every other destination is unaffected and
  video is still copied. The card shows "Delayed 30 s" and the buffer filling,
  and the Stream Deck key shows a delay marker. Stop plays the buffered tail
  out before the destination ends. The card then says "Airing delayed buffer:
  N s left" with **Cut now**. **Stop now** (`?now=true` on the stop route, or
  `now: true` on the MCP tool), Stop all and delete cut the tail at once.
  Failover passes through the delay. Changing the delay while live restarts
  the destination. YouTube goes live, and is ended, only when the delayed data
  arrives and when the tail has played out. Up to 64 MiB per destination is
  held in RAM, and the rest spills to the data directory. Disk use is capped
  at the delay at 50 Mbit/s and must leave the recording guard's free-space
  floor. When the buffer runs out of room it drops data and keeps the delay.
  New: `destination.delay` events, `polyemesis_destination_delay_*` metrics,
  and the delay in MCP status tools. See
  [BROADCAST-DELAY.md](docs/BROADCAST-DELAY.md).

- **Chat polls across every platform.** Ask a question with 2 to 6 options
  from the console's Polls panel, a Stream Deck key or an AI assistant over
  MCP (`start_poll`). Polyemesis posts it into every connected chat. Viewers
  vote by typing the number or `!vote N`, one vote per viewer per platform,
  and the last vote counts. Automod-blocked messages are not votes, and a
  banned or timed-out viewer's vote is dropped for the rest of the poll. The
  result shows live with a per-platform breakdown. History is kept and can be
  exported as CSV or JSON. Results are counts only and never name a voter, so
  `read` tokens can see them. A logo-free poll widget for OBS uses your chat
  overlay's key and style. The `poll.started`, `poll.vote`, `poll.ended` and
  `poll.display` events are on the events stream. Optionally, a poll also
  runs as a native Twitch poll and as a YouTube live poll. While one runs,
  that chat votes there: its typed numbers are not counted, so nobody is
  counted twice. If the native poll fails or stops early, typed votes count
  again. Twitch's count updates live. YouTube's count is merged when the poll
  closes, because its API can't read a running poll. A YouTube poll costs 40
  API quota units and is skipped if they don't fit above the send reserve.
  The Twitch mirror adds the `channel:manage:polls` scope, so **connected
  Twitch accounts are marked "reconnect needed" once.** YouTube needs no new
  scope. Over MCP, `get_polls` is in the Read level, and `start_poll` and
  `end_poll` are in Control and both ask for confirmation first, because each
  posts into every chat as the streamer. See [docs/POLLS.md](docs/POLLS.md).
- **Degraded-link failover.** Failover can now leave a primary that is still
  connected but failing. Set a bitrate floor, an RTT ceiling and a loss ceiling
  (RTT and loss for SRT and SRTLA encoders only), and how long a breach must
  last, per programme on its ingest settings. The backup, the playlist or the slate then goes on air, and the reason
  starts `degraded: ` and says what was measured. The link must read healthy
  for the same time before it counts as recovered, and your return rule then
  applies. Off by default. The reason reaches status, the timeline, the events
  stream, a new appended `failover.switched` webhook trigger, MQTT
  (`failoverReason`, `degraded`) and the Stream Deck. New metrics:
  `polyemesis_failover_degraded`, `polyemesis_failover_active`,
  `polyemesis_ingest_link_rtt_seconds`, `polyemesis_ingest_link_loss_ratio`
  and `polyemesis_srtla_links`, graphed in the Grafana Ingest row. See
  [docs/LINK-HEALTH.md](docs/LINK-HEALTH.md).
- **SRTLA receiver for bonded IRL encoders.** Set **Settings → Ingest → SRTLA
  bonding port**, for example 5000/udp, and BELABOX, Moblin or IRL Pro can bond
  several cellular links into one SRT publish to a programme, using its usual
  token. The source card shows each link's bitrate and share. Off by default.
  It is native Go code, tested against BELABOX's reference `srtla_send`; no
  AGPL code was copied. See [docs/SRTLA.md](docs/SRTLA.md).
- **Chat commands.** `!bitrate` for anyone, and `!switch
  primary|backup|playlist|slate|auto` and `!fix` for moderators and the
  broadcaster, acting on one chosen programme and answered through the chat
  send. Off by default. See
  [docs/LINK-HEALTH.md](docs/LINK-HEALTH.md#chat-commands).
- **Live link stats on the source card.** An SRT publisher now shows its
  bitrate and its loss over the last second or so.
- **Automod: two model checkers, with automatic fallback.** The primary
  defaults to a typed-decision moderation model on the **Jev** protocol:
  hosted TypeSafe Jev (`jev-1.14.0`), or a local Jev-compatible model such as
  `laya` on Ollama. An optional **fallback** speaks the OpenAI-compatible
  protocol. It is asked only when the primary times out, errors, gives a reply
  it cannot read, runs out of budget or is rate-limited. A message passes
  unchecked only when both fail. See
  [docs/AUTOMOD.md](docs/AUTOMOD.md#the-model-tier).
  - **Presets:** OpenAI, Cerebras, Groq, OpenRouter, Ollama and Custom on the
    OpenAI-compatible side; TypeSafe Jev, Ollama (laya) and Custom on the Jev
    side. Each preset carries its JSON mode (strict `json_schema` where the
    provider enforces it), its reasoning setting (`reasoning_effort: "none"`
    for Cerebras), and a privacy line on the card that says where messages
    go.
  - **Separate settings:** each checker has its own key (bring your own;
    polyemesis ships none), budget, timeout and counters.
  - **Test first:** a checker can't be switched on until its **Test** button
    has passed three made-up messages. The server enforces this.
  - **Keys stay bound:** every key is bound to the endpoint it was entered
    for, and is never sent anywhere else. Moving the address clears the key
    unless you tested the new address with its key.
  - **Jev gating:** the confidence floor applies to Jev's probability that
    the message breaks your rule. Between 0.5 and the floor, a message is only
    flagged.
  - **Per-category actions:** harassment, hate speech, threats, sexual
    content, spam and other each get their own action. Every category starts
    on flag; the card suggests spam → delete and timeouts for the rest.
  - **Local presets are flag-only.** The card warns that CPU-only hosts will
    probably miss the timeout.
  - **Who decided:** the card, the moderation log and `chat.moderation` events
    (`decidedBy`) say which checker made each decision.
  - **Upgrades:** a model block saved before this is read as
    OpenAI-compatible with a custom address, so existing setups keep working
    unchanged. The single model action carries over to every category.

- **Stream Deck: a Destination key shows every issue polyemesis reports for its
  stream.** Beside the states it already showed, the key now shows:
  - **Reconnecting ×N**: more than five restarts in 15 minutes, the threshold of
    the restart-storm alert.
  - **Dropping frames**: for a destination fed by a rendition, or one that
    re-encodes. A destination that passes its video straight through never
    shows it.
  - **YouTube: poor**: YouTube's own stream-health verdict, for a destination
    with a connected account.
  - **Reconnect account** (red) or **Account expiring**.
  - **On backup source**, **On playlist**, **On slate** or **Source degraded**:
    shown on every destination its programme feeds, not only on the Failover
    key.

  When several are true at once, the key shows the most severe one with a
  **+N** badge. During a restart storm on an expired account, the key holds red
  **Reconnect account** through every reconnect instead of flipping colour. The key's settings list every current issue. The plugin adds no
  requests. `GET /api/v1/status` gains four optional destination fields:
  `restartStorm`, `droppingFrames`, `accountHealth` and `platformHealth`. Each
  is absent while there is nothing to report. `accountHealth` is the state word
  only, so a read token sees no account detail. The YouTube read is read-only,
  happens at most once every 5 minutes per destination, and one read is shared
  by every destination on a channel. Twitch has no stream-health API, so a
  Twitch destination never shows a platform face. See
  [docs/STREAM-DECK.md](docs/STREAM-DECK.md#what-the-faces-mean).
- **A startup warning on FFmpeg 9.0 and 9.0.1.** Both releases shipped a
  scheduler change that can misorder output, end streams early and use extreme
  memory, and 9.0.2 reverted it (FFmpeg commit 9d4ca2122). A destination that
  mixes several audio tracks is the shape it affects. polyemesis still starts
  on them, but the banner and the log now name the binary and say to install
  9.0.2 or later, or an 8.1 release. 9.0.2 and 8.x stay quiet. FFmpeg 9.0.2 is
  tested and supported; see docs/INSTALL.md.

- **`destination.held`: page on the stops that won't fix themselves.** When
  polyemesis stops retrying a destination after repeated quick rejections, it
  now raises a `destination.held` alert (`critical`, on the timeline) and sends
  a `destination.held` webhook. Both fire once per hold, not on every check.
  Both are subscribable in the alert-rule and webhook pickers. The events
  stream's existing `destination.held` is now the same edge, so it is never
  announced twice. Before this, a hold appeared only as `held` inside
  `destination.down`, which is unchanged. A rule or hook whose event list is
  empty means "everything", so it starts receiving these on upgrade. See
  [docs/MONITORING.md](docs/MONITORING.md) and [docs/HOOKS.md](docs/HOOKS.md).
- **Chat overlay URLs with the key in the path.** Some OBS browser sources drop
  the part of a URL after `#`, where the overlay key is, and the overlay stays
  empty. `/overlay/k/{key}` and `/overlay/dock/k/{key}` serve the same pages,
  and Settings offers them under **Copy OBS URL (key in path)**. The `#` form
  stays the default, because a reverse proxy logs paths. Polyemesis's own log
  never records the key, and the pages send `Referrer-Policy: no-referrer`. See
  [docs/OVERLAY.md](docs/OVERLAY.md#the-key-in-the-path).
- **MCP server, off until you turn it on.** `POST /mcp` lets Claude Desktop,
  Claude Code or any Model Context Protocol client read what is on air and, if
  you allow it, act on it. Setup, with config examples, is in
  [docs/MCP.md](docs/MCP.md).
  - Opt-in in three layers. A switch under **Automation → AI assistants** is
    off on new and upgraded installs, and while it is off every method on
    `/mcp` answers the `404` an unknown endpoint gets. Under the switch are
    three levels: Read, Content (chat and transcripts) and Control (every
    write tool). The first enable turns on Read only. Only a signed-in
    operator can change these; no API token can, an admin one included. Every
    change is audited, and turning on Control is `critical`.
  - Streamable HTTP, stateless, on the official Go SDK (v1.8.0; protocol
    `2026-07-28` back to `2024-11-05`). `polyemesis mcp -url ...` relays stdio
    to it for clients that only start local servers. The token comes from
    `POLYEMESIS_TOKEN` or a file, never the command line.
  - Authenticated with MCP tokens: the new `mcp-read` and `mcp-admin` scopes.
    `/mcp` refuses every other API token, and an MCP token works nowhere but
    `/mcp`. A browser session is refused. An `mcp-read` token reaches the Read
    level: 9 tools for status, destinations and their failure causes, sources
    and link stats, routing presets, levels, recordings, timeline and alerts,
    and stop impact. An `mcp-admin` token reaches every level that is on,
    adding chat and transcript search, the chat user card, and 11 write tools:
    start, stop, retry, go live, stop all, mute and gain, apply a routing
    preset, recording on/off, failover switch, create a clip, and schedule a
    start or stop.
  - Every tool is a call to the HTTP API with the caller's own token at its
    base tier, so scopes, `?source=` and the API's own checks apply unchanged.
  - Stop tools, go live, retry, routing presets, recording off and scheduled
    stops do nothing without `confirm: true`, and answer with what they would
    do: what a stop ends, whether a retry interrupts a live output or lifts a
    hold, and which tracks a preset changes and whether it restarts the
    destination. Write tools carry MCP destructive hints.
  - No tool returns a credential, to any token. Requests made for MCP get the
    read-safe views, and every answer is checked against every stored
    credential.
  - Chat and transcript text is fenced as untrusted viewer content. Arguments
    are checked strictly, and none can choose the route a tool calls.
  - Every write is a new `mcp.action` audit event, "Action via MCP (token
    name)", naming the tool, target and outcome.
  - Viewer analytics is not a tool yet; it comes with #4.
- **Chat overlay and OBS dock.** One chat overlay for every connected platform,
  as an OBS browser source (`/overlay`), and a denser dock (`/overlay/dock`).
  Both are display-only. Set them up under **Settings → Chat overlays**.
  - Each overlay has its own platform filter, so a scene sent to Twitch can
    show only Twitch chat. The filter is applied on the server.
  - Four style presets (classic, bubbles, bold outline, compact strip) with
    font, size, colours, background opacity, fade time and line count, a live
    preview, and **Copy OBS URL**.
  - A message appears only after automod has judged it. With the model
    checker on, a message waits up to 4 seconds for its verdict and is left
    off the overlay if none arrives. Hidden, deleted and automod-blocked
    messages never appear, and a message removed after it was shown leaves
    the screen at once. Lines carry no user IDs, accounts or moderation data.
  - Each overlay has a display-only key, in the URL fragment so it never
    reaches server or proxy logs. The key reads that overlay's config and
    display stream and nothing else. Rotating or revoking it in Settings ends
    open overlays within seconds.
  - New event types `chat.display` and `chat.undisplay` (topic `overlay`) on
    `GET /api/v1/events`. See [docs/OVERLAY.md](docs/OVERLAY.md).
- **Real-time events API.** `GET /api/v1/events` streams what is happening as
  Server-Sent Events, in the same envelope as webhooks: destinations going up,
  down, held, restarted or changing level (live or by restart), the ingest,
  failover switches, a platform broadcast going live or ending, the recorder
  starting and stopping, chat messages, retractions and moderation actions,
  and operator alerts. A destination's events carry the classified `cause`,
  `held`, `keyChanged` and the whole failure reason.
  - Authenticated like the rest of the API: a bearer token or the session
    cookie, never a token in the URL. A `read` token gets everything except
    chat and the audit alerts that are not on the timeline.
  - Every event is numbered. A client that reconnects with `Last-Event-ID` is
    replayed what it missed, for up to 5 minutes, or told `stream.gap`.
  - A client that falls 256 events behind is disconnected, not silently
    skipped, and resumes from the replay. A heartbeat every 15 seconds keeps
    proxies from cutting a quiet stream. Revoking a token, or changing the
    password, ends an open stream.
  - Webhook redaction applies to every event before it is sent or held for
    replay.
  - `?topics=` filters by family.
  - curl, browser, Node and Python examples are in
    [docs/EVENTS.md](docs/EVENTS.md). Polls, viewer samples and the failover
    degraded reason are listed there as coming with #14, #4 and #6.
- **A Grafana dashboard and Prometheus alert rules, shipped.**
  `deploy/grafana/polyemesis.json` (Grafana 10 or later) has a **Programme**
  picker and rows for overview, ingest, destinations, host and recording, plus
  a placeholder row for viewers and playback quality. The destinations row
  shows why each failing destination is failing, whether it is held, and
  failures by cause. `deploy/prometheus/alerts.yml` adds destination down
  (naming the cause), ingest down, recording disk low, and restart storms.
  `deploy/monitoring/` runs Prometheus and Grafana with both pre-provisioned.
  A CI test fails if the dashboard or the rules query a metric the server does
  not export. [GRAFANA.md](docs/GRAFANA.md)
- **Failure causes on the Prometheus scrape.** Three new families, each
  labelled `id` and `name`:
  - `polyemesis_destination_failure_reason{cause}`: 1 for the current cause;
  - `polyemesis_destination_held`;
  - `polyemesis_destination_failures_total{cause}`: failed runs by cause.

  `cause` is always one of the nine published codes, never FFmpeg's text.
  Every cause is emitted at 0, so `increase()` sees the first failure.
  [MONITORING.md](docs/MONITORING.md#on-the-scrape)

- **Why a destination is failing.** When a destination fails, polyemesis
  reads the end of FFmpeg's stderr and the timing of the run, and names the
  cause. The causes are `key_rejected`, `platform_not_ready`, `dns`,
  `connection_refused`, `timeout`, `tls`, `codec_rejected`, `upload_too_slow`
  and `unknown`. For `unknown`, the card shows FFmpeg's raw last line.
  - The destination card shows the cause in place of the raw line.
  - `GET /api/v1/status` carries it as the destination's `reason`.
  - The `destination.down` alert text and its timeline row include it, and the
    alert gains `cause`, `reason` and `detail` fields.
  - Webhooks gain `cause`, `held` and `keyChanged` fields.
  - The MQTT destination topic gains `cause`, `held` and `causeDetail`, plus a
    `<name> failure` discovery sensor.
  - The Stream Deck key shows the cause.

  After 3 quick key or codec refusals in a row, a destination is **held**: it
  stops retrying until somebody presses **Retry**. Network causes keep the
  normal backoff. For a destination linked to a connected YouTube or Twitch
  account, a refused key starts a read-only check of the account's current
  keys. If the stored key is not among them, the card offers "YouTube's key
  changed — click to update". The fixtures are real FFmpeg 8.1.3 stderr,
  captured in Docker against local ingest servers.
  [MONITORING.md](docs/MONITORING.md#why-a-destination-is-failing)

- **A remedy beside every failure, and a way to its log.**
  - A failed row in the Go live sheet has a **Retry** button. It starts a
    refused start again, and it restarts a failed or held process.
  - The destination card, Monitoring's process rows, the log summary and
    any stat tile in its warning colour each say what to do. Each offers the
    control that does it: edit the destination, the renditions page, the
    sources page, or that process's log.
  - Timeline rows about a destination or an ingest have a **Show log** link,
    and `/monitoring?process=<name>` opens the FFmpeg log filtered to that
    process.
  - The timeline stores each row's subject in a new `subject` column, which
    is added on upgrade. `GET /api/v1/timeline` rows gain `process`.

- **A destination's per-track gain and mute apply to the running stream.** A
  level change used to be an argv change, so it replaced the destination's
  FFmpeg process and dropped that platform's connection. Each mixed track now
  has a named `volume@trk<N>` stage, a destination's FFmpeg takes commands on
  stdin, and a level-only change is sent there instead. FFmpeg's reply must be
  a whole stderr line with `ret:0`. A refusal, a 2 s timeout, or a Windows host
  (where the channel is not verified) falls back to a restart, and the reason
  is reported. Mute sets the level to zero and keeps the track in the graph, so
  it is live in matrix mode as well. A matrix cell's gain, a track added or
  removed, and a change that flips the loudness normaliser still restart. The
  new `POST /api/v1/destinations/{id}/tracks/{track}/gain` answers
  `applied: "live"` or `applied: "restart"` with a `reason`, and the
  destination's status carries the same outcome as `levelChange`.
  [HOT-RELOAD.md](docs/HOT-RELOAD.md#live-per-track-gain-and-mute-the-stdin-command-channel)

- **A Stream Deck plugin, in `integrations/streamdeck`.** Destination state,
  Go live, hold-to-stop for every destination or one, the install-wide Record
  switch, routing presets, failover and a Mute track key on keys, and on a
  Stream Deck+ the audio meters and a Track gain dial (0.5 dB a step, push to
  mute, touch for 0 dB). Each mute or gain change says whether it applied live
  or by a restart, and why. It pairs with an API token: a read token shows state, and
  a key that would change something says it needs an admin token instead of
  failing. A stop key shows how many YouTube broadcasts the stop would end and
  refuses when that count cannot be loaded. The plugin is built from source;
  no packaged release is published yet.
  [STREAM-DECK.md](docs/STREAM-DECK.md)

- **A stream timeline on the Monitoring page.** Failovers, a destination going
  down, recovering, falling behind and catching up, and the ingest being lost
  and recovered are kept in the database, newest first, at most 1000 rows for
  30 days. They were raised as alerts and then lost unless a webhook kept them.
  `GET /api/v1/timeline` serves them. A stream ending is not on the timeline,
  because the backend raises no such event.

- **Routes for pairing and for a stop preview.** `GET /api/v1/auth/connection`
  says which token made the request, its scope, and whether it is admin, and
  Settings gains a Test connection panel that uses it.
  `GET /api/v1/destinations/stop-impact` counts the destinations a stop would
  touch and the YouTube broadcasts it would end for good, for a programme or,
  with `?destination=`, for one destination. The Stop all dialog now reads its
  count from it instead of counting the rows on screen.

- **An install-wide recording switch.** `GET` and `POST /api/v1/recording`
  read and set `recording.enabled` alone, and report `off`, `recording`,
  `not_running`, or `halted_disk` with the reason when the free-space floor has
  stopped the recorder. A change is audited like a settings save.

- **`GET /api/v1/levels` says whether its reading is current.** A `feed` field
  reads `none`, `live` or `stale`, so a meter can show that it has no feed
  rather than holding the last bar.

- **A Go live sheet in place of Start all.** It lists each destination as Ready
  or Blocked, with the YouTube count a later stop would end, and one confirm
  that names how many it will start. Starts go one at a time, two seconds
  apart, through the same path as a card's own Start, and a refusal shows on
  its row as it happens.

- **First-run and setup help in the app.** The dashboard shows a checklist
  derived from live state (source, encoder pointed at the ingest, signal,
  destination, go live) that reopens if a source or encoder is lost. A source
  card shows its ingest as a protocol and port and says Waiting for encoder or
  Signal detected. Where a platform has a device flow and developer
  credentials are stored, the add-destination form offers Connect with a code
  without leaving the form.

- **Routing and meters.** An Overview tab shows destinations against tracks,
  with an amber cell where a music track reaches a VOD mix. Matrix cells carry a
  state chip and a dB label. Clear in the mix matrix can be undone. A sticky
  bar names the destination while an edit is unsaved. Meters print their held
  peak, offer Auto zoom, draw the -14 reference, and show Not set with a Set
  target action where no loudness target exists. A track whose meter feed drops
  keeps its last measured peak and when it was taken.

- **Monitoring and status.** The dashboard has one health verdict line, held
  back while a backup, playlist or slate is on air. Failover is a full-width
  banner with the time on air. The FFmpeg log opens on its errors, with repeats
  collapsed and the raw output one click away. A destination's problem rows
  name the reason, the failing step and the as-of time.

- **Navigation and Settings.** The navigation is grouped into five labelled
  sections. Settings has a left section rail and a search box that matches
  section names.

- **On the site:** a home-page strip comparing self-hosting with Restream's
  hosted plans, dated to its evidence file; a GitHub star count and latest
  release read at build time, left out when the read fails; and a routing
  table for each use case, with sports and esports added.
- **Recurring events and planned rollover: the console (wave 3, #18 and #19).**
  The Automation page gets a broadcast template editor on each start schedule
  (title pattern with `{date}`, description, privacy, category, upcoming events
  to keep, thumbnail) and an upcoming-events list with skip and single-event
  retitle. Each destination card whose platform has a cap shows the cap with
  its source, a countdown to the cutoff, the rollover settings, and a "Roll over
  now" button that reports the server's answer as it came back. A platform's
  refusal of an upcoming-event create is kept on the template and shown there.
  Skipping a show that has already started, or whose destination is live, is
  refused with a 409, and reconciling an orphaned row for such a show removes
  only the local row. The optional chat notice before a rollover is off by
  default. The Twitch countdown starts from Helix `started_at` when it can be read.

### Changed

- **Website: every row of cards lines up.** Card rows on the home, features, comparison, download,
  multistream, how-to, free restream and five comparison pages, and the previous/next cards on
  every documentation page, now share one grid: card edges sit on the same lines as the headings,
  one 24px gutter everywhere, cards in a row are the same height, and the link or note at the foot
  of each card sits on the same line as its neighbours'. Sets of five fill their rows (three over
  two, or two, two and one full-width) instead of leaving a lone card beside a gap, wide tables
  reach the full width of the page, callouts match the paragraph they sit in, and the multistream,
  Streamlabs and Twitch VOD pages use the same card style as the rest of the site. A browser check
  in the site build measures this at four screen widths.

- **Website: quicker to read, quicker to try.** On the home page the routing problem and the
  playable patchbay now come straight after the spec strip, before the hosted-pricing table. The
  hero has one copy action for the install command, no pill above the headline, and no colour in
  it. The announcement bar is on the home page only, and says what the latest release is (or links
  straight to the install page). Sections no longer fade in on scroll, so a full-page capture,
  print or link preview shows the whole page. Screenshots on the features page show a low-resolution
  preview while they load instead of an empty frame, and the first one loads immediately. Headings
  that named limits now name choices ("Choose polyemesis if", "Choose another tool when"), eyebrows
  that repeated their heading are gone, the download page's ports are a list, and the spec strip
  has one value per cell. Standalone text links have a 24px hit area. Em dashes in the site's own
  copy became ordinary punctuation, and arrows mark a section's primary link only.

- **Broadcast palette, in the console and on the website.** Failed is a lighter coral, so it can
  no longer be mistaken for an on-air lamp, and it reads better: the failed badge goes from 4.55:1
  to 6.18:1 on a card, failed text on a raised card from 4.22:1 to 5.73:1, and the destructive
  button from 4.98:1 to 6.76:1. Placeholder and disabled text goes from 3.41:1 to 5.36:1. The focus
  ring is the brighter blue (4.49:1 to 7.13:1 on a card), and so is the primary button's hover,
  which composited to 3.84:1 at 85% opacity. Blue text is always the brighter blue. Idle is neutral
  grey instead of cyan; the Facebook chat accent, author colours, the moderator shield and caption
  speaker labels keep their cyan as an identity colour that never means a state. The audio meter has
  no amber stop: amber means a warning, everywhere. On the website, a lit crosspoint is blue, like
  the console's routing checkboxes, and the hero's decorative light sweep is gone. New ON AIR and REC
  tally tokens are in place for the next console change. For anyone styling against the tokens:
  `--live` is `--ok`, `--armed` is `--idle`, `--meter-high` is gone, and `--cat-cyan` is new. A test
  computes the contrast of the palette's text, fill, button and indicator pairs from the token
  values, and fails a change that drops one below WCAG AA.

- **Console quick wins from the 2026-11 design review.** Monitoring's sentence under the title now
  reads the whole page: it says live updates have dropped or the process list has stopped updating, that the relay is losing
  packets, or that host CPU or memory is over its threshold, before it says "All 12 running", and
  it turns amber when it does, even with no process running. The audio meter lights flat green, yellow and red zones (green below
  -18 dBFS, red from -2) instead of a colour ramp, and the bitrate chart is a straight green trace
  that breaks where samples are missing, with the programme's degraded-link failover floor drawn
  as a dashed line when that failover is on. A running process is plain text with a small green
  dot instead of a filled green pill; filled pills stay for warnings and failures, and on air is a solid red tally lamp everywhere it
  is shown: the header's live count (and its dot on a phone), the Dashboard's ON AIR bar, a destination
  card's badge and the Recordings page's running recorder. Form labels
  are sentence case instead of capitals. Every control draws the same 2px focus ring at a 2px
  offset (tab triggers and the dialog's close button drew none), buttons move down 1px when
  pressed, and text fields have a visible edge (3.66:1 against a card, from 1.24:1). Monitoring's
  info icons, switches and "Show log" links have 24px targets. Pages share one right edge whether
  or not they scroll, the programme switcher shows the programme's name instead of cutting it to
  ten characters (on a phone, the part before the dash, with the wordmark giving way to the logo, and the on-air chip beside it keeping only its dot and state words so the programme is named once; where "Off air" has no room the chip says "Off" rather than cutting the word), headings and help text wrap evenly, and Routing no longer scrolls sideways on a
  phone. Routing's mute-and-gain rows stack below about 42rem (name, role and channels; the meter
  with its own L/R and dB readouts; then gain and Mute) instead of drawing the readouts over the
  slider, and a long role wraps rather than being cut. The Sources card header, its publish rows
  and the Renditions card header and buttons stack on a phone as well. The Dashboard's compact chat Send button has a name, and the Sources ingest fields are
  tied to their captions for screen readers. The sign-in screen names the server it is signing in
  to and whether that server answers (degraded only on the health check's own 503), from the
  browser's own address and the public health check only; it shows no build or version, which
  stay behind sign-in.

- **YouTube captions by HTTP POST: the delay is applied once, by YouTube (#8).** A YouTube
  destination on *caption ingestion (HTTP POST)* still needs a 30 s or 60 s delay, and it is
  still sent as the broadcast's `broadcastStreamDelayMs`. polyemesis no longer holds that
  destination's output back as well, so viewers are about 30 or 60 seconds behind instead of
  about 60 or 120. Each caption is posted as soon as it is recognised, stamped with the moment
  its words were spoken, which is when the matching video reached YouTube (PROVISIONAL: YouTube
  does not document the stamp's clock). Changing such a destination's delay between 30 s and 60 s
  no longer restarts its output; switching a delayed YouTube destination to or from this route
  does, because the delay moves, and the editor says so. The card still shows the delay. Stop
  airs no delayed tail from polyemesis on this route. In all 15 languages.
- **BREAKING for native installs: polyemesis requires FFmpeg 8.0 or later.**
  It was 6.0. polyemesis now refuses to start on an older FFmpeg, and the
  refusal names the binary, the version it found and the fix. Enhanced RTMP
  multitrack and two-track FLV need 8.x. CI now tests exactly the latest 8.x
  (8.1.3) and the latest 9.x (9.0.2).
  - **Docker users:** nothing changes. The image bundles FFmpeg 8.1.
  - **Affected:** a binary-mode install that uses a distribution FFmpeg older
    than 8, such as Ubuntu 24.04's apt 6.1.1 or Debian 13's 7.1.5.
  - **Fix:** do any one of these:
    - re-run `scripts/install.sh --ffmpeg force`, which installs a pinned,
      sha256-checked static FFmpeg 8.1 to `/usr/local/bin` and pins it in the
      config;
    - install FFmpeg 8 or later yourself (Ubuntu 26.04 ships 8.0; Homebrew
      ships 9.x);
    - switch to the Docker image.
  - **The CUDA image** now carries the same pinned static FFmpeg 8.1.3 instead
    of Ubuntu 24.04's 6.1.1, so NVENC and Enhanced RTMP multitrack ingest work
    together. Its NVENC needs a host NVIDIA driver of **570.0 or newer** (NVENC
    SDK 13.0); the old image's 6.1.1 needed 520–550, depending on SDK. See
    docs/HARDWARE.md.
  - **The installer** no longer has a separate "older than recommended" answer
    for 6.x and 7.x hosts. A host's FFmpeg either clears the 8.0 floor or gets
    the install offer.
  - **`POLYEMESIS_FFMPEG_ASSUME_MAJOR`** still refuses any value below the
    floor, which is now 8.
  - See [docs/UPGRADING.md](docs/UPGRADING.md).
- **Routing has one save model (app-council A26, A27).** Every edit, mute and
  gain included, now waits for Save changes in a single bottom bar with
  Discard; mute and gain no longer apply the moment they move, and the header
  Save is gone. Loudness, A/V delay, ducking, the VOD mix and the compiled
  result sit in a collapsed Advanced section, with the FFmpeg filter graph
  behind "Show FFmpeg graph". The destination PUT is unchanged.
- **The Jobs Policy tab leads with four decisions (app-council A63).** Default
  mode, jobs at once and the CPU ceiling and resume levels come first, under a
  sentence that reads them back. The twelve tuning settings sit behind an
  Advanced section that opens by itself whenever one differs from its default
  or is out of range. Revert and Save policy stay on screen with a save status.
- **On phones the Dashboard shows destinations first (app-council A87).** Below
  the desktop breakpoint the destinations and Go live come straight after the
  on-air strip; Chat and Pipeline, the failover note and Stream info are
  collapsed sections below them. The desktop layout is unchanged.
- **Monitoring lists processes in a compact table (app-council A72).** Process,
  State, Restarts, Uptime and Errors, with failed and reconnecting processes
  first; a row expands to the cause, remedy, PID, log and full command.
- **One status vocabulary across the console (app-council A91).** "Not
  running" had four spellings side by side: the on-air counts said "stopped",
  the cards those destinations were said OFF, the ingest chip said "Offline"
  and the Pipeline card said "idle". There are now two words: **Off** for
  something you switched off, and **Stopped** for something that is on and not
  running. The on-air counts read "live · stopped · off", matching the cards.
  Presentation only: no API, MQTT, webhook, SSE or Stream Deck value changed.
- **Long explanations are one line, with the rest under "More about this"
  (app-council A84).** The mux-queue, multitrack and give-up notes in the
  destination dialog, the slate encoder, silence and GPU notes in Settings, and
  the expert-arguments note on Monitoring now show one sentence; the full
  paragraph is one click away.
- **Settings saves say "Saved" beside the button (app-council A95).** Ingest,
  listener ports, the Pipeline tab and platform credentials show the same save
  status as Playout, Clips and Recordings instead of a success toast; a refused
  save says "Not saved" there and still toasts the reason. Recordings' switches
  are confirmed by the card's one status line.
- **Settings Overview: "Ready to stream" agrees with the checklist.** It is shown
  only once every step before Go live is done, so it no longer sits above
  "2 of 5 done".
- **Empty states carry their next step.** A programme lane with no
  destinations offers Add destination on that programme; Chat with no account
  connected offers Open platform credentials. An empty schedule name says why
  Save is greyed.

- **Settings is reorganised around tasks (settings council).** A new
  **Overview** is the landing page on an install with no source: it shows
  whether you are ready to stream, the same five-step setup checklist as the
  Dashboard, and one tile per area (ingest, platforms, overlays, OBS, pipeline,
  security), each saying "could not read" rather than "0" when a read fails.
  The side menu is grouped into Stream in, Stream out, Processing, Access and
  security, and Danger zone; every old `?tab=` link still opens the same panel.
  One **Show advanced settings** switch hides expert cards (MQTT, preview,
  audio meters, process logs, Enhanced Broadcasting hardware) until you want
  them, but never hides a card whose value differs from the default, and a
  search result opens the card it lands on. Pipeline is split into Routine and
  Once per install. **Platforms** is one row per platform, connected first,
  with one status and one button; the credentials, redirect URI, accounts and
  setup guide open under the row, and the developer-app pair is an
  "App credentials (advanced)" layer. The last credential check is stored and
  shown after a reload. **Security** leads with the data directory and
  secret.key backup. Ingest cards say whether they apply to the whole install
  or to one programme, degraded-link thresholds are edited on Sources only, and
  "Synthesise a silent track" now reads "Add silence when the source has no
  audio".
- **Schedules: "All destinations" is an explicit choice.** A start or stop
  schedule now names its destinations or ticks **All destinations**
  (`allDestinations` in the API). It can no longer mean every destination by
  leaving the list empty, which for a stop meant "stop the whole broadcast" by
  accident. The editor will not save a schedule with nothing chosen, and its hint
  says what the action does. Existing schedules with an empty list were set to
  All destinations on upgrade and behave as before. API clients that do not send
  the field keep working, except a new stop with no destinations, which is
  refused with a message naming `allDestinations`. See
  [docs/SCHEDULED-BROADCAST.md](docs/SCHEDULED-BROADCAST.md).
- **Automation: plainer labels and explicit "All events".** The tab that holds
  script hooks is now called **Hooks** (the `?tab=hooks` link is unchanged), and
  the Alerts card is **Alert rules**. An alert rule has an **All events** switch,
  on by default; turn it off to pick events, and the dialog will not save with
  none picked. Schedule badges, alert summaries and the severity list show
  translated labels instead of raw values such as `playlist.start` or `warning`.
  Jargon is gone: *Combine repeat alerts for*, *Run if late by up to*, *Recent
  runs*, and *Merged / Waiting / Discarded / Retried* with a one-line
  explanation. Every select in the rule and schedule dialogs is named by its
  label. Empty alert, schedule, hook and run lists use the shared empty state,
  with the button that fills them.
- **Chat: one empty state.** The timeline's empty pane uses the shared empty
  state with a short title (for example "No messages yet" or "Messages are
  filtered out"). The counters read *Duplicates skipped* and *Saved to history*.
- **OBS chat dock says what went wrong.** A dock URL with no key, a key the
  server refused, and a server that cannot be reached now each have their own
  message, and the unreachable case retries by itself. While it loads, the dock
  shows "Connecting to polyemesis…" instead of a blank panel. The on-stream
  overlay still draws nothing in every one of these cases.
- **Console polish: scoped counts, one empty state, readable warnings.** The
  header's live badge counts the selected programme and names it ("Main: 3
  live"), or says "All programmes: 5 live" when none is selected; the on-air
  strip's healthy line says "All 5 running destinations healthy". Programme
  lanes on the Dashboard read "9 destinations · 5 live" with an on-air glyph
  that is not colour alone. The no-failover note is one line with a link to
  Settings → Failover, its explanation behind "Why this matters". Every empty
  list on the Dashboard, Jobs, Monitoring, Sources, Routing, Renditions and
  Meters uses one empty state, with the next step as a button where there is
  one. Warnings and hints on these pages are set at 12px instead of 10px.
  Routing shows "Unsaved changes", "Saved" or a failure that keeps your edit
  beside Save, and a read-only mix-matrix row says why in visible text.
  Monitoring's memory line and ingest chart are translated.

- **Dashboard: clearer on-air answer and one filled start action.** The on-air
  strip counts live, stopped and failing destinations against the whole install
  ("2 live · 3 stopped · 0 failing"), so a switched-off card is no longer read as
  a fault. It says "Ingest is not receiving" instead of "All healthy" when every
  destination is up over a feed that is not arriving. Its End all is now Stop all
  and opens the same confirmation as the toolbar. The stop-all dialog is shorter
  and no longer points at a Start all button that does not exist; it says to start
  again with Go live. Go live is the one filled start action: Add destination and
  each stopped card's Start are outline buttons. The metadata card is titled
  Stream info. The Go live sheet marks a running destination LIVE, not READY, and
  wraps long source names instead of cutting them off. A "0 accounts" figure no
  longer sits beside the Go live card when no platform account is connected.
  The strip's full-screen view is named Full-screen status.
- **Shell: keyboard and phone fixes.** A Skip to content link comes first on
  every page. The closed mobile nav drawer is removed from the tab order. On a
  phone the header now shows the on-air answer beside the menu (the live count, or
  Off air). System moves into the Operations group beside Monitoring, and the
  settings group is headed Setup.
- **Chat composer.** When no platform can send, the field reads "Chat not
  connected" and the reason sits beneath it instead of being clipped.


- **Jobs, Monitoring and sign-in say what they mean.** Jobs says once, in place,
  when the server cannot be reached, instead of a red toast every four seconds.
  Cancelling a running transcode asks first; a queued job still cancels on the
  click. The CPU gate says "CPU over ceiling" in words, and Pause all appears only
  when there is something to pause. Monitoring opens with one status sentence
  under the title, puts the FFmpeg log full width, keeps raw FFmpeg arguments
  behind a button, and shows a dash rather than 0% before the first sample. On
  the first-run and sign-in screen, a wrong setup code or a dropped connection
  keeps what you typed, validation shows inline, and the setup-code help is
  readable at 12px and up. Monitoring's CPU warning follows the Jobs CPU ceiling
  you saved, and its status sentence counts processes that are not running. The
  System page opens with the version, the ingest state and the host load, and
  the debug recorder sits under Support tools.

- **One status vocabulary, ready for the pages.** `lib/status.ts` maps every
  process, destination, delay, rendition, recorder, clip-buffer, ingest and
  output state to one translated word, a tone and a non-colour indicator. The
  words are presentation only: the API, MQTT, webhook and Stream Deck values are
  unchanged. Pages adopt it as they are touched.
- **Badges, table headers and select labels are 12px.** Status badges, table
  column headers and dropdown and select group labels moved from the 10px micro
  step to 12px.
- **Confirmation dialog: consequence line and caution tone.** `ConfirmDestructive`
  accepts a one-sentence `consequence` and `tone="caution"` for reversible live
  actions. The default look is unchanged, so existing callers do not change.

- **Each platform card names its credentials the way that platform's console
  does.** Settings → Platforms used to label every card "Client ID" and "Client
  secret". The Facebook card now says **App ID** and **App Secret**, which is
  what Meta's App settings → Basic page shows. Vimeo says **Client identifier**,
  and Twitch and Kick say **Client Secret**. Under each field, a short hint says
  where to find it in that platform's console. The values you paste and the API
  fields they are stored in have not changed.
- **The site banner and comparison blurbs lead with what polyemesis does.** The
  bar on every page now reads "Free and open source (MIT). One stream in, every
  platform out, each with its own mix." and links to /features. The comparison
  card on the home page, the docs index and `docs/COMPARISON.md` describe the
  page as how polyemesis compares with the other tools. The comparison page's
  content is unchanged.
- **Twitch sign-in now uses PKCE.** Connecting a Twitch account sends an S256
  `code_challenge`, and the token exchange sends the matching `code_verifier`
  along with the client secret. A leaked authorization code is no use to anyone
  but the server that asked for it. Twitch's discovery document began
  advertising `code_challenge_methods_supported: ["S256"]` on 2026-10-19. The
  weekly `oauth (live platforms)` check caught the change and has failed on
  `main` since. That check now asserts S256 the way it does for Google: Twitch
  advertises it, `Twitch.PKCE()` is on, and the consent URL carries it. It will
  fail if Twitch withdraws S256. Accounts that were already connected are not
  affected: refresh sends no verifier. A sign-in started before the upgrade and
  finished after it still exchanges without a verifier.

- **Deploying this release restarts every running destination once.** The
  destination argv changed: each mixed track gained its named `volume@` stage,
  and `-nostdin` is gone from destination processes everywhere except Windows.
  Every destination's restart signature therefore moves on the first reconcile
  after the upgrade, and each one restarts once. Plan the upgrade for a time
  when no stream is live.

- **Stdin is a command channel for destination processes.** Only the
  supervisor's serialised writer writes to it, and only validated
  `volume@<name> -1 volume <level>` lines. Every other child keeps `-nostdin`.

- **Text contrast and size on configuration surfaces.** Enabled text no longer
  uses the subtle foreground (about 3.1:1 on a card); it uses the muted one
  (about 5.8:1). Labels and values on six configuration surfaces move from
  9–11 px to the 12 px step.

- **UX Council gaps, in the app.** The dashboard's setup checklist lists
  every publish address with its own copy button and a Signal / No signal
  badge (A1). A source that starts receiving shows a live thumbnail and an Add
  destination button (A2). Linked platform accounts carry a `health` field
  from the last token refresh (`ok`, `expiring`, `refreshFailing`, `expired`)
  in `GET /api/v1/platforms/accounts`, and the destination dialog shows it on
  an account chip (A5). The destination dialog is four steps over one form
  (Platform, Connect or key, Audio, Video and advanced) and leads with Connect
  with a code on a self-signed server (A6). The routing save bar counts the
  pending changes and lists what moves on air before Save (A7). The on-air
  strip shows the time on air and an End all button (A8). Presets open a
  review before they apply, the matrix has an undo history, and three starter
  presets stay in view (UX Council A14). Debug recording moves to a System page, and
  Settings ends with a Danger zone (A17). Delete dialogs count what still uses
  the thing being deleted (A18). Routing, Renditions, playout, meters, chat,
  clips, library, Jobs, Automation, Monitoring, Settings and the destination
  dialog go through the catalogue in all fifteen locales (A19). Twelve-pixel
  text and the muted foreground reach the remaining configuration surfaces
  (B2), and Polish, Turkish, Russian and Ukrainian text draws in the app's own
  faces (B3).

- **UX Council gaps, on the site.** The features page's screenshots are
  recaptured from a seeded install with a release version in the header,
  including the connected platforms list, the dashboard and the full
  Monitoring page (S5). The home page uses one track layout throughout and
  leaves the Twitch GPU detail to the Twitch page (S7).

- **The site's hero leads with the outcome and a copy-first install action.**
  The install command and a Copy button come first and the quickstart becomes a
  text link, which reverses the earlier docs-first order. The CSS drawing is
  replaced by a crop of the real routing screen.

- **Inputs pages say what they are doing.** Renditions reads "Not encoding,
  starts when the first feed goes live" rather than "Starting" when no encode
  has been asked for, and each feed chip names its state in words. Sources
  shows the SRT host and port in clear with a Copy button; the passphrase and
  stream ID stay masked behind Reveal. The degraded-link thresholds fold behind
  a summary of the numbers, and the source switch says Accepting or Off, with
  what that means for an encoder. Add destination on a source opens the
  destination dialog already on that source. Meters put the per-track levels
  above loudness compliance, state dBFS above the readouts, and announce a clip
  in words. Routing shows why Save is greyed beside the button, and destination
  names and routing summaries wrap rather than truncate.

- **Clips, Recordings and Playout read more plainly.** A replay buffer that is
  enabled but not running reads Waiting, not Ready. With the buffer off, Clips
  shows one empty state with a Turn on rolling buffer button, and the capture
  presets appear only while it runs. The buffer window is an editable draft with
  an explicit Apply, so the three-second refresh no longer overwrites an edit.
  Recordings shows recorder status as a status pill, marks a live segment
  Recording now, leaves a blank retention box empty while Save stays off, and
  says 0 means no limit. Switching recording off while the stream is live asks
  first. In Playout, turning Public on or Protection open asks first; turning
  them off saves at once.
- **The Clips buffer stays on after a restart (app-council A14).** The rolling
  buffer's switch and window are now saved per programme, so a programme whose
  buffer was on comes back with it on, at the same window, after an upgrade,
  reboot or crash. A programme that never turned it on, including every one on
  an upgraded install, still starts with it off. The Clips page no longer says
  the switch is off after a restart; it says the switch is saved. A failure to
  save the switch is reported as a server error and leaves the buffer as it
  was. `GET /clips` now carries the configured `windowSeconds` even while the
  buffer is off.
- **Playout reads as a control room (app-council A16-A20).** A status line under
  the title says whether playout is on, who can watch and how many are
  watching; when it is off, the line carries a Turn on playout button, and the
  share links are greyed with the reason beside them. Share and Who can watch
  sit side by side; the quality levels (formerly the ladder) and packaging are
  in a closed Advanced section. Packaging fields no longer save, and restart
  the stream, each time one loses focus: the changes are held until Apply and
  restart stream, the bar counts them and shows each field's previous value,
  and it asks first, naming the viewers, when anyone is watching. Quality level
  names and audio tracks apply with their own Apply button. The player page
  title and description have a Save button and say Unsaved changes, Saving,
  Saved or Not saved. Labels are plainer: Quality levels, Original (no
  re-encode), Rewind window, Audio track (number, from 0). The settings, API
  and saved values are unchanged.
- **Library, Recordings and the trim editor say what they are for (app-council
  A37-A41).** Library's media upload card opens from an Upload media button
  instead of sitting above the archive. When transcription is off, the header
  shows "Transcripts: off" with an Install whisper.cpp link instead of a
  paragraph ending in a config.yaml key; search stays on for the transcripts
  you already have, and each greyed captions button names that status as its
  reason. Regroup is now Rebuild sessions, with a one-line explanation under
  the title. The play indicator and Generate proxy no longer share the film
  icon, and a waiting job's reason is printed on its row. A track's speaker
  name saves with Apply rather than on leaving the field. The clip editor is
  titled Trim recording. Recordings' retention policy says Unsaved changes,
  Saving and Saved beside its Save button, and Library, Recordings and Clips
  use the same empty state.
- **Chat overlays say whether OBS is connected (app-council A98).** An
  overlay draws nothing when it fails, so a broken browser source looked the
  same as an idle one. Settings now shows, for the selected overlay, how many
  sources have it open, or when one last did, with a Check again button.
  `GET /overlays` carries `connected` and `lastSeen` for each overlay.
- **Chat: keyboard, attention and polls.** Enter that confirms an IME
  composition no longer sends the message. Message rows are reachable from the
  keyboard, and Enter, the context-menu key or Shift+F10 opens their menu. New
  messages are announced once instead of on every line. The attention banner
  names each platform that needs work, with its reason, and the poll form sits
  behind a disclosure. The Twitch mirror option appears only when Twitch is
  connected. Hide all on a user card works in batches of five and reports a
  partial failure as "Hid X of Y lines; N failed". The ban action in the message
  menu is now Open card to ban.

### Removed

- **Trovo.** Trovo shut down on 30 June 2026, so it is gone from Add
  destination, the platform presets, the capability matrix and the sign-in
  options, and the Trovo OAuth provider has been removed. Creating a Trovo
  destination through the API answers 400, and starting one answers 409, each
  saying "Trovo shut down on 30 June 2026". A Trovo destination saved before
  the shutdown is kept: on the first start after upgrading it is switched off,
  its card says why, and its settings stay readable until you delete it. Stored
  Trovo account tokens, the Trovo app credentials and any pending Trovo sign-in
  are deleted from the database at the same time.

### Fixed

- **Wave 3 review fixes: captions, rollover, X and the recurring look-ahead.**
  Two captioned programmes no longer share live-caption scratch files: each
  captioner run writes its audio windows in a directory of its own, so one
  programme can no longer air another's speech as captions. A planned YouTube
  rollover of a broadcast taking captions by HTTP POST now creates the replacement
  with them, the monitor stream and the old delay, and takes it through testing
  before live; before, the replacement went live with captions off. In the SEI
  route, a picture that goes out without a caption (a PCR inside it, for example)
  no longer spends the caption pair it was due, so scripts are no longer garbled
  or left on screen; a program map that spans packets is read; a caption that
  arrives at a clock jump is kept; a short backward jump restarts captions within
  two seconds; and an HEVC source is named in the log. The cue log reopens after
  a failed roll, the YouTube caption `seq` no longer restarts at 1 after a
  restart, and the player's caption track fetches only what was appended.
  X: rate limits are read from X's headers and every call waits for the reset
  after a 429, without counting toward the give-up budget; the account id is
  asked for once per token; X's `{error_type, message}` refusals are shown in
  X's words; an empty answer to a publish, an end or a read is a failure; token
  requests use HTTP Basic for a confidential app. A broadcast ended in X while the
  encoder is still sending is not replaced until the encoder stops, a canceled
  one is a fault released by switching the destination off and on, and a
  switched-off X destination is no longer read every 15 seconds. Publishing an X
  broadcast posts X's default announcement, which is now documented. Recurring:
  a refused event create backs off (five minutes, doubling to six hours, and six
  hours at once for YouTube's upcoming-broadcast cap) instead of retrying every
  five minutes; switching a schedule off, removing a target or lowering the
  look-ahead cancels the events nothing will air; the go-live mirror never moves
  to a later week's event; and a second rollover still uses the template. The
  template editor no longer shows an editable form of defaults when the stored
  template could not be read.

- **An X go-live keeps every warning X sends.** When X published a broadcast but reported errors
  and also failed to post the announcement, only the announcement warning reached the log; X's
  own report was dropped. Both are kept now. (X stays gated off until the app is whitelisted.)

- **An X destination saved from the editor is an X destination again.** The editor's X preset
  carried no platform, so a destination made from it was saved as a custom RTMP endpoint, and
  nothing X-specific (sign-in, the Livestream lifecycle, chat) reached it. It now saves as X, like
  the server's own preset.
- **X go-live sent no title.** The broadcast coordinator published X broadcasts through the generic
  transition, which always sent an empty publish. It now sends the go-live title.

- **The caption picker no longer says a route changes nothing on air (#8).** It says captions go
  out only for a route that is on, that a live destination must be stopped to change its route,
  and, only for a change that adds or removes the in-stream (SEI) route, that the save restarts
  an enabled destination. A change between off and YouTube's HTTP POST restarts nothing when the
  destination has no delay; with a delay it moves the delay between polyemesis and YouTube, and
  the picker says the save restarts the output. In all 15 languages.
- **A tier that burns captions in now starts the captioner on its own (#8).** Burn-in is how a
  platform with no caption route gets captions, so a programme whose only caption demand is a
  burned-in tier used by an enabled destination now runs the captioner. A tier that starts while
  no captioner runs starts from an empty burn file, so a caption left by a crash is never drawn.

- **A metadata push or reschedule no longer switches a YouTube broadcast's captions off.**
  `liveBroadcasts.update` resets every field of a part it is sent without, and the update
  sent `contentDetails` without `closedCaptionsType` and `latencyPreference`. Both are now
  read back and carried through unchanged.

- **A scheduled YouTube broadcast that fails to create or bind now names the ingest stream it
  made.** With a stream of its own and no held key, the stream was left on the channel
  with no mention. The error now gives its id, so the stream can be removed in YouTube Studio
  once nothing streams to it. polyemesis never deletes a YouTube ingest stream itself, because
  a concurrent request may already be using it.

- **Wave 3 review fixes (API).**
  - An X destination whose broadcast create is refused now stops retrying after the usual 20 attempts. It tries again after an UP edge, or after the operator switches the destination off and on.
  - A refusal that echoes the stream source key no longer puts that key in the fault, the log or the alert.
  - A planned YouTube rollover whose replacement would not start after the old broadcast had ended now points the destination at the replacement, so the show is not left dark.
  - Facebook and other countdown-only destinations now show their countdown.
  - The rollover chat notice goes only to the destination's own account's chat.
  - A Twitch reconnect re-reads the stream's start time and no longer restarts the destination every ten minutes when Twitch keeps its count.
  - A replacement broadcast gets the recurring template's category and thumbnail.
  - Rollover warnings are redacted.
  - A recurring template never makes a private or unlisted destination's events public.
  - Recurring look-ahead logs and the calendar's 502 answers are redacted.
  - A second event created in the same look-ahead pass reuses the first one's stream key.
  - An event whose record cannot be written is stranded or cancelled, instead of becoming a duplicate later.
  - Retitling an occurrence is refused for a show that has started or a destination that is live.
  - A thumbnail one byte over 2 MB answers 413, not 500.
  - Creating a YouTube vertical half now starts it straight away.
  - A vertical half reuses an existing rendition only when the whole encode matches, not just the shape.
  - Concurrent vertical status reads make one YouTube call.
  - The vertical create body is size-limited and refuses unknown fields.
  - The caption export route is documented in [API.md](docs/API.md) as refused to read tokens.
- **Live captions: the captioner restarts when it stops on its own, within a budget.**
  A captioner whose audio tap or speech recognition stopped by itself used to stay
  "enabled" but dead until a setting changed. It now reports itself stopped at
  once and is rebuilt. If it stops within 30 seconds of starting three times in a
  row, captions are switched off with a warning rather than respawning a new
  FFmpeg and whisper on every reconcile. Switching captions off now publishes a
  status too, so the dashboard no longer shows a captioner that is gone.
- **Live captions: an automatic track stays automatic, and every setting applies.**
  Starting the captioner no longer replaces the stored configuration with its
  resolved track and model, so a later source change picks the new layout's
  track. The restart check now covers the whole resolved configuration (threads,
  health limits, denoise, speaker), so changing one restarts the captioner, and
  the reconcile right after a start no longer restarts it for nothing.
- **Live captions: overlapping cues.** A new caption no longer gets erased by the
  previous caption's end time or overwritten by its resends. A cue that ends
  before it starts is erased right after it is shown, and one cue schedules at
  most 32 resends. After a timestamp jump, captions registered against the old
  timeline are dropped rather than sent in one burst.
- **Live captions: in-stream injection follows PMT changes safely.** Program
  tables are read only from packets that start a section and carry a payload. A
  PMT that moves video to a new PID or to HEVC releases the pictures still held,
  unchanged, on the PID they arrived on, and stops rewriting the old PID.
- The caption profanity filter now masks a word in single quotes ('damn').
- The burn-in subtitles filter (not wired yet) escapes its path at both FFmpeg
  levels, the filter option and the filtergraph, so paths with quotes, colons or
  commas open correctly.
- **Console fixes for YouTube vertical, rollover and the recurring calendar.**
  A vertical half that is switched off can now be started from its sub-row. Start
  switches it on, and the sub-row is its only Start button. Before, a pair created
  while the horizontal half was off could never be started. The YouTube vertical
  section treats only a 404 as "no vertical half yet". Any other failed read shows
  the error with a Retry button, and no longer offers an Add that the server
  refuses. On the planned-rollover panel, "Roll over now" appears only on a
  platform that rolls over automatically, and a failed read shows the error
  instead of an empty card. A refused or platform-rejected title edit in the
  recurring calendar now says the title was not changed. Before, it reused the
  skip messages ("Not removed", "refused to delete the event").
- **X live chat shows only this account's own broadcast.** X's activity stream is shared by the whole app, so it also carries chat from every other connected X account. Before, every chat event on it was labelled as this account's, so another account's viewers could appear in your chat and be moderated against your broadcast. Now a message passes only when its `broadcast_id` is the broadcast this account is running, and an event that names no broadcast is dropped. The running broadcast is re-read at most every 15 seconds.
- **X broadcast errors no longer carry the stream key.** X's create call sends the stream key as `source_id`, and X's error text can echo it back. Polyemesis now removes the key from that text, on a refused create and on a successful answer that carries errors, before the error reaches a fault, a log line or the timeline.
- **X go-live and end report X's errors.** A publish or end that X answers with errors and no state is now a failure, instead of a success with an empty state. The end also reads the state from the broadcast object. A broadcast's stream is now found by the source's id as well as its key. PROVISIONAL: X is still switched off until the app is whitelisted.
- **X live chat shows your own new broadcast from its first message.** Another account's chat used to start a 15-second wait, so the opening of your show could be dropped. A broadcast seen for the first time is now checked straight away. One that has already been judged as someone else's is checked again only every 15 seconds. Live chat also stops with a clear reason if X creates a chat subscription without returning its id, because that subscription could never be removed.
- **YouTube cleans up after a failed scheduled broadcast.** When a destination with its own ingest has a new stream made for a scheduled show, and the broadcast create or bind then fails, that new stream is deleted. A stream found on the channel is never deleted. A vertical stream that YouTube returns without an id is now refused.
- **A YouTube rollover never deletes a replacement that may be live.** When the start fails with an error that is not a refusal, polyemesis reads the replacement's state again before removing it. The replacement is also scheduled two minutes ahead, because YouTube wants a start time in the future. PROVISIONAL.
- **X live chat shows only this account's own broadcast.** X's activity stream is shared by the whole app, so it also carries chat from every other connected X account. Before, every chat event on it was labelled as this account's, so another account's viewers could appear in your chat and be moderated against your broadcast. Now a message passes only when its `broadcast_id` is the broadcast this account is running, and an event that names no broadcast is dropped.
- A manual planned rollover now answers 200 "rolled over" when it has finished,
  502 with the redacted reason when the platform refuses it, and never 202
  "rolling over" for a rollover that had already run. The sweep path is unchanged.
- A manual planned rollover is no longer cancelled when the browser disconnects
  partway through, so it cannot leave a new public broadcast unbound. It is
  bounded to five minutes.
- Platform refusal text from a planned rollover is redacted before it reaches the
  timeline, the result row, the webhook and the response.
- Two concurrent YouTube vertical pairings for one destination no longer both
  create a vertical stream on YouTube. The second answers 409.
- A YouTube vertical destination is repaired the same way as every other
  destination write before it is stored, so a setting the platform cannot send
  is dropped rather than kept.
- X: while the Livestream switch is off, the connect-time entitlement check makes
  no request to X and reports the gate.

- **Phone Dashboard cards no longer run off the screen.** At 390px with a
  source, the Ingest, Chat and Pipeline cards were 546px wide in a 356px
  column; the top-row grid now has a shrinkable single column below the
  desktop breakpoint. The header's on-air chip stays on one line.
- **Settings Overview tiles no longer show a stale "Reading…" line** after two
  reads in one tile settle.
- **A slow credential check can no longer mark a newer pair as verified.** The
  stored verdict is keyed to the client ID and secret that were checked; a
  verdict for a pair replaced while it was checked is dropped.
- **Clips: the Window stat shows "—", not "—s",** when the buffer has nothing
  measured.

- **A failed refresh no longer hides the timeline.** The incidents already on
  screen stay, with a line saying how old the list is. A failed destination list
  in Expert mode is said, with a retry. Numeric policy fields no longer snap to
  the minimum while you type.


- **A destination's restart count now includes restarts caused by live level
  changes.** Two restarts were never counted. One is the restart that keeps a
  live mute or gain in place when a failover or a new ingest session changes
  the audio format. The other is the restart that replaces a level change the
  running FFmpeg refused, which is every level change on Windows. The card,
  `polyemesis_destination_restarts_total`, the `destination.restarted` event
  and the Stream Deck restart-storm key now count each of these once. The
  backup output's own count, shown on the card, counts them too. A level
  applied in place is still not a restart.
- **The OAuth acceptance suite no longer creates a Facebook live video.**
  `scripts/acceptance-oauth.sh` said its credentialed step "starts no
  broadcast", but the step called Facebook's ingest lookup, and that lookup
  creates a `LIVE_NOW` live video. Run with real credentials, it would have left
  one on the account. A YouTube channel with no stream would also have got a
  new stream inserted. The default run now creates nothing on any platform.
  Twitch and Kick read their ingest. YouTube lists its stream keys. Facebook is
  skipped. `POLY_OAUTH_ALLOW_BROADCAST=1` (the driver's `-allow-broadcast`) opts
  in to creating one public Facebook live video. The run ends that video, and
  reports its id, before it exits. The suite also says now that a Kick run
  rotates the refresh token. The live step moved to `scripts/internal/oauthlive`,
  where a test fails if the default path calls a method that creates anything.

- **The source card's "retrans" count was always 0.** It counted
  retransmissions the listener had sent, which a receive-only listener never
  does. It now counts the retransmitted packets received.

- **Automod no longer pays the model for a message a rule already removed.**
  While the model checker was on, every non-empty message was sent to it,
  including ones the free checkers had just deleted. Those answers could change
  nothing. Messages the free checkers only flag are still sent.
- **`install.sh --ffmpeg force` downloads again.** It pinned a BtbN daily
  build, and BtbN deletes dailies after about two weeks, so every forced
  FFmpeg install failed with a 404. It now pins the month-end release
  `autobuild-2026-09-30-13-08` (FFmpeg 8.1.3), the kind BtbN keeps for about
  two years, and a test refuses any pin that is not a month-end build.
- **A stale FFmpeg pin in the Docker image now says what to change.** When
  Alpine moves `ffmpeg` to a new version, the image build used to stop at apk's
  "unable to select packages". It now prints the version the branch serves and
  the `FFMPEG_VERSION` line to change.
- **Audio no longer falls behind the video after a format-changing failover.**
  When a failover switched to a source with a different sample rate or channel
  count, a destination that rode the switch could put silence in front of its
  audio. The silence was as long as the stream had been running, so the audio
  stayed that far behind the video until the destination restarted. Measured:
  96 s behind after a failover 98 s in. Switching onto a backup encoder at
  another rate does this. So does switching onto the slate (always 48 kHz
  stereo) under a 44.1 kHz or mono primary. Destinations that mix one track
  now stay in sync and keep their connection. A destination that mixes several
  tracks still gets the silence at such a switch. See
  [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md).
- **Multi-track destinations stay in sync through a format-changing failover
  too.** A destination that mixes several tracks no longer falls behind its
  video when the slate or a backup encoder sends audio at another sample rate
  or channel count. The slate is now built at the primary's measured sample
  rate and channel count, not always 48 kHz stereo. A backup or playlist feed
  now has its audio re-encoded to the primary's format, track by track, as it
  goes on air; its video is still copied. The destination then decodes one
  audio format across the switch and never rebuilds its mix. Measured on a
  two-track mix: 39.9 s of audio hidden behind its timestamps before, none
  after. A feed starts this conversion only once the primary has been
  measured. The primary itself is still copied untouched. See
  [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md).

- **Show log works on timeline rows about another programme.** Every
  programme's ingest is named `ingest`, so these rows had no link. The link now
  names the row's programme, and Monitoring switches to that programme before
  it opens the log. `/monitoring?programme=<id>` does the same from anywhere.
- **Connecting an MCP client no longer writes a warning to the log.** Every
  client sends `GET /mcp` as it connects, and the stateless server answers it
  with the `405` the spec calls for. That answer is now logged at debug. Every
  other `405`, and any other refusal on `/mcp`, is still a warning.
- **An SRT programme's healthy destinations no longer read as stalled after a
  show.** An SRT source has no ingest process, because the shared SRT listener
  feeds the programme directly, and the server took "no ingest process" to
  mean "no ingest". So when the encoder disconnected, every running
  destination was blamed for its own stopped output:
  `polyemesis_destination_up` 0, `_stalled` 1 and a stall warning on its card,
  and a `destination.down` with `reason: "stalled"` per destination on
  webhooks and the events stream. A source that stops is now the ingest's
  news for SRT as well as RTMP and pull. A destination that stops while its
  source is still arriving is still reported as stalled.
  - As a result, SRT sources now get the ingest events RTMP and pull sources
    already had. These are the `ingest.lost` and `ingest.recovered` built-in
    alerts, and the `ingest.published` and `ingest.disconnected` webhooks and
    stream events. Like RTMP and pull, `ingest.lost` also fires once when
    nothing has arrived 20 seconds after the server starts.

- **An SRT programme's ingest metrics describe its listener.**
  `polyemesis_ingest_up` was 0 and `polyemesis_ingest_state` was `stopped`
  for every SRT programme, even while the stream was arriving, so the
  `failed` half of `PolyemesisIngestDown` could never fire for one. They now
  read the shared SRT listener: `running` while it is bound, `failed` when
  the SRT port is configured and nothing is bound, and `stopped` when the port
  is turned off or the source is disabled. MQTT's `live` is now whether the stream is arriving. It had
  read `false` for SRT throughout, and `true` for RTMP from the first show
  onwards. An SRT programme's MQTT bitrate is the relay's.

- **CI runs the Prometheus rule tests.** A `prometheus rules` job runs
  `promtool check rules` and `promtool test rules` on
  `deploy/prometheus/alerts.yml`, and checks that every query on the Grafana
  dashboard parses (`scripts/promtool-check.sh`, Prometheus 3.15.0 pinned by
  digest).
- **An SRT ingest on the shared listener shows its uptime.** It has no ingest
  process, so the dashboard's Ingest card and the header read "—" for the
  whole broadcast. Each programme's relay hub now reports how long its stream
  has been arriving, as `relay.receivingSec` on the status payload: from the
  first datagram after a silence of more than 3 s, and absent while nothing
  arrives. The card and the header print it where there is no process uptime.
  The features page lists the ingest uptime again.

- **The header's Ingest chip follows the programme selected in the header.**
  It read the last status frame on the socket and the bitrate series, which
  names no programme, so another programme's encoder turned it Live and put
  that programme's uptime beside it. It now reads the selected programme's own
  frames and relay bytes, through the same code as the dashboard's Ingest card.

- **Routing preset pickers open on the edited destination's own programme.**
  The role defaults (the music picker opens on the track annotated music)
  came from the programme selected in the header. A preset is applied against
  the destination's programme, so editing another programme's destination
  could apply that programme's mic as its music bed. The roles are now read
  from the destination's programme, and the server's defaults stand until
  that read answers.

- **A healthy install no longer shows "189 ERRORS".** Every process that
  decodes video from the relay joins it mid-GOP, and until the next keyframe
  FFmpeg's decoder complains once a frame. Those lines were counted as errors
  on the preview card and in Monitoring. A video decoder's lines are now logged
  as info until the process's first output frame, or for 8 s after the spawn
  when no frame count is reported. A decode error after that still counts.
  Those warm-up lines are also never a destination's failure reason. A run
  that died during warm-up had been shown one as its reason, on the card, in
  alerts and in webhooks. A real failure printed after them still classifies.

- **The meters and loudness processes show an uptime.** Their stdout is level
  data, so the supervisor never learned that media was flowing, and Monitoring
  showed `--:--:--` for as long as they ran.

- **The dashboard's Ingest and Pipeline cards follow the programme selected
  in the header.** On an install with several programmes they showed whichever
  one had reported last. The Ingest card's state, bitrate, track count, video
  line and publish address, and the Pipeline card's recorder, preview, meters,
  relay subscribers and renditions, are now the selected programme's, and read
  "—" until that programme reports.

- **A four-track source shows four audio chips.** Destination cards always drew
  six. They now draw one per track of the card's own programme.

- **Routing presets pick the music track from the track roles.** The preset
  pickers defaulted music to Track 1 whatever the annotations said. Music, mic
  and clean now default to the tracks annotated with those roles, and a track
  the operator picks is never overwritten.

- **No words run together on the site.** Astro drops the space between text and
  a link or inline element on the next source line, so the comparison,
  download, docs, vs, features and multistream pages read "rendered
  fromdocs/API.mdin the repository". The site build now fails on any such
  join.

- **An automod model reply without `abusive` or `confidence` is a failure, not
  a pass.** Such a reply (`{}`, `{"No": null}`) used to read as "not abusive"
  with no error, so a model that ignored the schema looked healthy and never
  fired. It now fails open with an error in the model's failure count, as does
  a confidence outside 0..1.
- **The site no longer makes two different time-to-first-stream claims.** The
  home and download pages said about five minutes and the features page two,
  and neither was measured. Both figures are gone, and a guard keeps a step
  count the quickstart does not match from coming back.

- **Destructive dialogs and the Go live card are translated.** Their labels and
  consequence text were bare English in a fifteen-locale app. They now go
  through the catalogue, and a test fails on a bare JSX label in pages and
  components.

- **Japanese, Korean and Simplified Chinese text has a font to fall back to.**
  IBM Plex Sans has no kana, Hangul or Han glyphs. Per-language rules now name
  the platform's own CJK faces after it.

- **A track's role chip no longer wraps into its meter.** The label column is
  bounded and the chip truncates, with its full name on hover.

- **Recordings keep their finish time.** The 30-second scan re-indexes every
  segment, and it was clearing the finish time an earlier writer had recorded.
  A re-index with no finish time of its own now keeps the stored one. Found by
  #940.
- **A restarted two-track destination keeps publishing while a one-track
  standby is on air.** The backup and the playlist now carry one silent pad
  per primary track, and the slate publishes one silent track per primary
  track. Before, a destination restarted in that window exited at once and
  stopped publishing after a few seconds. Verified with real FFmpeg; the
  Docker drift matrix run is recorded in the PR notes.
- **A live level restart says so.** `destination.restarted` said "the process
  exited and was started again" for restarts the engine made to apply a live
  audio level. It now says a live audio level change restarted the output (#933).
- **A broadcast delay that is still filling reads "Filling delay".** The
  destination card and the Renditions chip said Stopped while the buffer filled,
  before any process existed. They now read Filling delay, in amber.
- **Wave 3 review, last eleven findings.**
  - Roll over now can retry a failed rollover straight away. The ten-minute
    wait now applies only after a rollover that worked, or one that moved the
    destination to its replacement. The 15-second sweep still waits after a
    failure.
  - A browser that leaves while the vertical status read is in flight no
    longer leaves an error on the sub-row for a minute.
  - A failed vertical pairing removes the portrait rendition it made, unless
    another destination now uses it.
  - A store error during the "already a vertical half" check refuses the pair
    with a 500. Before, the error let the pair go through.
  - Deleting an X destination while X is still publishing its broadcast now
    ends the broadcast once X has it running. Before, the broadcast was left
    on air. That delete now asks for confirmation, as deleting a live one does.
  - A retitle whose template or destination cannot be read answers 500. Only
    a deleted row answers 409.
  - A skip is refused if the destination was switched on after the skip's
    first check. The skip now reads the destination again just before the
    platform delete.
  - When two schedules for one X destination fire close together, X's PUBLISH
    carries the title of the show that started last.
  - The caption go-live check refuses a YouTube broadcast set to ultra-low
    latency. YouTube takes no captions on those.
  - After a failed X broadcast list read, X chat checks again after 3 seconds,
    not 15. Before, the first 15 seconds of chat on a show started outside
    polyemesis could be lost.

## [0.11.0] — 2026-09-24

### Added

- **`install.sh --version` and `update.sh --binary`: install a named release,
  and only a binary that is that release.** Binary installs always took
  `releases/latest`, and GitHub never marks a pre-release as latest, so a
  staging box could not install a release candidate. It silently got the
  previous release. `--version vX.Y.Z[-rc.N]` asks for the tag by name,
  refuses one that does not exist, and the installer now says which tag it is
  installing and why. The generated `update.sh` used to end by printing
  `sudo install ./polyemesis`, and nothing checked the file. A wrong-arch or
  `VERSION=dev` build would install and then crash-loop. `update.sh --binary
  PATH --version TAG` now refuses before stopping anything unless the file's
  sha256 matches `SHA256SUMS` for this host's architecture and its `-version`
  prints the tag. It then backs up, installs, starts the service and checks it
  stayed up. Deploys are still run by hand.

- **`scripts/cut-release.sh` (and `make tag VERSION=vX.Y.Z`) runs the release
  gates before the tag exists.** Every gate in `release.yml` ran only after the
  tag was pushed, so a wrong date meant deleting the tag, re-dating through a PR,
  waiting 20+ minutes for CI on `main` and tagging again. The date must be today
  in UTC, which is tomorrow after 17:00 in California. The script reads
  `changelog-gate`, the empty-`[Unreleased]` check and `ci-gate` out of
  `release.yml` and runs them against `HEAD`. It also refuses a commit with no
  green release rehearsal, a dirty tree, a `HEAD` that is not `origin/main`, and
  the last 15 minutes before midnight UTC. With `--tag` it cuts the annotated
  tag. RELEASE-RUNBOOK.md no longer says that re-pushing a tag fixes a wrong
  date. It cannot, because the gate reads the tagged commit.

### Fixed

- **An install stopped by a signal is rolled back instead of reported as a success.**
  `install.sh` decided success from `$?`, and a SIGINT or SIGTERM sent to the
  script itself (by `timeout`, `kill` or systemd) is handled only after the
  running command returns -- usually with 0. The rollback handler then exited 0
  and undid nothing, leaving a half-made install that looked finished. The INT
  and TERM traps now pass 130 and 143, so a signal always rolls back.

- **A process no longer freezes while showing Running after a long run of
  output with no newline in it.** The supervisor read a child's stderr one
  line at a time and gave up on a line over 512 KiB, and it stopped reading
  stdout once the progress parser gave up on a line over 1 MiB. After that
  nothing read the pipe, it filled, FFmpeg blocked writing to it, and the
  process stayed Running while doing nothing until someone restarted it. The
  audio-meter sidecar triggered this when `POLYEMESIS_FFMPEG_LOGLEVEL` was
  raised to `info` or above: it was the one long-running child without
  `-nostats`, and FFmpeg's stats line ends each update in `\r`, never `\n`, so
  it grew into one line that filled the buffer in about 40 minutes. The
  meters sidecar now passes `-nostats`. Stderr is split at `\r` as well as
  `\n`, and a longer run is cut into 512 KiB log lines. Both pipes are
  drained to the end whatever their reader does.

- **Restarting a destination whose child would not stop can no longer run two
  copies or show a live stream as Stopped.** When a restart's stop reached
  its deadline and killed the child, the new supervisor started at once,
  while the old one was still waiting for the killed child to be reaped. For
  that time two FFmpeg processes could push to the same stream key. When the
  old supervisor finished it wrote Stopped over the new one's Running, and
  the card showed Stopped while the destination was live. The new child now
  starts only when the old supervisor has finished, so the destination shows
  Stopped for that short gap. A Stop in the gap still cancels the restart.

- **The in-app rollback refuses a binary that would not start on the
  database.** It swaps binaries only, so after a release that raises the
  schema version it would have put back a binary that refuses the migrated
  database at boot: a bad upgrade turned into a service that does not start.
  Staging now records, beside `<binary>.previous`, the schema the outgoing
  binary opens; the upgrade plan reports `rollbackAvailable: false` with the
  reason in `rollbackBlocked`, and `POST /api/v1/upgrade/rollback` answers
  `409` and moves nothing. A rollback point staged by an earlier release has
  no such record and is refused too: a 0.6.x binary opens the same schema but
  cannot read the stream keys 0.7.0 sealed, and nothing tells it from a 0.7.x
  one. Roll back through the backup instead, as `docs/UPGRADING.md` describes.
- **A `[redacted]` placeholder written back is refused instead of stored over
  the real credential.** A `read` token is shown `[redacted]` in place of a
  destination's backup stream key, expert arguments and the secret part of its
  URLs, and in place of ingest and failover credentials in settings and
  sources. That document PUT back with an admin credential stored the
  placeholder — sealing `[redacted]` as the backup key — and nothing failed
  until a failover needed it. Every such field now refuses a value containing
  the placeholder with a `400` that names the field.
- **Alert delivery failures are on the metrics endpoint.** A webhook that
  stopped accepting deliveries showed only on the Automation page.
  `/api/v1/metrics` now has `polyemesis_alert_deliveries_total{result="sent"|"failed"}`
  and `polyemesis_alert_last_success_timestamp_seconds`, and MONITORING.md
  gives the Prometheus rule for "deliveries are failing and none is getting
  through". Both counters, and the Automation page's, keep a deleted
  programme's deliveries, so removing a programme never lowers them and
  Prometheus does not mistake the drop for a counter reset.

- **A programme whose engine failed to start is no longer invisible.** When
  one source's engine failed to build or start, the server logged it and kept
  the others on air, which is right, but nothing reported it afterwards.
  `GET /api/v1/health` said `ok` for "1 of 2 source(s) running", and
  `/api/v1/metrics` emitted no ingest series for that programme, so
  `polyemesis_ingest_up == 0` and the bitrate alert in MONITORING.md could not
  fire for it. Health now answers `degraded` (still `200`) with the count, the
  scrape reports the programme as a stopped ingest, and a new gauge,
  `polyemesis_source_engine_up`, is 0 for it.

- **Alerts say which programme they are about, and a filling disk alerts
  once.** On an install with more than one programme, `ingest.lost`,
  `failover.switched` and `audio.clipping` read the same for every programme:
  same title, same `key`, and nothing saying which one. A receiver that
  deduplicated on `key` dropped the second programme's outage as a repeat.
  They now carry the programme's id in `key` (`ingest:1`), its name in the
  title, and `sourceId` and `sourceName` fields. `disk.low` and
  `disk.recovered` were sent once per programme, because every programme
  measures the same recordings volume; they are now sent once per install,
  with a second `disk.low` only if a later programme sees the recorder halt
  after the first reported a warning.

- **Routed tracks stay in step after a real-length failover outage.**
  The per-track realignment added for a failover to a source with fewer
  tracks was tested with a 5 s gap. In the field, with a 30 s outage, track 2
  came back summed with the backup's audio from the outage, and then the
  routed audio froze while the video kept going until the destination
  restarted. The graph was not the cause. The ffmpeg CLI treats any MPEG-TS
  timestamp jump over 10 s as a broken source and moves the whole input back
  by the jump. So track 2's return pulled track 0 and the video back 30 s,
  and the two streams then swapped the correction on every packet. Relay
  consumers (destinations, recorder, preview, meters) now read with
  `-dts_delta_threshold` set to a year, so they keep the relay's
  forward-only timeline as it is. A new engine test builds the relay with the
  production feed hop and runs the production destination command on it,
  with a 30 s one-track outage.

- **Sources added from the console keep their SRT encoder across the upgrade.** The shared SRT port now admits only sources set to SRT, which rightly shuts out RTMP and pull sources -- but every source the Sources page created on 0.10.0 and earlier was stored with no ingest mode (the create form sends only a name), and SRT was the only ingest that ever reached them. On the first boot after upgrading, their encoders were refused with "no SRT pipeline for source". The upgrade now sets those sources to `srt` (only the mode; token, latency and passphrase are kept) and logs which it changed, once. A source created from its name alone is now SRT from the start, and the API refuses an explicitly unset mode. See [docs/UPGRADING.md](docs/UPGRADING.md). Verified in Docker: a source created with `{name}` on the v0.10.0 image, then booted on this build, accepts the same SRT encoder.
- **A stalled destination no longer looks healthy, and its alert is always closed.** When a sink stopped reading, `/api/v1/status` and `/metrics` kept reporting `running`, `up=1`, no warning, and FFmpeg's whole-run bitrate, frozen and non-zero. After 5 seconds with no output movement, the process status now reports `"stalled": true` with `stalledSec`, and bitrate and speed read 0. The destination carries a "Stalled" warning unless the ingest itself is lost. `polyemesis_destination_up` now means delivering, so it reads 0, and there is a new `polyemesis_destination_stalled` gauge. Separately, a stall that ended with the sink resetting and the child respawning dropped its `destination.falling_behind` silently, so no `caught_up` or `recovered` was ever sent. That latch is now held through the respawn and closed by exactly one message. Exploratory row 7.
- **A stalled destination no longer looks healthy, and its alert is always closed.** When a sink stopped reading, `/api/v1/status` and `/metrics` kept reporting `running`, `up=1`, no warning, and FFmpeg's whole-run bitrate, frozen and non-zero. After 5 seconds with no output movement, the process status now reports `"stalled": true` with `stalledSec`, and bitrate and speed read 0. When the source is still arriving, the destination itself reports `"stalled": true` and carries a "Stalled" warning, `polyemesis_destination_up` reads 0 (it now means delivering), and a new `polyemesis_destination_stalled` gauge reads 1. A lost ingest freezes every output, but it does not count as a destination stall, so it does not page once per destination. Separately, a stall that ended with the sink resetting and the child respawning dropped its `destination.falling_behind` silently, so no `caught_up` or `recovered` was ever sent. That latch is now held through the respawn and closed by exactly one message. Exploratory row 7.
- **A stalled destination no longer looks healthy, and its alert is always closed.** When a sink stopped reading, `/api/v1/status` and `/metrics` kept reporting `running`, `up=1`, no warning, and FFmpeg's whole-run bitrate, frozen and non-zero. After 5 seconds with no output movement, the process status now reports `"stalled": true` with `stalledSec`, and bitrate and speed read 0. When the source is still arriving, the destination itself reports `"stalled": true` and carries a "Stalled" warning, `polyemesis_destination_up` reads 0 (it now means delivering), and a new `polyemesis_destination_stalled` gauge reads 1. "The source" is the failover feed while failover is on air, and a destination is not counted stalled until the source has been arriving for those 5 seconds, so a returning ingest does not flash every destination stalled. A lost ingest freezes every output, but it does not count as a destination stall, so it does not page once per destination. `polyemesis_destination_info` gains a `source_id` label, and docs/MONITORING.md's slow-output query now uses it to skip destinations whose ingest is not arriving. Its "nobody is streaming" query now reads the ingest bitrate instead of `polyemesis_ingest_up`, which stays 1 while an SRT or RTMP listener waits for a publisher that has gone. Separately, a stall that ended with the sink resetting and the child respawning dropped its `destination.falling_behind` silently, so no `caught_up` or `recovered` was ever sent. That latch is now held through the respawn and closed by exactly one message, unless you disable or delete the destination while it is open, which closes it without one. Exploratory row 7.

- **A clean restart of an idle server no longer logs three ERROR lines.** Shutdown unsubscribed the recorder, preview and meters from the relay hub whether or not they had ever started. On an idle engine none had, so the hub reported three removals of names it never held -- at ERROR, because since #711 that message means a teardown named the wrong subscriber and left a real one feeding a dead process. It now unsubscribes only consumers that hold a relay port, which they do exactly when they subscribed; a real mismatch still logs. Seen on the staging box on its first restart after the exploratory-testing fixes.

- **The Docker upgrade command named an image tag that does not exist.**
  `GET /upgrade/plan` printed `docker pull rainmanjam/polyemesis:v0.10.0`, the
  release feed's spelling, but release.yml publishes images as `:0.10.0` (the
  `v` is dropped), so the pull answered "not found". Before any update check it
  printed `rainmanjam/polyemesis:` with an empty tag, and the manual install
  plan named a `polyemesis--linux-amd64` file. The image tag now comes from one
  function, `upgrade.ImageTag`, and a plan with no tag names none.
- **A compose-built install was told to upgrade with a command that does
  nothing.** The repo's `docker-compose.yml` builds the image from source and
  stamps version `compose`, so the update check can never compare it — and
  `/upgrade/plan` offered `docker compose pull && docker compose up -d`, which
  skips a `build:` service and restarts the same image, exiting 0. An image
  whose version is not a release tag (`compose`, `docker`, a `git describe`
  string) is now told `git pull && docker compose up -d --build`, with the
  published image named as the way to a comparable version. `upgrade.PlanFor`
  takes the running and offered versions as named fields, so the two strings
  cannot be passed the wrong way round. docs/UPGRADING.md said the same no-op
  pull and is corrected.
- **A GPU container was told to pull the CPU image.** The CUDA and VA-API
  images are published as `:0.10.0-cuda` and `:0.10.0-vaapi` and carry the
  same version as the default image, so the upgrade plan named `:0.10.0`, and
  that pull succeeds onto an image with no hardware encoder. The GPU
  Dockerfiles now declare `POLYEMESIS_IMAGE_VARIANT`, and `upgrade.ImageTag`
  takes the variant as a required argument, so it adds the suffix. A variant
  this build does not recognise gets no tag in the plan.
- `-verify-backup` no longer writes to the backup it checks. It opened
  `polyemesis.db` read-write, so SQLite folded the `-wal` into the main file and
  deleted both sidecars, although INSTALL.md says it writes nothing; and on a
  read-only directory or mount — how the Docker `update.sh` hands it the backup
  — it could not create the `-shm` and refused a good backup. It now verifies a
  private copy in the temporary directory and leaves the backup byte-identical.
- `-verify-backup` no longer accepts any `secret.key`. It checked only that the
  file existed, so a key from another install, an empty file, a non-hex or
  wrong-length one, or a directory all passed — and the restore then either
  refused to boot or brought every credential back unreadable. It now reads the
  key with the parser the server boots with, and tries it on the values the
  database sealed: for each sealed column holding any, the key must open at
  least one, or the backup is refused naming the table. The success line now
  says so. Where a column holds one value this server's own key cannot open
  either (the MQTT password or automod key after an earlier bad restore), the
  refusal says so and names what to re-enter or clear in the console, instead of
  sending you to take a backup that would fail the same way.
- **Re-running `install.sh` no longer moves a working install off its own
  ports.** Every port it checked was already held by the service it was about
  to restart, so under `--yes` it accepted its own offer: the web UI moved
  8080 -> 8081. A port held by the polyemesis process (binary mode) or published
  by the polyemesis container (docker mode) is now recognised as ours. The port
  check also reads only the local-address column, so `10.0.0.80:5000` is no
  longer taken for a listener on port 80.
- **`--srt-port` / `--rtmp-port` now mean something, or are refused.** The
  server's SRT and RTMP listeners are database settings (Settings -> Listeners),
  and nothing the installer writes reaches them. In binary mode the flags only
  opened a firewall port and printed an address nothing listened on; they are
  now refused with a pointer to Settings -> Listeners. In docker mode the chosen
  host port is published onto the server's 6000/1935 inside the container,
  instead of `N:N`, which published a port nothing inside listened on. A
  docker re-run keeps the container side an existing `docker-compose.yml`
  already has, so an install that made `7000:7000/udp` work by moving the
  listener to 7000 is not rewritten to `7000:6000/udp`; the docker summary now
  says that moving the listener means changing that container side too.
- **The docker-mode `update.sh` refused every real upgrade.** Its backup check
  unpacked the archive into a root-owned 0700 directory and bound it into the
  image read-only; the image runs as uid 10001 and SQLite cannot open a WAL
  database without creating its `-shm`, so the check failed with
  `unable to open database file (14)` and left the container stopped. The
  archive now goes in on stdin and is unpacked inside the container. The
  acceptance stub that had accepted the read-only mount now models it.
- **Docker backups are 0600.** `backup-*.tar.gz` holds `secret.key` and
  `tls/ca.key`, and was written 0644 by tar inside the container, so any local
  account could read the key out of it. The script now creates the file itself
  under `umask 077` (and with noclobber), and tightens archives left by earlier
  runs.
- **A failed docker `update.sh` no longer leaves the container stopped in
  silence.** It stops the container before the backup, and every refusal after
  that point used to exit with it down. It now starts what it stopped, says so,
  and removes the unverified archive; if the start fails, it says the container
  is STOPPED and prints the command. A failure after the new image was pulled
  does not claim "nothing was upgraded" -- `up -d` may already have recreated
  the container on it -- and names the verified archive instead.
- **The binary-mode `update.sh` does the same**: a refusal after `systemctl
  stop` starts the service again (or says STOPPED, with the command) and
  removes the unverified copy.
- **The binary-mode rollback no longer deletes the recordings.** `update.sh`
  printed `rm -rf <dataDir> && cp -a <backup> <dataDir>` as the way back. That
  deleted every recording and upload made since the upgrade, and copied the
  root-owned `polyemesis.previous` into the live directory, where every later
  backup carried it. `install.sh` now writes `rollback.sh`, which restores the
  state (database, `secret.key`, `tls/`), removes the newer `-wal`/`-shm`,
  leaves the media directories alone, puts the previous binary back and starts
  the service. `docs/UPGRADING.md` describes it.
- **Every destination start cost about 15 seconds of dead air.** The relay
  consumers' 15s `-analyzeduration` was documented as a ceiling that probing
  leaves early, but the ffmpeg CLI forces the MPEG-TS demuxer's `scan_all_pmts`
  to 1. With it set, the demuxer never declares its header complete, so every
  destination, recorder, preview and meter read the whole window before writing
  anything: 15.5s to first output on a 2s GOP, on 8.1.2 and 9.0.1. Relay
  consumers now pass `-scan_all_pmts 0` and start in about one GOP. A late
  joiner still waits for the next keyframe (#460). All four consumers now take
  their input options from the one function, so none can miss this again.
  `docs/investigations/398-e-probe-window.sh`, which had "shown" the ceiling
  behaviour, stopped its consumer with SIGTERM. The signal flushes the probe,
  so the result was an artefact. The script now measures time to first output.
- **A short-segment recording lost its first segment.** A recorder started
  before the encoder connected held the whole 15s probe window of media. It
  then passed all of it to the segment muxer at once. With `segmentSeconds`
  below about 15, segment 1 opened in the same wall-clock second as segment 0.
  Both got the same `rec-%Y%m%d-%H%M%S` name, and segment 1 overwrote segment 0,
  so the first ten seconds were gone. The probe fix above removes the backlog
  in the usual case, but not in every case. If a declared track sends nothing
  for 15s, for example an audio track that stays silent (#674), the probe still
  runs its full window. The backlog then comes back. With `segmentSeconds`
  below about 16, the first segment can still be overwritten. Segment names
  are still only unique to the second.
- **A recording or file destination stopped after its publisher left is
  finalised, not killed.** Relay consumers read with no timeout by design, so a
  destination can ride through a quiet patch. On a silent feed, FFmpeg never
  acts on a single SIGTERM, so these stops waited out the 8s grace and were
  SIGKILLed. That left an MKV with no duration and no cues. Affected: the last
  segment when an ingest ended, `recording.enabled=false`, `docker stop` on an
  idle server, and the Stop button on a file destination. The supervisor now
  wakes a stopping relay consumer that has not exited after 0.75s. The relay
  sends that consumer alone one empty PES start per stream, which completes the
  packet FFmpeg is holding, and only into a feed that has been silent for
  0.25s. The 0.75s is FFmpeg's 0.5s stats period plus a margin: on Linux FFmpeg
  registers a SIGTERM only at its next stats tick, and a wake that lands before
  then is forwarded as ordinary media and cannot be repeated. A 0.3s wake passed
  on macOS, where the signal is noticed at once, and failed about half the time
  on Linux. Measured: 8.0s and `duration=N/A` before, 0.75s and a finalised file
  after, on both. The no-timeout rule is untouched: a running destination still
  rides through silence.
- **A segment that was never finalised no longer logs a WARN every 30 seconds.**
  The recordings scanner re-probed an unmeasurable segment on every pass for
  the life of the process. It now asks once per file size and says once, in
  plain words, that the file was most likely never finalised and how to remux
  it. Only a probe that ran and found no duration is remembered; a timeout or
  an ffprobe that could not start is retried on the next scan, so a good
  segment is not stuck at 0 ms by one slow moment.
- **A failover to a source with fewer tracks no longer leaves the routed tracks
  out of step.** A slate or a one-track backup carries only track 0, so tracks 2
  and up vanish from the relay for the length of the outage. `amix` and the
  duck's sidechain pair their inputs by sample count, not timestamp, so when the
  primary returned, the missing track picked up where it stopped and was summed
  with track 0 from an outage earlier: audio from before and after the outage in
  one mix, offset by the outage (18 s measured) until the destination restarted.
  Every track's chain in a multi-track graph now ends with
  `aresample=async=1:first_pts=0`, which fills the gap with silence and anchors
  every track at the same origin. One-track graphs are unchanged.
- **Failover away from a dead primary lands at the grace period, not eight
  seconds after it.** The copy hop that carried the primary into the selector
  sits blocked on its now-quiet input and ignores SIGTERM, and the switch
  waited out its full 8 s shutdown grace before starting the slate or backup
  (11.5 s measured against a 3 s `graceSeconds`). Because the incoming feed's
  timestamp offset is stamped at the decision, those 8 s also put its timeline
  behind wall clock, and the next switch repaid them as an 8 s forward jump.
  A switch now sends the outgoing copy hop SIGTERM while its input is still
  delivering (a healthy hop exits in about 0.1 s that way) and waits at most
  0.5 s. Only a hop still running after that is cut off from its input, so it
  has nothing left to publish, and the replacement starts while the old child
  finishes dying in the background. The wait, and so any leftover jump at the
  next switch, is now at most 0.5 s, and a quick switch back does not wait for
  the feed before last. The seam ledger line gains `outDetached=true` when a
  hop is left behind this way.

- **Deleting the last recorded segment works as soon as recording is off.**
  `DELETE /recordings/{id}` refused any segment that started within one
  segment length plus two minutes, recorder or no recorder, so turning
  recording off and deleting what it had just made answered `409` for up to an
  hour and two minutes, telling the operator to stop the recording they had
  already stopped. With recording off the recorder's own segments are now
  deletable at once. A file destination's output keeps the guard either way,
  and its refusal now names the destination instead of the recorder.
- **The same delete works when the free-space floor has stopped the recorder.**
  The guard read `recording.enabled`, which stays on when `minFreeGb` halts
  recording, so the last segment was refused for up to an hour and two minutes
  exactly when the operator was deleting to free the disk. It now asks the
  engines whether any recorder process is running.
- Multi-source playout: every source's playout muxer wrote the same
  `<dataDir>/playout/<variant>/`, so two programmes overwrote each other's
  segments and either one's teardown cleared the other's live window. Each
  source now packages into `playout/<sourceId>/`; the public URL is unchanged. A
  top-level `playout/<variant>/` left by an earlier release can be deleted.
- **Changing `playout.sourceId` switches what the public page serves without a
  restart.** The `/playout/` mount built its file handler once, from whichever
  programme was named at the first public request after boot, so a saved switch
  showed in the console while the audience kept the old programme until the
  server restarted. A request that arrived before any engine was up likewise
  pinned 503 until restart. The programme is now resolved on every request.
- A recording's `sourceId` flapped between programmes: every source's recording
  manager scans the shared recordings directory and stamped its own source on
  every file it saw, so the last scanner won and the clip editor named a clip's
  tracks after the wrong programme. Master segments are now named
  `rec-s<sourceId>-<date>-<time>.mkv` and attributed from the name; a segment
  written by an earlier release keeps whatever attribution it already had.
  The name also stops two sources that start recording in the same second
  from writing the same file.
- One-port SRT refusals now reach the encoder with their typed reason
  (`REJ_BADSECRET`, `REJ_CLOSE`, `REJ_RESOURCE`, `REJ_ROGUE`, `REJ_UNSECURE`), as
  [TROUBLESHOOTING](docs/TROUBLESHOOTING.md) already described. Every refusal
  used to arrive as the generic `REJ_PEER`, because gosrt's server loop
  overwrote the reason; the listener now runs its own accept loop.
- The one-port SRT listener admits a publisher only into a source whose ingest
  is set to SRT. It used to accept any source's token, so an SRT publish was
  admitted into an RTMP or pull source beside the ingest already writing its
  stream, and into a source created from its name alone -- which the API
  reported `tokenEnforced: false` with no publish URL. Such a publish is now
  refused with `REJ_RESOURCE`; choose SRT on the source first. `POST /sources`
  now fills unspecified ingest fields from the defaults, so
  `{"name": "…", "ingest": {"mode": "srt"}}` is a complete request, and the
  Sources page explains an unchosen or pull source's token instead of pointing
  at a one-port setting that no longer exists.
- **`/api/v1/status` and `/api/v1/loudness` no longer answer 200 with an empty
  body after the audio falls silent.** ebur128 prints `nan`/`-inf` for a
  loudness window with no signal; the parser now reads those as the -70 LUFS
  "not measured" floor. Separately, any response body that cannot be encoded
  now answers 500 with an error and is logged, rather than a silent empty 200,
  and an unencodable WebSocket event is dropped and logged instead of closing
  every open console.
- **`/healthz`, `/health`, `/livez` and `/readyz` no longer answer 200.** They
  fell through to the console's HTML page, so an uptime monitor pointed at them
  stayed green while checking nothing. They now answer `404` with a body naming
  the real check, `/api/v1/health`.
- **`destination.falling_behind` now fires during a stall, and clears after it.**
  It judged FFmpeg's `speed=`, an average over the whole run carried in the
  progress report that stops arriving when a sink stops reading. A stalled
  destination therefore read ~1.00x while stalled, alerted only after the heal,
  stayed raised for most of an hour, never sent `caught_up`, and a second stall
  could not alert. It now measures how fast the output time advances over the
  last 20 seconds, and says nothing while the ingest itself is lost.
- **`destination.up` means the destination is delivering, not that its process
  exists.** It fired on every spawn, so a destination pointed at a closed port
  announced "delivering" and flapped without sending a byte. It now waits for
  FFmpeg's output time to move. A sink that stops reading, which leaves the
  process running, now produces `destination.down` with `reason: "stalled"`
  after the 10s dwell, and `destination.up` when data flows again. Broadcast
  lifecycle automation does not end a broadcast on a stall.
- **A stalled destination is visible on `/metrics`.** New counters
  `polyemesis_destination_output_seconds_total` and
  `polyemesis_destination_output_bytes_total` stop advancing when delivery does;
  nothing else on the scrape moved, since the process stays running and the
  bitrate gauge is FFmpeg's whole-run average. That gauge's help text now says
  so.
- **`/api/v1/health` no longer says the database is fine while it is failing.**
  The check read page one, which a full volume and a file with a corrupt page
  elsewhere both serve. So health said `ok` while every save failed with
  "database or disk is full", and on a database with a damaged hooks table it
  said `ok` while `/hooks` answered 500 and hooks had stopped. The store now
  records the storage errors its real statements get, including a corrupt page
  met partway through reading a table and a write through a prepared
  statement, and health reports them as `degraded`: a full or read-only volume
  until the next successful write or committed transaction, a damaged file
  until restart.
- **Saving the Settings page no longer unsets the ingest mode chosen on the
  Sources page.** `GET /settings` served the settings blob's copy of `ingest`,
  which the Sources page never updates, and the page PUTs the whole document
  back on every save — so the write-through copied the stale block over the
  live source. Publishes were refused while `/health` said ok. `settings.ingest`
  is now read from the default source on both GET and PUT, so an unchanged
  round-trip is a no-op by construction.
- **An unknown field under `settings.automod` is now a 400, like every other
  section.** Automod's own JSON decoder sat outside the request's
  unknown-field check, so a typo was accepted and dropped. AUTOMOD.md named the
  history bounds `window`, `retain`, `idleEviction` and `maxAuthors`, none of
  which the server reads; it now names `windowSeconds`, `retainPerAuthor` and
  `idleEvictionSeconds`, says the 20,000-author ceiling is fixed, and a test
  holds the table to the struct.
- **An automod cell can no longer be armed over a checker that is not
  configured.** The API accepted `twitch/ban/model` with the model switched
  off. The console drew that cell as inert, and its banner still said an
  irreversible action was armed. Once someone configured the model, the ban
  went live. `PUT /settings` now refuses to switch on such a cell and names
  it. `/automod/matrix` and the console banner count only cells that can fire.
- **A Settings save from a stale page no longer undoes other operators'
  changes.** The page PUTs the whole document, so the last save won on every
  field. A tab opened before someone disarmed an auto-ban re-armed it by saving
  an unrelated retention change. `GET /settings` now carries a `version`, and a
  `PUT` that sends back an out-of-date one is refused with `409
  settings_conflict` and nothing is stored. The console sends it on every save
  and, on a conflict, keeps your draft and offers a reload. Clients that send no
  `version` are not checked. A save that is stored and then answers with an
  error (`503 no_source` for an ingest change before any source exists, or a
  failed reconcile) returns the stored `version` in the error body. The console
  sends that version on its next save, so that save is not refused as a
  conflict with your own change.
- **Live chat works on an install with more than one source.** The chat socket
  opened `/api/v1/ws` without `?source=`, which the server refuses with
  `400 source_required` once there are two programmes, so the chat page sat on
  "socket offline" and reconnected forever while its scrollback loaded fine.
  It now names the programme the console is following, waits until that is
  known, and moves when the operator switches. Every socket URL is built by
  one helper, and a test refuses any `new WebSocket(` that bypasses it.
- **Expert mode's Clear asks first.** It deleted a destination's extra FFmpeg
  arguments on one click, which reconciles the destination and restarts a live
  output. It now opens the same confirmation every other destructive action
  uses, naming the destination and saying that a live output restarts.
- **An expired session returns to the login screen.** Only the page load read
  a 401; once signed in, an expired or revoked session made every button fail
  with a "not signed in" toast for as long as the operator kept clicking. A 401
  on any request now signs the console out and says why. A wrong password at
  sign-in or on a password change is still reported in place, because those
  401s are about what was typed, not the session.
- **A go-live push no longer looks in flight for ever after a server
  restart.** Push jobs live in the server's memory, so a restart mid-push lost
  the job and the composer polled a 404 every 1.2 seconds, holding Push
  disabled on "Pushing…" until the tab was reloaded. A 404, or ten failed polls
  in a row, now ends the poll: the composer says the push's status was lost,
  marks unfinished rows Unknown, and Push works again. The composer's requests
  also go through the console's shared client now, so a reverse proxy's HTML
  error page reads as "request failed (502)" rather than a JSON parse error,
  and an expired session on this card signs the console out like everywhere
  else.
- **A tab left open across an upgrade no longer goes blank.** The lazy pages
  (Jobs, Chat, Monitoring, Playout, the clip editor) are separate files named
  by content hash, and an upgrade replaces them, so an old tab's first visit to
  one failed to load it -- and with no error boundary the whole console
  unmounted to an empty page that Back did not fix. The console now reloads
  itself once when that happens, to pick up the new version. If a page still
  fails, only that page is replaced by a notice with a Reload button; the
  navigation and every other page keep working.
- **The automatic-moderation card and about thirty other sentences follow the
  language setting.** A pass over every page in every locale found the whole
  automod matrix card, and sentences on the dashboard, chat pane, routing,
  meters, renditions, clips, playout, monitoring, automation and settings pages,
  still in English whatever language was chosen -- four of them with a
  translation already in all fifteen catalogues that the page never asked for.
  They are catalogue keys now, translated in all fifteen languages, and a test
  keeps them from drifting back to literals. Three stay English on purpose: the
  meter rows' status words (a family keyed together or not at all), the
  engine's own status sentence, and the model's default instruction, which is
  a setting sent to the model verbatim.
- **The upgrade check for keys that did not survive a restore said "0" when it
  had not run.** `docs/UPGRADING.md` counted `keyUnreadable` with
  `curl -s localhost:8080/… | grep -o keyUnreadable | wc -l`. An `install.sh`
  install serves HTTPS on 443, so the request failed, `-s` hid it, and `wc -l`
  printed `0` — the all-clear, confirmed against a data directory restored
  without `secret.key`. It also read a path the response does not have: rows are
  `{"destination": {...}}`. The check is now `curl -f` against the operator's own
  `$POLYEMESIS_URL`, and `jq` lists the affected destinations by name, so `[]` is
  the all-clear and no output is a failed request. `INSTALL.md`'s health check
  had the same hard-coded `http://localhost:8080`, which fails on every default
  `install.sh` install; it now has a table of the three install shapes, and
  `HARDWARE.md`'s encoder-verdict check had the same `curl -s localhost:8080`.
  All three are held by `internal/testenv/operator_docs_test.go`.

- **The Prometheus example sent its bearer token in cleartext.**
  `docs/MONITORING.md` gave no `scheme`, and Prometheus defaults to `http` on
  port 80 — which, on an install terminating TLS itself, is the HTTP→HTTPS
  redirect. The token crossed the network once per scrape before the redirect
  answered, and the scrape then failed against the self-signed certificate. The
  example now says `scheme: https`, names `:443`, and verifies with
  `tls_config.ca_file` pointing at the install's local CA.

- **The docs told systemd operators to move to 443 by editing a line that
  cannot move them.** `INSTALL.md`, `TLS.md` and `CONFIGURATION.md` all said to
  set `addr: ":443"` in `config.yaml`, but both systemd units and the image's
  `CMD` pass `--addr`, and `main.go` applies the flag after loading the file:
  the server came back on the same port with the same warning. They now say
  where the port actually comes from on each install shape, including that the
  compose file `install.sh --mode docker` writes is the one where the file does
  win. `TLS.md` also now warns that the image's `HEALTHCHECK` is plain HTTP, so
  turning TLS on inside the container marks a healthy server unhealthy, and
  gives the HTTPS replacement. `INSTALL.md`'s code citations by line number —
  two of them ranges that ran backwards — are symbol names now, and a guard
  refuses a `file:line` citation in the operator docs.

- **The release notes and `INSTALL.md` still called the Windows runtime abort
  (#440) unresolved.** It was fixed in 0.9.0 and the issue is closed; the
  release-gate test had been requiring the release body to name it, which kept
  the stale warning in place. The Windows warning now names the defect that is
  still open — a service stop truncates the recording in progress.

- **The macOS launchd job in `INSTALL.md` crash-looped as written.** It ran
  `/usr/local/bin/polyemesis -config …/config.yaml`, and no step created either
  file; a missing explicit `-config` refuses to start, and `KeepAlive` repeats
  that forever. The section now installs the binary and copies
  `config.example.yaml` first, and says the console is then on `https://`.
  The Gatekeeper note uses the release asset's real name
  (`polyemesis-<tag>-darwin-<arch>`) and says to clear the quarantine flag
  before the first launch — a quarantined binary was seen to hang silently.

- **`INSTALL.md`'s systemd steps left the data directory world-readable.** The
  unit's own install notes had gained `chmod 0750 /var/lib/polyemesis` (the
  directory holds `secret.key`); the page had not. It now has it, says the block
  runs from a clone, gives the command for a downloaded release binary (whose
  name carries the tag), and says the result answers on `https://<host>:8080`.

- **`UPGRADING.md`'s manual upgrade now has `update.sh`'s guards, and its
  Docker half upgrades.** The binary procedure is a paste-safe script that
  stamps the backup to the minute, refuses an existing destination (a second
  same-day upgrade used to nest its copy inside the first), keeps
  `polyemesis.previous`, and runs `-verify-backup` before the binary is
  replaced. The Docker half ran `docker compose pull` against the repository's
  compose file, which *builds* its image — so it restarted the old version; a
  clone now does `git pull` and `up -d --build`. Its backup tarball was written
  into the clone, where the next build's `COPY . .` took it into the image, and
  its `|| exit 1` closed the terminal it was pasted into. The page also no longer
  says a binary-mode `update.sh` "pulls": it stops at a verified backup, with the
  service stopped, and prints the two commands that finish the job.
  It accepts the release asset as downloaded (mode 0644) rather than refusing it
  as "no executable"; checks the copy with the new binary when the installed one
  predates 0.9.0's `-verify-backup` (it would otherwise exit on an undefined
  flag and call a good backup bad); and its way back restores the state —
  database, `secret.key`, `tls/` — while keeping `recordings/`, `uploads/` and
  the other media directories, instead of `rm -rf` of the whole data directory.

- **`UPGRADING.md` had no note for 0.9.0 or 0.10.0.** 0.9.0 changed two
  defaults an existing install can hit — the loopback default bind, and a
  missing explicit `--config` refusing to start — and the page's banner still
  named 0.8.0 as the newest release. Both versions now have a note (0.10.0: no
  schema change, and a 0.9.0 binary opens its database), the 0.7.0 note no
  longer says a missing `secret.key` produces "no error to notice" (0.9.0 logs
  one), and every release from here on must have an `Upgrading to X.Y.Z`
  section, held by a test and by `RELEASE-RUNBOOK.md`.

- **Nothing said the console must be served from the root of its own
  hostname.** It requests its API, assets, WebSocket and HLS preview by
  absolute path, so behind `location /polyemesis/` on a shared host it loads
  blank. `deploy/nginx.conf.example`, `INSTALL.md` and `TLS.md` now say to give
  it a hostname of its own and proxy `/`.

- **`API.md` described `stop-all` as unscoped and never mentioned its
  confirmation body.** It is scoped by `?source=` when one is given, and it
  refuses without `{"confirm": true}` — so a script written from the page got a
  `400` on every call. Both are documented now, with an example. The page also
  says the refused-route table has fifteen rows of which thirteen are `GET`s
  (a test computes both from `readScopeDeniedPatterns`), that `/hls/*` is
  mounted at the root, and what `POST /routing/compile` takes: the profile,
  wrapped in `{"profile": …}`.

- **The failover backup encoder's address was in no user-facing page.** It is
  the source's publish token plus `.backup`, on the primary's port, for SRT and
  RTMP alike; `docs/OBS.md` now says so, and that
  `failover.backup.rtmp.streamKey` — which looks like the backup's address —
  addresses nothing. `DESIGN-ONE-PORT-INGEST.md` no longer promises an
  immediate reconnect after a blip: a new publisher is refused for three
  seconds (`srtserver.StaleAfter`) after the old one's last packet.

- **The "newer schema" refusal names the right backup.** An older binary
  refusing a database a newer release wrote told the operator to restore "a
  backup taken before the rollback". That backup is the newer database again,
  and it is refused the same way. The message now says to restore the backup
  taken before the upgrade, which is the one `update.sh` makes.
- **A misspelled `config.yaml` key stops the server instead of being ignored.**
  Only the `tls:` block refused unknown keys. Anywhere else, a typo was
  dropped and its setting stayed at the default, with nothing logged: for
  example `trustProxyhHeaders: true` (cookies lose `Secure` behind a proxy) or
  `DataDir:` (the database goes to `./data`). The whole file is now decoded
  strictly, nested blocks included, and the error names the key, its line and
  the keys valid in that block. The retired `enhancedRtmp` key is still accepted. A
  `tls.hostname` with no `tls.mode` is also refused, because it meant plain
  HTTP. See [docs/UPGRADING.md](docs/UPGRADING.md).
- **`-reset-admin` and `-verify-backup` no longer create a data directory where
  they are run.** Both ran after the server made its directories. So on a hand
  install, whose unit passes `--data` while the copied config says `./data`,
  `-reset-admin` created `./data/…` and an empty database in the current
  directory. It then told the operator of a working install to "complete
  first-run setup". Both now run before anything is created. `-reset-admin`
  refuses when there is no database, and names the absolute path it looked
  in. The reset commands in INSTALL.md and the FAQ now pass `-data`.
- **Editing `addr:` in config.yaml now moves the port on an `install.sh`
  install.** The unit the installer wrote passed `--addr` as well, and the
  flag beats the file. So an operator who followed the docs or the server's
  ":443" warning restarted onto the same port. The generated unit no longer
  passes `--addr`. Where a flag does set the address (the shipped
  `deploy/polyemesis.service`, the image's `CMD`, an older generated unit),
  the startup warnings now say it came from `--addr` and where to change it.
  See [docs/UPGRADING.md](docs/UPGRADING.md).
- **Docker no longer kills a shutdown before its recordings are finalised.**
  The server allows itself 35 s to shut down, and systemd waits 45 s. The
  repository's `docker-compose.yml` (including its GPU variants) and the one
  `install.sh --mode docker` writes set `stop_grace_period: 30s`. So a
  shutdown that used its budget was SIGKILLed mid-teardown, which truncates
  recordings. This is the #645 failure, fixed for systemd but not for Docker.
  All compose files now say `45s`, and a test holds every compose grace
  period and every `TimeoutStopSec` to the budget plus its margin. An existing
  compose file keeps `30s` until you edit it or re-run `install.sh`.
- **An unknown `--log` value stops the server instead of meaning `info`.**
  `--log warning` or `--log trace` used to start a server logging at `info`,
  and nothing said the value had been ignored. The server now refuses to
  start and lists the accepted values: `debug`, `info`, `warn`, `error`. Case
  is still ignored.

### Security

- **The self-signed local CA can now vouch only for this server.** It carried
  no name constraints, so anyone who read `<dataDir>/tls/ca.key` -- a shell
  through expert mode, a backup, a stolen disk -- could mint a certificate for
  any site that every client trusting the CA would accept. The CA is now
  limited, by critical name constraints, to `tls.hostname`, `localhost` and
  the loopback addresses. **An existing CA is replaced on the first start**,
  and so is the CA whenever `tls.hostname` changes; the start logs a `WARN`
  naming the new CA's file and fingerprint. Remove the old CA from every trust
  store and install the new one: see `docs/UPGRADING.md`.
- **Deleting a source, or a destination that is on air, has to be confirmed in
  the request.** `DELETE /sources/{id}` cascades to every destination and
  rendition on the programme, destroying their stream keys and ending any live
  YouTube broadcast among them, and `DELETE /destinations/{id}` ends its
  broadcast — both permanently, and both reachable with an admin API token
  that never sees the console's dialog. A source delete now needs
  `{"confirm": true, "destinations": N}` matching the current count (`409` on
  a stale count), and a destination in `testing` or `live` needs
  `{"confirm": true}`. The console sends both. API scripts that delete sources
  must change; see `docs/UPGRADING.md`.
- **Changing or deleting an alert rule is now recorded.** Creating, editing
  or deleting a rule wrote no log line and raised nothing, so deleting the only
  rule left no trace and the channel just went quiet. Each change now writes an
  `INFO` line (name, redacted URL, client address) and raises a new event,
  `alerts.rule_changed`. A deleted rule is sent that event itself before it
  stops receiving anything.
- **A read-scoped API token no longer reads viewer chat.** `read` means
  metadata, not content, and recordings and transcripts were already refused,
  but `GET /chat`, `/chat/messages`, `/chat/search` and `/chat/users` answered
  a read token with the scrollback, and `/ws` sent it every chat message.
  Those four routes now answer `403`, and a read-scoped socket is not sent
  `chat` events (connection state still arrives). A monitoring script that
  read chat needs an `admin` token.

- **A refused Kick webhook no longer writes its secret to the log.** The
  request log recorded the full path of every 4xx and 5xx, and
  `/api/v1/chat/kick/{secret}` answers a wrong method, a non-JSON body or a
  bad signature with one, so the webhook secret reached journald on exactly
  the requests that were not Kick's. The request log now records the route
  pattern a request matched, which redacts every path parameter; a request
  that matched no route is still logged with its path.

- **One IPv6 host can no longer dodge the login throttle by rotating
  addresses.** The login and setup throttles keyed on the full client
  address, and a VPS routinely gets a whole /64, so each of its addresses got
  its own five free attempts. An IPv6 client is now counted by its /64, and
  an IPv4-mapped address as its IPv4 address. Each throttle also has a
  budget that all addresses share (100 attempts in a burst, then one a
  second), charged when an attempt is let through rather than when it fails,
  so a large pool of addresses cannot guess without limit either, however many
  requests it has in flight. A correct password gives its charge back.

- **A client that reaches the port directly can no longer choose its own
  throttle key.** With `trustProxyHeaders: true`, `X-Forwarded-For` and
  `X-Real-IP` were believed from any peer. The docs say to bind 127.0.0.1
  behind the proxy, but the shipped unit's `--addr :8080` overrides
  `config.yaml`, so the port was often public. A direct client could then pick
  a fresh login and setup throttle key per request and write any address into
  the audit log. The headers are now believed only from loopback and from the
  new `trustedProxies` list (addresses or CIDRs). The server logs once when it
  ignores a forwarded header from an unlisted peer, and warns at startup when
  `trustProxyHeaders` is on and the listener is public, saying whether
  `--addr` or `config.yaml` set it. A proxy that is not on the same host must
  now be listed; see [docs/UPGRADING.md](docs/UPGRADING.md).

- **A fresh install can no longer be claimed by whoever reaches its port
  first.** `install.sh` starts the service and opens the firewall before the
  operator has a browser open, and `POST /api/v1/setup` made the first caller
  the admin. While no admin exists, the server now makes a one-time setup code
  at startup, writes it to `<dataDir>/setup-code` (mode 0600), and prints it
  once in the startup banner. Setup refuses without it (`403`). The code is
  used up when the admin is created, and a restart before then keeps it. The
  first-run screen has a field for it and says where to find it, and
  `install.sh` prints it at the end. `POLYEMESIS_SETUP_CODE` presets it for
  unattended installs. Installs that already have an admin are unaffected. See
  [docs/UPGRADING.md](docs/UPGRADING.md) and
  [docs/INSTALL.md](docs/INSTALL.md#the-first-run-setup-code).

- **Upgrading from 0.6.x no longer leaves plaintext stream keys in
  `polyemesis.db`.** `secure_delete` only zeroes what is freed while it is on,
  and every release before 0.7.0 ran without it: a real 0.6.0 install with five
  destinations still had two keys legible in the destinations root page after
  upgrading to 0.10.0, where a page split had left them. The upgrade now runs
  `VACUUM` and truncates the log whenever it seals keys or opens a pre-0.7.0
  file, and refuses to start if it cannot. Installs that already sealed under
  0.7.0–0.10.0 still need the one-off `VACUUM` in `docs/UPGRADING.md`, which
  now says so for every such install, not only 0.7.0 ones. The regression test
  writes its pre-upgrade history with `secure_delete` off, as 0.6.0 did; the
  earlier fixture wrote it through the fixed code and could not see this.
- **A misspelled key in the `tls:` block now stops startup.** `mdoe:` or
  `Mode:` (keys are case-sensitive) used to be ignored, so `mode` fell back to
  `off`: plain HTTP and session cookies without `Secure`, logged nowhere on a
  loopback bind. The error names the key, its line, the case-correct spelling
  when there is one, and the valid keys. Every key the block has ever had is
  still valid; the rest of `config.yaml` still ignores unknown keys.
- **The CSRF token is bound to the session.** It was a random value checked
  only against the `polyemesis_csrf` cookie, so anything able to write a cookie
  for the host (a sibling subdomain, a plaintext hop) could plant
  `polyemesis_csrf=x` ahead of the real one, send `x` in the header, and pass.
  The token is now an HMAC of the session token under the server key and is
  checked against the session; the cookie is only how the console learns it.
  Browsers signed in across the upgrade are re-sent the bound value on their
  next request instead of being locked out of writes. Sessions now also carry a
  random ID, so two logins in the same second are distinct sessions.
- **`install.sh` installs one pinned FFmpeg build, checked against a hash in
  the script.** The static FFmpeg it offers came from BtbN's rolling `latest`
  release, which changes daily, and was checked against the
  `checksums.sha256` in that same release. Two installs a day apart got
  different builds, and anyone able to replace the tarball could replace its
  checksum too; the result was then run as root. The installer now downloads
  one dated release (`FFMPEG_BTBN_TAG`) and refuses any file whose sha256 is
  not the one written in `install.sh`. A test rejects `latest`, an undated tag
  or a missing hash.
- **Release binaries and images now carry signed build provenance.** The only
  integrity check a release offered was `SHA256SUMS`, which is published by the
  same release as the binaries, so a replaced binary could come with a
  replaced checksum. The release workflow now attests every file in
  `SHA256SUMS` and each image digest through GitHub's Sigstore-backed
  attestations. `gh attestation verify <file> --repo rainmanjam/polyemesis`
  then fails for anything the release workflow did not build. docs/INSTALL.md
  has the commands. The installer and in-app upgrade still check
  `SHA256SUMS` only.

### CI

- **The release workflow rehearses itself every week.** The GPU images, the
  arm64 image and the SBOM are built only by `release.yml`, which ran only
  when someone pushed a tag, so drift in any of them first showed up as a
  failed release (v0.7.0, v0.8.0 and v0.9.0 all failed on their first tag
  run). `release.yml` now also runs as a dry run every Tuesday on `main` and
  publishes nothing. `scripts/test-release-gates.sh` checks that `PUBLISH`
  is false for a scheduled run.
- **A release needs `security.yml` green on `main` as well as `ci.yml`.**
  `ci-gate` asked only whether `ci.yml` passed for the tagged commit. Branch
  protection is not strict, so a commit whose `security.yml` run (gitleaks,
  govulncheck, npm-audit, semgrep) was red on `main` could still be tagged and
  published. The gate now requires a successful push-to-`main` run of both
  workflows and names each one that is missing.
- **The workflow linter checks shell again.** The required `workflow lint`
  job ran `actionlint -shellcheck=`, which turned shellcheck off for every
  `run:` block. Nine real findings are now fixed, among them `sha256sum *`
  with no `--` in the release checksums step, and a `sudo wc -l <` whose
  redirect was never read as root. The seven PowerShell steps that shellcheck
  was parsing as bash now declare `shell: pwsh`. With both done the flag is
  removed, and a test keeps it off. shellcheck itself is pinned (0.11.0, by
  checksum) rather than taken from the runner image, whose 0.9.0 reports
  findings 0.11.0 does not, so the verdict no longer depends on the image.

### Changed

- **The published images are built from pinned base images.** Every `FROM`
  in `Dockerfile`, `Dockerfile.cuda` and `Dockerfile.vaapi` used a floating
  tag (`alpine:3.24`, `node:24-alpine`, `golang:1.27-alpine`, `ubuntu:26.04`,
  `nvidia/cuda:…`). Two builds of the same commit could therefore start from
  different bytes, and whatever the registry served on release day went in
  unreviewed. Each is now `tag@sha256:…`. Base-image security fixes now arrive
  as Dependabot digest-bump PRs instead of on every rebuild, and a test fails
  any `FROM` without a digest.

## [0.10.0] — 2026-09-23

### Added

- **Twenty-one capabilities the server had and no operator could reach.** A
  sweep asked one question of every settings leaf, route and capability: can an
  operator actually get at this. 113 of 153 leaves had a control. The rest are
  reachable now — and four controls that were already reachable were repaired,
  because they did not do what they said.

  The worst was failover, which was a one-way door: `POST /failover/source`
  worked and had been live since the failover tier shipped, and no screen called
  it. An operator could pin a source and not hand it back.

  The cause is one mistake and it is instructive. `settings_drift_test.go`
  asserted that every settings field is *named* in `types.ts`, and its own header
  already said why that is not enough — "nameable is not reachable. A field
  present in types.ts still needs a control somewhere. It matches anywhere in the
  file." It documented the inadequacy it shipped. Eight findings trace to it,
  three of them passing because the name matched on an unrelated interface:
  `sourceId` on `Destination` standing in for `playout.sourceId`, `encoder` and
  `preset` on `Rendition` standing in for the slate's.

- **Every claim on the features page now carries a receipt.** The site said an
  operator could hand a pinned source back with auto on 2026-08-16. The console
  gained that ability on 2026-09-09 — twenty-four days during which the site
  advertised a capability nobody could reach, with nothing red anywhere, because
  no artefact tied the site's *claims* to the product.

  Each section now names a `provenBy` wrapper in `api.ts`, and the guard asserts
  the wrapper exists **and** is called from outside `api.ts`. The second half is
  the one that matters: in the failover case the route existed, was live, and had
  a wrapper — what was missing was a screen reaching it.

- Automatic moderation on the features page, with `docs/AUTOMOD.md`.

- `docs/GITNEXUS-IMPACT.md` — what the code-intelligence graph does not record,
  and why a zero from it is not an answer. `docs/BRANCH-ARCHIVE.md` — which
  mechanism preserves each deleted branch. Both withheld from the site.

### Changed

- **The design system is executed rather than declared.** The type scale existed
  and nothing used it: `index.css` declared six steps and wired them into
  Tailwind, the component kit reached past them for arbitrary sizes, and
  `index.css` itself set `body` to a literal 13px — a seventh step, which is
  precisely what the spec says happens when a seventh gets chosen by accident.

  Every hardcoded size now names a step. This is a large and deliberate visible
  change, because everything inherits the kit: body 13→12px, page titles 15→28px,
  card titles 13→18px. One palette and one type scale, shared between the app and
  the site through a single token source, with a drift guard over both.

- Every screenshot recaptured against the design system, and the site's
  screenshots — which had shipped eight days older than the docs — resynchronised.

### Fixed

- **A preview start whose hub vanished kept its relay port.** Every other return
  below the allocation in `startPreviewLocked` released the port; this one did
  not. Reachability is narrow: `previewFlowing` a few lines above already returns
  false when `downstreamHub()` is nil, so reaching it needs the hub to disappear
  between the two calls — which a failover can do and a single-threaded test
  cannot.

  Worth recording how it was confirmed. The fix was argued from symmetry and
  shipped explicitly unmeasured, after a first test was deleted for passing
  against the unfixed code too. It was measured afterwards, and it was a real
  leak.

- **A screenshot had been ageing for three weeks behind a guard that was working
  exactly as written.** `08-mix-matrix.png` skipped its check whenever no
  destination on the capture install used the matrix, and none did. A destination
  now carries a matrix profile, so the shot is taken rather than skipped: 19
  capture tests, 0 skipped. Two captions were wrong and are corrected — the
  matrix grid runs the opposite way to what its caption described, and no
  filtergraph appears in that shot at all.

- The transcription workflow picked the newest whisper.cpp release rather than
  the newest release *carrying the binary*, so an upstream source-only release
  broke it. It now selects on the asset.

- **A competitor's price was wrong on the live site, and nothing in the
  repository could have said so.** Aitum renamed its paid suite ("Aitum App" ->
  Nexus) and moved its price ($4.99/mo -> $5.49/mo) inside 32 days; both stale
  figures were published on `/how-to-multistream-from-obs` and
  `/vs/aitum-multistream`. Re-reading all eight vendors against their own pages
  confirmed everything else the site leans on, Restream's full tier table
  included — monthly and annual, every figure exact.

### Security

- The `lodash-es` advisory carried in by mermaid 12's new dependency.
- A nested `sharp` advisory that dependabot structurally could not reach, because
  the parent pins it — cleared with an `overrides` dedupe rather than by waiting.
- Two advisories published between green runs.

### Testing

- vitest 5 across both workspaces as a single commit, because
  `@vitest/coverage-v8` declares a hard peer on the exact vitest version and the
  two cannot move separately.
- Test ports are drawn below the OS dynamic range, so a port a test lends cannot
  be re-lent underneath it by the OS.
- The invariant that two unguarded `Subscribe` calls rest on is now pinned.
- react and react-dom move as one bump, guarded by a check that compares
  major *and* minor — these install happily mismatched and fail only at typecheck.

### CI

- `workflow lint` and the `web/` build now block a merge instead of only
  reporting. Both ran on every pull request and neither could stop one: the job
  that catches a broken workflow file was advisory about workflow files, and
  `npm run build` — `astro check && astro build && scripts/check-build.mjs`, the
  only CI `web/` has — was red-but-mergeable. The site build needed a
  `changes`/shim partition first, because a workflow-level `paths:` filter skips
  the whole workflow and an absent check can never be satisfied.

- Competitor claims now have a device instead of a convention. Three pages named
  `docs/evidence/competitor-facts-<date>.md` as "the only sanctioned source" and
  nothing in CI referenced that file; one page's header even stated the rule
  — "every price carries a visible checked date" — while four of its five
  siblings ignored it. `check-build.mjs` now requires every page sourcing that
  file to **render** a date the file actually claims, read from the built page
  rather than the source: two pages had a `const CHECKED` they never put on
  screen, which is a stamp only the author can see. A weekly `claim freshness`
  job fails when the newest check passes 90 days — a schedule and not a test,
  because a time-based assertion in the build turns red on a morning nobody
  touched the repository.

## [0.9.0] — 2026-09-06

### Security

- **A destination could be linked to an account belonging to a different
  platform, so a YouTube token was sent to Facebook.** Nothing compared a
  destination's platform to its account's, and the pre-announce path takes the
  broadcaster from the destination and the bearer token from the account — so
  the pairing was expressible and, once written, silently sent credentials to
  the wrong host.

  It is refused now on every route that writes a destination, and refused by a
  test rather than by a request to remember. The first version of this fix
  repaired two routes and left a sentence saying any new one must call the same
  helper; two routes were already skipping it, and one of them — the Facebook
  stream-key refresh — saved an unrepaired row every time an operator pressed
  Refresh. All four writers repair, and a build-time guard fails on a fifth that
  does not. It also refuses to pass over fewer than four write sites, because a
  walk that matches nothing would otherwise report success.
- **A token refresh could overwrite a reconnect, and the account would then ask
  to be reconnected for ever.** The refresh re-read under a lock keyed by
  account id and then wrote back the whole row, while the consent callback and
  the device-flow path held no lock and keyed by platform and account ref. A
  refresh begun before consent finished put the pre-consent scopes back over
  the ones consent had just granted, so the install kept saying "reconnect this
  account" — and reconnecting lost the same race again. Writes are now narrowed
  to the token columns with a compare-and-swap, so a concurrent write is
  detected instead of clobbered.

- **A destination's stream key was recoverable from a WebSocket opened with a
  read-scoped token.** `eventView` decided what a read socket may see by looking
  each event type up in a policy table — and both of its unknown cases passed
  the event through untouched. An event type this build did not classify, or a
  policy constant added without a case, sent the admin shape of the payload.
  Both now fail closed. That change also caused, and caught, a regression worth
  recording: making the default fail closed silently dropped **every** ordinary
  event — Log, Status, Levels, Stats, Loudness — because `wsPassthrough` is
  `iota`, the zero value, and had always reached the sender through the `default`
  arm being replaced. The general hazard is now a comment at the case: *the arm
  being replaced has to be enumerated first, and a zero-valued constant is the
  member most likely to be sitting in it unnoticed.* (#716)
- **A password change deleted every API token and left their sockets
  streaming.** Revocation had two halves — delete the row, and mark the id in an
  in-process set the `/ws` tick consults. `handleRevokeAPIToken` did both;
  `handleChangePassword`, which calls `DeleteAllAPITokens`, marked nothing, so a
  socket opened with an admin token kept receiving admin-shaped events until it
  or the process died, while the response correctly reported zero surviving
  tokens. The set is gone: the tick asks the store, the way the session half
  beside it already asks for `users.token_epoch`, and fails closed on a read
  error. Every deletion path — that handler, the password change, `resetadmin`,
  and whatever is added next — is now honoured without being told to be. (#706)
- **`requireScope` passed a request carrying no principal.** Its siblings
  `requireSession` and `requireCSRF` both fail closed on the same condition.
  Every route group carrying it also carries `requireAuth` today, so no shipped
  behaviour changes — what is gone is the affordance of mounting them apart or
  in the wrong order and losing scope enforcement silently. (#710)
- **A hook secret that would not decrypt degraded to an unsigned delivery.**
  Restore a backup taken on another machine, or rotate `secret.key`, and the
  row loaded with an empty secret — and the dispatcher skips the signature
  header on an empty secret, so every webhook went out **unsigned**. At the far
  end that is indistinguishable from a forgery. `Hook.SecretUnreadable` is now
  the same device `Destination.KeyUnreadable` already was: the row still loads
  intact, and three places refuse to deliver it. (#715)
- **An unknown destination kind was written outside the data directory.**
  `db.DestKind` and `ffmpeg.DestKind` are declared independently and joined by a
  conversion that compiles for any string, so a fifth kind reached a `default`
  that answered "this does not write a file" — and without the confinement an
  audio file target is written relative to the process working directory. The
  default now fails closed and treats an unknown kind as a file writer. (#712)
- **The Windows ACL builder stored SIDs as unpinned integers.**
  `TrusteeValueFromSID` converts a `*windows.SID` to a `uintptr`, and `x/sys`
  documents that the caller must pin it for the lifetime of the trustee. Both
  SIDs were Go-allocated and collectable while the `EXPLICIT_ACCESS` outlived
  the call. The consequence was a wrong DACL, on the files holding every stream
  key. (#705)
- **The security gate's 32 open findings are closed** — two fixed and six
  suppressed with guards, each verdict checked against the code rather than
  taken from the rule id. `web/Dockerfile` installed with npm lifecycle scripts
  enabled while CI passed `--ignore-scripts` everywhere else, so the hardening
  stopped at the edge of the container; and `contents: read` sat at workflow
  level, granting it to every job whether or not the job reads the repository.
  Both workflows now start from `permissions: {}` with each job declaring its
  own, so a job added later that forgets gets nothing and fails at checkout
  rather than inheriting quietly. (#681, #690)

- **The server no longer listens on every interface in the clear by default.**
  With no `config.yaml` and no `--addr`, the default is now `127.0.0.1:8080`.
  An explicit address still binds where it is told, with the existing warning.
  Every shipped launch path — the systemd unit, `install.sh`, all three
  Dockerfiles and `config.example.yaml` — passes an address explicitly and is
  unaffected.
- **Pull-source URLs go through the same address guard as webhooks.** They were
  scheme-checked only, so a pull of `http://169.254.169.254/` was dialled.
  Loopback and link-local are now refused. **Cost:** pulling from a service on
  the same box is refused with no per-source opt-in yet; LAN and RFC1918
  sources are still allowed. **Bound, and stated in code:** literal addresses
  only — FFmpeg resolves its own names, so a hostname pointing at a private
  address still passes.
- **HTTP and HLS pulls carry `-protocol_whitelist`** without `file`. Defence in
  depth: measured on FFmpeg 9.0.1, the local-file read the audit suspected did
  **not** reproduce — the refusal was already FFmpeg's default. This makes it
  ours rather than a dependency's.
- **Changing the admin password can revoke API tokens, and always says what
  survived.** The password change killed sessions and left `api_tokens`
  untouched, so a leaked admin token outlived the operator's incident-response
  gesture. Revocation is opt-in because a password change cannot tell a routine
  rotation from a compromise; the disclosure is unconditional.
- **`POST /setup` is throttled** per address, on its own counter, so first boot
  is no longer an unlimited race.

### Fixed

- **The YouTube setup guide told operators to pick the option that breaks every
  week.** Both copies of it ended a step with "You do not need to publish the
  app." Google issues a refresh token that expires after seven days to any
  external app left in *Testing* — unless the only permissions it asks for are
  name, email and profile, and polyemesis asks for more than that. So an install
  that followed the guide lost its YouTube connection weekly, nothing said why,
  and reconnecting restarted the same clock. The guide now says to set the
  publishing status to *In production*, and says the thing that makes that
  advice usable: an app only you sign in to does not need to pass Google's
  verification review. Verification is what Google asks for before *other
  people* can grant it access.
- **What YouTube chat costs is now written down.** It is the only platform here
  that is metered — Twitch is a socket and Kick posts webhooks, both free — and
  the allowance is per Google Cloud project rather than per channel, which is
  why each install needs its own. Neither fact was anywhere an operator would
  find it.
- **A scheduled stop could fire against every destination on the install.**
  `[null]` in a schedule's `destinationIds` decodes to `[0]`, and the list was
  normalised — dropping every id at or below zero — *before* it was validated,
  where an empty list means all of them. So a stop schedule that named three
  destinations ran unattended against every YouTube broadcast on the box, and
  stopping a YouTube broadcast completes it permanently. A list that empties in
  normalisation is refused now.
- **Retrying a failed job could answer 500 with the name of a database index,
  and rescheduling one could lose the outcome entirely.** A new unique index
  stops the same target being queued twice — but three writers move a row into
  the index's reach, and only one of them had been given the refusal to match.
  Retry answers 409 naming the job in the way. Reschedule and requeue carry the
  same branch, which matters more than it sounds: nothing returns their error to
  a person, so the failure there was a job stuck cancelled for ever with a
  worker's result thrown away and nothing on screen.
- **Two guards had been passing for the wrong reason.** The previous-release
  schema test compared column *names* only, so a migration whose DEFAULT
  diverged from `schema.sql` was invisible — and the divergence that motivates
  it, a `scope_ver` that never triggers re-consent, would have reached upgraded
  installs alone. And its own positive control had a floor of four against a
  measured fourteen, so a pattern change that dropped ten of them still passed.
  Both compare what they claim to compare now.
- **A rendition column dropped from one of four hand-written lists was stored
  nowhere, and the round-trip test did not notice.** Removing a column from the
  update statement left the package green, because the test compared nine fields
  by hand. An operator set a pad colour, was told it was saved, and it was not.
  The write lists come from one declaration now, and the round trip fills every
  field by reflection so the next column added is covered without anyone
  remembering to add it.
- **Two settings saved at once could leave an engine holding a value nobody
  saved.** Routing every install-wide setting through a single push meant each
  setter re-applied all of them, so one that read the block before a sibling's
  write landed rolled that sibling back — silently, with the manager still
  reporting it set. It is a lost update rather than a data race, which is why
  the race detector never saw it. A save is serialised end to end now and
  publishes one consistent snapshot.
- **The webhook dispatcher could be read while it was being replaced.** Setting
  it took the engine's lock and three of the four places that read it did not —
  including the loop that decides, several times a minute, whether a programme
  needs observing at all. Both halves are older than this release; what changed
  is that install-wide settings are now pushed to *running* engines rather than
  only to ones being created, so the write lands beside a live reader often
  enough to matter. Every read goes through the accessor that holds the lock,
  and the publish loop reads once per batch rather than once per event — a
  replacement landing mid-loop would otherwise deliver part of one observation
  to the old dispatcher and part to the new, which is a split nobody could
  reconstruct from a delivery log.
- **An archive could be written at a quality that destroys the master.** The
  recording name and the codec family were validated and the quality was not, so
  any positive number reached the encoder verbatim. A CRF in the high thirties
  encodes cleanly, copies every audio track bit-for-bit and shrinks enormously —
  so verification, which compares containers, streams and durations and has no
  opinion about the picture, passed it and the replace renamed a smeared copy
  over a bit-exact original. It is bounded now, and the bound says why it is
  that number. Note on upgrade: a job already queued past the bound will fail on
  its next attempt, with an error that names the bound.
- **A second `Start` on the SRT listener reported the first one's success.**
  Listeners accumulated across calls, so on a second start every bind failed
  against the server's own sockets while the success check saw the previous
  call's listeners — spawning a duplicate serve loop per listener and reporting
  working address families as down, with nothing explaining why. A failed start
  leaves no residue now, and a second one is refused.
- **The crash backstop's ordering was enforced by a comment.** It has to run
  before the first child of any kind, because Windows fixes job membership at
  process creation and cannot apply it afterwards — so a probe added above it
  would leave a process outside the job, surviving a crash as an orphan holding
  a port. Nothing failed when the call was moved, and because the Unix side is a
  no-op it showed up on nobody's machine. The ordering is structural now and a
  guard refuses a spawn site that precedes it.
- **Disconnecting a platform account untied live destinations without saying
  so.** The delete succeeded, the foreign key quietly nulled the destination
  rows, the lifecycle coordinator stopped tracking them, and a broadcast still
  running was never completed. Disconnect now names the destinations it will
  affect and asks first.
- **Width and height were interchangeable numbers in the filter builder.**
  Transposing them at either of two call sites compiled and passed the whole
  package, because the assertions named no pixel count and the fixtures could
  not see a transposition. What that produces is not a crash: it is a logo
  scaled off the wrong edge, or a caption past the bottom of the frame, on air.
  They travel as one value now, so the transposition does not build.

- **A raised YouTube API quota was never paced against, and in two of the three
  places it failed it failed silently.** `YouTubeConfig.QuotaUnits` existed and
  its own comment invited operators who had been granted more to say so — and
  nothing set it, so an install granted a larger allowance after a YouTube API
  Services audit polled chat at the default ten thousand units, roughly a
  hundred times slower than it was entitled to. The allowance and the send
  reserve are now settings, validated, and pushed into a chat connection that is
  already up, so the change takes effect on save rather than at the next
  restart. That second half was found by the hot-reload table: every settings
  leaf has to say what happens when it changes mid-stream, and the only honest
  answer for a value read once at adapter construction was "nothing until you
  restart".

  Three more failures were found by auditing that fix rather than by anyone
  hitting them. `budget` stored a `paused` flag beside the numbers that decide
  whether polling should stop, and `setLimits` wrote the new allowance without
  touching it — so an operator whose chat ran out at four in the afternoon, who
  was granted more, came back and pressed Save, watched nothing happen: the
  status reported `Paused: true` and `Remaining: 990000` in the same breath, and
  only midnight Pacific cleared it. The flag is gone rather than fixed. Paused
  is derived from what is affordable at the moment it is reported, and `pause()`
  pauses by spending the allowance — which is what a `quotaExceeded` from
  YouTube actually means. A summary recomputed from the numbers it summarises
  has nothing to desynchronise from.

  Underneath it, `clampQuota` had been written to be the one spelling of an
  acceptable allowance and a second rule was left upstream of it in
  `NewYouTube`, so the stored pair `(10000, 0)` — what every settings row
  written before the reserve field existed holds — meant a 200-unit send reserve
  at boot and no reserve at all after a save. The operator's ability to say "we
  are moving to Twitch" on stream disappeared the first time they opened
  Settings without changing anything. Both rules now live in `clampQuota`.

  The two ways this can come back are closed at compile time rather than by
  care: `QuotaUnits` and `QuotaReserve` are distinct types, so writing the
  allowance and the reserve into `YouTubeConfig` the wrong way round — which the
  clamp would then quietly turn into a 200-unit-a-day budget — does not build;
  and `var _ quotaPacer = (*YouTubeAdapter)(nil)` means renaming `SetQuota` no
  longer builds, vets and passes chat, api and engine on the strength of two
  test fakes.
- **A data race between clip planning and encoder re-detection.**
  `internal/api/clips.go` read `tools.HWEncoders` unlocked while
  `RefreshEncoderCapabilities` rewrote it — a read the neighbouring
  `renditions.go` documents as unsafe and avoids.
- **`/api/v1/health` reports something that can be false.** It was a constant
  three mechanisms treated as proof. It now reads a real database page, checks
  the engine, and reports the recording floor. A halted recorder is `degraded`
  with 200, not 503: a restart adds no disk and would drop live video.
  **Behaviour change:** an install with sources and no engine now fails its
  container HEALTHCHECK where it used to pass.
- **A restored data directory with no `secret.key` no longer mints one
  silently.** Boot now names how many destinations cannot be read and why.
- **The Start/Stop flip guard never lifted itself** — it armed for elapsed
  rather than remaining time, and its test was green only because fake timers
  advanced the clock before React flushed.
- **A held stop is no longer discarded on unmount**; leaving the page now sends
  the stop it owed.
- **The one confirmation dialog fronting every destructive action was
  untranslated.** Four English literals `i18n.test.ts` structurally could not
  see are now keys in all 15 catalogues.
- **Clips and Recordings claimed "none" for reads that failed** — the exact
  bug `lib/readState.ts` exists to forbid.
- **Playout link rotation and clip-history purge now confirm before acting**,
  naming the viewers dropped and the files deleted.
- **`update.sh` refuses to restart a container that is publishing**, matching
  the guard `uninstall.sh` already had. Docker installs also gained log
  rotation.
- **The generated helper scripts no longer splice an empty compose command into
  command position** (#658), and backticks in a generated comment no longer
  execute at write time.

- **Windows CI aborted the Go runtime, fifteen times in 1,607 job runs.** Traced
  to a mechanism rather than patched at the symptom: 21 engine methods refused a
  nil receiver by *dereferencing* it, which on amd64 is a hardware nil check
  rather than a Go panic — and on Windows those are not equivalent. A hardware
  nil check raises a real `EXCEPTION_ACCESS_VIOLATION`, and Go's recovery from
  one writes below the goroutine stack into the adjacent heap span
  (golang/go#81238, open). Every one of the 21 now refuses with an explicit
  check, which is the upstream reporter's own negative control. (#440)
- **A destination starved by the relay reported itself healthy.** The failure
  dump said only that the sink recorded no bytes, which is compatible with every
  fault a sender can have and pointed squarely at the sink, which was innocent —
  three plausible fixes were verified against real runs and all three were
  wrong. The destination's own stderr had named it in one line the whole time,
  in `data/logs/process.log`. (#674, #675)
- **Three sidecars could be published into an engine that had already
  stopped**, leaving a child nothing in the program could signal, report or
  count — the exact shape of an FFmpeg found on a production host with its ppid
  pointing at the live server and no escalation anywhere in the log. A
  process census now counts what the operating system counts rather than what
  the program still holds a handle to. (#631)
- **Stopping an engine leaked a relay port and a hub subscription per auxiliary
  child.** The pool is 500 ports shared across every engine and `Manager.Sync`
  stops one on every source delete, so each delete burned three ports plus one
  per rendition, permanently, until `Allocate` began failing everywhere at once
  and read as an unrelated fault. Measured before the fix. Releasing a port now
  also requires holding it, so a stale integer cannot hand back a port the pool
  has since reassigned to another engine. (#707, #708)
- **A relay subscriber name collision silently replaced one consumer.** The hub
  registered by bare map assignment, so a second consumer under an existing name
  replaced the first — which kept running, kept a correct command line, kept a
  green card, and received nothing. The hub logged *"relay subscriber added"*
  either way, so the log positively confirmed the wrong thing. An occupied name
  is now refused, and `Unsubscribe` no longer reports removing something it did
  not have. (#711)
- **Twelve mutation handlers reported success for a reconcile that failed.** Two
  spellings lived side by side in one package — three turned the error into a
  500, twelve logged it and returned 200 — and nothing in the signature said
  which was right. `Engine.Reconcile` is event-driven with no ticker, so a
  failure is never retried: stored state and the running FFmpeg diverged until
  the next successful mutation or a restart, while the console showed a green
  toast. The worst case was invisible — a destination delete returned
  `{"status":"deleted"}`, the row left the list, and the child kept publishing.
  There is now one spelling, the response carries what did not happen, and the
  console raises the warning from the transport rather than at fifteen call
  sites. (#709)
- **The console reported a failed read as an empty result, and the empty result
  gave advice.** A failed `listRenditions()` rendered *"No shared encodes yet.
  Create one on the Renditions page first"* and a failed `listAccounts()`
  replaced the account picker with **Connect account** — sending an operator to
  build an encode they already had, or to re-authorise an account already
  linked, discarding a half-filled dialog on the way. Nine further instances
  were found by the rule that replaced the old hand-maintained filename list,
  one of them a real bug: the metadata composer hides itself on `null` but its
  catch stored `[]`, so a failed read rendered it with zero platforms. (#719)
- **`kill()` was the one of three signal sites without a reaped-check**, so it
  could signal a process group by number after the pid had been reaped and
  possibly reissued. (#720)
- **`stopAux` took a slot and a name that had to correspond**, with nothing
  linking them: `e.stopAux(&e.preview, "recorder")` compiled and read correctly
  while releasing the wrong port. One value per consumer now. (#714)
- **An interrupted header read arrived as an unrecognisable exec failure.**
  `ProbeFile`'s count branch folded `ctx.Err()` into its error and explained at
  length why it had to; the header read did not, while its own comment claimed
  the property. The caller could not tell an interrupted probe from a file
  ffprobe disliked — and one of those must not delete the operator's upload.
  (#696)
- **Backup ingest could only be requested when the broadcast was created.**
  `enable_backup_ingest` is a create parameter, so an operator turning the
  toggle on afterwards had one route: press **Refresh key**, which starts a
  fresh Facebook broadcast and discards the one they were configured against,
  with its comment thread and its title. The ingest is now added to the existing
  broadcast. (#727)
- **Two sort calls with no comparator.** `Array.prototype.sort` has none by
  default and compares UTF-16 code units, so the monitoring page's process
  filter put every capital before every lowercase — `Recorder` above
  `dest:studio-a`, regardless of the alphabet. The jobs page's weekday sort was
  the same call on numbers and happens to be harmless at 0–6; it is fixed
  anyway, because the fault is latent rather than absent.
- **The clip editor's shuttle badge decided nothing** — both branches of its
  conditional rendered the same string, so reverse read `-2x` from the negative
  number while forward read a bare `2x`. Shuttle runs both ways and direction is
  what the badge is for.
- **The clip editor's transcript lines were reachable by pointer only.** Each
  seeks the player on click and had no keyboard route at all, on the fastest way
  to navigate a long recording.

- **A `--config` path that did not exist booted a different, empty install.**
  `config.Load` returned defaults on a missing file, which is right for the
  implicit `config.yaml` and wrong for a path the operator typed: a typo
  created `./data`, minted a **new `secret.key`**, opened an empty database,
  bound `:8080` in the clear and reopened unauthenticated `POST /setup` — the
  window `-reset-admin` exists to close — while looking healthy. An explicit
  `--config` that is absent now refuses to start, naming the path; the
  implicit default still defaults. (#644)
- **Login throttling could be bypassed behind the reverse proxy the docs tell
  you to deploy.** `deploy/nginx.conf.example` shipped
  `$proxy_add_x_forwarded_for`, which appends to whatever the client sent, and
  the server read the *leftmost* `X-Forwarded-For` hop — the client's own bytes.
  Rotating that header minted a fresh throttle key per request, so the
  5-attempt policy never fired: unlimited online guessing at the single admin
  password, with attacker-chosen addresses in the audit log. The server now
  reads the **rightmost** hop, the one the proxy appended, which is correct
  whether a proxy appends or overwrites; the shipped nginx example overwrites.
  A chain of several trusted proxies now keys everyone behind the last one
  together, which throttles too much rather than too little. (#647)
- **The documentation-drift guards never ran on the pull requests that drift
  the documentation.** The guards are Go tests — `platforms_doc_drift_test.go`
  reads `docs/PLATFORMS.md`, `api_docs_route_table_test.go` reads
  `docs/API.md` — and every Go step in CI is gated on the change touching
  code, so a documentation-only pull request ran none of them and reported
  green as "did no work". That is how `docs/UPGRADING.md` came to tell an
  operator, mid-migration, that a CI-tested feature does not work, and how
  `docs/MODULES.md` came to name a base image and a Go version no Dockerfile
  uses. The docs-only path now discovers the packages whose tests read
  `docs/*.md` and runs them; it costs seconds, because those tests read
  markdown and need no ffmpeg, database or network. It also counts what ran
  and fails on a low count, since `go test -run` matching nothing exits 0.
  (#651)
- **Shutdown had no single deadline, so a stop could outlast the one systemd
  waits for.** `TimeoutStopSec=45` sat above four budgets chosen separately and
  added together by the order they ran in: 20s for the HTTP servers, 5s for the
  lifecycle drain, then **30 seconds per engine, stopped one after another**,
  plus a captioner wait that took no context at all. On a two-programme install
  one wedged child passed 45s without anything in the process believing it had
  overrun; systemd then SIGKILLed the cgroup, and a recorder killed mid-write
  leaves a Matroska file with no trailer at exactly the size a reader would
  call plausible. The process now has one budget
  (`engine.ShutdownBudget`, 35s) that every phase draws from, engines are
  stopped concurrently so sharing a deadline does not mean dividing it, and
  running out of it is logged rather than left to systemd to reveal. A test
  pins the budget below the `TimeoutStopSec` in both the shipped unit and the
  one `install.sh` writes. (#645)
- **The console resolved its programme once per page load.** `/sources` was
  read in an effect that never re-ran, so creating a second source during
  first-run setup — or deleting the one being followed — left the whole
  console pointed at a stale answer until someone reloaded: Meters read "NOT
  UPDATING", Monitoring's process list died, Clips showed "No clips yet.", all
  against a healthy server. Every one of those is a plausible idle state,
  which is why it was never reported as a bug. The Sources page now asks for a
  re-resolve after a create or delete, and the provider re-resolves on its own
  when the status socket names a programme it has not seen — which covers the
  changes made in another tab, by another operator, or straight through the
  API. An operator already on a programme that still exists stays on it.
  (#646)
- **`install.sh --tls acme` uninstalled the working server it had just
  installed.** `verify()` probed `https://127.0.0.1:PORT` with `-k` in every
  TLS mode. That connection carries no SNI, and the ACME path sets only
  `GetCertificate` — no `Certificates` fallback, unlike selfsigned, whose leaf
  carries `127.0.0.1` — so autocert had no name to look up and the handshake
  aborted before any HTTP happened. `-k` skips *verifying* a certificate; it
  cannot invent one. `verify()` then returned 1 before `INSTALL_COMPLETE` was
  set, and the EXIT trap disabled the unit, removed the binary,
  `/etc/polyemesis`, `/opt/polyemesis` and the service account, then printed
  that nothing was left running. In acme mode the installer now asks the
  question that has an answer on loopback — the `:80` redirect listener acme
  mode binds — and the failure trap refuses to remove a service that is
  `active`, saying so and pointing at `uninstall.sh`. (#642)
- **`update.sh` backed up a live database and never checked the copy opened.**
  Both generated scripts copied the data directory while the server was still
  running — `cp -a` for binary installs, `docker run … tar czf` for compose —
  and stopped the service afterwards. The guard checked that `polyemesis.db`
  and `secret.key` *existed*, then printed "backup verified". Migrations run
  forward only, so that copy is the only way back from an upgrade, and every
  way it can exist without being usable leaves a file of plausible size. Both
  scripts now stop the service **before** copying, and the copy is opened,
  walked with `integrity_check` and checked for this server's schema by the
  installed binary itself (`polyemesis -verify-backup`), which runs no
  migration. The binary install also keeps the running executable as
  `polyemesis.previous`, since the rollback instructions said "reinstall the
  previous binary" without keeping one. (#643)
- **The console printed credentials as readable text in five places.** The
  Sources page showed the publish token twice — once as `STREAMKEY`, once as
  `TOKEN` — in plain text on the page an operator opens while someone is
  helping them get a broadcast up. 0.8.0 masked the stream key *input*; it did
  not touch these, because they are not inputs. Also fixed: the webhook signing
  secret shown after creating a hook, the API token shown after minting one,
  and the ingest URL on the dashboard and in Settings, which embeds the SRT
  passphrase in cleartext for an admin (the server masks it only for a
  read-scope principal).

  All now render through a new `SecretCode`: masked to a fixed width, with a
  deliberate reveal. **Copy still works while masked** — moving a secret to the
  clipboard never required putting it on the screen. The webhook secret gained
  a Copy button it never had, so reading it off the screen is no longer the
  only way to get it.

  Not masked, deliberately: the RTMP address beside the stream key, and an
  ingest URL with no credential in it. Masking those would train an operator to
  press reveal on everything, which is how a mask stops meaning anything.

  `secret-fields.test.ts` grew the check that would have caught this. It asked
  whether any `<Input>` was bound to a credential — a question no `<code>` block
  can fail. It now also asks whether one is printed into element text, and it
  strips comments first, because the first version failed on the docstring
  explaining the bug.

### Changed

- **`make check` runs what CI runs** — `POLYEMESIS_LEDGER=strict`, `-race`, and
  the Windows vet leg — and its parity guard compares the invocation rather
  than a substring. **Cost:** the local loop goes from about four minutes to
  about twenty-five, and needs a C toolchain.
- **Lint can fail the build.** `react-hooks/exhaustive-deps` and
  `no-unused-vars` are errors; the three violations that surfaced are fixed
  with no suppressions.
- **SonarCloud waits for its quality gate**, `web/`'s lockfile is watched by
  dependabot and audited, every shell script is parsed by `make sh-syntax`, and
  the installer suite refuses to report PASS when a step did not run (#657).
- **The website says what the server actually does.** Platform figures are
  recommendations, not enforced ceilings — and the build now asserts them
  against `internal/db/platforms.go`, so the page cannot drift from the code.
  A stale "YouTube is capped at 3 destinations" warning that fired with no
  YouTube selected is gone.
- **Eight documentation contradictions corrected**, including `UPGRADING.md`
  telling an operator mid-migration that a CI-tested feature does not work, and
  `MODULES.md` naming a base image and Go version no Dockerfile uses.


- **Facebook does have a persistent stream key, and the product said it did
  not.** Eight places — two of them operator-facing copy — stated that Facebook
  issues a fresh key per broadcast and that there is *"no permanent key to
  reuse"*. That is true of the key polyemesis fetches and false of Facebook:
  Live Producer offers a persistent key under *Advanced settings*, reusable
  every time you go live, with one stated limit of one live video at a time.
  Meta's Graph reference documents no way to mint or read it — checked against
  the complete endpoint list and the complete create-parameter table — so
  polyemesis cannot fetch it, but an operator can paste it once and it does not
  go stale. Saying the key did not exist told them not to look for it. (#724)

### Added

- **The meters page says when tracks are not being metered, and which programme
  it is showing.** One metering process merges every track, and amerge refuses
  past 64 channels, so a very wide ingest was measured as a prefix while the
  remaining tracks drew flat bars — indistinguishable from silence, on the one
  page whose whole job is telling those apart. The server had counted the
  dropped tracks since the limit existed and the number reached nothing:
  `ffmpeg.MetersDropped` says in as many words that it exists "so a wide ingest
  degrades visibly", and then no caller carried it out of the package. It now
  rides on the source payload and renders as a warning that says *unmeasured,
  not silent*. Absent when nothing is dropped, which is every install anyone is
  likely to run.

  The page also names its programme when the install has more than one. The
  console follows a single programme, resolved to whichever source is first when
  nothing is remembered, and there is still no switcher — so this does not fix
  multi-source metering, it stops the page reporting a slice of the install as
  though it were all of it. See #638 for the switcher.

- **A programme switcher.** `rememberProgramme` had existed since #606 and was
  called from exactly one place, with the value the resolver had just picked —
  so the persistence worked perfectly and only ever persisted the fallback. No
  screen offered a way to say otherwise, and an operator running a horizontal
  and a vertical programme watched one of them for the life of the install while
  the other ran unwatched. (#638)
- **The platform warning, where the rendition is actually chosen.** The
  comparison shipped earlier as a log line at stream start: correct, and
  unreachable. Attaching a 4K60 rendition at 40 Mbps to an X destination showed
  nothing in the console and the operator found out from a log after committing.
  One implementation, deliberately — the browser asks and does not derive,
  because a second copy of a comparison whose whole value is researched, dated
  figures in one committed file would drift the same way hand-copied numbers
  always do. (#661)
- **A hand-pasted Facebook key can use the broadcast its account is running.**
  A key polyemesis fetched carries the live-video id inside it; a persistent key
  pasted from Live Producer does not, so chat, metadata and *End broadcast* had
  nothing to attach to. Facebook allows a persistent key one live video at a
  time, which is what makes asking the account a fact rather than a guess — and
  where it is not, polyemesis now refuses and names both broadcasts rather than
  editing whichever sorted first. (#725)

### Removed

- **The compact/comfortable layout density toggle**, which sat in the console
  header between the username and the language switcher. It rescaled Tailwind's
  `--spacing` inside `<main>` to pack more onto the screen, and remembered the
  choice per browser.

  Removed whole rather than hidden. The preference was applied on mount from
  `localStorage`, so deleting only the button would have left anyone already
  switched to compact stuck there with nothing to switch back — a setting with
  no control is worse than no setting. Gone with it: the `useDensity` hook, the
  `DENSITY` block in `index.css`, and the `dense-grid` opt-in that gave the
  dashboard a fourth column above 1280px.

  `Button`'s `min-h`/`min-w` floors and its `rem`-spelled icon size stay. They
  were written to stop compact from shrinking a control carrying Start and Stop
  below a reliable target, and at a single density they are no-ops — but a
  minimum target size is worth asserting on its own terms rather than left to
  whatever the spacing scale produces.

### Testing

- **Three suites for the faults a 75-second run cannot see.** `#380` named
  duration, concurrency and mid-stream failure; all three exist now, none is in
  the CI matrix, and each refuses to believe a lie shaped like the result you
  want. The duration suite asserts delivery *first*, because every trend it
  watches reads flat on an idle server — a long run against a broadcast that
  died in its first minute would otherwise report perfect health, at length. The
  concurrency suite asserts liveness before any number, because a first attempt
  by hand measured per-destination cost halving with every doubling: an artefact
  of several FFmpeg readers competing on one socket until all but one died. It
  measures **2.69% of a core for one destination and 3.44% each for six**,
  against a published "roughly 4%" — corroborated, not contradicted. The
  fault-injection suite pairs every injection with a positive control that must
  fire, because a recovery suite whose fault did not land reports a perfect
  recovery with every check green. (#380)
- **A soak that can catch #440**, and a raised default now the runner has been
  measured. (#700, #701)
- **Seventeen build-time guards from a poka-yoke audit.** Each one converts a
  decision somebody would otherwise have to remember into something the build
  refuses: every spawn site is enrolled in the process census or carries a
  stated reason it needs no enrolment; every spawner says whether its children
  get a process group of their own, and the claim is checked against the code;
  every platform either has a routing policy row or an opt-out with a reason;
  the two RTMP switches over one closed type set are compared; and the UI's
  read-state rule is enforced by walking every file rather than by a
  hand-maintained list of three filenames — which is what let a dialog with five
  instances go unnoticed. (#705–#721, #723)
- **Guards are mutation-tested before they are trusted.** Every device above was
  watched failing — the guard removed, the test red, the guard restored — on the
  principle that a guard nobody has watched fail is a guard nobody should trust.
  Three of those runs found the *test* wrong rather than the code: a recovery
  check asserting `running`, which the driver's own header calls meaningless
  because a crash-looping process reports it too; a fixture whose second entry
  let an ambiguity check mask the exclusion under test; and a resolve-failure
  case that used the profile, so it failed one step later than it meant to.

## [0.8.0] — 2026-09-01

### Changed

- **`uptimeSec` now counts from the first media, not from the spawn.** An ingest
  is started *listening*, so the old figure included however long it sat waiting
  for an encoder to connect: arming a source in the morning and going live at
  noon reported four hours of uptime for a stream that had been on air for none.
  A process that is running but has seen no media reports `uptimeSec: 0`, which
  is what it is doing. `startedAt` is unchanged and still reports the spawn.
  **This changes the meaning of a published MQTT field** — see `docs/MQTT.md`.
- **Processes with nothing to flush now get a 1-second shutdown grace instead of
  8.** `meters`, `loudness` and `silence` write nothing a reader will ever look
  at — `-f null` for the first two, an mpegts stream to a UDP relay for the
  third — so waiting eight seconds to kill them cost that long on every ingest
  switch and stop, and the waits stack serially. Every other kind keeps the full
  window: an unlisted kind defaults to 8 seconds, so forgetting to classify a new
  process costs latency and never a truncated recording.

### Added

- **A warning when the ingest's video codec is copied to an RTMP destination that
  may refuse it.** Selecting HEVC or AV1 in OBS produces Enhanced RTMP, which
  polyemesis ingests; video is then stream-copied, so that bitstream reaches the
  platform verbatim. FFmpeg muxes HEVC into FLV happily and the platform drops the
  stream — a failure that looks correct everywhere the operator can see. The
  destination card now says so. It suggests and never refuses: a custom endpoint
  that does accept HEVC is a real setup.
- **A bandwidth calculator on the website**, at `/calculator`. It models a
  stream-copying restreamer rather than multistreaming in general: because video
  is copied and not re-encoded, the video half of the sum is one bitstream
  repeated, and it is bounded by the lowest ceiling among the destinations
  receiving it — so sending 6000 to Twitch and 12000 to YouTube is not a choice
  the operator has without a rendition. It reports the encoder's upstream and the
  server's uplink separately, because those are different connections, and
  converts both into data per stream, week and month, which is what a host bills
  on. Platform ceilings are the server's own figures from `internal/db/platforms.go`,
  dated on the page. Custom RTMP and SRT endpoints can be added by name. A
  verbose mode adds overhead, headroom and audio-track inputs, and gives each
  selected destination its own video mode, rendition bitrate and audio bitrate
  in that destination's own row — the case where one platform's ceiling forces
  a rendition and the others keep the copy.
- **Structured data binding the name, the site and the repository.** "polyemesis"
  is a coined word, which helps ranking and hurts identity: nothing told a search
  or answer engine that the name, this site and the GitHub repository were one
  thing rather than three strings that happen to co-occur. The site now emits a
  `SoftwareApplication` node with `sameAs` pointing at the repository, and a
  `WebSite` node for the site as an entity distinct from the software it
  documents. Both assert only what is already true and independently checkable.
  Deliberately absent, and recorded in the layout as a rule rather than an
  omission: no `FAQPage`, no `ItemList` over the comparison pages, and no
  `dateModified` — structured data detaches a figure from the "checked" date
  beside it on the page, and a competitor's price reproduced in a result card
  without that stamp is a stale claim we published.

- **A teardown counter with a denominator.** The supervisor now counts clean and
  killed teardowns per process kind. Previously only kills were recorded, in a
  log line, and a clean teardown wrote nothing at all — `supervise()` returns on
  context cancellation before reaching the exit log — so there was no ratio to be
  alarmed by and no way to see an exceptional path becoming the normal one.

### Fixed

- **The RTMP ingest stream key was rendered in plain text.** Settings drew it
  with an ordinary text input, so the credential OBS authenticates with was
  readable on screen with nothing marking it as a secret — during exactly the
  activity that puts a screen in front of an audience.
- **Secret fields had no way to reveal what was typed.** The SRT passphrases,
  the MQTT password, the OAuth client secret and the destination stream key were
  masked but had no toggle, so checking a pasted key meant saving and reopening.
  All six now use the existing `SecretInput`, which masks by default and reveals
  only on an explicit press. A test now fails if any input bound to a credential
  is not one, because the component already existed and nothing required it.
- The reveal control is now translated rather than hardcoded English.

## [0.7.0] — 2026-08-28

### Upgrading
- **Building from source now needs Go 1.27.** `go.mod` declares `go 1.27.0`, so
  a 1.26.x toolchain refuses the build outright rather than degrading — a hard
  stop with no release note was the only thing wrong with it. Nothing changes
  for anyone installing a binary or running the container; both ship their own
  toolchain.
- **TLS now serves on :443 rather than :8080 when a certificate is configured.**
  An install reached at `https://host:8080` moves, and a firewall that only
  opens 8080 makes the console unreachable after the upgrade with nothing on
  screen to say why. Open 443, or set the listen address back explicitly.

### Fixed
- **Stopping one destination had no confirmation, and the button could invert
  under the cursor.** It ends a live broadcast that cannot be resumed, and it
  had less protection than either neighbour: bulk stop-all requires a
  server-side confirmation and delete requires typing the name. Stop is now
  held briefly with a visible Undo — a confirmation dialog in front of a
  frequent deliberate action is trained away within a day, after which it costs
  a click and protects nothing. Separately, the button swaps between Start and
  Stop in place on a status repaint that arrives every two seconds, so a click
  aimed at Start could land on Stop; a click is refused for a moment after the
  action changes, and says why. (#506)
- **Clips and the loudness monitor were broken on any multi-programme install.**
  Four routes scoped inside the handler rather than at the router, which a
  survey of the router's middleware could not see. The meters page's "NOT
  UPDATING" was a staleness counter reporting refused reads, not an analyser
  that would not start. (#606)
- **A model spend ceiling was refilled every time settings were saved.** The
  hourly counter lived on an object that `ApplyAutomod` rebuilds on every
  settings save and on the API-key write, so the only bound on model spend was
  reset by the reflex of tweaking a setting mid-incident — which is when
  somebody is least likely to be watching the bill. `CallsThisHour` reset with
  it, so the evidence went at the same moment as the limit. The budget now
  outlives the configuration. (#502)
- **The automod confidence floor was copied without a guard.** A stored `0` —
  which is also what an unset field arrives as — removed the floor so every
  model opinion acted; a stored `80`, from reading the 0..1 scale as a
  percentage, sat above every verdict the model can return and silently retired
  the checker. Neither said which. Values outside the usable range are now
  refused at save time and cannot reach the checker. (#503)
- **One bad automod regex silently disarmed every pattern rule.** (#608)
- **Loudness readings outlived the audio they measured.** (#609)
- **A stream key on a non-RTMP destination was stored, never sent, and
  unreachable.** (#610)
- **A destination could publish while nothing downstream of it was running.**
  The path taken when the ingest probe lands started the destinations and
  reconciled none of the things that consume them — the meters, the recorder,
  the preview, the clip buffer, the captions and the loudness analyser all
  stayed on the layout from before the probe. (#612)
- **Per-programme lanes left a large empty column.** Lanes carry their own
  preview, so the page correctly stops drawing the one at the top — but nothing
  replaced it, leaving roughly four hundred pixels of empty page directly under
  the card an operator looks at first. The side cards now sit beside the ingest
  card instead of stacked, which removes the gap and moves the destination list
  up the page. (#614)
- **Five figures on the destination card were never translatable.** Bitrate,
  Uptime, Restarts, Dropped and Speed were hardcoded English. They now have
  keys, translated in all fourteen locales. (#615)

- **A scheduled broadcast could silently fail to go on air on a multi-programme
  install.** `schedules` carries no `source_id` — a timetable is a property of
  the box — but every engine ran its own `scheduler.Runner` over that one table.
  Whichever swept first wrote `enabled` on every destination, including other
  programmes', then reconciled ONLY ITS OWN engine and marked the occurrence
  handled; the other engines read it as handled and never reconciled. Those
  destinations sat enabled in the database with no process publishing, while the
  log said `schedule fired`. `MarkScheduleRun`'s `WHERE last_run_at < ?` is a
  ratchet on the row, not a lease over the work, and the actuator has no way to
  learn whether it won it. There is now one runner, owned by the manager, whose
  reconcile covers every engine — so the shape that caused this is no longer
  representable rather than merely unlikely. The runs page also stops reporting
  the default programme's scheduler as though it were the only one.
- **The dashboard's grouped destination list and the Prometheus scrape lost
  every programme but one.** Scoping `Engine.Status` to its own source was
  right, and it removed a leak three callers were quietly relying on: the
  status payload, the WebSocket push of the same payload, and `/metrics` all
  read the DEFAULT engine, whose status had happened to carry every row on the
  machine. A multi-source install then showed the selected programme's
  destinations and zero for the others, and stopped emitting series for them.
  The metrics half is the dangerous one — a missing series is indistinguishable
  from a destination nobody configured, so an alert on a dead destination never
  evaluates. The destination list now comes from `Manager.DestinationStatuses`,
  every programme's, each still compiled by the engine that owns it.
- **The RTMP relay stopped answering publish and play once gortmplib reached
  v1.0.1.** That release split `ServerConn.Accept` into `AcceptConn` (the
  connection, and reading the play/publish command) and `AcceptAction` (the
  response that admits it). `Accept` still exists, still compiles, and is now an
  alias for `AcceptConn` alone — so the connection came up and no peer was ever
  answered: subscribers reported `Input/output error`, publishers a refused
  connect, against a healthy relay. Nothing in the type system marked the
  change; the end-to-end tests that run real FFmpeg against a real listener are
  what caught it. The bump is taken together with the migration, because
  merging it alone ships broken RTMP.

<!-- RB-5 (#499): this heading was dated 2026-08-21 in #487, while preparing
the tag -- and the tag was then deliberately held. Nothing noticed, so this
section described a shipped release for four days while every install path
(git, GitHub Releases, Docker Hub) still ended at v0.6.0, lacking the
seal-at-rest fix below. Dating it again is a release act, not a docs edit:
`.github/workflows/release.yml`'s changelog-gate now refuses to publish any
tag whose version does not match the top dated heading here, and
changelog-freshness.yml alarms if a dated heading ever again sits this long
with no matching tag. -->

### Security
- **Alert rules had no SSRF guard, and the test button was a port-scan oracle.**
  A rule's webhook URL was fetched without restriction, so an operator-supplied
  endpoint could reach link-local metadata services and private ranges from
  inside the network the server runs in, and the test button's timing and error
  text distinguished an open port from a closed one. (#607)
- **`golang.org/x/crypto/openpgp` is now pinned out of the build.** The package
  is unmaintained with no upstream fix (GO-2026-5932). It is not reachable from
  this code and the module edge is not removable — `x/crypto/acme` and
  `nacl/secretbox` are both genuinely used — so the package boundary is held
  deliberately by a test rather than left as a property of today's imports.
  (#508)

- **Two sources could share a publish token, and an encoder would land in
  whichever one the lookup happened to return.** `sources.token` had no unique
  constraint and RTMP's target map is last-writer-wins, so a duplicate admitted a
  publisher into the wrong programme — the same class of cross-programme mistake
  this release fixes elsewhere. Now a partial unique index, partial because the
  column defaults to empty and several sources legitimately have no token yet.

  **This can stop an upgrade.** A database that already holds duplicate tokens is
  REFUSED at open, before anything is written, naming the sources by id and name
  (never the token — a startup log is not a place for a publish secret) and
  carrying the exact command to clear one. That is deliberate: de-duplicating
  automatically would mean choosing which source keeps the token, and that choice
  decides which programme an already-publishing encoder lands in — a judgement
  the code has no basis for making at boot. Clear the duplicate, restart, then
  rotate the affected source's token from the UI.

  Duplicates could only arise from a client writing one, which is also fixed:
  the general update path no longer touches the token at all, so a stale browser
  tab can no longer roll a rotation back. Rotation is now the only writer. (#505)
- **A viewer's chat message could put text on a permanent third-party ban
  record.** The moderation model's own prose was passed as the ban reason to
  Twitch, and to Kick — a second sink found only when the fix was written. The
  only thing between the model and the platform was a 1000-rune truncation, so a
  crafted message wrote arbitrary text onto a moderation record under the
  broadcaster's credential, and the operator's system prompt could travel back
  out the same field. `PlatformReason()` is now a pure function of a closed
  `Category`: it takes no argument and reads no free-text field, so there is no
  expression that gets model output to a platform write. The model may not even
  claim the categories only a deterministic checker produces. Fail-open on an
  unrecognised category is kept deliberately — it is this package's tested
  contract, and inverting it was the plausible wrong move. (#495)
- **Webhook targets could reach the network the server sits on.** Validation
  checked scheme and host and stopped, so a hook could be pointed at cloud
  metadata, loopback, or a private range. Now refused at save time with a
  deliberate per-hook opt-in for self-hosted endpoints, plus a dial-time check
  that closes DNS rebinding by dialling the address it just verified. RFC6598
  carrier-NAT space — what Tailscale hands out — is refused too: `net.IP.IsPrivate`
  covers RFC1918 and IPv6 ULA and nothing else. (#489)
- **Editing a track label on one programme rewrote another's ingest.** A 500 ms
  debounce autosaved to a route that carried no source, and the handler resolved
  the default engine, so a keystroke on programme 2 rewrote programme 1 and
  restarted its live destinations. Routes now refuse rather than falling back —
  the fallback is what made it silent — and an AST test requires every caller
  reaching for the default engine to be recorded with a reason, so a new handler
  that forgets fails the build. (#497)
- **0.7.0's seal-at-rest migration left the plaintext stream keys it replaced
  legible in the database file.** The migration writes the ciphertext and blanks
  the `stream_key` column, and every check that reads the row back through SQL
  confirms it is empty. `grep` on the raw file disagrees. SQLite unlinks the
  bytes of a shortened cell without zeroing them — `secure_delete` defaults to
  **0** under `modernc.org/sqlite`, which is the driver this ships with — and it
  writes the sealed rows into the write-ahead log rather than over the old pages,
  so copies survive in both files. Measured on a 60-destination install: 60
  plaintext copies still in `polyemesis.db` after a clean-shutdown upgrade, and
  122 in `polyemesis.db-wal` after an upgrade over a server that had been killed.

  This is the one claim `internal/secrets` makes for itself — "a leaked database
  file … is not a leaked set of live streaming credentials" — and it was false
  for every install that had upgraded. No attacker is needed and nothing has to
  go wrong; it is the state a normal upgrade leaves behind.

  The database is now opened with `secure_delete` on, and the migration
  truncates the write-ahead log once it has committed. Neither half is
  sufficient alone: with the checkpoint but no pragma, two copies survived in
  freed pages; with the pragma but no checkpoint, every copy the migration had
  not yet checkpointed survived in the `-wal`.

  **The pragma only protects writes made after it is set, so an install that
  already ran the 0.7.0 migration is not fixed by upgrading.** It needs a one-off
  `VACUUM` plus `PRAGMA wal_checkpoint(TRUNCATE)` with the server stopped — see
  [UPGRADING](docs/UPGRADING.md#if-you-already-upgraded-to-070-one-off-remediation-required).
  Any backup taken between upgrading and running that scrub still contains the
  plaintext and cannot be repaired; rotate those keys instead of trusting the
  archive.

- **Twitch's negotiated ingest host was never checked, and an omitted
  `authentication` fell back to the operator's own key.** `multitrack.Resolve`
  validated that the returned `url_template` was `rtmp://` or `rtmps://` and
  that it named *a* host — never *which* host. A response carrying
  `rtmps://attacker.example/app/{stream_key}` resolved cleanly, and the engine
  replaces a destination's stored target wholesale with what Resolve returns, so
  nothing downstream reasserted the intended host. The stream key travels in the
  RTMP connect as the stream name, which means the first packet of that publish
  hands the credential over.

  The second half made it worse. `key := streamKey; if ep.Authentication != ""
  { key = ep.Authentication }` reads as a safe default: it means an endpoint
  that mints no key gets the operator's own long-lived channel credential
  instead of the per-broadcast one — while `Outcome.Use` documents the minted key
  as *mandatory*. So the hostile-host case shipped the permanent credential, not
  a disposable one.

  The host is now constrained to `.global-contribute.live-video.net`, the Amazon
  IVS contribute domain all three independently measured ingest hosts sit under,
  and a successful negotiation carrying no minted key is refused rather than
  substituted for.

- **The credential-carrying POST followed redirects.** `multitrack`'s HTTP
  client set no `CheckRedirect`, so Go followed up to ten. Go strips
  `Authorization` and `Cookie` across hosts — but the stream key is in the
  **body**, and 307/308 preserve the method and replay it. Measured: 301/302/303
  became GETs and leaked nothing; 307 and 308 delivered 684 bytes of request
  JSON, stream key included, to a second server. Every redirect is now refused.
  The same policy is reapplied to an injected client, so the test seam cannot
  silently drop it.

- **A stream key straddling the 300-byte error snippet was printed unmasked.**
  The non-2xx path read `scrub(snippet(raw), streamKey)` — truncate first, then
  look for the key — so a key beginning before offset 300 and ending after it
  was searched for in a haystack shorter than itself and never matched. Measured
  with a 55-byte key at offset 270: 30 bytes leaked, and no placeholder appeared
  anywhere in the line, so the output did not even look redacted. This is
  [#306](https://github.com/rainmanjam/polyemesis/issues/306)'s exact failure
  mode reproduced in newer code. Scrubbing is now the outermost transform, and
  the rule is written down where the next transform will be added.

- **The Twitch minted key was registered as a secret in only one of its two
  spellings.** The engine registers `Outcome.Target.Key`, which already carries
  `?clientConfigId=…`; `SecretSet.Scrub` is a substring replace, so text
  containing the key *without* its query went unmasked. What survived was
  `v1_<signature>_<manifest>_[redacted]` — the operator's original key masked
  because it is a suffix, and the signature and hex manifest left standing. The
  guard that was supposed to catch this registered the bare key, which no code
  path produces, so it passed while the shipped code leaked. `wireSpellings` now
  emits the pre-`?` prefix, symmetric with its existing control-character
  expansion and the same truncation class, and the guard registers what the
  engine registers.

- **`secret.key` was read at whatever mode it was found in.** Creating it was
  careful (`0o700` directory, `0o600` file); reading it accepted anything. A key
  file restored from a tar archive without `--same-permissions`, copied under
  umask 022, or rsynced without `-p` lands at 0644 and stayed there for ever,
  silently — and that file decrypts every destination stream key in the
  database. It is now narrowed on every successful read, the way `db.Open`
  already treats the database and its sidecars.

- **`ScrubDestinationText` collected a destination's secrets differently from
  its sibling.** The supervisor spec passes the minted key alongside the row;
  this path passed only the row, so the two answers to "which strings on this
  destination are secret" disagreed — and the shorter answer belonged to the
  function that hands text back to a caller, on its way to a **retained** MQTT
  topic that cannot be recalled. Not reachable today, because the only text that
  arrives there predates the negotiation; fixed anyway, since that is a property
  of the current callers rather than of the function.

- **A destination's stream key could reach `server.log` on the give-up path.**
  The supervisor logs the child's error twice: once per retry, and once when it
  stops retrying. The retry line was scrubbed; the give-up line four lines below
  it read the same variable raw, and that variable is FFmpeg's stderr, which on
  a refusal names the publish URL and its key.

  The give-up path is the worse of the two to leak on. It fires only after
  `MaxRestarts` **consecutive** failures — after a destination has been refused
  over and over — which is exactly the state an operator is in when they stop
  watching and start copying `server.log` into a bug report. The retry line got
  the attention because it fires often; this one fires when somebody is about to
  hand the file to a stranger.

  A fix and a test for one line of a pair, with the sibling left open, is the
  shape this file has now recorded twice.

- **A destination's stream key could reach `data/logs/process.log` in the
  clear.** When a publish endpoint refused a connection, FFmpeg printed the
  output URL it could not open — key included — and the scrubber did not remove
  it. Not because it was missing: the destination declared its key and the
  stderr path did apply the scrub. `alerts.SecretSet` replaces the EXACT
  literals it was given, and on the run this was measured on the configured
  value was 65 bytes because a terminal had appended a bracketed-paste escape
  to it, while what FFmpeg opened and printed back was the 56-byte prefix
  ending at that escape. A 65-byte needle does not occur inside a 56-byte
  haystack, so nothing matched. `process.log` is persisted and rotated, which
  makes it exactly the artifact that gets collected into bug reports and
  support bundles — and a failure is when an operator copies logs to somebody
  else. ([#306](https://github.com/rainmanjam/polyemesis/issues/306))

- **A stream key containing a control character is now refused when you save
  it, rather than silently truncated.** The other half of a publish URL was
  already protected — `url.Parse` has always rejected these bytes — but the key
  is joined on afterwards and nothing looked at it. It is refused rather than
  repaired, so what is stored is always what goes on the wire: sanitising would
  mint a credential you never typed, and a destination that publishes with a
  quietly shortened key fails with nothing to explain why. The message names
  the offending byte and its position and never echoes the key. An existing
  destination carrying such a key must have it re-entered before it can be
  saved again. Behind that, `engine.destSecrets` now also declares the prefix a
  value takes if it is truncated at a control character, so the scrub covers
  what reaches the wire and not only what was stored.

- **The multistream leak check certified clean while a key was in the log.**
  Step 8b of `scripts/acceptance-multistream.sh` searched for the exact
  configured value, so any transformation defeated it — and so did its positive
  control, which searched the same bytes. It now searches for a distinctive
  16-character prefix of each key, and a new check plants a truncated copy of
  every configured key and proves the new predicate finds what the old one did
  not. Measured: with the pre-#307 predicate restored, a truncated key planted
  in `process.log` is certified clean by 8b and caught by the new control.
  ([#307](https://github.com/rainmanjam/polyemesis/issues/307))

- **Destination stream keys are encrypted at rest.** `internal/secrets` has
  sealed every OAuth client secret and access token since it existed, and its
  own docstring says what that buys: "a leaked database file — a backup, a
  snapshot, an errant scp — is not a leaked set of live streaming credentials."
  Destination stream keys were the one credential class outside that sentence,
  sitting in `stream_key TEXT NOT NULL DEFAULT ''`, and they are the worst one
  to leave out — long-lived, rarely rotated, and worth exactly what an attacker
  wants, which is the ability to broadcast to your channel. They are now sealed
  with the same NaCl secretbox key as everything else.
  ([#297](https://github.com/rainmanjam/polyemesis/issues/297))

- **Upgrading takes no action and no downtime.** The plaintext columns are kept
  and still read, so a database written by 0.7.0 works on the first read after
  the upgrade — before the migration, in fact. The first open with a key file
  seals every key it finds still in plaintext and blanks the column it came out
  of, and it is guarded by "is there still plaintext here" rather than by a
  one-shot marker, so an interrupted upgrade finishes itself on the next start.

- **A key that cannot be decrypted disables its destination instead of
  publishing with an empty one.** If the key file is lost, replaced, or the
  database is restored onto a different machine, the affected destinations stay
  in the list with their names, platforms and routing intact and are shown as
  needing attention: *the stream key could not be read on this machine —
  re-enter it to enable this destination.* Nothing is written to the row, so
  putting the right key file back restores every one of them by itself, with no
  repair step. Re-typing the key is the other way out, and an unrelated edit —
  a rename, a routing change — deliberately leaves the sealed bytes alone
  rather than destroying a key the right key file would have recovered.

- **Go 1.26.6, for two standard-library advisories published that day.** A
  toolchain bump rather than a defect in this code, recorded here because the
  released binaries are statically linked: a stdlib advisory reaches an operator
  only when a new binary is cut, so the version the release was built with is
  part of what the release is.
  ([#329](https://github.com/rainmanjam/polyemesis/pull/329))

- **The route that decides what your ingest pulls had no upload check at all.**
  Saving a pull source that names an upload this server was never able to
  inspect has been refused since #201 — but only on `PUT /settings`, and the
  engine does not read its ingest from there. `engine.effectiveSettings` does
  `settings.Ingest = src.Ingest`, so the source ROW is what the engine's FFmpeg
  opens, and `POST /sources` and `PUT /sources/{id}` accepted such a URL with a
  `201` and a `200`. That is the route the Sources page writes through, and it
  is the only route a second programme has ever had. Both now refuse it, with
  the same scoping the settings gate uses: what the save introduces, never state
  you inherited. ([#255](https://github.com/rainmanjam/polyemesis/issues/255))

- **And the check in front of all three routes was reading the URL, not the
  file.** It matched the exact spelling the Library hands you and split it at the
  first `/`, while the engine resolves the same URL through `filepath.Join` — so
  `file://uploads/./show.ts`, `file://uploads//show.ts` and
  `file://./uploads/show.ts` all opened the identical file and none of them was
  checked. One typed dot was the whole bypass, on the gate #201 shipped as well
  as on the two above, and the source card stayed silent about it too. The check
  now asks the engine what path a URL resolves to instead of re-reading the
  format, so every way of writing one file gets one answer.
  ([#201](https://github.com/rainmanjam/polyemesis/issues/201),
  [#255](https://github.com/rainmanjam/polyemesis/issues/255))

- **A source that was already pulling from an unchecked file now says so.** The
  gate above cannot see a URL saved before it existed, or one whose upload was
  downgraded underneath it, and there is no second gate downstream the way a
  playlist item has one. The source card now carries the server's own sentence,
  naming the file and what is wrong with it — and which of the two things is
  wrong, because they have different remedies: a file nothing could inspect is
  worth uploading again, and a file that *was* inspected and refused is not, so
  that one says to point the source somewhere else instead. It reports rather
  than refuses, deliberately: a missing inspection is a fact about this server —
  no `ffprobe`, an inspection cut short — and never about your file, so on an
  install without `ffprobe` a fail-closed re-check would take every `file://`
  ingest off air at once, at whatever hour the supervisor next respawned. That
  reasoning does not extend to a refusal, which *is* a fact about the file — see
  the entry below, which is where that half was settled. The field is on
  `GET /sources`, so a monitoring script sees it too.
  ([#255](https://github.com/rainmanjam/polyemesis/issues/255),
  [#264](https://github.com/rainmanjam/polyemesis/issues/264))

- **A source pulling from a file this server inspected and rejected is now
  taken off air.** The warning above is kept for an upload nothing could
  inspect, and only for that: a missing inspection is a fact about this server,
  so refusing on it would stop every `file://` ingest on a box without
  `ffprobe`. A *refusal* cannot be that — it exists only where an inspection ran
  and read the bytes — so an inherited pull source naming one is re-checked on
  every engine reconcile and its ingest is not started, primary and standby
  alike. The card still names the file and says why, and the reconcile records
  the same sentence, so a save made afterwards answers with it rather than
  leaving you a stopped programme and a log line. The job that writes that
  verdict — the *Media re-check* above — ships in this same release, so the gate
  is live rather than waiting on a producer.
  ([#255](https://github.com/rainmanjam/polyemesis/issues/255))

- **A busy server stopped inspecting uploads, and the caller chose when.** The
  four-slot semaphore that bounds concurrent `ffprobe` children waited for its
  slot *inside* the probe's own 30-second deadline, so a queued upload could
  spend the whole budget waiting and be stored with the verdict "the inspection
  was cut short". Nobody had to disconnect to reach that state any more — eleven
  more uploads did it. The wait now runs on the request's context and the
  deadline starts when the probe does, so a busy machine makes an upload slow
  rather than unchecked. ([#216](https://github.com/rainmanjam/polyemesis/issues/216))

- **A rejected upload told you where the server keeps its files.** `ffprobe`
  names its input in front of nearly everything it prints, and the `400` body
  passed those words through verbatim — the data directory and the internal
  `.partial-` name included. The path is now replaced with the name the file
  would have been given, at the handler egress, and the rest of the sentence is
  kept: `moov atom not found` is what tells an operator their download was
  truncated. ([#181](https://github.com/rainmanjam/polyemesis/issues/181))

- **Two outbound payload egresses were absent from the coverage ledger.** The
  webhook `POST` and the alert `POST` send payloads outward with no principal,
  which is the same shape as the retained MQTT topic the ledger already carries,
  and neither was listed — so nothing read their bytes. Both are now inspected
  by a proof that reads the real request off a real socket on a server whose
  every credential column holds a sentinel. The `:80` redirect listener is
  recorded as emitted-and-uninspected rather than left outside the ledger.
  ([#169](https://github.com/rainmanjam/polyemesis/issues/169))

- **`GET /assets/` returned a directory listing of the whole UI bundle, to
  anyone.** The SPA handler decided whether a request named a real asset by
  opening it -- and **opening a directory succeeds**, so the request was handed
  to `http.FileServer`, which answers a directory with a generated index page.
  An anonymous caller received the complete inventory of fingerprinted chunk
  names, which name product areas and reveal which features a deployment was
  built with.

  No credentials, no user data and no configuration were exposed; what leaked is
  build layout. A directory now falls through to the SPA fallback, which is how
  every other unknown path is answered.

  It survived this long because CI's Go job does not run `npm run build`, so the
  tests ran against an empty `dist` -- and against an empty filesystem the fixed
  and unfixed handlers are byte-identical. The bug was not merely untested, it
  was unwritable as a test until the handler was made to accept an arbitrary
  filesystem.

- **A `file://` pull now pins the input demuxer to the file protocol.** The
  engine's pull input carries `-protocol_whitelist file`, the same pin
  `ProbeFile` already used, so what an input demuxer may open is bounded by the
  argv rather than by whatever this FFmpeg build happens to enable.

  **This closes nothing observable today and is recorded as hardening, not as a
  fix.** Measured against FFmpeg 8.1.2: an ffconcat script naming `http://…` is
  refused identically with and without the flag, and one naming a sibling file
  is still resolved with it on. The substitution hole is closed by the format
  allowlist, which is not a flag and lives elsewhere.

- **Your MQTT broker password was echoed back in a validation error, and
  logged every five seconds.** A broker URL written with the credential inline
  — `mqtt://user:password@host:1883` — reached two places that repeated the URL
  verbatim.

  `MQTTSettings.problems()` builds the messages `Settings.Validate` joins into
  the **`400` body a settings save returns**, so the password came back over the
  wire to whoever submitted it. `parseBroker` wrapped the parse failure into the
  connection error, which the reconnect loop wrote to the log as an **`Error`
  line on every retry**.

  Two separate branches leaked, and the guard meant to prevent exactly this
  reached neither. `mqtt://user:pw@` parses cleanly and has no host, so the
  no-host message — which echoed the whole URL — fired before the credentials
  check ever ran. And `mqtt://user:pw@ho st:1883` does not parse at all, so the
  credentials check *could* not run: `url.Error.Error()` renders as
  `parse "<the whole URL>": <reason>`, carrying the password inside the wrapped
  error with no string concatenation anywhere in our code.

  Both messages now state the fault without repeating the input, and the
  credentials check runs first where a URL parses at all.

  **If you have ever saved a broker URL with a password in it, rotate that
  broker credential.** Upgrading stops new copies being written; it cannot
  remove the copies already in your logs, or in whatever collected those `400`
  responses.

- **A pull or destination password containing `@`, `/`, `!`, `#` or `%` was
  rendered verbatim in `GET /processes`, which a `read` token can reach.** The
  scrubber that removes credentials from a rendered FFmpeg command line is a
  substring replacement over the argv the process was actually handed. It was
  collecting the password through Go's URL parser, which **decodes** it — so for
  `rtsp://user:p%40ssw0rd%21@cam/stream` it held `p@ssw0rd!` and looked for that
  in a command line containing `p%40ssw0rd%21`. It matched nothing and masked
  nothing.

  Only passwords needing percent-encoding were affected; the username, the path
  and plainly-spelled passwords were always scrubbed. Both the pull side and the
  destination side had it, through separate code paths.

  **If a pull source or destination of yours uses a password with one of those
  characters, treat it as exposed to anyone who held a `read` token and rotate
  it.**

- **The ingest URL in `ingest started` carried the credential, on every boot.**
  `PublicIngestURL` renders the server half only for RTMP — deliberately, and the
  comment above the caller said so — but it renders `…&passphrase=<cleartext>`
  for SRT and the operator's pull URL WHOLE for pull, and a pull URL is where a
  camera password or a CDN path token lives. Both reached `ingest started` and
  `backup ingest started` at Info, so they landed in journalctl and
  `server.log`. Debug mode's scrubbing does not cover this: the recorder is a
  second consumer and the inner handler still writes the original record. The log
  rendering now keeps the host and port — the question a failed ingest actually
  asks — and drops the rest, and an allowlist test fails the build if a third log
  line acquires the full one.

- **Facebook's credential check printed the app secret on any network hiccup.**
  The pair travels in the query string, which is load-bearing — a POST form makes
  Facebook reject correct credentials — but a transport failure arrives as a
  `*url.Error` carrying the full URL, and that was interpolated raw into the
  message an operator sees and the logs keep. A DNS outage, a timeout or a TLS
  error was enough.

- **A credential in an unrecognised attribute shape reached the debug bundle.**
  Rendering a value before scrubbing it is only safe if the rendering keeps the
  bytes. It did not, twice: a `[]byte` was base64-encoded, so the credential
  arrived present, unrecognisable to the exact-match set, and decodable by the
  recipient in one step; and `&`, `<` and `>` were escaped, so a camera or CDN
  password containing an ampersand — ordinary, and a declared literal — was
  transformed out of matching range.

- **The debug bundle's scrub set had never heard of three inventories.** It was
  built from destinations; sources were added after an earlier review; platform
  accounts (which hold the OAuth access and refresh tokens), the install-wide
  ingest, and the failover BACKUP ingest were in none of them. The backup pull
  URL is logged in full at the moment the selector switches to it — which is when
  an operator is recording, because that switch is the fault they are capturing.

- **The source credential extractor read a pull URL with the publish-URL rule.**
  `urlSecrets` says in its own comment that it "is NOT correct for a pull URL,
  where the credential is in the URL and nowhere else". It takes the last path
  segment, which for a publish URL is the stream key and for a pull URL is the
  filename, so a CDN URL was declared to the recorder as `index.m3u8` and the
  credential left in the clear.

- **The hook response pass ran backwards.** The residual `Redact` was applied
  before the declared secret set rather than after it, inverting the rule
  `internal/alerts` states about itself. `Redact` transforms text, so a declared
  literal arriving inside a URL was no longer byte-identical when the exact pass
  ran.

- **An interrupted upgrade left plaintext stream keys in the write-ahead log,
  permanently.** The 0.7.0 migration seals every row, commits, then truncates the
  log — but the function returned early when it found nothing left to seal, and
  the truncate sits after that return. An upgrade that committed and then died
  came back, found no work, and never truncated the log again. Not on that boot
  and not on any later one. Measured at 162 plaintext keys still greppable after
  the restart.

- **The checkpoint never checked itself.** `PRAGMA wal_checkpoint(TRUNCATE)` does
  not fail with an error when it cannot get the lock; it returns a row with
  `busy=1` and the log where it was. The code used `Exec`, which discards result
  rows, so the fatal-on-failure guarantee written above it was armed for a SQL
  error that cannot happen and blind to the refusal that can.

- **Exporting a debug bundle now requires a signed-in operator.** `GET /debug`
  and `PUT /debug` stay reachable by an admin API token, so a dashboard can read
  capture state and an automation can start one. Taking the file — a copy of the
  server's own logs, meant for somebody who does not have the box — joins
  `/upgrade/stage` and `/auth/tokens` behind a session.

- **Expert mode accepted `-report`.** It makes FFmpeg write its own log file
  whose first line is the argv it was invoked with, stream key included, into a
  directory nothing here scrubs.

- **`ProtectProc=invisible` on both unit definitions.** A destination's stream key
  reaches FFmpeg as a command-line argument, and on a stock Linux host
  `/proc/<pid>/cmdline` is readable by every local account. The host half is
  `hidepid=2`; see INSTALL.md.

- **The installer ran an unverified third-party FFmpeg as root.** It refuses its
  own binary without a matching `SHA256SUMS`, and fetched FFmpeg with no
  integrity check at all — then extracted it and executed it to probe for libsrt.
  It now verifies against the published `checksums.sha256` and refuses the
  upgrade if that is missing or does not match, running nothing from the
  download.

- **Renaming a webhook destroyed a signing secret that was only unreadable.**
  Two deliberate designs composed into permanent loss. The read path swallows a
  decrypt failure and leaves the secret empty on purpose — "a secret that will
  not open leaves the hook UNSIGNED rather than unreadable", so one bad row
  cannot fail the whole listing — while the write path read an empty secret as
  "keep the stored one", fetched it back, and re-sealed it. When the ciphertext
  would not open, after a restore against the wrong key or a half-finished
  rotation, the re-seal wrote a valid encryption of the empty string over bytes
  that would have opened again once the right key came back.

  Editing any other field was enough to trigger it, and the API had been
  correctly reporting `hasSecret:false` the whole time — so the operator's own
  diagnostic step was what destroyed the value. The keep path no longer
  re-seals; the stored bytes are preserved in SQL.

- **A slate colour could rewrite the filtergraph.** `SlateSettings.Color` is
  documented as "any spelling FFmpeg's colour parser accepts" and nothing
  validated it, yet it was escaped one level deep under a comment asserting its
  inputs were "validated elsewhere to be a name or 0xRRGGBB". The strict
  whitelist that comment refers to is only ever applied to rendition *text*
  colours — so the validated field got the strong escaping and the unvalidated
  one got the weak escaping. A filtergraph is unescaped twice; both slate call
  sites now use the two-level escaper and the single-level one is gone.

- **The transcription confinement check did not confine.** A recording name is
  refused if it contains a separator or `..`, and the joined path is then tested
  for being inside the recordings directory — using an absolute-path conversion,
  which does not follow symlinks. A symlink placed in that directory passed
  every check, and both the existence test and the reader after it traversed to
  the target. Links are now resolved before the test, on the directory as well
  as the file, so an install whose recordings path is itself a symlink still
  works.

### Added
- **Trovo signs in.** OAuth 2.0 authorization code (no PKCE — Trovo documents
  none), the **stream key** over `channel_details_self`, title and category
  push over `channel_update_self`, and the live viewer count off the same
  channel response the key comes from. Trovo publishes no broadcast object at
  all, so start/end is *Not possible* there exactly as on Twitch and Kick.
  Chat and moderation are documented and deliberately not built yet; their
  scopes are not requested until they are.
- **A platform may now supply the stream key without an ingest URL.** Trovo
  issues its ingest hostname per region and publishes it nowhere in its API, so
  the server URL is copied out of the creator dashboard once. `Refresh stream
  key` keeps whatever URL the destination already had rather than blanking it,
  and says which field to go and fetch when there is none — previously it
  overwrote unconditionally, which for a platform like this turned a working
  destination into "an RTMP URL is required".
- **Every figure on every screen now says what it means.** Sixty-eight readings
  across twelve pages named a quantity and nothing else: `Speed 2.17x`,
  `Deviation — LU`, `Relay drops`, `PID`. Each now carries an explanation on
  hover — what it measures, and what a bad value would look like. It is not a
  convention that could be forgotten next time: a figure without an
  explanation no longer compiles.
- **The audio chips on a destination card name their tracks.** Six identical
  numbered squares said which tracks a platform receives and nothing about what
  those tracks are, so checking that the podcast feed carries the mic and not
  the music meant holding the routing editor's ordering in your head. They now
  read `Track 3 (Commentary) is included`, using the editor's own names.
  Excluded chips are named too — the question is usually "why is the music
  missing?". On a multi-programme install a card whose programme is not the
  selected one keeps the plain wording rather than borrowing another show's
  names.
- **Icon-only buttons and status dots answer on hover.** Every icon button
  already told a screen reader what it does and told a mouse user nothing;
  sixty-two of them now say the same sentence to both. The status dot carried
  its meaning in colour and shape alone — readable only if you already knew the
  vocabulary, and invisible to a screen reader — and now names its state.
- **The mix matrix says which track and which output each cell is.** The grid
  described every cell for a screen reader and showed everyone else an
  unlabelled box of digits, in a table whose row and column headers scroll out
  of view. Cells name the track by name where the ingest provides one.
- **The dashboard says when a disconnect would end the broadcast.** Failover is
  the device that prevents the one unrecoverable failure here — a completed
  YouTube broadcast cannot return to live — and it is off by default for good
  reasons that do not include "nobody should find out it exists". Once a
  programme has an enabled destination and failover is off, the destination
  list says so, and links to the setting. It clears itself when the setting
  changes rather than offering a dismiss button, so what is on screen is always
  the current exposure. (#512)
- **A lane per programme, so position says which is which.** Headings put a
  programme's name above its destinations but left its preview elsewhere on the
  page, so answering "is THIS one on air, and where is it going?" meant reading
  a name in two places and trusting they matched. A lane answers it by position:
  the picture and the destinations carrying it are the same box. (#483)
- **Destinations grouped under the programme they carry.** (#481)
- **The programme a destination carries is chosen, not guessed.** Creating a
  destination on a multi-source install silently attached it to whichever source
  the server picked. (#479)
- **A preview per source, following what is on air, that stops when unwatched.**
  (#472)
- **The second (VOD) audio mix has a UI.** `vodProfile` shipped complete — the
  column, the migration, the API, `routing.CompilePair`, the engine's gate on
  Twitch's one-track ingest — and appeared in the frontend exactly once, as a
  type on line 268 of `types.ts`. An operator could switch Enhanced Broadcasting
  on, watch it negotiate, and have no way to say what the second mix *contained*;
  the feature was API-reachable only.

  It is now edited on **Routing → Second (VOD) audio mix**, and it is the SAME
  editor the live mix uses rather than a second one written beside it. Both are
  a `routing.Profile`, so the block under the destination picker was extracted
  into one `ProfileEditor` component and is rendered twice — same track picks,
  mix matrix, music rights, loudness, delay, ducking, presets, and its own
  compiled filter graph from the same Go code that will run. Two things had to
  change to make it reusable: every DOM id is now namespaced (`id="norm"` became
  a duplicate the moment there were two editors, so clicking the second one's
  label moved the first one's control), and each instance compiles itself
  instead of borrowing a result that is not its own.

  Off is the default and stays the default. Switching it on seeds the second mix
  from the live one, so the first edit is the actual difference. Switching it
  off sends an explicit `null` — the API decodes over the stored row, so an
  omitted field would leave the pointer alone and the operator would watch their
  delete undo itself on the next load.

  The editor is **not** gated on Twitch: `routing.CompilePair` →
  `engine/destinations.go` → `ffmpeg.secondAudioMap` is a real two-mix egress
  and correct for every other target. What is gated on Twitch is the
  *explanation*. Where Enhanced Broadcasting is off on a Twitch RTMP
  destination, the engine refuses the pair at plan time and the page says the
  second mix is **not being sent**, in those words. Where it is on, the page
  says the answer is decided at go-live and lands on the destination card — it
  does not claim a second track that no negotiation has granted yet.

- **Twitch Enhanced Broadcasting is now reachable. EXPERIMENTAL: no broadcast
  has ever been published through a key it minted.** The negotiation is not the
  gap — `internal/multitrack/live_test.go` reaches `ingest.twitch.tv` on every
  run and Twitch answers: a supported-GPU inventory is accepted, a VOD audio
  track is granted, and a key is minted. What has never been observed is
  anything after that. No second audio track has been seen arriving at Twitch,
  and `internal/engine`'s wiring around the negotiation has only ever been
  driven by an `httptest` server; watching it end to end needs a real stream key
  and a real broadcast. Nothing is gated on that label: the feature is linked in,
  the toggle is one click, and a negotiation that does not succeed falls back
  to the ordinary Twitch ingest — the path every install used before this
  existed. `internal/multitrack` was
  complete, documented and tested, and nothing imported it: `go list -deps
  ./cmd/polyemesis` did not include the package, the `multitrack` column on a
  destination was written by the API, migrated, persisted, scanned back and
  shown as a toggle, and no code path ever read it. It is wired into the
  go-live path. A destination that opted in negotiates once per start — not per
  reconcile and not per reconnect — and on success publishes to the ingest
  Twitch names with the 312-character key Twitch mints, which carries the
  agreed rendition ladder signed inside it. That key is registered as a
  credential in its own right rather than assumed covered by the operator's:
  the minted value ENDS WITH the original, so a scrubber that knows only the
  original masks the last segment and leaves the signature and the manifest
  standing in `process.log` — a partially redacted live credential, which is
  the shape of #310 and #324.

  The hardware the negotiation needs is DECLARED under Settings → Pipeline →
  Enhanced Broadcasting hardware, not detected. Twitch validates the inventory
  and refuses by name on a zero vendor ID, an unrecognised vendor and an
  out-of-date driver; polyemesis can measure one of `multitrack.GPU`'s six
  fields on one platform, so a request assembled from that one with zeros in
  the rest would describe a machine that does not exist. With nothing declared
  no request is made at all, which is the state every install starts in and is
  not a fault: the destination publishes to the ordinary Twitch ingest and says
  so once, on its card, in plain text rather than behind an alert triangle.
  A refusal, a 5xx, a timeout and a hung platform all reach the same place.

- **A second audio mix is no longer pushed silently at an ingest that takes
  one.** The engine compiled the VOD pair on `destination.vodProfile` alone and
  never read `destination.multitrack`, so a Twitch destination with a second
  mix configured sent TWO audio tracks to an ingest documented as carrying one
  — with nothing in the log, the status or the card saying so, while
  `db.Destination.VODProfile`'s own comment promised "the engine reports it".
  It reports it now, in both directions: the pair is refused outright at plan
  time on a Twitch destination that did not opt in, and dropped at go-live on
  one that did and whose negotiation did not succeed. Both say why. The
  redundant feed of such a destination carries one track too, because it
  publishes to the operator's own backup URL whatever the negotiation said.
  The generic two-mix egress to every non-Twitch target is untouched.

- **A platform registry seeded from OBS's own service list**, carrying each
  destination's ingest hosts, its maximum video bitrate and its capability row.
  The Kick preset shipped with a real per-channel ingest host in a shape that
  could not publish, and six presets had no capability row at all — both
  invisible until something tried to use them.
  ([#312](https://github.com/rainmanjam/polyemesis/issues/312))

- **Rumble is a fifth chat platform.** Its live-stream API does carry chat,
  which the previous survey had concluded it did not.
  ([#321](https://github.com/rainmanjam/polyemesis/pull/321))

- **RTMP egress can carry a second audio track, and it has been measured doing
  it.** `ffmpeg.DestSpec.SecondAudioOutLabel` names a second finished mix from
  the destination's filter graph; it is mapped and encoded as a second audio
  track alongside the first. One track remains the default and every existing
  destination emits byte-for-byte the command it emitted before — no caller sets
  the field yet, because `routing.Compile` still describes one mix per
  destination. What is new is that the capability is no longer a guess: FFmpeg
  8.1 muxes two AAC tracks into FLV as Enhanced RTMP multitrack, this project's
  own RTMP server carries them, and
  `internal/ffmpeg.TestTwoDistinctMixesReachAnRTMPFarEnd` publishes the built
  argv into that server and reads a 300 Hz tone off one received track and a
  5000 Hz tone off the other. Tones rather than a track count, because two
  tracks carrying the same audio is a failure a count cannot see — and the same
  test proves it can tell, by publishing one mix twice and asserting the
  difference is absent. Whether any PLATFORM accepts a second audio track is a
  separate question this does not answer; the refusal of `-c:a copy` on an RTMP
  destination, which rests on that question, is untouched.
  ([#141](https://github.com/rainmanjam/polyemesis/issues/141))

- **`internal/multitrack` speaks Twitch Enhanced Broadcasting**, the negotiated
  configuration behind what Amazon calls IVS Multitrack Video — the one path a
  platform has published that takes a *second* audio track and says what it is
  for. It fetches a configuration from Twitch, reads the verdict, and resolves
  the ingest endpoint and stream key it hands back. Nothing publishes through it
  yet; see below for what is deliberately not built.

  **EXPERIMENTAL:** the four measurements below come from responses Twitch sent
  to this package's own requests, and `live_test.go` asks for them again on
  every run rather than replaying a capture — so a change at the far end shows
  up as a failing test rather than as a fixture that has quietly stopped being
  true. A declared GPU inventory does get past Twitch's hardware check. What is
  untested is everything after the negotiation answers: no key minted here has
  ever carried a broadcast.

  Four things were measured against the live endpoint rather than assumed, and
  each one changes what the code has to do:

  - **A refusal arrives as HTTP 200.** Every response — valid, invalid,
    unsupported hardware, unparseable schema version — was `200`, with the
    verdict in `status.result`. A successful negotiation omits the `status`
    object entirely rather than saying `"success"`, so a client that reads the
    status code, or that treats an absent status as an error, has read the
    wrong field.
  - **`authentication` is the stream key, not an OAuth token.** This does not
    depend on a connected account, which is what the issue expected. Better:
    on a *successful* negotiation Twitch mints a new 312-character key that
    carries the agreed ladder inside it, hex-encoded and signed, with the
    operator's original key as its last segment. Publishing with the operator's
    own key instead would connect and send a stream the ingest never agreed the
    shape of.
  - **A second audio track does not require a multi-rendition video ladder.**
    Asking for `maximum_video_tracks: 1` returns exactly one rendition *and*
    both audio tracks — live on track 0, VOD on track 1. That is what makes the
    feature reachable at all for polyemesis, which publishes one video track to
    an RTMP destination.
  - **Twitch refuses a client with no supported GPU**, by name: no GPU
    information, a vendor ID of zero, an unrecognised vendor, an out-of-date
    driver. There is no software-encoder path through this endpoint, so on a
    headless host encoding with libx264 the fallback to the ordinary ingest is
    the *normal* outcome and not the exceptional one.

  The operator's own settings are **the input to the negotiation, not something
  it overrides** — the returned ladder is derived from the canvas the client
  says it is producing, so an operator who picks 720p gets a 720p negotiation.
  Where Twitch's answer differs anyway (a `maximum_aggregate_bitrate` ceiling
  was simply ignored), the difference is reported and never silently applied,
  following the rule already written into `services.URLProblem`.

  Both the request and the response carry a credential, so neither may be
  logged as it stands: `Config.Redacted` is the only shape of a configuration
  fit to print, and every error the client returns is scrubbed of the key.
  ([#326](https://github.com/rainmanjam/polyemesis/issues/326))

- **Capped VBR: a rendition can now set a bitrate ceiling and a rate window.**
  `RenditionSpec` had carried `MaxrateKbps` and `BufsizeKbps` since it was
  written, and `RenditionArgs` used both correctly — but nothing could set
  them. `renditionSpecOf` mapped `VideoKbps` and stopped, so every install fell
  through to the CBR relationship whatever the operator intended. Nothing was
  broken; the code described a capability the product did not have, which is
  the harder kind to notice because every individual piece passes its own test.

  A ceiling **below** the target bitrate is refused rather than clamped. There
  is no way to resolve `-b:v 6000 -maxrate 4000` without overriding one of the
  two numbers, and whichever is chosen the operator gets a stream at a bitrate
  they did not pick with no sign that a field they filled in was ignored. Both
  fields default to `0`, which emits byte-for-byte the command line every
  existing install already emits.
  ([#341](https://github.com/rainmanjam/polyemesis/issues/341))

- **The first-run screen states what the admin password does and does not
  protect.** A strength meter now scores the password as it is typed, and the
  notice beside it corrects an assumption the screen previously invited: this
  password protects the admin UI and the API, and it is **not** what encrypts
  your stream keys. Those are sealed with a key file in the data directory,
  which must be backed up alongside the database or every destination comes
  back disabled after a restore.

  The key is deliberately not derived from the password, because the server
  refreshes OAuth tokens while nobody is logged in. Deriving it would be
  security theatre that also broke unattended restarts.
  ([#346](https://github.com/rainmanjam/polyemesis/pull/346))

- **An upload the server never managed to inspect can be re-checked in place,
  instead of being re-uploaded.** `POST /media/{name}/verify` queues a *Media
  re-check*: it reads the stored bytes again and records what it actually
  established. Both directions are reported, because both matter to somebody —
  a file that was refused and now passes has just become usable as a playlist
  item, and one that passes and now fails has just stopped being usable. A
  second refusal repeats the reason rather than silently re-asserting the first.
  The route takes the same two checks as deleting an upload, in the same order,
  so a probe sidecar cannot be named as the thing to verify.
  ([#202](https://github.com/rainmanjam/polyemesis/issues/202))

- **A raw `.h264`, `.hevc` or `.mpegvideo` dump can go straight into the
  Library.** These files carry no container, so nothing in them says how long
  they are, and the upload gate refused them: *"polyemesis cannot work out how
  long this file is — re-save it as MP4 or MPEG-TS and upload it again."* A real
  remedy, and manual work the product can do. polyemesis now **counts** the
  length instead, by decoding the file once, and accepts it. Measured on a
  10-minute 720p dump: 2.8 seconds. It runs inside the inspection budget an
  upload already has, so nothing new can make an upload slower than it could be
  before; a file too long to count inside that budget gets the old refusal, with
  the old remedy. Your file also keeps its extension now instead of being stored
  as `.bin` — for a raw stream that is the only hint FFmpeg has about how to read
  it.

  **The Library says which of the two it is.** A duration a container declared
  and a duration polyemesis counted are not the same claim: the first was written
  down by whatever made the file, the second is every frame we decoded times the
  frame rate the encoder declared in the bitstream, and a raw stream holds
  nothing to check that rate against. So `durationSource` sits beside
  `durationSeconds` and says `declared` or `counted`, rather than the two being
  indistinguishable once the number is in the field.
  ([#218](https://github.com/rainmanjam/polyemesis/issues/218))

- **An upload can now be recorded as *inspected and refused*, which is a thing
  the server previously had no way to write down.** The record beside every
  upload carried one boolean, so it had two states — accepted, and stored
  without being inspected — and the product needed three. A refusal was
  therefore never stored at all: the upload handler answers `400` and throws
  the staged bytes away, which works exactly once, at upload time, while
  nothing references the file. It does not work for anything that inspects an
  upload *later*: by then the file is published, `DELETE /api/v1/media/{name}`
  answers `409` while a playlist item names it, and the only state available to
  record the refusal in was the one that says "nobody read this" — which every
  consumer answers by telling the operator to upload the same bytes again, for
  a file that will be refused identically.

  `GET /api/v1/media` now carries `outcome` on every row, always present, with
  four values: `verified`, `unverified`, `refused`, and `unrecorded`. The fourth
  is not stored anywhere — it is what the listing says when there is no record
  beside the file, which is every upload an install made before verdicts
  existed, and it stays distinct from every recorded state because refusing
  those would strand media an operator has had for a year. The Library shows
  **Refused** rather than **Not checked** for the new state, the playlist editor
  does not offer it, and both settings validators refuse it with a sentence that
  does *not* say "upload it again".

  `verified` keeps its exact meaning and is still written to disk, because the
  sidecar format exists in every install and a format change has two directions
  in time: an older binary reading one of today's records — a rollback, or a
  second process during an upgrade — sees `verified: false` with a reason and
  refuses the file, which is the wrong label but the safe answer. An `outcome`
  this build does not recognise falls back to the same field rather than being
  trusted, and a record that claims a pass in one field while denying it in the
  other is refused outright.

  This state was added before anything wrote it, because a re-verify that
  recorded its findings as "not checked" would be worse than no re-verify at
  all. The job that writes it — the *Media re-check* under Added — ships in this
  same release, so `refused` is a value you can now actually be shown.
  ([#202](https://github.com/rainmanjam/polyemesis/issues/202))

- **A destination can now forward its audio bit-for-bit.** Set `copy` on a
  destination's audio block — `-c:a copy`, no decode, no mix, no encoder — so an
  archive or contribution feed carries the same bits your encoder sent us.
  Available in the destination dialog.

  It is called **copy** and not "passthrough" on purpose: passthrough already
  means a video rendition at the ingest's own resolution, and reusing the word
  would make "a passthrough destination" ambiguous in exactly the conversations
  where it matters.

  Copy still **selects**. The compiled routing profile decides which tracks go
  out and the role policy still removes excluded ones, so the DMCA switch keeps
  working; what you give up is everything the mix stage does to the samples.
  Because of that, a destination that sets `copy` alongside mix settings the
  copy path cannot honour is **refused, naming each setting individually**,
  rather than accepted with the settings silently ignored. A form that says one
  thing while the stream does another is the worse outcome.

- **Staging and rolling back the server binary now have endpoints.**
  `GET /api/v1/upgrade/plan` reports what an upgrade would do and is a read.
  `POST /api/v1/upgrade/stage` and `POST /api/v1/upgrade/rollback` perform it
  and are **session-only** — deliberately out of reach of an API token of either
  scope, because a leaked token that can replace the server's own binary is a
  different category of problem from one that can read a stream key.

- **One control starts or stops every destination.** An operator with eight
  destinations was pressing eight buttons; `POST /destinations/start-all` and
  `POST /destinations/stop-all` now act on the whole install, with a matching
  pair of buttons beside the destination list on the dashboard. There is no id
  list and no per-card selection: the routes act on everything, deliberately,
  because a bulk control with a selection is the per-destination control with
  extra steps and one more thing that can be stale by the time it is pressed.

  Each row is driven through the same code as the per-destination start and
  stop, so the bulk control can never be more destructive than the button it
  replaces — and **the answer is a list, never a boolean**. One row per
  destination, naming which it was, what happened (`started`, `stopped`,
  `warned`, `failed` or `skipped`) and why when something did not happen. Eight
  destinations of which two refuse is not "failed", and the operator should not
  have to open eight cards to find out which two.

  **Starts are paced**, one destination at a time with a gap between them, so a
  burst of encoder processes does not contend on the box and the same
  connections do not arrive at a platform as one clap. That is a pacing choice
  about this machine; it encodes no platform's published ceiling, counts
  nothing and caps nothing. Stops are not paced — tearing down is local.

  Stopping asks for confirmation, and the confirmation says what stopping
  actually costs: **stop and disable are one thing on the server**, so stopping
  ends every YouTube broadcast on the install and a completed YouTube broadcast
  cannot return to live. Starting again puts the video back on the wire; it does
  not bring the broadcasts back. That was already true of the per-destination
  Stop button one row at a time — the wording is new, not the consequence.

- **The viewer count is on screen, and a withheld one does not read as zero.**
  Three platforms implement `GET /platforms/accounts/{id}/stats`, the route
  answered, and nothing in the UI called it — so the capability matrix said
  "Viewers: Works" while no operator could see a number anywhere. Every
  connected account in Settings → Platforms now carries its live state, polled
  once a minute while the tab is visible and stopped entirely for a platform
  that has said it cannot answer.

  The distinction the round is actually about: a live stream whose count the
  platform DECLINED to give reads "Viewer count not reported", never `0` and
  never a dash. YouTube omits the number when nobody is watching, when the owner
  has hidden it, and once the broadcast ends — three states, one absent key —
  and the one that matters is the streamer with an audience who would otherwise
  be told nobody is there. A reported `0` still renders as `0`, because on a
  live stream that is a fact. A platform polyemesis cannot ask shows the
  server's own sentence naming it rather than an empty space, and an offline
  channel says offline rather than reporting an audience of none.

  The interval is a quota decision. One YouTube stats read is three requests
  against a project-wide ceiling of 10,000 units a day that title push,
  compliance and chat all draw from, so polling harder does not merely slow this
  panel down — it takes metadata push down with it.

- **polyemesis.com publishes the documentation.** The site went from 6 pages to
  37: the 23 user-facing documents in `docs/` are rendered at `/docs/<slug>`
  rather than linked to GitHub, five comparison pages sit under `/vs/`, and
  `/free-restream-service` and `/how-to-multistream-from-obs` answer the two
  queries with the most measured search demand this project can address.

  The documents were invisible to search before this — 63,000 words reachable
  only on github.com, so the authority accrued to Microsoft's domain and the
  pages had no title, description or internal linking under our control.

  Publishing is an **allowlist**, not a glob with exclusions. `docs/` also holds
  `RESEARCH-COMPETITIVE.md` and `COPY-CONSTRAINTS.md`, and a glob would make the
  next internal note public by default. A build check fails when a file in that
  directory appears in neither list, so adding a document is a decision somebody
  writes down.

- **`llms.txt`, `/.well-known/security.txt`, an apple-touch-icon, `HowTo`
  structured data on the install steps, and `lastmod` in the sitemap.** The
  `lastmod` dates come from `git log -1` per page rather than the clock: a
  build-time stamp marks every page as modified on every deploy, which is a lie
  a crawler discounts, costing the signal it was meant to give.

  `security.txt` carries the mandatory RFC 9116 `Expires` field, and the build
  now fails when it is past and warns 30 days out. An expired security contact
  reads as an abandoned one to the person deciding between reporting privately
  and going public.

- **Broadcast lifecycle: polyemesis can tell YouTube to go live and to end.**
  Connecting an account used to mean fetching a key and pushing a title;
  "going live" was still bytes arriving at an ingest. A coordinator now drives
  the platform's own broadcast state from the UP/DOWN edges the engine already
  derives, and the `broadcastLifecycle` column says per platform what that
  actually means. **YouTube is driven** — it goes live when video starts
  arriving and ends when you disable or delete the destination, never when the
  encoder merely crashes, because a completed YouTube broadcast cannot return
  to live and a crash is recoverable. **Facebook is commanded by hand**:
  connecting creates the live video and "End broadcast" is on the destination
  menu, but nothing ends it for you. Twitch and Kick publish no such API at
  all, which is established by enumeration rather than assumed.

  A refused transition raises a fault and never stops the stream. Stopping the
  encoder on a failed transition would destroy the only condition under which a
  retry could succeed, since YouTube requires an active ingest to transition.

- **Facebook's stream health and end-broadcast are wired to routes.** Both
  existed in the provider and neither had a route, so the menu item would have
  404ed while the stream stayed live. The health pane also states that Twitch,
  YouTube and Kick publish no ingest numbers at all — that absence is a fact
  about those platforms, not a gap in polyemesis.

- **The documentation site renders diagrams.** `docs/INSTALL.md` gained
  flowcharts of the installer and of the FFmpeg gate, and `/docs/install`
  draws them instead of printing forty lines of source at the reader.

- **A documented way to remove polyemesis.** `docs/INSTALL.md` had no "Removing
  it" section, and binary installs shipped no uninstall script, so the only
  removal instructions were the ones an operator worked out themselves. Both now
  exist, and the uninstall path is exercised in CI rather than described.

- **The non-systemd caveat, written down.** polyemesis puts each FFmpeg child in
  its own process group so that stopping one destination stops its helpers and a
  Ctrl-C reaches polyemesis rather than killing its children mid-write. The cost
  of that isolation is that a polyemesis which is *killed* rather than asked to
  stop signals nothing on the way out, and its encoders keep running, reparented
  to `init`, still holding the relay ports they were reading. Under the shipped
  unit `KillMode=mixed` closes that — systemd SIGKILLs the whole cgroup. Outside
  systemd nothing does, and now `INSTALL.md` says so next to the `KillMode` entry.
  ([#448](https://github.com/rainmanjam/polyemesis/issues/448))

### Changed
- **The uninstaller refuses while a broadcast is on air.** Stopping the service
  ends every live broadcast on the install, and a completed broadcast cannot be
  returned to — yet the installer asked "Proceed?" before the reversible act of
  installing while the uninstaller asked nothing before the irreversible one.
  All three uninstallers (systemd, Docker, Windows) now refuse while the service
  is publishing, name what is live, and require the service name typed to
  confirm. A run with no terminal is refused rather than assumed, so an
  unattended job cannot uninstall a broadcast server by inheriting the script.
  `--remove-data` performs the deletion with guards instead of printing an
  unguarded `rm -rf` for the operator to paste.
- **A version tag cannot publish a release the changelog does not describe.**
  `release.yml` gained two gates: one requiring a successful CI run against the
  tagged commit, and one requiring the tag to match the top version heading in
  this file, dated. Both fail closed — an API error, an unparseable changelog or
  an undated heading all refuse rather than guess. (#499)
- **Two features are now labelled EXPERIMENTAL throughout — labelled, not
  gated.** Twitch Enhanced Broadcasting and hardware encoding both shipped in
  0.7.0 with a gap in the evidence behind them, and the label names where each
  gap actually starts. For Enhanced Broadcasting it is *after* the negotiation:
  `internal/multitrack/live_test.go` reaches `ingest.twitch.tv` on every run and
  Twitch accepts a supported-GPU inventory, grants a VOD audio track and mints a
  key — what has never been observed is a broadcast published through that key,
  and `internal/engine`'s wiring around it has only ever been driven by an
  `httptest` server. For hardware encoding it is the `_nvenc`, `_qsv`, `_vaapi`
  and `_amf` flags — eight of the twelve encoder profiles — which were read off
  FFmpeg's option tables inside a GPU-less container. Both remain fully enabled,
  nothing is hidden, no opt-in env var or feature flag exists, and every control
  works exactly as before.

  **The first version of these labels was wrong in both directions, and only
  running something found it.** They asserted that the negotiation had never
  reached Twitch while a non-skipping test in the tree reached it on every green
  build, and they warned a Mac operator off `h264_videotoolbox` — the one
  hardware family a real encode confirms. A claim about what has *never* been
  tested can only be checked by running the test.

  One convention, applied everywhere, and each use names the specific untested
  claim rather than saying "beta": a `<Experimental>` component in the UI
  (`ui/src/components/Experimental.tsx`), an `// EXPERIMENTAL: <what is
  unverified>` line in Go doc comments, a `> **EXPERIMENTAL — <claim>.**`
  blockquote in docs, and a leading `**EXPERIMENTAL.**` sentence on a changelog
  entry. The badge deliberately uses the neutral `outline` variant: the app's
  saturated tokens mean the state of a *destination*, and "unverified on
  hardware" is a property of the feature.

  This also corrects one claim that read as stronger than it was.
  `encoderProfiles`' doc comment said the values were "verified by running
  `ffmpeg -h encoder=<name>` and a real one-frame encode" — true, but the option
  table answers on a machine with no such device, and the one-frame encode only
  ran for encoders the container could open. The narrower evidence that *does*
  exist is now named where the table lives:
  `TestEveryConfiguredEncoderOpensWithItsOwnFlags` runs a real encode per
  registered encoder with that encoder's own row — capped-VBR path included —
  and answers for whichever encoders the machine running it registers. A CI
  runner with an NVIDIA card would retire the NVENC caveat by itself.

  The UI badge is gated on the encoder *family* rather than on
  `encoder.hardware`, which is what made it fire on VideoToolbox.

- **Documentation-only pull requests no longer run the acceptance matrix.**
  Three documentation PRs in one day each fired all 27 checks — the full
  acceptance matrix, three-OS builds, Docker and browser suites — to validate
  files no job reads. One of them was a single file. The gate fails toward
  running: `code` is false only when every changed path is documentation, so a
  new top-level directory gets the full matrix until somebody decides otherwise.

  The first attempt at this made every documentation PR **unmergeable**, and the
  mechanism is worth recording because it is not obvious. Twelve acceptance
  suites are required status checks. A skipped *ordinary* job still reports, and
  a skip satisfies the requirement — but a skipped **matrix** job never expands
  its matrix, so the per-leg contexts are not skipped, they are never created.
  The checks list showed one entry named literally `acceptance: ${{
  matrix.suite }}` and twelve required contexts reported missing. The matrix now
  always expands and the gate is on each step instead, so a documentation run
  costs a runner allocation rather than a suite.
  ([#350](https://github.com/rainmanjam/polyemesis/issues/350))

- **The `internal/api` coverage guard could not fire before the ceiling above
  it.** Its three probes carried no `-timeout`, so Go's ten-minute default
  applied to each — thirty minutes of worst case behind a job ceiling of
  twenty-five. It could never have reported; it could only ever have been
  cancelled, which is what happened when it hung for twenty-four minutes against
  a step measured at 98–114 s. Bounded now at four minutes per probe with a
  step timeout above that, so Go's own panic still wins the race and names the
  test that was running.
  ([#357](https://github.com/rainmanjam/polyemesis/pull/357))

- **The marketing site moved to Cloudflare Workers Static Assets**, because
  Cloudflare stopped offering new Pages projects.
  ([#332](https://github.com/rainmanjam/polyemesis/pull/332))

- **The marketing site gained a screenshot lightbox, a version in the footer, a
  "who this is not for" section, and touch affordances** for the scrollable
  comparison table that previously only signalled to a cursor. Screenshots were
  re-shot at 61% smaller with no loss of resolution — quantisation in the
  capture pipeline rather than downscaling, which was measured making PNG
  compression *worse*.
  ([#335](https://github.com/rainmanjam/polyemesis/pull/335),
  [#336](https://github.com/rainmanjam/polyemesis/pull/336),
  [#337](https://github.com/rainmanjam/polyemesis/pull/337),
  [#338](https://github.com/rainmanjam/polyemesis/pull/338),
  [#345](https://github.com/rainmanjam/polyemesis/pull/345))

- **BREAKING: a `read` API token gets metadata, not content.** Thirteen routes
  now answer `403` to a `read`-scoped token that previously served them: the
  five media and export downloads, both transcript endpoints, and
  `GET /library/search`.

  Search is on that list and it is the one worth reading twice, because it looks
  like a metadata query and is not. `db.TranscriptHit` carries the segment
  `text`, its neighbouring `context` and the `speaker`, so a token that iterated
  common words would rebuild whole transcripts without ever requesting a route
  with `transcript` in its path. The line is drawn at what the bytes are, not at
  what the URL says.

  Listing is unaffected: a `read` token still sees recordings, clips, stems and
  sessions, their durations, sizes and status, and whether a transcript exists.
  `GET /library` still returns the bare list of speaker labels — who appears,
  rather than what was said. **If you have automation that downloads recordings
  or reads transcripts with an API token, it needs an `admin` token now**; the
  route works, the scope does not.

  **Every token you already have becomes `admin` when you upgrade.** The
  migration is `ALTER TABLE api_tokens ADD COLUMN scope TEXT NOT NULL DEFAULT
  'admin'`, so existing automation keeps working exactly as it did and nothing
  breaks on restart. The consequence worth stating plainly is the other
  direction: **nothing is restricted until you act.** The `read` scope is opt-in
  — a monitoring script does not become read-only by upgrading, and if you want
  it to be, issue it a new `read` token and revoke the old one.

- **BREAKING: an upload that no probe could read is stored but marked, and some
  formats are refused outright.** Uploads now carry a verdict record, so a file
  that was accepted without being inspected — a client disconnect mid-probe, no
  `ffprobe` on the box — is distinguishable from one that passed, and playlists
  refuse to reference it. Files uploaded before this exist as "no record", which
  is deliberately not the same as "unverified": your existing library is not
  stranded.

  Raw elementary streams (`.h264`, `.hevc`, `.mpegvideo` dumps) are now refused
  at upload with a message telling you to remux. They report no duration, so the
  old behaviour was to accept them and then fail the normalise job permanently —
  a playlist item that could never go on air. Refusing at the door is the same
  answer given earlier and with a remedy. See #218 if you need them supported.

- **BREAKING: a job-state conflict answers `409`, and a running job cannot be
  deleted.** `POST /jobs/{id}/cancel` and `/retry` against a job in a state that
  does not permit it returned `500`; they now return `409` with a sentence.
  `DELETE /jobs/{id}` on a RUNNING job is refused rather than removing the row
  and orphaning its worker.

- **`/hls/*` is session-only.** The dashboard preview playlist no longer accepts
  a bearer token of either scope, and the routes answer `GET` and `HEAD` only.
  If you point a player, a probe or a script at an HLS URL from outside a
  browser session, it stops working.

- **Revoking an API token now closes its open WebSocket, and changing the
  account password closes the operator's.** A socket used to keep the principal
  it was opened with for ever, so revocation — the only lever you have after a
  leak — did not reach a live connection.

- **An install with no source is one somebody can actually use.** A fresh
  database no longer manufactures a "Main" source nobody configured, so zero
  sources is a normal state rather than a state the product had never been run
  in — and every screen now behaves as though it were. Reads answer (the
  dashboard, the telemetry socket and the Prometheus scrape used to nil-deref
  together on the very first page load); the routes that act on a pipeline
  refuse with `503` and `code: "no_source"` rather than falling over; and the
  Dashboard, Sources and Settings pages draw an empty state naming the one next
  action instead of a red toast.

  Two things that were previously silent are now loud. The settings **ingest**
  editor refuses a change on an install with nowhere to write it through to,
  where it used to store the block, answer `200` and have no effect whatever;
  every other setting in the same request is still saved, because `PUT
  /settings` also holds the listeners, recording, chat, automod and alerts, all
  of which an operator legitimately configures before creating a source. And
  the startup banner says `ingest no programme yet` instead of naming a port
  that is bound and will refuse the encoder aimed at it.

  Upgrading installs are unaffected: the migration still carries an existing
  single-ingest configuration onto a source called "Main", and now tells that
  case apart from a first run rather than seeding both. (#387)

- **The listener ports stay editable on an install with no source.** They belong
  to the whole install rather than to a programme — one SRT listener serves
  every source and tells them apart by publish token — so they lived on the
  ingest tab, which the empty state had replaced wholesale. A first install
  whose 1935 or 6000 was already taken therefore had no port control anywhere in
  the UI, and was invited to create a source that would arrive on the port that
  could not bind. (#387)

- **`POST /alerts/rules/{id}/test` says which absence it is refusing for.** It
  answered `503 the alert notifier is not running` on an install with no
  programme, which sent the operator looking for a subsystem to restart:
  `Engine.Alerts()` is nil for exactly one reason, and it is this one. It now
  carries `code: "no_source"` like every other refusal of the same condition.
  (#387)

- **The startup SRT warning names a screen that exists on the boot that prints
  it.** "switch Settings → Ingest to RTMP" was executable only because a seeded
  source put an ingest form on that tab. `docs/QUICKSTART.md`, `docs/INSTALL.md`,
  the meters page and the media uploader carried the same kind of pointer and
  now say where the setting really is. (#387)

- **The last source can be deleted.** The store refused it, on the grounds that
  an install with none had no ingest and no way through the UI to get one back.
  Neither half holds any more: zero sources is the state a fresh install boots
  into, and the Sources page carries the form that ends it. The refusal was
  standing between an operator and a place they can already be — most obviously
  the one replacing their single source, who had to create the replacement
  first and then work out which of two rows was which. The delete button on the
  only source is no longer greyed out, and its confirmation says what the
  install is left with: destinations and renditions go with the source,
  recordings stay on disk, and nothing is publishing until another source
  exists. A boot after that delete does not put "Main" back. (#387)

- **The confirmation of a deleted source stopped disagreeing with the warning
  that preceded it.** The dialog said renditions go with the source, which they
  do; the message afterwards named destinations alone, so the operator who
  wanted to keep a 720p encode for a replacement was told nothing had been lost
  that they would have to build again. Both now name the same three things, and
  deleting the LAST source says so — that message is the only thing on screen at
  the moment the install stops having a programme. (#387)

- **Install-wide state comes off the engine** (first of six changes for #387).
  Tools, recording paths, the host sampler and the settings read no longer route
  through whichever engine happens to be default. No behaviour change intended,
  with one exception worth stating: `GET /system` now reads settings from the
  store rather than the engine's snapshot, so it stops lagging a settings save.

- **An onboarding tour, offered once per install rather than once per browser.**
  A new operator finishes the signup screen and lands on an empty dashboard, and
  everything they need next is either in a terminal they have closed or in
  `docs/`. The tour covers the things the console does not say out loud: where a
  source's publish address and token live now that `install.sh` has printed them
  and exited, that Routing is the part which is not restreaming, what the
  `experimental` badge is actually claiming, and that `secret.key` is a separate
  file whose absence brings every destination back *disabled* after a restore
  with nothing on screen to explain why.

  It is **offered, not launched**: a dismissible strip under the header, plus a
  replay control in Settings. Completion is stored server-side —
  `users.tour_completed_at`, added by an idempotent migration — so an operator
  opening the same install from a second machine is not offered it again. New
  routes: `GET /api/v1/tour` and `POST /api/v1/tour/complete`, the write
  admin-scoped because a read-only token must not mutate user state.

  Built on [driver.js](https://github.com/kamranahmedse/driver.js) (MIT, **zero
  dependencies**), themed from `ui/src/index.css`'s own tokens rather than the
  library's stock white popover. A tour's failure mode is silent — a selector
  that stops matching simply highlights nothing — so the steps are DATA in
  `ui/src/lib/tourSteps.ts` and `ui/src/lib/tour-drift.test.ts` asserts every
  selector against the component that owns it, reading past comments so an
  anchor cannot be kept green by leaving the words behind in one.

- **A keyframe lookup that finds nothing now widens to a bounded window rather
  than reading the whole file.** The fallback asked for one packet record per
  frame across the entire recording and buffered all of it, on a path an
  authenticated request can reach — so asking about a few seconds of a long
  archive allocated in proportion to the archive. Both cases the fallback exists
  for, a long GOP and a file shorter than the lookback, are covered by a
  ten-minute window centred on the point in question.

### Fixed
- **The retention sweep could delete the recording being written.** The age
  branch removed anything past the cutoff with nothing protecting the open
  segment, which a segment length longer than the max-age reaches in the ordinary
  course of things — and the sweep runs every thirty seconds, so it would find it.
  Both branches now skip the live segment by filename. The size cap's existing
  guard was positional rather than identity-based — it protected whichever row
  sorted last — so it has been replaced rather than copied. (#504)
- **Two goroutines racing could kill the process and end every live broadcast.**
  A subscriber's `close()` was select-then-close, which is check-then-act rather
  than atomic; three teardown paths call it and nothing in the package recovers,
  so the second close panicked the daemon. A per-instance `sync.Once` makes the
  double close unrepresentable. (#496)
- **A rollback to 0.6.x would have silently dropped every destination.** 0.7.0 is
  the first release that seals stream keys at rest, so an older binary would open
  the database, read an empty key and publish nowhere, with nothing refusing and
  nothing explaining. The schema is now stamped with `PRAGMA user_version` and a
  database written by a newer release is refused before any write, naming both
  versions. This protects rollbacks *from* 0.7.0 onward; it cannot protect the
  0.7.0→0.6.x rollback itself, because the binary doing the reading is already
  shipped. (#498)
- **The upgrade path was not exercised by anything.** A column declared only in
  `schema.sql` reached fresh installs and not upgraded ones, because the file is
  `CREATE TABLE IF NOT EXISTS` — so every hook read on an upgraded install
  answered "no such column" while the whole test suite stayed green, since every
  test builds a fresh database. Migration added, and with it a test that creates
  a row, drops the column, asserts the database is unreadable in that state, then
  migrates and checks the row survived. (#489)
- **The Windows job-object setup handed the kernel an address the Go runtime was
  free to invalidate, which is a write into freed memory.** `ensureJob`
  converts `&info` to a `uintptr` and passes the integer to
  `windows.SetInformationJobObject`. unsafe's rules permit that only where the
  compiler arranges for the object to be *retained and not moved* until the call
  completes, and it does not do so here: the x/sys wrapper is an ordinary,
  splittable Go function that happens to take a `uintptr`. The
  `runtime.KeepAlive` added earlier covered retention only —
  `KeepAlive` is deliberately special-cased *not* to force its argument to
  escape, so the struct stayed a stack local, and any stack growth between the
  conversion and the syscall relocates it while the integer keeps pointing at
  the freed old stack. The kernel then writes 112 bytes into memory the runtime
  has already recycled, and the crash lands later in an unrelated goroutine as
  `fatal error: found pointer to free object`. The conversions now sit in the
  argument list of `//go:uintptrescapes` wrappers, which forces the struct onto
  the heap — where Go's collector never moves it — and retains it for the
  duration of the call. No tool in the toolchain flags the unfixed form, so the
  guarantee is now asserted by reading the compiler's escape analysis back out
  in a test that runs on every platform, not just Windows. (#440)
- **The destination hold was bounded on a clock nobody was measuring.**
  `reconcileOutputs` holds every destination until the ingest layout is probed,
  and its only exit was five CONSECUTIVE probe failures. But `probeLoop` probes
  only while the relay is carrying data, so the counter advances during flowing
  stretches and freezes, unreset, during quiet ones — and a relay that goes
  quiet and back is routine in an encoder's first minute, because the selector
  leaves the primary for the slate and returns. The exit therefore needs 65s of
  FLOW, and the wall-clock cost is that plus every quiet spell in between,
  without limit. Adds a wall-clock ceiling set above the counter's own worst
  case, so on a stream being probed the counter still reaches five first and the
  ceiling only bites where the counter is blind. (#473, #485)
- **A destination was answered for by the first programme's engine rather than
  its own.** `s.eng()` is `mgr.Default()`, which is `engines[0]` — correct only
  on a single-source install. (#478)
- **47 mistake-proofing defects, and the toolchain they were found on.** (#480)
- **The upgrade guard refused upgrades it was written to protect.** The
  generated `update.sh` checked its backup with `tar tzf … | grep -q secret.key`
  under `set -o pipefail`. `grep -q` exits on its first match, `tar` then takes
  SIGPIPE, the pipeline returns 141, and `if !` inverts that into "the file is
  missing" — so a backup that **did** contain `secret.key` was rejected with
  *"Refusing to upgrade."*

  It is not a race and not a rare edge. The determinant is the pipe buffer:
  below roughly 200 entries `tar` finishes writing before `grep` exits and the
  check passes; above it, `tar` is still writing and takes the signal. Measured
  in `debian:bookworm-slim` — 0, 10 and 200 extra files pass, 2000 fails — so it
  would have blocked the upgrade on essentially every install with a real
  recordings volume. It is invisible on macOS, where bsdtar exits 0 on SIGPIPE,
  which is how it passed local testing.

  Now listed once into a variable and tested with herestrings. Not
  `printf | grep`, which takes SIGPIPE just the same and would have moved the
  bug rather than removed it. ([#347](https://github.com/rainmanjam/polyemesis/issues/347))

- **`hevc_vaapi` was offered and could not start.** `encoderProfiles` carried
  six of the twelve encoders the product lets you pick, so the other six fell
  through to an unknown-encoder branch. For most that meant missing tuning. For
  `hevc_vaapi` it was fatal: the branch left `prof.vaapi` false, so the command
  line got neither `-vaapi_device` nor `format=nv12,hwupload`, and VA-API
  encodes from GPU surfaces. It is never probed, so it was never greyed out
  either — selectable in the editor, dead at go-live.

  All twelve now carry a profile, and the values were read off real encoder
  option tables rather than inferred. **EXPERIMENTAL: an option table is not
  silicon.** `ffmpeg -h encoder=<name>` is answered from the binary and returns
  the same text on a machine with no such device in it, which is the machine
  every one of these twelve was read on — so no NVENC, QSV or VA-API encode has
  been observed running with the flags below. The `libx264`/`libx265` rows are
  exempt: that argv predates renditions. Nothing is disabled by the label; a
  flag that is wrong surfaces as an encoder that refuses to open, which the
  start gate already reports by name. That mattered: **`-profile:v high` is
  H.264 only, and an HEVC encoder does not ignore it, it refuses to open**
  (`x265 [error]: unknown profile <high>`). Copying the existing rows across —
  the obvious fix — would have broken every HEVC encode instead of fixing one.

- **Capped VBR did nothing on NVENC. EXPERIMENTAL: the fix has not been
  confirmed on an NVIDIA card.** The whole effect of this change is on an argv
  nobody has watched NVENC accept — the reasoning below is from FFmpeg's option
  tables and source, on a host with no GPU. `-rc cbr` was appended unconditionally,
  immediately before `-b:v/-maxrate/-bufsize`, so a rendition with a ceiling
  above its target still ran constant-bitrate and the operator's number was
  inert. The mode is now chosen from what was asked for, and emitted before the
  rest so an install that sets neither field gets a byte-identical command line.

  The other four families were checked and need no change, which is worth
  recording so nobody re-derives it: QSV has no `-rc` at all and infers the mode
  from `-b:v` against `-maxrate`, VA-API's `-rc_mode` already defaults to `auto`,
  and VideoToolbox is capped-VBR unless `-constant_bit_rate` is set.
  ([#341](https://github.com/rainmanjam/polyemesis/issues/341))

- **A failed release build could leave container tags with no release behind
  them.** The image and binary jobs ran in parallel, so a failure in the second
  left Docker Hub and GHCR already carrying `:latest` with no GitHub Release, no
  checksums and no SBOM. Because `install.sh` pins compose to `:latest`, that
  split the two install modes: docker operators would take the new version
  immediately while binary installs stayed put. The images now wait for the
  binaries.

- **Two pages stated the opposite of what the code does.**
  `docs/PLATFORMS.md` said `moderation:ban` was "deliberately not requested:
  nothing in polyemesis bans or times out a viewer" — while `internal/oauth`
  requests it and automod's action matrix carries both. That was not drift but a
  decision the page never caught up with: the scope was omitted on purpose, the
  omission was reversed when moderation shipped, and the code kept the original
  argument deliberately "because it is the argument to re-read if the decision
  is ever revisited". The page kept only the superseded half, which is the worst
  error available on that page — an operator reads it, connects an account, and
  is shown a permission the documentation promised would not be asked for.

  `docs/HARDWARE.md` said `libx264` stays the default on a machine with a
  working GPU and that "hardware is an opt-in you make deliberately".
  `DefaultVideoEncoder` returns the first encoder that **passed the probe**,
  hardware first. `docs/RENDITIONS.md` had it right all along, so the two pages
  contradicted each other with nothing to tell a reader which to believe.

- **`docs/UPGRADING.md` said nothing about the change that matters most to a
  restore.** No 0.7.0 section, and no mention of `secret.key` anywhere across
  the page — for the release that makes that file the difference between a
  restore and a silent outage. It also never mentioned `update.sh`, so operators
  were steered to the hand-rolled procedure rather than the guarded script
  `install.sh` writes. It now carries the 0.7.0 note, the one-way warning, the
  key check in *Rolling back*, and a `keyUnreadable` check in *Verifying* —
  because a restore that omits the key file looks completely successful until
  go-live.

- **`docs/ENCODING.md` overcounted the encoders twice in one sentence.** It
  claimed twelve hardware encoders "each probed with a real test encode": ten are
  hardware, and six are probed, with the HEVC verdicts inferred from their H.264
  siblings.

- **A stop arriving mid-spawn signalled nothing, and the child ran on
  unsignalled.** `Start()` publishes `p.running` and returns, but `p.cmd` is not
  set until `runOnce` has built the command, opened its pipes and had
  `cmd.Start()` return. A `Stop` landing between those two points took its
  `p.running` arm, cancelled the supervise context, and called `terminate()` —
  which found `p.cmd == nil` and returned having sent no signal and armed no
  escalator.

  `p.cmd == nil` meant two different things there, "no child yet" and "the child
  is already reaped", and nothing could tell them apart. `runOnce` uses
  `exec.Command` rather than `exec.CommandContext`, so the cancelled context
  killed nothing: the child was spawned behind a stop that had already given up
  on it, `cmd.Wait()` blocked on a process nobody had asked to leave, and `Stop`
  waited out its entire deadline before a `SIGKILL` it did not wait for. Found
  while investigating #126's one lead — a `teardown 12001.831ms STOP DEADLINE`
  that turned out not to be a slow child but a child nobody ever asked to
  leave. ([#126](https://github.com/rainmanjam/polyemesis/issues/126),
  [#330](https://github.com/rainmanjam/polyemesis/pull/330))

- **A binary or systemd install gets a guarded `update.sh` too, and both modes
  now check for `secret.key`.** `write_helper_scripts` opened with
  `[ "$MODE" = "docker" ] || return 0`, so the generated `update.sh` existed
  only for Docker. Docker operators got a script that refuses to upgrade when
  the backup would be a lie; systemd operators got the same procedure written in
  `UPGRADING.md`, where nothing checks whether they ran it or whether it worked.
  Migrations are forward-only in both modes, so the install that most needed the
  guard rail had the least.

  Neither mode checked for `secret.key`. Counting entries proves an archive is
  not empty; it does not prove it holds the one file whose absence cannot be
  recovered from. Now that destination stream keys are sealed at rest, a
  database restored **without** `secret.key` comes back with every destination
  disabled — correctly, but the restore reads as completely successful until
  someone goes live.

  Writing the test found a bug in the script itself: `cp -a src dest` **nests**
  when `dest` exists, so a second run inside the same minute produced
  `data.bak-<stamp>/data/` and then looked for `secret.key` in the wrong
  directory. It now refuses when the destination exists rather than guessing
  which of the two the operator meant — found by running the generated script
  twice, which is what a real operator does after a failed upgrade.
  ([#347](https://github.com/rainmanjam/polyemesis/issues/347))

- **The first-run screen used four colour tokens from the marketing site's
  palette, not the application's.** `border-border`, `bg-card-raised/40`,
  `bg-warn` and `bg-border` exist in `web/src/styles/global.css` and not in
  `ui/src/index.css`, so each resolved to nothing and the affected elements
  rendered unstyled. Caught by a UI test that was twice misread as flaky before
  it was believed.
  ([#352](https://github.com/rainmanjam/polyemesis/pull/352))

- **Cloudflare Workers appends header values rather than replacing them, so a
  `no-cache` default defeated every immutable asset rule.** Every fingerprinted
  bundle was being revalidated on each load despite carrying a correct
  `immutable` directive of its own.
  ([#334](https://github.com/rainmanjam/polyemesis/pull/334))

- **A playlist item whose file was refused after the fact said "not yet queued
  for normalisation" for ever.** The *Media re-check* above is the first thing
  in this product that can refuse a file which was already accepted, and the
  playlist had nowhere to put that answer: `GET /failover/playlist` reported
  the queue state it found, so an item whose source can never normalise showed
  the amber "working on it" reading, and the operator waited on a job that
  fails and re-queues on every save.

  An item with no derivative now names the refusal and its reason, and says
  that sending the same file again will not change it — the remedy for a
  refusal is a different file, not patience.

  **An item that is already on air keeps playing.** Its derivative was
  transcoded from those bytes and is intact, so pulling it off air would black
  out a running programme to report something the operator can act on whenever
  they like. It stays `ready`, and the finding arrives as a new `warning`
  field beside it. Readiness answers "may this go to air"; a re-inspection of
  the source is not an answer to that question once a derivative exists.
  ([#273](https://github.com/rainmanjam/polyemesis/issues/273))

- **A pull source could name an upload this server was never allowed to
  inspect.** An upload's `pullUrl` is offered copyable in the Library, and
  pasting it into **Settings → Ingest → Pull** handed the path to the engine's
  own FFmpeg — which is not the inspection path, and carries neither the format
  allowlist nor the protocol pin. So a file whose inspection a dropped
  connection cut short could be routed to air with nothing having read a byte
  of it.

  Saving a pull source — primary or backup — that names an upload **recorded as
  unchecked** is now refused, with the file and the reason in the message. It is
  the same test a playlist item already gets, scoped the same way: only a URL
  this save *changes* is checked, so an unrelated settings change is never
  refused over a source configured before the gate existed. An upload with no
  record at all — every file stored before inspection existed — is still
  allowed, because refusing those would strand media an operator has had for a
  year.

- **A killed upload left bytes on your disk that nothing would ever remove.**
  Nothing in the product swept the uploads directory. A staged file stranded by
  a process that died mid-transfer, or by a cleanup that failed, stayed
  forever — invisible and unreferenceable, but occupying up to 8 GiB of the
  volume the database, the recorder and the HLS preview share, with the
  free-space floor unaware of it and nothing anywhere reporting the total. The
  same for an inspection record whose upload was removed out of band.

  Startup now clears both, once, before anything can accept an upload, and
  **logs what it removed and how much space came back** — the boot after the
  crash is the one where that line matters. Only leftovers older than an hour
  are touched, so a sweep can never race a transfer that is still arriving.

- **An upload was briefly listable as an empty file while it was being
  published.** Finalising an upload reserved its final name with an exclusive
  create, which is what makes two uploads drawing one name a loud failure
  instead of one operator's bytes silently replacing another's. But the
  reservation was a zero-byte file *under that final name*, so until the bytes
  were renamed into place `GET /api/v1/media` showed an empty row with a working
  `pullUrl`, and a settings save would accept it as a playlist item. The window
  was a file write wide on every platform and tens of milliseconds on Windows.
  The reservation now uses a name the listing refuses, so nothing is ever
  visible under the final name except the finished file — and a name that is
  genuinely already taken is still refused.

- **A media record could be written for a name no upload has.** The call that
  records what was found in a file checked only that the name had no path
  separator, so any other string wrote a sidecar that nothing in the product
  ever reads or removes. No caller could produce one, which is exactly why the
  contract was narrowed now: it refuses a name that is not a listable upload,
  and a name with no file beside it.

- **A destination whose FFmpeg had already died could be reported as running
  indefinitely.** The supervisor waited for a child's output pipes to reach
  end-of-file before reaping the child itself — and a pipe reaches end-of-file
  when the *last* writer closes it, not when the process you started exits.
  FFmpeg spawning a helper that outlives it, or anything the child forks and
  does not wait for, inherits those pipes and holds them open. The supervisor
  was then waiting on a process it never started: the destination stayed green
  on the dashboard, the restart policy never fired, and a stop of that
  destination was bounded by the lifetime of a grandchild rather than by any
  timeout.

  The child is now reaped on its own, and the drain that captures its stderr is
  bounded afterwards. The tail of stderr that becomes a destination's error
  message is unchanged on the ordinary path.

- **Stopping a destination cost a sleeping goroutine per stop, for eight
  seconds each.** The escalation that kills a child which has not heeded the
  shutdown signal waited out the full grace period on every stop, including the
  overwhelming majority that finish in milliseconds. Stopping forty
  destinations left forty goroutines parked, each holding a reference to a
  process it still intended to kill. The escalation now returns as soon as the
  child is reaped.

- **`POST /destinations/{id}/stop` said `"stopped"` whether or not the process
  actually stopped.** The supervisor reports the same state on both outcomes:
  the one where the child was reaped, and the one where the shutdown deadline
  expired, `SIGKILL` was sent, and nothing waited to see whether it worked. The
  second means a process that may still be running is still holding — and still
  publishing to — the relay port and the stream the response has just declared
  free.

  A stop now also answers `reaped`, plus a `warning` saying what is uncertain
  when it is `false`. Nothing about the `state` field has changed, so existing
  callers are unaffected.

- **A `read` token could read a pull URL's credential from `GET /sources`.** The
  masking ran, and masked the wrong part: it blanked only the last path segment
  and only for `rtmp`/`rtsp`/`srt`-like schemes, so an HLS pull URL over `https`
  came back untouched and one with the credential mid-path had its *filename*
  masked instead. Redaction for a read-scoped reader now masks every path
  segment and every query parameter, on every scheme. A `read` token sees the
  host and nothing else; a session and an `admin` token are unaffected.

- **A `read` API token no longer reaches the live media of a stream you have not
  published.** `read` means metadata, not content, and live playout is content.

  The three playout routes sit outside every authenticated group by necessity —
  a viewer has no account — so the scope check runs per request in the handler,
  and it was asking the wrong question: it resolved the caller and then threw
  the principal away, so any valid bearer was treated as the operator previewing
  their own private stream. A `read` token is now treated exactly as an
  anonymous viewer on `/playout/*`, `/playout/public` and `/playout/poster.jpg`
  — the same status, the same body, the same headers.

  Two consequences worth knowing before you upgrade. On a stream with
  `Public: false`, a `read` token now gets `404` where it used to get the media.
  On a published stream protected by the playback token, a `read` token that
  does **not** present that playback token now gets the `401` challenge instead
  of being served; presenting it in `?t=`, the header, the cookie or as a basic
  password works as it always did. Anonymous viewers, the signed-in console and
  `admin` tokens are unaffected, and an open stream is unaffected for everyone.

- **The "Partly bound" badge on a source card now appears only when there is
  something to fix.** A wildcard SRT listener binds one socket per address
  family and survives one of them failing, which on an IPv4-only host — a
  container with IPv6 disabled — meant a permanent orange badge on a perfectly
  healthy install. The badge now distinguishes an address family this machine
  does not have from one it has and could not bind, and only warns about the
  second. The log line is unchanged and still records both, because whoever is
  working out why an encoder will not connect needs the first one too.

- **`POST /routing/presets/{preset}` now applies the defaults it advertises.**
  A request with no body used to run on Go's zero value for every track index
  rather than on the OBS-convention defaults `GET /routing/presets` publishes
  under `defaults`, so a bodyless `mic-only` compiled `[0:a:0]` — the full mix —
  where the catalogue two routes away said track 3. Both routes answered `200`
  and neither disagreed out loud.

  **Behaviour change worth knowing before you upgrade.** A bodyless apply of
  `mic-only` now selects track 3 instead of track 1, and `clean-feed` track 2
  instead of track 1. Anything that sends a body with a Content-Length —
  including the console, which always sends the full option set — is unaffected.
  A body is still a FULL REPLACEMENT rather than a patch over the defaults, so a
  partial body means exactly what it always meant: an omitted field is zero.

  Two smaller changes fall out of the same fix, and the first paragraph used to
  imply neither happened. A **chunked** body now takes effect: it reports
  `Content-Length: -1` whatever it carries, so the old gate discarded it and
  applied the defaults. And a **whitespace-only** body is now treated as absent
  (200, defaults) where it used to be `400 invalid request body: EOF` — it is
  not a "full replacement", because there is nothing in it to replace with.

- **Three more values a `read` token could read.** The automod model endpoint
  when it carries an `?api_key=` in the query string, and a destination's
  `extraInputArgs` / `extraOutputArgs` — which are the resolved FFmpeg argv, the
  same bytes `GET /destinations/{id}/expert` already refuses that token for.

- **A `kind: file` destination no longer loses its filename for a `read`
  token.** That field is a filename rather than a URL, and running it through
  the URL masker replaced the whole thing with `[redacted]`.

- **Redacted fields keep their JSON key.** Four fields carry `omitempty`, so
  blanking them removed them from the document entirely and a `read` token's
  response was a different shape from an admin's. They now come back as
  `[redacted]`.

- **`Vary` on principal-dependent responses now names `Cookie` as well as
  `Authorization`.** The signed-in operator — the principal that receives the
  unredacted body — arrives in a cookie, so a shared cache keyed on
  `Authorization` alone filed their response under the same key as every
  anonymous caller's.

- **Deleting a completed clip export left its file on disk for ever.** All three
  delete paths -- the single-job delete, the bulk purge and the unattended
  scheduled sweep -- removed the job row and left the exported media behind,
  unreferenced and invisible, occupying the volume the database and recorder
  share. The file now goes with the row.

- **Staging or rolling back the binary now raises an audit event.** Both were
  logged and neither was audited, so the one question an operator asks after an
  unexpected version change -- *was that me?* -- had no answer in the place they
  would look.

- **Masked URL segments came back percent-encoded.** A redacted path segment in
  a `/sources` or `/settings` body rendered as `%5Bredacted%5D` rather than
  `[redacted]`, because the mask is written into the URL and `url.String`
  escapes its brackets. Cosmetic, but it landed in the one place a reader is
  least equipped to tell a deliberate omission from a bug.

- **The startup banner no longer prints an ingest port in pull mode.** Pull
  dials out and has no inbound port, so `ingest      pull (port 6000)` read as
  "point the encoder at 6000" — an instruction that can never work, and one
  that sends you to your firewall to debug a port that was never in the path.
  It now reads `pull (dials out; no inbound port)`.

- **Deleting a destination could end a broadcast this process never put on
  air.** The coordinator decided from the platform's answer alone; the phase it
  had actually confirmed was recorded and never read. They diverge in the case
  the code calls "the somebody-else's-broadcast story" — a broadcast started in
  YouTube Studio, or carried through an upgrade — where the platform says live
  and polyemesis never transitioned it. On YouTube `complete` is terminal.

- **Start and stop reported the wrong thing on a multi-source install.** The
  effect was read back from the DEFAULT engine only, so a destination belonging
  to another programme was simply not found: a failed enable reported as
  started, and a stop that left a process holding the port reported as clean.
  Invisible on a single-source install, which is every development box.

- **The debug bundle's size cap counted the wrong bytes.** It measured raw
  string length while JSON writes six bytes for a control character, so the
  ceiling the package states about itself was wrong by a factor of six —
  785,437 bytes against an asserted 393,216. Measurement, cutting and every
  budget deduction now run in encoded units.

- **Attributes logged inside a group reached the bundle as `{}`.** The key names
  survived and every value vanished, which reads as "the field was blank" rather
  than "the recorder cannot represent this".

- **YouTube's broadcast-create advice was dead on the real path.** It matched
  against a truncated body, and its own comment says the reason code sits past
  that cut — so an operator whose channel is not enabled for live streaming got
  the raw snippet instead of the sentence naming the button.

- **The secret key file was created with a Unix file mode alone.** The load path
  has always restricted it properly; the branch that MINTS the key did not, so
  on Windows the file it had just generated was readable by everyone.

- **A supervisor restart could leave a live child unreachable.** The teardown
  cleared the process handle unconditionally, so a predecessor still unwinding
  could blank a successor's — after which `terminate()` and `kill()` could not
  find the new child, and it kept running and kept publishing to a destination
  the operator believed was stopped.

- **Three published pages were missing from the sitemap.**
  `/multistream`, `/twitch-vod-track` and `/vs/streamlabs` were built and served
  but absent from the `lastmod` map.

- **A credential in an unrecognised attribute reached the debug bundle
  verbatim.** The recorder's scrub walk handled `string`, `[]string`,
  `map[string]any`, `error` and `fmt.Stringer`, and passed everything else
  through untouched — its comment asserted that what reached that branch
  "cannot carry a credential without having been formatted first".
  `slog.Any("detail", map[string]string{"token": key})` reaches it. Neither the
  declared secret set nor the residual `alerts.Redact` pass ever saw the value,
  and it travelled into a file intended for somebody who does not have the
  operator's server. Reproduced in four shapes — a map of strings, a slice of
  any, a struct, and a map of slices. Unrecognised values are now rendered and
  scrubbed before capture.

- **The debug ring counted its records and never weighed them.** The buffer
  bounded the record COUNT at 5,000 and nothing bounded their size, so a single
  enormous log line made the export unbounded — and the browser buffers that
  file whole, twice, in the tab of the person trying to report a fault. Each
  record now has an 8 KB budget, spent in a fixed order, applied AFTER the
  scrub so a cut can never strand the front half of a stream key as text the
  secret set no longer matches. The bundle states its own size and how many
  lines were shortened, and the confirmation dialog shows both before anything
  is sent.

- **Four of six pages were shipping with no cache revalidation.**
  `web/public/_headers` scoped `no-cache` to `/` and `/*.html`, and its comment
  claimed that covered the built pages. It did not: with
  `build.format: "file"` and `trailingSlash: "never"` a page is BUILT as
  `features.html` and SERVED as `/features`, and Cloudflare matches the request
  path. The failure that block exists to prevent — "a deploy is invisible until
  caches expire" — was live on `/features`, `/comparison`, `/docs` and
  `/download`. A build check now derives the expectation from the built output.

- **114 Copy buttons did nothing.** The click handler shipped inside
  `CodeBlock.astro`, which the rendered documentation pages never mount. Styled,
  focusable, labelled and inert.

- **Eleven copy defects**, found by three independent reviewers with different
  lenses. The ones that mattered were claims about other people's products:
  a sentence whose "either" retroactively negated the clause before it, turning
  a correct statement into the overclaim it was written to avoid; a wrong
  restream.io plan limit that contradicted a sibling page; four capability cells
  asserted "No" for competitors with nothing behind them; and a card presenting a
  DESCRIPTION of a function in the quoted-source treatment that gives its
  neighbours their authority.

  Also removed: a per-destination CPU figure that appears nowhere but in prose,
  and a claim that the second Twitch audio track works, which
  `docs/AUDIO-ROUTING.md` marks EXPERIMENTAL because no broadcast has ever been
  published through a key Enhanced Broadcasting minted.

- **An engine test asked for three contiguous UDP ports and checked one.**
  `testenv.FreeUDPPort` probes a port, releases it, and returns the number, so a
  three-port allocator built on it had two numbers nobody had checked and one
  that was already nobody's. It failed CI three times in one day on three
  different ranges, each time blaming the code under test for a port it never
  held. `testenv.FreeUDPWindow` reserves the window and holds it.

- **The installer offered to fix FFmpeg only for the hosts that did not need
  it.** `install.sh` has always known how to fetch a current static build and
  verify it carries libsrt before displacing anything — and offered that only
  when your FFmpeg was 6.x or 7.x, where it buys one feature. A host **below
  the 6.0 floor, or with no FFmpeg at all**, got an error and a reading list,
  one item of which was "a static build with libsrt" linking to the very
  releases page the script downloads from. It now offers. Declining is what
  ends the install, not the old version.

  The same gap was one branch over: an FFmpeg can clear 6.0 and carry **no
  libsrt** (Homebrew's does), which costs per-destination audio routing
  entirely, and that branch also told you to compile one yourself.

- **PATH order no longer decides which FFmpeg runs.** When the installer
  installs FFmpeg itself it pins the absolute path into the config it
  generates, so `/usr/bin` winning the PATH cannot silently give the service
  the older binary.

- **A release with no published `SHA256SUMS` is refused rather than installed.**
  A checksum *mismatch* already died; an **unverifiable** download warned and
  installed as root. `--allow-unverified` keeps the escape hatch as something
  you type.

- **YouTube's error advice never fired.** Both advice functions matched against
  the response body *truncated at 300 characters for display*, and a realistic
  YouTube 403 is 552 bytes with the machine-readable reason at index 448 —
  Google puts a long human message before `errors[]`. So the sentences written
  for "this channel is not enabled for live streaming" and for the concurrent
  and shared-ingestion quota limits could not be reached. The quota ones are
  what an operator hits at the moment they are trying to go live.

- **A taken port is offered a free one** during the interview, instead of the
  installer predicting a bind failure and then causing it.

- **An automod rule that asked for a timeout with no duration banned the viewer
  permanently.** Every adapter reads a zero duration as "forever" — that is the
  documented contract — and the executor passed the configured seconds straight
  through. A rule saved without `timeoutSeconds` therefore removed a viewer for
  good and logged it as a successful timeout. Saving such a rule is now refused,
  and the executor clamps at the point of use, which is what protects the rules
  already stored.

- **Retention deleted the subtitles of recordings that still existed.** The
  sweep identifies an orphaned transcript by walking the filename backwards and
  testing prefixes that end at a dot. Transcripts are written one file per track
  named after the speaker, joined with a *hyphen*, plus a merged `-all` file — so
  a transcript of a surviving recording never matched and was removed. The
  `.json` transcript does end at a dot, so it survived: the sweep kept the
  machine-readable file and deleted every human-readable subtitle track for a
  recording sitting in the library. Hyphens are now prefix boundaries too, which
  only ever keeps more files.

- **Cancelling a transcode could hang for ever.** The child is killed when the
  context ends, but a grandchild — x265 and SVT-AV1 both spawn their own workers
  — can hold the output pipes open after its parent is gone. The wait delay that
  exists for exactly this only takes effect inside the process wait, and the
  reader goroutines were waited on first, so the readers never saw end-of-file,
  the wait was never reached, and the delay never applied. Measured: the previous
  ordering was still blocked 25 seconds after cancellation; it now returns in
  about 300ms.

- **Every reconnect published a spurious destination-down webhook.** The
  ten-second dwell that absorbs a supervisor reconnect was declared, documented,
  and never applied: a zero dwell — which the configuration documents as "takes
  every default", and which is what the engine constructs — fell through the
  clamp for negatives without picking the default up. Any subscriber therefore
  received a down-and-up pair for every restart.

- **A library with more than 32,766 recordings rendered as if it were empty.**
  Four queries bind one parameter per recording from an unbounded list; past
  SQLite's limit the statement fails, and the handler substitutes an empty map.
  Every session then showed no members and no poster, every recording fell into
  the ungrouped list, and every title, description and tag disappeared — while
  the page still returned 200. Nothing had been lost and there was no way to
  tell that by looking. The queries are chunked, and the metadata failure is now
  logged like its two siblings.

- **Refiling several recordings at once left the session they came from
  reporting recordings it no longer had.** The bulk edit steals each recording
  from its previous session but only recalculated the destination. The
  one-at-a-time path already recalculated both, and said why.

- **The automod API key, model timeout and spend counters were stored but never
  read.** Setting the key returned "configured" without rebuilding the model
  checker, so it took effect only at the next restart and a rotated key kept
  sending the old one; the configured model timeout was dropped when the engine
  was built, so every model-decided timeout ran at the built-in default; and the
  statistics endpoint returned a hardcoded zero after the wiring it was waiting
  for had landed, which an operator watching their model bill reads as "nothing
  has been spent".

- **An internationalised hostname could never obtain a certificate.** The
  configured name was compared against the incoming SNI after lowercasing only,
  but a client sends the punycode form while the operator configures the
  readable one — two spellings of a single name, never equal. The policy refused
  to request a certificate for the only hostname it was configured with.

- **A Windows build could abort with `fatal error: found pointer to free object`.**
  The job-object teardown took the address of a local struct, converted it through
  `unsafe.Pointer` to `uintptr`, and handed that to `SetInformationJobObject`. A
  `uintptr` is an integer as far as the garbage collector is concerned, so nothing
  kept the struct alive across the call and the collector was free to reclaim it
  while the kernel still held the address. `runtime.KeepAlive` now bounds the
  lifetime at each of the three sites.

  **No vet had ever run for Windows**, which is why a class of defect the tool
  reports by name went unreported: every vet invocation in CI ran for the host
  platform, and `_windows.go` files do not compile there. `GOOS=windows go vet
  ./...` now runs on every code change, so the next one fails a check rather than
  a customer's stream.
  ([#440](https://github.com/rainmanjam/polyemesis/issues/440))

- **The generated systemd unit contained the output of `ps`.** The unit template
  is a heredoc, and one line used a backticked `ps` where it meant to name the
  command in prose. The shell ran it while writing the file, so the unit grew from
  40 lines to 215, 176 of them a process listing spliced into the middle of a
  service definition. `systemd-analyze verify` prints `Missing '='` for those
  lines **and still exits 0**, so the check that was supposed to catch this
  reported success; it is now the printed output that is asserted on, not the exit
  code.

- **Binary mode did not get the guard rails docker mode already had.** Its helper
  scripts were written in only one of the two branches that need them, and an
  interrupted install could run its rollback twice. There was also no uninstall
  script for binary installs at all — so the documented way to remove polyemesis
  worked for one installation method and silently did not exist for the other.

### Testing
- **The Windows uninstaller had never been parsed by anything.** No workflow
  referenced `deploy/windows/` at all, so a syntax error in a script that ends
  live broadcasts would have shipped undetected. It is now parsed and exercised
  on `windows-latest`, inside the existing required job rather than a new one,
  since branch protection names a fixed set of contexts and a new job would never
  gate a merge.

  The gate found a defect immediately: the live-broadcast check returned its
  results through a bare pipeline, and a pipeline that matches nothing emits
  nothing — so "no encoder is publishing" and "the process table could not be
  read" arrived as the same value. The caller treats the second as fatal, so on
  an idle host every uninstall refused with a message about the wrong thing, and
  the confirmation prompt was unreachable. It failed safe, but it did not work.
  (#509)
- **A test that asserted a state it could not reach, and quarantined itself
  instead of failing.** `TestASilenceTierLiftsTheHoldOnAnUnmeasuredLayout`
  exists to make the second term of `holdDests := !measured && silenceSig == ""`
  load-bearing — its own comment says deleting that term would otherwise pass
  every test in the file. Its setup asked for a silence tier on an unmeasured
  video-only probe with the slate off, and neither path to a non-empty
  `silenceSig` is open in that state: `wantSilence` returns "" unless `measured`,
  deliberately, and `holdSilence` only carries a frozen signature while the
  selector is off the primary. Verified by deleting the term, which passed.
  Rebuilt on the one state that does reach the branch — selector on the slate,
  signature frozen, layout unmeasured, which is an ingest that dropped out
  mid-event rather than one that never arrived — and it now asserts it got there
  before asserting anything else. `internal/engine` reports no quarantined
  tests. (#161, #486)
- **A supervisor warning failed an unrelated test, twice.** The second sweep in
  `TestEnsureFeedHolds...` asserted the log buffer was EMPTY, meaning to say the
  refusal was not repeated. `buf` is wired to `e.log`, the whole engine's
  logger, and the engine runs a source whose binary cannot start, so its
  supervisor retries on a backoff and logs "process exited" for the length of
  the test. Any retry landing between Reset and String failed it and then
  reported that WARN as a refusal storm. Narrowed to the refusal itself.
  (#474, #484)
- **flake-rate can run the Go mode under `-race`.** The Windows corruption in
  #440 is the only place the crash appears, and `ci.yml` runs `-race` on ubuntu
  alone, so no required job could see it. (#482)
- **Ten UI-drift guards, recovered from a branch that was never merged.** They
  check that the React frontend still offers what the Go side expects — the
  Facebook crosspost and donate controls, the destination dialog's save payload,
  the backup-ingest toggle, the card's link to a scheduled broadcast, the
  header's one question about being live, and the composer's tag and compliance
  pushes. `internal/db/limits_drift_test.go` and
  `internal/oauth/capabilities_drift_test.go` already established the pattern;
  these are eight more of it, plus two in `internal/oauth`.

  **They are here because each one was watched to fail, not because they
  passed.** All ten passed on recovery, which this release has learned means
  nothing on its own: a mutation was applied to the source each guard names, the
  test was run by its full name with `-v` so a mistyped filter could not report
  `[no tests to run]` as success, and the file was restored from a backup with
  `git diff` confirmed empty. One example of the class: deleting
  `id="dest-fb-donate"` kills the crosspost guard, and wrapping the block in
  `{false && …}` kills it too — which proves the guard bounds the block rather
  than grepping the whole file.

  **Two real defects were found in the recovered code and fixed before it
  landed.** A source-grep guard passes forever if the string it looks for
  survives in a comment: deleting the real code in `AppLayout.tsx` and leaving
  its text in a `// was: …` comment kept one test green.
  `facebook_ui_drift_test.go` already shipped `stripJSComments` for exactly that
  and documents it at length; the sibling file in the same package was not using
  it. And one guard sliced from an unguarded `strings.Index`, so a rename
  produced a slice-bounds panic instead of a readable failure.

  Recorded rather than fixed: the two `internal/oauth` guards remain
  comment-defeatable, because that package has no comment stripper and copying a
  forty-line helper between packages is the wrong repair. Promoting `readUI` and
  `stripJSComments` to a shared test helper is the follow-up.
  ([#367](https://github.com/rainmanjam/polyemesis/pull/367))

- **Five acceptance suites that talk to a real far end**, where before this
  release exactly one did. The argument for them is their hit rate rather than
  their theory: each of the first three found a defect on its first live run,
  and none was reachable by a unit test, because on each side of the boundary
  both halves were individually correct. What was wrong was the composition, and
  only a real far end refuses a bad composition.

  What they settled, and what it cost:

  | Suite | Needs a credential? | Found |
  |---|---|---|
  | `acceptance-chat.sh` | no | opens a real socket to each platform and asserts the specific refusal — DNS, TLS with the right SNI, and the line protocol, everything except a valid login ([#316](https://github.com/rainmanjam/polyemesis/pull/316)) |
  | `acceptance-oauth.sh` | 28 of 46 checks, no | every provider's OAuth surface is public, so discovery documents, advertised grant types and PKCE methods are all comparable against what `internal/oauth` hardcodes ([#322](https://github.com/rainmanjam/polyemesis/pull/322)) |
  | `acceptance-automod.sh` | no | the model endpoint's own key in `server.log` and in the operator's spend panel — `net/http` puts the request URL verbatim into `*url.Error`, and the redaction reasoning had been applied to the settings blob and nowhere else ([#324](https://github.com/rainmanjam/polyemesis/pull/324)) |
  | `acceptance-transcribe.sh` | no | three defects against the real model host ([#323](https://github.com/rainmanjam/polyemesis/pull/323)) |
  | `acceptance-hooks.sh` | no | needs a listener we control, not a platform account ([#325](https://github.com/rainmanjam/polyemesis/pull/325)) |

  The rule this yielded, recorded because it overturned the plan that preceded
  it: **ask what the far end will tell a stranger before assuming the test needs
  an account.** The note had assumed chat and OAuth had to wait for a connected
  account; between them forty-three checks run with no credential at all.

  Still open, and honestly so: nothing proves an OAuth refresh **succeeds**, and
  no chat suite performs a valid login. Both steps are written and skipping
  until an account is connected, so the hour-four token failure remains bounded
  from one side only.

- **A variable-frame-rate source keeps its timing through a rendition, and now
  fails the build if that changes.** `-fps_mode` and `-vsync` are deliberately
  never set, so a VFR source — screen capture, some phone encoders — passes
  through with its presentation timestamps intact. Measured on a fixture that is
  30 fps nominal and about 18 fps actual: 72 frames over 3.967 s in, 72 frames
  over 3.967 s out. Forcing CFR was measured too, and is worse: the same fixture
  becomes 120 frames, 48 of them duplicates filling gaps where the source had
  nothing to say.

  One measurement trap is recorded with it, because it produced a false alarm
  first: ffprobe reports a **uniform** `duration_time` for every frame of an
  MPEG-TS, because the container does not store per-frame durations and ffprobe
  derives them from `r_frame_rate`. Read that field and a VFR stream looks like
  it was silently resampled and lost a fifth of its running time. It was not.
  ([#342](https://github.com/rainmanjam/polyemesis/issues/342))

- **Three suites that existed and ran nowhere are now scheduled**, which is the
  gap this release keeps finding: a suite that is never executed is
  indistinguishable from one that was never written, except that it reads as
  covered.

  `acceptance-obs-multitrack.sh` had already established that OBS 30.2.3 sends
  no multitrack audio at all — a negative six documents now depend on — and
  getting it onto a runner surfaced two defects of its own. **The observer had
  no floor:** below FFmpeg 7.1 multitrack FLV does not demux, so on Ubuntu's
  stock 6.1.1 the suite reported one track whatever OBS sent. A green run on a
  host that could not have produced any other answer. It now proves the floor
  with a two-track round trip rather than a version string. It was also the last
  host suite not using the shared teardown, and running it twice back-to-back
  bound `:1935` while the previous run still held it — reporting "the RTMP
  listener never bound", a teardown bug wearing the costume of the product
  failure the suite exists to detect.

  `acceptance-transcribe.sh` checks twenty hardcoded claims about a model host
  nobody here operates — ten filenames to URLs and ten byte counts gating
  `VerifyModelFile`. Those can rot with no commit of ours, so they need a clock
  rather than a push.
  ([#355](https://github.com/rainmanjam/polyemesis/pull/355),
  [#356](https://github.com/rainmanjam/polyemesis/pull/356))

- **The rate-control test runs the whole path, because every individual piece
  already worked.** Asserting on `RenditionSpec` alone would have passed before
  #341 was fixed, so the test drives a stored row through the mapping and into
  the argv the encoder is actually started with.
  ([#354](https://github.com/rainmanjam/polyemesis/pull/354))

- **The upgrade guard that stops an install eating `secret.key` had no test**,
  and `acceptance-install.sh` turned out never to have run in CI at all.
  ([#353](https://github.com/rainmanjam/polyemesis/pull/353))

- **The partial-bind test reserved one half of a port and hoped for the other.**
  It now reserves udp4 first and retries, and uses `s.Report()` to distinguish a
  lost race from a real regression rather than reporting both as failure.
  ([#339](https://github.com/rainmanjam/polyemesis/pull/339))

- **A respawn gate that waited on a feed which had not started yet.**
  `StateStopped` meant both "never started" and "died", so the gate could not
  tell them apart — the same conflation that produced the mid-spawn stop above,
  on the respawn path instead of the teardown one.
  ([#290](https://github.com/rainmanjam/polyemesis/issues/290))

- **Two guards that read `Dashboard.tsx` as text are now browser tests that
  drive it.** `internal/oauth/composer_tags_drift_test.go` proved that the
  substring `tags` appeared within 400 characters of the push `fetch`, and that
  `withCompliance.length > 0` was written somewhere in the file. Neither can
  tell whether the component compiles, mounts, or does the thing it names — the
  #107 defect, one package over. They are replaced by `ui/e2e/go-live-composer.spec.ts`,
  which types into the tags input, clicks Push and reads the body off the wire,
  and which drives the compliance notice and the Push button through both the
  stored and not-stored cases. The stated blocker was re-tested rather than
  assumed: the composer decides everything from `GET /metadata`, so a stubbed
  response reaches every branch with the real component and no OAuth fixture.
  The Go file is deleted. Browser suite 92 → 97.
  ([#259](https://github.com/rainmanjam/polyemesis/issues/259))

- **A required test reached a third party's live servers, and went red when they
  answered differently.** It constructed `Options{}` — the zero `oauth.Set`, which
  resolves to the platforms' real hosts — so it called `id.kick.com` and depended
  on Kick returning 401 *today*. Twice in one afternoon an S3-fronted edge answered
  `AccessDenied` in XML instead and the required matrix went red for a reason
  unrelated to the change under review. It now runs against an `httptest` stub.
  ([#439](https://github.com/rainmanjam/polyemesis/issues/439))

- **Every CI job now has a time ceiling, and the un-retried network installs are
  retried.** An audit rather than a sprinkle: 30 of 31 jobs already had
  `timeout-minutes`, and the one that did not was falling back to GitHub's default
  of six hours while waiting on a third-party scanner. Three network installs had
  no second attempt and now have three. Deliberately left alone, with the reasoning
  recorded so it is not redone: `npm ci` (npm already retries twice), the two
  `curl` sites that carry `--retry 3 --retry-all-errors` *and* a loop, and the
  installer's own `apt` — where apt is the subject under test and retrying it would
  hide the failure the job exists to catch.

  The gitleaks download needed more than a flag. It piped `curl` into `tar`, and
  `--retry` restarts a transfer from the beginning, so retrying would have re-fed
  bytes into a `tar` that had already consumed part of the stream — surfacing as a
  corrupt extract rather than the download failure it was. The archive now lands as
  a file first, which is what makes the retry safe.

- **The installer is now run, instead of only asked what it would do.** CI checked
  the installer's preflight and stopped there, so every defect above was reachable
  only by installing on a real machine. A job now installs, verifies the service,
  uninstalls, and checks the rollback path, on a runner rather than in a container
  because systemd has to be PID 1 for any of it to mean anything.

- **The acceptance teardown check could not fail, and could not attribute.** It ran
  as `trap 'poly_report_orphans' RETURN`, and a RETURN trap fires after the return
  value is already fixed — so the finding could never reach an exit status no matter
  what it found. It also counted every `ffmpeg` on the host, which cannot tell a
  suite's own leak from a developer's transcode or a sibling suite running in
  parallel, and a check that goes red for someone else's process is one people
  learn to ignore.

  Both are fixed, and fixing them immediately found that **the harness was
  manufacturing the leak it was later asked to detect**, twice over. `poly_stop_server`
  escalated to SIGKILL after 15s while polyemesis's own shutdown can spend 20s in
  `httpServer.Shutdown` alone before it reaches `eng.Stop` — the call that tears the
  destinations down — so the teardown killed the server mid-shutdown and orphaned its
  encoders; the grace is now read from the shipped unit's own `TimeoutStopSec`
  rather than chosen. And two suites SIGKILL polyemesis deliberately, to prove a
  crashed job is recovered and that the broker publishes its will message; on a real
  box systemd's cgroup reaps the children, and outside systemd nothing did, so those
  suites now reap what they knowingly orphan.
  ([#448](https://github.com/rainmanjam/polyemesis/issues/448))

- **A test helper could not find a free port when the kernel handed it one near
  the top of the range.** `FreeUDPWindow` probes an ephemeral port and scans
  *upward* for a run of free numbers, guarded by `start+n < 65535` — which is false
  on the first iteration when the probe lands within `n` of the ceiling. It then
  tried not a single bind and reported "no run of 3 free UDP ports" about a machine
  where everything below was free. Linux's default ephemeral range stops at 60999
  and never reaches it; macOS runs to 65535 and does, which is why it read as a
  macOS quirk rather than a bug. The scan now wraps.

- **A stop-path test was budgeted below the stop path's own worst case.** It gave
  `Stop` five seconds and asserted the error was nil, while this package's own
  constants put the permitted worst case at `shutdownGrace + drainGrace` = 10s. A
  loaded runner that merely delayed the child produced a deadline error with nothing
  wrong — the test failed at 5.01s, its entire budget, having asserted nothing about
  the code.

## [0.6.0] — 2026-08-09


An operator-facing release: the server now tells you an update exists and what a
restart would cost, a rendition stopped doing work it threw away, and a failover
respawn stopped ignoring its own backoff.

### Added

- **The server tells you when there is a newer release, and what stopping would
  interrupt.** A banner in the UI, translated across all fifteen locales, and a
  version endpoint behind it.

  The second half is the part that matters. polyemesis is not a tool you run and
  close; it is the thing carrying a broadcast, and restarting it drops every
  destination mid-stream. So the check reports what is **on air** — how many
  encoders are publishing, how many destinations are live, whether a recording is
  running, and which programmes those belong to — rather than a yes/no on whether
  an upgrade is allowed. "Cannot upgrade" is useless on its own; "3 destinations
  are live and a recording is running" lets an operator decide it is fine.

  A destination that is reconnecting counts as live, because from the desk it is
  mid-broadcast, and restarting underneath it turns a recoverable blip into a
  dropped show.

### Changed

- **A rendition that drops frames now drops them before scaling, not after.**
  `-r` decimates at the encoder, which is after the filter graph, so a 60→30
  rendition scaled all sixty frames of the source and then discarded half of
  them. Every one of those scales was work spent on a frame that never reached
  the encoder.

  Measured on a 6-core Haswell VPS, 4K60 → 1080p30 at veryfast/6000k: 2.13×
  realtime before, 2.49× after — **17% more headroom** on the case that needed it
  most.

### Fixed

- **A supervised child that had to be killed is no longer reported as one that
  stopped.** `Stop` gives a wedged process a deadline and then sends SIGKILL,
  but it returned at that point without waiting and recorded the fact only in a
  log line — and both outcomes then reported the same state, so nothing could
  tell "the child exited" from "the child was killed and may still be running".

  The failover tier is where that matters: it starts a replacement feed into the
  same hub the moment the teardown returns, so a child still alive and still
  writing is two publishers on one input. The condition is now surfaced on the
  tier and logged. The switch still proceeds — leaving the tier with no feed at
  all is worse than a seam — so this is a thing you are told about rather than a
  thing that blocks a switch.

- **The failover respawn backoff is measured from the feed's start, not from the
  decision to switch.** The timestamp recorded as a feed's start time was taken
  before a teardown that blocks for as long as the outgoing process needs to
  exit — up to twelve seconds. After a slow teardown the backoff interval had
  therefore already elapsed before the replacement had run for a single second,
  so a feed that failed immediately was respawned on every 500 ms sweep instead
  of backing off.

  Nothing else at that seam changed in this release. A second change was made
  alongside this one — moving `-output_ts_offset` from the switch decision to
  the feed's actual start, on the same reasoning — and was **reverted before
  release**. With it, the failover suite reported a backwards decode timestamp
  at a switch in 3 runs of 12; without it, 0 in 12, both before the change and
  after reverting it. A platform drops the connection on a backwards DTS, which
  is the failover tier failing at the one thing it exists for, so the
  configuration with none of them is the one that ships.

  Whether that change CREATED those steps or merely made existing ones large
  enough to cross the suite's one-millisecond threshold is not settled, and the
  distinction matters: one is a bug introduced, the other a bug revealed. Issue
  #126 stays open for it and the check now reports the size of every step,
  including the ones too small to fail on. What is settled is the direction, and
  the bug the reverted change described — a backwards step caused by a slow
  teardown — was never observed at all.

### Internal

- **Groundwork for in-place upgrades, not yet reachable.** A new `internal/upgrade`
  package works out how an install was made — container, systemd unit, or a
  binary someone runs themselves — and refuses to act where acting is useless or
  reckless. For the one case it can act on it stages a checksum-verified binary
  beside the running one, keeping the outgoing one for rollback, ordered so that
  every state the process can be killed into leaves a runnable box.

  Nothing calls it yet. There is no endpoint and no button, so this release
  changes no upgrade behaviour; it is listed here because it exists in the tree
  and because the next release is expected to wire it. Four adversarial review
  rounds went into it, which is the appropriate amount for code whose failure
  mode is a server with no runnable binary.

## [0.5.0] — 2026-08-08


The first release with the core review in it, and the version number is
deliberately 0.5.0 rather than 0.6.0.

0.5.0 was written up in this file on 2026-08-07 and then never tagged, so its
contents have never been in anyone's hands — a heading that looked released
above work that was not. Folding this release into it closes that rather than
shipping 0.6.0 over a hole in the version line, or retroactively tagging a
release nobody could have installed.

So everything below is one release: the four-reviewer core review worked end to
end, plus the two operator-visible changes the never-cut 0.5.0 had described.

### Added

- **`polyemesis -reset-admin`** — set a new admin password from the box the
  database lives on, for an operator who has shell access and no way in through
  the UI. Asks twice without echoing, signs out every existing session, and
  exits without binding a port, so it is safe to run against a live server. Pipe
  the password twice to script it.

  The password is deliberately **not** a flag: argv is visible in `ps` to every
  other user on the machine, lands in shell history, and appears in any audit log
  that records command lines.

  It also replaces advice nobody should follow. Deleting the row from the users
  table does restore first-run setup — `needsSetup` is just "the table is empty"
  — but `POST /api/v1/setup` is unauthenticated and the only guard on it is that
  an account already exists. Deleting the account removes that guard, so until
  setup is finished anyone who can reach the port can claim the install.
  `-reset-admin` never opens that window.

### Fixed

Nine more defects from the same core review, in the transport and concurrency
layers. Every one is the shape the whole review turned out to be: a protection
that is present in name and absent in effect.

- **An RTMP publisher for an SRT source is refused immediately again.** The
  readiness grace described further down this same release claimed in its own
  comment that it could not be used to hold connections open. It could: a target is registered for every
  source whatever its ingest mode, so any valid token for an SRT-mode source was
  found, enabled, and permanently not ready — the one verdict the grace waits
  on. Every connect burned the full six seconds, in parallel, for a state no
  amount of waiting could change. The listener now waits only where a subscriber
  is genuinely on its way, and caps waiters per publisher slot so one reconnect
  gets its grace while a flood does not multiply it.

- **A measured layout survives the encoder going quiet.** Roughly nine seconds
  into any outage the engine began reporting a real measured layout as the
  placeholder, because the check asked "is a stream arriving now" where it meant
  "has one ever been measured". The meters were torn down, the captioner rebuilt
  against an unknown source, and the routing preview stopped describing the graph
  its destinations were running. The stem plan had the same mistake from the
  other end: an encoder going quiet emptied it and restarted the recorder
  **without stems** mid-outage, then restarted it again when the probe returned.

- **A genuine data race in the relay's loss measurement.** `Deliver` runs on the
  SRT read loop, and a publisher takeover deliberately overlaps two of them —
  closing the incumbent's connection wakes its `Read`, which is not the same as
  it having finished. Two goroutines wrote the continuity counters and the
  send-error tally with nothing between them, corrupting the TSLost figure that
  the "UDP on loopback is defensible because it is measured" argument rests on.

- **A dead subscriber no longer counts as a reader.** A subscriber whose FFmpeg
  exited while nothing was publishing was never noticed — there are no writes to
  fail — so readiness kept reporting a closed socket as a live reader and
  admitted publishers into a stream nobody was reading.

- **A mid-stream cue point no longer replaces the cached metadata.** Every AMF0
  data message shared one setup slot, so a cue point evicted `onMetaData` and
  every subscriber attaching afterwards got the cue point replayed where its
  metadata should have been.

- **A source disabled between connect and publish is now refused.** The SRT
  publish callback re-checked the token and the pipeline but not whether the
  source was still enabled, so an operator's "off" did nothing until the session
  ended by itself.

- **Two recorders can no longer start on the same segment pattern**, and **two
  engines can no longer start for one source.** Free-space recovery reconciled
  the recorder without the lock every other caller takes, and the manager's
  engine sync had no such lock at all while being reached from several HTTP
  handlers — leaving a running engine that nothing held a reference to.

- **A destination can no longer report "running" while publishing nothing.** The
  primary feed's signature named what the failover tier was supposed to be rather
  than what the feed was reading, so a feed started during a hub swap matched the
  signature the next reconcile asked for and was left alone permanently: the hub
  carried zero bytes, every destination read healthy, and nothing raised an
  error.

### Changed

- Faster on the paths that run constantly: the relay fan-out takes no lock and
  allocates nothing per datagram, a held publisher is woken by its subscriber
  attaching instead of polling the database sixty times, and the status snapshot
  no longer scans its destination list twice per row.

  One optimisation was tried and **withdrawn**. Coalescing status pushes onto a
  150 ms window is a genuine saving, and it cost three broadcasts in ten: with it
  the failover suite handed a destination a backwards decode timestamp at a
  switch in 3 runs out of 10, measured against 0 in 10 without it. A platform
  drops the connection on a backwards DTS, which is the failover tier failing at
  the one thing it exists to do. See issue #126.

Earlier in the same review, four defects in the per-destination audio routing:

Four defects in the per-destination audio routing, all of them audible, all
found by a four-reviewer pass over the compiler and confirmed by measuring what
FFmpeg actually produced rather than by reading the filtergraph.

- **The downmix now reads the track's channel layout, not just its channel
  count.** Every 3-channel track was treated as 3.0 = FL FR FC. A 2.1 track is
  FL FR LFE, so its LFE was folded into both legs at −7.7 dB — precisely what
  the file promised in its header never happened. The same mistake dropped a
  real channel from `hexagonal` and `6.1(back)`, which have no LFE at the index
  where 5.1 and 7.1 keep theirs. Coefficients are now assigned by channel name
  from libavutil's own layout table, with the old count-keyed table kept as the
  fallback for a layout ffprobe could not name.

  **What you may notice.** The layouts that were already correct — mono, stereo,
  3.0, quad, 5.0, 5.1, 5.1(side), 6.1, 7.1 — compile to byte-identical graphs
  and their destinations do not restart. A destination fed a 2.1, 3.1, 4.1,
  hexagonal or 6.1(back) track will restart once on upgrade, and will sound
  different afterwards, because it was wrong before.

- **`auto` clip protection now looks at the gain, not only the track count.**
  The rule was "only summing across tracks can clip, so one track needs no
  limiter". But `pan` sums too, per output channel, and validation caps each
  cell at 2.0 and never the row. A one-track matrix with three cells at maximum
  gain on one leg compiled to `c0=2*c0+2*c2+2*c4` — six times full scale, hard
  clipping, with `auto` having decided no protection was needed. The track count
  keeps its say and the peak per-output gain gets one as well. This only ever
  adds a limiter: nothing that has one today loses it, and any profile peaking
  at or below unity compiles to the string it always did.

- **A wide track of unknown layout no longer sits off-centre.** The fallback
  splits even channels left and odd channels right, then normalised each leg by
  its own sum — so an odd channel count, which puts one more channel on the
  left, scaled the two sides by different divisors. Nine channels gave a
  permanent 1.94 dB image shift. Both legs are now divided by the same figure.

- **A matrix whose ingest has narrowed now says the level moved.** A saved 5.1
  matrix meeting a stereo ingest drops the cells addressing the missing channels
  and keeps coefficients that were scaled for the old width, leaving the
  destination 7.7 dB down. It warned about the channel numbers and never about
  the volume, which is the part anyone would actually notice. The coefficients
  are still the operator's to change — rescaling them silently would be the same
  category of mistake — but the drop is now stated in dB.

### Changed — the two from the never-cut 0.5.0

Written up on 2026-08-07 against a tag that was never pushed. Both are still
accurate and both ship here for the first time.

- **An RTMP publisher is now admitted only when something is subscribed to read
  it.** `Target.Ready` used to mean "an engine exists for this source and its
  mode is rtmp", which says a subscriber SHOULD exist, never that one does. A
  publisher whose ingest child never spawned, or was crash-looping, was admitted
  into a stream with no reader: the encoder goes green, the bytes are fanned out
  to nobody, and the operator has a healthy OBS and no output with nothing
  saying why. It is now asked of the listener directly. The same fix was applied
  to the failover standby, which had been a hardcoded `true` sitting directly
  below a comment describing the opposite contract.

  **What you may notice.** The ingest child carries no reconnect flags — it
  exits whenever its publisher does and is respawned on a 500 ms–5 s backoff —
  so an encoder reconnecting after a network blip arrives while nothing is
  subscribed. Rather than refuse it, the listener now HOLDS such a connection
  for up to 6 seconds and admits it if a subscriber attaches. Only that one
  verdict waits; an unknown key or a disabled source is still answered at once.

- **Destinations are not planned until the ingest layout has been measured.**
  Before a probe lands, the layout is a placeholder of six stereo tracks that
  exists so the routing editor has something to draw. Compiling a real routing
  graph against it fails in two ways, and the second is why this is a guard
  rather than a warning: a profile naming a track the stream lacks emits
  `[0:a:5]` and FFmpeg refuses to start — loud, and diagnosable — but the
  placeholder also claims two channels on every track, so a real 5.1 track
  compiles to `pan=stereo|c0=c0|c1=c1`, which is perfectly valid FFmpeg. The
  destination starts, stays up, and publishes front L/R only, with centre —
  where dialogue lives — discarded and no error anywhere.

- **A routing preview compiled from the placeholder now says so.** Every
  destination endpoint returns a compiled `filterComplex` so the editor can show
  the mix without a second round trip. Those compiled against an unmeasured
  layout are marked `routingProvisional`. They are flagged, never withheld:
  configuring a destination before going live is when most people configure them.

- **Both ingest ports bind by default, and both are published by default.**
  The RTMP listener used to bind only when some enabled source was configured
  for it, while SRT bound unconditionally. That was not a policy — it was two
  histories preserved side by side: `ffmpeg -listen 1` had held 1935 only while
  an RTMP source existed, and the SRT listener had always been up. The asymmetry
  showed on a fresh install that had chosen no ingest mode at all, which still
  opened 6000 while refusing to open 1935 on the grounds that nothing there
  spoke the protocol.
  It also disagreed with this project's own instructions: `docs/HARDWARE.md` and
  `docs/TROUBLESHOOTING.md` have always said to run
  `-p 6000:6000/udp -p 1935:1935`, so we documented publishing a port that might
  not be listening. datarhei Restreamer opens both, and so do we now.
  `install.sh` matches: it asks for an RTMP port the way it asks for an SRT one
  rather than a yes/no, defaults to publishing 1935, and takes `--rtmp-port 0`
  to decline it. **The port is the switch on both sides** — the server treats 0
  as off too — instead of a yes/no in the installer and a port in the settings
  meaning the same thing two different ways.
  What this adds is narrow: a host with **no firewall at all** now has 1935
  reachable, where the source list used to close it by accident. Everywhere else
  the ufw rule and the compose publish still decide, which is what they always
  claimed to do. Both listeners refuse an unknown token or key in constant time,
  both require the source to be ready before admitting anything, and a
  connection that says nothing dies on the handshake timeout.

### Fixed

Twelve defects in the work above, found across four independent review passes.
The ones with operator-visible consequences:

- A probe takes up to ten seconds and used to commit its result unconditionally.
  One in flight across an ingest restart or a mode change re-certified the
  previous transport's track layout under the new mode — and stamped it in a way
  that satisfied the guard permanently, so destinations compiled against a dead
  stream's layout until something else changed.
- The guard held every tier below it, not just destinations. Killing the primary
  encoder clears the probe state, so it fired for exactly the window the failover
  selector exists to cover: the selector stopped being reconciled and the late
  catch-up put a backwards decode timestamp in the output — the discontinuity a
  receiving platform drops the connection on.
- A destination could be left subscribed to a relay hub that was then closed
  underneath it. Closing a hub stops delivery without ending the process, so
  FFmpeg sat there running and receiving nothing: 76 seconds, zero bytes, no
  error. It reproduced about one run in two.
- A video-only source going idle tore down its silence tier, after which every
  destination on that source was torn down too, for as long as the encoder was
  quiet.
- A probe that could never succeed — a missing ffprobe, an unidentifiable
  stream — held every destination down and said nothing at all. It now says so.

### Installer

- **The installer now offers to serve HTTPS on 443, and opens it.** The port was
  asked for before the TLS mode was chosen, so an operator picked one without
  yet knowing they would be serving HTTPS at all — and the default 8080 gave a
  working but unlovely install where every link carried a port and nothing
  listened on the one people try first. Offered only when the port is still the
  untouched default; a port given on the command line or typed at the prompt is
  a decision and is left alone. The firewall rule follows the chosen port rather
  than opening 443 unconditionally, because a port nothing binds looks like
  working TLS and serves nothing.

- **`CAP_NET_BIND_SERVICE` is granted for any privileged port, not only for
  ACME.** It used to be gated on `tls.mode: acme`, which covered the `:80`
  challenge and missed the web UI itself: selfsigned is the DEFAULT choice, so
  an operator taking the 443 offer would have got a unit that could not bind the
  port the installer had just written into its own `ExecStart` — `bind:
  permission denied`, on a fresh install, from following the prompts.

### Testing

- The cross-platform smoke test now publishes over **E-RTMP and SRT** as well as
  injecting into the relay hub, and measures per-destination audio for each.
  Both publishers are pure Go — `gortmplib` and `datarhei/gosrt` — so the one-port
  listener, the readiness gate and multitrack FLV demux are exercised on macOS
  and Windows too, not only on the Linux acceptance suites. FFmpeg is left doing
  only what every build can do: muxing.
- The container suite gained two steps that publish routed audio to a real RTMP
  sink, from an E-RTMP ingest and from an SRT ingest, closing a coverage gap
  where each half had been proven and the combination never had.
- A destination that runs its whole life and writes nothing now fails the
  failover suite. It used to be a note, and a note is why the 76-second silent
  destination above stayed green.

## [0.4.0] — 2026-08-07


A minor bump. Nothing here breaks a stored config, but two behaviours change in
ways an existing install will notice: **ingest mode no longer has a default**, so
a fresh install asks rather than choosing; and **RTMP ingest is now addressed by
stream key** on one shared listener rather than by a port per source. Keys that
already worked keep working through a grandfather clause.

The headline is multi-source RTMP: how many programmes an install can carry no
longer depends on which protocol the encoder speaks. Alongside it, the audio
track ceiling goes from six to thirty-two, destinations gained per-platform
encoder guidance sourced from what each platform publishes, and the project got
a website.

The fixes are worth reading in full. Three of them — a data race in the new RTMP
listener, multitrack being unusable for any subscriber that joined late, and
every SRT install reporting its ingest as offline — were found by tests written
after the feature, which is the argument for writing them.

### Added

- **Chat search.** `GET /api/v1/chat/search?q=` matches a message's text or its
  author's name across the retained scrollback, newest first, optionally scoped
  to one platform. The pane gains a search box that replaces the live timeline
  while a query is active — results come from the database and are frozen at the
  moment of the query, so letting live messages append underneath them would
  present two different things as one list. Search is where an operator can
  wrongly conclude something did not happen, so the retention caveat renders on
  an empty result, not only on a full one.
- **Right-click and double-click moderation on a chat message.** Timeouts, delete
  and the user card, two seconds from reading a bad line to acting on it.
  Double-click is the same menu for pointers with no secondary button. It adds no
  capability the user card lacked; a permanent ban deliberately does *not* fire
  from the menu but opens the card, which confirms — the one irreversible action
  reachable from a right-click should not be a single click on a menu that
  appeared under the cursor.
- **Links out to the platform.** Twitch gets its real moderator viewer card in a
  separate window. YouTube, Kick and Facebook get a profile or channel link and
  say so, because none of them publishes a per-viewer chat history at any URL —
  a uniform "Open on <platform>" label would promise a moderator the same thing
  everywhere and deliver it only on Twitch.
- **Per-setting help.** An `(i)` popover beside a setting's label explains what it
  actually changes — 2.5× your round-trip time for SRT latency, why the free-space
  floor is the only limit that accounts for files polyemesis did not write. Click
  rather than hover, so it works on touch and under a screen reader, and the body
  is a catalogue key, so the explanation is translated too.
- **Multi-source RTMP.** RTMP ingest was `ffmpeg -listen 1` — a single-connection
  receiver that cannot demultiplex by path — so an install could carry exactly
  one RTMP source while SRT carried as many as you liked. That asymmetry was an
  artefact of the implementation rather than a decision. `internal/rtmpserver`
  is now one listener on one port for every source, addressed by the stream key
  in the publish URL, the same shape `internal/srtserver` already had. Publishers
  push to it and this install's own FFmpeg processes subscribe to it on the same
  socket. Media is never parsed: RTMP *messages* are relayed, so `-map 0 -c copy`
  downstream is untouched and Enhanced RTMP multitrack passes through without the
  server knowing what a track is. Existing keys keep working through a
  grandfather clause.
- **Thirty-two audio tracks, up from six.** `routing.MaxTracks` is 32, with a
  Σchannels ≤ 64 guard because FFmpeg's `amerge` caps there and the failure past
  it is a filter graph that will not build.
- **A marketing website.** `web/` — Astro, static, its own container image, with
  a build-time guard on the parts of it that have silently broken before.
- **Per-platform encoder guidance.** Each preset carries the resolution, bitrate
  and frame rate the platform itself publishes, with the source URL and the date
  it was read attached — both required fields, because a figure whose provenance
  is not on screen is indistinguishable from a guess. Cross-checked against OBS's
  own `services.json`.
- **An advanced section for customising a destination's encode.** A variant is
  a second encode seeded from a shared one, so an operator can have "the same
  tier but 4500 kbps for the constrained uplink" without editing the tier every
  other destination is on.
- **Alerts when a destination stops keeping up**, and security and configuration
  events for Slack and Discord.
- **An OBS acceptance suite.** `scripts/acceptance-obs-multitrack.sh` runs OBS
  headless in Docker and publishes into a real polyemesis, which is the only way
  to test OBS's own handshake and metadata rather than FFmpeg's.

### Changed

- **The application is translated.** Every page under `src/pages` now reads from
  the catalogue; a sweep for hard-coded prose returns nothing. The catalogue grew
  from 135 keys to 1,098 and all fifteen languages are complete.

  Previously only the nav shell and three widgets were extracted, so an operator
  who chose Deutsch got a German sidebar and an English application. The 135 keys
  that existed were complete in every language, which is exactly why the gap was
  easy to miss: the coverage looked like 100%.

  Extraction found prose in four shapes, each invisible to the check written for
  the one before it — JSX text, string props and toasts; ternaries such as
  `{busy ? "Pushing…" : "Push to platforms"}`; object-literal properties built
  from template literals; and eight module-scope tables. The last of those could
  never have called `useT()` at all, since hooks do not run at module scope, so
  each now holds a `TranslationKey` and is translated where it is rendered — a
  `Record<K, TranslationKey>` cannot hold a sentence, which the compiler enforces.
- **One word per locale for "rendition".** Ten of the fifteen catalogues disagreed
  with themselves across `nav.renditions`, `rend.title`, `sources.renditions` and
  `dash.renditions`. In Polish the sidebar read "Opcje jakości" while the page it
  opened was titled "Warianty", so following the link appeared to land elsewhere.
  Japanese, Korean and Dutch additionally used a term meaning "rendering
  settings"; a rendition is one video variant at a given size and bitrate, not a
  settings screen, and they now say so.
- `vitest` runs in CI alongside `tsc` and `oxlint`, covering the pure logic the
  browser suite cannot practically enumerate — platform link construction across
  five platforms, and the translation catalogues themselves.
- **Ingest mode is an explicit first-run choice, with no default.** The two
  options are not interchangeable and the difference is not recoverable by
  guessing: SRT carries every audio track, and RTMP delivers a single one on any
  FFmpeg below 7.1 — which includes Ubuntu 24.04's stock build. Defaulting to
  either silently hands a share of installs the wrong thing, and the RTMP failure
  is invisible: the stream works, and one of the six tracks arrives.
- **The video treatment on a destination is two cards, not a dropdown.** Copying
  video is `-c:v copy` and costs nothing; a shared encode is the most expensive
  thing on the page. A `<select>` presented them as the same kind of choice. The
  picker now leads with what an encode *produces* rather than what someone named
  it, and states the consequence of joining or leaving one — including what
  happens to the encode you are leaving, which nothing else in this space tells
  you.
- **A motion scale that is actually wired.** `--motion-instant/quick/settle` and
  `--ease-out` were declared and registered with Tailwind zero times, so all 27
  `transition-colors` ran on its 150ms default — and the reduced-motion block
  that set those tokens to `0ms` did nothing at all. The website, which had no
  named timings, now shares the same scale.
- **The E-RTMP multitrack harness is Go**, not Python. It was the repository's
  only `.py` file; everything else that stands up a real stream and measures what
  comes back is already a `//go:build ignore` main under `scripts/`.

### Fixed

- Chat's `(i)` help buttons no longer all announce as "More information"; each
  names the setting it explains, so a screen-reader user can tell a dozen of them
  apart.
- **A data race in the RTMP listener.** `serveSubscriber` assigned `sub.conn`
  after publishing the subscriber into the stream table, so `Stop` could read the
  field while it was being written. Found by CI's `-race`; no local run had ever
  used it.
- **Enhanced RTMP multitrack was unusable for any subscriber that joined late.**
  Tracks 2..N arrive wrapped in `AudioExMultitrack`, and the setup cache did not
  unwrap them — so a late subscriber held coded frames for tracks it had no
  decoder configuration for, and `ffprobe` hung rather than failing. Late is the
  normal case: the ingest child subscribes when the source is enabled, and the
  operator starts their encoder whenever they like.
- **Every healthy SRT install reported its ingest as offline.** The header read
  `status.ingest.state`, and SRT deliberately has no ingest child — `srtserver`
  delivers straight into the hub — so the most prominent status in the
  application contradicted the meters, the LIVE badge and the API. It now asks
  the question the rest of the app asks: are bytes arriving.
- **The reconnecting indicator faded itself.** `live` pulsed a separate halo and
  kept its core opaque; `warn` pulsed the core, dropping the only indicator to
  35% opacity twice a second. The state that most needs attention was the one
  rendered hardest to see.
- **Twenty-one raw Tailwind colours collided with the semantic tones**, and the
  guard whitelisted them: `red-500` is ΔE 8.11 from `--down`, which is to say
  indistinguishable.
- **Recordings were truncated on stop rather than finalised**, on every platform
  and not only Windows as first recorded.
- **`--tls acme --yes` spun forever, and a re-install never restarted.**
- **The default Docker install reported unhealthy forever.**
- **An unmatched `/api` path answered 200 with the UI instead of a 404.**
- **The Docker upgrade backup archived an empty volume and exited 0.**
- **A short window drew two scrollbars**, one of which scrolled 14px of nothing.
- **A fresh install logged an ERROR about an RTMP port nobody had set.**
- **The installer's FFmpeg download could be walked down to plaintext HTTP.**
  Every other fetch in `install.sh` pins the scheme; this one went through
  `urllib.request.urlretrieve`, which follows redirects without restricting it —
  and it writes a binary into `/usr/local/bin` that a root service executes.

## [0.3.0] — 2026-08-05


A minor bump rather than a patch, and deliberately: `facebook.backupIngest`
became `backupIngestWanted` with no compatibility alias, which breaks any client
that *writes* that field. Everything else here is a fix.

An adversarial audit of the 0.2.0 release, and the fixes for everything it
found. Every change below traces to a numbered finding in that audit.

### Security

- **A stream key could reach your MQTT broker as a retained message.** FFmpeg
  prints the whole publish URL when a destination refuses it, that line became
  the process's last error, and the last error was published to MQTT as
  `ingestError` — **retained**, so it outlived the process and was readable by
  every subscriber on the broker with no session at all.

  **If you run the MQTT integration, clear your retained topics after
  upgrading.** Upgrading stops new keys being published; it cannot unpublish
  what a broker is already holding.

  The same bytes were also returned unmasked by `GET /processes` and its logs
  endpoint, and written to `process.log` on disk — both behind authentication,
  so no privilege boundary was crossed there, but a log file is the artifact
  people attach to bug reports. All of it is now masked where the line is
  captured. Expert mode still shows the real command deliberately: it exists so
  an operator can approve the command that will actually run.

### Changed

- **BREAKING, for API clients only:** the destination field
  `facebook.backupIngest` has moved to a top-level `backupIngestWanted`. It was
  inside a platform-named block while the engine that reads it is
  platform-neutral, which meant no platform but Facebook could ever have a
  redundant feed.

  Stored rows migrate on first open — **there is no compatibility alias**, so a
  script that *writes* `facebook.backupIngest` stops having any effect. One that
  only reads a destination is unaffected apart from the new name. See
  [docs/API.md](docs/API.md).

- Enabling or disabling backup ingest from the destination dialog now works.
  Unchecking it previously sent nothing, and the server kept the stored `true` —
  so the toggle was one-way and the second feed kept running at double the
  upload.

- A YouTube made-for-kids declaration can now be withdrawn. Setting it back to
  "leave as it is on YouTube" previously sent nothing and the stored value
  survived.

### Fixed

- **A deleted source could leave an FFmpeg publishing forever.** Shutting an
  engine down stopped a destination's primary process and not its backup, and
  the backup restarts itself by design — so it kept publishing to the platform,
  invisible to the process list, holding its relay port, until the daemon
  itself was restarted.
- **A standby SRT encoder and the primary evicted each other.** Both were keyed
  by source alone, so whichever connected first held the slot and the other was
  refused; if the primary went quiet for three seconds the standby took over,
  and the recovering primary was then the one refused. Redundant ingest did not
  work in the situation it exists for.
- Two schedules starting the same Facebook destination no longer move one
  broadcast back and forth between their times every five minutes. Each show
  gets its own event page.
- The pre-announce sweep no longer reverts an operator edit that lands while it
  is talking to Facebook, and no longer changes the stream key of a destination
  that is currently live.
- A backup feed now appears in the process list, so its command line and logs
  are reachable — which is what you need at the moment redundancy breaks.
- A reconnect-policy change now reaches a destination's backup as well as its
  primary, and revives one that had already given up under a stricter limit.
- Editing an expert argument's whitespace, or changing a setting that does not
  alter the command line, no longer drops a live connection.
- A destination nobody has touched is no longer reported as retuned on every
  reconcile.
- The hooks card and the playlist editor are translated. Creating a webhook
  twice by double-clicking, which produced two hooks and showed one signing
  secret, is fixed.

### Internal

- Concurrent reconciles are serialized. Two could previously start the same
  destination, leaving one FFmpeg running that received no packets and that
  nothing could stop.
- Providers gained one injection point for their HTTP base. The previous
  mechanism redirected one call in thirteen while looking like it redirected
  all of them, and two other providers hard-coded past their own.
- Six guards that could not fail were rewritten, and the classes they belonged
  to were swept across the codebase — route tests that assert only a status
  code (the embedded UI answers an unrouted API path, so they pass with the
  route deleted), and source-text assertions that survive their block being
  switched off.

## [0.2.0] — 2026-08-04


### Added

- **Compliance metadata now reaches the platforms.** A YouTube COPPA
  self-declaration, a YouTube privacy status and a Twitch content label were
  editable, validated, saved — and had never once been sent. `PushCompliance`
  existed from the first release and no code path called it, so every value an
  operator set stopped at the database.

  This is a behaviour change on upgrade, and a deliberate one: an install with
  compliance already stored begins writing it to the platform on the next push.
  A destination configured months ago with a privacy setting nobody remembers
  will apply it. Two destinations pointing at one account with *different*
  compliance refuse the push and name both, rather than letting one silently
  win — discarding a legal declaration with nothing anywhere saying so is the
  failure this change exists to end.

- **Facebook accepts the metadata everything else already did.** Title,
  description and tags on the composer push; audience, crossposting and the
  donate button applied when the broadcast is created. A tag word Facebook
  cannot resolve is a warning naming the word, not a failed push.

- **A Facebook show can be announced before it starts.** A destination on a
  start schedule gets its broadcast created ahead of time, so there is a public
  event page days early rather than one appearing the moment bytes arrive.

  A `once` schedule further out than Facebook's seven-day limit still saves and
  still runs — it simply gets no event page, and is told so.

- **A Facebook destination can publish a redundant backup feed.** A second
  supervised FFmpeg to Facebook's backup ingest, so a dropped connection does
  not drop the broadcast. Off by default: it doubles that destination's upload
  bandwidth, which is an operator's call on a thin uplink. Enabling it costs one
  reconnect and the dialog says so — a backup endpoint exists only on a
  broadcast created with one, and creating that issues a new stream key.

- **Every release carries a software bill of materials.** SPDX and CycloneDX,
  scanned from the source tree rather than the binaries, so the embedded web
  UI's npm dependencies appear alongside the Go modules and the pinned Actions.

- **The failover selector can put a playlist on air.** A fourth candidate,
  ranked below both ingests and above the slate: a scheduled programme is a
  fallback for "nobody is streaming", not a pre-emption of somebody who is, so a
  presenter who is live stays live. An operator who wants the playlist to win
  anyway pins it, the same way they would pin the backup.

  What it changes for a viewer: an outage that used to land on the standby card
  now lands on programming, and a selector already sitting on the card leaves it
  for the playlist rather than waiting for an encoder. The return to a real
  ingest is immediate and is not subject to the return mode, exactly as leaving
  the slate already was — there is no flapping risk between a file and an
  encoder to bound.

  Five new reasons reach `Failover.Reason` — `the playlist is running`, `the
  primary ingest stopped delivering and the playlist is running`, `neither
  ingest is delivering, so the playlist is on air`, `an operator selected the
  playlist`, and `the playlist stopped running` when it ends and the standby
  card takes over. None of the existing fourteen were reworded. The decision table
  frozen in `internal/engine/testdata/selector_golden.txt` grows from 1024 rows
  to 3200, and a second table proves the addition is additive: all 1024
  decisions that predate the playlist are byte-for-byte what they were,
  reason strings included. The settings that turn a playlist feed on, and the
  tier that answers them, are the entry below.
- **A file can hold the stream, and failover keeps watching while it does.** The
  playlist is a supervised FFmpeg loop publishing into **a relay hub of its
  own**, and that hub is the whole of the design. A file played into the
  *primary's* hub puts bytes on it; the primary therefore reads live; and
  failover never switches away from a live primary — so a file on air would have
  disabled the entire failover feature, silently, for as long as it played. With
  its own hub the primary's stays empty and every failover decision underneath
  stays reachable. `scripts/acceptance-failover.sh` measures precisely that: the
  encoder drops while the file is on air, the primary is *seen* to go down, and
  the destination rides the switch onto the file without restarting.

  Two settings turn it on — `failover.playlist.enabled` and
  `failover.playlist.filePath`, the path relative to the data directory and
  confined to it exactly as a `file://` pull source and the slate's still are. A
  path that escapes it is refused at validation and never becomes an FFmpeg
  argument. There is no form control for them yet: they are reachable through the
  settings API, and the control belongs with the sequencing work rather than
  with this. Both are hot-reloadable and neither disturbs anything else — a save
  that touches the recorder or a destination costs the playlist nothing, and
  editing the path restarts only the playlist's own process.

  Disabling the playlist while it is on air hands the stream to the slate in the
  same breath, rather than leaving the selector holding a source whose hub has
  just been closed. That was worth two seconds of dead air on every destination
  while the failed-start backoff expired, on an ordinary operator action, and it
  is fixed here rather than shipped: a tier that has been torn down stops
  counting as a candidate at the instant it goes, not at the next sweep.

  **The file's codec parameters must match what your encoder sends.** It is
  copied, not re-encoded, so its codec, resolution, frame rate and pixel format
  reach destinations unchanged, and switching onto a file that differs is a
  mid-stream codec change — which platforms answer by dropping the connection.
  The slate cannot cause this because it is synthesised at the departed ingest's
  probed geometry; the playlist can, and nothing validates it yet. Probing the
  file at save time is its own piece of work.
  [docs/SCHEDULED-BROADCAST.md](docs/SCHEDULED-BROADCAST.md) states the
  constraint where an operator meets it.
- **Lifecycle webhooks.** A signed POST the moment the stream starts or stops,
  or a destination goes up or down — one delivery per transition, in order, with
  an HMAC-SHA256 over the timestamp and body. Deliberately not an alert: an alert
  coalesces, debounces and rate-floors because a person is reading it, and a
  script cannot act on eleven events it was never given.

  It delivers an event that did not previously exist. The alert watcher only
  emits `ingest.recovered` after it has emitted `ingest.lost`, so an install
  whose streamer connects shortly after boot produced neither — there has been
  no "the stream started" event in polyemesis until now.

  Ordering is per endpoint and is bought with head-of-line blocking, bounded at
  33s by default. A 4xx is never retried. No destination URL, stream key,
  publish token or ingest passphrase reaches a payload, enforced centrally and
  guarded by a test that plants a key in three fields. Automation → Webhooks.
  See [docs/HOOKS.md](docs/HOOKS.md).
- **Chat scrollback sent on connect is now an operator setting**
  (`chat.historyMessages`). This is the in-memory ring a browser reads the
  instant it opens the page, before any stored history is queried — a different
  number from the retention bounds beside it, which govern what is kept on disk.
  Applied live by resizing the ring, so it takes effect on the next connection
  rather than the next restart. Bounded 1–50,000 rather than at the millions
  `keepMessages` allows, because the ring is allocated in full up front: the
  ceiling is memory reserved whether or not anyone is talking.
- **Alert delivery attempts are now an operator setting**
  (`alerts.retryAttempts`). How hard a webhook that is not answering gets chased
  before the alert is dropped, 1–10, first try included. Applied to every
  running engine and remembered for engines created later, so a source added
  after the save does not quietly run a different budget from the rest. Only
  retryable failures are affected — a 404 from a deleted webhook is still
  permanent. The backoff curve underneath stays unexposed: it was chosen against
  measured behaviour and nothing argues for moving it.

  These were the last two knobs `docs/roadmap/UNREACHABLE-KNOBS.md` rated worth
  exposing. Building them surfaced a separate defect: `db.ChatSettings` cited a
  test, `TestChatDefaultsMatchTheChatPackage`, that kept the package default and
  the settings default in step. **That test did not exist in any package under
  any name.** Both pairs are now genuinely pinned.

- **Changing a destination's reconnect policy no longer drops it.**
  `minBackoffSeconds`, `maxBackoffSeconds` and `giveUpAfter` govern what the
  supervisor does *after* FFmpeg exits and never reach the command line, yet
  editing one used to tear the destination down and rebuild it — so the only way
  to say "be more patient with this platform" was to drop the connection to it.
  They are now delivered to the running process. A retune shortens a backoff
  already in flight and never lengthens it, and never resets the restart
  counters. Raising the give-up threshold on a destination that has already
  given up does restart it, deliberately: leaving it failed for ever would be
  the silent no-op this work exists to remove.
- **Saving settings now reports what it did.** `PUT /api/v1/settings` returns a
  `reload` array alongside the settings, naming what restarted, what applied
  live, and why. "Saved" is a statement about the database; this is a statement
  about the stream. See [docs/HOT-RELOAD.md](docs/HOT-RELOAD.md).
- **Every settings field now has a recorded reload class.** 141 rules — one per
  leaf of the settings tree — each naming a class, the function that carries the
  change, and a reason. A reflection walk fails the build when a field is added
  without one, when a rule names a field that no longer exists, or when it names
  a function nobody wrote. The distribution: 89 live, 49 respawn, 1 rebind, 2
  on-demand, 0 next-start.

- **Job-history retention now applies without a restart.** `postProd.retainDays`
  and `postProd.retainJobs` were read exactly once, at boot, so lowering them
  did nothing an operator could observe until the next restart — the settings
  page said saved and the history did not move. The purge now runs hourly and
  re-reads its settings each sweep, the same shape recording retention already
  used. Found by the reload classification table, which refused to call it live.
- **The sidebar collapses to an icon-only rail on desktop.** A footer chevron
  or Ctrl/Cmd+B toggles it, and the preference is remembered in
  `localStorage`, the same place `polyemesis.language` already lives. Below
  the `md` breakpoint the off-canvas drawer is unchanged.

### Fixed

- **An IPv4 publisher could not reach the SRT ingest on macOS, and neither end
  said why.** A bare `:6000` became one dual-stack socket, and the reply to a
  v4-mapped peer went out as an IPv4 control message on an `AF_INET6` socket —
  which Darwin refuses and the dependency discards. The datagrams arrived, the
  handshake never completed, and the typed refusals that explain every other
  rejection never ran.

  A wildcard address now binds `0.0.0.0` and `::` as two listeners, neither of
  which takes that path. If one family cannot be bound, the other still serves.
  Fixes issue #28; the upstream report is `datarhei/gosrt#148`.

- **A metadata push could report a result before it had one.** The composer is
  promised a 202 with every account pending, and the workers were started
  *before* the job was snapshotted — so a platform that failed instantly could
  appear in the reply that was meant to say "not yet". With no developer
  credentials configured, that takes microseconds.

- **Settings a destination's platform cannot send are now cleared, and said.**
  Configuring a destination for one platform and then switching it left the
  first platform's settings behind, invisible — the compliance panel renders
  only for the platforms that have one. Inert while it stayed there, and live
  again if the destination was ever pointed back.

- **A scheduled Facebook broadcast that no longer exists is replaced.** If the
  operator deletes the video on Facebook, the reschedule refuses forever. Three
  consecutive refusals now mean the broadcast is gone rather than briefly
  unreachable, and a fresh one is created. One transient failure changes
  nothing.

- **`meters.intervalMs` was stored, reported as saved, and ignored.** The value
  was captured into the metering sidecar's stdout handler when it spawned, and
  it is not part of that process's restart signature — so changing it did
  nothing at all until some unrelated edit happened to restart the meters.
  Lowering the interval to watch a quiet channel appeared to work and did not.
  The throttle now reads the current value per frame.
- **The VA-API probe tested the wrong GPU on a multi-GPU host.** Detection
  already ranked the render nodes under `/dev/dri` and handed the encode the
  right one; the probe ignored that and named `/dev/dri/renderD128` regardless.
  On a machine whose first render node is display-only — or a container passed
  `renderD129` — VA-API was tested on hardware it would never run on, failed,
  and was withheld from the editor on the strength of it. The probe now names
  the node detection chose. Where detection finds no usable node it still names
  `renderD128`, because a VA-API probe with no device fails on every machine
  including the ones where VA-API works, and "no node found" must not be
  reported as "this encoder is broken".

### Changed

- **A hung acceptance suite now says what it was waiting for.** Every suite
  carries a deadline below the CI job's own, so a stall reports the last step it
  reached, how long it had been there, the live processes and the tail of the
  server log — rather than being cancelled by the job ceiling with the log
  ending mid-sentence. The install steps are bounded too, after one job spent
  its whole budget inside `apt` and looked exactly like a suite hang.

- **A timed-out wait in the acceptance suites now reports what it observed.**
  `acceptance-failover` and `acceptance-mqtt` both asserted causes they had
  never measured, and `acceptance-mqtt` discarded `docker run`'s stdout, stderr
  and exit status at the call site. Failures now carry the trajectory of every
  sample taken, whether the value arrived just after the ceiling, and — for the
  broker — the container's exit code, status and last log lines. No timeout was
  changed and no retry was added; the flake rate is still the thing being
  measured. See issue #38.
- **The Windows service now announces the recording-truncation defect itself.**
  Stopping the service still truncates the in-progress recording — the graceful
  stop is a `CTRL_BREAK` console event and a service has no console — but the
  service no longer lets that happen silently. Every start writes a warning to
  the Event Viewer under event ID 3, naming the cause, the bound (only the
  segment in progress; earlier segments are finalised and playable), and the
  workaround. `docs/TROUBLESHOOTING.md` carries the same under the symptom.
  The defect itself is unchanged and remains open: both candidate fixes —
  allocating a console, or asking FFmpeg to quit over stdin — need a real
  Windows host to verify, and shipping an unverified fix to the process
  machinery every stream depends on is the worse trade.

## [0.1.0] — 2026-07-31


The first tagged release. Entries are grouped by what they do for an operator
rather than by which package changed.

**What it is for:** one encoder in, many platforms out, and **a different audio
mix for each destination**. Video is never re-encoded on a destination path, so
a dozen destinations cost roughly what one does.

**Read [Known limitations](#known-limitations-at-010) before deploying it.**

### Multi-source ingest

- **Several independent programmes in one instance.** Each source has its own
  ingest, its own destinations, its own renditions and its own recordings. The
  motivating case is a horizontal and a vertical programme from one OBS with the
  vertical plugin, going to different platforms.
- **One-port ingest, addressed by token.** All sources can share a single SRT
  port, with the per-source token acting as the address. This improves on the
  approach it was inspired by in two ways: the token *is* the streamid rather
  than being a server-wide secret alongside a name, and a stale connection is
  taken over after 3 seconds instead of locking out the encoder that is trying
  to reconnect.
- **Token rotation with a grace period.** A rotated token keeps working for five
  minutes, so rotating one does not cut off a live encoder.
- **Typed SRT rejections.** A refused publisher is told *why* — bad passphrase,
  no such token, already publishing — rather than getting a generic close.

### Failover and synthetic sources

- **Source selector.** A permanent relay between the ingest and everything
  downstream, so a source switch never restarts a destination and never drops a
  platform connection. Off by default: with it off the pipeline is byte-for-byte
  what it was without the feature.
- **Standby slate.** A generated picture, built at the *probed* geometry of the
  departed ingest so a copying destination does not choke on the change.
- **Silence tier.** A video-only ingest — which every major platform refuses —
  gets a synthesised silent stereo track, so destinations keep working instead of
  crash-looping on a track that is not there.
- **Automatic or manual return** to the primary, with a configurable stability
  window.

### Audio

- Per-destination multichannel routing: each destination selects and mixes its
  own tracks from one ingest.
- Track annotations describing what each incoming track *is* — mic, music,
  commentary, language — held against the source rather than the destination.
- EBU R128 loudness measured **after** routing, which is what the platform on the
  other end actually receives.
- Per-destination loudness targets, ducking, and audio delay.

### Post-production

- Recording, segmenting, and a retention policy with a free-space guard.
- Job queue with a governor, pausing, retries and a policy UI.
- Whisper transcription with FTS5 search across transcripts.
- Media proxies, keyframe indexes, and a clipper.
- Rolling clip buffer for capturing a moment that has already happened.

### Platforms

- Providers for the major platforms, with OAuth where it is offered.
- Unified chat across platforms.
- Platform capability presets, so a destination is configured with limits the
  platform will actually accept.
- **Metadata push** — title, description, category and tags, shaped per platform
  and reporting what each one refused rather than claiming success.
- **Kick's stream key is fetched, not pasted.** It was recorded here as
  unavailable for a long time: Kick publishes no `/streamkey` endpoint, so
  reading the endpoint list finds nothing. The key rides as `stream.key` on the
  channels response polyemesis already fetches, withheld unless the token
  carries `streamkey:read`.

### Chat moderation

Delete, ban, timeout and lift, across all four connected platforms. Before this
only one of the four could delete.

- A **cross-platform moderator user card**: what one person has said, newest
  last. No platform here publishes an API for a user's message history —
  Twitch's own mod card is a web-app feature backed by internal endpoints — so
  this reads polyemesis's retained scrollback. Shallower than Twitch's card, and
  it works across four platforms at once.
- Facebook's reversible **hide**, and a local-only hide everywhere else.
- Twitch **channel rules**: slow mode, followers-only, subscribers-only,
  no repeated messages.
- **Chat retention is configurable** — hours and message count — where it was
  previously hard-coded at two hours and 2000 messages.

Two platform traps are handled rather than inherited:

- `DELETE /helix/moderation/chat` with **no** `message_id` deletes every message
  in the channel and returns success. An empty id is refused before the URL is
  built.
- Kick counts timeouts in **minutes** where YouTube and Twitch count seconds, so
  a unified `600` would mean ten minutes on two platforms and seven days on the
  third. Each adapter converts at the last moment and rounds **up** — truncating
  30s to zero minutes would reach Kick as a permanent ban.

### Overlays

- **Image watermarks** on renditions: nine anchors, size and margins as
  percentages of the frame so the same logo is correct on landscape and vertical
  tiers alike.
- **Text overlays**: content, font, anchor, size, colour, margins and an optional
  background box. Two weights of Inter ship embedded, because FFmpeg's
  `drawtext` takes a font *path* and a container image has neither fontconfig nor
  a font file. A build without `drawtext` drops the text and keeps the picture up
  rather than failing the rendition.

### Per-destination tuning

For the destination that needs one setting nothing else does:

- **Transport and muxer**: mux queue limits in packets or bytes, read/write
  timeout, and the flags that stop FFmpeg guessing a duration it cannot know.
- **Resilience**: minimum and maximum reconnect backoff, and a give-up
  threshold counted on **consecutive** failures — so a destination that
  reconnects once an hour for a week never accumulates its way to the limit.
- **Audio**: bitrate, codec and mono output per destination.
- **Expert arguments**, spliced into the FFmpeg command line, with a dry run
  that tells you whether the result would start without starting it. Treat
  access to it as equivalent to shell access.

### Media uploads

- **Put a file on the server from the browser** — Library → Media. Before this,
  broadcasting from a file meant copying it onto the box yourself: fine on a
  Linux host you already have a session on, a wall for everyone running the
  container.
- **The filename you send is discarded.** This is the only endpoint where a
  caller supplies both the bytes and something path-shaped, so the server names
  the file itself, with a random suffix that also stops an upload overwriting
  an existing one by guessing its name.
- **Nothing is left behind by a failure.** The body streams to a temporary file
  and is renamed on success, so an oversized, empty or cancelled upload leaves
  no partial file — which matters because a half-written video is not visibly
  broken in a listing, and the operator would find out when the broadcast they
  scheduled went to air.
- **Free space is checked before the write**, not discovered during it. A
  filled volume takes the database and the HLS preview with it.
- **Every file carries an origin** — *uploaded*, *recorded* or *clip* — derived
  from which store it came out of rather than stored beside it. Uploads live in
  their own directory, so a retention policy written about footage the server
  captured cannot delete a file an operator deliberately put there.

### Automatic chat moderation

Three checkers, cheapest first, and a switch matrix over all of them.

- **Rules** — regular expressions over one message. Go's `regexp` is RE2, so an
  operator-supplied pattern cannot become a denial of service. Normalisation
  defeats the evasions people actually use: case, padded and doubled letters,
  zero-width characters, leetspeak and Cyrillic homoglyphs.
- **History** — a *sequence* from one author, and the layer neither of the
  others can replace. Rate and repetition are properties of a sequence, so no
  per-message classifier can see them: ten identical messages are individually
  innocuous and collectively the commonest abuse there is. Detects flooding,
  repeated phrases through case and spacing, link and mention spam, and
  sustained upper case.
- **Model** — an optional external API, asked only about what the first two
  could not settle, and **off by default**. Any OpenAI-compatible endpoint,
  including a locally hosted one.

**The switch is three-dimensional: action × platform × checker.** The same
action deserves different trust depending on the evidence — a regex hit is
reproducible and a model verdict is not — and an operator's exposure differs per
channel. Cells are gated by what each platform can actually do, and an
unavailable one renders inert *with its reason* rather than as an unticked box:
a switch that silently does nothing leaves the operator believing that channel
is protected.

**Nothing is automatic on a fresh install except flagging for review.**
Auto-ban is offered wherever the platform supports it and defaults off for every
checker, the model included — refusing to expose a capability is not a safety
feature, it is a decision taken away from the person who knows their channel.
There are per-platform and global kill switches, because mid-incident nobody
should be unticking fifteen boxes.

**It fails open, everywhere.** A model timeout, 500, rate-limit or unreadable
key lets the message through and flags it; a rule that will not compile is
logged loudly and the other checkers keep running; the action queue drops rather
than blocking, because blocking would stall chat for every viewer during exactly
the raid it exists for. A moderation outage must not silence a chat.

Messages display *before* automod sees them and are retracted after, so nothing
here can make chat feel slow.

### Operations

- Prometheus metrics, alert rules with webhook delivery, and schedules.
- **MQTT telemetry**, retained, with Home Assistant discovery — so the stream
  appears as entities in a dashboard the operator already runs.
- TLS with automatic certificate selection; HSTS deliberately opt-in and never
  sent over a self-signed certificate.
- API tokens, hashed at rest and individually revocable.
- **Sessions are revocable.** A stateless JWT cannot be deleted server-side, so
  changing the password bumps a token epoch and every token issued before it
  stops working — which is what "somebody else has my session" has to mean.
- Fourteen UI languages.
- **An interactive Linux installer** (`scripts/install.sh`), in Docker or binary
  mode, with rollback on failure. It exists for the traps that a hand-written
  `docker run` reliably falls into: `/udp` on the SRT port, a 30-second stop
  grace period so a recording is finalised rather than truncated, a UDP firewall
  rule, and `CAP_NET_BIND_SERVICE` when ACME is chosen. Binary mode refuses to
  proceed below the FFmpeg 6.0 floor — naming the caller's distribution version
  where it recognises it — and verifies the download against the release's
  published `SHA256SUMS`. It never handles a password, because there is no
  account until the first-run screen creates one.

### Security

Findings from a pre-release review, each fixed with a test that fails when the
fix is removed:

- **The Kick webhook now verifies its signature.** The hook existed, was
  nil-checked, and was never assigned at its one construction site — so
  verification silently never ran and the unguessable callback path was the only
  control. A missing verifier now refuses the delivery instead of skipping the
  check.
- First-run setup is atomic, so two concurrent requests to an uninitialised
  install cannot both create an administrator.
- The webhook path secret is compared in constant time, like the playout and SRT
  tokens already were.
- Every database read is a compile-time constant, so no value can reach the SQL
  text by construction.
- CI gained dependency, secret and static-analysis scanning — gated on proof
  that the scan actually examined the tree, after a run reported zero findings
  having read zero files.

### Fixed

Bugs worth naming, because each was found by measurement rather than by review:

- **A file destination could never restart.** FFmpeg refuses an existing output
  path, so the first respawn died with "already exists" and every one after it
  did the same — the destination crash-looped and the operator was left with a
  zero-byte recording. The output path is now chosen per spawn, and never
  overwrites existing footage.
- **`amix` was quietly dropping a three-track mix by about 9.5 dB**, because its
  default divides by the input count. Now `normalize=0`.
- **A negative audio delay was doing nothing.** `-itsoffset` shifts every stream
  of an input together, so audio and video moved in lockstep and the delivered
  offset measured 0 ms for every requested value. Replaced with the `setts`
  bitstream filter, which moves video alone and preserves `-c:v copy`.
- **A data race in the event broker** lost dropped-message counts under load.
- **Editing the ingest in Settings did nothing** once sources arrived, because
  the engine read the source row instead. Settings now writes through.
- **The Sources page sent server-computed fields back in its PUT**, so every save
  returned 400 and the control silently reverted.
- **A live-stream restart on every blur.** Editing a port committed on focus
  loss, so tabbing out of a field was an outage. Changes are now explicit.
- **The relay forwarded zero-length datagrams**, which FFmpeg reports as EOF —
  one empty datagram would have ended every consumer on a hub at once.
- **The VA-API image could not be built at all.** An automated
  `bump ubuntu from 24.04 to 26.04` changed `Dockerfile.vaapi`'s `FROM` line and
  left the FFmpeg pin at a 24.04 archive revision that does not exist on 26.04.
  Nothing caught it: the release workflow runs only on a tag, and the container
  suites run only on `main`. Found by rehearsing the release. The image now
  pins Ubuntu 26.04's FFmpeg **8.0.1**, up from 6.1.1, which also brings AV1
  VA-API and QSV encoders.

### Testing

- Acceptance suites that **measure** rather than assert: audio routing proved by
  bandpass RMS per track, video passthrough proved by frame-level `framemd5`
  identity, failover proved by counting destination restarts and backwards
  decode timestamps across a real source switch.
- Browser end-to-end suite against the shipped container.
- Fixed-value guards on suite check counts, after one suite reported
  *"7 passed, 0 failed — PASSED"* having silently skipped five checks.
- **A real broadcast now runs on Linux, macOS and Windows on every push**, not
  just a health check. The cross-platform job pushes a three-track stream in,
  compiles two destinations with different track selections, and measures
  per-band energy in each output — so a destination silently carrying the wrong
  mix fails, where a check that only asked whether FFmpeg exited 0 would pass.
  This is the part built on process groups, signals and path construction, which
  is exactly where Windows differs.

### Documentation

An accuracy pass over every page, checking each claim against the code rather
than reading for tone. What it found is why it was worth doing:

- The quickstart's **first command pulled from a registry the project has never
  published to**, and its OBS instructions contradicted `OBS.md` in a way that
  produced a working stream carrying a single audio track — configuring away the
  only reason to run polyemesis.
- `SECURITY.md` and `TLS.md` both promised that **every** response carries
  `X-Frame-Options: DENY`. The embeddable player deliberately drops it. A
  security policy that overstates its own coverage is worse than one that states
  the exception.
- `TLS.md` documented the `:80` redirect as `301`/`308`, which holds only when
  `tls.hostname` is set; without it the target comes from the client's own `Host`
  header and the redirect is deliberately temporary and uncacheable.
- `HARDWARE.md` contradicted itself on render-node selection. Detection does
  choose a node and the encode honours it — but the *probe* still names
  `renderD128` unconditionally, so on a multi-GPU host it tests the wrong device
  and the editor declines to offer VA-API on the strength of it.
- `INSTALL.md` claimed `make release` emits no Windows target. It emits two.
- The stale "nobody has run this on Windows" claim appeared on four separate
  pages. Platform status is now stated on two axes instead of one adjective —
  what CI proves, which is identical for Linux, macOS and Windows, and what
  operating it proves, which is not — so a reader can see where the platforms
  are equal rather than inferring it from a word.
- Every file link and all 110 anchor links across 51 markdown files were
  machine-checked and resolve.

### Known limitations at 0.1.0

Stated here rather than discovered later. None is a bug; each is a boundary.

#### Access and identity

- **One user, no roles.** Access to the UI is full control of the server's
  streaming and — through expert mode and file destinations — meaningful control
  of the machine. Put a reverse proxy in front if you need access control.
- **No audit log.** Nothing records which change was made when, or from where.
- **OAuth needs your own developer app.** Signing in to YouTube, Twitch, Kick or
  Facebook works, but each operator registers their own application per platform;
  there is no shared one. Stream URL and key need none of this. See
  [PLATFORMS.md](docs/PLATFORMS.md).

#### Ingest

- **RTMP serves at most one source**, carries a single stereo pair, and is
  unencrypted. SRT is the path that makes this product worth running.
- **Enhanced RTMP / multitrack FLV is not implemented.** The `enhancedRtmp`
  config key parses and nothing branches on it.

#### Operating systems

- **Windows is tested, not operated.** It clears the same CI floor as Linux and
  macOS including a measured broadcast, but nobody has run a real show on it,
  and **stopping the service truncates an in-progress recording** — the graceful
  stop is a `CTRL_BREAK_EVENT`, which Windows delivers only through a console.
- **linux/arm64 is built and verified at the ELF level, not run.**
- **Homebrew's FFmpeg on macOS has no libsrt.** Use Docker or a build with
  `--enable-libsrt`.

#### Video

- **No hardware encode has ever been run on real GPU hardware.** Detection,
  refusal and fallback are all tested with shims; a successful NVENC, QSV or
  VA-API encode has not been observed. See [HARDWARE.md](docs/HARDWARE.md).
- **The VA-API probe names `/dev/dri/renderD128` unconditionally**, so on a
  multi-GPU host it can test the wrong device and decline to offer VA-API on
  the strength of it. Detection picks the right node; the probe was never wired
  to it.
- **FFmpeg 6.x cuts a copied clip up to one GOP long.** Use 8.x for exact
  out-points.
- No HDR tone-map path, no compositing or video grid, no Decklink/SDI.

#### Playout

- **Scheduled file broadcast joins mid-file, not at frame 0**, and there is no
  playlist sequencing. Files can now be uploaded from the browser, but they play
  one at a time. See [SCHEDULED-BROADCAST.md](docs/SCHEDULED-BROADCAST.md).
- **LL-HLS is not implemented and was declined deliberately** — FFmpeg cannot
  emit partial segments at all. Preview latency was tuned to 2.2–3.2 s instead.

#### Automatic moderation

- **The model checker sends chat messages to whatever endpoint you configure.**
  Off by default, and a locally hosted OpenAI-compatible model keeps everything
  on your hardware. The rules and history checkers send nothing anywhere. The
  endpoint is **not** filtered against private addresses, deliberately: a
  private address is the recommended deployment, and only an admin — who can
  already read your tokens — can set it. See [SECURITY.md](SECURITY.md).
- **No moderation is perfect and this one is not tuned on your community.**
  Every irreversible action starts off for that reason.
- **Automod has not been run against a real raid.** The bounds are tested — the
  author ring holds 5,000 distinct authors, evicts the idle ones, and is capped
  at 20,000 regardless of idleness — but a live incident on a busy channel has
  not happened.

#### Streaming platforms

- **Instagram Live cannot work** and is marked unsupported rather than shipped
  as a preset that never connects.

[Unreleased]: https://github.com/rainmanjam/polyemesis/compare/v0.15.0...HEAD
[0.15.0]: https://github.com/rainmanjam/polyemesis/compare/v0.14.0...v0.15.0
[0.14.0]: https://github.com/rainmanjam/polyemesis/compare/v0.13.0...v0.14.0
[0.13.0]: https://github.com/rainmanjam/polyemesis/compare/v0.12.0...v0.13.0
[0.12.0]: https://github.com/rainmanjam/polyemesis/compare/v0.11.0...v0.12.0
[0.11.0]: https://github.com/rainmanjam/polyemesis/compare/v0.10.0...v0.11.0
[0.10.0]: https://github.com/rainmanjam/polyemesis/compare/v0.9.0...v0.10.0
[0.9.0]: https://github.com/rainmanjam/polyemesis/compare/v0.8.0...v0.9.0
[0.8.0]: https://github.com/rainmanjam/polyemesis/compare/v0.7.0...v0.8.0
[0.7.0]: https://github.com/rainmanjam/polyemesis/compare/v0.6.0...v0.7.0
[0.6.0]: https://github.com/rainmanjam/polyemesis/compare/v0.5.0...v0.6.0
[0.5.0]: https://github.com/rainmanjam/polyemesis/compare/v0.4.0...v0.5.0
[0.4.0]: https://github.com/rainmanjam/polyemesis/compare/v0.3.0...v0.4.0
[0.3.0]: https://github.com/rainmanjam/polyemesis/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/rainmanjam/polyemesis/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/rainmanjam/polyemesis/releases/tag/v0.1.0
