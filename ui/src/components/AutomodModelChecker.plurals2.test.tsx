// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";

import { AutomodModelChecker } from "./AutomodModelChecker";
import { setLanguage } from "@/lib/i18n";
import type { AutomodModel, AutomodPreset } from "@/lib/types";

/* The spend line beside the hourly ceiling. The catalogue has no plural rules,
 * so a count of exactly 1 picks its own key; without it French and Italian read
 * "1 appels" / "1 chiamate" and English reads "1 calls". */

afterEach(() => {
  cleanup();
  setLanguage("en");
});

const presets: AutomodPreset[] = [
  { id: "jev", protocol: "jev", name: "TypeSafe Jev", endpoint: "https://api.typesafe.ai/v1/systemone",
    model: "jev-1.14.0", privacy: "hosted_us", local: false },
];

const checker = (): AutomodModel => ({
  enabled: true, tested: true, protocol: "jev", preset: "jev", endpoint: presets[0].endpoint, model: "jev-1.14.0",
  hasApiKey: true, timeoutSeconds: 4, maxCallsPerHour: 500, action: "flag", minConfidence: 0.8,
  instruction: "",
});

/** No failures and no decisions, so the spend line is the only reading drawn. */
function withSpend(callsThisHour: number, ceiling: number) {
  render(
    <AutomodModelChecker slot="primary" value={checker()} onPatch={vi.fn()} presets={presets}
      stats={{ callsThisHour, ceiling, failures: 0, decisions: 0 } as never}
      statsFailed={false} instruction="be kind" onKeyChanged={() => undefined}
      enableLabel="automod.modelEnabled" enableNote="automod.modelEnabledNote" />,
  );
}

describe("AutomodModelChecker: automod.spend, against a ceiling", () => {
  it("says 1 appel, not 1 appels, for one call this hour (fr)", () => {
    setLanguage("fr");
    withSpend(1, 500);
    expect(screen.getByText("1 appel sur 500 cette heure")).toBeTruthy();
    expect(screen.queryByText("1 appels sur 500 cette heure")).toBeNull();
  });

  it("says 2 appels for two calls this hour (fr)", () => {
    setLanguage("fr");
    withSpend(2, 500);
    expect(screen.getByText("2 appels sur 500 cette heure")).toBeTruthy();
  });

  it("says 1 chiamata, not 1 chiamate, for one call this hour (it)", () => {
    setLanguage("it");
    withSpend(1, 500);
    expect(screen.getByText("1 chiamata su 500 in quest'ora")).toBeTruthy();
    expect(screen.queryByText("1 chiamate su 500 in quest'ora")).toBeNull();
  });

  it("says 2 chiamate for two calls this hour (it)", () => {
    setLanguage("it");
    withSpend(2, 500);
    expect(screen.getByText("2 chiamate su 500 in quest'ora")).toBeTruthy();
  });
});

describe("AutomodModelChecker: automod.spendNoCeiling, with the ceiling at zero", () => {
  it("says 1 call, not 1 calls, this hour against no ceiling (en)", () => {
    withSpend(1, 0);
    expect(screen.getByText("1 call this hour, against no ceiling")).toBeTruthy();
    expect(screen.queryByText("1 calls this hour, against no ceiling")).toBeNull();
  });

  it("says 2 calls this hour against no ceiling (en)", () => {
    withSpend(2, 0);
    expect(screen.getByText("2 calls this hour, against no ceiling")).toBeTruthy();
  });
});
