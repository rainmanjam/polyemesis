// A destination key's image: the state tile, the platform's mark and the destination's name, drawn by
// src/keyImage.ts and sent with setImage. The state and its reason stay the title, so these tests hold
// the image to three things: it is the same tile the manifest's PNG is, its mark and name read on that
// tile (WCAG AA, as test/keyContrast.test.ts holds the title), and it never reaches a title's lines.
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import { destinationFace } from "../src/keyFace.js";
import {
  BAND,
  FACE,
  NAME_MAX_PX,
  NAME_MIN_PX,
  NAME_TITLE_LINES_MAX,
  destinationImage,
  destinationLayout,
  displayName,
  fitName,
  glyphFor,
  imagePainter,
  nameRepeatsMark,
  layoutSvg,
  textWidth,
} from "../src/keyImage.js";
import type { DestStatus } from "../src/model.js";
import { KEY_STATE } from "../src/render.js";
import { ADMIN, PAIRED, READ, json, runtimeFor, settle, surface } from "./fakeInstall.js";
import { contrast, KEY_COLOURS, SHADE_SLOPE, SHADE_TOP, TITLE_COLOURS } from "../scripts/keyPalette.mjs";

const AA = 4.5;
const ROOT = join(dirname(fileURLToPath(import.meta.url)), "..");
const TONES = Object.keys(KEY_STATE) as (keyof typeof KEY_STATE)[];
const MARKED = ["youtube", "twitch", "kick", "facebook", "x"] as const;

/** What facekit and the device draw for a title: 22/144 type, 1.14 line height, 9/144 off the bottom. */
const TITLE_LINE = 22 * 1.14;
const TITLE_BOTTOM = 9;
const titleTop = (lines: number) => FACE - TITLE_BOTTOM - lines * TITLE_LINE;

const decode = (url: string) => {
  const m = /^data:image\/svg\+xml;base64,(.+)$/.exec(url);
  if (!m) throw new Error(`not an SVG data URL: ${url.slice(0, 40)}`);
  return Buffer.from(m[1]!, "base64").toString("utf8");
};

describe("the tile is the manifest's tile", () => {
  it.each(TONES)("%s: the state colour, lit from the top as scripts/icons.mjs lights it", (tone) => {
    const l = destinationLayout(tone, { platform: "twitch", name: "Twitch" }, 1);
    const base = KEY_COLOURS[tone]!;
    expect(l.tileTop).toEqual(base.map((c) => Math.min(255, Math.round(c * SHADE_TOP))));
    expect(l.tileBottom).toEqual(base.map((c) => Math.min(255, Math.round(c * (SHADE_TOP - SHADE_SLOPE)))));
    expect(l.ink).toEqual(TITLE_COLOURS[tone]);
  });
});

describe("the mark and name read on the tile", () => {
  it.each(TONES)("%s: the ink reaches AA over the whole top band", (tone) => {
    const base = KEY_COLOURS[tone]!;
    const ink = TITLE_COLOURS[tone]!;
    // The band runs from its top edge to the bottom of the mark; read the lightest and darkest rows.
    for (const y of [BAND.y, BAND.y + BAND.glyph]) {
      const k = SHADE_TOP - SHADE_SLOPE * (y / FACE);
      const tile = base.map((c) => Math.min(255, Math.round(c * k)));
      expect(contrast(ink, tile), `${tone} at y=${y}`).toBeGreaterThanOrEqual(AA);
    }
  });
});

