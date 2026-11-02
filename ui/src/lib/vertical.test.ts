import { describe, expect, it } from "vitest";
import {
  DEFAULT_FACE_SHARE,
  boxFromDrag,
  faceAspect,
  lockFaceBox,
  nudgeBox,
  overShortLimit,
  verticalBody,
  type VerticalSpec,
} from "./vertical";

describe("the face-cam box", () => {
  // The face panel is 1080 wide and round(1920 * share) tall, even. A box the
  // operator draws on a 16:9 frame must have that panel's shape IN SOURCE
  // PIXELS, or the server's cover crop throws part of the face away.
  it("is locked to the face panel's aspect, in source pixels", () => {
    const share = DEFAULT_FACE_SHARE; // 0.4 -> 1080x768
    expect(faceAspect(share)).toBeCloseTo(1080 / 768, 6);
    const box = lockFaceBox({ x: 0.7, y: 0.05, w: 0.25, h: 0.9 }, 16 / 9, share);
    // w*16/9 / (h) == 1080/768  ->  h = 0.25 * (16/9) / (1080/768)
    expect(box.w).toBeCloseTo(0.25, 6);
    expect(box.h).toBeCloseTo((0.25 * 16) / 9 / (1080 / 768), 6);
    expect((box.w * (16 / 9)) / box.h).toBeCloseTo(faceAspect(share), 6);
  });

  it("stays inside the frame, shrinking rather than spilling", () => {
    const box = lockFaceBox({ x: 0.9, y: 0.9, w: 0.5, h: 0.5 }, 16 / 9, 0.4);
    expect(box.x + box.w).toBeLessThanOrEqual(1 + 1e-9);
    expect(box.y + box.h).toBeLessThanOrEqual(1 + 1e-9);
    expect((box.w * (16 / 9)) / box.h).toBeCloseTo(faceAspect(0.4), 6);
  });

  it("is drawn by dragging, from either corner", () => {
    const a = boxFromDrag({ x: 0.6, y: 0.1 }, { x: 0.9, y: 0.5 }, 16 / 9, 0.4);
    const b = boxFromDrag({ x: 0.9, y: 0.5 }, { x: 0.6, y: 0.1 }, 16 / 9, 0.4);
    expect(a.x).toBeCloseTo(0.6, 6);
    expect(b.x).toBeCloseTo(0.6, 6);
    expect(a.w).toBeCloseTo(0.3, 6);
    expect(b.w).toBeCloseTo(0.3, 6);
  });

  it("nudges by a percent and never leaves the frame", () => {
    const box = { x: 0.005, y: 0.5, w: 0.2, h: 0.2 };
    expect(nudgeBox(box, "ArrowLeft").x).toBe(0);
    expect(nudgeBox(box, "ArrowRight").x).toBeCloseTo(0.015, 6);
    expect(nudgeBox({ ...box, y: 0.79 }, "ArrowDown").y).toBeCloseTo(0.8, 6);
    expect(nudgeBox(box, "Enter")).toBe(box);
  });
});

describe("the short-length guard", () => {
  // 60 s is a short. A millisecond more is still exported, with a warning.
  it("warns past sixty seconds and not at sixty", () => {
    expect(overShortLimit(60_000, 60)).toBe(false);
    expect(overShortLimit(60_001, 60)).toBe(true);
    expect(overShortLimit(10_000, undefined)).toBe(false);
  });
});

describe("the vertical body", () => {
  it("sends only what the layout uses", () => {
    const spec: VerticalSpec = {
      layout: "center",
      focus: 0.3,
      face: { x: 0.7, y: 0.05, w: 0.25, h: 0.3 },
      faceShare: 0.4,
      faceBottom: true,
      captions: { enabled: false, style: {} },
    };
    expect(verticalBody(spec)).toEqual({ layout: "center", focus: 0.3 });
    expect(verticalBody({ ...spec, layout: "stacked" })).toEqual({
      layout: "stacked",
      face: { x: 0.7, y: 0.05, w: 0.25, h: 0.3 },
      faceShare: 0.4,
      faceBottom: true,
    });
    expect(verticalBody({ ...spec, layout: "blur", captions: { enabled: true, style: { uppercase: true } } })).toEqual({
      layout: "blur",
      captions: { enabled: true, style: { uppercase: true } },
    });
  });
});
