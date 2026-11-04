// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";

/* The storage target row and its Retry now toast read one as one. The
 * catalogue has no plural rules, so a count of exactly 1 has its own key:
 * "1 uploads queued again", and in Spanish "1 verificadas". Polish has no
 * single plural to borrow, so its badges were rewritten as "label: value". */

const toasts: string[] = [];

vi.mock("sonner", () => ({
  toast: {
    error: () => {},
    success: (m: string) => toasts.push(m),
    info: (m: string) => toasts.push(m),
    warning: () => {},
  },
}));

const { StorageTargetsCard } = await import("./StorageTargetsCard");
const { api } = await import("@/lib/api");
const { setLanguage } = await import("@/lib/i18n");
import type { StorageTarget } from "@/lib/types";

const target = (over: Partial<StorageTarget> = {}): StorageTarget => ({
  id: 1,
  name: "archive",
  provider: "r2",
  endpoint: "https://acct.r2.example",
  bucket: "media",
  region: "auto",
  prefixTemplate: "",
  pathStyle: false,
  accessKeyId: "fake-access-key",
  hasSecret: true,
  offloadStems: false,
  offloadTranscripts: false,
  offloadClips: false,
  verifyDownload: false,
  allowPrivateTarget: false,
  enabled: true,
  maxAttempts: 8,
  createdAt: "",
  updatedAt: "",
  counts: {},
  ...over,
});

afterEach(() => {
  cleanup();
  setLanguage("en");
  toasts.length = 0;
  vi.restoreAllMocks();
});

async function draw(counts: StorageTarget["counts"]) {
  vi.spyOn(api.storage, "list").mockResolvedValue({ targets: [target({ counts })], available: true });
  render(<StorageTargetsCard />);
  return screen.findByTestId("storage-target");
}

async function retryWith(retried: number) {
  const row = await draw({ failed: 1 });
  vi.spyOn(api.storage, "retry").mockResolvedValue({ retried });
  fireEvent.click(within(row).getByRole("button", { name: /Retry now/ }));
  await waitFor(() => expect(toasts.length).toBeGreaterThan(0));
}

describe("StorageTargetsCard: Retry now's toast", () => {
  it("says 1 upload queued again", async () => {
    await retryWith(1);
    expect(toasts).toContain("1 upload queued again");
    expect(toasts).not.toContain("1 uploads queued again");
  });

  it("says 2 uploads queued again", async () => {
    await retryWith(2);
    expect(toasts).toContain("2 uploads queued again");
  });
});

describe("StorageTargetsCard: count badges, in Spanish", () => {
  it("says 1 verificada", async () => {
    setLanguage("es");
    const row = await draw({ verified: 1 });
    expect(within(row).getByText("1 verificada")).toBeTruthy();
    expect(within(row).queryByText("1 verificadas")).toBeNull();
  });

  it("says 2 verificadas", async () => {
    setLanguage("es");
    const row = await draw({ verified: 2 });
    expect(within(row).getByText("2 verificadas")).toBeTruthy();
  });

  it("says 1 fallida", async () => {
    setLanguage("es");
    const row = await draw({ failed: 1 });
    expect(within(row).getByText("1 fallida")).toBeTruthy();
    expect(within(row).queryByText("1 fallidas")).toBeNull();
  });

  it("says 2 fallidas", async () => {
    setLanguage("es");
    const row = await draw({ failed: 2 });
    expect(within(row).getByText("2 fallidas")).toBeTruthy();
  });

  it("says 1 atascada", async () => {
    setLanguage("es");
    const row = await draw({ stuck: 1 });
    expect(within(row).getByText("1 atascada")).toBeTruthy();
    expect(within(row).queryByText("1 atascadas")).toBeNull();
  });

  it("says 2 atascadas", async () => {
    setLanguage("es");
    const row = await draw({ stuck: 2 });
    expect(within(row).getByText("2 atascadas")).toBeTruthy();
  });
});

describe("StorageTargetsCard: the waiting badge, in Polish", () => {
  it("says Oczekujące: 1 for one upload waiting", async () => {
    setLanguage("pl");
    const row = await draw({ pending: 1 });
    expect(within(row).getByText("Oczekujące: 1")).toBeTruthy();
    // The pre-rewrite plural, which read "1 waiting ones" in the genitive.
    expect(within(row).queryByText("1 oczekujących")).toBeNull();
  });

  it("says Oczekujące: 2 for one pending and one uploading", async () => {
    setLanguage("pl");
    const row = await draw({ pending: 1, uploading: 1 });
    expect(within(row).getByText("Oczekujące: 2")).toBeTruthy();
  });
});
