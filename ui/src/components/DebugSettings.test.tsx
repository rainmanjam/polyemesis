// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, render, screen, waitFor } from "@testing-library/react";

import { DebugSettings } from "./DebugSettings";

/* THE CONTROL THAT HANDS A COPY OF THE SERVER'S LOGS TO SOMEBODY ELSE.
 *
 * The Playwright spec drives this against the shipped container and is the
 * stronger evidence — it proves a real file arrives. What it cannot do cheaply
 * is enumerate the states: an export offered when the buffer is empty, a
 * truncated capture that does not say so, an error that never reaches the
 * operator, a poll that keeps running after recording stops.
 *
 * THE ASSERTION THAT MATTERS MOST IS THAT THE DIALOG GATES THE EXPORT. Not that
 * a dialog appears — that it is the ONLY route to api.exportDebug. A button
 * that fired regardless would still show a dialog, still look diligent, and
 * still have sent the file.
 *
 * jsdom rather than the suite's node environment, for the reason set out in
 * useFacebookStreamHealth.test.tsx: these behaviours ARE effects, and
 * renderToStaticMarkup does not run them.
 */

const debugState = vi.fn();
const setDebug = vi.fn();
const exportDebug = vi.fn();

vi.mock("@/lib/api", async () => {
  const actual = await vi.importActual<typeof import("@/lib/api")>("@/lib/api");
  return {
    ...actual,
    api: {
      debugState: () => debugState(),
      setDebug: (b: unknown) => setDebug(b),
      exportDebug: () => exportDebug(),
    },
  };
});

const state = (over: Partial<Record<string, unknown>> = {}) => ({
  recording: false,
  level: "INFO",
  held: 0,
  seen: 0,
  capacity: 5000,
  bytes: 0,
  recordsTruncated: 0,
  ...over,
});

beforeEach(() => {
  vi.useFakeTimers({ shouldAdvanceTime: true });
  debugState.mockReset();
  setDebug.mockReset();
  exportDebug.mockReset();
  debugState.mockResolvedValue(state());
  setDebug.mockImplementation(async (b: { recording?: boolean; reset?: boolean }) =>
    state({ recording: b.recording ?? false, held: b.reset ? 0 : 12, seen: b.reset ? 0 : 12 }),
  );
  // jsdom has neither, and the component hands the browser a blob to save.
  globalThis.URL.createObjectURL = vi.fn(() => "blob:test");
  globalThis.URL.revokeObjectURL = vi.fn();
});

afterEach(() => {
  cleanup();
  vi.useRealTimers();
  vi.restoreAllMocks();
});

const exportButton = () =>
  screen.getByRole("button", { name: /export bundle/i }) as HTMLButtonElement;
const recordSwitch = () =>
  screen.getByRole("switch", { name: /record server activity/i });

/** Radix renders the switch state onto data-state; jest-dom is not installed,
 *  so this reads the attribute directly rather than through a matcher. */
const switchState = () => recordSwitch().getAttribute("data-state");

async function tick(seconds = 2) {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(seconds * 1000 + 50);
  });
}

