// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router";

import { SourcesPage } from "./SourcesPage";
// SourcesPage now asks the provider to re-resolve the programme after a
// create or delete, because the console follows one source and that answer
// changes when the set does. #646.
import { LiveDataProvider } from "@/components/LiveDataProvider";
import { asSourceId, type SourceView } from "@/lib/types";

/* #18: remove() had no busy guard, unlike patch()/rotate() in this same file.
 *
 * ConfirmDestructive disables its OWN Confirm button while its onConfirm
 * promise is in flight, but that guard dies with the dialog: Escape or an
 * overlay click closes it without waiting for the delete to finish, which
 * hands the row's own Trash2 button back to the operator while the delete is
 * still running underneath -- open to firing a second delete of a source
 * already gone. This pins the fix at the row: the delete button stays
 * disabled from the moment remove() starts until it settles, matching what
 * patch() and rotate() already guarantee for their own buttons.
 *
 * jsdom because ConfirmDestructive is a Radix dialog with a real input to
 * type into; MemoryRouter because the empty state this test drives toward,
 * NoProgrammeYet, links back into the app shell.
 */

vi.mock("@/lib/api", async () => {
  const actual = await vi.importActual<typeof import("@/lib/api")>("@/lib/api");
  return { ...actual, api: { ...actual.api, listSources: vi.fn(), deleteSource: vi.fn() } };
});

import { api } from "@/lib/api";

const source: SourceView = {
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

describe("SourcesPage, deleting a source", () => {
  it("disables the row's delete button for as long as the delete is in flight", async () => {
    let resolveDelete: () => void = () => {};
    vi.mocked(api.listSources).mockResolvedValue([source]);
    vi.mocked(api.deleteSource).mockReturnValue(
      new Promise<void>((resolve) => {
        resolveDelete = resolve;
      }),
    );

    render(
      <MemoryRouter>
        <LiveDataProvider>
          <SourcesPage />
        </LiveDataProvider>
      </MemoryRouter>,
    );

    // jest-dom is not installed (see DebugSettings.test.tsx), so `disabled`
    // is read off the button directly rather than through a matcher.
    const deleteButton = (await screen.findByLabelText("Delete Main")) as HTMLButtonElement;
    expect(deleteButton.disabled).toBe(false);

    fireEvent.click(deleteButton);

    const typeField = await screen.findByLabelText(/Type/);
    fireEvent.change(typeField, { target: { value: "Main" } });
    fireEvent.click(screen.getByRole("button", { name: "Delete source" }));

    // The delete is in flight -- api.deleteSource has been called and its
    // promise is deliberately held open. Without the fix this button stays
    // enabled the whole time, because remove() never told the row it was
    // busy.
    await waitFor(() => expect(api.deleteSource).toHaveBeenCalledWith(source.id));
    await waitFor(() => expect(deleteButton.disabled).toBe(true));

    vi.mocked(api.listSources).mockResolvedValue([]);
    resolveDelete();

    // load() runs after the delete resolves; once it lands the row -- and its
    // button -- is gone rather than merely re-enabled, which is the only way
    // to confirm remove()'s finally actually ran.
    await waitFor(() => expect(screen.queryByLabelText("Delete Main")).toBeNull());
  });
});
