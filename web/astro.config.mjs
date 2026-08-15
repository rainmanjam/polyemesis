// @ts-check
import { execFileSync } from "node:child_process";
import { defineConfig } from "astro/config";
import sitemap from "@astrojs/sitemap";
import tailwindcss from "@tailwindcss/vite";

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
/** @type {Record<string, string>} */
const pageSource = {
  "/": "src/pages/index.astro",
  "/features": "src/pages/features.astro",
  "/comparison": "src/pages/comparison.astro",
  "/docs": "src/pages/docs.astro",
  "/download": "src/pages/download.astro",
};

/** @param {string} file @returns {string|undefined} */
function lastCommitISO(file) {
  try {
    const out = execFileSync("git", ["log", "-1", "--format=%cI", "--", file], {
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
  integrations: [
    sitemap({
      serialize(item) {
        const path = new URL(item.url).pathname.replace(/\/$/, "") || "/";
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
