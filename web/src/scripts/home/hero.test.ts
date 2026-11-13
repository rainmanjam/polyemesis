// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi, type Mock } from "vitest";
import { CASE_ORDER, CASES } from "../../data/cases.mjs";
import type { MotionGate } from "./motion";
import { initHero } from "./hero";
import { heroModel } from "./hero-model";
import { mountGlobe, type GlobeHandle } from "./globe";

/* The globe module is mocked: three.js and WebGL are not what these tests are
 * about. They check how the hero wires the globe, not how it draws. */
vi.mock("./globe", () => ({ mountGlobe: vi.fn() }));

let allowed: boolean;
let subs: Array<(a: boolean) => void>;
let gate: MotionGate;
let root: HTMLElement;
let stop: (() => void) | null;

/* The same markup contract Hero.astro renders: a tablist, one panel per case
 * (only the first visible) and a fine-print line holding the first case's text. */
function mount(): HTMLElement {
  document.body.innerHTML = `
    <section data-hero>
      <div role="tablist" data-case-tabs>
        ${CASE_ORDER.map(
          (k, i) =>
            `<button type="button" role="tab" id="tab-${k}" data-key="${k}" aria-controls="panel-${k}" aria-selected="${i === 0}" tabindex="${i === 0 ? 0 : -1}">${CASES[k].tab}</button>`,
        ).join("")}
      </div>
      ${CASE_ORDER.map(
        (k, i) =>
          `<div role="tabpanel" id="panel-${k}" aria-labelledby="tab-${k}"${i === 0 ? "" : " hidden"}>
            <p data-clock>ON AIR 00:14:35</p>
            ${CASES[k].dests.map(() => `<div class="meter"><i data-meter></i></div>`).join("")}
          </div>`,
      ).join("")}
      <p data-fine>${CASES.sports.fine}</p>
      <div data-globe></div>
    </section>`;
  return document.querySelector<HTMLElement>("[data-hero]")!;
}

const selected = () => root.querySelector('[role="tab"][aria-selected="true"]')!.getAttribute("data-key");
const fine = () => root.querySelector("[data-fine]")!.textContent;

let globe: { setCase: Mock<GlobeHandle["setCase"]>; destroy: Mock<GlobeHandle["destroy"]> };

beforeEach(() => {
  globe = { setCase: vi.fn<GlobeHandle["setCase"]>(), destroy: vi.fn<GlobeHandle["destroy"]>() };
  vi.mocked(mountGlobe).mockReset();
  vi.mocked(mountGlobe).mockResolvedValue(globe);
  vi.useFakeTimers();
  allowed = true;
  subs = [];
  gate = {
    allowed: () => allowed,
    userPaused: () => !allowed,
    setUserPaused: () => {},
    subscribe: (fn) => {
      subs.push(fn);
      return () => {
        subs = subs.filter((s) => s !== fn);
      };
    },
  };
  // Visible the moment it is observed.
  vi.stubGlobal(
    "IntersectionObserver",
    class {
      constructor(private cb: (e: Array<{ isIntersecting: boolean; intersectionRatio: number }>) => void) {}
      observe() {
        this.cb([{ isIntersecting: true, intersectionRatio: 1 }]);
      }
      disconnect() {}
    },
  );
  root = mount();
  stop = null;
});

afterEach(() => {
  stop?.();
  vi.useRealTimers();
  vi.unstubAllGlobals();
  document.body.innerHTML = "";
});

