import { describe, expect, it } from "vitest";
import { destinationFace, ISSUE_RANK, type KeyFace } from "../src/keyFace.js";
import type { DestStatus, FailoverStatus } from "../src/model.js";
import { issueLines, titleFor } from "../src/render.js";

// Every Destination key shows whatever polyemesis reports about that
// destination's stream: the states it always showed, and the six signals added
// beside them. When several are true at once the most severe is the face and a
// "+N" badge counts the rest; the property inspector lists them all.

const base = (over: Partial<DestStatus> = {}): DestStatus => ({
  id: 1,
  name: "YouTube",
  platform: "youtube",
  enabled: true,
  sourceId: 7,
  process: { state: "running" },
  ...over,
});

/** The failover of programme 7, the programme `base` is on. */
const on7 = (f: Partial<FailoverStatus>) => ({ sourceId: 7, failover: { active: "primary" as const, ...f } });

describe("each signal, alone, on the destination key", () => {
  it.each<[string, DestStatus, ReturnType<typeof on7> | undefined, Partial<KeyFace>]>([
    [
      "a restart storm while live: amber, still LIVE, 'Reconnecting xN'",
      base({ restartStorm: 7 }),
      undefined,
      { tone: "warn", label: "streamdeck.state.live", reason: "streamdeck.issue.restartStorm", reasonVars: { count: 7 } },
    ],
    [
      "a restart storm between runs: the reconnecting label counts, the cause rides beneath",
      base({ restartStorm: 9, process: { state: "reconnecting" }, reason: { cause: "timeout" } }),
      undefined,
      { tone: "warn", label: "streamdeck.issue.restartStorm", labelVars: { count: 9 }, reason: "streamdeck.reason.timeout" },
    ],
    [
      "dropping frames: amber, still LIVE",
      base({ droppingFrames: 42 }),
      undefined,
      { tone: "warn", label: "streamdeck.state.live", reason: "streamdeck.issue.droppingFrames" },
    ],
    [
      "YouTube rates the stream bad: 'YouTube: poor'",
      base({ platformHealth: { platform: "youtube", status: "bad", issues: ["bitrateLow"] } }),
      undefined,
      { tone: "warn", label: "streamdeck.state.live", reason: "streamdeck.issue.platformPoor", reasonVars: { platform: "YouTube" } },
    ],
    [
      "an expired account: red 'Reconnect account', and the key still says LIVE",
      base({ accountHealth: "expired" }),
      undefined,
      { tone: "error", label: "streamdeck.state.live", reason: "streamdeck.issue.reconnectAccount" },
    ],
    [
      "a refresh that keeps failing: red 'Reconnect account'",
      base({ accountHealth: "refreshFailing" }),
      undefined,
      { tone: "error", reason: "streamdeck.issue.reconnectAccount" },
    ],
    [
      "an account about to expire: amber 'Account expiring'",
      base({ accountHealth: "expiring" }),
      undefined,
      { tone: "warn", reason: "streamdeck.issue.accountExpiring" },
    ],
    [
      "an account problem shows on a key that is off, before going live finds it",
      base({ enabled: false, process: null, accountHealth: "expired" }),
      undefined,
      { tone: "error", label: "streamdeck.state.off", reason: "streamdeck.issue.reconnectAccount" },
    ],
    [
      "the programme is on its backup source",
      base(),
      on7({ active: "backup" }),
      { tone: "warn", label: "streamdeck.state.live", reason: "streamdeck.issue.sourceBackup" },
    ],
    [
      "the programme is on its playlist",
      base(),
      on7({ active: "playlist" }),
      { tone: "warn", reason: "streamdeck.issue.sourcePlaylist" },
    ],
    [
      "the programme is on its slate",
      base(),
      on7({ active: "slate" }),
      { tone: "warn", reason: "streamdeck.issue.sourceSlate" },
    ],
    [
      "the primary is degraded (#6's field): 'Source degraded', with the server's measurement",
      base(),
      on7({ degraded: "bitrate 640 kbps below 800 kbps for 5s" }),
      { tone: "warn", reason: "streamdeck.issue.sourceDegraded", detail: "bitrate 640 kbps below 800 kbps for 5s" },
    ],
    [
      "upload slow, as before",
      base({ reason: { cause: "upload_too_slow" } }),
      undefined,
      { tone: "warn", label: "streamdeck.state.live", reason: "streamdeck.reason.upload_too_slow" },
    ],
  ])("%s", (_name, dest, src, want) => {
    const face = destinationFace(dest, undefined, null, src);
    expect(face).toMatchObject(want);
    expect(face.more ?? 0).toBe(0);
  });

  it("a healthy live destination has no issues and no badge", () => {
    const face = destinationFace(base(), undefined, null, on7({}));
    expect(face).toMatchObject({ tone: "live", label: "streamdeck.state.live" });
    expect(face.reason).toBeUndefined();
    expect(face.issues ?? []).toEqual([]);
  });

  it("does not put another programme's failover on this destination", () => {
    // An unscoped key reads the default programme's failover beside an
    // install-wide destination list (#543). Programme 8's backup is not 7's.
    const face = destinationFace(base(), undefined, null, { sourceId: 8, failover: { active: "backup" } });
    expect(face.tone).toBe("live");
  });

  it("does not guess the programme of a legacy row with no sourceId", () => {
    const face = destinationFace(base({ sourceId: undefined }), undefined, null, on7({ active: "slate" }));
    expect(face.tone).toBe("live");
  });

  it("does not report a source on a destination that is off", () => {
    const face = destinationFace(base({ enabled: false, process: null }), undefined, null, on7({ active: "backup" }));
    expect(face).toMatchObject({ tone: "idle", label: "streamdeck.state.off" });
  });

  it("does not report dropped frames or platform health on a destination that is not running", () => {
    const face = destinationFace(
      base({ process: { state: "failed" }, droppingFrames: 9, platformHealth: { platform: "youtube", status: "bad" } }),
      undefined,
      null,
    );
    expect(face.issues?.map((i) => i.key)).toEqual(["streamdeck.state.failed"]);
  });
});

