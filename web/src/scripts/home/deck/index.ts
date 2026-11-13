/* Stream Deck + section behaviour: the legend, the log and the scripted show.
 *
 * The server renders the section's resting state (the still, every legend row,
 * four log lines), so the page is complete without this. This wires the rows
 * to one deck store, writes the log from every action, and plays LOOP while the
 * gate allows and the section is on screen. The first control the visitor
 * operates (a legend row, a key, a dial) ends the show for good: they are
 * driving. With motion off or paused the section holds its resting state, and
 * every control still works and still writes the log.
 *
 * The 3D scene is mounted through whenNear, so three.js and the model are not
 * fetched until the section approaches. It reads the same store, so the scene,
 * the legend and the log cannot disagree. Pause itself is wired by boot() alone. */
import { whenNear } from "../lazy";
import { whileVisible, type MotionGate } from "../motion";
import {
  HOLD_MS, HOLD_SECONDS, KEYS, LOOP, REST, REST_LOG, START, createStore, dialDesc, keyDesc, logFor, takeOver,
  type Action, type DeckStore, type LogRow,
} from "./state";

const LOG_ROWS = 4;
const LOOP_END_S = 26; // a little after the last scripted action, then the show starts over
const TICK_MS = 100;
/* After a tap on a destination, or Go live, the destination settles (the real
 * thing takes about a second to start). */
const SETTLE_MS = 1300;

const clock = (t: number) => `${Math.floor(t / 60)}:${String(Math.floor(t % 60)).padStart(2, "0")}`;
const SVG = "http://www.w3.org/2000/svg";

