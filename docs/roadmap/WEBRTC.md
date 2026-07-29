# WebRTC: WHEP output, and WHIP input

**Status: DEFERRED (2026-07-28).** Not scheduled. The research below is complete
and current — including a measured dependency audit that will not need repeating
— so picking it up later does not mean redoing it.

**Recommendation when it is picked up:** build **WHEP**. Ship it *beside* the HLS
preview, not instead of it. Treat **WHIP** as a separate decision made after WHEP
has seen real use.

**One consequence for the rest of the roadmap:** deferring WHEP removes the
sub-second answer to preview latency, which raises the stakes on
[LL-HLS](LL-HLS.md). Where WHEP reaches under a second, tuned HLS reaches
roughly 2–5. If low latency matters now, LL-HLS is the only path left on the
schedule.

---

## Why this is worth doing

The built-in preview is HLS, and HLS is 10–30 seconds behind. An operator
checking their own feed is therefore checking what they were doing half a minute
ago. WHEP reaches sub-second.

The part that makes it worth the dependency: **the video needs no re-encode.**

The ingest is already H.264 inside MPEG-TS, and it is already stream-copied
through the hub. `-c:v copy -f rtp` payloads that straight into RTP per RFC 6184.
So WHEP costs **approximately zero CPU for video** — which makes it *cheaper*
than the HLS preview it sits beside, and `PreviewArgs` is currently described in
the source as "the one place polyemesis re-encodes video".

Audio must transcode: WebRTC does not carry AAC, so AAC → Opus at
`-application lowdelay -frame_duration 20`. Single-digit percent of one core.

## The dependency question

This project rejected `yutopp/go-rtmp` for pulling seven modules including
logrus. `pion/webrtc` pulls more. That comparison has to be made honestly rather
than waved away.

**Measured, not recalled.** A probe module that constructs a real
`PeerConnection` and a `TrackLocalStaticRTP`, built `CGO_ENABLED=0`, then read
back with `go version -m`:

```
22 modules linked, 9.97 MiB binary (vs ~2.4 MiB for a bare hello-world)
```

```
pion/webrtc/v4 v4.2.18   pion/ice/v4 v4.4.0        pion/dtls/v3 v3.1.5
pion/srtp/v3 v3.0.12     pion/stun/v3 v3.1.6       pion/turn/v5 v5.0.12
pion/sctp v1.11.1        pion/sdp/v3 v3.0.19       pion/rtp v1.10.5
pion/rtcp v1.2.17        pion/datachannel v1.6.2   pion/interceptor v0.1.47
pion/transport/v4 v4.0.2 pion/mdns/v2 v2.1.0       pion/randutil v0.1.0
pion/logging v0.2.4      github.com/google/uuid    github.com/wlynxg/anet
golang.org/x/crypto      golang.org/x/net          golang.org/x/sys
golang.org/x/time
```

Four are already in polyemesis's graph, so **18 are net new**. Every pion module
is MIT; `wlynxg/anet` is BSD-3-Clause. All compatible with this project's MIT.

### How that compares to the dependency this project refused

| | `yutopp/go-rtmp` — rejected | `pion/webrtc/v4` |
|---|---|---|
| Net-new modules | 7 | **18** |
| Logging | **logrus** — a framework, own dep tree, global state | `pion/logging` — two files, stdlib only, a `LoggerFactory` interface built to be replaced |
| Dead upstream | `pkg/errors`, `mapstructure` — both dead | none; one org, one release cadence |
| **Could FFmpeg do it instead?** | **Yes** — `ffmpeg -listen 1` was a complete answer | **No** — FFmpeg has no ICE/DTLS/SRTP offer-answer that can serve a browser |

The last row is the whole argument. go-rtmp was refused because a subprocess
already did the job and the dependency bought nothing. There is no FFmpeg path to
a browser PeerConnection. And the precedent already exists here:
**`datarhei/gosrt` was accepted on exactly this reasoning**, and
`internal/srtserver` is the shape to copy.

**Honest costs, to be written into `docs/DEPENDENCIES.md` rather than
discovered later:**

- **+7.5 MiB** on a project whose identity is one small static binary.
- **`pion/logging` is a second logging vocabulary** in a binary that uses
  `log/slog`, and this project's dependency doc names that as a rejection
  reason. Mitigated by a ~30-line `LoggerFactory` adapter onto `*slog.Logger` so
  pion never writes to stdout on its own. Own the counter-argument; do not gloss
  it.
- `wlynxg/anet` is dead weight — an Android netlink workaround for
  `net.Interfaces()`. polyemesis never ships to Android. It rides along anyway.

## WHEP design

```
relay.Hub  (MPEG-TS datagrams)
  └─ hub.Subscribe("whep:<sourceID>", port)  →  udp://127.0.0.1:<port>
       └─ supervised ffmpeg
            -i udp://127.0.0.1:<port>
            -map 0:v:0  -c:v copy    -payload_type 96  -f rtp rtp://127.0.0.1:<vport>
            -map 0:a:N? -c:a libopus -payload_type 111 -f rtp rtp://127.0.0.1:<aport>
       └─ Go: 2 × net.UDPConn → rtp.Packet → TrackLocalStaticRTP.Write
            └─ N × PeerConnection, all sharing the two tracks
```

