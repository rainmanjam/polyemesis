// @vitest-environment jsdom

import { translate } from "@/lib/i18n";
import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router";

import { RoutingPage } from "./RoutingPage";
import { TooltipProvider } from "@/components/ui/tooltip";
import { LiveDataContext, type LiveData } from "@/hooks/useLiveData";
import { api, type DestinationWithRouting } from "@/lib/api";
import { autoApi } from "@/lib/autoApi";
import type { TrackLevelResult } from "@/lib/liveTrackLevel";
import type { Destination, RoutingProfile, RoutingResult } from "@/lib/types";

/* The routing page, rendered whole so the wiring is what is tested.
 *
 *  - Mute and gain are pending draft edits like every other (A26): nothing is
 *    written until Save changes, which sends one PUT; Discard restores.
 *  - A stored `muted` survives every path that ends in a profile PUT.
 *  - Advanced starts collapsed and the FFmpeg graph is behind a button (A27). */

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
  await screen.findAllByRole("button", { name: "Mute track 1" });
}

/** The tracks the last Save sent. */
function savedTracks() {
  const calls = updateDestination.mock.calls;
  expect(calls.length).toBeGreaterThan(0);
  const body = calls[calls.length - 1][1] as { profile: RoutingProfile };
  return body.profile.tracks;
}

const saveButton = () => screen.getByRole("button", { name: /^save changes$/i }) as HTMLButtonElement;

async function save() {
  await waitFor(() => expect(saveButton().disabled).toBe(false));
  await act(async () => fireEvent.click(saveButton()));
}

const muteButtons = () => screen.queryAllByRole("button", { name: /^(un)?mute track/i });

const saveState = () => document.querySelector("[data-save-state]")?.getAttribute("data-save-state");

/* app-council A26: ONE save model. Mute and gain used to go straight to the
 * per-track live route while every other edit waited for Save, so the page had
 * two save models and two save surfaces. Now every edit, mute and gain
 * included, is a pending edit in the draft: nothing is written until Save
 * changes, Save writes the draft in one PUT (the server applies a level-only
 * change live on its own, ffmpeg.MaskLiveVolumes), and Discard puts the stored
 * value back. */
