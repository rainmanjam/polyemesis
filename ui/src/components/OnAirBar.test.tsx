// @vitest-environment jsdom

/* THE FIRST TIER OF THE DASHBOARD, previously untested.
 *
 * Its whole claim is that the two questions an operator asks under load -- "am
 * I on air?" and "what is broken?" -- are answered above the inventory rather
 * than inside it. The failure modes follow from that: a headline that reads the
 * wrong state, counts that render as a steady zero (which teaches the eye to
 * skip the place a real one appears), and a fault list that is not empty when
 * nothing is wrong. */

import { afterEach, describe, expect, it } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";

import { OnAirBar } from "./OnAirBar";
import type { DestStatus, Status } from "@/lib/types";
import { setLanguage, translate } from "@/lib/i18n";

afterEach(cleanup);

/* DestinationId is branded on purpose -- a bare number cannot be one, which is
   what stops a source id being passed where a destination id belongs. Tests
   need ids all the same, so they are minted here, in one visible place. */
const did = (n: number) => n as unknown as DestStatus["id"];

const dest = (over: Partial<DestStatus> = {}): DestStatus =>
  ({
    id: did(1),
    name: "Twitch",
    kind: "rtmp",
    platform: "twitch",
    enabled: true,
    summary: "",
    tracks: null,
    filterComplex: "",
    normalization: "off",
    warnings: null,
    ...over,
  }) as DestStatus;

const proc = (state: string, over: Partial<DestStatus> = {}) =>
  dest({ process: { state }, ...over } as Partial<DestStatus>);

const status = (destinations: DestStatus[] = []): Status =>
  ({ ingest: null, destinations }) as Status;

describe("OnAirBar headline", () => {
  it("reads ON AIR when something is running", () => {
    render(<OnAirBar status={status([proc("running")])} />);
    expect(screen.getByText("On air")).toBeTruthy();
  });

  it("reads COMING UP while a destination is still connecting", () => {
    // Distinct from both live and failed on purpose: an operator who reads
    // "Off air" during the connect window starts pulling things apart.
    render(<OnAirBar status={status([proc("starting")])} />);
    expect(screen.getByText("Coming up")).toBeTruthy();
  });

  it("reads OFF AIR when an enabled destination has failed", () => {
    render(<OnAirBar status={status([proc("failed")])} />);
    expect(screen.getByText("Off air")).toBeTruthy();
  });

  it("reads OFF AIR on an install with nothing enabled, which is not a fault", () => {
    render(<OnAirBar status={status([dest({ enabled: false })])} />);
    expect(screen.getByText("Off air")).toBeTruthy();
  });
});

describe("OnAirBar lamp (2026-11 review, row 10)", () => {
  // On air is the tally lamp, the same red the destination cards' badges
  // light, not the green that also means running and healthy.
  it("lights a solid tally lamp beside ON AIR", () => {
    render(<OnAirBar status={status([proc("running")])} />);
    const lamp = screen.getByTestId("on-air-lamp");
    expect(lamp.className.split(/\s+/)).toContain("bg-tally");
  });

  it("has no lamp while off air", () => {
    render(<OnAirBar status={status([proc("failed")])} />);
    expect(screen.queryByTestId("on-air-lamp")).toBeNull();
  });
});

describe("OnAirBar counts", () => {
  it("names the live, stopped and off counts in words, not as a bare fraction", () => {
    // "2/3" alone gets read as a bitrate on a console full of them.
    render(<OnAirBar status={status([proc("running"), proc("running", { id: did(2) }), proc("failed", { id: did(3) })])} />);
    expect(screen.getByText(/2 live/)).toBeTruthy();
    expect(screen.getByText(/1 failed/)).toBeTruthy();
  });

  it("counts against the whole install, and a switched-off card counts as Off, not Stopped", () => {
    // A2: the denominator used to be enabled destinations only, so "5/5" sat
    // beside thirteen cards. The numbers now add up to the install. A91: a
    // switched-off destination is counted under the word its card shows, Off,
    // and an enabled one that is not running under Stopped.
    const off = dest({ id: did(4), enabled: false, process: null } as Partial<DestStatus>);
    render(<OnAirBar status={status([proc("running"), proc("failed", { id: did(2) }), off])} />);
    expect(screen.getByText("1 live · 0 stopped · 1 off")).toBeTruthy();
    expect(screen.getByText(/1 failed/)).toBeTruthy();
  });

  it("renders no count at all when it would be zero", () => {
    // A steady "0 failed" is a number that teaches the eye to skip the place
    // where a real one will appear.
    render(<OnAirBar status={status([proc("running")])} />);
    expect(screen.queryByText(/failed/)).toBeNull();
    expect(screen.queryByText(/connecting/)).toBeNull();
  });

  it("renders the failed count once there is something to count", () => {
    render(<OnAirBar status={status([proc("failed")])} />);
    expect(screen.getByText(/1 failed/)).toBeTruthy();
  });
});

