// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi, type Mock } from "vitest";
import type { MotionGate } from "../motion";
import { DESTS, HOLD_MS, HOLD_SECONDS, KEYS, REST, REST_LOG, dialDesc, gainName, keyDesc, type DeckStore } from "./state";

/* scene.ts imports three.js and needs WebGL, so it is replaced by a spy. Like the real one it puts a
 * <canvas> in the box when it mounts, which is how index.ts learns the 3D device is up. */
const mountDeck = vi.hoisted(() => vi.fn());
vi.mock("./scene", () => ({ mountDeck }));

import { initDeck } from "./index";

/* ------------------------------------------------------------------ fakes */

type Entry = { isIntersecting: boolean; intersectionRatio: number };
type Watcher = { el: Element; opts: IntersectionObserverInit; cb: (entries: Entry[]) => void };
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
    subscribers: () => subs.size,
  };
}

/** The scene trigger (whenNear) observes the box with a rootMargin; the show's visibility watch
 *  (whileVisible) observes the section with a threshold grid. */
const nearWatchers = () => watchers.filter((w) => w.opts.rootMargin !== undefined);
const visibleWatchers = () => watchers.filter((w) => w.opts.rootMargin === undefined);
const approach = () => {
  for (const w of nearWatchers()) w.cb([{ isIntersecting: true, intersectionRatio: 1 }]);
};
const see = (on: boolean) => {
  for (const w of visibleWatchers()) w.cb([{ isIntersecting: on, intersectionRatio: on ? 1 : 0 }]);
};

/* ----------------------------------------------------------------- markup */

/** The section as StreamDeck.astro renders it, down to the attributes index.ts queries: the box, the clock,
 *  the log with its four resting lines, the status line, one disabled row per key, two dial rows of four buttons. */
const DIAL_OPS = [
  ["down", "−"],
  ["up", "+"],
  ["push", "push"],
  ["zero", "0 dB"],
] as const;
const clockText = (t: number) => `${Math.floor(t / 60)}:${String(t % 60).padStart(2, "0")}`;

function fixture(): HTMLElement {
  document.body.innerHTML = `
<section id="deck" class="dk" data-deck aria-labelledby="h-deck">
  <div class="dk-box" data-deck-box role="group">
    <img class="dk-still" alt="">
    <div class="dk-tip" data-tip aria-hidden="true"></div>
    <p class="dk-hint" data-hint hidden>drag to orbit</p>
    <p class="dk-mode" data-mode hidden><i></i><span>show loop</span></p>
  </div>
  <div class="dk-sr" data-sr role="status" aria-live="polite"></div>
  <div class="dk-log">
    <div class="dk-hd"><h3>What just happened</h3><em data-clock>${clockText(REST_LOG[0].t)}</em></div>
    <ol data-log aria-label="Recent plugin actions">${REST_LOG.map(
      (e) => `<li class="${e.audio ? "audio" : ""}"><span class="t">${clockText(e.t)}</span><span class="v">${e.verb}</span><span class="m">${e.what} <i>${e.res}</i></span></li>`,
    ).join("")}</ol>
  </div>
  <div class="dk-acts">
    <h3><span>On the deck</span><em data-acts-hint></em></h3>
    <ul class="dk-rows">
      ${KEYS.map(
        (k, i) => `<li><button type="button" class="dk-r" data-key="${i}" disabled aria-label="${k.name}, ${k.kind === "hold" ? `hold ${HOLD_SECONDS} seconds` : "tap"}. ${keyDesc(REST, i)}" title="${k.tip}">
          <span class="n"><span>${k.name}</span></span><span class="d" data-desc>${keyDesc(REST, i)}</span><span class="c">${k.kind}</span></button></li>`,
      ).join("")}
      ${([0, 1] as const)
        .map(
          (dial) => `<li><div class="dk-r dial" data-dial="${dial}"><span class="n"><span>${gainName(dial)}</span></span>
          <span class="d db" data-db>${dialDesc(REST, dial)}</span>
          <span class="bs">${DIAL_OPS.map(([op, text]) => `<button type="button" data-op="${op}" disabled aria-label="${gainName(dial)} gain: ${op}">${text}</button>`).join("")}</span></div></li>`,
        )
        .join("")}
    </ul>
  </div>
</section>`;
  return document.querySelector<HTMLElement>("[data-deck]")!;
}

