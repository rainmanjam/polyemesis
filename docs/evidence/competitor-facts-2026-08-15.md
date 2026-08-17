# Competitor facts, checked 2026-08-15

Gathered by an independent research pass with a source URL required for every
claim.

## What may be published about another product

Every claim must trace to a **dated, citable source**, and the date must be shown
to the reader next to any figure that can move — pricing above all.

Two kinds of source qualify:

1. **This file.** Secondhand research, a URL per claim, checked on the date in
   the title.
2. **A primary source, quoted.** A line from the product's own repository or
   documentation, reproduced on the page with its file path and the date it was
   read.

**THE SECOND IS THE STRONGER OF THE TWO AND THIS RULE USED TO FORBID IT.** The
header said this file was "the ONLY sanctioned source". Written to stop claims
being invented from memory, it also outlawed the best evidence available: a
comparison page that quotes a competitor's own source code, names the file and
dates the read is showing a reader something they can verify themselves, which
no summary of mine can do.

`/comparison` was audited against the narrow rule and reported as a provenance
breach. It is not. It reads competitors' repositories directly, quotes the line,
prints the path, and stamps the date — a higher standard than this file meets. A
rule that marks that as non-compliant is the rule that is wrong, and it has been
corrected rather than the page.

**A PAGE-LEVEL "WE SURVEYED THESE PRODUCTS" FOOTER IS NOT A SOURCE FOR A CELL.**
The amended rule was checked adversarially and this is the hole it left: two
capability rows on `/comparison` asserted "Yes" for both competitors on the
strength of a general re-check stamp at the foot of the page. A footer says
somebody looked at the products. It does not say anybody checked *that row*.
Provenance is per claim or it is decoration.

What remains forbidden is unchanged and is the thing that actually matters: a
claim about another product with **no** dated source of either kind. A star
count nobody re-checks, a plan limit from memory, a capability someone assumed.
Those were found on `/comparison` and removed — and they were breaches under
either version of this rule.

## THE CLAIM THAT MUST NOT BE MADE

obs-multi-rtmp and Aitum Multistream **do** send different audio to different
destinations. They SELECT an OBS audio track per output, and the summing that
produced those tracks happened upstream in OBS's Advanced Audio Properties.

Writing "they cannot send different audio per destination" is **false**, and it
is the exact overclaim this file exists to prevent. The true and narrower
argument:

> They select a track OBS has already mixed. Building three different mixes means
> configuring three track layouts in OBS and encoding each output on the stream
> PC. polyemesis sums the tracks server-side, so one upload and one video encode
> feed every destination.

Restreamer and restream.io are a different case: neither can produce a different
mix per destination at all. Restreamer can copy, transcode the codec, or force
silence; restream.io duplicates one stereo track everywhere.

---

### 1. Restreamer (`datarhei/restreamer`)

