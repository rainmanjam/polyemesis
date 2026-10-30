// @vitest-environment jsdom

import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router";

import { RoutingPage } from "./RoutingPage";
import { TooltipProvider } from "@/components/ui/tooltip";
import { LiveDataContext, type LiveData } from "@/hooks/useLiveData";
import { ApiError, api, type DestinationWithRouting } from "@/lib/api";
import { autoApi } from "@/lib/autoApi";
import type { TrackLevelResult } from "@/lib/liveTrackLevel";
import type { Destination, RoutingProfile, RoutingResult } from "@/lib/types";

/* I4 on the routing page, rendered whole so the wiring is what is tested.
 *
 *  - A live mute or gain goes to the per-track route, not through Save, and the
 *    outcome reads as the server reported it.
 *  - A stored `muted` survives every path that ends in a profile PUT.
 *  - The live change does not wipe an unsaved draft, and the next Save does
 *    not undo it. */

afterEach(cleanup);
beforeAll(() => {
  vi.stubGlobal(
    "ResizeObserver",
    class {
      observe() {}
      unobserve() {}
      disconnect() {}
    },
  );
  if (!Element.prototype.scrollIntoView) Element.prototype.scrollIntoView = () => {};
  if (!Element.prototype.hasPointerCapture) Element.prototype.hasPointerCapture = () => false;
});
afterAll(() => vi.unstubAllGlobals());

const TRACKS = [0, 1, 2].map((index) => ({ index, channels: 2, codec: "aac", layout: "stereo" }));

function storedProfile(): RoutingProfile {
  return {
    mode: "simple",
    tracks: [
      { track: 0, enabled: true, gain: 1 },
      { track: 1, enabled: true, gain: 0.8, muted: true },
    ],
    matrix: null,
    normalize: "auto",
  } as RoutingProfile;
}

function dest(over: Partial<Destination> = {}): Destination {
  return {
    id: 7,
    name: "Twitch",
    kind: "rtmp",
    platform: "twitch",
    enabled: true,
    sourceId: 3,
    audio: {},
    profile: storedProfile(),
    ...over,
  } as unknown as Destination;
}

const compiled: RoutingResult = {
  filterComplex: "[0:a:0]anull[aout]",
  outLabel: "aout",
  summary: "Tracks 1, 2",
  tracks: [0, 1],
  normalization: "off",
  warnings: null,
};

let levels: LiveData["levels"] = null;

function live(): LiveData {
  return {
    programme: 3,
    programmeKnown: true,
    programmes: [{ id: 3, name: "Main" }],
    connected: true,
    snapshotKnown: true,
    status: null,
    source: { id: 3, name: "Main", probed: true, tracks: TRACKS, annotations: [] },
    levels,
    system: null,
    bitrate: [],
    logs: [],
  } as unknown as LiveData;
}

let stored: Destination;
let setTrackLevel: ReturnType<typeof vi.fn>;
let updateDestination: ReturnType<typeof vi.fn>;

/** A stand-in compiler: one named volume stage per enabled track, so a level
 *  change moves only levels and a selection change moves the graph. */
function compile(p: RoutingProfile): RoutingResult {
  const on = p.tracks.filter((s) => s.enabled);
  const legs = on.map(
    (s) => `[0:a:${s.track}]pan=stereo|c0=c0|c1=c1,volume@trk${s.track}=volume=${s.muted ? 0 : s.gain}[a_t${s.track}]`,
  );
  return { ...compiled, filterComplex: legs.join(";"), tracks: on.map((s) => s.track) };
}

function rows(): DestinationWithRouting[] {
  return [{ destination: structuredClone(stored), routing: compile(stored.profile) } as DestinationWithRouting];
}

