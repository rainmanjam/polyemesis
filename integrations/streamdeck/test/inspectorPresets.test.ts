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
  ];
  const els = Object.fromEntries(ids.map((id) => [id, element(id, id === "presetId")]));
  const sent: unknown[] = [];
  let socket: { onopen?: () => void; onmessage?: (m: { data: string }) => void; readyState: number; send(s: string): void };
  const window: Record<string, unknown> = {
    POLYEMESIS_STRINGS: { en: { "streamdeck.pi.presetChoose": "Choose a preset", "streamdeck.pi.presetMissing": "{id} (gone)" } },
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
  return { els, sent, receive };
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
