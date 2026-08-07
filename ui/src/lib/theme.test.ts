/// <reference types="node" />
import { describe, expect, it } from "vitest";
import { readFileSync, readdirSync, statSync } from "node:fs";
import { join } from "node:path";

/* Colour classes that name a token which was never declared.
 *
 * Tailwind silently drops a utility whose colour does not exist — no build
 * error, no console warning, just an element that inherits its parent. That is
 * invisible in review and invisible in a screenshot unless you already know
 * what colour it was supposed to be.
 *
 * It had already happened twice when this test was written. `text-ok` (no --ok
 * token) marked a *healthy* backup feed, so the good state rendered as plain
 * body text while the bad state was amber — the two were distinguished only by
 * the presence of colour, and the good one was the invisible half. `danger`
 * (no --danger token) coloured the banner warning that a stream is public to
 * the internet, whose own comment reads "the one thing on this page that must
 * never be subtle."
 *
 * Reading index.css rather than the built bundle keeps this a unit test: it
 * needs no build step and fails on the commit that introduces the typo.
 */

/* Read from disk, not through Vite. `import "../index.css?raw"` returns an
 * empty string here — vitest stubs CSS imports by default — which made every
 * assertion below pass against an empty token set. The "declares the signal
 * tokens" case exists to catch exactly that, and did.
 *
 * tsconfig.app.json restricts `types` to vite/client, so the node import is
 * declared per-file above rather than by widening the app's global types:
 * browser code has no business seeing `process` or `fs`. */
const SRC = new URL("../", import.meta.url).pathname;

function declaredColours(): Set<string> {
  const css = readFileSync(join(SRC, "index.css"), "utf8");
  const names = new Set<string>();
  for (const m of css.matchAll(/--color-([a-z0-9-]+)\s*:/g)) names.add(m[1]);
  return names;
}

function tsxFiles(dir: string): string[] {
  return readdirSync(dir).flatMap((entry: string) => {
    const p = join(dir, entry);
    return statSync(p).isDirectory() ? tsxFiles(p) : p.endsWith(".tsx") ? [p] : [];
  });
}

/* Tailwind's own palette. A utility naming one of these is valid without any
 * token, so it is not evidence of a typo — whether raw palette colours *should*
 * be used here is a separate question from whether they render. */
const PALETTE = new Set([
  "inherit", "current", "transparent", "black", "white",
  "slate", "gray", "zinc", "neutral", "stone", "red", "orange", "amber", "yellow",
  "lime", "green", "emerald", "teal", "cyan", "sky", "blue", "indigo", "violet",
  "purple", "fuchsia", "pink", "rose",
]);

/* Non-colour utilities that share these prefixes, excluded by shape rather
 * than by name: sizes (`border-2`, `text-sm`), edges (`border-t`, `divide-y`,
 * `border-b-0`), alignment (`text-left`) and background behaviour
 * (`bg-no-repeat`). An edge utility is the awkward case — `border-t` and
 * `border-teal` differ only in length. */
const NON_COLOUR =
  /^(0|px|auto|none|solid|dashed|dotted|double|hidden|full|left|right|center|justify|start|end|top|bottom|middle|clip|ellipsis|balance|pretty|wrap|nowrap|xs|sm|base|md|lg|xl|\d?xl|inner|collapse|separate|no-repeat|repeat(-[xy])?|contain|cover|fixed|local|scroll|origin|clip-\w+|[tblrxyse](-\d+)?|\d+(\.\d+)?)$/;

const PREFIX = "text|bg|border|ring|fill|stroke|outline|divide|accent|caret|placeholder|shadow|from|via|to";

describe("colour utilities name a declared token", () => {
  const declared = declaredColours();

  it("index.css declares the signal tokens the app is built on", () => {
    // Guards the guard: if the parse silently returned nothing, every
    // assertion below would pass vacuously.
    for (const t of ["live", "warn", "down", "destructive", "primary", "muted"]) {
      expect(declared, `--color-${t} should be declared`).toContain(t);
    }
  });

  it("no utility refers to a token that does not exist", () => {
    const bad: string[] = [];

    for (const file of tsxFiles(SRC)) {
      const src = readFileSync(file, "utf8");
      // Only inside className/class attributes — prose in comments and UI copy
      // produces matches like `text-splitting` otherwise.
      for (const attr of src.matchAll(/class(?:Name)?=(?:"([^"]*)"|\{`([^`]*)`\}|\{"([^"]*)"\})/g)) {
        const chunk = attr[1] ?? attr[2] ?? attr[3] ?? "";
        const line = src.slice(0, attr.index).split("\n").length;

        for (const u of chunk.matchAll(
          new RegExp(`(?<![\\w-])(${PREFIX})-([a-z][a-z0-9-]*)(?:/\\d+)?(?![\\w-])`, "g"),
        )) {
          const name = u[2];
          if (NON_COLOUR.test(name)) continue;
          if (PALETTE.has(name.split("-")[0])) continue;
          // `ring-offset-surface` names its colour after the second segment.
          const candidates = [name, name.replace(/^offset-/, "")];
          if (candidates.some((c) => declared.has(c))) continue;
          bad.push(`${file.slice(SRC.length)}:${line}  ${u[0]}`);
        }
      }
    }

    expect(bad, `these name no --color-* token, so Tailwind emits nothing:\n${bad.join("\n")}`)
      .toEqual([]);
  });
});
