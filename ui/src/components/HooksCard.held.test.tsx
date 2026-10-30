// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";

import { HooksCard } from "./HooksCard";
import { api } from "@/lib/api";
import { translate } from "@/lib/i18n";
import type { Hook, HookMeta } from "@/lib/types";

/* destination.held is a subscribable webhook trigger. The picker lists what
 * the server offers, and a trigger with no label renders as its wire name --
 * so a new one reaches the screen whether or not anybody wrote the label. This
 * pins that it was written: the operator reads "Destination held", not
 * "destination.held". */

const list = vi.spyOn(api.hooks, "list");
const meta = vi.spyOn(api.hooks, "meta");

beforeEach(() => {
  list.mockReset().mockResolvedValue([
    { id: 7, name: "CI", enabled: true, url: "https://ci.example.com/[redacted]", hasSecret: true,
      triggers: [], timeoutSeconds: 10, maxAttempts: 3, createdAt: "", updatedAt: "" } as unknown as Hook,
  ]);
  meta.mockReset().mockResolvedValue({
    specVersion: "1",
    headers: { signature: "X-Polyemesis-Signature" },
    triggers: ["destination.down", "destination.held"],
    bounds: { minTimeoutSeconds: 1, maxTimeoutSeconds: 30, minAttempts: 1, maxAttempts: 5 },
    stats: { sent: 0, failed: 0, endpoints: 0, queued: 0, dropped: 0, retries: 0 },
  } as unknown as HookMeta);
});

afterEach(cleanup);

describe("the webhook trigger picker", () => {
  it("names destination.held in words, not its wire name", async () => {
    render(<HooksCard />);
    fireEvent.click(await screen.findByRole("button", { name: translate("en", "common.edit") }));
    expect(await screen.findByText("Destination held")).toBeTruthy();
    expect(screen.queryByText("destination.held")).toBeNull();
  });
});
