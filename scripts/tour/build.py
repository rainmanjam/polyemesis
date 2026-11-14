"""Generates the HyperFrames project files (index.html + compositions/*.html) and chapters.json
from edit.json (shot durations) and the shot specs below. Reader: hyperframes check/render."""
import json, os, re
os.chdir(os.path.dirname(os.path.abspath(__file__)))   # edit.json, hf/ and chapters.json sit beside this file
HF = "hf"
D = 0.0  # hard cuts between shots
ED = json.load(open("edit.json"))
ORDER = ["s1a", "s1b", "s2a", "s2b", "s2c", "s3a", "s3b", "s3c"]
CH = [("01 · ONE STREAM IN", "ONE STREAM IN", "s1a"), ("02 · SPLITTING THE AUDIO", "SPLITTING THE AUDIO", "s2a"),
      ("03 · GOING LIVE EVERYWHERE", "GOING LIVE EVERYWHERE", "s3a")]
START = {}
t = 0.0
for s in ORDER:
    START[s] = round(t, 3)
    t = t + ED[s] - D
END_START = round(t, 3)          # end card starts when the last shot's dissolve begins
ENDDUR = 3.8
TOTAL = round(END_START + ENDDUR, 3)
CONTENT_END = END_START           # overlays fade out here

EASE = "sine.inOut"
def aff(o, s_): return (s_, o[0] * (1 - s_), o[1] * (1 - s_))
def Zs(t, d, o, a, b, e=EASE): return dict(t=t, d=d, A=aff(o, a), B=aff(o, b), e=e)
def Za(t, d, A, B, e=EASE): return dict(t=t, d=d, A=A, B=B, e=e)
# dashboard shots: a constant 1.3x frame that keeps the right-hand column (Chat/Audience/Pipeline) out of shot
DASH_TOP = (1.3, -3, 0)
DASH_INGEST = (1.3, -3, -313)
# s3a is framed 117 px lower down: the console header and the foot of the empty "Stream info" card above Destinations stay out of shot
DASH_DEST = (1.3, -3, -117)
BASE = {"s1a": DASH_INGEST, "s3a": DASH_DEST, "s3b": DASH_TOP}
ZOOM = {
    "s1a": [],
    "s1b": [Zs(1.0, 9.5, (1000, 500), 1, 1.12)],
    "s2a": [Zs(0.6, 7.0, (1400, 100), 1, 1.12)],
    "s2b": [],
    "s2c": [Zs(0.5, 8.5, (1400, 100), 1, 1.15)],
    "s3a": [Za(2.0, 0.9, DASH_DEST, (1.4, -384, -326))],
    "s3b": [],
    "s3c": [],
}

def chip_w(text): return int(round(len(text) * 13.7 + 28))
def chip(x, y, lines): return dict(x=x, y=y, lines=lines, w=max(chip_w(l) for l in lines), h=36 if len(lines) == 1 else 64)
def co(shot, cid, t, d, rings, paths, anchor, cx, cy, lines):
    c = chip(cx, cy, lines)
    return dict(shot=shot, id=cid, t=t, d=d, rings=rings, paths=paths, anchor=anchor, chip=c)

def diag_rings():
    xs = [937.0, 1085.7, 1235.4, 1385.0, 1533.8]
    r = [[x - 3, 215 + 49 * i - 3, 62, 42] for i, x in enumerate(xs)]
    segs = [[(r[i][0] + 59, r[i][1] + 39), (r[i + 1][0] + 1, r[i + 1][1] + 3)] for i in range(4)]
    return r, segs

