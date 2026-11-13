// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { MotionGate } from "./motion";
import { initPanels, LOOP_PANELS, miniature, PANEL_NAMES } from "./panels";

type Watcher = { el: Element; cb: (entries: Array<{ isIntersecting: boolean; intersectionRatio: number }>) => void };
let watchers: Watcher[];

function fakeGate(initial: boolean) {
  let allowed = initial;
  const subs = new Set<(a: boolean) => void>();
  const gate: MotionGate = {
    allowed: () => allowed,
    userPaused: () => !allowed,
    setUserPaused: () => {},
    subscribe(fn) {
      subs.add(fn);
      return () => subs.delete(fn);
    },
  };
  return {
    gate,
    set(a: boolean) {
      allowed = a;
      for (const fn of [...subs]) fn(a);
    },
  };
}

/** Every looping panel, mounted from the same markup FeatureTour.astro renders. */
function fixture(names: readonly string[] = LOOP_PANELS): HTMLElement {
  document.body.innerHTML = `<section data-panels>${names
    .map((n) => `<article data-panel="${n}"><div data-live>${miniature(n)}</div></article>`)
    .join("")}</section>`;
  return document.querySelector<HTMLElement>("[data-panels]")!;
}

const live = (name: string) => document.querySelector(`[data-panel="${name}"] [data-live]`)!;
const see = (on: boolean) => {
  for (const w of watchers) w.cb([{ isIntersecting: on, intersectionRatio: on ? 1 : 0 }]);
};

beforeEach(() => {
  vi.useFakeTimers();
  watchers = [];
  (globalThis as unknown as { IntersectionObserver: unknown }).IntersectionObserver = class {
    private w: Watcher | undefined;
    constructor(private cb: Watcher["cb"]) {}
    observe(el: Element) {
      this.w = { el, cb: this.cb };
      watchers.push(this.w);
    }
    unobserve() {}
    disconnect() {
      watchers = watchers.filter((w) => w !== this.w);
    }
  };
});

afterEach(() => {
  vi.useRealTimers();
  document.body.innerHTML = "";
});

describe("initPanels", () => {
  it("names the panels the tour ships", () => {
    expect(PANEL_NAMES).toEqual(["failover", "chat", "captions", "recordings", "link", "hooks"]);
  });

  it("starts nothing while the gate disallows motion", () => {
    const root = fixture();
    const snapshot = root.innerHTML;
    const dispose = initPanels(root, fakeGate(false).gate);
    see(true);
    vi.advanceTimersByTime(60_000);
    expect(root.innerHTML).toBe(snapshot);
    expect(vi.getTimerCount()).toBe(0);
    dispose();
  });

  it.each(LOOP_PANELS)("%s: rests, changes while running, and returns to rest byte for byte", (name) => {
    const root = fixture([name]);
    const rest = live(name).innerHTML;
    const probe = document.createElement("div");
    probe.innerHTML = miniature(name);
    expect(rest).toBe(probe.innerHTML); // the resting markup is the n=0 frame
    const g = fakeGate(true);
    const dispose = initPanels(root, g.gate);
    vi.advanceTimersByTime(30_000);
    expect(live(name).innerHTML).toBe(rest); // not on screen: nothing runs
    see(true);
    vi.advanceTimersByTime(3_500);
    expect(live(name).innerHTML).not.toBe(rest);
    see(false);
    expect(live(name).innerHTML).toBe(rest);
    see(true);
    vi.advanceTimersByTime(3_500);
    g.set(false); // Pause motion
    expect(live(name).innerHTML).toBe(rest);
    expect(vi.getTimerCount()).toBe(0);
    dispose();
  });

  it("recordings keep their summary line and update it", () => {
    const root = fixture(["recordings"]);
    const dispose = initPanels(root, fakeGate(true).gate);
    see(true);
    const sums = new Set<string>();
    for (let i = 0; i < 6; i++) {
      vi.advanceTimersByTime(1700);
      const s = root.querySelector("[data-recsum]");
      expect(s).not.toBeNull();
      sums.add(s!.textContent!);
      expect(root.querySelector(".rc-list")).not.toBeNull();
    }
    expect(sums.size).toBeGreaterThan(1);
    dispose();
  });

  it("link updates its readout and its rows", () => {
    const root = fixture(["link"]);
    const dispose = initPanels(root, fakeGate(true).gate);
    const v = () => [...root.querySelectorAll("[data-v], [data-link], [data-rate]")].map((e) => e.textContent + (e as HTMLElement).style.width).join("|");
    const rest = v();
    see(true);
    vi.advanceTimersByTime(3_300);
    expect(v()).not.toBe(rest);
    dispose();
  });

  it("is deterministic: two runs show the same frames", () => {
    const run = () => {
      const root = fixture();
      const d = initPanels(root, fakeGate(true).gate);
      see(true);
      vi.advanceTimersByTime(9_000);
      const out = root.innerHTML;
      d();
      document.body.innerHTML = "";
      return out;
    };
    expect(run()).toBe(run());
  });

  it("its disposer stops both loops", () => {
    const root = fixture();
    const dispose = initPanels(root, fakeGate(true).gate);
    see(true);
    vi.advanceTimersByTime(3_000);
    expect(vi.getTimerCount()).toBeGreaterThanOrEqual(LOOP_PANELS.length);
    dispose();
    expect(vi.getTimerCount()).toBe(0);
    expect(watchers).toHaveLength(0);
  });

  it("ignores a panel it has no loop for", () => {
    document.body.innerHTML = `<section data-panels><article data-panel="meters"><div data-live>x</div></article></section>`;
    const dispose = initPanels(document.querySelector<HTMLElement>("[data-panels]")!, fakeGate(true).gate);
    expect(watchers).toHaveLength(0);
    dispose();
  });
});

