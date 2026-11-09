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
    expect((saveBtn() as HTMLButtonElement).disabled).toBe(true);
  });

  it("will not save an enable until the acknowledgement is ticked", () => {
    render(<AutoStopControl sourceId={7} value={undefined} />);
    fireEvent.click(toggle());
    expect(screen.getByText(/ends any live YouTube broadcast among them for good/)).toBeTruthy();
    expect((saveBtn() as HTMLButtonElement).disabled).toBe(true);
    fireEvent.click(saveBtn());
    expect(updateSource).not.toHaveBeenCalled();
  });

  it("sends the whole object in one PUT once acknowledged", async () => {
    const onSaved = vi.fn();
    render(<AutoStopControl sourceId={7} value={undefined} onSaved={onSaved} />);
    fireEvent.click(toggle());
    fireEvent.change(screen.getByLabelText("Seconds with no feed before stopping"), { target: { value: "300" } });
    fireEvent.click(ackBox());
    fireEvent.click(saveBtn());
    await waitFor(() => expect(updateSource).toHaveBeenCalledTimes(1));
    expect(updateSource).toHaveBeenCalledWith(7, {
      ingest: { autoStop: { enabled: true, waitSeconds: 300, acknowledged: true } },
    });
    await waitFor(() => expect(onSaved).toHaveBeenCalledWith({ enabled: true, waitSeconds: 300, acknowledged: true }));
  });

  it("treats an empty wait as the default (0)", async () => {
    render(<AutoStopControl sourceId={7} value={undefined} />);
    fireEvent.click(toggle());
    fireEvent.click(ackBox());
    fireEvent.click(saveBtn());
    await waitFor(() => expect(updateSource).toHaveBeenCalled());
    expect(updateSource).toHaveBeenCalledWith(7, {
      ingest: { autoStop: { enabled: true, waitSeconds: 0, acknowledged: true } },
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

  it("turning it off sends enabled false with the acknowledgement cleared", async () => {
    render(<AutoStopControl sourceId={7} value={{ enabled: true, waitSeconds: 120, acknowledged: true }} />);
    fireEvent.click(toggle());
    fireEvent.click(saveBtn());
    await waitFor(() => expect(updateSource).toHaveBeenCalled());
    expect(updateSource).toHaveBeenCalledWith(7, {
      ingest: { autoStop: { enabled: false, waitSeconds: 120, acknowledged: false } },
    });
  });
});
