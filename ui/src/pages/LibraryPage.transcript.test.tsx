// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";

/* #785: deleteTranscript (api.ts:1025) and setTranscriptSpeaker (api.ts:1032)
 * were both defined and never called from anywhere in ui/. A transcript that
 * whisper.cpp got wrong -- the wrong language, a track transcribed against
 * the wrong microphone -- was permanent from this screen: there was no
 * delete, and so no way to ask it to try again either, and no way to fix a
 * speaker name short of accepting it forever.
 *
 * These pin the three controls PlayerDialog now offers per track: rename the
 * speaker, delete the track's transcript with a confirmation, and re-run
 * transcription for the recording. */

vi.mock("@/lib/api", async () => {
  const actual = await vi.importActual<typeof import("@/lib/api")>("@/lib/api");
  return { ...actual, api: { ...actual.api } };
});

import { api } from "@/lib/api";
import { PlayerDialog } from "./LibraryPage";
import type { LibraryRecording, TranscriptView } from "@/lib/types";

const rec = (): LibraryRecording =>
  ({
    id: 1,
    filename: "rec-1.mkv",
    startedAt: "2026-01-01T00:00:00Z",
    finishedAt: "2026-01-01T00:10:00Z",
    bytes: 1000,
    durationMs: 600_000,
    tracks: 1,
    hasTranscript: true,
    assets: { proxy: false, poster: false },
  }) as unknown as LibraryRecording;

const transcriptView = (speaker: string): TranscriptView =>
  ({
    transcript: {
      recordingId: 1,
      recording: "rec-1.mkv",
      tracks: [
        {
          id: 9,
          recordingId: 1,
          track: 0,
          speaker,
          createdAt: "2026-01-01T00:00:00Z",
          count: 1,
          durationMs: 1000,
        },
      ],
    },
    merged: [],
    speakers: [speaker],
    segments: 1,
  }) as unknown as TranscriptView;

function draw(speaker = "Ana", transcribeAvailable = true) {
  api.libraryRecording = vi
    .fn()
    .mockResolvedValue({ recording: rec(), transcriptTracks: null });
  api.transcript = vi.fn().mockResolvedValue(transcriptView(speaker));
  api.setTranscriptSpeaker = vi
    .fn()
    .mockResolvedValue({ track: 0, speaker: "Ben" });
  api.deleteTranscript = vi.fn().mockResolvedValue({ status: "ok" });
  api.submitRecordingJob = vi.fn().mockResolvedValue({ created: true });

  return render(
    <PlayerDialog
      target={{ recordingId: 1 }}
      onClose={() => {}}
      jobsAvailable
      transcribeAvailable={transcribeAvailable}
      onChanged={() => {}}
    />,
  );
}

afterEach(cleanup);

describe("PlayerDialog: naming, deleting and re-running a track's transcript", () => {
  it("loads the track's speaker into an editable, labelled field", async () => {
    draw("Ana");
    const input = (await screen.findByLabelText(
      "Speaker for track 0",
    )) as HTMLInputElement;
    expect(input.value).toBe("Ana");
  });

  it("saves a renamed speaker on blur, not on every keystroke", async () => {
    draw("Ana");
    const input = await screen.findByLabelText("Speaker for track 0");

    fireEvent.change(input, { target: { value: "Ben" } });
    expect(api.setTranscriptSpeaker).not.toHaveBeenCalled();

    fireEvent.blur(input);
    await waitFor(() =>
      expect(api.setTranscriptSpeaker).toHaveBeenCalledWith(1, 0, "Ben"),
    );
  });

  it("does not delete the transcript on the click itself", async () => {
    draw();
    fireEvent.click(await screen.findByLabelText("Delete the transcript for track 0"));
    expect(api.deleteTranscript).not.toHaveBeenCalled();
  });

  it("asks first, naming the track, before deleting it", async () => {
    draw();
    fireEvent.click(await screen.findByLabelText("Delete the transcript for track 0"));

    // The track number is IN THE TITLE (interpolated), the same way
    // PlayoutPage's "Remove the {name} rung?" names its rung -- a
    // non-requireTyping ConfirmDestructive never renders `subject` into the
    // body, so a caller that wants the row it is about to affect on screen
    // has to put it in the title or description.
    await screen.findByRole("dialog", { name: "Delete track 0's transcript?" });

    fireEvent.click(screen.getByRole("button", { name: "Delete transcript" }));
    await waitFor(() => expect(api.deleteTranscript).toHaveBeenCalledWith(1, 0));
  });

  it("leaves the transcript alone when the dialog is cancelled", async () => {
    draw();
    fireEvent.click(await screen.findByLabelText("Delete the transcript for track 0"));
    await screen.findByRole("dialog");
    fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
    expect(api.deleteTranscript).not.toHaveBeenCalled();
  });

  it("re-runs transcription for the whole recording", async () => {
    draw();
    fireEvent.click(await screen.findByRole("button", { name: "Transcribe" }));
    await waitFor(() =>
      expect(api.submitRecordingJob).toHaveBeenCalledWith(1, "transcribe"),
    );
  });

  it("disables the re-run button when transcription is unavailable, same as RecordingList's", async () => {
    draw("Ana", false);
    // jest-dom is not installed (see DebugSettings.test.tsx), so the disabled
    // state is read off the element directly rather than through a matcher.
    const button = (await screen.findByRole("button", {
      name: "Transcribe",
    })) as HTMLButtonElement;
    expect(button.disabled).toBe(true);
  });
});
