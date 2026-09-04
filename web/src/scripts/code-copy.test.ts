// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

/* THE FALLBACK IS THE PART WORTH TESTING, AND IT IS THE PART THAT NEVER RUNS
 * IN DEVELOPMENT.
 *
 * navigator.clipboard is refused on insecure origins and by some privacy
 * settings. A reader on http:// -- which is every reader of a self-hosted copy
 * of these docs before they have TLS -- takes the catch branch on every click.
 * Nobody developing the site sees it, because localhost counts as secure.
 *
 * So what these pin is: the button never claims a copy that did not happen, and
 * the reader still gets the command in one gesture. Plus the delegation itself,
 * which is the reason this module exists at all: /docs/api has forty-odd of
 * these blocks and binding a listener per button is thirty-nine too many.
 *
 * This file was 24 lines of new code with 24 of them uncovered -- all of it --
 * and it sat below the visible edge of the gate's file list until that list was
 * sorted by uncovered lines rather than by filename.
 */

function block(code: string): HTMLElement {
  document.body.innerHTML = `
    <div class="code">
      <button data-code-copy>Copy</button>
      <pre><code>${code}</code></pre>
    </div>`;
  return document.body.querySelector("button")!;
}

let writeText: ReturnType<typeof vi.fn>;

beforeEach(async () => {
  vi.useFakeTimers();
  writeText = vi.fn().mockResolvedValue(undefined);
  Object.defineProperty(navigator, "clipboard", {
    value: { writeText },
    configurable: true,
    writable: true,
  });
  document.body.innerHTML = "";
  vi.resetModules();
  await import("./code-copy");
});

afterEach(() => {
  vi.useRealTimers();
  document.body.innerHTML = "";
});

describe("the docs copy button", () => {
  it("copies the block's text and says so, then puts its own label back", async () => {
    const btn = block("docker compose up -d");
    btn.click();
    await vi.waitFor(() => expect(writeText).toHaveBeenCalledWith("docker compose up -d"));

    expect(btn.textContent).toBe("Copied");
    expect(btn.dataset.copied).toBe("true");

    // The label has to return, or the next reader sees "Copied" over a button
    // they have not pressed.
    vi.advanceTimersByTime(1600);
    expect(btn.textContent).toBe("Copy");
    expect(btn.dataset.copied).toBeUndefined();
  });

  it("selects the text instead of claiming a copy when the clipboard is refused", async () => {
    writeText.mockRejectedValue(new Error("NotAllowedError"));
    const btn = block("systemctl restart polyemesis");
    btn.click();
    await vi.waitFor(() => expect(writeText).toHaveBeenCalled());

    // NOT "Copied". A reader on an insecure origin who is told the command is
    // on their clipboard, and then pastes the previous thing, is worse off than
    // one who was told nothing.
    expect(btn.textContent).toBe("Copy");
    expect(btn.dataset.copied).toBeUndefined();

    // The honest fallback: the command is selected, so one gesture still gets it.
    expect(document.getSelection()?.toString()).toContain("systemctl restart polyemesis");
  });

  it("ignores clicks that are not on a copy button", async () => {
    block("irrelevant");
    document.body.querySelector("code")!.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    await Promise.resolve();
    expect(writeText).not.toHaveBeenCalled();
  });

  it("does nothing when the block has no text to copy", async () => {
    const btn = block("");
    btn.click();
    await Promise.resolve();
    expect(writeText).not.toHaveBeenCalled();
    expect(btn.textContent).toBe("Copy");
  });
});
