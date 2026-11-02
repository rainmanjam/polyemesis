// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import type { PlayoutPublicView } from "@/lib/types";

/* The public player's states, over a mocked API. Playback itself (hls.js,
 * MediaSource) does not exist in jsdom and is driven end to end by the
 * Playwright run against a real server instead; what is pinned here is which
 * surface a viewer gets for each answer, and that the link is read right. */

const calls = vi.hoisted(() => ({ tokens: [] as (string | undefined)[], view: null as unknown }));

vi.mock("@/lib/api", () => ({
  ApiError: class ApiError extends Error {
    status: number;
    constructor(status: number, msg: string) {
      super(msg);
      this.status = status;
    }
  },
  api: {
    playoutPublic: (token?: string) => {
      calls.tokens.push(token);
      return Promise.resolve(calls.view);
    },
    playoutPosterUrl: (token?: string) => "/api/v1/playout/poster.jpg" + (token ? `?t=${token}` : ""),
    playoutOfflineClipUrl: (path: string, token?: string) => path + (token ? `?t=${token}` : ""),
    playoutBeacon: () => Promise.resolve(true),
  },
}));

import { OfflineCard, PublicPlayer } from "./PublicPlayer";

const TOKEN = "playback-token-for-tests-only_xxxxxxxxxxxxx";

function view(over: Partial<PlayoutPublicView> = {}): PlayoutPublicView {
  return {
    enabled: true,
    title: "Friday show",
    description: "",
    master: "/playout/master.m3u8",
    poster: "/api/v1/playout/poster.jpg",
    variants: [{ name: "720p", playlist: "/playout/720p/index.m3u8", height: 720, codec: "h264" }],
    viewers: 3,
    state: "live",
    offline: {},
    ...over,
  };
}

function at(path: string) {
  window.history.replaceState(null, "", path);
}

beforeEach(() => {
  calls.tokens = [];
});
afterEach(() => {
  cleanup();
  at("/");
});

describe("PublicPlayer", () => {
  it("plays when live, with the path token and a ?t= timestamp read apart", async () => {
    calls.view = view();
    at(`/watch/${TOKEN}?t=1793541600`);
    const { container } = render(<PublicPlayer />);
    await waitFor(() => expect(container.querySelector("video")).not.toBeNull());
    // The timestamp did not replace the token.
    expect(calls.tokens[0]).toBe(TOKEN);
    expect(screen.queryByTestId("offline-card")).toBeNull();
    expect(screen.getByRole("button", { name: "Share this moment" })).toBeTruthy();
  });

  it("shows the offline card, not a spinning player, for a published stream that is off air", async () => {
    calls.view = view({ state: "offline", offline: { message: "Back Sunday" } });
    at(`/watch/${TOKEN}`);
    render(<PublicPlayer />);
    const card = await screen.findByTestId("offline-card");
    expect(card.dataset.state).toBe("offline");
    expect(screen.getByText("Back Sunday")).toBeTruthy();
    expect(screen.getByRole("heading", { name: "Not live right now" })).toBeTruthy();
  });

  it("says ended, distinctly, for a broadcast that just stopped", async () => {
    calls.view = view({ state: "ended", endedAt: "2026-11-01T20:00:00Z" });
    at("/watch");
    render(<PublicPlayer />);
    const card = await screen.findByTestId("offline-card");
    expect(card.dataset.state).toBe("ended");
    expect(screen.getByRole("heading", { name: "The broadcast has ended" })).toBeTruthy();
    expect(calls.tokens[0]).toBeUndefined();
  });

  it("still accepts a legacy ?t=<token> link", async () => {
    calls.view = view();
    at(`/watch?t=${TOKEN}`);
    render(<PublicPlayer />);
    await waitFor(() => expect(calls.tokens[0]).toBe(TOKEN));
  });
});

describe("OfflineCard", () => {
  const now = Date.parse("2026-11-01T12:00:00Z");

  it("counts down to the next scheduled start", () => {
    render(
      <OfflineCard
        view={view({ state: "offline", nextStartAt: "2026-11-01T15:30:00Z" })}
        token={TOKEN}
        now={now}
      />,
    );
    expect(screen.getByTestId("countdown").textContent).toMatch(/Next stream in 3 hours/);
  });

  it("says starting soon once the scheduled moment has passed", () => {
    render(<OfflineCard view={view({ state: "offline", nextStartAt: "2026-11-01T11:59:00Z" })} now={now} />);
    expect(screen.getByText("Starting soon")).toBeTruthy();
    expect(screen.queryByTestId("countdown")).toBeNull();
  });

  it("links the latest VOD safely and loops the clip with the token", () => {
    const { container } = render(
      <OfflineCard
        view={view({
          state: "ended",
          offline: { vodUrl: "https://example.com/vod/9", clip: "/api/v1/playout/offline-clip" },
        })}
        token={TOKEN}
        now={now}
      />,
    );
    const a = screen.getByRole("link", { name: "Watch the latest VOD" });
    expect(a.getAttribute("href")).toBe("https://example.com/vod/9");
    expect(a.getAttribute("rel")).toContain("noopener");
    const clip = container.querySelector("video");
    expect(clip?.getAttribute("src")).toBe(`/api/v1/playout/offline-clip?t=${TOKEN}`);
    expect(clip?.hasAttribute("loop")).toBe(true);
    expect(clip?.muted).toBe(true);
  });

  it("falls back to the poster when there is no clip", () => {
    const { container } = render(<OfflineCard view={view({ state: "offline" })} token={TOKEN} now={now} />);
    expect(container.querySelector("video")).toBeNull();
    expect(container.querySelector("img")?.getAttribute("src")).toBe(`/api/v1/playout/poster.jpg?t=${TOKEN}`);
  });
});
