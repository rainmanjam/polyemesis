# SEO changes and the docs-rendering scope

Measured against the tree on 2026-08-15. Every number here was counted, not
estimated.

DataForSEO now runs (the registered command pointed at
`dataforseo-universal-cli-tool`, which does not exist on npm; the real package is
`dataforseo-mcp-server`). Part 0 below is measured from it.

## Part 0 — what people actually search, measured

Google Ads volume, US, August 2026. Every figure below was pulled, not estimated.
Note the API caps `search_volume` at **10 keywords per call** — three batches.

| Keyword | Monthly | Comp | CPC |
|---|---|---|---|
| restream / restreamer | **60,500** | MEDIUM | $2.00 |
| multistream / multistreaming | **3,600** | LOW | $5.55 |
| obs multi rtmp | **590** | LOW | $7.43 |
| restreamer alternative | **210** | MEDIUM | $7.43 |
| obs audio tracks | 140 | LOW | — |
| stream to twitch and youtube at the same time | 140 | LOW | $5.70 |
| twitch vod track | 110 | LOW | — |
| srt server | 70 | LOW | **$14.49** |
| obs studio multiple streams | 50 | LOW | $4.52 |
| obs vod track | 30 | LOW | — |
| multistream software | 20 | LOW | $4.52 |
| obs multitrack audio · restream.io alternative · rtmp relay server · multistream to youtube and twitch | 10 each | LOW/MED | — |

### THE HYPOTHESIS WAS WRONG, AND IT WAS MINE

I argued — and an independent audit agreed — that this product's buyer searches
the **problem**, not the product's vocabulary, and that the site was losing
traffic by leading with insider phrasing. Every problem-language phrase we
proposed returns **no measurable volume at all**:

> twitch vod muted music · obs different audio per platform · stream different
> audio to youtube and twitch · per destination audio routing · self hosted
> restreaming server · self hosted multistreaming · srt multitrack audio · obs
> separate audio tracks stream · open source restreaming · obs enhanced
> broadcasting · obs srt multitrack · obs audio tracks per destination

Below Google's reporting threshold, every one. The reasoning was plausible and
the conclusion was false, which is exactly why this needed measuring rather than
arguing. **Do not rewrite the H1s toward those phrases.** The volume is
elsewhere.

### Where it actually is, and who holds it

Live SERP, top 10, US. **polyemesis appears nowhere on any of the three.**

**`multistreaming` — 3,600/mo, LOW competition.** The page-one result set is
entirely hosted SaaS: Streamlabs, Restream, StreamYard, OneStream, Riverside,
plus a Reddit thread and a listicle. **Not one self-hosted option ranks.** Low
competition on a 3,600/mo head term with no incumbent matching this product's
category is the single best opening on the board.

