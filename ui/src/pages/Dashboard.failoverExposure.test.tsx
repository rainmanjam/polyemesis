// @vitest-environment jsdom

import { afterEach, describe, expect, it } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";

import { FailoverExposure } from "./Dashboard";
import { translate } from "@/lib/i18n";

/* app-council A85. The failover exposure note was a two-line paragraph above
 * the destinations on every visit with failover off: the lead, the whole
 * argument (restart, YouTube replay, the remux hop) and the link. It pushed the
 * cards down and read as a fault on a page where nothing was wrong. It is now
 * one line, the lead and the link to the setting, with the argument behind a
 * closed disclosure for whoever wants it. */

afterEach(cleanup);

const draw = () =>
  render(
    <MemoryRouter>
      <FailoverExposure notice={{ kind: "unprotected" }} />
    </MemoryRouter>,
  );

describe("FailoverExposure: one line plus a link, detail behind a disclosure", () => {
  it("keeps the lead and the link to the failover setting on the visible line", () => {
    draw();
    expect(screen.getByText(translate("en", "i18nSweep.noFailoverLead"))).toBeTruthy();
    const link = screen.getByRole("link");
    expect(link.getAttribute("href")).toBe("/settings?tab=pipeline#failover");
  });

  it("puts the long explanation inside a closed details element", () => {
    draw();
    const body = screen.getByText(translate("en", "i18nSweep.noFailoverBody"));
    const details = body.closest("details");
    expect(details, "the explanation is still part of the always-visible paragraph").not.toBeNull();
    expect(details?.open).toBe(false);
    expect(details?.querySelector("summary")?.textContent).toBe(translate("en", "dash.failoverWhy"));
    // The link is not hidden inside the disclosure: the fix stays one click away.
    expect(details?.contains(screen.getByRole("link"))).toBe(false);
  });

  it("renders nothing when failover is on", () => {
    const { container } = render(
      <MemoryRouter>
        <FailoverExposure notice={{ kind: "none" }} />
      </MemoryRouter>,
    );
    expect(container.textContent).toBe("");
  });
});
