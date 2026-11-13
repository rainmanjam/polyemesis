/* The hero globe: a dotted earth with the current case's source city, its five
 * destination cities, an off-white video arc and one coral audio arc per track
 * to each destination, and small pulses travelling along the arcs.
 *
 * It replaces the still poster inside [data-globe] at runtime. three.js and the
 * land data are imported here and nowhere else, so neither is in the page's
 * first-load script. The poster stays until the first frame has been drawn; if
 * WebGL is unavailable nothing changes and nothing is logged.
 *
 * Motion is the caller's gate: it renders continuously only while the globe is
 * on screen and the gate allows. Otherwise it draws one still frame (arcs fully
 * drawn, no pulses, no sway) and redraws one whenever the case changes.
 *
 * Labels are HTML over the canvas, so they are real text in the site's mono
 * font. Colours come from the root element's custom properties. */
import {
  AdditiveBlending, BackSide, BufferAttribute, BufferGeometry, CatmullRomCurve3, Color, DoubleSide, Float32BufferAttribute, Group,
  LineBasicMaterial, LineSegments, Mesh, MeshBasicMaterial, PerspectiveCamera, Points, RingGeometry, Scene, ShaderMaterial,
  SphereGeometry, TubeGeometry, Vector3, WebGLRenderer,
} from "three";
import { meterLevel } from "./hero-model";
import { arcPoints, centreOf, latLonToVec3, type Vec3 } from "./globe-math";
import { whileVisible, type MotionGate } from "./motion";

export interface Case {
  source: { label: string; city: string; lat: number; lon: number };
  tracks: Array<{ id: string; label: string }>;
  dests: Array<{ name: string; city: string; lat: number; lon: number; tracks: string[] }>;
}
export interface GlobeHandle {
  setCase(c: Case): void;
  destroy(): void;
}

const DEG = Math.PI / 180;
const SEG = 120; // tube segments per arc
const RS = 6; // tube radial segments
const PER = 4; // pulses per arc
const DESTS = 5;
const MAX_ARCS = DESTS * 6; // the video arc plus up to five audio arcs each
const SEP = 0.03; // gap between neighbouring strands of one route
const RETRACT_S = 0.5;
const DRAW_S = 0.95;
const SWAP_MS = 650;

const sm = (x: number) => x * x * (3 - 2 * x);
const clamp01 = (x: number) => Math.min(1, Math.max(0, x));
const shortest = (d: number) => {
  while (d > 180) d -= 360;
  while (d < -180) d += 360;
  return d;
};

/* Side-by-side offsets for n audio strands, never 0 (the video strand's): in
 * units of SEP, e.g. 2 -> [-1, 1], 3 -> [-2, -1, 1]. */
function slots(n: number): number[] {
  const out: number[] = [];
  for (let i = 0; i < n; i++) out.push((i % 2 ? 1 : -1) * (Math.floor(i / 2) + 1));
  return out.sort((a, b) => a - b);
}

/* "YouTube · Español" -> "Bogotá · ESPAÑOL": the city, and the lane when the
 * destination names one. */
function labelText(city: string, name: string): string {
  const lane = name.split("·")[1]?.trim();
  return lane ? `${city} · ${lane.toUpperCase()}` : city;
}

const noop: GlobeHandle = { setCase() {}, destroy() {} };

export async function mountGlobe(el: HTMLElement, gate: MotionGate, initial: Case): Promise<GlobeHandle> {
  /* Probe for WebGL first, on the canvas the renderer will use, so a browser
   * without it never builds a renderer, and three.js never logs a failure. (The
   * three.js code itself is already downloaded: it is imported with this module.) */
  let canvas: HTMLCanvasElement;
  let gl: WebGL2RenderingContext | null;
  try {
    canvas = document.createElement("canvas");
    gl = canvas.getContext("webgl2", { alpha: true, antialias: true, powerPreference: "default" });
  } catch {
    return noop;
  }
  if (!gl) return noop;

  try {
    const land = await import("../../data/land-110m.json");
    return build(land.default as [number, number][], canvas, gl, el, gate, initial);
  } catch {
    return noop;
  }
}

