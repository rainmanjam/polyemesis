// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";

import { BroadcastTemplateEditor } from "./BroadcastTemplateEditor";
import type { BroadcastTemplate } from "@/lib/types";

/* The recurring-broadcast template in the schedule dialog (#18). What the operator must be able to trust: the form
 * shows the stored template, a save sends exactly the form, a refused look-ahead create stays visible as a banner,
 * a picture the server would refuse is refused here before any upload, and a removal goes through the confirmation
 * and leaves the form empty. */

const mocks = vi.hoisted(() => ({
  getTemplate: vi.fn(),
  saveTemplate: vi.fn(),
  deleteTemplate: vi.fn(),
  uploadThumbnail: vi.fn(),
  toastSuccess: vi.fn(),
}));

vi.mock("@/lib/recurringApi", async (importOriginal) => {
  const real = await importOriginal<typeof import("@/lib/recurringApi")>();
  return {
    ...real,
    getTemplate: mocks.getTemplate,
    saveTemplate: mocks.saveTemplate,
    deleteTemplate: mocks.deleteTemplate,
    uploadThumbnail: mocks.uploadThumbnail,
  };
});

vi.mock("sonner", () => ({ toast: { success: mocks.toastSuccess, error: vi.fn() } }));

afterEach(() => {
  cleanup();
  for (const m of Object.values(mocks)) m.mockReset();
});

const stored: BroadcastTemplate = {
  scheduleId: 4,
  titlePattern: "Tuesday live {date}",
  description: "Every week at seven.",
  privacy: "unlisted",
  category: "20",
  lookahead: 3,
  hasThumbnail: false,
};

const titleInput = () => screen.getByLabelText("Title pattern") as HTMLInputElement;
const lookaheadInput = () => screen.getByLabelText("Upcoming events to keep") as HTMLInputElement;
const saveButton = () => screen.getByRole("button", { name: "Save template" }) as HTMLButtonElement;
const pictureInput = () => screen.getByLabelText("Choose picture") as HTMLInputElement;

