// @vitest-environment jsdom
/// <reference types="node" />
//
// CAN AN OPERATOR ACTUALLY TOUCH THIS SETTING? The question
// internal/db/settings_drift_test.go has always been unable to ask.
//
// That guard walks db.Settings and asserts every leaf's json name is NAMEABLE
// somewhere in ui/src/lib/types.ts. Its own header says what that is worth:
//
//     NAMEABLE IS NOT REACHABLE. A field present in types.ts still needs a
//     control somewhere.
//
// Eight findings in the reachability sweep trace to that sentence. A field can
// be validated, stored, compiled into FFmpeg arguments, declared in types.ts,
// and have no box anywhere in the web UI -- and every guard in the repository
// stays green, because every guard in the repository was reading declarations.
//
// This one reads the DOM. It renders each settings screen into jsdom, walks the
// controls it produced, and asks of every leaf: is there an input, switch,
// select, slider or textarea whose displayed state depends on this leaf? A leaf
// with none is either a decision written down below, or a feature no operator
// can reach.
//
// ---------------------------------------------------------------------------
// HOW A CONTROL IS TIED TO A LEAF
//
// Not by name. Matching names is the technique that produced #788 -- three of
// its findings passed because the name matched an unrelated interface. This
// matches on BEHAVIOUR, in two passes:
//
//  1. FINGERPRINT. Every leaf is given a value no other leaf has (`zzq41`,
//     `70041`) and the screen is rendered once. A control displaying `70041` is
//     displaying that leaf and nothing else. One render resolves most of the
//     tree, which is what makes the whole thing affordable.
//
//  2. PROBE. What the fingerprint cannot see -- booleans, enums, and any leaf a
//     control reformats before showing it -- is settled by changing the leaf and
//     re-rendering. A control whose OWN value or checked state moves is bound to
//     it. Controls that appear or vanish do not count: revealing a section is
//     not the same as editing a field, and counting it would let a leaf that
//     merely gates a card claim a control it does not have.
//
//     The probe uses group testing rather than one render per leaf: perturb the
//     whole unresolved set at once, and if nothing on the screen moved, nothing
//     in that set is bound here -- one render prunes ninety leaves. Only sets
//     that DO move get split. That is the difference between five seconds and a
//     minute, and it rests on one assumption worth stating: that two leaves
//     bound to the same control cannot cancel each other out exactly. Nothing in
//     this UI does that, and if something ever does the failure is a leaf
//     reported unreachable, not one wrongly reported reachable.
//
//  3. WRITE. The two passes above only ever read what a control DISPLAYS, and a
//     control can display one leaf while its onChange writes another -- the
//     copy-paste hazard in any block repeated per field. These forms are
//     controlled, so the test is local and needs no save: type into a correctly
//     wired control and the leaf moves, the component re-renders, and the
//     control shows what you typed. Type into a miswired one and the displayed
//     leaf never moved, so React puts the OLD value back and the DOM reverts.
//
//     Fields are driven before toggles, because a section switch sits above the
//     fields it reveals and turning it off unmounts them before they are
//     tested. The first version of this pass did exactly that and reported
//     success over four untested MQTT fields.
//
// ---------------------------------------------------------------------------
// WHAT THIS DOES NOT COVER, stated here rather than discovered later. A guard
// that overclaims is how #788 happened.
//
//  - ONLY THE SCREENS IN `SCREENS`. A control on a screen not listed there is
//    invisible to this, exactly as if it did not exist. That is why every
//    screen carries a floor on how many controls it must produce (see the
//    positive control below): a screen that silently stops rendering must fail
//    rather than quietly shrink the coverage.
//  - ONE STATE PER SCREEN, plus a few. Booleans are rendered both ways, and an
//    enum that HAS a control on a screen is rendered once per choice, because
//    such an enum is usually the thing gating what else the screen shows
//    (`ingest.mode` is why the RTMP fields exist at all). An enum with no
//    control gets no variants, so a field gated by one is reported unreachable.
//  - VALUES OUTSIDE A CONTROL'S DOMAIN. A `<Select>` over the six PCI vendor
//    IDs it knows shows the same blank for `70041` as for `41041`, so the probe
//    sees no movement. Those leaves are listed in BOUND_BUT_UNMEASURABLE with
//    the control that does edit them, and that claim is itself checked.
//  - LISTS EDITED BY ROW. A playlist entry is added by picking an upload and
//    removed by a button; its value is text in the row, not a field. Reachable,
//    invisible here.
//  - WHETHER THE CONTROL SAVES. Pass 3 types into every control it can drive
//    and requires it to KEEP what it was given, which catches a control that
//    displays one leaf and writes to another. It still does not follow the
//    value to the wire: whether the PUT is issued and accepted is the page
//    suites' question. Radix Selects and Sliders are not driven at all -- their
//    value moves through a portal menu this does not open -- so for those, pass
//    2's display evidence is still the whole of it.
//
// ---------------------------------------------------------------------------
// THE LISTS BELOW ARE THE POINT. A leaf that no control reaches is either a
// decision somebody wrote down or a bug -- never an oversight nobody noticed.
// Each entry says which, and an entry that becomes wrong FAILS: a leaf that
// gains a control while still listed is a stale excuse, and stale excuses are
// what turn a guard into decoration.
//
// So when a screen gains one of the controls NO_CONTROL_YET is waiting for, this
// file goes red and the fix is to delete a line. That is the intended way to
// find out it worked.

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { ReactElement } from "react";
import { act, cleanup, fireEvent, render } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router";
import { readFileSync } from "node:fs";
import { join } from "node:path";

import { stripJSComments } from "./strip-js-comments";

/* The live-data socket. Mocked because it is a WebSocket, and because none of
 * these screens read a setting through it -- every settings form on them is
 * seeded from GET /settings, which the fetch stub below answers. */
vi.mock("@/hooks/useLiveData", () => ({
  useLiveData: () => ({
    status: null,
    connected: true,
    frameError: false,
    source: null,
    bitrate: [],
    levels: null,
    system: null,
    logs: [],
    programme: null,
    programmeKnown: true,
    snapshotKnown: true,
    sourceCount: 1,
    programmes: [],
    selectProgramme: () => {},
    recordingsRevision: 0,
  }),
  useIngestLive: () => false,
}));

import { PlayoutPage } from "@/pages/PlayoutPage";
import { RecordingsPage } from "@/pages/RecordingsPage";
import { SettingsPage } from "@/pages/SettingsPage";

/* vitest runs with `ui/` as the root, so this is `ui/`. `import.meta.url` is
 * NOT usable here: under the jsdom environment vite hands it out as a `/@fs/`
 * URL and readFileSync cannot open it. */