S2B = 0.55   # in this take the s2b clicks land 0.55 s later than in the first cut
dr, dsegs = diag_rings()
CALLOUTS = [
    co("s1a", "a1", 3.6, 3.2, [[292, 775, 200, 26]], [[(492, 788), (610, 788)]], (610, 788), 610, 771, ["ONE FEED · SRT · 1080p60"]),
    co("s1a", "a2", 6.9, 3.2, [[873, 731, 110, 42]], [[(928, 731), (928, 722)]], (928, 722), 881, 686, ["6 AUDIO TRACKS"]),
    co("s1b", "b1", 1.0, 4.6, [], [[(561, 418), (553, 418), (553, 766), (561, 766)], [(553, 592), (522, 592)]], (522, 592), 300, 561, ["FIVE COMMENTARY", "LANGUAGES"]),
    co("s1b", "b2", 6.2, 3.6, [[566, 774, 1221, 68]], [[(566, 808), (396, 808)]], (396, 808), 300, 791, ["CROWD"]),
    co("s2a", "c1", 0.9, 5.6, [[925, 207, 826, 250]], [[(1610, 207), (1610, 156)]], (1610, 156), 1340, 122, ["EVERY CHANNEL HEARS EVERYTHING"]),
    co("s2b", "d1", 1.7 + S2B, 3.6, [[298, 198, 236, 72]], [[(298, 224), (288, 224), (288, 522), (296, 522)]], (296, 522), 296, 505, ["YOUTUBE · ESPAÑOL"]),
    co("s2b", "d2", 3.4 + S2B, 5.8, [[571, 432, 32, 32], [571, 576, 32, 32], [571, 648, 32, 32], [571, 720, 32, 32]], [[(571, 592), (557, 592)]], (557, 592), 296, 561 + 8, ["SWITCHING OFF", "EN · PT · FR · AR"]),
    co("s2b", "d3", 9.6 + S2B, 4.0, [[566, 485, 1221, 70], [566, 773, 1221, 70]],
       [[(530, 652), (556, 652)], [(556, 520), (556, 808)], [(556, 520), (566, 520)], [(556, 808), (566, 808)]], (530, 652), 296, 620, ["KEEPS:", "ESPAÑOL + CROWD"]),
    co("s2c", "e1", 0.9, 4.0, dr, dsegs + [[(996, 233), (1039, 233), (1039, 156)]], (1039, 156), 870, 122, ["ONE LANGUAGE PER CHANNEL"]),
    co("s2c", "e2", 5.1, 3.8, [[1672, 183, 76, 274]], [[(1710, 183), (1710, 156)]], (1710, 156), 1540, 122, ["CROWD ON ALL FIVE"]),
    co("s3b", "f1", 1.0, 4.2, [[296, 129, 566, 32]], [[(862, 145), (930, 145)]], (930, 145), 930, 128, ["5 LIVE · FROM ONE UPLOAD"]),
    co("s3c", "g1", 1.0, 5.2, [[1184, 116, 332, 48]], [[(1346, 164), (1346, 220)]], (1346, 220), 1188, 220, ["MEASURED AS IT LEAVES"]),
    co("s3c", "g2", 1.0, 5.2, [[298, 124, 236, 56]], [[(298, 155), (288, 155), (288, 442), (296, 442)]], (296, 442), 296, 425, ["YOUTUBE · ENGLISH"]),
    co("s3c", "g3", 7.7, 5.8, [[1184, 116, 332, 48]], [[(1346, 164), (1346, 220)]], (1346, 220), 1188, 220, ["MEASURED AS IT LEAVES"]),
    co("s3c", "g4", 7.7, 5.8, [[298, 177, 236, 56]], [[(298, 207), (288, 207), (288, 442), (296, 442)]], (296, 442), 296, 425, ["YOUTUBE · ESPAÑOL"]),
]
CAPS = [  # shot, text, local start, duration
    ("s1a", "One match feed comes in.", 0.6, 5.6),
    ("s1b", "Five languages and the crowd. Each on its own track.", 0.6, 10.2),
    ("s2a", "To start, every channel gets every track.", 0.5, 7.4),
    ("s2b", "Choose what each channel hears.", 0.6, 12.4),
    ("s2c", "Same picture. Five different soundtracks.", 0.5, 8.4),
    ("s3a", "One click starts all five.", 0.6, 14.2),
    ("s3b", "Five channels live from one upload.", 0.5, 5.1),
    ("s3c", "Each channel's own mix, measured on the way out.", 0.6, 12.8),
]
G = lambda shot, t: round(START[shot] + t, 3)

