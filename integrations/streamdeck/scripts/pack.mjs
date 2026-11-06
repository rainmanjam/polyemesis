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

const bin = join(root, "node_modules", ".bin", "streamdeck");
const r = spawnSync(
  bin,
  ["pack", "com.polyemesis.streamdeck.sdPlugin", "--output", "dist", "--force", "--no-update-check", ...process.argv.slice(2)],
  { cwd: root, stdio: "inherit", shell: process.platform === "win32" },
);

if (!readFileSync(manifest).equals(before)) writeFileSync(manifest, before);
if (r.error) console.error("pack: could not run " + bin + ": " + r.error.message);
process.exit(r.status ?? 1);
