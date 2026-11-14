import { describe, expect, it } from "vitest";

import {
  destinationsSettled,
  failedStateWords,
  findLegibleSecrets,
  loudnessRowsFromFeed,
  parseSecretsFile,
  progressFromStatus,
  secretsFromEnv,
  shootProblems,
  silentTrackProblem,
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

describe("findLegibleSecrets, however the page shows the value", () => {
  const SECRET = ["Zq9", "Xw-long", "-value-1234"].join("");
  const entry = [{ kind: "password", value: SECRET }];

  it("finds a value wrapped across lines or spaces", () => {
    const wrapped = `${SECRET.slice(0, 6)}\n${SECRET.slice(6, 12)} ${SECRET.slice(12)}`;
    expect(findLegibleSecrets(`x ${wrapped} y`, entry)).toEqual(["password"]);
  });

  it("finds a value shown in other case", () => {
    expect(findLegibleSecrets(SECRET.toUpperCase(), entry)).toEqual(["password"]);
  });

  it("finds a value that is percent-encoded", () => {
    expect(findLegibleSecrets(`https://x/?k=${encodeURIComponent(SECRET + "/é")}`, [{ kind: "token", value: SECRET + "/é" }])).toEqual([
      "token",
    ]);
  });

  it("does not throw on a malformed percent sequence", () => {
    expect(findLegibleSecrets("100% sure %E0%A4%A", entry)).toEqual([]);
  });

  it("does not count a masked value (dots and the last four characters) as a leak", () => {
    expect(findLegibleSecrets(`••••••••••••${SECRET.slice(-4)}`, entry)).toEqual([]);
  });

  it("ignores a value that is shorter than 8 once its whitespace is gone", () => {
    expect(findLegibleSecrets("a b c d e f g h", [{ kind: "x", value: "a b c d" }])).toEqual([]);
  });
});

describe("failedStateWords", () => {
  const words: [string, string][] = [
    ["Failed", "failed"],
    ["FAIL", "fail"],
    ["Crashed", "crashed"],
    ["Unreachable", "unreachable"],
    ["Offline", "offline"],
    ["Degraded", "degraded"],
    ["Reconnecting", "reconnecting"],
    ["Error", "errors"],
    ["Errors", "errors"],
    ["Stopped", "stopped"],
  ];
  for (const [shown, found] of words) {
    it(`finds ${shown}, as a badge and inside a sentence`, () => {
      expect(failedStateWords(shown)).toEqual([found]);
      expect(failedStateWords(`Connection ${shown.toLowerCase()}.`)).toEqual([found]);
      expect(failedStateWords(`Ingest ${shown} for the last ten seconds and still going on`)).toEqual([found]);
    });
  }

  it("finds Reconnecting with its ellipsis", () => {
    expect(failedStateWords("Reconnecting…")).toEqual(["reconnecting"]);
  });

  it("reports a word once, in order of appearance", () => {
    expect(failedStateWords("Offline\nFailed\nOffline")).toEqual(["offline", "failed"]);
  });

  it("does not match Failover, download, shutdown or other words that merely contain one", () => {
    expect(failedStateWords("Failover\nFailover on\nFailovers: 2\nDownload\nShutdown\nUnfailed\nOfflined")).toEqual([]);
  });

  it("ignores zero counts", () => {
    expect(failedStateWords("0 failed · 0 stopped")).toEqual([]);
    expect(failedStateWords("0 errors")).toEqual([]);
    expect(failedStateWords("errors: 0")).toEqual([]);
    expect(failedStateWords("sessions with errors: 0")).toEqual([]);
    expect(failedStateWords("Errors 0")).toEqual([]);
    expect(failedStateWords("5 live · 0 stopped · 0 off")).toEqual([]);
  });

  it("ignores a zero on the line under its label (a stat tile)", () => {
    expect(failedStateWords("FAILED\n0\nCompleted\n12")).toEqual([]);
  });

  it("does not let a zero from the previous tile hide a failure", () => {
    expect(failedStateWords("Dropped\n0\nFailed\n3")).toEqual(["failed"]);
  });

  it("does not let a zero that starts the next line hide a failure", () => {
    expect(failedStateWords("Failed\n0 dropped")).toEqual(["failed"]);
  });

  it("reports a non-zero count", () => {
    expect(failedStateWords("2 errors")).toEqual(["errors"]);
    expect(failedStateWords("Errors 3")).toEqual(["errors"]);
    expect(failedStateWords("5 live · 1 stopped · 0 off")).toEqual(["stopped"]);
    expect(failedStateWords("5 live · 0 starting · 1 failed · 0 blocked")).toEqual(["failed"]);
  });

  it("does not take a decimal or a longer number for a zero", () => {
    expect(failedStateWords("errors: 0.5")).toEqual(["errors"]);
    expect(failedStateWords("errors: 05")).toEqual(["errors"]);
    expect(failedStateWords("0.5 failed")).toEqual(["failed"]);
  });

  it("flags a sentence too: an error banner must not pass", () => {
    expect(failedStateWords("The destination crashed and will not restart.")).toEqual(["crashed"]);
    expect(
      failedStateWords("Let's Encrypt limits failed attempts to five per hostname per hour. Clear what is flagged first."),
    ).toEqual(["failed"]);
  });

  it("does not report a word the shot declares as its subject", () => {
    expect(failedStateWords("Failed\nCrashed", ["failed"])).toEqual(["crashed"]);
    expect(failedStateWords("2 errors", ["errors"])).toEqual([]);
    expect(failedStateWords("1 error", ["Error"])).toEqual([]);
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
const loud = (over: Partial<LoudnessRow> = {}): LoudnessRow => ({ name: "YouTube · English", integrated: true, verdict: "pass", ...over });
const five = (over: Partial<DestProgress> = {}) =>
  ["English", "Spanish", "French", "German", "Arabic"].map((l) => row({ name: `YouTube · ${l}`, ...over }));
const fiveLoud = () => five().map((r) => loud({ name: r.name }));

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

  it("a destination with no loudness row of its own, even when the count matches", () => {
    const l = fiveLoud();
    l[2] = loud({ name: "Something else" });
    const r = destinationsSettled(five(), l, 30);
    expect(r.ok).toBe(false);
    expect(r.why).toContain("YouTube · French");
    expect(r.why).toMatch(/no loudness row/);
  });

  it("no loudness rows at all", () => {
    const r = destinationsSettled(five(), [], 30);
    expect(r.ok).toBe(false);
    expect(r.why).toContain("YouTube · English");
  });

  it("a destination whose second row for the same name is not passing", () => {
    const l = [...fiveLoud(), loud({ name: "YouTube · English", verdict: "warn" })];
    const r = destinationsSettled(five(), l, 30);
    expect(r.ok).toBe(false);
    expect(r.why).toContain("YouTube · English");
  });
});

describe("destinationsSettled accepts", () => {
  it("and ignores a row for something that is not one of the destinations", () => {
    const l = [...fiveLoud(), loud({ name: "Not a destination", integrated: false, verdict: "fail" })];
    expect(destinationsSettled(five(), l, 30)).toEqual({ ok: true, why: "" });
  });

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

describe("secretsFromEnv", () => {
  it("refuses an unset or empty variable, saying how to opt out", () => {
    expect(() => secretsFromEnv(undefined, () => "")).toThrow(/SHOT_SECRETS_FILE is required.*none/);
    expect(() => secretsFromEnv("", () => "")).toThrow(/SHOT_SECRETS_FILE is required/);
  });

  it("reads nothing for an explicit none", () => {
    expect(secretsFromEnv("none", () => "never read")).toEqual([]);
  });

  it("parses the named file", () => {
    expect(secretsFromEnv("/w/secrets.tsv", (p) => (p === "/w/secrets.tsv" ? `password\t${PW}\n` : ""))).toEqual([
      { kind: "password", value: PW },
    ]);
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

describe("silentTrackProblem", () => {
  const card = (n: number, reading: string) => `Track ${n}\nstereo · aac\n${reading} dBFS`;
  const six = (readings: string[]) => readings.map((r, i) => card(i + 1, r));

  it("accepts six finite readings", () => {
    expect(silentTrackProblem(six(["-12.3", "-30.0", "-8.1", "-20.5", "-40.2", "-18.0"]), 6)).toBeNull();
  });

  it("names every track that reads minus infinity, with either minus sign", () => {
    expect(silentTrackProblem(six(["-∞", "-30.0", "-8.1", "-20.5", "−∞", "-18.0"]), 6)).toBe(
      "tracks 1, 5 read minus infinity",
    );
  });

  it("names a single silent track in the singular", () => {
    expect(silentTrackProblem(six(["-12.3", "-30.0", "-8.1", "-20.5", "-40.2", "-∞"]), 6)).toBe(
      "track 6 reads minus infinity",
    );
  });

  it("refuses a card count other than the expected one", () => {
    expect(silentTrackProblem(six(["-12.3", "-30.0"]), 6)).toBe("found 2 track cards, expected 6");
    expect(silentTrackProblem([], 6)).toBe("found 0 track cards, expected 6");
  });

  it("treats a card with no reading as silent, and a card with no number by its position", () => {
    expect(silentTrackProblem(["Track 1\nwaiting", "Track 2\n-9.0 dBFS"], 2)).toBe("track 1 reads minus infinity");
    expect(silentTrackProblem(["stereo\n-9.0 dBFS", "no reading"], 2)).toBe("track 2 reads minus infinity");
  });
});
