// @vitest-environment jsdom

import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";

import { Button } from "@/components/ui/button";

afterEach(cleanup);

/* ARIA-LABEL IS NOT A TOOLTIP, and that asymmetry is the whole subject.
 *
 * Every icon-only button in this codebase already had an accessible name, so
 * a screen-reader user was told exactly what each glyph does. A sighted
 * operator hovering the same button got nothing, because aria-label produces
 * no hover text -- and only one button in the product had been given the
 * second copy by hand.
 *
 * These lock the derivation rather than the wording: what matters is that the
 * words cannot be supplied once and land in only one of the two places.
 */
describe("Button tooltips", () => {
  it("gives an icon button the tooltip its accessible name already says", () => {
    render(<Button size="icon" aria-label="Copy the publish token" />);
    expect(screen.getByRole("button").getAttribute("title")).toBe("Copy the publish token");
  });

  it("does the same for the small icon size, which is the common one here", () => {
    render(<Button size="icon-sm" aria-label="Delete this source" />);
    expect(screen.getByRole("button").getAttribute("title")).toBe("Delete this source");
  });

  it("lets an explicit title win, for when hover and announcement differ", () => {
    render(
      <Button size="icon" aria-label="Delete the source Studio A" title="Delete this source" />,
    );
    expect(screen.getByRole("button").getAttribute("title")).toBe("Delete this source");
  });

  it("leaves a labelled button alone, so no tooltip repeats its own text", () => {
    // A tooltip reading "Start all" over a button reading "Start all" is
    // noise, and it covers the thing it describes while you read it.
    render(
      <Button aria-label="Start all destinations">
        <span>Start all</span>
      </Button>,
    );
    expect(screen.getByRole("button").getAttribute("title")).toBeNull();
  });

  it("adds nothing when there is no accessible name to borrow", () => {
    render(<Button size="icon" />);
    expect(screen.getByRole("button").getAttribute("title")).toBeNull();
  });
});
