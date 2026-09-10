// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, within } from "@testing-library/react";

import { FailoverControl } from "./Dashboard";
import { translate } from "@/lib/i18n";
import type { FailoverStatus } from "@/lib/types";

/* FAILOVER WAS A ONE-WAY DOOR. Issue #768.
 *
 * POST /failover/source is registered at internal/api/api.go:960, works, and
 * had zero callers in this console. `failover.return` defaults to `manual` on
 * purpose -- an automatic return flaps, and every flap is a visible cut -- so
 * the product moved a broadcast onto its backup, slate or playlist when the
 * primary dropped and then offered NOTHING anywhere that could move it back.
 * The operator discovered that mid-broadcast, watching the standby feed go to
 * the platform with no control able to end it.
 *
 * These tests are about REACHABILITY, which is why several of them are about
 * what is on screen rather than about what was called. A wrapper in api.ts with
 * no button is the same bug one layer up, and a type with a field in it is the
 * same bug two layers up.
 *
 * The absent case is asserted as hard as the present one. A permanent row on
 * every install is furniture, and furniture is what an operator scrolls past on
 * the one day it matters -- the argument the failover exposure notice on this
 * same page already makes at length. */

afterEach(cleanup);

const RETURN = translate("en", "dash.failoverReturn");
const HAND_BACK = translate("en", "dash.failoverHandBack");

function state(over: Partial<FailoverStatus> = {}): FailoverStatus {
  return {
    active: "backup",
    switchedAt: "2026-09-09T10:00:00Z",
    switches: 1,
    primaryLive: true,
    backupLive: true,
    backupEnabled: true,
    slateEnabled: true,
    ...over,
  };
}

function draw(over: Partial<FailoverStatus> | null, opts: { manualReturn?: boolean } = {}) {
  const onSwitch = vi.fn().mockResolvedValue(undefined);
  render(
    <FailoverControl
      state={over === null ? null : state(over)}
      manualReturn={opts.manualReturn ?? true}
      busy={false}
      onSwitch={onSwitch}
    />,
  );
  return onSwitch;
}

describe("FailoverControl: the way back to the primary ingest", () => {
  it("renders nothing while the primary is on air and nothing is pinned", () => {
    draw({ active: "primary" });

    // Not "no button": nothing at all. There is no door to be on the wrong
    // side of, and a standing panel saying so is the noise that makes the real
    // one invisible.
    expect(screen.queryByRole("button")).toBeNull();
    expect(document.body.textContent).toBe("");
  });

  it("renders nothing when the failover tier is not running", () => {
    // The default on most installs. `failover` is omitted from the status
    // payload entirely, and absent must draw nothing rather than "Nothing".
    draw(null);
    expect(document.body.textContent).toBe("");
  });

  it("says which source is on air, and offers the way back", () => {
    draw({ active: "backup" });

    expect(screen.getByText(/On air/).textContent).toContain("Backup ingest");
    expect(
      screen.getByRole("button", { name: RETURN }),
      "the broadcast is on the backup and no control on the page can end that",
    ).toBeTruthy();
  });

  it("does not switch the source on the click itself", () => {
    const onSwitch = draw({ active: "backup" });

    fireEvent.click(screen.getByRole("button", { name: RETURN }));

    // Cutting the feed the platforms are receiving is not something a stray
    // click on a busy dashboard gets to do.
    expect(
      onSwitch,
      "the switch went out on the first click, cutting the live feed with no confirmation",
    ).not.toHaveBeenCalled();
  });

  it("asks first, and says what the switch does to the broadcast", async () => {
    draw({ active: "backup" });
    fireEvent.click(screen.getByRole("button", { name: RETURN }));

    const dialog = await screen.findByRole("dialog");
    // The two facts that make this a decision rather than a click: viewers see
    // it, and the destinations do not have to be restarted.
    expect(dialog.textContent).toContain("viewers see the join");
    expect(dialog.textContent).toContain("keep running");
  });

  it("switches to the primary once the operator confirms", async () => {
    const onSwitch = draw({ active: "backup" });
    fireEvent.click(screen.getByRole("button", { name: RETURN }));

    const dialog = await screen.findByRole("dialog");
    fireEvent.click(within(dialog).getByRole("button", { name: RETURN }));

    expect(onSwitch).toHaveBeenCalledWith("primary");
  });

  it("says that a manual return mode is why nothing has come back on its own", () => {
    draw({ active: "slate" }, { manualReturn: true });

    expect(document.body.textContent).toContain("the return mode is manual");
  });

  it("does not claim a manual return mode on an install configured for auto", () => {
    // The same panel on an install whose return mode is `auto`. Saying
    // "nothing brings this back" there would be a false alarm about a return
    // that is already coming.
    draw({ active: "slate" }, { manualReturn: false });

    expect(document.body.textContent).not.toContain("the return mode is manual");
    expect(document.body.textContent).toContain("comes back once the primary ingest");
  });

  it("warns that a switch to a primary that is still down is held, not applied", () => {
    // engine.chooseFrom honours a pin only while that source is available, so
    // the request answers 200 and the picture does not change. Without this the
    // operator gets a success toast and watches the backup keep going out.
    draw({ active: "backup", primaryLive: false });

    expect(document.body.textContent).toContain("held until it is");
  });

  it("offers no hand-back while the detector is already in charge", () => {
    draw({ active: "backup", pinned: "" });

    expect(screen.queryByRole("button", { name: HAND_BACK })).toBeNull();
  });

  it("hands the choice back to the detector, after confirming that too", async () => {
    const onSwitch = draw({ active: "slate", pinned: "slate" });

    fireEvent.click(screen.getByRole("button", { name: HAND_BACK }));
    expect(onSwitch).not.toHaveBeenCalled();

    const dialog = await screen.findByRole("dialog");
    // Clearing a pin is not free: with no pin, a slate gives way to a live
    // ingest the moment one is delivering (engine.chooseFrom's slate branch).
    expect(dialog.textContent).toContain("change what is on air immediately");
    fireEvent.click(within(dialog).getByRole("button", { name: HAND_BACK }));

    expect(onSwitch).toHaveBeenCalledWith("auto");
  });

  it("shows the pin as a pin even while it is the primary that is pinned", () => {
    // active === primary would normally draw nothing. A standing pin is the
    // exception: the detector is not choosing, and the only control that can
    // give it back is here.
    draw({ active: "primary", pinned: "primary" });

    expect(screen.getByRole("button", { name: HAND_BACK })).toBeTruthy();
    expect(screen.queryByRole("button", { name: RETURN })).toBeNull();
  });
});
