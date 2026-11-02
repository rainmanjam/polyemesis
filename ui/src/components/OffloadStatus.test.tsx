// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";

import { OffloadBadge, OffloadFetchNotice, summarise } from "./OffloadStatus";
import { secretRequired } from "./StorageTargetsCard";
import { api } from "@/lib/api";
import type { LibraryRecording, RecordingOffload, StorageTarget } from "@/lib/types";

const on = (state: RecordingOffload["targets"][number]["state"], lastError?: string) => ({
  targetId: 1,
  targetName: "archive",
  state,
  attempts: 1,
  lastError,
});

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

describe("summarise", () => {
  it("names the one thing a Library row should say", () => {
    expect(summarise({ evicted: true, targets: [on("verified")] })).toBe("inStorage");
    expect(summarise({ evicted: true, fetch: { state: "fetching", since: "" }, targets: [] })).toBe("fetching");
    expect(summarise({ evicted: true, fetch: { state: "failed", since: "" }, targets: [] })).toBe("fetchFailed");
    expect(summarise({ evicted: false, targets: [on("verified"), on("stuck")] })).toBe("stuck");
    expect(summarise({ evicted: false, targets: [on("failed"), on("pending")] })).toBe("failed");
    expect(summarise({ evicted: false, targets: [on("uploading")] })).toBe("uploading");
    expect(summarise({ evicted: false, targets: [on("verified")] })).toBe("verified");
  });
});

describe("OffloadBadge", () => {
  it("offers Fetch back for a recording only in storage", async () => {
    const fetch = vi.spyOn(api, "fetchRecording").mockResolvedValue({ fetch: {} });
    const changed = vi.fn();
    render(<OffloadBadge recordingId={4} offload={{ evicted: true, targets: [on("verified")] }} onChanged={changed} />);
    fireEvent.click(screen.getByRole("button"));
    await waitFor(() => expect(changed).toHaveBeenCalled());
    expect(fetch).toHaveBeenCalledWith(4);
  });

  it("offers Retry now for a stuck upload and shows its error", async () => {
    const retry = vi.spyOn(api, "retryRecordingOffload").mockResolvedValue({ retried: 1 });
    render(<OffloadBadge recordingId={9} offload={{ evicted: false, targets: [on("stuck", "HTTP 403 AccessDenied")] }} />);
    expect(screen.getByTestId("offload-badge").dataset.state).toBe("stuck");
    expect(screen.getByTitle("HTTP 403 AccessDenied")).toBeTruthy();
    fireEvent.click(screen.getByRole("button"));
    await waitFor(() => expect(retry).toHaveBeenCalledWith(9));
  });

  it("renders nothing for a recording no target has seen", () => {
    const { container } = render(<OffloadBadge recordingId={1} />);
    expect(container.innerHTML).toBe("");
  });
});

describe("OffloadFetchNotice: how the clipper waits", () => {
  const rec = (offload?: RecordingOffload) => ({ recording: { id: 3, offload } as LibraryRecording, transcriptTracks: null });

  it("asks for the fetch, then opens the editor once the file is local", async () => {
    vi.spyOn(api, "fetchRecording").mockResolvedValue({ fetch: {} });
    vi.spyOn(api, "libraryRecording").mockResolvedValue(rec({ evicted: false, targets: [on("verified")] }));
    const ready = vi.fn();
    render(<OffloadFetchNotice recordingId={3} onReady={ready} />);
    await waitFor(() => expect(ready).toHaveBeenCalled(), { timeout: 3000 });
  });

  it("shows the error and a Retry that asks again after the fetch failed", async () => {
    const fetch = vi.spyOn(api, "fetchRecording").mockResolvedValue({ fetch: {} });
    vi.spyOn(api, "libraryRecording").mockResolvedValue(
      rec({ evicted: true, fetch: { state: "failed", error: "connection reset", since: "" }, targets: [] }),
    );
    render(<OffloadFetchNotice recordingId={3} onReady={vi.fn()} />);
    await screen.findByText(/connection reset/, undefined, { timeout: 3000 });
    fireEvent.click(screen.getByRole("button"));
    await waitFor(() => expect(fetch).toHaveBeenCalledTimes(2));
  });
});

describe("secretRequired", () => {
  const saved = { endpoint: "https://s3.example", bucket: "b1", allowPrivateTarget: false, hasSecret: true } as StorageTarget;
  const draft = (over: Record<string, unknown> = {}) =>
    ({ endpoint: "https://s3.example/", bucket: "b1", allowPrivateTarget: false, ...over }) as never;
  it("keeps a saved secret for the same address and demands it for another", () => {
    expect(secretRequired(saved, draft())).toEqual({ required: false, rebind: false });
    expect(secretRequired(saved, draft({ endpoint: "https://other.example" }))).toEqual({ required: true, rebind: true });
    expect(secretRequired(saved, draft({ bucket: "b2" }))).toEqual({ required: true, rebind: true });
    expect(secretRequired(saved, draft({ allowPrivateTarget: true }))).toEqual({ required: true, rebind: true });
    expect(secretRequired(undefined, draft()).required).toBe(true);
  });
});
