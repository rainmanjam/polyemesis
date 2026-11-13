import { describe, expect, it } from "vitest";

import {
  destinationsSettled,
  failedStateWords,
  findLegibleSecrets,
  loudnessRowsFromFeed,
  parseSecretsFile,
  progressFromStatus,
  shootProblems,
  type DestProgress,
  type LoudnessRow,
} from "./captureGuards";

// Invented values, built so no literal looks like a credential.
const PW = ["pass", "word", "-for-the-demo"].join("");
const TOKEN = ["tok", "en-", "abcdef123456"].join("");

describe("findLegibleSecrets", () => {
  it("reports the kind of each secret present and never its value", () => {
    const text = `Sign in\n${PW}\nSource ${TOKEN}`;
    const result = findLegibleSecrets(text, [
      { kind: "password", value: PW },
      { kind: "token", value: TOKEN },
      { kind: "streamKey", value: "not-on-screen-at-all" },
    ]);
    expect(result).toEqual(["password", "token"]);
    expect(JSON.stringify(result)).not.toContain(PW);
    expect(JSON.stringify(result)).not.toContain(TOKEN);
  });

  it("ignores values shorter than 8 characters", () => {
    expect(findLegibleSecrets("a short 1234567 value", [{ kind: "pin", value: "1234567" }])).toEqual([]);
  });

  it("reports a kind once however many entries share it", () => {
    const text = `${PW} ${TOKEN}`;
    expect(
      findLegibleSecrets(text, [
        { kind: "token", value: PW },
        { kind: "token", value: TOKEN },
      ]),
    ).toEqual(["token"]);
  });
});

describe("failedStateWords", () => {
  it("finds Failed, FAIL, Crashed and Unreachable", () => {
    expect(failedStateWords("Failed\nFAIL\nCrashed\nUnreachable")).toEqual([
      "failed",
      "fail",
      "crashed",
      "unreachable",
    ]);
  });

  it("reports a word once", () => {
    expect(failedStateWords("Failed\nFailed")).toEqual(["failed"]);
  });

  it("ignores zero counts", () => {
    expect(failedStateWords("0 failed · 0 stopped")).toEqual([]);
    expect(failedStateWords("errors: 0")).toEqual([]);
    expect(failedStateWords("sessions with errors: 0")).toEqual([]);
    expect(failedStateWords("Errors 0")).toEqual([]);
  });

  it("ignores a zero on the line under its label (a stat tile)", () => {
    expect(failedStateWords("Failed\n0\nCompleted\n12")).toEqual([]);
  });

  it("does not let a zero from the previous tile hide a failure", () => {
    expect(failedStateWords("Dropped\n0\nFailed\n3")).toEqual(["failed"]);
  });

  it("reports a non-zero count of errors or stopped", () => {
    expect(failedStateWords("2 errors")).toEqual(["errors"]);
    expect(failedStateWords("Errors 3")).toEqual(["errors"]);
    expect(failedStateWords("5 live · 1 stopped · 0 off")).toEqual(["stopped"]);
    expect(failedStateWords("Stopped\n2")).toEqual(["stopped"]);
  });

  it("reports a singular error under the plural name, and reads 00 as zero", () => {
    expect(failedStateWords("1 error")).toEqual(["errors"]);
    expect(failedStateWords("00 errors")).toEqual([]);
    expect(failedStateWords("errors: 00")).toEqual([]);
  });

  it("does not match Failover", () => {
    expect(failedStateWords("Failover\nFailover on\nFailovers: 2")).toEqual([]);
  });

  it("does not match a bare Errors label or filter", () => {
    expect(failedStateWords("Errors\nWarnings\nAll")).toEqual([]);
  });

  it("does not match a bare Stopped label with no count", () => {
    expect(failedStateWords("Stopped")).toEqual([]);
  });

  it("does not match prose in a settings label", () => {
    const text =
      "Let's Encrypt limits failed attempts to five per hostname per hour. Clear what is flagged first.\n" +
      "Failing step not reported\nOnly failures worth retrying are retried.";
    expect(failedStateWords(text)).toEqual([]);
  });

  it("does not match a long line even without a full stop", () => {
    expect(failedStateWords("one two three four five six seven eight nine ten eleven failed")).toEqual([]);
  });

  it("does not count the dot separators as words", () => {
    expect(failedStateWords("5 live · 0 starting · 1 failed · 0 blocked")).toEqual(["failed"]);
  });

  it("does not take a decimal for a zero", () => {
    expect(failedStateWords("errors: 0.5")).toEqual(["errors"]);
  });

  it("does not take a 0 that begins a longer number for a zero", () => {
    expect(failedStateWords("errors: 05")).toEqual(["errors"]);
  });

  it("does not report a word the shot declares as its subject", () => {
    expect(failedStateWords("Failed\nCrashed", ["failed"])).toEqual(["crashed"]);
    expect(failedStateWords("2 errors", ["errors"])).toEqual([]);
  });

  it("is case-insensitive about the allowance", () => {
    expect(failedStateWords("FAIL", ["Fail"])).toEqual([]);
  });
});

