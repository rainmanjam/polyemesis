// @vitest-environment jsdom

import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router";

import { RoutingPage } from "./RoutingPage";
import { TooltipProvider } from "@/components/ui/tooltip";
import { LiveDataContext, type LiveData } from "@/hooks/useLiveData";
import { api, type DestinationWithRouting } from "@/lib/api";
import { autoApi } from "@/lib/autoApi";
import { translate } from "@/lib/i18n";
import type { Destination, PresetOpts, RoutingProfile, RoutingResult, SourceInfo } from "@/lib/types";

/* THE PRESET PICKERS OPEN ON THE EDITED DESTINATION'S OWN PROGRAMME.
 *
 * A routing preset picks track indices, and the server applies it against the
 * destination's programme (applyPreset is sent editedSourceId, #497). The
 * pickers' role defaults -- "the music picker opens on the track annotated
 * music" -- were read from the programme selected in the header instead, so
 * on a two-programme install editing Studio B's destination with Studio A
 * selected opened the music picker on Studio A's music track and applied the
 * preset with it: Studio B's mic went out as its music bed. */

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

const TRACKS = [0, 1, 2, 3].map((index) => ({ index, channels: 2, codec: "aac", layout: "stereo" }));

const compiled: RoutingResult = {
  filterComplex: "[0:a:0]anull[aout]",
  outLabel: "aout",
  summary: "Track 1",
  tracks: [0],
  normalization: "off",
  warnings: null,
};

/** Studio B's destination: the one being edited. */
function destOnStudioB(): Destination {
  return {
    id: 21,
    name: "Studio B to YouTube",
    kind: "rtmp",
    platform: "youtube",
    enabled: true,
    sourceId: 2,
    audio: {},
    profile: { mode: "simple", tracks: [{ track: 0, enabled: true, gain: 1 }], matrix: null, normalize: "auto" },
  } as unknown as Destination;
}

/** Studio A, selected in the header: music is on track 1 there. */
const studioA = {
  id: 1,
  name: "Studio A",
  probed: true,
  tracks: TRACKS,
  annotations: [{ track: 1, role: "music" }],
} as unknown as SourceInfo;

/** Studio B, the destination's programme: music is on track 3. */
const studioB = {
  id: 2,
  name: "Studio B",
  probed: true,
  tracks: TRACKS,
  annotations: [{ track: 3, role: "music" }],
} as unknown as SourceInfo;

function live(): LiveData {
  return {
    programme: 1,
    programmeKnown: true,
    programmes: [
      { id: 1, name: "Studio A" },
      { id: 2, name: "Studio B" },
    ],
    connected: true,
    snapshotKnown: true,
    status: null,
    source: studioA,
    levels: null,
    system: null,
    bitrate: [],
    logs: [],
  } as unknown as LiveData;
}

let applyPreset: ReturnType<typeof vi.fn>;
let sourceRead: ReturnType<typeof vi.fn>;

beforeEach(() => {
  vi.spyOn(autoApi, "loudness").mockResolvedValue({ enabled: false, reports: [] } as never);
  vi.spyOn(api, "listDestinations").mockImplementation(async () => [
    { destination: destOnStudioB(), routing: compiled } as DestinationWithRouting,
  ]);
  vi.spyOn(api, "listPresets").mockResolvedValue({
    presets: [{ id: "no-music", name: "No music", description: "", needsMusicTrack: true }],
    // The server's generic slots: music 0.
    defaults: { musicTrack: 0, micTrack: 2, surroundTrack: 0, cleanTrack: 1, language: "", musicPolicy: "" },
  } as unknown as Awaited<ReturnType<typeof api.listPresets>>);
  vi.spyOn(api, "compileRouting").mockImplementation((async (p: RoutingProfile) => ({
    routing: { ...compiled, tracks: p.tracks.map((s) => s.track) },
  })) as never);
  vi.spyOn(api, "putAnnotations").mockResolvedValue(undefined as never);
  sourceRead = vi.fn(async (id: number | null) => (id === 2 ? studioB : studioA));
  vi.spyOn(api, "source").mockImplementation(sourceRead as never);
  // Never resolves: the review dialog is not under test, only what was asked.
  applyPreset = vi.fn(() => new Promise(() => {}));
  vi.spyOn(api, "applyPreset").mockImplementation(applyPreset as never);
});

afterEach(() => vi.restoreAllMocks());

async function renderPage() {
  render(
    <LiveDataContext.Provider value={live()}>
      <TooltipProvider>
        <MemoryRouter initialEntries={["/routing/21"]}>
          <Routes>
            <Route path="/routing/:id" element={<RoutingPage />} />
          </Routes>
        </MemoryRouter>
      </TooltipProvider>
    </LiveDataContext.Provider>,
  );
  return (await screen.findAllByRole("button", { name: translate("en", "route.preset.noMusic") }))[0];
}

describe("preset role defaults on a destination of another programme", () => {
  it("opens the music picker on the destination's programme's music track, not the header's", async () => {
    const noMusic = await renderPage();
    // Let the destination's programme layout land.
    await act(async () => {});
    expect(sourceRead).toHaveBeenCalledWith(2);

    await act(async () => fireEvent.click(noMusic));

    expect(applyPreset).toHaveBeenCalled();
    const [id, opts, sourceId] = applyPreset.mock.calls[0] as [string, PresetOpts, number];
    expect(id).toBe("no-music");
    expect(sourceId).toBe(2);
    expect(opts.musicTrack, "the preset took Studio A's music track for Studio B's destination").toBe(3);
  });

  it("falls back to the server's defaults, never the header programme's roles, until that layout is read", async () => {
    // Studio B's layout read never answers.
    sourceRead.mockImplementation(async (id: number | null) =>
      id === 2 ? new Promise(() => {}) : studioA,
    );
    const noMusic = await renderPage();

    await act(async () => fireEvent.click(noMusic));

    const [, opts] = applyPreset.mock.calls[0] as [string, PresetOpts];
    expect(opts.musicTrack, "Studio A's music annotation stood in for Studio B's").toBe(0);
  });
});
