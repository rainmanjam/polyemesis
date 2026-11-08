// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";

import { ChatUserCard } from "./ChatUserCard";
import { api } from "@/lib/api";
import type { ChatUserCard as CardData } from "@/lib/types";

/* THE OPERATOR'S PRIVATE NOTE ON THE CARD: explicit Save, text only, capped. */

const chatUser = vi.spyOn(api, "chatUser");
const noteGet = vi.spyOn(api, "viewerNote");
const noteSave = vi.spyOn(api, "saveViewerNote");
const noteDelete = vi.spyOn(api, "deleteViewerNote");
vi.spyOn(api, "viewerTags").mockResolvedValue({ viewers: [], presetTags: [] });

const empty = { platform: "kick", authorId: "u1", note: "", tags: [] as string[] };

function draw() {
  chatUser.mockResolvedValue({ platform: "kick", authorId: "u1", name: "someone", messages: [], truncated: false, retentionNote: "" } as CardData);
  return render(<ChatUserCard platform="kick" account="acct" authorId="u1" authorName="someone" open onOpenChange={() => {}} />);
}

describe("viewer notes on the card", () => {
  beforeEach(() => {
    noteGet.mockReset();
    noteSave.mockReset();
    noteDelete.mockReset();
  });
  afterEach(cleanup);

  it("loads the note for this viewer in this channel and does not save until Save is pressed", async () => {
    noteGet.mockResolvedValue({ ...empty, note: "likes memes", tags: ["regular"] } as never);
    draw();
    const box = (await screen.findByDisplayValue("likes memes")) as HTMLTextAreaElement;
    expect(noteGet).toHaveBeenCalledWith({ platform: "kick", account: "acct", authorId: "u1" });
    expect((screen.getByRole("button", { name: "Save note" }) as HTMLButtonElement).disabled).toBe(true);
    fireEvent.change(box, { target: { value: "likes memes a lot" } });
    expect(noteSave).not.toHaveBeenCalled();
    expect((screen.getByRole("button", { name: "Save note" }) as HTMLButtonElement).disabled).toBe(false);
  });

  it("saves the note and tags from the preset chips and a free tag", async () => {
    noteGet.mockResolvedValue(empty as never);
    noteSave.mockImplementation(async (_k, body) => ({ ...empty, ...body }) as never);
    draw();
    const box = await screen.findByLabelText("Notes");
    await waitFor(() => expect((box as HTMLTextAreaElement).disabled).toBe(false));
    fireEvent.change(box, { target: { value: "  raided us twice " } });
    fireEvent.click(screen.getByRole("button", { name: /known troll/ }));
    fireEvent.change(screen.getByLabelText("New tag"), { target: { value: "night owl" } });
    fireEvent.click(screen.getByRole("button", { name: "Add tag" }));
    fireEvent.click(screen.getByRole("button", { name: "Save note" }));
    await waitFor(() =>
      expect(noteSave).toHaveBeenCalledWith(
        { platform: "kick", account: "acct", authorId: "u1" },
        { note: "raided us twice", tags: ["known troll", "night owl"] },
      ),
    );
  });

  it("renders a note as text, never as HTML", async () => {
    noteGet.mockResolvedValue({ ...empty, note: "<img src=x onerror=alert(1)>", tags: ["<b>bold</b>"] } as never);
    const { container } = draw();
    await screen.findByDisplayValue("<img src=x onerror=alert(1)>");
    expect(screen.getByText("<b>bold</b>")).toBeTruthy();
    expect(document.body.querySelector("img")).toBeNull();
    expect(container.querySelector("b")).toBeNull();
  });

  it("caps the note and the tag count at the server's limits", async () => {
    noteGet.mockResolvedValue({ ...empty, tags: ["1", "2", "3", "4", "5", "6", "7", "8", "9", "10"] } as never);
    draw();
    const box = (await screen.findByLabelText("Notes")) as HTMLTextAreaElement;
    expect(box.maxLength).toBe(1000);
    fireEvent.change(screen.getByLabelText("New tag"), { target: { value: "eleventh" } });
    fireEvent.click(screen.getByRole("button", { name: "Add tag" }));
    expect(screen.queryByText("eleventh")).toBeNull();
  });

  it("deleting asks again inline", async () => {
    noteGet.mockResolvedValue({ ...empty, note: "x" } as never);
    noteDelete.mockResolvedValue({ status: "deleted" });
    draw();
    await screen.findByDisplayValue("x");
    fireEvent.click(screen.getByRole("button", { name: "Delete note" }));
    expect(noteDelete).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: /Delete note — confirm/ }));
    await waitFor(() => expect(noteDelete).toHaveBeenCalledWith({ platform: "kick", account: "acct", authorId: "u1" }));
  });

  it("a failed read says so and offers retry instead of an editable empty note", async () => {
    noteGet.mockRejectedValue(new Error("boom"));
    draw();
    await waitFor(() => expect(screen.getByText("Could not load this viewer's note.")).toBeTruthy());
    expect(screen.queryByLabelText("Notes")).toBeNull();
  });
});
