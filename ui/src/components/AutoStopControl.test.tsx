// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { AutoStopControl, parseWait } from "./AutoStopControl";
import { LIMITS } from "@/lib/limits";
import { WIRE } from "@/lib/contract/wire";

const updateSource = vi.fn(async (_id: number, _body: unknown): Promise<unknown> => ({}));
vi.mock("@/lib/api", async () => {
  const actual = await vi.importActual<typeof import("@/lib/api")>("@/lib/api");
  return { ...actual, api: { updateSource: (id: number, body: unknown) => updateSource(id, body) } };
});

const saveBtn = () => screen.getByRole("button", { name: /Apply/ });
const toggle = () => screen.getByRole("switch", { name: "Stop on ingest loss" });
const ackBox = () => screen.getByRole("checkbox", { name: /stopping ends YouTube broadcasts/ });

beforeEach(() => updateSource.mockClear());
afterEach(cleanup);

describe("AutoStopControl", () => {
  it("is off by default and shows the default wait as the placeholder", () => {
    render(<AutoStopControl sourceId={7} value={undefined} />);
    expect(toggle().getAttribute("aria-checked")).toBe("false");
    expect(screen.queryByRole("checkbox")).toBeNull();
    const wait = screen.getByLabelText("Seconds with no feed before stopping") as HTMLInputElement;
    expect(wait.placeholder).toBe(String(WIRE.defaults.autoStopWaitSeconds));
    // Nothing pending, no Apply: the Sources page's e2e (and its users) rely on
    // "Apply" meaning "there is something to apply". Mutation: render the button
    // unconditionally. Observed to fail with the button present.
    expect(screen.queryByRole("button", { name: /Apply/ })).toBeNull();
    fireEvent.click(toggle());
    expect(saveBtn()).toBeTruthy();
  });

  it("will not save an enable until the acknowledgement is ticked", () => {
    render(<AutoStopControl sourceId={7} value={undefined} />);
    fireEvent.click(toggle());
    expect(screen.getByText(/ends any live YouTube broadcast among them for good/)).toBeTruthy();
    expect((saveBtn() as HTMLButtonElement).disabled).toBe(true);
    fireEvent.click(saveBtn());
    expect(updateSource).not.toHaveBeenCalled();
  });

  it("sends what changed, with the version it read, in one PUT once acknowledged", async () => {
    const onSaved = vi.fn();
    render(<AutoStopControl sourceId={7} value={undefined} info={{ version: "v1", effectiveWaitSeconds: 120 }} onSaved={onSaved} />);
    fireEvent.click(toggle());
    fireEvent.change(screen.getByLabelText("Seconds with no feed before stopping"), { target: { value: "300" } });
    fireEvent.click(ackBox());
    fireEvent.click(saveBtn());
    await waitFor(() => expect(updateSource).toHaveBeenCalledTimes(1));
    expect(updateSource).toHaveBeenCalledWith(7, {
      ingest: { autoStop: { enabled: true, waitSeconds: 300, acknowledged: true } },
      autoStopVersion: "v1",
    });
    await waitFor(() =>
      expect(onSaved).toHaveBeenCalledWith({ enabled: true, waitSeconds: 300, acknowledged: true }, undefined),
    );
  });

  it("treats an empty wait as the default (0)", async () => {
    render(<AutoStopControl sourceId={7} value={undefined} />);
    fireEvent.click(toggle());
    fireEvent.click(ackBox());
    fireEvent.click(saveBtn());
    await waitFor(() => expect(updateSource).toHaveBeenCalled());
    // The wait is unchanged (default), so it is not sent.
    expect(updateSource).toHaveBeenCalledWith(7, {
      ingest: { autoStop: { enabled: true, acknowledged: true } },
    });
  });

  it("rejects a wait outside the bounds and blocks Save", () => {
    render(<AutoStopControl sourceId={7} value={{ enabled: true, waitSeconds: 120, acknowledged: true }} />);
    const wait = screen.getByLabelText("Seconds with no feed before stopping");
    for (const bad of [String(LIMITS.autoStopWaitSeconds.min - 1), String(LIMITS.autoStopWaitSeconds.max + 1), "1.5"]) {
      fireEvent.change(wait, { target: { value: bad } });
      expect((saveBtn() as HTMLButtonElement).disabled).toBe(true);
    }
    expect(wait.getAttribute("aria-invalid")).toBe("true");
    fireEvent.change(wait, { target: { value: String(LIMITS.autoStopWaitSeconds.min) } });
    expect((saveBtn() as HTMLButtonElement).disabled).toBe(false);
    expect(parseWait(String(LIMITS.autoStopWaitSeconds.max))).toEqual({ ok: true, seconds: LIMITS.autoStopWaitSeconds.max });
  });

  it("shows the server's refusal", async () => {
    updateSource.mockRejectedValueOnce(new Error("autoStop.acknowledged must be true"));
    render(<AutoStopControl sourceId={7} value={undefined} />);
    fireEvent.click(toggle());
    fireEvent.click(ackBox());
    fireEvent.click(saveBtn());
    expect((await screen.findByRole("alert")).textContent).toContain("autoStop.acknowledged must be true");
  });

  it("turning it off sends only enabled false", async () => {
    render(<AutoStopControl sourceId={7} value={{ enabled: true, waitSeconds: 120, acknowledged: true }} />);
    fireEvent.click(toggle());
    fireEvent.click(saveBtn());
    await waitFor(() => expect(updateSource).toHaveBeenCalled());
    expect(updateSource).toHaveBeenCalledWith(7, { ingest: { autoStop: { enabled: false } } });
  });

  // A control left open on an old state edits only the wait. It must not
  // re-send the enabled+acknowledged it loaded: another panel may have turned
  // the setting off, and that re-send would turn it back on with a valid
  // acknowledgement.
  //
  // Mutation: send { enabled, waitSeconds, acknowledged } whole (the old save).
  // Observed to fail with the body carrying enabled and acknowledged.
  it("an edit of only the wait sends only the wait, never the loaded acknowledgement", async () => {
    render(
      <AutoStopControl
        sourceId={7}
        value={{ enabled: true, waitSeconds: 120, acknowledged: true }}
        info={{ version: "v1", effectiveWaitSeconds: 120 }}
      />,
    );
    fireEvent.change(screen.getByLabelText("Seconds with no feed before stopping"), { target: { value: "300" } });
    fireEvent.click(saveBtn());
    await waitFor(() => expect(updateSource).toHaveBeenCalled());
    expect(updateSource).toHaveBeenCalledWith(7, {
      ingest: { autoStop: { waitSeconds: 300 } },
      autoStopVersion: "v1",
    });
  });

  // Mutation: send acknowledged whenever the loaded value had it. Observed to
  // fail with acknowledged in a save that only turns the setting off and on.
  it("acknowledged travels only when the box was ticked in this edit", async () => {
    render(<AutoStopControl sourceId={7} value={{ enabled: false, waitSeconds: 0, acknowledged: true }} />);
    fireEvent.click(toggle());
    // The leftover acknowledgement does not stand in for the tick.
    expect((saveBtn() as HTMLButtonElement).disabled).toBe(true);
    fireEvent.click(ackBox());
    fireEvent.click(saveBtn());
    await waitFor(() => expect(updateSource).toHaveBeenCalled());
    expect(updateSource).toHaveBeenCalledWith(7, { ingest: { autoStop: { enabled: true, acknowledged: true } } });
  });

  describe("the effective wait", () => {
    const stored = { enabled: true, waitSeconds: 120, acknowledged: true };
    it("is shown for the stored setting", () => {
      render(<AutoStopControl sourceId={7} value={stored} info={{ version: "v", effectiveWaitSeconds: 120 }} />);
      expect(screen.getByTestId("autostop-effective").textContent).toBe("Effective wait: 120 s");
    });
    it("says what raised it", () => {
      const { rerender } = render(
        <AutoStopControl sourceId={7} value={stored} info={{ version: "v", effectiveWaitSeconds: 330, raisedBy: "failover" }} />,
      );
      expect(screen.getByTestId("autostop-effective").textContent).toMatch(/^Effective wait: 330 s, .*failover grace/);
      rerender(
        <AutoStopControl sourceId={7} value={stored} info={{ version: "v", effectiveWaitSeconds: 3630, raisedBy: "pull" }} />,
      );
      expect(screen.getByTestId("autostop-effective").textContent).toMatch(/^Effective wait: 3630 s, .*reconnect delay/);
    });
    it("is not shown while the setting is off or the wait is being edited", () => {
      const { rerender } = render(
        <AutoStopControl sourceId={7} value={undefined} info={{ version: "v", effectiveWaitSeconds: 120 }} />,
      );
      expect(screen.queryByTestId("autostop-effective")).toBeNull();
      rerender(<AutoStopControl sourceId={7} value={stored} info={{ version: "v", effectiveWaitSeconds: 120 }} />);
      fireEvent.change(screen.getByLabelText("Seconds with no feed before stopping"), { target: { value: "300" } });
      expect(screen.queryByTestId("autostop-effective")).toBeNull();
    });
  });
});
