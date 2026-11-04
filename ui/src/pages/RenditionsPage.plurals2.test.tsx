// @vitest-environment jsdom

import { afterEach, describe, expect, it } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";

import { RenditionCard } from "./RenditionsPage";
import { setLanguage } from "@/lib/i18n";
import type { DestStatus, Rendition, RenditionView } from "@/lib/types";

/* The rendition card reads one as one. The catalogue has no plural rules, so a
 * count of exactly 1 has its own key: a one-core machine was told it "reports
 * 1 logical cores", and a Spanish card with one feed on said "1 de 2
 * activados". */

const noop = () => {};

const rendition = (over: Partial<Rendition> = {}): Rendition =>
  ({
    id: 1,
    name: "720p",
    width: 1280,
    height: 720,
    fps: 30,
    videoBitrate: 3000,
    encoder: "libx264",
    preset: "veryfast",
    gopSeconds: 2,
    deinterlace: "off",
    note: "",
    ...over,
  }) as Rendition;

const view = (over: Partial<RenditionView> = {}): RenditionView => ({
  rendition: rendition(),
  destinations: 0,
  enabledDestinations: 0,
  ...over,
});

afterEach(() => {
  cleanup();
  setLanguage("en");
});

/* 4K60 on a software encoder is past SOFTWARE_UNREALISTIC_PIXELS, the only
 * verdict the card itself prints, and its detail carries the core count. */
function drawCores(cores: number) {
  render(
    <RenditionCard
      view={view({ rendition: rendition({ width: 3840, height: 2160, fps: 60 }) })}
      live={undefined}
      users={[]}
      hardwareExists
      source={null}
      cores={cores}
      busy={false}
      onEdit={noop}
      onRestart={noop}
      onDelete={noop}
    />,
  );
}

const feed = (id: number, enabled: boolean) => ({ id, name: `Feed ${id}`, enabled }) as unknown as DestStatus;

function drawFeeds(users: DestStatus[]) {
  render(
    <RenditionCard
      view={view()}
      live={undefined}
      users={users}
      hardwareExists
      source={null}
      busy={false}
      onEdit={noop}
      onRestart={noop}
      onDelete={noop}
    />,
  );
}

describe("RenditionCard: the machine's core count", () => {
  it("says 1 logical core on a one-core machine", () => {
    drawCores(1);
    expect(screen.getByText(/ This machine reports 1 logical core\./)).toBeTruthy();
    expect(screen.queryByText(/This machine reports 1 logical cores/)).toBeNull();
  });

  it("says 2 logical cores on a two-core machine", () => {
    drawCores(2);
    expect(screen.getByText(/ This machine reports 2 logical cores\./)).toBeTruthy();
  });
});

describe("RenditionCard: enabled feeds, in Spanish", () => {
  it("says 1 de 2 activado with one feed on", () => {
    setLanguage("es");
    drawFeeds([feed(1, true), feed(2, false)]);
    expect(screen.getByText("1 de 2 activado")).toBeTruthy();
    expect(screen.queryByText("1 de 2 activados")).toBeNull();
  });

  it("says 2 de 3 activados with two feeds on", () => {
    setLanguage("es");
    drawFeeds([feed(1, true), feed(2, true), feed(3, false)]);
    expect(screen.getByText("2 de 3 activados")).toBeTruthy();
  });
});
