// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";

import { AutomodConfig } from "./AutomodConfig";
import { AutomodModelChecker } from "./AutomodModelChecker";
import { api } from "@/lib/api";
import type { AutomodModel, AutomodPreset, AutomodSettings, Settings } from "@/lib/types";

/* The two model checkers, as an operator meets them: a Test that has to pass
 * before the switch moves, a privacy line that says where messages go, a
 * flag-only lock and a CPU warning on the local presets, and one category table
 * the two checkers share. */

const presets: AutomodPreset[] = [
  { id: "jev", protocol: "jev", name: "TypeSafe Jev", endpoint: "https://api.typesafe.ai/v1/systemone",
    model: "jev-1.14.0", privacy: "hosted_us", local: false },
  { id: "jev-ollama", protocol: "jev", name: "Ollama (laya)", endpoint: "http://localhost:11434/v1/systemone",
    model: "laya", compact: true, privacy: "local", local: true },
  { id: "cerebras", protocol: "openai", name: "Cerebras", endpoint: "https://api.cerebras.ai/v1/chat/completions",
    model: "qwen-3.8-27b", jsonMode: "json_schema", reasoningEffort: "none", privacy: "hosted_us", local: false },
  { id: "custom", protocol: "openai", name: "Custom", endpoint: "", model: "", privacy: "unknown", local: false },
];

const checker = (over: Partial<AutomodModel> = {}): AutomodModel => ({
  enabled: false, protocol: "jev", preset: "jev", endpoint: presets[0].endpoint, model: "jev-1.14.0",
  hasApiKey: true, timeoutSeconds: 4, maxCallsPerHour: 500, action: "flag", minConfidence: 0.8,
  instruction: "", ...over,
});

const testRun = vi.spyOn(api, "testAutomodChecker");
const setKey = vi.spyOn(api, "setAutomodKey");
const stats = vi.spyOn(api, "automodStats");
const matrix = vi.spyOn(api, "automodMatrix");

function mount(value: AutomodModel, slot: "primary" | "secondary" = "primary") {
  const onPatch = vi.fn();
  render(
    <AutomodModelChecker slot={slot} value={value} onPatch={onPatch} presets={presets} stats={null}
      statsFailed={false} instruction="be kind" onKeyChanged={() => undefined}
      enableLabel="automod.modelEnabled" enableNote="automod.modelEnabledNote" />,
  );
  return onPatch;
}

const passed = { passed: true, tested: true, checker: "primary" as const, cases: [
  { case: "harmless" as const, ok: true, flagged: false, latencyMs: 52 },
  { case: "abusive" as const, ok: true, flagged: true, category: "harassment", latencyMs: 55 },
  { case: "spam" as const, ok: true, flagged: false, latencyMs: 59 },
] };

