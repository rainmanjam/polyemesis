// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";

import {
  chatExportProblem,
  chatExportUrl,
  downloadChatFile,
  parseLocalInput,
  recordingChatFileName,
  toLocalInput,
} from "./chatExport";

describe("chat export helpers", () => {
  it("names a recording's chat file like the server's sidecar", () => {
    expect(recordingChatFileName("rec-s1-20260304-200000.mkv", "json")).toBe("rec-s1-20260304-200000.chat.json");
    expect(recordingChatFileName("rec.v2.mp4", "vtt")).toBe("rec.v2.chat.vtt");
    expect(recordingChatFileName("noext", "vtt")).toBe("noext.chat.vtt");
  });

  it("reads and writes datetime-local text in local time", () => {
    const d = new Date(2026, 2, 4, 9, 5);
    expect(toLocalInput(d)).toBe("2026-03-04T09:05");
    expect(parseLocalInput("2026-03-04T09:05")?.getTime()).toBe(d.getTime());
    expect(parseLocalInput("")).toBeNull();
    expect(parseLocalInput("not a time")).toBeNull();
  });

  it("names the two problems the server would refuse with a 400", () => {
    expect(chatExportProblem("", "")).toBe("needFrom");
    expect(chatExportProblem("2026-03-04T10:00", "2026-03-04T09:00")).toBe("needOrder");
    expect(chatExportProblem("2026-03-04T10:00", "2026-03-04T10:00")).toBe("needOrder");
    expect(chatExportProblem("2026-03-04T10:00", "")).toBeNull();
    expect(chatExportProblem("2026-03-04T10:00", "2026-03-04T11:00")).toBeNull();
  });

  it("builds the export URL, leaving off a blank end and an unset platform", () => {
    const from = new Date("2026-03-04T10:00:00Z");
    const to = new Date("2026-03-04T11:00:00Z");
    expect(chatExportUrl("/api/v1", { from, to, platform: "twitch", format: "csv" })).toBe(
      "/api/v1/chat/export?from=2026-03-04T10%3A00%3A00.000Z&format=csv&to=2026-03-04T11%3A00%3A00.000Z&platform=twitch",
    );
    expect(chatExportUrl("/api/v1", { from, to: null, platform: "", format: "json" })).toBe(
      "/api/v1/chat/export?from=2026-03-04T10%3A00%3A00.000Z&format=json",
    );
  });
});

describe("downloadChatFile", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
  });

  it("saves the body under the given name", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response("{}", { status: 200 })));
    const create = vi.fn(() => "blob:x");
    const revoke = vi.fn();
    vi.stubGlobal("URL", { createObjectURL: create, revokeObjectURL: revoke });
    const click = vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => {});

    await downloadChatFile("/api/v1/recordings/1/chat?format=json", "rec.chat.json");

    expect(click).toHaveBeenCalledOnce();
    expect(create).toHaveBeenCalledOnce();
    expect(revoke).toHaveBeenCalledWith("blob:x");
  });

  it("throws the server's own sentence on an error", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(new Response(JSON.stringify({ error: "chat is no longer stored" }), { status: 404 })),
    );
    await expect(downloadChatFile("/x", "f")).rejects.toThrow("chat is no longer stored");
  });

  it("falls back to the status when the error has no body", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response("<html>", { status: 502 })));
    await expect(downloadChatFile("/x", "f")).rejects.toThrow("502");
  });
});
