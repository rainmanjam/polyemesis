// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";

import { AutomodMatrix } from "./AutomodMatrix";
import { api } from "@/lib/api";
import type {
  AutomodAction,
  AutomodChecker,
  AutomodMatrixView,
  AutomodSettings,
  Settings,
} from "@/lib/types";

/* ARMING A BAN OVER A CHECKER THAT DOES NOT EXIST.
 *
 * `automod.New` takes each checker as a pointer, and a nil one "simply
 * contributes nothing". Nil is the DEFAULT for two of the three: the rule set
 * is nil until a rule is written, and the model is nil unless Model.Enabled.
 *
 * Every cell in those two columns was still a live switch. So on a fresh
 * install an operator could arm "Ban / Model" — permission for a language
 * model to permanently remove a viewer — see the collapsed line count it as an
 * armed automatic action, and be granting that permission to nothing at all.
 * There was no screen anywhere to configure the model, which is what made it
 * unfixable rather than merely wrong; AutomodConfig is the other half.
 *
 * The device is the one this card already applies to a platform that cannot
 * perform an action: the cell is INERT WITH THE REASON. Not disabled — a
 * greyed switch still says "this could be turned on" — and never an unticked
 * box, which says the channel is protected and the protection is off.
 */

const matrix = vi.spyOn(api, "automodMatrix");

const ACTIONS: AutomodAction[] = ["flag", "ban"];
const CHECKERS: AutomodChecker[] = ["rules", "history", "model"];

function view(unavailable: string[] = []): AutomodMatrixView {
  return {
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
        available: !unavailable.includes(`twitch/${action}/${checker}`),
        reason: unavailable.includes(`twitch/${action}/${checker}`)
          ? "Twitch cannot hide a message"
          : undefined,
      })),
    ),
  };
}

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

async function open(a: AutomodSettings, v = view()) {
  matrix.mockResolvedValue(v as never);
  const onChange = vi.fn();
  render(
    <AutomodMatrix settings={{ automod: a } as Settings} onChange={onChange} />,
  );
  // Sixty cells collapse to a line per platform; the table is behind it.
  const row = await screen.findByRole("button", { name: /twitch/ });
  fireEvent.click(row);
  return onChange;
}

describe("AutomodMatrix: a checker that was never configured", () => {
  beforeEach(() => matrix.mockReset());
  afterEach(cleanup);

  it("renders no switch for the rules or model column on a fresh install", async () => {
    await open(automod());

    expect(screen.queryByRole("switch", { name: "Rules may Ban on twitch" })).toBeNull();
    expect(screen.queryByRole("switch", { name: "Model may Ban on twitch" })).toBeNull();
    // The one checker that is always built stays a real choice.
    expect(
      screen.getByRole("switch", { name: "History may Ban on twitch" }),
    ).toBeTruthy();
  });

  it("says which of the two reasons applies, because the fix differs", async () => {
    await open(automod());

    // A model that is off is a switch away; one with no endpoint is a field
    // away. "Not configured" for both sends an operator to the wrong one.
    expect(
      screen.getAllByTitle(/No rule is switched on/).length,
    ).toBeGreaterThan(0);
    expect(screen.getAllByTitle(/model checker is switched off/).length).toBeGreaterThan(0);

    cleanup();
    matrix.mockReset();
    await open(
      automod({ model: { ...automod().model, enabled: true } }),
    );
    expect(screen.getAllByTitle(/has no endpoint/).length).toBeGreaterThan(0);
  });

  it("does not count a permission granted to nothing as an armed action", async () => {
    // The collapsed line is the only thing an operator sees without expanding
    // sixty cells. "1 automatic action" over a nil checker is the same lie the
    // stale server summary told, in a different direction.
    await open(automod({ on: { "twitch/ban/model": true } }));
    expect(screen.getByText("nothing automatic")).toBeTruthy();
  });

  it("gives the column back the moment the checker is configured", async () => {
    await open(automod({ rules: [{ id: 1, name: "spam", enabled: true, pattern: "x", action: "ban" }] }));

    expect(
      screen.getByRole("switch", { name: "Rules may Ban on twitch" }),
    ).toBeTruthy();
  });

  it("counts the armed cell once the checker behind it exists", async () => {
    await open(
      automod({
        rules: [{ id: 1, name: "spam", enabled: true, pattern: "x", action: "ban" }],
        on: { "twitch/ban/rules": true },
      }),
    );
    expect(screen.getByText("1 automatic action")).toBeTruthy();
  });

  it("keeps the platform's own reason when the platform is the one that cannot", async () => {
    // Both gates are shut here. Configuring the model would change nothing,
    // so the reason shown has to be the platform's.
    await open(automod(), view(["twitch/ban/model"]));

    const cannot = screen.getAllByTitle("Twitch cannot hide a message");
    expect(cannot.length).toBe(1);
    expect(cannot[0].textContent).toBe("n/a");
  });
});
