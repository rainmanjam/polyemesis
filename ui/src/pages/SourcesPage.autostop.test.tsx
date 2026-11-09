// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router";

import { SourceCard, SourcesPage } from "./SourcesPage";
import { LiveDataProvider } from "@/components/LiveDataProvider";
import { asSourceId, type SourceView } from "@/lib/types";

/* Stop on ingest loss ends YouTube broadcasts for good, and AutoStopControl is
 * its ONLY writer. The Sources page's own saves (the enable switch, Apply on the
 * ingest draft) used to send the page's whole ingest block back, autoStop
 * included, so a stale tab or an old cached UI re-applied its view of it. And
 * saving the control reloaded the page, throwing away unsaved edits in the
 * card. */

vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn(), warning: vi.fn() } }));

vi.mock("@/lib/api", async () => {
  const actual = await vi.importActual<typeof import("@/lib/api")>("@/lib/api");
  return { ...actual, api: { ...actual.api, listSources: vi.fn(), updateSource: vi.fn() } };
});

import { api } from "@/lib/api";

const armed = { enabled: true, waitSeconds: 120, acknowledged: true };

const source = (autoStop: typeof armed | undefined = armed): SourceView => ({
  id: asSourceId(1),
  name: "Main",
  enabled: true,
  ingest: {
    mode: "srt",
    srt: { passphrase: "", latencyMs: 200 },
    rtmp: { app: "live", streamKey: "" },
    autoStop,
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
});

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

describe("the Sources page and ingest.autoStop", () => {
  // Mutation: send `s.ingest` unstripped in patch(). Observed to fail with
  // "autoStop was sent in the request body".
  it("never sends autoStop in its own saves", async () => {
    vi.mocked(api.listSources).mockResolvedValue([source()]);
    vi.mocked(api.updateSource).mockResolvedValue(source());
    render(
      <MemoryRouter>
        <LiveDataProvider>
          <SourcesPage />
        </LiveDataProvider>
      </MemoryRouter>,
    );
    // The enable switch is a plain page save that carries the whole ingest block.
    fireEvent.click(await screen.findByRole("switch", { name: "Enable Main" }));
    await waitFor(() => expect(api.updateSource).toHaveBeenCalled());
    const body = vi.mocked(api.updateSource).mock.calls[0][1] as { ingest: Record<string, unknown> };
    expect(body.ingest, "the ingest block is still sent").toBeTruthy();
    expect("autoStop" in body.ingest, "autoStop was sent in the request body").toBe(false);
  });

  // Mutation: strip nothing in the card's draft (the old `useState(source.ingest)`
  // keyed on source.ingest). Observed to fail with the typed latency reset.
  it("keeps unsaved edits in the card when the auto-stop control saves, and applies without autoStop", () => {
    const onPatch = vi.fn();
    const draw = (s: SourceView) => (
      <SourceCard source={s} busy={false} onPatch={onPatch} onRotate={() => {}} onDelete={() => {}} />
    );
    const { rerender } = render(draw(source()));
    const latency = screen.getByLabelText(/^Latency \(ms\)/) as HTMLInputElement;
    fireEvent.change(latency, { target: { value: "350" } });
    expect((screen.getByLabelText(/^Latency \(ms\)/) as HTMLInputElement).value).toBe("350");

    // What the page does when AutoStopControl saved: the same source, new autoStop.
    rerender(draw(source({ enabled: false, waitSeconds: 120, acknowledged: false })));
    expect((screen.getByLabelText(/^Latency \(ms\)/) as HTMLInputElement).value, "an unsaved edit was thrown away").toBe("350");

    // The card's own Apply (the auto-stop control has one too, and it is disabled).
    const apply = screen.getAllByRole("button", { name: "Apply" }).find((b) => !(b as HTMLButtonElement).disabled);
    fireEvent.click(apply as HTMLElement);
    const sent = onPatch.mock.calls[0][0] as { ingest: { srt: { latencyMs: number } } & Record<string, unknown> };
    expect(sent.ingest.srt.latencyMs).toBe(350);
    expect("autoStop" in sent.ingest, "autoStop was part of the applied draft").toBe(false);
  });

  it("is not dirtied by the auto-stop control saving", () => {
    const draw = (s: SourceView) => (
      <SourceCard source={s} busy={false} onPatch={() => {}} onRotate={() => {}} onDelete={() => {}} />
    );
    const { rerender } = render(draw(source()));
    expect(screen.queryByRole("button", { name: "Discard" })).toBeNull();
    rerender(draw(source({ enabled: false, waitSeconds: 300, acknowledged: false })));
    expect(screen.queryByRole("button", { name: "Discard" })).toBeNull();
  });

  // Mutation: call load() from onAutoStopSaved. Observed to fail with "the page
  // re-read the sources".
  it("saving the control updates the page in place instead of re-reading the sources", async () => {
    vi.mocked(api.listSources).mockResolvedValue([source()]);
    vi.mocked(api.updateSource).mockResolvedValue(source());
    render(
      <MemoryRouter>
        <LiveDataProvider>
          <SourcesPage />
        </LiveDataProvider>
      </MemoryRouter>,
    );
    fireEvent.change(await screen.findByLabelText(/^Latency \(ms\)/), { target: { value: "350" } });
    const reads = vi.mocked(api.listSources).mock.calls.length;
    fireEvent.click(screen.getByRole("switch", { name: "Stop on ingest loss" })); // off
    const apply = screen.getAllByRole("button", { name: "Apply" }).find((b) => !(b as HTMLButtonElement).disabled);
    fireEvent.click(apply as HTMLElement);
    await waitFor(() => expect(api.updateSource).toHaveBeenCalled());
    expect(vi.mocked(api.updateSource).mock.calls[0][1]).toEqual({
      ingest: { autoStop: { enabled: false } },
    });
    // Let the save settle.
    await new Promise((r) => setTimeout(r, 50));
    expect(vi.mocked(api.listSources).mock.calls.length, "the page re-read the sources").toBe(reads);
    expect((screen.getByLabelText(/^Latency \(ms\)/) as HTMLInputElement).value).toBe("350");
  });
});