beforeEach(() => {
  stored = dest();
  levels = null;
  vi.spyOn(autoApi, "loudness").mockResolvedValue({ enabled: false, reports: [] } as never);
  vi.spyOn(api, "listDestinations").mockImplementation(async () => rows());
  vi.spyOn(api, "listPresets").mockResolvedValue({
    presets: [{ id: "mix-a", name: "Mix A", description: "" }],
    defaults: { musicTrack: 0, micTrack: 2, surroundTrack: 0, cleanTrack: 1, language: "", musicPolicy: "" },
  } as unknown as Awaited<ReturnType<typeof api.listPresets>>);
  vi.spyOn(api, "compileRouting").mockImplementation((async (p: RoutingProfile) => ({
    routing: compile(p),
  })) as never);
  vi.spyOn(api, "putAnnotations").mockResolvedValue(undefined as never);
  vi.spyOn(api, "applyPreset").mockResolvedValue({
    profile: {
      mode: "simple",
      tracks: [
        { track: 1, enabled: true, gain: 1 },
        { track: 2, enabled: true, gain: 1 },
      ],
      matrix: null,
      normalize: "auto",
    },
    routing: compiled,
  } as never);
  updateDestination = vi.fn(async () => ({ destination: stored, routing: compiled }));
  vi.spyOn(api, "updateDestination").mockImplementation(updateDestination as never);
  setTrackLevel = vi.fn(
    async (_id: number, track: number, change: { muted?: boolean; gain?: number }) => {
      const tr = stored.profile.tracks.find((s) => s.track === track)!;
      Object.assign(tr, change);
      return { applied: "live", destination: structuredClone(stored) } as TrackLevelResult;
    },
  );
  vi.spyOn(api, "setTrackLevel").mockImplementation(setTrackLevel as never);
});

afterEach(() => vi.restoreAllMocks());

async function renderPage() {
  render(
    <LiveDataContext.Provider value={live()}>
      <TooltipProvider>
      <MemoryRouter initialEntries={["/routing/7"]}>
        <Routes>
          <Route path="/routing/:id" element={<RoutingPage />} />
        </Routes>
      </MemoryRouter>
      </TooltipProvider>
    </LiveDataContext.Provider>,
  );
  await screen.findByRole("button", { name: "Mute track 1" });
}

/** The tracks the last Save sent. */
function savedTracks() {
  const calls = updateDestination.mock.calls;
  expect(calls.length).toBeGreaterThan(0);
  const body = calls[calls.length - 1][1] as { profile: RoutingProfile };
  return body.profile.tracks;
}

const saveButton = () => screen.getAllByRole("button", { name: /^save$/i })[0] as HTMLButtonElement;

async function save() {
  await waitFor(() => expect(saveButton().disabled).toBe(false));
  await act(async () => fireEvent.click(saveButton()));
}

const muteButtons = () => screen.queryAllByRole("button", { name: /^(un)?mute track/i });

