import { describe, expect, it } from "vitest";
import { readFileSync } from "node:fs";
import { join } from "node:path";

import { breadcrumbLd, breadcrumbLdJson } from "./breadcrumbs";
import { splitAccent } from "./accent";
import { fillRelease, releaseLine } from "./release";

/* The shared shell every inner page sits on: pure helpers by value, the
 * components and stylesheet by source text (rendering is check-build's job). */
const src = (p: string) => readFileSync(join(__dirname, "..", p), "utf8");

describe("breadcrumbLd", () => {
  it("gives every crumb with an href an absolute item, and leaves the last one bare", () => {
    const ld = breadcrumbLd([{ label: "polyemesis", href: "/" }, { label: "features" }]);
    expect(ld["@type"]).toBe("BreadcrumbList");
    expect(ld.itemListElement).toEqual([
      { "@type": "ListItem", position: 1, name: "polyemesis", item: "https://polyemesis.com/" },
      { "@type": "ListItem", position: 2, name: "features" },
    ]);
  });

  it("drops a middle crumb with no href and renumbers", () => {
    const ld = breadcrumbLd([
      { label: "polyemesis", href: "/" },
      { label: "docs", href: "/docs" },
      { label: "the routing" },
      { label: "Audio routing", href: "/docs/audio-routing" },
    ]);
    expect(ld.itemListElement.map((i) => [i.position, i.name])).toEqual([
      [1, "polyemesis"],
      [2, "docs"],
      [3, "Audio routing"],
    ]);
    expect(ld.itemListElement[2].item).toBe("https://polyemesis.com/docs/audio-routing");
  });

  it("keeps the item on a last crumb that has an href, and honours another site", () => {
    const ld = breadcrumbLd([{ label: "a", href: "/a" }], "https://example.test");
    expect(ld.itemListElement[0].item).toBe("https://example.test/a");
  });

  it("does not double the slash when the site ends in one", () => {
    const ld = breadcrumbLd([{ label: "a", href: "/a" }], "https://example.test/");
    expect(ld.itemListElement[0].item).toBe("https://example.test/a");
  });

  it("returns an empty list for no crumbs", () => {
    expect(breadcrumbLd([]).itemListElement).toEqual([]);
  });
});

describe("breadcrumbLdJson", () => {
  it("never lets a label close the script element", () => {
    const json = breadcrumbLdJson([{ label: "</script><b>x", href: "/a" }]);
    expect(json).not.toContain("<");
    expect(JSON.parse(json).itemListElement[0].name).toBe("</script><b>x");
  });
});

describe("splitAccent", () => {
  it("splits around the one accent word", () => {
    expect(splitAccent("The audio is the difference.", "audio")).toEqual(["The ", "audio", " is the difference."]);
  });
  it("returns the heading whole when there is no accent", () => {
    expect(splitAccent("Plain heading")).toEqual(["Plain heading", "", ""]);
    expect(splitAccent("Plain heading", "")).toEqual(["Plain heading", "", ""]);
  });
  it("throws when the accent is missing from the heading", () => {
    expect(() => splitAccent("x", "y")).toThrow(/not in/);
  });
  it("matches the accent as a whole word, not inside another word", () => {
    expect(splitAccent("Restreamer, you stream", "stream")).toEqual(["Restreamer, you ", "stream", ""]);
    expect(() => splitAccent("Restreamer", "stream")).toThrow(/not in/);
    expect(() => splitAccent("stream the stream", "stream")).toThrow(/more than once/);
    expect(splitAccent("Pick (once) now", "once")).toEqual(["Pick (", "once", ") now"]);
  });
  it("throws when the accent occurs twice", () => {
    expect(() => splitAccent("go go", "go")).toThrow(/more than once/);
  });
});

describe("release line", () => {
  const r = { version: "0.18.0", date: "2026-11-11" };
  it("reads version and date", () => {
    expect(releaseLine(r)).toBe("v0.18.0, released 2026-11-11");
  });
  it("is null with no release", () => {
    expect(releaseLine(null)).toBeNull();
  });
  it("fills every {release} token", () => {
    expect(fillRelease("A {release}; B {release}.", r)).toBe("A v0.18.0, released 2026-11-11; B v0.18.0, released 2026-11-11.");
  });
  it("falls back to the current release when there is none", () => {
    expect(fillRelease("Here: {release}.", null)).toBe("Here: the current release.");
  });
});

describe("shell components", () => {
  it("PageHeader has exactly one h1", () => {
    expect(src("components/inner/PageHeader.astro").match(/<h1/g)).toHaveLength(1);
  });
  it("InnerPage is not a second main, forwards noindex and path, and gates the rail on index", () => {
    const page = src("layouts/InnerPage.astro");
    expect(page).not.toContain("<main");
    expect(page).toMatch(/<Base[^>]*\bnoindex=\{noindex\}/s);
    expect(page).toMatch(/<Base[^>]*\bpath=\{path\}/s);
    const uses = page.match(/<PageIndex/g) ?? [];
    expect(uses).toHaveLength(1);
    expect(page).toMatch(/index\?\.length[\s\S]*<PageIndex/);
    expect(page).toContain("data-column");
    expect(page).toMatch(/import \{ bootInner \} from "..\/scripts\/inner"/);
  });
  it("CloseBand defaults the primary link to the download page", () => {
    const band = src("components/inner/CloseBand.astro");
    expect(band).toContain('"Install polyemesis"');
    expect(band).toContain('"/download"');
  });
  /* Source assertions, not a render: vitest here has no Astro plugin, so a .astro file cannot be
     imported. The guard on the <a> is what decides it, so assert the guard and the attribute. */
  it("Crumbs renders the last item as plain text with aria-current, even when it has an href", () => {
    const crumbs = src("components/inner/Crumbs.astro");
    expect(crumbs).toMatch(/const last = crumbs\.length - 1/);
    expect(crumbs).toMatch(/i < last && c\.href \? \(\s*<a href=\{c\.href\}>/);
    expect(crumbs).toMatch(/<span aria-current=\{i === last \? "page" : undefined\}>/);
    expect(crumbs).toContain("breadcrumbLdJson(crumbs)");
  });
});

describe("inner.css", () => {
  const css = src("styles/inner.css");
  it("defines the shell", () => {
    for (const s of [".ph", ".lay", ".band", ".longform", ".crumb", "text-transform: lowercase"]) {
      expect(css).toContain(s);
    }
  });
  it("carries a banner per area, in order, with room between them", () => {
    const areas = ["shell", "nav", "index", "table", "docs", "docs-index", "install", "features", "comparison", "vs", "longform", "calculator"];
    const lines = css.split("\n");
    const at = areas.map((a) => lines.findIndex((l) => l.trim().startsWith(`/* ===== ${a} `)));
    expect(at.every((n) => n >= 0)).toBe(true);
    expect([...at].sort((x, y) => x - y)).toEqual(at);
    // Four lines or more between banners, so parallel edits to neighbouring areas do not conflict.
    for (let i = 1; i < at.length; i++) expect(at[i] - at[i - 1]).toBeGreaterThanOrEqual(4);
  });
  it("scopes the index rail to nav.rail, because the homepage uses .rail on SVG paths", () => {
    expect(css).not.toMatch(/(^|[\s,>])\.rail\b/m);
  });
  it("uses none of the banned things", () => {
    expect(css).not.toContain(".reveal");
    expect(css).not.toContain("data-reveal");
    expect(css).not.toContain("animation-timeline");
    expect(css).not.toContain("--color-faint");
  });
});
