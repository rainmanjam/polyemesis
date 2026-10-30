import { existsSync, readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import { KEY_STATE } from "../src/render.js";
import { STREAM_DECK_KEYS } from "../src/keys.js";

const ROOT = join(dirname(fileURLToPath(import.meta.url)), "..");
const PLUGIN = join(ROOT, "com.polyemesis.streamdeck.sdPlugin");
const manifest = JSON.parse(readFileSync(join(PLUGIN, "manifest.json"), "utf8")) as {
  UUID: string;
  Version: string;
  SDKVersion: number;
  Software: { MinimumVersion: string };
  OS: { Platform: string; MinimumVersion: string }[];
  Nodejs: { Version: string };
  CodePath: string;
  Icon: string;
  CategoryIcon: string;
  Actions: {
    UUID: string;
    Icon: string;
    Controllers: string[];
    PropertyInspectorPath: string;
    States: { Image: string }[];
    DisableAutomaticStates?: boolean;
    UserTitleEnabled?: boolean;
    SupportedInMultiActions?: boolean;
  }[];
};

// The values the Elgato manifest schema accepts, read from
// node_modules/@elgato/schemas/streamdeck/plugins/manifest.json.
const SCHEMA_SOFTWARE_VERSIONS = ["6.4", "6.5", "6.6", "6.7", "6.8", "6.9", "7.0", "7.1", "7.2", "7.3", "7.4", "7.5", "7.6"];
const SCHEMA_NODE_VERSIONS = ["20", "24", "26"];

/** Resolves a manifest image path: Stream Deck adds the .png and the @2x variant itself. */
function imageExists(rel: string): boolean {
  return existsSync(join(PLUGIN, `${rel}.png`)) && existsSync(join(PLUGIN, `${rel}@2x.png`));
}

describe("manifest", () => {
  it("has a reverse-DNS UUID and actions prefixed by it", () => {
    expect(manifest.UUID).toMatch(/^[a-z0-9.-]+$/);
    for (const a of manifest.Actions) expect(a.UUID.startsWith(`${manifest.UUID}.`)).toBe(true);
  });

  it("declares a Software and Node version the schema accepts", () => {
    expect(SCHEMA_SOFTWARE_VERSIONS).toContain(manifest.Software.MinimumVersion);
    expect(SCHEMA_NODE_VERSIONS).toContain(manifest.Nodejs.Version);
  });

  it("uses SDK version 3 and a four-part version", () => {
    expect(manifest.SDKVersion).toBe(3);
    expect(manifest.Version).toMatch(/^(0|[1-9]\d*)(\.(0|[1-9]\d*)){3}$/);
  });

  it("names only mac and windows, the platforms the schema allows", () => {
    expect(manifest.OS.map((o) => o.Platform).sort()).toEqual(["mac", "windows"]);
  });

  it("points at a code path and icons that exist in the plugin folder", () => {
    expect(manifest.CodePath).toBe("bin/plugin.js");
    expect(imageExists(manifest.Icon)).toBe(true);
    expect(imageExists(manifest.CategoryIcon)).toBe(true);
  });

  // Each key action has its own face set: imgs/keys/<action>/<state>, with the action's own glyph.
  const shortName = (uuid: string) => uuid.replace(`${manifest.UUID}.`, "");

  it("gives every key action the same states, in KEY_STATE order, from its own face set", () => {
    const keys = manifest.Actions.filter((a) => a.Controllers.includes("Keypad"));
    expect(keys.length).toBeGreaterThan(0);
    const expected = [
      "idle", "live", "warn", "error", "held", "unbound", "halted", "needsAdmin", "muted", "restarted", "unavailable",
    ];
    expect(Object.keys(KEY_STATE)).toEqual(expected);
    for (const a of keys) {
      // The device draws States[index]; the runtime sends KEY_STATE[tone] as index.
      expect(a.States, a.UUID).toHaveLength(expected.length);
      for (const name of expected) {
        const index = KEY_STATE[name as keyof typeof KEY_STATE];
        expect(a.States[index]?.Image, `${a.UUID} state ${name}`).toBe(
          `imgs/keys/${shortName(a.UUID)}/${name}`,
        );
      }
    }
  });

  it("gives each key action's states an image on disk, at both sizes", () => {
    const keys = manifest.Actions.filter((a) => a.Controllers.includes("Keypad"));
    for (const a of keys) {
      for (const n of Object.keys(KEY_STATE)) {
        expect(imageExists(`imgs/keys/${shortName(a.UUID)}/${n}`), `${a.UUID} ${n}`).toBe(true);
      }
    }
  });

  it("gives every action an icon and a property inspector that exist", () => {
    for (const a of manifest.Actions) {
      expect(imageExists(a.Icon), a.UUID).toBe(true);
      expect(existsSync(join(PLUGIN, a.PropertyInspectorPath)), a.UUID).toBe(true);
    }
  });

  it("exposes exactly the actions the plugin implements, and the dial is an encoder", () => {
    expect(manifest.Actions.map((a) => a.UUID.replace(`${manifest.UUID}.`, "")).sort()).toEqual(
      ["destination", "failover", "go-live", "meter", "preset", "record", "stop-all", "stop-destination", "track-gain", "track-mute"],
    );
    expect(manifest.Actions.find((a) => a.UUID.endsWith(".meter"))!.Controllers).toEqual(["Encoder"]);
    expect(manifest.Actions.find((a) => a.UUID.endsWith(".track-gain"))!.Controllers).toEqual(["Encoder"]);
  });

  it("stops Stream Deck advancing key states and overriding titles, because the plugin paints them", () => {
    for (const a of manifest.Actions.filter((x) => x.Controllers.includes("Keypad"))) {
      expect(a.DisableAutomaticStates, a.UUID).toBe(true);
      expect(a.UserTitleEnabled, a.UUID).toBe(false);
    }
  });

  it("keeps stop-all out of multi-actions, where its hold could not complete", () => {
    const stop = manifest.Actions.find((a) => a.UUID.endsWith(".stop-all"))!;
    expect(stop.SupportedInMultiActions).toBe(false);
  });

  it("tells the user what the gain dial's rotate, push and touch do", () => {
    const dial = manifest.Actions.find((a) => a.UUID.endsWith(".track-gain")) as unknown as {
      Encoder: { layout: string; TriggerDescription: Record<string, string> };
    };
    expect(dial.Encoder.layout).toBe("$B1");
    expect(Object.keys(dial.Encoder.TriggerDescription).sort()).toEqual(["Push", "Rotate", "Touch"]);
  });

  it("ships track mute as a key that paints its own faces, now that the server mutes live (I4)", () => {
    const mute = manifest.Actions.find((a) => a.UUID.endsWith(".track-mute"))!;
    expect(mute.Controllers).toEqual(["Keypad"]);
    expect(mute.States[KEY_STATE.muted]!.Image).toBe("imgs/keys/track-mute/muted");
    expect(mute.States[KEY_STATE.restarted]!.Image).toBe("imgs/keys/track-mute/restarted");
  });
});

describe("translations are used", () => {
  it("references every catalog key from the code or the inspector", () => {
    const sources = [
      "src/keyFace.ts",
      "src/render.ts",
      "src/levelsFeed.ts",
      "src/plugin.ts",
      "src/config.ts",
      "src/runtime.ts",
      "com.polyemesis.streamdeck.sdPlugin/ui/pi.html",
      "com.polyemesis.streamdeck.sdPlugin/ui/pi.js",
    ]
      .map((rel) => readFileSync(join(ROOT, rel), "utf8"))
      .join("\n");
    const unused = STREAM_DECK_KEYS.filter((k) => !sources.includes(k));
    expect(unused).toEqual([]);
  });
});
