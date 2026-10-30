// Writes the plugin's placeholder PNG icons. These are flat colour tiles, not
// finished artwork: they exist so the manifest's image references resolve and
// the key states are distinguishable. Replace them with designed art before a
// Marketplace submission. Run: node scripts/icons.mjs
import { writeFileSync, mkdirSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { deflateSync } from "node:zlib";

const ROOT = join(dirname(fileURLToPath(import.meta.url)), "..", "com.polyemesis.streamdeck.sdPlugin", "imgs");

const CRC_TABLE = Array.from({ length: 256 }, (_, n) => {
  let c = n;
  for (let k = 0; k < 8; k++) c = c & 1 ? 0xedb88320 ^ (c >>> 1) : c >>> 1;
  return c >>> 0;
});
function crc32(buf) {
  let c = 0xffffffff;
  for (const b of buf) c = CRC_TABLE[(c ^ b) & 0xff] ^ (c >>> 8);
  return (c ^ 0xffffffff) >>> 0;
}
function chunk(type, data) {
  const len = Buffer.alloc(4);
  len.writeUInt32BE(data.length);
  const body = Buffer.concat([Buffer.from(type, "ascii"), data]);
  const crc = Buffer.alloc(4);
  crc.writeUInt32BE(crc32(body));
  return Buffer.concat([len, body, crc]);
}

/** A solid-colour RGBA PNG. alpha 0 gives a transparent tile. */
function png(size, [r, g, b, a = 255]) {
  const row = Buffer.alloc(1 + size * 4);
  for (let x = 0; x < size; x++) row.set([r, g, b, a], 1 + x * 4);
  const raw = Buffer.concat(Array.from({ length: size }, () => row));
  const ihdr = Buffer.alloc(13);
  ihdr.writeUInt32BE(size, 0);
  ihdr.writeUInt32BE(size, 4);
  ihdr.set([8, 6, 0, 0, 0], 8);
  return Buffer.concat([
    Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]),
    chunk("IHDR", ihdr),
    chunk("IDAT", deflateSync(raw)),
    chunk("IEND", Buffer.alloc(0)),
  ]);
}

function write(rel, size, color) {
  const file = join(ROOT, rel);
  mkdirSync(dirname(file), { recursive: true });
  writeFileSync(file, png(size, color));
}

// Key states, in the order KEY_STATE in src/render.ts defines them.
const KEY_COLOURS = {
  idle: [58, 63, 71],
  live: [31, 157, 85],
  warn: [214, 158, 46],
  error: [197, 48, 48],
  held: [49, 130, 206],
  unbound: [45, 55, 72],
};
for (const [name, colour] of Object.entries(KEY_COLOURS)) {
  write(`keys/${name}.png`, 72, colour);
  write(`keys/${name}@2x.png`, 144, colour);
}

const WHITE = [255, 255, 255];
for (const name of ["destination", "go-live", "stop-all", "failover", "meter"]) {
  write(`actions/${name}.png`, 20, WHITE);
  write(`actions/${name}@2x.png`, 40, WHITE);
}
write("actions/meter.png", 20, WHITE);
write("plugin-icon.png", 256, [14, 116, 144]);
write("plugin-icon@2x.png", 512, [14, 116, 144]);
write("category-icon.png", 28, WHITE);
write("category-icon@2x.png", 56, WHITE);
console.log("wrote placeholder icons under", ROOT);
