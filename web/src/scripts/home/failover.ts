/* The recovery beat: a four-stage illustration of what failover does when the
 * encoder drops. It is a diagram, not telemetry, so the log has no timestamps
 * and the copy says so (aria-label "Event log, illustrative").
 *
 * The root is [data-failover]. Markup contract (Recovery.astro owns it):
 *   [data-fo-controls]      hidden in the server render, shown here, so no
 *                           control sits on the page doing nothing before JS
 *   button[data-stage="i"]  one per stage, steppable by hand
 *   [data-play]             "Pull the cable", replays from the drop
 *   [data-frame]            gets data-state = the stage id; autoplay watches it
 *   [data-enc]              encoder text
 *   [data-dest]             one per destination, text is "live" or "slate"
 *   [data-log]              the ol that receives one line per stage. It is
 *                           aria-live="polite" only while the visitor drives it;
 *                           autoplay is never announced.
 *
 * When the gate disallows motion (reduced motion, or Pause) nothing is timed:
 * the stage buttons step at once and "Pull the cable" jumps straight to the
 * slate holding the stream. A timed run stopped part-way (Pause, scrolling
 * away, a hidden tab) puts "live" back, so the beat never rests on a failure.
 */

import { whileVisible, type MotionGate } from "./motion";

export interface Stage {
  id: "live" | "drop" | "slate" | "recovered";
  label: string;
  ms: number;
  encoder: string;
  dest: "live" | "slate";
  log: string;
}

export const STAGES: readonly Stage[] = [
  { id: "live", label: "Live", ms: 2600, encoder: "Encoder connected", dest: "live", log: "Encoder connected. All destinations live." },
  { id: "drop", label: "Encoder drops", ms: 1800, encoder: "Encoder lost", dest: "live", log: "Ingest lost." },
  { id: "slate", label: "Slate on air", ms: 3400, encoder: "Encoder lost, slate holding", dest: "slate", log: "Slate on air for Twitch, YouTube and Kick." },
  { id: "recovered", label: "Recovered", ms: 2400, encoder: "Encoder back", dest: "live", log: "Encoder back. Programme restored." },
];

/** Pause before the first autoplay, so the visitor sees the live state first. */
const LEAD_IN_MS = 700;
/** Where "Pull the cable" lands when nothing may move: the slate holding. */
const SLATE = STAGES.findIndex((s) => s.id === "slate");

export function initFailover(root: HTMLElement, gate: MotionGate): { play(): void; show(i: number): void; destroy(): void } {
  const frame = root.querySelector<HTMLElement>("[data-frame]");
  const enc = root.querySelector<HTMLElement>("[data-enc]");
  const log = root.querySelector<HTMLElement>("[data-log]");
  const dests = [...root.querySelectorAll<HTMLElement>("[data-dest]")];
  const steps = [...root.querySelectorAll<HTMLButtonElement>("button[data-stage]")];
  const playBtn = root.querySelector<HTMLButtonElement>("[data-play]");
  const doc = root.ownerDocument;

  for (const el of root.querySelectorAll<HTMLElement>("[data-fo-controls]")) el.hidden = false;

  let timer: ReturnType<typeof setTimeout> | undefined;
  /* True once a run reached "recovered" or the visitor took over by hand. An
   * interrupted autoplay (scrolled away, tab hidden, motion paused) is not
   * done, so the next start replays it from the top. */
  let autoplayed = false;
  /* A timed run is showing a stage past "live" and before "recovered". */
  let inFlight = false;
  let shown = -1;

  const stopTimer = () => {
    clearTimeout(timer);
    timer = undefined;
  };

  const announce = (on: boolean) => log?.setAttribute("aria-live", on ? "polite" : "off");

  function show(i: number): void {
    const s = STAGES[i];
    if (!s) return;
    if (frame) frame.dataset.state = s.id;
    if (enc) enc.textContent = s.encoder;
    for (const d of dests) d.textContent = s.dest;
    steps.forEach((b, k) => {
      if (k === i) b.setAttribute("aria-current", "step");
      else b.removeAttribute("aria-current");
    });
    if (log) {
      /* Append on a forward step so a live region announces only the new line;
       * rebuild on anything else (first show, same stage, backward, a jump). */
      const line = (st: Stage) => {
        const li = doc.createElement("li");
        li.textContent = st.log;
        return li;
      };
      if (i === shown + 1 && shown >= 0) log.append(line(s));
      else log.replaceChildren(...STAGES.slice(0, i + 1).map(line));
    }
    shown = i;
  }

  function run(from: number): void {
    stopTimer();
    let i = from;
    const step = () => {
      show(i);
      inFlight = i > 0 && i < STAGES.length - 1;
      if (i >= STAGES.length - 1) {
        autoplayed = true;
        return;
      }
      timer = setTimeout(() => {
        i++;
        step();
      }, STAGES[i].ms);
    };
    step();
  }

  /** Stop any timed run; one caught part-way goes back to "live", silently. */
  function halt(): void {
    stopTimer();
    if (!inFlight) return;
    inFlight = false;
    announce(false);
    show(0);
  }

  const onStep = (e: Event) => {
    stopTimer();
    inFlight = false;
    autoplayed = true;
    announce(true);
    show(Number((e.currentTarget as HTMLElement).dataset.stage));
  };
  const onPlay = () => {
    autoplayed = true;
    announce(true);
    if (!gate.allowed()) {
      stopTimer();
      inFlight = false;
      show(SLATE);
      return;
    }
    run(1);
  };

  for (const b of steps) b.addEventListener("click", onStep);
  playBtn?.addEventListener("click", onPlay);

  announce(false);
  show(0);

  /* whileVisible stops only what it started. A visitor's run begun while the
   * frame was under half on screen must still stop when motion is switched off. */
  const unsub = gate.subscribe((allowed) => {
    if (!allowed) halt();
  });
  const dispose = whileVisible(
    frame ?? root,
    gate,
    () => {
      if (autoplayed) return;
      announce(false);
      timer = setTimeout(() => run(0), LEAD_IN_MS);
    },
    halt,
    0.5,
  );

  return {
    play: onPlay,
    show,
    destroy() {
      dispose();
      unsub();
      stopTimer();
      for (const b of steps) b.removeEventListener("click", onStep);
      playBtn?.removeEventListener("click", onPlay);
    },
  };
}