describe("AutomodModelChecker", () => {
  beforeEach(() => {
    testRun.mockReset();
    setKey.mockReset();
  });
  afterEach(cleanup);

  it("keeps the switch locked until a Test passes for what is on screen", async () => {
    testRun.mockResolvedValue(passed as never);
    mount(checker());
    const sw = screen.getByRole("switch", { name: "Ask the model" });
    expect((sw as HTMLButtonElement).disabled).toBe(true);
    expect(screen.getByText(/Run Test before switching this on/)).toBeTruthy();

    fireEvent.click(screen.getByRole("button", { name: "Test" }));
    await waitFor(() => expect(testRun).toHaveBeenCalled());
    // The connection on screen, the instruction it will be asked with, and
    // NO key: none was typed, so the server uses the sealed one -- which it
    // sends only to the saved address.
    expect(testRun.mock.calls[0][0]).toEqual({
      checker: "primary", protocol: "jev", preset: "jev", endpoint: presets[0].endpoint,
      model: "jev-1.14.0", timeoutSeconds: 4, minConfidence: 0.8, instruction: "be kind",
    });
    expect(await screen.findByText(/Passed\. This checker can now be switched on/)).toBeTruthy();
    expect(screen.getByText("Abusive message: flagged as Harassment (55 ms)")).toBeTruthy();
    expect((screen.getByRole("switch", { name: "Ask the model" }) as HTMLButtonElement).disabled).toBe(false);
  });

  it("names the failure and leaves the switch locked when the Test fails", async () => {
    testRun.mockResolvedValue({ passed: false, tested: false, problem: "harmless_flagged", checker: "primary",
      cases: [{ case: "harmless", ok: true, flagged: true, category: "spam", latencyMs: 9 }] } as never);
    mount(checker());
    fireEvent.click(screen.getByRole("button", { name: "Test" }));
    expect(await screen.findByText("Failed: it flagged an ordinary message.")).toBeTruthy();
    expect((screen.getByRole("switch", { name: "Ask the model" }) as HTMLButtonElement).disabled).toBe(true);
  });

  it("sends a typed key with the Test, for an address the stored key was not given for", async () => {
    testRun.mockResolvedValue(passed as never);
    const onPatch = mount(checker({ hasApiKey: false, endpoint: "https://elsewhere.example/v1/systemone" }));
    fireEvent.change(screen.getByLabelText("New key"), { target: { value: "typed-for-elsewhere" } });
    fireEvent.click(screen.getByRole("button", { name: "Test" }));
    await waitFor(() => expect(testRun).toHaveBeenCalled());
    expect(testRun.mock.calls[0][0].key).toBe("typed-for-elsewhere");
    // It passed, so the server sealed it: the card says a key is stored and
    // the field is emptied.
    await waitFor(() => expect(onPatch).toHaveBeenCalledWith({ hasApiKey: true }));
  });

  it("says a passing key for an unsaved address is held until that address is saved", async () => {
    testRun.mockResolvedValue({ ...passed, keyPending: true } as never);
    const onPatch = mount(checker({ hasApiKey: false, endpoint: "https://elsewhere.example/v1/systemone" }));
    fireEvent.change(screen.getByLabelText("New key"), { target: { value: "typed" } });
    fireEvent.click(screen.getByRole("button", { name: "Test" }));
    expect(await screen.findByText(/It will become this checker's key when you save this address/)).toBeTruthy();
    expect(onPatch).not.toHaveBeenCalledWith({ hasApiKey: true });
  });

  it("warns that a stored key is not carried to a changed address", () => {
    const value = checker({ hasApiKey: true });
    const onPatch = vi.fn();
    const { rerender } = render(
      <AutomodModelChecker slot="primary" value={value} onPatch={onPatch} presets={presets} stats={null}
        statsFailed={false} instruction="" onKeyChanged={() => undefined}
        enableLabel="automod.modelEnabled" enableNote="automod.modelEnabledNote" />,
    );
    expect(screen.queryByText(/sent only to the address it was given for/)).toBeNull();
    rerender(
      <AutomodModelChecker slot="primary" value={{ ...value, endpoint: "https://moved.example/v1" }} onPatch={onPatch}
        presets={presets} stats={null} statsFailed={false} instruction="" onKeyChanged={() => undefined}
        enableLabel="automod.modelEnabled" enableNote="automod.modelEnabledNote" />,
    );
    expect(screen.getByText(/sent only to the address it was given for/)).toBeTruthy();
  });

  it("stores the fallback's key under its own name", async () => {
    setKey.mockResolvedValue({ hasApiKey: true } as never);
    mount(checker({ hasApiKey: false }), "secondary");
    fireEvent.change(screen.getByLabelText("New key"), { target: { value: "sec-key" } });
    fireEvent.click(screen.getByRole("button", { name: "Save key" }));
    await waitFor(() => expect(setKey).toHaveBeenCalledWith("sec-key", "secondary"));
  });

  it("says where messages go, and that a local model only flags and may be too slow on a CPU", () => {
    mount(checker({ preset: "jev-ollama", endpoint: presets[1].endpoint, model: "laya" }));
    expect(screen.getByText(/Private: messages stay on this machine/)).toBeTruthy();
    expect(screen.getByText(/Local models are flag-only/)).toBeTruthy();
    expect(screen.getByText(/num_thread/)).toBeTruthy();
    cleanup();
    mount(checker());
    expect(screen.getByText(/sent to TypeSafe Jev, in the US/)).toBeTruthy();
    expect(screen.queryByText(/Local models are flag-only/)).toBeNull();
  });

  it("fills the provider's address and model when a preset is chosen", () => {
    const onPatch = mount(checker({ protocol: "openai", preset: "custom", endpoint: "x", model: "y" }));
    fireEvent.change(screen.getByLabelText("Provider"), { target: { value: "cerebras" } });
    expect(onPatch).toHaveBeenCalledWith({ protocol: "openai", preset: "cerebras",
      endpoint: presets[2].endpoint, model: "qwen-3.8-27b" });
  });
});

describe("AutomodConfig: the fallback checker and the category table", () => {
  beforeEach(() => {
    stats.mockReset();
    stats.mockResolvedValue({ callsThisHour: 0, ceiling: 500, failures: 0, fallbacks: 3,
      lastDecidedBy: "secondary" } as never);
    matrix.mockReset();
    matrix.mockResolvedValue({ presets, modelCategories: ["harassment", "spam"] } as never);
  });
  afterEach(cleanup);

  const automod = (): AutomodSettings => ({
    enabled: true, history: {} as AutomodSettings["history"],
    model: checker({ enabled: true, tested: true }),
    secondary: checker({ protocol: "openai", preset: "cerebras", endpoint: presets[2].endpoint }),
  });

  function show(a: AutomodSettings) {
    const onChange = vi.fn();
    render(<AutomodConfig settings={{ automod: a } as Settings} onChange={onChange} />);
    return onChange;
  }

  it("offers the fallback on request and says which checker decided", async () => {
    show(automod());
    expect(await screen.findByText(/3 answered by the fallback/)).toBeTruthy();
    expect(screen.getByText("Last decision: fallback checker")).toBeTruthy();
    expect(screen.queryByRole("switch", { name: "Ask the fallback checker" })).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Set up a fallback checker" }));
    const group = screen.getByRole("group", { name: "Fallback checker" });
    expect(within(group).getByRole("switch", { name: "Ask the fallback checker" })).toBeTruthy();
  });

  it("starts every category on the default and writes the operator's choice", async () => {
    const onChange = show(automod());
    await waitFor(() => expect(matrix).toHaveBeenCalled());
    const spam = (await screen.findByLabelText("Spam")) as HTMLSelectElement;
    expect(spam.value).toBe("");
    expect(screen.getByText("Suggested: Delete")).toBeTruthy();
    fireEvent.change(spam, { target: { value: "delete" } });
    expect((onChange.mock.calls.at(-1)![0] as Settings).automod!.model.categoryActions).toEqual({ spam: "delete" });

    fireEvent.click(screen.getByRole("button", { name: "Use the suggested actions" }));
    expect((onChange.mock.calls.at(-1)![0] as Settings).automod!.model.categoryActions).toEqual({
      spam: "delete", harassment: "timeout", hate_speech: "timeout", threat: "timeout",
    });
  });
});
