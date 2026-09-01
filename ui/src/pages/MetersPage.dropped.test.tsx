// @vitest-environment jsdom

import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import { MetersPage } from "./MetersPage";
import { LiveDataContext } from "@/hooks/useLiveData";
import type { LiveData } from "@/hooks/useLiveData";

/* THE COUNT EXISTED FOR A YEAR AND RENDERED NOWHERE.
 *
 * ffmpeg.MetersDropped has been computed since the 64-channel limit was
 * introduced, and its comment states the purpose: so a wide ingest "degrades
 * visibly instead of silently metering a prefix and letting an operator believe
 * a track is silent when it is merely unmeasured." Nothing carried it out of
 * that package. The device was built and never connected, so the failure it
 * described happened anyway.
 *
 * A test that only checks the warning renders would not have caught the
 * original bug, because the original bug was that there was nothing to render.
 * So this asserts BOTH directions: absent when zero, present when not. The
 * zero case is the one that keeps the badge honest -- a warning on every
 * install is furniture, and furniture is ignored. */

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
  // No router: MetersPage navigates nowhere, so wrapping it in one would be
  // scaffolding for a dependency the page does not have.
  return render(
    <LiveDataContext.Provider value={value}>
      <MetersPage />
    </LiveDataContext.Provider>,
  );
}

const src = (over: Record<string, unknown>) => ({
  id: 1,
  name: "Main",
  probed: true,
  tracks: [{ index: 0, channels: 2, codec: "aac", layout: "stereo" }],
  ...over,
});

describe("MetersPage: unmetered tracks", () => {
  it("says so when tracks were dropped, and says unmeasured rather than silent", () => {
    draw(withLive({ source: src({ metersDropped: 3 }) as never }));

    const note = screen.getByRole("status");
    expect(note.textContent).toContain("last 3 tracks are not being metered");
    // The whole point. "No signal" and "not measured" draw the same flat bar,
    // and only one of them means the track is fine.
    expect(note.textContent).toContain("unmeasured, not silent");
  });

  it("uses the singular for one dropped track", () => {
    draw(withLive({ source: src({ metersDropped: 1 }) as never }));
    expect(screen.getByRole("status").textContent).toContain("last 1 track is not being metered");
  });

  it("says nothing when every track is metered", () => {
    // The control. Without this the assertion above passes just as happily on a
    // banner that is always on, which is worse than no banner: it trains the
    // operator to scroll past the row that will one day matter.
    draw(withLive({ source: src({ metersDropped: 0 }) as never }));
    expect(screen.queryByRole("status")).toBeNull();
  });

  it("says nothing on a server that does not send the field", () => {
    // Older server, or a payload predating this. Absent must read as "nothing
    // dropped", never as a warning nobody can act on.
    draw(withLive({ source: src({}) as never }));
    expect(screen.queryByRole("status")).toBeNull();
  });
});

describe("MetersPage: which programme", () => {
  it("names the programme when the install has more than one", () => {
    draw(withLive({ sourceCount: 2, source: src({ name: "Vertical" }) as never }));
    expect(screen.getByText("Vertical")).toBeTruthy();
  });

  it("stays quiet on a single-source install", () => {
    // A label that never varies is furniture. The overwhelming majority of
    // installs have one programme and gain nothing from being told so.
    draw(withLive({ sourceCount: 1, source: src({ name: "Main" }) as never }));
    expect(screen.queryByText("Main")).toBeNull();
  });
});
