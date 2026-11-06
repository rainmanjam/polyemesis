// `streamdeck pack`, then put manifest.json back byte for byte. The CLI
// rewrites the manifest it packs -- today it drops the trailing newline, and
// with --version it stamps the version -- so a plain `npm run pack` left a
// clean checkout dirty, and `make check` with it. The packed file keeps the
// CLI's version of the manifest; the working tree keeps the committed one.
import { readFileSync, writeFileSync } from "node:fs";
import { spawnSync } from "node:child_process";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const root = join(dirname(fileURLToPath(import.meta.url)), "..");
const manifest = join(root, "com.polyemesis.streamdeck.sdPlugin", "manifest.json");
const before = readFileSync(manifest);

// Run the CLI's own entry point with this Node, never through a shell: the
// .bin shim is a .cmd on Windows, which needs one, and a shell would also
// interpret the arguments passed through from the command line.
const cliPkg = join(root, "node_modules", "@elgato", "cli", "package.json");
const bin = join(dirname(cliPkg), JSON.parse(readFileSync(cliPkg, "utf8")).bin.streamdeck);
const r = spawnSync(
  process.execPath,
  [bin, "pack", "com.polyemesis.streamdeck.sdPlugin", "--output", "dist", "--force", "--no-update-check", ...process.argv.slice(2)],
  { cwd: root, stdio: "inherit" },
);

if (!readFileSync(manifest).equals(before)) writeFileSync(manifest, before);
if (r.error) console.error("pack: could not run " + bin + ": " + r.error.message);
process.exit(r.status ?? 1);
