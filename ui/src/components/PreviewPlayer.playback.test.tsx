// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, render, screen } from "@testing-library/react";

import { PreviewPlayer } from "./PreviewPlayer";

/* THE HALF OF PreviewPlayer THAT ONLY EXISTS ONCE A STREAM ARRIVES.
 *
 * PreviewPlayer.test.tsx renders with hls.js reporting itself unsupported,
 * which is the right way to ask what the overlay says -- and which means the
 * player never attaches, never plays, and never tears down. Everything the
 * reported bug is actually about lives on the other side of that: the picture
 * that outlived its stream.
 *
 * Two things kept the old frame on screen and both are asserted here:
 *
 *   destroying the Hls instance leaves the <video> holding its last decoded
 *   frame, so switching source showed the PREVIOUS programme under the NEW
 *   one's name until a segment of the new one arrived
 *
 *   `playing` never went false on teardown, so the opaque overlay that would
 *   have covered that frame was not drawn over it
 *
 * Neither is visible in jsdom as a picture, so they are asserted as the two
 * things that produce one: what the <video> element holds, and what the
 * component says is on screen.
 *
 * jsdom rather than the suite's node environment, for the reason set out in
 * useFacebookStreamHealth.test.tsx: these behaviours ARE effects.
 */

interface FakePlayer {
  src?: string;
  media?: HTMLVideoElement;
  destroyed: boolean;
  emit(event: string, data?: unknown): void;
}

const hls = vi.hoisted(() => ({
  supported: true,
  players: [] as FakePlayer[],
}));

vi.mock("hls.js", () => {
  type Handler = (event: string, data: unknown) => void;
  class FakeHls {
    static Events = { MANIFEST_PARSED: "manifestParsed", ERROR: "hlsError" };
    static isSupported() {
      return hls.supported;
    }
    src?: string;
    media?: HTMLVideoElement;
    destroyed = false;
    handlers: Record<string, Handler[]> = {};
    constructor() {
      hls.players.push(this as unknown as FakePlayer);
    }
    loadSource(src: string) {
      this.src = src;
    }
    attachMedia(media: HTMLVideoElement) {
      this.media = media;
    }
    on(event: string, fn: Handler) {
      (this.handlers[event] ??= []).push(fn);
    }
    emit(event: string, data?: unknown) {
      for (const fn of this.handlers[event] ?? []) fn(event, data);
    }
    destroy() {
      this.destroyed = true;
    }
  }
  return { default: FakeHls };
});

/* jsdom implements none of the media element's verbs, and the component's whole
 * teardown is three of them. Stubbed rather than skipped, so the calls are
 * observable. */
let playResult: () => Promise<void>;
let loads: HTMLVideoElement[];

beforeEach(() => {
  hls.supported = true;
  hls.players = [];
  playResult = () => Promise.resolve();
  loads = [];
  vi.spyOn(HTMLMediaElement.prototype, "play").mockImplementation(function (
    this: HTMLVideoElement,
  ) {
    return playResult();
  });
  vi.spyOn(HTMLMediaElement.prototype, "pause").mockImplementation(() => {});
  vi.spyOn(HTMLMediaElement.prototype, "load").mockImplementation(function (
    this: HTMLVideoElement,
  ) {
    loads.push(this);
  });
  vi.spyOn(HTMLMediaElement.prototype, "canPlayType").mockReturnValue("");
});

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  vi.useRealTimers();
});

const video = (root: HTMLElement) =>
  root.querySelector("video") as HTMLVideoElement;

/** The stream arrives: hls.js parses a manifest and playback begins. */
const streamArrives = async (player = hls.players.at(-1)!) => {
  await act(async () => {
    player.emit("manifestParsed");
    await Promise.resolve();
  });
};

const waiting = () => screen.queryByText("Waiting for a stream…");