def M(p, o, s): return (o[0] + (p[0] - o[0]) * s, o[1] + (p[1] - o[1]) * s)
def f(v): return round(v, 2)

# ---------------- callouts composition ----------------
def callouts_html():
    els, js = [], []
    for c in CALLOUTS:
        s0 = G(c["shot"], c["t"])
        ring_svg = "".join('<rect id="%s-r%d" rx="8" ry="8" fill="none" stroke="#ece9e2" stroke-width="2" x="%s" y="%s" width="%s" height="%s"/>' % (c["id"], k, *r) for k, r in enumerate(c["rings"]))
        line_svg, lines = "", []
        for pi, poly in enumerate(c["paths"]):
            for j in range(len(poly) - 1):
                lid = "%s-l%d-%d" % (c["id"], pi, j)
                (x1, y1), (x2, y2) = poly[j], poly[j + 1]
                line_svg += '<line id="%s" stroke="#ece9e2" stroke-width="1.75" stroke-linecap="round" x1="%s" y1="%s" x2="%s" y2="%s"/>' % (lid, x1, y1, x2, y2)
                lines.append((lid, poly[j], poly[j + 1]))
        ch = c["chip"]
        els.append('<div id="%s" class="clip co" data-start="%s" data-duration="%s"><svg class="sv" viewBox="0 0 1920 1080" width="1920" height="1080">%s%s</svg>'
                   '<div id="%s-chip" class="chip" style="width:%dpx;height:%dpx">%s</div></div>' % (c["id"], s0, c["d"], ring_svg, line_svg, c["id"], ch["w"], ch["h"], "<br/>".join(ch["lines"])))
        js.append(dict(id=c["id"], s=s0, d=c["d"], shot=c["shot"], rings=c["rings"], lines=[(a, list(p), list(q)) for a, p, q in lines],
                       anchor=list(c["anchor"]), off=[ch["x"] - c["anchor"][0], ch["y"] - c["anchor"][1]]))
    zs = []
    for shot in ORDER:
        for z in ZOOM[shot]:
            zs.append(dict(t=G(shot, z["t"]), d=z["d"], A=list(z["A"]), B=list(z["B"]), e=z["e"], shot=shot))
    return """<template>
  <style>
    @font-face { font-family: "JetBrains Mono"; src: url("assets/fonts/jetbrains-mono.woff2") format("woff2"); font-weight: 100 800; }
    #root { position: absolute; inset: 0; }
    .co { position: absolute; left: 0; top: 0; width: 1920px; height: 1080px; }
    .sv { position: absolute; left: 0; top: 0; overflow: visible; }
    .chip { position: absolute; left: 0; top: 0; background: #0c0e12; border: 1px solid #1f2228; border-radius: 6px; padding: 4px 12px;
      font-family: "JetBrains Mono", monospace; font-weight: 500; font-size: 22px; line-height: 26px; letter-spacing: 0.02em; color: #ece9e2; white-space: nowrap; }
  </style>
  <div id="root" data-composition-id="callouts" data-width="1920" data-height="1080">
    %s
  </div>
  <script>
    const C = %s;
    const Z = %s;
    const BASE = %s;
    const M = (p, T) => [T[0] * p[0] + T[1], T[0] * p[1] + T[2]];
    const tl = gsap.timeline({ paused: true });
    const st = (c, T) => {
      const A = M(c.anchor, T);
      return {
        rings: c.rings.map((r) => { const p = M([r[0], r[1]], T); return { x: p[0], y: p[1], width: r[2] * T[0], height: r[3] * T[0] }; }),
        lines: c.lines.map((l) => { const a = M(l[1], T), b = M(l[2], T); return { x1: a[0], y1: a[1], x2: b[0], y2: b[1] }; }),
        chip: [A[0] + c.off[0], A[1] + c.off[1]],
      };
    };
    C.forEach((c) => {
      const zs = Z.filter((z) => z.shot === c.shot);
      const s0 = st(c, BASE[c.shot] || [1, 0, 0]);
      c.rings.forEach((r, k) => gsap.set("#" + c.id + "-r" + k, { attr: s0.rings[k] }));
      c.lines.forEach((l, k) => gsap.set("#" + l[0], { attr: s0.lines[k] }));
      gsap.set("#" + c.id + "-chip", { x: s0.chip[0], y: s0.chip[1] });
      gsap.set("#" + c.id, { opacity: 0 });
      zs.forEach((z) => {
        const a = st(c, z.A), b = st(c, z.B), v = { duration: z.d, ease: z.e, immediateRender: false };
        c.rings.forEach((r, k) => tl.fromTo("#" + c.id + "-r" + k, { attr: a.rings[k] }, { attr: b.rings[k], ...v }, z.t));
        c.lines.forEach((l, k) => tl.fromTo("#" + l[0], { attr: a.lines[k] }, { attr: b.lines[k], ...v }, z.t));
        tl.fromTo("#" + c.id + "-chip", { x: a.chip[0], y: a.chip[1] }, { x: b.chip[0], y: b.chip[1], ...v }, z.t);
      });
      tl.fromTo("#" + c.id, { opacity: 0 }, { opacity: 1, duration: 0.45, ease: "power2.out", immediateRender: false }, c.s);
      tl.fromTo("#" + c.id, { opacity: 1 }, { opacity: 0, duration: 0.35, ease: "power2.in", immediateRender: false }, c.s + c.d - 0.4);
    });
    window.__timelines["callouts"] = tl;
  </script>
</template>
""" % ("\n    ".join(els), json.dumps(js), json.dumps(zs), json.dumps({k: list(v) for k, v in BASE.items()}))

