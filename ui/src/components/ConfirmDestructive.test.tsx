// @vitest-environment jsdom
//
// Opted in per file rather than by changing the project default. This is the
// only test here that needs a DOM, and it needs one because Radix renders the
// dialog through a PORTAL -- renderToStaticMarkup returns an empty string for
// it, which is how the first version of this test "passed" nothing at all.
import { afterEach, describe, expect, it } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";

import { ConfirmDestructive } from "./ConfirmDestructive";

/* A DIALOG MUST NOT ASK FOR A STRING IT IS MISSPELLING.
 *
 * requireTyping gates the action on an EXACT match against the subject. Label's
 * base classes carry `uppercase`, and the subject was rendered inside the Label
 * -- so a source named "Main" was shown as "MAIN" while the input demanded
 * "Main". Reported from the product: "when I type in MAIN, the delete button
 * never becomes active". They typed exactly what the dialog showed them.
 *
 * Nothing on screen could have corrected them. The placeholder does render the
 * true case, but a placeholder disappears the moment you type a character.
 *
 * It reached everything using requireTyping, not just sources: webhook names,
 * destination names, uploaded filenames. Anything not already all-caps.
 */
describe("ConfirmDestructive", () => {
  afterEach(cleanup);

  const show = (subject: string) =>
    render(
      <ConfirmDestructive
        open
        onOpenChange={() => {}}
        subject={subject}
        title="Delete"
        description="This cannot be undone."
        confirmLabel="Delete"
        requireTyping
        onConfirm={() => {}}
      />,
    );

  it("renders the subject in its own case, not the Label's", () => {
    show("Main");
    const el = screen.getByText("Main", { selector: "span" });
    expect(
      el.className,
      "the subject inherits Label's uppercase, so a source named Main is DISPLAYED as " +
        "MAIN while the input demands Main -- typing what is shown can never unlock it",
    ).toContain("normal-case");
  });

  it("shows the subject verbatim, whatever its case", () => {
    show("my-webhook");
    expect(screen.getByText("my-webhook", { selector: "span" })).toBeTruthy();
  });
});

/* AN EMPTY SUBJECT MUST NOT UNLOCK THE TYPED CHALLENGE.
 *
 * `"".trim() === ""` is true, so `requireTyping` with an empty subject handed
 * the operator a live Delete button before they touched anything -- the
 * friction absent from exactly the action that asked for it. The Label reads
 * "Type  to confirm" with a blank where the name should be, so there is
 * nothing on screen to notice either.
 *
 * Hardening rather than a live hazard: every current caller passes a
 * server-validated non-empty name. The point is that the guarantee lives HERE,
 * so an eighth caller writing `subject={dest?.name ?? ""}` cannot switch the
 * control off by accident.
 */
describe("ConfirmDestructive: the typed challenge with no subject", () => {
  afterEach(cleanup);

  const showWith = (subject: string) =>
    render(
      <ConfirmDestructive
        open
        onOpenChange={() => {}}
        subject={subject}
        title="Delete"
        description="This cannot be undone."
        confirmLabel="Delete"
        requireTyping
        onConfirm={() => {}}
      />,
    );

  it("keeps the button locked when the subject is empty and nothing was typed", () => {
    showWith("");
    const button = screen.getByRole("button", { name: "Delete" });
    expect(
      (button as HTMLButtonElement).disabled,
      "requireTyping with an empty subject unlocked before the operator typed anything: " +
        "the confirmation was on screen with its control already off",
    ).toBe(true);
  });

  it("still unlocks for a real subject once it is typed", () => {
    showWith("Main");
    const input = screen.getByPlaceholderText("Main");
    fireEvent.change(input, { target: { value: "Main" } });
    expect((screen.getByRole("button", { name: "Delete" }) as HTMLButtonElement).disabled).toBe(
      false,
    );
  });
});
