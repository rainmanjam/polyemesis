// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import type { BeaconV1, PlayoutPublicView } from "@/lib/types";

/* LivePlayer's hls.js wiring over a fake hls.js: the HEVC/AV1 level removal,
 * the quality menu, a fatal error, and the QoE beacons it sends. jsdom has no
 * MediaSource, so the codec probe reports nothing decodable -- which is the
 * "fall back to H.264" path this file exists to drive. */

const fake = vi.hoisted(() => {
  type Handler = (e: string, data: unknown) => void;
  const state = {
    instances: [] as FakeHls[],
    beacons: [] as { b: BeaconV1; token?: string }[],
  };
  class FakeHls {
    static isSupported = () => true;
    static Events = {
      MANIFEST_PARSED: "manifestParsed",
      LEVELS_UPDATED: "levelsUpdated",
      FRAG_CHANGED: "fragChanged",
      ERROR: "error",
    };
    handlers = new Map<string, Handler[]>();
    levels: { name: string; height: number; url: string[] }[] = [];
    removed: number[] = [];
    currentLevel = -1;
    loadLevel = 0;
    latency = 7.5;
    liveSyncPosition = 0;
    src = "";
    destroyed = false;
    constructor() {
      state.instances.push(this);
    }
    on(ev: string, h: Handler) {
      this.handlers.set(ev, [...(this.handlers.get(ev) ?? []), h]);
    }
    emit(ev: string, data: unknown = {}) {
      for (const h of this.handlers.get(ev) ?? []) h(ev, data);
    }
    loadSource(s: string) {
      this.src = s;
    }
    attachMedia() {}
    removeLevel(i: number) {
      this.removed.push(i);
      this.levels.splice(i, 1);
    }
    destroy() {
      this.destroyed = true;
    }
  }
  return { state, FakeHls };
});

vi.mock("hls.js", () => ({ default: fake.FakeHls }));
vi.mock("@/lib/liveCaptions", () => ({ useLiveCaptionTrack: () => {} }));
vi.mock("@/lib/api", () => ({
  ApiError: class extends Error {},
  api: {
    playoutPublic: () => Promise.resolve(currentView),
    playoutPosterUrl: () => "/poster.jpg",
    playoutOfflineClipUrl: (p: string) => p,
    playoutBeacon: (b: BeaconV1, token?: string) => {
      fake.state.beacons.push({ b, token });
      return Promise.resolve(true);
    },
  },
}));

import { PublicPlayer } from "./PublicPlayer";

let currentView: PlayoutPublicView;
const TOKEN = "playback-token-for-tests-only_xxxxxxxxxxxxx";

function liveView(variants: PlayoutPublicView["variants"]): PlayoutPublicView {
  return {
    enabled: true, title: "", description: "", master: "/playout/master.m3u8", poster: "/poster.jpg",
    variants, viewers: 1, state: "live", offline: {},
  };
}

const level = (name: string, height: number) => ({
  name, height, url: [`http://x/playout/${name}/index.m3u8`],
});

afterEach(() => {
  cleanup();
  vi.useRealTimers();
  fake.state.instances = [];
  fake.state.beacons = [];
  window.history.replaceState(null, "", "/");
});

async function mount(variants: PlayoutPublicView["variants"]) {
  currentView = liveView(variants);
  window.history.replaceState(null, "", `/watch/${TOKEN}`);
  render(<PublicPlayer />);
  await waitFor(() => expect(fake.state.instances.length).toBeGreaterThan(0));
  return fake.state.instances[0];
}