/* ---------------------------------------------------------------- readers */

const $ = <T extends Element = HTMLElement>(sel: string) => document.querySelector<T>(sel)!;
const keyRow = (i: number) => $<HTMLButtonElement>(`button[data-key="${i}"]`);
const dialBtn = (dial: number, op: string) => $<HTMLButtonElement>(`[data-dial="${dial}"] button[data-op="${op}"]`);
const logLines = () =>
  [...document.querySelectorAll("[data-log] li")].map((li) => ({
    verb: li.querySelector(".v")!.textContent,
    text: li.querySelector(".m")!.textContent!.replace(/\s+/g, " ").trim(),
    audio: li.classList.contains("audio"),
  }));
const top = () => logLines()[0];
const desc = (i: number) => keyRow(i).querySelector("[data-desc]")!.textContent;
const db = (dial: number) => $(`[data-dial="${dial}"] [data-db]`).textContent;
const mode = () => $("[data-mode]");

/** Lets the scene's dynamic import and mountDeck settle. */
const settle = async () => {
  await vi.dynamicImportSettled();
  await vi.advanceTimersByTimeAsync(0);
};

let g: ReturnType<typeof fakeGate>;
let disposers: Array<() => void>;
let sceneOff: Mock<() => void>;
let sceneStore: DeckStore;

/** Mounts the section. With `scene`, the section is near, the (mock) 3D device has mounted, and it is on screen. */
async function mount(opts: { motion?: boolean; scene?: boolean } = {}) {
  g = fakeGate(opts.motion ?? true);
  const root = fixture();
  const off = initDeck(root, g.gate);
  disposers.push(off);
  if (opts.scene) {
    approach();
    await settle();
    see(true);
  }
  return { root, off };
}

beforeEach(() => {
  vi.useFakeTimers();
  watchers = [];
  disposers = [];
  sceneOff = vi.fn<() => void>();
  mountDeck.mockReset();
  mountDeck.mockImplementation(async (box: HTMLElement, _gate: MotionGate, store: DeckStore) => {
    sceneStore = store;
    box.append(document.createElement("canvas"));
    return sceneOff;
  });
  (globalThis as unknown as { IntersectionObserver: unknown }).IntersectionObserver = class {
    private w: Watcher | undefined;
    constructor(private cb: Watcher["cb"], private opts: IntersectionObserverInit = {}) {}
    observe(el: Element) {
      this.w = { el, opts: this.opts, cb: this.cb };
      watchers.push(this.w);
    }
    unobserve() {}
    disconnect() {
      watchers = watchers.filter((w) => w !== this.w);
    }
  };
});

afterEach(() => {
  for (const off of disposers) off();
  vi.useRealTimers();
  document.body.innerHTML = "";
});

/* ----------------------------------------------------------------- the tests */