# ---------------- captions + scrim composition ----------------
def captions_html():
    els, caps = [], []
    for i, (shot, text, t0, d) in enumerate(CAPS):
        s = G(shot, t0)
        els.append('<div id="cap%d" class="clip cap" data-start="%s" data-duration="%s">%s</div>' % (i, s, d, text.replace("'", "&#39;")))
        caps.append([s, d])
    s2b = START["s2b"] + S2B   # the save click, scrim and patch follow the take
    return """<template>
  <style>
    @font-face { font-family: "Bricolage Grotesque"; src: url("assets/fonts/bricolage.woff2") format("woff2"); font-weight: 200 800; }
    #root { position: absolute; inset: 0; }
    #scrim { position: absolute; left: 0; bottom: 0; width: 1920px; height: 280px; background: linear-gradient(to top, rgba(7,8,10,0.85) 0%%, rgba(7,8,10,0) 100%%); }
    #patch { position: absolute; left: 1520px; top: 985px; width: 400px; height: 95px; background: rgba(7,8,10,0.97); border-radius: 14px; box-shadow: 0 0 28px 20px rgba(7,8,10,0.97); }
    .hp { position: absolute; left: 1888px; top: 0; width: 32px; height: 60px; background: #0c0d11; }
    .cap { position: absolute; left: 96px; bottom: 84px; color: #ece9e2; font-family: "Bricolage Grotesque", sans-serif; font-weight: 600;
      font-size: 58px; line-height: 66px; letter-spacing: -0.02em; white-space: nowrap; }
  </style>
  <div id="root" data-composition-id="captions" data-width="1920" data-height="1080">
    <div id="scrim" class="clip" data-start="0" data-duration="%s"></div>
    <div id="patch" class="clip" data-start="%s" data-duration="1.95"></div>
    <div id="hp1" class="clip hp" data-start="%s" data-duration="%s"></div>
    %s
  </div>
  <script>
    const tl = gsap.timeline({ paused: true });
    const CAPS = %s;
    CAPS.forEach(([s, d], i) => {
      tl.fromTo("#cap" + i, { opacity: 0, y: 14 }, { opacity: 1, y: 0, duration: 0.55, ease: "power3.out", immediateRender: false }, s);
      tl.fromTo("#cap" + i, { opacity: 1, y: 0 }, { opacity: 0, y: -8, duration: 0.4, ease: "power2.in", immediateRender: false }, s + d - 0.45);
    });
    gsap.set("#patch", { opacity: 0 });
    tl.fromTo("#patch", { opacity: 0 }, { opacity: 1, duration: 0.2, immediateRender: false }, %s);
    tl.fromTo("#patch", { opacity: 1 }, { opacity: 0, duration: 0.2, immediateRender: false }, %s);
    // the Save button sits under the scrim: let the scrim step aside while it is clicked
    tl.fromTo("#scrim", { opacity: 1 }, { opacity: 0, duration: 0.5, ease: "power2.inOut", immediateRender: false }, %s);
    tl.fromTo("#scrim", { opacity: 0 }, { opacity: 1, duration: 0.5, ease: "power2.inOut", immediateRender: false }, %s);
    tl.fromTo("#scrim", { opacity: 1 }, { opacity: 0, duration: 0.3, immediateRender: false }, %s);
    window.__timelines["captions"] = tl;
  </script>
</template>
""" % (CONTENT_END + 0.3, f(s2b + 14.55), START["s3b"], ED["s3b"], "\n    ".join(els), json.dumps(caps), f(s2b + 14.55), f(s2b + 16.25), f(s2b + 13.1), f(s2b + 14.55), f(CONTENT_END))

