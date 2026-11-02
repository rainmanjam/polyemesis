// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";

import { StorageTargetsCard } from "./StorageTargetsCard";
import { api } from "@/lib/api";
import type { StorageTarget } from "@/lib/types";

const target = (over: Partial<StorageTarget> = {}): StorageTarget => ({
  id: 1,
  name: "archive",
  provider: "r2",
  endpoint: "https://acct.r2.example",
  bucket: "media",
  region: "auto",
  prefixTemplate: "",
  pathStyle: false,
  accessKeyId: "fake-access-key",
  hasSecret: true,
  mode: "move",
  keepLocalHours: 12,
  offloadStems: false,
  offloadTranscripts: false,
  offloadClips: false,
  verifyDownload: false,
  allowPrivateTarget: false,
  enabled: true,
  maxAttempts: 8,
  createdAt: "",
  updatedAt: "",
  counts: { verified: 3, failed: 1, stuck: 2 },
  evicted: 1,
  ...over,
});

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

describe("StorageTargetsCard", () => {
  it("lists a target with its mode and counts, and Retry now re-queues", async () => {
    vi.spyOn(api.storage, "list").mockResolvedValue({ targets: [target()], available: true });
    const retry = vi.spyOn(api.storage, "retry").mockResolvedValue({ retried: 3 });
    render(<StorageTargetsCard />);
    const row = await screen.findByTestId("storage-target");
    expect(row.textContent).toContain("archive");
    expect(row.textContent).toContain("12");
    expect(row.textContent).toContain("3");
    fireEvent.click(within(row).getAllByRole("button")[0]);
    await waitFor(() => expect(retry).toHaveBeenCalledWith(1));
  });

  it("asks for the secret again when the address changes, and tests what is on screen", async () => {
    vi.spyOn(api.storage, "list").mockResolvedValue({ targets: [target({ counts: {} })], available: true });
    const test = vi.spyOn(api.storage, "test").mockResolvedValue({
      ok: false,
      failedStep: "get",
      steps: [
        { step: "put", ok: true },
        { step: "get", ok: false, error: "the object read back is not the object written" },
        { step: "delete", ok: true },
      ],
    });
    const update = vi.spyOn(api.storage, "update").mockResolvedValue(target());
    render(<StorageTargetsCard />);
    const row = await screen.findByTestId("storage-target");
    fireEvent.click(within(row).getAllByRole("button")[0]); // edit
    const endpoint = await screen.findByDisplayValue("https://acct.r2.example");
    expect(screen.queryByTestId("storage-secret-rebind")).toBeNull();

    fireEvent.change(endpoint, { target: { value: "https://elsewhere.example" } });
    expect(screen.getByTestId("storage-secret-rebind")).toBeTruthy();
    const dialog = screen.getByRole("dialog");
    const save = within(dialog).getByRole("button", { name: /^Save$/ });
    expect((save as HTMLButtonElement).disabled).toBe(true);

    fireEvent.change(endpoint, { target: { value: "https://acct.r2.example" } });
    expect(screen.queryByTestId("storage-secret-rebind")).toBeNull();
    fireEvent.click(within(dialog).getByRole("button", { name: /^Test$/ }));
    const result = await screen.findByTestId("storage-test-result");
    expect(result.textContent).toContain("the object read back is not the object written");
    expect(test).toHaveBeenCalledWith(expect.objectContaining({ id: 1, endpoint: "https://acct.r2.example" }));
    expect(test.mock.calls[0][0]).not.toHaveProperty("secretAccessKey");

    fireEvent.click(save);
    await waitFor(() => expect(update).toHaveBeenCalledWith(1, expect.objectContaining({ mode: "move", maxAttempts: 8 })));
  });

  it("says so when the read failed rather than showing an empty list", async () => {
    vi.spyOn(api.storage, "list").mockRejectedValue(new Error("unreachable"));
    render(<StorageTargetsCard />);
    await waitFor(() => expect(screen.queryByTestId("storage-target")).toBeNull());
    expect(screen.getByTestId("storage-targets").textContent).not.toContain("No storage target yet");
  });
});
