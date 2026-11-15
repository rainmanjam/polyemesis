// @vitest-environment jsdom

import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
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
const ON = translate("en", "route.musicExclusionOn");
const RAIL_NO_MUSIC = translate("en", "route.railNoMusic");

/** The Music rights card, found as the nearest ancestor of its own switch that
 *  also holds its heading. Assertions about the card go through this so the
 *  Destinations list beside it can never satisfy them. */
function musicCard(): HTMLElement {
  let el = screen.getByLabelText(translate("en", "route.keepMusicOut")).parentElement;
  while (el && !within(el).queryByText(translate("en", "routing.overview.musicRights"))) el = el.parentElement;
  if (!el) throw new Error("the Music rights card was not found");
  return el;
}
/** The Studio B destination's row in the Destinations list. */
function listRow(): HTMLElement {
  return screen.getByRole("button", { name: /Studio B to YouTube/ });
}
const inCard = () => within(musicCard());
const inRow = () => within(listRow());

describe("the music badge on a two-programme install (destination on Studio B, header on Studio A)", () => {
  it("does not say music is excluded when only the header's programme has a music track", async () => {
    headerSource = withAnn(1, [{ track: 1, role: "music" }]);
    destSource = withAnn(2, []);
    await renderPage();
    expect(inCard().queryByText(EXCLUDED), "Studio A's music was counted for Studio B's destination").toBeNull();
    expect(inCard().getByText(IDLE)).toBeTruthy();
    expect(inRow().queryByText(EXCLUDED)).toBeNull();
  });

  it("says music is excluded when the destination's own programme has the music track", async () => {
    headerSource = withAnn(1, []);
    destSource = withAnn(2, [{ track: 3, role: "music" }]);
    await renderPage();
    expect(inCard().getByText(EXCLUDED)).toBeTruthy();
    expect(inCard().queryByText(IDLE)).toBeNull();
  });

  it("claims nothing while the destination's programme has not been read", async () => {
    headerSource = withAnn(1, []);
    destSource = new Promise<SourceInfo>(() => {});
    await renderPage();
    expect(inCard().getByText(ON)).toBeTruthy();
    expect(inCard().queryByText(EXCLUDED)).toBeNull();
    expect(inCard().queryByText(IDLE)).toBeNull();
  });

  it("claims nothing when that read fails", async () => {
    headerSource = withAnn(1, [{ track: 1, role: "music" }]);
    sourceRead.mockImplementation(async (id: number | null) => {
      if (id === 2) throw new Error("boom");
      return headerSource;
    });
    await renderPage();
    expect(inCard().getByText(ON)).toBeTruthy();
    expect(inCard().queryByText(EXCLUDED)).toBeNull();
    expect(inCard().queryByText(IDLE)).toBeNull();
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
    const trigger = await screen.findByLabelText(roleAria);
    await act(async () => {
      fireEvent.pointerDown(trigger, { button: 0, ctrlKey: false, pointerType: "mouse" });
      fireEvent.click(trigger);
    });
    const opt = await screen.findByRole("option", { name: role });
    await act(async () => fireEvent.click(opt));
  }
  const NO_ROLE = translate("en", "signature.track.noRole");

  it("follows an in-page edit on a programme other than the seed's, both ways", async () => {
    headerSource = withAnn(1, []);
    serverHoldsOnB([]);
    await renderPage();
    expect(inCard().getByText(IDLE)).toBeTruthy();

    await setRole(0, "Music");
    await waitFor(() => expect(inCard().getByText(EXCLUDED)).toBeTruthy(), { timeout: 4000 });

    await setRole(0, NO_ROLE);
    await waitFor(() => expect(inCard().getByText(IDLE)).toBeTruthy(), { timeout: 4000 });
    expect(inCard().queryByText(EXCLUDED)).toBeNull();
  }, 15000);

  it("catches up with a change made elsewhere on the page's own refresh", async () => {
    headerSource = withAnn(1, []);
    const setServer = serverHoldsOnB([{ track: 3, role: "music" }]);
    await renderPage();
    expect(inCard().getByText(EXCLUDED)).toBeTruthy();

    setServer([]); // another tab removed the mark
    await waitFor(() => expect(inCard().getByText(IDLE)).toBeTruthy(), { timeout: 5000 });
    expect(inCard().queryByText(EXCLUDED), "a removed mark still reads as excluded").toBeNull();
    expect(inRow().queryByText(EXCLUDED)).toBeNull();
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
    expect(inCard().getByText(ON)).toBeTruthy();

    fail = false;
    await waitFor(() => expect(inCard().getByText(EXCLUDED)).toBeTruthy(), { timeout: 5000 });
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
    expect(inCard().getByText(EXCLUDED)).toBeTruthy();

    fail = true;
    await waitFor(() => expect(inCard().getByText(ON)).toBeTruthy(), { timeout: 5000 });
    expect(inCard().queryByText(EXCLUDED)).toBeNull();
  }, 15000);

  /* Reads that overlap, run slowly, or finish out of order. */

  /** Reads of Studio B: the first `immediate` answer at once with no marks, the
   *  rest wait for the test. `pending` holds a resolver per waiting read, in the
   *  order the page issued them. */
  function deferredReadsOfB(immediate: number) {
    const pending: ((marks: unknown[]) => void)[] = [];
    let issued = 0;
    sourceRead.mockImplementation((id: number | null) => {
      if (id !== 2) return Promise.resolve(headerSource);
      issued++;
      if (issued <= immediate) return Promise.resolve(withAnn(2, []));
      return new Promise<SourceInfo>((resolve) => pending.push((marks) => resolve(withAnn(2, marks))));
    });
    (api.putAnnotations as unknown as ReturnType<typeof vi.fn>).mockResolvedValue(undefined);
    return pending;
  }
  const MUSIC = [{ track: 0, role: "music" }];

  it("applies a read that takes longer than one poll interval when it returns", async () => {
    headerSource = withAnn(1, []);
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
    await waitFor(() => expect(pending.length).toBe(1));
    // Longer than the 2 s poll: no second read is started while this one is out.
    await act(async () => new Promise((r) => setTimeout(r, 2600)));
    expect(pending.length, "a poll started a second read while one was in flight").toBe(1);

    await act(async () => pending[0](MUSIC));
    await waitFor(() => expect(inCard().getByText(EXCLUDED)).toBeTruthy());
  }, 15000);

  it("ends on the newest of three overlapping reads however they finish", async () => {
    headerSource = withAnn(1, []);
    const pending = deferredReadsOfB(0); // 1: the selection's read, left slow
    await renderPage().catch(() => {});
    await setRole(0, "Music"); // saves, then re-reads: 2
    await waitFor(() => expect(pending.length).toBeGreaterThanOrEqual(2), { timeout: 4000 });
    await setRole(0, NO_ROLE); // saves, then re-reads: 3
    await waitFor(() => expect(pending.length).toBeGreaterThanOrEqual(3), { timeout: 4000 });

    await act(async () => pending[2]([])); // newest, no marks
    await waitFor(() => expect(inCard().getByText(IDLE)).toBeTruthy());
    await act(async () => pending[0](MUSIC)); // oldest, arriving last
    await act(async () => pending[1](MUSIC)); // older still
    await act(async () => {});
    expect(inCard().getByText(IDLE), "an older response overwrote the newest").toBeTruthy();
    expect(inCard().queryByText(EXCLUDED)).toBeNull();
  }, 20000);

  it("keeps the newer answer when a save's re-read overtakes a slow read", async () => {
    headerSource = withAnn(1, []);
    const pending = deferredReadsOfB(0); // 1: slow
    await renderPage().catch(() => {});
    await setRole(0, "Music");
    await waitFor(() => expect(pending.length).toBeGreaterThanOrEqual(2), { timeout: 4000 });
    await act(async () => pending[1](MUSIC)); // the re-read
    await waitFor(() => expect(inCard().getByText(EXCLUDED)).toBeTruthy());
    await act(async () => pending[0]([])); // the slow read, stale, last
    await act(async () => {});
    expect(inCard().getByText(EXCLUDED), "a stale slow read overwrote the re-read").toBeTruthy();
  }, 20000);

  it("stays neutral through a second edit until a read after ITS save lands", async () => {
    headerSource = withAnn(1, []);
    const pending = deferredReadsOfB(1); // the selection's read answers at once
    await renderPage();
    await setRole(0, "Music"); // first save, then its re-read: pending[0]
    await waitFor(() => expect(pending.length).toBe(1), { timeout: 4000 });
    await setRole(0, NO_ROLE); // second edit, while that re-read is out

    await act(async () => pending[0](MUSIC)); // the first re-read lands
    await act(async () => {});
    expect(inCard().getByText(ON), "the first re-read ended the unsaved state of the second edit").toBeTruthy();
    expect(inCard().queryByText(EXCLUDED)).toBeNull();

    await waitFor(() => expect(pending.length).toBe(2), { timeout: 4000 }); // the second save's re-read
    expect(inCard().getByText(ON)).toBeTruthy();
    await act(async () => pending[1]([]));
    await waitFor(() => expect(inCard().getByText(IDLE)).toBeTruthy());
  }, 20000);

  /* The destinations list (the rail) beside the card. */

  it("does not say \"no music\" in the list for a destination with no marked music track", async () => {
    headerSource = withAnn(1, []);
    serverHoldsOnB([]);
    await renderPage();
    expect(inRow().queryByText(RAIL_NO_MUSIC), "the list claims no music where nothing is marked").toBeNull();
    expect(inRow().getByText(ON)).toBeTruthy();
  });

  it("does not say \"no music\" in the list while the destination's marks are unknown", async () => {
    headerSource = withAnn(1, [{ track: 1, role: "music" }]);
    destSource = new Promise<SourceInfo>(() => {});
    await renderPage();
    expect(inRow().queryByText(RAIL_NO_MUSIC)).toBeNull();
    expect(inRow().queryByText(EXCLUDED)).toBeNull();
    expect(inRow().getByText(ON)).toBeTruthy();
  });

  it("says music is excluded in the list and on the card once the destination's own programme marks a track", async () => {
    headerSource = withAnn(1, []);
    serverHoldsOnB([{ track: 3, role: "music" }]);
    await renderPage();
    expect(inRow().getByText(EXCLUDED)).toBeTruthy();
    expect(inCard().getByText(EXCLUDED)).toBeTruthy();
    expect(inRow().queryByText(RAIL_NO_MUSIC)).toBeNull();
  });
});
