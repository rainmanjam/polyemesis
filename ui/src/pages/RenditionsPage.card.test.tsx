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
    expect(screen.getByTestId("restart-reason").textContent).toContain("Nothing to restart");
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
