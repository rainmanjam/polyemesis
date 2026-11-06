import { describe, expect, it } from "vitest";

import { stepForError } from "./destinationSteps";

/* A save refusal sends the dialog to the step that holds the field (A6). */
describe("stepForError", () => {
  it.each([
    ["an RTMP URL is required", 2],
    ["the stream key is empty", 2],
    ["audio bitrate must be between 64 and 320", 3],
    ["rendition 4 does not exist", 4],
    ["a destination must name a programme", 1],
  ])("%s -> step %d", (msg, step) => {
    expect(stepForError(msg)).toBe(step);
  });

  it("stays put on a sentence that names no field", () => {
    expect(stepForError("the server is restarting")).toBeNull();
  });
});