describe("the failover panel", () => {
  /** The old Recovery section is still on the page; the panel is a second failover root. */
  function both(): HTMLElement {
    document.body.innerHTML = `
      <section data-failover><div data-fo-controls hidden><button data-stage="0">Live</button><button data-play>Pull</button></div><div data-frame data-state="live"></div></section>
      <section data-panels><article data-panel="failover">
        <div data-fo-controls hidden><button type="button" data-stage="0">Live</button><button type="button" data-stage="2">Slate on air</button><button type="button" data-play>Pull the cable</button></div>
        <div data-frame data-state="live"><span data-enc></span></div>
        <ul><li data-dest></li><li data-dest></li><li data-dest></li></ul>
        <ol data-log aria-live="off"></ol>
      </article></section>`;
    return document.querySelector<HTMLElement>("[data-panels]")!;
  }

  it("starts at live, shows its own controls and leaves the other root alone", () => {
    const root = both();
    const dispose = initPanels(root, fakeGate(false).gate);
    const panel = root.querySelector<HTMLElement>('[data-panel="failover"]')!;
    expect(panel.querySelector<HTMLElement>("[data-frame]")!.dataset.state).toBe("live");
    expect(panel.querySelector<HTMLElement>("[data-fo-controls]")!.hidden).toBe(false);
    expect(document.querySelector<HTMLElement>("[data-failover] [data-fo-controls]")!.hidden).toBe(true);
    // with motion off, "Pull the cable" is one instant change to the slate
    panel.querySelector<HTMLButtonElement>("[data-play]")!.click();
    expect(panel.querySelector<HTMLElement>("[data-frame]")!.dataset.state).toBe("slate");
    expect(document.querySelector<HTMLElement>("[data-failover] [data-frame]")!.dataset.state).toBe("live");
    dispose();
  });
});

describe("a miniature inside a closed fold", () => {
  /** The same article, with its miniature behind a <details> (the phone's markup). */
  function folded(name: string, open: boolean) {
    document.body.innerHTML = `<section data-panels><article data-panel="${name}"><details class="fold"${open ? " open" : ""}><summary>x</summary><div data-live>${miniature(name)}</div></details></article></section>`;
    return {
      root: document.querySelector<HTMLElement>("[data-panels]")!,
      fold: document.querySelector<HTMLDetailsElement>("details")!,
    };
  }
  const toggle = (fold: HTMLDetailsElement, open: boolean) => {
    fold.open = open;
    fold.dispatchEvent(new Event("toggle"));
  };

  it("does not run while the fold is closed, runs when it is opened, and rests again when it is closed", () => {
    const { root, fold } = folded("hooks", false);
    const rest = live("hooks").innerHTML;
    const { gate } = fakeGate(true);
    initPanels(root, gate);
    see(true);
    vi.advanceTimersByTime(20_000);
    expect(live("hooks").innerHTML).toBe(rest);

    toggle(fold, true);
    vi.advanceTimersByTime(5_000);
    expect(live("hooks").innerHTML).not.toBe(rest);

    toggle(fold, false);
    expect(live("hooks").innerHTML).toBe(rest);
    vi.advanceTimersByTime(20_000);
    expect(live("hooks").innerHTML).toBe(rest);
  });

  it("runs at once when there is no fold or the fold starts open", () => {
    const { root } = folded("chat", true);
    const rest = live("chat").innerHTML;
    initPanels(root, fakeGate(true).gate);
    see(true);
    vi.advanceTimersByTime(5_000);
    expect(live("chat").innerHTML).not.toBe(rest);
  });
});
