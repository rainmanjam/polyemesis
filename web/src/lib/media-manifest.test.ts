import { readdirSync, readFileSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";

import { KEPT_WITHOUT_USER, usersOf } from "../../../scripts/check-shots.mjs";

/* docs/media/shots.json says which scene shoots which screenshot and who uses
 * it. This test holds it to the two image folders and to the code, so a shot
 * added, removed or re-homed without the manifest fails here. `usedBy` and
 * `site` are written by `node scripts/check-shots.mjs --write-used-by`, never
 * typed; this test compares against the same derivation. */

const ROOT = new URL("../../../", import.meta.url).pathname;
const MEDIA = join(ROOT, "docs", "media");
const SITE_SHOTS = join(ROOT, "web", "src", "assets", "shots");
const WEB_SRC = join(ROOT, "web", "src");

interface Shot {
  file: string;
  scene: "sports" | "full" | "lanes";
  test: string;
  screen: string;
  site: boolean;
  usedBy: string[];
  exception: null | "fixture-messages" | "fixture-responses" | "clip" | "framed";
  allowFailedWords: string[];
  caption?: string;
}
const manifest = JSON.parse(readFileSync(join(MEDIA, "shots.json"), "utf8")) as {
  viewport: { width: number; height: number };
  scale: number;
  theme: string;
  shots: Shot[];
};
const pngs = (dir: string) => readdirSync(dir).filter((f) => f.endsWith(".png")).sort((a, b) => a.localeCompare(b));
const names = (xs: Shot[]) => xs.map((s) => s.file).sort((a, b) => a.localeCompare(b));

describe("docs/media/shots.json", () => {
  it("has the positive control: at least 25 shots", () => {
    expect(manifest.shots.length).toBeGreaterThanOrEqual(25);
  });

  it("describes a dark 1440x900 capture at 2x", () => {
    expect(manifest.viewport).toEqual({ width: 1440, height: 900 });
    expect(manifest.scale).toBe(2);
    expect(manifest.theme).toBe("dark");
  });

  it("lists every PNG in docs/media and nothing else", () => {
    expect(names(manifest.shots)).toEqual(pngs(MEDIA));
  });

  it("names each file once", () => {
    expect(new Set(manifest.shots.map((s) => s.file)).size).toBe(manifest.shots.length);
  });

  it("has a site copy exactly when usedBy is non-empty, or the file is kept on purpose", () => {
    const expected = manifest.shots.filter((s) => s.usedBy.length > 0 || KEPT_WITHOUT_USER.includes(s.file));
    expect(pngs(SITE_SHOTS)).toEqual(names(expected));
    expect(names(manifest.shots.filter((s) => s.site))).toEqual(names(expected));
  });

  it("keeps a whitelisted file with a site copy and no user", () => {
    for (const f of KEPT_WITHOUT_USER) {
      const s = manifest.shots.find((x) => x.file === f);
      expect(s?.site).toBe(true);
      expect(s?.usedBy).toEqual([]);
    }
  });

  it("derives usedBy from the code", () => {
    for (const s of manifest.shots) expect([s.file, s.usedBy]).toEqual([s.file, usersOf(s.file, WEB_SRC)]);
  });

  it("gives every shot a scene and a test title that exists in ui/e2e/capture.spec.ts", () => {
    const spec = readFileSync(join(ROOT, "ui", "e2e", "capture.spec.ts"), "utf8");
    for (const s of manifest.shots) {
      expect(["sports", "full", "lanes"]).toContain(s.scene);
      if (s.scene === "lanes") expect(s.test).toBe("scripts/capture-lanes.sh");
      else expect([s.file, spec.includes(`test("${s.test}"`)]).toEqual([s.file, true]);
    }
  });

  it("declares every exception shot in docs/media/README.md", () => {
    const readme = readFileSync(join(MEDIA, "README.md"), "utf8");
    for (const s of manifest.shots.filter((x) => x.exception !== null)) {
      expect([s.file, readme.includes(s.file)]).toEqual([s.file, true]);
    }
  });

  it("requires a caption for any allowed failed word", () => {
    for (const s of manifest.shots) {
      if (s.allowFailedWords.length > 0) expect([s.file, (s.caption ?? "").trim().length > 0]).toEqual([s.file, true]);
    }
  });

  it("pins the declared exceptions", () => {
    const by = (f: string) => manifest.shots.find((s) => s.file === f)?.exception;
    expect(by("09-chat.png")).toBe("fixture-messages");
    expect(by("22-platforms-connected.png")).toBe("fixture-responses");
    expect(by("02-routing-hero.png")).toBe("clip");
    expect(by("07-monitoring.png")).toBe("framed");
    expect(manifest.shots.filter((s) => s.exception !== null)).toHaveLength(4);
  });
});
