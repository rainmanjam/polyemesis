/* The recovery beat: a four-stage illustration of what failover does when the
 * encoder drops. It is a diagram, not telemetry, so the log has no timestamps
 * and the copy says so (aria-label "Event log, illustrative").
 *
 * The root is [data-failover]. Markup contract (Recovery.astro owns it):
 *   button[data-stage="i"]  one per stage, steppable by hand
 *   [data-play]             "Pull the cable", replays from the drop
 *   [data-frame]            gets data-state = the stage id
 *   [data-enc]              encoder text
 *   [data-dest]             one per destination, text is "live" or "slate"
 *   [data-log]              the ol that receives one line per stage
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

export function initFailover(root: HTMLElement, gate: MotionGate): { play(): void; show(i: number): void; destroy(): void } {
  const frame = root.querySelector<HTMLElement>("[data-frame]");
  const enc = root.querySelector<HTMLElement>("[data-enc]");
  const log = root.querySelector<HTMLElement>("[data-log]");
  const dests = [...root.querySelectorAll<HTMLElement>("[data-dest]")];
  const steps = [...root.querySelectorAll<HTMLButtonElement>("button[data-stage]")];
  const playBtn = root.querySelector<HTMLButtonElement>("[data-play]");
  const doc = root.ownerDocument;

  let timer: ReturnType<typeof setTimeout> | undefined;
  /* True once a run reached "recovered" or the visitor took over by hand. An
   * interrupted autoplay (scrolled away, tab hidden, motion paused) is not
   * done, so the next start replays it from the top. */
  let autoplayed = false;
  let shown = -1;

  const stopTimer = () => {
    clearTimeout(timer);
    timer = undefined;
  };

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

  const onStep = (e: Event) => {
    stopTimer();
    autoplayed = true;
    show(Number((e.currentTarget as HTMLElement).dataset.stage));
  };
  const onPlay = () => {
    autoplayed = true;
    run(1);
  };

  for (const b of steps) b.addEventListener("click", onStep);
  playBtn?.addEventListener("click", onPlay);

  show(0);

  const dispose = whileVisible(
    root,
    gate,
    () => {
      if (autoplayed) return;
      timer = setTimeout(() => run(0), LEAD_IN_MS);
    },
    stopTimer,
    0.5,
  );

  return {
    play: onPlay,
    show,
    destroy() {
      dispose();
      stopTimer();
      for (const b of steps) b.removeEventListener("click", onStep);
      playBtn?.removeEventListener("click", onPlay);
    },
  };
}
