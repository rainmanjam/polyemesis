// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";

/* app-council A83 on Renditions. With no renditions the page drew a centred
 * card of 12px text and a small button. It is the kit's EmptyState now: a short
 * title, the existing explanation as its body, and New rendition as the one
 * action. */

vi.mock("@/hooks/useLiveData", () => ({
  useLiveData: () => ({ status: null, system: null }),
}));

vi.mock("@/lib/api", async () => {
  const actual = await vi.importActual<typeof import("@/lib/api")>("@/lib/api");
  // Every other read the page makes answers nothing, which the page already
  // tolerates; only the list matters here.
  const empty = () => Promise.reject(new Error("not in this test"));
  return {
    ...actual,
    api: new Proxy(
      { ...actual.api, listRenditions: () => Promise.resolve([]) } as Record<string, unknown>,
      { get: (target, key: string) => (key in target && key === "listRenditions" ? target[key] : empty) },
    ),
  };
});

const { RenditionsPage } = await import("./RenditionsPage");
const { translate } = await import("@/lib/i18n");

afterEach(cleanup);

describe("RenditionsPage with no renditions", () => {
  it("shows the kit's EmptyState with New rendition as its action", async () => {
    render(
      <MemoryRouter>
        <RenditionsPage />
      </MemoryRouter>,
    );
    const title = await screen.findByText(translate("en", "rend.emptyTitle"));
    const box = title.closest(".border-dashed") as HTMLElement;
    expect(box).not.toBeNull();
    expect(box.textContent).toContain(translate("en", "rend.empty"));
    expect(box.querySelector("button")?.textContent).toContain(translate("en", "rend.newRendition"));
  });
});