describe("the mark", () => {
  it.each(MARKED)("%s has its own Simple Icons mark", (p) => {
    expect(glyphFor(p)).toBe(p);
  });

  it.each(["custom", "rumble", "trovo", "vimeo", "tiktok", "", "constructor", "__proto__"])(
    "%j gets the neutral transmitter, never another platform's mark",
    (p) => {
      expect(glyphFor(p)).toBe("destination");
    },
  );

  it("draws each mark's Simple Icons path verbatim, the same path the console uses", () => {
    const ui = readFileSync(join(ROOT, "..", "..", "ui", "src", "components", "platformGlyphs.tsx"), "utf8");
    const paths = [...ui.matchAll(/glyph\(\s*"([^"]+)",\s*"([^"]+)"/g)].map((m) => [m[1]!.toLowerCase(), m[2]!]);
    expect(paths.map(([n]) => n).sort()).toEqual([...MARKED].sort());
    for (const [platform, d] of paths) {
      const svg = layoutSvg(destinationLayout("live", { platform: platform!, name: "n" }, 1));
      expect(svg, platform).toContain(`<path d="${d}"`);
    }
  });

  it("draws the transmitter, and no brand's path, for a custom destination", () => {
    const svg = layoutSvg(destinationLayout("live", { platform: "custom", name: "Studio" }, 1));
    expect(svg).toContain("<circle");
    // The transmitter's arcs are strokes; every brand mark is a filled path.
    expect(svg).not.toMatch(/<path d="[^"]*" fill="rgb/);
  });

  it("sits in the top band, clear of a title of up to three lines", () => {
    const l = destinationLayout("live", { platform: "youtube", name: "YouTube" }, NAME_TITLE_LINES_MAX);
    expect(l.glyph.y + l.glyph.size).toBeLessThanOrEqual(titleTop(NAME_TITLE_LINES_MAX));
    expect(l.glyph.x).toBeGreaterThanOrEqual(BAND.x);
  });
});

describe("the name", () => {
  it("is the destination's own name", () => {
    expect(destinationLayout("live", { platform: "youtube", name: "Main" }, 1).name?.text).toBe("Main");
  });

  it("falls back to the platform's name, then the server's word, when the destination has none", () => {
    expect(displayName({ platform: "x", name: "" })).toBe("X");
    expect(displayName({ platform: "youtube", name: "  " })).toBe("YouTube");
    expect(displayName({ platform: "rumble", name: "" })).toBe("rumble");
  });

  it("drops control characters and runs of space", () => {
    expect(displayName({ platform: "kick", name: "a\n\tb\u0000  c" })).toBe("a b c");
    expect(displayName({ platform: "kick", name: "a\u0007b\u007fc" })).toBe("a b c");
  });

  it("drops what XML cannot carry, so a server's name can never make the SVG malformed", () => {
    expect(displayName({ platform: "kick", name: "a\ufffeb\uffffc\ud800d" })).toBe("a b c d");
    expect(displayName({ platform: "kick", name: "😀 Kick" })).toBe("😀 Kick");
  });

  it("is escaped, so a name can never inject markup into the image", () => {
    const svg = decode(destinationImage("live", { platform: "custom", name: `<b>"&'` }, 1));
    expect(svg).toContain(">&lt;b&gt;&quot;&amp;&apos;</text>");
    expect(svg).not.toContain("<b>");
  });

  it("is drawn at the title's own size when it fits, beside the mark, and centred on it", () => {
    const l = destinationLayout("live", { platform: "kick", name: "Kick" }, 1);
    expect(l.name).toMatchObject({ fontSize: NAME_MAX_PX, x: BAND.x + BAND.glyph + BAND.gap });
    const mid = BAND.y + BAND.glyph / 2;
    expect(l.name!.baseline).toBeGreaterThan(mid);
    expect(l.name!.baseline - l.name!.fontSize * 0.72).toBeLessThan(mid);
  });

  it.each(["YouTube", "Facebook", "Twitch", "Kick", "X", "Studio RTMP", "YouTube main channel", "ライブ配信チャンネル", "WWWWWWWWWWWW"])(
    "%j stays inside the key and above the title, shrunk and then cut",
    (name) => {
      const l = destinationLayout("live", { platform: "custom", name }, NAME_TITLE_LINES_MAX);
      const n = l.name!;
      expect(n.x + n.width).toBeLessThanOrEqual(FACE - BAND.right);
      expect(n.width).toBeCloseTo(textWidth(n.text, n.fontSize));
      expect(n.fontSize).toBeGreaterThanOrEqual(NAME_MIN_PX);
      expect(n.fontSize).toBeLessThanOrEqual(NAME_MAX_PX);
      expect(n.baseline).toBeLessThanOrEqual(titleTop(NAME_TITLE_LINES_MAX));
      if (n.text !== name) {
        expect(n.text.endsWith("…")).toBe(true);
        expect(n.fontSize).toBe(NAME_MIN_PX);
      }
    },
  );

  it("is left off when it only repeats a mark that is the platform's name: X shows its mark, not \"X X\"", () => {
    for (const name of ["X", "x", " X ", ""]) {
      const l = destinationLayout("live", { platform: "x", name }, 1);
      expect(l.name, JSON.stringify(name)).toBeUndefined();
      expect(l.glyph.id).toBe("x");
    }
    expect(decode(destinationImage("live", { platform: "x", name: "X" }, 1))).not.toContain("<text");
  });

  it("keeps an operator's own name on X, and the platform's name beside a symbol mark", () => {
    expect(destinationLayout("live", { platform: "x", name: "X — clips" }, 1).name?.text).toBe("X — clips");
    expect(destinationLayout("live", { platform: "x", name: "Xavier" }, 1).name?.text).toBe("Xavier");
    for (const [platform, name] of [["youtube", "YouTube"], ["twitch", "Twitch"], ["kick", "Kick"], ["facebook", "Facebook"]]) {
      expect(destinationLayout("live", { platform: platform!, name: name! }, 1).name?.text, platform).toBe(name);
    }
    // a custom destination named "X" has the transmitter, not X's mark: its name is not a repeat
    expect(destinationLayout("live", { platform: "custom", name: "X" }, 1).name?.text).toBe("X");
    expect(nameRepeatsMark({ platform: "custom", name: "X" }, "X")).toBe(false);
  });

  it("shrinks before it cuts", () => {
    const fit = fitName("Facebook", 85);
    expect(fit.text).toBe("Facebook");
    expect(fit.fontSize).toBeLessThan(NAME_MAX_PX);
  });

  it("counts wide scripts as a full em, so a CJK name is cut where a Latin one of the same length is not", () => {
    expect(textWidth("配信", 10)).toBe(20);
    expect(fitName("ライブ配信チャンネル", 85).text.endsWith("…")).toBe(true);
    expect(fitName("Live chann", 85).text).toBe("Live chann");
  });

  it("is left off when the title is tall enough to reach the band; the mark stays", () => {
    const l = destinationLayout("error", { platform: "twitch", name: "Twitch" }, NAME_TITLE_LINES_MAX + 1);
    expect(l.name).toBeUndefined();
    expect(l.glyph.id).toBe("twitch");
    expect(decode(destinationImage("error", { platform: "twitch", name: "Twitch" }, 4))).not.toContain("<text");
  });
});

