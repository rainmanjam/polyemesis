// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";

import { ChatExportDialog } from "./ChatExportDialog";
import { RecordingChatMenu } from "./RecordingChatMenu";
import { downloadChatFile } from "@/lib/chatExport";
import type { Recording } from "@/lib/types";

vi.mock("@/lib/chatExport", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/chatExport")>()),
  downloadChatFile: vi.fn(),
}));

const rec = (over: Partial<Recording> = {}): Recording => ({
  id: 7,
  filename: "rec-s1-20260304-200000.mkv",
  startedAt: "2026-03-04T20:00:00Z",
  bytes: 1,
  durationMs: 1000,
  tracks: 1,
  ...over,
});

/** Radix opens its menu on a primary-button pointerdown, not on click. */
const open = (el: HTMLElement) => fireEvent.pointerDown(el, { button: 0, ctrlKey: false, pointerType: "mouse" });

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

describe("RecordingChatMenu", () => {
  it("offers JSON and subtitles and downloads the chosen one by its sidecar name", async () => {
    vi.mocked(downloadChatFile).mockResolvedValue();
    render(<RecordingChatMenu recording={rec({ chatSaved: true })} />);

    open(screen.getByRole("button", { name: /rec-s1-20260304-200000\.mkv/ }));
    expect(await screen.findByText(/saved with this recording/i)).toBeTruthy();
    fireEvent.click(screen.getByRole("menuitem", { name: /subtitles/i }));

    await waitFor(() => expect(downloadChatFile).toHaveBeenCalledOnce());
    expect(downloadChatFile).toHaveBeenCalledWith(
      "/api/v1/recordings/7/chat?format=vtt",
      "rec-s1-20260304-200000.chat.vtt",
    );
  });

  it("says when no file was saved, and still offers the download", async () => {
    vi.mocked(downloadChatFile).mockResolvedValue();
    render(<RecordingChatMenu recording={rec({ chatSaved: false })} />);

    open(screen.getByRole("button"));
    expect(await screen.findByText(/no chat file was saved/i)).toBeTruthy();
    fireEvent.click(screen.getByRole("menuitem", { name: "JSON" }));
    await waitFor(() =>
      expect(downloadChatFile).toHaveBeenCalledWith(
        "/api/v1/recordings/7/chat?format=json",
        "rec-s1-20260304-200000.chat.json",
      ),
    );
  });

  it("survives a failed download", async () => {
    vi.mocked(downloadChatFile).mockRejectedValue(new Error("gone"));
    render(<RecordingChatMenu recording={rec()} />);
    open(screen.getByRole("button"));
    fireEvent.click(await screen.findByRole("menuitem", { name: "JSON" }));
    // The rejection is handled (a toast), not thrown into the page.
    await waitFor(() => expect(downloadChatFile).toHaveBeenCalled());
  });
});

describe("ChatExportDialog", () => {
  it("refuses a window that ends before it starts and downloads a good one", async () => {
    const click = vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => {});
    render(<ChatExportDialog platforms={["twitch", "youtube"]} />);

    fireEvent.click(screen.getByRole("button", { name: /export chat/i }));
    const from = await screen.findByLabelText("From");
    const to = screen.getByLabelText("To");

    fireEvent.change(from, { target: { value: "2026-03-04T10:00" } });
    fireEvent.change(to, { target: { value: "2026-03-04T09:00" } });
    fireEvent.click(screen.getByRole("button", { name: "Download" }));
    expect(await screen.findByRole("alert")).toBeTruthy();
    expect(click).not.toHaveBeenCalled();

    fireEvent.change(to, { target: { value: "2026-03-04T11:00" } });
    fireEvent.click(screen.getByRole("button", { name: "Download" }));
    await waitFor(() => expect(click).toHaveBeenCalledOnce());
    click.mockRestore();
  });

  it("asks for a start time when it is cleared", async () => {
    const click = vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => {});
    render(<ChatExportDialog platforms={[]} />);
    fireEvent.click(screen.getByRole("button", { name: /export chat/i }));
    fireEvent.change(await screen.findByLabelText("From"), { target: { value: "" } });
    fireEvent.click(screen.getByRole("button", { name: "Download" }));
    expect((await screen.findByRole("alert")).textContent).toMatch(/start time/i);
    expect(click).not.toHaveBeenCalled();
    click.mockRestore();
  });
});
