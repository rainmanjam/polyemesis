/* The homepage video tour. The file and its chapters come from scripts/tour:
 * publish.sh writes /media/tour.mp4 and its poster, chapters.json holds the
 * chapters, and `publish.sh --check` verifies them against these numbers.
 *
 * The video is silent (the tour carries captions instead), 1920x1080 H.264.
 *
 * @typedef {{ title: string, start: number }} Chapter
 * @type {{ src: string, poster: string, width: number, height: number,
 *          duration: number, chapters: Chapter[] }} */
export const TOUR = {
  src: "/media/tour.mp4",
  poster: "/media/tour-poster.jpg",
  width: 1920,
  height: 1080,
  duration: 98.2,
  chapters: [
    { title: "One stream in", start: 0 },
    { title: "Splitting the audio", start: 22.2 },
    { title: "Going live everywhere", start: 59.2 },
  ],
};

/** The one line both the homepage and /tour print under the video, so they cannot drift.
 * It is how docs/media/README.md describes the demo rig. */
export const TOUR_DEMO_NOTE =
  "The tour was filmed on a demo install: its five destinations are labelled as YouTube channels and publish to a local receiver, not to YouTube.";

/** Seconds as m:ss, rounded down: 58.6 -> "0:58". @param {number} seconds */
export function clock(seconds) {
  const whole = Math.floor(seconds);
  return `${Math.floor(whole / 60)}:${String(whole % 60).padStart(2, "0")}`;
}

/** Seconds as an ISO 8601 duration for <time datetime>, rounded down: 22.2 -> "PT22S", 65 -> "PT1M5S", 3600 -> "PT1H".
 * @param {number} seconds */
export function isoDuration(seconds) {
  const whole = Math.floor(seconds);
  const h = Math.floor(whole / 3600);
  const m = Math.floor((whole % 3600) / 60);
  const sec = whole % 60;
  return `PT${h ? `${h}H` : ""}${m ? `${m}M` : ""}${sec || (!h && !m) ? `${sec}S` : ""}`;
}
