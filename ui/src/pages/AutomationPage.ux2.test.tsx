// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";

import { AutomationPage } from "./AutomationPage";
import { LiveDataContext, type LiveData } from "@/hooks/useLiveData";
import { translate, type TranslationKey } from "@/lib/i18n";

/* app-council A54-A58 and the EmptyState adoption, through the rendered page.
 *
 * Every assertion here is about what the operator reads or what leaves the
 * dialog: the explicit All destinations choice and the refusal beside the save
 * button (A54), the All events switch (A55), the Hooks tab (A56), translated
 * labels in place of machine ids (A57), and named selects (A58). */

const en = (k: TranslationKey, v?: Record<string, string | number>) => translate("en", k, v);
const originalFetch = globalThis.fetch;
const seen: { url: string; method: string; body: unknown }[] = [];

const base = {
  enabled: true, kind: "daily", tz: "UTC", atMinutes: 1380, days: [], runAt: "",
  graceSeconds: 300, lastRunAt: "", createdAt: "", updatedAt: "", localTime: "23:00",
};
let routes: Record<string, unknown>;

beforeEach(() => {
  seen.length = 0;
  routes = {
    "/api/v1/alerts/rules": [
      {
        id: 1, name: "Ops", enabled: true, url: "https://x.invalid/…", format: "json",
        events: [], minSeverity: "warning", debounceSeconds: 10, minIntervalSeconds: 30,
        createdAt: "", updatedAt: "",
      },
    ],
    "/api/v1/alerts/meta": {
      events: ["destination.down", "disk.low"],
      formats: ["json", "discord"],
      severities: ["info", "warning", "critical"],
      bounds: { maxNameLen: 120 },
      stats: { queued: 0, dropped: 0, coalesced: 0, pending: 0, sent: 0, failed: 0, retries: 0, deferred: 0 },
    },
    "/api/v1/schedules": [
      { ...base, id: 7, name: "Night stop", action: "stop", destinationIds: [2], allDestinations: false },
      { ...base, id: 8, name: "Show start", action: "start", destinationIds: [], allDestinations: true },
      { ...base, id: 9, name: "Broken row", action: "stop", destinationIds: [], allDestinations: false },
    ],
    "/api/v1/schedules/runs": [],
  };
  globalThis.fetch = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(typeof input === "string" ? input : input.toString());
    seen.push({ url, method: init?.method ?? "GET", body: init?.body ? JSON.parse(String(init.body)) : undefined });
    return {
      ok: true,
      status: 200,
      text: async () => JSON.stringify(routes[url.split("?")[0]] ?? {}),
    } as unknown as Response;
  }) as unknown as typeof fetch;
});

afterEach(() => {
  globalThis.fetch = originalFetch;
  window.history.replaceState(null, "", "/");
  cleanup();
});

function draw(tab?: string) {
  window.history.replaceState(null, "", tab ? `/automation?tab=${tab}` : "/automation");
  const value = {
    programme: 1, programmeKnown: true, snapshotKnown: true,
    status: { destinations: [{ id: 2, name: "YouTube main" }, { id: 3, name: "Twitch" }] },
    source: null, bitrate: [], logs: [],
  } as unknown as LiveData;
  return render(
    <LiveDataContext.Provider value={value}>
      <AutomationPage />
    </LiveDataContext.Provider>,
  );
}

const rowOf = async (name: string) => (await screen.findByText(name)).closest("tr") as HTMLElement;

describe("A56: the tab that holds script hooks is called Hooks", () => {
  it("names the tab Hooks and keeps the subtitle to alerts and schedules", async () => {
    draw();
    expect(await screen.findByRole("tab", { name: en("auto.tabWebhooks") })).toBeTruthy();
    expect(en("auto.tabWebhooks")).toBe("Hooks");
    expect(screen.queryByRole("tab", { name: /Webhooks/ })).toBeNull();
    expect(en("auto.subtitle")).not.toMatch(/webhook/i);
  });
});

describe("A57: machine values are shown as labels", () => {
  it("badges a schedule with its translated action, not its id", async () => {
    draw("schedules");
    const row = await rowOf("Night stop");
    expect(within(row).getByText(en("auto.stopDestinations"))).toBeTruthy();
    expect(within(row).queryByText("stop", { exact: true })).toBeNull();
  });

  it("summarises a rule with the format and severity labels", async () => {
    draw();
    const row = await rowOf("Ops");
    expect(row.textContent).toContain(en("auto.formatPlainJson"));
    expect(row.textContent).toContain(en("timeline.severityWarning"));
    expect(row.textContent).not.toMatch(/\bjson\b|\bwarning\b/);
  });
});