describe("precedence: held > failed/key changed > account > reconnecting/stalled > expiring > storm > drops > platform > source > upload slow", () => {
  // Everything at once, on a running destination, then peeled from the top.
  const all = (over: Partial<DestStatus> = {}) =>
    base({
      accountHealth: "expired",
      restartStorm: 8,
      droppingFrames: 30,
      platformHealth: { platform: "youtube", status: "bad" },
      reason: { cause: "upload_too_slow" },
      ...over,
    });
  const src = on7({ active: "backup" });

  it("ranks the issues in the agreed order", () => {
    expect(ISSUE_RANK).toEqual([
      "held",
      "failed",
      "account",
      "cycling",
      "accountExpiring",
      "restartStorm",
      "droppingFrames",
      "platform",
      "source",
      "uploadSlow",
    ]);
  });

  it("shows the account first when it is the worst, and counts the other five", () => {
    const face = destinationFace(all(), undefined, null, src);
    expect(face).toMatchObject({ tone: "error", label: "streamdeck.state.live", reason: "streamdeck.issue.reconnectAccount", more: 5 });
    expect(face.issues?.map((i) => i.key)).toEqual([
      "streamdeck.issue.reconnectAccount",
      "streamdeck.issue.restartStorm",
      "streamdeck.issue.droppingFrames",
      "streamdeck.issue.platformPoor",
      "streamdeck.issue.sourceBackup",
      "streamdeck.reason.upload_too_slow",
    ]);
  });

  it.each<[string, Partial<DestStatus>, string]>([
    ["storm beats dropped frames", { accountHealth: undefined }, "streamdeck.issue.restartStorm"],
    ["dropped frames beat the platform", { accountHealth: undefined, restartStorm: undefined }, "streamdeck.issue.droppingFrames"],
    [
      "the platform beats the source",
      { accountHealth: undefined, restartStorm: undefined, droppingFrames: undefined },
      "streamdeck.issue.platformPoor",
    ],
    [
      "the source beats a slow upload",
      { accountHealth: undefined, restartStorm: undefined, droppingFrames: undefined, platformHealth: null },
      "streamdeck.issue.sourceBackup",
    ],
  ])("%s", (_name, over, top) => {
    expect(destinationFace(all(over), undefined, null, src).reason).toBe(top);
  });

  it("a failed destination outranks its account, and its cause is still the reason", () => {
    const face = destinationFace(
      all({ process: { state: "failed" }, reason: { cause: "key_rejected" } }),
      undefined,
      null,
      src,
    );
    expect(face).toMatchObject({ tone: "error", label: "streamdeck.state.failed", reason: "streamdeck.reason.key_rejected" });
    // Account and storm remain; drops, platform and source need a running process.
    expect(face.more).toBe(2);
  });

  it("key changed still outranks the cause it explains, and outranks the account", () => {
    const face = destinationFace(
      all({ process: { state: "failed" }, reason: { cause: "key_rejected", keyChanged: true } }),
      undefined,
      null,
    );
    expect(face.reason).toBe("streamdeck.reason.keyChanged");
  });

  it("held outranks everything, failed included", () => {
    const face = destinationFace(
      all({ process: { state: "failed" }, reason: { cause: "key_rejected", held: true } }),
      undefined,
      null,
    );
    expect(face).toMatchObject({ tone: "held", label: "streamdeck.state.held", more: 2 });
  });

  it("an expired account outranks a stall, and the label still says Stalled", () => {
    const face = destinationFace(all({ stalled: true }), undefined, null);
    expect(face).toMatchObject({ tone: "error", label: "streamdeck.state.stalled", reason: "streamdeck.issue.reconnectAccount" });
    expect(face.issues?.[1]?.key).toBe("streamdeck.state.stalled");
  });

  it("an expired account outranks reconnecting with a cause", () => {
    const face = destinationFace(
      base({ accountHealth: "expired", process: { state: "reconnecting" }, reason: { cause: "timeout" } }),
      undefined,
      null,
    );
    expect(face).toMatchObject({ tone: "error", label: "streamdeck.state.reconnecting", reason: "streamdeck.issue.reconnectAccount", more: 1 });
  });

  it("key changed while reconnecting still outranks the account: it names the fix", () => {
    const face = destinationFace(
      base({ accountHealth: "expired", process: { state: "reconnecting" }, reason: { cause: "key_rejected", keyChanged: true } }),
      undefined,
      null,
    );
    expect(face).toMatchObject({ tone: "warn", reason: "streamdeck.reason.keyChanged", more: 1 });
  });

  it("an account that is only expiring stays below reconnecting", () => {
    const face = destinationFace(
      base({ accountHealth: "expiring", process: { state: "reconnecting" }, reason: { cause: "timeout" } }),
      undefined,
      null,
    );
    expect(face).toMatchObject({ tone: "warn", label: "streamdeck.state.reconnecting", reason: "streamdeck.reason.timeout", more: 1 });
  });

  it("a storm between runs is one issue, not two: the count is on the reconnecting face", () => {
    const face = destinationFace(
      base({ restartStorm: 9, process: { state: "reconnecting" }, reason: { cause: "timeout" } }),
      undefined,
      null,
    );
    expect(face.issues).toHaveLength(1);
  });

  it("keeps the stop warning as its own line, outside the count", () => {
    const face = destinationFace(all({ stopWarning: "Stopping ends the broadcast" }), undefined, null);
    expect(face.warning).toBe("streamdeck.warn.stopWarning");
    expect(face.more).toBe(4);
  });
});

