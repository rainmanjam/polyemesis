# Product and Positioning Recommendations for polyemesis

**Author:** Product & Positioning Reviewer (Antigravity)  
**Date:** August 9, 2026  
**Target File:** `docs/notes/rec-product-agy.md`  

---

## Executive Summary

`polyemesis` possesses a rare and technically elegant differentiator in the live streaming ecosystem: **per-destination multichannel audio routing from a single multitrack ingest, while stream-copying video untouched (`-c:v copy`)**. 

However, the current marketing, documentation, and product copy are heavily weighted toward **mechanisms** (routing matrices, filtergraphs, ref-counted renditions, relay hubs) rather than **the high-value creator problems** it solves (DMCA copyright strikes on YouTube, muted Twitch VODs, automated VOD archiving with AI transcript search, per-platform loudness compliance). Furthermore, the initial onboarding flow in `docs/QUICKSTART.md` introduces significant friction by requiring custom OBS FFmpeg recording setups and manual developer OAuth app configurations.

This review answers the four core product positioning questions with direct empirical evidence from the repository and provides seven prioritized, actionable recommendations.

---

## Answers to Core Questions

### 1. Who is this actually for, and does the current documentation and site copy reach that person? Would someone with the problem find this by searching?

#### Target Audience Profile
Based on repository constraints (`README.md:92`, `docs/FAQ.md:116`, `docs/COMPARISON.md:88` — *"One user, no roles"*), `polyemesis` is built for **tech-savvy solo creators, live-stream broadcasters, and independent A/V operators** who stream simultaneously to multiple platforms (YouTube, Twitch, Kick, Facebook) using OBS Studio with multitrack audio output.

Specific target personas:
1. **Gaming & IRL Streamers:** Wanting to play copyrighted music live on Twitch while sending a clean, music-free mix to YouTube to avoid Content ID copyright strikes or channel strikes.
2. **Multilingual / Dual-Commentary Broadcasters:** Streaming primary game/mic audio to Channel A while sending secondary commentary/translated audio to Channel B.
3. **Houses of Worship & Event Broadcasters:** Routing room/PA audio to local feeds, a balanced stereo mix to YouTube/Facebook, and preserving all raw uncompressed audio tracks for post-production editing.
4. **Vertical + Horizontal Dual-Streamers:** Running OBS vertical plugins sending parallel feeds to TikTok/Instagram alongside landscape streams.

#### Evaluation of Current Copy & Discoverability
* **Current Copy Reach:** The site (`web/src/pages/index.astro:50-62`) and `README.md:8-14` state the capability clearly: *"Six tracks from OBS. A different mix to every platform."* However, the messaging quickly shifts into dense technical jargon (*"SRT multitrack ingest"*, *"mix matrices"*, *"filtergraphs"*, *"ref-counted renditions"*).
* **Search Discoverability (SEO):** **Weak.** A streamer facing a DMCA issue will search Google or YouTube for:
  * *"how to stream music on twitch without youtube copyright strike"*
  * *"send obs track 2 clean mix to youtube"*
  * *"restream without music on youtube VOD"*
  * *"obs multi track audio restream self hosted"*
  
  Currently, meta tags in `web/src/pages/index.astro:36-37` and `web/src/pages/Base.astro` feature titles like `"polyemesis — self-hosted restreaming server with per-destination audio routing"`. These titles use engineering terms (*"per-destination audio routing"*) rather than problem terms (*"avoid copyright strikes"*, *"clean music mix"*). As noted in `docs/notes/website-copy-review-2026-08-08.md:141` (D23), all page titles exceed 60 characters and truncate in search engines without capturing user intent.

---

### 2. What is the first-run experience, and where would a new user give up? Trace it from `docs/QUICKSTART.md`.

Tracing the journey of a new user following `docs/QUICKSTART.md`:

