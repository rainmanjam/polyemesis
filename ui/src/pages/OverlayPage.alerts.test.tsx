// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import { OverlayPage } from "./OverlayPage";
import type { OverlayStyle } from "@/lib/overlay";
import type { ActivityDisplay, AlertStyle } from "@/lib/types";

const style = (alerts?: Partial<AlertStyle>): OverlayStyle => ({
  preset: "classic", font: "sans", fontSize: 24, textColor: "#ffffff", backgroundColor: "#000000",
  backgroundOpacity: 0, authorColors: true, fadeSeconds: 0, maxLines: 12, showPlatform: true, showBadges: true,
  ...(alerts ? { alerts: alerts as AlertStyle } : {}),
});

const display = (over: Partial<ActivityDisplay> = {}): ActivityDisplay => ({
  id: "a1", platform: "twitch", kind: "follow", name: "Mara", at: "2026-11-07T20:00:00Z", ...over,
});

let keepOpen = false;
function sseOf(...envs: { id: string; type: string; data: unknown }[]): Response {
  const body = envs.map((e) => `id: ${e.id}\nevent: ${e.type}\ndata: ${JSON.stringify(e)}\n\n`).join("");
  return new Response(new ReadableStream({
    // A stream that ends reconnects, and the test server then refuses (401),
    // which clears the screen: tests about timing keep it open.
    start(c) { c.enqueue(new TextEncoder().encode(body)); if (!keepOpen) c.close(); },
  }), { status: 200 });
}
const env = (d: ActivityDisplay, type = "activity.display") => ({ id: `e-${d.id}`, type, data: d });

/** Serves config + ONE stream, then refuses (so the page does not loop). */
function serve(config: OverlayStyle, ...envs: { id: string; type: string; data: unknown }[]) {
  let first = true;
  const calls: { url: string; key: string | null }[] = [];
  vi.stubGlobal("fetch", vi.fn(async (url: RequestInfo | URL, init?: RequestInit) => {
    calls.push({ url: String(url), key: new Headers(init?.headers).get("X-Overlay-Key") });
    if (String(url).endsWith("/overlay/config")) {
      return new Response(JSON.stringify({ name: "x", platforms: [], style: config }), { status: 200 });
    }
    if (!first) return new Response("{}", { status: 401 });
    first = false;
    return sseOf(...envs);
  }));
  return calls;
}

afterEach(() => {
  keepOpen = false;
  cleanup();
  vi.unstubAllGlobals();
  window.history.replaceState(null, "", "/");
});

