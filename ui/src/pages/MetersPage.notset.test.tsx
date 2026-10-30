// @vitest-environment jsdom

import { afterEach, describe, expect, it } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";

import { ComplianceRow } from "./MetersPage";

/* A16: a destination with no loudness target read UNKNOWN, which looked like a
 * broken tab. With no target it now reads "Not set" and offers Set target, which
 * opens that destination's routing page. A target that is set keeps its verdict. */
afterEach(cleanup);

const base = {
  destinationId: 7,
  destination: "YouTube — full mix",
  seconds: 0,
  momentaryLufs: -23,
  shortTermLufs: -23,
  integratedLufs: -23,
  rangeLu: 1,
  truePeakDbtp: -2,
  integrated: false,
  deviationLu: 0,
  reason: "no target",
  at: "",
};

function row(target: "none" | "profile") {
  const report = {
    ...base,
    verdict: "unknown" as const,
    target: {
      lufs: -14,
      truePeakDbtp: -1,
      toleranceLu: 1,
      source: target,
      reason: "",
    },
  };
  return (
    <MemoryRouter>
      <ComplianceRow report={report} truePeakFailOverDb={1} />
    </MemoryRouter>
  );
}

describe("ComplianceRow without a loudness target", () => {
  it("reads Not set instead of UNKNOWN", () => {
    render(row("none"));
    expect(screen.getByText("Not set")).toBeTruthy();
    expect(screen.queryByText("unknown")).toBeNull();
  });

  it("offers Set target, linking to that destination's routing page", () => {
    render(row("none"));
    const link = screen.getByRole("link", { name: "Set target" });
    expect(link.getAttribute("href")).toBe("/routing/7");
  });

  it("carries the existing 20-second explanation on the action", () => {
    render(row("none"));
    const link = screen.getByRole("link", { name: "Set target" });
    expect(link.getAttribute("title")).toMatch(/20/);
  });
});

describe("ComplianceRow with a loudness target", () => {
  it("keeps the verdict and does not offer Set target", () => {
    render(row("profile"));
    expect(screen.queryByText("Not set")).toBeNull();
    expect(screen.queryByRole("link", { name: "Set target" })).toBeNull();
    expect(screen.getByText("unknown")).toBeTruthy();
  });
});