# ---------------- chapter label + progress ----------------
def chrome_html():
    cs = [START[c[2]] for c in CH]
    ends = cs[1:] + [CONTENT_END]
    labs = []
    for i, (full, short, _) in enumerate(CH):
        a = cs[i] + (0.15 if i == 0 else D + 0.05)
        b = ends[i] + 0.3
        labs.append('<div id="lab%d" class="clip lab" data-start="%s" data-duration="%s">%s</div>' % (i, round(a, 3), round(b - a, 3), full))
    segs = "".join('<div class="seg"><div id="fill%d" class="fill"></div></div>' % i for i in range(3))
    return """<template>
  <style>
    @font-face { font-family: "JetBrains Mono"; src: url("assets/fonts/jetbrains-mono.woff2") format("woff2"); font-weight: 100 800; }
    #root { position: absolute; inset: 0; }
    #hscrim { position: absolute; left: 250px; top: 0; width: 1000px; height: 78px; background: linear-gradient(90deg, rgba(7,8,10,0.62) 0%%, rgba(7,8,10,0.62) 55%%, rgba(7,8,10,0) 100%%); }
    .lab { position: absolute; left: 290px; top: 9px; transform-origin: 0 0; font-family: "JetBrains Mono", monospace; font-weight: 500; font-size: 22px;
      line-height: 26px; letter-spacing: 0.04em; color: #ece9e2; white-space: nowrap; }
    #prog { position: absolute; left: 290px; top: 42px; width: 408px; height: 3px; display: flex; gap: 8px; }
    .seg { position: relative; flex: 1; height: 3px; background: rgba(236,233,226,0.22); border-radius: 2px; overflow: hidden; }
    .fill { position: absolute; left: 0; top: 0; width: 100%%; height: 100%%; background: #ece9e2; transform-origin: 0 0; transform: scaleX(0); }
  </style>
  <div id="root" data-composition-id="chrome" data-width="1920" data-height="1080">
    <div id="hscrim" class="clip" data-start="0" data-duration="%s"></div>
    %s
    <div id="prog" class="clip" data-start="0" data-duration="%s">%s</div>
  </div>
  <script>
    const tl = gsap.timeline({ paused: true });
    const CS = %s, ENDS = %s;
    gsap.set("#prog", { opacity: 0 });
    CS.forEach((c, i) => {
      const a = i === 0 ? 0.15 : c + 0.35;
      gsap.set("#lab" + i, { opacity: 0, scale: 2.3 });
      tl.fromTo("#lab" + i, { opacity: 0, scale: 2.3 }, { opacity: 1, scale: 2.3, duration: 0.3, ease: "power2.out", immediateRender: false }, a);
      tl.fromTo("#lab" + i, { scale: 2.3 }, { scale: 1, duration: 0.7, ease: "power2.inOut", immediateRender: false }, a + 1.2);
      tl.fromTo("#lab" + i, { opacity: 1 }, { opacity: 0, duration: 0.3, ease: "power2.in", immediateRender: false }, ENDS[i]);
      tl.fromTo("#prog", { opacity: 1 }, { opacity: 0, duration: 0.15, immediateRender: false }, i === 0 ? 0.1 : c + 0.1);
      tl.fromTo("#prog", { opacity: 0 }, { opacity: 1, duration: 0.3, immediateRender: false }, a + 1.9);
      tl.fromTo("#fill" + i, { scaleX: 0 }, { scaleX: 1, duration: ENDS[i] - c, ease: "none", immediateRender: false }, c);
    });
    tl.fromTo("#prog", { opacity: 1 }, { opacity: 0, duration: 0.3, immediateRender: false }, ENDS[2]);
    window.__timelines["chrome"] = tl;
  </script>
</template>
""" % (CONTENT_END + 0.3, "\n    ".join(labs), CONTENT_END + 0.3, segs, json.dumps(cs), json.dumps(ends))