describe("live mute on the routing page", () => {
  it("goes to the per-track route of the destination's programme, not through Save", async () => {
    await renderPage();
    await act(async () => fireEvent.click(screen.getByRole("button", { name: "Mute track 1" })));
    expect(setTrackLevel).toHaveBeenCalledWith(7, 0, { muted: true }, 3);
    expect(updateDestination).not.toHaveBeenCalled();
    expect((await screen.findByTestId("live-level-outcome")).textContent).toBe("Applied live");
    // Nothing is pending: the change is already stored.
    expect(saveButton().disabled).toBe(true);
    expect(screen.getByRole("button", { name: "Unmute track 1" })).toBeTruthy();
  });

  it("shows a restart as a visible notice with the server's reason", async () => {
    setTrackLevel.mockResolvedValueOnce({
      applied: "restart",
      reason: "the destination is not running",
      destination: stored,
    });
    await renderPage();
    await act(async () => fireEvent.click(screen.getByRole("button", { name: "Mute track 1" })));
    const note = await screen.findByTestId("live-level-outcome");
    expect(note.textContent).toBe("Applied by restart: the destination is not running");
    expect(note.className).toMatch(/text-warn/);
  });

  it("keeps an unsaved draft, and the next Save does not undo the live mute", async () => {
    await renderPage();
    // An unsaved edit: track 3 into the mix.
    await act(async () => fireEvent.click(screen.getByLabelText("Include track 3")));
    await waitFor(() => expect(saveButton().disabled).toBe(false));
    await act(async () => fireEvent.click(screen.getByRole("button", { name: "Mute track 1" })));
    await screen.findByRole("button", { name: "Unmute track 1" });
    // The draft survived the list update and is still pending.
    expect((screen.getByLabelText("Include track 3") as HTMLButtonElement).getAttribute("data-state")).toBe("checked");
    await save();
    const sent = savedTracks();
    expect(sent.find((s) => s.track === 0)?.muted).toBe(true);
    expect(sent.find((s) => s.track === 2)?.enabled).toBe(true);
  });

  it("a 409 disables every mute with the server's reason, shown once", async () => {
    setTrackLevel.mockRejectedValueOnce(new ApiError(409, "passes its audio through unchanged"));
    await renderPage();
    await act(async () => fireEvent.click(screen.getByRole("button", { name: "Mute track 1" })));
    expect((await screen.findByTestId("live-level-notice")).textContent).toBe(
      "passes its audio through unchanged",
    );
    for (const b of muteButtons()) expect((b as HTMLButtonElement).disabled).toBe(true);
  });

  it("a 404 disables that track's mute and says why under it", async () => {
    setTrackLevel.mockRejectedValueOnce(new ApiError(404, "track is not part of this destination's selection"));
    await renderPage();
    await act(async () => fireEvent.click(screen.getByRole("button", { name: "Mute track 1" })));
    expect((await screen.findByTestId("live-block-0")).textContent).toMatch(/not part of/);
    expect((screen.getByRole("button", { name: "Mute track 1" }) as HTMLButtonElement).disabled).toBe(true);
    expect((screen.getByRole("button", { name: "Unmute track 2" }) as HTMLButtonElement).disabled).toBe(false);
  });

  it("an audio-copy destination never calls the route and says why", async () => {
    stored = dest({ kind: "srt", audio: { copy: true } } as Partial<Destination>);
    await renderPage();
    expect(screen.getByTestId("live-level-notice").textContent).toMatch(/audio through unchanged/);
    for (const b of muteButtons()) expect((b as HTMLButtonElement).disabled).toBe(true);
  });

  it("a track ticked but not saved cannot take a live mute and says to save first", async () => {
    await renderPage();
    await act(async () => fireEvent.click(screen.getByLabelText("Include track 3")));
    expect((screen.getByRole("button", { name: "Mute track 3" }) as HTMLButtonElement).disabled).toBe(true);
    expect(screen.getByTestId("live-block-2").textContent).toMatch(/^Save first/);
  });

  it("the second (VOD) mix's editor carries no live control", async () => {
    stored = dest({ vodProfile: storedProfile() } as Partial<Destination>);
    await renderPage();
    // Three rows in the live editor; the VOD editor's rows add none.
    expect(muteButtons()).toHaveLength(3);
    expect(screen.getAllByLabelText("Include track 1")).toHaveLength(2);
  });
});

describe("an edit made while Save is out", () => {
  // MUTATION: always reseed (drop the draftRef comparison in save). Observed: FAIL.
  it("is kept, and the form stays dirty, when the save answers", async () => {
    let finish: () => void = () => {};
    updateDestination.mockImplementation(
      () => new Promise((r) => (finish = () => r({ destination: stored, routing: compiled }))),
    );
    await renderPage();
    await act(async () => fireEvent.click(screen.getByLabelText("Include track 3")));
    await save();
    expect(updateDestination).toHaveBeenCalledTimes(1);
    // The operator keeps working while the PUT is out: track 3 back out.
    await act(async () => fireEvent.click(screen.getByLabelText("Include track 3")));
    // The server stores what was sent: track 3 in.
    stored.profile.tracks.push({ track: 2, enabled: true, gain: 1 });
    await act(async () => finish());
    await waitFor(() => expect(api.listDestinations).toHaveBeenCalledTimes(2));
    await waitFor(() => expect(saveButton().disabled).toBe(false));
    expect(screen.getByLabelText("Include track 3").getAttribute("data-state")).toBe("unchecked");
  });

  // MUTATION: compare only the live mix in save. Observed: FAIL.
  it("is kept on the second mix too", async () => {
    stored = dest({ vodProfile: storedProfile() } as Partial<Destination>);
    let finish: () => void = () => {};
    updateDestination.mockImplementation(
      () => new Promise((r) => (finish = () => r({ destination: stored, routing: compiled }))),
    );
    await renderPage();
    await act(async () => fireEvent.click(screen.getAllByLabelText("Include track 3")[0]));
    await save();
    // An edit to the SECOND mix while the PUT is out.
    await act(async () => fireEvent.click(screen.getAllByLabelText("Include track 3")[1]));
    stored.profile.tracks.push({ track: 2, enabled: true, gain: 1 });
    await act(async () => finish());
    await waitFor(() => expect(api.listDestinations).toHaveBeenCalledTimes(2));
    await waitFor(() => expect(saveButton().disabled).toBe(false));
    expect(screen.getAllByLabelText("Include track 3")[1].getAttribute("data-state")).toBe("checked");
  });

  it("an untouched draft is reseeded from the stored row and Save goes quiet", async () => {
    await renderPage();
    await act(async () => fireEvent.click(screen.getByLabelText("Include track 3")));
    updateDestination.mockImplementation(async (_id: number, body: { profile: RoutingProfile }) => {
      stored.profile = structuredClone(body.profile);
      return { destination: stored, routing: compiled };
    });
    await save();
    await waitFor(() => expect(api.listDestinations).toHaveBeenCalledTimes(2));
    await waitFor(() => expect(saveButton().disabled).toBe(true));
    expect(screen.getByLabelText("Include track 3").getAttribute("data-state")).toBe("checked");
  });
});

