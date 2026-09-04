// @vitest-environment jsdom

import { afterEach, describe, expect, it } from "vitest";
import { cleanup, render } from "@testing-library/react";
import { SourceCard } from "./SourcesPage";
import type { Settings, SourceView } from "@/lib/types";

/* THE ROW THAT LEAKED WAS THE ONE THE LABEL SAID WAS AN ADDRESS.
 *
 * The first masking pass keyed on `proto === "streamKey"`, the RTMP two-box
 * split, and was verified on an RTMP install where that is the whole story. In
 * SRT mode there is no split: publishURLs welds `passphrase=` and `streamid=`
 * into the srt row itself, so the "address" carried both credentials and went
 * to the plain <code> branch. The page it was reported against was fixed for
 * one ingest mode and untouched for the one the docs recommend.
 *
 * secret-fields.test.ts could not see it -- it matches identifier names, and
 * the expression here is `{url}`. So this tests the DATA PATH: feed the page
 * the map the server actually sends and read the DOM. It does not care what
 * the variable is called. */

afterEach(cleanup);

const TOKEN = "EXAMPLE-TOKEN-0000000000";
const PASS = "EXAMPLE-PASSPHRASE-000000";

const ingest = (mode: "srt" | "rtmp"): Settings["ingest"] => ({
  mode,
  srt: { passphrase: "", latencyMs: 200 },
  rtmp: { app: "live", streamKey: "" },
});

function draw(mode: "srt" | "rtmp", publishUrls: Record<string, string>) {
  const source = {
    id: 1,
    name: "Main",
    enabled: true,
    ingest: ingest(mode),
    token: TOKEN,
    position: 0,
    createdAt: "",
    updatedAt: "",
    publishUrls,
    isDefault: true,
    tokenEnforced: true,
    publishing: false,
    destinations: 0,
    renditions: 0,
    running: true,
  } as SourceView;
  const noop = () => {};
  return render(
    <SourceCard source={source} busy={false} onPatch={noop} onRotate={noop} onDelete={noop} />,
  );
}

describe("SourcesPage: credentials never reach the DOM unrevealed", () => {
  it("masks the SRT publish URL, which carries passphrase AND streamid", () => {
    const { container } = draw("srt", {
      srt: `srt://<server>:6000?latency=200&passphrase=${PASS}&streamid=${TOKEN}`,
    });
    const text = container.textContent ?? "";
    expect(text).not.toContain(PASS);
    expect(text).not.toContain(TOKEN);
  });

  it("masks streamid even when it is the only credential in the URL", () => {
    // tokenEnforced or not, the streamid names the source. Masked regardless.
    const { container } = draw("srt", { srt: `srt://<server>:6000?streamid=${TOKEN}` });
    expect(container.textContent ?? "").not.toContain(TOKEN);
  });

  it("masks the RTMP streamKey row and the TOKEN row", () => {
    const { container } = draw("rtmp", {
      rtmp: "rtmp://<server>:1935/live",
      streamKey: TOKEN,
    });
    expect(container.textContent ?? "").not.toContain(TOKEN);
  });

  it("leaves the plain RTMP address readable", () => {
    // The control. An address is not a secret, and masking it would train an
    // operator to press reveal on everything until the mask means nothing.
    const { container } = draw("rtmp", {
      rtmp: "rtmp://<server>:1935/live",
      streamKey: TOKEN,
    });
    expect(container.textContent ?? "").toContain("1935/live");
  });
});
