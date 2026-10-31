// @vitest-environment jsdom

import { afterEach, describe, expect, it } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";

import { RenditionCard, RenditionDialog } from "./RenditionsPage";
import type { Rendition, RenditionBounds, RenditionView } from "@/lib/types";

/* Two explanations this page computed correctly and then put somewhere the
 * operator could not reach. */

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

const bounds: RenditionBounds = {
  minDimension: 128,
  maxDimension: 7680,
  maxFps: 240,
  minBitrate: 100,
  maxBitrate: 100_000,
  minGopSeconds: 1,
  maxGopSeconds: 10,
};

afterEach(cleanup);

describe("RenditionCard", () => {
  it("names each feed's state in words, not only in a dot", () => {
    // A34. The chips were a coloured dot with the state in a hover title, which
    // touch users never see and which a colour-blind operator cannot read.
    render(
      <RenditionCard
        view={view({ destinations: 1, enabledDestinations: 0 })}
        live={undefined}
        users={[{ id: 1, name: "Twitch", enabled: false } as never]}
        hardwareExists
        source={null}
        busy={false}
        onEdit={noop}
        onRestart={noop}
        onDelete={noop}
      />,
    );
    const chip = screen.getByText("Twitch").parentElement as HTMLElement;
    // Off, the word the destination card uses for a switched-off feed (A91).
    expect(chip.textContent).toContain("Off");
    // And the rendition nothing enabled uses reads Stopped, not "Idle".
    expect(screen.queryByText("Idle")).toBeNull();
    expect(screen.getAllByText("Stopped").length).toBeGreaterThan(0);
  });

  it("labels Delete with words, not an icon alone", () => {
    // A33. Delete was an icon beside Restart with only an aria-label, so a
    // sighted operator had to know the trash can meant the rendition.
    render(
      <RenditionCard
        view={view()}
        live={undefined}
        users={[]}
        hardwareExists
        source={null}
        busy={false}
        onEdit={noop}
        onRestart={noop}
        onDelete={noop}
      />,
    );
    expect(screen.getByRole("button", { name: "Delete" })).toBeTruthy();
  });

  it("says Not encoding, not Starting, when a feed wants this rendition and no encode exists", () => {
    // A10. renditionSignal used to return "Starting" whenever a destination was
    // enabled and no process existed, so an idle rendition read as a stuck
    // encoder with no explanation. Nothing has been requested from the encoder
    // until a feed goes live, so the honest reading is "Not encoding" and the
    // sentence says what will start it.
    render(
      <RenditionCard
        view={view({ enabledDestinations: 1 })}
        live={undefined}
        users={[{ id: 1, name: "YouTube", enabled: true } as never]}
        hardwareExists
        source={null}
        busy={false}
        onEdit={noop}
        onRestart={noop}
        onDelete={noop}
      />,
    );
    expect(screen.queryByText("Starting")).toBeNull();
    expect(screen.getByText("Not encoding · starts when the first feed goes live")).toBeTruthy();
  });

  it("puts the reason for a greyed Restart button in the card", () => {
    // button.tsx sets `disabled:pointer-events-none`, so the title on a
    // disabled button never reaches a pointer and never reaches a screen
    // reader — the whole explanation lived somewhere unreachable.
    render(
      <RenditionCard
        view={view()}
        live={undefined}
        users={[]}
        hardwareExists
        source={null}
        busy={false}
        onEdit={noop}
        onRestart={noop}
        onDelete={noop}
      />,
    );
    const restart = screen.getByRole("button", { name: /Restart/ }) as HTMLButtonElement;
    expect(restart.disabled).toBe(true);
    // A28: the reason is visible text the disabled button points at through
    // aria-describedby, so a keyboard or screen-reader user hears it too. A
    // title on a disabled button reaches nobody (button.tsx pointer-events-none).
    const reasonId = restart.getAttribute("aria-describedby");
    expect(reasonId).toBeTruthy();
    expect(document.getElementById(reasonId as string)?.textContent).toContain("Nothing to restart");
  });

  it("says nothing of the kind while an encode is reported", () => {
    render(
      <RenditionCard
        view={view()}
        live={{ id: 1, name: "720p", consumers: 1, process: { state: "running" } } as never}
        users={[]}
        hardwareExists
        source={null}
        busy={false}
        onEdit={noop}
        onRestart={noop}
        onDelete={noop}
      />,
    );
    expect(screen.queryByTestId("restart-reason")).toBeNull();
  });
});

describe("RenditionDialog", () => {
  it("renders the deinterlace hint as a sentence, not as its translation key", () => {
    // `?.hint ?? ""` printed "rend.deintOffHint" under the control, eleven lines
    // below a SelectItem that wraps the same value in t().
    render(
      <RenditionDialog
        open
        onOpenChange={noop}
        rendition={rendition()}
        caps={null}
        redetecting={false}
        onRedetect={noop}
        presets={[]}
        fonts={null}
        disclaimer=""
        bounds={bounds}
        source={null}
        users={[]}
        counts={{ destinations: 0, enabledDestinations: 0 }}
        onSaved={noop}
      />,
    );
    expect(
      screen.getByText(/Right for the progressive sources almost everyone has\./),
    ).toBeTruthy();
    expect(screen.queryByText("rend.deintOffHint")).toBeNull();
  });
});
