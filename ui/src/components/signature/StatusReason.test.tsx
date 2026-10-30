// @vitest-environment jsdom

import { afterEach, describe, expect, it } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";

import { StatusReason } from "./StatusReason";
import { timestamp } from "@/lib/format";

/* THE STATUS REASON RULE (B1). Every chip or row that reports something other
 * than a pass names three things: why, which step failed, and when the reading
 * was taken. An unknown is printed as an unknown. A line that is simply absent
 * reads as "nothing to say", which is the failure this exists to stop. */
afterEach(cleanup);

describe("StatusReason", () => {
  it("names the reason verbatim", () => {
    render(<StatusReason reason="Output rejected: stream key invalid" step="Output to Twitch" asOf="2026-10-29T12:34:56Z" />);
    expect(screen.getByText("Output rejected: stream key invalid")).toBeTruthy();
  });

  it("names the failing step when one is given", () => {
    render(<StatusReason reason="x" step="Output to Twitch" asOf={null} />);
    expect(screen.getByText(/Output to Twitch/)).toBeTruthy();
  });

  it("says the step was not reported rather than printing nothing", () => {
    render(<StatusReason reason="x" step={null} asOf={null} />);
    expect(screen.getByText(/not reported/i)).toBeTruthy();
  });

  it("prints the as-of time in the console's own clock format", () => {
    const iso = "2026-10-29T12:34:56Z";
    render(<StatusReason reason="x" step="s" asOf={iso} />);
    expect(screen.getByText(new RegExp(timestamp(iso).replace(/[.*+?^${}()|[\]\\]/g, "\\$&")))).toBeTruthy();
  });

  it("says the as-of time was not recorded rather than printing nothing", () => {
    render(<StatusReason reason="x" step="s" asOf={null} />);
    expect(screen.getByText(/not recorded/i)).toBeTruthy();
  });

  it("treats the Go zero time as not recorded", () => {
    render(<StatusReason reason="x" step="s" asOf="0001-01-01T00:00:00Z" />);
    expect(screen.getByText(/not recorded/i)).toBeTruthy();
  });
});
