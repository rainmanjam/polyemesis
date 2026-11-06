// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";
import { ApiError, api } from "./api";
import { onSessionEnded } from "./session";

/* uploadMedia keeps XHR for upload progress but shares the transport's session
 * check and error shape. */

class FakeXhr {
  static reply = { status: 200, text: "{}" };
  status = 0;
  responseText = "";
  withCredentials = false;
  headers: Record<string, string> = {};
  upload: { onprogress?: unknown } = {};
  onload?: () => void;
  onerror?: () => void;
  onabort?: () => void;
  open() {}
  setRequestHeader(k: string, v: string) {
    this.headers[k] = v;
  }
  abort() {}
  send() {
    this.status = FakeXhr.reply.status;
    this.responseText = FakeXhr.reply.text;
    this.onload?.();
  }
}

const file = () => new File(["x"], "a.mp4");

afterEach(() => vi.unstubAllGlobals());

describe("api.uploadMedia", () => {
  it("sends a 401 mid-upload to the sign-in screen, like every other request", async () => {
    vi.stubGlobal("XMLHttpRequest", FakeXhr);
    FakeXhr.reply = { status: 401, text: JSON.stringify({ error: "not signed in" }) };
    const ended = vi.fn();
    const off = onSessionEnded(ended);
    await expect(api.uploadMedia(file())).rejects.toBeInstanceOf(ApiError);
    off();
    expect(ended).toHaveBeenCalledTimes(1);
  });

  it("keeps the server's error code", async () => {
    vi.stubGlobal("XMLHttpRequest", FakeXhr);
    FakeXhr.reply = { status: 409, text: JSON.stringify({ error: "disk full", code: "disk_full" }) };
    await expect(api.uploadMedia(file())).rejects.toMatchObject({ status: 409, code: "disk_full", message: "disk full" });
  });

  it("resolves with the stored file", async () => {
    vi.stubGlobal("XMLHttpRequest", FakeXhr);
    FakeXhr.reply = { status: 201, text: JSON.stringify({ name: "a.mp4" }) };
    await expect(api.uploadMedia(file())).resolves.toMatchObject({ name: "a.mp4" });
  });
});
