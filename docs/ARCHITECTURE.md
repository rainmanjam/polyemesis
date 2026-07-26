# polyemesis — architecture

A self-hosted restreaming server. Ingest once, fan out to N destinations, with
**per-destination multichannel audio routing**. Video is always `-c:v copy`;
only audio is re-encoded, per destination, from a user-defined mix of the
ingest's audio tracks.

---

## 1. Process graph

```
  OBS / synthetic source
        │
        │  SRT (mpegts): 1 video + up to 6 AAC tracks
        │  or RTMP (1 video + 1 AAC track)
        ▼
┌───────────────────────────────────────────┐
│ ingest ffmpeg  (supervised)               │
│   -i srt://0.0.0.0:P?mode=listener        │
│   -map 0 -c copy -f mpegts                │
│   udp://127.0.0.1:HUB_IN                  │   all tracks preserved, no decode
└───────────────────────────────────────────┘
        │ MPEG-TS datagrams (1316 B)
        ▼
┌───────────────────────────────────────────┐
│ relay hub  (pure Go, internal/relay)      │
│   net.UDPConn on 127.0.0.1:HUB_IN         │
│   replicate every datagram → N subscribers│
└───────────────────────────────────────────┘
   │        │         │            │
   │        │         │            └────────────► meters ffmpeg
   │        │         │                           -map 0:a -f null -
   │        │         │                           astats → WS levels
   │        │         └─────────────────────────► preview ffmpeg
   │        │                                     LL-HLS → hls.js
   │        └───────────────────────────────────► recorder ffmpeg
   │                                              -map 0 -c copy → MKV segments
   │                                              (ALL audio tracks kept)
   ▼
┌───────────────────────────────────────────┐   one per destination,
│ destination ffmpeg  (supervised)          │   independently restartable
│   -i udp://127.0.0.1:SUB_N                │
│   -filter_complex <routing graph>         │  ◄── the differentiator
│   -map 0:v:0 -c:v copy                    │
│   -map [aout] -c:a aac -b:a 160k -ac 2    │
│   → rtmp(s):// | srt:// | file            │
└───────────────────────────────────────────┘
```

Each box is one `os/exec` child in its own process group, owned by a supervisor
goroutine (`internal/supervisor`). Killing polyemesis kills every child.

### Why a Go UDP hub, not multicast and not SRT

The ingest must publish **once** while N consumers read independently.

| option | verdict |
|---|---|
| loopback **multicast** (`udp://239.x.x.x`) | works on Linux with a route, unreliable on macOS/Windows, needs iface selection. Rejected: portability. |
| local **SRT relay** (ffmpeg listener re-serving) | costs an extra encode-free but real process, adds its own latency budget (`latency` ms) and CPU, and a consumer reconnect storms the relay. Rejected: cost. |
| **Go UDP fan-out hub** ✅ | ingest sends to one loopback port; Go replicates each datagram to per-subscriber loopback ports. Zero extra processes, no multicast routing, subscribers attach/detach freely. |

**Tradeoff (documented, accepted):** loopback UDP can drop datagrams under memory
pressure, and there is no retransmit. MPEG-TS is 188-byte packets carried 7-per-
datagram, so a loss costs a few TS packets — a decoder glitch, not a stream
death. We mitigate with a large `SO_RCVBUF`, non-blocking sends that never let a
slow subscriber stall the hub, and `fifo_size`/`overrun_nonfatal=1` on the
consumer side. A future `relay.Mode = "srt"` can swap the transport without
touching anything above it.

### Why FFmpeg's RTMP listener, not yutopp/go-rtmp

RTMP is the *fallback* ingest (single audio track), so the bar is "robust and
cheap", not "feature-rich".

- go-rtmp hands us FLV tags. To reach the relay we would then have to write an
  FLV→MPEG-TS muxer plus AVC/AAC bitstream handling (AVCDecoderConfigurationRecord
  → Annex-B, ADTS framing) by hand. That is a large, subtle surface area for zero
  user-visible gain.
- `ffmpeg -listen 1 -i rtmp://0.0.0.0:1935/live/KEY` demuxes FLV and emits
  MPEG-TS with `-c copy`, which is **the exact same code path as SRT ingest** —
  one `IngestArgs` builder, one supervisor, one relay entry point.
- The listener serves one publisher then exits; the supervisor already respawns
  processes with backoff, so "wait for next publisher" is free.
- Stream-key auth comes from the URL path, which ffmpeg matches against the
  publisher's `app/playpath`.

