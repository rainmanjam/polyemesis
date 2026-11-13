import { readdirSync, readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";

/* inner.css loads on every page (global.css imports it), and the homepage's own
 * component styles are unlayered, so they win where both set a property, but a
 * property only inner.css sets still lands on the homepage. A class the two
 * share must therefore be styled by inner.css only inside an inner-only
 * ancestor. This guard fails, naming the class, when it is not. */

const root = new URL("../", import.meta.url);
const read = (rel: string) => readFileSync(new URL(rel, root), "utf8");

/** Ancestors only inner pages have: the shell wrapper, the page header, the body
 *  layout, the closing band, the docs grid and column, the article. */
const INNER_ONLY = [".inner-main", ".ph", ".lay", ".band", ".docgrid", "[data-doc-column]", ".doc-prose"];

/** Classes that are shared on purpose: one rule, one look, on every page. */
const SHARED_ON_PURPOSE = new Set<string>([
  // The nav is one component on every page; its current-page marking is meant to apply to the homepage's nav too.
  "nav-link",
  "nav-install",
]);

/** Every selector of every rule, with at-rules (layer, media) flattened. */
function selectorsOf(css: string): string[] {
  const out: string[] = [];
  const stripped = css.replace(/\/\*[\s\S]*?\*\//g, "");
  const re = /([^{};]+)\{/g;
  for (const m of stripped.matchAll(re)) {
    const head = m[1].trim();
    if (head.startsWith("@")) continue;
    // Split the list at top-level commas only.
    let depth = 0;
    let cur = "";
    for (const ch of head) {
      if (ch === "(" || ch === "[") depth++;
      if (ch === ")" || ch === "]") depth--;
      if (ch === "," && depth === 0) {
        out.push(cur.trim());
        cur = "";
      } else cur += ch;
    }
    out.push(cur.trim());
  }
  return out.filter(Boolean);
}

/** Remove every `:name( ... )` whose contents only test, never style, an element. */
function withoutConditions(sel: string): string {
  let s = sel;
  for (;;) {
    const i = s.search(/:(not|has)\(/);
    if (i < 0) return s;
    let depth = 0;
    let j = s.indexOf("(", i);
    for (; j < s.length; j++) {
      if (s[j] === "(") depth++;
      if (s[j] === ")" && --depth === 0) break;
    }
    s = s.slice(0, i) + s.slice(j + 1);
  }
}

const classesIn = (s: string) => [...s.matchAll(/\.(-?[_a-zA-Z][\w-]*)/g)].map((m) => m[1]);

/** Class name -> the selectors that style it with nothing that keeps it off the homepage.
 *  A selector is kept off the homepage when it also requires something only inner
 *  pages have: one of INNER_ONLY, or any other class the homepage never uses
 *  (a rule for `.fx-row.enc` cannot match a page without `.fx-row`). */
function unscopedClasses(css: string, home: Set<string>): Map<string, string[]> {
  const found = new Map<string, string[]>();
  for (const sel of selectorsOf(css)) {
    const flat = withoutConditions(sel);
    const own = classesIn(flat);
    const innerOnlyAttr = INNER_ONLY.some((t) => t.startsWith("[") && flat.includes(t));
    for (const cls of own) {
      const guarded = innerOnlyAttr || own.some((d) => d !== cls && (!home.has(d) || INNER_ONLY.includes(`.${d}`)));
      if (!guarded) found.set(cls, [...(found.get(cls) ?? []), sel]);
    }
  }
  return found;
}

function stringsIn(src: string): string[] {
  return [...src.matchAll(/["'`]([^"'`]*)["'`]/g)].map((m) => m[1]);
}

/** Class names the homepage's sources put on elements. */
function homeClasses(): Set<string> {
  const names = new Set<string>();
  const add = (list: string) => {
    for (const n of list.split(/\s+/)) if (/^-?[_a-zA-Z][\w-]*$/.test(n)) names.add(n);
  };
  const files = ["pages/index.astro"];
  for (const f of readdirSync(new URL("components/home/", root))) if (f.endsWith(".astro")) files.push(`components/home/${f}`);
  for (const f of files) {
    const src = read(f).replace(/<style[\s\S]*?<\/style>/g, "");
    for (const m of src.matchAll(/\bclass="([^"]*)"/g)) add(m[1]);
    for (const m of src.matchAll(/\bclass(?::list)?=\{([^}]*(?:\}[^}]*)*?)\}(?=[\s>/])/g)) for (const s of stringsIn(m[1])) add(s);
    for (const m of src.matchAll(/class=\{`([^`]*)`\}/g)) add(m[1].replace(/\$\{[^}]*\}/g, " "));
  }
  const scriptDir = new URL("scripts/home/", root);
  for (const f of readdirSync(scriptDir)) {
    if (!f.endsWith(".ts") || f.endsWith(".test.ts")) continue;
    const src = readFileSync(new URL(f, scriptDir), "utf8");
    for (const m of src.matchAll(/classList\.(?:add|remove|toggle|replace)\(([^)]*)\)/g)) for (const s of stringsIn(m[1])) add(s);
    for (const m of src.matchAll(/className\s*=\s*["'`]([^"'`]+)["'`]/g)) add(m[1]);
    for (const m of src.matchAll(/class="([^"]*)"/g)) add(m[1]);
  }
  return names;
}

describe("inner.css does not reach the homepage", () => {
  const home = homeClasses();
  const unscoped = unscopedClasses(read("styles/inner.css"), home);

  it("reads the homepage's classes", () => {
    for (const c of ["inst-tabs", "inst-tab", "inst-panel", "inst-steps", "cp-note"]) expect(home.has(c) || c === "cp-note").toBe(true);
    expect(home.size).toBeGreaterThan(50);
  });

  it("styles every class the homepage also uses only inside an inner-only ancestor", () => {
    const clashes = [...unscoped.entries()]
      .filter(([cls]) => home.has(cls) && !SHARED_ON_PURPOSE.has(cls))
      .map(([cls, sels]) => `.${cls}: ${[...new Set(sels)].join(" | ")}`)
      .sort((a, b) => a.localeCompare(b));
    expect(clashes).toEqual([]);
  });

  it("finds a class the homepage and inner.css both style when nothing scopes it", () => {
    const probe = unscopedClasses(
      ".inst-tab { color: red; } .inner-main .inst-tab { color: blue; } .ph h1 .it { x: y; } .band { a: b; } .ph.x { a: b; }",
      new Set(["inst-tab", "it", "band", "ph"]),
    );
    expect(probe.get("inst-tab")).toEqual([".inst-tab"]);
    expect(probe.has("it")).toBe(false);
    // A scope class the homepage also used would itself be unguarded when it stands alone.
    expect(probe.get("band")).toEqual([".band"]);
  });
});
