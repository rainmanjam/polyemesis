import { describe, expect, it } from "vitest";

import { attention, onAir } from "./attention";
import { asDestinationId } from "./types";
import type { DestStatus, ProcessState, RenditionStatus, Status } from "./types";

/* The overview tier's two claims: what is on air, and what is wrong.
 *
 * Both are judgements rather than readings — which absences count as faults,
 * and which denominator "3 of 5" is measured against — so they are tested here
 * and merely wired up in the page. Same split as dashboardFacts.ts. */

const dest = (over: Omit<Partial<DestStatus>, "id"> & { id: number }): DestStatus =>
  ({
    name: `dest ${over.id}`,
    kind: "rtmp",
    platform: "custom",
    enabled: true,
    ...over,
    // Branded last, so the plain number the cases above read naturally with is
    // still the id these rows carry.
    id: asDestinationId(over.id),
  }) as unknown as DestStatus;

const proc = (state: ProcessState, over: Record<string, unknown> = {}) =>
  ({ state, ...over }) as DestStatus["process"];

const status = (over: Partial<Status> = {}): Status =>
  ({ destinations: [], renditions: [], ...over }) as unknown as Status;

describe("onAir", () => {
  it("counts live against the destinations the operator asked for, not against every row", () => {
    // A destination switched off is not one that failed to go live. Counting it
    // in the denominator makes a correctly configured install read as
    // permanently short of target, which teaches the operator to ignore it.
    const rows = [
      dest({ id: 1, process: proc("running") }),
      dest({ id: 2, process: proc("running") }),
      dest({ id: 3, enabled: false, process: null }),
    ];
    expect(onAir(rows)).toEqual({ live: 2, enabled: 2, degraded: 0, failed: 0 });
  });

  it("separates trying from failed, because they need different actions", () => {
    const rows = [
      dest({ id: 1, process: proc("reconnecting") }),
      dest({ id: 2, process: proc("starting") }),
      dest({ id: 3, process: proc("failed") }),
    ];
    expect(onAir(rows)).toMatchObject({ live: 0, enabled: 3, degraded: 2, failed: 1 });
  });

  it("reports zeroes rather than throwing before the first status frame", () => {
    expect(onAir(undefined)).toEqual({ live: 0, enabled: 0, degraded: 0, failed: 0 });
  });
});

describe("attention", () => {
  it("says nothing about a healthy install", () => {
    // The panel has to be able to be empty. One that always has a row in it is
    // decoration, and an operator stops reading it within a week.
    expect(
      attention(
        status({
          ingest: proc("running"),
          destinations: [dest({ id: 1, process: proc("running") })],
        }),
      ),
    ).toEqual([]);
  });

  it("says nothing about states somebody chose", () => {
    // Stopped ingest, disabled destination, and a rendition nobody is drawing
    // on. All three are configuration, not fault.
    expect(
      attention(
        status({
          ingest: proc("stopped"),
          destinations: [dest({ id: 1, enabled: false, process: proc("failed") })],
          renditions: [
            { id: 1, name: "720p", consumers: 0, process: { state: "failed" } } as unknown as RenditionStatus,
          ],
        }),
      ),
    ).toEqual([]);
  });

  it("puts the ingest first, because every destination below it carries that feed", () => {
    const items = attention(
      status({
        ingest: proc("reconnecting"),
        destinations: [dest({ id: 1, process: proc("failed") })],
      }),
    );
    // Ordering is worst-tone-first, and the ingest is a `warn` here while the
    // destination is a `down` — so this asserts the ingest is NOT hoisted above
    // a harder failure. It is first only among equals.
    expect(items.map((i) => i.subject)).toEqual(["dest 1", "Ingest"]);
  });

  it("orders failed ahead of reconnecting", () => {
    const items = attention(
      status({
        destinations: [
          dest({ id: 1, name: "warm", process: proc("reconnecting") }),
          dest({ id: 2, name: "dead", process: proc("failed") }),
        ],
      }),
    );
    expect(items.map((i) => i.subject)).toEqual(["dead", "warm"]);
  });

  it("carries the server's own words and never invents any", () => {
    const items = attention(
      status({
        destinations: [
          dest({ id: 1, process: proc("failed", { lastError: "connection refused" }) }),
          dest({ id: 2, name: "quiet", process: proc("failed") }),
        ],
      }),
    );
    expect(items[0].detail).toBe("connection refused");
    expect(items[1].detail).toBeUndefined();
  });

  it("raises an unconfirmed stop as its own row", () => {
    // process.state reads "stopped" on both arms of Stop, so nothing else on
    // the page can say the child may still be publishing.
    const items = attention(
      status({
        destinations: [
          dest({ id: 1, process: proc("stopped"), stopWarning: "SIGKILL issued, not reaped" }),
        ],
      }),
    );
    expect(items).toHaveLength(1);
    expect(items[0]).toMatchObject({ tone: "warn", detail: "SIGKILL issued, not reaped" });
  });

  it("keeps a stable key per subject so a row does not remount every status frame", () => {
    const first = attention(status({ destinations: [dest({ id: 4, process: proc("failed") })] }));
    const second = attention(status({ destinations: [dest({ id: 4, process: proc("failed") })] }));
    expect(first[0].key).toBe(second[0].key);
  });

  it("is empty before the first status frame arrives", () => {
    expect(attention(null)).toEqual([]);
  });
});
