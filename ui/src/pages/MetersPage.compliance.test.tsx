// @vitest-environment jsdom

import { afterEach, describe, expect, it } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";

import { ComplianceRow } from "./MetersPage";

/* The placeholder report an analyser publishes the moment it is planned, drawn
 * through the same formatters as a measurement. */

type Report = Parameters<typeof ComplianceRow>[0]["report"];

const report = (over: Partial<Report> = {}): Report =>
  ({
    destinationId: 1,
    destination: "Twitch",
    seconds: 0,
    momentaryLufs: 0,
    shortTermLufs: 0,
    integratedLufs: 0,
    rangeLu: 0,
    truePeakDbtp: 0,
    integrated: false,
    target: {
      lufs: -14,
      truePeakDbtp: -2,
      toleranceLu: 1,
      source: "platform",
      reason: "Twitch",
    },
    verdict: "unknown",
    deviationLu: 0,
    reason: "waiting for programme",
    at: "2026-08-17T00:00:00Z",
    ...over,
  }) as Report;

afterEach(cleanup);

describe("ComplianceRow", () => {
  it("prints no numbers at all for a destination nothing has been measured on", () => {
    // "Momentary 0.0 LUFS / True peak 0.0 dBTP" is digital full scale on a feed
    // nobody has listened to. The formatters' own floors cannot catch it: zero
    // is a plausible value on both scales -- it is the loudest one.
    const { container } = render(
      <ComplianceRow report={report()} truePeakFailOverDb={1} />,
    );
    expect(container.textContent).not.toContain("0.0");
    // Six stats, six dashes.
    expect(screen.getAllByText("—")).toHaveLength(6);
  });

  it("does not paint a true peak red against a ceiling nothing was measured against", () => {
    // truePeakDbtp 0 against a -2 dBTP ceiling with a +1 dB failover is "over"
    // on every arithmetic — and it is not a reading.
    const { container } = render(
      <ComplianceRow report={report()} truePeakFailOverDb={1} />,
    );
    expect(container.querySelector(".text-down")).toBeNull();
  });

  it("still shows the readings once any programme has gone through", () => {
    const { container } = render(
      <ComplianceRow
        report={report({ seconds: 4, momentaryLufs: -18.2, truePeakDbtp: -3.4 })}
        truePeakFailOverDb={1}
      />,
    );
    expect(container.textContent).toContain("-18.2");
    expect(container.textContent).toContain("-3.4");
  });
});
