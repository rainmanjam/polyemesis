// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router";

import { McpAccessCard, McpStatusLine } from "./McpAccessCard";
import { api } from "@/lib/api";
import type { McpAccess, McpLevel } from "@/lib/types";

/* The MCP card is a view of server-enforced settings: the switch, three
 * levels, a warning beside each level that lets viewers' text reach an
 * assistant that can act, and the one status line the System page repeats. */

const access = (enabled: boolean, levels: McpLevel[]): McpAccess => ({
  enabled,
  levels: enabled ? levels : [],
  allLevels: ["read", "content", "control"],
  tools: {},
  endpoint: "/mcp",
});

const get = vi.spyOn(api, "mcpAccess");
const set = vi.spyOn(api, "setMcpAccess");

beforeEach(() => {
  get.mockReset();
  set.mockReset();
});
afterEach(cleanup);

const draw = (node: React.ReactNode) => render(<MemoryRouter>{node}</MemoryRouter>);

describe("McpAccessCard", () => {
  it("is off, with every level disabled, until the switch is turned on", async () => {
    get.mockResolvedValue(access(false, []));
    draw(<McpAccessCard />);
    expect(await screen.findByText("MCP: off")).toBeTruthy();
    for (const name of ["Read", "Content", "Control"]) {
      expect((screen.getByRole("checkbox", { name }) as HTMLButtonElement).disabled).toBe(true);
    }
  });

  it("turns MCP on without naming levels, so the server's first-enable default (Read) applies", async () => {
    get.mockResolvedValue(access(false, []));
    set.mockResolvedValue(access(true, ["read"]));
    draw(<McpAccessCard />);
    fireEvent.click(await screen.findByRole("switch", { name: "Allow MCP connections" }));
    await waitFor(() => expect(set).toHaveBeenCalledWith(true, undefined));
    expect(await screen.findByText("MCP: on (Read)")).toBeTruthy();
  });

  it("adds a level in the server's order and says so in the status line", async () => {
    get.mockResolvedValue(access(true, ["read"]));
    set.mockResolvedValue(access(true, ["read", "control"]));
    draw(<McpAccessCard />);
    fireEvent.click(await screen.findByRole("checkbox", { name: "Control" }));
    await waitFor(() => expect(set).toHaveBeenCalledWith(true, ["read", "control"]));
    expect(await screen.findByText("MCP: on (Read + Control)")).toBeTruthy();
  });

  it("warns about prompt injection on Content and Control, not on Read, and links to an MCP token", async () => {
    get.mockResolvedValue(access(true, ["read"]));
    draw(<McpAccessCard />);
    expect(await screen.findByText(/prompt injection/)).toBeTruthy();
    expect(screen.getByText(/can end a live broadcast/)).toBeTruthy();
    const link = screen.getByRole("link", { name: "Create an MCP token" });
    expect(link.getAttribute("href")).toBe("/settings?tab=security");
  });
});

describe("McpStatusLine", () => {
  it("repeats the status on the System page and links to the card", async () => {
    get.mockResolvedValue(access(true, ["read", "content"]));
    draw(<McpStatusLine />);
    const line = await screen.findByTestId("mcp-status-line");
    expect(line.textContent).toBe("MCP: on (Read + Content)");
    expect(line.getAttribute("href")).toBe("/automation?tab=mcp");
  });
});