const ROOT = process.cwd();

/* --------------------------------------------------------------- the leaves */

type Kind = "string" | "number" | "boolean" | "enum" | "opaque";

interface Leaf {
  /** Dotted json path, with `[]` marking a list whose element this is in. */
  path: string;
  kind: Kind;
  /** For `enum`, the string literals the union allows. */
  choices?: string[];
}

/** The text between the braces of the block starting at or after `from`. */
function braceBlock(src: string, from: number): string {
  let i = src.indexOf("{", from);
  const start = i;
  let depth = 0;
  for (; i < src.length; i++) {
    if (src[i] === "{") depth++;
    else if (src[i] === "}" && --depth === 0) return src.slice(start + 1, i);
  }
  return "";
}

/** `name: type` pairs declared directly in an interface body.
 *
 *  Depth-aware rather than line-anchored, because the settings types nest
 *  inline object literals on one line (`srt: { passphrase: string; latencyMs:
 *  number }`) -- the same shape that forced the Go guard's regex to stop
 *  anchoring to the start of a line. */
function members(body: string): Array<{ name: string; type: string }> {
  const out: Array<{ name: string; type: string }> = [];
  let i = 0;
  while (i < body.length) {
    const m = /^\s*(\w+)\??\s*:/.exec(body.slice(i));
    if (!m) {
      i++;
      continue;
    }
    let j = i + m[0].length;
    let depth = 0;
    let type = "";
    for (; j < body.length; j++) {
      const c = body[j];
      if (c === "{" || c === "[" || c === "(") depth++;
      if (c === "}" || c === "]" || c === ")") depth--;
      if (depth === 0 && (c === ";" || c === "\n")) break;
      type += c;
    }
    out.push({ name: m[1], type: type.trim() });
    i = j + 1;
  }
  return out;
}

/** Every leaf of the `Settings` tree in types.ts, following named interfaces
 *  and type aliases into it.
 *
 *  types.ts is the source rather than db.Settings because vitest cannot read Go
 *  -- and because the two are already chained: settings_drift_test.go asserts
 *  every Go leaf is named in here, and this asserts every leaf named in here is
 *  reachable. Neither half is sufficient alone, which is exactly the point.
 *
 *  Same technique as preset-drift.test.ts and service-registry-drift.test.ts: a
 *  text scan, not a TypeScript parse. A real parse is the dependency these
 *  guards exist to avoid; the cost is that a shape none of these files uses
 *  today (a mapped type, a conditional type) would read as an opaque leaf --
 *  and an opaque leaf has to be declared below before this file will pass, so
 *  it cannot slip past silently. */
