// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router";

import { SourcesPage } from "./SourcesPage";
import { LiveDataProvider } from "@/components/LiveDataProvider";
import { asSourceId, type SourceView } from "@/lib/types";

/* CREATING A SOURCE CHANGES WHICH PROGRAMME THE CONSOLE FOLLOWS.
 *
 * The console follows one programme, resolved from /sources. That resolution
 * used to run once per page load, so adding a source during first-run setup --
 * the moment it most often happens -- left every other page pointed at the old
 * answer, or at nothing, until a reload. The page now asks the provider to
 * re-resolve. Delete is covered in SourcesPage.delete.test.tsx; this is the
 * create half, and the reason it is separate is that create is the case
 * without a source to start from. #646.
 */

vi.mock("@/lib/api", async () => {
  const actual = await vi.importActual<typeof import("@/lib/api")>("@/lib/api");
  return {
    ...actual,
    api: { ...actual.api, listSources: vi.fn(), createSource: vi.fn(), source: vi.fn() },
  };
});

import { api } from "@/lib/api";

const made: SourceView = {
  id: asSourceId(1),
  name: "Main",
  enabled: true,
  ingest: {
    mode: "srt",
    srt: { passphrase: "", latencyMs: 200 },
    rtmp: { app: "live", streamKey: "" },
  },
  token: "tok",
  position: 0,
  createdAt: "",
  updatedAt: "",
  publishUrls: {},
  isDefault: true,
  tokenEnforced: true,
  publishing: false,
  destinations: 0,
  renditions: 0,
  running: true,
};

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

describe("SourcesPage, creating a source", () => {
  it("re-resolves the programme after the source is created", async () => {
    // Starts empty: the shape of a first run, where the resolved answer is
    // "no programme" and stays that way without this.
    vi.mocked(api.source).mockResolvedValue(null as never);
    vi.mocked(api.listSources).mockResolvedValue([]);
    vi.mocked(api.createSource).mockResolvedValue(made as never);

    render(
      <MemoryRouter>
        <LiveDataProvider>
          <SourcesPage />
        </LiveDataProvider>
      </MemoryRouter>,
    );

    await waitFor(() => expect(vi.mocked(api.listSources).mock.calls.length).toBeGreaterThan(0));
    const before = vi.mocked(api.listSources).mock.calls.length;

    // From here on the install has one source.
    vi.mocked(api.listSources).mockResolvedValue([made]);

    // Two controls match /add source/ on an empty install -- the header button
    // and the empty-state call to action. Either opens the same form; take the
    // first rather than tightening the query, because which one a first-run
    // operator presses is not what this test is about.
    fireEvent.click(screen.getAllByRole("button", { name: /add source/i })[0]);

    const name = await screen.findByLabelText(/name/i);
    fireEvent.change(name, { target: { value: "Main" } });

    const submit = screen
      .getAllByRole("button")
      .find((b) => /^(create|add|save)$/i.test(b.textContent?.trim() ?? ""));
    if (!submit) throw new Error("no submit button in the add-source form");
    fireEvent.click(submit);

    await waitFor(() => expect(vi.mocked(api.createSource)).toHaveBeenCalled());
    // Two reads at minimum: the page's own list(), and the provider's
    // re-resolve. Asserting "more than before" rather than an exact count,
    // because the page is free to reload itself as often as it likes -- the
    // property is that the PROVIDER was told, not how many requests happened.
    await waitFor(() =>
      expect(vi.mocked(api.listSources).mock.calls.length).toBeGreaterThan(before + 1),
    );
  });
});