describe("the alerts overlay page", () => {
  it("draws an activity.display with the defaults, and sends the key only as a header", async () => {
    window.history.replaceState(null, "", "/overlay/alerts#key=pov_al");
    const calls = serve(style(), env(display()));
    const { container } = render(<OverlayPage />);
    await waitFor(() => expect(screen.getByText("Mara followed")).toBeTruthy());
    const box = container.querySelector("[data-alert-id]") as HTMLElement;
    expect(box.dataset.alertLayout).toBe("banner");
    expect(calls.every((c) => c.key === "pov_al" && !c.url.includes("pov_al"))).toBe(true);
    expect(document.documentElement.style.background).toBe("transparent");
  });

  it("reads the key from the path form", async () => {
    window.history.replaceState(null, "", "/overlay/alerts/k/pov_path");
    const calls = serve(style(), env(display()));
    render(<OverlayPage />);
    await waitFor(() => expect(screen.getByText("Mara followed")).toBeTruthy());
    expect(calls.every((c) => c.key === "pov_path")).toBe(true);
  });

  it("says Someone for a blanked name, and draws the message as plain text", async () => {
    window.history.replaceState(null, "", "/overlay/alerts#key=pov_al");
    serve(
      style(),
      env(display({ kind: "tip", name: "", amount: { currency: "USD", minor: 500, display: "$5.00" }, message: "<img src=x onerror=alert(1)>" })),
    );
    const { container } = render(<OverlayPage />);
    await waitFor(() => expect(screen.getByText("Someone sent $5.00")).toBeTruthy());
    // Escaped text, not an element.
    expect(screen.getByText("<img src=x onerror=alert(1)>")).toBeTruthy();
    expect(container.querySelector("img")).toBeNull();
  });

  it("honours showMessage and showAmount, the layout and the kinds filter", async () => {
    window.history.replaceState(null, "", "/overlay/alerts#key=pov_al");
    serve(
      style({ layout: "card", kinds: ["tip"], showMessage: false, showAmount: false, seconds: 3, sound: "none", accent: "#ff0000" }),
      env(display({ id: "f", kind: "follow", name: "Skipped" })),
      env(display({ id: "t", kind: "tip", name: "Rae", amount: { display: "$9.00" }, message: "hello" })),
    );
    const { container } = render(<OverlayPage />);
    await waitFor(() => expect(screen.getByText("Rae sent a tip")).toBeTruthy());
    expect(screen.queryByText("hello")).toBeNull();
    expect(screen.queryByText(/\$9\.00/)).toBeNull();
    expect(screen.queryByText(/Skipped/)).toBeNull();
    expect((container.querySelector("[data-alert-id]") as HTMLElement).dataset.alertLayout).toBe("card");
  });

  it("shows one alert at a time", async () => {
    window.history.replaceState(null, "", "/overlay/alerts#key=pov_al");
    serve(style(), env(display({ id: "1", name: "First" })), env(display({ id: "2", name: "Second" })));
    render(<OverlayPage />);
    await waitFor(() => expect(screen.getByText("First followed")).toBeTruthy());
    expect(screen.queryByText("Second followed")).toBeNull();
  });

  it("never draws chat or polls", async () => {
    window.history.replaceState(null, "", "/overlay/alerts#key=pov_al");
    serve(
      style(),
      { id: "c1", type: "chat.display", data: { id: "c1", platform: "twitch", name: "v", text: "chat line here", at: "" } },
      env(display()),
    );
    render(<OverlayPage />);
    await waitFor(() => expect(screen.getByText("Mara followed")).toBeTruthy());
    expect(screen.queryByText("chat line here")).toBeNull();
  });

  it("draws nothing when the key is refused", async () => {
    window.history.replaceState(null, "", "/overlay/alerts#key=pov_bad");
    vi.stubGlobal("fetch", vi.fn(async () => new Response("{}", { status: 401 })));
    const { container } = render(<OverlayPage />);
    await new Promise((r) => setTimeout(r, 50));
    expect(container.textContent).toBe("");
  });

  it("draws nothing (no error banner) when the server is down", async () => {
    window.history.replaceState(null, "", "/overlay/alerts#key=pov_al");
    vi.stubGlobal("fetch", vi.fn(async () => { throw new Error("network down"); }));
    const { container } = render(<OverlayPage />);
    await new Promise((r) => setTimeout(r, 50));
    expect(container.textContent).toBe("");
    expect(screen.queryByRole("alert")).toBeNull();
  });

  it("draws nothing when there is no key at all", async () => {
    window.history.replaceState(null, "", "/overlay/alerts");
    const f = vi.fn();
    vi.stubGlobal("fetch", f);
    const { container } = render(<OverlayPage />);
    await new Promise((r) => setTimeout(r, 20));
    expect(container.textContent).toBe("");
    expect(f).not.toHaveBeenCalled();
  });
});

describe("the alerts overlay's timer", () => {
  // A timeout can fire before Date.now has reached the deadline it was set for
  // (a clock that slews or steps back, or a browser that rounds timers down).
  // Nothing is due then; the timer must re-arm rather than leave the alert up
  // until the next event happens to arrive.
  it("retires the alert even when its timer fires before the deadline by the clock", async () => {
    window.history.replaceState(null, "", "/overlay/alerts#key=pov_al");
    keepOpen = true;
    serve(style({ layout: "banner", kinds: ["follow"], showMessage: true, showAmount: true, seconds: 3, sound: "none", accent: "#7c3aed" }), env(display()));
    render(<OverlayPage />);
    await waitFor(() => expect(screen.getByText("Mara followed")).toBeTruthy());
    const real = Date.now.bind(Date);
    vi.spyOn(Date, "now").mockImplementation(() => real() - 60);
    await waitFor(() => expect(screen.queryByText("Mara followed")).toBeNull(), { timeout: 6000 });
    vi.restoreAllMocks();
  }, 15000);
});