def endcard_html():
    return """<template>
  <style>
    @font-face { font-family: "Bricolage Grotesque"; src: url("assets/fonts/bricolage.woff2") format("woff2"); font-weight: 200 800; }
    @font-face { font-family: "Instrument Serif"; src: url("assets/fonts/instrument-serif-italic.woff2") format("woff2"); font-weight: 400; font-style: italic; }
    @font-face { font-family: "JetBrains Mono"; src: url("assets/fonts/jetbrains-mono.woff2") format("woff2"); font-weight: 100 800; }
    #root { position: absolute; inset: 0; background: #07080a; }
    #card { position: absolute; inset: 0; display: flex; flex-direction: column; align-items: center; justify-content: center; text-align: center; }
    #h { font-family: "Bricolage Grotesque", sans-serif; font-weight: 600; font-size: 132px; line-height: 138px; letter-spacing: -0.035em; color: #ece9e2; }
    #h em { font-family: "Instrument Serif", serif; font-style: italic; font-weight: 400; color: #ff5a3c; font-size: 1.14em; letter-spacing: -0.01em; }
    #sub { margin-top: 54px; font-family: "JetBrains Mono", monospace; font-weight: 500; font-size: 30px; letter-spacing: 0.04em; color: #a8a49b; }
    #btn { margin-top: 56px; background: #ff5a3c; color: #120503; font-family: "Bricolage Grotesque", sans-serif; font-weight: 600; font-size: 36px; padding: 22px 52px; border-radius: 999px; }
  </style>
  <div id="root" data-composition-id="endcard" data-width="1920" data-height="1080">
    <div id="card">
      <div id="h">One stream in.<br/><em>Different</em> audio out.</div>
      <div id="sub">polyemesis.com · open source · self-hosted</div>
      <div id="btn">Install polyemesis</div>
    </div>
  </div>
  <script>
    const tl = gsap.timeline({ paused: true });
    tl.fromTo("#h", { opacity: 0, y: 18 }, { opacity: 1, y: 0, duration: 0.7, ease: "power3.out", immediateRender: false }, 0.35);
    tl.fromTo("#sub", { opacity: 0 }, { opacity: 1, duration: 0.6, ease: "power2.out", immediateRender: false }, 0.9);
    tl.fromTo("#btn", { opacity: 0 }, { opacity: 1, duration: 0.6, ease: "power2.out", immediateRender: false }, 1.2);
    window.__timelines["endcard"] = tl;
  </script>
</template>
"""