describe("a live level shows what the server stored", () => {
  // MUTATION: merge `change` instead of storedLevel(res, ...) in applyLevel. Observed: FAIL.
  it("merges the answer's stored gain, not the request's own value", async () => {
    // The server holds a different gain from the one asked for (a clamp, or
    // another client's change that landed first).
    setTrackLevel.mockImplementation(async (_id: number, track: number) => {
      const tr = stored.profile.tracks.find((s) => s.track === track)!;
      tr.gain = 0.6;
      return { applied: "live", destination: structuredClone(stored) } as TrackLevelResult;
    });
    await renderPage();
    // An unsaved edit, so the next Save sends the draft the answer was merged into.
    await act(async () => fireEvent.click(screen.getByLabelText("Include track 3")));
    const thumb = screen.getAllByRole("slider")[0];
    await act(async () => fireEvent.keyDown(thumb, { key: "ArrowLeft" }));
    await waitFor(() => expect(setTrackLevel).toHaveBeenCalledWith(7, 0, { gain: 0.95 }, 3));
    await waitFor(() => expect(screen.getAllByText("60%").length).toBeGreaterThan(0));
    await save();
    expect(savedTracks().find((s) => s.track === 0)?.gain).toBe(0.6);
  });
});

describe("a stored mute survives every routing-page save path", () => {
  it("another track's checkbox", async () => {
    await renderPage();
    await act(async () => fireEvent.click(screen.getByLabelText("Include track 3")));
    await save();
    expect(savedTracks().find((s) => s.track === 1)?.muted).toBe(true);
  });

  it("the draft gain slider of an unsaved track", async () => {
    await renderPage();
    await act(async () => fireEvent.click(screen.getByLabelText("Include track 3")));
    const thumb = screen.getAllByRole("slider")[2];
    await act(async () => fireEvent.keyDown(thumb, { key: "ArrowLeft" }));
    expect(setTrackLevel).not.toHaveBeenCalled();
    await save();
    const sent = savedTracks();
    expect(sent.find((s) => s.track === 2)?.gain).toBe(0.95);
    expect(sent.find((s) => s.track === 1)?.muted).toBe(true);
  });

  it("an Overview cell toggle", async () => {
    await renderPage();
    await act(async () => fireEvent.mouseDown(screen.getByRole("tab", { name: /overview/i })));
    await act(async () => fireEvent.click(screen.getByRole("tab", { name: /overview/i })));
    const cell = await screen.findByRole("button", { name: /^track 3 to twitch/i });
    await act(async () => fireEvent.click(cell));
    await save();
    expect(savedTracks().find((s) => s.track === 1)?.muted).toBe(true);
  });

  it("a mode switch to the matrix and back", async () => {
    await renderPage();
    const matrix = screen.getAllByRole("tab", { name: /matrix/i })[0];
    await act(async () => fireEvent.mouseDown(matrix));
    const simple = screen.getAllByRole("tab", { name: /simple/i })[0];
    await act(async () => fireEvent.mouseDown(simple));
    await act(async () => fireEvent.click(screen.getByLabelText("Include track 3")));
    await save();
    expect(savedTracks().find((s) => s.track === 1)?.muted).toBe(true);
  });

  it("discard, then an edit", async () => {
    await renderPage();
    await act(async () => fireEvent.click(screen.getByLabelText("Include track 3")));
    await act(async () => fireEvent.click(screen.getByRole("button", { name: /discard/i })));
    await act(async () => fireEvent.click(screen.getByLabelText("Include track 3")));
    await save();
    expect(savedTracks().find((s) => s.track === 1)?.muted).toBe(true);
  });

  it("a preset, which carries the mute over for a track it keeps", async () => {
    await renderPage();
    // Not one of the three starters, so it sits behind More recipes (A14).
    await act(async () => fireEvent.click(screen.getByTestId("more-recipes")));
    await act(async () => fireEvent.click(screen.getAllByRole("button", { name: /mix a/i })[0]));
    await waitFor(() => expect(api.applyPreset).toHaveBeenCalled());
    // Reviewed before it lands in the draft (A14).
    await act(async () => fireEvent.click(await screen.findByRole("button", { name: "Apply preset" })));
    await save();
    const sent = savedTracks();
    expect(sent.find((s) => s.track === 1)?.muted).toBe(true);
    // Unmuted is said, not implied: an absent key is no answer to a server
    // that merges the track list over the stored one.
    expect(sent.find((s) => s.track === 2)?.muted).toBe(false);
  });
});

