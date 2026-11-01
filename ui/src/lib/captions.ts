import type { CaptionMode } from "@/lib/types";

/** The caption routes each platform takes, as the console shows them.
 *
 *  This is a MIRROR of the decision table in internal/captions/platform.go. The
 *  server is the authority: it refuses a mode the platform cannot carry. The
 *  mirror exists so the picker can offer only routes that will be accepted, and
 *  internal/captions/platform_ui_mirror_test.go fails if the two disagree.
 *
 *  `status` is how far the route is verified, not whether it is built.
 *  "provisional" routes need a live check before they are relied on. "skipped"
 *  platforms offer no route at all. */
export type CaptionRouteStatus = "go" | "provisional" | "skipped";

export const CAPTION_ROUTES: Record<string, { status: CaptionRouteStatus; modes: CaptionMode[] }> = {
  twitch: { status: "go", modes: ["sei"] },
  youtube: { status: "provisional", modes: ["youtube_post", "sei"] },
  facebook: { status: "provisional", modes: ["facebook_sei"] },
  kick: { status: "skipped", modes: [] },
};

/** The modes an operator can pick for a platform, "off" first. A platform not in
 *  the table is off only: a new platform is never captioned by accident. */
export function captionModesFor(platform: string): CaptionMode[] {
  const route = CAPTION_ROUTES[platform];
  return route ? ["off", ...route.modes] : ["off"];
}

/** Whether captions are offered at all for a platform. Kick is false. */
export function captionsOffered(platform: string): boolean {
  return (CAPTION_ROUTES[platform]?.modes.length ?? 0) > 0;
}

/** The YouTube HTTP POST route needs a 30 s or 60 s broadcast delay. */
export function captionModeNeedsDelay(mode: CaptionMode): boolean {
  return mode === "youtube_post";
}

/** How long polyemesis itself holds a destination's output back, mirroring the
 *  server's db.OutputDelaySeconds. A YouTube destination that posts captions by
 *  HTTP POST keeps its 30 s or 60 s delay, but YouTube applies it (the
 *  broadcast's stream delay), so its output is not held back here as well and
 *  viewers are the delay behind, not twice it. Every other destination is held
 *  back by its delay. */
export function outputDelaySeconds(platform: string, mode: CaptionMode, delaySeconds: number): number {
  if (platform === "youtube" && mode === "youtube_post") return 0;
  return delaySeconds;
}

/** The caption mode a save should send for the dialog as it stands. A mode the
 *  picker cannot show (the kind was switched away from RTMP, or the platform
 *  changed to one that does not take it) is sent as "off". Without this, a
 *  stored `sei` survives a switch to Kick and the server refuses a save about a
 *  control that is no longer on screen. */
export function captionModeToSend(kind: string, platform: string, mode: CaptionMode): CaptionMode {
  if (kind !== "rtmp") return "off";
  return captionModesFor(platform).includes(mode) ? mode : "off";
}

/** Splits a list the operator typed, one word per line or comma-separated, into
 *  its entries. The server trims, de-duplicates and refuses anything that is not
 *  one word, so this only splits. */
export function captionWords(text: string): string[] {
  return text
    .split(/[\n,]/)
    .map((w) => w.trim())
    .filter((w) => w !== "");
}
