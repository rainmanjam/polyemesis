/// <reference types="node" />
import { describe, expect, it } from "vitest";
import { readFileSync } from "node:fs";
import { join } from "node:path";

import {
  retentionDirty,
  retentionDraft,
  savePolicyBody,
  switchPatchBody,
} from "./retentionDraft";
import type { Settings } from "./types";

/* The recording card had one shared object: the four retention number inputs
 * edited `settings` in place, and every switch spread that same object into its
 * PUT. Flipping Stems therefore committed a half-typed segment length. */

const server = (over: Partial<Settings["recording"]> = {}): Settings =>
  ({
    recording: {
      enabled: true,
      segmentSeconds: 30,
      maxGb: 50,
      maxAgeHours: 168,
      minFreeGb: 5,
      stems: false,
      stemCodec: "flac",
      ...over,
    },
  }) as Settings;

describe("switchPatchBody", () => {
  it("never carries a half-typed retention number with a switch", () => {
    // The operator is mid-way through typing "30" and has reached "3".
    // db/settings.go floors SegmentSeconds at 10, so this PUT is either a
    // rejection blaming a control they never touched, or -- once they reach a
    // legal digit -- a silent recorder restart on a value nobody confirmed.
    const body = switchPatchBody(server(), { stems: true });
    expect(body.recording.segmentSeconds).toBe(30);
    expect(body.recording.stems).toBe(true);
  });

  it("sends the server's own last answer for every field it is not about", () => {
    const body = switchPatchBody(server({ maxGb: 50 }), { enabled: false });
    expect(body.recording).toEqual({
      enabled: false,
      segmentSeconds: 30,
      maxGb: 50,
      maxAgeHours: 168,
      minFreeGb: 5,
      stems: false,
      stemCodec: "flac",
    });
  });
});

describe("savePolicyBody", () => {
  it("is the one control that commits what was typed", () => {
    const body = savePolicyBody(server(), {
      segmentSeconds: 60,
      maxGb: 10,
      maxAgeHours: 24,
      minFreeGb: 2,
    });
    expect(body.recording.segmentSeconds).toBe(60);
    expect(body.recording.maxGb).toBe(10);
    // And leaves the switches exactly where the server had them.
    expect(body.recording.stems).toBe(false);
    expect(body.recording.enabled).toBe(true);
  });
});

describe("retentionDirty", () => {
  it("is false for a draft that matches the server", () => {
    const s = server();
    expect(retentionDirty(s, retentionDraft(s))).toBe(false);
    expect(retentionDirty(s, null)).toBe(false);
  });

  it("is true the moment any of the four differs, so an abandoned edit is visible", () => {
    const s = server();
    expect(retentionDirty(s, { ...retentionDraft(s), segmentSeconds: 3 })).toBe(true);
    expect(retentionDirty(s, { ...retentionDraft(s), minFreeGb: 0 })).toBe(true);
  });
});

/* AND THAT THE PAGE ACTUALLY ASKS. */
const ROOT = new URL("../../../", import.meta.url).pathname;
const recPage = () => readFileSync(join(ROOT, "ui/src/pages/RecordingsPage.tsx"), "utf8");

describe("RecordingsPage, wired to these decisions", () => {
  it("keeps the retention numbers out of `settings` entirely", () => {
    const src = recPage();
    // Every number input used to setSettings(). None may again: that is what
    // made the shared object shared.
    expect(src).toContain("setDraft((d) => (d ? { ...d, segmentSeconds: Number(e.target.value) } : d))");
    expect(src).not.toContain("segmentSeconds: Number(e.target.value),");
    expect(src).not.toContain("maxGb: Number(e.target.value) },");
    expect(src).toContain("saveRetention(switchPatchBody(settings, patch))");
    expect(src).toContain("saveRetention(savePolicyBody(settings, draft))");
    // The enable switch used to spread `settings` by hand rather than go
    // through the one function that is careful about it.
    expect(src).toContain("onCheckedChange={(v) => saveRecording({ enabled: v })}");
    expect(src).toContain('t("rec.unsavedRetention")');
  });

  it("dashes all four disk figures when the usage read has not answered", () => {
    const src = recPage();
    // "Recordings 0, Used 0 B" beside "Free —, Volume —" reads as an empty
    // disk rather than as an unanswered question.
    expect(src).not.toContain("value={usage?.count ?? 0}");
    expect(src).not.toContain("value={bytes(usage?.usedBytes ?? 0)}");
    expect(src).toContain('value={usage ? usage.count : "—"}');
    expect(src).toContain('value={usage ? bytes(usage.usedBytes) : "—"}');
  });
});
