// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";

import { AutomodConfig } from "./AutomodConfig";
import { api } from "@/lib/api";
import type { AutomodRule, AutomodSettings, Settings } from "@/lib/types";

/* A RENDERED CONTROL, OR THE VALUE IS UNREACHABLE.
 *
 * The gap being closed here is not a missing type: every field below already
 * existed in lib/types.ts, was already carried by GET/PUT /settings, and was
 * already acted on by the server. Twenty-seven of them simply had no input on
 * any screen, so the only way to set one was to edit the database — and two API
 * client methods, setAutomodKey and automodStats, had no caller at all.
 *
 * So these assertions are deliberately about what an operator can REACH. They
 * query by accessible name, never by class or test id: a field that renders but
 * cannot be named is a field a screen-reader user still cannot reach, and the
 * defect would only be half fixed.
 */

const stats = vi.spyOn(api, "automodStats");
const setKey = vi.spyOn(api, "setAutomodKey");

const zeroStats = () => ({ callsThisHour: 0, ceiling: 500, failures: 0 });

function automod(over: Partial<AutomodSettings> = {}): AutomodSettings {
  return {
    enabled: true,
    history: {},
    model: {
      enabled: false,
      endpoint: "",
      model: "gpt-4o-mini",
      hasApiKey: false,
      timeoutSeconds: 4,
      maxCallsPerHour: 500,
      action: "flag",
      minConfidence: 0.8,
      instruction: "",
    },
    ...over,
  } as AutomodSettings;
}

const rule = (over: Partial<AutomodRule> = {}): AutomodRule => ({
  id: 1,
  name: "spam",
  enabled: true,
  pattern: "buy followers",
  action: "flag",
  ...over,
});

function show(a: AutomodSettings, saveError?: string) {
  const onChange = vi.fn();
  const settings = { automod: a } as Settings;
  render(
    <AutomodConfig settings={settings} onChange={onChange} saveError={saveError} />,
  );
  return onChange;
}

describe("AutomodConfig: the rule list", () => {
  beforeEach(() => {
    stats.mockReset();
    stats.mockResolvedValue(zeroStats() as never);
    setKey.mockReset();
  });
  afterEach(cleanup);

  it("renders an input for every field of a stored rule", async () => {
    show(automod({ rules: [rule({ action: "timeout", timeoutSeconds: 600 })] }));
    await waitFor(() => expect(stats).toHaveBeenCalled());

    expect((screen.getByLabelText("Name") as HTMLInputElement).value).toBe("spam");
    expect((screen.getByLabelText("Pattern") as HTMLInputElement).value).toBe(
      "buy followers",
    );
    expect((screen.getByLabelText("Action") as HTMLSelectElement).value).toBe(
      "timeout",
    );
    expect(
      (screen.getByLabelText("Timeout (seconds)") as HTMLInputElement).value,
    ).toBe("600");
    expect(screen.getByRole("switch", { name: "Enabled" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "Delete rule spam" })).toBeTruthy();
  });

  it("offers the duration only for the action that has one", async () => {
    // A field with no effect is read as a field with one: a timeout beside
    // "Delete" says the deletion is temporary.
    show(automod({ rules: [rule()] }));
    await waitFor(() => expect(stats).toHaveBeenCalled());
    expect(screen.queryByLabelText("Timeout (seconds)")).toBeNull();
  });

  it("writes a typed pattern back to the draft", async () => {
    const onChange = show(automod({ rules: [rule()] }));
    await waitFor(() => expect(stats).toHaveBeenCalled());

    fireEvent.change(screen.getByLabelText("Pattern"), {
      target: { value: "free (?:v-?bucks|robux)" },
    });
    const next = onChange.mock.calls[0][0] as Settings;
    expect(next.automod?.rules?.[0].pattern).toBe("free (?:v-?bucks|robux)");
  });

  it("adds a rule that cannot act until its action is chosen", async () => {
    const onChange = show(automod({ rules: [rule({ id: 4 })] }));
    await waitFor(() => expect(stats).toHaveBeenCalled());

    fireEvent.click(screen.getByRole("button", { name: "Add rule" }));
    const added = (onChange.mock.calls[0][0] as Settings).automod?.rules ?? [];
    expect(added).toHaveLength(2);
    // Flagging changes nothing an audience sees, so a half-typed rule cannot
    // delete anything; and the id is past the highest, never a reused one.
    expect(added[1].action).toBe("flag");
    expect(added[1].id).toBe(5);
  });

  it("asks before removing a rule, and removes it on confirmation", async () => {
    const onChange = show(automod({ rules: [rule(), rule({ id: 2, name: "caps" })] }));
    await waitFor(() => expect(stats).toHaveBeenCalled());

    fireEvent.click(screen.getByRole("button", { name: "Delete rule caps" }));
    expect(screen.getByText("Delete this rule?")).toBeTruthy();
    expect(onChange).not.toHaveBeenCalled();

    fireEvent.click(screen.getByRole("button", { name: "Delete" }));
    await waitFor(() => expect(onChange).toHaveBeenCalled());
    const left = (onChange.mock.calls[0][0] as Settings).automod?.rules ?? [];
    expect(left.map((r) => r.name)).toEqual(["spam"]);
  });
});

