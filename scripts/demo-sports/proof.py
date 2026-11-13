#!/usr/bin/env python3
"""Optional, manual: proves each destination path carries its own language.

Usage: python3 scripts/demo-sports/proof.py <dir>
<dir> holds <en|es|pt|fr|ar>.pcm (s16le, stereo, 48 kHz, a capture of what the
receiver got on that path) and ebur-<k>.log (ffmpeg ebur128 output for it).

Exit 0 when, on every path, its own voice is at least 15 dB above the strongest
other voice; exit 1 otherwise; exit 2 when numpy is missing.
"""
import math
import os
import re
import sys

try:
    import numpy as np
except ImportError:
    print("numpy is required for the proof")
    sys.exit(2)

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from voices import VOICES  # noqa: E402

CODES = ["en", "es", "pt", "fr", "ar"]
NAMES = {"en": "English", "es": "Español", "pt": "Português", "fr": "Français", "ar": "Arabic"}
HZ = {c: v[0] for c, v in zip(CODES, VOICES)}
RATE = 48000
MIN_MARGIN_DB = 15.0


def band_energy(x, hz):
    """Energy within +-7 Hz of hz in the Hann-windowed spectrum of x."""
    spec = np.abs(np.fft.rfft(x * np.hanning(len(x)))) ** 2
    fr = np.fft.rfftfreq(len(x), 1 / RATE)
    return spec[(fr > hz - 7) & (fr < hz + 7)].sum()


def margin_db(x, own):
    """dB by which the path's own voice exceeds the strongest other voice."""
    e = {c: band_energy(x, HZ[c]) for c in CODES}
    other = max(e[c] for c in CODES if c != own)
    return 10 * math.log10(max(e[own], 1e-12) / max(other, 1e-12))


def envelope(x):
    w = 2400
    n = len(x) // w
    return np.sqrt((x[:n * w].reshape(n, w) ** 2).mean(axis=1))


def correlation(a, b):
    """Envelope correlation, over the shorter of the two captures."""
    m = min(len(a), len(b))
    return np.corrcoef(a[:m], b[:m])[0, 1]


def load_mono(path):
    x = np.fromfile(path, dtype="<i2").astype(np.float64) / 32768.0
    return x.reshape(-1, 2).mean(axis=1) if x.size else np.zeros(RATE)


def main(d):
    ok = True
    env = {}
    print("%-22s %-10s %-10s %-12s" % ("path", "LUFS", "peak dBTP", "own margin dB"))
    for k in CODES:
        x = load_mono(os.path.join(d, k + ".pcm"))
        log = open(os.path.join(d, "ebur-" + k + ".log"), errors="replace").read()
        lufs = re.findall(r"I:\s+(-?[\d.]+) LUFS", log)
        peak = re.findall(r"Peak:\s+(-?[\d.]+) dBFS", log)
        m = margin_db(x, k)
        ok &= m >= MIN_MARGIN_DB
        env[k] = envelope(x)
        print("%-22s %-10s %-10s %-12.1f" % ("live/%s (%s)" % (k, NAMES[k]), lufs[-1] if lufs else "?", peak[-1] if peak else "?", m))
    print("\nown voice at least %.0f dB above every other on every path: %s" % (MIN_MARGIN_DB, "YES" if ok else "NO"))
    print("envelope correlation English vs Español: %.2f  (1.00 would mean the same mix)" % correlation(env["en"], env["es"]))
    return 0 if ok else 1


if __name__ == "__main__":
    if len(sys.argv) != 2:
        print("usage: proof.py <dir>")
        sys.exit(2)
    sys.exit(main(sys.argv[1]))
