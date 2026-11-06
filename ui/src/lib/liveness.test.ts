import { describe, expect, it } from "vitest";

import { attention, onAir } from "./attention";
import { headerLive } from "./headerLive";
import { ingestChipReading } from "./ingestChip";
import {
  destinationLanded,
  destinationOnAir,
  destinationShownState,
  ingestSignalTone,
  ingestWaiting,
  onAirSeconds,
} from "./liveness";
import type { DestStatus, ProcessStatus, Status } from "./types";

const proc = (over: Partial<ProcessStatus>): ProcessStatus =>
  ({ name: "p", kind: "dest", state: "running", pid: 1, restarts: 0, uptimeSec: 0, progress: {}, ...over }) as ProcessStatus;

const dest = (id: number, over: Omit<Partial<DestStatus>, "process"> & { process?: Partial<ProcessStatus> | null }): DestStatus =>
  ({
    id,
    name: `d${id}`,
    enabled: true,
    sourceId: 1,
    ...over,
    process: over.process === null ? undefined : proc(over.process ?? {}),
  }) as unknown as DestStatus;

const header = (destinations: DestStatus[]) =>
  headerLive({ destinations, programme: null, programmes: [], sourceCount: 1 }).count;

describe("one frame, one live count", () => {
  // The recorded disagreement: a Kick destination whose process is running but
  // has carried no media (IVS refused the connect). The header said 1 live,
  // OnAirBar said 0 live and 1 degraded, the card said Starting.
  const frames: Record<string, DestStatus[]> = {
    "running, no media yet": [dest(1, { process: { state: "running", uptimeSec: 0 } })],
    "running with media": [dest(1, { process: { state: "running", uptimeSec: 12 } })],
    "disabled but still running": [dest(1, { enabled: false, process: { state: "running", uptimeSec: 12 } })],
    mixed: [
      dest(1, { process: { state: "running", uptimeSec: 5 } }),
      dest(2, { process: { state: "running", uptimeSec: 0 } }),
      dest(3, { process: { state: "reconnecting" } }),
      dest(4, { process: { state: "failed" } }),
    ],
  };
  for (const [name, frame] of Object.entries(frames)) {
    it(`header, bar and predicate agree: ${name}`, () => {
      const live = frame.filter(destinationOnAir).length;
      expect(header(frame)).toBe(live);
      expect(onAir(frame).live).toBe(live);
    });
  }

  it("says 0 live for a running process that has carried no media", () => {
    const frame = frames["running, no media yet"];
    expect(header(frame)).toBe(0);
    expect(onAir(frame)).toMatchObject({ live: 0, degraded: 1 });
    expect(destinationShownState(frame[0].process)).toBe("starting");
  });
});

describe("the bar's degraded count always has a row to explain it", () => {
  it("lists a running-with-no-media destination as starting", () => {
    const destinations = [dest(1, { process: { state: "running", uptimeSec: 0 } })];
    expect(onAir(destinations).degraded).toBe(1);
    const rows = attention({ destinations } as unknown as Status);
    expect(rows).toHaveLength(1);
    expect(rows[0]).toMatchObject({ tone: "warn", state: "starting", destinationId: 1 });
  });

  it("lists nothing for one that is on air", () => {
    const destinations = [dest(1, { process: { state: "running", uptimeSec: 8 } })];
    expect(attention({ destinations } as unknown as Status)).toEqual([]);
  });
});

describe("destination liveness policy", () => {
  it("treats media as having moved on uptime or output time", () => {
    expect(destinationShownState(proc({ uptimeSec: 3 }))).toBe("running");
    expect(destinationShownState(proc({ uptimeSec: 0, progress: { outTimeMs: 4000 } as ProcessStatus["progress"] }))).toBe("running");
    expect(destinationShownState(proc({ uptimeSec: 0 }))).toBe("starting");
    expect(destinationShownState(null)).toBeUndefined();
  });

  it("keeps a stalled process on air but does not call its start landed", () => {
    const stalled = proc({ uptimeSec: 9, stalled: true });
    expect(destinationOnAir({ enabled: true, process: stalled })).toBe(true);
    expect(destinationLanded(stalled)).toBe(false);
    expect(destinationLanded(proc({ uptimeSec: 9 }))).toBe(true);
    expect(destinationLanded(proc({ uptimeSec: 0 }))).toBe(false);
  });

  it("times the broadcast from the longest enabled destination on air", () => {
    const frame = [
      dest(1, { process: { state: "running", uptimeSec: 30 } }),
      dest(2, { enabled: false, process: { state: "running", uptimeSec: 999 } }),
      dest(3, { process: { state: "running", uptimeSec: 0 } }),
    ];
    expect(onAirSeconds(frame)).toBe(30);
  });
});

describe("ingest liveness", () => {
  const listener = proc({ state: "running", progress: {} as ProcessStatus["progress"] });

  it("does not read a waiting listener as live (chip)", () => {
    // Running with no bytes arriving: an SRT/RTMP listener idling for its encoder.
    const r = ingestChipReading(listener, false, false, true);
    expect(r).not.toEqual({ kind: "state", state: "running" });
    expect(r).toEqual({ kind: "waiting" });
  });

  it("is live only while bytes arrive", () => {
    expect(ingestChipReading(listener, true, false, true)).toEqual({ kind: "state", state: "running" });
    expect(ingestWaiting(listener, false)).toBe(true);
    expect(ingestWaiting(listener, true)).toBe(false);
    expect(ingestWaiting(null, false)).toBe(false);
  });

  it("draws a waiting listener armed, never live-green", () => {
    expect(ingestSignalTone(listener, false, false)).toBe("armed");
    expect(ingestSignalTone(listener, true, false)).toBe("live");
    expect(ingestSignalTone(listener, false, true)).toBe("warn");
    expect(ingestSignalTone(proc({ state: "failed" }), false, false)).toBe("down");
  });
});
