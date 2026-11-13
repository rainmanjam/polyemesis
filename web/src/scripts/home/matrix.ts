/* The playable mix matrix: destinations down, audio tracks across, one switch
 * per cell. The state is plain data (see MatrixState) and every change returns
 * a new one, so the page is a function of it.
 *
 * tableHtml() is the single source of the markup: the Astro component renders
 * it on the server (the sports case, correct without JavaScript) and
 * initMatrix() re-renders it when a preset swaps the whole grid. A cell toggle
 * only updates attributes and text in place, so the focused cell stays focused.
 * Cells are real <button>s, so Tab, Space and Enter come from the browser. */
import { CASES } from "../../data/cases.mjs";
import type { MotionGate } from "./motion";

/* cases.mjs's typedefs do not resolve for importers, so the shape read here is
 * spelled out. */
type CaseKey = keyof typeof CASES;
interface Case {
  key: CaseKey;
  tab: string;
  tracks: { id: string; label: string }[];
  dests: { name: string; tracks: string[] }[];
}

export interface MatrixState {
  tracks: string[];
  dests: string[];
  on: boolean[][]; // on[dest][track]
}

/** A case's own routing: each destination carries the tracks the case lists. */
export function fromCase(c: Case): MatrixState {
  return {
    tracks: c.tracks.map((t) => t.label),
    dests: c.dests.map((d) => d.name),
    on: c.dests.map((d) => c.tracks.map((t) => d.tracks.includes(t.id))),
  };
}

/** Every destination carries every track. */
export function same(c: Case): MatrixState {
  const s = fromCase(c);
  return { ...s, on: s.on.map((row) => row.map(() => true)) };
}

/** A new state with one cell flipped; the input is left alone. */
export function toggle(s: MatrixState, d: number, t: number): MatrixState {
  return { ...s, on: s.on.map((row, i) => (i === d ? row.map((v, j) => (j === t ? !v : v)) : row)) };
}

/** What destination d receives: "video + English + Crowd", or "video only". */
export function receives(s: MatrixState, d: number): string {
  const lit = s.tracks.filter((_, t) => s.on[d][t]);
  return lit.length ? ["video", ...lit].join(" + ") : "video only";
}

/** "5 destinations · 5 different mixes · 1 video". */
export function summary(s: MatrixState): string {
  const mixes = new Set(s.on.map((row) => row.join())).size;
  const n = s.dests.length;
  return `${n} ${n === 1 ? "destination" : "destinations"} · ${mixes} ${mixes === 1 ? "mix" : "different mixes"} · 1 video`;
}

const esc = (v: string) => v.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;").replace(/"/g, "&quot;");

/** The grid as a real table: column headers are the tracks, row headers the
 * destinations, and each cell one switch named "<track> to <destination>".
 *
 * ONE MARKUP, TWO LAYOUTS. On a phone the stylesheet turns the table's boxes into stacked cards (one per
 * destination: its name, "hears: ...", then a two-across grid of the tracks), and a table whose boxes are
 * not table boxes loses its semantics in the browser. So the roles are written out (table, rowgroup, row,
 * columnheader, rowheader, cell): a screen reader still reads "destination, track, state" either way. Each
 * cell shows the track's NAME on a phone (.mx-tn) in place of "sent"/"off" (.mx-st); both are aria-hidden,
 * the button's own label ("<track> to <destination>") is what is read. */
export function tableHtml(s: MatrixState): string {
  const head = s.tracks
    .map((t, i) => `<th scope="col" role="columnheader" class="mx-col"><small>Track ${i + 1}</small><span>${esc(t)}</span></th>`)
    .join("");
  const rows = s.dests
    .map((dest, d) => {
      const cells = s.tracks
        .map((t, i) => {
          const on = s.on[d][i];
          return `<td role="cell" class="mx-c"><button type="button" class="mx-cell" data-d="${d}" data-t="${i}" aria-pressed="${on}" aria-label="${esc(`${t} to ${dest}`)}"><i class="mx-sq" aria-hidden="true"></i><span class="mx-st" aria-hidden="true">${on ? "sent" : "off"}</span><span class="mx-tn" aria-hidden="true">${esc(t)}</span></button></td>`;
        })
        .join("");
      return `<tr role="row" class="mx-row"><th scope="row" role="rowheader" class="mx-dest">${esc(dest)}</th>${cells}<td role="cell" class="mx-hears" data-hears="${d}">${esc(receives(s, d))}</td></tr>`;
    })
    .join("");
  return `<table class="mx-table" role="table"><caption class="sr-only">Which audio tracks each destination receives. Every destination always gets the video.</caption><thead role="rowgroup"><tr role="row"><th scope="col" role="columnheader" class="mx-dest">Destination</th>${head}<th scope="col" role="columnheader" class="mx-rcv">What it receives</th></tr></thead><tbody role="rowgroup"><tr role="row" class="mx-video"><th scope="row" role="rowheader" class="mx-dest">Video</th><td role="cell" colspan="${s.tracks.length}"><div class="mx-vbar">On for every destination. Locked.</div></td><td role="cell" class="mx-hears">same video, always</td></tr>${rows}</tbody></table>`;
}

