/* Geometry for the globe, with no three.js in it so it is cheap to test and
 * never pulls the renderer into a chunk of its own. Degrees in, unit-free
 * vectors out: +y is north, longitude 0 lies on +x, east runs toward -z. */

export type Vec3 = [number, number, number];

const DEG = Math.PI / 180;

export function latLonToVec3(lat: number, lon: number, r: number): Vec3 {
  const la = lat * DEG;
  const lo = lon * DEG;
  return [r * Math.cos(la) * Math.cos(lo), r * Math.sin(la), -r * Math.cos(la) * Math.sin(lo)];
}

/* `segments + 1` points along the great circle from a to b, at radius r, raised
 * by sin(pi t) so the middle sits at r * (1 + lift). With an even `segments`
 * the middle is a sample. */
export function arcPoints(a: [number, number], b: [number, number], r: number, segments: number, lift: number): Vec3[] {
  const va = latLonToVec3(a[0], a[1], 1);
  const vb = latLonToVec3(b[0], b[1], 1);
  const dot = Math.min(1, Math.max(-1, va[0] * vb[0] + va[1] * vb[1] + va[2] * vb[2]));
  const omega = Math.acos(dot);
  const so = Math.sin(omega);
  const out: Vec3[] = [];
  for (let i = 0; i <= segments; i++) {
    const t = i / segments;
    // Two places on top of each other have no great circle: stay put.
    const w0 = so < 1e-9 ? 1 : Math.sin((1 - t) * omega) / so;
    const w1 = so < 1e-9 ? 0 : Math.sin(t * omega) / so;
    const x = va[0] * w0 + vb[0] * w1;
    const y = va[1] * w0 + vb[1] * w1;
    const z = va[2] * w0 + vb[2] * w1;
    const k = (r * (1 + lift * Math.sin(Math.PI * t))) / Math.hypot(x, y, z);
    out.push([x * k, y * k, z * k]);
  }
  return out;
}

/* The direction that sits in the middle of some places: the normalised sum of
 * their unit vectors, as [lat, lon] in degrees. */
export function centreOf(places: ReadonlyArray<readonly [number, number]>): [number, number] {
  let x = 0;
  let y = 0;
  let z = 0;
  for (const [lat, lon] of places) {
    const v = latLonToVec3(lat, lon, 1);
    x += v[0];
    y += v[1];
    z += v[2];
  }
  const len = Math.hypot(x, y, z) || 1;
  return [Math.asin(y / len) / DEG, Math.atan2(-z, x) / DEG];
}
