# FFmpeg failure fixtures

Each `.stderr` file is real FFmpeg stderr from one failed (or, for the control,
successful) publish. `failreason_test.go` classifies every file and asserts the
cause in the table below. None of them was written by hand.

## How they were made

Run `capture/capture.sh`. It:

1. Builds `pe-w1-ffmpeg:8.1.3` from `capture/Dockerfile.ffmpeg`. That is Alpine
   3.24 with `ffmpeg=8.1.3-r0`, the same release and package that the shipping
   image pins.
2. Starts three ingest stand-ins on a private bridge network, `pe-w1-net`:
   - `pe-w1-nginx` (tiangolo/nginx-rtmp, `capture/nginx.conf`). It is an RTMP
     ingest whose `on_publish` check admits only keys that start with `good`.
     It also has two HTTP listeners that play Icecast: 401 on :8000 and 503 on
     :8001.
   - `pe-w1-mediamtx` (bluenviron/mediamtx:1, `capture/mediamtx.yml`). It
     serves RTMP, RTMPS with a fresh self-signed certificate, and SRT. The
     `secured` path needs a publish password, and `open` takes anyone.
   - `pe-w1-blackhole`. It accepts a TCP connection and then never sends
     anything.
3. Runs FFmpeg the way a destination runs it: `-loglevel warning -nostats
   -progress pipe:1`, with a lavfi test source encoded to H.264 and AAC. Each
   scenario writes `NAME.stderr` and adds a row to `manifest.tsv` with the exit
   code, the wall time in ms and the command.
4. Removes every `pe-w1-*` container and the network. It prunes nothing.

No public endpoint is contacted. Every target is a container on `pe-w1-net`, or
a name under `.invalid`, which RFC 6761 reserves so that it never resolves. Every
key and password in these files is fake.

The capture uses `-re`, so a run that falls behind also prints `Resumed reading
… after a lag` lines. A destination reads from the relay without `-re` and never
prints them. The classifier treats them as noise.

## The fixtures and what they must classify as

| Fixture | What was done | Cause |
| --- | --- | --- |
| `rtmp-onpublish-403` | RTMP publish with a key that `on_publish` answers 403 | `key_rejected` |
| `rtmp-auth-failed` | RTMP to MediaMTX `secured` with a wrong password | `key_rejected` |
| `srt-wrong-passphrase` | SRT stream id with a wrong password | `key_rejected` |
| `icecast-401` | Icecast source with a wrong password (HTTP 401) | `key_rejected` |
| `rtmp-unknown-app` | RTMP to an application nginx does not have | `key_rejected` (look-alike) |
| `rtmp-unconfigured-path` | RTMP to a MediaMTX path that is not configured | `key_rejected` (look-alike) |
| `srt-unconfigured-path` | SRT to a MediaMTX path that is not configured | `key_rejected` (look-alike) |
| `icecast-503` | Icecast source against HTTP 503 | `platform_not_ready` |
| `rtmp-already-publishing` | RTMP publish to a key another encoder holds | `platform_not_ready` |
| `rtmp-dns-unresolvable` | RTMP to `pe-w1-no-such-ingest.invalid` | `dns` |
| `rtmp-connection-refused` | RTMP to a port nothing listens on | `connection_refused` |
| `rtmp-silent-ingest-timeout` | RTMP to the blackhole, `-rw_timeout 5s` | `timeout` |
| `rtmps-to-plain-port` | `rtmps://` to the plain RTMP port | `tls` |
| `rtmps-untrusted-cert` | `rtmps://` with `-tls_verify 1` to a self-signed certificate | `tls` |
| `flv-mpeg2-refused` | MPEG-2 video into FLV, refused by FFmpeg's own muxer | `codec_rejected` |
| `rtmp-flv1-dropped` | Sorenson video to MediaMTX, which dropped the connection | `unknown` |
| `rtmp-throttled-upload` | Egress capped at 16 kbit/s under a 1 Mbit/s stream | `upload_too_slow` (from timing) |
| `rtmp-accepted` | A key the ingest admits, `-t 4`, exit 0 | no reason |

## What these files cannot tell apart

These limits are real. They are not gaps in the classifier.

- **A refused key and a wrong path print the same text.** At the default log
  level, nginx-rtmp's `on_publish` 403, an unknown application and an
  unconfigured MediaMTX path all end with only `Error opening output <url>: I/O
  error`. No lower-level line comes before it. The same is true at `-loglevel
  info`. SRT's `Peer rejected connection` is likewise identical for a wrong
  password and an unknown stream id. All of these are `key_rejected`, because
  the key is the part an operator usually got wrong. The remedy text says to
  check the key first and then the server URL.
- **"Already publishing" fails with the same error code as a wrong password.**
  Both come back as `Operation not permitted` (EPERM). The server's own words
  (`Server error: Already publishing`, `Server error: authentication failed`)
  are on the line before, and the classifier decides on that line. "Already
  publishing" is the platform still holding the previous session. It clears by
  itself, so it is never counted toward the retry hold.
- **An ingest that drops a codec it cannot carry gives no reason.**
  `rtmp-flv1-dropped` shows only `Broken pipe`. FFmpeg's stderr has nothing
  else, so the cause is `unknown` with the raw last line. Only a refusal by
  FFmpeg's own muxer (`flv-mpeg2-refused`) is `codec_rejected`.
- **A slow upload prints nothing.** `rtmp-throttled-upload` holds only the `-re`
  lag notices and the flv header lines. The cause comes from timing: media moved
  and the delivery rate fell below realtime. The rate the test uses, 0.0151x, is
  the `speed=` of the final `-progress` block in `rtmp-throttled-upload.progress`.
  For this run the windowed rate and the run average are the same, because the
  run was throttled from the first second. In production the supervisor
  measures the rate over a 10 s window and does not use `speed=`.