Two RTP outputs means two more loopback ports per source from the existing
`relay.PortAllocator`; check `relayPortSpan` in `internal/engine/manager.go` has
headroom.

### Codec caveats, all real

- The browser needs `packetization-mode=1`. Register one H.264 codec in the
  `MediaEngine` with
  `profile-level-id=42e01f;packetization-mode=1;level-asymmetry-allowed=1`.
  Chrome and Safari accept baseline→high; Firefox is fussier.
- SPS/PPS must repeat in band. MPEG-TS normally carries them per IDR; an ingest
  that does not will show a joining viewer green until the next one. May need a
  `dump_extra` bitstream filter.
- **Join latency is time-to-next-IDR.** On a 2-second GOP that is up to two
  seconds of nothing. There is no fix that does not re-encode. Offer an explicit
  opt-in "force short GOP (re-encodes)" and default it off.
- Ship a re-encode fallback (`-c:v libx264 -tune zerolatency -g 30`) for the
  ingest whose profile a browser refuses.

**VP8 is the wrong choice.** It mandates a re-encode, which is precisely the cost
WHEP exists to avoid.

### Ref counting is better here than for HLS

Mirror `Engine.PreviewRequested` / `previewLoop` / `sweepPreview`, with the same
lock ordering — but the liveness signal is a fact rather than a guess. HLS infers
a viewer from playlist polls and waits ~30 s to be sure. WebRTC gives an explicit
disconnect, so the sweep becomes 2–3 seconds of certainty.

Keep a short grace window regardless: a page reload is a disconnect immediately
followed by a new offer, and cycling FFmpeg on every reload feels broken.

### ICE, honestly

- **The case that matters is the easy one.** Operator's browser and the box on
  the same LAN, or the browser *is* on the box. Host candidates alone work. No
  STUN, no TURN, nothing to configure.
- **Remote operator:** `SettingEngine.SetICEUDPMux` on one fixed UDP port plus
  `SetNAT1To1IPs(publicIP, ICECandidateTypeHost)`. That is **one forwardable
  port**, which is the same doctrine as
  [DESIGN-ONE-PORT-ONLY.md](../DESIGN-ONE-PORT-ONLY.md).
- **STUN buys almost nothing** for a server whose address the operator already
  knows. Default to none; expose an optional setting for the awkward NAT.
- **Do not ship TURN.** Do not embed `pion/turn` as a server. If a viewer is
  behind a UDP-blocking firewall the honest answer is *"the HLS preview still
  works"* — which is exactly why WHEP sits **beside** HLS rather than replacing
  it. HLS is the compatibility floor; WHEP is the latency path.
- **TLS is load-bearing.** Browsers require a secure context for WebRTC, so WHEP
  is unusable over plain `http://` except on localhost. Surface that in the UI
  rather than letting it present as a bug.

### Auth

Route inside the existing authenticated group. Do **not** wire it into
`playout`'s anonymous token path — that is public playback at scale, explicitly
not the goal, and one SRTP encryption per viewer does not scale the way a segment
on disk does. Cap concurrent peers so a leaked session cannot fork unbounded
goroutines.

## WHIP design

A fourth `IngestMode` beside srt / rtmp / pull.

**The impedance mismatch:** the hub speaks whole MPEG-TS datagrams; WebRTC speaks
RTP carrying H.264 and Opus.

