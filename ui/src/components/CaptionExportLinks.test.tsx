// @vitest-environment jsdom

import { afterEach, describe, expect, it } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";

import { CaptionExportLinks } from "./CaptionExportLinks";
import en from "@/lib/i18n/en.json";

// The #8 caption export buttons. A recording's transcript downloads as SRT, VTT
// or JSON, and so do the captions viewers saw live; each link names the route's
// format and source exactly, because the server refuses anything else.

afterEach(() => cleanup());

const hrefs = (testId: string) =>
  Array.from(screen.getByTestId(testId).querySelectorAll("a")).map((a) => a.getAttribute("href"));

describe("CaptionExportLinks", () => {
  // MUTATION: the live row's links built without source=live. Observed: FAIL,
  // the live links downloaded the stored transcript.
  it("offers the stored transcript and the live captions in all three formats", () => {
    render(<CaptionExportLinks recordingId={7} hasTranscript fromProgramme />);
    const base = "/api/v1/library/recordings/7/transcript/export?format=";
    expect(hrefs("caption-export-recording")).toEqual([`${base}srt`, `${base}vtt`, `${base}json`]);
    expect(hrefs("caption-export-live")).toEqual([
      `${base}srt&source=live`,
      `${base}vtt&source=live`,
      `${base}json&source=live`,
    ]);
    expect(screen.getByText(en["wave3.captions.exportHint"])).toBeTruthy();
  });

  it("offers only the live captions when there is no transcript yet", () => {
    render(<CaptionExportLinks recordingId={7} hasTranscript={false} fromProgramme />);
    expect(screen.queryByTestId("caption-export-recording")).toBeNull();
    expect(screen.getByTestId("caption-export-live")).toBeTruthy();
  });

  it("draws nothing for a recording with neither a transcript nor a programme", () => {
    render(<CaptionExportLinks recordingId={7} hasTranscript={false} fromProgramme={false} />);
    expect(screen.queryByTestId("caption-export")).toBeNull();
  });
});
