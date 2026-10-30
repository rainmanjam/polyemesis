// @vitest-environment jsdom

import { afterEach, describe, expect, it } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";

import { MeterScale } from "./AudioMeter";

/* A16 follow-up: the -14 streaming reference and the -12 break sit 2 dB apart.
 * On a -60..0 ruler that is about 3% of the width, so both 9px numbers overlap on
 * any normal meter. The streaming reference keeps its number; -12 keeps its tick
 * on the bar and loses its number. */
afterEach(cleanup);

describe("MeterScale labels", () => {
  it("prints the -14 streaming reference", () => {
    render(<MeterScale />);
    expect(screen.getByText("-14")).toBeTruthy();
  });

  it("does not print -12 beside it, where the two numbers would overlap", () => {
    render(<MeterScale />);
    expect(screen.queryByText("-12")).toBeNull();
  });

  it("still prints -6, the hot break", () => {
    render(<MeterScale />);
    expect(screen.getByText("-6")).toBeTruthy();
  });
});
