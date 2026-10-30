// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import { OverlayPage } from "./OverlayPage";
import type { OverlayStyle } from "@/lib/overlay";

const style = (preset: OverlayStyle["preset"], maxLines = 12): OverlayStyle => ({
  preset, font: "sans", fontSize: 24, textColor: "#ffffff", backgroundColor: "#000000",
  backgroundOpacity: 0, authorColors: true, fadeSeconds: 0, maxLines, showPlatform: true, showBadges: true,
});

function sse(id: string, text: string): Response {
  const env = { id, type: "chat.display", data: { id, platform: "twitch", name: "v", text, at: "" } };
  const body = `id: ${id}\nevent: chat.display\ndata: ${JSON.stringify(env)}\n\n`;
  return new Response(new ReadableStream({
    start(c) { c.enqueue(new TextEncoder().encode(body)); c.close(); },
  }), { status: 200 });
}

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  window.history.replaceState(null, "", "/");
});

describe("the overlay page", () => {
  // Saving in Settings ends open streams so the overlay reconnects; the
  // reconnect is where a new style must be picked up, without a reload.
  it("re-reads its style when the stream reconnects", async () => {
    window.history.replaceState(null, "", "/overlay#key=pov_k");
    const configs = [style("classic"), style("outline", 1)];
    const streams = [sse("a", "one"), sse("b", "two")];
    const fetchMock = vi.fn(async (url: RequestInfo | URL) => {
      if (String(url).endsWith("/overlay/config")) {
        return new Response(JSON.stringify({ name: "x", platforms: [], style: configs.shift() ?? style("outline", 1) }), { status: 200 });
      }
      return streams.shift() ?? new Response("", { status: 401 });
    });
    vi.stubGlobal("fetch", fetchMock);
    render(<OverlayPage />);
    await waitFor(() => expect(screen.getByRole("log").dataset.overlayPreset).toBe("classic"));
    await waitFor(() => expect(screen.getByRole("log").dataset.overlayPreset).toBe("outline"), { timeout: 4000 });
    // and the new maxLines (1) applies to the lines already on screen
    await waitFor(() => expect(document.querySelectorAll(".overlay-line").length).toBe(1));
  });

  // The path-key form (/overlay/dock/k/{key}), for an OBS that drops the
  // fragment: the key goes in the same header as the fragment form's, and the
  // page is the dock, though its path does not end in /dock.
  it("reads the key from the path and sends it in the header, on the dock too", async () => {
    window.history.replaceState(null, "", "/overlay/dock/k/pov_pathkey");
    const keys: (string | null)[] = [];
    vi.stubGlobal("fetch", vi.fn(async (url: RequestInfo | URL, init?: RequestInit) => {
      keys.push(new Headers(init?.headers).get("X-Overlay-Key"));
      if (String(url).endsWith("/overlay/config")) {
        return new Response(JSON.stringify({ name: "x", platforms: [], style: style("classic") }), { status: 200 });
      }
      return sse("a", "one");
    }));
    const { container } = render(<OverlayPage />);
    await waitFor(() => expect(screen.getByText("one")).toBeTruthy());
    expect(keys.length).toBeGreaterThan(0);
    expect(keys.every((k) => k === "pov_pathkey")).toBe(true);
    // The dock's solid background, not the overlay's transparent one.
    expect((container.firstElementChild as HTMLElement).style.background).not.toBe("transparent");
  });

  it("draws nothing, not an error, when the key is refused", async () => {
    window.history.replaceState(null, "", "/overlay#key=pov_bad");
    vi.stubGlobal("fetch", vi.fn(async () => new Response("{}", { status: 401 })));
    const { container } = render(<OverlayPage />);
    await new Promise((r) => setTimeout(r, 50));
    expect(container.textContent).toBe("");
  });
});

/** One SSE response carrying the given envelopes, then closing. */
function sseOf(...envs: { id: string; type: string; data: unknown }[]): Response {
  const body = envs.map((e) => `id: ${e.id}\nevent: ${e.type}\ndata: ${JSON.stringify(e)}\n\n`).join("");
  return new Response(new ReadableStream({
    start(c) { c.enqueue(new TextEncoder().encode(body)); c.close(); },
  }), { status: 200 });
}

const pollEnv = (id: string, state = "open") => ({
  id, type: "poll.display",
  data: { id: "poll-3", question: "Which map next?", total: 4, state,
    options: [{ label: "Dust", votes: 3 }, { label: "Mirage", votes: 1 }],
    endsAt: new Date(Date.now() + 60_000).toISOString() },
});

const chatEnv = (id: string) => ({
  id, type: "chat.display", data: { id, platform: "twitch", name: "viewer", text: "chat line here", at: "" },
});

describe("the poll widget page (#14)", () => {
  const serve = (stream: () => Response) =>
    vi.stubGlobal("fetch", vi.fn(async (url: RequestInfo | URL) => {
      if (String(url).endsWith("/overlay/config")) {
        return new Response(JSON.stringify({ name: "x", platforms: ["twitch"], style: style("classic") }), { status: 200 });
      }
      return stream();
    }));

  // The poll page draws the poll and never a chat line, though both arrive
  // on the same stream; and nothing on it names a platform.
  it("draws only the poll, with no platform on it, and transparently", async () => {
    window.history.replaceState(null, "", "/overlay/poll/k/pov_poll");
    let first = true;
    serve(() => {
      if (!first) return new Response("{}", { status: 401 });
      first = false;
      return sseOf(chatEnv("c1"), pollEnv("p1"));
    });
    const { container } = render(<OverlayPage />);
    await waitFor(() => expect(screen.getByText("Which map next?")).toBeTruthy());
    expect(screen.getByText("Dust")).toBeTruthy();
    expect(screen.getByText("75%")).toBeTruthy();
    expect(screen.queryByText("chat line here")).toBeNull();
    expect(container.textContent?.toLowerCase()).not.toMatch(/twitch|youtube|kick|ttv/);
    expect((container.firstElementChild as HTMLElement).style.background).toBe("transparent");
  });

  it("draws nothing while no poll is running", async () => {
    window.history.replaceState(null, "", "/overlay/poll#key=pov_poll");
    serve(() => new Response("{}", { status: 401 }));
    const { container } = render(<OverlayPage />);
    await new Promise((r) => setTimeout(r, 50));
    expect(container.textContent).toBe("");
  });

  // And the chat overlay, on the same stream, never draws the poll.
  it("leaves the poll off the chat overlay", async () => {
    window.history.replaceState(null, "", "/overlay#key=pov_chat");
    let first = true;
    serve(() => {
      if (!first) return new Response("{}", { status: 401 });
      first = false;
      return sseOf(pollEnv("p1"), chatEnv("c1"));
    });
    render(<OverlayPage />);
    await waitFor(() => expect(screen.getByText("chat line here")).toBeTruthy());
    expect(screen.queryByText("Which map next?")).toBeNull();
  });
});
