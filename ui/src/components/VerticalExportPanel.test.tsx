// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { createRef } from "react";

vi.mock("@/lib/autoApi", () => ({
  autoApi: { post: vi.fn(() => Promise.resolve({})), del: vi.fn(() => Promise.resolve({})) },
}));

import { VerticalExportPanel } from "./VerticalExportPanel";
import { defaultVerticalSpec, type ClipTemplatesView, type VerticalSpec } from "@/lib/vertical";

afterEach(cleanup);

const OPTIONS: ClipTemplatesView = {
  templates: [{ id: 3, name: "Face top", updatedAt: "", vertical: { layout: "stacked", face: { x: 0.1, y: 0.1, w: 0.2, h: 0.2 }, faceShare: 0.3 } }],
  layouts: ["center", "stacked", "blur"],
  fonts: ["Inter-Bold.ttf", "Inter-Regular.ttf"],
  drawText: false,
  maxShortSeconds: 60,
  defaultStyle: { font: "Inter-Bold.ttf", sizePct: 4.2 },
};

function renderPanel(spec: VerticalSpec, durationMs: number, onChange = vi.fn(), onEnabled = vi.fn()) {
  render(
    <VerticalExportPanel
      enabled
      onEnabledChange={onEnabled}
      spec={spec}
      onChange={onChange}
      options={OPTIONS}
      onTemplatesChanged={() => undefined}
      hasTranscript
      durationMs={durationMs}
      videoRef={createRef<HTMLVideoElement>()}
    />,
  );
  return { onChange, onEnabled };
}

describe("VerticalExportPanel", () => {
  // The 60 s guard is a warning in the editor too: shown past 60.000 s, not at it.
  it("warns past sixty seconds and not at sixty", () => {
    renderPanel(defaultVerticalSpec(), 60_000);
    expect(screen.queryByText(/60 s or less/)).toBeNull();
    cleanup();
    renderPanel(defaultVerticalSpec(), 60_001);
    expect(screen.getByText(/60 s or less/)).toBeTruthy();
  });

  it("nudges the face box with the arrow keys", () => {
    const spec = { ...defaultVerticalSpec(), layout: "stacked" as const };
    const { onChange } = renderPanel(spec, 30_000);
    fireEvent.keyDown(screen.getByTestId("face-box"), { key: "ArrowRight" });
    const next = onChange.mock.calls[0][0] as VerticalSpec;
    expect(next.face.x).toBeCloseTo(spec.face.x + 0.01, 6);
    expect(next.face.y).toBe(spec.face.y);
  });

  it("applies a saved template and switches the short on", () => {
    const { onChange, onEnabled } = renderPanel(defaultVerticalSpec(), 30_000);
    fireEvent.click(screen.getByRole("button", { name: /^Apply a template: Face top$/ }));
    const next = onChange.mock.calls[0][0] as VerticalSpec;
    expect(next.layout).toBe("stacked");
    expect(next.faceShare).toBe(0.3);
    expect(onEnabled).toHaveBeenCalledWith(true);
  });

  it("says when this FFmpeg cannot draw captions", () => {
    const spec = { ...defaultVerticalSpec(), captions: { enabled: true, style: {} } };
    renderPanel(spec, 30_000);
    expect(screen.getByRole("status").textContent).toMatch(/cannot draw text/);
  });
});
