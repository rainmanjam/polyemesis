# One-port ingest: design

Inspired by datarhei Core, not copied from it. Core proves the shape works —
one SRT port, many programmes, demultiplexed at accept time. This document is
about the five places their design leaves something on the table, and what
polyemesis does instead.

Verified against `datarhei/gosrt` v0.6.0 (MIT, pure Go, no cgo) and
`datarhei/core`'s `srt/srt.go` and `rtmp/rtmp.go`, read 2026-07-27.

## What Core does

A publisher sets an SRT streamid of the form:

```
<resource>,mode:publish,token:<token>
```

`resource` is a name the *publisher* chooses. `token` is a single value from
the server's configuration — `srt.token`, one for the whole server. RTMP is the
same idea over a path: `rtmp://host:1935/live/<name>.stream?token=<token>`.

Core's `gosrt.Server` exposes `HandleConnect(ConnRequest) ConnType`, which sees
the streamid before the connection is established and may return
`PUBLISH | SUBSCRIBE | REJECT`.

## Five things to do better

### 1. The token IS the address

Core separates *which* programme (`resource`) from *whether you may publish*
(`token`), and the token is shared across every resource on the server. Their
own tracker has "Single Token per RTMP Endpoint" open against exactly this.

polyemesis uses one opaque per-source token as the entire streamid:

```
streamid=<token>
```

The token names the programme and authorises it in one value. This is both
simpler and stronger:

- **No shared secret.** Learning one source's token tells you nothing about any
  other, and rotating one revokes exactly one programme.
- **No name squatting.** In Core, resource names are publisher-chosen strings
  and "you can publish only one stream with that same name" — so anyone who
  learns the shared token can occupy a name and lock the legitimate publisher
  out. Here you cannot address a source without already holding its token.
- **No enumeration oracle.** An unknown token and a wrong token are the same
  rejection, so probing cannot distinguish "this programme exists" from "it
  does not".
- **No grammar to get wrong.** There is no mini-language to parse, so there is
  no parser to disagree with the encoder about.

Lookup is `SourceByToken`, which already exists, with a constant-time compare.

### 2. Stale-connection takeover

Core refuses a second publisher for a resource that is already publishing.
That is correct against genuine double-publishing and wrong in the case that
actually happens: an encoder's uplink drops, the far side has not yet reaped
the dead socket, the encoder reconnects — and is refused. The operator is off
the air and cannot get back on until a timeout they cannot see.

polyemesis compares liveness before refusing. If the incumbent has delivered no
data within a grace window it is evicted and the newcomer accepted; if the
incumbent is genuinely live, the newcomer is refused with a rejection code that
says so. Reconnect after a blip becomes immediate, and real double-publishing
is still refused.

This is the difference between "my stream came back" and "my stream is down and
the server will not let me in".

### 3. Rejections an operator can act on

gosrt exposes the full SRT rejection set — `REJ_BADSECRET`, `REJ_RESOURCE`,
`REJ_ROGUE`, `REJ_CLOSE`, `REJ_BACKLOG` and the rest — and libsrt surfaces the
code to the caller. Core rejects; the streamer sees a generic failure in OBS.

polyemesis maps causes to codes:

| Cause | Code | What OBS can show |
|---|---|---|
| Token unknown or wrong | `REJ_BADSECRET` | rejected credentials |
| Source already publishing, and live | `REJ_RESOURCE` | resource busy |
| Source disabled | `REJ_CLOSE` | not accepting |
| Malformed streamid | `REJ_ROGUE` | bad request |

Each is also logged server-side with the peer address and the source it was
aimed at — never the token itself.

### 4. Rotate without dropping the stream

Rotating a shared token in Core means editing config; every publisher using it
breaks. Even per-source, a naive rotation kills a live stream the moment it
lands, which makes rotation something operators avoid — and a credential nobody
rotates is the problem the feature was meant to solve.

polyemesis keeps the previous token valid for a grace window after rotation
(and never past the end of the current session). The live stream survives, the
new token works immediately, and the old one expires on its own.

### 5. Per-source link telemetry

gosrt's `statistics.go` carries RTT, packet loss and retransmission per
connection. Core has an SRT stats endpoint; Restreamer's UI does not put link
quality in front of the operator per channel.

polyemesis surfaces RTT, loss and retransmit rate on each source card, because
"why is my stream breaking up" is a question about the uplink, and with several
programmes on one install the answer is per programme.

## Architectural consequence: one fewer process per source

Today the SRT ingest is an FFmpeg child doing
`-i srt://… -c copy -f mpegts udp://relay`. The relay hub already speaks
MPEG-TS bytes and `gosrt.Conn` is an `io.Reader`, so the Go listener writes
straight into the hub: no child, no remux hop, and one less thing to supervise.

It also removes a dependency on how the host's FFmpeg was compiled. Homebrew's
FFmpeg has no SRT, which is why the host acceptance suites have never exercised
a real SRT publisher and every SRT proof so far has had to run in Docker.

## Not doing (yet)

- **RTMP on one port.** It stays per-source. RTMP is the single-track fallback,
  so a port each costs little, and a Go RTMP server is a separate dependency
  decision — Core uses a joy4 fork; `mediamtx` is the better-maintained MIT
  option today.
- **Replacing per-port addressing.** Both work at once: the shared port
  demultiplexes by token, and a source may still hold a dedicated port. Core is
  one-port only; per-port remains genuinely useful for firewall rules, separate
  NICs and QoS marking per programme.

## Risk

gosrt owns the handshake, encryption and congestion control. polyemesis owns
accept → demux → hub, reconnect behaviour, the stats the dashboard reads, and
the interaction with the failover selector. That is a real subsystem in the one
path where a bug means nobody's stream comes up, so it ships behind a setting
with per-port addressing as the fallback until it has proven itself on real
hardware.