function settingsLeaves(): Leaf[] {
  const src = stripJSComments(readFileSync(join(ROOT, "src/lib/types.ts"), "utf8"));

  const ifaces = new Map<string, string>();
  for (const m of src.matchAll(/export interface (\w+)\s*\{/g)) {
    ifaces.set(m[1], braceBlock(src, m.index));
  }
  const aliases = new Map<string, string>();
  for (const m of src.matchAll(/export type (\w+)\s*=\s*([^;]+);/g)) {
    // A union written over several lines starts with a leading `|`.
    aliases.set(m[1], m[2].trim().replace(/^\|/, "").trim());
  }

  const leaves: Leaf[] = [];
  const open: string[] = [];
  function walk(body: string, prefix: string) {
    for (const { name, type } of members(body)) {
      const path = prefix ? `${prefix}.${name}` : name;
      let t = type.replace(/\|\s*(null|undefined)/g, "").trim();
      const list = t.endsWith("[]");
      if (list) t = t.slice(0, -2).trim();
      const under = path + (list ? "[]" : "");

      if (t.startsWith("{")) {
        walk(t.slice(1, t.lastIndexOf("}")), under);
        continue;
      }
      if (aliases.has(t)) t = aliases.get(t)!;
      if (t.startsWith('"')) {
        leaves.push({
          path,
          kind: "enum",
          choices: t.split("|").map((s) => s.trim().replace(/"/g, "")),
        });
        continue;
      }
      if (ifaces.has(t)) {
        // A type that contains itself would otherwise walk for ever. Nothing in
        // Settings does today; the stack costs one line and removes the class.
        if (open.includes(t)) continue;
        open.push(t);
        walk(ifaces.get(t)!, under);
        open.pop();
        continue;
      }
      if (t === "string") leaves.push({ path, kind: "string" });
      else if (t === "number") leaves.push({ path, kind: "number" });
      else if (t === "boolean") leaves.push({ path, kind: "boolean" });
      else leaves.push({ path, kind: "opaque" });
    }
  }
  walk(ifaces.get("Settings")!, "");
  return leaves;
}

/* ------------------------------------------------------------- the fixture */

type Doc = Record<string, unknown>;

/** Writes `value` at a dotted path, creating the objects on the way and a
 *  one-element array for every `[]` segment. One element is enough: a list
 *  editor that renders a row for one entry renders a row for ten. */
function put(root: Doc, path: string, value: unknown) {
  const parts = path.split(".");
  let node: Doc = root;
  for (let i = 0; i < parts.length - 1; i++) {
    const isList = parts[i].endsWith("[]");
    const key = isList ? parts[i].slice(0, -2) : parts[i];
    if (isList) {
      const rows = (node[key] as Doc[]) ?? [{}];
      node[key] = rows;
      rows[0] ??= {};
      node = rows[0];
    } else {
      node[key] = (node[key] as Doc) ?? {};
      node = node[key] as Doc;
    }
  }
  node[parts[parts.length - 1]] = value;
}

/** The two values a leaf takes: `0` in the baseline, `1` under the probe.
 *
 *  The numbers are five digits and far from anything the UI computes, so a
 *  control showing `70041` is showing leaf 41 rather than a coincidence, and
 *  the strings are unpronounceable for the same reason. Booleans have only the
 *  two values there are; enums step to their next choice. */
function value(leaf: Leaf, index: number, variant: 0 | 1): unknown {
  switch (leaf.kind) {
    case "number":
      return variant === 0 ? 70000 + index : 41000 + index;
    case "string":
      return variant === 0 ? `zzq${index}` : `zzr${index}`;
    case "boolean":
      return variant === 0;
    case "enum":
      return variant === 0 ? leaf.choices![0] : (leaf.choices![1] ?? leaf.choices![0]);
    default:
      return undefined;
  }
}

/** A whole settings document with every leaf at its baseline value, except the
 *  named ones, which take their probe value. */
function document_(leaves: Leaf[], probed: Set<string>): Doc {
  const root: Doc = {};
  leaves.forEach((leaf, i) => {
    if (leaf.kind === "opaque") return;
    put(root, leaf.path, value(leaf, i, probed.has(leaf.path) ? 1 : 0));
  });
  return root;
}

/* -------------------------------------------------------------- the server */

/** The settings document the next render will be served. */
let served: Doc = {};

/* Every read these screens make, answered from one place.
 *
 * Stubbed at `fetch` rather than at `@/lib/api` deliberately: `api` is ninety
 * methods and a mock of it goes stale silently, method by method, as pages gain
 * reads. There is one fetch. A screen that grows a read it does not tolerate an
 * empty answer to fails HERE, loudly, at the render -- which is the behaviour
 * this file wants from every kind of drift. */
function responseFor(path: string): unknown {
  if (path.endsWith("/settings")) return served;
  if (path.endsWith("/recordings/usage")) {
    return { usedBytes: 1, freeBytes: 2, totalBytes: 3, count: 0, storage: { halted: false } };
  }
  if (path.endsWith("/recordings")) return [];
  if (path.endsWith("/playout")) {
    return {
      status: {
        enabled: true,
        public: false,
        master: "",
        format: "hls",
        variants: [],
        analytics: { viewers: 0, peak: 0, sessions: 0, uncounted: 0 },
        usage: { bytes: 0, segments: 0 },
      },
      // The playout form is seeded from this rather than from /settings, so it
      // has to carry the same fixture or the whole block reads as unreachable.
      settings: served.playout ?? {},
      protection: "token",
      token: "t",
      title: "",
      description: "",
      urls: { master: "", watch: "", embed: "" },
      exposed: false,
      running: true,
    };
  }
  if (path.endsWith("/automod/matrix")) {
    return {
      enabled: true,
      platformEnabled: {},
      cells: [
        { platform: "twitch", action: "delete", checker: "rules", auto: false, available: true },
      ],
      summary: {},
      actions: ["delete"],
      checkers: ["rules"],
      platforms: ["twitch"],
    };
  }
  if (path.endsWith("/failover/playlist")) return { ready: true, items: [] };
  if (path.endsWith("/sources")) return [{ id: 1, name: "src" }];
  if (path.endsWith("/system")) {
    return {
      version: "v0",
      ingestUrl: "",
      ingestMode: "srt",
      maxTracks: 8,
      tlsEnabled: false,
      dataDir: "/data",
      uiBuilt: true,
      // hasLibsrt true so the ingest tab does not take its "no SRT" branch and
      // hide the form this file is here to walk.
      ffmpeg: {
        ffmpeg: "ffmpeg",
        ffprobe: "ffprobe",
        version: "7.0",
        major: 7,
        minor: 0,
        hasLibsrt: true,
        hasLibx264: true,
        videoEncoders: [],
        hwEncoders: [],
        encoderCaps: [],
        filters: [],
      },
      gpu: { platform: "linux", devices: [], vendors: [], nvidia: false, notes: [] },
    };
  }
  return [];
}

function serveWith(settings: Doc) {
  served = settings;
  vi.stubGlobal("fetch", (url: string) =>
    Promise.resolve({
      ok: true,
      status: 200,
      text: () => Promise.resolve(JSON.stringify(responseFor(String(url)))),
    } as Response),
  );
}

/* -------------------------------------------------------------- the screens */

interface Screen {
  name: string;
  /** The URL the screen is opened at. The query string matters: SettingsPage
   *  keeps its tabs there, and a tab that is not open renders nothing at all. */
  at: string;
  element: ReactElement;
  /** How few controls this screen may render before something is wrong. THE
   *  POSITIVE CONTROL: see the test that reads it. */
  atLeast: number;
}

const SCREENS: Screen[] = [
  { name: "Settings / ingest", at: "/settings?tab=ingest", element: <SettingsPage />, atLeast: 4 },
  {
    name: "Settings / pipeline",
    at: "/settings?tab=pipeline",
    element: <SettingsPage />,
    atLeast: 30,
  },
  { name: "Recordings", at: "/recordings", element: <RecordingsPage />, atLeast: 5 },
  { name: "Playout", at: "/playout", element: <PlayoutPage />, atLeast: 15 },
];

/** What counts as a control an operator can put a value into. Roles rather than
 *  classes, so this agrees with the rest of the suite -- which finds everything
 *  by role and accessible name and asserts no class anywhere. */
const CONTROLS =
  "input, textarea, select, " +
  "[role='switch'], [role='checkbox'], [role='combobox'], [role='slider'], [role='radio']";

function roleOf(el: Element): string {
  const role = el.getAttribute("role");
  if (role) return role;
  if (el instanceof HTMLInputElement) return `input:${el.type}`;
  return el.tagName.toLowerCase();
}

/** What the control is showing right now. */
function shownValue(el: Element): string {
  if (el instanceof HTMLInputElement) {
    return el.type === "checkbox" || el.type === "radio" ? String(el.checked) : el.value;
  }
  if (el instanceof HTMLTextAreaElement || el instanceof HTMLSelectElement) return el.value;
  const checked = el.getAttribute("aria-checked");
  if (checked !== null) return checked;
  const now = el.getAttribute("aria-valuenow");
  if (now !== null) return now;
  // A Radix select trigger renders the chosen item's LABEL, not its value, so
  // the text is the only thing that moves when the selection does.
  return (el.textContent ?? "").trim();
}

/** How a control is recognised again in the next render.
 *
 *  An id or an accessible name is an identity. Failing both -- several selects
 *  on these screens have neither -- the position among controls of the same
 *  role is used, which is only an identity while the screen holds the same
 *  number of them. `differs` below refuses to compare those keys when the count
 *  moved, so a control appearing cannot be read as a control changing. */
function keyOf(el: Element, ordinal: number): string {
  if (el.id) return `#${el.id}`;
  const label = el.getAttribute("aria-label");
  if (label) return `@${label}`;
  const name = el.getAttribute("name");
  if (name) return `=${name}`;
  return `~${roleOf(el)}[${ordinal}]`;
}

interface Shot {
  /** control identity -> what it shows. */
  keyed: Map<string, string>;
  /** every value shown by any control, for the fingerprint pass. */
  shown: Set<string>;
  /** role -> how many controls have it. */
  census: Map<string, number>;
}

/** Render one screen against one settings document and read its controls. */
/** Turn the macrotask queue over until the screen stops changing, and return
 *  what it settled on. Extracted from `shoot` so the write pass below settles
 *  by exactly the same rule -- a second reading taken under a different
 *  settling rule would not be comparable to the first. */
async function settle(): Promise<Shot> {
  let previous = "";
  let shot = readControls();
  for (let turn = 0; turn < 40; turn++) {
    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 0));
    });
    shot = readControls();
    const now = signatureOf(shot);
    if (shot.keyed.size > 0 && now === previous) break;
    previous = now;
  }
  return shot;
}

