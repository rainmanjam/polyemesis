// @vitest-environment jsdom

import { readFileSync } from "node:fs";
import { join } from "node:path";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { CASES } from "../../data/cases.mjs";
import type { MotionGate } from "./motion";
import { fromCase, initMatrix, receives, same, summary, tableHtml, toggle } from "./matrix";

const gate = (allowed = true): MotionGate => ({
  allowed: () => allowed,
  userPaused: () => !allowed,
  setUserPaused: () => {},
  subscribe: () => () => {},
});

describe("fromCase", () => {
  it("builds the sports grid: five destinations, six tracks, two cells on per row", () => {
    const s = fromCase(CASES.sports);
    expect(s.dests).toHaveLength(5);
    expect(s.tracks).toEqual(["English", "Español", "Português", "Français", "Arabic", "Crowd"]);
    expect(s.on).toHaveLength(5);
    for (const row of s.on) {
      expect(row).toHaveLength(6);
      expect(row.filter(Boolean)).toHaveLength(2);
    }
    expect(s.on[0]).toEqual([true, false, false, false, false, true]);
  });
});

describe("toggle", () => {
  it("flips one cell and returns a new state without touching the input", () => {
    const s = fromCase(CASES.sports);
    const before = JSON.stringify(s);
    const next = toggle(s, 0, 1);
    expect(next).not.toBe(s);
    expect(next.on[0][1]).toBe(true);
    expect(next.on[0][0]).toBe(true);
    expect(next.on[1]).toEqual(s.on[1]);
    expect(JSON.stringify(s)).toBe(before);
    expect(toggle(next, 0, 1).on[0][1]).toBe(false);
  });
});

describe("receives", () => {
  it("lists the video, then the lit tracks as the data writes them", () => {
    expect(receives(fromCase(CASES.sports), 0)).toBe("video + English + Crowd");
    expect(receives(fromCase(CASES.worship), 0)).toBe("video + Room mix");
  });
  it("reads video only when nothing is lit", () => {
    let s = fromCase(CASES.sports);
    s = toggle(toggle(s, 0, 0), 0, 5);
    expect(receives(s, 0)).toBe("video only");
  });
});

describe("same", () => {
  it("turns every cell on, keeping the case's rows and columns", () => {
    const s = same(CASES.streamer);
    expect(s.dests).toEqual(fromCase(CASES.streamer).dests);
    expect(s.tracks).toEqual(fromCase(CASES.streamer).tracks);
    expect(s.on.flat().every(Boolean)).toBe(true);
  });
});

describe("summary", () => {
  it("counts destinations and distinct mixes", () => {
    expect(summary(fromCase(CASES.sports))).toBe("5 destinations · 5 different mixes · 1 video");
    expect(summary(same(CASES.sports))).toBe("5 destinations · 1 mix · 1 video");
  });
});

describe("tableHtml", () => {
  it("escapes data and names each cell by track and destination", () => {
    const html = tableHtml(fromCase(CASES.worship));
    expect(html).toContain('aria-label="Room mix to Custom RTMP"');
    expect(html).toMatch(/aria-pressed="true"[^>]*aria-label="Room mix to Custom RTMP"|aria-label="Room mix to Custom RTMP"[^>]*aria-pressed="true"/);
  });
});

describe("tableHtml phone layout hooks", () => {
  const html = tableHtml(fromCase(CASES.sports));
  const doc = new DOMParser().parseFromString(`<body>${html}</body>`, "text/html");
  const sports = CASES.sports;

  it("writes the table roles out, so a stacked-card layout keeps the structure a screen reader reads", () => {
    expect(doc.querySelector("table")?.getAttribute("role")).toBe("table");
    expect(doc.querySelectorAll("tbody")[0].getAttribute("role")).toBe("rowgroup");
    // header row + video row + one row per destination
    expect(doc.querySelectorAll('tr[role="row"]').length).toBe(2 + sports.dests.length);
    expect(doc.querySelectorAll('th[role="rowheader"]').length).toBe(1 + sports.dests.length);
    expect(doc.querySelectorAll('th[role="columnheader"]').length).toBe(2 + sports.tracks.length);
    expect(doc.querySelectorAll('td[role="cell"] button.mx-cell').length).toBe(sports.dests.length * sports.tracks.length);
  });

  it("gives every destination row the class the card layout hangs on, and the video row its own", () => {
    expect(doc.querySelectorAll("tr.mx-row").length).toBe(sports.dests.length);
    expect(doc.querySelectorAll("tr.mx-video").length).toBe(1);
  });

  it("shows the track's name in each cell, hidden from a screen reader (the button's label is read)", () => {
    const cells = [...doc.querySelectorAll<HTMLElement>("button.mx-cell")];
    expect(cells.length).toBeGreaterThan(0);
    for (const b of cells) {
      const name = b.querySelector(".mx-tn");
      expect(name?.getAttribute("aria-hidden")).toBe("true");
      const track = sports.tracks[Number(b.dataset.t)].label;
      expect(name?.textContent).toBe(track);
      expect(b.getAttribute("aria-label")).toContain(track);
      expect(b.getAttribute("aria-label")).toContain(sports.dests[Number(b.dataset.d)].name);
    }
  });
});

