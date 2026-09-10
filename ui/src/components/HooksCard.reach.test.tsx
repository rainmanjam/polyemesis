// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";

import { HooksCard } from "./HooksCard";
import { api } from "@/lib/api";
import { translate } from "@/lib/i18n";
import type { Hook, HookMeta } from "@/lib/types";

/* TWO THINGS THE SERVER COULD DO AND NO OPERATOR COULD REACH.
 *
 * #771 -- hooks.Hook.Validate refuses a LAN, loopback or metadata address with
 * "set allowPrivateTarget to permit a self-hosted endpoint on purpose". The
 * field was on the wire, in the store, migrated into old databases, and had no
 * control anywhere: the server's own error named a setting the console could
 * not set. On a self-hosted install, posting to a box on the same LAN is the
 * ordinary case, so this was not an edge -- it was a dead end reached by
 * following the instructions in the error message.
 *
 * #778 -- docs/HOOKS.md:141 says "if you lose it, edit the hook and set a new
 * one", PUT /hooks/{id} has always accepted a `secret`, and the edit dialog had
 * no field for it. The workaround, deleting the hook and making another, is a
 * DIFFERENT operation: `sequence` counts from 1 per endpoint and HOOKS.md tells
 * receivers a reset means polyemesis restarted, so a consumer verifying
 * ordering sees its counter go backwards.
 *
 * Both are asserted through the rendered form and the request that leaves it,
 * because the bug in both cases was a field that existed everywhere except on a
 * screen. */

const list = vi.spyOn(api.hooks, "list");
const meta = vi.spyOn(api.hooks, "meta");
const update = vi.spyOn(api.hooks, "update");
const create = vi.spyOn(api.hooks, "create");

const ALLOW = translate("en", "common.allowPrivateTarget");
const NEW_KEY = translate("en", "hooks.rotateLabel");
const GENERATE = translate("en", "hooks.rotateGenerate");

const hookMeta = () =>
  ({
    specVersion: "1",
    headers: { signature: "X-Polyemesis-Signature" },
    triggers: ["ingest.published"],
    bounds: { minTimeoutSeconds: 1, maxTimeoutSeconds: 30, minAttempts: 1, maxAttempts: 5 },
    stats: { sent: 0, failed: 0, endpoints: 0, queued: 0, dropped: 0, retries: 0 },
  }) as unknown as HookMeta;

const stored = (over: Partial<Hook> = {}): Hook =>
  ({
    id: 7,
    name: "CI",
    enabled: true,
    url: "https://ci.example.com/[redacted]",
    hasSecret: true,
    triggers: [],
    timeoutSeconds: 10,
    maxAttempts: 3,
    createdAt: "",
    updatedAt: "",
    ...over,
  }) as Hook;

beforeEach(() => {
  list.mockReset().mockResolvedValue([stored()]);
  meta.mockReset().mockResolvedValue(hookMeta());
  update.mockReset().mockResolvedValue(stored() as never);
  create.mockReset().mockResolvedValue({ id: 8, secret: "server-minted" } as never);
});

// Cleanup only. restoreAllMocks() would put the real api.hooks back and the
// next test would render against a fetch that is not there -- which reports as
// "the webhooks could not be read", i.e. as a failure of the thing under test.
afterEach(cleanup);

/** Opens the edit dialog on the one stored hook. */
async function openEdit() {
  render(<HooksCard />);
  fireEvent.click(await screen.findByRole("button", { name: translate("en", "common.edit") }));
  return screen.findByRole("dialog");
}

