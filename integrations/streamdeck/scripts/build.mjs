// Bundles src/plugin.ts into the plugin folder. Everything the plugin imports
// is inlined, so the shipped folder has no node_modules and no install step.
// `ws` may load two optional native speedups; they are left external and ws
// falls back to pure JavaScript without them.
import { writeFileSync } from "node:fs";
import { build } from "esbuild";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { inspectorCatalogSource } from "./inspectorCatalog.mjs";

const root = join(dirname(fileURLToPath(import.meta.url)), "..");

await build({
  entryPoints: [join(root, "src", "plugin.ts")],
  outfile: join(root, "com.polyemesis.streamdeck.sdPlugin", "bin", "plugin.js"),
  bundle: true,
  platform: "node",
  format: "esm",
  target: "node20",
  sourcemap: false,
  external: ["bufferutil", "utf-8-validate"],
  banner: {
    js: 'import { createRequire as __createRequire } from "node:module"; const require = __createRequire(import.meta.url);',
  },
  logLevel: "warning",
});
writeFileSync(join(root, "com.polyemesis.streamdeck.sdPlugin", "ui", "strings.js"), inspectorCatalogSource());
console.log("built com.polyemesis.streamdeck.sdPlugin/bin/plugin.js and ui/strings.js");