describe("initMatrix", () => {
  let root: HTMLElement;
  const presetBtn = (key: string) => root.querySelector<HTMLButtonElement>(`[data-preset="${key}"]`)!;
  const cell = (d: number, t: number) => root.querySelector<HTMLButtonElement>(`button[data-d="${d}"][data-t="${t}"]`)!;
  const hears = (d: number) => root.querySelector(`[data-hears="${d}"]`)!.textContent;
  let dispose: () => void;

  beforeEach(() => {
    document.body.innerHTML = `<section id="matrix" data-matrix>
      <button type="button" data-preset="sports" aria-pressed="true">Sports club</button>
      <button type="button" data-preset="worship" aria-pressed="false">Church &amp; events</button>
      <button type="button" data-preset="streamer" aria-pressed="false">Streamer</button>
      <button type="button" data-preset="same" aria-pressed="false">Same mix everywhere</button>
      <button type="button" data-reset>Reset</button>
      <div data-mx-table>${tableHtml(fromCase(CASES.sports))}</div>
      <p data-mx-summary></p>
      <p data-mx-live aria-live="polite"></p>
    </section>`;
    root = document.getElementById("matrix")!;
    dispose = initMatrix(root, gate());
  });
  afterEach(() => dispose());

  it("flips aria-pressed and the row's text when a cell is clicked", () => {
    expect(cell(0, 1).getAttribute("aria-pressed")).toBe("false");
    cell(0, 1).click();
    expect(cell(0, 1).getAttribute("aria-pressed")).toBe("true");
    expect(hears(0)).toBe("video + English + Español + Crowd");
    expect(hears(1)).toBe("video + Español + Crowd");
    expect(root.querySelector("[data-mx-live]")!.textContent).toBe("YouTube · English now receives video + English + Español + Crowd");
  });

  it("keeps the focused cell focused after a toggle", () => {
    cell(2, 0).focus();
    cell(2, 0).click();
    expect(document.activeElement).toBe(cell(2, 0));
  });

  it("makes every cell a real button, which is what gives Tab, Space and Enter", () => {
    const cells = root.querySelectorAll("button[data-d]");
    expect(cells).toHaveLength(30);
    for (const c of cells) {
      expect(c.tagName).toBe("BUTTON");
      expect(c.getAttribute("type")).toBe("button");
      expect(c.hasAttribute("tabindex")).toBe(false);
    }
  });

  it("rebuilds the whole grid from a case preset", () => {
    presetBtn("streamer").click();
    expect(root.querySelectorAll("button[data-d]")).toHaveLength(25);
    expect(root.querySelectorAll("thead th")[1].textContent).toContain("Mic");
    expect(hears(0)).toBe("video + Mic + Game + Music + Alerts + Chat TTS");
    expect(presetBtn("streamer").getAttribute("aria-pressed")).toBe("true");
    expect(presetBtn("sports").getAttribute("aria-pressed")).toBe("false");
  });

  it("Same mix everywhere keeps the case and lights every cell", () => {
    presetBtn("same").click();
    expect(root.querySelectorAll('button[data-d][aria-pressed="true"]')).toHaveLength(30);
    expect(presetBtn("same").getAttribute("aria-pressed")).toBe("true");
    expect(presetBtn("sports").getAttribute("aria-pressed")).toBe("false");
  });

  it("clears the preset mark on a hand edit unless the grid is the case's own routing again", () => {
    cell(0, 1).click();
    expect(presetBtn("sports").getAttribute("aria-pressed")).toBe("false");
    cell(0, 1).click();
    expect(presetBtn("sports").getAttribute("aria-pressed")).toBe("true");
  });

  it("Reset returns to the current case's own routing", () => {
    presetBtn("worship").click();
    cell(0, 4).click();
    presetBtn("same").click();
    root.querySelector<HTMLButtonElement>("[data-reset]")!.click();
    expect(root.querySelectorAll('button[data-d][aria-pressed="true"]')).toHaveLength(5);
    expect(hears(1)).toBe("video + Broadcast mix");
    expect(presetBtn("worship").getAttribute("aria-pressed")).toBe("true");
  });

  it("updates the summary line", () => {
    presetBtn("same").click();
    expect(root.querySelector("[data-mx-summary]")!.textContent).toBe("5 destinations · 1 mix · 1 video");
  });

  it("stops responding after dispose", () => {
    dispose();
    cell(0, 1).click();
    expect(cell(0, 1).getAttribute("aria-pressed")).toBe("false");
    dispose = () => {};
  });
});

describe("MixMatrixPlay.astro copy", () => {
  const src = readFileSync(join(process.cwd(), "src/components/home/MixMatrixPlay.astro"), "utf8");
  it("states no track limit and no per-cell mute (docs/AUDIO-ROUTING.md: 32 tracks; gain per cell, mute per track)", () => {
    expect(src).not.toMatch(/up to six/i);
    expect(src).not.toContain("mute and L/R per cell");
  });
});