const row = (over: Partial<DestProgress> = {}): DestProgress => ({
  name: "YouTube · English",
  state: "running",
  uptimeSec: 40,
  speed: 1.0,
  dropped: 0,
  duplicated: 0,
  ...over,
});
const loud = (over: Partial<LoudnessRow> = {}): LoudnessRow => ({ integrated: true, verdict: "pass", ...over });
const five = (over: Partial<DestProgress> = {}) =>
  ["English", "Spanish", "French", "German", "Arabic"].map((l) => row({ name: `YouTube · ${l}`, ...over }));
const fiveLoud = () => [0, 1, 2, 3, 4].map(() => loud());

describe("destinationsSettled refuses", () => {
  it("an empty list", () => {
    const r = destinationsSettled([], [], 30);
    expect(r.ok).toBe(false);
    expect(r.why).toMatch(/no running destinations/i);
  });

  const cases: [string, Partial<DestProgress>][] = [
    ["under the minimum uptime", { uptimeSec: 12 }],
    ["speed 0.84", { speed: 0.84 }],
    ["speed 1.07", { speed: 1.07 }],
    ["dropped 1", { dropped: 1 }],
    ["duplicated 1", { duplicated: 1 }],
    ["a state other than running", { state: "reconnecting" }],
  ];
  for (const [title, over] of cases) {
    it(`a destination with ${title}, naming it`, () => {
      const rows = five();
      rows[2] = row({ name: "YouTube · French", ...over });
      const r = destinationsSettled(rows, fiveLoud(), 30);
      expect(r.ok).toBe(false);
      expect(r.why).toContain("YouTube · French");
    });
  }

  it("a loudness row not yet integrated, naming its destination", () => {
    const l = fiveLoud();
    l[1] = loud({ integrated: false, name: "YouTube · Spanish" });
    const r = destinationsSettled(five(), l, 30);
    expect(r.ok).toBe(false);
    expect(r.why).toContain("YouTube · Spanish");
  });

  it("a loudness verdict other than pass, naming its destination", () => {
    const l = fiveLoud();
    l[3] = loud({ verdict: "warn", name: "YouTube · German" });
    const r = destinationsSettled(five(), l, 30);
    expect(r.ok).toBe(false);
    expect(r.why).toContain("YouTube · German");
  });

  it("a loudness row with no name, by its position", () => {
    const l = fiveLoud();
    l[3] = loud({ verdict: "fail" });
    const r = destinationsSettled(five(), l, 30);
    expect(r.ok).toBe(false);
    expect(r.why).toContain("loudness row 4");
  });

  it("fewer loudness rows than destinations", () => {
    const r = destinationsSettled(five(), [loud(), loud()], 30);
    expect(r.ok).toBe(false);
    expect(r.why).toMatch(/2 loudness rows for 5 destinations/);
  });
});

describe("destinationsSettled accepts", () => {
  it("five rows at uptime 40, speed 1.0, nothing dropped, five integrated passing rows", () => {
    expect(destinationsSettled(five(), fiveLoud(), 30)).toEqual({ ok: true, why: "" });
  });
});

describe("parseSecretsFile", () => {
  it("reads kind<TAB>value lines and skips blanks", () => {
    expect(parseSecretsFile(`password\t${PW}\n\ntoken\t${TOKEN}\n`)).toEqual([
      { kind: "password", value: PW },
      { kind: "token", value: TOKEN },
    ]);
  });

  it("keeps a tab inside the value", () => {
    expect(parseSecretsFile("k\ta\tb")).toEqual([{ kind: "k", value: "a\tb" }]);
  });

  it("refuses a malformed line by number without quoting it", () => {
    let message = "";
    try {
      parseSecretsFile(`password\t${PW}\n${TOKEN}-no-tab`);
    } catch (e) {
      message = (e as Error).message;
    }
    expect(message).toContain("line 2");
    expect(message).not.toContain(TOKEN);
  });

  it("refuses a line with an empty kind", () => {
    expect(() => parseSecretsFile(`\t${PW}`)).toThrow(/line 1/);
  });
});

