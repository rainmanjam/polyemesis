# Competitor facts, checked 2026-08-15

Gathered by an independent research pass with a source URL required for every
claim. **This file is the ONLY sanctioned source for competitor claims on the
site.** Anything published about another product must trace to a line here, and
the "checked" date above must be shown next to any figure that can move —
pricing above all.

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
