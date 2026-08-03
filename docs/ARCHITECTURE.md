# polyemesis — architecture

A self-hosted restreaming server. Ingest once, fan out to N destinations, with
**per-destination multichannel audio routing**. Every destination is `-c:v copy`;
only audio is re-encoded, per destination, from a user-defined mix of the
ingest's audio tracks.

Video is re-encoded in exactly one place: a **rendition**, a shared encode that
any number of destinations select and that touches video only. See §3.

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
   ├──────────────────────────────┐
   ▼                              ▼
┌────────────────────────────┐  ┌──────────────────────────────────────┐
│ destination ffmpeg         │  │ rendition ffmpeg  (supervised)       │
│   on PASSTHROUGH           │  │   -i udp://127.0.0.1:SUB_R           │
│   -i udp://127.0.0.1:SUB_N │  │   -map 0:v:0 -c:v libx264 -b:v 6000k │
│   -filter_complex <graph>  │  │   -map 0:a   -c:a copy               │
│   -map 0:v:0 -c:v copy     │  │      ▲ EVERY audio track, untouched  │
│   -map [aout] -c:a aac     │  │   -f mpegts udp://127.0.0.1:RHUB_IN  │
│   → rtmp(s) | srt | file   │  └──────────────────────────────────────┘
└────────────────────────────┘                   │ video re-encoded ONCE
                                                 ▼
                                ┌──────────────────────────────────────┐
                                │ rendition relay hub                  │
                                │   a second relay.Hub, its own port   │
                                └──────────────────────────────────────┘
                                     │           │            │
                                     ▼           ▼            ▼
                                ┌──────────────────────────────────────┐
                                │ destination ffmpeg, one per dest     │
                                │   -i udp://127.0.0.1:SUB_M           │
                                │   -filter_complex <its OWN graph>    │
                                │   -map 0:v:0 -c:v copy               │
                                │   -map [aout] -c:a aac -b:a 160k     │
                                │   → rtmp(s):// | srt:// | file       │
                                └──────────────────────────────────────┘
```

Every destination box is `-c:v copy` plus its own `-filter_complex`, whichever
hub it reads. That is the invariant: moving a destination onto a rendition
changes only which relay it subscribes to.

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

## 3. Renditions — the shared video encode

A rendition is one named video output profile. Destinations **select** one
rather than owning one, so N destinations that all need 1080p60 cost **one**
encode rather than N. `rendition_id IS NULL` is **passthrough**: no process, no
encode, subscribed straight to the ingest hub. That is the default, and it is
exactly what every destination did before renditions existed.

### The invariant: video only, audio copied

```
rendition ffmpeg:  -map 0:v:0 -c:v <encoder> -b:v <kbps>
                   -map 0:a   -c:a copy          ◄── every track, untouched
destination:       -map 0:v:0 -c:v copy
                   -filter_complex <its own routing graph>
                   -map [aout] -c:a aac -b:a <kbps> -ac 2