describe("the legend", () => {
  it("enables every row and writes the hint, which the server leaves out because the rows were dead without script", async () => {
    await mount();
    expect([...document.querySelectorAll<HTMLButtonElement>("button[data-key], button[data-op]")].every((b) => !b.disabled)).toBe(true);
    expect($("[data-acts-hint]").textContent).toBe("press a row to press that key");
  });

  it("rests in the state the still shows, with the four resting log lines", async () => {
    await mount();
    expect(logLines().map((l) => l.verb)).toEqual(REST_LOG.map((e) => e.verb));
    expect([3, 4, 5, 6, 7].map(desc)).toEqual(Array(5).fill("Live"));
    expect([db(0), db(1)]).toEqual(["+1.5 dB", "-3.0 dB"]);
  });

  describe("tap rows", () => {
    it("Go live starts every destination that is off, then they settle live a moment later", async () => {
      await mount();
      keyRow(1).click();
      vi.advanceTimersByTime(HOLD_MS);
      expect([3, 4, 5, 6, 7].map(desc)).toEqual(Array(5).fill("Off · tap starts it"));
      keyRow(0).click();
      expect(top()).toMatchObject({ verb: "tap", text: "Go live → 5 destinations starting" });
      expect(desc(3)).toBe("Starting");
      vi.advanceTimersByTime(1300);
      expect([3, 4, 5, 6, 7].map(desc)).toEqual(Array(5).fill("Live"));
    });

    it("Go live with everything already live says so", async () => {
      await mount();
      keyRow(0).click();
      expect(top().text).toBe("Go live → every destination is already live");
    });

    it("a destination row taps that destination: a live one ignores it, an off one starts", async () => {
      await mount();
      keyRow(4).click();
      expect(top()).toMatchObject({ verb: "tap", text: `${DESTS[1].name} → already live, tap ignored` });
      expect(desc(4)).toBe("Live");
      keyRow(1).click();
      vi.advanceTimersByTime(HOLD_MS);
      keyRow(4).click();
      expect(top().text).toBe(`${DESTS[1].name} → starting`);
      expect(desc(4)).toBe("Starting");
      expect(desc(3)).toBe("Off · tap starts it");
    });
  });

  describe("hold rows", () => {
    it("Stop all writes nothing and announces the hold at once, then stops everything when the hold completes", async () => {
      await mount();
      const before = logLines();
      keyRow(1).click();
      expect(logLines()).toEqual(before);
      expect($("[data-sr]").textContent).toBe("Holding Stop all");
      vi.advanceTimersByTime(HOLD_MS - 1);
      expect(logLines()).toEqual(before);
      vi.advanceTimersByTime(1);
      expect(top()).toMatchObject({ verb: "hold", text: "Stop all → 5 stopped (1 YouTube broadcast ended)", audio: false });
      expect([3, 4, 5, 6, 7].map(desc)).toEqual(Array(5).fill("Off · tap starts it"));
      expect($("[data-sr]").textContent).toBe("hold Stop all 5 stopped (1 YouTube broadcast ended)");
    });

    it("Routing preset applies to every destination when the hold completes, as an audio line", async () => {
      await mount();
      keyRow(2).click();
      expect($("[data-sr]").textContent).toBe("Holding Routing preset");
      vi.advanceTimersByTime(HOLD_MS);
      expect(top()).toMatchObject({ verb: "hold", text: "Routing preset → No music, applied to 5 destinations", audio: true });
    });

    it("pressing the other hold row while one is held does not start a second hold", async () => {
      await mount();
      keyRow(1).click();
      keyRow(2).click();
      vi.advanceTimersByTime(HOLD_MS * 2);
      expect(logLines().filter((l) => l.verb === "hold")).toHaveLength(1);
      expect(top().text).toContain("Stop all");
    });
  });

  describe("dials", () => {
    it("minus and plus turn the track down and up half a dB, and write an audio line each time", async () => {
      await mount();
      dialBtn(0, "down").click();
      expect(db(0)).toBe("+1.0 dB");
      expect(top()).toMatchObject({ verb: "turn", text: `${gainName(0)} → +1.0 dB, applied live`, audio: true });
      dialBtn(1, "up").click();
      expect(db(1)).toBe("-2.5 dB");
      expect(top().text).toBe(`${gainName(1)} → -2.5 dB, applied live`);
    });

    it("push mutes and unmutes, and the level shows MUTED while it is muted", async () => {
      await mount();
      dialBtn(1, "push").click();
      expect(top()).toMatchObject({ verb: "push", text: `${gainName(1)} → MUTED`, audio: true });
      expect(db(1)).toBe("MUTED · -3.0 dB");
      expect($(`[data-dial="1"] [data-db]`).classList.contains("mut")).toBe(true);
      dialBtn(1, "push").click();
      expect(top().text).toBe(`${gainName(1)} → unmuted`);
      expect($(`[data-dial="1"] [data-db]`).classList.contains("mut")).toBe(false);
    });

    it("0 dB puts the level back to zero", async () => {
      await mount();
      dialBtn(0, "zero").click();
      expect(top()).toMatchObject({ verb: "touch", text: `${gainName(0)} → 0.0 dB`, audio: true });
      expect(db(0)).toBe("0.0 dB");
    });
  });

  it("keeps only four log lines, newest first", async () => {
    await mount();
    for (let i = 0; i < 6; i++) dialBtn(0, "up").click();
    const lines = logLines();
    expect(lines).toHaveLength(4);
    expect(lines.map((l) => l.text)).toEqual(Array(4).fill(`${gainName(0)} → +4.5 dB, applied live`).map((t, n) => t.replace("+4.5", `+${(4.5 - n * 0.5).toFixed(1)}`)));
    expect(document.querySelectorAll("[data-log] li")).toHaveLength(4);
  });

  it("announces a visitor's action to a screen reader in the log's words", async () => {
    await mount();
    dialBtn(0, "zero").click();
    expect($("[data-sr]").textContent).toBe(`touch ${gainName(0)} 0.0 dB`);
  });

  it("keeps each key row's accessible name in step with its state", async () => {
    await mount();
    keyRow(1).click();
    vi.advanceTimersByTime(HOLD_MS);
    expect(keyRow(3).getAttribute("aria-label")).toBe(`${DESTS[0].name}, tap. Off · tap starts it`);
  });
});