describe("AutomodConfig: a pattern the server will not take", () => {
  beforeEach(() => {
    stats.mockReset();
    stats.mockResolvedValue(zeroStats() as never);
  });
  afterEach(cleanup);

  it("says so against the field, before the save", async () => {
    show(automod({ rules: [rule({ pattern: "(unclosed[" })] }));
    await waitFor(() => expect(stats).toHaveBeenCalled());

    // The rule's own message, not the card-level sentence about what one
    // uncompilable pattern does to the rest of them -- both render, and only
    // the first is attached to a field.
    expect(screen.getByText(/^This pattern does not compile/)).toBeTruthy();
    expect(screen.getByLabelText("Pattern").getAttribute("aria-invalid")).toBe(
      "true",
    );
  });

  it("puts the server's refusal on the rule it names, and on no other", async () => {
    // NewRuleSet is all-or-nothing, so the 400 is about the whole document.
    // A toast four seconds long, naming no field, leaves the operator to
    // re-read six patterns.
    show(
      automod({ rules: [rule(), rule({ id: 2, name: "lookahead", pattern: "(?=x)" })] }),
      'rule "lookahead": error parsing regexp: invalid or unsupported Perl syntax',
    );
    await waitFor(() => expect(stats).toHaveBeenCalled());

    const refused = screen.getByRole("group", { name: "lookahead" });
    expect(refused.textContent).toContain("The server refused this pattern");
    expect(
      screen.getByRole("group", { name: "spam" }).textContent,
    ).not.toContain("The server refused this pattern");
  });

  it("pins nothing when the rejection is about something else entirely", async () => {
    show(automod({ rules: [rule()] }), "listeners: rtmpPort is already in use");
    await waitFor(() => expect(stats).toHaveBeenCalled());
    expect(screen.queryByText(/The server refused this pattern/)).toBeNull();
  });
});

describe("AutomodConfig: the model checker", () => {
  beforeEach(() => {
    stats.mockReset();
    stats.mockResolvedValue(zeroStats() as never);
    setKey.mockReset();
  });
  afterEach(cleanup);

  it("renders a control for every stored model field", async () => {
    show(
      automod({
        model: {
          enabled: true,
          endpoint: "https://api.example/v1/chat/completions",
          model: "gpt-4o-mini",
          hasApiKey: false,
          timeoutSeconds: 4,
          maxCallsPerHour: 500,
          action: "timeout",
          timeoutForBan: 300,
          minConfidence: 0.8,
          instruction: "Flag targeted abuse.",
        },
      }),
    );
    await waitFor(() => expect(stats).toHaveBeenCalled());

    expect(screen.getByRole("switch", { name: "Ask the model" })).toBeTruthy();
    expect((screen.getByLabelText("Endpoint") as HTMLInputElement).value).toBe(
      "https://api.example/v1/chat/completions",
    );
    expect((screen.getByLabelText("Model") as HTMLInputElement).value).toBe(
      "gpt-4o-mini",
    );
    // A PROMPT, so a textarea. In a single-line input all but the first few
    // words of the thing every verdict is decided by are off screen.
    const instruction = screen.getByLabelText("Instruction");
    expect(instruction.tagName).toBe("TEXTAREA");
    expect((instruction as HTMLTextAreaElement).value).toBe("Flag targeted abuse.");
    expect(
      (screen.getByLabelText("Confidence floor") as HTMLInputElement).value,
    ).toBe("0.8");
    expect(
      (screen.getByLabelText("Calls per hour") as HTMLInputElement).value,
    ).toBe("500");
    expect(
      (screen.getByLabelText("Answer timeout (seconds)") as HTMLInputElement)
        .value,
    ).toBe("4");
    expect(
      (screen.getByLabelText("Action on a positive verdict") as HTMLSelectElement)
        .value,
    ).toBe("timeout");
    // The two timeouts are not the same timeout, and only one of them is how
    // long a viewer stays silenced.
    expect(
      (screen.getByLabelText("Silence the viewer for (seconds)") as HTMLInputElement)
        .value,
    ).toBe("300");
  });

  it("names a confidence floor the server would silently discard", async () => {
    // The server does not refuse this: it logs and keeps 0.8. So a floor of 0
    // -- act on every opinion the model has -- is silent everywhere else.
    show(
      automod({
        model: { ...automod().model, enabled: true, minConfidence: 80 },
      }),
    );
    await waitFor(() => expect(stats).toHaveBeenCalled());
    expect(screen.getByText(/Outside 0.01 to 1/)).toBeTruthy();
    expect(
      screen.getByLabelText("Confidence floor").getAttribute("aria-invalid"),
    ).toBe("true");
  });

  it("shows the hour's spend beside the ceiling that bounds it", async () => {
    stats.mockResolvedValue({
      callsThisHour: 412,
      ceiling: 500,
      failures: 3,
      lastError: "hourly ceiling reached",
    } as never);
    show(automod());

    expect(await screen.findByText("412 of 500 calls this hour")).toBeTruthy();
    expect(screen.getByText("3 calls failed")).toBeTruthy();
    expect(screen.getByText(/hourly ceiling reached/)).toBeTruthy();
  });

  it("does not print a reassuring zero when the spend could not be read", async () => {
    // "0 of 500 calls this hour" is the reading an operator checks when they
    // suspect the model is spending; a fabricated one stops them looking.
    stats.mockRejectedValue(new Error("unreachable"));
    show(automod());

    expect(await screen.findByText(/Spend could not be read/)).toBeTruthy();
    expect(screen.queryByText(/calls this hour/)).toBeNull();
  });
});