```

`db.Rendition` has no audio field and must never grow one. If it mixed audio,
every destination downstream of it would receive one pre-mixed stereo pair
instead of the multitrack ingest, and per-destination audio routing — the
product — would be gone for exactly the destinations that most need a rendition.

The consequences worth naming:

- Audio is encoded **once**, at the destination, never twice. A rendition adds
  no audio generation loss and no audio latency.
- The full multitrack stream survives the encode, so a destination can be moved
  onto a rendition, or between renditions, without touching its routing profile.
- The rendition's output is still MPEG-TS with N audio tracks, so its hub is
  indistinguishable from the ingest hub to everything downstream. That is why
  the destination builder needed no rendition-specific branch.

### Its own hub

`startRendition` allocates a subscription on the ingest hub for its input, then
creates a **second `relay.Hub`** (`relay.New(log, 0)` — port 0, so the kernel
picks one clear of the per-consumer allocator's range) for its output. Its
destinations subscribe to that hub instead of the ingest's.

Instantiating another hub was preferred to teaching the existing one about
tiers: the hub is already a value with a lifecycle, and "a rendition is an
ingest as far as its destinations are concerned" is a smaller idea than a hub
with routing rules inside it.

### Ref counting

The encode starts with the **first enabled** destination that selects it and
stops with the last. `CountEnabledDestinationsByRendition` is the ref count and
it comes from the database, not from a counter kept in memory — a counter can
drift from the rows, and the whole engine is reconciliation rather than
commands.

`wantedRenditions` therefore omits any rendition with zero enabled consumers,
and `diffRenditions` compares wanted against running to produce a start list and
a stop list. A rendition nobody selects, or whose destinations are all stopped,
has no process and burns no CPU.

### Reconcile order

`reconcileOutputs` is deliberately one function, because the ordering is
load-bearing:

1. stop destinations that must move
2. stop renditions that must go
3. start renditions that must come up
4. start destinations

A destination brought up before its rendition would sit spinning on a relay
nobody is publishing to, and one left running while its rendition's hub closes
underneath it would look healthy and send nothing. `upstreamHub` refuses to
start a destination whose rendition is not up, and records why, so the card says
"rendition failed to start: …" instead of showing green and silence.

### What restarts what

`renditionSig` hashes everything the encode's command line depends on —
dimensions, fps, bitrate, encoder, preset, GOP, plus the *source* frame rate but
only when the rendition inherits it, since the keyframe interval is counted in
frames. Name and note are excluded: renaming a tier must not interrupt the
destinations riding it.

That signature is folded into each downstream destination's own spec
(`destSpec`), so editing a rendition restarts that encode and exactly the
destinations reading it — and nothing else. A rendition is replaced rather than
adjusted, because FFmpeg cannot change its output resolution mid-run and the
destinations copying its video have to be restarted onto the new stream anyway.

### Deleting, and backwards compatibility

`destinations.rendition_id` is `ON DELETE SET NULL`, not `CASCADE`: deleting a
rendition must drop its destinations back to passthrough, never delete them. The
API takes the usage counts *before* the delete and returns a warning with them,
because falling back to passthrough silently would hand a 4K source to a
platform that was on 1080p60 precisely because it will not take one.

The column is added by a migration, not by `schema.sql`, since that file also
runs against databases created before renditions existed. An existing install
upgrades with every destination on `rendition_id = NULL` — passthrough — and
therefore behaves identically with zero user action.

### Preset starting points

`db.RenditionPresets()` ships conservative starting points (1080p60, 1080p30,
720p60, 720p30, all `libx264`/`veryfast`/2 s GOP) and every one of them carries
`db.PresetDisclaimer` — *"Starting point — verify current limits with the
platform."* — in its note. Platform ceilings change and differ by partner
status; presenting one as authoritative would break a live stream, so where a
value was uncertain the lower one was chosen and the disclaimer is rendered
verbatim in both the UI and the README.

---

## 4. The HTTP edge: TLS and security headers

### Where the decision lives

`tls.mode` accepts five values but only four are servable. Resolving `auto` down
to one of them is done **once**, in `internal/config`, and everything downstream
consumes the answer:

```
config.TLS.Resolve(trustProxy) ──► config.Mode {off|acme|selfsigned|manual}
        │
        ├──► tlsx.New(...)              the *tls.Config the listener serves
        ├──► api.securityHeaders(...)   whether HSTS may ever be sent
        └──► main.reportStartup(...)    what the banner claims
