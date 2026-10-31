import { describe, expect, it } from "vitest";

import { headerLive } from "./headerLive";

/* THE HEADER'S LIVE BADGE NAMES ITS SCOPE (app-council A81).
 *
 * The badge counted every running destination on the install, while the ingest
 * chip beside it, and the programme switcher beside that, are about the
 * selected programme. With Main selected the header read "5 live" while Main
 * had 3: two numbers side by side describing two different things, with
 * nothing on screen to say so. The badge now counts the selected programme and
 * says whose count it is, or says "All programmes" where there is no
 * programme to scope to. */

const running = (id: number, sourceId: number | null) => ({
  id,
  sourceId,
  process: { state: "running" as const },
});
const stopped = (id: number, sourceId: number | null) => ({
  id,
  sourceId,
  process: { state: "stopped" as const },
});

const programmes = [
  { id: 1, name: "Main" },
  { id: 2, name: "Studio B" },
];

describe("headerLive", () => {
  it("counts only the selected programme's running destinations and names the programme", () => {
    const destinations = [running(1, 1), running(2, 1), running(3, 1), running(4, 2), running(5, 2), stopped(6, 1)];
    expect(headerLive({ destinations, programme: 1, programmes, sourceCount: 2 })).toEqual({
      count: 3,
      key: "chrome.liveCountProgramme",
      params: { programme: "Main", count: 3 },
      offKey: "chrome.offAirProgramme",
      offParams: { programme: "Main" },
    });
  });

  it("says All programmes when a multi-programme install has no programme selected", () => {
    const destinations = [running(1, 1), running(4, 2)];
    expect(headerLive({ destinations, programme: null, programmes, sourceCount: 2 })).toEqual({
      count: 2,
      key: "chrome.liveCountAll",
      params: { count: 2 },
      offKey: "onAir.offAir",
    });
  });

  it("says All programmes when the selected programme's name is not known yet", () => {
    const destinations = [running(1, 1), running(4, 2)];
    const r = headerLive({ destinations, programme: 9, programmes, sourceCount: 2 });
    expect(r.key).toBe("chrome.liveCountAll");
    expect(r.count).toBe(2);
  });

  it("keeps the plain count on a single-programme install, where there is only one scope", () => {
    const destinations = [running(1, 1), running(2, 1), stopped(3, 1)];
    expect(headerLive({ destinations, programme: 1, programmes: [programmes[0]], sourceCount: 1 })).toEqual({
      count: 2,
      key: "chrome.liveCount",
      params: { count: 2 },
      offKey: "onAir.offAir",
    });
  });

  it("counts nothing before the first status frame", () => {
    expect(headerLive({ destinations: undefined, programme: 1, programmes, sourceCount: 2 }).count).toBe(0);
  });

  it("names the programme when it is off air, so another programme's broadcast does not read as this one's", () => {
    const destinations = [stopped(1, 1), running(4, 2)];
    const r = headerLive({ destinations, programme: 1, programmes, sourceCount: 2 });
    expect(r.count).toBe(0);
    expect(r.offKey).toBe("chrome.offAirProgramme");
    expect(r.offParams).toEqual({ programme: "Main" });
  });

  it("keeps the plain Off air where there is no programme to name", () => {
    const r = headerLive({ destinations: [], programme: 1, programmes: [programmes[0]], sourceCount: 1 });
    expect(r.offKey).toBe("onAir.offAir");
  });
});
