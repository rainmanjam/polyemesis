// @vitest-environment jsdom

import { afterEach, describe, expect, it } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";

import { SourceCard } from "./SourcesPage";
import type { Settings, SourceView } from "@/lib/types";

/* Two claims this card makes about a source, and used not to.
 *
 * jsdom rather than the static renderer: the card holds a local draft in state
 * and the select is a Radix trigger, neither of which the static renderer runs.
 */

const ingest = (over: Partial<Settings["ingest"]> = {}): Settings["ingest"] => ({
  mode: "srt",
  srt: { passphrase: "", latencyMs: 200 },
  rtmp: { app: "live", streamKey: "" },
  ...over,
});

const source = (over: Partial<SourceView> = {}): SourceView =>
  ({
    id: 1,
    name: "Main",
    enabled: true,
    ingest: ingest(),
    token: "tok",
    position: 0,
    createdAt: "",
    updatedAt: "",
    publishUrls: {},
    isDefault: true,
    tokenEnforced: true,
    publishing: false,
    destinations: 0,
    renditions: 0,
    running: true,
    ...over,
  }) as SourceView;

const noop = () => {};

function draw(over: Partial<SourceView>) {
  return render(
    <SourceCard
      source={source(over)}
      busy={false}
      onPatch={noop}
      onRotate={noop}
      onDelete={noop}
    />,
  );
}

afterEach(cleanup);

describe("SourceCard, on a disabled source", () => {
  it("says publishes are refused, beside the running badge", () => {
    // `running` is Engine(id) != nil, and manager.go builds an engine for every
    // row whether or not it is enabled — so a disabled source kept a saturated
    // green "running" badge, its publish URLs and its "the token is enforced"
    // line while the SRT listener answered REJ_CLOSE. `enabled` appeared exactly
    // twice in the file, both times on the switch.
    draw({ enabled: false, running: true });
    expect(screen.getByText("Disabled — publishes are refused")).toBeTruthy();
    // And beside, not instead of: both facts are true at once.
    expect(screen.getByText("running")).toBeTruthy();
  });

  it("says nothing of the kind about an enabled source", () => {
    const { container } = draw({ enabled: true, running: true });
    expect(container.querySelector("[data-testid='source-disabled']")).toBeNull();
  });
});

describe("SourceCard, while busy", () => {
  it("disables Delete along with the rest of the card (#18)", () => {
    // Rotate has always disabled itself under `busy`; Delete did not, because
    // SourcesPage.remove() never set busyId in the first place. This is the
    // card-level half of that fix -- see SourcesPage.delete.test.tsx for the
    // page-level half, which pins that remove() actually sets it.
    //
    // jest-dom is not installed (see DebugSettings.test.tsx), so the disabled
    // state is read off the element directly rather than through a matcher.
    render(
      <SourceCard source={source()} busy onPatch={noop} onRotate={noop} onDelete={noop} />,
    );
    expect((screen.getByLabelText("Delete Main") as HTMLButtonElement).disabled).toBe(true);
  });

  it("leaves Delete enabled when the card is idle", () => {
    render(
      <SourceCard source={source()} busy={false} onPatch={noop} onRotate={noop} onDelete={noop} />,
    );
    expect((screen.getByLabelText("Delete Main") as HTMLButtonElement).disabled).toBe(false);
  });
});

describe("SourceCard, on a pull ingest", () => {
  it("renders the URL field the mode needs", () => {
    // The select has always offered Pull; only the srt and rtmp branches drew
    // anything. db/settings.go rejects the save with "pull url is required",
    // naming a field that was not on the page — and a source that already had
    // one stored started dialling a URL nobody was ever shown.
    draw({
      ingest: ingest({
        mode: "pull",
        pull: { url: "srt://camera:9000", reconnectDelayMaxSeconds: 30, rtspTransport: "tcp" },
      }),
    });
    const url = screen.getByLabelText("Source URL") as HTMLInputElement;
    expect(url.value).toBe("srt://camera:9000");
  });

  it("does not draw the pull fields for an SRT ingest", () => {
    draw({ ingest: ingest({ mode: "srt" }) });
    expect(screen.queryByLabelText("Source URL")).toBeNull();
  });
});

describe("SourceCard: Add destination stays on this programme (A30)", () => {
  it("calls back with the card's own source instead of linking to Dashboard", () => {
    // A30. The receiving card's button used to be a link to "/", so the operator
    // landed on Dashboard with the source they had been looking at forgotten.
    // It is now a button that opens the dialog on this programme.
    let asked = 0;
    const source = {
      id: 7,
      name: "Studio B",
      enabled: true,
      ingest: { mode: "srt", srt: { passphrase: "", latencyMs: 200 }, rtmp: { app: "live", streamKey: "" } },
      token: "EXAMPLE-TOKEN-0000000000",
      position: 1,
      createdAt: "",
      updatedAt: "",
      publishUrls: {},
      isDefault: false,
      tokenEnforced: true,
      publishing: true,
      running: true,
      destinations: 0,
      renditions: 0,
    } as unknown as SourceView;
    render(
      <SourceCard
        source={source}
        busy={false}
        onPatch={() => {}}
        onRotate={() => {}}
        onDelete={() => {}}
        onAddDestination={() => {
          asked += 1;
        }}
      />,
    );
    const add = screen.getByRole("button", { name: "Add destination" });
    expect(add.tagName).toBe("BUTTON");
    fireEvent.click(add);
    expect(asked).toBe(1);
  });
});

describe("SourceCard: the enable switch says what it does (A33)", () => {
  // A33. The switch had only an aria-label, so a sighted operator saw a bare
  // toggle with no word for its state and no line for its consequence.
  const card = (enabled: boolean) =>
    ({
      id: 8,
      name: "Main",
      enabled,
      ingest: { mode: "srt", srt: { passphrase: "", latencyMs: 200 }, rtmp: { app: "live", streamKey: "" } },
      token: "EXAMPLE-TOKEN-0000000000",
      position: 0,
      createdAt: "",
      updatedAt: "",
      publishUrls: {},
      isDefault: true,
      tokenEnforced: true,
      publishing: false,
      running: true,
      destinations: 0,
      renditions: 0,
    }) as unknown as SourceView;

  it("states Accepting and what that means, while the source is on", () => {
    render(<SourceCard source={card(true)} busy={false} onPatch={() => {}} onRotate={() => {}} onDelete={() => {}} />);
    expect(screen.getByText("Accepting")).toBeTruthy();
    expect(screen.getByText("Encoders can connect to this source.")).toBeTruthy();
  });

  it("says what a switched-off source does to an encoder, in words", () => {
    render(<SourceCard source={card(false)} busy={false} onPatch={() => {}} onRotate={() => {}} onDelete={() => {}} />);
    expect(screen.getByText("Encoders that connect here are refused until this is on.")).toBeTruthy();
  });
});

