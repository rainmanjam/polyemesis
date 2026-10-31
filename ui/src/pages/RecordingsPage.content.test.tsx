// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";

/* WHAT THE RECORDINGS CARD SAYS, AND WHAT IT LETS THE OPERATOR SAVE (A11, A22, A23).
 *
 * A11: the header printed the recorder's raw state ("failed", "reconnecting")
 * in lower case, and every non-running state used the grey outline style, so a
 * failed recorder looked the same as a disabled one. It reads through the kit's
 * status map now.
 *
 * A22: clearing a retention box stored Number('') = 0 in the draft, and Save
 * sent it. The server treats 0 as "no limit", so a blank field silently removed
 * the disk cap. A blank or out-of-range field now disables Save.
 *
 * A23: switching recording off while a broadcast is live took effect at once,
 * with no confirmation. It asks first now. */

let ingestLive = false;
let recorderStatus: unknown = null;
const putSettings = vi.fn((s: unknown) => Promise.resolve(s));

const SETTINGS = {
  recording: {
    enabled: true,
    segmentSeconds: 30,
    maxGb: 50,
    maxAgeHours: 168,
    minFreeGb: 5,
    stems: false,
    stemCodec: "flac",
  },
  postProd: {},
};

vi.mock("@/lib/api", () => ({
  api: {
    listRecordings: () => Promise.resolve([]),
    recordingUsage: () => Promise.resolve({ bytes: 0, files: 0, storage: { halted: false, reason: null } }),
    getSettings: () => Promise.resolve(SETTINGS),
    putSettings: (s: unknown) => putSettings(s),
    deleteRecording: () => Promise.resolve({}),
    downloadUrl: (id: number) => `/dl/${id}`,
  },
}));

vi.mock("@/lib/autoApi", () => ({
  autoApi: { get: () => Promise.resolve([]) },
}));

vi.mock("@/hooks/useLiveData", () => ({
  useLiveData: () => ({ recordingsRevision: 0, status: recorderStatus }),
  useIngestLive: () => ingestLive,
}));

vi.mock("sonner", () => ({
  toast: { error: () => {}, success: () => {}, info: () => {} },
}));

const { RecordingsPage } = await import("./RecordingsPage");

afterEach(() => {
  cleanup();
  ingestLive = false;
  recorderStatus = null;
  putSettings.mockClear();
});

describe("RecordingsPage recorder status (A11)", () => {
  it("reads a failed recorder as Failed, not as the raw lower-case state", async () => {
    recorderStatus = { recorder: { state: "failed" } };
    render(<RecordingsPage />);

    await waitFor(() => expect(screen.getByText("Failed")).toBeTruthy());
    expect(screen.queryByText("failed")).toBeNull();
  });
});

describe("RecordingsPage retention fields (A22)", () => {
  it("disables Save policy while a retention box is blank, so no 0 is sent", async () => {
    render(<RecordingsPage />);

    const max = (await waitFor(() => {
      const el = document.getElementById("rec-gb");
      if (!el) throw new Error("retention field not rendered yet");
      return el;
    })) as HTMLInputElement;
    fireEvent.change(max, { target: { value: "" } });

    const save = screen.getByRole("button", { name: "Save retention policy" }) as HTMLButtonElement;
    expect(save.disabled).toBe(true);
    expect(putSettings).not.toHaveBeenCalled();
  });
});

describe("RecordingsPage recording switch during a live broadcast (A23)", () => {
  it("asks before switching recording off while the ingest is live", async () => {
    ingestLive = true;
    render(<RecordingsPage />);

    const enabled = (await waitFor(() => {
      const el = document.getElementById("rec-enabled");
      if (!el) throw new Error("switch not rendered yet");
      return el;
    })) as HTMLButtonElement;
    fireEvent.click(enabled);

    // Nothing is written until the operator confirms.
    expect(putSettings).not.toHaveBeenCalled();
    await waitFor(() =>
      expect(screen.getByText("Stop recording while the stream is live?")).toBeTruthy(),
    );
  });
});
