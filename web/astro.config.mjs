// @ts-check
import { execFileSync } from "node:child_process";
import { existsSync } from "node:fs";
import { defineConfig } from "astro/config";
import sitemap from "@astrojs/sitemap";
import tailwindcss from "@tailwindcss/vite";
import { satteri } from "@astrojs/markdown-satteri";
import { PUBLISHED } from "./src/data/docs.mjs";
import { mdastDocLinks } from "./src/lib/mdast-doc-links.mjs";
import { hastCodeBlock } from "./src/lib/hast-code-block.mjs";
import { hastDocTables } from "./src/lib/hast-doc-tables.mjs";

/* lastmod, taken from git rather than from the clock.
 *
 * The tempting implementation is `new Date()` at build time, which would stamp
 * every page as freshly modified on every deploy. That is a lie told to a
 * crawler about content that did not change, and search engines discount a feed
 * whose lastmod always equals its fetch date -- so it costs the signal it was
 * meant to provide.
 *
 * The last commit that touched a page's source is the real answer. A page whose
 * copy has not changed in six months says so, which is what makes the date on a
 * page that DID change worth anything.
 *
 * Falls back to omitting lastmod rather than guessing: no date at all is how the
 * feed behaved before this, and is honest. A shallow CI clone or a file added
 * but not yet committed both land here. */
/* A DOCS PAGE'S SOURCE IS THE MARKDOWN, NOT THE ROUTE. All 23 render through
 * one src/pages/docs/[slug].astro, so keying them to that file would stamp
 * every document with the date somebody last touched the template -- 23 URLs
 * changing lastmod in unison for a change none of them contains. The markdown
 * file is the thing whose content the date is a claim about. */
/** @type {Record<string, string>} */
const pageSource = {
  "/": "src/pages/index.astro",
  "/features": "src/pages/features.astro",
  "/comparison": "src/pages/comparison.astro",
  "/docs": "src/pages/docs.astro",
  "/download": "src/pages/download.astro",
  "/free-restream-service": "src/pages/free-restream-service.astro",
  "/how-to-multistream-from-obs": "src/pages/how-to-multistream-from-obs.astro",
  "/vs/aitum-multistream": "src/pages/vs/aitum-multistream.astro",
  "/vs/obs-multi-rtmp": "src/pages/vs/obs-multi-rtmp.astro",
  "/vs/restreamer": "src/pages/vs/restreamer.astro",
  "/vs/streamelements": "src/pages/vs/streamelements.astro",
  ...Object.fromEntries(PUBLISHED.map((d) => [`/docs/${d.slug}`, `../docs/${d.file}`])),
};

/* Paths that declare a canonical somewhere else, and therefore must not be
 * submitted in the sitemap.
 *
 * A sitemap entry says "index this URL"; a canonical on the page says "index
 * that one instead". A crawler handed both contradicts itself out of the
 * conflict by trusting neither, which costs the signal the canonical was there
 * to give. Derived from the manifest so that the two can only disagree if
 * somebody edits the manifest, where the reasoning is written down. */
const canonicalisedElsewhere = new Set(
  PUBLISHED.filter((d) => d.canonical && d.canonical !== `/docs/${d.slug}`).map((d) => `/docs/${d.slug}`),
);

/* An ABSOLUTE path to git, chosen from a fixed list.
 *
 * SonarCloud javascript:S4036 failed this file twice. The first attempt pinned
 * `env.PATH` to /usr/bin:/bin, which the rule's own documentation offers as a
 * remediation -- and the analyser flagged it again anyway, because it cannot see
 * that a variable holds a safe value. The rule wants a literal absolute path,
 * and arguing with a static analyser about intent is a fight that costs more
 * than it returns.
 *
 * It is also, on reflection, the better fix. Pinning PATH still leaves the
 * lookup to the OS; naming the binary removes the lookup. Both candidates are
 * directories no unprivileged process can write to: /usr/bin is git on Linux and
 * on macOS via the Xcode shim, /opt/homebrew/bin covers an Apple-silicon
 * developer box where /usr/bin/git may be absent.
 *
 * Resolved once at config load rather than per page -- five existsSync calls to
 * answer the same question was the shape of the first draft.
 *
 * No candidate found means no lastmod, which is the documented degradation: the
 * feed omits the field rather than inventing a date. */
