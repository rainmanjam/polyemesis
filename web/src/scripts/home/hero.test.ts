// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { MotionGate } from "./motion";
import { initHero, upgradeToButton, type HeroController } from "./hero";
import { STORIES, STORY_ORDER, destLabel, statusFor, type StoryKey } from "./stories";

let allowed: boolean;
let userPaused: boolean;
let subs: Array<(a: boolean) => void>;
let gate: MotionGate;
let observers: Array<{ el: Element; cb: (e: Array<{ isIntersecting: boolean }>) => void }>;
let hero: HeroController | null;

class StubIO {
  constructor(private cb: (e: Array<{ isIntersecting: boolean }>) => void) {}
  observe(el: Element) {
    observers.push({ el, cb: this.cb });
  }
  disconnect() {
    observers = observers.filter((o) => o.cb !== this.cb);
  }
  unobserve() {}
}

function panelHtml(k: StoryKey): string {
  const s = STORIES[k];
  const chips = [`<li class="si" data-src="0"></li>`]
    .concat(s.tracks.map((_, i) => `<li class="si" data-src="${i + 1}"></li>`))
    .join("");
  const lines = [0, 1, 2, 3, 4].map((i) => `<path class="ln" data-from="${i}" data-to="hub"></path>`).join("") +
    s.dests.map((d) => `<path class="ln" data-from="hub" data-to="${d.id}"></path>`).join("");
  const dests = s.dests
    .map(
      (d) =>
        `<li><div class="dest" data-dest="${d.id}" data-on="1" data-label="${destLabel(s, d)}">` +
        `<span class="dest-name">${d.name}</span><span class="dest-sub">${d.sub}</span><i class="pip"></i></div></li>`,
    )
    .join("");
  return `<div class="dg" data-dg data-story="${k}"><svg>${lines}</svg><ul>${chips}</ul><ul>${dests}</ul></div>`;
}

function build(): HTMLElement {
  document.body.innerHTML = `
    <section data-hero>
      <div role="tablist" data-story-tabs hidden>
        ${STORY_ORDER.map((k) => `<button type="button" role="tab" id="tab-${k}" data-story="${k}" aria-controls="panel-${k}" aria-selected="${k === "streamer"}">${STORIES[k].tab}</button>`).join("")}
      </div>
      ${STORY_ORDER.map((k) => `<div role="tabpanel" id="panel-${k}" aria-labelledby="tab-${k}"${k === "streamer" ? "" : " hidden"}>${panelHtml(k)}</div>`).join("")}
      <p data-status aria-live="polite"></p>
      <p data-fine></p>
      <button type="button" data-pause hidden aria-pressed="false">Pause motion</button>
    </section>`;
  return document.querySelector<HTMLElement>("[data-hero]")!;
}

const $ = <T extends HTMLElement = HTMLElement>(sel: string) => document.querySelector<T>(sel)!;
const dest = (k: StoryKey, id: string) => $<HTMLButtonElement>(`#panel-${k} .dest[data-dest="${id}"]`);
const status = () => $("[data-status]");
const key = (el: Element, k: string) => el.dispatchEvent(new KeyboardEvent("keydown", { key: k, bubbles: true }));

beforeEach(() => {
  allowed = true;
  userPaused = false;
  subs = [];
  observers = [];
  vi.stubGlobal("IntersectionObserver", StubIO);
  gate = {
    allowed: () => allowed,
    userPaused: () => userPaused,
    setUserPaused: (p) => {
      userPaused = p;
      allowed = !p;
      subs.forEach((f) => f(allowed));
    },
    subscribe: (fn) => {
      subs.push(fn);
      return () => {
        subs = subs.filter((f) => f !== fn);
      };
    },
  };
  hero = null;
});

afterEach(() => {
  hero?.destroy();
  vi.useRealTimers();
  vi.unstubAllGlobals();
  document.body.innerHTML = "";
});

function start(): HTMLElement {
  const root = build();
  hero = initHero(root, gate);
  return root;
}