describe("shootProblems", () => {
  const ok = { theme: "dark", visibleText: "Dashboard\n5 live", secrets: [], allow: [] as string[] };

  it("is empty for a dark, clean, calm page", () => {
    expect(shootProblems(ok)).toEqual([]);
  });

  it("refuses a light page, and one with no theme", () => {
    expect(shootProblems({ ...ok, theme: "light" })[0]).toMatch(/theme is "light"/);
    expect(shootProblems({ ...ok, theme: null })[0]).toMatch(/theme is "none"/);
  });

  it("names the kinds of secrets and never a value", () => {
    const out = shootProblems({
      ...ok,
      visibleText: `x ${PW}`,
      secrets: [{ kind: "password", value: PW }],
    });
    expect(out).toHaveLength(1);
    expect(out[0]).toContain("password");
    expect(out.join("\n")).not.toContain(PW);
  });

  it("names the failed-state words", () => {
    const out = shootProblems({ ...ok, visibleText: "Failed" });
    expect(out[0]).toContain("failed");
  });

  it("allows a declared failed word", () => {
    expect(shootProblems({ ...ok, visibleText: "Failed", allow: ["failed"] })).toEqual([]);
  });

  it("reports every problem in order", () => {
    const out = shootProblems({
      theme: "light",
      visibleText: `${PW} Failed`,
      secrets: [{ kind: "password", value: PW }],
      allow: [],
    });
    expect(out).toHaveLength(3);
  });
});

describe("progressFromStatus", () => {
  const dest = (over: Record<string, unknown> = {}) => ({
    name: "YouTube · English",
    enabled: true,
    process: { state: "running", uptimeSec: 41, progress: { speed: 1.01, dropFrames: 0, dupFrames: 0 } },
    ...over,
  });

  it("maps the enabled destinations' process fields", () => {
    expect(progressFromStatus({ destinations: [dest()] })).toEqual([
      { name: "YouTube · English", state: "running", uptimeSec: 41, speed: 1.01, dropped: 0, duplicated: 0 },
    ]);
  });

  it("skips a disabled destination", () => {
    expect(progressFromStatus({ destinations: [dest({ enabled: false })] })).toEqual([]);
  });

  it("reads a destination with no process as stopped", () => {
    expect(progressFromStatus({ destinations: [dest({ process: null })] })).toEqual([
      { name: "YouTube · English", state: "stopped", uptimeSec: 0, speed: 0, dropped: 0, duplicated: 0 },
    ]);
  });

  it("reads a destination with no progress as zero", () => {
    expect(progressFromStatus({ destinations: [dest({ process: { state: "starting", uptimeSec: 1 } })] })[0]).toMatchObject({
      state: "starting",
      speed: 0,
      dropped: 0,
    });
  });

  it("reads a status with no destinations as empty", () => {
    expect(progressFromStatus({})).toEqual([]);
    expect(progressFromStatus({ destinations: null })).toEqual([]);
  });
});

describe("loudnessRowsFromFeed", () => {
  it("maps the live reports, naming the destination", () => {
    expect(
      loudnessRowsFromFeed({
        reports: [
          { destination: "YouTube · English", mix: "live", integrated: true, verdict: "pass" },
          { destination: "YouTube · Spanish", integrated: false, verdict: "unknown" },
        ],
      }),
    ).toEqual([
      { name: "YouTube · English", integrated: true, verdict: "pass" },
      { name: "YouTube · Spanish", integrated: false, verdict: "unknown" },
    ]);
  });

  it("leaves out the VOD mix", () => {
    expect(
      loudnessRowsFromFeed({ reports: [{ destination: "a", mix: "vod", integrated: true, verdict: "pass" }] }),
    ).toEqual([]);
  });

  it("reads a feed with no reports as empty", () => {
    expect(loudnessRowsFromFeed({})).toEqual([]);
    expect(loudnessRowsFromFeed({ reports: null })).toEqual([]);
  });
});