describe("BroadcastTemplateEditor", () => {
  it("fills the form from the stored template and saves exactly what the form holds", async () => {
    mocks.getTemplate.mockResolvedValue(stored);
    mocks.saveTemplate.mockResolvedValue({ ...stored, titlePattern: "Weekly {date}" });

    render(<BroadcastTemplateEditor scheduleId={4} />);
    await waitFor(() => expect(titleInput().value).toBe("Tuesday live {date}"));
    expect(lookaheadInput().value).toBe("3");
    expect((screen.getByLabelText("Privacy") as HTMLSelectElement).value).toBe("unlisted");

    fireEvent.change(titleInput(), { target: { value: "Weekly {date}" } });
    fireEvent.change(screen.getByLabelText("Privacy"), { target: { value: "private" } });
    fireEvent.click(saveButton());

    await waitFor(() => expect(mocks.saveTemplate).toHaveBeenCalledTimes(1));
    expect(mocks.saveTemplate).toHaveBeenCalledWith(4, {
      titlePattern: "Weekly {date}",
      description: "Every week at seven.",
      privacy: "private",
      category: "20",
      lookahead: 3,
    });
    await waitFor(() => expect(mocks.toastSuccess).toHaveBeenCalledWith("Template saved."));
  });

  // MUTATION: lookaheadOk dropped from the save button's disabled. Observed: FAIL, the button was enabled at 11.
  it("holds the save shut for an empty title or a look-ahead outside 1 to 10", async () => {
    mocks.getTemplate.mockResolvedValue(stored);
    render(<BroadcastTemplateEditor scheduleId={4} />);
    await waitFor(() => expect(saveButton().disabled).toBe(false));

    fireEvent.change(lookaheadInput(), { target: { value: "11" } });
    expect(saveButton().disabled).toBe(true);
    fireEvent.change(lookaheadInput(), { target: { value: "0" } });
    expect(saveButton().disabled).toBe(true);
    fireEvent.change(lookaheadInput(), { target: { value: "10" } });
    expect(saveButton().disabled).toBe(false);

    fireEvent.change(titleInput(), { target: { value: "   " } });
    expect(saveButton().disabled).toBe(true);
  });

  it("shows a schedule with no template as such, with defaults and no picture or remove control", async () => {
    mocks.getTemplate.mockResolvedValue(null);
    render(<BroadcastTemplateEditor scheduleId={4} />);

    await waitFor(() => expect(screen.getByText("No template yet. Upcoming events need one.")).toBeTruthy());
    expect((screen.getByLabelText("Privacy") as HTMLSelectElement).value).toBe("public");
    expect(lookaheadInput().value).toBe("2");
    expect(pictureInput().disabled).toBe(true);
    expect(screen.queryByRole("button", { name: "Remove template" })).toBeNull();
  });

  it("keeps the platform's last refusal visible as a banner with when it happened", async () => {
    mocks.getTemplate.mockResolvedValue({
      ...stored,
      lastError: "quota: too many upcoming events",
      lastErrorAt: "2026-11-01T10:00:00Z",
    });
    render(<BroadcastTemplateEditor scheduleId={4} />);

    const banner = await screen.findByText("The platform refused the last upcoming event");
    const alert = banner.closest("[role=alert]") as HTMLElement;
    expect(within(alert).getByText("quota: too many upcoming events")).toBeTruthy();
    expect(within(alert).getByText(/^Refused at .+\. The next check tries again\.$/)).toBeTruthy();
  });

  it("says why the template could not be read, and why a save was refused", async () => {
    mocks.getTemplate.mockRejectedValueOnce(new Error("database is locked"));
    const { unmount } = render(<BroadcastTemplateEditor scheduleId={4} />);
    expect((await screen.findByRole("alert")).textContent).toBe("database is locked");
    unmount();

    mocks.getTemplate.mockResolvedValue(stored);
    mocks.saveTemplate.mockRejectedValue(new Error("the title pattern is too long"));
    render(<BroadcastTemplateEditor scheduleId={4} />);
    await waitFor(() => expect(saveButton().disabled).toBe(false));
    fireEvent.click(saveButton());
    expect((await screen.findByRole("alert")).textContent).toBe("the title pattern is too long");
    expect(mocks.toastSuccess).not.toHaveBeenCalled();
  });

  it("refuses a picture of the wrong type or over 2 MB before any upload", async () => {
    mocks.getTemplate.mockResolvedValue(stored);
    render(<BroadcastTemplateEditor scheduleId={4} />);
    await waitFor(() => expect(pictureInput().disabled).toBe(false));

    fireEvent.change(pictureInput(), { target: { files: [new File(["x"], "a.gif", { type: "image/gif" })] } });
    expect((await screen.findByRole("alert")).textContent).toBe("A thumbnail must be a PNG or a JPEG.");

    const big = new File([new Uint8Array(2 * 1024 * 1024 + 1)], "a.png", { type: "image/png" });
    fireEvent.change(pictureInput(), { target: { files: [big] } });
    await waitFor(() =>
      expect(screen.getByRole("alert").textContent).toBe("That picture is larger than 2 MB."),
    );
    expect(mocks.uploadThumbnail).not.toHaveBeenCalled();
  });

  it("uploads an accepted picture and rereads the template, so the stored flag appears", async () => {
    mocks.getTemplate.mockResolvedValueOnce(stored).mockResolvedValueOnce({ ...stored, hasThumbnail: true });
    mocks.uploadThumbnail.mockResolvedValue(undefined);
    render(<BroadcastTemplateEditor scheduleId={4} />);
    await waitFor(() => expect(pictureInput().disabled).toBe(false));

    const pic = new File([new Uint8Array([0x89, 0x50])], "a.png", { type: "image/png" });
    fireEvent.change(pictureInput(), { target: { files: [pic] } });

    await screen.findByText("A picture is stored for this template.");
    expect(mocks.uploadThumbnail).toHaveBeenCalledWith(4, pic, "image/png");
    expect(mocks.getTemplate).toHaveBeenCalledTimes(2);
  });

  it("names the server's refusal of a picture", async () => {
    mocks.getTemplate.mockResolvedValue(stored);
    mocks.uploadThumbnail.mockRejectedValue(new Error("the bytes are not a PNG"));
    render(<BroadcastTemplateEditor scheduleId={4} />);
    await waitFor(() => expect(pictureInput().disabled).toBe(false));

    fireEvent.change(pictureInput(), {
      target: { files: [new File(["x"], "a.png", { type: "image/png" })] },
    });
    expect((await screen.findByRole("alert")).textContent).toBe(
      "The picture was not stored: the bytes are not a PNG",
    );
  });

  it("removes the template only through the confirmation, and then shows the empty form", async () => {
    mocks.getTemplate.mockResolvedValue(stored);
    mocks.deleteTemplate.mockResolvedValue(undefined);
    render(<BroadcastTemplateEditor scheduleId={4} />);
    await waitFor(() => expect(titleInput().value).toBe("Tuesday live {date}"));

    fireEvent.click(screen.getByRole("button", { name: "Remove template" }));
    expect(mocks.deleteTemplate).not.toHaveBeenCalled();
    const dialog = await screen.findByRole("dialog");
    fireEvent.click(within(dialog).getByRole("button", { name: "Remove template" }));

    await waitFor(() => expect(mocks.deleteTemplate).toHaveBeenCalledWith(4));
    await screen.findByText("No template yet. Upcoming events need one.");
    expect(titleInput().value).toBe("");
  });

  // A failed read must not leave an editable form of defaults behind: saving it would overwrite a private or
  // unlisted template with "public", look-ahead 2 and no category, values the operator was never shown.
  // MUTATION: render the form when the load failed. Observed: FAIL, the Save button was present.
  it("shows no editable form when the stored template could not be read, and retries on request", async () => {
    mocks.getTemplate.mockRejectedValueOnce(new Error("server unavailable"));
    mocks.getTemplate.mockResolvedValueOnce(stored);
    render(<BroadcastTemplateEditor scheduleId={4} />);

    expect((await screen.findByRole("alert")).textContent).toBe("server unavailable");
    expect(screen.queryByRole("button", { name: "Save template" })).toBeNull();
    expect(screen.queryByLabelText("Title pattern")).toBeNull();
    expect(mocks.saveTemplate).not.toHaveBeenCalled();

    fireEvent.click(screen.getByRole("button", { name: "Retry" }));
    await waitFor(() => expect(titleInput().value).toBe("Tuesday live {date}"));
    expect((screen.getByLabelText("Privacy") as HTMLSelectElement).value).toBe("unlisted");
  });
});
