// @vitest-environment jsdom

import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";

/* THE WHOLE PLAYOUT PAGE, wired (app-council A16-A20).
 *
 * The card tests mount each piece alone. These mount the page over a mocked
 * API, to pin the wiring between them: Turn on writes enabled, the title is
 * written only by Save, and a packaging draft applied after a header toggle
 * carries both -- it merges over the CURRENT settings, so the switch the
 * operator flipped meanwhile is not reverted by the apply. */

const state = vi.hoisted(() => ({
  settings: {
    playout: {
      enabled: false,
      public: false,
      allowCrossOrigin: false,
      format: "hls",
      segmentSeconds: 4,
      playlistSegments: 6,
      dvrWindowSeconds: 0,
      maxDiskMb: 2048,
      audioKbps: 128,
      sessionIdleSeconds: 30,
      maxSessions: 100,
      variants: [{ name: "main", enabled: true, renditionId: null, audioTrack: 0 }],
    },
  } as Record<string, unknown> & { playout: Record<string, unknown> },
  puts: [] as Array<{ playout: Record<string, unknown> }>,
  publishes: [] as Array<Record<string, unknown>>,
}));

const view = () => ({
  running: true,
  exposed: false,
  protection: "token",
  title: "",
  description: "",
  urls: { watch: "/w", master: "/m.m3u8", embed: "<iframe>" },
  status: {
    enabled: state.settings.playout.enabled,
    public: false,
    analytics: { viewers: 0, peak: 0, sessions: 0, uncounted: 0 },
    usage: { bytes: 0, overLimit: false },
    variants: [],
  },
});

vi.mock("@/lib/api", () => ({
  api: {
    playout: () => Promise.resolve(view()),
    getSettings: () => Promise.resolve(state.settings),
    listRenditions: () => Promise.resolve([]),
    listSources: () => Promise.resolve([]),
    putSettings: (s: { playout: Record<string, unknown> }) => {
      state.puts.push(s);
      state.settings = { ...state.settings, playout: s.playout };
      return Promise.resolve(state.settings);
    },
    updatePlayoutPublish: (b: Record<string, unknown>) => {
      state.publishes.push(b);
      return Promise.resolve({});
    },
    rotatePlayoutToken: () => Promise.resolve({}),
    resetPlayoutAnalytics: () => Promise.resolve({}),
  },
}));

vi.mock("sonner", () => ({
  toast: { error: () => {}, success: () => {}, info: () => {} },
}));

const { PlayoutPage } = await import("./PlayoutPage");

beforeAll(() => {
  Element.prototype.scrollIntoView = () => {};
});

afterEach(() => {
  cleanup();
  state.puts.length = 0;
  state.publishes.length = 0;
  state.settings = { ...state.settings, playout: { ...state.settings.playout, enabled: false } };
});

describe("PlayoutPage wiring", () => {
  it("Turn on playout writes enabled: true", async () => {
    render(<PlayoutPage />);
    fireEvent.click(await screen.findByRole("button", { name: "Turn on playout" }));
    await waitFor(() => expect(state.puts).toHaveLength(1));
    expect(state.puts[0].playout.enabled).toBe(true);
  });

  it("writes the title only on Save", async () => {
    render(<PlayoutPage />);
    const title = await screen.findByLabelText("Title");
    fireEvent.change(title, { target: { value: "Friday" } });
    fireEvent.blur(title);
    expect(state.publishes).toHaveLength(0);
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(state.publishes).toEqual([{ title: "Friday", description: "" }]));
  });

  it("applies a packaging draft over a header toggle made meanwhile, keeping both", async () => {
    render(<PlayoutPage />);
    fireEvent.click(await screen.findByRole("button", { name: /Advanced/ }));
    fireEvent.change(screen.getByLabelText("Segment length (s)"), { target: { value: "2" } });

    fireEvent.click(screen.getByRole("switch", { name: "Enable playout" }));
    await waitFor(() => expect(state.puts).toHaveLength(1));

    fireEvent.click(await screen.findByRole("button", { name: "Apply and restart stream" }));
    await waitFor(() => expect(state.puts).toHaveLength(2));
    expect(state.puts[1].playout).toMatchObject({ enabled: true, segmentSeconds: 2 });
  });
});