describe("OnAirBar fault list", () => {
  it("is EMPTY when nothing is wrong, and that emptiness is the feature", () => {
    // A panel that always has something in it is decoration, and an operator
    // stops reading it within a week -- which costs them the shift it mattered.
    const { container } = render(<OnAirBar status={status([proc("running")])} />);
    expect(container.querySelector("ul")).toBeNull();
  });

  it("lists a failed destination by name", () => {
    render(<OnAirBar status={status([proc("failed", { name: "YouTube" })])} />);
    const list = screen.getByRole("list");
    expect(list.textContent).toContain("YouTube");
  });
});

describe("OnAirBar across-the-room", () => {
  it("keeps the large-type view closed until it is asked for", () => {
    render(<OnAirBar status={status([proc("running")])} />);
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(screen.getByRole("button", { name: /full-screen status/i })).toBeTruthy();
  });
});

describe("OnAirBar verdict line", () => {
  it("says all destinations are healthy, and how long the ingest has been stable", () => {
    const st = {
      ingest: { state: "running", uptimeSec: 760 },
      destinations: [proc("running"), proc("running", { id: did(2) })],
    } as unknown as Status;
    render(<OnAirBar status={st} />);

    expect(screen.getByText(translate("en", "health.verdictHealthy", { count: 2 }))).toBeTruthy();
    expect(screen.getByText(/Ingest stable/)).toBeTruthy();
  });

  it("names how many destinations need attention, out of how many there are", () => {
    render(<OnAirBar status={status([proc("running"), proc("failed", { id: did(2) })])} />);

    expect(screen.getByTestId("health-verdict").textContent).toBe("1 of 2 destinations needs attention");
  });

  it("names its scope: the running destinations, not the whole install (app-council A2)", () => {
    // Two running, one switched off. "All 2 destinations healthy" beside a
    // page of three cards read as the whole install; the sentence now says
    // which destinations it is about. Literal English on purpose, so the test
    // pins the words and not only the key.
    const st = {
      ingest: { state: "running", uptimeSec: 760 },
      destinations: [
        proc("running"),
        proc("running", { id: did(2) }),
        proc("stopped", { id: did(3), enabled: false }),
      ],
    } as unknown as Status;
    render(<OnAirBar status={st} />);

    expect(screen.getByTestId("health-verdict").textContent).toBe("All 2 running destinations healthy");
  });

  it("offers a way to the first destination that needs attention", () => {
    render(<OnAirBar status={status([proc("running"), proc("failed", { id: did(2) })])} />);

    expect(screen.getByRole("button", { name: translate("en", "health.verdictShow") })).toBeTruthy();
  });

  it("does not claim healthy while the broadcast is on the slate", () => {
    const st = {
      ingest: null,
      destinations: [proc("running"), proc("running", { id: did(2) })],
    } as unknown as Status;
    render(
      <OnAirBar
        status={st}
        failover={{ active: "slate", switchedAt: "", switches: 1, primaryLive: false, backupLive: false, backupEnabled: false, slateEnabled: true }}
      />,
    );

    expect(screen.queryByText(translate("en", "health.verdictHealthy", { count: 2 }))).toBeNull();
  });

  it("does not claim healthy while an enabled destination is quietly stopped", () => {
    render(<OnAirBar status={status([proc("running"), proc("stopped", { id: did(2) })])} />);

    expect(screen.queryByText(translate("en", "health.verdictHealthy", { count: 2 }))).toBeNull();
  });
});

describe("OnAirBar on-air strip (A8)", () => {
  const timed = (state: string, uptimeSec: number, id = 1) =>
    dest({ id: did(id), process: { state, uptimeSec } } as Partial<DestStatus>);

  it("shows how long the broadcast has been on air, from the longest-running destination", () => {
    render(<OnAirBar status={status([timed("running", 60, 1), timed("running", 760, 2)])} />);
    expect(screen.getByTestId("on-air-duration").textContent).toMatch(/12/);
  });

  it("offers Stop all only while something is on air, and it asks the dashboard", () => {
    let asked = 0;
    const { rerender } = render(
      <OnAirBar status={status([timed("stopped", 0)])} onEndAll={() => asked++} />,
    );
    expect(screen.queryByRole("button", { name: "Stop all" })).toBeNull();
    rerender(<OnAirBar status={status([timed("running", 30)])} onEndAll={() => asked++} />);
    fireEvent.click(screen.getByRole("button", { name: "Stop all" }));
    expect(asked).toBe(1);
  });
});

