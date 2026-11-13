// @vitest-environment jsdom
import { describe, expect, it } from "vitest";

import { DEFAULTS, LIMITS, announcement, compute, initCalc, type CalcKey } from "./calc-mini";

const gate = { allowed: () => false, subscribe: () => () => {} } as never;

describe("homepage calculator maths", () => {
  it("computes the default inputs to the figures /calculator shows for one hour", () => {
    const v = compute(DEFAULTS);
    expect([v.ingest, v.egress, v.perHour]).toEqual(["7.8 Mbps", "15.5 Mbps", "5.8 GB"]);
  });

  it("data is the server's overhead-only figure: headroom must not leak into it", () => {
    // 2 x (6000+160) kbps x 1.05 = 12.936 Mbps -> x 0.45 GB per Mbps-hour = 5.8212 GB.
    // With the 20% headroom too it would be 6.99 GB, which prints as 7.0.
    expect(compute(DEFAULTS).perHour).toBe("5.8 GB");
    expect(compute({ ...DEFAULTS, dests: 10 }).perHour).toBe("29.1 GB"); // 29.106 with overhead; 34.9 with headroom
  });

  it("the bigger leg's bar is full and the other is its share", () => {
    const v = compute(DEFAULTS);
    expect(v.egressShare).toBe(1);
    expect(v.ingestShare).toBeCloseTo(6160 / 12320, 9);
  });

  it("uses singular and plural wording", () => {
    expect(compute({ ...DEFAULTS, dests: 1 }).egressNote).toMatch(/^1 destination, /);
    expect(compute({ ...DEFAULTS, dests: 2 }).egressNote).toMatch(/^2 destinations, /);
    expect(compute({ ...DEFAULTS, tracks: 1 }).ingestNote).not.toMatch(/track/);
    expect(compute({ ...DEFAULTS, tracks: 3 }).ingestNote).toMatch(/across 3 audio tracks/);
  });

  it("never prints NaN or a negative at any corner of the limits", () => {
    for (const lo of [true, false]) {
      const v = compute(Object.fromEntries((Object.keys(LIMITS) as CalcKey[]).map((k) => [k, lo ? LIMITS[k].min : LIMITS[k].max])) as never);
      expect(JSON.stringify(v)).not.toMatch(/NaN|-\d/);
    }
  });
});

function mount(): HTMLElement {
  document.body.innerHTML = `<section>
    <input data-calc-in="video" type="range" min="500" max="20000" value="6000">
    <input data-calc-in="audio" type="range" min="32" max="320" value="160">
    <button data-calc-step="tracks" data-dir="-1"></button>
    <input data-calc-in="tracks" type="number" value="1">
    <button data-calc-step="tracks" data-dir="1"></button>
    <button data-calc-step="dests" data-dir="-1"></button>
    <input data-calc-in="dests" type="number" value="2">
    <button data-calc-step="dests" data-dir="1"></button>
    <output data-calc-out="ingest"></output><output data-calc-out="egress"></output><output data-calc-out="per-hour"></output>
    <p data-calc-out="ingest-note"></p><p data-calc-out="egress-note"></p>
    <output data-calc-readout="video"></output><output data-calc-readout="audio"></output>
    <p data-calc-live></p></section>`;
  return document.querySelector("section")!;
}
const text = (root: HTMLElement, name: string) => root.querySelector(`[data-calc-out="${name}"]`)!.textContent;

describe("initCalc", () => {
  it("moving a slider recomputes the outputs to compute() for the new inputs, without announcing per tick", () => {
    const root = mount();
    initCalc(root, gate);
    const video = root.querySelector<HTMLInputElement>('[data-calc-in="video"]')!;
    video.value = "9000";
    video.dispatchEvent(new Event("input", { bubbles: true }));
    const want = compute({ ...DEFAULTS, video: 9000 });
    expect([text(root, "ingest"), text(root, "egress"), text(root, "per-hour")]).toEqual([want.ingest, want.egress, want.perHour]);
    expect(root.querySelector('[data-calc-readout="video"]')!.textContent).toBe("9000 kbps");
    expect(root.querySelector("[data-calc-live]")!.textContent).toBe("");
    video.dispatchEvent(new Event("change", { bubbles: true }));
    expect(root.querySelector("[data-calc-live]")!.textContent).toBe(announcement(want));
  });

  it("a stepper press changes the count and stops at the limit", () => {
    const root = mount();
    initCalc(root, gate);
    const dests = root.querySelector<HTMLInputElement>('[data-calc-in="dests"]')!;
    const plus = root.querySelector<HTMLButtonElement>('[data-calc-step="dests"][data-dir="1"]')!;
    plus.click();
    expect(dests.value).toBe("3");
    expect(text(root, "egress")).toBe(compute({ ...DEFAULTS, dests: 3 }).egress);
    dests.value = "20";
    dests.dispatchEvent(new Event("input", { bubbles: true }));
    expect(plus.getAttribute("aria-disabled")).toBe("true"); // not `disabled`: see the next test
    plus.click();
    expect(dests.value).toBe("20");
  });

  it("a stepper that reaches its limit keeps focus: aria-disabled, never disabled", () => {
    const root = mount();
    initCalc(root, gate);
    const dests = root.querySelector<HTMLInputElement>('[data-calc-in="dests"]')!;
    const plus = root.querySelector<HTMLButtonElement>('[data-calc-step="dests"][data-dir="1"]')!;
    const minus = root.querySelector<HTMLButtonElement>('[data-calc-step="dests"][data-dir="-1"]')!;
    plus.focus();
    for (let i = 0; i < 18; i++) plus.click(); // 2 -> 20, the limit
    expect(dests.value).toBe("20");
    expect(plus.disabled).toBe(false);
    expect(plus.getAttribute("aria-disabled")).toBe("true");
    expect(document.activeElement).toBe(plus);
    plus.click();
    expect(dests.value).toBe("20");
    expect(minus.hasAttribute("disabled")).toBe(false);
    expect(minus.getAttribute("aria-disabled")).toBe("false");
    minus.click();
    expect(dests.value).toBe("19");
    expect(plus.getAttribute("aria-disabled")).toBe("false");
  });

  it("gives each slider a value text with its unit, and keeps it current", () => {
    const root = mount();
    initCalc(root, gate);
    const video = root.querySelector<HTMLInputElement>('[data-calc-in="video"]')!;
    const audio = root.querySelector<HTMLInputElement>('[data-calc-in="audio"]')!;
    expect(video.getAttribute("aria-valuetext")).toBe("6000 kbps");
    expect(audio.getAttribute("aria-valuetext")).toBe("160 kbps");
    video.value = "9000";
    video.dispatchEvent(new Event("input", { bubbles: true }));
    expect(video.getAttribute("aria-valuetext")).toBe("9000 kbps");
  });

  it("a typed decimal or out-of-range count is put back to an integer inside the limits on leave", () => {
    const root = mount();
    initCalc(root, gate);
    const dests = root.querySelector<HTMLInputElement>('[data-calc-in="dests"]')!;
    for (const [typed, shown] of [["2.5", "3"], ["99", "20"], ["-4", "1"], ["", "2"]]) {
      dests.value = typed;
      dests.dispatchEvent(new Event("input", { bubbles: true }));
      expect(text(root, "egress")).not.toMatch(/NaN|-/);
      dests.dispatchEvent(new Event("change", { bubbles: true }));
      expect(dests.value).toBe(shown);
    }
  });
});