async function shoot(screen: Screen, settings: Doc): Promise<Shot> {
  serveWith(settings);
  const route = screen.at.split("?")[0];
  render(
    <MemoryRouter initialEntries={[screen.at]}>
      <Routes>
        <Route path={route} element={screen.element} />
      </Routes>
    </MemoryRouter>,
  );
  // SETTLED, NOT MERELY NON-EMPTY, and this is the difference between a guard
  // and a coin toss.
  //
  // Every one of these screens shows a spinner and fills in as its reads land,
  // and they do not all land together: the settings arrive, the form appears,
  // and the automod matrix -- a second read, from a second endpoint -- adds its
  // switches after that. Stopping at the first control means the matrix is in
  // some renders and not others, so the same leaf reads as reachable or not
  // depending on scheduling. That was observed, not imagined: automod.enabled
  // flipped between runs of this file before this loop existed.
  //
  // So: turn the macrotask queue over until every control is showing the same
  // thing it showed last turn. Each turn flushes every promise chain that was
  // already pending, and two turns with an identical reading mean nothing
  // further is in flight. The whole reading rather than the control COUNT,
  // because a second read that fills a value into a control already on screen
  // changes what this file measures without changing how many controls there
  // are. Cheaper than polling on a timer, and it ends when the screen is done
  // rather than when a chosen number of milliseconds is up.
  //
  // A screen that never produces a control simply falls out of the loop. It is
  // NOT thrown from here: throwing fails every test in the file with "expected
  // 0 to be greater than 0", which is true and useless. The empty screen is
  // returned, and the positive control below reports it in words that say what
  // to go and look at.
  const shot = await settle();
  cleanup();
  return shot;
}

/** Every frozen control the walk stepped over, for the positive control below.
 *  Module-level because the walk runs once and the assertion is a separate
 *  test. */
const FROZEN_SEEN = new Set<string>();

/** A control the operator cannot actually operate.
 *
 *  The walk's question is "can somebody CHANGE this setting", and a disabled or
 *  read-only control answers no while looking exactly like one that answers
 *  yes: it renders, it holds a value, and its value moves when the fixture
 *  moves -- so the fingerprint matched and the leaf was credited as reachable.
 *  A setting behind a permanently disabled control is precisely the kind of
 *  unreachable capability this file exists to find, and it was the one shape
 *  guaranteed to be missed.
 *
 *  Skipping is the safe direction. A control disabled only while a save is in
 *  flight (`disabled={busy}`) would now fall to the excuse lists rather than be
 *  silently credited, which is a failure someone reads rather than a pass
 *  nobody does. */
function isFrozen(el: Element): boolean {
  if (el.getAttribute("aria-disabled") === "true") return true;
  if (el.getAttribute("data-disabled") !== null) return true;
  if (el instanceof HTMLInputElement || el instanceof HTMLTextAreaElement) {
    return el.disabled || el.readOnly;
  }
  if (el instanceof HTMLSelectElement || el instanceof HTMLButtonElement) return el.disabled;
  return false;
}

/** The controls on screen right now. */
function readControls(): Shot {
  const keyed = new Map<string, string>();
  const shown = new Set<string>();
  const census = new Map<string, number>();
  document.querySelectorAll(CONTROLS).forEach((el) => {
    if (isFrozen(el)) {
      FROZEN_SEEN.add(keyOf(el, census.get(roleOf(el)) ?? 0));
      return;
    }
    const role = roleOf(el);
    const ordinal = census.get(role) ?? 0;
    census.set(role, ordinal + 1);
    const v = shownValue(el);
    shown.add(v);
    keyed.set(keyOf(el, ordinal), v);
  });
  return { keyed, shown, census };
}

/** Everything `shoot` is about to read, as one string, so "has this screen
 *  stopped changing" is asked of the ANSWERS rather than of how many there are.
 *  A second read that fills a value in without adding a control moves this and
 *  does not move a count. */
function signatureOf(shot: Shot): string {
  return [...shot.keyed].map(([k, v]) => `${k}=${v}`).join("\u0000");
}

/* ------------------------------------------- pass 3: does the control WRITE? */

/* DISPLAYING A LEAF IS NOT EDITING IT.
 *
 * The two passes above ask whether a control's DISPLAY moves with a leaf. They
 * cannot see where that control's onChange sends what you type, and this file's
 * header has always disclosed the gap: "it does not say the change survives a
 * PUT". The gap has a shape, and it is a copy-paste: SettingsPage's MQTT block
 * repeats the same four-line spread for host, port, user and prefix, and an
 * onChange left naming `prefix` in the block that displays `user` credits
 * `mqtt.user` as reachable while making it impossible to edit. Both leaves are
 * still displayed, so both passes are still green.
 *
 * Reading the PUT body would answer it, but that needs a Save button per screen
 * and a settled request per control. There is a sharper signal already on
 * screen. These forms are CONTROLLED: the input renders `value={x.a}`. Type
 * into a correctly wired one and the leaf moves, the component re-renders, and
 * the input shows what you typed. Type into one whose onChange writes `x.b` and
 * the displayed leaf never moved -- React re-renders it with the OLD value and
 * the DOM reverts under you. One event per control, no save, no network.
 *
 * A control that reformats its input (clamping a port, trimming a slug) reverts
 * too and is not a defect. Those are listed in REFORMATS below, by hand, for
 * the same reason every other list here is: so that a new one is a decision
 * somebody wrote down rather than a silent hole.
 */

/** Controls that legitimately do not keep what they are given, and why.
 *
 *  Two shapes qualify, and neither is a defect:
 *
 *   - SAVE-THROUGH. The control has no local draft. It PUTs on change and then
 *     shows whatever the next read returns -- `checked={settings.recording
 *     .enabled}` with `onCheckedChange={(v) => saveRecording({enabled: v})}`.
 *     The stub in this file answers every read with the same document, so the
 *     old value comes straight back. Against a real server these keep their
 *     value; against this one they cannot, and that is the stub's honesty
 *     rather than the control's fault.
 *   - REFORMATTING. A control that clamps or trims what it is handed.
 *
 *  Listed by hand, like every other exception here, so a seventh entry is
 *  something somebody decided rather than a hole that opened. The population is
 *  small and it is entirely switches: the copy-paste this pass exists to catch
 *  lives in repeated TEXT blocks, and every one of those is still checked. */