describe("the save bar says what Save will do to a running destination", () => {
  const bar = () => screen.getAllByRole("status").find((e) => /Saving|Only levels/.test(e.textContent ?? ""));

  it("a structural change warns of a restart", async () => {
    await renderPage();
    // Let the stored mix's own compile land first, so a stale graph is on hand.
    await waitFor(() => expect(api.compileRouting).toHaveBeenCalled());
    await act(async () => fireEvent.click(screen.getByLabelText("Include track 3")));
    // Before the new draft has compiled, the older compile must not be read as
    // this draft's: the answer is the conservative one.
    expect(bar()?.textContent).toMatch(/^Saving restarts Twitch/);
    await waitFor(() => expect(bar()?.textContent).toMatch(/^Saving restarts Twitch/));
  });

  it("a level-only draft change says it applies live where supported", async () => {
    // A 404 leaves track 1 on the draft slider, so its gain becomes a pending
    // level-only edit for Save rather than a live call.
    setTrackLevel.mockRejectedValueOnce(new ApiError(404, "not in the selection"));
    await renderPage();
    await act(async () => fireEvent.click(screen.getByRole("button", { name: "Mute track 1" })));
    await screen.findByTestId("live-block-0");
    const thumb = screen.getAllByRole("slider")[0];
    await act(async () => fireEvent.keyDown(thumb, { key: "ArrowLeft" }));
    await waitFor(() =>
      expect(bar()?.textContent).toMatch(/applies them to Twitch live, without a restart, where the server supports it/),
    );
    await save();
    await waitFor(() => expect(savedTracks().find((x) => x.track === 0)?.gain).toBe(0.95));
  });
});

