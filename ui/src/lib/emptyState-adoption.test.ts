import { describe, expect, it } from "vitest";
import { readFileSync } from "node:fs";

/* ONE EMPTY STATE ON THE OPS PAGES (app-council A83).
 *
 * The review found two empty-state styles side by side: the kit's EmptyState
 * in three files and hand-written muted sentences, dashed boxes and centred
 * cards everywhere else. Each page below now draws its empty lists with
 * EmptyState. Rendering every page's empty branch needs a page-sized harness
 * each (Dashboard, Monitoring and NoProgrammeYet have render tests of their
 * own); this pins the rest at the source, and names the old markup so it
 * cannot come back unnoticed. */

const read = (rel: string) => readFileSync(`src/${rel}`, "utf8");

const PAGES: Record<string, string[]> = {
  // file -> the hand-drawn empty markup it used to carry
  "pages/JobsPage.tsx": ['<div className="px-3 py-8 text-center text-sm text-muted-foreground">{empty}</div>'],
  "pages/RenditionsPage.tsx": ['<p className="max-w-lg text-[12px] text-muted-foreground">'],
  "pages/MetersPage.tsx": ['{monitorOn ? t("meters.nothingToMeasure") : t("meters.monitorOff")}'],
  "pages/RoutingPage.tsx": ['<CardContent className="py-8 text-center text-[12px] text-muted-foreground">'],
  "pages/MonitoringPage.tsx": ["rounded border border-dashed border-border py-6 text-center"],
  "pages/Dashboard.tsx": ['<p className="text-tiny text-muted-foreground">{t("dash.destNoneHere")}</p>'],
  "components/NoProgrammeYet.tsx": ["<CardContent"],
};

describe("the ops pages draw their empty lists with EmptyState", () => {
  it.each(Object.keys(PAGES))("%s uses EmptyState and not its old hand-drawn markup", (rel) => {
    const src = read(rel);
    expect(src).toContain("<EmptyState");
    for (const old of PAGES[rel]) expect(src, `${rel} still carries: ${old}`).not.toContain(old);
  });
});
