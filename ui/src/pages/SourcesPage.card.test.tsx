// @vitest-environment jsdom

import { afterEach, describe, expect, it } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";

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