**`obs multi rtmp` — 590/mo, LOW, $7.43 CPC.** Page one is the plugin's own
forum post, Reddit, an AUR package and YouTube tutorials — no comparison
content. `/comparison` already argues precisely this case ("They select a track.
polyemesis mixes.") and does not rank.

**`restreamer alternative` — 210/mo, MEDIUM, $7.43 CPC.** The **#1 organic
result is a Reddit thread titled "Is there any self-hosted alternative to
Restream?"** — that is this product's pitch, verbatim, as the top result for a
commercial-intent query, and polyemesis is absent from the page.

`srt server` is worth noting separately: only 70/mo, but **$14.49 CPC** — by far
the highest commercial intent measured here, and a term this product legitimately
owns technically.

### What this changes

The structural findings in Parts 1–3 are unaffected — they were verified against
the repo, not inferred from search data. What changes is priority: the H1 rewrite
drops, and comparison/alternative content rises, because that is where measurable
demand exists and where the competition is weakest.

## Part 1 — files and metadata

| # | Change | Impact | Effort | Notes |
|---|---|---|---|---|
| 1 | Render `docs/*.md` as HTML pages | **Highest** | Large | See Part 2. 23 pages, 63,273 words currently invisible to search |
| 2 | `llms.txt` at site root | Medium-high | XS | Plain markdown précis. The audience increasingly finds infra tools by asking a model |
| 3 | `/.well-known/security.txt` | Medium-high | XS | RFC 9116. You publish GHSA advisories; the domain offers no reporting route. **Mandatory `Expires:`** — needs a CI check, a stale one is worse than absence |
| 4 | `lastmod` in sitemap | Medium | XS | Astro sitemap emits `<loc>` only. No freshness signal on any URL |
| 5 | `apple-touch-icon.png` | Low-med | XS | Only `favicon.svg` exists; iOS home screen gets a blank tile |
| 6 | `humans.txt`, `ads.txt`, `manifest.webmanifest`, `opensearch.xml`, `browserconfig.xml` | **None** | — | Rejected. Vanity, N/A, or dead standards |

Already correct and not to be touched: `robots.txt`, auto sitemap, canonical,
OG + Twitter card with a 2560×1280 image, JSON-LD `SoftwareApplication`,
`theme-color`, self-hosted subset fonts, `_headers`, flat-file URLs.

Verified good: all 6 built pages have exactly **one `<h1>`**, image `alt` text
and explicit dimensions are present throughout, and there is no render-blocking
third-party script.

**Correction to an earlier reading of mine.** I first recorded titles at 54–69
chars and descriptions at 130–166 as "within range on both". That was wrong at
the top of each range: Google truncates titles around 55–60 characters and
descriptions around 155, so `/features` (~70 char title, 167 char description)
and `/docs` (~70 char title) are cut off in the SERP today. Counted, not
estimated — see Part 3.

## Part 3 — on-page, from an independent audit

| # | Change | Impact | Where |
|---|---|---|---|
| 7 | `/features` and `/docs` titles exceed the truncation point; `/features` description is 167 chars | **High** | `features.astro:114-115`, `docs.astro:57` |
| 8 | `_headers` `no-cache` misses 4 of 6 pages | **High** | `public/_headers:41-44` — see below |
| 9 | H1s carry no problem language | Medium | `features.astro:120`, `comparison.astro:127`, `download.astro:15`, `docs.astro:63` |
| 10 | `HowTo` JSON-LD on the install steps | Medium | `download.astro:122-147` — a real `<ol>` of 4 sequential steps |
| 11 | Generic internal anchor text | Low-med | `index.astro:271` "The full list →", `comparison.astro:322` / `docs.astro:140` "Install it" |
| 12 | 404 has a canonical but no `noindex` | Low | `404.astro` — risks soft-404 indexing |
| 13 | `SoftwareApplication` JSON-LD also injected on 404 | Low | `Base.astro:56-76` |

Assessed and **rejected**: `FAQPage` (index.astro:316-339 is "Built for" /
"Not built for" bullet lists, not Q&A pairs), `VideoObject` (no `<video>` on the
site), `BreadcrumbList` (flat structure, no breadcrumb UI — becomes applicable
only *after* docs rendering lands).

### #8 in detail, because it is a live bug

`public/_headers` scopes revalidation to two patterns:

```
/
  Cache-Control: no-cache
/*.html
  Cache-Control: no-cache
```

Its own comment says this is "scoped to the built pages rather than `/*`, so it
cannot leak onto a hashed asset". But `astro.config.mjs` sets
`trailingSlash: "never"` with `build.format: "file"`, so the served URLs are
`/features`, `/comparison`, `/docs`, `/download` — none of which end in `.html`,
and none of which are `/`. **Four of the six pages match neither rule and never
get `no-cache`.** The stated failure mode of that block — "a deploy is invisible
until caches expire" — is exactly what happens on those four today.

The fix must keep the block's original intent: revalidate HTML, never touch
`/_astro/*`. Adding the extensionless paths explicitly does that; broadening to
`/*` does not, and the comment says why.

### #9, with a caveat the audit did not have

The four H1s flagged are editorial — "The audio is the difference.",
"One binary, or one container." The audit proposed replacing them with keyword
strings like *"Multistream Audio Routing & Split Track Features"*. **Do not do
that.** `docs/COPY-CONSTRAINTS.md` exists precisely to govern what this copy may
claim and how it reads, and that suggestion is worse writing sold as SEO.

The real finding underneath is sound though: an H1 can carry the problem *and*
the voice. `features.astro:36` already proves it — "The broadcast keeps the
music. The archive does not." is the Twitch-VOD-mute problem stated in the
site's own register. That is the bar. Rewrite the four toward it; do not
keyword-stuff them.

## Part 2 — docs rendering

### What exists

| | Count | Words |
|---|---|---|
| Publishable, user-facing | **23** | **63,273** |
| Internal — must NOT publish | **14** | — |
| Cross-doc `.md` links to rewrite | **110** | — |
| Code fences in publishable docs | **228** | — |

Publishable: INSTALL, QUICKSTART, OBS, AUDIO-ROUTING, RENDITIONS, PLATFORMS,
TLS, TROUBLESHOOTING, FAQ, CONFIGURATION, HARDWARE, ENCODING, MONITORING, MQTT,
HOOKS, API, UPGRADING, SCHEDULED-BROADCAST, TESTING, ARCHITECTURE, DEPENDENCIES,
HOT-RELOAD, COMPARISON.

Excluded, and this list is load-bearing: SITE-IMPROVEMENTS, SITE-DEPLOY,
REVIEW-POKA-YOKE, **RESEARCH-COMPETITIVE**, TEST-STRATEGY,
WEBSITE-COPY-PROPOSAL, **COPY-CONSTRAINTS**, MODULES, DESIGN-SYSTEM,
DESIGN-DESTINATION-HEALTH, DESIGN-ONE-PORT-ONLY, DESIGN-ONE-PORT-INGEST,
CHANGES-SINCE-v0.6.0, README.

`RESEARCH-COMPETITIVE.md` and `COPY-CONSTRAINTS.md` are the two that matter:
one is competitor analysis, the other records what the marketing copy may and
may not claim. Publishing either by accident is a self-inflicted wound. **The
publish list must be an allowlist, never a glob with exclusions** — a glob means
the next internal note added to `docs/` is public by default.

### The blocker nobody would predict

`web/scripts/check-build.mjs:127` fails the build on any hand-styled `<pre>`:

```js
const handRolled = [...html.matchAll(/<pre\s+class="[^"]+"/g)];
```

Astro renders markdown code fences through Shiki, which emits
`<pre class="astro-code" style="...">`. Confirmed by running the guard's own
regex against that string: **it matches.** So the first docs page with a code
fence fails the build, and there are **228 fences**.

This is the guard working correctly — it exists because three different `<pre>`
treatments once shipped across three pages. The fix is to route rendered fences
through the `CodeBlock` component (a Shiki transformer or a rehype pass), not to
weaken the check. Budget this properly; it is the single largest unknown in the
work.

### Other real work, in order of surprise

1. **No doc has frontmatter.** Every one starts at `# Title`. 23 unique titles
   and 23 unique meta descriptions have to be written. That is copywriting, not
   plumbing, and it is where the SEO value actually is — a generated description
   is worth roughly nothing.
2. **`COMPARISON.md` collides with `/comparison`.** The site already has a
   comparison page. Publishing the doc at `/docs/comparison` puts two pages on
   one intent, competing with each other. Decide: canonical one to the other, or
   do not publish the doc.
3. **110 cross-doc links** are `](AUDIO-ROUTING.md)` style. Each needs rewriting
   to a site URL, and a link that silently 404s is worse than the GitHub link it
   replaced. This needs a build-time check that every rewritten target resolves —
   the same shape as the existing internal-link check at `check-build.mjs:206`.
4. **Sitemap 5 → 28 URLs.** Fine, but the `_headers` guard
   (`TestThePagesHeadersRestateEverySecurityHeaderTheNginxConfigDeclares`)
   requires any new path prefix needing headers to be stated in both
   `public/_headers` and `nginx-security-headers.conf`.
5. **Navigation.** 23 pages need a docs sidebar and breadcrumbs; `Nav.astro`
   currently carries 5 top-level links. `BreadcrumbList` JSON-LD becomes
   genuinely applicable at that point — it is not today.

### Why this is worth doing

The current `/docs` page contains three headings and a link to GitHub Issues.
Every search for *"OBS multitrack SRT"*, *"different audio mix per platform"*,
*"Twitch VOD muted music"* that this project could win currently lands on
`github.com` — so the authority accrues to Microsoft's domain, not to
polyemesis.com, and the pages have no title, description, or internal linking
under your control.

### Recommended sequence

1. Files 2–5 above. Hours, no risk, independent of everything else.
2. Resolve the `<pre>` guard on **one** doc page end to end. Until that is
   solved, the other 22 cannot ship.
3. OBS, AUDIO-ROUTING, TROUBLESHOOTING, INSTALL — the four highest-intent pages,
   with hand-written titles and descriptions. Measure.
4. The remaining 19 once the pattern is proven.