describe("PreviewPlayer once a stream is playing", () => {
  it("plays the named programme, not the default alias", async () => {
    render(<PreviewPlayer active sourceId={12} outputLive />);
    expect(
      hls.players.at(-1)!.src,
      "the tile loaded a playlist that is not its own source's",
    ).toBe("/hls/12/index.m3u8");
  });

  it("keeps the unqualified alias for a caller with no source in hand", () => {
    render(<PreviewPlayer active outputLive />);
    expect(hls.players.at(-1)!.src).toBe("/hls/index.m3u8");
  });

  it("uncovers the picture and offers audio once playback starts", async () => {
    render(<PreviewPlayer active outputLive />);
    expect(waiting(), "the overlay was already gone before anything played")
      .toBeTruthy();

    await streamArrives();

    expect(waiting(), "the overlay stayed over a playing stream").toBeNull();
    expect(screen.getByLabelText("Unmute preview")).toBeTruthy();
  });

  it("lets the operator unmute what is playing", async () => {
    render(<PreviewPlayer active outputLive />);
    await streamArrives();

    await act(async () => {
      screen.getByLabelText("Unmute preview").click();
    });
    expect(screen.getByLabelText("Mute preview")).toBeTruthy();

    await act(async () => {
      screen.getByLabelText("Mute preview").click();
    });
    expect(screen.getByLabelText("Unmute preview")).toBeTruthy();
  });

  it("keeps the overlay up when the browser refuses to autoplay", async () => {
    // A blocked play() must not read as a playing stream, or the operator is
    // looking at a black box with no explanation and a mute button.
    playResult = () => Promise.reject(new Error("blocked"));
    render(<PreviewPlayer active outputLive />);
    await streamArrives();

    expect(waiting()).toBeTruthy();
    expect(screen.queryByLabelText("Unmute preview")).toBeNull();
  });
});

describe("PreviewPlayer when the stream ends", () => {
  it("covers the last frame again after a fatal error", async () => {
    render(<PreviewPlayer active outputLive />);
    await streamArrives();
    const player = hls.players.at(-1)!;

    await act(async () => {
      player.emit("hlsError", { fatal: true });
    });

    expect(
      waiting(),
      "the stream died and nothing was drawn over its last decoded frame",
    ).toBeTruthy();
    expect(screen.queryByLabelText("Unmute preview")).toBeNull();
    expect(player.destroyed).toBe(true);
  });

  it("rides out a non-fatal error without blanking the picture", async () => {
    // hls.js reports recoverable trouble constantly on a live edge. Blanking on
    // each one would flicker the tile for the length of a stream.
    render(<PreviewPlayer active outputLive />);
    await streamArrives();

    await act(async () => {
      hls.players.at(-1)!.emit("hlsError", { fatal: false });
    });

    expect(waiting()).toBeNull();
  });

  it("keeps looking, because no playlist yet just means nobody is streaming", async () => {
    vi.useFakeTimers();
    render(<PreviewPlayer active outputLive />);
    act(() => {
      hls.players.at(-1)!.emit("hlsError", { fatal: true });
    });
    expect(hls.players).toHaveLength(1);

    await act(async () => {
      vi.advanceTimersByTime(4000);
    });
    expect(
      hls.players.length,
      "the player gave up, so a tile whose encoder starts later stays dark " +
        "until the page is reloaded",
    ).toBe(2);
  });

  it("stops retrying once the tile is gone", async () => {
    vi.useFakeTimers();
    const { unmount } = render(<PreviewPlayer active outputLive />);
    act(() => {
      hls.players.at(-1)!.emit("hlsError", { fatal: true });
    });
    unmount();

    await act(async () => {
      vi.advanceTimersByTime(20000);
    });
    expect(
      hls.players,
      "an unmounted tile kept reconnecting in the background",
    ).toHaveLength(1);
  });
});

