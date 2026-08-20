/// <reference types="node" />
import { describe, expect, it } from "vitest";
import { readFileSync } from "node:fs";
import { join } from "node:path";

/* The whole explanation for a greyed captions button lived in a `title` on a
 * disabled <button>, and ui/src/components/ui/button.tsx sets
 * `disabled:pointer-events-none` -- so the title never reaches a pointer and is
 * not exposed to a screen reader either. The prose that did exist rendered only
 * inside an EMPTY SEARCH RESULT, which an operator browsing their recordings
 * never sees.
 *
 * A placement defect has no separable decision function to test, so this pins
 * the placement itself: the sentence has to be in the page body, outside both
 * the `title` and the search branch. */

const ROOT = new URL("../../../", import.meta.url).pathname;
const read = (p: string) => readFileSync(join(ROOT, p), "utf8");

describe("LibraryPage", () => {
  it("prints the transcription reason as prose, not only in an unreachable title", () => {
    const src = read("ui/src/pages/LibraryPage.tsx");
    expect(src).toContain('t("lib.transcribeUnavailable")');
    expect(src).toContain("view?.jobsAvailable && !view.transcribeAvailable");
    // Rendered as page text, not as an attribute on the disabled control.
    expect(src).toContain('<p className="mb-2 text-[11px] text-warn">');
  });

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