describe("the image", () => {
  it("is a base64 SVG data URL of the layout, 144 units square", () => {
    const svg = decode(destinationImage("warn", { platform: "twitch", name: "Twitch" }, 1));
    expect(svg).toBe(layoutSvg(destinationLayout("warn", { platform: "twitch", name: "Twitch" }, 1)));
    expect(svg).toMatch(/^<svg xmlns="http:\/\/www.w3.org\/2000\/svg" width="144" height="144" viewBox="0 0 144 144">/);
    expect(svg).toContain('fill="url(#tile)"');
    // The ink on warn is the near-black title colour, not white.
    const [r, g, b] = TITLE_COLOURS.warn!;
    expect(svg).toContain(`fill="rgb(${r},${g},${b})"`);
  });
});

describe("destinationFace carries the destination it is about", () => {
  const dest = (over: Partial<DestStatus> = {}): DestStatus => ({
    id: 2,
    name: "Twitch",
    platform: "twitch",
    enabled: true,
    sourceId: 7,
    process: { state: "failed" },
    reason: { cause: "timeout" },
    ...over,
  });

  it("on every face of a bound destination", () => {
    expect(destinationFace(dest(), 7, null).mark).toEqual({ platform: "twitch", name: "Twitch" });
    expect(destinationFace(dest({ process: { state: "running" }, reason: null }), undefined, null).mark).toEqual({
      platform: "twitch",
      name: "Twitch",
    });
  });

  it("never on a missing destination or one on another programme", () => {
    expect(destinationFace(undefined, 7, null).mark).toBeUndefined();
    expect(destinationFace(dest({ sourceId: 8 }), 7, null).mark).toBeUndefined();
  });
});

