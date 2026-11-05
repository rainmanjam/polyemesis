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
    await waitFor(() => expect(update).toHaveBeenCalledWith(1, expect.objectContaining({ bucket: "media", maxAttempts: 8 })));
  });

  it("says so when the read failed rather than showing an empty list", async () => {
    vi.spyOn(api.storage, "list").mockRejectedValue(new Error("unreachable"));
    render(<StorageTargetsCard />);
    await waitFor(() => expect(screen.queryByTestId("storage-target")).toBeNull());
    expect(screen.getByTestId("storage-targets").textContent).not.toContain("No storage target yet");
  });
});

describe("StorageTargetsCard: a response that is not the list", () => {
  it("says the read failed instead of crashing", async () => {
    vi.spyOn(api.storage, "list").mockResolvedValue({} as never);
    render(<StorageTargetsCard />);
    await screen.findByText("Could not load the storage targets.");
    expect(screen.queryByText("No storage target yet.")).toBeNull();
  });
});

describe("StorageTargetsCard: adding and deleting", () => {
  it("creates a target from the form with the secret it was given", async () => {
    vi.spyOn(api.storage, "list").mockResolvedValue({ targets: [], available: true });
    const create = vi.spyOn(api.storage, "create").mockResolvedValue(target());
    render(<StorageTargetsCard />);
    await screen.findByText("No storage target yet.");
    fireEvent.click(screen.getByRole("button", { name: /Add target/ }));
    const dialog = await screen.findByRole("dialog");
    const save = within(dialog).getByRole("button", { name: /^Save$/ });
    expect((save as HTMLButtonElement).disabled).toBe(true); // a new target needs its secret
    fireEvent.change(within(dialog).getByLabelText("Name"), { target: { value: "archive" } });
    fireEvent.change(within(dialog).getByLabelText("Endpoint"), { target: { value: "https://s3.example" } });
    fireEvent.change(within(dialog).getByLabelText("Bucket"), { target: { value: "media" } });
    fireEvent.change(within(dialog).getByLabelText("Access key ID"), { target: { value: "fake-access-key" } });
    fireEvent.change(within(dialog).getByLabelText(/Secret access key/), { target: { value: "fake-secret-value-not-real" } });
    fireEvent.click(save);
    await waitFor(() =>
      expect(create).toHaveBeenCalledWith(
        expect.objectContaining({ name: "archive", bucket: "media", secretAccessKey: "fake-secret-value-not-real" }),
      ),
    );
  });

  it("deletes a target after confirming", async () => {
    vi.spyOn(api.storage, "list").mockResolvedValue({ targets: [target({ counts: {} })], available: true });
    const remove = vi.spyOn(api.storage, "remove").mockResolvedValue({ status: "deleted" });
    render(<StorageTargetsCard />);
    const row = await screen.findByTestId("storage-target");
    fireEvent.click(within(row).getByRole("button", { name: "Delete target" }));
    fireEvent.click(await screen.findByRole("button", { name: /^Delete$/ }));
    await waitFor(() => expect(remove).toHaveBeenCalledWith(1));
  });

  // #14 of the 2026-11-04 lingering-state audit: the delete always succeeds,
  // and says which unfinished uploads it could not abort. The sentence sends
  // the operator to their provider "using the keys and upload ids below", so
  // the key and upload id of each are what has to stay on screen -- not a
  // ten-second toast of the sentence alone.
  it("keeps the key and upload id of each upload a delete could not abort on screen, copyable", async () => {
    vi.spyOn(api.storage, "list").mockResolvedValue({ targets: [target({ counts: {} })], available: true });
    vi.spyOn(api.storage, "remove").mockResolvedValue({
      status: "deleted",
      warning: "2 unfinished uploads could not be cancelled and will keep billing",
      unabortedUploads: [
        { key: "recordings/2026/rec-a.mkv", uploadId: "upload-id-aaa111", relPath: "rec-a.mkv" },
        { key: "recordings/2026/rec-b.mkv", uploadId: "upload-id-bbb222", relPath: "rec-b.mkv" },
      ],
    });
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, "clipboard", { value: { writeText }, configurable: true });
    render(<StorageTargetsCard />);
    const row = await screen.findByTestId("storage-target");
    fireEvent.click(within(row).getByRole("button", { name: "Delete target" }));
    fireEvent.click(await screen.findByRole("button", { name: /^Delete$/ }));

    const panel = await screen.findByTestId("storage-unaborted");
    expect(panel.textContent).toContain("2 unfinished uploads could not be cancelled and will keep billing");
    const cells = within(panel)
      .getAllByRole("cell")
      .map((c) => c.textContent);
    expect(cells).toEqual([
      "recordings/2026/rec-a.mkv",
      "upload-id-aaa111",
      "recordings/2026/rec-b.mkv",
      "upload-id-bbb222",
    ]);

    fireEvent.click(within(panel).getByRole("button", { name: "Copy" }));
    expect(writeText).toHaveBeenCalledWith(
      "recordings/2026/rec-a.mkv\tupload-id-aaa111\nrecordings/2026/rec-b.mkv\tupload-id-bbb222",
    );

    // It stays until it is closed: the reload after the delete does not clear it.
    await waitFor(() => expect(api.storage.list).toHaveBeenCalledTimes(2));
    expect(screen.getByTestId("storage-unaborted")).toBeTruthy();
    fireEvent.click(within(panel).getByRole("button", { name: "Close" }));
    expect(screen.queryByTestId("storage-unaborted")).toBeNull();
  });

  it("reports a failed save and keeps the dialog open", async () => {
    vi.spyOn(api.storage, "list").mockResolvedValue({ targets: [target({ counts: {} })], available: false });
    vi.spyOn(api.storage, "update").mockRejectedValue(new Error("bucket must be 3-63 characters"));
    render(<StorageTargetsCard />);
    await screen.findByText(/Offload is not running on this server/);
    const row = await screen.findByTestId("storage-target");
    fireEvent.click(within(row).getByRole("button", { name: "Edit target" }));
    const dialog = await screen.findByRole("dialog");
    fireEvent.click(within(dialog).getByRole("button", { name: /^Save$/ }));
    await waitFor(() => expect(screen.getByRole("dialog")).toBeTruthy());
  });
});

// The editor holds a secret access key. Esc on a touched form used to close it
// with the secret gone (poka-yoke audit 2026-11-04).
describe("StorageTargetsCard editor dismissal", () => {
  it("keeps a touched editor open on Esc", async () => {
    vi.spyOn(api.storage, "list").mockResolvedValue({ targets: [], available: true });
    render(<StorageTargetsCard />);
    fireEvent.click(await screen.findByRole("button", { name: /add|new/i }));
    const dialog = await screen.findByRole("dialog");
    const field = within(dialog).getAllByRole("textbox")[0];
    fireEvent.change(field, { target: { value: "typed" } });
    fireEvent.keyDown(field, { key: "Escape" });
    expect(screen.queryByRole("dialog")).not.toBeNull();
  });
});
