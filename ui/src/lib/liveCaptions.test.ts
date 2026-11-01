// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";
import { renderHook } from "@testing-library/react";

import { mediaTimeOf, parseLiveVTT, parseVTTTime, useLiveCaptionTrack } from "./liveCaptions";

// The public player's live caption track (#8). The server's sidecar names its
// epoch and places each cue when its words were spoken; the player maps that
// wall-clock time onto the video through the playlist's program date-time.

const sidecar =
  "WEBVTT\nX-POLYEMESIS-EPOCH-MS: 1800000000000\n\n" +
  "1\n00:00:10.000 --> 00:00:12.000\nAna: hello &amp; welcome\n\n" +
  "2\nnot a timing line\n\n" +
  "3\n00:01:00.500 --> 00:01:02.000\nsecond\n\n";

describe("parseLiveVTT", () => {
  // MUTATION: the epoch is not added. Observed: FAIL, the first cue started at 10000.
  it("places each cue on the wall clock from the epoch the file names, and skips a malformed block", () => {
    expect(parseLiveVTT(sidecar)).toEqual([
      { startMs: 1_800_000_010_000, endMs: 1_800_000_012_000, text: "Ana: hello &amp; welcome" },
      { startMs: 1_800_000_060_500, endMs: 1_800_000_062_000, text: "second" },
    ]);
  });

  it("yields nothing for a file with no epoch, rather than cues in the wrong place", () => {
    expect(parseLiveVTT("WEBVTT\n\n1\n00:00:01.000 --> 00:00:02.000\nx\n")).toEqual([]);
    expect(parseLiveVTT("not vtt")).toEqual([]);
  });

  it("reads hour and minute forms of a timestamp, and refuses anything else", () => {
    expect(parseVTTTime("01:02:03.004")).toBe(3_723_004);
    expect(parseVTTTime("02:03.004")).toBe(123_004);
    expect(parseVTTTime("2:03")).toBeNull();
  });
});

describe("mediaTimeOf", () => {
  it("maps a wall-clock moment onto the video through one anchor", () => {
    // The fragment at media time 100 s was shown at the epoch + 5 s.
    const anchor = { wallMs: 1_800_000_005_000, mediaTime: 100 };
    expect(mediaTimeOf(1_800_000_010_000, anchor)).toBe(105);
    expect(mediaTimeOf(1_800_000_004_000, anchor)).toBe(99);
  });
});

class FakeCue {
  startTime: number;
  endTime: number;
  text: string;
  constructor(startTime: number, endTime: number, text: string) {
    this.startTime = startTime;
    this.endTime = endTime;
    this.text = text;
  }
}

