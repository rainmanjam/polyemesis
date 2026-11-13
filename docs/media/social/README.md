# Social preview

`social-preview.png` is the image GitHub shows when a link to this repository is
unfurled on Twitter, Slack, Discord or LinkedIn. Set it under
**Settings → General → Social preview**.

**2560×1280**, which is GitHub's 1280×640 at 2× so it stays sharp when scaled.
It is the website's home link-preview card, centred with a 40px margin on each
side of the 1280×640 frame.

## Regenerating

There is no separate source here any more. The card is drawn by
`web/og/card.html` from the `home` entry in `web/src/data/og.mjs`, the same
template that draws every link-preview image on polyemesis.com.

```bash
cd web
node scripts/build-og.mjs --social
```

That also re-renders `web/public/og/*.png`; with nothing changed the output is
byte-identical.

Re-run it after changing a font file in `web/public/fonts/` or upgrading Playwright
as well: `node scripts/build-og.mjs --check` compares card data and the template only,
so it cannot see either. Uploading the file in GitHub's settings is a manual step.
