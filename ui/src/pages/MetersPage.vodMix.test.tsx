// @vitest-environment jsdom

import { afterEach, describe, expect, it } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";

import { ComplianceRow } from "./MetersPage";

/* A destination with a VOD track has two compliance rows, one per mix, and the
 * VOD one must say which it is. Without the label the two rows for "YouTube"
 * read as one destination measured twice, with two different verdicts. */

type Report = Parameters<typeof ComplianceRow>[0]["report"];

const report = (over: Partial<Report> = {}): Report =>
  ({
    destinationId: 1,
    destination: "Twitch",
    seconds: 40,
    momentaryLufs: -16,
    shortTermLufs: -16,
    integratedLufs: -16,
    rangeLu: 3,
    truePeakDbtp: -2,
    integrated: true,
    target: { lufs: -14, truePeakDbtp: -1, toleranceLu: 1, source: "platform", reason: "Twitch" },
    verdict: "pass",
    deviationLu: -2,
    reason: "measured",
    at: "2026-08-17T00:00:00Z",
    ...over,
  }) as Report;

afterEach(cleanup);

describe("ComplianceRow for the VOD mix", () => {
  // MUTATION: in ComplianceRow, drop the mix === "vod" badge. Observed: FAIL,
  // "the VOD row does not say it is the VOD mix".
  it("labels a VOD mix row as the VOD mix", () => {
    render(<ComplianceRow report={report({ mix: "vod" })} truePeakFailOverDb={1} />);
    expect(screen.getByText("VOD mix")).toBeTruthy();
  });

  it("does not label the live mix row", () => {
    render(<ComplianceRow report={report({ mix: "live" })} truePeakFailOverDb={1} />);
    expect(screen.queryByText("VOD mix")).toBeNull();
  });
});
