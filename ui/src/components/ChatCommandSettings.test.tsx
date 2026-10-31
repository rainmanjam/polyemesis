// @vitest-environment jsdom

/* CHAT COMMANDS (#6) give every moderator of every connected channel a hand on
 * the broadcast, so the form's job is to make "on" a deliberate act and to say
 * which programme chat controls -- including one that no longer appears in the
 * programme list. */

import { afterEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";

const listSources = vi.fn();

vi.mock("@/lib/api", async () => {
  const actual = await vi.importActual<typeof import("@/lib/api")>("@/lib/api");
  return { ...actual, api: { listSources: () => listSources() } };
});

import { ChatCommandSettings } from "./ChatCommandSettings";
import type { ChatCommandSettings as Value } from "@/lib/types";

afterEach(() => {
  cleanup();
  listSources.mockReset();
});

const flush = () => act(async () => {});

describe("ChatCommandSettings", () => {
  it("is off until switched on, and switching keeps the chosen programme", async () => {
    listSources.mockResolvedValue([{ id: 3, name: "IRL" }]);
    const onChange = vi.fn<(v: Value) => void>();
    render(<ChatCommandSettings value={undefined} onChange={onChange} />);
    await flush();
    const sw = screen.getByRole("switch");
    expect(sw.getAttribute("aria-checked")).toBe("false");
    fireEvent.click(sw);
    expect(onChange).toHaveBeenLastCalledWith({ enabled: true });
  });

  it("shows the default programme when none is chosen", async () => {
    listSources.mockResolvedValue([{ id: 3, name: "IRL" }]);
    render(<ChatCommandSettings value={{ enabled: true }} onChange={() => {}} />);
    await flush();
    const trigger = screen.getByRole("combobox");
    expect(trigger.textContent).not.toBe("");
    expect(trigger.textContent).not.toContain("IRL");
  });

  it("names the chosen programme from the list", async () => {
    listSources.mockResolvedValue([{ id: 3, name: "IRL" }]);
    render(<ChatCommandSettings value={{ enabled: true, sourceId: 3 }} onChange={() => {}} />);
    await flush();
    expect(screen.getByRole("combobox").textContent).toContain("IRL");
  });

  it("still shows a stored programme the list does not hold, rather than a blank", async () => {
    listSources.mockRejectedValue(new Error("offline"));
    render(<ChatCommandSettings value={{ enabled: true, sourceId: 9 }} onChange={() => {}} />);
    await flush();
    expect(screen.getByRole("combobox").textContent).toContain("#9");
  });
});
