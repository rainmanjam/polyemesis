// @vitest-environment jsdom

import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
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

  /* Marks that change AFTER the card first read them. */

  /** The server's copy of Studio B's marks: what GET /source answers and what
   *  PUT /source/annotations last wrote. */
  function serverHoldsOnB(initial: unknown[]) {
    let held = initial;
    sourceRead.mockImplementation(async (id: number | null) =>
      id === 2 ? withAnn(2, held) : headerSource,
    );
    (api.putAnnotations as unknown as ReturnType<typeof vi.fn>).mockImplementation(
      async (list: unknown[], sid: number | null) => {
        if (sid === 2) held = list;
      },
    );
    return (next: unknown[]) => {
      held = next;
    };
  }

  async function setRole(track: number, role: string) {
    const roleAria = translate("en", "signature.track.roleAria", { n: track + 1 });
    if (!screen.queryByLabelText(roleAria)) {
      await act(async () => fireEvent.click(screen.getByRole("button", { name: translate("en", "route.labelTracks") })));
    }
    const trigger = await screen.findByLabelText(translate("en", "signature.track.roleAria", { n: track + 1 }));
    await act(async () => {
      fireEvent.pointerDown(trigger, { button: 0, ctrlKey: false, pointerType: "mouse" });
      fireEvent.click(trigger);
    });
    const opt = await screen.findByRole("option", { name: role });
    await act(async () => fireEvent.click(opt));
  }

  it("follows an in-page edit on a programme other than the seed's, both ways", async () => {
    headerSource = withAnn(1, []);
    serverHoldsOnB([]);
    await renderPage();
    expect(screen.getByText(IDLE)).toBeTruthy();

    await setRole(0, "Music");
    await waitFor(() => expect(screen.getByText(EXCLUDED)).toBeTruthy(), { timeout: 4000 });

    await setRole(0, translate("en", "signature.track.noRole"));
    await waitFor(() => expect(screen.getByText(IDLE)).toBeTruthy(), { timeout: 4000 });
    expect(screen.queryByText(EXCLUDED)).toBeNull();
  }, 15000);

  it("catches up with a change made elsewhere on the page's own refresh", async () => {
    headerSource = withAnn(1, []);
    const setServer = serverHoldsOnB([{ track: 3, role: "music" }]);
    await renderPage();
    expect(screen.getByText(EXCLUDED)).toBeTruthy();

    setServer([]); // another tab removed the mark
    await waitFor(() => expect(screen.getByText(IDLE)).toBeTruthy(), { timeout: 5000 });
    expect(screen.queryByText(EXCLUDED), "a removed mark still reads as excluded").toBeNull();
  }, 15000);

  it("retries a failed read, and says nothing while it is failing", async () => {
    headerSource = withAnn(1, []);
    let fail = true;
    sourceRead.mockImplementation(async (id: number | null) => {
      if (id !== 2) return headerSource;
      if (fail) throw new Error("boom");
      return withAnn(2, [{ track: 3, role: "music" }]);
    });
    await renderPage();
    expect(screen.getByText(translate("en", "route.musicExclusionOn"))).toBeTruthy();

    fail = false;
    await waitFor(() => expect(screen.getByText(EXCLUDED)).toBeTruthy(), { timeout: 5000 });
  }, 15000);

  it("drops to the neutral state, not the old answer, when a refresh fails", async () => {
    headerSource = withAnn(1, []);
    let fail = false;
    sourceRead.mockImplementation(async (id: number | null) => {
      if (id !== 2) return headerSource;
      if (fail) throw new Error("boom");
      return withAnn(2, [{ track: 3, role: "music" }]);
    });
    await renderPage();
    expect(screen.getByText(EXCLUDED)).toBeTruthy();

    fail = true;
    await waitFor(() => expect(screen.getByText(translate("en", "route.musicExclusionOn"))).toBeTruthy(), { timeout: 5000 });
    expect(screen.queryByText(EXCLUDED)).toBeNull();
  }, 15000);

  /* A late response must not overwrite a newer answer. */

  /** Reads of Studio B that wait for the test to answer them, oldest first. */
  function deferredReadsOfB(immediate: number) {
    const pending: ((marks: unknown[]) => void)[] = [];
    let issued = 0;
    sourceRead.mockImplementation((id: number | null) => {
      if (id !== 2) return Promise.resolve(headerSource);
      issued++;
      if (issued <= immediate) return Promise.resolve(withAnn(2, []));
      return new Promise<SourceInfo>((resolve) => pending.push((marks) => resolve(withAnn(2, marks))));
    });
    return pending;
  }

  it("keeps the newer answer when an older read of the programme answers last", async () => {
    headerSource = withAnn(1, []);
    // The page's first two reads of Studio B (the selection's and the poll's)
    // are both left unanswered.
    const pending = deferredReadsOfB(0);
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
    await waitFor(() => expect(pending.length).toBeGreaterThanOrEqual(2));

    await act(async () => pending[1]([{ track: 3, role: "music" }])); // the newer read
    await waitFor(() => expect(screen.getByText(EXCLUDED)).toBeTruthy());
    await act(async () => pending[0]([])); // the older read, arriving late
    await act(async () => {});
    expect(screen.getByText(EXCLUDED), "an older response overwrote the newer answer").toBeTruthy();
    expect(screen.queryByText(IDLE)).toBeNull();
  }, 15000);

  it("keeps the newer answer when a save's re-read is overtaken by a poll", async () => {
    headerSource = withAnn(1, []);
    // Two immediate reads (selection, first poll), then every read waits.
    const pending = deferredReadsOfB(2);
    await renderPage();
    expect(screen.getByText(IDLE)).toBeTruthy();

    await setRole(0, "Music");
    // The save lands and its re-read is issued, then a poll issues another.
    await waitFor(() => expect(pending.length).toBeGreaterThanOrEqual(2), { timeout: 8000 });
    await act(async () => pending[1]([{ track: 0, role: "music" }])); // the poll: newer
    await act(async () => pending[0]([])); // the save's re-read: older, stale
    await act(async () => {});
    await waitFor(() => expect(screen.getByText(EXCLUDED)).toBeTruthy());
  }, 20000);
});