```

`internal/tlsx` deliberately knows nothing about `config.yaml`: it takes an
already-resolved mode. That keeps the auto-resolution rules — and their tests —
in one package, and keeps `tlsx` a pure certificate concern. The alternative,
letting each consumer re-derive the mode, is how a banner ends up saying "acme"
while the listener serves self-signed.

Resolution order for `auto`: `trustProxyHeaders` wins first (the proxy
terminates TLS, so we must not), then a public FQDN plus an `acmeEmail` earns
ACME, then everything else falls to self-signed. It never returns `auto`.

### Backwards compatibility is a mapping, not a migration

`config.yaml` is read-only to the app — there is no `Config.Save()` and there
must not be one; the file is owned by whoever deploys the box. So the legacy
`tls.enabled` boolean cannot be rewritten into `tls.mode` on disk. It is mapped
at load time instead (`normalizeTLS`): an absent `tls.mode` falls back to
`enabled: true → manual`, `false/absent → off`. An explicit mode always wins.

The consequence is that an existing install upgrades to exactly what it served
yesterday and does not silently acquire `auto`. Swapping an operator's real
certificate for a generated one, or dropping HTTPS entirely, would both be
serious regressions, and neither is reachable from a config that predates the
mode field.

### The certificate layer (`internal/tlsx`)

| mode | source | persisted under `<dataDir>/tls/` |
|---|---|---|
| `manual` | `tls.LoadX509KeyPair` at startup | nothing |
| `selfsigned` | local CA + leaf, minted on demand | `ca.crt` `ca.key` `server.crt` `server.key` |
| `acme` | `autocert.Manager`, lazy on first handshake | `acme/` (account key + issued certs) |
| `off` | — | nothing |

Rules that hold across all of it:

- Private keys are written `0600` through an **atomic temp-file + rename**, into
  a `0700` directory. A half-written key from a power cut would otherwise brick
  HTTPS with material that is present but unparsable.
- Material that exists but does not parse is an **error, not a silent
  regeneration**. Writes are atomic, so corruption means something outside
  polyemesis interfered, and quietly replacing a CA the user has already
  installed in three browsers is worse than saying so.
- No key is logged, returned by an API, or carried in `CertInfo` — which is JSON
  encoded straight onto a response and therefore has no field for one.
- `tls.Config` is pinned explicitly: `MinVersion: TLS 1.2`, curves
  X25519/P-256/P-384, `NextProtos` h2 + http/1.1. Go's default already floors at
  1.2; stating it means a toolchain change cannot alter the policy silently and
  an auditor reads it in one place.

The self-signed CA is valid ten years and the leaf one, renewed inside a 30-day
window, or reissued when `tls.hostname` changes or the CA is replaced. The long
CA life is the point: installing a root into a browser, a phone and a keychain
is the most tedious step of a homelab setup, and an annual repeat is a reason to
abandon HTTPS. The leaf rotates underneath it instead. Leaf SANs always include
`localhost`, `127.0.0.1` and `::1` alongside the configured name, because the
first login usually arrives by loopback or over an SSH tunnel.

ACME issuance is pinned to the single configured hostname by an `autocert`
`HostPolicy`. Left open, any SNI arriving on a public port would trigger an
order, which is a free way for a stranger to exhaust this box's Let's Encrypt
rate limit.

### Failing soft on port 80

`startHTTPHelper` binds `:80` whenever polyemesis is terminating TLS: it serves
the ACME HTTP-01 responder (acme mode) and a permanent redirect to HTTPS
(always; `301` for GET/HEAD, `308` otherwise so an API client keeps its method
and body).

**Failure to bind is a warning, not a fatal error.** Port 80 is privileged and
frequently already taken, and a server that refuses to start over it leaves the
operator with no UI in which to fix the setting that stopped it starting. This
is the same judgement made earlier for the SRT capability check, and for the
same reason. ACME also advertises `acme.ALPNProto` on the 443 listener, so
TLS-ALPN-01 can still complete on a box where only 443 is reachable.

Configuration errors are treated differently and *are* fatal: `Config.Validate`
rejects `acme` without a hostname or email, `acme` with a private name, and
`manual` with files it cannot stat. Those are wrong before anything starts, and
starting anyway would just defer the same error to the first request.

### HSTS, and why it is the one opt-in

`Strict-Transport-Security` is persisted by the browser and cannot be retracted
by the server. On a self-signed box it also removes the click-through on the
certificate warning, so one header can shut both doors on a LAN machine
permanently.

The policy is therefore decided once, at router construction, from two inputs —
the resolved mode and `tls.hsts` — and passed into `securityHeaders` as
parameters rather than read off the server struct, so the decision with no undo
is stated at the call site and testable without building a server:

```go
allowHSTS := hsts && (mode == config.ModeACME || mode == config.ModeManual)
...
if allowHSTS && r.TLS != nil { h.Set("Strict-Transport-Security", "max-age=86400") }
```

`r.TLS` is the only proof of a genuinely encrypted hop. A forwarded header is
never consulted, even from a trusted proxy: in that deployment the resolved mode
is already `off`, and the policy for the connection the browser actually made
belongs to whoever terminated it. `max-age` is one day, with no
`includeSubDomains` and no `preload` — both widen the blast radius past this
host, and a day is long enough to matter while still ageing out of a mistake.
`Config.HSTSPolicy` returns the suppression reason so the startup banner can
explain the silence rather than the operator discovering it with `curl -I`.

### CSP

The policy is a `[]string` of directives, one per line with its justification
next to it, joined at init. Every relaxation is load-bearing for a feature that
fails *silently* — usually as a white page:

| directive | why |
|---|---|
| `media-src 'self' blob:` | hls.js hands `<video>` its MediaSource as a blob URL |
| `worker-src 'self' blob:` | hls.js compiles its demuxer worker from generated source |
| `connect-src 'self' ws: wss:` | the telemetry WebSocket; `ws:` because a LAN box may be on plain HTTP |
| `img-src 'self' data:` | inline icons |
| `style-src 'self' 'unsafe-inline'` | the bundle injects `<style>` at runtime |

What is **absent** is the load-bearing part: no `'unsafe-inline'` for
`script-src`. The UI is a Vite bundle of hashed module files with no inline
`<script>`, so nothing needs it, and it is the one relaxation that would turn an
injected string into executable code. `frame-ancestors 'none'`, `base-uri
'self'` and `form-action 'self'` complete it.

`securityHeaders` is added to the existing chain (`RequestID → Recoverer →
requestLogger`) at the router root, so it covers the embedded UI and the SPA
fallback as well as the API — a CSP that only guarded `/api/v1` would guard
nothing that renders.

### Transport security is not only the web UI

Worth stating because the TLS mode is easy to mistake for the whole story:

- SRT ingest carries its own AES encryption via a passphrase
  (`db.SRTSettings.Passphrase`, 10–79 characters, enforced in
  `Settings.Validate`), rendered into both the listener URL and the
  copy-pasteable encoder URL. RTMP ingest has no equivalent — it is
  authenticated by the stream key in the path and is otherwise in the clear.
- Destination URLs are passed to FFmpeg verbatim, and `db.Destination.Validate`
  accepts `rtmps://` as well as `rtmp://`, so a destination can be TLS-wrapped
  independently of how the UI is served.

