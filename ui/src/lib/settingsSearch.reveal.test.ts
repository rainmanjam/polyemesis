// @vitest-environment jsdom
import { afterEach, describe, expect, it } from "vitest";

import { revealSettingsHit } from "./settingsSearch";

afterEach(() => {
  document.body.innerHTML = "";
});

function panel(html: string) {
  document.body.innerHTML = `<div role="tabpanel">${html}</div>`;
}

describe("revealSettingsHit", () => {
  it("focuses the field when it is rendered", () => {
    panel(`<div data-slot="card-title">MQTT telemetry</div><div><label for="mq-url">Broker URL</label><input id="mq-url" /></div>`);
    expect(revealSettingsHit({ anchor: "mq-url", label: "Broker URL", context: "MQTT telemetry" })).toBe(true);
    expect(document.activeElement?.id).toBe("mq-url");
  });

  it("lands on the card when the field is hidden behind a switch that is off", () => {
    panel(`<div data-slot="card-title">MQTT telemetry</div><div><label for="mq-enabled">Enabled</label></div>`);
    expect(revealSettingsHit({ anchor: "mq-url", label: "Broker URL", context: "MQTT telemetry" })).toBe(true);
    const title = document.querySelector("[data-slot=card-title]")!;
    expect(title.hasAttribute("data-search-hit")).toBe(true);
  });

  it("reports nothing found while the panel has not mounted", () => {
    expect(revealSettingsHit({ anchor: "mq-url", label: "Broker URL", context: "MQTT telemetry" })).toBe(false);
  });
});
