import { describe, expect, it } from "vitest";
import {
  type CodecEnv,
  codecMime,
  detectCodecSupport,
  forSegments,
  nativeSource,
  playableVariants,
} from "./playerCodecs";
import type { PlayoutPublicVariant } from "./types";

const h264: PlayoutPublicVariant = { name: "360p", playlist: "/playout/360p/index.m3u8", height: 360, codec: "h264" };
const h264hd: PlayoutPublicVariant = { name: "720p", playlist: "/playout/720p/index.m3u8", height: 720, codec: "h264" };
const hevc: PlayoutPublicVariant = { name: "4k", playlist: "/playout/4k/index.m3u8", height: 2160, codec: "hevc" };
const av1: PlayoutPublicVariant = { name: "av1", playlist: "/playout/av1/index.m3u8", height: 1080, codec: "av1" };
const unknown: PlayoutPublicVariant = { name: "source", playlist: "/playout/source/index.m3u8" };

/** An environment that decodes exactly the listed codec families. */
function env(families: string[], via: "mse" | "native" | "both" = "mse"): CodecEnv {
  const ok = (type: string) => families.some((f) => type.includes(f));
  return {
    isTypeSupported: via === "native" ? undefined : ok,
    canPlayType: via === "mse" ? () => "" : (type) => (ok(type) ? "probably" : ""),
  };
}

describe("detectCodecSupport", () => {
  it("asks MediaSource with the same codec strings the master playlist advertises", () => {
    const asked: string[] = [];
    detectCodecSupport({ isTypeSupported: (t) => (asked.push(t), true), canPlayType: () => "" });
    expect(asked).toContain(codecMime("h264"));
    expect(asked).toContain(codecMime("hevc"));
    expect(asked).toContain(codecMime("av1"));
    expect(codecMime("hevc")).toContain("hvc1.1.6.L120.90");
    expect(codecMime("h264")).toContain("mp4a.40.2");
  });

  it("falls back to canPlayType when MediaSource is absent", () => {
    expect(detectCodecSupport(env(["avc1", "hvc1"], "native"))).toEqual({ h264: true, hevc: true, av1: false });
  });

  it("treats either API saying yes as support", () => {
    const e: CodecEnv = { isTypeSupported: () => false, canPlayType: (t) => (t.includes("av01") ? "maybe" : "") };
    expect(detectCodecSupport(e).av1).toBe(true);
  });

  it("survives an API that throws", () => {
    const e: CodecEnv = {
      isTypeSupported: () => {
        throw new Error("boom");
      },
      canPlayType: () => "",
    };
    expect(detectCodecSupport(e)).toEqual({ h264: false, hevc: false, av1: false });
  });
});

describe("playableVariants", () => {
  it("keeps every rung when the browser decodes them all", () => {
    const r = playableVariants([h264, hevc, av1], { h264: true, hevc: true, av1: true });
    expect(r.playable.map((v) => v.name)).toEqual(["360p", "4k", "av1"]);
    expect(r.dropped).toEqual([]);
    expect(r.fellBack).toBe(false);
  });

  it("drops HEVC and AV1 rungs the browser cannot decode and keeps H.264", () => {
    const r = playableVariants([h264, hevc, h264hd, av1], { h264: true, hevc: false, av1: false });
    expect(r.playable.map((v) => v.name)).toEqual(["360p", "720p"]);
    expect(r.dropped.map((v) => v.name)).toEqual(["4k", "av1"]);
    expect(r.fellBack).toBe(true);
  });

  it("keeps a rung whose codec is unknown rather than guessing it away", () => {
    const r = playableVariants([unknown, hevc], { h264: true, hevc: false, av1: false });
    expect(r.playable.map((v) => v.name)).toEqual(["source"]);
  });

  it("never empties the ladder: with nothing decodable it keeps the H.264 rungs", () => {
    // A browser that answers no to everything is usually lying (an embedded
    // webview), and an empty ladder plays nothing at all.
    const r = playableVariants([hevc, h264], { h264: false, hevc: false, av1: false });
    expect(r.playable.map((v) => v.name)).toEqual(["360p"]);
    expect(r.fellBack).toBe(true);
  });

  it("with only unsupported rungs and no H.264, keeps them all and reports no fallback", () => {
    const r = playableVariants([hevc, av1], { h264: true, hevc: false, av1: false });
    expect(r.playable.map((v) => v.name)).toEqual(["4k", "av1"]);
    expect(r.fellBack).toBe(false);
  });
});

describe("nativeSource", () => {
  const master = "/playout/master.m3u8";

  it("plays the master when every rung is decodable", () => {
    expect(nativeSource(master, [h264, hevc], { h264: true, hevc: true, av1: false })).toBe(master);
  });

  it("plays the best H.264 rung directly when the master holds one it cannot decode", () => {
    // Native HLS cannot be told to skip a rung, so the fallback is to bypass
    // the master and load the highest decodable rung's own playlist.
    expect(nativeSource(master, [h264, hevc, h264hd], { h264: true, hevc: false, av1: false })).toBe(
      "/playout/720p/index.m3u8",
    );
  });

  it("plays the master when there is no ladder to choose from", () => {
    expect(nativeSource(master, [], { h264: true, hevc: false, av1: false })).toBe(master);
  });
});

describe("forSegments (#6: the packager writes MPEG-TS)", () => {
  const all = { h264: true, hevc: true, av1: true };

  it("never offers AV1: neither hls.js nor any native player demuxes AV1 from MPEG-TS", () => {
    expect(forSegments(all, "mse").av1).toBe(false);
    expect(forSegments(all, "native").av1).toBe(false);
  });

  it("refuses HEVC on native HLS, which wants HEVC in fMP4, and keeps it for hls.js, which demuxes it", () => {
    expect(forSegments(all, "native").hevc).toBe(false);
    expect(forSegments(all, "mse").hevc).toBe(true);
  });

  it("never claims more than the browser does", () => {
    expect(forSegments({ h264: false, hevc: false, av1: false }, "mse")).toEqual({ h264: false, hevc: false, av1: false });
    expect(forSegments(all, "mse").h264).toBe(true);
  });

  it("sends native HLS to the H.264 rung when the ladder holds HEVC, even on a browser that decodes HEVC", () => {
    const master = "/playout/master.m3u8";
    expect(nativeSource(master, [h264, hevc], forSegments(all, "native"))).toBe(h264.playlist);
  });
});