---

## 5. Package layout

```
cmd/polyemesis/main.go          wiring, flags, graceful shutdown

internal/
  config/      config.yaml load/defaults/validation, path resolution (read-only:
               config.yaml is owned by the deployer, never rewritten by the app)
  db/          modernc.org/sqlite, migrations, stores
  ffmpeg/      detect.go (>=6.0 gate + encoder probe), probe.go, and the
               command BUILDERS: ingest.go · destination.go · rendition.go ·
               recorder.go · preview.go · meters.go
  routing/     profile.go (model+validation) · filtergraph.go · presets.go
  relay/       UDP fan-out hub, port allocator, TS continuity/loss measurement
  supervisor/  process lifecycle, pgid kill, backoff, -progress parser, log ring,
               rotating file sink for logs that must outlive the process
  stats/       ring buffers (30 min bitrate), host CPU/RAM
  metrics/     Prometheus text exposition, rendered from the engine's status
  auth/        bcrypt, JWT cookie, CSRF double-submit, API tokens, login throttle
  tlsx/        certificate layer: tls.Config, local CA + leaf, autocert, expiry
               introspection. Takes an already-resolved mode; knows no yaml.
  secrets/     NaCl secretbox token encryption at rest
  fsperm/      restricts access to the files and directories holding secrets
  oauth/       youtube.go · twitch.go · kick.go · facebook.go + PKCE + refresh,
               plus the capability matrix the UI and the docs both render
  recording/   segment index, retention sweeper (max GB / max age), free-space guard
  events/      in-process pub/sub the WebSocket fans out
  engine/      the orchestrator: owns ingest+relay+recorder+preview+meters,
               plus the rendition tier and the destinations that consume it
  api/         chi router, REST handlers, WebSocket hub
  web/         go:embed ui/dist + SPA fallback

  -- ingest and viewer-facing edges
  srtserver/   the one-port SRT ingest: one listener serving every source,
               demultiplexed by the publish token
  playout/     the viewer-facing origin: packages the relay into public HLS
               (optionally DASH) and counts who is watching
  meters/      the measurement tier: what a destination is ACTUALLY sending,
               in the unit the platform on the other end cares about

  -- chat
  chat/        unified cross-platform chat: one pane, one send box, four
               platforms, plus moderation (delete, ban, timeout, hide) and the
               YouTube quota pacer, which is the whole design problem there

  -- capture and post-production
  clips/       keeps the last N seconds of live in memory, cuts a file on demand
  clipper/     cuts a clip out of a recording already on disk
  jobs/        the durable background queue every heavy task runs on
  transcribe/  recorded multitrack MKV to per-track transcripts and subtitles,
               via whisper.cpp
  media/       a finished recording to the derived files a library needs: a
               low-bitrate proxy, keyframe index, thumbnails
  uploads/     operator-supplied files, and the one place a stored name is
               turned into a path inside the uploads directory
  playlistmedia/
               one upload to the single normalised derivative a playlist item
               is PLAYED FROM -- one fixed 1080p30 profile, so the concat
               demuxer can splice a list the operator assembled from anything.
               The profile's version is part of the derivative's filename, so
               changing the encode cannot silently reuse files that predate it

  -- operations
  scheduler/   starts and stops destinations at a time the operator chose
  alerts/      "something an operator would want to know" to webhook deliveries
  mqtt/        retained telemetry, with Home Assistant discovery

ui/            Vite + React + TS + Tailwind + shadcn/ui + Recharts + hls.js
```

