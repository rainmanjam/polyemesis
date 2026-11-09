// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { ObsBridgeSettings } from "./ObsBridgeSettings";
import type { ObsConfig, ObsResponse } from "@/lib/types";

/* The OBS bridge section (#16). The server decides everything -- the stop
 * acknowledgement, the programme rule, which scene switches go live -- so what
 * is asserted here is that the page sends what the operator set, shows what
 * the server reports, and keeps the password out of the configuration. */

const config = (o: Partial<ObsConfig> = {}): ObsConfig => ({
  enabled: true,
  connect: true,
  url: "ws://192.168.1.20:4455",
  hasPassword: true,
  sourceId: 1,
  destinations: [3],
  startOnStream: true,
  stopOnStream: false,
  stopAcknowledged: false,
  stopGraceSeconds: 0,
  scenes: [{ scene: "Just Chatting", preset: "no-music" }],
  ...o,
});

const response = (o: Partial<ObsConfig> = {}): ObsResponse => ({
  config: config(o),
  status: {
    connection: { state: "connected", detail: "connected to obs-websocket", since: "2026-10-30T12:00:00Z" },
    streaming: true,
    scene: "Gameplay",
    lastActions: [
      {
        at: "2026-10-30T12:00:05Z",
        action: "start",
        via: "websocket",
        results: [{ id: 3, name: "YouTube", outcome: "started" }],
      },
      {
        at: "2026-10-30T12:01:05Z",
        action: "scene",
        via: "script",
        scene: "Just Chatting",
        preset: "no-music",
        results: [{ id: 3, name: "YouTube", outcome: "skipped", message: "needs a restart" }],
      },
    ],
  },
});

const ingest = { mode: "srt" };
let sourcesList: unknown[] = [];
const resetSources = () => {
  sourcesList = [
    { id: 1, name: "Studio A", isDefault: false, ingest: { ...ingest } },
    { id: 2, name: "Studio B", isDefault: true, ingest: { ...ingest } },
  ];
};
resetSources();
const updateSource = vi.fn(async (_id: number, _body: unknown) => ({}));
const getObs = vi.fn(async () => response());
const putObs = vi.fn(async (c: ObsConfig) => ({ ...response(), config: c }));
const putObsPassword = vi.fn(async (pw: string) => ({ hasPassword: pw !== "" }));

vi.mock("@/lib/api", async () => {
  const actual = await vi.importActual<typeof import("@/lib/api")>("@/lib/api");
  return {
    ...actual,
    api: {
      getObs: () => getObs(),
      putObs: (c: ObsConfig) => putObs(c),
      putObsPassword: (pw: string) => putObsPassword(pw),
      listSources: async () => sourcesList,
      updateSource: (id: number, body: unknown) => updateSource(id, body),
      listDestinations: async () => [
        { destination: { id: 3, name: "YouTube", sourceId: 1 } },
        { destination: { id: 4, name: "Twitch", sourceId: 1 } },
        { destination: { id: 5, name: "Kick", sourceId: 2 } },
      ],
      listPresets: async () => ({
        presets: [
          { id: "no-music", name: "No music" },
          { id: "mic-only", name: "Mic only" },
        ],
        defaults: { musicTrack: 0, micTrack: 2, surroundTrack: 0, cleanTrack: 1 },
      }),
    },
  };
});
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));

beforeEach(() => {
  getObs.mockClear();
  putObs.mockClear();
  putObsPassword.mockClear();
  updateSource.mockClear();
  getObs.mockImplementation(async () => response());
  resetSources();
});
afterEach(cleanup);

