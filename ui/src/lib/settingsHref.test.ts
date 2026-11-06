import { readdirSync, readFileSync, statSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";
import { SETTINGS_ANCHORS, SETTINGS_TABS, settingsHref, settingsTabFor } from "./settingsSearch";

describe("settingsHref", () => {
  it("builds the same links the views always hand-wrote", () => {
    expect(settingsHref("platforms")).toBe("/settings?tab=platforms");
    expect(settingsHref("security")).toBe("/settings?tab=security");
    expect(settingsHref("pipeline", "failover")).toBe("/settings?tab=pipeline#failover");
  });

  it("opens, for every tab, the panel it names", () => {
    for (const tab of SETTINGS_TABS.filter((t) => t !== "debug")) {
      const q = new URL(settingsHref(tab), "http://x").searchParams.get("tab") ?? "";
      expect(settingsTabFor(q)).toBe(tab);
    }
  });

  it("refuses a tab or anchor that does not exist, at compile time", () => {
    // @ts-expect-error a typo would silently open the Overview
    settingsHref("platfroms");
    // @ts-expect-error not an anchor the page defines
    settingsHref("pipeline", "nope");
    expect(SETTINGS_ANCHORS.failover).toBe("failover");
  });
});

/** Every production source file under ui/src. */
function sources(dir: string): string[] {
  return readdirSync(dir).flatMap((name) => {
    const p = join(dir, name);
    if (statSync(p).isDirectory()) return sources(p);
    return /\.(ts|tsx)$/.test(name) && !/\.test\./.test(name) ? [p] : [];
  });
}

describe("no view hand-builds a settings link", () => {
  it("goes through settingsHref", () => {
    const offenders = sources("src")
      .filter((p) => !p.endsWith("lib/settingsSearch.ts"))
      .filter((p) => /["'`]\/settings\?/.test(readFileSync(p, "utf8")));
    expect(offenders).toEqual([]);
  });
});
