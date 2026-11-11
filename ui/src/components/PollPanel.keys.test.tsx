// @vitest-environment jsdom

import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";

import { PollPanel } from "./PollPanel";
import { api } from "@/lib/api";
import type { Poll, PollsResponse } from "@/lib/polls";

/* Option rows are an editable list. Each row needs a React key that survives
 * a delete in the middle, or the rows after it reuse the wrong DOM node. The
 * keys are client-only, so the payload the server receives must not carry them. */

const list = vi.spyOn(api, "listPolls");
const start = vi.spyOn(api, "startPoll");

const running: Poll = {
  id: 9,
  question: "Q?",
  options: [{ label: "A", votes: 0, byPlatform: {} }, { label: "C", votes: 0, byPlatform: {} }],
  total: 0,
  byPlatform: {},
  state: "open",
  durationSeconds: 60,
  startedAt: new Date().toISOString(),
  endsAt: new Date(Date.now() + 60_000).toISOString(),
  announced: [],
  native: [],
};

beforeAll(() => {
  (window as unknown as { ResizeObserver?: unknown }).ResizeObserver ??= class {
    observe() {}
    unobserve() {}
    disconnect() {}
  };
});

beforeEach(() => {
  list.mockReset();
  start.mockReset();
  list.mockResolvedValue({ current: null, history: [] } as PollsResponse);
  start.mockResolvedValue(running);
});
afterEach(cleanup);

describe("poll option rows keep their identity when a middle row is removed", () => {
  it("keeps the last row's input node and value after deleting the middle row", async () => {
    render(<PollPanel />);
    fireEvent.change(await screen.findByLabelText("Question"), { target: { value: "Q?" } });
    fireEvent.change(screen.getByLabelText("Option 1"), { target: { value: "A" } });
    fireEvent.change(screen.getByLabelText("Option 2"), { target: { value: "B" } });
    fireEvent.click(screen.getByRole("button", { name: /Add an option/ }));
    fireEvent.change(screen.getByLabelText("Option 3"), { target: { value: "C" } });

    const cNode = screen.getByLabelText("Option 3") as HTMLInputElement;
    fireEvent.click(screen.getByRole("button", { name: "Remove option 2" }));

    // With index keys React would unmount the old row 3 node and reuse the
    // node for row 2, so the original C node would be detached.
    expect(cNode.isConnected).toBe(true);
    expect(cNode.value).toBe("C");
    expect((screen.getByLabelText("Option 2") as HTMLInputElement).value).toBe("C");
    expect(screen.queryByLabelText("Option 3")).toBeNull();
  });

  it("sends only the option text, with no row keys, after a middle delete", async () => {
    render(<PollPanel />);
    fireEvent.change(await screen.findByLabelText("Question"), { target: { value: "Q?" } });
    fireEvent.change(screen.getByLabelText("Option 1"), { target: { value: "A" } });
    fireEvent.change(screen.getByLabelText("Option 2"), { target: { value: "B" } });
    fireEvent.click(screen.getByRole("button", { name: /Add an option/ }));
    fireEvent.change(screen.getByLabelText("Option 3"), { target: { value: "C" } });
    fireEvent.click(screen.getByRole("button", { name: "Remove option 2" }));
    fireEvent.click(screen.getByRole("button", { name: /Start poll/ }));
    await waitFor(() => expect(start).toHaveBeenCalled());
    expect(start.mock.calls[0][0]).toEqual(
      expect.objectContaining({ question: "Q?", options: ["A", "C"] }),
    );
  });
});
