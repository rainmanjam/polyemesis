import { describe, expect, it } from "vitest";
import { keySettings, pollOptions } from "../src/config.js";
import { KEY_STATE } from "../src/render.js";
import { pollClock } from "../src/runtime.js";
import { ADMIN, PAIRED, READ, json, runtimeFor, settle, surface } from "./fakeInstall.js";

// The chat poll key (#14). Press starts the poll its settings describe, announced in every chat; while
// a poll runs the face shows the votes and the time left, and a HELD press ends it -- the poll read at
// key down, never one started since.

const SETTINGS = { pollQuestion: "Next map?", pollOptions: ["Dust", "Mirage"], pollDuration: 60 };
const NONE = json(200, { current: null, history: [] });
const running = (state = "open", total = 4) =>
  json(200, {
    current: { id: 12, question: "Next map?", state, total, endsAt: new Date(Date.now() + 65_000).toISOString(),
      options: [{ label: "Dust", votes: 3, byPlatform: { twitch: 3 } }] },
    history: [],
  });

describe("poll key settings", () => {
  it("reads options one per line and drops blanks; a poll needs a question", () => {
    expect(pollOptions("Dust\n\n  Mirage \r\nNuke")).toEqual(["Dust", "Mirage", "Nuke"]);
    expect(pollOptions(["a", " ", 3, "b"])).toEqual(["a", "b"]);
    expect(pollOptions("")).toBeUndefined();
    expect(keySettings({ pollQuestion: "  ", pollOptions: "a\nb", pollDuration: "45" })).toMatchObject({
      pollQuestion: undefined, pollOptions: ["a", "b"], pollDuration: 45,
    });
  });

  it("formats the time left without going negative", () => {
    expect(pollClock(new Date(65_000).toISOString(), 0)).toBe("1:05");
    expect(pollClock(new Date(0).toISOString(), 10_000)).toBe("0:00");
    expect(pollClock("nope", 0)).toBe("0:00");
  });
});

describe("poll key", () => {
  it("says the poll is not set up until it has a question and two options", async () => {
    const { rt } = runtimeFor({ "GET /api/v1/auth/connection": ADMIN, "GET /api/v1/polls": NONE });
    rt.configure(PAIRED);
    await rt.verify();
    const key = surface("k", "poll", { pollQuestion: "Next map?", pollOptions: ["Dust"] });
    rt.add(key);
    await rt.tick();
    expect(key.states.at(-1)).toBe(KEY_STATE.unbound);
    expect(key.titles.at(-1)).toBe("Set the poll");
    await rt.pressPollDown("k");
    expect(key.alerts).toBe(1);
  });

  it("is ready to start, naming its question, and reads no programme's status", async () => {
    const { rt, net } = runtimeFor({ "GET /api/v1/auth/connection": ADMIN, "GET /api/v1/polls": NONE });
    rt.configure(PAIRED);
    await rt.verify();
    const key = surface("k", "poll", SETTINGS);
    rt.add(key);
    await rt.tick();
    expect(key.states.at(-1)).toBe(KEY_STATE.idle);
    expect(key.titles.at(-1)).toBe("Start poll\nNext map?");
    expect(net.sent.some((r) => r.url.pathname === "/api/v1/status")).toBe(false);
  });

  it("shows the votes and the time left while a poll runs, and counting while it closes", async () => {
    const routes = { "GET /api/v1/auth/connection": ADMIN, "GET /api/v1/polls": running() };
    const { rt } = runtimeFor(routes);
    rt.configure(PAIRED);
    await rt.verify();
    const key = surface("k", "poll", SETTINGS);
    rt.add(key);
    await rt.tick();
    expect(key.states.at(-1)).toBe(KEY_STATE.live);
    expect(key.titles.at(-1)).toMatch(/^Poll on\n4 votes · 1:0[0-5]$/);
    routes["GET /api/v1/polls"] = running("closing", 9);
    await rt.tick();
    expect(key.states.at(-1)).toBe(KEY_STATE.warn);
    expect(key.titles.at(-1)).toMatch(/^Counting\n9 votes/);
  });

  it("starts its poll with a tap when none is running", async () => {
    const { rt, net } = runtimeFor({
      "GET /api/v1/auth/connection": ADMIN,
      "GET /api/v1/polls": NONE,
      "POST /api/v1/polls": json(201, { id: 12 }),
    });
    rt.configure(PAIRED);
    await rt.verify();
    const key = surface("k", "poll", SETTINGS);
    rt.add(key);
    await rt.pressPollDown("k");
    const posts = net.writes();
    expect(posts.map((r) => r.url.pathname)).toEqual(["/api/v1/polls"]);
    expect(JSON.parse(posts[0]!.body!)).toEqual({ question: "Next map?", options: ["Dust", "Mirage"], durationSeconds: 60 });
    expect(key.oks).toBe(1);
  });

  it("ends the running poll only once a hold completes, and a short press ends nothing", async () => {
    const { rt, net, sched } = runtimeFor({
      "GET /api/v1/auth/connection": ADMIN,
      "GET /api/v1/polls": running(),
      "POST /api/v1/polls/12/end": json(200, { id: 12, state: "ended" }),
    });
    rt.configure(PAIRED);
    await rt.verify();
    const key = surface("k", "poll", SETTINGS);
    rt.add(key);

    await rt.pressPollDown("k");
    expect(key.states.at(-1)).toBe(KEY_STATE.held);
    expect(key.titles.at(-1)).toBe("Hold to end\nNext map?");
    await rt.pressHoldUp("k");
    sched.fireAll();
    await settle();
    expect(net.writes()).toEqual([]);

    await rt.pressPollDown("k");
    sched.fireAll();
    await settle();
    expect(net.writes().map((r) => r.url.pathname)).toEqual(["/api/v1/polls/12/end"]);
    expect(key.oks).toBe(1);
  });

  it("needs an admin token to start or end, and sends nothing on a read token", async () => {
    const { rt, net } = runtimeFor({ "GET /api/v1/auth/connection": READ, "GET /api/v1/polls": NONE });
    rt.configure(PAIRED);
    await rt.verify();
    const key = surface("k", "poll", SETTINGS);
    rt.add(key);
    await rt.tick();
    expect(key.states.at(-1)).toBe(KEY_STATE.needsAdmin);
    await rt.pressPollDown("k");
    expect(net.writes()).toEqual([]);
    expect(key.alerts).toBe(1);
  });

  it("says the install is unreachable rather than inventing a poll state", async () => {
    const { rt } = runtimeFor({ "GET /api/v1/auth/connection": ADMIN, "GET /api/v1/polls": json(500, { error: "boom" }) });
    rt.configure(PAIRED);
    await rt.verify();
    const key = surface("k", "poll", SETTINGS);
    rt.add(key);
    await rt.tick();
    expect(key.states.at(-1)).toBe(KEY_STATE.error);
  });

  it("refuses a malformed poll answer", async () => {
    const { rt } = runtimeFor({ "GET /api/v1/auth/connection": ADMIN, "GET /api/v1/polls": json(200, { current: { id: "x" } }) });
    rt.configure(PAIRED);
    await rt.verify();
    const key = surface("k", "poll", SETTINGS);
    rt.add(key);
    await rt.tick();
    expect(key.states.at(-1)).toBe(KEY_STATE.error);
  });
});
