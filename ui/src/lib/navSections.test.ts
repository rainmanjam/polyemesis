/// <reference types="node" />
import { describe, expect, it } from "vitest";
import { readFileSync } from "node:fs";
import { join } from "node:path";

import { NAV_SECTIONS } from "./navSections";
import en from "./i18n/en.json";

/* A11: the flat 14-entry navigation is grouped into five labelled sections.
 *
 * The guard reads the NAV literal in AppLayout.tsx rather than trusting a copy
 * of it, the same way tour-drift.test.ts does: a route added to NAV and left out
 * of a section would otherwise be unreachable from the grouped rail, and nothing
 * on screen would say so. */

const APP_LAYOUT = join(new URL("../", import.meta.url).pathname, "components", "AppLayout.tsx");

function navRoutes(): string[] {
  const src = readFileSync(APP_LAYOUT, "utf8");
  const nav = src.slice(src.indexOf("const NAV: NavItem[] = ["));
  return [...nav.matchAll(/\{ to: "([^"]+)"/g)].map((m) => m[1]);
}

describe("navigation sections", () => {
  const routes = navRoutes();

  it("the scanner reads the whole NAV literal", () => {
    // Positive control: a scanner that matched nothing would make every test
    // below pass over an empty list. Fifteen since the System page (A17).
    expect(routes.length).toBe(15);
  });

  it("every route in NAV appears in exactly one section", () => {
    const placed = NAV_SECTIONS.flatMap((s) => s.routes);
    expect([...placed].sort()).toEqual([...routes].sort());
  });

  it("every section has a label English defines, and no two share one", () => {
    const keys = NAV_SECTIONS.map((s) => s.labelKey);
    expect(new Set(keys).size).toBe(keys.length);
    for (const key of keys) expect(en[key]).toBeTruthy();
  });

  it("groups the console the way an operator works: live, content, community, operations, settings", () => {
    expect(NAV_SECTIONS.map((s) => s.labelKey)).toEqual([
      "nav.group.live",
      "nav.group.content",
      "nav.group.community",
      "nav.group.operations",
      "nav.group.settings",
    ]);
  });

  it("keeps System beside Monitoring under Operations, and out of the settings group (A82)", () => {
    // Both are where an operator goes to find out why something is wrong. The
    // settings group had held System, and its heading read "Settings" beside a
    // link also named Settings.
    const section = (key: string) => NAV_SECTIONS.find((s) => s.labelKey === key)!;
    expect(section("nav.group.operations").routes).toEqual(["/jobs", "/monitoring", "/system"]);
    expect(section("nav.group.settings").routes).toEqual(["/settings"]);
  });
});
