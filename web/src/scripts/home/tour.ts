/* The video tour: a muted <video> with chapter buttons.
 *
 * The tour plays by itself only while at least half of it is on screen, the
 * motion gate allows, and the tab is visible; it pauses otherwise. A visitor
 * who pauses it stays paused: scrolling away and back does not restart it until
 * they press play again. A chapter button is the visitor asking, so it seeks and
 * plays whatever the gate or the scroll position says.
 *
 * whileVisible is not used because it reads isIntersecting, which is true at
 * any overlap at all, whatever threshold is set; half visible has to be
 * measured from intersectionRatio. */

import type { MotionGate } from "./motion";

const HALF = 0.5;

export function initTour(root: HTMLElement, gate: MotionGate): () => void {
  const doc = root.ownerDocument;
  const video = root.querySelector<HTMLVideoElement>("video");
  if (!video) return () => {};

  let visible = false;
  let running = false; // the auto-player has started the video and not yet stopped it
  let visitorPaused = false;
  let ourPause = false; // the next "pause" event is ours, not the visitor's

  // play() rejects when the browser refuses it (autoplay policy, a source that
  // will not load). The controls are on screen, so the visitor can still press
  // play; there is nothing to report here.
  const start = () => {
    void video.play()?.catch(() => {});
  };

  const update = () => {
    const should = visible && gate.allowed() && doc.visibilityState === "visible";
    /* Quiet means quiet: a video the visitor started from a chapter button is
     * playing without the auto-player ever having run, and it must still stop
     * when the site's Pause is pressed, the tab is hidden or the tour leaves
     * the screen. */
    if (!should && !video.paused) {
      ourPause = true;
      video.pause();
    }
    if (should === running) return;
    running = should;
    if (should && !visitorPaused) start();
  };

  const io = new IntersectionObserver(
    (entries) => {
      const last = entries.at(-1);
      if (last) visible = last.isIntersecting && last.intersectionRatio >= HALF;
      update();
    },
    { threshold: [0, HALF] },
  );
  io.observe(video);
  const unsub = gate.subscribe(update);

  const onVisibility = () => update();
  const onPause = () => {
    if (ourPause) ourPause = false;
    else visitorPaused = true;
  };
  const onPlay = () => {
    visitorPaused = false;
  };
  doc.addEventListener("visibilitychange", onVisibility);
  video.addEventListener("pause", onPause);
  video.addEventListener("play", onPlay);

  root.classList.add("is-live");
  const seekers = [...root.querySelectorAll<HTMLButtonElement>("[data-seek]")].map((btn) => {
    btn.disabled = false; // rendered disabled by Tour.astro: only a live tour has chapters to press
    const fn = () => {
      video.currentTime = Number(btn.dataset.seek);
      visitorPaused = false;
      start();
    };
    btn.addEventListener("click", fn);
    return [btn, fn] as const;
  });

  return () => {
    io.disconnect();
    unsub();
    doc.removeEventListener("visibilitychange", onVisibility);
    video.removeEventListener("pause", onPause);
    video.removeEventListener("play", onPlay);
    for (const [btn, fn] of seekers) {
      btn.removeEventListener("click", fn);
      btn.disabled = true;
    }
    root.classList.remove("is-live");
    if (running && !video.paused) {
      ourPause = true;
      video.pause();
    }
  };
}