describe("the scripted show", () => {
  it("does not play while motion is off, however visible the section is", async () => {
    await mount({ motion: false, scene: true });
    const before = logLines();
    vi.advanceTimersByTime(30_000);
    expect(logLines()).toEqual(before);
    expect(mode().hidden).toBe(true);
    expect([3, 4, 5, 6, 7].map(desc)).toEqual(Array(5).fill("Live"));
  });

  it("does not play until the section is on screen, and not without the 3D device", async () => {
    await mount();
    approach();
    await settle();
    vi.advanceTimersByTime(30_000);
    expect(logLines().map((l) => l.verb)).toEqual(REST_LOG.map((e) => e.verb));
    expect(mode().hidden).toBe(true);
  });

  it("starts from a reset state when it plays, and offers itself as a show loop", async () => {
    await mount({ scene: true });
    expect(mode().hidden).toBe(false);
    expect(mode().classList.contains("on")).toBe(true);
    expect(mode().querySelector("span")!.textContent).toBe("show loop");
    expect(document.querySelectorAll("[data-log] li")).toHaveLength(0);
    expect([3, 4, 5, 6, 7].map(desc)).toEqual(Array(5).fill("Off · tap starts it"));
    expect([db(0), db(1)]).toEqual(["0.0 dB", "0.0 dB"]);
  });

  it("plays its steps in order, each when its time comes", async () => {
    await mount({ scene: true });
    const seen: string[] = [];
    const watch = (s: number) => {
      vi.advanceTimersByTime(s * 1000);
      seen.push(`${top().verb}|${top().text}`);
    };
    vi.advanceTimersByTime(1100);
    expect(document.querySelectorAll("[data-log] li")).toHaveLength(0);
    watch(0.2); // 1.3 s: go live
    watch(1.5); // 2.8 s: Twitch fails to start
    watch(5.7); // 8.5 s: the second dial is turned down
    watch(2.4); // 10.9 s: the first dial turned up
    expect(seen).toEqual([
      "tap|Go live → 5 destinations starting",
      `fail|${DESTS[1].name} → Failed: Timed out`,
      `turn|${gainName(1)} → -3.0 dB, applied live`,
      `turn|${gainName(0)} → +1.5 dB, applied live`,
    ]);
    expect(desc(4)).toBe("Live");
    expect(db(1)).toBe("-3.0 dB");
  });

  it("announces its own steps to nobody: the screen reader hears only the visitor", async () => {
    await mount({ scene: true });
    vi.advanceTimersByTime(9000);
    expect($("[data-sr]").textContent).toBe("");
  });

  it("starts over from a reset state each cycle", async () => {
    await mount({ scene: true });
    vi.advanceTimersByTime(25_900);
    expect(desc(7)).toBe("Live"); // the last step brought the last destination back
    vi.advanceTimersByTime(400);
    expect(document.querySelectorAll("[data-log] li")).toHaveLength(0);
    expect([3, 4, 5, 6, 7].map(desc)).toEqual(Array(5).fill("Off · tap starts it"));
    vi.advanceTimersByTime(1000);
    expect(top().text).toBe("Go live → 5 destinations starting");
  });

  it("pauses when motion stops, resting in the complete state, and plays again from the top when it resumes", async () => {
    await mount({ scene: true });
    vi.advanceTimersByTime(9000);
    g.set(false);
    expect(logLines().map((l) => l.verb)).toEqual(REST_LOG.map((e) => e.verb));
    expect([3, 4, 5, 6, 7].map(desc)).toEqual(Array(5).fill("Live"));
    expect([db(0), db(1)]).toEqual(["+1.5 dB", "-3.0 dB"]);
    expect(mode().hidden).toBe(true);
    const frozen = logLines();
    vi.advanceTimersByTime(30_000);
    expect(logLines()).toEqual(frozen);
    expect(vi.getTimerCount()).toBe(0);
    g.set(true);
    vi.advanceTimersByTime(1300);
    expect(top().text).toBe("Go live → 5 destinations starting");
    expect(mode().hidden).toBe(false);
  });

  it("pauses when the section leaves the screen, and plays again when it returns", async () => {
    await mount({ scene: true });
    vi.advanceTimersByTime(3000);
    see(false);
    expect(logLines().map((l) => l.verb)).toEqual(REST_LOG.map((e) => e.verb));
    vi.advanceTimersByTime(10_000);
    expect(logLines().map((l) => l.verb)).toEqual(REST_LOG.map((e) => e.verb));
    see(true);
    vi.advanceTimersByTime(1300);
    expect(top().verb).toBe("tap");
  });

  it("pauses while the tab is hidden", async () => {
    await mount({ scene: true });
    vi.advanceTimersByTime(3000);
    const vis = vi.spyOn(document, "visibilityState", "get").mockReturnValue("hidden");
    document.dispatchEvent(new Event("visibilitychange"));
    expect(logLines().map((l) => l.verb)).toEqual(REST_LOG.map((e) => e.verb));
    vis.mockRestore();
  });

  describe("once the visitor takes over", () => {
    const stopped = async (act: () => void) => {
      await mount({ scene: true });
      vi.advanceTimersByTime(2000);
      act();
      const after = logLines();
      vi.advanceTimersByTime(40_000);
      return { after };
    };

    it("operating a key stops the show for good: no more steps, and it does not restart on a pause and resume or a scroll", async () => {
      const { after } = await stopped(() => keyRow(0).click());
      expect(logLines().length).toBeGreaterThan(0);
      expect(top().verb).toBe("tap");
            g.set(false);
      g.set(true);
      see(false);
      see(true);
      vi.advanceTimersByTime(40_000);
      expect(logLines().map((l) => l.text)).toEqual(after.map((l) => l.text));
    });

    it("operating a dial stops the show", async () => {
      await mount({ scene: true });
      vi.advanceTimersByTime(2000);
      dialBtn(0, "up").click();
      const after = logLines();
      vi.advanceTimersByTime(40_000);
      expect(logLines()).toEqual(after);
    });

    it("focus entering a legend row stops the show without pressing anything", async () => {
      const { after } = await stopped(() => keyRow(2).dispatchEvent(new FocusEvent("focusin", { bubbles: true })));
      expect(top().text).toBe(after[0].text);
      expect(logLines().map((l) => l.text)).toEqual(after.map((l) => l.text));
    });

    it("focus entering a dial row stops the show", async () => {
      const { after } = await stopped(() => $(`[data-dial="1"]`).dispatchEvent(new FocusEvent("focusin", { bubbles: true })));
      expect(logLines().map((l) => l.text)).toEqual(after.map((l) => l.text));
    });

    it("says 'you're driving' while the 3D device is up and motion is allowed, and nothing when it is not", async () => {
      await mount({ scene: true });
      keyRow(0).click();
      expect(mode().hidden).toBe(false);
      expect(mode().querySelector("span")!.textContent).toBe("you're driving");
      g.set(false);
      expect(mode().hidden).toBe(true);
    });

    it("the show's settle timers do not fire into the visitor's state", async () => {
      await mount({ scene: true });
      vi.advanceTimersByTime(1300); // the show's Go live: every destination is starting
      expect(desc(3)).toBe("Starting");
      dialBtn(0, "up").click(); // the visitor takes over: what was starting is finished at once
      expect(desc(3)).toBe("Live");
      vi.advanceTimersByTime(5000);
      expect([3, 4, 5, 6, 7].map(desc)).toEqual(Array(5).fill("Live"));
    });

    it("a hold the show had started is put back up, not left on a confirm face", async () => {
      await mount({ scene: true });
      vi.advanceTimersByTime(16_500);
      expect(sceneStore.get().held).toBe("preset");
      dialBtn(0, "up").click();
      expect(sceneStore.get().held).toBeNull();
      vi.advanceTimersByTime(5000);
      expect(logLines().some((l) => l.text.startsWith("Routing preset"))).toBe(false);
    });
  });
});

