// @vitest-environment jsdom

import { afterEach, describe, expect, it } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";

import { SourceCard } from "./SourcesPage";
import type { Settings, SourceView } from "@/lib/types";

/* The first-run moment, on the card. An operator who has just created a source
 * needs three things from it: what the ingest IS (a value, not a blank select),
 * whether an encoder has connected yet, and what to do next. Before this, the
 * select rendered empty and nothing said "waiting" or "receiving" at all.
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
    name: "Studio A",
    enabled: true,
    ingest: ingest(),
    token: "tok",
    position: 0,
    createdAt: "",
    updatedAt: "",
    publishUrls: { srt: "srt://<server>:40557?streamid=studio-a" },
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
    <SourceCard source={source(over)} busy={false} onPatch={noop} onRotate={noop} onDelete={noop} />,
  );
}

afterEach(cleanup);

describe("SourceCard ingest value", () => {
  it("shows the protocol and port as the select's value", () => {
    draw({});
    // Two spans with a flex gap, so the protocol and the port are asserted
    // separately rather than as one string with a space in it.
    const value = screen.getByTestId("ingest-mode").textContent ?? "";
    expect(value).toContain("SRT");
    expect(value).toContain(":40557");
  });

  it("shows a placeholder, not a blank control, while the protocol is unset", () => {
    draw({ ingest: ingest({ mode: undefined as never }) });
    expect(screen.getByTestId("ingest-mode").textContent).toContain("Choose protocol");
  });
});

describe("SourceCard ingest state", () => {
  it("says it is waiting for the encoder while nothing is connected", () => {
    draw({ publishing: false });
    expect(screen.getByRole("status").textContent).toContain("Waiting for encoder");
  });

  it("flips to signal detected once an encoder is publishing to this source", () => {
    draw({ publishing: true });
    expect(screen.getByRole("status").textContent).toContain("Signal detected");
    expect(screen.queryByText("Waiting for encoder")).toBeNull();
  });

  it("says nothing about a disabled source's encoder", () => {
    draw({ enabled: false, publishing: false });
    expect(screen.queryByText("Waiting for encoder")).toBeNull();
  });
});