**Dependency direction:** `api → engine → {supervisor, relay, routing, ffmpeg, db}`.
`routing` and `ffmpeg` are pure (string in / string out, no I/O) which is what
makes them exhaustively unit-testable without a live process.

`tlsx` sits off to the side: `main` and `api` depend on it, it depends on
nothing of ours. Its only inputs are a resolved `Mode` and a data directory,
which is what lets its tests mint and expire certificates against a fake clock
without a config file or a listener.

---

## 6. Build & verify order

1. config, db, ffmpeg detect + builders
2. **routing engine + its unit tests** (the differentiator, pure, testable first)
3. relay hub, supervisor
4. engine orchestrator, meters, stats, recording
5. renditions: `ffmpeg/rendition.go` and its arg tests, then the store, then the
   engine's ref counting — in that order, because the invariant that a rendition
   never encodes audio is provable in a pure function before any process exists
6. auth, secrets, api, websocket
7. tls: `config`'s mode resolution and the legacy `tls.enabled` mapping first —
   they are pure and the backwards-compatibility rules are the part a regression
   would be silent in — then `tlsx`, then the listener wiring in `main`
8. frontend (theme → shadcn chrome → bespoke meters/matrix → pages)
9. oauth
10. docs, Docker, systemd

`go build ./...` · `go test ./...` · `make build` (embeds UI) must all pass.
