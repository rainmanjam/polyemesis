// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";

import { AutomationPage } from "./AutomationPage";
import { LiveDataContext, type LiveData } from "@/hooks/useLiveData";
import { translate } from "@/lib/i18n";

/* THE 400 THAT NAMED A FIELD NOTHING COULD SET. Issue #771.
 *
 * alerts.Rule.Validate refuses a LAN, loopback or cloud-metadata endpoint with
 * "set allowPrivateTarget to permit a self-hosted endpoint on purpose". The
 * field is on the wire, in the store, and migrated into upgraded databases --
 * and this dialog had no control for it, so an operator following the error
 * message had nowhere to go. A self-hosted install notifying a box on the same
 * LAN is the ordinary case, not an edge one, which is what makes a refusal with
 * an unreachable escape hatch the whole bug: a guard nobody can satisfy is a
 * guard that gets switched off wholesale instead of used.
 *
 * Asserted through the rendered form and the body that leaves it, because the
 * defect was precisely a field that existed everywhere except on a screen. */

const originalFetch = globalThis.fetch;
const ALLOW = translate("en", "common.allowPrivateTarget");
const NEW_RULE = "New rule";
const CREATE = translate("en", "auto.createRule");

const seen: { url: string; body: unknown }[] = [];

const routes: Record<string, unknown> = {
  "/api/v1/alerts/rules": [],
  "/api/v1/alerts/meta": {
    events: ["ingest.down"],
    formats: ["json"],
    severities: ["info", "warning"],
    bounds: { maxNameLen: 120 },
  },
  "/api/v1/schedules": [],
  "/api/v1/schedules/runs": [],
};

beforeEach(() => {
  seen.length = 0;
  globalThis.fetch = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(typeof input === "string" ? input : input.toString());
    seen.push({ url, body: init?.body ? JSON.parse(String(init.body)) : undefined });
    return {
      ok: true,
      status: 200,
      text: async () => JSON.stringify(routes[url.split("?")[0]] ?? {}),
    } as unknown as Response;
  }) as unknown as typeof fetch;
});

afterEach(() => {
  globalThis.fetch = originalFetch;
  cleanup();
});

function draw() {
  const value = {
    programme: 1,
    programmeKnown: true,
    snapshotKnown: true,
    status: null,
    source: null,
    bitrate: [],
    logs: [],
  } as unknown as LiveData;
  return render(
    <LiveDataContext.Provider value={value}>
      <AutomationPage />
    </LiveDataContext.Provider>,
  );
}

async function openNewRule() {
  draw();
  fireEvent.click(await screen.findByRole("button", { name: NEW_RULE }));
  return screen.findByRole("dialog");
}

describe("AutomationPage: the private-target opt-in on an alert rule (#771)", () => {
  it("offers the opt-in the server's own refusal names", async () => {
    await openNewRule();

    expect(
      screen.getByRole("checkbox", { name: ALLOW }),
      "the 400 tells the operator to set a field nothing on this dialog can set",
    ).toBeTruthy();
  });

  it("words the risk without making a normal self-hosted setup sound wrong", async () => {
    await openNewRule();

    const hint = screen.getByText(/refuses to deliver/);
    expect(hint.textContent).toContain("receiver on your own network");
    expect(hint.textContent).toContain("probe the machine");
  });

  it("sends the opt-in when it is ticked", async () => {
    await openNewRule();

    fireEvent.change(screen.getByLabelText(translate("en", "auto.name")), {
      target: { value: "Home Assistant" },
    });
    fireEvent.click(screen.getByRole("checkbox", { name: ALLOW }));
    fireEvent.click(screen.getByRole("button", { name: CREATE }));

    await waitFor(() =>
      expect(seen.some((c) => c.url.endsWith("/alerts/rules") && c.body)).toBe(true),
    );
    const post = seen.find((c) => c.url.endsWith("/alerts/rules") && c.body);
    expect(post?.body).toMatchObject({ allowPrivateTarget: true });
  });

  it("sends it as false rather than omitting it when it is not ticked", async () => {
    // Omitted would leave a rule that ALREADY carries the opt-in unable to lose
    // it: the server's request type reads an absent field as "leave the stored
    // value alone", so the box would tick but never untick.
    await openNewRule();

    fireEvent.change(screen.getByLabelText(translate("en", "auto.name")), {
      target: { value: "Slack" },
    });
    fireEvent.click(screen.getByRole("button", { name: CREATE }));

    await waitFor(() =>
      expect(seen.some((c) => c.url.endsWith("/alerts/rules") && c.body)).toBe(true),
    );
    const post = seen.find((c) => c.url.endsWith("/alerts/rules") && c.body);
    expect(post?.body).toMatchObject({ allowPrivateTarget: false });
  });
});
