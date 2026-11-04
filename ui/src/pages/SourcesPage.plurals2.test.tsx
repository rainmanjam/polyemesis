// @vitest-environment jsdom

import { afterEach, describe, expect, it } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";

import { SourceCard } from "./SourcesPage";
import type { Settings, SourceView } from "@/lib/types";

/* The SRTLA bonding panel names how many network links carry the stream. A
 * group can be down to one modem, and the catalogue has no plural rules, so
 * count 1 has its own key: the panel used to read "Bonded over 1 links". */

afterEach(cleanup);

const ingest: Settings["ingest"] = {
  mode: "srt",
  srt: { passphrase: "", latencyMs: 200 },
  rtmp: { app: "live", streamKey: "" },
} as Settings["ingest"];

const link = (n: number) => ({
  peer: `10.0.0.${n}:5000`,
  since: "",
  lastSeen: "",
  packets: 100,
  bytes: 1000,
  bitrateKbps: 2000,
  share: 0.5,
});

function draw(links: number) {
  const source = {
    id: 1,
    name: "Main",
    enabled: true,
    ingest,
    token: "tok",
    position: 0,
    createdAt: "",
    updatedAt: "",
    publishUrls: {},
    isDefault: true,
    tokenEnforced: true,
    publishing: true,
    destinations: 0,
    renditions: 0,
    running: true,
    bonded: { upstreamLocal: "", since: "", links: Array.from({ length: links }, (_, i) => link(i + 1)) },
  } as unknown as SourceView;
  render(<SourceCard source={source} busy={false} onPatch={() => {}} onRotate={() => {}} onDelete={() => {}} />);
}

describe("SourceCard: the bonding panel reads one link as one", () => {
  it("says Bonded over 1 link", () => {
    draw(1);
    expect(screen.getByText("Bonded over 1 link")).toBeTruthy();
    expect(screen.queryByText("Bonded over 1 links")).toBeNull();
  });

  it("says Bonded over 2 links", () => {
    draw(2);
    expect(screen.getByText("Bonded over 2 links")).toBeTruthy();
  });
});