**Rejected — re-mux in Go.** Depayload RTP, rebuild Annex-B, hand-write an
MPEG-TS muxer. This is precisely what
[ARCHITECTURE.md](../ARCHITECTURE.md#why-ffmpegs-rtmp-listener-not-yutoppgo-rtmp)
already refused for go-rtmp — *"a large, subtle surface area for zero
user-visible gain"*. Same verdict, same reason.

**Right shape — an FFmpeg subprocess re-mux,** mirroring `srtserver`: pion
receives, writes RTP to loopback UDP, and FFmpeg reads a small generated `.sdp`
and muxes to MPEG-TS into the hub. Force H.264 in the `MediaEngine` so video
stays `-c:v copy`; Opus → AAC is unavoidable.

That generated SDP file is the genuinely awkward part — `-protocol_whitelist`,
exact payload types, and cryptic FFmpeg errors when it is wrong.

**State the limitation plainly.** WHIP realistically carries **one audio track**.
Multiple audio m-lines are legal SDP, but no browser capture path produces
independent labelled programme audio and no WHIP client in the wild sends them.
**So WHIP has exactly the same limitation as RTMP:** it cannot feed the
per-destination multitrack routing that is the reason this product exists. It is
a convenience ingest — "stream from a laptop with no encoder installed" — not a
production one, and the UI should say so the way the RTMP singleton error does.

One place it beats RTMP: the endpoint is an HTTP path, so it is token-addressed
per source on the existing HTTPS port. No port-collision reason to cap it at one.

Also honest: browser-sourced video is roughly 720p30 constrained baseline with
aggressive congestion backoff. Not broadcast grade.

## Test plan

**Unit — no browser, no network.** Two pion PeerConnections talking to each other
inside one `go test`: the code under test as offerer, a test-only PC as answerer.
Feed known RTP into the loopback port and assert SSRC, sequence and payload
arrive intact and ordered. That is a real SRTP round trip in plain Go.

The ref-count state machine goes in as a **pure function** table test —
`whepIdle(settings, peers, lastZeroAt, now) bool` — which is the idiom
`preview_ondemand_test.go` already uses and needs no network at all.

**Acceptance — `scripts/acceptance-whep.sh` plus a Go driver.** Push the same
synthetic three-tone `testsrc2` stream the other suites use, and act as a WHEP
client in Go with pion as receiver. Measured claims:

- the SDP answer carries exactly one H.264 and one Opus m-line, payload types
  matching the offer;
- decode the received Opus and run **the same `astats` bandpass tone check
  `acceptance-audio.sh` already uses** — proving the *correct* track was
  selected, not merely that audio arrived;
- count H.264 NAL types to prove an IDR landed within N seconds of connecting;
- **ref-count proof:** no FFmpeg child before the first POST, one after, gone
  within idle+sweep after the DELETE;
- **latency:** compare RTP timestamp to arrival wall clock and *report the
  transit delta as a number* rather than asserting a threshold, in this
  project's style.

**Browser — Playwright, and it genuinely works here.** Two things make it
CI-safe:

1. **WHEP is receive-only.** No camera, no microphone, no fake-device flags, no
   permission dance — which removes the usual headless-WebRTC pain entirely.
2. **The assertion is `getStats()`, not a screenshot.** Poll the inbound-rtp
   video report for `framesDecoded > 0` and `bytesReceived` rising across two
   samples, plus `<video>.currentTime` advancing. That is a measurement; it
   cannot pass on a black frame or a wedged connection the way
   `expect(video).toBeVisible()` can.

Set `--autoplay-policy=no-user-gesture-required`, or the element never starts and
`framesDecoded` stays 0 for reasons unrelated to WHEP. Run against the container
on `127.0.0.1`, which is a secure context — **so no certificate is needed**, a
real advantage of the existing `acceptance-browser.sh` setup. One exception is
required: that script deliberately publishes no ingest ports, and this suite
needs the WebRTC UDP port.

**Gap to own:** Chromium is one decoder. Safari and Firefox negotiate H.264
profiles differently, and Playwright's WebKit and Firefox builds are not the
shipping browsers. Automate Chromium; treat Safari as manual.

## Risks

1. **H.264 pass-through is the entire value proposition and the least certain
   part.** If common ingest profiles turn out un-negotiable, WHEP degrades to a
   re-encode — a lower-latency preview at the same CPU cost, a much weaker
   pitch. **Spike it first:** half a day forwarding a real ingest to Chrome with
   `-c copy`, before anything else is built. Failure there roughly doubles the
   estimate.
2. **IDR wait on join** reads as broken. Needs a spinner and a documented GOP
   recommendation.
3. **+7.5 MiB binary**, non-negotiable, on a project whose identity is a small
   static binary.
4. **`pion/logging`** is a second logging vocabulary in a binary whose dependency
   doc names that as a rejection reason.
5. **Goroutine and socket leak per peer.** Each PC is several goroutines plus a
   DTLS session; a closed laptop lid leaves one until ICE timeout. Cap peers, set
   a disconnect timeout, assert goroutine count returns to baseline.
6. **NAT reality.** Any off-LAN operator needs a port forward. Do not paper over
   it with a public STUN server that mostly will not help a server-side
   candidate.
7. **The WHIP SDP-file dance is fragile.**
8. **`CGO_ENABLED=0` verified today, not guaranteed forever.** The dependency-bump
   checklist must gain a four-target build loop for any pion upgrade.

## Effort

**WHEP — 8–12 days.** Spike 0.5 · `internal/webrtcx` core 2 · `WHEPArgs` 1 ·
engine ref-count lifecycle 1.5 · API and auth 1 · UI player and settings 1.5 ·
acceptance and Playwright 1.5 · docs 1 · contingency +2.

**WHIP — 6–9 days on top, and lower value.** SDP generation 1 · OnTrack plumbing
2 · fourth ingest mode through db/engine/api/UI 1.5 · acceptance, needing a
headless Go publisher 1.5 · docs 1 · contingency +2.

---

## See also

- [ROADMAP](README.md) — where this sits against the other seven
- [../DEPENDENCIES.md](../DEPENDENCIES.md) — the bar this dependency must clear
- [../ARCHITECTURE.md](../ARCHITECTURE.md) — the relay hub this reads from