describe("the +N badge on the key's title", () => {
  it("rides on the reason line", () => {
    const face = destinationFace(
      base({ accountHealth: "expired", restartStorm: 8 }),
      undefined,
      null,
    );
    expect(titleFor(face, "en")).toBe("Live\nReconnect account +1");
  });

  it("is never clipped away by a long reason", () => {
    const face: KeyFace = { tone: "warn", label: "streamdeck.state.live", reason: "streamdeck.issue.sourceDegraded", detail: "x", more: 3 };
    const title = titleFor(face, "de");
    expect(title.split("\n")[1]?.endsWith(" +3")).toBe(true);
  });

  it("rides on the label when there is no reason line", () => {
    const face = destinationFace(base({ stalled: true, accountHealth: "expiring" }), undefined, null);
    expect(titleFor(face, "en").split("\n")[0]).toBe("Stalled +1");
  });

  it("substitutes the restart count into the face, translated", () => {
    const face = destinationFace(base({ restartStorm: 7 }), undefined, null);
    expect(titleFor(face, "en")).toBe("Live\nReconnecting ×7");
    expect(titleFor(face, "de")).toContain("7");
  });

  it("names the platform in its own words", () => {
    const face = destinationFace(base({ platformHealth: { platform: "youtube", status: "bad" } }), undefined, null);
    expect(titleFor(face, "en")).toBe("Live\nYouTube: poor");
  });

  it("is absent with one issue", () => {
    const face = destinationFace(base({ accountHealth: "expired" }), undefined, null);
    expect(titleFor(face, "en")).not.toContain("+");
  });
});