describe("A10: the signal path strip", () => {
  it("names an unmeasured output rather than drawing a reading", async () => {
    await renderPage();
    await waitFor(() => expect(screen.getByTestId("strip-output").getAttribute("data-state")).toBe("off"));
    expect(autoApi.loudness).toHaveBeenCalledWith(3);
  });

  it("flags a hot source with a silent output as the routing dropping signal", async () => {
    levels = { peak: [[-8, -9], [-60, -60]], rms: [[-20, -21], [-70, -70]] } as LiveData["levels"];
    vi.mocked(autoApi.loudness).mockResolvedValue({
      enabled: true,
      reports: [
        {
          destinationId: 7,
          seconds: 40,
          momentaryLufs: -70,
          truePeakDbtp: -120,
          reason: "",
          at: new Date().toISOString(),
        },
      ],
    } as never);
    await renderPage();
    await screen.findByTestId("strip-dropped");
    expect(screen.getByTestId("strip-source").textContent).toBe("-8.0 dBFS peak");
    expect(screen.getByTestId("strip-output").textContent).toMatch(/below the loudness gate/);
  });

  it("leaves a muted track out of the source stage, so its silence is not a fault", async () => {
    // Track 2 (index 1) is stored muted and is the only hot one.
    levels = { peak: [[-90, -90], [-6, -6]], rms: [[-90, -90], [-20, -20]] } as LiveData["levels"];
    vi.mocked(autoApi.loudness).mockResolvedValue({
      enabled: true,
      reports: [
        { destinationId: 7, seconds: 40, momentaryLufs: -70, truePeakDbtp: -120, reason: "", at: new Date().toISOString() },
      ],
    } as never);
    await renderPage();
    await waitFor(() => expect(screen.getByTestId("strip-output").getAttribute("data-state")).toBe("live"));
    expect(screen.queryByTestId("strip-dropped")).toBeNull();
    expect(screen.getByTestId("strip-source").textContent).toBe("-90.0 dBFS peak");
  });
});

describe("a matrix Save never moves a polarity to another cell", () => {
  type Cell = { track: number; channel: number; out: number; gain: number; invert?: boolean };
  const key = (c: Cell) => `${c.track}:${c.channel}:${c.out}`;

  /** What a server that merges the sent cell list over the stored one BY
   *  POSITION would end up holding: the failure the explicit field defends. */
  function indexMerge(storedCells: Cell[], sent: Cell[]): Cell[] {
    return sent.map((c, i) => ({ ...(storedCells[i] ?? {}), ...c }));
  }

  function matrixDest() {
    return dest({
      profile: {
        mode: "matrix",
        tracks: [
          { track: 0, enabled: true, gain: 1 },
          { track: 1, enabled: true, gain: 1 },
        ],
        // Track 2's left channel to the left output, inverted.
        matrix: [{ track: 1, channel: 0, out: 0, gain: 1, invert: true }],
        normalize: "auto",
      } as RoutingProfile,
    });
  }

  function sentMatrix(): Cell[] {
    const calls = updateDestination.mock.calls;
    expect(calls.length).toBeGreaterThan(0);
    return (calls[calls.length - 1][1] as { profile: RoutingProfile }).profile.matrix as Cell[];
  }

  // MUTATION: drop the matrix line from wireProfile. Observed: FAIL.
  it("a new cell sorted ahead of an inverted one goes out with invert false", async () => {
    stored = matrixDest();
    await renderPage();
    const input = screen.getAllByLabelText("Track 1 channel 1 to left")[0];
    await act(async () => fireEvent.change(input, { target: { value: "0.5" } }));
    await save();
    const sent = sentMatrix();
    // The new cell sorts first, where the inverted one used to sit.
    expect(sent.map(key)).toEqual(["0:0:0", "1:0:0"]);
    for (const c of sent) expect(typeof c.invert).toBe("boolean");
    const merged = indexMerge(matrixDest().profile.matrix as Cell[], sent);
    expect(merged.filter((c) => c.invert).map(key)).toEqual(["1:0:0"]);
  });

  // MUTATION: un-invert by dropping the key in toggleInvert AND drop the matrix
  // line from wireProfile. Observed: FAIL.
  it("un-inverting after a re-sort says invert false on that cell", async () => {
    stored = matrixDest();
    await renderPage();
    const input = screen.getAllByLabelText("Track 1 channel 1 to left")[0];
    await act(async () => fireEvent.change(input, { target: { value: "0.5" } }));
    const btn = screen.getAllByRole("button", { name: /^Invert polarity.*Track 2 channel 1 to left/ })[0];
    await act(async () => fireEvent.click(btn));
    await save();
    const sent = sentMatrix();
    expect(sent).toEqual([
      { track: 0, channel: 0, out: 0, gain: 0.5, invert: false },
      { track: 1, channel: 0, out: 0, gain: 1, invert: false },
    ]);
    const merged = indexMerge(matrixDest().profile.matrix as Cell[], sent);
    expect(merged.filter((c) => c.invert)).toEqual([]);
  });
});
