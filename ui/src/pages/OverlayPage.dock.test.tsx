// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import { OverlayPage } from "./OverlayPage";
import { translate } from "@/lib/i18n";

/* app-council A99: THE DOCK SAYS WHICH THING WENT WRONG.
 *
 * The dock is the one overlay surface only the operator sees, inside OBS. It
 * used to answer every failure with "This overlay key is not valid any more" --
 * a URL with NO key in it, a key the server refused, and a server that did not
 * answer at all -- and to draw nothing (a blank black panel) while it loaded.
 * Each of those has a different fix: copy the URL again, make a new key, or
 * check the server. The overlay itself still draws nothing in every case: an
 * error there would be burned into the broadcast. */

const style = {
  preset: "classic", font: "sans", fontSize: 24, textColor: "#ffffff", backgroundColor: "#000000",
  backgroundOpacity: 0, authorColors: true, fadeSeconds: 0, maxLines: 12, showPlatform: true, showBadges: true,
};

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  window.history.replaceState(null, "", "/");
});

function sse(id: string, text: string): Response {
  const env = { id, type: "chat.display", data: { id, platform: "twitch", name: "v", text, at: "" } };
  const body = `id: ${id}\nevent: chat.display\ndata: ${JSON.stringify(env)}\n\n`;
  return new Response(new ReadableStream({
    start(c) { c.enqueue(new TextEncoder().encode(body)); c.close(); },
  }), { status: 200 });
}

const en = (k: Parameters<typeof translate>[1]) => translate("en", k);

describe("the OBS dock's failure and loading states", () => {
  it("says the URL has no key when there is none, without asking the server", async () => {
    window.history.replaceState(null, "", "/overlay/dock");
    const fetchMock = vi.fn();
    vi.stubGlobal("fetch", fetchMock);
    render(<OverlayPage />);
    expect(screen.getByText(en("overlay.dockMissingKey"))).toBeTruthy();
    expect(screen.queryByText(en("overlay.dockDenied"))).toBeNull();
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("says the key was refused when the server answers 401", async () => {
    window.history.replaceState(null, "", "/overlay/dock#key=pov_old");
    vi.stubGlobal("fetch", vi.fn(async () => new Response("{}", { status: 401 })));
    render(<OverlayPage />);
    await waitFor(() => expect(screen.getByText(en("overlay.dockDenied"))).toBeTruthy());
  });

  it("says the server could not be reached on a network error, not that the key is bad", async () => {
    window.history.replaceState(null, "", "/overlay/dock#key=pov_ok");
    vi.stubGlobal("fetch", vi.fn(async () => { throw new TypeError("Failed to fetch"); }));
    render(<OverlayPage />);
    await waitFor(() => expect(screen.getByText(en("overlay.dockNetworkError"))).toBeTruthy());
    expect(screen.queryByText(en("overlay.dockDenied"))).toBeNull();
  });

  it("treats a server error as unreachable too, and recovers when it answers", async () => {
    window.history.replaceState(null, "", "/overlay/dock#key=pov_ok");
    let calls = 0;
    let streamed = false;
    vi.stubGlobal("fetch", vi.fn(async (url: RequestInfo | URL) => {
      if (String(url).endsWith("/overlay/config")) {
        calls++;
        if (calls === 1) return new Response("{}", { status: 502 });
        return new Response(JSON.stringify({ name: "x", platforms: [], style }), { status: 200 });
      }
      if (streamed) return new Response("", { status: 401 });
      streamed = true;
      return sse("a", "back on air");
    }));
    render(<OverlayPage />);
    await waitFor(() => expect(screen.getByText(en("overlay.dockNetworkError"))).toBeTruthy());
    // The dock retries on its own: an OBS dock has no reload button anyone
    // uses. Proven by a chat line arriving, not merely by the error going.
    await waitFor(() => expect(screen.getByText("back on air")).toBeTruthy(), { timeout: 5000 });
    expect(screen.queryByText(en("overlay.dockNetworkError"))).toBeNull();
  });

  it("shows a small loading line instead of a blank panel while the config loads", async () => {
    window.history.replaceState(null, "", "/overlay/dock#key=pov_ok");
    vi.stubGlobal("fetch", vi.fn(() => new Promise<Response>(() => {})));
    render(<OverlayPage />);
    expect(screen.getByText(en("overlay.dockLoading"))).toBeTruthy();
  });
});

describe("the on-stream overlay draws nothing in every one of those states", () => {
  const cases: [string, string, () => Promise<Response>][] = [
    ["no key", "/overlay", async () => new Response("{}", { status: 401 })],
    ["a refused key", "/overlay#key=pov_old", async () => new Response("{}", { status: 401 })],
    ["a network error", "/overlay#key=pov_ok", async () => { throw new TypeError("Failed to fetch"); }],
    ["still loading", "/overlay#key=pov_ok", () => new Promise<Response>(() => {})],
  ];
  it.each(cases)("%s", async (_name, url, respond) => {
    window.history.replaceState(null, "", url);
    vi.stubGlobal("fetch", vi.fn(respond));
    const { container } = render(<OverlayPage />);
    await new Promise((r) => setTimeout(r, 50));
    expect(container.textContent).toBe("");
  });
});