describe("one save model: mute and gain wait for Save changes (A26)", () => {
  // RED FIRST: against the live-apply page (f96576bb) this and the next two
  // failed on the save state, expected dirty, received clean.
  it("a mute is a pending edit: no write until Save changes, then one PUT", async () => {
    await renderPage();
    await act(async () => fireEvent.click(screen.getByRole("button", { name: "Mute track 1" })));
    // Pending, not applied: the draft is dirty and nothing went to the server.
    expect(saveState()).toBe("dirty");
    expect(screen.getByRole("button", { name: "Unmute track 1" })).toBeTruthy();
    expect(setTrackLevel).not.toHaveBeenCalled();
    expect(updateDestination).not.toHaveBeenCalled();
    await save();
    expect(updateDestination).toHaveBeenCalledTimes(1);
    expect(savedTracks().find((s) => s.track === 0)?.muted).toBe(true);
    expect(setTrackLevel).not.toHaveBeenCalled();
  });

  it("a gain is a pending edit, and Save sends it in the same single PUT", async () => {
    await renderPage();
    const thumb = screen.getAllByRole("slider")[0];
    await act(async () => fireEvent.keyDown(thumb, { key: "ArrowLeft" }));
    expect(saveState()).toBe("dirty");
    expect(saveButton().disabled).toBe(false);
    expect(updateDestination).not.toHaveBeenCalled();
    await save();
    expect(updateDestination).toHaveBeenCalledTimes(1);
    expect(savedTracks().find((s) => s.track === 0)?.gain).toBe(0.95);
    // The stored mute on track 2 rides along untouched.
    expect(savedTracks().find((s) => s.track === 1)?.muted).toBe(true);
    expect(setTrackLevel).not.toHaveBeenCalled();
  });

  it("Discard puts the stored mute and gain back and writes nothing", async () => {
    await renderPage();
    // Track 2 is stored muted: unmute it, and move track 1's gain.
    await act(async () => fireEvent.click(screen.getByRole("button", { name: "Unmute track 2" })));
    await act(async () => fireEvent.keyDown(screen.getAllByRole("slider")[0], { key: "ArrowLeft" }));
    expect(screen.getByRole("button", { name: "Mute track 2" })).toBeTruthy();
    expect(screen.getAllByText("95%").length).toBeGreaterThan(0);
    expect(saveState()).toBe("dirty");
    await act(async () => fireEvent.click(screen.getByRole("button", { name: /^discard$/i })));
    expect(screen.getByRole("button", { name: "Unmute track 2" })).toBeTruthy();
    expect(screen.queryAllByText("95%")).toHaveLength(0);
    expect(saveState()).toBe("clean");
    expect(updateDestination).not.toHaveBeenCalled();
    expect(setTrackLevel).not.toHaveBeenCalled();
  });

  it("a mute and an unsaved track go out together in one Save", async () => {
    await renderPage();
    await act(async () => fireEvent.click(screen.getByLabelText("Include track 3")));
    await act(async () => fireEvent.click(screen.getByRole("button", { name: "Mute track 1" })));
    await save();
    expect(updateDestination).toHaveBeenCalledTimes(1);
    const sent = savedTracks();
    expect(sent.find((s) => s.track === 0)?.muted).toBe(true);
    expect(sent.find((s) => s.track === 2)?.enabled).toBe(true);
  });

  it("drops the 'apply without Save' copy and says mute and gain wait for Save", async () => {
    await renderPage();
    expect(screen.queryByText(translate("en", "route.live.hint"))).toBeNull();
    expect(screen.getAllByText(translate("en", "routingUx.muteGainHint")).length).toBeGreaterThan(0);
  });

  it("has exactly one Save button on the page", async () => {
    await renderPage();
    expect(screen.getAllByRole("button", { name: /^save( changes)?$/i })).toHaveLength(1);
  });

  it("an audio-copy destination disables mute and says why", async () => {
    stored = dest({ kind: "srt", audio: { copy: true } } as Partial<Destination>);
    await renderPage();
    expect(screen.getAllByTestId("live-level-notice")[0].textContent).toMatch(/audio through unchanged/);
    for (const b of muteButtons()) expect((b as HTMLButtonElement).disabled).toBe(true);
  });

  it("a mute under the second (VOD) mix changes only the second mix", async () => {
    stored = dest({ vodProfile: { ...storedProfile(), tracks: [{ track: 0, enabled: true, gain: 1 }] } } as Partial<Destination>);
    await renderPage();
    // Each mix's editor carries its own row of mutes.
    expect(screen.getAllByRole("button", { name: "Mute track 1" })).toHaveLength(2);
    await act(async () => fireEvent.click(screen.getAllByRole("button", { name: "Mute track 1" })[1]));
    await save();
    const body = updateDestination.mock.calls[0][1] as { profile: RoutingProfile; vodProfile: RoutingProfile };
    expect(body.vodProfile.tracks.find((s) => s.track === 0)?.muted).toBe(true);
    expect(body.profile.tracks.find((s) => s.track === 0)?.muted).toBeFalsy();
    expect(setTrackLevel).not.toHaveBeenCalled();
  });
});

/* app-council A27: Presets, Mute and gain and Music rights stay in the main
 * column; loudness, A/V delay, ducking, the second mix and the compiled graph
 * move into an Advanced section that starts collapsed, and the graph itself is
 * behind a button rather than printed. */