describe("A54: every destination is a choice, and naming nothing is refused", () => {
  it("lists every-destination rows as such, and a row with neither as not running", async () => {
    draw("schedules");
    expect((await rowOf("Show start")).textContent).toContain(en("auto.targetEveryDestination"));
    expect((await rowOf("Broken row")).textContent).toContain(en("auto.targetNoDestinations"));
  });

  it("says why Save is greyed when the name is empty, linked to the button", async () => {
    draw("schedules");
    fireEvent.click(within(await rowOf("Night stop")).getByRole("button", { name: en("auto.edit") }));
    const dialog = await screen.findByRole("dialog");
    fireEvent.change(within(dialog).getByLabelText(en("auto.name")), { target: { value: "" } });
    const save = within(dialog).getByRole("button", { name: en("auto.saveSchedule") }) as HTMLButtonElement;
    expect(save.disabled).toBe(true);
    const reason = document.getElementById(save.getAttribute("aria-describedby") ?? "");
    expect(reason?.textContent).toBe(en("auto.blockName"));
  });

  it("refuses to save a stop with nothing picked, says why, and saves it once All destinations is ticked", async () => {
    draw("schedules");
    fireEvent.click(within(await rowOf("Night stop")).getByRole("button", { name: en("auto.edit") }));
    const dialog = await screen.findByRole("dialog");

    // The hint is the stop's own, not "what start the show usually means".
    expect(within(dialog).getByText(en("auto.destinationsStopHint"))).toBeTruthy();

    fireEvent.click(within(dialog).getByRole("checkbox", { name: /YouTube main/ }));
    const save = within(dialog).getByRole("button", { name: en("auto.saveSchedule") }) as HTMLButtonElement;
    expect(save.disabled, "a stop naming nothing could be saved").toBe(true);
    expect(within(dialog).getByText(en("auto.blockDestinationsNone"))).toBeTruthy();

    fireEvent.click(within(dialog).getByRole("checkbox", { name: en("auto.allDestinations") }));
    expect(save.disabled).toBe(false);
    // The list is not drawn beside All: both at once is refused by the server.
    expect(within(dialog).queryByRole("checkbox", { name: /YouTube main/ })).toBeNull();
    fireEvent.click(save);

    await waitFor(() => expect(seen.some((c) => c.method === "PUT" && c.url.endsWith("/schedules/7"))).toBe(true));
    const put = seen.find((c) => c.method === "PUT" && c.url.endsWith("/schedules/7"));
    expect(put?.body).toMatchObject({ allDestinations: true, destinationIds: [] });
  });

  it("sends allDestinations from the list's on/off switch, so toggling cannot change the target", async () => {
    draw("schedules");
    const row = await rowOf("Show start");
    fireEvent.click(within(row).getByRole("switch"));
    await waitFor(() => expect(seen.some((c) => c.method === "PUT" && c.url.endsWith("/schedules/8"))).toBe(true));
    const put = seen.find((c) => c.method === "PUT" && c.url.endsWith("/schedules/8"));
    expect(put?.body).toMatchObject({ allDestinations: true, enabled: false });
  });

  it("starts a new schedule with All destinations off, so it has to be chosen", async () => {
    routes["/api/v1/schedules"] = [];
    draw("schedules");
    fireEvent.click((await screen.findAllByRole("button", { name: en("auto.newSchedule") }))[0]);
    const dialog = await screen.findByRole("dialog");
    fireEvent.change(within(dialog).getByLabelText(en("auto.name")), { target: { value: "Late" } });
    const all = within(dialog).getByRole("checkbox", { name: en("auto.allDestinations") });
    expect(all.getAttribute("aria-checked") ?? (all as HTMLInputElement).checked.toString()).toMatch(/false/);
    expect(within(dialog).getByText(en("auto.blockDestinationsNone"))).toBeTruthy();
    expect(within(dialog).getByText(en("auto.destinationsStartHint"))).toBeTruthy();
  });
});

