// @ts-check
import { defineConfig } from "astro/config";
import sitemap from "@astrojs/sitemap";
import tailwindcss from "@tailwindcss/vite";

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
  integrations: [sitemap()],
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
