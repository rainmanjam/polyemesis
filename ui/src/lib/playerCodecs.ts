import type { PlayoutCodec, PlayoutPublicVariant } from "./types";

/** Which video codec families the browser can decode.
 *
 *  Video on the public origin is COPIED from the ingest or a rendition, so an
 *  HEVC encoder or an AV1 ingest reaches the viewer as HEVC or AV1. A browser
 *  that cannot decode the rung it lands on shows a black frame and an error;
 *  this is how the page knows to stay on an H.264 rung instead. */
export type CodecSupport = Record<PlayoutCodec, boolean>;

/** The two browser APIs that answer the question, injected so tests can say
 *  what a given browser would answer. */
export interface CodecEnv {
  /** MediaSource.isTypeSupported, absent where there is no MSE (iOS Safari
   *  before 17.1 has none, and plays HLS natively instead). */
  isTypeSupported?: (type: string) => boolean;
  /** HTMLVideoElement.canPlayType. */
  canPlayType: (type: string) => string;
}

/** Representative strings for each codec FAMILY. The master playlist's CODECS
 *  is now the stream's exact profile and level, or absent (see
 *  internal/playout/codecs.go); this check answers the coarser question the
 *  fallback needs -- can this browser decode the family at all. */
const VIDEO_CODEC: Record<PlayoutCodec, string> = {
  h264: "avc1.640028",
  hevc: "hvc1.1.6.L120.90",
  av1: "av01.0.08M.08",
};

/** The MIME type a support check asks about: fMP4, which is what hls.js
 *  transmuxes MPEG-TS into, carrying the codec and the AAC audio every rung has. */
export function codecMime(codec: PlayoutCodec): string {
  return `video/mp4; codecs="${VIDEO_CODEC[codec]},mp4a.40.2"`;
}

function safely(f: () => boolean): boolean {
  try {
    return f();
  } catch {
    return false;
  }
}

/** What this browser can decode. Either API saying yes counts: Safari answers
 *  canPlayType for native HLS and MSE for hls.js, and each is right for the
 *  path that asks it. */
export function detectCodecSupport(env: CodecEnv): CodecSupport {
  const ask = (codec: PlayoutCodec) => {
    const mime = codecMime(codec);
    return (
      safely(() => !!env.isTypeSupported?.(mime)) || safely(() => env.canPlayType(mime) !== "")
    );
  };
  return { h264: ask("h264"), hevc: ask("hevc"), av1: ask("av1") };
}

/** The real browser's environment. */
export function browserCodecEnv(video?: HTMLVideoElement | null): CodecEnv {
  const ms = (globalThis as { MediaSource?: { isTypeSupported?: (t: string) => boolean } })
    .MediaSource;
  const el = video ?? (typeof document !== "undefined" ? document.createElement("video") : null);
  return {
    isTypeSupported: ms?.isTypeSupported ? (t) => ms.isTypeSupported!(t) : undefined,
    canPlayType: (t) => el?.canPlayType(t) ?? "",
  };
}

export interface PlayableLadder {
  /** The rungs this browser should be offered. */
  playable: PlayoutPublicVariant[];
  /** The rungs left out because their codec is not decodable here. */
  dropped: PlayoutPublicVariant[];
  /** True when rungs were dropped and the page fell back to H.264. */
  fellBack: boolean;
}

/** Which rungs to offer. A rung whose codec is unknown is kept: guessing it
 *  away could empty a ladder that plays perfectly well.
 *
 *  It never returns an empty ladder when there is an H.264 rung to fall back
 *  to, even if the browser claims it cannot decode H.264 either -- a browser
 *  saying no to everything is usually an embedded webview misreporting, and an
 *  empty ladder plays nothing at all. With no H.264 to fall back to, the ladder
 *  is left as it is and the player is allowed to try. */
export function playableVariants(
  variants: PlayoutPublicVariant[],
  support: CodecSupport,
): PlayableLadder {
  const ok = (v: PlayoutPublicVariant) => !v.codec || support[v.codec];
  const playable = variants.filter(ok);
  const dropped = variants.filter((v) => !ok(v));
  if (dropped.length === 0) return { playable, dropped, fellBack: false };
  if (playable.length > 0) return { playable, dropped, fellBack: true };
  const h264 = variants.filter((v) => v.codec === "h264");
  if (h264.length > 0) {
    return { playable: h264, dropped: variants.filter((v) => v.codec !== "h264"), fellBack: true };
  }
  return { playable: variants, dropped: [], fellBack: false };
}

/** The URL native HLS should load. Native playback cannot be told to skip a
 *  rung, so when the master holds one this browser cannot decode, the page
 *  bypasses the master and loads the tallest decodable rung's own playlist. */
export function nativeSource(
  master: string,
  variants: PlayoutPublicVariant[],
  support: CodecSupport,
): string {
  const ladder = playableVariants(variants, support);
  if (!ladder.fellBack || ladder.playable.length === 0) return master;
  const best = [...ladder.playable].sort((a, b) => (b.height ?? 0) - (a.height ?? 0))[0];
  return best.playlist;
}

/** Which path plays the segments: hls.js over MediaSource, or the browser's
 *  own HLS (Safari, set-top boxes). */
export type PlaybackPath = "mse" | "native";

/** What the browser can decode, narrowed to what it can decode OUT OF THE
 *  SEGMENTS THIS SERVER WRITES. The packager writes MPEG-TS, and a decoder is
 *  not enough:
 *
 *   - AV1 in MPEG-TS has no demuxer anywhere a viewer is -- hls.js has none,
 *     and no native HLS player accepts it -- so an AV1 rung is never playable.
 *   - HEVC in HLS is fMP4-only on native players (Apple's HLS spec), so native
 *     HLS cannot play an HEVC rung from TS even on a device that decodes HEVC.
 *     hls.js's full build does demux HEVC from MPEG-TS, so through MSE it is
 *     decided by the decoder alone. */
export function forSegments(support: CodecSupport, path: PlaybackPath): CodecSupport {
  return {
    h264: support.h264,
    hevc: path === "mse" && support.hevc,
    av1: false,
  };
}
