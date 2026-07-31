# Social preview

`social-preview.png` — the image GitHub shows when a link to this repository is
unfurled on Twitter, Slack, Discord or LinkedIn. Set it under
**Settings → General → Social preview**.

**2560×1280**, which is GitHub's 1280×640 at 2× so it stays sharp when scaled.
Content sits inside a 56px margin — GitHub's template asks for 40pt, and the
extra room keeps the headline out of the square crop some clients apply.

## Regenerating

`card.html` is the source. It is HTML rather than a design file so the palette
can be **copied verbatim from `ui/src/index.css`** rather than eyeballed —
DESIGN-SYSTEM.md's whole argument is that `:root` is the system and every
consumer reads the same block, and a social card that drifts from the product's
own colours is the first place that promise breaks.

```bash
cd ui
cp ../docs/media/social/card.html .
node - <<'JS'
import { chromium } from "playwright";
const b = await chromium.launch();
const p = await b.newPage({ viewport: { width: 1280, height: 640 }, deviceScaleFactor: 2 });
await p.goto("file://" + process.cwd() + "/card.html");
await p.evaluate(() => document.fonts.ready);
await p.waitForTimeout(400);
await p.screenshot({ path: "../docs/media/social/social-preview.png" });
await b.close();
JS
rm card.html
```

The meter bars use fixed heights rather than random ones, so re-rendering
produces a byte-comparable image. A preview that changes on every build is one
nobody can review.

## What it shows, and why

Three destinations taking three different subsets of the same three tracks, each
with its own post-routing level. That is the product's entire argument in one
frame, and it is the one thing a competitor's card cannot show honestly —
neither Restreamer nor restream.io routes audio per destination.

DESIGN-SYSTEM.md puts it directly: *"The hero should be the meter. It is the one
thing no competitor's landing page can show honestly."*