describe("HooksCard: the private-target opt-in (#771)", () => {
  it("offers the opt-in the server's own refusal names", async () => {
    await openEdit();

    expect(
      screen.getByRole("checkbox", { name: ALLOW }),
      "the 400 tells the operator to set a field nothing on this dialog can set",
    ).toBeTruthy();
  });

  it("explains what the guard is for without making the normal case sound wrong", async () => {
    await openEdit();

    const hint = screen.getByText(/refuses to deliver/);
    // Both halves. Only the risk reads as a scolding for a normal workflow;
    // only the permission reads as a switch with no consequence.
    expect(hint.textContent).toContain("receiver on your own network");
    expect(hint.textContent).toContain("probe the machine");
  });

  it("is off until it is ticked, and reaches the wire when it is", async () => {
    await openEdit();

    fireEvent.click(screen.getByRole("checkbox", { name: ALLOW }));
    fireEvent.click(screen.getByRole("button", { name: translate("en", "common.save") }));

    await waitFor(() => expect(update).toHaveBeenCalled());
    expect(update.mock.calls[0][1]).toMatchObject({ allowPrivateTarget: true });
  });

  it("sends the opt-in as false rather than omitting it when it is not ticked", async () => {
    // Omitted would leave a hook that ALREADY has the opt-in unable to lose it
    // -- hookRequest treats an absent field as "leave the stored value alone".
    await openEdit();
    fireEvent.click(screen.getByRole("button", { name: translate("en", "common.save") }));

    await waitFor(() => expect(update).toHaveBeenCalled());
    expect(update.mock.calls[0][1]).toMatchObject({ allowPrivateTarget: false });
  });

  it("sends only fields the server's request type declares", async () => {
    // api.decodeJSONInto calls DisallowUnknownFields and api.hookRequest has no
    // `id`, `hasSecret`, `createdAt` or `updatedAt`. Handing it the stored hook
    // the dialog was seeded with earns a 400 naming whichever one the decoder
    // reached first, which is every edit save failing.
    await openEdit();
    fireEvent.click(screen.getByRole("button", { name: translate("en", "common.save") }));

    await waitFor(() => expect(update).toHaveBeenCalled());
    const sent = Object.keys(update.mock.calls[0][1]);
    expect(sent.filter((k) => ["id", "hasSecret", "createdAt", "updatedAt"].includes(k))).toEqual(
      [],
    );
  });
});

describe("HooksCard: rotating the signing key (#778)", () => {
  it("offers a new signing key on the edit dialog", async () => {
    await openEdit();

    expect(
      screen.getByLabelText(NEW_KEY),
      "the documented rotation has no field, so the only way to change a key is delete-and-recreate",
    ).toBeTruthy();
  });

  it("says why rotating is not the same as deleting and recreating", async () => {
    await openEdit();

    // The consequence a receiver sees, which is the whole reason this is a
    // rotation rather than a second webhook.
    expect(screen.getByText(/delivery numbering/).textContent).toContain("never received");
  });

  it("keeps the current key when the field is left alone", async () => {
    await openEdit();
    fireEvent.click(screen.getByRole("button", { name: translate("en", "common.save") }));

    await waitFor(() => expect(update).toHaveBeenCalled());
    // db.UpdateHook reads an empty secret as "unchanged". Sending "" would be
    // harmless; sending the KEY as an empty string is not, so the field is
    // omitted rather than blanked.
    expect(update.mock.calls[0][1]).not.toHaveProperty("secret");
  });

  it("sends the new key, and shows it once, because the server never will", async () => {
    await openEdit();

    fireEvent.change(screen.getByLabelText(NEW_KEY), { target: { value: "a-new-signing-key" } });
    fireEvent.click(screen.getByRole("button", { name: translate("en", "common.save") }));

    await waitFor(() => expect(update).toHaveBeenCalled());
    expect(update.mock.calls[0][1]).toMatchObject({ secret: "a-new-signing-key" });
    // PUT answers with the hook and never with a plaintext key, so this tab is
    // the only place the operator can still read what they just set.
    expect(await screen.findByText(/cannot be shown again/i)).toBeTruthy();
  });

  it("can mint one rather than leaving the operator to invent a key", async () => {
    await openEdit();

    fireEvent.click(screen.getByRole("button", { name: GENERATE }));

    // 32 bytes as hex, the length db.CreateHook mints for a new hook.
    const field = screen.getByLabelText(NEW_KEY) as HTMLInputElement;
    expect(field.value).toMatch(/^[0-9a-f]{64}$/);
  });

  it("offers no rotation on a hook that does not exist yet", async () => {
    // A create mints its own key and shows it once. A second way to set one on
    // the same form is how an operator ends up with a key they typed and a key
    // they were shown, and no way to tell which the server kept.
    render(<HooksCard />);
    fireEvent.click(await screen.findByRole("button", { name: translate("en", "hooks.new") }));
    await screen.findByRole("dialog");

    expect(screen.queryByLabelText(NEW_KEY)).toBeNull();
  });
});
