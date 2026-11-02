// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";

import { OffloadBadge, summarise } from "./OffloadStatus";
import { secretRequired } from "./StorageTargetsCard";
import { api } from "@/lib/api";
import type { RecordingOffload, StorageTarget } from "@/lib/types";

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
    expect(summarise({ targets: [on("verified"), on("stuck")] })).toBe("stuck");
    expect(summarise({ targets: [on("failed"), on("pending")] })).toBe("failed");
    expect(summarise({ targets: [on("uploading")] })).toBe("uploading");
    expect(summarise({ targets: [on("verified")] })).toBe("verified");
  });
});

describe("OffloadBadge", () => {
  it("offers Retry now for a stuck upload and shows its error", async () => {
    const retry = vi.spyOn(api, "retryRecordingOffload").mockResolvedValue({ retried: 1 });
    const changed = vi.fn();
    render(<OffloadBadge recordingId={9} offload={{ targets: [on("stuck", "HTTP 403 AccessDenied")] }} onChanged={changed} />);
    expect(screen.getByTestId("offload-badge").dataset.state).toBe("stuck");
    expect(screen.getByTitle("HTTP 403 AccessDenied")).toBeTruthy();
    fireEvent.click(screen.getByRole("button"));
    await waitFor(() => expect(retry).toHaveBeenCalledWith(9));
    await waitFor(() => expect(changed).toHaveBeenCalled());
  });

  it("shows a verified upload with no action", () => {
    render(<OffloadBadge recordingId={4} offload={{ targets: [on("verified")] }} />);
    expect(screen.getByTestId("offload-badge").dataset.state).toBe("verified");
    expect(screen.queryByRole("button")).toBeNull();
  });

  it("renders nothing for a recording no target has seen", () => {
    const { container } = render(<OffloadBadge recordingId={1} />);
    expect(container.innerHTML).toBe("");
  });
});

describe("secretRequired", () => {
  const saved = {
    endpoint: "https://s3.example",
    bucket: "b1",
    pathStyle: false,
    allowPrivateTarget: false,
    hasSecret: true,
  } as StorageTarget;
  const draft = (over: Record<string, unknown> = {}) =>
    ({ endpoint: "https://s3.example/", bucket: "b1", pathStyle: false, allowPrivateTarget: false, ...over }) as never;
  it("keeps a saved secret for the same address and demands it for another", () => {
    expect(secretRequired(saved, draft())).toEqual({ required: false, rebind: false });
    expect(secretRequired(saved, draft({ endpoint: "https://other.example" }))).toEqual({ required: true, rebind: true });
    expect(secretRequired(saved, draft({ bucket: "b2" }))).toEqual({ required: true, rebind: true });
    expect(secretRequired(saved, draft({ pathStyle: true }))).toEqual({ required: true, rebind: true });
    expect(secretRequired(saved, draft({ allowPrivateTarget: true }))).toEqual({ required: true, rebind: true });
    expect(secretRequired(undefined, draft()).required).toBe(true);
  });
});
