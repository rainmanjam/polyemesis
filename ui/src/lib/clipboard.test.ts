import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

/* THE HELPER EXISTS BECAUSE A COPY BUTTON FAILED SILENTLY.
 *
 * HooksCard's Copy was `void navigator.clipboard?.writeText(x)`: on an http://
 * install clipboard is undefined, the click did nothing, and the operator --
 * told the secret is never shown again -- believed it had copied. So the one
 * property this file must pin is that EVERY outcome says something. A helper
 * that reports success and rejection but not absence has the original bug in
 * the branch that matters most on the installs that have it. */

const toast = vi.hoisted(() => ({ success: vi.fn(), error: vi.fn() }));
vi.mock("sonner", () => ({ toast }));

import { copyToClipboard } from "./clipboard";

// A Translator that echoes the key and its args, so assertions read the key.
const t = ((key: string, args?: Record<string, unknown>) =>
  args ? `${key}:${JSON.stringify(args)}` : key) as never;

const original = Object.getOwnPropertyDescriptor(navigator, "clipboard");
function setClipboard(value: unknown) {
  Object.defineProperty(navigator, "clipboard", { value, configurable: true });
}

beforeEach(() => {
  toast.success.mockClear();
  toast.error.mockClear();
});
afterEach(() => {
  if (original) Object.defineProperty(navigator, "clipboard", original);
  else Reflect.deleteProperty(navigator, "clipboard");
});

describe("copyToClipboard", () => {
  it("reports success, naming what was copied", async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    setClipboard({ writeText });
    copyToClipboard(t, "value", "Secret");
    await Promise.resolve();
    expect(writeText).toHaveBeenCalledWith("value");
    expect(toast.success).toHaveBeenCalledWith('sources.copied:{"what":"Secret"}');
    expect(toast.error).not.toHaveBeenCalled();
  });

  it("reports a rejected write", async () => {
    setClipboard({ writeText: vi.fn().mockRejectedValue(new Error("denied")) });
    copyToClipboard(t, "value", "Secret");
    await Promise.resolve();
    await Promise.resolve();
    expect(toast.error).toHaveBeenCalledWith('sources.copyFailed:{"what":"secret"}');
    expect(toast.success).not.toHaveBeenCalled();
  });

  it("reports an ABSENT clipboard API the same way, instead of doing nothing", () => {
    // The branch that was missing. Insecure contexts have no
    // navigator.clipboard at all, and a no-op here is the original bug.
    setClipboard(undefined);
    copyToClipboard(t, "value", "Secret");
    expect(toast.error).toHaveBeenCalledWith('sources.copyFailed:{"what":"secret"}');
    expect(toast.success).not.toHaveBeenCalled();
  });

  it("lowercases the noun only for the failure message", () => {
    setClipboard(undefined);
    copyToClipboard(t, "v", "Token");
    // "Could not copy the token" reads mid-sentence; "Token copied" does not.
    expect(toast.error.mock.calls[0][0]).toContain('"what":"token"');
  });
});
