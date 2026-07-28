import type { ConsoleMessage, Page } from "@playwright/test";

/** Errors a healthy install produces anyway, and which would otherwise make
 *  every navigation assertion fail for the wrong reason.
 *
 *  Kept as an explicit list rather than a loose "ignore 404s", because the
 *  point of watching the console is to catch the errors nobody expected. A
 *  broad filter turns that into no check at all. */
const EXPECTED = [
  // The player asks for the HLS playlist before anything is streaming. With no
  // ingest there is nothing to serve, and 404 is the correct answer.
  /\/hls\/[^\s]*\.m3u8/,
  // Chrome logs this for any <video> whose source 404s, which is the same
  // situation as above seen from the element rather than the network.
  /Failed to load because no supported source was found/,
];

/** Collects genuine console errors for the lifetime of a page.
 *
 *  A UI change that throws in a component still renders the rest of the page,
 *  so "the heading is present" can pass while the panel below it is broken.
 *  Watching the console is what closes that gap. */
export function watchConsole(page: Page): { errors: string[] } {
  const errors: string[] = [];
  page.on("console", (msg: ConsoleMessage) => {
    if (msg.type() !== "error") return;
    // A failed resource load reports only "Failed to load resource: ... 404",
    // with no URL in the text -- the URL is in location(). Matching on the text
    // alone would mean either missing every expected 404 or ignoring all of
    // them, and ignoring all of them is not a check.
    const text = msg.text();
    const url = msg.location()?.url ?? "";
    const subject = `${text} ${url}`;
    if (EXPECTED.some((re) => re.test(subject))) return;
    errors.push(url ? `${text} (${url})` : text);
  });
  page.on("pageerror", (err) => errors.push(`uncaught: ${err.message}`));
  return { errors };
}
