// @vitest-environment jsdom
//
// The shared kit components: EmptyState, StatusPill, DisabledReason and
// SaveStatus. jsdom is opted into per file because every one of them renders
// through the DOM and Radix portals would otherwise come back empty.
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { Inbox } from "lucide-react";

import { EmptyState } from "./EmptyState";
import { StatusPill } from "./StatusPill";
import { DisabledReason } from "./DisabledReason";
import { disabledDescribedBy } from "@/lib/disabledReason";
import { SaveStatus } from "./SaveStatus";
import { destinationStatus, processStatus } from "@/lib/status";

afterEach(cleanup);

describe("EmptyState (A83)", () => {
  it("shows the title, the one-line body and the single primary action", () => {
    const onClick = vi.fn();
    render(
      <EmptyState
        icon={Inbox}
        title="No destinations yet"
        body="Add one to send the stream somewhere."
        action={{ label: "Add destination", onClick }}
      />,
    );
    expect(screen.getByText("No destinations yet")).toBeTruthy();
    expect(screen.getByText("Add one to send the stream somewhere.")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Add destination" }));
    expect(onClick).toHaveBeenCalledTimes(1);
  });

  it("renders with no action and no body when only a title is given", () => {
    render(<EmptyState title="Nothing here" />);
    expect(screen.getByText("Nothing here")).toBeTruthy();
    expect(screen.queryByRole("button")).toBeNull();
  });

  it("gives two empty states on one page distinct reason ids", () => {
    const action = { label: "Clip now", onClick: () => {}, disabled: true, disabledReason: "Turn the buffer on first." };
    render(
      <>
        <EmptyState title="A" action={action} />
        <EmptyState title="B" action={{ ...action, disabledReason: "Other reason." }} />
      </>,
    );
    const [first, second] = screen.getAllByRole("button", { name: "Clip now" });
    const firstReason = first.getAttribute("aria-describedby");
    const secondReason = second.getAttribute("aria-describedby");
    expect(firstReason).toBeTruthy();
    expect(firstReason).not.toBe(secondReason);
    expect(document.getElementById(firstReason as string)?.textContent).toBe("Turn the buffer on first.");
    expect(document.getElementById(secondReason as string)?.textContent).toBe("Other reason.");
  });

  it("shows a disabled action's reason as visible text linked by aria-describedby", () => {
    render(
      <EmptyState
        title="No clips yet"
        action={{ label: "Clip now", onClick: () => {}, disabled: true, disabledReason: "Turn the buffer on first." }}
      />,
    );
    const button = screen.getByRole("button", { name: "Clip now" });
    expect((button as HTMLButtonElement).disabled).toBe(true);
    const reason = screen.getByText("Turn the buffer on first.");
    expect(button.getAttribute("aria-describedby")).toBe(reason.id);
  });
});

describe("StatusPill (A11, A91)", () => {
  it("prints the translated word and carries the tone, not only the colour", () => {
    const { container } = render(<StatusPill reading={processStatus("running")} />);
    expect(screen.getByText("Live")).toBeTruthy();
    const pill = container.querySelector("[data-tone]");
    expect(pill?.getAttribute("data-tone")).toBe("ok");
  });

  it("prints a failure as Failed in the bad tone, never as the idle Stopped word", () => {
    const { container } = render(<StatusPill reading={processStatus("failed")} />);
    expect(screen.getByText("Failed")).toBeTruthy();
    expect(container.querySelector("[data-tone]")?.getAttribute("data-tone")).toBe("bad");
  });

  it("prints a disabled destination as Stopped in the idle tone", () => {
    const { container } = render(<StatusPill reading={destinationStatus(false, "running")} />);
    expect(screen.getByText("Stopped")).toBeTruthy();
    expect(container.querySelector("[data-tone]")?.getAttribute("data-tone")).toBe("idle");
  });
});

describe("DisabledReason (A28, A61)", () => {
  it("renders nothing while the control is enabled", () => {
    const { container } = render(<DisabledReason id="r" reason={null} />);
    expect(container.innerHTML).toBe("");
  });

  it("renders the reason as visible text with the id the control points at", () => {
    render(<DisabledReason id="publish-reason" reason="Add a destination first." />);
    const el = screen.getByText("Add a destination first.");
    expect(el.closest("p")?.id).toBe("publish-reason");
  });

  it("links a control to the reason only while a reason is showing", () => {
    expect(disabledDescribedBy("r", "Add a destination first.")).toBe("r");
    expect(disabledDescribedBy("r", null)).toBeUndefined();
    expect(disabledDescribedBy("r", "")).toBeUndefined();
  });
});

describe("SaveStatus (A95, A20)", () => {
  it("is silent while clean, but keeps its live region mounted", () => {
    render(<SaveStatus state="clean" />);
    const region = screen.getByRole("status");
    expect(region.textContent).toBe("");
    expect(region.getAttribute("aria-live")).toBe("polite");
  });

  it("says Unsaved changes while dirty and Saving with a spinner while a write is in flight", () => {
    const { container, rerender } = render(<SaveStatus state="dirty" />);
    expect(screen.getByRole("status").textContent).toBe("Unsaved changes");
    rerender(<SaveStatus state="saving" />);
    expect(screen.getByRole("status").textContent).toBe("Saving…");
    expect(container.querySelector("svg.animate-spin")).not.toBeNull();
  });

  it("announces Saved politely and Not saved in the bad tone", () => {
    const { rerender } = render(<SaveStatus state="saved" />);
    const saved = screen.getByRole("status");
    expect(saved.getAttribute("aria-live")).toBe("polite");
    expect(saved.textContent).toBe("Saved");
    rerender(<SaveStatus state="failed" />);
    expect(screen.getByRole("status").textContent).toBe("Not saved. Try again.");
    expect(screen.getByRole("status").className).toContain("text-down");
  });
});
