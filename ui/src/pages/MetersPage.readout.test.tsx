// @vitest-environment jsdom

import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import { MetersPage } from "./MetersPage";
import { LiveDataContext } from "@/hooks/useLiveData";
import type { LiveData } from "@/hooks/useLiveData";

/* THE METERS PAGE IS WHERE AN OPERATOR CHECKS THE MIX, SO THE NUMBERS HAVE TO
 * BE READ AT A GLANCE AND HEARD BY A SCREEN READER.
 *
 * Three things the council found on this page, each pinned here:
 *   - A35: the loudness compliance card sat above the per-track meters the page
 *     exists for. The meters come first now; compliance follows them.
 *   - A36: a clip was one red badge with no announcement, and the readouts had
 *     a unit only in a caption under them, at a size nobody could read. The
 *     unit now sits above the readouts, and a clip is announced in words by a
 *     polite region that is always mounted, so it is there before it is needed.
 */

afterEach(cleanup);

function withLive(over: Partial<LiveData>) {
  const base = {
    programme: 1,
    programmeKnown: true,
    sourceCount: 1,
    connected: true,
    status: null,
    source: null,
    levels: null,
    system: null,
    bitrate: [],
    logs: [],
  } as unknown as LiveData;
  return { ...base, ...over };
}

function draw(value: LiveData) {
  return render(
    <LiveDataContext.Provider value={value}>
      <MetersPage />
    </LiveDataContext.Provider>,
  );
}

const src = {
  id: 1,
  name: "Main",
  probed: true,
  tracks: [{ index: 0, channels: 2, codec: "aac", layout: "stereo" }],
};

describe("MetersPage: the meters come before the compliance card (A35)", () => {
  it("draws Track 1 above the Loudness compliance heading", () => {
    draw(withLive({ source: src as never, levels: { peak: [[-10, -12]], rms: [[-20, -21]] } as never }));
    const track = screen.getByText("Track 1");
    const loudness = screen.getByText("Loudness compliance");
    expect(track.compareDocumentPosition(loudness) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
  });
});

describe("MetersPage: clipping is announced and the unit is stated (A36)", () => {
  it("announces a clip in words through a polite region that is mounted before any clip", () => {
    draw(withLive({ source: src as never, levels: { peak: [[0, -6]], rms: [[-3, -9]] } as never }));
    const region = document.querySelector('[aria-live="polite"]');
    expect(region).not.toBeNull();
    expect(region?.textContent).toContain("Clipping on track 1");
  });

  it("keeps the announcement region in the page with nothing in it while nothing clips", () => {
    draw(withLive({ source: src as never, levels: { peak: [[-10, -12]], rms: [[-20, -21]] } as never }));
    const region = document.querySelector('[aria-live="polite"]');
    expect(region).not.toBeNull();
    expect(region?.textContent).toBe("");
  });

  it("states the rms / peak unit above the readouts it belongs to", () => {
    draw(withLive({ source: src as never, levels: { peak: [[-10, -12]], rms: [[-20, -21]] } as never }));
    const caption = screen.getByText("rms / peak dBFS");
    const readout = [...document.querySelectorAll("span")].find(
      (el) => el.textContent === "-20.0 / -10.0",
    );
    expect(readout).toBeTruthy();
    expect(caption.compareDocumentPosition(readout as Node) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
  });
});

describe("MetersPage: a disabled monitor switch says why (A28)", () => {
  it("links the loudness monitor switch to the sentence that says why it is blank", () => {
    // A28. The switch is disabled while the analyser's state is unknown. The
    // sentence explaining that was printed but not connected to the switch, so
    // a keyboard or screen-reader user met a dead control with no reason.
    draw(withLive({ source: src as never, levels: null, status: null }));
    const sw = document.getElementById("loud-monitor") as HTMLElement;
    expect(sw).toBeTruthy();
    const reasonId = sw.getAttribute("aria-describedby");
    expect(reasonId).toBeTruthy();
    expect(document.getElementById(reasonId as string)?.textContent).toContain("Waiting for the analyser");
  });
});
