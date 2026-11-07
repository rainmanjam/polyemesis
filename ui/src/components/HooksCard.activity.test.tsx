// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";

import { HooksCard } from "./HooksCard";
import { api } from "@/lib/api";
import { translate } from "@/lib/i18n";
import type { Hook, HookMeta } from "@/lib/types";

/* The `activity` trigger is OPT-IN: a hook receives it only by naming it, and
 * an empty trigger list still means "every lifecycle trigger" without it. The
 * picker shows it after the ordinary ones, with that said beside the box. */

const list = vi.spyOn(api.hooks, "list");
const meta = vi.spyOn(api.hooks, "meta");
const create = vi.spyOn(api.hooks, "create");

const hookMeta = (withOptIn: boolean) =>
  ({
    specVersion: "1",
    headers: { signature: "X-Polyemesis-Signature" },
    triggers: ["ingest.published", "destination.up"],
    ...(withOptIn ? { optInTriggers: ["activity"] } : {}),
    bounds: { minTimeoutSeconds: 1, maxTimeoutSeconds: 30, minAttempts: 1, maxAttempts: 5 },
    stats: { sent: 0, failed: 0, endpoints: 0, queued: 0, dropped: 0, retries: 0 },
  }) as unknown as HookMeta;

beforeEach(() => {
  list.mockReset().mockResolvedValue([] as Hook[]);
  create.mockReset().mockResolvedValue({ id: 8, secret: "s" } as never);
});
afterEach(cleanup);

async function openNew(withOptIn = true) {
  meta.mockReset().mockResolvedValue(hookMeta(withOptIn));
  render(<HooksCard />);
  fireEvent.click((await screen.findAllByRole("button", { name: translate("en", "hooks.new") }))[0]);
  return screen.findByRole("dialog");
}

describe("HooksCard: the opt-in activity trigger", () => {
  it("lists it after the ordinary triggers, labelled, with the opt-in hint", async () => {
    const dialog = await openNew();
    const boxes = within(dialog).getAllByRole("checkbox").map((b) => b.parentElement?.textContent ?? "");
    const at = boxes.findIndex((t) => t.includes(translate("en", "hooks.triggerActivity")));
    expect(at).toBeGreaterThan(boxes.findIndex((t) => t.includes(translate("en", "hooks.triggerIngestPublished"))));
    expect(at).toBeGreaterThan(boxes.findIndex((t) => t.includes(translate("en", "hooks.triggerDestinationUp"))));
    expect(within(dialog).getByText(translate("en", "hooks.triggerActivityHint"))).toBeTruthy();
  });

  it("is absent on a server that does not send optInTriggers", async () => {
    const dialog = await openNew(false);
    expect(within(dialog).queryByText(translate("en", "hooks.triggerActivity"))).toBeNull();
  });

  it("is sent as a trigger when ticked, and not when it is not", async () => {
    const dialog = await openNew();
    fireEvent.change(within(dialog).getByLabelText(translate("en", "hooks.url")), { target: { value: "https://example.com/h" } });
    const activity = within(dialog).getByRole("checkbox", { name: new RegExp(translate("en", "hooks.triggerActivity")) });
    expect(activity.getAttribute("aria-checked")).toBe("false");
    fireEvent.click(activity);
    expect(activity.getAttribute("aria-checked")).toBe("true");
    fireEvent.click(within(dialog).getByRole("button", { name: translate("en", "hooks.create") }));
    await waitFor(() => expect(create).toHaveBeenCalled());
    // A catch-all hook keeps every lifecycle trigger it received, plus activity.
    expect((create.mock.calls[0][0] as { triggers: string[] }).triggers).toEqual([
      "ingest.published",
      "destination.up",
      "activity",
    ]);
  });

  it("unticking it leaves the lifecycle triggers in place", async () => {
    const dialog = await openNew();
    fireEvent.change(within(dialog).getByLabelText(translate("en", "hooks.url")), { target: { value: "https://example.com/h" } });
    const activity = within(dialog).getByRole("checkbox", { name: new RegExp(translate("en", "hooks.triggerActivity")) });
    fireEvent.click(activity);
    fireEvent.click(activity);
    fireEvent.click(within(dialog).getByRole("button", { name: translate("en", "hooks.create") }));
    await waitFor(() => expect(create).toHaveBeenCalled());
    expect((create.mock.calls[0][0] as { triggers: string[] }).triggers).toEqual(["ingest.published", "destination.up"]);
  });
});
