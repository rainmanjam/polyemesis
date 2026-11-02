// @vitest-environment jsdom

/// <reference types="node" />
import { afterEach, describe, expect, it } from "vitest";
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { cleanup, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";

import { DestinationCard } from "./DestinationCard";
import { translate } from "@/lib/i18n";
import type { DestStatus } from "@/lib/types";

/* app-council A2 and A89 on the destination card.
 *
 * A2: a destination the operator has switched off was labelled STOPPED, the
 * same word a broadcast that ended uses, so eight switched-off cards beside
 * five live ones read as eight faults. It is labelled Off.
 *
 * A89: the LIVE badge said so by fill colour and small text. A running card's
 * badge now carries a glyph as well, so the state reads without colour. */

afterEach(cleanup);

const dest = (over: Partial<DestStatus> = {}): DestStatus =>
  ({
    id: 1,
    name: "Twitch",
    kind: "rtmp",
    platform: "twitch",
    enabled: false,
    summary: "",
    tracks: null,
    filterComplex: "",
    normalization: "off",
    warnings: null,
    ...over,
  }) as DestStatus;

const noop = () => {};

function draw(over: Partial<DestStatus>) {
  return render(
    <MemoryRouter>
      <DestinationCard
        dest={dest(over)}
        onStart={noop}
        onStop={noop}
        onRestart={noop}
        onEdit={noop}
        onDelete={noop}
        onRefreshKey={noop}
        onMoveEarlier={noop}
        onMoveLater={noop}
        canMoveEarlier={false}
        canMoveLater={false}
      />
    </MemoryRouter>,
  );
}

describe("DestinationCard status badge", () => {
  it("labels a switched-off destination Off, not Stopped", () => {
    draw({ enabled: false });
    const badge = screen.getByTestId("card-state-badge");
    expect(badge.textContent).toBe(translate("en", "state.off"));
    expect(badge.textContent).not.toBe(translate("en", "state.stopped"));
  });

  it("gives a running destination's badge a glyph, not colour alone", () => {
    draw({
      enabled: true,
      process: { name: "d1", kind: "destination", state: "running", pid: 1, restarts: 0, uptimeSec: 30 },
    } as Partial<DestStatus>);
    const badge = screen.getByTestId("card-state-badge");
    expect(badge.textContent).toBe(translate("en", "state.live"));
    expect(badge.querySelector("[data-badge-cue='on-air']")).not.toBeNull();
  });

  // 2026-11 review, row 10: on air is the tally lamp, not the green "ok" fill
  // that also means running and healthy elsewhere.
  it("lights an on-air destination's badge as the tally lamp", () => {
    draw({
      enabled: true,
      process: { name: "d1", kind: "destination", state: "running", pid: 1, restarts: 0, uptimeSec: 30 },
    } as Partial<DestStatus>);
    const cls = screen.getByTestId("card-state-badge").className.split(/\s+/);
    expect(cls).toContain("bg-tally");
    expect(cls).toContain("text-tally-foreground");
    expect(cls).not.toContain("bg-ok-dim");
  });

  it("keeps a failed destination's badge a failure, never a lamp", () => {
    draw({
      enabled: true,
      process: { name: "d1", kind: "destination", state: "failed", pid: 1, restarts: 0, uptimeSec: 0 },
    } as Partial<DestStatus>);
    expect(screen.getByTestId("card-state-badge").className).not.toContain("bg-tally");
  });

  // Reached by a mouse click on the On Air bar, which :focus-visible ignores.
  it("keeps a :focus ring for the programmatic jump that lands on it", () => {
    const { container } = draw({ enabled: true });
    const card = container.querySelector("[tabindex='-1']") ?? container.firstElementChild;
    expect((card as HTMLElement | null)?.hasAttribute("data-jump-target")).toBe(true);
    // ...and the stylesheet gives that attribute the ring on plain :focus.
    const css = readFileSync(join(process.cwd(), "src", "index.css"), "utf8");
    expect(css).toMatch(/\[data-jump-target\]:focus\s*\{\s*outline: 2px solid var\(--ring\);/);
  });
});
