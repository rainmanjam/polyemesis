// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { ObsBridgeSettings } from "./ObsBridgeSettings";
import type { ObsConfig, ObsResponse } from "@/lib/types";

/* Scene rules are an editable list with no id of their own. Deleting a middle
 * rule must leave the other rules' inputs holding their own values, and the
 * save must send only the rules, with no row keys in them. */

const scene = (name: string) => ({ scene: name, preset: "no-music" });

const response = (scenes: ObsConfig["scenes"]): ObsResponse => ({
  config: {
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
    scenes,
  },
  status: {
    connection: { state: "connected", detail: "connected to obs-websocket", since: "2026-10-30T12:00:00Z" },
    streaming: true,
    scene: "Gameplay",
    lastActions: [],
  },
});

const getObs = vi.fn(async () => response([scene("A"), scene("B"), scene("C")]));
const putObs = vi.fn(async (c: ObsConfig) => ({ ...response(c.scenes), config: c }));

vi.mock("@/lib/api", async () => {
  const actual = await vi.importActual<typeof import("@/lib/api")>("@/lib/api");
  return {
    ...actual,
    api: {
      getObs: () => getObs(),
      putObs: (c: ObsConfig) => putObs(c),
      putObsPassword: async (pw: string) => ({ hasPassword: pw !== "" }),
      listSources: async () => [
        { id: 1, name: "Studio A", isDefault: false, ingest: { mode: "srt" } },
        { id: 2, name: "Studio B", isDefault: true, ingest: { mode: "srt" } },
      ],
      updateSource: async () => ({}),
      listDestinations: async () => [{ destination: { id: 3, name: "YouTube", sourceId: 1 } }],
      listPresets: async () => ({
        presets: [{ id: "no-music", name: "No music" }],
        defaults: { musicTrack: 0, micTrack: 2, surroundTrack: 0, cleanTrack: 1 },
      }),
    },
  };
});
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));

beforeEach(() => {
  getObs.mockClear();
  putObs.mockClear();
});
afterEach(cleanup);

describe("OBS scene rules keep their identity when a middle rule is removed", () => {
  it("keeps the last rule's input node and value after deleting the middle rule", async () => {
    render(<ObsBridgeSettings />);
    await screen.findByTestId("obs-status");
    const inputs = screen.getAllByLabelText("OBS scene") as HTMLInputElement[];
    expect(inputs.map((i) => i.value)).toEqual(["A", "B", "C"]);
    const cNode = inputs[2];

    fireEvent.click(screen.getAllByRole("button", { name: "Remove rule" })[1]);

    // With index keys React would unmount the row-3 node and reuse it for row 2.
    expect(cNode.isConnected).toBe(true);
    const after = screen.getAllByLabelText("OBS scene") as HTMLInputElement[];
    expect(after.map((i) => i.value)).toEqual(["A", "C"]);
    expect(after[1]).toBe(cNode);
  });

  it("sends only the rules, with no row keys, after a middle delete", async () => {
    render(<ObsBridgeSettings />);
    await screen.findByTestId("obs-status");
    fireEvent.click(screen.getAllByRole("button", { name: "Remove rule" })[1]);
    fireEvent.click(screen.getByRole("button", { name: /Save/ }));
    await waitFor(() => expect(putObs).toHaveBeenCalled());
    expect(putObs.mock.calls[0][0].scenes).toEqual([scene("A"), scene("C")]);
  });
});