describe("A55: every event is a switch, on by default", () => {
  it("hides the event list while All events is on, and refuses an empty pick once it is off", async () => {
    draw();
    fireEvent.click((await screen.findAllByRole("button", { name: en("auto.newRuleButton") }))[0]);
    const dialog = await screen.findByRole("dialog");
    fireEvent.change(within(dialog).getByLabelText(en("auto.name")), { target: { value: "Pager" } });

    const allEvents = within(dialog).getByRole("switch", { name: en("auto.allEvents") });
    expect(allEvents.getAttribute("aria-checked")).toBe("true");
    expect(within(dialog).queryByRole("checkbox", { name: /disk/i })).toBeNull();

    fireEvent.click(allEvents);
    const create = within(dialog).getByRole("button", { name: en("auto.createRule") }) as HTMLButtonElement;
    expect(create.disabled).toBe(true);
    expect(within(dialog).getByText(en("auto.blockEventsNone"))).toBeTruthy();

    fireEvent.click(within(dialog).getByRole("checkbox", { name: en("auto.evDiskLow") }));
    expect(create.disabled).toBe(false);
    fireEvent.click(create);
    await waitFor(() => expect(seen.some((c) => c.method === "POST" && c.url.endsWith("/alerts/rules"))).toBe(true));
    expect(seen.find((c) => c.method === "POST" && c.url.endsWith("/alerts/rules"))?.body).toMatchObject({
      events: ["disk.low"],
    });
  });

  it("sends an empty list, the server's every-event, while All events is on", async () => {
    draw();
    fireEvent.click((await screen.findAllByRole("button", { name: en("auto.newRuleButton") }))[0]);
    const dialog = await screen.findByRole("dialog");
    fireEvent.change(within(dialog).getByLabelText(en("auto.name")), { target: { value: "Pager" } });
    fireEvent.click(within(dialog).getByRole("button", { name: en("auto.createRule") }));
    await waitFor(() => expect(seen.some((c) => c.method === "POST" && c.url.endsWith("/alerts/rules"))).toBe(true));
    expect(seen.find((c) => c.method === "POST" && c.url.endsWith("/alerts/rules"))?.body).toMatchObject({ events: [] });
  });
});

describe("A58: every select in the two dialogs has an accessible name", () => {
  it("names the rule dialog's selects by their labels", async () => {
    draw();
    fireEvent.click((await screen.findAllByRole("button", { name: en("auto.newRuleButton") }))[0]);
    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByRole("combobox", { name: en("auto.payloadFormat") })).toBeTruthy();
    expect(within(dialog).getByRole("combobox", { name: en("auto.minSeverity") })).toBeTruthy();
  });

  it("names the schedule dialog's selects by their labels", async () => {
    draw("schedules");
    fireEvent.click(within(await rowOf("Night stop")).getByRole("button", { name: en("auto.edit") }));
    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByRole("combobox", { name: en("auto.action") })).toBeTruthy();
    expect(within(dialog).getByRole("combobox", { name: en("auto.repeats") })).toBeTruthy();
  });

  it("uses the plain labels", () => {
    expect(en("auto.coalescingWindow")).toMatch(/^Combine repeat alerts/);
    expect(en("auto.lastSweep")).toBe("Recent runs");
    expect([en("auto.coalesced"), en("auto.pending"), en("auto.dropped"), en("auto.retries")]).toEqual([
      "Merged", "Waiting", "Discarded", "Retried",
    ]);
    // The Chat page's counters, same finding: "Deduplicated" and "Stored" said
    // nothing about what was counted.
    expect(en("chatpage.deduplicated")).toBe("Duplicates skipped");
    expect(en("chatpage.stored")).toBe("Saved to history");
  });
});

describe("empty lists use the kit's EmptyState, with the action that fills them", () => {
  it("offers New schedule from an empty schedule list", async () => {
    routes["/api/v1/schedules"] = [];
    draw("schedules");
    expect(await screen.findByText(en("auto.noSchedulesTitle"))).toBeTruthy();
    expect(screen.getAllByRole("button", { name: en("auto.newSchedule") })).toHaveLength(2);
    expect(screen.getByText(en("auto.noRunsTitle"))).toBeTruthy();
  });

  it("offers New rule from an empty rule list", async () => {
    routes["/api/v1/alerts/rules"] = [];
    draw();
    expect(await screen.findByText(en("auto.noRulesTitle"))).toBeTruthy();
    expect(screen.getAllByRole("button", { name: en("auto.newRuleButton") })).toHaveLength(2);
  });
});
