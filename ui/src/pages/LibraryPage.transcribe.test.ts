/// <reference types="node" />
import { describe, expect, it } from "vitest";
import { readFileSync } from "node:fs";
import { join } from "node:path";

/* The whole explanation for a greyed captions button lived in a `title` on a
 * disabled <button>, and ui/src/components/ui/button.tsx sets
 * `disabled:pointer-events-none` -- so the title never reaches a pointer and is
 * not exposed to a screen reader either.
 *
 * app-council A38 moved the explanation from a paragraph (which ended in a
 * config.yaml key) to the header's "Transcripts: off" status, and each greyed
 * button names that status through aria-describedby. The rendered behaviour is
 * pinned in LibraryPage.content.test.tsx; these keep the two source-level
 * facts that a render cannot show. */

const ROOT = new URL("../../../", import.meta.url).pathname;
const read = (p: string) => readFileSync(join(ROOT, p), "utf8");

describe("LibraryPage", () => {
  it("still keeps the button disabled, because the fix is a warning and not a control", () => {
    // Nothing here makes transcription possible; the only honest change is to
    // say why, where it can be read.
    expect(read("ui/src/pages/LibraryPage.tsx")).toContain(
      "disabled={working || !transcribeAvailable}",
    );
  });

  it("is why the title alone was not enough", () => {
    // The property this whole finding rests on. If button.tsx ever stops
    // suppressing pointer events on a disabled button, revisit the placement.
    expect(read("ui/src/components/ui/button.tsx")).toContain("disabled:pointer-events-none");
  });
});
