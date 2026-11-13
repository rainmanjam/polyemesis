#!/usr/bin/env python3
"""Writes match.filter and pitch.filter: the -filter_complex_script for the synthetic match feed.

Output labels: [v] (1920x1080, 60 fps) and [a0]..[a5] (stereo, 48 kHz):
a0..a4 = English, Espanol, Portugues, Francais, Arabic commentary (synthetic
speech-like bursts, each with its own fundamental and rhythm), a5 = crowd bed.
Nothing here is real footage, real speech or a real club.
"""
import os
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, HERE)
from voices import VOICES  # noqa: E402

CHECK = "--check" in sys.argv[1:]
FONT = "/usr/share/fonts/dejavu/DejaVuSans.ttf"
BOLD = "/usr/share/fonts/dejavu/DejaVuSans-Bold.ttf"
W, H = 2112, 1188            # canvas; the camera crops 1920x1080 out of it
pitch = [f"color=c=0x14552a:s={W}x{H}:r=1"]
v = []
for i in range(0, 12, 2):    # mowing stripes
    pitch.append(f"drawbox=x={i*176}:y=0:w=176:h={H}:c=0x0f4722:t=fill")
L = "c=white@0.85:t=6"
for b in ("x=160:y=110:w=1792:h=968", "x=1053:y=110:w=6:h=968",
          "x=160:y=330:w=290:h=528", "x=1662:y=330:w=290:h=528",
          "x=160:y=470:w=110:h=248", "x=1842:y=470:w=110:h=248"):
    pitch.append(f"drawbox={b}:{L}")
pitch.append(f"drawtext=fontfile={FONT}:text='◯':fontsize=300:fontcolor=white@0.85:x=1056-text_w/2:y=594-text_h/2")
pitch.append("format=yuv420p[v]")
OUT = {"pitch.filter": ",\n  ".join(pitch) + "\n"}
v.insert(0, "[0:v]format=yuv420p")
# players: (colour, centre x, centre y, amplitude x, amplitude y, period x, period y, phase)
P = []
for k in range(6):
    P.append(("0xd62828", 420 + 150 * k, 300 + 110 * (k % 4), 90 + 15 * k, 70, 17 + 3 * k, 13 + 2 * k, k * 1.1))
    P.append(("0x2f74e0", 1700 - 150 * k, 330 + 100 * ((k + 1) % 4), 90 + 12 * k, 75, 19 + 2 * k, 15 + 2 * k, k * 0.9 + 0.5))
for col, cx, cy, ax, ay, px, py, ph in P:
    v.append(f"drawtext=fontfile={FONT}:text='●':fontsize=46:fontcolor={col}:"
             f"x='{cx}+{ax}*sin(2*PI*t/{px}+{ph})-text_w/2':y='{cy}+{ay}*cos(2*PI*t/{py}+{ph})-text_h/2'")
v.append(f"drawtext=fontfile={FONT}:text='●':fontsize=24:fontcolor=white:"
         "x='1056+620*sin(2*PI*t/9)-text_w/2':y='594+300*sin(2*PI*t/6.3)-text_h/2'")
v.append("crop=1920:1080:x='96+84*sin(2*PI*t/37)':y='54+40*sin(2*PI*t/53+1)'")
v.append("noise=alls=3:allf=t")
# scorebug (drawn after the crop, so it does not pan with the camera)
v.append("drawbox=x=56:y=44:w=740:h=76:c=black@0.74:t=fill")
v.append("drawbox=x=56:y=44:w=10:h=76:c=0xd62828:t=fill")
v.append("drawbox=x=786:y=44:w=10:h=76:c=0x2f74e0:t=fill")
v.append(f"drawtext=fontfile={BOLD}:text='HOME':fontsize=40:fontcolor=white:x=86:y=62")
v.append(f"drawtext=fontfile={BOLD}:text='1 – 0':fontsize=44:fontcolor=0xffd84a:x=250:y=59")
v.append(f"drawtext=fontfile={BOLD}:text='AWAY':fontsize=40:fontcolor=white:x=410:y=62")
v.append(f"drawtext=fontfile={BOLD}:fontsize=40:fontcolor=white:x=600:y=62:"
         "text='%{eif\\:floor((3792+t)/60)\\:d}\\:%{eif\\:mod(floor(3792+t)\\,60)\\:d\\:2}'")
v.append(f"drawtext=fontfile={FONT}:text='MATCH CAM 1 · MANCHESTER':fontsize=28:fontcolor=white@0.8:x=w-text_w-56:y=h-70")
v.append("format=yuv420p,realtime[v]")
out = [",\n  ".join(v)]
for i, (f, p, off, syl) in enumerate(VOICES):
    ph = i * 0.9
    e = (f"0.08*clip(2.5*sin(2*PI*t/{p}+{ph})+{off},0,1)*(0.55+0.45*abs(sin(2*PI*{syl}*t)))"
         f"*(sin(2*PI*{f}*t+12*sin(2*PI*0.3*t))+0.5*sin(4*PI*{f}*t)+0.25*sin(6.2*PI*{f}*t))")
    out.append(f"aevalsrc=exprs='{e}':sample_rate=48000:channel_layout=mono,pan=stereo|c0=c0|c1=c0,arealtime[a{i}]")
out.append("anoisesrc=color=pink:amplitude=0.07:sample_rate=48000,lowpass=f=3500,"
           "volume='1+2.2*pow(max(0,sin(2*PI*t/23)),8)':eval=frame,pan=stereo|c0=c0|c1=c0,arealtime[a5]")
OUT["match.filter"] = ";\n".join(out) + "\n"

if CHECK:
    bad = []
    for name, want in OUT.items():
        try:
            with open(os.path.join(HERE, name), encoding="utf-8") as f:
                have = f.read()
        except OSError:
            have = None
        if have != want:
            bad.append(name)
    if bad:
        print("committed filter graphs differ from the generator: " + ", ".join(bad))
        sys.exit(1)
    print("committed filter graphs match the generator")
else:
    for name, text in OUT.items():
        with open(os.path.join(HERE, name), "w", encoding="utf-8") as f:
            f.write(text)
