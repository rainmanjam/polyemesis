/* The data shape of a /vs/<product> page, and the check that keeps it honest.
 * Every page is data (src/data/vs/<slug>.mjs) rendered by layouts/VsPage.astro.
 *
 * String fields hold plain text, plus these four inline tags and nothing else:
 * <a href>, <code>, <em>, <strong>. They are rendered with `set:html`, so
 * validateVs is the only thing between a data file and the page's markup. */

/** A column after polyemesis. `key` is the data-col value the chooser hides. */
export interface VsProduct {
  key: string;
  name: string;
}

interface SectionBase {
  id: string;
  heading: string;
  /** A paragraph before the content. */
  intro?: string;
  /** Paragraphs after the content. */
  after?: string[];
}

export type VsSection =
  | (SectionBase & { kind: "prose"; paragraphs: string[] })
  | (SectionBase & { kind: "table"; caption: string; head: string[]; rows: string[][] })
  | (SectionBase & {
      kind: "steps";
      columns: { title: string; where?: string; steps: string[] }[];
    })
  | (SectionBase & { kind: "items"; items: { h: string; p: string; more?: string }[] });

export interface VsWhichItem {
  name: string;
  id?: string;
  body: string;
  verdict?: string;
  outcome?: string;
  /** Label and value pairs shown under the card, such as Kind or Licence. */
  facts?: [string, string][];
}

export interface VsData {
  slug: (typeof VS_SLUGS)[number];
  /** Unchanged from the page as it was. */
  title: string;
  description: string;
  heading: string;
  accent?: string;
  lede: string;
  /** A short statement under the header: a bold lead-in and the sentence it opens. */
  callout?: { lead: string; body: string };
  /** The date the claims about other products were checked, e.g. "15 August 2026". */
  checked: string;
  /** One product, or two on /vs/restreamer. */
  products: VsProduct[];
  /** [label, polyemesis, ...one cell per product] */
  glance: string[][];
  fits: { poly: string[]; other: { heading: string; items: string[] } };
  /** `verdict` sits under the name by default, or after the body ("foot"). */
  which?: { heading: string; intro?: string; verdict?: "head" | "foot"; items: VsWhichItem[] };
  sections: VsSection[];
  merits?: { heading: string; note?: string; items: { h: string; p: string }[]; after?: string[] };
  useOther?: {
    heading: string;
    items: string[];
    after?: string[];
    links?: { label: string; href: string }[];
  };
  /** The note at the foot: when it was checked and where it is recorded. */
  footer?: string;
  closing: { heading: string; accent?: string; lede?: string };
}

export const VS_SLUGS = ["aitum-multistream", "obs-multi-rtmp", "restreamer", "streamelements", "streamlabs"] as const;

/* Ids the template itself uses for its own sections. */
const RESERVED_IDS = ["glance", "fits", "which", "does-well", "use-other", "others"];

const ALLOWED_TAGS = new Set(["a", "code", "em", "strong"]);
const TAG = /<(\/?)([a-zA-Z][a-zA-Z0-9-]*)([^<>]*)>/g;
const HREF_ATTR = /^ href="([^"]*)"$/;
/* A path on this site, a fragment, or https. Refused inside any of them: `&`
 * (an entity such as `&#x2f;` is decoded before the URL is parsed), `\` and
 * control characters (the parser reads `\` as `/` and drops tabs), so a path
 * cannot turn into `//evil.com`. A path may not start `//` or `/\`. */
