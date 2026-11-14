/* VideoObject structured data for the tour page, and the two time formats it
 * and the chapter sections share. Every field corresponds to something the
 * page shows: the video and its poster, the title and description, the
 * "Filmed on" release date, the duration and the chapters. There is no
 * embedUrl, interactionStatistic or aggregateRating, because the page has
 * none of those. */
import type { TOUR } from "../data/tour.mjs";

/** Seconds as an ISO 8601 duration, fractions of a second dropped: 90 -> "PT1M30S", 0 -> "PT0S". */
export function isoDuration(seconds: number): string {
  const whole = Math.floor(seconds);
  const h = Math.floor(whole / 3600);
  const m = Math.floor((whole % 3600) / 60);
  const s = whole % 60;
  if (!h && !m && !s) return "PT0S";
  return `PT${h ? `${h}H` : ""}${m ? `${m}M` : ""}${s ? `${s}S` : ""}`;
}

/** Seconds as m:ss, rounded down: 65 -> "1:05". */
export function clock(seconds: number): string {
  const whole = Math.floor(seconds);
  return `${Math.floor(whole / 60)}:${String(whole % 60).padStart(2, "0")}`;
}

export interface VideoLdInput {
  site: string;
  route: string;
  name: string;
  description: string;
  tour: typeof TOUR;
  uploadDate: string;
}

export function videoLd(i: VideoLdInput): object {
  const { chapters, duration } = i.tour;
  return {
    "@context": "https://schema.org",
    "@type": "VideoObject",
    name: i.name,
    description: i.description,
    thumbnailUrl: [i.site + i.tour.poster],
    uploadDate: i.uploadDate,
    duration: isoDuration(duration),
    contentUrl: i.site + i.tour.src,
    hasPart: chapters.map((c, n) => ({
      "@type": "Clip",
      name: c.title,
      startOffset: c.start,
      endOffset: chapters[n + 1]?.start ?? duration,
      url: `${i.site}${i.route}#chapter-${n + 1}`,
    })),
  };
}
