// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { SecretInput } from "./SecretInput";

/* THE COMPONENT THAT GUARDS EVERY CREDENTIAL IN THE UI HAD NO TESTS.
 *
 * It was written, used on one field out of seven, and never pinned. These are
 * the properties the rest of the app now depends on -- secret-fields.test.ts
 * requires every credential input to BE one of these, which is only worth
 * requiring if the thing being required actually masks.
 *
 * MASKED-BY-DEFAULT IS THE ONE THAT MATTERS. A reveal toggle that starts
 * revealed is not a reveal toggle, and the failure is silent: the field looks
 * fine to whoever built it and leaks to whoever is watching the screen share. */

afterEach(cleanup);

describe("SecretInput", () => {
  it("is masked before anyone asks for it", () => {
    render(<SecretInput value="live_abc123" onChange={() => {}} data-testid="k" />);
    expect(screen.getByTestId("k")).toHaveProperty("type", "password");
  });

  it("reveals only on an explicit press, and re-hides", () => {
    render(<SecretInput value="live_abc123" onChange={() => {}} data-testid="k" />);
    const field = screen.getByTestId("k");
    const toggle = screen.getByRole("button");

    fireEvent.click(toggle);
    expect(field).toHaveProperty("type", "text");

    // Re-hiding matters as much as revealing: an operator who checked a key
    // needs to put it away again before sharing the screen, without a reload.
    fireEvent.click(toggle);
    expect(field).toHaveProperty("type", "password");
  });

  it("does not reveal on hover or focus", () => {
    // The docstring commits to this: revealing must be something you chose, not
    // something that happens because the pointer passed over the field.
    render(<SecretInput value="live_abc123" onChange={() => {}} data-testid="k" />);
    const field = screen.getByTestId("k");
    fireEvent.mouseOver(field);
    fireEvent.focus(field);
    expect(field).toHaveProperty("type", "password");
  });

  it("names the action for a screen reader, and the name tracks the state", () => {
    render(<SecretInput value="x" onChange={() => {}} />);
    const toggle = screen.getByRole("button");
    const hidden = toggle.getAttribute("aria-label");

    fireEvent.click(toggle);
    const shown = toggle.getAttribute("aria-label");

    // The icon alone says nothing without sight, and the two states must not
    // read identically -- otherwise the label is decoration.
    expect(hidden).toBeTruthy();
    expect(shown).toBeTruthy();
    expect(shown).not.toEqual(hidden);
  });

  it("keeps the value out of autofill and spellcheck", () => {
    // A stream key in the browser's autofill store, or underlined red and sent
    // to a spellchecker, is the same leak by a slower route.
    render(<SecretInput value="live_abc123" onChange={() => {}} data-testid="k" />);
    const field = screen.getByTestId("k");
    expect(field.getAttribute("autocomplete")).toBe("off");
    expect(field.getAttribute("spellcheck")).toBe("false");
  });

  it("reports edits to the caller", () => {
    const onChange = vi.fn();
    render(<SecretInput value="" onChange={onChange} data-testid="k" />);
    fireEvent.change(screen.getByTestId("k"), { target: { value: "pasted" } });
    expect(onChange).toHaveBeenCalled();
  });

  it("does not submit the form it sits in", () => {
    // The toggle lives inside dialogs with a save button. A <button> defaulting
    // to type=submit would save the form on reveal.
    render(<SecretInput value="x" onChange={() => {}} />);
    expect(screen.getByRole("button").getAttribute("type")).toBe("button");
  });
});