export function initDeck(root: HTMLElement, gate: MotionGate): () => void {
  const store = createStore(REST);
  const box = root.querySelector<HTMLElement>("[data-deck-box]");
  const logEl = root.querySelector<HTMLElement>("[data-log]");
  const clockEl = root.querySelector<HTMLElement>("[data-clock]");
  const srEl = root.querySelector<HTMLElement>("[data-sr]");
  const modeEl = root.querySelector<HTMLElement>("[data-mode]");
  const keyRows = [...root.querySelectorAll<HTMLButtonElement>("button[data-key]")];
  const dialRows = [...root.querySelectorAll<HTMLElement>("[data-dial]")];

  /* ------------------------------------------------------------- the log */
  const rows: LogRow[] = [...REST_LOG];
  let loopT = 0;
  let looping = false;
  let taken = false; // the visitor has operated a control: no more show
  const born = performance.now();
  /* The log's clock is the show's while it plays, and a running count from the
   * resting state's last line (0:11) otherwise. */
  const stamp = () => (looping ? Math.floor(loopT) : Math.floor(REST_LOG[0].t + (performance.now() - born) / 1000));

  const renderLog = () => {
    if (!logEl) return;
    const marks = new Set(KEYS.map((k) => k.platform).filter(Boolean));
    logEl.replaceChildren(
      ...rows.slice(0, LOG_ROWS).map((e) => {
        const li = document.createElement("li");
        if (e.audio) li.className = "audio";
        const t = document.createElement("span");
        t.className = "t";
        t.textContent = clock(e.t);
        const v = document.createElement("span");
        v.className = "v";
        v.textContent = e.verb;
        const m = document.createElement("span");
        m.className = "m";
        if (e.platform && marks.has(e.platform)) {
          const svg = document.createElementNS(SVG, "svg");
          svg.setAttribute("class", "mk");
          svg.setAttribute("aria-hidden", "true");
          svg.setAttribute("focusable", "false");
          const use = document.createElementNS(SVG, "use");
          use.setAttribute("href", `#deck-mk-${e.platform}`);
          svg.append(use);
          m.append(svg);
        }
        m.append(`${e.what} `);
        const i = document.createElement("i");
        i.textContent = e.res;
        m.append(i);
        li.append(t, v, m);
        return li;
      }),
    );
    if (clockEl) clockEl.textContent = clock(stamp());
  };

  /* Every action goes through here: the store steps, the log gets a line, and a
   * visitor's action is announced to a screen reader. */
  const perform = (a: Action, spoken: boolean) => {
    const before = store.get();
    store.dispatch(a);
    /* A hold has no log line until it completes, so a visitor's hold is announced as it starts. */
    if (spoken && a.type === "hold" && srEl) srEl.textContent = `Holding ${a.key === "stopall" ? "Stop all" : "Routing preset"}`;
    const entry = logFor(before, a, store.get());
    if (!entry) return;
    rows.unshift({ ...entry, t: stamp() });
    rows.length = Math.min(rows.length, LOG_ROWS);
    renderLog();
    if (spoken && srEl) srEl.textContent = `${entry.verb} ${entry.what} ${entry.res.replace(/^→\s*/, "")}`;
  };

  /* ---------------------------------------------------- the visitor's hands */
  const timers = new Set<number>();
  const later = (ms: number, fn: () => void) => {
    const id = window.setTimeout(() => {
      timers.delete(id);
      fn();
    }, ms);
    timers.add(id);
  };
  const clearTimers = () => {
    for (const id of timers) clearTimeout(id);
    timers.clear();
  };
  const setMode = (text: string | null, on = false) => {
    if (!modeEl) return;
    modeEl.hidden = text === null;
    modeEl.classList.toggle("on", on);
    const span = modeEl.querySelector("span");
    if (span && text !== null) span.textContent = text;
  };
  const take = () => {
    if (taken) return;
    taken = true;
    stopLoop();
    // The show's own settle timers must not fire into the visitor's state, and a
    // hold the show had started will never be completed: put the key back up.
    clearTimers();
    const next = takeOver(store.get());
    if (next !== store.get()) store.reset(next);
    setMode(sceneUp && gate.allowed() ? "you're driving" : null);
  };

  /** The visitor does something. Go live and a destination tap settle a moment later. */
  const user = (a: Action) => {
    take();
    perform(a, true);
    if (a.type === "golive" || a.type === "dest") later(SETTLE_MS, () => perform({ type: "settle" }, false));
  };
  const userHold = (key: "stopall" | "preset") => {
    take(); // a hold the show had going is cleared before this one is judged
    if (store.get().held) return;
    user({ type: "hold", key });
    later(HOLD_MS, () => {
      if (store.get().held === key) perform({ type: key }, true);
    });
  };
  /* What the scene sees: the same store, with every action it dispatches treated as the visitor's.
   * The scene runs its own hold timer: it dispatches hold, then the completed action, or a release. */
  const driven: DeckStore = { get: store.get, subscribe: store.subscribe, dispatch: user };

  /* The hint belongs to live controls, so it is written here and not in the server's markup. */
  const hint = root.querySelector<HTMLElement>("[data-acts-hint]");
  if (hint) hint.textContent = "press a row to press that key";
  for (const b of keyRows) {
    b.disabled = false;
    const i = Number(b.dataset.key);
    b.addEventListener("click", () => {
      const k = KEYS[i];
      if (k.kind === "hold") userHold(i === 1 ? "stopall" : "preset");
      else user(i === 0 ? { type: "golive" } : { type: "dest", index: i - 3 });
    });
  }
  dialRows.forEach((row) => {
    const dial = Number(row.dataset.dial) as 0 | 1;
    for (const b of row.querySelectorAll<HTMLButtonElement>("button[data-op]")) {
      b.disabled = false;
      b.addEventListener("click", () => {
        const op = b.dataset.op;
        if (op === "down") user({ type: "gain", dial, steps: -1 });
        else if (op === "up") user({ type: "gain", dial, steps: 1 });
        else if (op === "push") user({ type: "mute", dial });
        else user({ type: "zero", dial });
      });
    }
  });

  /* A visitor who lands on a legend row is reading it: stop the show (the same take-over a press
   * makes), so a focused row's accessible name, which carries the key's state, stops changing under them. */
  for (const el of [...keyRows, ...dialRows]) el.addEventListener("focusin", take);

  /* Legend rows show the store, whoever changed it. */
  const syncRows = () => {
    const s = store.get();
    keyRows.forEach((b, i) => {
      const text = keyDesc(s, i);
      const d = b.querySelector("[data-desc]");
      if (d) d.textContent = text;
      b.setAttribute("aria-label", `${KEYS[i].name}, ${KEYS[i].kind === "hold" ? `hold ${HOLD_SECONDS} seconds` : "tap"}. ${text}`);
    });
    dialRows.forEach((row) => {
      const dial = Number(row.dataset.dial) as 0 | 1;
      const db = row.querySelector("[data-db]");
      if (db) {
        db.textContent = dialDesc(s, dial);
        db.classList.toggle("mut", s.muted[dial]);
      }
    });
  };
  const unsub = store.subscribe(syncRows);

  /* ------------------------------------------------------------ the show */
  let ticker: ReturnType<typeof setInterval> | undefined;
  let nextEntry = 0;
  const restartShow = () => {
    clearTimers();
    rows.length = 0; // each pass of the show writes its own log
    renderLog();
    store.reset(START);
    loopT = 0;
    nextEntry = 0;
  };
  const tick = () => {
    loopT += TICK_MS / 1000;
    while (nextEntry < LOOP.length && LOOP[nextEntry].at <= loopT) perform(LOOP[nextEntry++].action, false);
    if (clockEl) clockEl.textContent = clock(loopT);
    if (loopT >= LOOP_END_S) restartShow();
  };
  function startLoop() {
    if (taken || ticker !== undefined || !sceneUp) return;
    looping = true;
    restartShow();
    setMode("show loop", true);
    ticker = setInterval(tick, TICK_MS);
  }
  function stopLoop() {
    if (ticker !== undefined) clearInterval(ticker);
    ticker = undefined;
    looping = false;
  }
  /* The show plays only over the 3D device: with WebGL unavailable the still is
   * what the section shows, and the legend rests in the state the still shows. */
  let sceneUp = false;
  let wanted = false;
  const start = () => {
    wanted = true;
    startLoop();
  };
  const stop = () => {
    wanted = false;
    if (taken) return;
    stopLoop();
    clearTimers();
    // Motion off, paused or off screen: the section rests in its complete state.
    rows.splice(0, rows.length, ...REST_LOG);
    store.reset(REST);
    renderLog();
    setMode(null);
  };
  const unwatch = whileVisible(root, gate, start, stop);

  /* Motion off or paused: the show is not offered. */
  const syncMode = () => {
    if (!gate.allowed()) setMode(null);
  };
  const unGate = gate.subscribe(syncMode);

  /* ------------------------------------------------------------ the scene */
  let destroyScene: (() => void) | undefined;
  let disposed = false;
  const unnear = box
    ? whenNear(box, async () => {
        const { mountDeck } = await import("./scene");
        const off = await mountDeck(box, gate, driven);
        if (disposed) {
          off();
          return;
        }
        destroyScene = off;
        sceneUp = box.querySelector("canvas") !== null;
        if (wanted) startLoop();
      })
    : () => {};

  /* The scene lost its WebGL context and has put the still back: stop the show and rest
   * the legend in the state the still shows. The controls keep driving the log. */
  const onSceneDown = () => {
    sceneUp = false;
    destroyScene = undefined;
    stopLoop();
    clearTimers();
    rows.splice(0, rows.length, ...REST_LOG);
    store.reset(REST);
    renderLog();
    setMode(null);
  };
  box?.addEventListener("deck:down", onSceneDown);

  renderLog();
  syncRows();

  return () => {
    disposed = true;
    box?.removeEventListener("deck:down", onSceneDown);
    unnear();
    destroyScene?.();
    unwatch();
    unGate();
    unsub();
    stopLoop();
    clearTimers();
  };
}