const REFORMATS: Record<string, string> = {
  "Recordings #rec-enabled": "save-through: onCheckedChange calls saveRecording, no local draft",
  "Recordings #rec-stems": "save-through: onCheckedChange calls saveRecording, no local draft",
  "Playout @Enable playout": "save-through: onCheckedChange calls savePlayoutSettings",
  "Playout @Serve viewers without a session": "save-through: onCheckedChange calls onSaveSettings",
  "Playout @Allow cross-origin playback": "save-through: onCheckedChange calls onSaveSettings",
  "Playout @Enable zzq41": "save-through: a rendition row's switch, saved on change",
};

/** Controls the write pass cannot drive, with why.
 *
 *  Radix Select and Slider are the population: both are button-and-portal
 *  widgets whose value changes through a menu this pass does not open. They are
 *  already covered for DISPLAY by pass 2, and the miswiring this pass exists to
 *  catch is a copy-paste in a repeated text block, which is not their shape. */
/** A control that RESTRUCTURES the screen when driven rather than just taking a
 *  value. Ordering the batch on this keeps a section switch from unmounting the
 *  fields beneath it before they have been tested. */
function isToggle(el: Element): boolean {
  if (el instanceof HTMLInputElement) return el.type === "checkbox";
  return !(el instanceof HTMLTextAreaElement);
}

function drivable(el: Element): boolean {
  if (el instanceof HTMLTextAreaElement) return true;
  if (el instanceof HTMLInputElement) {
    return ["text", "number", "url", "email", "password", "search", "tel", "checkbox"].includes(
      el.type,
    );
  }
  return el.getAttribute("role") === "switch" || el.getAttribute("role") === "checkbox";
}

/** What to type so the control has somewhere to move to.
 *
 *  Numbers step by one from where they are, rather than to a constant: a port
 *  field that clamps to 1..65535 would reject a made-up number and read as a
 *  revert, which would be this pass inventing a defect. */
function nextValueFor(el: HTMLInputElement | HTMLTextAreaElement): string {
  if (el instanceof HTMLInputElement && el.type === "number") {
    const n = Number(el.value);
    return Number.isFinite(n) ? String(n + 1) : "2";
  }
  return el.value === "zzq42" ? "zzq43" : "zzq42";
}

interface WritePass {
  driven: string[];
  reverted: string[];
}

/** Render the screen, type into every control it can drive, and report the ones
 *  that did not keep what they were given. */
async function writePass(screen: Screen, settings: Doc): Promise<WritePass> {
  serveWith(settings);
  const route = screen.at.split("?")[0];
  render(
    <MemoryRouter initialEntries={[screen.at]}>
      <Routes>
        <Route path={route} element={screen.element} />
      </Routes>
    </MemoryRouter>,
  );
  await settle();

  const driven: string[] = [];
  const reverted: string[] = [];
  const seen = new Set<string>();

  /* ROUNDS, because half these controls are behind the other half.
   *
   * The MQTT block is the case that proved it: host, port, user and prefix do
   * not exist until `mq-enabled` is on, so a single snapshot of the DOM drives
   * the switch and never sees the four fields it just revealed -- which are
   * exactly the repeated text block this pass was written to check. One pass
   * over a snapshot silently covered one control where it should cover five.
   *
   * So: scan, drive whatever is new, let the screen settle, scan again. Each
   * control is driven once (`seen`), and the loop ends when a round reveals
   * nothing further. The bound is a backstop against a pair of controls that
   * reveal each other, not an expected exit. */
  for (let round = 0; round < 6; round++) {
    const census = new Map<string, number>();
    const batch: Array<{ el: Element; key: string }> = [];
    document.querySelectorAll(CONTROLS).forEach((el) => {
      const role = roleOf(el);
      const ordinal = census.get(role) ?? 0;
      census.set(role, ordinal + 1);
      if (isFrozen(el) || !drivable(el)) return;
      const key = `${screen.name} ${keyOf(el, ordinal)}`;
      if (seen.has(key)) return;
      batch.push({ el, key });
    });
    if (batch.length === 0) break;

    /* FIELDS BEFORE TOGGLES, and this is not a matter of taste.
     *
     * A section switch sits ABOVE the fields it reveals, so in DOM order it is
     * driven first -- and turning MQTT off unmounted host, port, user and
     * prefix before any of them had been typed into. They were then skipped as
     * disconnected, so the repeated text block this pass exists to check went
     * untested while the pass reported success. That is exactly the shape of
     * failure this file was written against, reproduced inside it.
     *
     * Typing into a field first can destroy nothing: a text change reveals and
     * hides no section here, and a toggle is the only control that restructures
     * the screen. */
    batch.sort((x, y) => Number(isToggle(x.el)) - Number(isToggle(y.el)));

    for (const { el, key } of batch) {
      // Claimed on driving, not on scanning. Marking a control seen when it had
      // merely been LOOKED AT is what stopped those four MQTT fields from ever
      // being revisited once a toggle above them had unmounted them.
      if (!el.isConnected) continue;
      seen.add(key);

      if (el instanceof HTMLInputElement && el.type === "checkbox") {
        const was = el.checked;
        await act(async () => {
          fireEvent.click(el);
        });
        driven.push(key);
        if (el.isConnected && el.checked === was) reverted.push(key);
        continue;
      }
      if (!(el instanceof HTMLInputElement) && !(el instanceof HTMLTextAreaElement)) {
        // A Radix switch: aria-checked has to flip.
        const was = el.getAttribute("aria-checked");
        await act(async () => {
          fireEvent.click(el);
        });
        driven.push(key);
        if (el.isConnected && el.getAttribute("aria-checked") === was) reverted.push(key);
        continue;
      }

      const want = nextValueFor(el);
      await act(async () => {
        fireEvent.change(el, { target: { value: want } });
      });
      driven.push(key);
      // Gone from the DOM after the change: a control that unmounts itself by
      // being edited said nothing about where it writes, so it is not counted
      // either way.
      if (el.isConnected && el.value !== want) reverted.push(key);
    }

    // Whatever those changes revealed has to finish arriving before the next
    // scan, or the round after this one reads a half-built section.
    await settle();
  }

  cleanup();
  return { driven, reverted };
}

/* ------------------------------------------------------------- the verdicts */

/** Leaves whose kind this guard cannot give a value to at all.
 *
 *  A sparse map has no leaf to fingerprint: its KEYS are the setting. Listed
 *  rather than skipped so that a new one has to be classified by hand instead
 *  of disappearing into a filter. */
