/// <reference types="node" />
import { describe, expect, it } from "vitest";
import { readFileSync } from "node:fs";
import { join } from "node:path";

import { purgeTone } from "./purgeOutcome";

describe("purgeTone", () => {
  it("does not report a legitimate answer as a failure", () => {
    // "Nothing was old enough to purge" used to be `throw new Error(...)`,
    // raised so that the shared act() helper's catch would put it on screen --
    // in the red toast kept for things that went wrong.
    expect(purgeTone(0)).toBe("info");
  });

  it("reports an actual purge as a success", () => {
    expect(purgeTone(1)).toBe("success");
    expect(purgeTone(40)).toBe("success");
  });
});

/* AND THAT THE PAGE ACTUALLY ASKS. */
const ROOT = new URL("../../../", import.meta.url).pathname;
const page = () => readFileSync(join(ROOT, "ui/src/pages/JobsPage.tsx"), "utf8");

describe("JobsPage, purging the history", () => {
  it("stops using an exception as a return channel", () => {
    const src = page();
    expect(src).not.toContain("throw new Error(");
    expect(src).toContain('if (purgeTone(purged) === "info") toast.info(t("jobs.nothingWasOldEnoughTo"));');
    expect(src).toContain('toast.success(t("jobs.historyPurged", { count: purged }))');
  });
});

describe("JobsPage, the policy Save button", () => {
  it("no longer has a Save that exists only while there is nothing to save", () => {
    const src = page();
    // The button was rendered under `{!dirty && (` -- always visible, and
    // withdrawn the moment the operator edited a field. The poll does NOT
    // clobber the draft (load() guards on dirtyRef.current); the defect was
    // the vanishing button alone.
    expect(src).not.toContain("{!dirty && (");
    // One Save left, in a sticky bar, because the per-kind cards make this tab
    // long. Since A63 the bar is always rendered (pinned to the bottom) and
    // Save is disabled rather than withdrawn while there is nothing to write;
    // JobsPage.policyLayout.test.tsx checks that against the rendered page.
    expect(src.split('t("jobs.savePolicy")').length - 1).toBe(1);
    expect(src).toContain('className="sticky bottom-0 z-10 flex flex-wrap items-center justify-end gap-2');
    expect(src).not.toContain("{dirty && (");
  });
});

describe("PublicPlayer, the viewer count", () => {
  it("keeps asking once playback has started", () => {
    const src = readFileSync(join(ROOT, "ui/src/pages/PublicPlayer.tsx"), "utf8");
    // The only poll was gated on `phase.kind !== "waiting"`, so the count
    // froze at whatever it was the moment playback began -- zero, for the
    // first viewer to arrive -- and never moved again.
    expect(src).toContain('if (phase.kind !== "ready") return;');
    expect(src).toContain(".then((view) => setPhase({ kind: \"ready\", view }))");
    // And it may never DEMOTE the phase: a transient 404 must not tear the
    // player down under a viewer who is watching perfectly well.
    expect(src).not.toContain('if (phase.kind !== "ready") return;\n    const id = window.setInterval(() => void load()');
  });
});
