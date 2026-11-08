// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";

import { AutomodModelChecker } from "./AutomodModelChecker";
import { setLanguage } from "@/lib/i18n";
import type { AutomodModel, AutomodPreset } from "@/lib/types";

/* A spent hourly ceiling is not an outage, so the lines are not held; the
 * console says moderation is degraded instead. */

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

function withSpend(callsThisHour: number, ceiling: number, ceilingSpent?: boolean) {
  render(
    <AutomodModelChecker slot="primary" value={checker()} onPatch={vi.fn()} presets={presets}
      stats={{ callsThisHour, ceiling, failures: 0, decisions: 0, ceilingSpent } as never}
      statsFailed={false} instruction="be kind" onKeyChanged={() => undefined}
      enableLabel="automod.modelEnabled" enableNote="automod.modelEnabledNote" />,
  );
}

describe("AutomodModelChecker: a spent ceiling", () => {
  it("is degraded moderation only when the server says every checker is spent", () => {
    withSpend(500, 500, true);
    expect(screen.getByText(/Hourly ceiling reached: moderation is degraded/)).toBeTruthy();
  });

  it("is only this checker's note when the other one still answers", () => {
    withSpend(500, 500, false);
    expect(screen.queryByText(/moderation is degraded/)).toBeNull();
    expect(screen.getByText("This checker's hourly ceiling is reached.")).toBeTruthy();
  });

  it("is silent below the ceiling and with no ceiling", () => {
    withSpend(499, 500);
    expect(screen.queryByText(/ceiling is reached|moderation is degraded/)).toBeNull();
    cleanup();
    withSpend(900, 0);
    expect(screen.queryByText(/ceiling is reached|moderation is degraded/)).toBeNull();
  });
});