```
[1. Check FFmpeg] ──► [2. Run Docker/Binary] ──► [3. Point OBS] ──► [4. Annotate Tracks] ──► [5. Add Dest] ──► [6. Check Meters]
   (OS / SRT check)      (Port 6000/udp missing 1935)    (CRITICAL FRICTION)     (Easy step)          (OAuth Friction)       (Verification)
```

#### Step-by-Step Friction Breakdown & Drop-off Points

1. **Step 0: Check FFmpeg first (`docs/QUICKSTART.md:8-25`):**
   * *Friction:* Users running macOS Homebrew FFmpeg fail the `grep -x srt` test. Linux users on Ubuntu 22.04 / Debian 12 fail the 6.0 version floor. While Quickstart correctly advises Docker as a fallback, users without Docker installed hit an immediate wall before starting.
2. **Step 1: Run it (`docs/QUICKSTART.md:27-44`):**
   * *Friction:* Docker command on `index.astro:70` and Quickstart omits `-p 1935:1935` (`docs/notes/website-copy-review-2026-08-08.md:129` D16). If a user attempts RTMP ingest fallback first, the connection is refused.
3. **Step 3: Point OBS at it (`docs/QUICKSTART.md:55-88`) — PRIMARY GIVE-UP POINT:**
   * *Major Barrier 1 (Hijacking Local Recording):* Quickstart instructs users to configure multitrack SRT under **OBS Settings → Output → Advanced → Recording → Custom Output (FFmpeg) → Output to URL**. In OBS, using Recording to push SRT **disables native local recording**. Streamers who rely on local high-quality VOD recording in OBS are forced to sacrifice local recording or figure out dual-output plugins.
   * *Major Barrier 2 (UI Workflow Inversion):* Streamers must press **"Start Recording"** in OBS to stream into polyemesis, and explicitly **NOT** press "Start Streaming". Habitual muscle memory leads users to press "Start Streaming", which fails silently.
   * *Major Barrier 3 (Ingest Mode Trap):* In 0.4.0+, ingest mode has no default (`CHANGELOG.md:486`). If a user selects RTMP ingest mode on a box with FFmpeg < 7.1, classic RTMP delivers only 1 audio track (`docs/FAQ.md:58`), causing multitrack routing to quietly deliver silence without an error.
4. **Step 5: Add a Destination (`docs/QUICKSTART.md:98-109`, `docs/PLATFORMS.md`):**
   * *Secondary Give-up Point:* While adding a manual RTMP URL + stream key is simple, setting up automated stream keys, title/category pushes, or chat integration requires setting up custom developer OAuth apps on Google Cloud Console, Twitch Developer Console, and Meta Developers (`docs/PLATFORMS.md:160-250`). This multi-step API key setup will cause non-technical users to abandon advanced platform integrations.

---

### 3. The differentiator is real and rare. Is it explained in terms of the PROBLEM or the MECHANISM? Which would reach more people?

#### Current Framing in the Repository
* **Mechanism-first framing dominates:**
  * Landing page H1 (`web/src/pages/index.astro:50`): *"Six tracks from OBS. A different mix to every platform."*
  * Features page (`web/src/pages/features.astro:12-32`): *"Checkbox-per-track in simple mode, or a full channel-to-output mix matrix with a gain per cell..."*
  * Comparison page (`web/src/pages/comparison.astro:64`): *"The audio is the difference."*
  * Navigation & Titles: *"Per-destination multichannel audio routing"*, *"Mix Matrix"*, *"Renditions"*, *"Filtergraphs"*.
* **Problem-first framing is buried:**
  * `README.md:22-32` has a good section titled *"The problem it solves"*, mentioning DMCA music vs clean tracks.
  * `web/src/pages/index.astro:174-204` contains a 3-paragraph section explaining copyright strikes on YouTube and muted Twitch VODs.

