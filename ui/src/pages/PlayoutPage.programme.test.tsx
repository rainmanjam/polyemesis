// @vitest-environment jsdom

import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";

import { ProgrammeCard } from "./PlayoutPage";
import { translate } from "@/lib/i18n";
import type { PlayoutSettings, SourceView } from "@/lib/types";
import { asSourceId } from "@/lib/types";

/* #774: PlayoutSettings.sourceId is read by playout.go:273 -- the public
 * route picks the engine named there -- and the UI's PlayoutSettings
 * interface omitted the field entirely. Nothing under ui/ could read it back
 * or write a new one, so a two-programme install had no way to say which
 * programme its one public page serves short of editing the database by
 * hand: the route served whichever engine happened to come up first.
 *
 * Hidden on a single-source install, matching every other source picker in
 * this tree (DestinationDialog, ProgrammeSwitcher): with one programme there
 * is nothing to choose, and a control that offers one answer is furniture. */

const play = (over: Partial<PlayoutSettings> = {}): PlayoutSettings =>
  ({
    enabled: true,
    public: false,
    allowCrossOrigin: false,
    format: "hls",
    segmentSeconds: 4,
    playlistSegments: 6,
    dvrWindowSeconds: 0,
    maxDiskMb: 1024,
    audioKbps: 128,
    sessionIdleSeconds: 30,
    maxSessions: 100,
    variants: [],
    ...over,
  }) as PlayoutSettings;

const sources = (n: number): SourceView[] =>
  Array.from({ length: n }, (_, i) => ({
    id: asSourceId(i + 1),
    name: i === 0 ? "Horizontal" : "Vertical",
    publishUrls: {},
    isDefault: i === 0,
  })) as unknown as SourceView[];

/* Radix's Select is built on pointer capture, which jsdom does not
 * implement. Without these the trigger never opens and the test would be
 * measuring jsdom rather than the picker -- the same setup DestinationDialog's
 * own source-picker test uses. */
beforeAll(() => {
  (window as unknown as { ResizeObserver?: unknown }).ResizeObserver ??= class {
    observe() {}
    unobserve() {}
    disconnect() {}
  };
  window.HTMLElement.prototype.hasPointerCapture = () => false;
  window.HTMLElement.prototype.releasePointerCapture = () => {};
  window.HTMLElement.prototype.setPointerCapture = () => {};
  window.HTMLElement.prototype.scrollIntoView = () => {};
});

afterEach(cleanup);

describe("ProgrammeCard", () => {
  it("renders nothing on a single-source install", () => {
    const onSave = vi.fn().mockResolvedValue(undefined);
    render(<ProgrammeCard play={play()} sources={sources(1)} busy={false} onSave={onSave} />);
    expect(screen.queryByLabelText(translate("en", "play.programme"))).toBeNull();
  });

  it("renders nothing with no sources at all", () => {
    const onSave = vi.fn().mockResolvedValue(undefined);
    render(<ProgrammeCard play={play()} sources={[]} busy={false} onSave={onSave} />);
    expect(screen.queryByLabelText(translate("en", "play.programme"))).toBeNull();
  });

  it("offers every programme on a multi-source install, plus the default", async () => {
    const onSave = vi.fn().mockResolvedValue(undefined);
    render(<ProgrammeCard play={play()} sources={sources(2)} busy={false} onSave={onSave} />);

    const trigger = screen.getByLabelText(translate("en", "play.programme"));
    fireEvent.keyDown(trigger, { key: "ArrowDown" });

    expect(await screen.findByRole("option", { name: "Horizontal" })).toBeTruthy();
    expect(screen.getByRole("option", { name: "Vertical" })).toBeTruthy();
    expect(
      screen.getByRole("option", { name: translate("en", "play.defaultProgramme") }),
    ).toBeTruthy();
  });

  it("saves the chosen source id", async () => {
    const onSave = vi.fn().mockResolvedValue(undefined);
    render(<ProgrammeCard play={play()} sources={sources(2)} busy={false} onSave={onSave} />);

    fireEvent.keyDown(screen.getByLabelText(translate("en", "play.programme")), {
      key: "ArrowDown",
    });
    fireEvent.click(await screen.findByRole("option", { name: "Vertical" }));

    expect(onSave).toHaveBeenCalledTimes(1);
    const next = onSave.mock.calls[0][0] as PlayoutSettings;
    expect(next.sourceId).toBe(2);
  });

  it("saves null for the default programme, not the sentinel string", async () => {
    const onSave = vi.fn().mockResolvedValue(undefined);
    render(
      <ProgrammeCard play={play({ sourceId: asSourceId(2) })} sources={sources(2)} busy={false} onSave={onSave} />,
    );

    fireEvent.keyDown(screen.getByLabelText(translate("en", "play.programme")), {
      key: "ArrowDown",
    });
    fireEvent.click(
      await screen.findByRole("option", { name: translate("en", "play.defaultProgramme") }),
    );

    expect(onSave).toHaveBeenCalledTimes(1);
    const next = onSave.mock.calls[0][0] as PlayoutSettings;
    expect(next.sourceId).toBeNull();
  });
});
