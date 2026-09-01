// @vitest-environment jsdom

import { afterEach, describe, expect, it } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { SecretCode } from "./SecretCode";

/* THE READ-ONLY HALF OF THE SAME PROMISE.
 *
 * SecretInput masks a credential being typed; this masks one being displayed,
 * which is the more exposed case because nothing about a printed value suggests
 * it is sensitive. secret-fields.test.ts now REQUIRES this component wherever a
 * credential is printed, and a requirement is only worth having if the thing
 * required actually masks.
 *
 * MASKED-BY-DEFAULT IS THE PROPERTY. A reveal that starts revealed is not a
 * reveal, and the failure is silent -- it looks correct to whoever wrote it and
 * leaks to whoever is watching the screen share. */

afterEach(cleanup);

// Obviously synthetic, and it has to stay that way. The first version of this
// file used a real publish token copied off a screenshot, which gitleaks
// correctly refused: a fixture that LOOKS like a credential is one, as far as
// every scanner and every reader is concerned. The token's own alphabet is what
// makes a realistic-looking fixture dangerous here, so the fixture says what it
// is instead.
const SECRET = "EXAMPLE-NOT-A-REAL-TOKEN-0000000000";

describe("SecretCode", () => {
  it("does not put the value in the DOM until revealed", () => {
    const { container } = render(<SecretCode value={SECRET} />);
    // Not "is not visible" -- not PRESENT. A value hidden by CSS is still in
    // the page source, still copied by select-all, still in a screenshot of
    // devtools.
    expect(container.textContent).not.toContain(SECRET);
  });

  it("masks to a fixed width rather than one dot per character", () => {
    const short = render(<SecretCode value="abc" />).container.textContent;
    cleanup();
    const long = render(<SecretCode value={SECRET} />).container.textContent;
    // A mask that tracks length still answers "how long is it", which for a
    // token of known alphabet is the one free question an onlooker gets.
    expect(short).toBe(long);
  });

  it("shows the value after the reveal is pressed", () => {
    const { container } = render(<SecretCode value={SECRET} />);
    fireEvent.click(screen.getByRole("button"));
    expect(container.textContent).toContain(SECRET);
  });

  it("hides it again on a second press", () => {
    const { container } = render(<SecretCode value={SECRET} />);
    const btn = screen.getByRole("button");
    fireEvent.click(btn);
    fireEvent.click(btn);
    expect(container.textContent).not.toContain(SECRET);
  });

  it("reports its state to a screen reader", () => {
    // The icon alone says nothing without sight, and this control guards a
    // credential -- the one place a reader most needs to know what pressing it
    // will do before pressing it.
    render(<SecretCode value={SECRET} />);
    const btn = screen.getByRole("button");
    expect(btn.getAttribute("aria-pressed")).toBe("false");
    fireEvent.click(btn);
    expect(btn.getAttribute("aria-pressed")).toBe("true");
  });

  it("renders an empty value without leaking a mask that suggests content", () => {
    // A source with no token yet. The mask is fixed-width by design, so this
    // documents that an absent secret still draws one rather than pretending
    // to hold something -- callers gate on the value, not on this component.
    const { container } = render(<SecretCode value="" />);
    expect(container.querySelector("code")).not.toBeNull();
  });
});