describe("the 3D scene", () => {
  it("mounts nothing until the section is near", async () => {
    await mount();
    await settle();
    expect(mountDeck).not.toHaveBeenCalled();
    expect(nearWatchers()).toHaveLength(1);
    expect(nearWatchers()[0].el).toBe($("[data-deck-box]"));
  });

  it("mounts once, in the box, with the gate, when the section approaches, however many times it reports near", async () => {
    await mount();
    approach();
    approach();
    await settle();
    expect(mountDeck).toHaveBeenCalledTimes(1);
    expect(mountDeck.mock.calls[0][0]).toBe($("[data-deck-box]"));
    expect(mountDeck.mock.calls[0][1]).toBe(g.gate);
  });

  it("gives the scene a store that reads the legend's state and drives it as the visitor", async () => {
    await mount({ scene: true });
    // The show is playing; the scene is a visitor the moment it dispatches.
    vi.advanceTimersByTime(2000);
    sceneStore.dispatch({ type: "gain", dial: 0, steps: 2 });
    expect(sceneStore.get().gain[0]).toBe(1);
    expect(top()).toMatchObject({ verb: "turn", text: `${gainName(0)} → +1.0 dB, applied live` });
    expect(db(0)).toBe("+1.0 dB");
    expect($("[data-sr]").textContent).toBe(`turn ${gainName(0)} +1.0 dB, applied live`);
    const before = logLines();
    vi.advanceTimersByTime(40_000);
    expect(logLines()).toEqual(before); // and the show is over
  });

  it("the scene's store notifies subscribers when the legend changes the state", async () => {
    await mount({ scene: true });
    const seen = vi.fn();
    sceneStore.subscribe(seen);
    dialBtn(1, "push").click();
    expect(seen).toHaveBeenCalled();
    expect(sceneStore.get().muted[1]).toBe(true);
  });

  it("with the scene failing to mount, the legend still drives the log, nothing throws, and no show is offered", async () => {
    mountDeck.mockRejectedValue(new Error("no webgl"));
    const unhandled = vi.fn();
    process.on("unhandledRejection", unhandled);
    await mount();
    approach();
    await settle();
    see(true);
    expect(mountDeck).toHaveBeenCalledTimes(1);
    vi.advanceTimersByTime(30_000);
    expect(logLines().map((l) => l.verb)).toEqual(REST_LOG.map((e) => e.verb));
    expect(mode().hidden).toBe(true);
    dialBtn(0, "down").click();
    keyRow(0).click();
    expect(logLines().map((l) => l.text)).toEqual(["Go live → every destination is already live", `${gainName(0)} → +1.0 dB, applied live`, ...REST_LOG.slice(0, 2).map((e) => `${e.what} ${e.res}`.replace(/\s+/g, " "))]);
    await settle();
    process.off("unhandledRejection", unhandled);
    expect(unhandled).not.toHaveBeenCalled();
  });

  it("a scene that mounts without a canvas (the still stays) offers no show", async () => {
    mountDeck.mockImplementation(async () => sceneOff);
    await mount();
    approach();
    await settle();
    see(true);
    vi.advanceTimersByTime(30_000);
    expect(logLines().map((l) => l.verb)).toEqual(REST_LOG.map((e) => e.verb));
    expect(mode().hidden).toBe(true);
  });

  it("when the scene reports its context lost, the show stops, the legend rests, and the controls keep writing the log", async () => {
    await mount({ scene: true });
    vi.advanceTimersByTime(9000);
    $("[data-deck-box]").dispatchEvent(new CustomEvent("deck:down"));
    expect(logLines().map((l) => l.verb)).toEqual(REST_LOG.map((e) => e.verb));
    expect([3, 4, 5, 6, 7].map(desc)).toEqual(Array(5).fill("Live"));
    expect(mode().hidden).toBe(true);
    vi.advanceTimersByTime(30_000);
    expect(logLines().map((l) => l.verb)).toEqual(REST_LOG.map((e) => e.verb));
    dialBtn(0, "up").click();
    expect(top().verb).toBe("turn");
  });
});

