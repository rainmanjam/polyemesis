/// <reference types="node" />
import { describe, expect, it } from "vitest";
import { readFileSync } from "node:fs";
import { join } from "node:path";

import { pipelineDirty, pipelineSaveCarriesPassword } from "./pipelineSave";
import type { Settings } from "./types";

/* Type the MQTT broker password, click any of the eight OTHER Save buttons on
 * the Pipeline tab: "Settings saved.", the password never sent, the field
 * wiped by the effect that re-seeds the draft, `hasPassword` still false. */

const settings = (over: Partial<Settings> = {}): Settings =>
  ({
    mqtt: { enabled: true, brokerUrl: "mqtts://broker", hasPassword: false },
    synth: { silenceOnVideoOnly: true },
    ...over,
  }) as unknown as Settings;

describe("pipelineSaveCarriesPassword", () => {
  it("carries a typed password, whichever card the operator was looking at", () => {
    expect(pipelineSaveCarriesPassword("hunter2")).toBe(true);
  });

  it("leaves the stored password alone when the box is untouched", () => {
    // Empty means "leave the stored one alone" -- the box is never seeded from
    // the server, because the server does not send the password back.
    expect(pipelineSaveCarriesPassword("")).toBe(false);
  });
});

describe("pipelineDirty", () => {
  it("counts a typed password as an unsaved change on its own", () => {
    // The exact case that used to be discarded in silence: nothing else on the
    // tab edited, so a dirty check over the draft alone would have said there
    // was nothing to save.
    const s = settings();
    expect(pipelineDirty(s, s, "hunter2")).toBe(true);
  });

  it("is false when nothing has been touched", () => {
    const s = settings();
    expect(pipelineDirty(s, { ...s }, "")).toBe(false);
  });

  it("is true for an edit anywhere in the tab draft", () => {
    const s = settings();
    expect(pipelineDirty(s, { ...s, synth: { silenceOnVideoOnly: false } }, "")).toBe(true);
  });
});

/* AND THAT THE TAB ACTUALLY ROUTES THROUGH IT. */
const ROOT = new URL("../../../", import.meta.url).pathname;
const page = () => readFileSync(join(ROOT, "ui/src/pages/SettingsPage.tsx"), "utf8");

describe("SettingsPage, Pipeline tab", () => {
  it("has exactly one Save, and it is the one that carries the password", () => {
    const src = page();
    // Scoped to PipelineSettings, because the one remaining
    // `onClick={() => onSave(draft)}` in the file belongs to IngestSettings --
    // a different tab, with its own draft and its own single Save.
    const tab = src.slice(
      src.indexOf("function PipelineSettings("),
      src.indexOf("function MultitrackHardware("),
    );
    expect(tab.length).toBeGreaterThan(1000);
    const bare = tab.split("onClick={() => onSave(draft)}").length - 1;
    expect(bare).toBe(0);
    expect(tab).toContain("const saveTab = () =>");
    expect(tab).toContain("pipelineSaveCarriesPassword(mqttPassword)");
    expect(tab).toContain("? onSaveMqtt(draft, mqttPassword)");
    expect(tab).toContain("<Button size=\"sm\" onClick={saveTab} disabled={saving || !dirty}>");
    // And the MQTT card's own button is gone with the rest.
    expect(tab).not.toContain("onClick={() => onSaveMqtt(draft, mqttPassword)}");
  });

  it("shows the operator that the tab has an unsaved change", () => {
    const src = page();
    expect(src).toContain("const dirty = pipelineDirty(settings, draft, mqttPassword);");
    expect(src).toContain('t("set.pipelineUnsaved")');
    // Sticky: two long columns, and one Save at the top of them is a Save the
    // operator editing the last card cannot see.
    expect(src).toContain('className="sticky top-0 z-10 flex items-center gap-2');
  });

  it("leaves the multitrack card without a Save of its own", () => {
    // It called onSave(draft) like the other seven, which is how a GPU
    // declaration came to be saved by a button that dropped a typed password.
    expect(page()).toContain("<MultitrackHardware draft={draft} setDraft={setDraft} />");
  });
});
