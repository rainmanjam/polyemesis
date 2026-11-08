// @vitest-environment jsdom

import { useState } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";

import { AutomodProtectionCard } from "./AutomodProtection";
import { ProtectionOfferStandalone } from "./ProtectionOfferStandalone";
import { api, ApiError } from "@/lib/api";
import type {
  AutomodCell,
  AutomodExplanation,
  AutomodMatrixView,
  AutomodProtection,
  Settings,
} from "@/lib/types";

/* THE CARD IS A WAY INTO THE DRAFT, NOT A SECOND SOURCE OF TRUTH.
 *
 * Every assertion is about what the operator can reach and what lands in the
 * settings document the page will save: a switch writes a DIFF over the pack,
 * a rung writes the matrix cell `platform/rung/protection`, and nothing is
 * armed until a click. */

const matrix = vi.spyOn(api, "automodMatrix");
const explain = vi.spyOn(api, "automodExplain");
const getSettings = vi.spyOn(api, "getSettings");
const putSettings = vi.spyOn(api, "putSettings");

const RUNGS = ["delete", "timeout", "ban"] as const;

function view(unavailable: string[] = []): AutomodMatrixView {
  const cells: AutomodCell[] = ["twitch", "facebook"].flatMap((platform) =>
    RUNGS.map((action) => {
      const key = `${platform}/${action}`;
      return {
        platform,
        action,
        checker: "protection" as const,
        auto: false,
        available: !unavailable.includes(key),
        ...(unavailable.includes(key) ? { reason: "facebook cannot do that" } : {}),
      };
    }),
  );
  return {
    enabled: true,
    platforms: ["twitch", "facebook"],
    actions: ["flag", ...RUNGS],
    checkers: ["rules", "history", "protection", "model"],
    summary: {},
    cells,
  };
}

function prot(over: Partial<AutomodProtection> = {}): AutomodProtection {
  return {
    state: "on",
    pack: "en",
    packVersion: 1,
    impersonation: {},
    strikeWindowSeconds: 600,
    ladderTimeoutSeconds: 600,
    ...over,
  };
}

function settingsOf(p: AutomodProtection, on: Record<string, boolean> = {}): Settings {
  return { automod: { enabled: true, history: {}, model: {}, on, protection: p } } as unknown as Settings;
}

/** The card over a real draft, so a click is followed by the re-render a page
 *  would do. `latest` is the last settings document handed to onChange. */
function show(initial: Settings) {
  const latest = { current: initial };
  function Harness() {
    const [s, setS] = useState(initial);
    return (
      <AutomodProtectionCard
        settings={s}
        onChange={(n) => {
          latest.current = n;
          setS(n);
        }}
      />
    );
  }
  render(<Harness />);
  return latest;
}

const explanation = (over: Partial<AutomodExplanation> = {}): AutomodExplanation => ({
  protection: true,
  detections: [],
  held: false,
  wouldHoldIfOn: false,
  windowed: [],
  ...over,
});

beforeEach(() => {
  matrix.mockReset().mockResolvedValue(view());
  explain.mockReset();
  getSettings.mockReset();
  putSettings.mockReset();
});
afterEach(cleanup);

describe("the existing-install banner", () => {
  it("offers the pack while the answer is undecided and takes no action by itself", async () => {
    const latest = show(settingsOf(prot({ state: "" })));
    expect(screen.getByText(/Recommended protection is available/)).toBeTruthy();
    expect(latest.current.automod!.protection!.state).toBe("");
    expect(screen.queryByRole("list", { name: "Protection categories" })).toBeNull();
  });

  it("Turn on records on, and the categories appear", async () => {
    const latest = show(settingsOf(prot({ state: "" })));
    fireEvent.click(screen.getByRole("button", { name: "Turn on" }));
    expect(latest.current.automod!.protection!.state).toBe("on");
    expect(await screen.findByRole("list", { name: "Protection categories" })).toBeTruthy();
    // Nothing is armed by turning the pack on.
    expect(Object.keys(latest.current.automod!.on ?? {})).toEqual([]);
  });

  it("Not now records off, a final answer, and the banner goes", () => {
    const latest = show(settingsOf(prot({ state: "" })));
    fireEvent.click(screen.getByRole("button", { name: "Not now" }));
    expect(latest.current.automod!.protection!.state).toBe("off");
    expect(screen.queryByText(/Recommended protection is available/)).toBeNull();
  });
});

