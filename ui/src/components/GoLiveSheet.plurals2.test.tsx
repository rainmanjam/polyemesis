// @vitest-environment jsdom

/* The Go live sheet's footer summary is count-neutral in Spanish. It read
 * "1 en directo · 1 iniciando · 1 fallidos · 1 bloqueados": "fallidos" and
 * "bloqueados" are plural and wrong at 1. The catalogue has no plural rules,
 * so the line was rewritten to "label: value", which reads at any count. */

import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { GoLiveSheet } from "./GoLiveSheet";
import { goLiveRows } from "@/lib/goLive";
import { setLanguage } from "@/lib/i18n";
import type { DestStatus, DestinationId, SourceView } from "@/lib/types";

afterEach(cleanup);
afterEach(() => setLanguage("en"));

const did = (n: number) => n as unknown as DestinationId;

/** On air: running, and its output has moved. */
const onAir = { state: "running", startedAt: "2026-11-04T12:00:00Z", restarts: 0, progress: { outTimeMs: 4000 } };

const dest = (over: Record<string, unknown> = {}) =>
  ({
    id: did(1),
    name: "Twitch — no music",
    kind: "rtmp",
    platform: "twitch",
    enabled: false,
    summary: "",
    tracks: null,
    filterComplex: "",
    normalization: "off",
    warnings: null,
    sourceId: 1,
    ...over,
  }) as unknown as DestStatus;

const sources = [{ id: 1, name: "Studio A" }] as unknown as SourceView[];

function draw(destinations: DestStatus[]) {
  render(
    <GoLiveSheet
      open
      onOpenChange={() => {}}
      rows={goLiveRows(destinations, sources)}
      live={destinations}
      youtubeAtRisk={0}
      onStart={vi.fn(async () => {})}
      onRetry={async () => {}}
      onFinished={() => {}}
      paceMs={0}
    />,
  );
}

/** One row in each state the summary counts, `n` times over. */
function rowsOf(n: number): DestStatus[] {
  const out: DestStatus[] = [];
  let id = 1;
  for (let i = 0; i < n; i++) {
    out.push(dest({ id: did(id), name: `Live ${id++}`, enabled: true, process: onAir }));
    out.push(dest({ id: did(id), name: `Starting ${id++}`, enabled: true, process: { state: "starting" } }));
    out.push(dest({ id: did(id), name: `Failed ${id++}`, enabled: true, process: { state: "failed" } }));
    // No source: blocked.
    out.push(dest({ id: did(id), name: `Blocked ${id++}`, sourceId: 9 }));
  }
  return out;
}

describe("Go live sheet: the footer summary is count-neutral (es)", () => {
  it("reads en directo: 1 · iniciando: 1 · fallidos: 1 · bloqueados: 1", () => {
    setLanguage("es");
    draw(rowsOf(1));

    expect(screen.getByText("en directo: 1 · iniciando: 1 · fallidos: 1 · bloqueados: 1")).toBeTruthy();
    expect(screen.queryByText("1 en directo · 1 iniciando · 1 fallidos · 1 bloqueados")).toBeNull();
  });

  it("reads en directo: 1 · iniciando: 0 · fallidos: 0 · bloqueados: 1 with one live and one blocked", () => {
    setLanguage("es");
    draw([
      dest({ id: did(1), name: "Live", enabled: true, process: onAir }),
      dest({ id: did(2), name: "Blocked", sourceId: 9 }),
    ]);

    expect(screen.getByText("en directo: 1 · iniciando: 0 · fallidos: 0 · bloqueados: 1")).toBeTruthy();
    expect(screen.queryByText("1 en directo · 0 iniciando · 0 fallidos · 1 bloqueados")).toBeNull();
  });

  it("reads en directo: 2 · iniciando: 2 · fallidos: 2 · bloqueados: 2", () => {
    setLanguage("es");
    draw(rowsOf(2));

    expect(screen.getByText("en directo: 2 · iniciando: 2 · fallidos: 2 · bloqueados: 2")).toBeTruthy();
  });
});
