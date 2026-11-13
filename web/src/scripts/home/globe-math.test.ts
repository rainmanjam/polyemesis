import { describe, expect, it } from "vitest";
import { arcPoints, centreOf, latLonToVec3 } from "./globe-math";

const near = (a: number[], b: number[], eps: number) => a.every((v, i) => Math.abs(v - b[i]) < eps);
const len = (p: number[]) => Math.hypot(p[0], p[1], p[2]);

describe("latLonToVec3", () => {
  it("puts (0, 0) on +x", () => expect(near(latLonToVec3(0, 0, 1), [1, 0, 0], 1e-9)).toBe(true));
  it("puts the north pole on +y", () => expect(near(latLonToVec3(90, 0, 1), [0, 1, 0], 1e-9)).toBe(true));
  it("puts (0, 90 east) on -z", () => expect(near(latLonToVec3(0, 90, 1), [0, 0, -1], 1e-9)).toBe(true));
  it("scales by the radius", () => expect(len(latLonToVec3(40.71, -74, 2.5))).toBeCloseTo(2.5, 9));
});

describe("arcPoints", () => {
  const a: [number, number] = [53.48, -2.24];
  const b: [number, number] = [-23.55, -46.63];
  const pts = arcPoints(a, b, 1, 120, 0.12);

  it("returns segments + 1 points", () => expect(pts).toHaveLength(121));
  it("starts and ends on the sphere at the two places", () => {
    expect(len(pts[0])).toBeCloseTo(1, 9);
    expect(len(pts[120])).toBeCloseTo(1, 9);
    expect(near(pts[0], latLonToVec3(...a, 1), 1e-9)).toBe(true);
    expect(near(pts[120], latLonToVec3(...b, 1), 1e-9)).toBe(true);
  });
  it("lifts the middle by lift * r", () => expect(Math.abs(len(pts[60]) - 1.12)).toBeLessThan(1e-6));
  it("rises toward the middle and falls after it", () => {
    expect(len(pts[30])).toBeGreaterThan(1);
    expect(len(pts[30])).toBeLessThan(len(pts[60]));
    expect(len(pts[90])).toBeLessThan(len(pts[60]));
  });
  it("scales with the radius", () => expect(Math.abs(len(arcPoints(a, b, 3, 10, 0.2)[5]) - 3.6)).toBeLessThan(1e-6));
  it("does not produce NaN for two identical places", () => {
    for (const p of arcPoints(a, a, 1, 10, 0.1)) expect(p.every(Number.isFinite)).toBe(true);
  });
});

describe("centreOf", () => {
  it("is the place itself for one place", () => {
    const [lat, lon] = centreOf([[40, -70]]);
    expect(lat).toBeCloseTo(40, 9);
    expect(lon).toBeCloseTo(-70, 9);
  });
  it("is the middle of two places on the equator", () => {
    const [lat, lon] = centreOf([[0, 0], [0, 90]]);
    expect(lat).toBeCloseTo(0, 9);
    expect(lon).toBeCloseTo(45, 9);
  });
  it("does not jump across the date line", () => {
    const [, lon] = centreOf([[0, 170], [0, -170]]);
    expect(Math.abs(Math.abs(lon) - 180)).toBeLessThan(1e-9);
  });
  it("gives lat and lon that round-trip through latLonToVec3", () => {
    const [lat, lon] = centreOf([[12.5, -33.25]]);
    expect(near(latLonToVec3(lat, lon, 1), latLonToVec3(12.5, -33.25, 1), 1e-9)).toBe(true);
  });
});
