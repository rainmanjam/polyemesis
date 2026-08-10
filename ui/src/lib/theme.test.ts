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

/* ------------------------------------------------------- motion as a signal */

/** The three properties that make motion honest in this application.
 *
 *  Each one has already failed once, here or on the website:
 *   - a pulse applied to the indicator itself, so the urgent state was the one
 *     that periodically disappeared;
 *   - motion tokens declared and never wired, so the reduced-motion override
 *     that set them to 0ms did nothing at all;
 *   - a reduced-motion path that removed an animation and, with it, the only
 *     thing the animation was communicating.
 */
describe("motion is a signal, not decoration", () => {
  const statusDot = readFileSync(join(SRC, "components/signature/StatusDot.tsx"), "utf8");
  const css = readFileSync(join(SRC, "index.css"), "utf8");

  it("pulses the halo, never the dot itself", () => {
    // The core is the last <span>; it must carry no animate-* class. A pulse on
    // the core takes opacity to 0.35 at its midpoint, which is the indicator
    // half-vanishing on the state that most needs to be seen.
    const core = statusDot.slice(statusDot.lastIndexOf('"relative inline-flex h-full w-full'));
    expect(core).not.toMatch(/animate-signal/);
    // And both animated tones must go through one shared branch, so the
    // asymmetry that caused this cannot return by editing a single tone.
    expect(statusDot).toMatch(/const pulse\s*=/);
  });

  it("wires the motion tokens to Tailwind's default", () => {
    // Declared-but-unregistered is the state this was in: 27 transition-colors
    // using Tailwind's 150ms while --motion-instant sat unread, which also made
    // the reduced-motion override decorative.
    expect(css).toMatch(/--default-transition-duration:\s*var\(--motion-instant\)/);
    expect(css).toMatch(/--default-transition-timing-function:\s*var\(--ease-out\)/);
  });

  it("keeps the default a var(), so reduced motion can still override it", () => {
    // A literal here would be baked in at build time and the 0ms override would
    // stop working — the same bug, one level down.
    const decl = css.match(/--default-transition-duration:\s*([^;]+);/)?.[1] ?? "";
    expect(decl.trim()).toMatch(/^var\(/);
  });

  it("replaces the reconnecting blink rather than deleting it", () => {
    const reduced = css.slice(css.indexOf("@media (prefers-reduced-motion: reduce)"));
    // Suppressing the animation is fine; suppressing what it MEANT is not.
    expect(reduced).toMatch(/\.animate-signal-fast\s*\{[^}]*box-shadow/);
  });
});

/* ------------------------------------------------- the palette stays semantic */

/** No raw Tailwind signal colours in application code.
 *
 *  The app encodes state in five tokens — live, warn, down, armed, idle. A raw
 *  `amber-500` or `red-500` beside them is a second vocabulary for the same
 *  idea: `red-500` is ΔE 8.11 from `--down`, which is to say indistinguishable,
 *  so a reader cannot tell whether a red thing is a failed destination or just
 *  a red thing. Twenty-one such uses had accumulated.
 *
 *  The existing guard above cannot see this: it checks that a colour class
 *  names a DECLARED token, and Tailwind's own palette is declared, so every one
 *  of them passed.
 */
describe("state colour comes from tokens, not the Tailwind palette", () => {
  /** The public viewer page, deliberately.
   *
   *  Its red LIVE badge follows the convention every viewer already knows from
   *  YouTube and Twitch. That page is not the operator console and carries no
   *  destination state, so the collision with `--down` cannot mislead anyone
   *  reading it. Exempted by name rather than by pattern, so adding a second
   *  exemption has to be an explicit decision. */
  const EXEMPT = new Set(["pages/PublicPlayer.tsx"]);
  const SIGNAL = /\b(?:text|bg|border|ring|outline|fill|stroke)-(?:red|amber|yellow|orange|green|emerald|lime)-\d{2,3}\b/g;

  it("uses no raw signal colours outside the exemptions", () => {
    const offenders: string[] = [];
    for (const file of tsxFiles(SRC)) {
      // SRC carries a trailing slash, so no +1 here — that ate the first
      // character and made every exemption silently fail to match.
      const rel = file.slice(SRC.length);
      if (EXEMPT.has(rel)) continue;
      const hits = readFileSync(file, "utf8").match(SIGNAL);
      if (hits) offenders.push(`${rel}: ${[...new Set(hits)].join(", ")}`);
    }
    expect(
      offenders,
      "use the semantic tokens (text-warn, text-down, text-live…) so a colour " +
        "keeps meaning one thing. If a use is genuinely outside the state " +
        "vocabulary, add it to EXEMPT with the reason.",
    ).toEqual([]);
  });
});
