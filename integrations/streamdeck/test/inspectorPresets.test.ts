import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { runInNewContext } from "node:vm";
import { describe, expect, it } from "vitest";

// The property inspector's preset picker, run against a minimal fake DOM.
//
// The bug this pins: the saved preset arrives (didReceiveSettings) before the
// install's list does, so setting the select's value then found no option and
// was dropped, and the list then selected its FIRST preset. The picker showed a
// preset the key did not hold. A real <select> refuses a value it has no option
// for, and the fake below does the same, which is what makes this reproducible.

const PI = join(dirname(fileURLToPath(import.meta.url)), "..", "com.polyemesis.streamdeck.sdPlugin", "ui", "pi.js");

interface FakeEl {
  id: string;
  hidden: boolean;
  textContent: string;
  className: string;
  value: string;
  children: FakeEl[];
  listeners: Record<string, (() => void)[]>;
  addEventListener(type: string, fn: () => void): void;
  replaceChildren(...els: FakeEl[]): void;
  appendChild(el: FakeEl): void;
}

function element(id: string, isSelect = false): FakeEl {
  let value = "";
  const el: FakeEl = {
    id,
    hidden: false,
    textContent: "",
    className: "",
    children: [],
    listeners: {},
    get value() {
      return value;
    },
    set value(v: string) {
      // A <select> keeps a value only if one of its options carries it.
      if (!isSelect || el.children.some((c) => c.value === v)) value = v;
      else value = "";
    },
    addEventListener(type, fn) {
      (el.listeners[type] ??= []).push(fn);
    },
    replaceChildren(...els) {
      el.children = els;
      if (isSelect) value = els[0]?.value ?? "";
    },
    appendChild(child) {
      el.children.push(child);
      if (isSelect && el.children.length === 1) value = child.value;
    },
  };
  return el;
}

function loadInspector(action: string) {
  const ids = [
    "url", "token", "caPem", "sourceId", "destinationId", "presetId", "err-preset", "result",
    "admin-warning", "test", "block-source", "block-destination", "block-preset", "block-install-wide",
    "block-track", "trackDestination", "track", "err-track",
  ];
  const selects = ["presetId", "trackDestination", "track"];
  const els = Object.fromEntries(ids.map((id) => [id, element(id, selects.includes(id))]));
  const sent: unknown[] = [];
  let socket: { onopen?: () => void; onmessage?: (m: { data: string }) => void; readyState: number; send(s: string): void };
  const window: Record<string, unknown> = {
    POLYEMESIS_STRINGS: {
      en: {
        "streamdeck.pi.presetChoose": "Choose a preset",
        "streamdeck.pi.presetMissing": "{id} (gone)",
        "streamdeck.pi.destinationChoose": "Choose a destination",
        "streamdeck.pi.trackChoose": "Choose a track",
        "streamdeck.pi.notListed": "{id} (not listed)",
        "streamdeck.pi.destinationsFailed": "No list",
      },
    },
  };
  const context = {
    window,
    document: {
      getElementById: (id: string) => els[id],
      querySelectorAll: () => [],
      createElement: () => element("option"),
    },
    WebSocket: function () {
      socket = { readyState: 1, send: (s: string) => sent.push(JSON.parse(s)) };
      return socket;
    },
    JSON,
    Number,
    Object,
    String,
  };
  runInNewContext(readFileSync(PI, "utf8"), context);
  (window.connectElgatoStreamDeckSocket as (...a: unknown[]) => void)(
    1, "plugin-uuid", "registerPropertyInspector", JSON.stringify({ application: { language: "en" } }),
    JSON.stringify({ action, context: "ctx" }),
  );
  socket!.onopen!();
  const receive = (event: string, payload: unknown) => socket!.onmessage!({ data: JSON.stringify({ event, payload }) });
  const change = (id: string) => els[id]!.listeners.change!.forEach((fn) => fn());
  return { els, sent, receive, change };
}

const LIST = { type: "presets", presets: [{ id: "music", name: "Music" }, { id: "talk", name: "Talk" }] };

describe("inspector preset picker", () => {
  it("shows the key's saved preset when the settings arrive before the list", () => {
    const { els, receive } = loadInspector("com.polyemesis.streamdeck.preset");
    receive("didReceiveSettings", { settings: { presetId: "talk" } });
    receive("sendToPropertyInspector", LIST);
    expect(els.presetId!.value).toBe("talk");
  });

  it("shows the key's saved preset when the list arrives first", () => {
    const { els, receive } = loadInspector("com.polyemesis.streamdeck.preset");
    receive("sendToPropertyInspector", LIST);
    receive("didReceiveSettings", { settings: { presetId: "talk" } });
    expect(els.presetId!.value).toBe("talk");
  });

  it("chooses nothing for a key with no preset, rather than the first in the list", () => {
    const { els, receive } = loadInspector("com.polyemesis.streamdeck.preset");
    receive("didReceiveSettings", { settings: {} });
    receive("sendToPropertyInspector", LIST);
    expect(els.presetId!.value).toBe("");
    expect(els.presetId!.children[0]!.textContent).toBe("Choose a preset");
  });

  it("keeps a saved preset the install no longer lists, marked, instead of dropping it", () => {
    const { els, receive } = loadInspector("com.polyemesis.streamdeck.preset");
    receive("didReceiveSettings", { settings: { presetId: "old" } });
    receive("sendToPropertyInspector", LIST);
    expect(els.presetId!.value).toBe("old");
    expect(els.presetId!.children.at(-1)!.textContent).toBe("old (gone)");
  });

  it("asks the plugin for the list, never the server, when it opens on a preset key", () => {
    const { sent } = loadInspector("com.polyemesis.streamdeck.preset");
    expect(sent).toContainEqual(expect.objectContaining({ event: "sendToPlugin", payload: { type: "presets" } }));
  });

  it("hides the programme field on the install-wide Record key", () => {
    const record = loadInspector("com.polyemesis.streamdeck.record");
    expect(record.els["block-source"]!.hidden).toBe(true);
    expect(record.els["block-install-wide"]!.hidden).toBe(false);
    const preset = loadInspector("com.polyemesis.streamdeck.preset");
    expect(preset.els["block-source"]!.hidden).toBe(false);
  });
});