const GIT = ["/usr/bin/git", "/opt/homebrew/bin/git"].find((p) => existsSync(p));

/** @param {string} file @returns {string|undefined} */
function lastCommitISO(file) {
  if (!GIT) return undefined;
  try {
    const out = execFileSync(GIT, ["log", "-1", "--format=%cI", "--", file], {
      cwd: new URL(".", import.meta.url).pathname,
      encoding: "utf8",
      stdio: ["ignore", "pipe", "ignore"],
    }).trim();
    return out || undefined;
  } catch {
    return undefined;
  }
}


/* Static output on purpose. The site is marketing copy and screenshots — there
 * is nothing to render per-request, so it ships as files behind nginx and Cloud
 * Run scales it to zero between visitors. Adding a Node runtime would mean a
 * container to patch and a cold start to pay, for no capability. */
export default defineConfig({
  site: "https://polyemesis.com",
  output: "static",
  // Flat files rather than directories: /features.html, not /features/index.html.
  // With directory output nginx answers /features with a 301 to /features/,
  // which costs every internal navigation an extra round trip for nothing.
  build: { format: "file" },
  trailingSlash: "never",
  /* SHIKI IS OFF, and this is the resolution of the one blocker in this change.
   *
   * Astro renders a fence through Shiki, which emits `<pre class="astro-code"
   * style="...">`. check-build.mjs fails the build on any `<pre class="...">`,
   * for a reason its comment states: three treatments of the same content once
   * shipped across three pages of one site. There are 114 fenced blocks in the
   * published documents, so the first docs page would have failed.
   *
   * The alternative was a Shiki transformer deleting the class attribute. That
   * passes the check and defeats it: Shiki's own background and token colours
   * would still be on the page, and nothing else on this site has either, so the
   * docs would have become the fourth treatment -- arrived at by silencing the
   * alarm. Off, and lib/rehype-code-block.mjs puts the plain <pre> inside the
   * CodeBlock component's own markup instead, so a rendered fence and a
   * hand-written one are the same object. */
  markdown: {
    syntaxHighlight: false,
    /* Sätteri is Astro 7's default Markdown processor, and `processor` is the
       only place its plugins can be registered -- `remarkPlugins` and
       `rehypePlugins` still exist but switch the whole pipeline back to unified
       and need @astrojs/markdown-remark installed alongside. Three small
       visitors is not a reason to run a second Markdown processor.

       IF YOU EDIT ONE OF THESE PLUGINS AND THE OUTPUT DOES NOT CHANGE, delete
       `node_modules/.astro`. The content layer caches rendered markdown keyed on
       the SOURCE FILE, not on the pipeline that rendered it, so a plugin fix
       against unchanged documents is served from the store and the build reports
       success. Cost an hour once: a corrected attribute kept appearing in dist/
       in its broken spelling across three clean `rm -rf dist` builds. CI starts
       cold and never sees it. */
    processor: satteri({
      mdastPlugins: [mdastDocLinks],
      hastPlugins: [hastCodeBlock, hastDocTables],
    }),
  },
  integrations: [
    sitemap({
      serialize(item) {
        const path = new URL(item.url).pathname.replace(/\/$/, "") || "/";
        if (canonicalisedElsewhere.has(path)) return undefined;
        const src = pageSource[path];
        const iso = src && lastCommitISO(src);
        if (iso) item.lastmod = iso;
        return item;
      },
    }),
  ],
  vite: {
    // The cast is a version skew, not a real incompatibility: @tailwindcss/vite
    // is built against a different Vite minor than the one Astro bundles, so
    // their PluginContext types do not structurally match even though the
    // plugin runs correctly. Casting here keeps `astro check` in the build
    // command — dropping the type check to silence one known-benign mismatch
    // would cost far more than it saves.
    plugins: [/** @type {any} */ (tailwindcss())],
  },
});