def index_html():
    wr, vids, zin = [], [], []
    for i, s in enumerate(ORDER):
        wr.append('<div id="z-%s" class="zw" style="z-index:%d"><video id="v-%s" class="clip console" src="assets/shots/%s.mp4" data-start="%s" data-duration="%s" data-track-index="0" muted playsinline></video></div>'
                  % (s, 10 + i, s, s, START[s], ED[s]))
    zl = []
    for s in ORDER:
        for z in ZOOM[s]:
            zl.append(dict(w="#z-" + s, t=G(s, z["t"]), d=z["d"], A=list(z["A"]), B=list(z["B"]), e=z["e"]))
    host = lambda i, src, st, du, kind, z: '<div id="%s" data-composition-id="%s" data-composition-src="compositions/%s.html" data-start="%s" data-duration="%s" data-track-index="%d" data-track-kind="%s" data-width="1920" data-height="1080" style="z-index:%d"></div>' % (i, i, src, st, du, {"callouts": 1, "captions": 2, "chrome": 3, "endcard": 4}[i], kind, z)
    crop = '<div id="crop-s1a" class="clip crop" data-start="%s" data-duration="%s"></div>' % (START["s1a"], ED["s1a"])
    return """<!doctype html>
<html lang="en">
  <head>
    <meta charset="UTF-8" />
    <meta name="viewport" content="width=1920, height=1080" />
    <script src="https://cdn.jsdelivr.net/npm/gsap@3.15.0/dist/gsap.min.js"></script>
    <style>
      * { margin: 0; padding: 0; box-sizing: border-box; }
      html, body { margin: 0; width: 1920px; height: 1080px; overflow: hidden; background: #000; }
      #root { position: absolute; inset: 0; overflow: hidden; background: #000; }
      .zw { position: absolute; left: 0; top: 0; width: 1920px; height: 1080px; }
      .crop { position: absolute; left: 0; top: 822px; width: 1920px; height: 258px; background: #000; z-index: 20; }
      .console { position: absolute; left: 0; top: 0; width: 1920px; height: 1080px; object-fit: contain; }
    </style>
  </head>
  <body>
    <div id="root" data-composition-id="main" data-start="0" data-duration="%s" data-width="1920" data-height="1080">
      %s
      %s
      %s
      %s
      %s
      %s
    </div>
    <script>
      const ZL = %s;
      const STARTS = %s;
      const BASE = %s;
      const tl = gsap.timeline({ paused: true });
      gsap.set(".zw", { x: 0, y: 0, scale: 1, transformOrigin: "0 0" });
      Object.keys(BASE).forEach((k) => gsap.set("#z-" + k, { scale: BASE[k][0], x: BASE[k][1], y: BASE[k][2] }));
      ZL.forEach((z) =>
        tl.fromTo(z.w, { scale: z.A[0], x: z.A[1], y: z.A[2] },
          { scale: z.B[0], x: z.B[1], y: z.B[2], duration: z.d, ease: z.e, immediateRender: false }, z.t),
      );
      window.__timelines["main"] = tl;
    </script>
  </body>
</html>
""" % (TOTAL, "\n      ".join(wr), crop,
       host("callouts", "callouts", 0, CONTENT_END + 0.3, "graphics", 30),
       host("captions", "captions", 0, CONTENT_END + 0.3, "captions", 31),
       host("chrome", "chrome", 0, CONTENT_END + 0.3, "graphics", 32),
       host("endcard", "endcard", END_START, ENDDUR, "graphics", 40),
       json.dumps(zl), json.dumps(START), json.dumps({k: list(v) for k, v in BASE.items()}))

os.makedirs(HF + "/compositions", exist_ok=True)
open(HF + "/index.html", "w").write(index_html())
open(HF + "/compositions/callouts.html", "w").write(callouts_html())
open(HF + "/compositions/captions.html", "w").write(captions_html())
open(HF + "/compositions/chrome.html", "w").write(chrome_html())
open(HF + "/compositions/endcard.html", "w").write(endcard_html())
chs = [{"title": c[0], "start": START[c[2]]} for c in CH]
json.dump(chs, open("chapters.json", "w"), indent=1, ensure_ascii=False)
print("total", TOTAL, "starts", START, "end", END_START)