describe("LivePlayer over hls.js", () => {
  it("loads the master with the token and removes the rung this browser cannot decode", async () => {
    const hls = await mount([
      { name: "hd", playlist: "/playout/hd/index.m3u8", height: 720, codec: "h264" },
      { name: "uhd", playlist: "/playout/uhd/index.m3u8", height: 2160, codec: "hevc" },
    ]);
    expect(hls.src).toBe(`/playout/master.m3u8?t=${TOKEN}`);
    hls.levels = [level("hd", 720), level("uhd", 2160)];
    act(() => hls.emit("manifestParsed"));
    expect(hls.removed).toEqual([1]);
    expect(screen.getByText(/this browser cannot play/)).toBeTruthy();
    // One rung left: no menu to choose from.
    expect(screen.queryByRole("combobox")).toBeNull();
  });

  it("offers Auto plus each rung, and switches hls.js to the one chosen", async () => {
    const hls = await mount([
      { name: "lo", playlist: "/playout/lo/index.m3u8", height: 360, codec: "h264" },
      { name: "hi", playlist: "/playout/hi/index.m3u8", height: 1080, codec: "h264" },
    ]);
    hls.levels = [level("lo", 360), level("hi", 1080)];
    act(() => hls.emit("manifestParsed"));
    const select = screen.getByRole("combobox") as HTMLSelectElement;
    expect(Array.from(select.options).map((o) => o.textContent)).toEqual(["Auto", "360p", "1080p"]);
    fireEvent.change(select, { target: { value: "1" } });
    expect(hls.currentLevel).toBe(1);
    fireEvent.change(select, { target: { value: "-1" } });
    expect(hls.currentLevel).toBe(-1);
  });

  it("reports startup, the rung, latency and a fatal error in its beacons, with the token", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const hls = await mount([{ name: "hd", playlist: "/playout/hd/index.m3u8", height: 720, codec: "h264" }]);
    hls.levels = [level("hd", 720)];
    act(() => hls.emit("manifestParsed"));
    const video = document.querySelector("video")!;
    act(() => {
      fireEvent.playing(video);
    });
    await waitFor(() => expect(fake.state.beacons.length).toBe(1));
    const start = fake.state.beacons[0];
    expect(start.token).toBe(TOKEN);
    expect(start.b).toMatchObject({ v: 1, kind: "start", seq: 1, variant: "hd", latencyMs: 7500 });
    expect(start.b.startupMs).toBeGreaterThanOrEqual(0);

    act(() =>
      hls.emit("error", { fatal: true, type: "networkError", details: "fragLoadError", url: `/seg.ts?t=${TOKEN}` }),
    );
    expect(hls.destroyed).toBe(true);
    // The player retries on its own after five seconds.
    await act(async () => {
      vi.advanceTimersByTime(5000);
    });
    expect(fake.state.instances.length).toBe(2);
    await act(async () => {
      vi.advanceTimersByTime(15000);
    });
    const hb = fake.state.beacons.find((x) => x.b.kind === "heartbeat");
    expect(hb?.b.errors).toEqual(["network"]);
    expect(JSON.stringify(fake.state.beacons)).not.toContain("seg.ts");
  });

  it("sends end when the page goes away", async () => {
    await mount([{ name: "hd", playlist: "/playout/hd/index.m3u8", height: 720, codec: "h264" }]);
    act(() => {
      window.dispatchEvent(new Event("pagehide"));
    });
    expect(fake.state.beacons.map((x) => x.b.kind)).toContain("end");
  });

  it("draws the DVR bar, offers Go live when behind, and shares the moment on the wall clock", async () => {
    const hls = await mount([{ name: "hd", playlist: "/playout/hd/index.m3u8", height: 720, codec: "h264" }]);
    const video = stubVideo(document.querySelector("video")!, { start: 40, end: 160, at: 100 });
    hls.liveSyncPosition = 154;
    // The anchor: media time 100 played at a known instant.
    act(() => hls.emit("fragChanged", { frag: { programDateTime: 1_793_541_600_000, start: 100 } }));
    act(() => {
      fireEvent.playing(video);
      fireEvent.timeUpdate(video);
    });
    const bar = screen.getByRole("slider", { name: "Seek within the replay window" }) as HTMLInputElement;
    expect([bar.min, bar.max, bar.value]).toEqual(["40", "160", "100"]);
    expect(screen.getByText("Behind live")).toBeTruthy();

    fireEvent.change(bar, { target: { value: "70" } });
    expect(video.currentTime).toBe(70);

    const writes: string[] = [];
    Object.defineProperty(navigator, "clipboard", {
      configurable: true,
      value: { writeText: (t: string) => (writes.push(t), Promise.resolve()) },
    });
    await act(async () => {
      fireEvent.click(screen.getByRole("button", { name: "Share this moment" }));
    });
    // 30 s before the anchor's instant, in whole seconds, token in the path.
    expect(writes).toEqual([`${window.location.origin}/watch/${TOKEN}?t=1793541570`]);
    expect((screen.getByRole("textbox", { name: "Share this moment" }) as HTMLInputElement).value).toBe(writes[0]);
    expect(screen.getByRole("button", { name: "Link copied" })).toBeTruthy();

    act(() => {
      fireEvent.timeUpdate(video);
    });
    fireEvent.click(screen.getByRole("button", { name: "Go live" }));
    expect(video.currentTime).toBe(154);
    act(() => {
      fireEvent.timeUpdate(video);
    });
    expect(screen.queryByRole("button", { name: "Go live" })).toBeNull();
    expect(screen.getByText("LIVE")).toBeTruthy();
  });

  it("starts a ?t= link at that moment, or says it has left the replay window", async () => {
    for (const [at, inWindow] of [[1_793_541_580, true], [1_793_540_000, false]] as const) {
      currentView = liveView([{ name: "hd", playlist: "/playout/hd/index.m3u8", height: 720, codec: "h264" }]);
      window.history.replaceState(null, "", `/watch/${TOKEN}?t=${at}`);
      fake.state.instances = [];
      const { unmount } = render(<PublicPlayer />);
      await waitFor(() => expect(fake.state.instances.length).toBeGreaterThan(0));
      const hls = fake.state.instances[0];
      const video = stubVideo(document.querySelector("video")!, { start: 40, end: 160, at: 150 });
      act(() => hls.emit("fragChanged", { frag: { programDateTime: 1_793_541_600_000, start: 100 } }));
      act(() => {
        fireEvent.loadedMetadata(video);
      });
      if (inWindow) {
        expect(video.currentTime).toBe(80);
        expect(screen.queryByText(/no longer in the replay window/)).toBeNull();
      } else {
        expect(video.currentTime).toBe(150);
        expect(screen.getByText(/no longer in the replay window/)).toBeTruthy();
      }
      unmount();
    }
  });
});

/** jsdom's <video> has no media: give it a seekable window and a settable
 *  playhead so the DVR controls have something to read. */
function stubVideo(v: HTMLVideoElement, r: { start: number; end: number; at: number }) {
  let t = r.at;
  Object.defineProperty(v, "seekable", {
    configurable: true,
    get: () => ({ length: 1, start: () => r.start, end: () => r.end }),
  });
  Object.defineProperty(v, "currentTime", { configurable: true, get: () => t, set: (x: number) => (t = x) });
  Object.defineProperty(v, "play", { configurable: true, value: () => Promise.resolve() });
  return v;
}
