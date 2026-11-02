// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { createRef } from "react";

const api = vi.hoisted(() => ({
  post: vi.fn((..._a: unknown[]) => Promise.resolve({})),
  del: vi.fn((..._a: unknown[]) => Promise.resolve({})),
}));
vi.mock("@/lib/autoApi", () => ({ autoApi: api }));

import { VerticalExportPanel } from "./VerticalExportPanel";
import { defaultVerticalSpec, type ClipTemplatesView, type VerticalSpec } from "@/lib/vertical";

afterEach(() => {
  cleanup();
  api.post.mockClear();
  api.del.mockClear();
});

const OPTIONS: ClipTemplatesView = {
  templates: [{ id: 3, name: "Face top", updatedAt: "", vertical: { layout: "stacked", face: { x: 0.1, y: 0.1, w: 0.2, h: 0.2 }, faceShare: 0.3 } }],
  layouts: ["center", "stacked", "blur"],
  fonts: ["Inter-Bold.ttf", "Inter-Regular.ttf"],
  burnCaptions: false,
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

  it("says when this FFmpeg cannot burn captions", () => {
    const spec = { ...defaultVerticalSpec(), captions: { enabled: true, style: {} } };
    renderPanel(spec, 30_000);
    expect(screen.getByRole("status").textContent).toMatch(/libass/);
  });

  it("draws the face box by dragging on the frame, locked to the panel's shape", () => {
    const spec = { ...defaultVerticalSpec(), layout: "stacked" as const };
    const { onChange } = renderPanel(spec, 30_000);
    const picker = screen.getByTestId("face-picker");
    picker.getBoundingClientRect = () => ({ left: 0, top: 0, width: 1600, height: 900, right: 1600, bottom: 900, x: 0, y: 0, toJSON: () => ({}) });
    fireEvent.pointerDown(picker, { clientX: 960, clientY: 90, pointerId: 1 });
    fireEvent.pointerMove(picker, { clientX: 1440, clientY: 400, pointerId: 1 });
    fireEvent.pointerUp(picker, { pointerId: 1 });
    fireEvent.pointerMove(picker, { clientX: 100, clientY: 100, pointerId: 1 });
    expect(onChange).toHaveBeenCalledTimes(1);
    const face = (onChange.mock.calls[0][0] as VerticalSpec).face;
    expect(face.x).toBeCloseTo(0.6, 6);
    expect(face.w).toBeCloseTo(0.3, 6);
    expect((face.w * (16 / 9)) / face.h).toBeCloseTo(1080 / 768, 4);
  });

  it("grabs a still to draw on, falling back to the poster without a playing video", () => {
    const spec = { ...defaultVerticalSpec(), layout: "stacked" as const };
    render(
      <VerticalExportPanel enabled onEnabledChange={vi.fn()} spec={spec} onChange={vi.fn()} options={OPTIONS}
        onTemplatesChanged={() => undefined} hasTranscript durationMs={1000}
        videoRef={createRef<HTMLVideoElement>()} poster="/poster.jpg" />,
    );
    fireEvent.click(screen.getByRole("button", { name: /current frame/i }));
    expect(document.querySelector("img")?.getAttribute("src")).toBe("/poster.jpg");
  });

  it("changes the share, the face position and the crop pan", () => {
    const stacked = { ...defaultVerticalSpec(), layout: "stacked" as const };
    const a = renderPanel(stacked, 30_000);
    const [share] = screen.getAllByRole("slider");
    fireEvent.change(share, { target: { value: "30" } });
    expect((a.onChange.mock.calls[0][0] as VerticalSpec).faceShare).toBeCloseTo(0.3, 6);
    fireEvent.click(screen.getByRole("checkbox", { name: /bottom/i }));
    expect((a.onChange.mock.calls[1][0] as VerticalSpec).faceBottom).toBe(true);
    cleanup();
    const b = renderPanel(defaultVerticalSpec(), 30_000);
    fireEvent.change(screen.getByRole("slider"), { target: { value: "25" } });
    expect((b.onChange.mock.calls[0][0] as VerticalSpec).focus).toBeCloseTo(0.25, 6);
    fireEvent.click(screen.getByRole("button", { name: /blurred fit/i }));
    expect((b.onChange.mock.calls[1][0] as VerticalSpec).layout).toBe("blur");
  });

  it("sets the caption style", () => {
    const spec = { ...defaultVerticalSpec(), layout: "blur" as const, captions: { enabled: true, style: {} } };
    const { onChange } = renderPanel(spec, 30_000);
    const styleOf = (i: number) => (onChange.mock.calls[i][0] as VerticalSpec).captions.style;
    fireEvent.change(screen.getByRole("combobox"), { target: { value: "Inter-Regular.ttf" } });
    expect(styleOf(0).font).toBe("Inter-Regular.ttf");
    const [size, position] = screen.getAllByRole("slider");
    fireEvent.change(size, { target: { value: "5" } });
    expect(styleOf(1).sizePct).toBe(5);
    fireEvent.change(position, { target: { value: "60" } });
    expect(styleOf(2).positionPct).toBe(60);
    fireEvent.click(screen.getByRole("checkbox", { name: /upper case/i }));
    expect(styleOf(3).uppercase).toBe(true);
    fireEvent.click(screen.getByRole("checkbox", { name: /background box/i }));
    expect(styleOf(4).box).toBe(true);
    fireEvent.click(screen.getByRole("checkbox", { name: /burn in captions/i }));
    expect((onChange.mock.calls[5][0] as VerticalSpec).captions.enabled).toBe(false);
  });

  it("saves the current layout as a named template and deletes one", async () => {
    const changed = vi.fn();
    render(
      <VerticalExportPanel enabled onEnabledChange={vi.fn()} spec={{ ...defaultVerticalSpec(), layout: "blur" }}
        onChange={vi.fn()} options={OPTIONS} onTemplatesChanged={changed} hasTranscript durationMs={1000}
        videoRef={createRef<HTMLVideoElement>()} />,
    );
    fireEvent.change(screen.getByRole("textbox"), { target: { value: " Night show " } });
    fireEvent.click(screen.getByRole("button", { name: /save as a template/i }));
    await waitFor(() => expect(api.post).toHaveBeenCalledWith("/clipper/templates", { name: "Night show", vertical: { layout: "blur" } }));
    fireEvent.click(screen.getByRole("button", { name: /^Delete this template: Face top$/ }));
    await waitFor(() => expect(api.del).toHaveBeenCalledWith("/clipper/templates/3"));
    await waitFor(() => expect(changed).toHaveBeenCalledTimes(2));
  });

  it("reports a template that could not be saved", async () => {
    api.post.mockImplementationOnce(() => Promise.reject(new Error("there are already 100 templates")));
    render(
      <VerticalExportPanel enabled onEnabledChange={vi.fn()} spec={defaultVerticalSpec()} onChange={vi.fn()}
        options={OPTIONS} onTemplatesChanged={vi.fn()} hasTranscript durationMs={1000}
        videoRef={createRef<HTMLVideoElement>()} />,
    );
    fireEvent.change(screen.getByRole("textbox"), { target: { value: "x" } });
    fireEvent.click(screen.getByRole("button", { name: /save as a template/i }));
    await waitFor(() => expect(api.post).toHaveBeenCalled());
  });

  // A PORTRAIT FRAME IS NOT LETTERBOXED. The picker takes the frame's own shape
  // from the image, so where the operator drags is where the box is on the
  // source, and the box is locked to the panel using the SOURCE's aspect.
  //
  // MUTATION: the picker stays 16:9 and ignores the image's shape. Observed:
  // FAIL, the box's height is computed for a 16:9 source.
  it("takes the frame's shape from the image, so a portrait drag lands where it was drawn", () => {
    const spec = { ...defaultVerticalSpec(), layout: "stacked" as const };
    const onChange = vi.fn();
    render(
      <VerticalExportPanel enabled onEnabledChange={vi.fn()} spec={spec} onChange={onChange} options={OPTIONS}
        onTemplatesChanged={() => undefined} hasTranscript durationMs={1000}
        videoRef={createRef<HTMLVideoElement>()} poster="/portrait.jpg" />,
    );
    const img = document.querySelector("img") as HTMLImageElement;
    Object.defineProperty(img, "naturalWidth", { value: 1080 });
    Object.defineProperty(img, "naturalHeight", { value: 1920 });
    fireEvent.load(img);
    const picker = screen.getByTestId("face-picker");
    expect(parseFloat(picker.style.aspectRatio)).toBeCloseTo(1080 / 1920, 6);
    picker.getBoundingClientRect = () => ({ left: 0, top: 0, width: 450, height: 800, right: 450, bottom: 800, x: 0, y: 0, toJSON: () => ({}) });
    fireEvent.pointerDown(picker, { clientX: 270, clientY: 40, pointerId: 1 });
    fireEvent.pointerMove(picker, { clientX: 405, clientY: 400, pointerId: 1 });
    const face = (onChange.mock.calls[0][0] as VerticalSpec).face;
    expect(face.x).toBeCloseTo(0.6, 6);
    expect(face.w).toBeCloseTo(0.3, 6);
    expect(((face.w * 1080) / (face.h * 1920))).toBeCloseTo(1080 / 768, 4);
  });
});
