// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";

import { AutomodConfig } from "./AutomodConfig";
import { api } from "@/lib/api";
import { setLanguage } from "@/lib/i18n";
import type { AutomodModel, AutomodPreset, AutomodSettings, Settings } from "@/lib/types";

/* How often the fallback answered, and how often a message passed unchecked.
 * Rendered in Spanish, where the participle and the verb agree with the count:
 * with no plural rules, one fallback read "1 respondidos" and one failed-open
 * message read "1 pasaron". */

const presets: AutomodPreset[] = [
  { id: "jev", protocol: "jev", name: "TypeSafe Jev", endpoint: "https://api.typesafe.ai/v1/systemone",
    model: "jev-1.14.0", privacy: "hosted_us", local: false },
  { id: "cerebras", protocol: "openai", name: "Cerebras", endpoint: "https://api.cerebras.ai/v1/chat/completions",
    model: "qwen-3.8-27b", jsonMode: "json_schema", reasoningEffort: "none", privacy: "hosted_us", local: false },
];

const checker = (over: Partial<AutomodModel> = {}): AutomodModel => ({
  enabled: false, protocol: "jev", preset: "jev", endpoint: presets[0].endpoint, model: "jev-1.14.0",
  hasApiKey: true, timeoutSeconds: 4, maxCallsPerHour: 500, action: "flag", minConfidence: 0.8,
  instruction: "", ...over,
});

const automod = (): AutomodSettings => ({
  enabled: true, history: {} as AutomodSettings["history"],
  model: checker({ enabled: true, tested: true }),
  secondary: checker({ protocol: "openai", preset: "cerebras", endpoint: presets[1].endpoint }),
});

const stats = vi.spyOn(api, "automodStats");
const matrix = vi.spyOn(api, "automodMatrix");

function show(fallbacks: number, failedOpen: number) {
  stats.mockResolvedValue({ callsThisHour: 0, ceiling: 500, failures: 0, fallbacks, failedOpen } as never);
  render(<AutomodConfig settings={{ automod: automod() } as Settings} onChange={vi.fn()} />);
}

beforeEach(() => {
  stats.mockReset();
  matrix.mockReset();
  matrix.mockResolvedValue({ presets, modelCategories: ["harassment", "spam"] } as never);
});

afterEach(() => {
  cleanup();
  setLanguage("en");
});

describe("AutomodConfig: automod.fallbacks (es)", () => {
  it("says 1 respondido, not 1 respondidos, for one fallback answer", async () => {
    setLanguage("es");
    show(1, 0);
    expect(await screen.findByText("1 respondido por el de respaldo porque el principal no pudo")).toBeTruthy();
    expect(screen.queryByText("1 respondidos por el de respaldo porque el principal no pudo")).toBeNull();
  });

  it("says 2 respondidos for two fallback answers", async () => {
    setLanguage("es");
    show(2, 0);
    expect(await screen.findByText("2 respondidos por el de respaldo porque el principal no pudo")).toBeTruthy();
  });
});

describe("AutomodConfig: automod.failedOpen (es)", () => {
  it("says 1 pasó, not 1 pasaron, for one message that passed unchecked", async () => {
    setLanguage("es");
    show(0, 1);
    expect(await screen.findByText("1 pasó sin comprobar porque ningún verificador respondió")).toBeTruthy();
    expect(screen.queryByText("1 pasaron sin comprobar porque ningún verificador respondió")).toBeNull();
  });

  it("says 2 pasaron for two messages that passed unchecked", async () => {
    setLanguage("es");
    show(0, 2);
    expect(await screen.findByText("2 pasaron sin comprobar porque ningún verificador respondió")).toBeTruthy();
  });
});
