/* Hero behaviour: the case tabs, the fine print and the auto-advance.
 *
 * The server renders every case panel (only the first visible) and the first
 * case's fine print, so the page is complete without this. This wires the tabs
 * through the shared controller, keeps [data-fine] on the current case, and
 * steps through the cases every 12 s while motion is allowed and the hero is on
 * screen; the same gate moves the level meters and the ON AIR clock. The first pointer or key press inside the hero ends the stepping for
 * good: the visitor has taken over. Pause itself is wired by boot() alone.
 *
 * Each change is announced as a `case:change` event on the root. The globe
 * replaces the poster inside [data-globe]: its module (and three.js with it) is
 * imported only once the box is near the viewport, which is after boot has run,
 * so the poster is what paints first. This file forwards each change to it. */
import { CASES, CASE_ORDER } from "../../data/cases.mjs";
import { initTabs } from "../tabs";
import { clockText, meterLevel } from "./hero-model";
import { whenNear } from "./lazy";
import { whileVisible, type MotionGate } from "./motion";
import type { Case } from "./globe";

const ADVANCE_MS = 12_000;
const METER_MS = 100;
/* Meters are numbered by destination within a case (every case has five). */
const DESTS = 5;

export function initHero(root: HTMLElement, gate: MotionGate): () => void {
  const tablist = root.querySelector<HTMLElement>("[data-case-tabs]");
  if (!tablist) return () => {};
  const fine = root.querySelector<HTMLElement>("[data-fine]");

  let current: string = CASE_ORDER[0];
  let tabs: ReturnType<typeof initTabs> | null = null;

  const onSelect = (key: string) => {
    current = key;
    // textContent, never innerHTML: the string is data, not markup.
    const text = (CASES as Record<string, { fine: string } | undefined>)[key]?.fine;
    if (fine && text !== undefined) fine.textContent = text;
    root.dispatchEvent(new CustomEvent("case:change", { detail: { key } }));
  };
  tabs = initTabs(tablist, onSelect);

  let timer: ReturnType<typeof setInterval> | undefined;
  let taken = false; // the visitor has used the hero: no more stepping

  const clearAdvance = () => {
    if (timer !== undefined) clearInterval(timer);
    timer = undefined;
  };
  const startAdvance = () => {
    if (taken || timer !== undefined) return;
    timer = setInterval(() => {
      const next = CASE_ORDER[(CASE_ORDER.indexOf(current as (typeof CASE_ORDER)[number]) + 1) % CASE_ORDER.length];
      tabs?.select(next);
    }, ADVANCE_MS);
  };

  /* The meters and the ON AIR clock: a picture of the product, moving only
   * while the gate allows. Stopping freezes them where they are; the server
   * markup is the fixed, complete state with motion off. The time is a counter
   * we advance ourselves, so the motion is a pure function of ticks. */
  const meters = [...root.querySelectorAll<HTMLElement>("[data-meter]")];
  const clocks = [...root.querySelectorAll<HTMLElement>("[data-clock]")];
  let meterTimer: ReturnType<typeof setInterval> | undefined;
  let clockTimer: ReturnType<typeof setInterval> | undefined;
  let t = 0;
  let seconds = 0;
  const startMotion = () => {
    if (meterTimer === undefined && meters.length) {
      meterTimer = setInterval(() => {
        t += METER_MS / 1000;
        meters.forEach((m, i) => {
          m.style.transform = `scaleX(${meterLevel(i % DESTS, t).toFixed(3)})`;
        });
      }, METER_MS);
    }
    if (clockTimer === undefined && clocks.length) {
      clockTimer = setInterval(() => {
        seconds += 1;
        for (const el of clocks) el.textContent = clockText(seconds);
      }, 1000);
    }
  };
  const stopMotion = () => {
    if (meterTimer !== undefined) clearInterval(meterTimer);
    if (clockTimer !== undefined) clearInterval(clockTimer);
    meterTimer = clockTimer = undefined;
  };

  const start = () => {
    startAdvance();
    startMotion();
  };
  const stop = () => {
    clearAdvance();
    stopMotion();
  };
  const unwatch = whileVisible(root, gate, start, stop);

  /* The globe. The case can change while the module is still loading, so the
   * handle is told the current case once it exists; and a globe that arrives
   * after the disposer ran is destroyed on arrival. */
  const caseOf = (key: string) => (CASES as Record<string, Case>)[key];
  const globeEl = root.querySelector<HTMLElement>("[data-globe]");
  let globe: Awaited<ReturnType<typeof import("./globe").mountGlobe>> | null = null;
  let disposed = false;
  const unnear = globeEl
    ? whenNear(globeEl, async () => {
        const first = current;
        const { mountGlobe } = await import("./globe");
        const g = await mountGlobe(globeEl, gate, caseOf(first));
        if (disposed) {
          g.destroy();
          return;
        }
        globe = g;
        if (current !== first) g.setCase(caseOf(current));
      })
    : () => {};
  const onCase = (e: Event) => globe?.setCase(caseOf((e as CustomEvent<{ key: string }>).detail.key));
  root.addEventListener("case:change", onCase);

  const takeOver = (e: Event) => {
    /* Pause lives inside the hero but is a page control, not a visitor taking
     * the cases over: pressing it must not end the auto-advance for good. */
    if (e.target instanceof Element && e.target.closest("[data-pause]")) return;
    taken = true;
    clearAdvance();
  };
  const events = ["pointerdown", "keydown", "click"] as const;
  for (const e of events) root.addEventListener(e, takeOver);

  return () => {
    for (const e of events) root.removeEventListener(e, takeOver);
    disposed = true;
    root.removeEventListener("case:change", onCase);
    unnear();
    globe?.destroy();
    globe = null;
    unwatch();
    stop();
    tabs?.destroy();
  };
}
