// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";

import { api } from "./api";
import { autoApi } from "./autoApi";
import { onSessionEnded } from "./session";

/* A session that expires mid-use has to put the operator back on the login
 * screen. Before this, only App's load-time gate read a 401: every request
 * after that raised a toast saying "not signed in" and left the operator on a
 * console where every button failed the same way, for as long as they kept
 * clicking. Sessions last seven days, so this is a weekly event on a console
 * that is left open.
 *
 * BOTH CLIENTS, because lib/autoApi.ts is the only way some routes are
 * reached and a contract honoured by one of them reads as honoured. */

afterEach(() => {
  vi.unstubAllGlobals();
});

function respond(status: number, body: unknown) {
  vi.stubGlobal(
    "fetch",
    vi.fn(async () =>
      new Response(JSON.stringify(body), {
        status,
        headers: { "Content-Type": "application/json" },
      }),
    ),
  );
}

function listen() {
  const heard = vi.fn();
  const stop = onSessionEnded(heard);
  return { heard, stop };
}

describe("a 401 in the middle of a session", () => {
  it("ends the session from lib/api.ts", async () => {
    respond(401, { error: "not signed in" });
    const { heard, stop } = listen();
    await expect(api.listDestinations()).rejects.toThrow();
    expect(heard).toHaveBeenCalledTimes(1);
    stop();
  });

  it("ends the session from lib/autoApi.ts", async () => {
    respond(401, { error: "not signed in" });
    const { heard, stop } = listen();
    await expect(autoApi.get("/alerts")).rejects.toThrow();
    expect(heard).toHaveBeenCalledTimes(1);
    stop();
  });

  it("does not end it for a wrong password at sign-in", async () => {
    respond(401, { error: "incorrect username or password" });
    const { heard, stop } = listen();
    await expect(api.login("admin", "nope")).rejects.toThrow();
    expect(heard).not.toHaveBeenCalled();
    stop();
  });

  it("does not end it for a wrong current password on a change", async () => {
    // The session is fine; the operator mistyped. Throwing them out to the
    // login screen for it would lose the form they were filling in.
    respond(401, { error: "current password is incorrect" });
    const { heard, stop } = listen();
    await expect(api.changePassword("wrong", "new-password-123")).rejects.toThrow();
    expect(heard).not.toHaveBeenCalled();
    stop();
  });

  it("does not end it for a 403", async () => {
    respond(403, { error: "forbidden" });
    const { heard, stop } = listen();
    await expect(api.listDestinations()).rejects.toThrow();
    expect(heard).not.toHaveBeenCalled();
    stop();
  });
});