describe("the teardown", () => {
  it("calls the scene's teardown and leaves no timer, observer or gate subscription behind", async () => {
    const { off } = await mount({ scene: true });
    keyRow(0).click(); // a settle timer in flight
    vi.advanceTimersByTime(100);
    expect(vi.getTimerCount()).toBeGreaterThan(0);
    off();
    expect(sceneOff).toHaveBeenCalledTimes(1);
    expect(vi.getTimerCount()).toBe(0);
    expect(watchers).toHaveLength(0);
    expect(g.subscribers()).toBe(0);
    const before = logLines();
    vi.advanceTimersByTime(60_000);
    expect(logLines()).toEqual(before);
  });

  it("stops the show mid-play: nothing more is written", async () => {
    const { off } = await mount({ scene: true });
    vi.advanceTimersByTime(3000);
    off();
    // The section is left resting in its complete state, and the show writes nothing more.
    const resting = logLines();
    expect(resting.map((l) => l.verb)).toEqual(REST_LOG.map((e) => e.verb));
    vi.advanceTimersByTime(60_000);
    expect(logLines()).toEqual(resting);
    expect(vi.getTimerCount()).toBe(0);
  });

  it("stops watching the section before the scene is ever near", async () => {
    const { off } = await mount();
    off();
    expect(watchers).toHaveLength(0);
    approach();
    await settle();
    expect(mountDeck).not.toHaveBeenCalled();
  });

  it("a scene that finishes mounting after the teardown is torn down at once", async () => {
    let finish!: (off: () => void) => void;
    mountDeck.mockImplementation(
      () =>
        new Promise((r) => {
          finish = r;
        }),
    );
    const { off } = await mount();
    approach();
    await settle();
    off();
    finish(sceneOff);
    await settle();
    expect(sceneOff).toHaveBeenCalledTimes(1);
  });

  it("no longer hears the scene's context loss", async () => {
    const { off } = await mount({ scene: true });
    off();
    dialBtn(0, "up").click();
    const before = logLines();
    $("[data-deck-box]").dispatchEvent(new CustomEvent("deck:down"));
    expect(logLines()).toEqual(before);
  });
});

describe("a page without the optional parts", () => {
  it("works with no box, log, clock, status or mode elements: the rows still drive the store", async () => {
    g = fakeGate(true);
    document.body.innerHTML = `<section data-deck><button data-key="0" disabled></button><button data-key="3" disabled></button></section>`;
    const off = initDeck(document.querySelector("[data-deck]")!, g.gate);
    disposers.push(off);
    expect(() => document.querySelector<HTMLButtonElement>('[data-key="0"]')!.click()).not.toThrow();
    expect(watchers.filter((w) => w.opts.rootMargin !== undefined)).toHaveLength(0);
  });
});