describe("AutomodConfig: the API key", () => {
  beforeEach(() => {
    stats.mockReset();
    stats.mockResolvedValue(zeroStats() as never);
    setKey.mockReset();
  });
  afterEach(cleanup);

  it("stores a typed key through the endpoint that had no caller", async () => {
    setKey.mockResolvedValue({ hasApiKey: true } as never);
    const onChange = show(automod());
    await waitFor(() => expect(stats).toHaveBeenCalled());

    fireEvent.change(screen.getByLabelText("New key"), {
      target: { value: "sk-live-1234" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Save key" }));

    await waitFor(() => expect(setKey).toHaveBeenCalledWith("sk-live-1234"));
    const next = onChange.mock.calls[0][0] as Settings;
    expect(next.automod?.model.hasApiKey).toBe(true);
    // And the field is emptied, so the key is not left on screen afterwards.
    await waitFor(() =>
      expect(screen.queryByDisplayValue("sk-live-1234")).toBeNull(),
    );
  });

  it("reports a stored key without ever rendering one", async () => {
    // The key is sealed server-side and returned by nothing. hasApiKey is all
    // there is, so "configured" plus a way to replace it is all that can
    // honestly be drawn.
    show(automod({ model: { ...automod().model, hasApiKey: true } }));
    await waitFor(() => expect(stats).toHaveBeenCalled());

    expect(screen.getByText("A key is stored.")).toBeTruthy();
    expect(screen.queryByLabelText("New key")).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Replace key" }));
    expect(
      (screen.getByLabelText("New key") as HTMLInputElement).value,
    ).toBe("");
  });

  it("clears the key with the empty string the endpoint treats as a clear", async () => {
    setKey.mockResolvedValue({ hasApiKey: false } as never);
    const onChange = show(automod({ model: { ...automod().model, hasApiKey: true } }));
    await waitFor(() => expect(stats).toHaveBeenCalled());

    fireEvent.click(screen.getByRole("button", { name: "Clear key" }));
    await waitFor(() => expect(setKey).toHaveBeenCalledWith(""));
    expect((onChange.mock.calls[0][0] as Settings).automod?.model.hasApiKey).toBe(
      false,
    );
  });

  it("says so when the key could not be stored", async () => {
    // It is the one control here that acts before the page's Save button, so
    // it has to report its own outcome rather than leave it assumed.
    setKey.mockRejectedValue(new Error("sealed store is read-only"));
    show(automod());
    await waitFor(() => expect(stats).toHaveBeenCalled());

    fireEvent.change(screen.getByLabelText("New key"), {
      target: { value: "sk-live-1234" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Save key" }));
    expect(await screen.findByText(/could not be stored/)).toBeTruthy();
  });
});
