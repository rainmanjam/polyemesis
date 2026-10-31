// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";

import { ChatEmpty } from "./ChatPanel";
import { translate } from "@/lib/i18n";

/* The kit's EmptyState carries the primary action where one exists. Chat's
 * "no account connected" state repeated the side note's sentence without its
 * "Open platform credentials" link, so the one next step was only in the
 * sidebar. It is now the empty state's button. */

afterEach(cleanup);

describe("ChatEmpty with no account connected", () => {
  it("offers Open platform credentials as its action", () => {
    const open = vi.fn();
    render(<ChatEmpty loading={false} configured error="" statuses={[]} onOpenCredentials={open} />);
    expect(screen.getByText(translate("en", "chatpage.emptyNoAccountTitle"))).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: translate("en", "chatpage.openCredentials") }));
    expect(open).toHaveBeenCalledTimes(1);
  });

  it("has no action in a state whose next step is not on the credentials page", () => {
    render(<ChatEmpty loading={false} configured={false} error="" statuses={[]} onOpenCredentials={() => {}} />);
    expect(screen.queryByRole("button")).toBeNull();
  });
});
