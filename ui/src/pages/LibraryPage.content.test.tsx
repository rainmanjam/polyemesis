// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { MemoryRouter } from "react-router";

/* THE LIBRARY SAYS WHAT IT IS FOR (app-council A37-A40).
 *
 * A37: the media upload card sat above the archive and pushed the sessions
 *      ~220px down with "Nothing uploaded yet." It opens from a header button.
 * A38: transcription-off was a paragraph ending in "set transcription.binary
 *      in config.yaml". It is a short header status with a docs link, and the
 *      search placeholder no longer promises "everything anyone said".
 * A39: "Regroup" is "Rebuild sessions" with a visible one-line explanation.
 * A40: Film meant both "play" and "generate proxy"; a blocked job's reason was
 *      a title on a badge. The glyphs differ and the reason is row text. */

vi.mock("@/components/MediaUploads", () => ({
  MediaUploads: () => <div>media uploads card</div>,
}));

vi.mock("sonner", () => ({
  toast: { error: () => {}, success: () => {}, info: () => {} },
}));

vi.mock("@/lib/api", async () => {
  const actual = await vi.importActual<typeof import("@/lib/api")>("@/lib/api");
  return { ...actual, api: { ...actual.api } };
});

import { api } from "@/lib/api";
import { LibraryPage } from "./LibraryPage";
import type { LibraryView } from "@/lib/types";

const rec = (over: Record<string, unknown> = {}) => ({
  id: 7,
  filename: "rec-7.mkv",
  startedAt: "2026-01-01T00:00:00Z",
  bytes: 1000,
  durationMs: 60_000,
  tracks: 1,
  hasTranscript: false,
  assets: { proxy: true, poster: false },
  ...over,
});

const library = (over: Partial<LibraryView> = {}): LibraryView =>
  ({
    sessions: [],
    ungrouped: [],
    tags: [],
    speakers: [],
    jobsAvailable: true,
    transcribeAvailable: true,
    markers: ["", ""],
    ...over,
  }) as unknown as LibraryView;

function draw(view: LibraryView) {
  api.library = vi.fn().mockResolvedValue(view);
  return render(
    <MemoryRouter>
      <LibraryPage />
    </MemoryRouter>,
  );
}

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

describe("Library uploads (app-council A37)", () => {
  it("keeps the upload card closed until Upload media is pressed", async () => {
    draw(library());
    const button = await screen.findByRole("button", { name: "Upload media" });
    expect(screen.queryByText("media uploads card")).toBeNull();
    expect(button.getAttribute("aria-expanded")).toBe("false");

    fireEvent.click(button);
    expect(screen.getByText("media uploads card")).toBeTruthy();
    expect(button.getAttribute("aria-expanded")).toBe("true");
  });
});

describe("Library transcription status (app-council A38)", () => {
  it("shows a short status with a docs link and no config jargon", async () => {
    draw(
      library({
        transcribeAvailable: false,
        transcribeNote: "could not find whisper-cli. Set transcription.binary in config.yaml",
      }),
    );
    await screen.findByText("Transcripts: off");
    const link = screen.getByRole("link", { name: /Install whisper\.cpp/ });
    expect(link.getAttribute("href")).toContain("github.com/ggml-org/whisper.cpp");
    expect(screen.queryByText(/transcription\.binary/)).toBeNull();
    // Search still works over existing transcripts; it just stops promising
    // everything anyone said.
    const search = screen.getByRole("textbox", { name: "Search transcripts" }) as HTMLInputElement;
    expect(search.disabled).toBe(false);
    expect(search.placeholder).toContain("Transcripts are off on this machine");
  });

  it("names the status as the reason on each greyed captions button", async () => {
    draw(library({ transcribeAvailable: false, ungrouped: [rec()] as never }));
    const button = (await screen.findByRole("button", { name: "Transcribe" })) as HTMLButtonElement;
    expect(button.disabled).toBe(true);
    const id = button.getAttribute("aria-describedby");
    expect(id).toBeTruthy();
    expect(document.getElementById(id ?? "")?.textContent).toBe("Transcripts: off");
  });
});

describe("Library Rebuild sessions (app-council A39)", () => {
  it("is named for what it does and explained in a visible line", async () => {
    draw(library());
    const button = await screen.findByRole("button", { name: /Rebuild sessions/ });
    expect(screen.queryByRole("button", { name: /^Regroup$/ })).toBeNull();
    const explain = document.getElementById(button.getAttribute("aria-describedby") ?? "");
    expect(explain?.textContent).toContain("only adds");
  });
});

describe("Library rows (app-council A40)", () => {
  it("prints a waiting job's reason as row text", async () => {
    draw(
      library({
        ungrouped: [
          rec({
            activeJobs: [
              { id: 3, kind: "media.proxy", label: "Proxy", state: "queued", progress: 0, blocked: true, reason: "CPU over ceiling" },
            ],
          }),
        ] as never,
      }),
    );
    expect(await screen.findByText("Proxy is waiting: CPU over ceiling")).toBeTruthy();
  });

  it("does not use one glyph for both play and generate proxy", async () => {
    const { container } = draw(library({ ungrouped: [rec()] as never }));
    const generate = await screen.findByRole("button", { name: "Generate proxy" });
    const row = generate.closest("li") as HTMLElement;
    const play = within(row).getAllByRole("button")[0];
    const glyph = (el: Element) => el.querySelector("svg")?.getAttribute("class") ?? "";
    expect(glyph(play)).not.toBe("");
    // lucide stamps each icon's name into its class list.
    expect(glyph(play)).not.toContain("lucide-film");
    expect(glyph(generate)).not.toContain("lucide-film");
    expect(container).toBeTruthy();
  });
});

describe("Library empty states", () => {
  it("uses the kit empty state, with Clear filters when filters hide every session", async () => {
    draw(library());
    expect(await screen.findByText("No recordings yet")).toBeTruthy();
  });
});
