import { describe, expect, it } from "vitest";
import { keySettings } from "../src/config.js";
import { KEY_STATE } from "../src/render.js";
import { viewerCount, viewersFace, viewersFeedback } from "../src/viewers.js";
import { ADMIN, PAIRED, READ, json, runtimeFor, surface } from "./fakeInstall.js";

// The viewers key and dial (Wave 4 #4). The count is the server's analytics sampler's last tick; a count a
// platform did not report is "Not reported", never 0, and the total is the server's sum of what did report.

const LIVE = {
  enabled: true,
  onAir: true,
  total: 15,
  reported: 2,
  readings: [
    { platform: "youtube", live: true, reason: "hidden" },
    { platform: "twitch", live: true, viewers: 12 },
    { platform: "player", live: true, viewers: 3 },
  ],
  points: [],
  intervalSeconds: 60,
};

describe("viewers face", () => {
  it("shows the total, and one platform summed over its accounts", () => {
    const live = { enabled: true, onAir: true, total: 15, readings: [
      { platform: "twitch", viewers: 12 }, { platform: "twitch", viewers: 4 }, { platform: "youtube" },
    ] };
    expect(viewerCount(live, undefined)).toBe(15);
    expect(viewerCount(live, "twitch")).toBe(16);
    expect(viewersFace(live, undefined, "en")).toEqual({ tone: "live", label: "streamdeck.viewers.total", detail: "15" });
    expect(viewersFace(live, "twitch", "en").detail).toBe("Twitch 16");
  });

  it("never turns a missing count into 0", () => {
    const live = { enabled: true, onAir: true, readings: [{ platform: "youtube" }, { platform: "kick" }] };
    expect(viewerCount(live, undefined)).toBeUndefined();
    expect(viewerCount(live, "youtube")).toBeUndefined();
    // A platform no running destination streams to was never asked: not reported, not 0.
    expect(viewerCount(live, "player")).toBeUndefined();
    expect(viewersFace(live, undefined, "en")).toMatchObject({ tone: "warn", label: "streamdeck.viewers.notReported" });
    expect(viewersFace(live, "youtube", "en")).toMatchObject({ tone: "warn", label: "streamdeck.viewers.notReported", detail: "YouTube" });
    // A real, reported 0 is a 0.
    expect(viewersFace({ enabled: true, onAir: true, total: 0, readings: [] }, undefined, "en").detail).toBe("0");
  });

  it("says off air and analytics off instead of a number", () => {
    expect(viewersFace({ enabled: true, onAir: false, readings: [] }, undefined, "en").label).toBe("streamdeck.viewers.offAir");
    expect(viewersFace({ enabled: false, onAir: false, readings: [] }, "player", "en")).toMatchObject({
      tone: "unavailable", label: "streamdeck.viewers.off", detail: "Player",
    });
    expect(viewersFeedback({ enabled: true, onAir: true, readings: [] }, "kick", "en")).toEqual({
      title: "Kick", value: "Not reported", indicator: 0,
    });
  });

  it("keeps only a known platform from the settings", () => {
    expect(keySettings({ viewersPlatform: "twitch" }).viewersPlatform).toBe("twitch");
    expect(keySettings({ viewersPlatform: "myspace" }).viewersPlatform).toBeUndefined();
    expect(keySettings({}).viewersPlatform).toBeUndefined();
  });
});