const MUTE = "com.polyemesis.streamdeck.track-mute";
const GAIN = "com.polyemesis.streamdeck.track-gain";
const DESTS = {
  type: "destinations",
  destinations: [
    { id: 5, name: "Twitch", tracks: [{ track: 0, name: "Main" }, { track: 2, name: "Guest mic" }] },
    { id: 6, name: "YouTube", tracks: [{ track: 1, name: "Track 2" }] },
  ],
};
const settingsSent = (sent: unknown[]) =>
  sent.filter((m) => (m as { event?: string }).event === "setSettings").map((m) => (m as { payload: unknown }).payload);

describe("inspector track picker", () => {
  it("shows only the track block on a track key, and asks for the key's programme's destinations once its settings arrive", () => {
    const { els, sent, receive } = loadInspector(MUTE);
    expect(els["block-track"]!.hidden).toBe(false);
    expect(els["block-destination"]!.hidden).toBe(true);
    expect(els["block-source"]!.hidden).toBe(false);
    // Not before: the form does not hold the programme yet, and the list would be every programme's.
    expect(sent.some((m) => (m as { payload?: { type?: string } }).payload?.type === "destinations")).toBe(false);
    receive("didReceiveSettings", { settings: { sourceId: 7 } });
    expect(sent).toContainEqual(expect.objectContaining({ event: "sendToPlugin", payload: { type: "destinations", sourceId: 7 } }));
    receive("sendToPropertyInspector", DESTS);
    expect(els.trackDestination!.children.map((c) => c.textContent)).toEqual(["Choose a destination", "Twitch", "YouTube"]);
  });

  it("shows the saved destination and track 0 when the settings arrive before the list", () => {
    const { els, receive } = loadInspector(MUTE);
    receive("didReceiveSettings", { settings: { destinationId: 5, track: 0 } });
    receive("sendToPropertyInspector", DESTS);
    expect(els.trackDestination!.value).toBe("5");
    expect(els.track!.value).toBe("0");
    expect(els.track!.children.map((c) => c.textContent)).toEqual(["Choose a track", "Main", "Guest mic"]);
  });

  it("shows the saved destination and track when the list arrives first", () => {
    const { els, receive } = loadInspector(MUTE);
    receive("sendToPropertyInspector", DESTS);
    receive("didReceiveSettings", { settings: { destinationId: 5, track: 2 } });
    expect(els.trackDestination!.value).toBe("5");
    expect(els.track!.value).toBe("2");
  });

  it("keeps a saved track the destination no longer selects, marked, instead of choosing another", () => {
    const { els, receive } = loadInspector(MUTE);
    receive("didReceiveSettings", { settings: { destinationId: 5, track: 4 } });
    receive("sendToPropertyInspector", DESTS);
    expect(els.track!.value).toBe("4");
    expect(els.track!.children.at(-1)!.textContent).toBe("4 (not listed)");
  });

  it("saves track 0 as 0, saves no track when none is chosen, and clears the track on a new destination", () => {
    const { els, sent, receive, change } = loadInspector(MUTE);
    receive("sendToPropertyInspector", DESTS);
    receive("didReceiveSettings", { settings: { sourceId: 7 } });
    els.trackDestination!.value = "5";
    change("trackDestination");
    expect(settingsSent(sent).at(-1)).toEqual({ sourceId: 7, destinationId: 5 });
    els.track!.value = "0";
    change("track");
    expect(settingsSent(sent).at(-1)).toEqual({ sourceId: 7, destinationId: 5, track: 0 });
    els.trackDestination!.value = "6";
    change("trackDestination");
    expect(settingsSent(sent).at(-1)).toEqual({ sourceId: 7, destinationId: 6 });
  });

  it("asks again for the new programme's destinations when the programme changes", () => {
    const { els, sent, change } = loadInspector(MUTE);
    els.sourceId!.value = "8";
    change("sourceId");
    expect(sent.at(-1)).toMatchObject({ event: "sendToPlugin", payload: { type: "destinations", sourceId: 8 } });
  });

  it("says why when the list could not be loaded", () => {
    const { els, receive } = loadInspector(MUTE);
    receive("sendToPropertyInspector", { type: "destinations", key: "streamdeck.pi.destinationsFailed" });
    expect(els["err-track"]!.hidden).toBe(false);
    expect(els["err-track"]!.textContent).toBe("No list");
  });

  it("shows the same pickers on the gain dial", () => {
    const { els, sent, receive } = loadInspector(GAIN);
    expect(els["block-track"]!.hidden).toBe(false);
    receive("didReceiveSettings", { settings: {} });
    expect(sent).toContainEqual(expect.objectContaining({ event: "sendToPlugin", payload: { type: "destinations" } }));
  });

  it("does not show the track block on other keys", () => {
    expect(loadInspector("com.polyemesis.streamdeck.destination").els["block-track"]!.hidden).toBe(true);
  });
});
