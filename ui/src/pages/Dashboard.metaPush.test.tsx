// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";

/* A METADATA PUSH WHOSE JOB THE SERVER NO LONGER HAS.
 *
 * Push jobs live in the server's memory (api/metadata.go). Restart the server
 * mid-push -- an upgrade, a crash, a container reschedule -- and GET
 * /metadata/push/{id} answers 404 "no such metadata push" for ever after. The
 * composer polled that every 1.2 s, swallowed each failure, and kept its Push
 * button disabled on "Pushing…" until the tab was reloaded: a push that looked
 * permanently in flight, and a composer that could not push again.
 *
 * And it parsed every body with a bare JSON.parse, so a reverse proxy's HTML
 * 502 page reached the operator as "Unexpected token '<'". */

const toastError = vi.fn();
vi.mock("sonner", async () => {
  const actual = await vi.importActual<typeof import("sonner")>("sonner");
  return { ...actual, toast: { ...actual.toast, error: (m: unknown) => toastError(m) } };
});

import { GoLiveComposer } from "./Dashboard";

const originalFetch = globalThis.fetch;

type Reply = { status: number; body: string };
const json = (v: unknown, status = 200): Reply => ({ status, body: JSON.stringify(v) });

const target = {
  accountId: 1,
  platform: "youtube",
  accountName: "Main channel",
  caps: { fields: ["title"] },
};
const pendingJob = {
  id: "job-1",
  done: false,
  results: [{ accountId: 1, platform: "youtube", accountName: "Main channel", state: "pending", applied: [] }],
  metadata: { title: "Tonight", description: "", category: "" },
};

function serve(routes: (path: string, method: string) => Reply) {
  globalThis.fetch = vi.fn(async (url: RequestInfo | URL, init?: RequestInit) => {
    const path = String(url).replace(/^\/api\/v1/, "");
    const r = routes(path, (init?.method ?? "GET").toUpperCase());
    return {
      ok: r.status < 400,
      status: r.status,
      headers: new Headers({ "Content-Type": r.body.startsWith("<") ? "text/html" : "application/json" }),
      text: async () => r.body,
    } as unknown as Response;
  }) as unknown as typeof fetch;
}

beforeEach(() => {
  vi.useFakeTimers({ shouldAdvanceTime: true });
});

afterEach(() => {
  cleanup();
  vi.useRealTimers();
  globalThis.fetch = originalFetch;
  toastError.mockReset();
});

describe("GoLiveComposer's push poll", () => {
  it("stops polling and frees the Push button once the server has lost the job", async () => {
    let polls = 0;
    serve((path) => {
      if (path === "/metadata") return json({ targets: [target], last: pendingJob });
      if (path === "/metadata/broadcast-window") return json({ accounts: [] });
      if (path === "/metadata/push/job-1") {
        polls++;
        return json({ error: "no such metadata push" }, 404);
      }
      throw new Error(`unexpected ${path}`);
    });

    render(<GoLiveComposer />);
    await screen.findByText("Pushing…");
    await vi.advanceTimersByTimeAsync(1300);
    await waitFor(() => expect(polls).toBe(1));

    // Terminal: the button is usable again and the rows say status was lost.
    await screen.findByRole("button", { name: /Push to platforms/ });
    expect(screen.getByText(/lost track of this push/i)).toBeTruthy();

    // And the poll does not keep asking.
    await vi.advanceTimersByTimeAsync(6000);
    expect(polls).toBe(1);
  });

  it("gives up after repeated failures that are not a 404", async () => {
    let polls = 0;
    serve((path) => {
      if (path === "/metadata") return json({ targets: [target], last: pendingJob });
      if (path === "/metadata/broadcast-window") return json({ accounts: [] });
      polls++;
      return { status: 502, body: "<html><body>Bad Gateway</body></html>" };
    });

    render(<GoLiveComposer />);
    await screen.findByText("Pushing…");
    await vi.advanceTimersByTimeAsync(1200 * 20);
    await screen.findByRole("button", { name: /Push to platforms/ });
    const after = polls;
    await vi.advanceTimersByTimeAsync(1200 * 10);
    expect(polls).toBe(after);
  });

  it("reports a proxy's HTML error page as a status, not a JSON parse error", async () => {
    serve((path, method) => {
      if (path === "/metadata") return json({ targets: [target] });
      if (path === "/metadata/broadcast-window") return json({ accounts: [] });
      if (path === "/metadata/push" && method === "POST") {
        return { status: 502, body: "<html><body>Bad Gateway</body></html>" };
      }
      throw new Error(`unexpected ${path}`);
    });

    render(<GoLiveComposer />);
    fireEvent.change(await screen.findByLabelText("Title"), { target: { value: "Tonight" } });
    fireEvent.click(screen.getByRole("button", { name: /Push to platforms/ }));
    await waitFor(() => expect(toastError).toHaveBeenCalled());
    const msg = String(toastError.mock.calls[0][0]);
    expect(msg).not.toMatch(/JSON|Unexpected token/);
    expect(msg).not.toContain("<html>");
    expect(msg).toContain("502");
  });
});