const NOT_A_VALUE: Record<string, string> = {
  "automod.platformEnabled":
    "a per-platform map; the matrix's platform switches edit it, keyed by platform name rather than by a field this can fingerprint",
  "automod.on":
    'a sparse set keyed "platform/action/checker"; every cell of the matrix writes into it, and there is no leaf to give a value to',
  "playout.sourceId":
    "a branded SourceId, edited by PlayoutPage's ProgrammeCard Select whose options are the live source list plus 'default'. The walk cannot mint a SourceId that is in that domain, so it cannot fingerprint the control -- but the control exists and #774 is what added it. This is the Select-domain limit this file's header names, not a missing control",
};

/** Leaves that are deliberately not editable, and never will be. The Go guard's
 *  skip list in the same spirit: a name here is a decision. */
const NOT_A_CONTROL: Record<string, string> = {
  "mqtt.hasPassword":
    "read-only. The password is never returned by any API; it is set through the separate mq-pw field and PUT /settings/mqtt-password, and this flag only reports that one is stored",
  "automod.model.hasApiKey":
    "read-only, same shape as mqtt.hasPassword -- the key has its own endpoint and is never in the settings blob",
  "automod.rules[].id":
    "server-assigned. A rule's identity is not something an operator types",
};

/** Leaves a control DOES reach, by a route this guard cannot measure. Each one
 *  names the evidence, and the evidence is checked -- see the test below. An
 *  entry here is not a promise, it is a claim with a receipt. */
const BOUND_BUT_UNMEASURABLE: Array<{
  path: string;
  why: string;
  /** A control id that must be rendered on one of the screens above. */
  /* NO `control: "#id"` VARIANT, deliberately.
   *
   *  There was one, and what it proved was that SOME element with that id had
   *  been rendered. A `<div id="mt-vendor-0" />` satisfies it. It could not
   *  tell an editable Select from a label, a disabled trigger, or a leftover id
   *  on a wrapper -- and this is the one list in the file that CLAIMS A CONTROL
   *  EXISTS, so a receipt that cannot see the handler is the exact failure the
   *  list was written to prevent, reintroduced inside it.
   *
   *  `boundAt` reads the source and requires the binding itself, so the
   *  evidence names the write rather than the markup around it. One shape, and
   *  it is the strong one. */
  boundAt: { file: string; needle: string };
}> = [
  {
    path: "multitrack.gpus[].vendorId",
    why:
      "a Select over the PCI vendor IDs Twitch recognises. Both probe values are outside that set, so the trigger shows the same empty label for each and nothing moves",
    boundAt: {
      file: "src/pages/SettingsPage.tsx",
      needle: "onValueChange={(v) => patch(i, { vendorId: Number(v) })}",
    },
  },
  {
    path: "ingest.pull.rtspTransport",
    why:
      'a Select over tcp/udp, read as `pull.rtspTransport || "tcp"`. Any value outside the two collapses to the same label, so both probe values render identically',
    boundAt: {
      file: "src/components/PullIngestFields.tsx",
      needle: 'value={pull.rtspTransport || "tcp"}',
    },
  },
  {
    path: "playout.variants[].renditionId",
    why:
      "a Select over the renditions this install has. The fixture cannot invent renditions with the probe's ids, so the trigger falls back to the same label for both",
    boundAt: {
      file: "src/pages/PlayoutPage.tsx",
      needle: 'value={v.renditionId == null ? "source" : String(v.renditionId)}',
    },
  },
  {
    path: "failover.playlist.items[].upload",
    why:
      "edited by row rather than by field: an entry is added from the uploads picker, reordered and removed by its own buttons, and shown as text. There is no control holding the value",
    boundAt: { file: "src/components/PlaylistEditor.tsx", needle: "{item.upload}" },
  },
];

/** THE BUGS. Leaves with no control anywhere on any screen this file renders --
 *  settings the server accepts, validates and acts on, that no operator can
 *  change.
 *
 *  This list is not a permission. It is the reachability sweep's findings,
 *  written where the next change to the UI will trip over them: add the control
 *  and this file fails until the line is deleted. */
const NO_CONTROL_YET: Record<string, string> = {



  // The whole standby-input block. FailoverBackup is a complete ingest
  // description -- mode, SRT, RTMP and pull -- with no editor at all.
  "failover.backup.mode": "#788 sweep. No standby-input editor exists",
  "failover.backup.srt.passphrase": "#788 sweep. No standby-input editor exists",
  "failover.backup.srt.latencyMs": "#788 sweep. No standby-input editor exists",
  "failover.backup.rtmp.app": "#788 sweep. No standby-input editor exists",
  "failover.backup.rtmp.streamKey": "#788 sweep. No standby-input editor exists",
  "failover.backup.pull.url": "#788 sweep. No standby-input editor exists",
  "failover.backup.pull.reconnectDelayMaxSeconds": "#788 sweep. No standby-input editor exists",
  "failover.backup.pull.rtspTransport": "#788 sweep. No standby-input editor exists",


  // Automod's stored policy. The matrix edits which cells are on; everything
  // that decides what those cells DO is unreachable.

  "automod.history.windowSeconds": "#788 sweep. No sequence-detector editor exists",
  "automod.history.maxMessages": "#788 sweep. No sequence-detector editor exists",
  "automod.history.maxRepeats": "#788 sweep. No sequence-detector editor exists",
  "automod.history.maxLinks": "#788 sweep. No sequence-detector editor exists",
  "automod.history.maxMentionsPerMessage": "#788 sweep. No sequence-detector editor exists",
  "automod.history.minLengthForCaps": "#788 sweep. No sequence-detector editor exists",
  "automod.history.maxCapsRatio": "#788 sweep. No sequence-detector editor exists",
  "automod.history.action": "#788 sweep. No sequence-detector editor exists",
  "automod.history.timeoutSeconds": "#788 sweep. No sequence-detector editor exists",
  "automod.history.retainPerAuthor": "#788 sweep. No sequence-detector editor exists",
  "automod.history.idleEvictionSeconds": "#788 sweep. No sequence-detector editor exists",

};

/* ----------------------------------------------------------------- the walk */

interface Walk {
  /** leaf path -> how it was reached. */
  reached: Map<string, string>;
  /** leaf path -> leaf, for everything nothing reached. */
  unreached: Map<string, Leaf>;
  /** screen name -> how many controls its baseline render produced. */
  controlsPerScreen: Map<string, number>;
  /** every control identity seen in any render, for the evidence check below. */
  identities: Set<string>;
}