The product has exactly one publisher (the streamer), so single-publisher
semantics are correct, not a limitation.

---

## 2. Audio routing engine (`internal/routing`)

The whole feature reduces to: **build a `-filter_complex` string from a profile.**

### Model

```go
type Profile struct {
    Mode      Mode        // "simple" | "matrix"
    Tracks    []TrackSel  // simple mode: {Track, Enabled, Gain}
    Matrix    []Cell      // matrix mode: {Track, Channel, Out, Gain}
    Normalize NormMode    // "off" | "limiter" | "loudnorm"
    SampleRate int        // 48000
}
```

Matrix mode **subsumes** simple mode: simple mode is compiled into the same
per-track `pan` + `amix` shape, just with cells derived from the checkboxes and
the standard downmix table.

### Compilation

For each **selected** track *i* with *c* channels:

| c | filter |
|---|---|
| 1 | `pan=stereo\|c0=G*c0\|c1=G*c0`  (mono → centered) |
| 2 | `pan=stereo\|c0=G*c0\|c1=G*c1` |
| 6 | `pan=stereo\|c0=0.4142*c0+0.2929*c2+0.2929*c4\|c1=...` scaled by G |

The 5.1 coefficients are FFmpeg's own normalized ITU downmix:
`L = (FL + 0.707·FC + 0.707·BL) / (1 + 0.707 + 0.707)` → `0.4142 / 0.2929 / 0.2929`.
LFE is dropped (ffmpeg's default `lfe_mix_level = 0`). Normalizing by the
coefficient sum is what prevents a hot 5.1 source from clipping on downmix.

Then sum and finish:

```
[a0][a1][a2] amix=inputs=3:duration=longest:normalize=0 [mixed]
[mixed] alimiter=limit=0.95:level=disabled [lim]
[lim]   aresample=48000:async=1:first_pts=0 [aout]
```

`normalize=0` on `amix` is **essential** — the default divides by input count,
so selecting 3 tracks would quietly drop everything ~9.5 dB. We control level
explicitly with per-track gain instead, and catch the resulting clipping risk
with the limiter, which is auto-enabled whenever ≥2 tracks are summed.

Single track + no normalization degenerates to one `pan` — no `amix` at all.

The generated string is returned to the UI verbatim so the user can see exactly
what their routing compiles to.

### Presets

`Everything` · `No music` (all but a designated track) · `Mic only` · `5.1 → stereo`.

---

## 3. Package layout

```
cmd/polyemesis/main.go          wiring, flags, graceful shutdown

internal/
  config/      config.yaml load/save/defaults, path resolution
  db/          modernc.org/sqlite, migrations, stores
  ffmpeg/      detect.go (>=6.0 gate), probe.go, and the command BUILDERS:
               ingest.go · destination.go · recorder.go · preview.go · meters.go
  routing/     profile.go (model+validation) · filtergraph.go · presets.go
  relay/       UDP fan-out hub, port allocator
  supervisor/  process lifecycle, pgid kill, backoff, -progress parser, log ring
  meters/      astats stderr parser → per-channel dBFS + peak hold
  stats/       ring buffers (30 min bitrate), host CPU/RAM
  auth/        bcrypt, JWT cookie, CSRF double-submit
  secrets/     NaCl secretbox token encryption at rest
  oauth/       youtube.go · twitch.go · kick.go + token refresh
  recording/   segment index, retention sweeper (max GB / max age)
  engine/      the orchestrator: owns ingest+relay+recorder+preview+meters+dests
  api/         chi router, REST handlers, WebSocket hub
  web/         go:embed ui/dist + SPA fallback

ui/            Vite + React + TS + Tailwind + shadcn/ui + Recharts + hls.js
```

**Dependency direction:** `api → engine → {supervisor, relay, routing, ffmpeg, db}`.
`routing` and `ffmpeg` are pure (string in / string out, no I/O) which is what
makes them exhaustively unit-testable without a live process.

---

## 4. Build & verify order

1. config, db, ffmpeg detect + builders
2. **routing engine + its unit tests** (the differentiator, pure, testable first)
3. relay hub, supervisor
4. engine orchestrator, meters, stats, recording
5. auth, secrets, api, websocket
6. frontend (theme → shadcn chrome → bespoke meters/matrix → pages)
7. oauth
8. docs, Docker, systemd

`go build ./...` · `go test ./...` · `make build` (embeds UI) must all pass.
