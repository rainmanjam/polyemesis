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

describe("SettingsSectionSearch", () => {
  it("offers the matching section and jumps to it when chosen", () => {
    const onPick = vi.fn();
    render(<SettingsSectionSearch labels={labels} onPick={onPick} />);
    fireEvent.change(screen.getByRole("searchbox", { name: "Search settings" }), {
      target: { value: "secur" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Security" }));
    expect(onPick).toHaveBeenCalledWith("security");
  });

  it("says plainly when no section matches", () => {
    render(<SettingsSectionSearch labels={labels} onPick={vi.fn()} />);
    fireEvent.change(screen.getByRole("searchbox", { name: "Search settings" }), {
      target: { value: "zzzz" },
    });
    expect(screen.getByText("No settings match.")).toBeTruthy();
    expect(screen.queryByRole("button")).toBeNull();
  });

  it("shows no results until something is typed", () => {
    render(<SettingsSectionSearch labels={labels} onPick={vi.fn()} />);
    expect(screen.queryByText("No settings match.")).toBeNull();
    expect(screen.queryByRole("button")).toBeNull();
  });
});
