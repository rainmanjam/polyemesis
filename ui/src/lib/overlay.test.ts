import { describe, expect, it, vi } from "vitest";
import {
  OVERLAY_KEY_HEADER,
  SSEParser,
  addLine,
  expireLines,
  keyFromHash,
  overlayUrl,
  removeLines,
  runOverlayStream,
  withOpacity,
  type ShownLine,
} from "./overlay";

const line = (id: string, receivedAt = 0): ShownLine => ({
  id, platform: "twitch", name: "n", text: id, at: "", receivedAt,
});

describe("the OBS URL", () => {
  it("carries the key in the fragment, never the path or query", () => {
    const u = new URL(overlayUrl("https://box.example", "pov_abc-_1"));
    expect(u.pathname).toBe("/overlay");
    expect(u.search).toBe("");
    expect(keyFromHash(u.hash)).toBe("pov_abc-_1");
    expect(new URL(overlayUrl("https://box.example", "pov_k", true)).pathname).toBe("/overlay/dock");
  });
  it("reads no key from an empty or foreign fragment", () => {
    expect(keyFromHash("")).toBe("");
    expect(keyFromHash("#scene=2")).toBe("");
  });
});

describe("the lines on screen", () => {
  it("trims to maxLines, oldest first, and ignores a repeat", () => {
    let ls: ShownLine[] = [];
    for (const id of ["a", "b", "c", "c"]) ls = addLine(ls, line(id), 2);
    expect(ls.map((l) => l.id)).toEqual(["b", "c"]);
  });
  it("removes a retracted line in the same update", () => {
    const ls = [line("a"), line("b")];
    expect(removeLines(ls, ["a", "zzz"]).map((l) => l.id)).toEqual(["b"]);
    expect(removeLines(ls, ["zzz"])).toBe(ls);
  });
  it("fades by receipt time, and never with fade 0", () => {
    const ls = [line("old", 1000), line("new", 9000)];
    expect(expireLines(ls, 5, 10_000).map((l) => l.id)).toEqual(["new"]);
    expect(expireLines(ls, 0, 1e12)).toBe(ls);
  });
  it("builds rgba from #rrggbb and refuses anything else", () => {
    expect(withOpacity("#ff0080", 50)).toBe("rgba(255, 0, 128, 0.5)");
    expect(withOpacity("red;x", 50)).toBe("transparent");
  });
});

describe("SSEParser", () => {
  it("joins frames split across chunks and skips comments", () => {
    const p = new SSEParser();
    expect(p.push("retry: 3000\n: hello\n\nid: b-1\nevent: chat.disp")).toEqual([]);
    const out = p.push('lay\ndata: {"a":1}\n\n: heartbeat\n\n');
    expect(out).toEqual([{ id: "b-1", event: "chat.display", data: '{"a":1}' }]);
  });
});

function sseResponse(body: string, status = 200): Response {
  const stream = new ReadableStream<Uint8Array>({
    start(c) {
      c.enqueue(new TextEncoder().encode(body));
      c.close();
    },
  });
  return new Response(stream, { status });
}

const frame = (id: string, type: string, data: unknown) =>
  `id: ${id}\nevent: ${type}\ndata: ${JSON.stringify({ id, type, data })}\n\n`;

describe("runOverlayStream", () => {
  it("sends the key as a header, resumes with Last-Event-ID, and stops on 401", async () => {
    const calls: { url: string; headers: Record<string, string> }[] = [];
    const responses = [
      sseResponse(frame("x-1", "chat.display", { id: "d1", platform: "twitch", name: "n", text: "hi", at: "" }) +
        frame("x-2", "chat.undisplay", { platform: "twitch", ids: ["d1"] }) +
        frame("x-3", "destination.down", {})),
      new Response("{}", { status: 401 }),
    ];
    const fetchImpl = vi.fn(async (url: RequestInfo | URL, init?: RequestInit) => {
      calls.push({ url: String(url), headers: init?.headers as Record<string, string> });
      return responses.shift()!;
    });
    const onLine = vi.fn();
    const onRemove = vi.fn();
    const onDenied = vi.fn();
    await runOverlayStream("pov_secret", { onLine, onRemove, onDenied }, new AbortController().signal,
      fetchImpl as unknown as typeof fetch, async () => {});

    expect(onLine).toHaveBeenCalledWith(expect.objectContaining({ id: "d1", text: "hi" }));
    expect(onRemove).toHaveBeenCalledWith(["d1"]);
    expect(onDenied).toHaveBeenCalledTimes(1);
    // The key is in a header on every request and in no URL.
    for (const c of calls) {
      expect(c.url).not.toContain("pov_secret");
      expect(c.headers[OVERLAY_KEY_HEADER]).toBe("pov_secret");
    }
    expect(calls[1].headers["Last-Event-ID"]).toBe("x-3");
  });
});