const SAFE_HREF = /^(\/(?![/\\])[^\s"<>\\&\x00-\x1f\x7f]*|#[^\s"<>&\x00-\x1f\x7f]*|https:\/\/[^\s"<>\\&\x00-\x1f\x7f]+)$/;

/** Why a link target is refused; null when it is fine. */
export function hrefProblem(href: string): string | null {
  return SAFE_HREF.test(href) ? null : `"${href}" is not a path, a fragment or an https link`;
}

/* Fields the template renders with `set:html`. Every other string is rendered
 * as escaped text, so a tag in one would show as literal characters. */
const HTML_PATHS = [
  /^callout\.body$/,
  /^fits\.poly\[\d+\]$/,
  /^fits\.other\.items\[\d+\]$/,
  /^which\.intro$/,
  /^which\.items\[\d+\]\.(body|outcome)$/,
  /^sections\[\d+\]\.(intro|after\[\d+\]|paragraphs\[\d+\]|columns\[\d+\]\.steps\[\d+\])$/,
  /^(merits|sections\[\d+\])\.items\[\d+\]\.(p|more)$/,
  /^merits\.(note|after\[\d+\])$/,
  /^useOther\.(items|after)\[\d+\]$/,
  /^footer$/,
];

/** What is wrong with the markup in one string; [] when it only uses the allowed tags. */
export function allowedTagProblems(s: string): string[] {
  const out: string[] = [];
  if (/<!--|<\?|<!\[CDATA\[/i.test(s)) out.push("comments, processing instructions and CDATA are not allowed");
  const open: string[] = [];
  for (const m of s.matchAll(TAG)) {
    const [, closing, rawName, attrs] = m;
    const name = rawName.toLowerCase();
    if (!ALLOWED_TAGS.has(name)) {
      out.push(`<${closing}${rawName}> is not allowed`);
    } else if (closing) {
      if (open.pop() !== name) out.push(`</${name}> has no matching opening tag`);
    } else {
      out.push(...openTagProblems(name, attrs, open));
      open.push(name);
    }
  }
  for (const name of open) out.push(`<${name}> is never closed`);
  // A `<` that starts a tag but never closes it is not matched above.
  if (/<[a-zA-Z/!?]/.test(s.replace(TAG, ""))) out.push("an unterminated tag");
  return out;
}

function openTagProblems(name: string, attrs: string, open: string[]): string[] {
  if (name !== "a") return attrs.trim() === "" ? [] : [`<${name}${attrs}> may not have attributes`];
  const out: string[] = [];
  if (open.includes("a")) out.push("an <a> may not sit inside another <a>");
  const href = HREF_ATTR.exec(attrs)?.[1];
  const bad = href === undefined ? "<a> needs exactly one href" : hrefProblem(href);
  if (bad) out.push(bad);
  return out;
}

function stringsIn(v: unknown, path: string): [string, string][] {
  if (typeof v === "string") return [[path, v]];
  if (Array.isArray(v)) return v.flatMap((x, i) => stringsIn(x, `${path}[${i}]`));
  if (v && typeof v === "object") {
    return Object.entries(v).flatMap(([k, x]) => stringsIn(x, path ? `${path}.${k}` : k));
  }
  return [];
}

function checkText(d: VsData): string[] {
  const out: string[] = [];
  for (const [path, s] of stringsIn(d, "")) {
    if (HTML_PATHS.some((re) => re.test(path))) {
      for (const p of allowedTagProblems(s)) out.push(`${path}: ${p}`);
    } else if (/<[a-zA-Z/!?]/.test(s)) {
      out.push(`${path}: a tag is not allowed here`);
    }
    if (/pre-release/i.test(s)) out.push(`${path}: says pre-release (state the release instead)`);
  }
  return out;
}

function checkHead(d: VsData): string[] {
  const out: string[] = [];
  if (!VS_SLUGS.includes(d.slug)) out.push(`slug "${d.slug}" is not one of the five pages`);
  for (const f of ["title", "description", "heading", "lede", "checked"] as const) {
    if (!d[f]?.trim()) out.push(`${f} is empty`);
  }
  if (d.accent) {
    const first = d.heading.indexOf(d.accent);
    if (first < 0 || d.heading.indexOf(d.accent, first + d.accent.length) >= 0) {
      out.push(`accent "${d.accent}" must appear exactly once in the heading`);
    }
  }
  return out;
}

function checkProducts(d: VsData): string[] {
  const out: string[] = [];
  if (d.products.length < 1 || d.products.length > 2) out.push("products must hold one or two products");
  const seen = new Set<string>(["polyemesis"]);
  for (const p of d.products) {
    if (!p.key.trim() || !p.name.trim() || p.key === "polyemesis") {
      out.push(`product "${p.name}" needs a name and a key other than polyemesis`);
    } else if (seen.has(p.key)) {
      out.push(`product key "${p.key}" is used twice`);
    }
    seen.add(p.key);
  }
  return out;
}

function checkLists(d: VsData): string[] {
  const out: string[] = [];
  if (d.glance.length < 3) out.push("glance needs at least 3 rows");
  for (const row of d.glance) {
    if (row.length !== 2 + d.products.length) {
      out.push(`glance row "${row[0]}" has ${row.length} cells, expected ${2 + d.products.length}`);
    }
  }
  if (d.fits.poly.length === 0) out.push("fits.poly is empty");
  if (d.fits.other.items.length === 0) out.push("fits.other is empty");
  if (d.which && d.which.items.length === 0) out.push("which has no items");
  if (d.merits && d.merits.items.length === 0) out.push("merits has no items");
  if (d.useOther && d.useOther.items.length === 0) out.push("useOther has no items");
  d.useOther?.links?.forEach((l, i) => {
    const bad = hrefProblem(l.href);
    if (bad) out.push(`useOther.links[${i}].href: ${bad}`);
  });
  return out;
}

function sectionProblem(s: VsSection): string | null {
  switch (s.kind) {
    case "prose":
      return s.paragraphs.length === 0 ? "has no paragraphs" : null;
    case "table":
      if (s.head[0]?.trim().toLowerCase() === "capability") {
        return 'may not start with a "Capability" header (the build reads such a table as the capability table)';
      }
      return s.rows.some((r) => r.length !== s.head.length) ? "has a row that does not match its header" : null;
    case "steps":
      return s.columns.length === 0 || s.columns.some((c) => c.steps.length === 0) ? "has an empty column" : null;
    case "items":
      return s.items.length === 0 ? "has no items" : null;
  }
}

function checkSections(d: VsData): string[] {
  const out: string[] = [];
  const ids = new Set<string>();
  for (const s of d.sections) {
    if (!/^[a-z][a-z0-9-]*$/.test(s.id)) out.push(`section id "${s.id}" must be lower-case letters, digits and hyphens`);
    else if (RESERVED_IDS.includes(s.id)) out.push(`section id "${s.id}" is reserved by the template`);
    else if (ids.has(s.id)) out.push(`section id "${s.id}" is used twice`);
    ids.add(s.id);
    const problem = sectionProblem(s);
    if (problem) out.push(`section ${s.id} ${problem}`);
  }
  return out;
}

/** Everything wrong with a page's data; [] when it is fine. */
export function validateVs(d: VsData): string[] {
  return [...checkHead(d), ...checkProducts(d), ...checkLists(d), ...checkSections(d), ...checkText(d)];
}
