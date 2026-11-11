/* Hero behaviour: story tabs, destination solo, the live status line.
 *
 * The server renders every story panel and the destinations as plain divs, so
 * the page reads without script. This upgrades it: it reveals the tablist and
 * Pause, swaps each destination div for a button, and wires the rest. Pause
 * itself is wired by boot() alone, so it is bound exactly once per page. */
import { STORIES, STORY_ORDER, statusFor, type Dest, type StoryKey } from "./stories";
import { whileVisible, type MotionGate } from "./motion";

export interface HeroController {
  story(): StoryKey;
  setStory(k: StoryKey): void;
  solo(id: string | null): void;
  destroy(): void;
}

const SWAP_MS = 150;

/** Marks the destination buttons: the solo'd or previewed one, and the pinned one for aria-pressed. */
function markDests(panel: HTMLElement, dest: Dest | undefined, pinned: string | null): void {
  for (const el of panel.querySelectorAll<HTMLElement>(".dest")) {
    const id2 = el.dataset.dest;
    el.classList.toggle("is-solo", !!dest && id2 === dest.id);
    el.classList.toggle("dim", !!dest && id2 !== dest.id);
    el.setAttribute("aria-pressed", pinned !== null && id2 === pinned ? "true" : "false");
  }
}

/** Dims the flow lines that do not carry the solo'd destination. */
function dimLines(panel: HTMLElement, dest: Dest | undefined, silent: (src: number) => boolean): void {
  for (const el of panel.querySelectorAll<SVGElement>(".ln")) {
    const from = el.dataset.from ?? "";
    const to = el.dataset.to ?? "";
    let dim = false;
    if (dest) dim = from === "hub" ? to !== dest.id : silent(Number(from));
    el.classList.toggle("dim", dim);
  }
}

/** A destination div becomes a button: same classes, children, data-* and box;
 * the accessible name moves from data-label to aria-label. */
export function upgradeToButton(el: HTMLElement): HTMLButtonElement {
  const btn = el.ownerDocument.createElement("button");
  for (const a of Array.from(el.attributes)) btn.setAttribute(a.name, a.value);
  btn.type = "button";
  btn.setAttribute("aria-pressed", "false");
  const label = el.dataset.label;
  if (label !== undefined) btn.setAttribute("aria-label", label);
  while (el.firstChild) btn.appendChild(el.firstChild);
  el.replaceWith(btn);
  return btn;
}