describe("the OBS bridge settings", () => {
  it("shows the connection the server reports and what the bridge last did", async () => {
    render(<ObsBridgeSettings />);
    const status = await screen.findByTestId("obs-status");
    expect(status.textContent).toContain("Connected");
    expect(status.textContent).toContain("OBS is streaming");
    expect(status.textContent).toContain("Gameplay");
    expect(screen.getByText(/YouTube: started/)).toBeTruthy();
    expect(screen.getByText(/needs a restart/)).toBeTruthy();
  });

  it("lists only the programme's destinations and sends the selection it was given", async () => {
    render(<ObsBridgeSettings />);
    await screen.findByTestId("obs-status");
    expect(screen.getByText("Twitch")).toBeTruthy();
    expect(screen.queryByText("Kick")).toBeNull();
    fireEvent.click(screen.getByText("Twitch").closest("label")!.querySelector("button")!);
    fireEvent.click(screen.getByRole("button", { name: /Save/ }));
    await waitFor(() => expect(putObs).toHaveBeenCalled());
    const sent = putObs.mock.calls[0][0];
    expect(sent.destinations).toEqual([3, 4]);
    expect(sent.sourceId).toBe(1);
  });

  it("sends the password on its own route and never in the configuration", async () => {
    render(<ObsBridgeSettings />);
    await screen.findByTestId("obs-status");
    fireEvent.change(screen.getByLabelText("obs-websocket password"), { target: { value: "typed-in-pw" } });
    fireEvent.click(screen.getByRole("button", { name: /Save/ }));
    await waitFor(() => expect(putObs).toHaveBeenCalled());
    expect(putObsPassword).toHaveBeenCalledWith("typed-in-pw");
    expect(JSON.stringify(putObs.mock.calls[0][0])).not.toContain("typed-in-pw");
  });

  it("asks for the acknowledgement and a grace period only once stopping is switched on", async () => {
    render(<ObsBridgeSettings />);
    await screen.findByTestId("obs-status");
    expect(screen.queryByText(/ends its YouTube broadcast for good/)).toBeNull();
    fireEvent.click(screen.getByRole("switch", { name: "Stop them when OBS stops streaming" }));
    expect(screen.getByText(/ends its YouTube broadcast for good/)).toBeTruthy();
    fireEvent.change(screen.getByLabelText("Wait before stopping (seconds)"), { target: { value: "30" } });
    fireEvent.click(screen.getByText("I understand that stopping ends YouTube broadcasts").querySelector("button")!);
    fireEvent.click(screen.getByRole("button", { name: /Save/ }));
    await waitFor(() => expect(putObs).toHaveBeenCalled());
    const sent = putObs.mock.calls[0][0];
    expect(sent.stopOnStream).toBe(true);
    expect(sent.stopAcknowledged).toBe(true);
    expect(sent.stopGraceSeconds).toBe(30);
  });

  it("adds and removes scene rules", async () => {
    render(<ObsBridgeSettings />);
    await screen.findByTestId("obs-status");
    fireEvent.click(screen.getByRole("button", { name: /Add rule/ }));
    fireEvent.change(screen.getAllByLabelText("OBS scene")[1], { target: { value: "BRB" } });
    fireEvent.click(screen.getAllByRole("button", { name: "Remove rule" })[0]);
    fireEvent.click(screen.getByRole("button", { name: /Save/ }));
    await waitFor(() => expect(putObs).toHaveBeenCalled());
    expect(putObs.mock.calls[0][0].scenes).toEqual([{ scene: "BRB", preset: "no-music" }]);
  });

  it("says so when the settings cannot be read", async () => {
    getObs.mockRejectedValueOnce(new Error("boom"));
    render(<ObsBridgeSettings />);
    expect(await screen.findByText("Could not load the OBS bridge settings.")).toBeTruthy();
  });

  it("labels a stop the ingest-loss fallback made", async () => {
    getObs.mockImplementation(async () => {
      const r = response();
      r.status.lastActions = [
        { at: "2026-10-30T12:02:00Z", action: "stop", via: "ingest", results: [{ id: 3, name: "YouTube", outcome: "stopped" }] },
      ];
      return r;
    });
    render(<ObsBridgeSettings />);
    await screen.findByTestId("obs-status");
    expect(screen.getByText(/ingest loss fallback/)).toBeTruthy();
  });

  describe("stop on ingest loss", () => {
    it("is shown for the bridge's own programme and saves to that source, not the bridge config", async () => {
      render(<ObsBridgeSettings />);
      await screen.findByTestId("obs-status");
      fireEvent.click(screen.getByRole("switch", { name: "Stop on ingest loss" }));
      fireEvent.click(screen.getByLabelText("I understand that stopping ends YouTube broadcasts", { selector: "#obs-autostop-ack" }));
      // The control's Save is the one inside its own container.
      const box = screen.getByTestId("obs-autostop-control");
      fireEvent.click(box.querySelector("button:not([role])") as HTMLElement);
      await waitFor(() => expect(updateSource).toHaveBeenCalled());
      expect(updateSource).toHaveBeenCalledWith(1, {
        ingest: { autoStop: { enabled: true, waitSeconds: 0, acknowledged: true } },
      });
      expect(putObs).not.toHaveBeenCalled();
    });

    // Mutation: drop the programme line from the mount. Observed to fail.
    it("names the programme it edits", async () => {
      render(<ObsBridgeSettings />);
      await screen.findByTestId("obs-status");
      expect(screen.getByTestId("obs-autostop-programme").textContent).toContain("Studio A");
    });

    it("resolves a null sourceId to the default programme", async () => {
      getObs.mockImplementation(async () => response({ sourceId: null }));
      render(<ObsBridgeSettings />);
      await screen.findByTestId("obs-status");
      fireEvent.click(screen.getByRole("switch", { name: "Stop on ingest loss" }));
      fireEvent.click(screen.getByLabelText("I understand that stopping ends YouTube broadcasts", { selector: "#obs-autostop-ack" }));
      fireEvent.click(screen.getByTestId("obs-autostop-control").querySelector("button:not([role])") as HTMLElement);
      await waitFor(() => expect(updateSource).toHaveBeenCalled());
      expect(updateSource.mock.calls[0][0]).toBe(2);
    });

    it("adds no field to the PUT /obs body", async () => {
      render(<ObsBridgeSettings />);
      await screen.findByTestId("obs-status");
      fireEvent.click(screen.getByRole("button", { name: /Save/ }));
      await waitFor(() => expect(putObs).toHaveBeenCalled());
      const keys = Object.keys(putObs.mock.calls[0][0]).sort();
      expect(keys).toEqual(Object.keys(config()).sort());
    });
  });
});
