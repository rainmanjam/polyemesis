import type { ViewerPlatform } from "./config.js";
import { t } from "./i18n.js";
import type { KeyFace } from "./keyFace.js";
import type { ViewersLive } from "./model.js";

// The viewers key and dial (Wave 4 #4): the audience across every platform, or one platform's, from the
// install's analytics sampler.
//
// A COUNT NOBODY REPORTED IS "Not reported", NEVER 0. A hidden YouTube count, a Kick streamer who opted out
// or a read the YouTube quota pacer refused is not an audience of none, and a key showing 0 on a stream
// with viewers would be the false zero the server refuses everywhere else. The total is the server's own
// sum of what did report.

const PLATFORM_NAMES: Record<Exclude<ViewerPlatform, "player">, string> = {
  youtube: "YouTube",
  twitch: "Twitch",
  kick: "Kick",
};

/** The platform's name as the key shows it. The player is a phrase, so it comes from the catalogue. */
export function viewerPlatformName(platform: ViewerPlatform, locale: string): string {
  return platform === "player" ? t(locale, "streamdeck.viewers.player") : PLATFORM_NAMES[platform];
}

/**
 * The count a key or dial shows: the total, or one platform's readings summed. Undefined when nothing
 * that was asked reported -- including a platform no running destination streams to, which was never asked.
 */
export function viewerCount(live: ViewersLive, platform: ViewerPlatform | undefined): number | undefined {
  if (platform === undefined) return live.total;
  let sum = 0;
  let reported = false;
  for (const r of live.readings) {
    if (r.platform !== platform || r.viewers === undefined) continue;
    sum += r.viewers;
    reported = true;
  }
  return reported ? sum : undefined;
}

/** The key face, from the sampler's last answer. Errors are the runtime's to paint. */
export function viewersFace(live: ViewersLive, platform: ViewerPlatform | undefined, locale: string): KeyFace {
  const name = platform === undefined ? undefined : viewerPlatformName(platform, locale);
  if (!live.enabled) return { tone: "unavailable", label: "streamdeck.viewers.off", detail: name };
  if (!live.onAir) return { tone: "idle", label: "streamdeck.viewers.offAir", detail: name };
  const count = viewerCount(live, platform);
  if (count === undefined) return { tone: "warn", label: "streamdeck.viewers.notReported", detail: name };
  const shown = count.toLocaleString(locale);
  // A partial tick's total is the audience minus whoever failed: said so, not shown as the audience.
  if (platform === undefined && live.partial) return { tone: "warn", label: "streamdeck.viewers.partial", detail: shown };
  return {
    tone: "live",
    label: "streamdeck.viewers.total",
    detail: name === undefined ? shown : `${name} ${shown}`,
  };
}

/** The dial's touch strip: the platform (or "Viewers") as the title and the count, or why there is none. */
export function viewersFeedback(
  live: ViewersLive,
  platform: ViewerPlatform | undefined,
  locale: string,
): { title: string; value: string; indicator: number } {
  const title = platform === undefined ? t(locale, "streamdeck.viewers.total") : viewerPlatformName(platform, locale);
  if (!live.enabled) return { title, value: t(locale, "streamdeck.viewers.off"), indicator: 0 };
  if (!live.onAir) return { title, value: t(locale, "streamdeck.viewers.offAir"), indicator: 0 };
  const count = viewerCount(live, platform);
  if (count === undefined) return { title, value: t(locale, "streamdeck.viewers.notReported"), indicator: 0 };
  if (platform === undefined && live.partial) {
    return { title, value: `${t(locale, "streamdeck.viewers.partial")} · ${count.toLocaleString(locale)}`, indicator: 0 };
  }
  return { title, value: count.toLocaleString(locale), indicator: 0 };
}