describe("the editor's layout (A27)", () => {
  const advanced = () => screen.getByTestId("routing-advanced") as HTMLDetailsElement;

  // MUTATION: render the Advanced <details> with `open`. Observed: FAIL.
  it("starts with Advanced collapsed, holding loudness, delay, ducking and the VOD mix", async () => {
    await renderPage();
    expect(advanced().tagName).toBe("DETAILS");
    expect(advanced().open).toBe(false);
    expect(advanced().querySelector("summary")?.textContent).toContain(translate("en", "routingUx.advanced"));
    // Still mounted while closed, so a second mix's compile error keeps blocking Save.
    expect(advanced().contains(screen.getByLabelText("Delay direction"))).toBe(true);
    expect(advanced().contains(document.getElementById("vod-enabled"))).toBe(true);
    expect(advanced().contains(screen.getByText(translate("en", "route.result")))).toBe(true);
    // The core job stays outside it.
    expect(advanced().contains(screen.getByRole("button", { name: "Mute track 1" }))).toBe(false);
    expect(advanced().contains(screen.getByText(translate("en", "routing.presets.title")))).toBe(false);
    expect(advanced().contains(screen.getByText(translate("en", "routingUx.muteGainTitle")))).toBe(false);
  });

  // MUTATION: render the FilterString unconditionally. Observed: FAIL.
  it("does not print the FFmpeg graph until Show FFmpeg graph is pressed", async () => {
    await renderPage();
    await waitFor(() => expect(api.compileRouting).toHaveBeenCalled());
    const show = await screen.findByRole("button", { name: translate("en", "routingUx.showGraph") });
    expect(show.getAttribute("aria-expanded")).toBe("false");
    expect(screen.queryByText(/volume@trk0/)).toBeNull();
    await act(async () => fireEvent.click(show));
    expect(show.getAttribute("aria-expanded")).toBe("true");
    expect(await screen.findByText(/volume@trk0/)).toBeTruthy();
  });

  it("keeps the /routing/:id deep link selecting the destination in the rail", async () => {
    await renderPage();
    const rail = screen.getByRole("button", { name: /^Twitch/ });
    expect(rail.className).toMatch(/bg-primary-dim/);
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
    // A gain is a pending draft edit now (A26), and a draft that moves only
    // levels is the one Save the server applies without a restart.
    await renderPage();
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

describe("RoutingPage: the Save reason and the destination list (A28, A29)", () => {
  it("links the greyed Save changes to the sentence that says why it is greyed", async () => {
    // A28. Save is disabled until a route changes, and a title on a disabled
    // button reaches nobody (button.tsx pointer-events-none). The reason is
    // visible text the button points at. Since A26 the one Save is the bar's.
    await renderPage();
    const save = await screen.findByRole("button", { name: "Save changes" }) as HTMLButtonElement;
    expect(save.disabled).toBe(true);
    const reasonId = save.getAttribute("aria-describedby");
    expect(reasonId).toBeTruthy();
    expect(document.getElementById(reasonId as string)?.textContent).toContain("Nothing to save");
  });

  it("does not cut a destination's routing summary short", async () => {
    // A29. "Tracks 1, 2, 3, 4 → stereo" was clipped to a few letters and the
    // music status beside it was lost. The summary wraps now.
    await renderPage();
    const summary = await screen.findByText("Tracks 1, 2");
    expect(summary.className).not.toMatch(/\btruncate\b/);
  });
});

/* app-council A95: Routing uses the kit's save model. The edit is held in a
 * draft behind an explicit Save, and <SaveStatus> beside the Save button says
 * where the draft is: "Unsaved changes", "Saved", or a failure that keeps the
 * draft. Before, a failed save was a toast that vanished and nothing on the
 * page said the routing was still unsaved. Since A26 mute and gain are draft
 * edits too (pinned in the "one save model" block above). */
describe("the save status beside Save", () => {
  const status = () => {
    const el = document.querySelector("[data-save-state]");
    expect(el, "no SaveStatus on the routing page").not.toBeNull();
    return el as HTMLElement;
  };

  it("says Unsaved changes while the draft differs, then Saved once it is written", async () => {
    await renderPage();
    expect(status().getAttribute("data-save-state")).toBe("clean");
    await act(async () => fireEvent.click(screen.getByLabelText("Include track 3")));
    expect(status().textContent).toBe(translate("en", "save.dirty"));
    await save();
    await waitFor(() => expect(status().textContent).toBe(translate("en", "save.saved")));
  });

  it("says the save failed, keeps the draft, and keeps Save available to retry", async () => {
    updateDestination.mockRejectedValueOnce(new Error("the server is restarting"));
    await renderPage();
    await act(async () => fireEvent.click(screen.getByLabelText("Include track 3")));
    await save();
    await waitFor(() => expect(status().getAttribute("data-save-state")).toBe("failed"));
    expect(status().textContent).toBe(translate("en", "save.failed"));
    expect((screen.getByLabelText("Include track 3") as HTMLButtonElement).getAttribute("data-state")).toBe("checked");
    expect(saveButton().disabled).toBe(false);
  });

  it("clears a failure as soon as the operator edits again", async () => {
    updateDestination.mockRejectedValueOnce(new Error("the server is restarting"));
    await renderPage();
    await act(async () => fireEvent.click(screen.getByLabelText("Include track 3")));
    await save();
    await waitFor(() => expect(status().getAttribute("data-save-state")).toBe("failed"));
    await act(async () => fireEvent.click(screen.getByLabelText("Include track 3")));
    // The failure is history once the draft moves on; the edit is pending again.
    expect(status().getAttribute("data-save-state")).toBe("dirty");
  });
});
