// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";

import { AutomodMatrix } from "./AutomodMatrix";
import { checkerReady, CHECKER_KEYS } from "@/lib/automodConfig";
import { api } from "@/lib/api";
import type {
  AutomodAction,
  AutomodChecker,
  AutomodMatrixView,
  AutomodSettings,
  Settings,
} from "@/lib/types";

/* The server now lists a fourth checker, "protection": the recommended-protection
 * pack's own column. It is armed one rung at a time on its own card, not as a
 * grid of switches, so the matrix must draw the columns it always has and the
 * pack's cells must not become switches (or an unlabelled header). */

const matrix = vi.spyOn(api, "automodMatrix");

const ACTIONS: AutomodAction[] = ["flag", "ban"];
const CHECKERS: AutomodChecker[] = ["rules", "history", "protection", "model"];

function view(): AutomodMatrixView {
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
        available: true,
      })),
    ),
  };
}

describe("AutomodMatrix: the protection column", () => {
  beforeEach(() => matrix.mockReset());
  afterEach(cleanup);

  it("is not drawn as a column of switches", async () => {
    matrix.mockResolvedValue(view() as never);
    render(
      <AutomodMatrix
        settings={{ automod: { enabled: true, history: {} } as unknown as AutomodSettings } as Settings}
        onChange={vi.fn()}
      />,
    );
    fireEvent.click(await screen.findByRole("button", { name: /twitch/ }));

    expect(screen.getByRole("switch", { name: "History may Ban on twitch" })).toBeTruthy();
    expect(screen.queryByRole("switch", { name: /Protection may/ })).toBeNull();
    expect(screen.queryByText("Protection")).toBeNull();
  });
});

describe("the protection checker's label and readiness", () => {
  it("has a label and reads as ready only while the pack is on", () => {
    expect(CHECKER_KEYS.protection).toBe("automod.checkerProtection");
    expect(checkerReady(undefined, "protection")).toEqual({
      ready: false,
      reason: "automod.protectionOff",
    });
    const base = {
      enabled: true,
      history: {},
      model: {},
      protection: { pack: "en", packVersion: 1, impersonation: {} },
    } as never as AutomodSettings;
    for (const state of ["", "off"] as const) {
      expect(
        checkerReady({ ...base, protection: { ...base.protection!, state } }, "protection"),
      ).toEqual({ ready: false, reason: "automod.protectionOff" });
    }
    expect(
      checkerReady({ ...base, protection: { ...base.protection!, state: "on" } }, "protection"),
    ).toEqual({ ready: true });
  });
});