export function initHero(root: HTMLElement, gate: MotionGate): HeroController {
  const doc = root.ownerDocument;
  const ac = new AbortController();
  const { signal } = ac;

  const tablist = root.querySelector<HTMLElement>("[data-story-tabs]");
  const pause = root.querySelector<HTMLElement>("[data-pause]");
  const statusEl = root.querySelector<HTMLElement>("[data-status]");
  const fineEl = root.querySelector<HTMLElement>("[data-fine]");
  const tabs = new Map<StoryKey, HTMLElement>();
  const panels = new Map<StoryKey, HTMLElement>();
  for (const k of STORY_ORDER) {
    const tab = root.querySelector<HTMLElement>(`#tab-${k}`);
    const panel = root.querySelector<HTMLElement>(`#panel-${k}`);
    if (tab) tabs.set(k, tab);
    if (panel) panels.set(k, panel);
  }
  const keys = STORY_ORDER.filter((k) => tabs.has(k) && panels.has(k));

  if (tablist) tablist.hidden = false;
  if (pause) pause.hidden = false;
  for (const el of root.querySelectorAll<HTMLElement>(".dest")) {
    if (el.tagName !== "BUTTON") upgradeToButton(el);
  }

  let story: StoryKey = keys.find((k) => !panels.get(k)?.hidden) ?? keys[0] ?? "streamer";
  let pinned: string | null = null;
  let preview: string | null = null;
  let stopVisible: (() => void) | null = null;
  let swapTimer: ReturnType<typeof setTimeout> | null = null;
  let finishSwap: (() => void) | null = null;

  const panelOf = (k: StoryKey) => panels.get(k);

  /** Selected state and roving tabindex follow `story`. */
  function syncTabs() {
    for (const key of keys) {
      const t = tabs.get(key);
      t?.setAttribute("aria-selected", String(key === story));
      t?.setAttribute("tabindex", key === story ? "0" : "-1");
    }
  }

  function bindVisibility() {
    stopVisible?.();
    stopVisible = null;
    const dg = panelOf(story)?.querySelector<HTMLElement>(".dg");
    if (!dg) return;
    stopVisible = whileVisible(
      dg,
      gate,
      () => dg.classList.add("is-playing"),
      () => dg.classList.remove("is-playing"),
    );
  }

  function writeStatus(name: string | null, text: string) {
    if (!statusEl) return;
    statusEl.replaceChildren();
    if (name !== null) {
      const b = doc.createElement("b");
      b.textContent = name;
      statusEl.append(b, doc.createTextNode(` ${text}`));
    } else {
      statusEl.textContent = text;
    }
  }

  /** Repaint the visible diagram and status for the current pin or preview. */
  function render() {
    const s = STORIES[story];
    const id = pinned ?? preview;
    const dest = id === null ? undefined : s.dests.find((d) => d.id === id);
    const panel = panelOf(story);
    const st = statusFor(s, dest ? dest.id : null, !dest);
    writeStatus(st.name, st.text);
    if (!panel) return;

    markDests(panel, dest, pinned);
    const silent = (src: number) => !!dest && (src === 0 ? !dest.video : dest.mix[src - 1] === 0);
    for (const el of panel.querySelectorAll<HTMLElement>(".si")) {
      el.classList.toggle("dim", silent(Number(el.dataset.src)));
    }
    dimLines(panel, dest, silent);
  }

  function solo(id: string | null) {
    pinned = id;
    preview = null;
    render();
  }

  function setStory(k: StoryKey) {
    // Re-selecting the story on show changes nothing, so a pin survives it.
    if (!keys.includes(k) || k === story) return;
    finishSwap?.();
    const prev = story;
    pinned = null;
    preview = null;
    story = k;
    syncTabs();
    if (fineEl) fineEl.textContent = STORIES[k].fine;
    stopVisible?.();
    stopVisible = null;

    const from = panelOf(prev);
    const to = panelOf(k);
    const done = () => {
      if (swapTimer !== null) clearTimeout(swapTimer);
      swapTimer = null;
      finishSwap = null;
      if (from) {
        from.classList.remove("swap-out");
        from.inert = false;
        from.hidden = true;
      }
      for (const key of keys) {
        const p = panelOf(key);
        if (p && key !== k) p.hidden = true;
      }
      if (to) to.hidden = false;
      render();
      bindVisibility();
    };
    if (gate.allowed() && from) {
      // The fading panel takes no clicks: its destinations share ids with the
      // incoming story's, so a click there would pin one in the new story.
      from.inert = true;
      from.classList.add("swap-out");
      finishSwap = done;
      swapTimer = setTimeout(done, SWAP_MS);
    } else {
      done();
    }
  }

  // Tabs: roving tabindex, arrows wrap, Home/End jump, activation follows focus.
  for (const [k, tab] of tabs) {
    tab.addEventListener("click", () => setStory(k), { signal });
    tab.addEventListener(
      "keydown",
      (e) => {
        const i = keys.indexOf(k);
        let next: number | null = null;
        if (e.key === "ArrowRight") next = (i + 1) % keys.length;
        else if (e.key === "ArrowLeft") next = (i - 1 + keys.length) % keys.length;
        else if (e.key === "Home") next = 0;
        else if (e.key === "End") next = keys.length - 1;
        if (next === null) return;
        e.preventDefault();
        const target = keys[next];
        tabs.get(target)?.focus();
        setStory(target);
      },
      { signal },
    );
  }

  // Destinations: hover or keyboard focus previews, a click pins (Enter and
  // Space reach us as clicks on a button), clicking the pinned one unpins.
  const idOf = (t: EventTarget | null) => (t as HTMLElement | null)?.closest<HTMLElement>(".dest")?.dataset.dest;
  root.addEventListener(
    "click",
    (e) => {
      const id = idOf(e.target);
      if (id === undefined) return;
      solo(pinned === id ? null : id);
    },
    { signal },
  );
  root.addEventListener(
    "pointerover",
    (e) => {
      const id = idOf(e.target);
      if (id === undefined || id === preview || (e as PointerEvent).pointerType !== "mouse" || pinned !== null) return;
      preview = id;
      render();
    },
    { signal },
  );
  root.addEventListener(
    "pointerout",
    (e) => {
      // Moving within a card, or straight onto another card (whose pointerover
      // takes over), is not leaving: rewriting the live status there is churn.
      if (idOf(e.target) === undefined || preview === null || idOf((e as PointerEvent).relatedTarget) !== undefined) return;
      preview = null;
      render();
    },
    { signal },
  );
  root.addEventListener(
    "focusin",
    (e) => {
      const id = idOf(e.target);
      let visible = false;
      try {
        visible = (e.target as HTMLElement).matches(":focus-visible");
      } catch {
        visible = false;
      }
      if (id === undefined || !visible || pinned !== null) return;
      preview = id;
      render();
    },
    { signal },
  );
  root.addEventListener(
    "focusout",
    (e) => {
      if (idOf(e.target) === undefined || preview === null) return;
      preview = null;
      render();
    },
    { signal },
  );
  root.addEventListener(
    "keydown",
    (e) => {
      if (e.key !== "Escape" || (pinned === null && preview === null)) return;
      solo(null);
    },
    { signal },
  );

  // Initial state.
  syncTabs();
  for (const key of keys) {
    const p = panelOf(key);
    if (p) p.hidden = key !== story;
  }
  if (fineEl) fineEl.textContent = STORIES[story].fine;
  render();
  bindVisibility();

  return {
    story: () => story,
    setStory,
    solo,
    destroy() {
      ac.abort();
      if (swapTimer !== null) clearTimeout(swapTimer);
      swapTimer = null;
      finishSwap = null;
      stopVisible?.();
      stopVisible = null;
    },
  };
}
