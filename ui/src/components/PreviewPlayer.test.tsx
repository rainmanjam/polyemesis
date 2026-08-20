// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";

import { PreviewPlayer } from "./PreviewPlayer";

vi.mock("hls.js", () => ({
  default: class {
    static isSupported() {
      return false;
    }
    destroy() {}
  },
}));

/* THE PICTURE MUST NOT OUTLIVE THE STREAM.
 *
 * Reported: when the encoder stops, the preview keeps showing the last decoded
 * frame. Two independent causes, both fixed here and both asserted:
 *
 *   the overlay had NO background, so the parent's bg-black sat behind the
 *   <video> and a stale frame showed straight through it
 *
 *   nothing told the component the stream had ended -- hls.js stalls at the
 *   live edge without a fatal error, so its own `playing` never went false
 *
 * And the case that makes the second one subtle: during a failover something
 * real IS being broadcast (a slate, a backup), and blanking the tile would hide
 * the very thing going out to the platforms.
 */
describe("PreviewPlayer", () => {
  afterEach(cleanup);

  it("covers the picture opaquely when nothing is on air", () => {
    render(<PreviewPlayer active outputLive={false} />);
    const overlay = screen.getByText("Ingest offline").parentElement!;
    expect(
      overlay.className,
      "a transparent overlay lets the last decoded frame of a finished stream " +
        "show through, which is the reported bug",
    ).toContain("bg-black");
    expect(screen.getByText(/Start your encoder/)).toBeTruthy();
  });

  it("does NOT blank the tile when a slate is on air", () => {
    // outputLive true, ingestLive false: the encoder is gone and the slate is
    // being broadcast. Hiding it would blind the operator at the one moment a
    // preview is worth most.
    render(
      <PreviewPlayer active outputLive ingestLive={false} onAir="slate" />,
    );
    expect(screen.queryByText("Ingest offline")).toBeNull();
  });

  it("labels a stand-in without covering it", () => {
    render(
      <PreviewPlayer active outputLive ingestLive={false} onAir="slate" />,
    );
    expect(screen.getByText("Input offline")).toBeTruthy();
    expect(screen.getByText(/showing slate/)).toBeTruthy();
  });

  it("says nothing about the input when the operator's own encoder is on air", () => {
    render(<PreviewPlayer active outputLive ingestLive onAir="primary" />);
    expect(screen.queryByText("Input offline")).toBeNull();
  });

  it("draws in the source's own shape once measured", () => {
    const { container } = render(
      <PreviewPlayer
        active
        outputLive
        aspect={{ width: 1080, height: 1920 }}
      />,
    );
    const box = container.firstElementChild as HTMLElement;
    expect(
      box.style.aspectRatio,
      "a vertical source letterboxed into 16:9",
    ).toBe("1080 / 1920");
    expect(box.className).not.toContain("aspect-video");
  });

  it("falls back to 16:9 while the geometry is unknown", () => {
    const { container } = render(<PreviewPlayer active />);
    expect((container.firstElementChild as HTMLElement).className).toContain(
      "aspect-video",
    );
  });

  it("behaves as before for a caller that supplies no telemetry", () => {
    // outputLive undefined must not read as "nothing on air", or every existing
    // mount would blank itself.
    render(<PreviewPlayer active />);
    expect(screen.getByText("Waiting for a stream…")).toBeTruthy();
    expect(screen.queryByText("Ingest offline")).toBeNull();
  });
});