describe("the runtime paints the image on destination keys only", () => {
  const status = (over: Record<string, unknown> = {}) =>
    json(200, {
      destinations: [{ id: 2, name: "Twitch", platform: "twitch", enabled: true, sourceId: 7, process: { state: "running" }, ...over }],
      source: { id: 7 },
    });

  async function paint(routes: Record<string, ReturnType<typeof json>>, kind: "destination" | "goLive", settings = {}) {
    const { rt } = runtimeFor(routes);
    rt.configure(PAIRED);
    await rt.verify();
    const s = surface("k", kind, settings);
    rt.add(s);
    await rt.tick();
    await settle();
    return { rt, s };
  }

  it("draws the destination's mark and name in the state's tile, beside the state title", async () => {
    const { s } = await paint({ "GET /api/v1/auth/connection": ADMIN, "GET /api/v1/status": status() }, "destination", {
      sourceId: 7,
      destinationId: 2,
    });
    expect(s.states.at(-1)).toBe(KEY_STATE.live);
    expect(s.titles.at(-1)).toBe("Live");
    expect(s.images.at(-1)).toBe(destinationImage("live", { platform: "twitch", name: "Twitch" }, 1));
  });

  it("counts the title's lines, so a tall title drops the name", async () => {
    const { s } = await paint(
      {
        "GET /api/v1/auth/connection": ADMIN,
        "GET /api/v1/status": status({ process: { state: "failed" }, reason: { cause: "timeout" }, backupError: "down", delaySeconds: 30 }),
      },
      "destination",
      { sourceId: 7, destinationId: 2 },
    );
    const lines = s.titles.at(-1)!.split("\n").length;
    expect(lines).toBeGreaterThan(NAME_TITLE_LINES_MAX);
    expect(s.images.at(-1)).toBe(destinationImage("error", { platform: "twitch", name: "Twitch" }, lines));
  });

  it("goes back to the manifest's image when the key is not about a destination it can vouch for", async () => {
    const missing = await paint({ "GET /api/v1/auth/connection": ADMIN, "GET /api/v1/status": status() }, "destination", {
      sourceId: 7,
      destinationId: 9,
    });
    expect(missing.s.images.at(-1)).toBeUndefined();
    expect(missing.s.images.length).toBeGreaterThan(0);

    const down = await paint({ "GET /api/v1/auth/connection": ADMIN }, "destination", { sourceId: 7, destinationId: 2 });
    expect(down.s.images.length).toBeGreaterThan(0);
    expect(down.s.images.every((i) => i === undefined)).toBe(true);
  });

  it("puts the padlock back on a read token's tap", async () => {
    const { rt, s } = await paint({ "GET /api/v1/auth/connection": READ, "GET /api/v1/status": status() }, "destination", {
      sourceId: 7,
      destinationId: 2,
    });
    expect(s.images.at(-1)).toMatch(/^data:image\/svg/);
    await rt.pressDestination("k");
    expect(s.states.at(-1)).toBe(KEY_STATE.needsAdmin);
    expect(s.images.at(-1)).toBeUndefined();
  });

  it("never sends an image to another kind of key", async () => {
    const { s } = await paint({ "GET /api/v1/auth/connection": ADMIN, "GET /api/v1/status": status() }, "goLive", { sourceId: 7 });
    expect(s.titles.length).toBeGreaterThan(0);
    expect(s.images).toEqual([]);
  });
});

describe("imagePainter: the plugin adapter's dedupe", () => {
  function painter(fail = () => false) {
    const sent: (string | undefined)[] = [];
    const p = imagePainter(async (image) => {
      if (fail()) throw new Error("socket closed");
      sent.push(image);
    });
    return { p, sent };
  }

  it("sends an image once while neither it nor the state changes", async () => {
    const { p, sent } = painter();
    p.state(1);
    await p.paint("a");
    await p.paint("a");
    expect(sent).toEqual(["a"]);
  });

  it("sends again after a state change, and for a new image", async () => {
    const { p, sent } = painter();
    p.state(1);
    await p.paint("a");
    p.state(3);
    await p.paint("a");
    await p.paint("b");
    await p.paint(undefined);
    await p.paint(undefined);
    expect(sent).toEqual(["a", "a", "b", undefined]);
  });

  it("forgets a send that failed, so a clear that did not get through is sent again", async () => {
    let down = false;
    const { p, sent } = painter(() => down);
    p.state(1);
    await p.paint("logo");
    down = true;
    await expect(p.paint(undefined)).rejects.toThrow("socket closed");
    down = false;
    await p.paint(undefined);
    expect(sent).toEqual(["logo", undefined]);
    // and a failed send of the SAME image as before is not taken for that earlier success either
    down = true;
    await expect(p.paint("logo")).rejects.toThrow();
    down = false;
    await p.paint(undefined);
    expect(sent).toEqual(["logo", undefined, undefined]);
  });
});
