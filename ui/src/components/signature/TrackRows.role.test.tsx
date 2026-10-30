// @vitest-environment jsdom

import { afterEach, describe, expect, it } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";

import { RoleBadge } from "./TrackRows";

/* A15: the role chip overflowed its card in the Track 3 row ("COMMENTARY" wrapped
 * and collided with "not measured"). The chip must shrink with its column, cut
 * its text with an ellipsis, and keep the full role name on hover. jsdom cannot
 * measure overflow, so these pin the rules that produce it. */
afterEach(cleanup);

describe("RoleBadge", () => {
  it("shows the role name", () => {
    render(<RoleBadge role="commentary" excluded={false} />);
    expect(screen.getByText("Commentary")).toBeTruthy();
  });

  it("keeps the full role name on hover once the text is cut", () => {
    render(<RoleBadge role="commentary" excluded={false} />);
    expect(screen.getByText("Commentary").closest("[title]")?.getAttribute("title")).toBe(
      "Commentary",
    );
  });

  it("may shrink with its column and truncates instead of wrapping", () => {
    render(<RoleBadge role="commentary" excluded={false} />);
    const chip = screen.getByText("Commentary").closest("[title]") as HTMLElement;
    expect(chip.className).toContain("min-w-0");
    expect(chip.className).toContain("max-w-full");
    expect(chip.className).toContain("truncate");
  });
});
