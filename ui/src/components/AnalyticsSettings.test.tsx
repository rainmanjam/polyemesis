// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";

import { AnalyticsSettings } from "./AnalyticsSettings";
import { translate } from "@/lib/i18n";

afterEach(cleanup);

describe("AnalyticsSettings", () => {
  it("shows the defaults for a document that predates it, bounded by the Go validator", () => {
    render(<AnalyticsSettings value={undefined} onChange={() => {}} />);
    const interval = screen.getByLabelText(translate("en", "set.analyticsInterval")) as HTMLInputElement;
    expect(interval.value).toBe("60");
    expect(interval.min).toBe("30");
    expect(interval.max).toBe("900");
    expect((screen.getByLabelText(translate("en", "set.analyticsRetention")) as HTMLInputElement).value).toBe("90");
  });

  it("reports a changed interval and retention with the rest kept", () => {
    const onChange = vi.fn();
    render(<AnalyticsSettings value={{ enabled: false, intervalSeconds: 120, retentionDays: 30 }} onChange={onChange} />);
    fireEvent.change(screen.getByLabelText(translate("en", "set.analyticsInterval")), { target: { value: "300" } });
    expect(onChange).toHaveBeenLastCalledWith({ enabled: false, intervalSeconds: 300, retentionDays: 30 });
    fireEvent.change(screen.getByLabelText(translate("en", "set.analyticsRetention")), { target: { value: "7" } });
    expect(onChange).toHaveBeenLastCalledWith({ enabled: false, intervalSeconds: 120, retentionDays: 7 });
  });
});
