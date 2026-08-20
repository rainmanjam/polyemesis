/// <reference types="node" />
import { describe, expect, it } from "vitest";
import { readFileSync } from "node:fs";
import { join } from "node:path";

import { runAtUnset, scheduleBlock, type ScheduleFormFacts } from "./scheduleCreate";

const NOW = Date.parse("2026-08-20T12:00:00Z");

const form = (over: Partial<ScheduleFormFacts> = {}): ScheduleFormFacts => ({
  name: "Evening show",
  action: "start",
  kind: "daily",
  runAt: "",
  ...over,
});

describe("runAtUnset", () => {
  it("treats the epoch as unset, because that is what the server does", () => {
    // `new Date(0).toISOString()` was the dialog's seed for a blank form. It
    // survives the server's Validate (which only rejects Go's zero time),
    // unixOrZero stores 0, and time.Unix(0,0) reads back as unset -- so the
    // schedule saved, toasted success, and could never fire.
    expect(runAtUnset(new Date(0).toISOString())).toBe(true);
    expect(runAtUnset("")).toBe(true);
    expect(runAtUnset("not a date")).toBe(true);
    expect(runAtUnset("2026-09-01T19:00:00Z")).toBe(false);
  });
});

describe("scheduleBlock: a one-shot that can never fire", () => {
  it("refuses to create a once schedule with no instant chosen", () => {
    const b = scheduleBlock(form({ kind: "once", runAt: "" }), {
      destinationsKnown: true,
      editing: false,
      now: NOW,
    });
    expect(b.kind).toBe("runAtUnset");
  });

  it("refuses the epoch specifically, which used to save and toast success", () => {
    const b = scheduleBlock(form({ kind: "once", runAt: new Date(0).toISOString() }), {
      destinationsKnown: true,
      editing: false,
      now: NOW,
    });
    expect(b.kind).toBe("runAtUnset");
  });

  it("refuses an instant already in the past", () => {
    const b = scheduleBlock(form({ kind: "once", runAt: "2026-08-19T19:00:00Z" }), {
      destinationsKnown: true,
      editing: false,
      now: NOW,
    });
    expect(b.kind).toBe("runAtPast");
  });

  it("accepts a future instant", () => {
    const b = scheduleBlock(form({ kind: "once", runAt: "2026-08-21T19:00:00Z" }), {
      destinationsKnown: true,
      editing: false,
      now: NOW,
    });
    expect(b.kind).toBe("ok");
  });

  it("does not ask a daily schedule for an instant it does not use", () => {
    const b = scheduleBlock(form({ kind: "daily", runAt: "" }), {
      destinationsKnown: true,
      editing: false,
      now: NOW,
    });
    expect(b.kind).toBe("ok");
  });
});

describe("scheduleBlock: destinations that have not arrived", () => {
  it("refuses to create a stop schedule while the destination list is unknown", () => {
    // "None selected means every destination" is what scheduler.go implements.
    // Before the first snapshot the checkbox list is empty for a reason that
    // has nothing to do with the operator's intent, so "stop nothing selected"
    // silently becomes "stop the whole broadcast at 23:00".
    const b = scheduleBlock(form({ action: "stop" }), {
      destinationsKnown: false,
      editing: false,
      now: NOW,
    });
    expect(b.kind).toBe("destinationsUnknown");
  });

  it("does not block a playlist schedule, which may not name destinations at all", () => {
    const b = scheduleBlock(form({ action: "playlist.stop" }), {
      destinationsKnown: false,
      editing: false,
      now: NOW,
    });
    expect(b.kind).toBe("ok");
  });

  it("does not block an edit, whose stored ids round-trip untouched", () => {
    const b = scheduleBlock(form({ action: "stop" }), {
      destinationsKnown: false,
      editing: true,
      now: NOW,
    });
    expect(b.kind).toBe("ok");
  });

  it("still requires a name", () => {
    const b = scheduleBlock(form({ name: "  " }), {
      destinationsKnown: true,
      editing: false,
      now: NOW,
    });
    expect(b.kind).toBe("name");
  });
});

/* AND THAT THE DIALOG ACTUALLY ASKS. */
const ROOT = new URL("../../../", import.meta.url).pathname;
const page = () => readFileSync(join(ROOT, "ui/src/pages/AutomationPage.tsx"), "utf8");

describe("AutomationPage, the schedule dialog", () => {
  it("seeds a blank one-shot empty rather than at the Unix epoch", () => {
    const src = page();
    expect(src).not.toContain("runAt: new Date(0).toISOString(),\n");
    expect(src).toContain('runAt: "",');
  });

  it("gates the Create button on the decision, not on the name alone", () => {
    // Scoped to ScheduleDialog: RuleDialog, further up, gates its own save on
    // a name and that is all a webhook rule needs.
    const src = page().slice(page().indexOf("function ScheduleDialog("));
    expect(src.length).toBeGreaterThan(1000);
    expect(src).not.toContain("disabled={saving || !form.name.trim()}");
    expect(src).toContain('disabled={saving || blocked.kind !== "ok"}');
    // The reason BESIDE the button: button.tsx sets
    // `disabled:pointer-events-none`, so a title on it is unreachable.
    expect(src).toContain("{t(blocked.reason)}");
  });

  it("does not render an empty destination list as an install with no destinations", () => {
    const src = page();
    expect(src).not.toContain("{(status?.destinations ?? []).map((d) => (");
    expect(src).toContain("const destinationsKnown = status !== null;");
  });
});