describe("initHero", () => {
  it("starts on the sports case", () => {
    stop = initHero(root, gate);
    expect(selected()).toBe("sports");
    expect(fine()).toBe(CASES.sports.fine);
  });

  it("advances to the next case after 12 seconds, and wraps", () => {
    stop = initHero(root, gate);
    vi.advanceTimersByTime(11_999);
    expect(selected()).toBe("sports");
    vi.advanceTimersByTime(1);
    expect(selected()).toBe("worship");
    vi.advanceTimersByTime(12_000);
    expect(selected()).toBe("streamer");
    vi.advanceTimersByTime(12_000);
    expect(selected()).toBe("sports");
  });

  it("does not advance while the gate disallows motion, and resumes when it allows", () => {
    allowed = false;
    stop = initHero(root, gate);
    vi.advanceTimersByTime(60_000);
    expect(selected()).toBe("sports");
    allowed = true;
    subs.forEach((fn) => fn(true));
    vi.advanceTimersByTime(12_000);
    expect(selected()).toBe("worship");
  });

  it("stops advancing when the gate turns motion off", () => {
    stop = initHero(root, gate);
    allowed = false;
    subs.forEach((fn) => fn(false));
    vi.advanceTimersByTime(60_000);
    expect(selected()).toBe("sports");
  });

  it("stops advancing for good after a click on a tab", () => {
    stop = initHero(root, gate);
    root.querySelector<HTMLElement>("#tab-streamer")!.click();
    expect(selected()).toBe("streamer");
    vi.advanceTimersByTime(120_000);
    expect(selected()).toBe("streamer");
    // not even when motion goes off and on again
    subs.forEach((fn) => fn(false));
    subs.forEach((fn) => fn(true));
    vi.advanceTimersByTime(120_000);
    expect(selected()).toBe("streamer");
  });

  it("keeps advancing after Pause then Resume (the Pause button is not a take-over)", () => {
    root.insertAdjacentHTML("beforeend", '<button type="button" data-pause aria-pressed="false">Pause motion</button>');
    const pause = root.querySelector<HTMLElement>("[data-pause]")!;
    stop = initHero(root, gate);
    // boot() wires Pause to the gate; here the same effect is the gate callbacks.
    pause.dispatchEvent(new Event("pointerdown", { bubbles: true }));
    pause.click();
    allowed = false;
    subs.forEach((fn) => fn(false));
    pause.click();
    allowed = true;
    subs.forEach((fn) => fn(true));
    vi.advanceTimersByTime(12_000);
    expect(selected()).toBe("worship");
  });

  it("stops advancing after a pointer press or a key press inside the hero", () => {
    stop = initHero(root, gate);
    root.dispatchEvent(new Event("pointerdown", { bubbles: true }));
    vi.advanceTimersByTime(60_000);
    expect(selected()).toBe("sports");

    stop();
    root = mount();
    stop = initHero(root, gate);
    root.querySelector<HTMLElement>("#tab-sports")!.dispatchEvent(new KeyboardEvent("keydown", { key: "Tab", bubbles: true }));
    vi.advanceTimersByTime(60_000);
    expect(selected()).toBe("sports");
  });

  it("dispatches case:change with the key on every change", () => {
    const seen: string[] = [];
    root.addEventListener("case:change", (e) => seen.push((e as CustomEvent<{ key: string }>).detail.key));
    stop = initHero(root, gate);
    vi.advanceTimersByTime(12_000);
    root.querySelector<HTMLElement>("#tab-sports")!.click();
    expect(seen.at(-2)).toBe("worship");
    expect(seen.at(-1)).toBe("sports");
  });

  it("shows the fine print of the current case", () => {
    stop = initHero(root, gate);
    root.querySelector<HTMLElement>("#tab-worship")!.click();
    expect(fine()).toBe(CASES.worship.fine);
    root.querySelector<HTMLElement>("#tab-streamer")!.click();
    expect(fine()).toBe(CASES.streamer.fine);
  });

  it("sets the fine print as text, not markup", () => {
    const original = CASES.worship.fine;
    CASES.worship.fine = "<b>x</b> & <i>y</i>";
    stop = initHero(root, gate);
    root.querySelector<HTMLElement>("#tab-worship")!.click();
    expect(root.querySelector("[data-fine] b")).toBeNull();
    expect(fine()).toBe("<b>x</b> & <i>y</i>");
    CASES.worship.fine = original;
  });

  it("shows only the selected case's panel", () => {
    stop = initHero(root, gate);
    root.querySelector<HTMLElement>("#tab-worship")!.click();
    expect(root.querySelector<HTMLElement>("#panel-worship")!.hidden).toBe(false);
    expect(root.querySelector<HTMLElement>("#panel-sports")!.hidden).toBe(true);
  });

  it("the disposer stops the timer and the tab controller", () => {
    const dispose = initHero(root, gate);
    dispose();
    vi.advanceTimersByTime(60_000);
    expect(selected()).toBe("sports");
    root.querySelector<HTMLElement>("#tab-streamer")!.click();
    expect(selected()).toBe("sports");
  });
});

describe("heroModel", () => {
  it("gives the sports case five tiles of six chips, two lit each", () => {
    const m = heroModel("sports");
    expect(m.tiles).toHaveLength(5);
    for (const t of m.tiles) {
      expect(t.chips).toHaveLength(6);
      expect(t.chips.filter((c) => c.on)).toHaveLength(2);
    }
  });
  it("keeps chips in track order and lights the destination's own tracks", () => {
    const t = heroModel("sports").tiles[1];
    expect(t.chips.map((c) => c.label)).toEqual(CASES.sports.tracks.map((x: { label: string }) => x.label));
    expect(t.chips.filter((c) => c.on).map((c) => c.label)).toEqual(["Español", "Crowd"]);
    expect(t.mix).toBe("video + ESPAÑOL + CROWD");
    expect(t.city).toBe("Bogotá");
  });
  it("counts destinations and distinct audio mixes for the header", () => {
    for (const k of CASE_ORDER) expect(heroModel(k).header).toEqual({ destinations: 5, mixes: 5 });
  });
});

