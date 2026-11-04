// @vitest-environment jsdom

import { afterEach, describe, expect, it } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";

import { ViewerReadoutLine } from "./AccountLiveStats";
import { setLanguage } from "@/lib/i18n";

/* The live viewer count beside an account. English "watching" does not change
 * with the count, so this renders in French, where it does: one viewer read
 * "1 spectateurs". */

afterEach(() => {
  cleanup();
  setLanguage("en");
});

describe("ViewerReadoutLine: stats.watching (fr)", () => {
  it("says 1 spectateur, not 1 spectateurs, for one viewer", () => {
    setLanguage("fr");
    render(<ViewerReadoutLine readout={{ kind: "count", count: 1 }} />);
    expect(screen.getByText("1 spectateur")).toBeTruthy();
    expect(screen.queryByText("1 spectateurs")).toBeNull();
  });

  it("says 2 spectateurs for two viewers", () => {
    setLanguage("fr");
    render(<ViewerReadoutLine readout={{ kind: "count", count: 2 }} />);
    expect(screen.getByText("2 spectateurs")).toBeTruthy();
  });
});