describe("the property inspector's list of every current issue", () => {
  it("lists every issue, most severe first, with the server's words, then the warning", () => {
    const face = destinationFace(
      base({
        accountHealth: "expiring",
        droppingFrames: 30,
        platformHealth: { platform: "youtube", status: "bad", issues: ["bitrateLow", "gopSizeLong"] },
        backupProcess: { state: "failed" },
        backupError: "backup refused",
      }),
      undefined,
      null,
      on7({ degraded: "loss 4% above 2% for 5s" }),
    );
    expect(issueLines(face, "en")).toEqual([
      "Account expiring",
      "Dropping frames: 30",
      "YouTube: poor: bitrateLow, gopSizeLong",
      "Source degraded: loss 4% above 2% for 5s",
      "Backup down: backup refused",
    ]);
  });

  it("names the state with a failed destination's cause", () => {
    const face = destinationFace(
      base({ process: { state: "failed" }, reason: { cause: "key_rejected" }, accountHealth: "expired" }),
      undefined,
      null,
    );
    expect(issueLines(face, "en")).toEqual(["Failed: Key rejected", "Reconnect account"]);
  });

  it("is empty for a healthy destination", () => {
    expect(issueLines(destinationFace(base(), undefined, null), "en")).toEqual([]);
  });

  it("is translated", () => {
    const face = destinationFace(base({ accountHealth: "expired" }), undefined, null);
    expect(issueLines(face, "fr")[0]).not.toBe("Reconnect account");
  });
});

describe("a restart storm on an expired account holds one face", () => {
  // The frames a key sees during a storm: up for a few seconds, then between
  // runs with a cause, then up again, the count climbing each time.
  const cycle = (n: number): DestStatus[] => [
    base({ accountHealth: "expired", restartStorm: n, process: { state: "running" }, reason: { cause: "unknown", line: "Broken pipe" } }),
    base({ accountHealth: "expired", restartStorm: n + 1, process: { state: "reconnecting" }, reason: { cause: "unknown", line: "Broken pipe" } }),
    base({ accountHealth: "expired", restartStorm: n + 1, process: { state: "starting" } }),
    base({ accountHealth: "expired", restartStorm: n + 1, process: { state: "reconnecting" }, reason: { cause: "connection_refused" } }),
  ];
  const frames = [...cycle(6), ...cycle(8), ...cycle(10)];

  // MUTATION: move "cycling" above "account" in ISSUE_RANK. Observed: FAIL on
  // the reconnecting frames (amber "Reconnecting xN" with the cause).
  it.each(frames.map((d, i) => [i, d] as const))("frame %i is red Reconnect account, with the storm in the count", (_i, d) => {
    const face = destinationFace(d, undefined, null, on7({ active: "backup" }));
    expect(face.tone).toBe("error");
    expect(face.reason).toBe("streamdeck.issue.reconnectAccount");
    expect(face.more).toBeGreaterThanOrEqual(1);
    const kinds = face.issues?.map((i) => i.key) ?? [];
    expect(kinds.some((k) => k === "streamdeck.issue.restartStorm" || face.issues?.some((i) => i.state === "streamdeck.issue.restartStorm"))).toBe(true);
    expect(titleFor(face, "en").split("\n")[1]).toMatch(/^Reconnect account \+\d$/);
  });
});