describe("PreviewPlayer teardown", () => {
  it("clears the <video>, not only the player", async () => {
    const { container, unmount } = render(<PreviewPlayer active outputLive />);
    const el = video(container);
    el.setAttribute("src", "/hls/index.m3u8");
    await streamArrives();

    unmount();

    expect(hls.players.at(-1)!.destroyed).toBe(true);
    expect(
      el.getAttribute("src"),
      "destroying the Hls instance leaves the <video> holding its last " +
        "decoded frame",
    ).toBeNull();
    expect(
      loads,
      "removing the src attribute without load() does not release the frame " +
        "the element is already showing",
    ).toContain(el);
  });

  it("does not show the previous programme under the new one's name", async () => {
    // The reported bug, in the form the grid makes easy to hit: the tile is
    // re-pointed at another source and the old picture stays up until a segment
    // of the new one arrives.
    const { container, rerender } = render(
      <PreviewPlayer active sourceId={1} outputLive />,
    );
    const el = video(container);
    // SEEDED, or the src assertion below proves nothing. hls.js feeds the
    // element through MediaSource and never writes the attribute, so on this
    // path it is null whether the effect clears it or not -- an assertion that
    // stays green with the whole teardown deleted. The native path (Safari,
    // iOS WebViews) does set it, and that is the case this stands in for.
    el.setAttribute("src", "/hls/1/index.m3u8");
    await streamArrives();
    expect(waiting()).toBeNull();

    rerender(<PreviewPlayer active sourceId={2} outputLive />);

    expect(
      waiting(),
      "source 1's last frame was left uncovered under source 2's tile",
    ).toBeTruthy();
    expect(
      el.getAttribute("src"),
      "source 1's playlist was left on the element under source 2's tile",
    ).toBeNull();
    expect(loads, "the element was not reloaded, so it keeps the old frame")
      .toContain(el);
    expect(hls.players.at(-1)!.src).toBe("/hls/2/index.m3u8");
    expect(hls.players).toHaveLength(2);
  });
});

describe("PreviewPlayer on a browser without Media Source Extensions", () => {
  beforeEach(() => {
    hls.supported = false;
  });

  it("hands the playlist to Safari, which plays HLS itself", async () => {
    vi.mocked(HTMLMediaElement.prototype.canPlayType).mockReturnValue("maybe");
    const { container } = render(
      <PreviewPlayer active sourceId={5} outputLive />,
    );

    expect(hls.players, "hls.js was loaded where it is not wanted").toHaveLength(
      0,
    );
    expect(video(container).getAttribute("src")).toBe("/hls/5/index.m3u8");
    await act(async () => {
      await Promise.resolve();
    });
    expect(waiting(), "Safari played but the overlay never lifted").toBeNull();
  });

  it("keeps the overlay up when Safari refuses to autoplay either", async () => {
    vi.mocked(HTMLMediaElement.prototype.canPlayType).mockReturnValue("maybe");
    playResult = () => Promise.reject(new Error("blocked"));
    render(<PreviewPlayer active sourceId={5} outputLive />);

    await act(async () => {
      await Promise.resolve();
    });
    expect(waiting()).toBeTruthy();
    expect(screen.queryByLabelText("Unmute preview")).toBeNull();
  });

  it("says it is waiting on a browser that can play neither", () => {
    // canPlayType stays "" -- no MSE, no native HLS. Nothing to do but say so.
    const { container } = render(<PreviewPlayer active outputLive />);
    expect(hls.players).toHaveLength(0);
    expect(video(container).hasAttribute("src")).toBe(false);
    expect(waiting()).toBeTruthy();
  });
});

describe("PreviewPlayer when the operator has turned the preview off", () => {
  it("attaches nothing at all", () => {
    // A tile that connected anyway would cost an encoder process per source for
    // a picture nobody asked to see.
    render(<PreviewPlayer active={false} outputLive />);
    expect(hls.players).toHaveLength(0);
    expect(screen.getByText("Preview is disabled in Settings")).toBeTruthy();
  });
});