describe("viewers key", () => {
  it("shows the total from the install, not reported rows left out, and reads no programme's status", async () => {
    const { rt, net } = runtimeFor({ "GET /api/v1/auth/connection": ADMIN, "GET /api/v1/analytics/live": json(200, LIVE) });
    rt.configure(PAIRED);
    await rt.verify();
    const key = surface("k", "viewers");
    rt.add(key);
    await rt.tick();
    expect(key.states.at(-1)).toBe(KEY_STATE.live);
    expect(key.titles.at(-1)).toBe("Viewers\n15");
    expect(net.sent.some((r) => r.url.pathname === "/api/v1/status")).toBe(false);
  });

  it("shows Not reported when nothing reported", async () => {
    const { rt } = runtimeFor({
      "GET /api/v1/auth/connection": ADMIN,
      "GET /api/v1/analytics/live": json(200, { ...LIVE, total: undefined, reported: 0, readings: [{ platform: "kick", live: true, reason: "hidden" }] }),
    });
    rt.configure(PAIRED);
    await rt.verify();
    const key = surface("k", "viewers");
    rt.add(key);
    await rt.tick();
    expect(key.states.at(-1)).toBe(KEY_STATE.warn);
    expect(key.titles.at(-1)).toBe("Not reported");
    expect(key.titles.join("|")).not.toMatch(/\b0\b/);
  });

  it("shows needs-admin on a read token, which the server refuses", async () => {
    const { rt } = runtimeFor({
      "GET /api/v1/auth/connection": READ,
      "GET /api/v1/analytics/live": json(403, { error: "this API token is read-only" }),
    });
    rt.configure(PAIRED);
    await rt.verify();
    const key = surface("k", "viewers");
    const dial = surface("d", "viewers", { viewersPlatform: "youtube" });
    rt.add(key);
    rt.add(dial);
    await rt.tick();
    expect(key.states.at(-1)).toBe(KEY_STATE.needsAdmin);
    expect(key.titles.at(-1)).toBe("Needs admin token");
    expect(dial.feedback.at(-1)).toEqual({ title: "Viewers", value: "Needs admin token", indicator: 0 });
  });

  it("drops a stale count when the install stops answering", async () => {
    const routes = { "GET /api/v1/auth/connection": ADMIN, "GET /api/v1/analytics/live": json(200, LIVE) };
    const { rt } = runtimeFor(routes);
    rt.configure(PAIRED);
    await rt.verify();
    const key = surface("k", "viewers", { viewersPlatform: "twitch" });
    rt.add(key);
    await rt.tick();
    expect(key.titles.at(-1)).toBe("Viewers\nTwitch 12");
    routes["GET /api/v1/analytics/live"] = json(502, { error: "bad gateway" });
    await rt.tick();
    expect(key.states.at(-1)).toBe(KEY_STATE.error);
    expect(key.titles.at(-1)).not.toContain("12");
  });

  it("paints a dial's strip with the platform and its count, only on a change", async () => {
    const { rt } = runtimeFor({ "GET /api/v1/auth/connection": ADMIN, "GET /api/v1/analytics/live": json(200, LIVE) });
    rt.configure(PAIRED);
    await rt.verify();
    const dial = surface("d", "viewers", { viewersPlatform: "player" });
    rt.add(dial);
    await rt.tick();
    await rt.tick();
    expect(dial.feedback).toEqual([{ title: "Player", value: "3", indicator: 0 }]);
  });
});

describe("viewers on a partial tick (PR #950 review, #6)", () => {
  it("does not show the rest of the audience as the total", () => {
    const live = { enabled: true, onAir: true, total: 12, partial: true, readings: [{ platform: "twitch", viewers: 12 }, { platform: "youtube" }] };
    expect(viewersFace(live, undefined, "en")).toMatchObject({ tone: "warn", label: "streamdeck.viewers.partial", detail: "12" });
    // One platform's own count is still its count.
    expect(viewersFace(live, "twitch", "en")).toMatchObject({ tone: "live", detail: "Twitch 12" });
    expect(viewersFeedback(live, undefined, "en")).toEqual({ title: "Viewers", value: "Partial · 12", indicator: 0 });
  });

  it("reads partial off the install's answer", async () => {
    const { rt } = runtimeFor({
      "GET /api/v1/auth/connection": ADMIN,
      "GET /api/v1/analytics/live": json(200, { ...LIVE, partial: true }),
    });
    rt.configure(PAIRED);
    await rt.verify();
    const key = surface("k", "viewers");
    rt.add(key);
    await rt.tick();
    expect(key.states.at(-1)).toBe(KEY_STATE.warn);
    expect(key.titles.at(-1)).toBe("Partial\n15");
  });
});