function build(
  landPoints: [number, number][],
  canvas: HTMLCanvasElement,
  gl: WebGL2RenderingContext,
  el: HTMLElement,
  gate: MotionGate,
  initial: Case,
): GlobeHandle {

  /* ---- colours, read once from the site's own custom properties ---- */
  const css = getComputedStyle(document.documentElement);
  const read = (name: string) => {
    const v = css.getPropertyValue(name).trim();
    if (!v) throw new Error(`missing ${name}`);
    return new Color(v);
  };
  const fg = read("--color-fg");
  const accent = read("--color-accent");
  const muted = read("--color-muted");
  const ink = read("--color-ink");
  /* ShaderMaterial output is written to the canvas as is, so shaders get the
   * display (sRGB) numbers; the built-in materials convert the Color objects. */
  const rgb = (c: InstanceType<typeof Color>, k = 1) => {
    const o = c.getRGB({ r: 0, g: 0, b: 0 }, "srgb");
    return new Vector3(o.r * k, o.g * k, o.b * k);
  };

  const PR = Math.min(window.devicePixelRatio || 1, 2);
  const renderer = new WebGLRenderer({ canvas, context: gl, alpha: true, antialias: true, powerPreference: "default" });
  renderer.setPixelRatio(PR);
  renderer.setClearColor(0x000000, 0);
  canvas.setAttribute("aria-hidden", "true");
  canvas.style.cssText = "position:absolute;inset:0;width:100%;height:100%;display:block";

  const scene = new Scene();
  const camera = new PerspectiveCamera(30, 1, 0.1, 50);
  const globe = new Group();
  scene.add(globe);
  const disposables: Array<{ dispose(): void }> = [];
  const track = <D extends { dispose(): void }>(d: D): D => (disposables.push(d), d);

  /* ---- body, halo, graticule, land ---- */
  const VERT = "varying vec3 vN; varying vec3 vP; void main(){ vN=normalize(normalMatrix*normal); vP=(modelViewMatrix*vec4(position,1.0)).xyz; gl_Position=projectionMatrix*modelViewMatrix*vec4(position,1.0);}";
  globe.add(
    new Mesh(
      track(new SphereGeometry(0.995, 96, 64)),
      track(
        new ShaderMaterial({
          uniforms: { uBody: { value: rgb(ink, 1.5) }, uRim: { value: rgb(muted, 0.65) } },
          vertexShader: VERT,
          fragmentShader:
            "uniform vec3 uBody; uniform vec3 uRim; varying vec3 vN; varying vec3 vP; void main(){ float f=pow(1.0-abs(dot(normalize(vN),normalize(-vP))),3.0); gl_FragColor=vec4(mix(uBody,uRim,f),1.0);}",
        }),
      ),
    ),
  );
  scene.add(
    new Mesh(
      track(new SphereGeometry(1.1, 64, 48)),
      track(
        new ShaderMaterial({
          transparent: true,
          depthWrite: false,
          side: BackSide,
          uniforms: { uHalo: { value: rgb(fg, 0.85) } },
          vertexShader: VERT,
          fragmentShader:
            "uniform vec3 uHalo; varying vec3 vN; varying vec3 vP; void main(){ float d=abs(dot(normalize(vN),normalize(-vP))); gl_FragColor=vec4(uHalo,smoothstep(0.0,0.5,d)*0.20);}",
        }),
      ),
    ),
  );

  const vec = ([x, y, z]: Vec3) => new Vector3(x, y, z);
  {
    const pts: Array<InstanceType<typeof Vector3>> = [];
    const seg = (la0: number, lo0: number, la1: number, lo1: number) =>
      pts.push(vec(latLonToVec3(la0, lo0, 1.0015)), vec(latLonToVec3(la1, lo1, 1.0015)));
    for (let lat = -60; lat <= 60; lat += 30) for (let lon = -180; lon < 180; lon += 5) seg(lat, lon, lat, lon + 5);
    for (let lon = -180; lon < 180; lon += 30) for (let lat = -80; lat < 80; lat += 5) seg(lat, lon, lat + 5, lon);
    globe.add(
      new LineSegments(
        track(new BufferGeometry().setFromPoints(pts)),
        track(new LineBasicMaterial({ color: fg, transparent: true, opacity: 0.07, depthWrite: false })),
      ),
    );
  }

  {
    const flat = new Float32Array(landPoints.length * 3);
    landPoints.forEach(([lat, lon], i) => flat.set(latLonToVec3(lat, lon, 1.001), i * 3));
    const geo = track(new BufferGeometry());
    geo.setAttribute("position", new Float32BufferAttribute(flat, 3));
    globe.add(
      new Points(
        geo,
        track(
          new ShaderMaterial({
            transparent: true,
            depthWrite: false,
            uniforms: { uPx: { value: PR }, uDot: { value: rgb(muted) } },
            vertexShader:
              "uniform float uPx; varying float vF; void main(){ vec4 wp=modelMatrix*vec4(position,1.0); vF=smoothstep(0.0,0.16,dot(normalize(wp.xyz),normalize(cameraPosition-wp.xyz))); gl_PointSize=2.4*uPx; gl_Position=projectionMatrix*viewMatrix*wp;}",
            fragmentShader:
              "uniform vec3 uDot; varying float vF; void main(){ if(length(gl_PointCoord-.5)>.5) discard; gl_FragColor=vec4(uDot,.95*vF);}",
          }),
        ),
      ),
    );
  }

  /* ---- markers ---- */
  const zAxis = new Vector3(0, 0, 1);
  const dotMat = track(new MeshBasicMaterial({ color: fg }));
  const ringGeo = track(new RingGeometry(0.014, 0.019, 56));
  const makeRing = () => {
    const m = new Mesh(ringGeo, track(new MeshBasicMaterial({ color: fg, transparent: true, opacity: 0, side: DoubleSide, depthWrite: false })));
    globe.add(m);
    return m;
  };
  const placeRing = (m: InstanceType<typeof Mesh>, v: InstanceType<typeof Vector3>) => {
    m.position.copy(v).multiplyScalar(1.005);
    m.quaternion.setFromUnitVectors(zAxis, v.clone().normalize());
  };
  const srcDot = new Mesh(track(new SphereGeometry(0.02, 20, 14)), dotMat);
  globe.add(srcDot);
  const srcRings = [makeRing(), makeRing()];
  const destGeo = track(new SphereGeometry(0.014, 16, 12));
  const dests = Array.from({ length: DESTS }, () => {
    const dot = new Mesh(destGeo, dotMat);
    globe.add(dot);
    return { dot, rings: [makeRing(), makeRing()], v: new Vector3(), appear: 0 };
  });

  /* ---- pulses: a fixed pool, one slot group per arc ---- */
  const P = MAX_ARCS * PER;
  const pPos = new Float32Array(P * 3);
  const pCol = new Float32Array(P * 3);
  const pA = new Float32Array(P);
  const pSz = new Float32Array(P);
  const pGeo = track(new BufferGeometry());
  pGeo.setAttribute("position", new BufferAttribute(pPos, 3));
  pGeo.setAttribute("aColor", new BufferAttribute(pCol, 3));
  pGeo.setAttribute("aAlpha", new BufferAttribute(pA, 1));
  pGeo.setAttribute("aSize", new BufferAttribute(pSz, 1));
  const pulses = new Points(
    pGeo,
    track(
      new ShaderMaterial({
        transparent: true,
        depthWrite: false,
        blending: AdditiveBlending,
        uniforms: { uPx: { value: PR } },
        vertexShader:
          "uniform float uPx; attribute vec3 aColor; attribute float aAlpha; attribute float aSize; varying vec3 vC; varying float vA; void main(){ vC=aColor; vA=aAlpha; gl_PointSize=aSize*uPx; gl_Position=projectionMatrix*modelViewMatrix*vec4(position,1.0);}",
        fragmentShader:
          "varying vec3 vC; varying float vA; void main(){ float a=smoothstep(.5,.05,length(gl_PointCoord-.5))*vA; gl_FragColor=vec4(vC*a,a);}",
      }),
    ),
  );
  pulses.frustumCulled = false;
  globe.add(pulses);

  /* ---- arcs ---- */
  interface Strand {
    dest: number;
    video: boolean;
    level: number; // meter index, for the pulse brightness
    mesh: InstanceType<typeof Mesh>;
    mat: InstanceType<typeof MeshBasicMaterial>;
    pts: Float32Array; // SEG + 1 points, for the pulses
    prog: number;
    want: number;
    startAt: number;
    phase: number;
    e: number; // eased, drawn fraction
    op: number;
  }
  let strands: Strand[] = [];
  const srcV = new Vector3();

  function clearStrands() {
    for (const s of strands) {
      globe.remove(s.mesh);
      s.mesh.geometry.dispose();
      s.mat.dispose();
    }
    strands = [];
  }

  function buildStrands(c: Case) {
    clearStrands();
    const a: [number, number] = [c.source.lat, c.source.lon];
    c.dests.slice(0, DESTS).forEach((d, j) => {
      const b: [number, number] = [d.lat, d.lon];
      const om = Math.acos(Math.min(1, srcV.dot(dests[j].v)));
      const base = arcPoints(a, b, 1.006, SEG, 0.05 + 0.13 * (om / Math.PI));
      const n = new Vector3().crossVectors(srcV, dests[j].v).normalize();
      const carried = c.tracks.filter((t) => d.tracks.includes(t.id));
      const offs = [0, ...slots(carried.length)];
      offs.forEach((off, i) => {
        const video = i === 0;
        // Strands converge only over the last ~8% at each end.
        const vs = base.map((p, k) => {
          const t = k / SEG;
          const taper = sm(Math.min(1, t / 0.08)) * sm(Math.min(1, (1 - t) / 0.08));
          return vec(p).addScaledVector(n, off * SEP * taper);
        });
        const mat = new MeshBasicMaterial({ color: video ? fg : accent, transparent: true, opacity: 0.9, depthWrite: false });
        const mesh = new Mesh(new TubeGeometry(new CatmullRomCurve3(vs), SEG, video ? 0.0074 : 0.0036, RS, false), mat);
        mesh.renderOrder = video ? 1 : 2;
        mesh.geometry.setDrawRange(0, 0);
        globe.add(mesh);
        const pts = new Float32Array((SEG + 1) * 3);
        vs.forEach((v, k) => v.toArray(pts, k * 3));
        const slot = strands.length;
        for (let q = 0; q < PER; q++) pSz[slot * PER + q] = video ? 7 : 5;
        strands.push({ dest: j, video, level: i, mesh, mat, pts, prog: 0, want: 1, startAt: 0, phase: (j * 0.13 + i * 0.07) % 1, e: 0, op: 0 });
      });
    });
    // Pulse colours are written into the shader as sRGB, like the other shaders.
    for (let o = 0; o < strands.length * PER; o++) {
      const video = strands[Math.floor(o / PER)].video;
      const c = rgb(video ? fg : accent);
      pCol.set([c.x, c.y, c.z], o * 3);
    }
    pGeo.attributes.aColor.needsUpdate = true;
    pGeo.attributes.aSize.needsUpdate = true;
    for (let o = strands.length * PER; o < P; o++) pA[o] = 0;
  }

  /* ---- labels: HTML over the canvas, placed by a small collision solver ---- */
  const overlay = document.createElement("div");
  overlay.className = "globe-labels";
  overlay.setAttribute("aria-hidden", "true");
  const NS = "http://www.w3.org/2000/svg";
  const leaders = document.createElementNS(NS, "svg");
  leaders.setAttribute("class", "globe-leaders");
  overlay.appendChild(leaders);
  interface Label {
    box: HTMLElement;
    leader: SVGLineElement;
    w: number;
    h: number;
    prev: string;
  }
  const makeLabel = (cls: string): Label => {
    const box = document.createElement("span");
    box.className = `gl-label ${cls}`.trim();
    overlay.appendChild(box);
    const leader = document.createElementNS(NS, "line");
    leaders.appendChild(leader);
    return { box, leader, w: 80, h: 22, prev: "" };
  };
  const srcLabel = makeLabel("src");
  const destLabels = dests.map(() => makeLabel(""));

  const measure = () => {
    for (const l of [srcLabel, ...destLabels]) {
      l.w = l.box.offsetWidth || l.w;
      l.h = l.box.offsetHeight || l.h;
    }
  };

  interface Item {
    l: Label;
    x: number;
    y: number;
    vis: number;
    alpha: number;
  }
  type Rect = { x: number; y: number; w: number; h: number };
  const overlap = (a: Rect, b: Rect) =>
    Math.max(0, Math.min(a.x + a.w, b.x + b.w) - Math.max(a.x, b.x)) * Math.max(0, Math.min(a.y + a.h, b.y + b.h) - Math.max(a.y, b.y));
  const DIRS = ["R", "L", "T", "B", "TR", "TL", "BR", "BL"] as const;
  function candidate(dir: (typeof DIRS)[number], g: number, ax: number, ay: number, w: number, h: number) {
    const q = g * 0.72;
    switch (dir) {
      case "R": return { x: ax + g, y: ay - h / 2 };
      case "L": return { x: ax - g - w, y: ay - h / 2 };
      case "T": return { x: ax - w / 2, y: ay - g - h };
      case "B": return { x: ax - w / 2, y: ay + g };
      case "TR": return { x: ax + q, y: ay - q - h };
      case "TL": return { x: ax - q - w, y: ay - q - h };
      case "BR": return { x: ax + q, y: ay + q };
      default: return { x: ax - q - w, y: ay + q };
    }
  }
  function layoutLabels(items: Item[], size: number) {
    const placed: Rect[] = [];
    const anchors = items.filter((it) => it.vis > 0.05).map((it) => ({ x: it.x - 8, y: it.y - 8, w: 16, h: 16, it }));
    for (const it of items) {
      const { l } = it;
      if (it.vis <= 0.05) {
        l.leader.setAttribute("opacity", "0");
        l.box.style.opacity = "0";
        continue;
      }
      let best: { pen: number; c: Rect; key: string; far: boolean } | null = null;
      [11, 30].forEach((g, gi) =>
        DIRS.forEach((dir, di) => {
          const c = { ...candidate(dir, g, it.x, it.y, l.w, l.h), w: l.w, h: l.h };
          let pen = gi * 40 + di * 3;
          pen += (Math.max(0, 2 - c.x) + Math.max(0, c.x + l.w - (size - 2)) + Math.max(0, 2 - c.y) + Math.max(0, c.y + l.h - (size - 2))) * 400;
          for (const p of placed) pen += overlap(c, p) * 8;
          for (const a of anchors) if (a.it !== it) pen += overlap(c, a) * 8;
          if (l.prev === dir + gi) pen -= 30;
          if (!best || pen < best.pen) best = { pen, c, key: dir + gi, far: gi === 1 };
        }),
      );
      const b = best!;
      l.prev = b.key;
      placed.push(b.c);
      l.box.style.transform = `translate(${b.c.x.toFixed(1)}px,${b.c.y.toFixed(1)}px)`;
      l.box.style.opacity = it.alpha.toFixed(2);
      if (b.far) {
        const nx = Math.max(b.c.x, Math.min(b.c.x + l.w, it.x));
        const ny = Math.max(b.c.y, Math.min(b.c.y + l.h, it.y));
        l.leader.setAttribute("x1", it.x.toFixed(1));
        l.leader.setAttribute("y1", it.y.toFixed(1));
        l.leader.setAttribute("x2", nx.toFixed(1));
        l.leader.setAttribute("y2", ny.toFixed(1));
        l.leader.setAttribute("opacity", (it.alpha * 0.8).toFixed(2));
      } else l.leader.setAttribute("opacity", "0");
    }
  }

  /* ---- sizing ---- */
  let S = 0;
  function resize() {
    S = Math.max(240, Math.round(el.clientWidth));
    const ndc = 0.9; // how much of the half-height the globe fills
    renderer.setSize(S, S, false);
    const dist = Math.sqrt(1 + Math.pow(1 / (ndc * Math.tan((camera.fov * DEG) / 2)), 2));
    camera.position.set(0, 0, dist);
    camera.aspect = 1;
    camera.updateProjectionMatrix();
    camDist = dist;
    leaders.setAttribute("viewBox", `0 0 ${S} ${S}`);
    measure();
  }
  let camDist = 5;

  /* ---- the case ---- */
  let current = initial;
  let homeLat = 0;
  let homeLon = 0;
  let srcRetire = false;
  let srcA = 0;

  function applyCase(c: Case) {
    current = c;
    srcV.copy(vec(latLonToVec3(c.source.lat, c.source.lon, 1)));
    srcDot.position.copy(srcV).multiplyScalar(1.004);
    srcRings.forEach((r) => placeRing(r, srcV));
    c.dests.slice(0, DESTS).forEach((d, j) => {
      const x = dests[j];
      x.v.copy(vec(latLonToVec3(d.lat, d.lon, 1)));
      x.dot.position.copy(x.v).multiplyScalar(1.004);
      x.rings.forEach((r) => placeRing(r, x.v));
      x.appear = 0;
      destLabels[j].box.textContent = labelText(d.city, d.name);
    });
    srcLabel.box.textContent = c.source.label;
    buildStrands(c);
    measure();
    // Turn toward the source, and far enough toward its destinations to keep them on the near side.
    [homeLat, homeLon] = centreOf([[c.source.lat, c.source.lon], ...c.dests.map((d): [number, number] => [d.lat, d.lon])]);
  }

  /* ---- drawing ---- */
  let vLat = 0;
  let vLon = 0;
  let tPrev = 0;
  const T0 = performance.now();
  const tmp = new Vector3();

  function frame(now: number, still: boolean) {
    const tt = still ? 0 : Math.max(0, (now - T0) / 1000);
    const dt = still ? 0.016 : Math.min(0.05, Math.max(0.001, (now - tPrev) / 1000 || 0.016));
    tPrev = now;

    const lat = homeLat + (still ? 0 : 2 * Math.sin(tt * 0.09));
    const lon = homeLon + (still ? 0 : 5 * Math.sin(tt * 0.12));
    const k = still ? 1 : 1 - Math.exp(-dt * 2.6);
    vLat += (lat - vLat) * k;
    vLon += shortest(lon - vLon) * k;
    globe.rotation.set(vLat * DEG, -Math.PI / 2 - vLon * DEG, 0);
    globe.updateMatrixWorld(true);

    for (const s of strands) {
      const want = now < s.startAt ? 0 : s.want;
      if (still) s.prog = s.want;
      else s.prog += Math.max(-dt / RETRACT_S, Math.min(dt / DRAW_S, want - s.prog));
      s.e = sm(clamp01(s.prog));
      s.mesh.geometry.setDrawRange(0, Math.floor(s.e * SEG) * RS * 6);
      s.mesh.visible = s.e > 0.004;
      s.mat.opacity = 0.92;
      s.op = s.mat.opacity;
    }
    pulses.visible = !still;
    if (!still) {
      strands.forEach((s, kk) => {
        const lvl = meterLevel(s.level % 5, tt);
        for (let q = 0; q < PER; q++) {
          const o = kk * PER + q;
          const u = (((tt * 0.14 + q / PER + s.phase) % 1) + 1) % 1;
          const f = u * s.e * SEG;
          const i0 = Math.floor(f);
          const i1 = Math.min(SEG, i0 + 1);
          const fr = f - i0;
          for (let a = 0; a < 3; a++) pPos[o * 3 + a] = s.pts[i0 * 3 + a] + (s.pts[i1 * 3 + a] - s.pts[i0 * 3 + a]) * fr;
          const edge = sm(Math.min(1, u / 0.06)) * sm(Math.min(1, (1 - u) / 0.06));
          pA[o] = s.e > 0.02 ? s.op * edge * (s.video ? 1.1 : 0.45 + 0.55 * lvl) : 0;
        }
      });
      pGeo.attributes.position.needsUpdate = true;
      pGeo.attributes.aAlpha.needsUpdate = true;
    }

    srcA += ((srcRetire ? 0 : 1) - srcA) * (still ? 1 : 1 - Math.exp(-dt * 7));
    const ringPhase = (off: number) => (still ? 0.3 : (tt * 0.45 + off) % 1);
    srcRings.forEach((r, q) => {
      const p = ringPhase(q * 0.5);
      r.scale.setScalar(1 + p * 5);
      (r.material as InstanceType<typeof MeshBasicMaterial>).opacity = (1 - p) * 0.7 * srcA;
    });
    srcDot.scale.setScalar(Math.max(0.001, srcA));
    const live = current.dests.length;
    dests.forEach((d, j) => {
      const video = strands.find((s) => s.dest === j && s.video);
      d.appear = j < live && video ? sm(clamp01((video.prog - 0.75) / 0.25)) : 0;
      d.rings.forEach((r, q) => {
        const p = ringPhase(0.2 + j * 0.17 + q * 0.5);
        r.scale.setScalar(1 + p * 3.6);
        (r.material as InstanceType<typeof MeshBasicMaterial>).opacity = (1 - p) * 0.6 * d.appear;
      });
      d.dot.scale.setScalar(Math.max(0.001, d.appear));
    });

    const project = (v: InstanceType<typeof Vector3>) => {
      tmp.copy(v).multiplyScalar(1.006);
      globe.localToWorld(tmp);
      const vis = sm(clamp01((tmp.z - 1 / camDist + 0.04) / 0.14));
      const w = tmp.clone().project(camera);
      return { x: ((w.x + 1) / 2) * S, y: ((1 - w.y) / 2) * S, vis };
    };
    const items: Item[] = [];
    {
      const p = project(srcV);
      items.push({ l: srcLabel, x: p.x, y: p.y, vis: srcA > 0.05 ? p.vis : 0, alpha: p.vis * srcA });
    }
    dests.forEach((d, j) => {
      const p = project(d.v);
      items.push({ l: destLabels[j], x: p.x, y: p.y, vis: d.appear > 0.05 ? p.vis : 0, alpha: p.vis * d.appear });
    });
    layoutLabels(items, S);

    renderer.render(scene, camera);
  }

  /* ---- running ---- */
  let raf = 0;
  let running = false;
  let dead = false;
  let swapTO = 0;
  let pending: Case | null = null;
  let posterHidden = false;
  const poster = [...el.children] as HTMLElement[];

  const drawStill = () => {
    if (dead) return;
    frame(performance.now(), true);
    if (!posterHidden) {
      posterHidden = true;
      for (const p of poster) p.style.display = "none";
    }
  };
  const loop = (now: number) => {
    raf = requestAnimationFrame(loop);
    frame(now, false);
  };
  const settle = () => {
    window.clearTimeout(swapTO);
    if (pending) applyCase(pending);
    pending = null;
    srcRetire = false;
  };
  const start = () => {
    if (dead || running) return;
    running = true;
    raf = requestAnimationFrame(loop);
  };
  const stop = () => {
    if (!running) return;
    running = false;
    cancelAnimationFrame(raf);
    settle();
    drawStill();
  };

  /* ---- go ---- */
  el.append(canvas, overlay);
  resize();
  applyCase(initial);
  vLat = homeLat;
  vLon = homeLon;
  drawStill();
  void document.fonts?.ready.then(() => {
    if (dead) return;
    measure();
    if (!running) drawStill();
  });
  const ro = new ResizeObserver(() => {
    resize();
    if (!running) drawStill();
  });
  ro.observe(el);
  const unwatch = whileVisible(el, gate, start, stop);

  /* A lost context blanks the canvas for good: tear the globe down, which puts
   * the poster back, and say so once (the Stream Deck scene does the same). */
  const onLost = (ev: Event) => {
    ev.preventDefault();
    console.warn("polyemesis: the globe lost its WebGL context; showing the still image instead.");
    handle.destroy();
  };
  canvas.addEventListener("webglcontextlost", onLost);

  const handle: GlobeHandle = {
    setCase(c) {
      if (dead) return;
      window.clearTimeout(swapTO);
      if (!running) {
        pending = null;
        applyCase(c);
        // Snap: with motion off (or off screen) the globe does not turn.
        vLat = homeLat;
        vLon = homeLon;
        drawStill();
        return;
      }
      // Arcs retract, the globe turns, the new arcs draw out.
      pending = c;
      srcRetire = true;
      for (const s of strands) s.want = 0;
      swapTO = window.setTimeout(() => {
        pending = null;
        applyCase(c);
        const now = performance.now();
        for (const s of strands) s.startAt = now + 450 + s.dest * 130;
        srcRetire = false;
      }, SWAP_MS);
    },
    destroy() {
      if (dead) return;
      dead = true;
      canvas.removeEventListener("webglcontextlost", onLost);
      unwatch();
      ro.disconnect();
      cancelAnimationFrame(raf);
      window.clearTimeout(swapTO);
      clearStrands();
      for (const d of disposables) d.dispose();
      renderer.dispose();
      overlay.remove();
      canvas.remove();
      for (const p of poster) p.style.display = "";
    },
  };
  return handle;
}
