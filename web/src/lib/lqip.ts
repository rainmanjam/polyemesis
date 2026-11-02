import { getImage } from "astro:assets";
import type { ImageMetadata } from "astro";

/* A LOW-QUALITY PLACEHOLDER FOR A LAZY SCREENSHOT (2026-11 design review).
 *
 * A lazy <img> with width and height reserves its box, so nothing shifts, but
 * until the file arrives that box is an empty bordered frame. On the features
 * page that was twelve empty frames for a reader scrolling faster than the
 * network. This paints a 32px-wide rendering of the same capture underneath,
 * scaled to the box, so the frame shows the screen's layout and colour at once
 * and sharpens when the real file decodes.
 *
 * A same-origin file, NOT a data: URI inlined into the page. The CSP allows
 * both, but a base64 blob per screenshot would be a high-entropy literal in
 * every built page; a ~300-byte webp fetched from /_astro/ is not. The browser
 * fetches it as soon as the element is rendered, which is the point: it has to
 * arrive before the lazy image it stands in for. */
export async function lqipStyle(src: ImageMetadata): Promise<string> {
  const tiny = await getImage({ src, format: "webp", width: 32, quality: 50 });
  return `background-image:url(${tiny.src});background-size:cover;background-position:top left`;
}
