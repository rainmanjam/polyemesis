#!/usr/bin/env python3
"""Does E-RTMP multitrack survive polyemesis's RTMP ingest path intact?

Run:  python3 scripts/verify-ertmp-multitrack.py [runs]

Builds a six-track FLV (MaxTracks), publishes it into an `ffmpeg -listen 1`
receiver, and checks what arrives.

WHAT THIS DOES AND DOES NOT COVER
It is a conformance check on FFMPEG: can this build mux six E-RTMP tracks, and
demux them again with the order intact. That is worth knowing on its own — the
answer is no below 7.1 — and it is why the tone detection is here.

It is NOT polyemesis's ingest path any more, and used to claim to be. IngestArgs
passed `-listen 1` when FFmpeg was the RTMP server; the listener is now
internal/rtmpserver and the ingest child DIALS it. The path that ships is covered
by TestEnhancedRTMPMultitrackSurvivesTheSharedListenerInOrder, which publishes
through the real server and identifies the tracks on arrival.

Keeping that distinction straight matters: while this script was passing, late
subscribers to a real multitrack stream were receiving decoder configuration for
the legacy track only, because rtmpserver did not recognise a sequence start
wrapped in AudioExMultitrack. This script cannot see that — there is no
rtmpserver in it.

WHY IT MEASURES TONES RATHER THAN COUNTING STREAMS
Six tracks in and six tracks out looks identical whether or not they were
reordered, and a reordering is the failure that matters -- polyemesis routes by
track index, so a shifted order silently sends the wrong audio to a platform
while every screen still looks correct. So each track carries a distinct tone
and is identified on arrival by its content.

To confirm the harness can actually fail, pass --shuffle: it republishes with the
tracks permuted and must report exactly that permutation.

Requires ffmpeg/ffprobe with libopus. No other dependencies -- the tone detector
is a Goertzel filter rather than an FFT so numpy is not needed.
"""
import argparse, json, math, struct, subprocess, sys, time

FREQS = [300, 500, 700, 1100, 1300, 1700]
PORT, SR = 11935, 8000
SHUFFLE = [3, 0, 5, 1, 4, 2]


def run(argv, **kw):
    return subprocess.run(argv, capture_output=True, **kw)

def goertzel(samples, freq, sr=SR):
    """Power at one frequency. Cheaper than an FFT and needs no numpy."""
    w = 2 * math.pi * freq / sr
    coeff, s1, s2 = 2 * math.cos(w), 0.0, 0.0
    for x in samples:
        s0 = x + coeff * s1 - s2
        s2, s1 = s1, s0
    return s1 * s1 + s2 * s2 - coeff * s1 * s2

def tone_of(path, stream):
    """Which published tone is in this audio stream?"""
    r = run(["ffmpeg", "-hide_banner", "-loglevel", "error", "-i", path,
             "-map", f"0:a:{stream}", "-t", "1", "-f", "s16le", "-ac", "1",
             "-ar", str(SR), "-"])
    if r.returncode != 0 or len(r.stdout) < 2 * SR // 2:
        return None, 0.0
    n = len(r.stdout) // 2
    xs = struct.unpack(f"<{n}h", r.stdout[: n * 2])
    powers = {f: goertzel(xs, f) for f in FREQS}
    best = max(powers, key=powers.get)
    runner = max((p for f, p in powers.items() if f != best), default=1.0) or 1.0
    return best, powers[best] / runner        # margin over the next candidate

