// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";

import { ConfirmDestructive } from "./ConfirmDestructive";
import { translate } from "@/lib/i18n";

/* A LIVE ACTION THAT CAN BE UNDONE IS NOT A DELETION (A4-A6, A15, A60).
 *
 * The dialog used to say "Delete" in a red button under a red alert for every
 * action. Stopping a live stream is not deletion, and an operator who has
 * learned that red means "gone for good" will hesitate in the wrong place or
 * click through the right one. The caution tone keeps the confirmation and
 * drops the false alarm; the consequence line names the exact effect. */
describe("ConfirmDestructive tone and consequence", () => {
  afterEach(cleanup);

  it("keeps the destructive look and the Delete label by default, as every caller expects", () => {
    render(
      <ConfirmDestructive
        open
        onOpenChange={() => {}}
        subject={{ unnamed: true }}
        title="Remove"
        description="Gone."
        onConfirm={() => {}}
      />,
    );
    const confirm = screen.getByRole("button", { name: translate("en", "common.delete") });
    expect(confirm.className).toContain("bg-destructive");
  });

  it("caution names its label as Confirm, uses the default button and an amber icon", () => {
    render(
      <ConfirmDestructive
        open
        onOpenChange={() => {}}
        subject={{ unnamed: true }}
        title="Stop this transcode?"
        description="Progress is lost."
        tone="caution"
        onConfirm={() => {}}
      />,
    );
    const confirm = screen.getByRole("button", { name: translate("en", "common.confirm") });
    expect(confirm.className).not.toContain("bg-destructive");
    expect(document.querySelector("svg.text-warn")).not.toBeNull();
    expect(document.querySelector("svg.text-down")).toBeNull();
  });

  it("shows the consequence as the lead line, before the description", () => {
    render(
      <ConfirmDestructive
        open
        onOpenChange={() => {}}
        subject={{ unnamed: true }}
        title="Stop YouTube?"
        description="You can start it again from Go live."
        tone="caution"
        consequence="Stops publishing to YouTube. The broadcast can be restarted."
        onConfirm={() => {}}
      />,
    );
    const lead = screen.getByTestId("confirm-consequence");
    expect(lead.textContent).toBe("Stops publishing to YouTube. The broadcast can be restarted.");
    const description = screen.getByText("You can start it again from Go live.");
    expect(lead.compareDocumentPosition(description) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
  });

  it("runs the confirm action once the caution dialog is confirmed", () => {
    const onConfirm = vi.fn();
    render(
      <ConfirmDestructive
        open
        onOpenChange={() => {}}
        subject={{ unnamed: true }}
        title="Stop?"
        description="x"
        tone="caution"
        onConfirm={onConfirm}
      />,
    );
    fireEvent.click(screen.getByRole("button", { name: translate("en", "common.confirm") }));
    expect(onConfirm).toHaveBeenCalledTimes(1);
  });
});
