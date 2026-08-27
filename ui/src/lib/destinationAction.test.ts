import { describe, expect, it } from "vitest";

import {
  FLIP_GUARD_MS,
  STOP_UNDO_MS,
  actionFor,
  clickIsTrustworthy,
  undoSecondsLeft,
} from "@/lib/destinationAction";

describe("actionFor", () => {
  it("names what the button will do, not what the destination is", () => {
    expect(actionFor(true)).toBe("stop");
    expect(actionFor(false)).toBe("start");
  });
});

describe("clickIsTrustworthy", () => {
  /* THE DEFECT (#506). The button swaps in place on a socket repaint that
   * lands roughly every two seconds, so a click aimed at "Start" can arrive
   * on "Stop" and end a live broadcast. Nothing about it looks wrong
   * afterwards -- the server was asked to stop something that was running. */
  it("refuses a click on an action that has only just appeared", () => {
    expect(clickIsTrustworthy(0)).toBe(false);
    expect(clickIsTrustworthy(FLIP_GUARD_MS - 1)).toBe(false);
  });

  it("honours a click once the button has settled", () => {
    expect(clickIsTrustworthy(FLIP_GUARD_MS)).toBe(true);
    expect(clickIsTrustworthy(60_000)).toBe(true);
  });

  /* THE CONTROL CASE. A guard that refuses everything would satisfy the first
   * test and make the product unusable, so the window has to be shown to be
   * shorter than the repaint interval it defends against -- otherwise every
   * button on a busy card would be permanently disabled. */
  it("closes well inside the socket's repaint interval", () => {
    expect(FLIP_GUARD_MS).toBeLessThan(2_000);
    // And long enough to cover a pointer already in flight, which cannot be
    // redirected in much under 200ms.
    expect(FLIP_GUARD_MS).toBeGreaterThanOrEqual(500);
  });
});

describe("undoSecondsLeft", () => {
  it("rounds up, so the last partial second does not read as none left", () => {
    // A countdown showing 0 beside a working Undo button says the opposite of
    // what is true.
    expect(undoSecondsLeft(1)).toBe(1);
    expect(undoSecondsLeft(999)).toBe(1);
    expect(undoSecondsLeft(1_001)).toBe(2);
    expect(undoSecondsLeft(STOP_UNDO_MS)).toBe(5);
  });

  it("never goes negative", () => {
    expect(undoSecondsLeft(-500)).toBe(0);
    expect(undoSecondsLeft(0)).toBe(0);
  });
});
