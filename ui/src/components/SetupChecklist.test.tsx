// @vitest-environment jsdom

/* THE FIRST-RUN CHECKLIST ON AN EMPTY DASHBOARD.
 *
 * The tour is one 7-step coach-mark pass, dismissed once and never shown again.
 * Afterwards nothing says which setup steps are still open, so an operator
 * cannot tell they are two of five from their first stream. This list reads
 * what the server reports now, and its gaps are the behaviour under test: a
 * step is never ticked on a guess, and Go live is never a dead button that
 * does not say why. */

import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { SetupChecklist } from "./SetupChecklist";
import { setupChecklist } from "@/lib/setupChecklist";
import { translate } from "@/lib/i18n";

afterEach(cleanup);

const T = (key: Parameters<typeof translate>[1], p?: Record<string, string | number>) =>
  translate("en", key, p);

function draw(
  input: Parameters<typeof setupChecklist>[0],
  over: Partial<{ onCopy: () => void; onAdd: () => void }> = {},
) {
  const checklist = setupChecklist(input);
  if (!checklist) throw new Error("test wants a checklist");
  return render(
    <MemoryRouter>
      <SetupChecklist
        checklist={checklist}
        ingestUrl="rtmp://192.168.1.20:1935/live/studio-a"
        onCopyIngest={over.onCopy ?? (() => {})}
        onAddDestination={over.onAdd ?? (() => {})}
      />
    </MemoryRouter>,
  );
}

describe("SetupChecklist", () => {
  it("says how many of the setup steps are done", () => {
    draw({ sourceCount: 1, signal: false, destinations: 0 });
    expect(screen.getByText(T("firstRun.progress", { done: 1, total: 5 }))).toBeTruthy();
  });

  it("makes the encoder step the one next action, and shows the ingest address there", () => {
    draw({ sourceCount: 1, signal: false, destinations: 0 });
    expect(screen.getByText("rtmp://192.168.1.20:1935/live/studio-a")).toBeTruthy();
    expect(screen.getByText(T("firstRun.badgeNext"))).toBeTruthy();
  });

  it("copies the ingest address from the encoder step", () => {
    const onCopy = vi.fn();
    draw({ sourceCount: 1, signal: false, destinations: 0 }, { onCopy });
    fireEvent.click(screen.getByRole("button", { name: T("dash.copyIngestUrl") }));
    expect(onCopy).toHaveBeenCalledOnce();
  });

  it("keeps Go live disabled and says exactly what is missing", () => {
    draw({ sourceCount: 1, signal: true, destinations: 0 });
    const go = screen.getByRole("button", { name: T("golive.trigger") });
    expect((go as HTMLButtonElement).disabled).toBe(true);
    expect(screen.getByText(T("firstRun.needDestination"))).toBeTruthy();
  });

  it("offers add-destination from the destination step", () => {
    const onAdd = vi.fn();
    draw({ sourceCount: 1, signal: true, destinations: 0 }, { onAdd });
    fireEvent.click(screen.getByRole("button", { name: T("firstRun.addDestination") }));
    expect(onAdd).toHaveBeenCalledOnce();
  });

  it("sends a fresh install to the sources page, and shows no address to publish to", () => {
    draw({ sourceCount: 0, signal: false, destinations: 0 });
    const link = screen.getByRole("link", { name: T("empty.goToSources") });
    expect(link.getAttribute("href")).toBe("/sources");
    expect(screen.queryByText("rtmp://192.168.1.20:1935/live/studio-a")).toBeNull();
  });

  it("offers the tour as a way to the detail, not as the only help", () => {
    draw({ sourceCount: 1, signal: false, destinations: 0 });
    expect(screen.getByRole("button", { name: T("firstRun.tour") })).toBeTruthy();
  });
});

describe("SetupChecklist: per-protocol addresses at step 2 (A1)", () => {
  it("offers a copy button for each protocol the source publishes", () => {
    const checklist = setupChecklist({ sourceCount: 1, signal: false, destinations: 0 })!;
    const onCopyAddress = vi.fn();
    render(
      <MemoryRouter>
        <SetupChecklist
          checklist={checklist}
          ingestUrl="rtmp://box:1935/live/a"
          onCopyIngest={() => {}}
          onAddDestination={() => {}}
          addresses={[
            { proto: "rtmp", url: "rtmp://box:1935/live" },
            { proto: "srt", url: "srt://box:9000?streamid=a" },
            { proto: "streamKey", url: "tok-123" },
          ]}
          onCopyAddress={onCopyAddress}
        />
      </MemoryRouter>,
    );
    fireEvent.click(screen.getByRole("button", { name: T("firstRun.copyAddress", { proto: "SRT" }) }));
    expect(onCopyAddress).toHaveBeenCalledWith({ proto: "srt", url: "srt://box:9000?streamid=a" });
    expect(screen.getByRole("button", { name: T("firstRun.copyAddress", { proto: "RTMP" }) })).toBeTruthy();
    // The key is copied, never printed.
    fireEvent.click(screen.getByRole("button", { name: T("firstRun.copyStreamKey") }));
    expect(onCopyAddress).toHaveBeenCalledWith({ proto: "streamKey", url: "tok-123" });
    expect(screen.queryByText("tok-123")).toBeNull();
  });
});