async function walkEveryScreen(leaves: Leaf[]): Promise<Walk> {
  const index = new Map(leaves.map((l, i) => [l.path, i] as const));
  const reached = new Map<string, string>();
  const unreached = new Map(leaves.map((l) => [l.path, l] as const));
  const controlsPerScreen = new Map<string, number>();
  const identities = new Set<string>();

  const allBooleans = new Set(leaves.filter((l) => l.kind === "boolean").map((l) => l.path));

  for (const screen of SCREENS) {
    /** Anything the fingerprint can claim from one render. */
    const harvest = (shot: Shot, how: string) => {
      for (const [path, leaf] of [...unreached]) {
        // A boolean shows `true`, and so does every other boolean; an enum
        // shows a label rather than its value. Neither is a fingerprint.
        if (leaf.kind === "boolean" || leaf.kind === "enum") continue;
        if (shot.shown.has(String(value(leaf, index.get(path)!, 0)))) {
          reached.set(path, `${screen.name} (${how})`);
          unreached.delete(path);
        }
      }
    };

    const baseline = await shoot(screen, document_(leaves, new Set()));
    controlsPerScreen.set(screen.name, baseline.keyed.size);
    for (const key of baseline.keyed.keys()) identities.add(key);
    // Nothing rendered. Every probe below would turn the macrotask queue over
    // forty times against the same blank page to learn what this already knows,
    // so the screen is abandoned here and the positive control reports it.
    if (baseline.keyed.size === 0) continue;
    harvest(baseline, "value");

    // The same screen with every boolean the other way round, because a card
    // shown only while something is OFF is as real as one shown while it is on.
    harvest(await shoot(screen, document_(leaves, allBooleans)), "value, booleans off");

    /** Did changing exactly these leaves move a control that exists in both
     *  renders? Controls that came or went are not evidence -- see the header. */
    const differs = async (paths: string[]): Promise<boolean> => {
      const shot = await shoot(screen, document_(leaves, new Set(paths)));
      for (const [key, v] of shot.keyed) {
        if (key.startsWith("~")) {
          const role = key.slice(1, key.indexOf("["));
          if (shot.census.get(role) !== baseline.census.get(role)) continue;
        }
        const before = baseline.keyed.get(key);
        if (before !== undefined && before !== v) return true;
      }
      return false;
    };

    /** Group testing: ask about the whole set, and only split what answers. */
    const hunt = async (paths: string[]): Promise<string[]> => {
      if (paths.length === 0) return [];
      if (!(await differs(paths))) return [];
      if (paths.length === 1) return paths;
      const half = Math.ceil(paths.length / 2);
      return [...(await hunt(paths.slice(0, half))), ...(await hunt(paths.slice(half)))];
    };

    const bound = await hunt([...unreached.keys()]);
    for (const path of bound) {
      reached.set(path, `${screen.name} (probe)`);
      unreached.delete(path);
    }

    // An enum WITH a control on this screen is usually the switch that decides
    // what else the screen shows, so the screen is rendered once per choice.
    // One that has no control here decides nothing here, and gets no renders.
    for (const path of bound) {
      const leaf = leaves[index.get(path)!];
      if (leaf.kind !== "enum") continue;
      for (const choice of (leaf.choices ?? []).slice(1)) {
        const doc = document_(leaves, new Set());
        put(doc, path, choice);
        harvest(await shoot(screen, doc), `value, ${path}=${choice}`);
      }
    }
  }

  return { reached, unreached, controlsPerScreen, identities };
}

/* ---------------------------------------------------------------- the tests */