describe("initHero", () => {
  it("un-hides the tablist and pause only once JS runs", () => {
    const root = build();
    expect($("[data-story-tabs]").hidden).toBe(true);
    expect($("[data-pause]").hidden).toBe(true);
    hero = initHero(root, gate);
    expect($("[data-story-tabs]").hidden).toBe(false);
    expect($("[data-pause]").hidden).toBe(false);
  });

  it("upgrades destinations to buttons, keeping classes, data and label", () => {
    const root = build();
    const before = root.querySelector<HTMLElement>("#panel-worship .dest")!;
    const label = before.getAttribute("data-label");
    const kids = before.children.length;
    hero = initHero(root, gate);
    const after = root.querySelector<HTMLElement>("#panel-worship .dest")!;
    expect(after.tagName).toBe("BUTTON");
    expect(after.getAttribute("type")).toBe("button");
    expect(after.getAttribute("aria-pressed")).toBe("false");
    expect(after.getAttribute("aria-label")).toBe(label);
    expect(after.className).toBe("dest");
    expect(after.dataset.dest).toBe("en");
    expect(after.children.length).toBe(kids);
    expect(root.querySelectorAll("div.dest").length).toBe(0);
  });

  it("upgradeToButton keeps the element's place in the DOM", () => {
    const root = build();
    const el = root.querySelector<HTMLElement>(".dest")!;
    const parent = el.parentElement!;
    const btn = upgradeToButton(el);
    expect(btn.parentElement).toBe(parent);
    expect(el.isConnected).toBe(false);
  });

  it("plays the visible diagram only while it intersects", () => {
    start();
    const dg = $("#panel-streamer .dg");
    expect(dg.classList.contains("is-playing")).toBe(false);
    const io = () => observers.find((o) => o.el === dg)!;
    io().cb([{ isIntersecting: true }]);
    expect(dg.classList.contains("is-playing")).toBe(true);
    io().cb([{ isIntersecting: false }]);
    expect(dg.classList.contains("is-playing")).toBe(false);

    io().cb([{ isIntersecting: true }]);
    vi.useFakeTimers();
    hero!.setStory("sports");
    vi.advanceTimersByTime(150);
    expect(dg.classList.contains("is-playing")).toBe(false);
    const dg2 = $("#panel-sports .dg");
    observers.find((o) => o.el === dg2)!.cb([{ isIntersecting: true }]);
    expect(dg2.classList.contains("is-playing")).toBe(true);
  });

  it("writes the caption plus hint on init", () => {
    start();
    expect(status().textContent).toBe(statusFor(STORIES.streamer, null, true).text);
    expect(status().textContent).toContain("Tap a destination");
  });

  it("pins on click and reports YouTube's mix", () => {
    start();
    dest("streamer", "youtube").click();
    expect(dest("streamer", "youtube").getAttribute("aria-pressed")).toBe("true");
    expect(status().querySelector("b")!.textContent).toBe("YouTube");
    expect(status().textContent).toContain("Music stays off.");
    expect(dest("streamer", "twitch").classList.contains("dim")).toBe(true);
    expect(dest("streamer", "youtube").classList.contains("is-solo")).toBe(true);
  });

  it("Enter and Space pin, Escape clears", () => {
    start();
    const yt = dest("streamer", "youtube");
    // A real button turns Enter and Space into a click; jsdom does not, so the
    // click stands in for the browser's synthesis.
    yt.focus();
    key(yt, "Enter");
    yt.click();
    expect(yt.getAttribute("aria-pressed")).toBe("true");
    key(yt, "Escape");
    expect(yt.getAttribute("aria-pressed")).toBe("false");
    expect(status().textContent).toBe(statusFor(STORIES.streamer, null, true).text);
    key(yt, " ");
    yt.click();
    expect(yt.getAttribute("aria-pressed")).toBe("true");
    expect(dest("streamer", "twitch").classList.contains("dim")).toBe(true);
    key(yt, "Escape");
    expect(dest("streamer", "twitch").classList.contains("dim")).toBe(false);
  });

  it("clicking the pinned destination again unpins it", () => {
    start();
    const yt = dest("streamer", "youtube");
    yt.click();
    yt.click();
    expect(yt.getAttribute("aria-pressed")).toBe("false");
    expect(status().querySelector("b")).toBeNull();
  });

  it("previews on mouse hover without pinning", () => {
    start();
    const yt = dest("streamer", "youtube");
    const over = new MouseEvent("pointerover", { bubbles: true });
    Object.defineProperty(over, "pointerType", { value: "mouse" });
    yt.dispatchEvent(over);
    expect(status().querySelector("b")!.textContent).toBe("YouTube");
    expect(yt.getAttribute("aria-pressed")).toBe("false");
    yt.dispatchEvent(new MouseEvent("pointerout", { bubbles: true }));
    expect(status().querySelector("b")).toBeNull();
  });

  it("dims the music chip when YouTube is soloed", () => {
    start();
    dest("streamer", "youtube").click();
    expect($('#panel-streamer li.si[data-src="2"]').classList.contains("dim")).toBe(true);
    expect($('#panel-streamer li.si[data-src="1"]').classList.contains("dim")).toBe(false);
    expect($('#panel-streamer .ln[data-from="2"]').classList.contains("dim")).toBe(true);
    expect($('#panel-streamer .ln[data-to="twitch"]').classList.contains("dim")).toBe(true);
    expect($('#panel-streamer .ln[data-to="youtube"]').classList.contains("dim")).toBe(false);
  });

  it("dims the video chip for a destination without video", () => {
    start();
    allowed = false;
    hero!.setStory("worship");
    dest("worship", "audio").click();
    expect($('#panel-worship li.si[data-src="0"]').classList.contains("dim")).toBe(true);
  });

  it("ArrowRight moves to Sports, selects it and shows its panel", () => {
    vi.useFakeTimers();
    start();
    const streamer = $("#tab-streamer");
    streamer.focus();
    key(streamer, "ArrowRight");
    vi.advanceTimersByTime(150);
    const sports = $("#tab-sports");
    expect(sports.getAttribute("aria-selected")).toBe("true");
    expect(streamer.getAttribute("aria-selected")).toBe("false");
    expect(document.activeElement).toBe(sports);
    expect($("#panel-sports").hidden).toBe(false);
    expect($("#panel-streamer").hidden).toBe(true);
    expect(sports.getAttribute("tabindex")).toBe("0");
    expect(streamer.getAttribute("tabindex")).toBe("-1");
    expect($("#tab-worship").getAttribute("tabindex")).toBe("-1");
    expect(hero!.story()).toBe("sports");
  });

  it("ArrowLeft and ArrowRight wrap", () => {
    allowed = false;
    start();
    key($("#tab-streamer"), "ArrowLeft");
    expect(hero!.story()).toBe("worship");
    key($("#tab-worship"), "ArrowRight");
    expect(hero!.story()).toBe("streamer");
  });

  it("End/Home jump to Worship/Streamer", () => {
    allowed = false;
    start();
    key($("#tab-streamer"), "End");
    expect(hero!.story()).toBe("worship");
    expect($("#panel-worship").hidden).toBe(false);
    key($("#tab-worship"), "Home");
    expect(hero!.story()).toBe("streamer");
    expect($("#panel-streamer").hidden).toBe(false);
  });

  it("story switch clears the pin and swaps the fine print", () => {
    allowed = false;
    start();
    dest("streamer", "youtube").click();
    $("#tab-worship").click();
    expect($("[data-fine]").textContent).toMatch(/doesn't license music/);
    expect(status().textContent).toBe(statusFor(STORIES.worship, null, true).text);
    hero!.setStory("streamer");
    expect(dest("streamer", "youtube").getAttribute("aria-pressed")).toBe("false");
    expect(dest("streamer", "twitch").classList.contains("dim")).toBe(false);
  });

  it("writes status with textContent, never markup", () => {
    start();
    const original = STORIES.streamer.dests[1].name;
    try {
      STORIES.streamer.dests[1].name = "<img src=x>";
      dest("streamer", "youtube").click();
      const b = status().querySelector("b")!;
      expect(b.textContent).toBe("<img src=x>");
      expect(status().querySelector("img")).toBeNull();
    } finally {
      STORIES.streamer.dests[1].name = original;
    }
  });

  it("runs the swap animation only when motion is allowed", () => {
    vi.useFakeTimers();
    start();
    hero!.setStory("sports");
    expect($("#panel-streamer").classList.contains("swap-out")).toBe(true);
    expect($("#panel-streamer").hidden).toBe(false);
    vi.advanceTimersByTime(150);
    expect($("#panel-streamer").classList.contains("swap-out")).toBe(false);
    expect($("#panel-streamer").hidden).toBe(true);
  });

  it("skips the swap animation when motion is not allowed", () => {
    allowed = false;
    start();
    const add = vi.spyOn(DOMTokenList.prototype, "add");
    hero!.setStory("sports");
    expect(add.mock.calls.some((c) => c.includes("swap-out"))).toBe(false);
    expect($("#panel-streamer").hidden).toBe(true);
    expect($("#panel-sports").hidden).toBe(false);
    add.mockRestore();
  });

  it("finishes a pending swap when the visitor switches again", () => {
    vi.useFakeTimers();
    start();
    hero!.setStory("sports");
    hero!.setStory("worship");
    vi.advanceTimersByTime(150);
    expect($("#panel-worship").hidden).toBe(false);
    expect($("#panel-sports").hidden).toBe(true);
    expect($("#panel-streamer").hidden).toBe(true);
  });

  it("does not bind pause itself (boot owns it)", () => {
    start();
    $("[data-pause]").click();
    expect(userPaused).toBe(false);
  });
});