describe("the toggle", () => {
  it("turning the pack off removes only the ladder rungs, not the fresh-install flag cells", () => {
    const latest = show(
      settingsOf(prot(), { "twitch/flag/protection": true, "twitch/delete/protection": true, "youtube/timeout/protection": true }),
    );
    fireEvent.click(screen.getByRole("switch", { name: "Recommended protection" }));
    expect(latest.current.automod!.on).toEqual({ "twitch/flag/protection": true });
  });

  it("turning the pack off disarms its rungs in the same draft, and leaves other checkers' cells", () => {
    const latest = show(
      settingsOf(prot(), { "twitch/ban/protection": true, "twitch/ban/rules": true }),
    );
    fireEvent.click(screen.getByRole("switch", { name: "Recommended protection" }));
    expect(latest.current.automod!.protection!.state).toBe("off");
    expect(latest.current.automod!.on).toEqual({ "twitch/ban/rules": true });
  });
});

describe("categories", () => {
  it("lists every category with the profanity one off by default", () => {
    show(settingsOf(prot()));
    expect(screen.getByRole("switch", { name: "Hate and slurs" }).getAttribute("aria-checked")).toBe("true");
    expect(screen.getByRole("switch", { name: "Profanity" }).getAttribute("aria-checked")).toBe("false");
  });

  it("unticking writes a diff, and ticking back removes it", () => {
    const latest = show(settingsOf(prot()));
    const sw = screen.getByRole("switch", { name: "Doxxing (phone, email, IP)" });
    fireEvent.click(sw);
    expect(latest.current.automod!.protection!.categories).toEqual({ doxxing: { enabled: false } });
    fireEvent.click(sw);
    expect(latest.current.automod!.protection!.categories).toEqual({});
  });

  it("a detector switch and a hold checkbox write their own diffs", () => {
    const latest = show(settingsOf(prot()));
    fireEvent.click(screen.getByRole("button", { name: "Details for Doxxing (phone, email, IP)" }));
    fireEvent.click(screen.getByRole("switch", { name: "Street addresses" }));
    expect(latest.current.automod!.protection!.categories).toEqual({
      doxxing: { detectors: { address: true } },
    });
    fireEvent.click(screen.getByRole("checkbox", { name: "Phone numbers: hold off overlays" }));
    expect(latest.current.automod!.protection!.holdDetectors).toEqual({ "doxxing/phone": false });
  });

  it("adds and removes a word and an allowed word", () => {
    const latest = show(settingsOf(prot()));
    fireEvent.click(screen.getByRole("button", { name: "Details for Hate and slurs" }));
    fireEvent.change(screen.getByLabelText("Add a word"), { target: { value: "  zz  " } });
    fireEvent.click(screen.getAllByRole("button", { name: "Add" })[0]);
    expect(latest.current.automod!.protection!.addTerms).toEqual([
      { term: "zz", category: "hate_speech", tier: "severe" },
    ]);
    fireEvent.change(screen.getByLabelText("Allow a pack word (all categories)"), { target: { value: "ok" } });
    fireEvent.keyDown(screen.getByLabelText("Allow a pack word (all categories)"), { key: "Enter" });
    expect(latest.current.automod!.protection!.allowTerms).toEqual(["ok"]);
    fireEvent.click(screen.getByRole("button", { name: "Remove zz" }));
    expect(latest.current.automod!.protection!.addTerms).toEqual([]);
  });

  it("refuses a one-character word before the save", () => {
    show(settingsOf(prot()));
    fireEvent.click(screen.getByRole("button", { name: "Details for Hate and slurs" }));
    fireEvent.change(screen.getByLabelText("Add a word"), { target: { value: "z" } });
    expect(screen.getByText(/2 to 100 characters/)).toBeTruthy();
    expect((screen.getAllByRole("button", { name: "Add" })[0] as HTMLButtonElement).disabled).toBe(true);
  });

  it("edits the domain allow-list and the protected names", () => {
    const latest = show(settingsOf(prot()));
    fireEvent.click(screen.getByRole("button", { name: "Details for Scams and bait links" }));
    fireEvent.change(screen.getByLabelText("Allow a domain (all categories)"), { target: { value: "mysponsor.com" } });
    fireEvent.click(screen.getAllByRole("button", { name: "Add" })[0]);
    expect(latest.current.automod!.protection!.allowDomains).toEqual(["mysponsor.com"]);

    fireEvent.click(screen.getByRole("button", { name: "Details for Impersonation" }));
    fireEvent.change(screen.getByLabelText("Extra names to protect"), { target: { value: "alt_name" } });
    fireEvent.keyDown(screen.getByLabelText("Extra names to protect"), { key: "Enter" });
    expect(latest.current.automod!.protection!.impersonation.protect).toEqual(["alt_name"]);
  });

  it("the advanced section sets the ladder lengths and a ceiling, never below one second", () => {
    const latest = show(settingsOf(prot()));
    fireEvent.click(screen.getByRole("button", { name: "Advanced" }));
    fireEvent.change(screen.getByLabelText("Timeout length (seconds)"), { target: { value: "0" } });
    // Zero is never committed (it is a permanent ban); blurring restores the saved value.
    expect(latest.current.automod!.protection!.ladderTimeoutSeconds).toBe(600);
    fireEvent.blur(screen.getByLabelText("Timeout length (seconds)"));
    expect((screen.getByLabelText("Timeout length (seconds)") as HTMLInputElement).value).toBe("600");
    fireEvent.change(screen.getByLabelText("Strike window (seconds)"), { target: { value: "120" } });
    expect(latest.current.automod!.protection!.strikeWindowSeconds).toBe(120);
    fireEvent.change(screen.getByLabelText("Highest action: Scams and bait links"), { target: { value: "delete" } });
    expect(latest.current.automod!.protection!.ceilings).toEqual({ scam: "delete" });
    // Self-harm is hold-only and has no control to raise it.
    expect(screen.queryByLabelText(/Self-harm/)).toBeNull();
  });
});