*   **A. AUDIO:** **Cannot mix or sum tracks.** The web UI only allows copying the primary stream audio, transcoding the codec (e.g., AAC/MP3), or forcing silence per egress target ([Source: Datarhei Docs](https://docs.datarhei.com/restreamer/)). It does not provide an audio matrix to sum discrete channels into a new stereo mix. Multi-track re-routing is only possible by writing custom FFmpeg CLI pipelines via the underlying REST API ([Source: Datarhei GitHub](https://github.com/datarhei/restreamer)).
*   **B. VIDEO:** Supports both stream **copy** (passthrough) and **per-destination re-encoding** via software/hardware FFmpeg ([Source: Datarhei Docs](https://docs.datarhei.com/restreamer/)). It supports **shared renditions**; multiple destination processes can pull from the single ingested internal stream without re-encoding.
*   **C. INGEST:** Supports **SRT** (single audio track in UI), **RTMP**, RTMPS, RTSP, and HLS ([Source: Datarhei Docs](https://docs.datarhei.com/restreamer/)). Multitrack SRT is not exposed or routable in the UI.
*   **D. PRICE:** **100% Free** and self-hosted ([Source: Datarhei GitHub](https://github.com/datarhei/restreamer)).
*   **E. LICENCE & HOSTING:** **Apache-2.0 License** ([Source: Datarhei License](https://github.com/datarhei/restreamer/blob/main/LICENSE)); **Self-hosted** (Docker container).
*   **F. CHANGES IN LAST 12 MONTHS:** Core stable release line remains at **v2.12.0** with ongoing dependency updates and Docker maintenance commits ([Source: Datarhei Releases](https://github.com/datarhei/restreamer/releases)). No new UI audio-matrix features added.

---

### 2. restream.io

*   **A. AUDIO:** **Neither.** It cannot sum tracks or select different tracks per destination. It duplicates the single ingested stereo audio track identically across all enabled endpoints ([Source: Restream Support](https://support.restream.io/)).
*   **B. VIDEO:** Cloud **copy** (passthrough replication) by default ([Source: Restream Support](https://support.restream.io/)). Shared renditions: Yes, a single incoming stream is distributed to multiple destinations. Cloud re-encoding is only applied when using the browser-based Restream Studio or enterprise transcode services.
*   **C. INGEST:** **RTMP**, **RTMPS**, and **SRT** ([Source: Restream SRT Guide](https://support.restream.io/en/articles/5198083-how-to-stream-with-srt-to-restream)). **Multitrack SRT is not supported** (single stereo stream only).
*   **D. PRICE:**
    *   **Free Plan:** Allows streaming to **2 simultaneous channels** (personal profiles/platforms only); includes Restream watermark on Studio; Studio output capped at 720p; **excludes** Custom RTMP destinations and Facebook Pages/Groups ([Source: Restream Pricing](https://restream.io/pricing)).
    *   **Standard Plan:** $19/mo (or $16/mo billed annually at $190/yr); 3 channels; no watermark; enables Custom RTMP destinations ([Source: Restream Pricing](https://restream.io/pricing)).
    *   **Professional Plan:** $49/mo (or $39/mo billed annually at $470/yr); 5 channels; 1080p Full HD Studio; Dual Format (horizontal + vertical) output ([Source: Restream Pricing](https://restream.io/pricing)).
*   **E. LICENCE & HOSTING:** **Proprietary SaaS** ([Source: Restream Terms](https://restream.io/terms)).
*   **F. CHANGES IN LAST 12 MONTHS:** Rolled out **Dual-Format streaming** (transmitting separate horizontal and vertical video outputs simultaneously) and integrated automated AI Clip generation tools ([Source: Restream Pricing](https://restream.io/pricing)).

---

### 3. obs-multi-rtmp (`sorayuki/obs-multi-rtmp`)

*   **A. AUDIO:** **SELECTS** a track. It allows you to select which OBS Audio Track / Mixer ID (Tracks 1–6) is assigned to each output ([Source: obs-multi-rtmp GitHub](https://github.com/sorayuki/obs-multi-rtmp)). It does **not** sum tracks internally within the plugin (summing of sources into Tracks 1–6 is performed by OBS Studio's internal audio mixer).
*   **B. VIDEO:** Supports both **copy** (`Get from OBS` encoder sharing) and **independent re-encoding** per destination (e.g., separate NVENC/x264 instances with custom bitrates/resolutions) ([Source: obs-multi-rtmp GitHub](https://github.com/sorayuki/obs-multi-rtmp)). Shared renditions: Yes, via `Get from OBS`.
*   **C. INGEST:** Plugin inside OBS Studio; inherits OBS scene/source ingestion (SRT, multitrack SRT via Media Source, RTMP, direct hardware inputs) ([Source: OBS Overview](https://obsproject.com/wiki/OBS-Studio-Overview)). Outputs over RTMP/RTMPS.
*   **D. PRICE:** **Free ($0)** ([Source: obs-multi-rtmp GitHub](https://github.com/sorayuki/obs-multi-rtmp)).
*   **E. LICENCE & HOSTING:** **GPL-2.0 License** ([Source: obs-multi-rtmp License](https://github.com/sorayuki/obs-multi-rtmp/blob/master/LICENSE)); **OBS Studio Plugin** (Local).
*   **F. CHANGES IN LAST 12 MONTHS:** Regular compatibility releases for OBS Studio 30.x/31.x/32.x ([Source: obs-multi-rtmp Releases](https://github.com/sorayuki/obs-multi-rtmp/releases)).

---

### 4. Aitum Multistream (`obs-aitum-multistream`)

*   **A. AUDIO:** **SELECTS** a track. Each configured destination can be assigned a specific OBS Audio Track (1–6) ([Source: Aitum Multistream GitHub](https://github.com/Aitum/obs-aitum-multistream)). It does **not** sum tracks within the plugin itself (summing is done upstream by OBS's Advanced Audio Properties).
*   **B. VIDEO:** Supports **copy** (cloning the main OBS stream output) and **independent re-encoding** per destination with custom resolution, bitrate, and encoder selection ([Source: Aitum Multistream GitHub](https://github.com/Aitum/obs-aitum-multistream)).
*   **C. INGEST:** Plugin inside OBS Studio (inherits OBS support for SRT, multitrack SRT, RTMP, and local captures) ([Source: Aitum Multistream GitHub](https://github.com/Aitum/obs-aitum-multistream)). Outputs over RTMP/RTMPS.
*   **D. PRICE:** **100% Free ($0)** for the Multistream plugin ([Source: Aitum Website](https://aitum.tv/)). *(Note: The separate "Aitum App" desktop automation suite is paid at $4.99/mo, but is not required to run Multistream)* ([Source: Aitum Pricing](https://aitum.tv/)).
*   **E. LICENCE & HOSTING:** **GPL-2.0 License** ([Source: Aitum Multistream License](https://github.com/Aitum/obs-aitum-multistream/blob/main/LICENSE)); **OBS Studio Plugin** (Local).
*   **F. CHANGES IN LAST 12 MONTHS:** Bundled into the **Aitum StreamSuite** packaging alongside Aitum Vertical, integrating unified multi-chat and metadata panels ([Source: Aitum StreamSuite](https://aitum.tv/)).

---

### G. Twitch Enhanced Broadcasting & OBS "Twitch VOD Track"

*   **Twitch Enhanced Broadcasting VOD Track Support:** **Does not currently support independent VOD audio splitting properly.** Enhanced Broadcasting replaces standard RTMP signaling with Enhanced RTMP multi-rendition video/audio signaling ([Source: Twitch Multiple Encodes Help](https://help.twitch.tv/s/article/multiple-encodes)). In current OBS Studio releases, when Enhanced Broadcasting is enabled, the legacy metadata-flagged Twitch VOD track is ignored or conflicts with the multitrack payload, resulting in live audio being archived to the VOD regardless of track exclusions ([Source: OBS Studio Issue #13636](https://github.com/obsproject/obs-studio/issues/13636)). Broadcasters requiring separate VOD audio must disable Enhanced Broadcasting.
*   **What OBS "Twitch VOD Track" Does:** It **SELECTS** a single designated audio track (e.g., Track 2) from OBS’s 6 tracks to be encoded and transmitted as a secondary audio stream tagged with FLV metadata (`@setDataFrame`) for Twitch VOD processing ([Source: OBS Studio Docs](https://obsproject.com/wiki/OBS-Studio-Overview)). It does **NOT** mix audio itself; the mix is defined by which checkboxes are enabled in OBS's *Advanced Audio Properties* matrix.

---

### H. `obs-multi-rtmp` Maintenance Status

*   **Status:** **Actively maintained** by developer `sorayuki` ([Source: obs-multi-rtmp Releases](https://github.com/sorayuki/obs-multi-rtmp/releases)).
*   **Latest Release:** **v0.7.4.3** ([Source: obs-multi-rtmp GitHub Releases](https://github.com/sorayuki/obs-multi-rtmp/releases)).


---

# 5. StreamElements — added 2026-08-15, same sourcing standard

**Two things share the name and must not be merged.**

**StreamElements Cloud (web platform)** offers **no** video restreaming or
multistreaming at all. Its dashboard is unified multi-platform chat, activity
feeds, overlays and tipping.
[Source](https://support.streamelements.com/hc/en-us/articles/19133887258258-Multi-Platform-Activity-Feed-Unified-Chat-Setup)

**SE.Live** (formerly OBS.Live) is the only video multistreaming StreamElements
ships, and it is a **local OBS Studio plugin**.
[Source](https://streamelements.com/selive)

## SE.Live

* **A. AUDIO — SELECTS a track, does not sum.** You choose which single OBS audio
  track (1–6) routes to each platform output. It cannot sum arbitrary tracks into
  a new stereo mix at the output stage; all summing happens upstream in OBS's
  Advanced Audio Properties matrix.
  [Source](https://support.streamelements.com/hc/en-us/articles/18853755490322-SE-Live-The-Complete-Guide-Setup-Features-Multistreaming-Troubleshooting)
  → This is the SAME case as obs-multi-rtmp and Aitum. The overclaim warning at
  the top of this file applies to it in full.

* **B. VIDEO.** Both shared copy and independent per-destination re-encode, with
  custom resolution, aspect (16:9 and 9:16), bitrate, framerate and encoder.
  Re-encodes run concurrently on the local machine. [Same source]

* **C. INGEST / EGRESS.** No ingest — local OBS capture only, not a relay.
  Egress is **RTMP/RTMPS only**; **SRT and multitrack SRT are not supported**.
  [Same source]

* **D. PRICE. Free, $0**, no paid tier, no paywall, and **no cap on simultaneous
  destinations** — bounded only by local encoding capacity and upstream
  bandwidth. [Source](https://streamelements.com/selive)

* **E. LICENCE / HOSTING. Windows only**; macOS and Linux are not supported.
  Core plugin is **GPL-2.0**
  ([repo](https://github.com/StreamElements/obs-streamelements-core)); the
  connected cloud backend is proprietary SaaS.
  [Source](https://streamelements.com/selive)

* **F. LAST 12 MONTHS.** Canvas system expanded for simultaneous dual-format
  (16:9 + 9:16) output in one OBS session; Twitch Dual-Output Beta integration
  added. Actively supported, no deprecation. [Same source]


---

# 6. MistServer (OptiMist Video B.V. / DDVTech) — added 2026-08-15

* **A. AUDIO — SELECTS, and the scoping matters.** The core routing server
  selects among existing tracks (by track ID `i#`, codec, language, bitrate, or
  the `optimal`/`first`/`last` heuristics). It does **not** natively sum tracks
  into a new mix per destination.
  [docs](https://docs.mistserver.org/) ·
  [changelog](https://releases.mistserver.org/changelog)

  **BUT IT IS NOT INCAPABLE OF MIXING, and any claim must say which.** Running an
  external Stream Process (`MistProcAV`, an FFmpeg pipeline) decodes, filters and
  injects a new track back into the buffer — so `amix`, `loudnorm` and `ebur128`
  are reachable, just not in the routing server. Writing "MistServer has no
  amix" without that scope is the same error as saying the OBS plugins cannot
  send different audio per destination: true of one layer, false of the product.

* **B. VIDEO.** Passthrough/copy by default — tracks land in a shared memory
  buffer (DTSC) and are remuxed per output protocol without re-encoding.
  Transcoding is a Stream Process whose output registers back into the shared
  buffer, so a rendition is reachable from every output protocol at once rather
  than transcoded per output. [docs](https://docs.mistserver.org/)

* **C. INGEST.** SRT (`MistInSRT`; caller and listener, passphrase, raw TS) and
  RTMP (`MistInRTMP`; RTMPS and Enhanced RTMP). **Multitrack audio ingest is
  supported** — multiple audio tracks in one stream over SRT via MPEG-TS PID
  demuxing, and over RTMP/E-RTMP/RTSP; all are retained as discrete tracks.
  [docs](https://docs.mistserver.org/)

* **D. PRICE. Free, with no stream limits or feature locks.** Optional paid SLAs,
  custom development and consulting.
  [download](https://mistserver.org/download)

* **E. LICENCE / HOSTING. Self-hosted**, and **public domain (The Unlicense)** —
  moved off the legacy aGPLv3 dual-licence.
  [licence](https://mistserver.org/license_types) ·
  [COPYING.md](https://github.com/DDVTECH/mistserver/blob/master/COPYING.md)

* **F. LAST 12 MONTHS.** v3.11.2 (Jul 2026) `SRT_ACCEPT` trigger, libsrt 1.5.6;
  v3.11 (Jul 2026) WebCodecs player, "Optimal" track sorting, `MistProcTSDemux`,
  JWT stream access tags; v3.10 `MistProcComposer` (compositing, PiP, failover).
  [changelog](https://releases.mistserver.org/changelog)

---

# 7. Castr — added 2026-08-15

* **A. WHAT IT IS.** Hosted multi-CDN multistreaming SaaS. Ingests RTMP/SRT/WHIP
  and restreams to 30+ platforms, plus an embeddable player, 24/7 playout and
  live-to-VOD. [multistream](https://castr.com/multistream/)

* **B. PRICE — THERE IS NO FREE TIER.** A 7-day trial on Starter (no card), then
  access locks. As of Aug 2026: Starter $19.99/mo ($199.99/yr) — 2 concurrent
  streams, 6 destinations, 200 GB/mo. Standard $49.99/mo — 10 destinations.
  Premium $199.99/mo — 20 destinations, ABR, API. Ultra $349.99/mo — 30
  destinations, failover ingest. [pricing](https://castr.com/pricing/)

  This matters for any sentence pairing Castr with Restream: **restream.io has a
  free tier and Castr does not.** Naming them together as interchangeable
  "hosted options" is fine; implying a shared free tier is not.

* **C. PER-DESTINATION AUDIO — NO.** All destinations receive identical stream
  audio. A higher-tier transcoding "Filter Track" feature can isolate an
  incoming track number and change its bitrate/volume, but cannot deliver
  independent mixes to separate destinations from one ingest.
  [multistream](https://castr.com/multistream/)

* **D. LAST 12 MONTHS.** Pre-recorded live scheduling, automated live-to-VOD
  replacing the manual recordings tab, and a repackaging into four self-serve
  tiers with annual bandwidth pooling. [pricing](https://castr.com/pricing/)

  *Uncertain and therefore unpublishable:* whether sub-second trial
  configurations carry a watermark.

---

# 8. Streamlabs Multistream — added 2026-08-16, same sourcing standard

Primary source, read **16 August 2026**: <https://streamlabs.com/multistream>,
including its own FAQ block on that page. Every quote below is theirs, verbatim,
including two grammatical slips reproduced rather than tidied so the quote can be
checked against the page.

## What it is

> Streamlabs Multistream is a feature that allows you to broadcast your live
> stream to multiple platforms at once. With Multistream you can go live to
> Twitch, YouTube, TikTok, Kick, Facebook, Patreon, X(Twitter), and RTMP
> destinations simultaneously. **We process your stream on our servers** so you
> can expand your reach without straining your PC.

That last clause is the architecture, stated by them: it is a CLOUD relay. The
stream leaves the operator's machine and is fanned out by Streamlabs. This is the
same shape as restream.io and Castr, and the opposite of a plugin.

> Streamlabs utilizes a cloud-based multistreaming system mean that you only send
> one stream (or two with Dual Output enabled — one vertical and one horizontal)
> to Streamlabs and our servers handle the rest.

[sic — "system mean that"]

## Price gate

> Streamlabs Multistream is an Ultra feature. If you want to multistream for
> free, you can go live to one vertical and one horizontal destination at the
> same time with Dual Ouput, no subscription required.

[sic — "Dual Ouput"]

So: **multistreaming proper requires the paid Ultra tier.** The free path is Dual
Output, and its scope is exactly two destinations of prescribed shapes — one
vertical, one horizontal. From the same page:

> Stream to one platform per canvas, completely free.

**NO PRICE FIGURE IS RECORDED HERE ON PURPOSE.** The Ultra page was not read on
16 August 2026, and a subscription price is the single most movable fact in this
file. Any page citing a Streamlabs price needs its own dated read.

## Ingest paths

> Yes, you have two options if you want to use Streamlabs to Multistream from
> OBS. **Streamlabs Plugin for OBS** — Multistreaming is built into the
> Streamlabs Plugin for OBS. **RTMP** — If you want to use OBS without the
> Plugin, you can set up your platforms in your Multistream setting in Dashboard
> and use the RTMP URL and Stream Key from this page as your stream destination
> in OBS.

RTMP in, therefore. Nothing on the page offers an SRT ingest, and nothing offers
a multitrack one.

## Chat

> You can read chat and post comments from Multichat for: YouTube, Twitch,
> Facebook Pages. You can read chat from Multichat for: Kick, X (Twitter),
> Facebook Profiles.

## WHAT THIS SOURCE DOES NOT SAY, AND WHICH THEREFORE MUST NOT BE CLAIMED

The page says **nothing** about per-destination audio. Not that it is absent —
nothing either way. Under the rule at the head of this file that means a
comparison row states what polyemesis does and **leaves the Streamlabs cell
blank**. It does not assert a "No".

This matters more here than it did for the plugins. obs-multi-rtmp, Aitum and
SE.Live were all read at the source level, so their track-SELECTION behaviour is
known and the narrow true claim could be written. For Streamlabs the relay is
somebody else's server and there is no source to read. Absence of a claim on
their marketing page is not evidence of absence in their product.
