// @vitest-environment jsdom

import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, render, screen } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router";

import { RoutingPage } from "./RoutingPage";
import { TooltipProvider } from "@/components/ui/tooltip";
import { LiveDataContext, type LiveData } from "@/hooks/useLiveData";
import { api, type DestinationWithRouting } from "@/lib/api";
import { autoApi } from "@/lib/autoApi";
import { translate } from "@/lib/i18n";
import type { Destination, RoutingProfile, RoutingResult, SourceInfo } from "@/lib/types";

/* THE MUSIC-RIGHTS BADGE READS THE DESTINATION'S OWN PROGRAMME.
 *
 * The server applies excludeRoles to the tracks of the programme the
 * destination belongs to (Profile.ExcludedTracks, filtergraph applyRolePolicy).
 * The card used the programme selected in the header, so on a two-programme
 * install it could say "music excluded" while the server sent music. */

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
    profile: { excludeRoles: ["music"], mode: "simple", tracks: [{ track: 0, enabled: true, gain: 1 }], matrix: null, normalize: "auto" },
  } as unknown as Destination;
}

/** Studio A, selected in the header. */
const withAnn = (id: number, annotations: unknown[]) =>
  ({ id, name: id === 1 ? "Studio A" : "Studio B", probed: true, tracks: TRACKS, annotations }) as unknown as SourceInfo;
let headerSource: SourceInfo;
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
    source: headerSource,
    levels: null,
    system: null,
    bitrate: [],
    logs: [],
  } as unknown as LiveData;
}

let sourceRead: ReturnType<typeof vi.fn>;
let destSource: SourceInfo | Promise<SourceInfo>;

beforeEach(() => {
  vi.spyOn(autoApi, "loudness").mockResolvedValue({ enabled: false, reports: [] } as never);
  vi.spyOn(api, "listDestinations").mockImplementation(async () => [
    { destination: destOnStudioB(), routing: compiled } as DestinationWithRouting,
  ]);
  vi.spyOn(api, "listPresets").mockResolvedValue({
    presets: [],
    defaults: { musicTrack: 0, micTrack: 2, surroundTrack: 0, cleanTrack: 1, language: "", musicPolicy: "" },
  } as unknown as Awaited<ReturnType<typeof api.listPresets>>);
  vi.spyOn(api, "compileRouting").mockImplementation((async (p: RoutingProfile) => ({
    routing: { ...compiled, tracks: p.tracks.map((s) => s.track) },
  })) as never);
  vi.spyOn(api, "putAnnotations").mockResolvedValue(undefined as never);
  sourceRead = vi.fn(async (id: number | null) => (id === 2 ? destSource : headerSource));
  vi.spyOn(api, "source").mockImplementation(sourceRead as never);
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
  await screen.findByLabelText(translate("en", "route.keepMusicOut"));
  await act(async () => {});
}

const EXCLUDED = translate("en", "route.musicExcluded");
const IDLE = translate("en", "route.musicExclusionIdle");

describe("the music badge on a two-programme install (destination on Studio B, header on Studio A)", () => {
  it("does not say music is excluded when only the header's programme has a music track", async () => {
    headerSource = withAnn(1, [{ track: 1, role: "music" }]);
    destSource = withAnn(2, []);
    await renderPage();
    expect(screen.queryByText(EXCLUDED), "Studio A's music was counted for Studio B's destination").toBeNull();
    expect(screen.getByText(IDLE)).toBeTruthy();
  });

  it("says music is excluded when the destination's own programme has the music track", async () => {
    headerSource = withAnn(1, []);
    destSource = withAnn(2, [{ track: 3, role: "music" }]);
    await renderPage();
    expect(screen.getByText(EXCLUDED)).toBeTruthy();
    expect(screen.queryByText(IDLE)).toBeNull();
  });

  it("claims nothing while the destination's programme has not been read", async () => {
    headerSource = withAnn(1, []);
    destSource = new Promise<SourceInfo>(() => {});
    await renderPage();
    expect(screen.getByText(translate("en", "route.musicExclusionOn"))).toBeTruthy();
    expect(screen.queryByText(EXCLUDED)).toBeNull();
    expect(screen.queryByText(IDLE)).toBeNull();
  });

  it("claims nothing when that read fails", async () => {
    headerSource = withAnn(1, [{ track: 1, role: "music" }]);
    sourceRead.mockImplementation(async (id: number | null) => {
      if (id === 2) throw new Error("boom");
      return headerSource;
    });
    await renderPage();
    expect(screen.getByText(translate("en", "route.musicExclusionOn"))).toBeTruthy();
    expect(screen.queryByText(EXCLUDED)).toBeNull();
    expect(screen.queryByText(IDLE)).toBeNull();
  });
});
