// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { translate } from "@/lib/i18n";

/* THE SYSTEM PAGE HAD NO MACHINE IN IT. (A74)
 *
 * It was titled "The machine itself" and showed an MCP line and the debug
 * recorder. An operator asking "is this box healthy?" found no version, no
 * ingest state and no uptime. The page now leads with those facts, from data
 * the console already reads, and the debug recorder sits under "Support tools".
 *
 * Disk and last restart are not on it: neither is on the wire today, and adding
 * an endpoint for them is out of this change. */

const state = vi.hoisted(() => ({
  status: {
    ingest: { name: "ingest", state: "running", pid: 1, uptimeSec: 3600, restarts: 0, lastError: "" },
  } as unknown,
}));

vi.mock("@/hooks/useLiveData", () => ({
  useLiveData: () => ({
    status: state.status,
    system: { cpuPercent: 12, memPercent: 40, numCpu: 8, memUsedBytes: 0, memTotalBytes: 0, procCpuPercent: 0, procMemBytes: 0 },
  }),
}));

vi.mock("@/lib/api", async () => {
  const actual = await vi.importActual<typeof import("@/lib/api")>("@/lib/api");
  return {
    ...actual,
    api: { ...actual.api, version: () => Promise.resolve({ version: "9.8.7" }) },
  };
});

vi.mock("@/components/DebugSettings", () => ({ DebugSettings: () => null }));
vi.mock("@/components/McpAccessCard", () => ({ McpStatusLine: () => null }));

const { SystemPage } = await import("./SystemPage");

afterEach(() => {
  cleanup();
});

describe("SystemPage: the machine's status first (A74)", () => {
  it("shows the running version", async () => {
    render(<MemoryRouter><SystemPage /></MemoryRouter>);

    expect(await screen.findByText("9.8.7")).toBeTruthy();
  });

  it("shows the ingest as a status pill in the kit's on-air tone", () => {
    render(<MemoryRouter><SystemPage /></MemoryRouter>);

    expect(document.querySelector('[data-tone="ok"]')).not.toBeNull();
  });

  it("groups the debug recorder under Support tools", () => {
    render(<MemoryRouter><SystemPage /></MemoryRouter>);

    expect(screen.getByText(translate("en", "system.supportTools"))).toBeTruthy();
  });
});