describe("hero motion", () => {
  const meters = () => [...root.querySelectorAll<HTMLElement>("[data-meter]")].map((m) => m.style.transform);
  const clock = () => root.querySelector("[data-clock]")!.textContent;

  it("does nothing while the gate disallows motion", () => {
    allowed = false;
    stop = initHero(root, gate);
    const m0 = meters();
    const c0 = clock();
    vi.advanceTimersByTime(30_000);
    expect(meters()).toEqual(m0);
    expect(clock()).toBe(c0);
  });

  it("moves the meters, differently per destination, and ticks the clock once a second", () => {
    stop = initHero(root, gate);
    const panel = root.querySelector("#panel-sports")!;
    const m0 = meters();
    vi.advanceTimersByTime(1000);
    expect(clock()).toBe("ON AIR 00:14:36");
    vi.advanceTimersByTime(4000);
    expect(clock()).toBe("ON AIR 00:14:40");
    const m1 = meters();
    expect(m1).not.toEqual(m0);
    const first = [...panel.querySelectorAll<HTMLElement>("[data-meter]")].map((m) => m.style.transform);
    expect(new Set(first).size).toBeGreaterThan(1);
  });

  it("stops when the gate turns motion off", () => {
    stop = initHero(root, gate);
    vi.advanceTimersByTime(2000);
    allowed = false;
    subs.forEach((fn) => fn(false));
    const m = meters();
    const c = clock();
    vi.advanceTimersByTime(30_000);
    expect(meters()).toEqual(m);
    expect(clock()).toBe(c);
  });

  it("the disposer stops the meters and the clock", () => {
    const dispose = initHero(root, gate);
    vi.advanceTimersByTime(2000);
    dispose();
    const m = meters();
    const c = clock();
    vi.advanceTimersByTime(30_000);
    expect(meters()).toEqual(m);
    expect(clock()).toBe(c);
  });

  it("keeps moving after the visitor takes over the tabs", () => {
    stop = initHero(root, gate);
    root.querySelector<HTMLElement>("#tab-worship")!.click();
    const c = clock();
    vi.advanceTimersByTime(3000);
    expect(clock()).not.toBe(c);
  });
});

describe("hero globe", () => {
  const settle = () => vi.advanceTimersByTimeAsync(0);

  it("mounts the globe once when its box is near, with the gate and the current case", async () => {
    stop = initHero(root, gate);
    await settle();
    expect(mountGlobe).toHaveBeenCalledTimes(1);
    const [el, g, c] = vi.mocked(mountGlobe).mock.calls[0];
    expect(el).toBe(root.querySelector("[data-globe]"));
    expect(g).toBe(gate);
    expect(c).toBe(CASES.sports);
    await vi.advanceTimersByTimeAsync(20_000);
    expect(mountGlobe).toHaveBeenCalledTimes(1);
  });

  it("does not mount the globe until its box is near", async () => {
    vi.stubGlobal(
      "IntersectionObserver",
      class {
        observe() {}
        disconnect() {}
      },
    );
    stop = initHero(root, gate);
    await settle();
    expect(mountGlobe).not.toHaveBeenCalled();
  });

  it("tells the globe about every case change", async () => {
    stop = initHero(root, gate);
    await settle();
    root.querySelector<HTMLElement>("#tab-worship")!.click();
    expect(globe.setCase).toHaveBeenLastCalledWith(CASES.worship);
    root.querySelector<HTMLElement>("#tab-streamer")!.click();
    expect(globe.setCase).toHaveBeenLastCalledWith(CASES.streamer);
  });

  it("gives the globe the case chosen while it was still loading", async () => {
    let resolve!: (g: typeof globe) => void;
    vi.mocked(mountGlobe).mockReturnValue(new Promise((r) => (resolve = r)));
    stop = initHero(root, gate);
    await settle();
    root.querySelector<HTMLElement>("#tab-worship")!.click();
    resolve(globe);
    await settle();
    expect(globe.setCase).toHaveBeenCalledWith(CASES.worship);
  });

  it("the disposer destroys the globe and stops forwarding case changes", async () => {
    const dispose = initHero(root, gate);
    await settle();
    dispose();
    expect(globe.destroy).toHaveBeenCalledTimes(1);
    root.querySelector<HTMLElement>("#tab-worship")!.click();
    expect(globe.setCase).not.toHaveBeenCalled();
  });

  it("destroys a globe that finishes loading after the disposer ran", async () => {
    let resolve!: (g: typeof globe) => void;
    vi.mocked(mountGlobe).mockReturnValue(new Promise((r) => (resolve = r)));
    const dispose = initHero(root, gate);
    await settle();
    dispose();
    resolve(globe);
    await settle();
    expect(globe.destroy).toHaveBeenCalledTimes(1);
  });
});