describe("useLiveCaptionTrack", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
    vi.useRealTimers();
  });

  // MUTATION: cues added without the seen-set check. Observed: FAIL, the second
  // poll added both cues again (4 cues).
  it("adds each cue once, at its place on the video, as the file grows", async () => {
    vi.useFakeTimers();
    vi.stubGlobal("VTTCue", FakeCue);
    const bodies = [sidecar, sidecar + "4\n00:02:00.000 --> 00:02:01.000\nthird\n\n"];
    const fetchMock = vi.fn(async () => ({ ok: true, text: async () => bodies.shift() ?? "" }));
    vi.stubGlobal("fetch", fetchMock);
    const cues: FakeCue[] = [];
    const track = { mode: "hidden", addCue: (c: FakeCue) => cues.push(c) };
    const video = { addTextTrack: vi.fn(() => track) } as unknown as HTMLVideoElement;
    const anchor = () => ({ wallMs: 1_800_000_000_000, mediaTime: 50 });

    const { unmount } = renderHook(() => useLiveCaptionTrack(video, "/playout/captions.vtt", "Captions", anchor));
    await vi.advanceTimersByTimeAsync(0);
    expect(track.mode).toBe("showing");
    expect(cues.map((c) => [c.startTime, c.text])).toEqual([
      [60, "Ana: hello &amp; welcome"],
      [110.5, "second"],
    ]);
    await vi.advanceTimersByTimeAsync(4000);
    expect(cues.map((c) => c.text)).toEqual(["Ana: hello &amp; welcome", "second", "third"]);
    unmount();
    expect(track.mode).toBe("disabled");
  });

  it("places nothing until the player knows the wall-clock time of its playhead", async () => {
    vi.useFakeTimers();
    vi.stubGlobal("VTTCue", FakeCue);
    vi.stubGlobal("fetch", vi.fn(async () => ({ ok: true, text: async () => sidecar })));
    const cues: FakeCue[] = [];
    const track = { mode: "hidden", addCue: (c: FakeCue) => cues.push(c) };
    const video = { addTextTrack: () => track } as unknown as HTMLVideoElement;
    let clock: { wallMs: number; mediaTime: number } | null = null;
    const anchor = () => clock;
    const { unmount } = renderHook(() => useLiveCaptionTrack(video, "/playout/captions.vtt", "Captions", anchor));
    await vi.advanceTimersByTimeAsync(0);
    expect(cues).toHaveLength(0);
    clock = { wallMs: 1_800_000_000_000, mediaTime: 0 };
    await vi.advanceTimersByTimeAsync(4000);
    expect(cues).toHaveLength(2);
    unmount();
  });

  it("keeps polling, more slowly, while the sidecar is missing", async () => {
    vi.useFakeTimers();
    vi.stubGlobal("VTTCue", FakeCue);
    const fetchMock = vi.fn(async () => ({ ok: false, text: async () => "" }));
    vi.stubGlobal("fetch", fetchMock);
    const video = { addTextTrack: () => ({ mode: "hidden", addCue: () => {} }) } as unknown as HTMLVideoElement;
    const anchor = () => null;
    const { unmount } = renderHook(() => useLiveCaptionTrack(video, "/playout/captions.vtt", "Captions", anchor));
    await vi.advanceTimersByTimeAsync(0);
    expect(fetchMock).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(4000);
    expect(fetchMock).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(11000);
    expect(fetchMock).toHaveBeenCalledTimes(2);
    unmount();
  });

  // A long show's sidecar grows to megabytes. After the first read each poll asks only for the bytes appended since,
  // so viewers do not refetch and re-parse the whole file every four seconds.
  // MUTATION: drop the Range header. Observed: FAIL, the second request carried no Range.
  it("asks only for the appended bytes after the first read, and places what they add", async () => {
    vi.useFakeTimers();
    vi.stubGlobal("VTTCue", FakeCue);
    const appended = "4\n00:02:00.000 --> 00:02:01.000\nthird\n\n";
    const fetchMock = vi.fn(async (_url: string, init?: RequestInit) => {
      const range = (init?.headers as Record<string, string> | undefined)?.Range;
      if (range) return { ok: true, status: 206, text: async () => appended, headers: new Headers() };
      return { ok: true, status: 200, text: async () => sidecar, headers: new Headers() };
    });
    vi.stubGlobal("fetch", fetchMock);
    const cues: FakeCue[] = [];
    const track = { mode: "hidden", addCue: (c: FakeCue) => cues.push(c) };
    const video = { addTextTrack: () => track } as unknown as HTMLVideoElement;
    const anchor = () => ({ wallMs: 1_800_000_000_000, mediaTime: 50 });

    const { unmount } = renderHook(() => useLiveCaptionTrack(video, "/playout/captions.vtt", "Captions", anchor));
    await vi.advanceTimersByTimeAsync(0);
    await vi.advanceTimersByTimeAsync(4000);
    const second = fetchMock.mock.calls[1][1]?.headers as Record<string, string>;
    expect(second.Range).toBe(`bytes=${new TextEncoder().encode(sidecar).length}-`);
    expect(cues.map((c) => c.text)).toEqual(["Ana: hello &amp; welcome", "second", "third"]);
    unmount();
  });
});
