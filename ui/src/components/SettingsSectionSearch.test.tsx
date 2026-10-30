// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";

import { SettingsSectionSearch } from "./SettingsSectionSearch";

const labels = {
  ingest: "Ingest",
  pipeline: "Pipeline",
  platforms: "Platforms",
  security: "Security",
  debug: "Debug",
} as const;

afterEach(() => cleanup());

const box = () => screen.getByRole("combobox", { name: "Search settings" });
const type = (value: string) => fireEvent.change(box(), { target: { value } });

describe("SettingsSectionSearch", () => {
  it("offers the matching section and jumps to it when chosen", () => {
    const onPick = vi.fn();
    render(<SettingsSectionSearch labels={labels} onPick={onPick} />);
    type("secur");
    fireEvent.mouseDown(screen.getByRole("option", { name: /^Security/ }));
    expect(onPick).toHaveBeenCalledWith("security");
  });

  it("finds a field inside a section and names the card it sits in", () => {
    const onPick = vi.fn();
    render(<SettingsSectionSearch labels={labels} onPick={onPick} />);
    type("broker");
    const option = screen.getByRole("option", { name: /Broker URL/ });
    expect(option.textContent).toContain("Pipeline › MQTT");
    fireEvent.mouseDown(option);
    expect(onPick).toHaveBeenCalledWith("pipeline");
  });

  it("tells the four Enabled switches apart by their card", () => {
    render(<SettingsSectionSearch labels={labels} onPick={vi.fn()} />);
    type("enabled");
    const where = screen.getAllByRole("option").map((o) => o.textContent);
    expect(where.some((w) => w?.includes("Failover"))).toBe(true);
    expect(where.some((w) => w?.includes("MQTT"))).toBe(true);
  });

  it("moves through results with the arrow keys and opens one with Enter", () => {
    const onPick = vi.fn();
    render(<SettingsSectionSearch labels={labels} onPick={onPick} />);
    type("password");
    const options = screen.getAllByRole("option");
    expect(options.length).toBeGreaterThan(1);
    expect(options[0].getAttribute("aria-selected")).toBe("true");
    fireEvent.keyDown(box(), { key: "ArrowDown" });
    expect(screen.getAllByRole("option")[1].getAttribute("aria-selected")).toBe("true");
    fireEvent.keyDown(box(), { key: "Enter" });
    expect(onPick).toHaveBeenCalledTimes(1);
    expect((box() as HTMLInputElement).value).toBe("");
  });

  it("clears on Escape", () => {
    render(<SettingsSectionSearch labels={labels} onPick={vi.fn()} />);
    type("mqtt");
    expect(screen.getAllByRole("option").length).toBeGreaterThan(0);
    fireEvent.keyDown(box(), { key: "Escape" });
    expect((box() as HTMLInputElement).value).toBe("");
    expect(screen.queryByRole("option")).toBeNull();
  });

  it("focuses on / from the page, but not while typing in another field", () => {
    render(
      <>
        <input aria-label="other" />
        <SettingsSectionSearch labels={labels} onPick={vi.fn()} />
      </>,
    );
    const other = screen.getByRole("textbox", { name: "other" });
    other.focus();
    fireEvent.keyDown(other, { key: "/" });
    expect(document.activeElement).toBe(other);

    other.blur();
    fireEvent.keyDown(document.body, { key: "/" });
    expect(document.activeElement).toBe(box());
  });

  it("says plainly when nothing matches", () => {
    render(<SettingsSectionSearch labels={labels} onPick={vi.fn()} />);
    type("zzzz");
    expect(screen.getByRole("status").textContent).toBe("No settings match.");
    expect(screen.queryByRole("option")).toBeNull();
  });

  it("shows no results until something is typed", () => {
    render(<SettingsSectionSearch labels={labels} onPick={vi.fn()} />);
    expect(screen.queryByText("No settings match.")).toBeNull();
    expect(screen.queryByRole("option")).toBeNull();
    expect(box().getAttribute("aria-expanded")).toBe("false");
  });
});
