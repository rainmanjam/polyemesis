// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen, waitFor } from "@testing-library/react";

import { HooksCard } from "./HooksCard";
import { api } from "@/lib/api";

/* "No webhooks." AND "Sent 0 / Failed 0 / Dropped 0" ARE TWO CLAIMS, NOT NONE.
 *
 * `load()` caught into a toast and left `hooks: []` and `meta: null`. The toast
 * is four seconds long and gone; the two false sentences stay for as long as
 * the tab is open. "Failed 0" and "Dropped 0" are precisely the readings an
 * operator checks when a receiver has gone quiet, so the zeros are the half
 * that stops them looking.
 */

const list = vi.spyOn(api.hooks, "list");
const meta = vi.spyOn(api.hooks, "meta");

const emptyMeta = () =>
  ({
    specVersion: "1",
    headers: { signature: "X-Sig" },
    triggers: [],
    stats: { sent: 0, failed: 0, endpoints: 0, queued: 0, dropped: 0, retries: 0 },
  }) as never;

describe("HooksCard: a read that did not answer", () => {
  beforeEach(() => {
    list.mockReset();
    meta.mockReset();
  });
  afterEach(cleanup);

  it("does not render an empty list or six zeros when the read failed", async () => {
    list.mockRejectedValue(new Error("unreachable"));
    meta.mockRejectedValue(new Error("unreachable"));
    render(<HooksCard />);

    // Both halves say it: the list, and the delivery grid.
    await waitFor(() => expect(screen.getAllByText(/could not be read/i)).toHaveLength(2));
    // The empty state is a positive claim -- it says the server answered and
    // there is nothing configured.
    expect(screen.queryByText(/^No webhooks\./)).toBeNull();
    // And the delivery grid must be gone entirely, not merely accompanied by a
    // warning: a reassuring zero beside a warning is still a reassuring zero.
    expect(screen.queryByText("Sent")).toBeNull();
    expect(screen.queryByText("Dropped")).toBeNull();
  });

  it("still shows the empty state and the real figures when the server answered", async () => {
    list.mockResolvedValue([]);
    meta.mockResolvedValue(emptyMeta());
    render(<HooksCard />);

    await waitFor(() => expect(screen.getByText(/^No webhooks\./)).toBeTruthy());
    expect(screen.getByText("Sent")).toBeTruthy();
    expect(screen.getByText("Dropped")).toBeTruthy();
  });
});