export function initMatrix(root: HTMLElement, gate: MotionGate): () => void {
  const table = root.querySelector<HTMLElement>("[data-mx-table]");
  const live = root.querySelector<HTMLElement>("[data-mx-live]");
  const sum = root.querySelector<HTMLElement>("[data-mx-summary]");
  const presets = [...root.querySelectorAll<HTMLButtonElement>("[data-preset]")];
  if (!table) return () => {};

  // "sports" | "worship" | "streamer" are cases; "same" is a case's grid, all on.
  const caseOf = (key: string): Case | undefined => (key in CASES ? (CASES[key as CaseKey] as Case) : undefined);
  const pressed = presets.find((b) => b.getAttribute("aria-pressed") === "true")?.dataset.preset ?? "";
  let current: CaseKey = pressed in CASES ? (pressed as CaseKey) : "sports";
  let active: string | null = current;
  let state = fromCase(CASES[current] as Case);

  const matches = (a: MatrixState, b: MatrixState) => a.on.every((row, d) => row.every((v, t) => v === b.on[d][t]));

  function paint() {
    for (const b of table!.querySelectorAll<HTMLButtonElement>("button[data-d]")) {
      const on = state.on[Number(b.dataset.d)][Number(b.dataset.t)];
      b.setAttribute("aria-pressed", String(on));
      const st = b.querySelector(".mx-st");
      if (st) st.textContent = on ? "sent" : "off";
    }
    for (const cell of table!.querySelectorAll<HTMLElement>("[data-hears]")) {
      const d = Number(cell.dataset.hears);
      if (state.on[d]) cell.textContent = receives(state, d);
    }
    if (sum) sum.textContent = summary(state);
    for (const b of presets) b.setAttribute("aria-pressed", String(b.dataset.preset === active));
  }

  function announce(text: string) {
    if (live) live.textContent = text;
  }

  function rebuild(next: MatrixState, key: string, say: string) {
    state = next;
    active = key;
    table!.innerHTML = tableHtml(state);
    paint();
    announce(say);
  }

  const onTableClick = (e: Event) => {
    const btn = (e.target as Element).closest<HTMLButtonElement>("button[data-d]");
    if (!btn || !table.contains(btn)) return;
    const d = Number(btn.dataset.d);
    state = toggle(state, d, Number(btn.dataset.t));
    active = matches(state, fromCase(CASES[current] as Case)) ? current : null;
    paint();
    announce(`${state.dests[d]} now receives ${receives(state, d)}`);
    if (gate.allowed()) {
      btn.classList.remove("mx-flip");
      void btn.offsetWidth; // restart the one-shot flash
      btn.classList.add("mx-flip");
    }
  };

  const onPreset = (e: Event) => {
    const key = (e.currentTarget as HTMLElement).dataset.preset ?? "";
    const c = caseOf(key);
    if (c) {
      current = c.key;
      rebuild(fromCase(c), key, `${c.tab} routing loaded: ${c.dests.length} destinations, ${c.tracks.length} tracks.`);
    } else if (key === "same") {
      rebuild(same(CASES[current] as Case), "same", "Every destination now receives every track.");
    }
  };

  const onReset = () => {
    const c = CASES[current] as Case;
    rebuild(fromCase(c), current, `${c.tab} routing restored.`);
  };

  const reset = root.querySelector<HTMLElement>("[data-reset]");
  table.addEventListener("click", onTableClick);
  for (const b of presets) b.addEventListener("click", onPreset);
  reset?.addEventListener("click", onReset);
  paint();
  return () => {
    table.removeEventListener("click", onTableClick);
    for (const b of presets) b.removeEventListener("click", onPreset);
    reset?.removeEventListener("click", onReset);
  };
}