#### Why Problem-Based Framing Reaches 10x More People
Streamers and creators do **not** search for *"multichannel matrix routing servers"* or *"FFmpeg filtergraph builders"*. They experience concrete, high-stakes pains:
1. **Fear of Copyright Strikes / Channel Termination:** YouTube Content ID flags background music, risking strikes or demonetization.
2. **Muted VODs on Twitch:** Twitch mutes VOD segments containing copyrighted tracks.
3. **Volume Mismatches:** Streamers get complaints that YouTube is too quiet while Twitch is deafening.
4. **Post-Production Bottlenecks:** Editors spending hours manually sifting through raw 3-hour VODs to find specific highlight moments.

**Verdict:** Positioning `polyemesis` around **the PROBLEM** (DMCA protection, automated clean mixes, VOD searchability) while using **the MECHANISM** (SRT multitrack, `-c:v copy`, routing matrix) as the *technical proof* will dramatically increase conversion and organic search traffic.

---

### 4. What is claimed that is not earned, or under-claimed that is?

#### A. Over-claimed (Claimed but carrying unstated caveats)

| Claimed Feature | Location | Reality & Repo Evidence |
|---|---|---|
| **"Unlimited destinations"** | `README.md:52`, `docs/COMPARISON.md:100` | Bounded by CPU and bandwidth. Each destination requires audio decoding, matrix mixing, and AAC re-encoding. Real benchmarks cap a 4-core box at ~96 destinations (`docs/FAQ.md:29-35`, `docs/notes/website-copy-review-2026-08-08.md:111` D11). `comparison.astro:39` accurately states "No configured cap", creating copy drift. |
| **"Unified Chat & Moderation across all 4 platforms"** | `README.md:53-58`, `web/src/pages/features.astro:68-76` | Facebook chat sending is **unsupported/unverified** (read/moderate only) (`docs/PLATFORMS.md:30-40`). Kick chat requires a **public HTTPS URL for webhooks** (`docs/PLATFORMS.md:317`), which fails silently without TLS/tunneling. YouTube & Twitch require manual OAuth app setup. |
| **"Seamless Failover without restarting destinations"** | `README.md:62-63`, `web/src/pages/index.astro:29-31` | `CHANGELOG.md:49-86` (v0.6.0) documents ongoing edge cases with backward decode timestamps (Issue #126 open). File-based playlist failover (`CHANGELOG.md:707-742`) requires exact manual codec matching; mismatched files cause platforms to disconnect. |
| **"Simple 5-minute Quickstart"** | `README.md:170`, `docs/QUICKSTART.md:3` | Requires non-standard OBS FFmpeg output setup (sacrificing native local recording), manual SRT/RTMP port forwarding, and complex multi-platform OAuth app creation. |

#### B. Under-claimed (Earned, impressive capabilities that are under-sold)

| Under-Claimed Feature | Location in Repo | Why it deserves bold positioning |
|---|---|---|
| **Per-Destination Loudness Normalization & LUFS Compliance** | `docs/AUDIO-ROUTING.md`, `CHANGELOG.md:233-238`, `index.astro:208` | `polyemesis` doesn't just route audio; it measures integrated LUFS *after* routing and applies per-destination loudness targets and automatic clip limiters! This solves the huge streamer pain of uneven stream volume across platforms (e.g. YouTube -14 LUFS vs Twitch -16 LUFS). Currently buried as a technical meter note. |
| **Multitrack VOD Vault & Searchable AI Transcripts** | `README.md:63-64`, `web/src/pages/features.astro:58-65` | It isn't just a restreamer; it's an **automated AI VOD archiving server**. Stream-copies all 32 tracks, extracts 24-bit WAV/FLAC stems, prunes disk space safely, and runs local Whisper transcription so creators can search past streams for spoken quotes (`features.astro:60`). This is a massive workflow win for highlight editors, currently hidden in a generic grid. |
| **Ultra-Low CPU Stream-Copying (`-c:v copy`)** | `README.md:40-41`, `docs/FAQ.md:18-25` | Re-encoding 4K video per platform burns massive GPU resources. `polyemesis` demuxes audio in Go, routes low-bitrate audio, and passes video untouched (`-c:v copy`). Restreaming to 10+ platforms uses < 5% CPU on modest hardware or a Pi. This efficiency should be highlighted with explicit benchmarks. |
| **Home Assistant & Smart Studio Automation (MQTT 5.0)** | `docs/MQTT.md`, `README.md:76-77`, `docs/HOOKS.md` | Full auto-discovery in Home Assistant and signed lifecycle webhooks allow creators to trigger "ON AIR" studio lighting, Stream Deck alerts, and custom automation. Unique among media servers, but missing from marketing pages. |
| **Zero-Downtime Hot-Reloading** | `docs/HOT-RELOAD.md`, `README.md:72-75` | Operators can adjust mix gain, toggle audio tracks, and add destinations live without dropping the stream or disconnecting platform viewers. |

---

## Concrete Recommendations

### 1. Reframe Landing Page & Marketing Copy from Mechanism-First to Problem-First
* **Impact:** **HIGH**
* **Effort:** **SMALL**
* **Grounded in Files:** `web/src/pages/index.astro:50-63`, `web/src/pages/features.astro:12-32`, `web/src/pages/Base.astro`, `docs/notes/website-copy-review-2026-08-08.md`
* **Action:** 
  1. Change the main hero H1 in `web/src/pages/index.astro:50` from *"Six tracks from OBS. A different mix to every platform."* to a problem-solving statement: **"Stream Music on Twitch. Send a Clean Mix to YouTube."** with subhead: *"One OBS upload. Per-platform audio routing. Zero copyright strikes."*
  2. Update SEO `<title>` and `<meta description>` tags in `index.astro`, `features.astro`, and `comparison.astro` to include high-intent search terms (*"avoid YouTube DMCA strikes restreaming"*, *"multitrack OBS clean audio mix"*, *"self-hosted restream server"*). Keep titles under 60 characters to avoid truncation (`docs/notes/website-copy-review-2026-08-08.md:141` D23).

---

### 2. Streamline the First-Run OBS Onboarding & Address the Recording Tab Tradeoff
* **Impact:** **HIGH**
* **Effort:** **MEDIUM**
* **Grounded in Files:** `docs/QUICKSTART.md:55-88`, `web/src/pages/download.astro:105-139`, `docs/OBS.md`
* **Action:**
  1. In `docs/QUICKSTART.md` and `download.astro`, explicitly address the OBS Recording tab tradeoff up front: explain that using Custom FFmpeg Output in Recording tab disables native OBS local recording, and provide instructions for dual OBS setups or using OBS 30+ SRT Stream tab when 1-2 tracks suffice.
  2. Add an interactive **"OBS Config Generator / Profile Exporter"** in the web UI (`Sources` page) that displays exact step-by-step settings tailored to the user's specific OBS version and audio track count, reducing configuration errors.

---

### 3. Promote "Automated Multitrack VOD Vault & AI Transcript Search" as a Core Marketing Pillar
* **Impact:** **HIGH**
* **Effort:** **MEDIUM**
* **Grounded in Files:** `web/src/pages/features.astro:57-65`, `README.md:63-65`, `web/src/pages/index.astro:255-265`
* **Action:**
  1. Move VOD Archiving & Whisper AI Transcription out of the generic "What else it does" grid on `index.astro` and elevate it to a primary feature section alongside Audio Routing.
  2. Add a visual mockup or screenshot of the Chat & Transcript search UI on `features.astro`, showing how creators can search past stream transcripts for spoken words to instantly locate clip highlights.

---

### 4. Position "Per-Destination Loudness Normalization" as Automated Broadcast Mastering
* **Impact:** **MEDIUM**
* **Effort:** **SMALL**
* **Grounded in Files:** `web/src/pages/index.astro:208-245`, `web/src/pages/comparison.astro:46`, `docs/AUDIO-ROUTING.md`, `CHANGELOG.md:233-238`
* **Action:**
  1. Rebrand the "Meters" feature in marketing copy to **"Per-Platform Broadcast Loudness Mastering"**.
  2. Highlight that `polyemesis` automatically normalizes post-routing audio to target platform standards (e.g., YouTube -14 LUFS, Twitch -16 LUFS) with automatic peak limiters to prevent audio clipping and volume mismatches across destinations.

---

### 5. Clarify Platform API & Chat Moderation Prerequisites
* **Impact:** **MEDIUM**
* **Effort:** **SMALL**
* **Grounded in Files:** `web/src/pages/features.astro:67-76`, `docs/PLATFORMS.md:30-40,317`, `README.md:53-58`
* **Action:**
  1. On `web/src/pages/features.astro:68` and `web/src/pages/index.astro:25`, clarify chat capabilities by noting Facebook is read/moderate only, and Kick requires a public HTTPS URL for webhooks.
  2. In `docs/PLATFORMS.md` and the UI `Settings → Platforms`, add a "Quick Setup Checklist" setting expectations for custom OAuth developer app registration before users begin connecting accounts.

---

### 6. Productize Home Assistant & Smart Studio Automation (MQTT 5.0 & Webhooks)
* **Impact:** **MEDIUM**
* **Effort:** **SMALL**
* **Grounded in Files:** `README.md:76-77`, `docs/MQTT.md`, `docs/HOOKS.md`, `web/src/pages/index.astro:281-295`
* **Action:**
  1. Add a dedicated highlight section on `web/src/pages/features.astro` titled **"Smart Studio & Home Assistant Automation"**.
  2. Showcase how retained MQTT 5.0 telemetry automatically exposes stream state entities in Home Assistant, enabling creators to automate studio lights, tally lights, and Stream Deck status displays upon go-live.

---

### 7. Reconcile Performance & Destination Capacity Claims Across Repo Files
* **Impact:** **LOW**
* **Effort:** **SMALL**
* **Grounded in Files:** `README.md:52`, `docs/COMPARISON.md:100`, `docs/FAQ.md:29-35`, `web/src/pages/comparison.astro:39`, `docs/notes/website-copy-review-2026-08-08.md:111`
* **Action:**
  1. Replace the claim of `"Unlimited destinations"` in `README.md:52` and `docs/COMPARISON.md:100` with **"No configured destination cap"**, aligning with `web/src/pages/comparison.astro:39`.
  2. Add explicit resource guidance in `docs/FAQ.md`: note that while video stream-copying costs near 0% CPU, audio re-encoding scales at ~4% CPU core per destination, allowing ~96 concurrent destinations on a typical 4-core server.

---

## Summary Matrix of Recommendations

| # | Recommendation | Target Area | Impact | Effort | Grounding File(s) |
|---|---|---|---|---|---|
| 1 | Problem-First Landing Page & SEO Overhaul | Marketing & Positioning | **High** | **Small** | `web/src/pages/index.astro`, `Base.astro` |
| 2 | Streamline First-Run OBS Onboarding Flow | User Experience | **High** | **Medium** | `docs/QUICKSTART.md`, `download.astro` |
| 3 | Elevate Multitrack VOD Vault & AI Transcript Search | Product Value | **High** | **Medium** | `web/src/pages/features.astro`, `README.md` |
| 4 | Market Loudness Compliance as Broadcast Mastering | Feature Positioning | **Medium** | **Small** | `web/src/pages/index.astro`, `AUDIO-ROUTING.md` |
| 5 | Disclose Chat & Moderation OAuth Prerequisites | Expectation Setting | **Medium** | **Small** | `web/src/pages/features.astro`, `PLATFORMS.md` |
| 6 | Showcase Home Assistant & Smart Studio Automation | Feature Positioning | **Medium** | **Small** | `README.md`, `MQTT.md`, `HOOKS.md` |
| 7 | Reconcile Destination Capacity Claims | Documentation Accuracy | **Low** | **Small** | `README.md`, `COMPARISON.md`, `FAQ.md` |