def one_session(tag, flv):
    ts = f"rx-{tag}.ts"
    # The mpegts muxer and `-map 0 -c copy` that the destination side still uses,
    # with `-listen 1` standing in for an RTMP server. Not IngestArgs: see the
    # module docstring.
    listener = subprocess.Popen(
        ["ffmpeg", "-hide_banner", "-loglevel", "error", "-listen", "1",
         "-fflags", "+genpts", "-i", f"rtmp://127.0.0.1:{PORT}/live/test",
         "-map", "0", "-c", "copy", "-f", "mpegts", "-flush_packets", "1",
         "-y", ts],
        stdout=subprocess.DEVNULL, stderr=subprocess.PIPE)
    time.sleep(1.5)
    pub = run(["ffmpeg", "-hide_banner", "-loglevel", "error", "-re",
               "-i", flv, "-map", "0", "-c", "copy", "-f", "flv",
               f"rtmp://127.0.0.1:{PORT}/live/test"])
    listener.wait(timeout=30)
    if pub.returncode != 0:
        return {"error": pub.stderr.decode()[:200]}

    # polyemesis's real ProbeArgs (internal/ffmpeg/build.go:1074).
    probe = run(["ffprobe", "-hide_banner", "-loglevel", "error",
                 "-print_format", "json", "-show_streams", "-show_format",
                 "-analyzeduration", "5000000", "-probesize", "5000000", "-i", ts])
    streams = json.loads(probe.stdout)["streams"] if probe.returncode == 0 else []
    audio = [s for s in streams if s["codec_type"] == "audio"]

    order, margins = [], []
    for i in range(len(audio)):
        f, m = tone_of(ts, i)
        order.append(f); margins.append(m)
    return {"probe_audio": len(audio),
            "probe_video": sum(1 for s in streams if s["codec_type"] == "video"),
            "order": order, "min_margin": min(margins) if margins else 0}


def build(codec, shuffled):
    """Six tones + video into FLV.

    AAC gives track 0 a legacy tag; Opus has no legacy FLV SoundFormat so track 0
    goes out as ExHeader+fourCC, which is the shape OBS writes track 0 in
    (plugins/obs-outputs/flv-mux.c). Testing both covers both.
    """
    out = f"six-{codec}{'-shuf' if shuffled else ''}.flv"
    argv = ["ffmpeg", "-hide_banner", "-loglevel", "error", "-y",
            "-f", "lavfi", "-i", "testsrc2=size=320x240:rate=15:duration=3"]
    order = [FREQS[i] for i in SHUFFLE] if shuffled else FREQS
    for f in order:
        argv += ["-f", "lavfi", "-i", f"sine=frequency={f}:duration=3"]
    argv += ["-map", "0:v"] + sum(([f"-map", f"{i+1}:a"] for i in range(len(order))), [])
    argv += ["-c:v", "libx264", "-preset", "ultrafast",
             "-c:a", {"aac": "aac", "opus": "libopus"}[codec], "-b:a", "96k",
             "-f", "flv", out]
    r = run(argv)
    if r.returncode != 0:
        sys.exit(f"could not build {out}: {r.stderr.decode()[:300]}")
    return out, order


def tags(path):
    """Confirm the stream really is E-RTMP multitrack and not legacy FLV."""
    d = open(path, "rb").read()
    off, seen = 9, {}
    while off + 11 <= len(d):
        off += 4
        if off + 11 > len(d):
            break
        t, sz = d[off] & 0x1f, int.from_bytes(d[off+1:off+4], "big")
        body = d[off+11: off+11+sz]
        if t == 8 and body:
            b = body[0]
            hi, lo = b >> 4, b & 0xf
            kind = "legacy" if hi != 9 else ("ExHeader Multitrack" if lo == 5 else f"ExHeader pkt={lo}")
            seen[f"0x{b:02x} {kind}"] = seen.get(f"0x{b:02x} {kind}", 0) + 1
        off += 11 + sz
    return seen


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("runs", nargs="?", type=int, default=3)
    ap.add_argument("--codec", choices=["aac", "opus", "both"], default="both")
    ap.add_argument("--shuffle", action="store_true",
                    help="publish permuted; the harness must report the permutation")
    a = ap.parse_args()

    codecs = ["aac", "opus"] if a.codec == "both" else [a.codec]
    ok = True
    for codec in codecs:
        flv, published = build(codec, a.shuffle)
        print(f"\n=== {codec} === published order {published}")
        for k, v in sorted(tags(flv).items()):
            print(f"    {k}  x{v}")
        for i in range(a.runs):
            r = one_session(i, flv)
            if "error" in r:
                print(f"  run {i}: FAILED {r['error']}"); ok = False; continue
            match = r["order"] == published
            ok &= match and r["probe_audio"] == len(FREQS)
            print(f"  run {i}: probe {r['probe_video']}v+{r['probe_audio']}a  "
                  f"order {r['order']}  {'MATCH' if match else '*** MISMATCH ***'}")

    if a.shuffle:
        print("\n--shuffle: MATCH lines above are correct — the harness tracked the permutation.")
    print("\nVERDICT:", "six tracks, order preserved, every run" if ok else "FAILED")
    return 0 if ok else 1


if __name__ == "__main__":
    sys.exit(main())
