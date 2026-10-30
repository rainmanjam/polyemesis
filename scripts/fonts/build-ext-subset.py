#!/usr/bin/env python3
# Builds ui/public/fonts/*-ext.woff2: the codepoints the shipped basic-Latin
# subsets lack (Latin Extended-A/B and Additional, combining marks, Cyrillic,
# arrows, a few currency signs), so ru/uk/pl/tr/... render in the product
# faces instead of falling through glyph by glyph to a system font (B3).
#
#   pip install fonttools brotli
#   python3 scripts/fonts/build-ext-subset.py <upstream-variable.ttf> \
#       ui/public/fonts/ibm-plex-sans.woff2 ui/public/fonts/ibm-plex-sans-ext.woff2 100 700
#
# Upstream sources (OFL): google/fonts ofl/ibmplexsans/IBMPlexSans[wdth,wght].ttf
# and ofl/jetbrainsmono/JetBrainsMono[wght].ttf. Prints the size and the
# unicode-range the file actually covers.
import sys, os
from fontTools.ttLib import TTFont
from fontTools import subset
from fontTools.varLib import instancer
src, base, out, wmin, wmax = sys.argv[1], sys.argv[2], sys.argv[3], float(sys.argv[4]), float(sys.argv[5])
have = set(TTFont(base).getBestCmap())
want = set()
for lo, hi in [(0x0100,0x024F),(0x02B0,0x02FF),(0x0300,0x036F),(0x0400,0x04FF),(0x0500,0x052F),(0x1E00,0x1EFF),(0x2116,0x2116),(0x20B4,0x20B4),(0x20BD,0x20BD),(0x20BA,0x20BA),(0x2190,0x2199),(0x2318,0x2318)]:
    want.update(range(lo, hi+1))
f = TTFont(src, lazy=False)
axes = {a.axisTag for a in f['fvar'].axes}
limits = {"wght": (wmin, wmax)}
if "wdth" in axes: limits["wdth"] = 100
full = {a.axisTag: (a.minValue, a.maxValue) for a in f['fvar'].axes}
if any(full.get(k) != (v if isinstance(v, tuple) else None) for k, v in limits.items()):
    f = instancer.instantiateVariableFont(f, limits)
if "gvar" in f:
    # Some upstream builds omit gvar data for glyphs without deltas; the
    # subsetter then trips on a missing key. Spell "no deltas" out.
    gv = f["gvar"]
    full_vars = {}
    for g in f.getGlyphOrder():
        try:
            full_vars[g] = gv.variations[g]
        except KeyError:
            full_vars[g] = []
    gv.variations = full_vars
cmap = set(f.getBestCmap())
cps = sorted((want & cmap) - have)
opts = subset.Options(); opts.flavor = "woff2"; opts.layout_features = ["*"]; opts.name_IDs = ["*"]; opts.notdef_outline = True
sub = subset.Subsetter(opts); sub.populate(unicodes=cps); sub.subset(f)
f.flavor = "woff2"; f.save(out)
# unicode-range string, collapsed into runs
runs=[]; 
for c in cps:
    if runs and c == runs[-1][1]+1: runs[-1][1]=c
    else: runs.append([c,c])
print(os.path.getsize(out), "bytes,", len(cps), "codepoints")
print(", ".join(f"U+{a:04X}" if a==b else f"U+{a:04X}-{b:04X}" for a,b in runs))