describe("the ladder", () => {
  it("starts with nothing armed and says so", async () => {
    show(settingsOf(prot()));
    expect((await screen.findAllByText(/Nothing armed/)).length).toBe(2);
    expect(screen.getByRole("button", { name: "Time out (10 min) on Twitch" }).getAttribute("aria-pressed")).toBe(
      "false",
    );
  });

  it("arms a timeout in one click, without asking, and disarms it in one", async () => {
    const latest = show(settingsOf(prot()));
    const pill = await screen.findByRole("button", { name: "Time out (10 min) on Twitch" });
    fireEvent.click(pill);
    expect(latest.current.automod!.on).toEqual({ "twitch/timeout/protection": true });
    fireEvent.click(pill);
    expect(latest.current.automod!.on).toEqual({});
  });

  it("asks before arming delete or ban, and arms only on confirmation", async () => {
    const latest = show(settingsOf(prot()));
    fireEvent.click(await screen.findByRole("button", { name: "Ban on Twitch" }));
    expect(latest.current.automod!.on ?? {}).toEqual({});
    expect(screen.getByText(/cannot undo a delete or a ban/)).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Arm Ban" }));
    expect(latest.current.automod!.on).toEqual({ "twitch/ban/protection": true });
  });

  it("cancelling the confirmation arms nothing", async () => {
    const latest = show(settingsOf(prot()));
    fireEvent.click(await screen.findByRole("button", { name: "Delete on Twitch" }));
    fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
    expect(latest.current.automod!.on ?? {}).toEqual({});
  });

  it("a rung the platform cannot do is inert and gives the server's reason", async () => {
    matrix.mockResolvedValue(view(["facebook/ban", "facebook/timeout"]));
    show(settingsOf(prot()));
    await screen.findByRole("button", { name: "Delete on Facebook" });
    expect(screen.queryByRole("button", { name: "Ban on Facebook" })).toBeNull();
    expect(screen.getByLabelText(/Ban on Facebook is unavailable: facebook cannot do that/)).toBeTruthy();
  });

  it("shows what is armed per platform, and the Twitch-safe note only on Twitch", async () => {
    show(settingsOf(prot(), { "twitch/delete/protection": true, "twitch/timeout/protection": true }));
    expect(await screen.findByText("Armed: Delete, Time out (10 min)")).toBeTruthy();
    expect(screen.getAllByText(/Twitch's AutoMod and blocked terms only see Twitch chat/).length).toBe(1);
    expect(screen.getAllByText(/No native word filter is available/).length).toBe(1);
  });

  it("warns when the matrix master switch is off", async () => {
    const s = settingsOf(prot());
    s.automod!.enabled = false;
    show(s);
    expect(await screen.findByText(/switched off in the matrix below/)).toBeTruthy();
  });

  it("uses the configured timeout length in the pill", async () => {
    show(settingsOf(prot({ ladderTimeoutSeconds: 90 })));
    expect(await screen.findByRole("button", { name: "Time out (90 s) on Twitch" })).toBeTruthy();
  });

  it("survives the matrix read failing: no ladder, categories still usable", async () => {
    matrix.mockRejectedValue(new Error("down"));
    show(settingsOf(prot()));
    await waitFor(() => expect(matrix).toHaveBeenCalled());
    expect(screen.queryByText("Action ladder")).toBeNull();
    expect(screen.getByRole("switch", { name: "Hate and slurs" })).toBeTruthy();
  });
});

describe("the test box", () => {
  async function run(text: string) {
    await screen.findByRole("button", { name: "Delete on Twitch" });
    fireEvent.change(screen.getByLabelText("Message"), { target: { value: text } });
    fireEvent.click(screen.getByRole("button", { name: "Check" }));
  }

  it("asks the offline route and lists detectors without ever echoing the text", async () => {
    explain.mockResolvedValue(
      explanation({
        held: true,
        wouldHoldIfOn: true,
        detections: [
          {
            checker: "protection",
            category: "doxxing",
            detector: "doxxing/phone",
            reason: "phone number",
            hold: true,
            action: "flag",
            act: true,
            ladder: [
              { strike: 1, action: "flag" },
              { strike: 2, action: "delete" },
              { strike: 3, action: "ban" },
            ],
            live: true,
          },
        ],
      }),
    );
    show(settingsOf(prot()));
    fireEvent.change(screen.getByLabelText("Pretend it is from (optional)"), { target: { value: "bob" } });
    await run("secret-ish text");
    expect(await screen.findByText("This message would be held off your overlays.")).toBeTruthy();
    expect(explain).toHaveBeenCalledWith({ text: "secret-ish text", platform: "twitch", authorName: "bob" });
    expect(screen.getByText("Doxxing (phone, email, IP) · Phone numbers")).toBeTruthy();
    // The finding's own action is a flag, which is not a platform action: the
    // box shows the ladder's answer for strikes 1, 2 and 3 instead.
    expect(screen.queryByText(/Platform action/)).toBeNull();
    expect(
      screen.getByText("If repeated: Flag for review, then Delete, then Ban"),
    ).toBeTruthy();
    // The typed text appears in the input and nowhere in the answer.
    expect(screen.queryAllByText(/secret-ish text/)).toHaveLength(0);
  });

  it("says plainly when nothing fired, and what it cannot answer", async () => {
    explain.mockResolvedValue(explanation());
    show(settingsOf(prot()));
    await run("gg");
    expect(await screen.findByText("No detector fired.")).toBeTruthy();
    expect(screen.getByText("This message would reach your overlays.")).toBeTruthy();
    expect(screen.getByText(/depend on what a viewer sent recently/)).toBeTruthy();
  });

  it("separates what the live engine does from what the pack would do", async () => {
    explain.mockResolvedValue(
      explanation({
        protection: false,
        wouldHoldIfOn: true,
        detections: [
          {
            checker: "protection",
            category: "hate_speech",
            detector: "hate_speech/severe",
            tier: "severe",
            form: "pack",
            reason: "hate term",
            hold: true,
            action: "flag",
            act: false,
            live: false,
          },
        ],
      }),
    );
    show(settingsOf(prot()));
    await run("x");
    expect(await screen.findByText(/shows what turning it on would do/)).toBeTruthy();
    expect(screen.getByText(/once protection is on/)).toBeTruthy();
    expect(screen.getByText(/Tier: Severe · Matched on: pack · not live yet/)).toBeTruthy();
  });

  it("reports a failure instead of an empty answer", async () => {
    explain.mockRejectedValue(new ApiError(400, "text is over 2000 characters"));
    show(settingsOf(prot()));
    await run("x");
    expect(await screen.findByRole("alert")).toBeTruthy();
    expect(screen.getByRole("alert").textContent).toContain("text is over 2000 characters");
    expect(screen.queryByText("No detector fired.")).toBeNull();
  });

  it("does not send an empty message", async () => {
    show(settingsOf(prot()));
    await screen.findByRole("button", { name: "Delete on Twitch" });
    expect((screen.getByRole("button", { name: "Check" }) as HTMLButtonElement).disabled).toBe(true);
    expect(explain).not.toHaveBeenCalled();
  });
});

describe("form fields", () => {
  it("allow-word entries below the server's two-character bound cannot be added", () => {
    show(settingsOf(prot()));
    fireEvent.click(screen.getByRole("button", { name: "Details for Hate and slurs" }));
    const add = screen.getByLabelText("Allow a pack word (all categories)");
    fireEvent.change(add, { target: { value: "x" } });
    const btn = add.parentElement!.querySelector("button") as HTMLButtonElement;
    expect(btn.disabled).toBe(true);
    expect(screen.getByText("A word is 2 to 100 characters.")).toBeTruthy();
    fireEvent.change(add, { target: { value: "xy" } });
    expect(btn.disabled).toBe(false);
  });

  it("clearing a seconds field to retype it does not turn into 1 on the way, and is clamped to the server's bound", () => {
    const latest = show(settingsOf(prot()));
    fireEvent.click(screen.getByRole("button", { name: "Advanced" }));
    const f = screen.getByLabelText("Timeout length (seconds)") as HTMLInputElement;
    fireEvent.change(f, { target: { value: "" } });
    expect(f.value).toBe("");
    fireEvent.change(f, { target: { value: "3" } });
    fireEvent.change(f, { target: { value: "30" } });
    expect(f.value).toBe("30");
    expect(latest.current.automod!.protection!.ladderTimeoutSeconds).toBe(30);
    fireEvent.change(f, { target: { value: "99999999" } });
    expect(latest.current.automod!.protection!.ladderTimeoutSeconds).toBe(1209600);
    fireEvent.change(f, { target: { value: "" } });
    fireEvent.blur(f);
    expect(f.value).toBe("1209600");
  });
});

describe("the standalone offer (chat review list)", () => {
  const stored = (state: "" | "on") => settingsOf(prot({ state }));

  it("shows only while undecided, and records the answer with a save", async () => {
    getSettings.mockResolvedValue(stored(""));
    putSettings.mockImplementation(async (s) => s);
    render(<ProtectionOfferStandalone />);
    fireEvent.click(await screen.findByRole("button", { name: "Turn on" }));
    await waitFor(() => expect(putSettings).toHaveBeenCalled());
    expect(putSettings.mock.calls[0][0].automod!.protection!.state).toBe("on");
    await waitFor(() => expect(screen.queryByText(/Recommended protection is available/)).toBeNull());
  });

  it("renders nothing once answered, or when settings cannot be read", async () => {
    getSettings.mockResolvedValue(stored("on"));
    const { container } = render(<ProtectionOfferStandalone />);
    await waitFor(() => expect(getSettings).toHaveBeenCalled());
    expect(container.textContent).toBe("");
    cleanup();
    getSettings.mockRejectedValue(new Error("down"));
    const again = render(<ProtectionOfferStandalone />);
    await waitFor(() => expect(getSettings).toHaveBeenCalledTimes(2));
    expect(again.container.textContent).toBe("");
  });

  it("re-reads the settings after a failed save so a retry carries the current version", async () => {
    getSettings.mockResolvedValue(stored(""));
    putSettings.mockRejectedValueOnce(new Error("settings_conflict")).mockImplementation(async (s) => s);
    render(<ProtectionOfferStandalone />);
    fireEvent.click(await screen.findByRole("button", { name: "Turn on" }));
    await waitFor(() => expect(getSettings).toHaveBeenCalledTimes(2));
    fireEvent.click(await screen.findByRole("button", { name: "Turn on" }));
    await waitFor(() => expect(putSettings).toHaveBeenCalledTimes(2));
  });

  it("keeps the banner when the save fails", async () => {
    getSettings.mockResolvedValue(stored(""));
    putSettings.mockRejectedValue(new Error("nope"));
    render(<ProtectionOfferStandalone />);
    fireEvent.click(await screen.findByRole("button", { name: "Not now" }));
    await waitFor(() => expect(putSettings).toHaveBeenCalled());
    expect(screen.getByText(/Recommended protection is available/)).toBeTruthy();
  });
});
