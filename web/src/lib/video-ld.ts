/* VideoObject structured data for the tour page, and the two time formats it
 * and the chapter sections share. Every field corresponds to something the
 * page shows: the video and its poster, the title and description, the
 * "Filmed on" release date, the duration and the chapters. There is no
 * embedUrl, interactionStatistic or aggregateRating, because the page has
 * none of those. */
import type { TOUR } from "../data/tour.mjs";

/* One implementation of both formats, shared with the player block
 * (data/tour.mjs), so the bar's "1:38" and the markup's PT1M38S cannot drift. */
export { clock, isoDuration } from "../data/tour.mjs";
import { isoDuration } from "../data/tour.mjs";

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
