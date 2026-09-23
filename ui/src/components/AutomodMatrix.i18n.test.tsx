// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";

import { AutomodMatrix } from "./AutomodMatrix";
import { api } from "@/lib/api";
import { setLanguage } from "@/lib/i18n";
import type { AutomodAction, AutomodChecker, AutomodMatrixView, AutomodSettings, Settings } from "@/lib/types";

/* The automatic-moderation card rendered in English in every locale: its title,
 * description, kill-switch note, irreversible-action warning, the collapsed
 * summary line and every label in the expanded table were literals. Only the
 * action and checker names went through the catalogue, so a Japanese operator
 * saw 「禁止」 in a table otherwise entirely in English. */

const ACTIONS: AutomodAction[] = ["flag", "delete", "ban"];
const CHECKERS: AutomodChecker[] = ["rules", "history", "model"];

const view: AutomodMatrixView = {
  enabled: true,
  platforms: ["twitch"],
  actions: ACTIONS,
  checkers: CHECKERS,
  summary: { twitch: 0 },
  cells: ACTIONS.flatMap((action) =>
    CHECKERS.map((checker) => ({
      platform: "twitch",
      action,
      checker,
      auto: false,
      available: !(action === "delete" && checker === "model"),
    })),
  ),
};

const automod = {
  enabled: true,
  history: {},
  on: { "twitch/ban/history": true },
  model: { enabled: false },
} as unknown as AutomodSettings;

afterEach(() => {
  cleanup();
  act(() => setLanguage("en"));
  vi.restoreAllMocks();
});

// Words that only the English card contains. Each is a whole visible label or
// the start of a sentence, so a hit is an untranslated string, not a
// coincidence.
const ENGLISH = [
  "Automatic moderation",
  "What each checker may do",
  "Off stops every automatic action",
  "An irreversible action is armed",
  "automatic action",
  "Active",
  "clear row",
  "no undo",
  "recorded for review",
  "always on",
  "n/a",
];

describe("AutomodMatrix in Japanese", () => {
  it("renders none of its English", async () => {
    vi.spyOn(api, "automodMatrix").mockResolvedValue(view as never);
    act(() => setLanguage("ja"));
    render(<AutomodMatrix settings={{ automod } as Settings} onChange={() => {}} />);
    fireEvent.click(await screen.findByRole("button", { name: /twitch/ }));

    const text = document.body.textContent ?? "";
    const titles = [...document.querySelectorAll("[title]")].map((e) => e.getAttribute("title")).join("\n");
    const labels = [...document.querySelectorAll("[aria-label]")].map((e) => e.getAttribute("aria-label")).join("\n");
    const leaked = ENGLISH.filter((s) => text.includes(s) || titles.includes(s) || labels.includes(s));
    expect(leaked).toEqual([]);
    expect(labels).not.toMatch(/ may .* on /);
  });
});
