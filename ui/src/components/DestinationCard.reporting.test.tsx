// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";

import { DestinationCard } from "./DestinationCard";
import type { DestStatus } from "@/lib/types";

/* WHAT THIS CARD MAY CLAIM TO HAVE MEASURED.
 *
 * Two defects, one rule. The rule is written in this card already, in the
 * comment above the Speed cell: a number nobody measured renders as a dash, not
 * as a zero. Bitrate, Uptime and Speed obeyed it. Restarts and Dropped did not,
 * and the stop that could not be confirmed was not rendered at all.
 *
 * jsdom rather than renderToStaticMarkup, matching the sibling test: this card
 * runs effects and a poll.
 */

vi.mock("@/lib/api", async () => {
  const actual = await vi.importActual<typeof import("@/lib/api")>("@/lib/api");
  return { ...actual, api: { ...actual.api } };
});

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

/** The value rendered beside one Stat label. The label and its value are
 *  siblings inside the Stat wrapper, so walk up from the label. */
function statValue(label: string): string {
  const el = screen.getByText(label);
  return el.parentElement?.textContent?.replace(label, "").trim() ?? "";
}

describe("A4-14: Restarts and Dropped", () => {
  afterEach(cleanup);

  it("dashes Restarts for a destination with no process, rather than claiming zero", () => {
    // No process means nothing has reported. "0" here is the same reassuring
    // number a healthy never-restarted run produces, so the reading that would
    // send somebody looking is indistinguishable from the one that says
    // nothing at all.
    draw({ enabled: false, process: null });
    expect(statValue("Restarts")).toBe("—");
  });

  it("still prints a real restart count when there is a process to have restarted", () => {
    draw({
      enabled: true,
      process: { state: "running", restarts: 3, uptimeSec: 90 },
    } as Partial<DestStatus>);
    expect(statValue("Restarts")).toBe("3");
  });

  it("dashes Dropped when the destination is not running", () => {
    // dropFrames is cumulative over a run, so a finished run's total stays on
    // screen and reads as a live count -- and "Dropped 0" beside a stopped
    // destination claims a clean run for a stream nothing is measuring.
    draw({
      enabled: false,
      process: { state: "stopped", restarts: 0, progress: { dropFrames: 0 } },
    } as Partial<DestStatus>);
    expect(statValue("Dropped")).toBe("—");
  });

  it("prints dropped frames while the destination is actually running", () => {
    draw({
      enabled: true,
      process: { state: "running", restarts: 0, progress: { dropFrames: 12 } },
    } as Partial<DestStatus>);
    expect(statValue("Dropped")).toBe("12");
  });
});

describe("A4-2: a stop that was never confirmed", () => {
  afterEach(cleanup);

  it("renders the server's stopWarning, which nothing in ui/src did", () => {
    // The stop ended on Stop's deadline arm: SIGKILL issued, not waited for,
    // the child possibly still publishing. process.state reads "stopped" on
    // BOTH arms, so this sentence is the only thing that can say it -- and the
    // operator's next move, starting it again, is what produces two encoders
    // on one stream key.
    const warning =
      "the previous stop was not confirmed: SIGKILL was issued and not waited for";
    draw({ enabled: false, stopWarning: warning });
    expect(screen.getByText(warning)).toBeTruthy();
  });

  it("says nothing about a stop that ended cleanly", () => {
    draw({ enabled: false });
    expect(screen.queryByText(/not confirmed/)).toBeNull();
  });
});