let walk: Walk;
let leaves: Leaf[];

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe("every settings leaf an operator is meant to change", () => {
  // One walk, read by every assertion below. Rendering four screens a couple of
  // hundred times is the whole cost of this file, and doing it once per
  // assertion would multiply it for no extra evidence.
  beforeEach(async () => {
    if (walk) return;
    leaves = settingsLeaves();
    walk = await walkEveryScreen(leaves.filter((l) => l.kind !== "opaque"));
  }, 120_000);

  /* THE POSITIVE CONTROL, and the reason it comes first.
   *
   * Every other assertion in this file is of the form "the walk found a control
   * for X". A walk that finds NOTHING satisfies none of them by failing loudly
   * -- but a walk that finds nothing and is compared against a list of things
   * it was not expected to find passes in silence. A renamed route, a changed
   * role, a page split into two components: any of them turns this file into a
   * guard that measures an empty set and reports success.
   *
   * That exact failure -- a check that is green because it is looking at
   * nothing -- is the one this repository keeps finding. So each screen states
   * a floor, and the floor is checked before anything is concluded from the
   * walk. */
  it("renders real controls on every screen it claims to walk", () => {
    for (const screen of SCREENS) {
      const found = walk.controlsPerScreen.get(screen.name) ?? 0;
      expect(
        found,
        `${screen.name} produced ${found} controls, fewer than the ${screen.atLeast} it must. ` +
          `Either the screen no longer renders at ${screen.at} -- a renamed route, a moved tab, ` +
          `a read the fetch stub in this file does not answer -- or the selector this file walks ` +
          `no longer matches its controls. Until that is fixed every reachability verdict below ` +
          `is measured over an empty screen and means nothing.`,
      ).toBeGreaterThanOrEqual(screen.atLeast);
    }
  });

  /* THE SAME CONTROL, NAMED. The floor above says each screen produced
   * SOMETHING; this says the walk found these particular leaves, one from each
   * screen and one behind a second network read.
   *
   * automod.enabled is on the list on purpose. Its switch belongs to the matrix,
   * which arrives from its own endpoint after the settings form is already on
   * screen -- so before `shoot` waited for the render to settle, this leaf came
   * back reachable or not depending on scheduling, and the only symptom was a
   * different excuse list failing on different runs. A named leaf that has to
   * be found turns that back into one failure with one cause.
   *
   * These are not the leaves this file is about. They are the proof it is
   * looking at the screens it says it is. */
  const MUST_BE_FOUND = [
    "ingest.srt.passphrase", // Settings / ingest
    "mqtt.brokerUrl", // Settings / pipeline
    "automod.enabled", // Settings / pipeline, behind GET /automod/matrix
    "recording.enabled", // Recordings
    "playout.maxSessions", // Playout
  ];

  it("finds the controls it is known to be able to find", () => {
    const lost = MUST_BE_FOUND.filter((p) => !walk.reached.has(p));
    expect(
      lost,
      `${lost.join(", ")} has a control on one of these screens and the walk did not find it. ` +
        `Nothing below can be trusted while that is true: an excuse list read against a walk ` +
        `that is missing controls is a list of fields wrongly declared unreachable.`,
    ).toEqual([]);
  });

  /** The other half of the same control: the walk must be able to say NO.
   *
   *  A fingerprint pass that matched everything -- a sentinel colliding with
   *  something the UI renders anyway, a `shown` set that accidentally contains
   *  the empty string and swallows every leaf -- would report a fully reachable
   *  tree and prove nothing. Automod's model block is unreachable today and its
   *  own entry in NO_CONTROL_YET says so; if the walk starts calling it
   *  reachable while no editor exists, the walk is broken, not the UI. */
  it("still reports an unreachable leaf as unreachable", () => {
    expect(
      walk.unreached.size,
      "the walk found a control for every settings leaf. That would be good news, and it is " +
        "not what this UI looks like -- check the fingerprint has not started matching " +
        "everything before deleting the lists in this file.",
    ).toBeGreaterThan(0);
  });

  it("gives every leaf a control, or a reason it has none", () => {
    const excused = new Set([
      ...Object.keys(NOT_A_CONTROL),
      ...Object.keys(NO_CONTROL_YET),
      ...BOUND_BUT_UNMEASURABLE.map((e) => e.path),
    ]);
    const orphans = [...walk.unreached.keys()].filter((p) => !excused.has(p));

    expect(
      orphans,
      `no control anywhere reaches ${orphans.join(", ")}. The server stores and acts on ` +
        `${orphans.length === 1 ? "it" : "them"} and no operator can change ` +
        `${orphans.length === 1 ? "it" : "them"}. Add an input, switch or select bound to ` +
        `the field -- and give it an accessible name, because that is how this suite finds ` +
        `controls -- or add the path to one of the lists in this file with the reason it has none.`,
    ).toEqual([]);
  });

  /* An excuse that has stopped being true is worse than no excuse: it is a
   * reason to stop looking, attached to a field somebody has since fixed. */
  it("keeps no excuse for a leaf that now has a control", () => {
    const stale = [
      ...Object.keys(NOT_A_CONTROL),
      ...Object.keys(NO_CONTROL_YET),
      ...BOUND_BUT_UNMEASURABLE.map((e) => e.path),
      ...Object.keys(NOT_A_VALUE),
    ].filter((p) => walk.reached.has(p));

    expect(
      stale,
      `${stale.join(", ")} now has a control this walk can see: ${stale
        .map((p) => walk.reached.get(p))
        .join(", ")}. Delete the entry from the list in this file that excuses it. ` +
        `If the fix was somebody adding the control, this failure is how it is confirmed.`,
    ).toEqual([]);
  });

  /* Every path in every list has to be a path that exists. A leaf renamed on
   * the Go side and in types.ts leaves its excuse behind, still spelled the old
   * way, silently excusing nothing while the renamed field goes unwatched. */
  it("excuses only leaves that exist", () => {
    const known = new Set(leaves.map((l) => l.path));
    const ghosts = [
      ...Object.keys(NOT_A_CONTROL),
      ...Object.keys(NO_CONTROL_YET),
      ...Object.keys(NOT_A_VALUE),
      ...BOUND_BUT_UNMEASURABLE.map((e) => e.path),
    ].filter((p) => !known.has(p));

    expect(
      ghosts,
      `${ghosts.join(", ")} is excused by a list in this file and is not a leaf of Settings. ` +
        `Either it was renamed -- in which case the renamed leaf is now unwatched -- or the ` +
        `excuse outlived the field.`,
    ).toEqual([]);
  });

  /* A leaf whose kind carries no value to fingerprint has to be named, or the
   * walk would drop it without anybody deciding to. */
  it("names every leaf it cannot give a value to", () => {
    const opaque = leaves.filter((l) => l.kind === "opaque").map((l) => l.path);
    const unexplained = opaque.filter((p) => !(p in NOT_A_VALUE));
    expect(
      unexplained,
      `${unexplained.join(", ")} has a type this file cannot build a fixture value for, so ` +
        `it is walked past rather than checked. Add it to NOT_A_VALUE with what edits it, ` +
        `or give it a shape the walk understands.`,
    ).toEqual([]);
  });

  it("every control it can drive keeps what it is given", async () => {
    const driven: string[] = [];
    const reverted: string[] = [];
    for (const screen of SCREENS) {
      const pass = await writePass(screen, document_(leaves, new Set()));
      driven.push(...pass.driven);
      reverted.push(...pass.reverted);
    }

    // POSITIVE CONTROL. A pass that drove nothing reports no reverts, which
    // looks exactly like a pass where everything is wired correctly.
    expect(
      driven.length,
      "the write pass drove no controls at all, so it is provably not checking anything",
    ).toBeGreaterThan(20);

    // TEMP PROBE
    const unexplained = reverted.filter((k) => !(k in REFORMATS));
    expect(
      unexplained,
      `these controls did not keep what was typed into them. A CONTROLLED input that ` +
        `discards your keystrokes is displaying one leaf and writing to another -- the ` +
        `copy-paste this pass exists to catch. If instead the control legitimately ` +
        `reformats its input (clamping a port, trimming a slug), add it to REFORMATS ` +
        `with which it is.`,
    ).toEqual([]);
  });

  /* POSITIVE CONTROL FOR THE FROZEN-CONTROL FILTER.
   *
   * `isFrozen` removes controls from the walk. A filter that matches nothing is
   * indistinguishable from no filter at all, and it would go on being
   * indistinguishable for as long as nobody rendered a disabled control -- at
   * which point the leaf behind it would be credited again with this file
   * still green. Asserting that the screens DO contain frozen controls is what
   * keeps the filter honest about doing something. */
  it("actually steps over frozen controls", () => {
    expect(
      FROZEN_SEEN.size,
      "no disabled or read-only control was seen on any of the four screens, so " +
        "the isFrozen filter is provably doing nothing and would not notice a " +
        "setting that became unreachable behind a permanently disabled control",
    ).toBeGreaterThan(0);
  });

  /* BOUND_BUT_UNMEASURABLE is the only list that claims a control EXISTS. A
   * claim with no check is the thing this whole file was written against, so
   * each entry carries its evidence and the evidence is read. */
  it("checks the control every unmeasurable entry claims", () => {
    // POSITIVE CONTROL. A for-loop over an empty list passes, and this test's
    // whole job is reading evidence -- so "there was evidence to read" has to
    // be asserted before the reading.
    expect(BOUND_BUT_UNMEASURABLE.length).toBeGreaterThan(0);

    for (const entry of BOUND_BUT_UNMEASURABLE) {
      const src = readFileSync(join(ROOT, entry.boundAt.file), "utf8");
      expect(
        src.includes(entry.boundAt.needle),
        `${entry.path} is excused because ${entry.boundAt.file} binds it as ` +
          `\`${entry.boundAt.needle}\`, and that binding is no longer there. Either the ` +
          `control moved -- update the entry -- or it is gone and the leaf is unreachable.`,
      ).toBe(true);
    }
  });
});