describe("DebugSettings", () => {
  it("offers no export while the buffer is empty", async () => {
    render(<DebugSettings />);
    await waitFor(() => expect(exportButton()).toBeDefined());

    // An export of nothing spends a Critical audit entry on no disclosure, and
    // hands somebody a file with no diagnostic in it.
    expect(exportButton().disabled).toBe(true);
    expect(
      (screen.getByRole("button", { name: /clear buffer/i }) as HTMLButtonElement).disabled,
    ).toBe(true);
  });

  it("turning the switch on starts recording", async () => {
    render(<DebugSettings />);
    await waitFor(() => expect(recordSwitch()).toBeDefined());

    await act(async () => {
      recordSwitch().click();
    });

    expect(setDebug).toHaveBeenCalledWith({ recording: true });
    await waitFor(() => expect(switchState()).toBe("checked"));
  });

  it("SAYS a capture was truncated rather than leaving it to arithmetic", async () => {
    // A ring that dropped its oldest records and shows the rest as though they
    // were all of them is how an engineer reads a capture and concludes the
    // fault left no trace.
    debugState.mockResolvedValue(state({ recording: true, held: 5000, seen: 22431 }));
    render(<DebugSettings />);

    await waitFor(() => expect(screen.getByText(/22,431/)).toBeDefined());
    expect(screen.getByText(/oldest dropped/i)).toBeDefined();
  });

  it("does not claim truncation when nothing was dropped", async () => {
    debugState.mockResolvedValue(state({ recording: true, held: 12, seen: 12 }));
    render(<DebugSettings />);

    await waitFor(() => expect(exportButton().disabled).toBe(false));
    expect(screen.queryByText(/oldest dropped/i)).toBeNull();
  });

  it("polls WHILE RECORDING and stops when it is off", async () => {
    debugState.mockResolvedValue(state({ recording: true, held: 3, seen: 3 }));
    render(<DebugSettings />);
    await waitFor(() => expect(exportButton().disabled).toBe(false));

    const afterLoad = debugState.mock.calls.length;
    await tick();
    expect(debugState.mock.calls.length).toBeGreaterThan(afterLoad);

    // Off, nothing changes, so nothing should be asked. A panel that keeps
    // polling an idle recorder is a request every two seconds for a number that
    // cannot move.
    debugState.mockResolvedValue(state({ recording: false, held: 3, seen: 3 }));
    setDebug.mockResolvedValue(state({ recording: false, held: 3, seen: 3 }));
    await act(async () => {
      recordSwitch().click();
    });
    await waitFor(() => expect(switchState()).toBe("unchecked"));

    const afterStop = debugState.mock.calls.length;
    await tick(6);
    expect(debugState.mock.calls.length).toBe(afterStop);
  });

  // THE GATE. Opening the dialog must not export; only confirming may.
  it("the export is gated by the confirmation", async () => {
    debugState.mockResolvedValue(state({ recording: true, held: 12, seen: 12 }));
    render(<DebugSettings />);
    await waitFor(() => expect(exportButton().disabled).toBe(false));

    await act(async () => {
      exportButton().click();
    });

    // The dialog is open and names the count, because confirming a number is a
    // decision and confirming a vibe is a click.
    const dialog = await screen.findByRole("dialog");
    expect(dialog).toBeDefined();
    expect(dialog.textContent).toMatch(/log lines/i);
    expect(dialog.textContent).toMatch(/12/);

    // AND NOTHING HAS BEEN EXPORTED YET.
    expect(exportDebug).not.toHaveBeenCalled();
  });

  it("confirming exports and hands the browser a file", async () => {
    debugState.mockResolvedValue(state({ recording: true, held: 12, seen: 12 }));
    exportDebug.mockResolvedValue({
      text: '{"records":[]}',
      filename: "polyemesis-debug-20260817-120000.json",
    });
    render(<DebugSettings />);
    await waitFor(() => expect(exportButton().disabled).toBe(false));

    await act(async () => {
      exportButton().click();
    });
    const dialog = await screen.findByRole("dialog");

    const confirm = Array.from(dialog.querySelectorAll("button")).find((b) =>
      /^export$/i.test(b.textContent?.trim() ?? ""),
    );
    expect(confirm).toBeDefined();
    await act(async () => {
      confirm!.click();
    });

    await waitFor(() => expect(exportDebug).toHaveBeenCalledTimes(1));
    // The blob is what the operator ends up with; a POST fetched into memory
    // that never reaches the disk is an export that silently did nothing.
    expect(globalThis.URL.createObjectURL).toHaveBeenCalled();
  });

  // THE SIZE IS PART OF THE DECISION, not decoration. The count says how much
  // diagnostic is in the bundle; the size decides whether it can be attached to
  // the thread the operator was about to attach it to.
  it("states the SIZE in the confirmation, not only the line count", async () => {
    debugState.mockResolvedValue(
      state({ recording: true, held: 12, seen: 12, bytes: 3_500_000 }),
    );
    render(<DebugSettings />);
    await waitFor(() => expect(exportButton().disabled).toBe(false));

    await act(async () => {
      exportButton().click();
    });
    const dialog = await screen.findByRole("dialog");

    // 3,500,000 bytes reads as MB, not as a raw byte count nobody can judge.
    expect(dialog.textContent).toMatch(/3\.3 MB/);
    expect(dialog.textContent).toMatch(/12/);
  });

  // A LINE CUT AT THE PER-RECORD CAP IS A DIFFERENT CLAIM from a line the ring
  // dropped, and an engineer told "the log just stops" needs to know which.
  it("says when long lines were shortened, distinctly from dropped ones", async () => {
    debugState.mockResolvedValue(
      state({ recording: true, held: 40, seen: 40, bytes: 9000, recordsTruncated: 3 }),
    );
    render(<DebugSettings />);

    await waitFor(() => expect(screen.getByText(/long lines shortened/i)).toBeDefined());
    expect(screen.getByText(/3 long lines shortened/i)).toBeDefined();
    // Nothing was DROPPED here — seen equals held — so the other claim must not
    // appear. Conflating them would send an engineer looking for missing lines
    // that are not missing.
    expect(screen.queryByText(/oldest dropped/i)).toBeNull();
  });

  it("makes no truncation claim when nothing was cut", async () => {
    debugState.mockResolvedValue(
      state({ recording: true, held: 12, seen: 12, bytes: 4096, recordsTruncated: 0 }),
    );
    render(<DebugSettings />);

    await waitFor(() => expect(exportButton().disabled).toBe(false));
    expect(screen.queryByText(/shortened/i)).toBeNull();
  });

  it("shows a failure instead of swallowing it", async () => {
    debugState.mockRejectedValue(new Error("the server is not answering"));
    render(<DebugSettings />);

    // An operator who pressed something and saw nothing happen has no next
    // move; the message is the whole difference.
    const alert = await screen.findByRole("alert");
    expect(alert.textContent).toMatch(/not answering/i);
  });
});