describe("OnAirBar: the feed behind a running broadcast (A3)", () => {
  it("says the ingest is not receiving, instead of 'all healthy', when every destination is up over a dead feed", () => {
    render(<OnAirBar status={status([proc("running")])} ingestReceiving={false} />);
    expect(screen.getByTestId("health-verdict").textContent).toBe(translate("en", "health.verdictNoIngest"));
    expect(screen.queryByText(/healthy/i)).toBeNull();
  });

  it("keeps the healthy line when the feed is receiving", () => {
    render(<OnAirBar status={status([proc("running")])} ingestReceiving={true} />);
    expect(screen.getByTestId("health-verdict").textContent).toMatch(/healthy/i);
  });
});

/* Before the first status frame there are no destinations to count, and the
 * strip read "OFF AIR, 0 live" for that moment of a cold load over a broadcast
 * that was going out. No reading is not off air. */
describe("OnAirBar before the first status frame", () => {
  it("reads loading, with no counts, rather than OFF AIR", () => {
    render(<OnAirBar status={null} />);
    expect(screen.getByText(translate("en", "common.loading"))).toBeTruthy();
    expect(screen.queryByText(translate("en", "onAir.offAir"))).toBeNull();
    expect(screen.queryByTestId("on-air-counts")).toBeNull();
  });

  it("still reads OFF AIR once a frame says nothing is running", () => {
    render(<OnAirBar status={status([proc("stopped")])} />);
    expect(screen.getByText(translate("en", "onAir.offAir"))).toBeTruthy();
    expect(screen.getByTestId("on-air-counts")).toBeTruthy();
  });
});

describe("OnAirBar: one reads as one (no plural rules, so count 1 has its own key)", () => {
  afterEach(() => setLanguage("en"));
  const ingest = { state: "running", uptimeSec: 760 };

  it("says one running destination is healthy, not all 1 destinations", () => {
    render(<OnAirBar status={{ ingest, destinations: [proc("running")] } as unknown as Status} />);
    expect(screen.getByTestId("health-verdict").textContent).toBe("1 running destination healthy");
  });

  it("says all 2 running destinations are healthy", () => {
    render(
      <OnAirBar status={{ ingest, destinations: [proc("running"), proc("running", { id: did(2) })] } as unknown as Status} />,
    );
    expect(screen.getByTestId("health-verdict").textContent).toBe("All 2 running destinations healthy");
  });

  it("says 2 of 3 destinations need attention", () => {
    render(
      <OnAirBar
        status={status([proc("running"), proc("failed", { id: did(2) }), proc("failed", { id: did(3) })])}
      />,
    );
    expect(screen.getByTestId("health-verdict").textContent).toBe("2 of 3 destinations need attention");
  });

  it("counts one failed destination in the singular (es: 1 fallido)", () => {
    setLanguage("es");
    render(<OnAirBar status={status([proc("running"), proc("failed", { id: did(2) })])} />);
    expect(screen.getByText("1 fallido")).toBeTruthy();
    expect(screen.queryByText("1 fallidos")).toBeNull();
  });

  it("counts two failed destinations in the plural (es: 2 fallidos)", () => {
    setLanguage("es");
    render(
      <OnAirBar
        status={status([proc("running"), proc("failed", { id: did(2) }), proc("failed", { id: did(3) })])}
      />,
    );
    expect(screen.getByText("2 fallidos")).toBeTruthy();
  });

  it("agrees with the feminine destinazioni in Italian (1 non riuscita, 2 non riuscite)", () => {
    setLanguage("it");
    render(<OnAirBar status={status([proc("running"), proc("failed", { id: did(2) })])} />);
    expect(screen.getByText("1 non riuscita")).toBeTruthy();
    cleanup();
    render(
      <OnAirBar
        status={status([proc("running"), proc("failed", { id: did(2) }), proc("failed", { id: did(3) })])}
      />,
    );
    expect(screen.getByText("2 non riuscite")).toBeTruthy();
  });
});
