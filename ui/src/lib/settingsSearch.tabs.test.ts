import { describe, expect, it } from "vitest";
import { SETTINGS_GROUPS, SETTINGS_TABS, settingsTabFor } from "./settingsSearch";

/* EVERY OLD ?tab= STILL OPENS ITS PANEL (settings-council R6).
 *
 * The rail was regrouped and an Overview added. The values below are every
 * `?tab=` a link in this repository, the OAuth callback (internal/api/
 * oauth_handlers.go), the tour (lib/tourSteps.ts) or an operator's bookmark
 * has ever carried. Each must still resolve to the panel it opened before. */
const LEGACY = ["ingest", "pipeline", "platforms", "overlays", "obs", "security", "danger"] as const;

describe("settingsTabFor", () => {
  it.each(LEGACY)("keeps ?tab=%s on its own panel", (tab) => {
    expect(settingsTabFor(tab)).toBe(tab);
  });

  it("opens the Overview for a value no build ever used, rather than an empty panel", () => {
    expect(settingsTabFor("nonsense")).toBe("overview");
    expect(settingsTabFor("")).toBe("overview");
  });

  it("never treats debug as a panel here: the page sends it to /system first", () => {
    expect(settingsTabFor("debug")).toBe("overview");
  });
});

describe("the grouped rail", () => {
  it("places every panel except Overview and Danger zone under exactly one group", () => {
    const grouped = SETTINGS_GROUPS.flatMap((g) => g.tabs);
    expect(new Set(grouped).size).toBe(grouped.length);
    const panels = SETTINGS_TABS.filter((t) => !["overview", "danger", "debug"].includes(t));
    expect([...grouped].sort()).toEqual([...panels].sort());
  });
});
