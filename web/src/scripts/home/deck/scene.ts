/* The 3D Stream Deck +: the model on a desk, its keys and touch strip drawn
 * from the plugin's own key code (faces.ts), its dials turning with the track
 * gain, and the pointer: drag to orbit, press a key, turn or push a dial.
 *
 * three.js and the model are imported here and nowhere else, so neither is in
 * the page's first-load script (index.ts imports this module only when the
 * section is near). The still stays until the first frame has been drawn; if
 * WebGL is unavailable nothing changes and nothing is logged.
 *
 * EVERYTHING DRAWN COMES FROM THE STORE. A key's colour, a dial's angle, a key
 * going down: all are read from, or derived from changes in, the deck state, so
 * the scripted show, the legend and the pointer cannot disagree about what the
 * device is doing. The pointer only dispatches actions.
 *
 * Motion is the caller's gate: the frame loop runs only while the section is on
 * screen and the gate allows. Otherwise the scene draws one complete frame
 * (springs snapped, no drift, no meter movement) and one more whenever the
 * state, the pointer or the size changes.
 *
 * The desk has no environment map fetched from anywhere (the site's content
 * security policy forbids it): the reflections come from a few soft boxes built
 * here. The desk's pool of light, contact shadow and key spill are baked into
 * canvases and fade to nothing toward the canvas edges, so the 3D area has no
 * visible edge. The model's licence is in /models/NOTICE.txt. */
import {
  ACESFilmicToneMapping, AdditiveBlending, CanvasTexture, CatmullRomCurve3, Color, DirectionalLight, DoubleSide, Group, LatheGeometry,
  LinearMipmapLinearFilter, MathUtils, Matrix4, Mesh, MeshBasicMaterial, MeshPhysicalMaterial, MeshStandardMaterial, PCFShadowMap,
  PerspectiveCamera, PlaneGeometry, PMREMGenerator, Quaternion, RectAreaLight, Raycaster, Scene, SRGBColorSpace, TubeGeometry,
  Vector2, Vector3, WebGLRenderer, type Object3D, type Texture,
} from "three";
import { GLTFLoader } from "three/addons/loaders/GLTFLoader.js";
import { RectAreaLightUniformsLib } from "three/addons/lights/RectAreaLightUniformsLib.js";
import goLiveIdle from "../../../../../integrations/streamdeck/com.polyemesis.streamdeck.sdPlugin/imgs/keys/go-live/idle@2x.png?url";
import stopAllIdle from "../../../../../integrations/streamdeck/com.polyemesis.streamdeck.sdPlugin/imgs/keys/stop-all/idle@2x.png?url";
import stopAllHeld from "../../../../../integrations/streamdeck/com.polyemesis.streamdeck.sdPlugin/imgs/keys/stop-all/held@2x.png?url";
import presetIdle from "../../../../../integrations/streamdeck/com.polyemesis.streamdeck.sdPlugin/imgs/keys/preset/idle@2x.png?url";
import presetHeld from "../../../../../integrations/streamdeck/com.polyemesis.streamdeck.sdPlugin/imgs/keys/preset/held@2x.png?url";
import iconMeter from "../../../../../integrations/streamdeck/com.polyemesis.streamdeck.sdPlugin/imgs/actions/meter@2x.png?url";
import iconGain from "../../../../../integrations/streamdeck/com.polyemesis.streamdeck.sdPlugin/imgs/actions/track-gain@2x.png?url";
import iconViewers from "../../../../../integrations/streamdeck/com.polyemesis.streamdeck.sdPlugin/imgs/actions/viewers@2x.png?url";
import { drawKey, keyFace, setTiles } from "./faces";
import { GAIN_MAX, GAIN_MIN, GAIN_STEP, HOLD_MS, KEYS, dialDesc, gainName, type DeckState, type DeckStore } from "./state";
import { whileVisible, type MotionGate } from "../motion";

const MODEL_URL = "/models/stream-deck-plus.glb";
const KS = 256; // a key face is drawn at 256 px
const FONT = '"Helvetica Neue",Helvetica,Arial,sans-serif';
const FLOOR = -0.02804; // the model's resting plane: the underside of its four feet
const MIN_PITCH = 18; // the camera never goes below this, so the desk always reads as a desk
const DIAL_DEG_PER_STEP = 15;
const DIALS = ["Meter", "Track gain", "Track gain", "Viewers"] as const;
const DIAL_TIPS = [
  "Meter: the loudest channel, read five times a second. Turning does nothing.",
  "Track gain: turn 0.5 dB a step, push to mute, touch the strip for 0 dB.",
  "Track gain: turn 0.5 dB a step, push to mute, touch the strip for 0 dB.",
  "Viewers: the audience across every platform. Turning does nothing.",
];

type Pick = { t: "key"; i: number } | { t: "dial"; i: number } | { t: "strip"; seg: number };

const noop = () => {};

export async function mountDeck(el: HTMLElement, gate: MotionGate, store: DeckStore): Promise<() => void> {
  /* Probe for WebGL first, on the canvas the renderer will use, so a browser
   * without it never fetches the model. (three.js itself is already downloaded:
   * it is imported with this module.) */
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
    const [gltf, images] = await Promise.all([new GLTFLoader().loadAsync(MODEL_URL), loadImages()]);
    return build(canvas, gl, el, gate, store, gltf.scene, images);
  } catch (err) {
    /* WebGL being unavailable is a normal browser and stays silent (above); a
     * scene that was meant to start and could not is worth one line. */
    console.warn("polyemesis: the Stream Deck + scene could not start; showing the still image instead.", err);
    return noop;
  }
}

async function loadImages(): Promise<Record<string, HTMLImageElement>> {
  const urls: Record<string, string> = {
    "go-live/idle": goLiveIdle,
    "stop-all/idle": stopAllIdle,
    "stop-all/held": stopAllHeld,
    "preset/idle": presetIdle,
    "preset/held": presetHeld,
    "icon/meter": iconMeter,
    "icon/track-gain": iconGain,
    "icon/viewers": iconViewers,
  };
  const out: Record<string, HTMLImageElement> = {};
  await Promise.all(
    Object.entries(urls).map(async ([k, u]) => {
      const img = new Image();
      img.src = u;
      await img.decode();
      out[k] = img;
    }),
  );
  return out;
}

function mkCanvas(w: number, h: number): HTMLCanvasElement {
  const c = document.createElement("canvas");
  c.width = w;
  c.height = h;
  return c;
}

/* Per-key and per-dial motion: a spring for the key's travel, an eased angle for a dial. */
interface KeyMotion { x: number; v: number; target: number; until: number }
interface DialMotion { ang: number; angT: number; push: number; pushT: number; pushUntil: number }

function build(
  canvas: HTMLCanvasElement,
  gl: WebGL2RenderingContext,
  el: HTMLElement,
  gate: MotionGate,
  store: DeckStore,
  model: Object3D,
  images: Record<string, HTMLImageElement>,
): () => void {
  setTiles(images);
  const renderer = new WebGLRenderer({ canvas, context: gl, alpha: true, antialias: true, powerPreference: "default" });
  const pmrem = new PMREMGenerator(renderer);
  const disposables: Array<{ dispose(): void }> = [];
  /* ONE TEARDOWN PATH. assemble registers a function here the moment it creates
   * something that must be undone (a watcher, an observer, the store
   * subscription, a listener, the still being hidden), so a throw at any point
   * releases exactly what exists by then, by the same list the normal disposer
   * and a lost context run. Registered in creation order: the first entry marks
   * the scene dead, so nothing below can draw once teardown has begun. */
  const teardown: Array<() => void> = [];
  let released = false;
  const release = () => {
    if (released) return;
    released = true;
    for (const fn of teardown) fn();
    for (const d of disposables) d.dispose();
    pmrem.dispose();
    renderer.dispose();
    canvas.remove();
  };
  try {
    return assemble(canvas, el, gate, store, model, images, renderer, pmrem, disposables, teardown, release);
  } catch (err) {
    /* Whatever was made before the throw is released, and the still stays. */
    release();
    throw err;
  }
}

function assemble(
  canvas: HTMLCanvasElement,
  el: HTMLElement,
  gate: MotionGate,
  store: DeckStore,
  model: Object3D,
  images: Record<string, HTMLImageElement>,
  renderer: WebGLRenderer,
  pmrem: PMREMGenerator,
  disposables: Array<{ dispose(): void }>,
  teardown: Array<() => void>,
  releaseAll: () => void,
): () => void {
  let dead = false;
  teardown.push(() => {
    dead = true;
  });
  renderer.setClearColor(0x000000, 0);
  renderer.outputColorSpace = SRGBColorSpace;
  renderer.toneMapping = ACESFilmicToneMapping;
  renderer.toneMappingExposure = 1.0;
  renderer.shadowMap.enabled = true;
  renderer.shadowMap.type = PCFShadowMap;
  canvas.setAttribute("aria-hidden", "true");
  const scene = new Scene();
  const camera = new PerspectiveCamera(26, 1, 0.05, 40);

  const KEYN = new Vector3(0, 0.647, 0.762).normalize(); // the key faces' normal, from the CAD
  const UP = new Vector3().crossVectors(KEYN, new Vector3(1, 0, 0)).normalize();
  const BASIS = new Matrix4().makeBasis(new Vector3(1, 0, 0), UP, KEYN);
  const STRIP_C = new Vector3(0, 0.0299, -0.0156);

  const root = new Group();
  scene.add(root);
  root.scale.setScalar(10); // everything below is in metres
  const VIG = { uVigRes: { value: new Vector2(1, 1) } };

  /* The desk layers (pool, shadow, spill) fade to nothing toward the edges of the canvas, in screen space. */
  const vignette = <M extends MeshBasicMaterial | MeshStandardMaterial>(mat: M, chunk: string): M => {
    mat.onBeforeCompile = (sh) => {
      sh.uniforms.uVigRes = VIG.uVigRes;
      sh.fragmentShader =
        `uniform vec2 uVigRes;
float vig(){vec2 q=gl_FragCoord.xy/uVigRes;vec2 e=min(q,1.0-q);return smoothstep(0.0,0.15,e.x)*smoothstep(0.0,0.12,e.y);}
` + sh.fragmentShader.replace(chunk, `${chunk}\ndiffuseColor.a*=vig();`);
    };
    mat.customProgramCacheKey = () => `vig${chunk}`;
    return mat;
  };

  const pickables: Object3D[] = [];
  const keyNodes: Object3D[] = [];
  const dialNodes: Object3D[] = [];
  const keyMats: MeshPhysicalMaterial[] = [];
  const keyCtx: CanvasRenderingContext2D[] = [];
  const keyTex: CanvasTexture[] = [];
  const keySig: string[] = [];
  const track = <D extends { dispose(): void }>(d: D): D => (disposables.push(d), d);

  const mkTex = (c: HTMLCanvasElement) => {
    const t = track(new CanvasTexture(c));
    t.colorSpace = SRGBColorSpace;
    t.anisotropy = renderer.capabilities.getMaxAnisotropy();
    t.generateMipmaps = true;
    t.minFilter = LinearMipmapLinearFilter;
    return t;
  };
  const lcdMaterial = (tex: Texture) =>
    track(
      new MeshPhysicalMaterial({
        color: 0x000000, emissive: 0xffffff, emissiveMap: tex, emissiveIntensity: 1.12, roughness: 0.16, metalness: 0,
        clearcoat: 1, clearcoatRoughness: 0.07, envMapIntensity: 0.5, toneMapped: false,
      }),
    );

  /* An environment made for reflections: a few soft boxes on black, so glass and metal pick up long deliberate highlights. */
  const buildEnv = () => {
    const es = new Scene();
    es.background = new Color(0x020203);
    const box = (w: number, h: number, dir: number[], dist: number, k: number, tint: number) => {
      const m = new Mesh(new PlaneGeometry(w, h), new MeshBasicMaterial({ color: new Color(tint).multiplyScalar(k), side: DoubleSide, toneMapped: false }));
      m.position.set(dir[0], dir[1], dir[2]).normalize().multiplyScalar(dist);
      m.lookAt(0, 0, 0);
      es.add(m);
    };
    box(9, 6, [-0.55, 0.62, 0.56], 9, 4.2, 0xf3f5f9);
    box(16, 1.0, [0.18, 0.86, 0.47], 9, 6.5, 0xf7f8fb);
    box(1.2, 11, [0.97, 0.3, -0.12], 9, 14, 0xeef2ff);
    box(1.2, 10, [-0.96, 0.26, 0.1], 9, 6, 0xf1f3f8);
    box(12, 2.6, [0.1, 0.3, -0.95], 9, 4.5, 0xdbe3f0);
    box(14, 14, [0, -1, 0], 9, 0.12, 0x7a7a84);
    const t = pmrem.fromScene(es, 0.02).texture;
    es.traverse((o) => {
      if (o instanceof Mesh) {
        o.geometry.dispose();
        (o.material as MeshBasicMaterial).dispose();
      }
    });
    return t;
  };
  const env = track(buildEnv());
  scene.environment = env;

  const M = {
    body: new MeshPhysicalMaterial({ color: 0x121316, roughness: 0.75, metalness: 0, clearcoat: 0, envMapIntensity: 0.7 }),
    plate: new MeshPhysicalMaterial({ color: 0x0a0b0d, roughness: 0.36, metalness: 0, clearcoat: 0.3, clearcoatRoughness: 0.34, envMapIntensity: 0.55 }),
    cap: new MeshPhysicalMaterial({ color: 0x040506, roughness: 0.1, metalness: 0, clearcoat: 1, clearcoatRoughness: 0.05, envMapIntensity: 0.5 }),
    keyside: new MeshPhysicalMaterial({ color: 0x0e0f11, roughness: 0.45, metalness: 0, clearcoat: 0.3, clearcoatRoughness: 0.3, envMapIntensity: 0.6 }),
    dial: new MeshStandardMaterial({ color: 0x2a2c31, roughness: 0.28, metalness: 0.95, envMapIntensity: 3.0 }),
    dialtop: new MeshPhysicalMaterial({ color: 0x08090b, roughness: 0.5, metalness: 0.1, clearcoat: 0.55, clearcoatRoughness: 0.22, envMapIntensity: 0.9 }),
    stand: new MeshPhysicalMaterial({ color: 0x121316, roughness: 0.75, metalness: 0, envMapIntensity: 0.6 }),
    rubber: new MeshStandardMaterial({ color: 0x0b0c0e, roughness: 0.55, metalness: 0, envMapIntensity: 0.5 }),
  };
  for (const m of Object.values(M)) track(m);

  /* Re-skins the model's meshes with our materials, by the node they belong to. */
  const skin = (node: Object3D) => {
    node.traverse((o) => {
      if (!(o instanceof Mesh)) return;
      const mn = (o.material as { name?: string } | undefined)?.name;
      let own = "";
      for (let p: Object3D | null = o; p && p !== root; p = p.parent) {
        if (/^(key|dial|body)/.test(p.name)) {
          own = p.name;
          break;
        }
      }
      if (own.startsWith("dial")) {
        if (mn === "dialcap") {
          o.visible = false;
          return;
        }
        o.material = M.dial;
      } else if (own.startsWith("key")) o.material = mn === "mat_7" || mn === "mat_0" ? M.cap : M.keyside;
      else if (own === "body_stand") o.material = M.stand;
      else o.material = mn === "plate" || mn === "mat_0" ? M.plate : M.body;
      o.castShadow = true;
      o.receiveShadow = true;
    });
  };

  /* The dial's cap: top dished 0.25 mm, fillet 1.5 mm, side to the knurl (7.5 mm radius, from the CAD cap). */
  const dialCap = () => {
    const pts: Vector2[] = [];
    const R = 0.0076, F = 0.0015, Y0 = 0.0003;
    pts.push(new Vector2(R, Y0 - 0.0024), new Vector2(R, Y0 - F));
    for (let a = 90; a >= 0; a -= 10) {
      const t = (a * Math.PI) / 180;
      pts.push(new Vector2(R - F + F * Math.sin(t), Y0 - F + F * Math.cos(t)));
    }
    const rd = R - F;
    for (let i = 1; i <= 8; i++) {
      const r = rd * (1 - i / 8);
      pts.push(new Vector2(r, Y0 - 0.00025 * (1 - (r / rd) * (r / rd))));
    }
    const g = track(new LatheGeometry(pts, 72));
    const m = new Mesh(g, M.dialtop);
    m.quaternion.setFromUnitVectors(new Vector3(0, 1, 0), KEYN);
    m.position.set(-0.0000488, -0.000247, 0.000209);
    m.castShadow = true;
    m.receiveShadow = true;
    return m;
  };

  /* Lights: a soft shadow-casting key, a large area light for the faceplate gradient, a rim from behind-right, a low fill. */
  RectAreaLightUniformsLib.init();
  const key = new DirectionalLight(0xfff0e0, 2.6);
  key.position.set(-8.5, 8, 3.5);
  key.castShadow = true;
  key.shadow.mapSize.set(2048, 2048);
  key.shadow.radius = 5;
  key.shadow.bias = -0.0004;
  key.shadow.normalBias = 0.025;
  Object.assign(key.shadow.camera, { left: -2.2, right: 2.2, top: 2.2, bottom: -2.2, near: 4, far: 24 });
  key.target.position.set(0, 0.1, -0.4);
  scene.add(key, key.target);
  const area = new RectAreaLight(0xfff4e8, 3.2, 7, 4);
  area.position.set(-4.5, 6.5, 6);
  area.lookAt(0, 0.3, -0.2);
  scene.add(area);
  const rim = new DirectionalLight(0xdce6ff, 2.0);
  rim.position.set(6, 5, -8);
  scene.add(rim);
  const fill = new DirectionalLight(0xffffff, 0.45);
  fill.position.set(8, 2.5, 4);
  scene.add(fill);

  /* The desk: a dark satin surface with a baked pool of light, dissolving into the page on every side. */
  (() => {
    const c = mkCanvas(512, 512);
    const x = c.getContext("2d")!;
    x.fillStyle = "#2a2d33";
    x.fillRect(0, 0, 512, 512);
    let seed = 7;
    const rnd = () => (seed = (seed * 16807) % 2147483647) / 2147483647;
    for (let i = 0; i < 70; i++) {
      const px = rnd() * 512, py = rnd() * 512, r = 40 + rnd() * 110, v = rnd() < 0.5 ? 255 : 0, a = 0.018 + rnd() * 0.03;
      const g = x.createRadialGradient(px, py, 0, px, py, r);
      g.addColorStop(0, `rgba(${v},${v},${v},${a})`);
      g.addColorStop(1, `rgba(${v},${v},${v},0)`);
      x.fillStyle = g;
      x.fillRect(0, 0, 512, 512);
    }
    const d = x.getImageData(0, 0, 512, 512);
    for (let i = 0; i < d.data.length; i += 4) {
      const n = (rnd() - 0.5) * 5;
      d.data[i] += n;
      d.data[i + 1] += n;
      d.data[i + 2] += n;
    }
    x.putImageData(d, 0, 0);
    const map = track(new CanvasTexture(c));
    map.colorSpace = SRGBColorSpace;
    const a = mkCanvas(512, 512);
    const ax = a.getContext("2d")!;
    const g = ax.createRadialGradient(256, 256, 0, 256, 256, 256);
    [[0, 1], [0.28, 0.92], [0.46, 0.6], [0.62, 0.26], [0.78, 0.06], [0.9, 0], [1, 0]].forEach(([o, v]) => g.addColorStop(o, `rgba(255,255,255,${v})`));
    ax.fillStyle = "#000";
    ax.fillRect(0, 0, 512, 512);
    ax.fillStyle = g;
    ax.fillRect(0, 0, 512, 512);
    const ad = ax.getImageData(0, 0, 512, 512);
    for (let i = 0; i < ad.data.length; i += 4) {
      const v = Math.max(0, Math.min(255, ad.data[i] + (rnd() - 0.5) * 3));
      ad.data[i] = ad.data[i + 1] = ad.data[i + 2] = v;
    }
    ax.putImageData(ad, 0, 0);
    const am = track(new CanvasTexture(a));
    const mat = track(
      vignette(
        new MeshStandardMaterial({ map, alphaMap: am, color: 0xd8d3cd, transparent: true, roughness: 1, metalness: 0, envMapIntensity: 0, emissive: 0xffffff, emissiveMap: map, emissiveIntensity: 0.8 }),
        "#include <alphamap_fragment>",
      ),
    );
    const m = new Mesh(track(new PlaneGeometry(0.66, 0.42)), mat);
    m.rotation.x = -Math.PI / 2;
    m.position.set(0.02, 0, 0.03);
    m.receiveShadow = true;
    root.add(m);
  })();

  /* The USB-C cable's path, in model coordinates. */
  const CP: Array<[number, number, number]> = [
    [0.0, -0.0225, -0.071], [0.0, -0.0262, -0.095], [0.0, -0.0267, -0.125], [0.012, -0.0267, -0.16], [0.05, -0.0267, -0.205],
    [0.12, -0.0267, -0.245], [0.22, -0.0267, -0.262], [0.36, -0.0267, -0.285], [0.52, -0.0267, -0.34], [0.72, -0.0267, -0.44],
  ];
  const cableCurve = new CatmullRomCurve3(CP.map((p) => new Vector3(...p)), false, "catmullrom", 0.5);

  /* The contact shadow: footprint and cable, baked and blurred, drawn top-down. */
  (() => {
    const X0 = -0.5, X1 = 0.8, Z0 = -0.55, Z1 = 0.25, W = 1300, H = 800, k = W / (X1 - X0);
    const c = mkCanvas(W, H);
    const x = c.getContext("2d")!;
    const px = (v: number) => (v - X0) * k;
    const pz = (v: number) => (v - Z0) * k;
    const blobRect = (x0: number, x1: number, z0: number, z1: number, blur: number, a: number) => {
      x.save();
      x.shadowColor = `rgba(0,0,0,${a})`;
      x.shadowBlur = blur;
      x.shadowOffsetX = 5000;
      x.fillStyle = "#000";
      x.fillRect(px(x0) - 5000, pz(z0), px(x1) - px(x0), pz(z1) - pz(z0));
      x.restore();
    };
    blobRect(0.02, 0.15, -0.115, -0.0, 52, 0.55);
    blobRect(-0.066, 0.066, -0.076, 0.03, 38, 0.85);
    blobRect(-0.062, 0.062, -0.112, 0.0, 26, 0.5);
    blobRect(-0.07, 0.07, -0.118, 0.008, 10, 0.75);
    [[-0.058, -0.02], [0.04, 0.058]].forEach(([a, b]) => {
      blobRect(a, b, -0.026, 0.002, 10, 0.9);
      blobRect(a, b, -0.118, -0.1, 10, 0.8);
    });
    const pts = cableCurve.getPoints(80);
    ([[22, 0.55, 0.011], [8, 0.8, 0.006]] as const).forEach(([bl, al, w]) => {
      x.save();
      x.shadowColor = `rgba(0,0,0,${al})`;
      x.shadowBlur = bl;
      x.shadowOffsetX = 5000;
      x.strokeStyle = "#000";
      x.lineWidth = w * k;
      x.lineCap = "round";
      x.beginPath();
      pts.forEach((p, i) => {
        const X = px(p.x) - 5000, Z = pz(p.z);
        if (i) x.lineTo(X, Z);
        else x.moveTo(X, Z);
      });
      x.stroke();
      x.restore();
    });
    const t = track(new CanvasTexture(c));
    const mat = track(vignette(new MeshBasicMaterial({ map: t, transparent: true, depthWrite: false, polygonOffset: true, polygonOffsetFactor: -2, polygonOffsetUnits: -2 }), "#include <map_fragment>"));
    const m = new Mesh(track(new PlaneGeometry(X1 - X0, Z1 - Z0)), mat);
    m.rotation.x = -Math.PI / 2;
    m.position.set((X0 + X1) / 2, 0.0002, (Z0 + Z1) / 2);
    m.renderOrder = 1;
    root.add(m);
  })();

  /* The lit keys spill a little colour onto the desk in front of the deck. */
  const spillC = mkCanvas(512, 256);
  const spillCtx = spillC.getContext("2d")!;
  const spillTex = track(new CanvasTexture(spillC));
  spillTex.colorSpace = SRGBColorSpace;
  {
    const mat = track(vignette(new MeshBasicMaterial({ map: spillTex, transparent: true, blending: AdditiveBlending, depthWrite: false, toneMapped: false }), "#include <map_fragment>"));
    const m = new Mesh(track(new PlaneGeometry(0.2, 0.1)), mat);
    m.rotation.x = -Math.PI / 2;
    m.position.set(0, 0.0004, 0.075);
    m.renderOrder = 2;
    root.add(m);
  }

  /* ---- the device ---- */
  const deck = new Group();
  deck.position.y = -FLOOR;
  root.add(deck);
  const byName: Record<string, Object3D> = {};
  deck.add(model);
  model.traverse((o) => {
    if (o.name) byName[o.name] = o;
  });
  for (const n of ["body_main", "body_stand"]) if (byName[n]) skin(byName[n]);
  const keyP0: Vector3[] = [];
  const dialP0: Vector3[] = [];
  for (let r = 0; r < 2; r++) {
    for (let c = 0; c < 4; c++) {
      const i = r * 4 + c;
      const n = byName[`key${r}${c}`];
      skin(n);
      keyP0[i] = n.position.clone();
      keyNodes[i] = n;
      const cv = mkCanvas(KS, KS);
      const ctx = cv.getContext("2d")!;
      const tex = mkTex(cv);
      const mat = lcdMaterial(tex);
      keyCtx[i] = ctx;
      keyTex[i] = tex;
      keyMats[i] = mat;
      keySig[i] = "";
      const pl = new Mesh(track(new PlaneGeometry(0.0172, 0.0172)), mat);
      pl.quaternion.setFromRotationMatrix(BASIS);
      pl.position.copy(KEYN).multiplyScalar(0.00045);
      n.add(pl);
      n.traverse((o) => {
        o.userData.pick = { t: "key", i } satisfies Pick;
      });
      pickables.push(n);
    }
  }
  /* The dials: one knurled mesh from the CAD, used four times, each with a lathe cap. */
  const tpl = byName.dial0;
  const copies = [tpl, tpl.clone(true), tpl.clone(true), tpl.clone(true)]; // cloned before any cap is added
  const DX = [-0.0442, -0.01473, 0.01473, 0.0442];
  for (let i = 0; i < 4; i++) {
    const n = copies[i];
    n.name = `dial${i}`;
    if (i) model.add(n);
    n.position.set(DX[i], 0.0107, 0.0173);
    skin(n);
    dialP0[i] = n.position.clone();
    dialNodes[i] = n;
    n.add(dialCap());
    n.traverse((o) => {
      o.userData.pick = { t: "dial", i } satisfies Pick;
    });
    pickables.push(n);
  }
  const stripC = mkCanvas(1600, 200);
  const stripCtx = stripC.getContext("2d")!;
  const stripTex = mkTex(stripC);
  const strip = new Mesh(track(new PlaneGeometry(0.1068, 0.0134)), lcdMaterial(stripTex));
  strip.quaternion.setFromRotationMatrix(BASIS);
  strip.position.copy(STRIP_C).addScaledVector(KEYN, 0.0002);
  strip.userData.pick = { t: "strip", seg: 0 } satisfies Pick;
  deck.add(strip);
  pickables.push(strip);

  /* The glow of the LCDs on the faceplate. */
  const glowC = mkCanvas(1024, 512);
  const glowCtx = glowC.getContext("2d")!;
  const glowTex = track(new CanvasTexture(glowC));
  glowTex.colorSpace = SRGBColorSpace;
  const glow = new Mesh(track(new PlaneGeometry(0.128, 0.064)), track(new MeshBasicMaterial({ map: glowTex, transparent: true, blending: AdditiveBlending, depthWrite: false, toneMapped: false })));
  glow.quaternion.setFromRotationMatrix(BASIS);
  glow.position.set(0, 0.0591, -0.0394).addScaledVector(KEYN, -0.0005);
  deck.add(glow);

  /* The CAD has a maker's wordmark recess above the keys: cover it with a flush plate so the face stays clean. */
  const patch = new Mesh(track(new PlaneGeometry(0.039, 0.0088)), track(M.plate.clone()));
  patch.quaternion.setFromRotationMatrix(BASIS);
  {
    const P = new Vector3(-0.0012, 0.0789, -0.0573);
    P.addScaledVector(KEYN, 0.00745 - P.dot(KEYN));
    patch.position.copy(P);
  }
  (patch.material as MeshPhysicalMaterial).polygonOffset = true;
  (patch.material as MeshPhysicalMaterial).polygonOffsetFactor = -2;
  (patch.material as MeshPhysicalMaterial).polygonOffsetUnits = -2;
  patch.receiveShadow = true;
  deck.add(patch);
  const cable = new Mesh(track(new TubeGeometry(cableCurve, 96, 0.002, 12, false)), M.rubber);
  cable.castShadow = true;
  cable.receiveShadow = true;
  deck.add(cable);

  /* ---- painting: keys and strip from the state ---- */
  const TILE_GLOW: Record<string, [number, number, number]> = { idle: [58, 63, 71], live: [11, 126, 57], warn: [232, 163, 61], error: [197, 48, 48], held: [75, 109, 177] };
  const tonesNow: string[] = [];
  let meterDb = -14;
  const fitStrip = (ctx: CanvasRenderingContext2D, txt: string, px: number, max: number, min: number) => {
    let p = px;
    ctx.font = `600 ${p}px ${FONT}`;
    while (ctx.measureText(txt).width > max && p > min) {
      p -= 1;
      ctx.font = `600 ${p}px ${FONT}`;
    }
    return p;
  };
  const barFor = (db: number) => Math.round(((Math.min(GAIN_MAX, Math.max(GAIN_MIN, db)) - GAIN_MIN) / (GAIN_MAX - GAIN_MIN)) * 100) / 100;
  /* The touch strip: four segments of 200x100, drawn at 2x. */
  const drawStrip = (ctx: CanvasRenderingContext2D, s: DeckState) => {
    ctx.setTransform(1, 0, 0, 1, 0, 0);
    ctx.fillStyle = "#0a0b0d";
    ctx.fillRect(0, 0, 1600, 200);
    const g = ctx.createLinearGradient(0, 0, 0, 200);
    g.addColorStop(0, "rgba(255,255,255,.035)");
    g.addColorStop(0.5, "rgba(255,255,255,0)");
    g.addColorStop(1, "rgba(0,0,0,.28)");
    ctx.fillStyle = g;
    ctx.fillRect(0, 0, 1600, 200);
    const h = ctx.createLinearGradient(0, 0, 1600, 0);
    h.addColorStop(0, "rgba(0,0,0,.4)");
    h.addColorStop(0.06, "rgba(0,0,0,0)");
    h.addColorStop(0.94, "rgba(0,0,0,0)");
    h.addColorStop(1, "rgba(0,0,0,.4)");
    ctx.fillStyle = h;
    ctx.fillRect(0, 0, 1600, 200);
    const live = s.dests.some((d) => d === "live");
    const segs = [
      { icon: "meter", title: "", value: `${Math.round(meterDb)} dB`, bar: Math.min(1, Math.max(0, (meterDb + 60) / 60)) },
      { icon: "track-gain", title: gainName(0), value: dialDesc(s, 0), bar: barFor(s.gain[0]) },
      { icon: "track-gain", title: gainName(1), value: dialDesc(s, 1), bar: barFor(s.gain[1]) },
      { icon: "viewers", title: "Viewers", value: live ? s.viewers.toLocaleString("en-US") : "Off air", bar: 0 },
    ];
    segs.forEach((seg, i) => {
      ctx.save();
      ctx.translate(i * 400, 0);
      ctx.fillStyle = "#fff";
      ctx.textBaseline = "alphabetic";
      ctx.textAlign = "left";
      if (seg.title) {
        const px = fitStrip(ctx, seg.title, 32, 272, 22);
        ctx.fillText(seg.title, 32, 20 + px * 0.86);
      }
      const img = images[`icon/${seg.icon}`];
      if (img) ctx.drawImage(img, 32, 80, 96, 96);
      ctx.textAlign = "right";
      fitStrip(ctx, seg.value, 48, 216, 26);
      ctx.fillText(seg.value, 368, 140);
      ctx.fillStyle = "#3a3a3a";
      ctx.beginPath();
      ctx.roundRect(152, 148, 216, 24, 6);
      ctx.fill();
      if (seg.bar > 0) {
        ctx.fillStyle = "#fff";
        ctx.beginPath();
        ctx.roundRect(152, 148, Math.max(12, 216 * seg.bar), 24, 6);
        ctx.fill();
      }
      ctx.restore();
    });
  };
  const drawGlow = () => {
    glowCtx.clearRect(0, 0, 1024, 512);
    for (let i = 0; i < 8; i++) {
      const col = TILE_GLOW[tonesNow[i]] ?? TILE_GLOW.idle;
      const r = Math.floor(i / 4), c = i % 4;
      const cx = 512 + ((c - 1.5) * 29.5) / 128 * 1024 * 0.9728;
      const cy = 256 + (r === 0 ? -1 : 1) * ((10.5 / 64) * 512 * 0.5);
      const gr = glowCtx.createRadialGradient(cx, cy, 0, cx, cy, 150);
      gr.addColorStop(0, `rgba(${col.join(",")},${tonesNow[i] === "idle" ? 0.03 : 0.5})`);
      gr.addColorStop(1, `rgba(${col.join(",")},0)`);
      glowCtx.fillStyle = gr;
      glowCtx.fillRect(cx - 150, cy - 150, 300, 300);
    }
    glowTex.needsUpdate = true;
    spillCtx.clearRect(0, 0, 512, 256);
    for (let c = 0; c < 4; c++) {
      const t = tonesNow[4 + c] !== "idle" ? tonesNow[4 + c] : tonesNow[c];
      if (!t || t === "idle") continue;
      const col = TILE_GLOW[t] ?? TILE_GLOW.idle;
      const cx = 256 + ((c - 1.5) * 0.0295) / 0.2 * 512;
      const gr = spillCtx.createRadialGradient(cx, 70, 0, cx, 70, 110);
      gr.addColorStop(0, `rgba(${col.join(",")},.12)`);
      gr.addColorStop(1, `rgba(${col.join(",")},0)`);
      spillCtx.fillStyle = gr;
      spillCtx.fillRect(cx - 120, 0, 240, 200);
    }
    const sg = spillCtx.createRadialGradient(256, 40, 0, 256, 40, 200);
    sg.addColorStop(0, "rgba(255,255,255,.05)");
    sg.addColorStop(1, "rgba(255,255,255,0)");
    spillCtx.fillStyle = sg;
    spillCtx.fillRect(0, 0, 512, 200);
    spillTex.needsUpdate = true;
  };
  let stripSig = "";
  const paint = (force: boolean) => {
    const s = store.get();
    let toneChanged = force;
    for (let i = 0; i < 8; i++) {
      const f = keyFace(s, i);
      const sig = JSON.stringify(f);
      if (sig !== keySig[i] || force) {
        keySig[i] = sig;
        drawKey(keyCtx[i], f);
        keyTex[i].needsUpdate = true;
        if (tonesNow[i] !== f.tone) toneChanged = true;
        tonesNow[i] = f.tone;
      }
    }
    const ss = JSON.stringify([Math.round(meterDb), s.gain, s.muted, s.dests.some((d) => d === "live") ? s.viewers : -1]);
    if (ss !== stripSig || force) {
      stripSig = ss;
      drawStrip(stripCtx, s);
      stripTex.needsUpdate = true;
    }
    if (toneChanged) drawGlow();
  };

  /* ---- motion: springs for the keys, eased angles for the dials ---- */
  const KD: KeyMotion[] = KEYS.map(() => ({ x: 0, v: 0, target: 0, until: 0 }));
  const DD: DialMotion[] = DIALS.map(() => ({ ang: 0, angT: 0, push: 0, pushT: 0, pushUntil: 0 }));
  let now = 0; // seconds of animated time
  let animated = false;
  const keyDown = (i: number, ms: number) => {
    KD[i].target = 1;
    KD[i].until = ms > 0 ? now + ms / 1000 : 1e9;
  };
  const keyUp = (i: number) => {
    KD[i].until = 0;
    KD[i].target = 0;
  };
  const dialAngle = (s: DeckState, dial: number) => (dial === 1 ? s.gain[0] : dial === 2 ? s.gain[1] : 0) / GAIN_STEP * DIAL_DEG_PER_STEP;

  /* What the store just did, read as hardware: which key went down, which dial pushed. */
  let prev = store.get();
  const syncFromState = (s: DeckState) => {
    if (s.held !== prev.held) {
      if (prev.held) keyUp(prev.held === "stopall" ? 1 : 2);
      if (s.held) keyDown(s.held === "stopall" ? 1 : 2, 0);
    }
    const started = s.dests.flatMap((d, i) => ((d === "starting" && (prev.dests[i] === "off" || prev.dests[i] === "failed")) ? [i] : []));
    if (started.length > 1) keyDown(0, 140);
    else if (started.length === 1) keyDown(3 + started[0], 140);
    for (const d of [0, 1] as const) {
      if (s.muted[d] !== prev.muted[d]) {
        DD[d + 1].pushT = 1;
        DD[d + 1].pushUntil = now + 0.16;
      }
    }
    for (let d = 0; d < 4; d++) DD[d].angT = (dialAngle(s, d) * Math.PI) / 180;
    prev = s;
  };
  for (let d = 0; d < 4; d++) DD[d].ang = DD[d].angT = (dialAngle(prev, d) * Math.PI) / 180;

  /* ---- the camera: fits the body and stand to the box at any aspect ---- */
  const TARGET = new Vector3(0, 0.42, -0.2);
  root.updateMatrixWorld(true);
  const HULL: Vector3[] = [];
  {
    const v = new Vector3();
    for (const n of ["body_main", "body_stand"]) {
      byName[n]?.traverse((o) => {
        if (!(o instanceof Mesh)) return;
        const a = o.geometry.attributes.position;
        const st = Math.max(1, Math.floor(a.count / 500));
        for (let i = 0; i < a.count; i += st) HULL.push(v.fromBufferAttribute(a, i).applyMatrix4(o.matrixWorld).clone());
      });
    }
  }
  const orbit = { yaw: 0, pitch: 0, ty: 0, tp: 0 };
  const _p = new Vector3(), _r = new Vector3(), _u = new Vector3(), _d = new Vector3(), _t = new Vector3();
  const FITW = 0.86, FITH = 0.86;
  const place = () => {
    const w = el.clientWidth || 800, h = el.clientHeight || 500, asp = w / h;
    const vt = Math.tan(MathUtils.degToRad(camera.fov / 2));
    const drift = animated ? 1 : 0;
    const yaw = MathUtils.degToRad(-21 + orbit.yaw + Math.sin(now * 0.23) * 3.2 * drift);
    const pit = MathUtils.degToRad(Math.max(MIN_PITCH, 29 + orbit.pitch + Math.sin(now * 0.17 + 1) * 1.2 * drift));
    _d.set(Math.sin(yaw) * Math.cos(pit), Math.sin(pit), Math.cos(yaw) * Math.cos(pit));
    _t.copy(TARGET);
    let dist = 3;
    for (let it = 0; it < 4; it++) {
      camera.position.copy(_d).multiplyScalar(dist).add(_t);
      camera.lookAt(_t);
      camera.updateMatrixWorld(true);
      let a = 1e9, b = -1e9, c = 1e9, e = -1e9;
      for (const q of HULL) {
        _p.copy(q).project(camera);
        a = Math.min(a, _p.x);
        b = Math.max(b, _p.x);
        c = Math.min(c, _p.y);
        e = Math.max(e, _p.y);
      }
      _r.setFromMatrixColumn(camera.matrixWorld, 0);
      _u.setFromMatrixColumn(camera.matrixWorld, 1);
      _t.addScaledVector(_r, ((a + b) / 2) * dist * vt * asp).addScaledVector(_u, ((c + e) / 2) * dist * vt);
      dist *= Math.max((b - a) / 2 / FITW, (e - c) / 2 / FITH);
    }
    dist *= 1 + Math.sin(now * 0.11) * 0.012 * drift;
    camera.position.copy(_d).multiplyScalar(dist).add(_t);
    camera.lookAt(_t);
  };

  const size = () => {
    const w = Math.max(160, el.clientWidth), h = Math.max(160, el.clientHeight);
    renderer.setPixelRatio(Math.min(window.devicePixelRatio || 1, 2));
    renderer.setSize(w, h, false);
    renderer.getDrawingBufferSize(VIG.uVigRes.value);
    camera.aspect = w / h;
    camera.updateProjectionMatrix();
  };

  /* ---- the frame ---- */
  const N = KEYN.clone();
  const q = new Quaternion();
  let raf = 0;
  let last = 0;
  let onScreen = false;
  let shown = false;
  const still = [...el.children].filter((c) => c.tagName === "PICTURE") as HTMLElement[];

  const advance = (dt: number) => {
    now += dt;
    for (let i = 0; i < 8; i++) {
      const k = KD[i];
      if (k.until && now >= k.until && k.target) k.target = 0;
      if (animated) {
        const n = Math.ceil(dt / 0.006) || 1;
        const h = dt / n;
        for (let s = 0; s < n; s++) {
          k.v += ((k.target - k.x) * 900 - k.v * 34) * h;
          k.x += k.v * h;
        }
        if (k.x < -0.02) k.x = -0.02;
      } else {
        k.x = k.target;
        k.v = 0;
      }
      keyNodes[i].position.copy(keyP0[i]).addScaledVector(N, -k.x * 0.0011);
      keyMats[i].emissiveIntensity = 1.12 * (1 - 0.4 * Math.min(1, Math.max(0, k.x)));
    }
    for (let i = 0; i < 4; i++) {
      const d = DD[i];
      if (d.pushUntil && now >= d.pushUntil) {
        d.pushT = 0;
        d.pushUntil = 0;
      }
      if (animated) {
        d.ang += (d.angT - d.ang) * Math.min(1, dt * 18);
        d.push += (d.pushT - d.push) * Math.min(1, dt * 30);
      } else {
        d.ang = d.angT;
        d.push = 0;
      }
      dialNodes[i].quaternion.copy(q.setFromAxisAngle(N, -d.ang));
      dialNodes[i].position.copy(dialP0[i]).addScaledVector(N, -d.push * 0.0009);
    }
    const ease = dragging?.mode === "orbit" ? 14 : 2.6;
    if (animated) {
      orbit.yaw += (orbit.ty - orbit.yaw) * Math.min(1, dt * ease);
      orbit.pitch += (orbit.tp - orbit.pitch) * Math.min(1, dt * ease);
    } else {
      orbit.yaw = orbit.ty;
      orbit.pitch = orbit.tp;
    }
  };

  let meterAt = 0;
  const render = (t: number) => {
    const dt = last ? Math.min(0.25, (t - last) / 1000) : 0;
    last = t;
    if (animated && Math.floor((now + dt) * 5) !== meterAt) {
      meterAt = Math.floor((now + dt) * 5);
      meterDb = -16 + Math.sin(now * 1.7) * 5 + Math.sin(now * 4.3) * 3 + (Math.random() - 0.5) * 3;
    }
    advance(dt);
    paint(false);
    place();
    renderer.render(scene, camera);
    if (!shown) {
      shown = true;
      for (const p of still) p.style.display = "none";
      el.querySelector<HTMLElement>("[data-hint]")?.removeAttribute("hidden");
    }
  };
  /* One frame now, and while anything is still settling. Continuous drawing is the gate's. */
  const frame = (t: number) => {
    raf = 0;
    if (dead) return;
    render(t);
    if (animated) raf = requestAnimationFrame(frame);
  };
  const draw = () => {
    if (dead || raf || !onScreen) return;
    raf = requestAnimationFrame(frame);
  };
  const start = () => {
    animated = true;
    last = 0;
    draw();
  };
  const stop = () => {
    animated = false;
    cancelAnimationFrame(raf);
    raf = 0;
    last = 0;
    draw(); // one snapped, static frame
  };
  teardown.push(() => cancelAnimationFrame(raf));
  teardown.push(whileVisible(el, gate, start, stop));
  const io = new IntersectionObserver(
    (es) => {
      onScreen = es.at(-1)?.isIntersecting ?? false;
      if (onScreen) draw();
    },
    { rootMargin: "120px" },
  );
  io.observe(el);
  teardown.push(() => io.disconnect());
  teardown.push(store.subscribe(() => {
    syncFromState(store.get());
    draw();
  }));
  const ro = new ResizeObserver(() => {
    size();
    draw();
  });
  ro.observe(el);
  teardown.push(() => ro.disconnect());

  /* The still comes back (and the hint goes) whenever the scene is torn down. */
  teardown.push(() => {
    el.querySelector<HTMLElement>("[data-hint]")?.setAttribute("hidden", "");
    for (const p of still) p.style.display = "";
  });
  el.appendChild(canvas);
  canvas.style.cssText = "position:absolute;inset:0;width:100%;height:100%;display:block";
  size();
  paint(true);
  place();

  /* ---- the pointer ---- */
  const ray = new Raycaster();
  const ndc = new Vector2();
  const tip = el.querySelector<HTMLElement>("[data-tip]");
  let dragging: { x: number; lastX: number; y: number; moved: boolean; mode: "orbit" | "key" | "dial" | "strip"; i: number; yaw0: number; pitch0: number; hold?: number } | null = null;
  const pick = (ev: { clientX: number; clientY: number }): Pick | null => {
    const r = canvas.getBoundingClientRect();
    ndc.set(((ev.clientX - r.left) / r.width) * 2 - 1, -((ev.clientY - r.top) / r.height) * 2 + 1);
    ray.setFromCamera(ndc, camera);
    const hit = ray.intersectObjects(pickables, true)[0];
    if (!hit) return null;
    let o: Object3D | null = hit.object;
    while (o && !o.userData.pick) o = o.parent;
    const p = o?.userData.pick as Pick | undefined;
    if (!p) return null;
    if (p.t === "strip") return { t: "strip", seg: Math.min(3, Math.floor((hit.uv?.x ?? 0.5) * 4)) };
    return p;
  };
  const showTip = (p: Pick | null, ev: PointerEvent) => {
    if (!tip) return;
    if (!p) {
      tip.classList.remove("on");
      return;
    }
    let n: string, t: string;
    if (p.t === "key") (n = KEYS[p.i].name), (t = KEYS[p.i].tip);
    else if (p.t === "dial") (n = p.i === 1 || p.i === 2 ? gainName((p.i - 1) as 0 | 1) : DIALS[p.i]), (t = DIAL_TIPS[p.i]);
    else (n = p.seg === 1 || p.seg === 2 ? gainName((p.seg - 1) as 0 | 1) : DIALS[p.seg]), (t = p.seg === 1 || p.seg === 2 ? "Touch the strip: back to 0 dB." : "Touch strip segment.");
    tip.replaceChildren();
    const b = document.createElement("b");
    b.textContent = n;
    const s = document.createElement("span");
    s.textContent = t;
    tip.append(b, s);
    const r = el.getBoundingClientRect();
    let x = ev.clientX - r.left + 14, y = ev.clientY - r.top + 16;
    if (x > r.width - 270) x = ev.clientX - r.left - 270;
    if (y > r.height - 80) y = ev.clientY - r.top - 80;
    tip.style.left = `${Math.max(0, x)}px`;
    tip.style.top = `${Math.max(0, y)}px`;
    tip.classList.add("on");
  };
  const actionFor = (key: number): Parameters<DeckStore["dispatch"]>[0] | null =>
    key === 0 ? { type: "golive" } : key >= 3 ? { type: "dest", index: key - 3 } : null;
  const gainDial = (i: number): 0 | 1 | null => (i === 1 ? 0 : i === 2 ? 1 : null);
  const onMove = (ev: PointerEvent) => {
    if (dragging) {
      const dx = ev.clientX - dragging.x, dy = ev.clientY - dragging.y;
      if (Math.abs(dx) + Math.abs(dy) > 4) dragging.moved = true;
      if (dragging.mode === "dial") {
        const steps = Math.trunc((ev.clientX - dragging.lastX) / 14);
        const g = gainDial(dragging.i);
        if (steps && g !== null) {
          dragging.lastX += steps * 14;
          store.dispatch({ type: "gain", dial: g, steps });
        }
      } else if (dragging.mode === "orbit") {
        orbit.ty = Math.max(-35, Math.min(35, dragging.yaw0 + dx * 0.28));
        if (ev.pointerType !== "touch") orbit.tp = Math.max(-15, Math.min(15, dragging.pitch0 + dy * 0.16));
        draw();
      }
      tip?.classList.remove("on");
      return;
    }
    const p = pick(ev);
    el.classList.toggle("over", !!p);
    showTip(p, ev);
  };
  const onLeave = () => {
    tip?.classList.remove("on");
    el.classList.remove("over");
  };
  const onDown = (ev: PointerEvent) => {
    tip?.classList.remove("on");
    const p = pick(ev);
    dragging = { x: ev.clientX, lastX: ev.clientX, y: ev.clientY, moved: false, mode: "orbit", i: 0, yaw0: orbit.ty, pitch0: orbit.tp };
    if (p?.t === "key") {
      dragging.mode = "key";
      dragging.i = p.i;
      if (KEYS[p.i].kind === "hold") {
        const which = p.i === 1 ? "stopall" : "preset";
        store.dispatch({ type: "hold", key: which });
        dragging.hold = window.setTimeout(() => store.dispatch({ type: which }), HOLD_MS);
      } else {
        keyDown(p.i, 0);
        draw();
      }
    } else if (p?.t === "dial") {
      dragging.mode = "dial";
      dragging.i = p.i;
    } else if (p?.t === "strip") {
      dragging.mode = "strip";
      dragging.i = p.seg;
    } else el.classList.add("grab");
    try {
      canvas.setPointerCapture(ev.pointerId);
    } catch {
      /* a synthetic pointer has nothing to capture */
    }
  };
  const release = (ev: PointerEvent | null, cancel: boolean) => {
    if (!dragging) return;
    const d = dragging;
    dragging = null;
    el.classList.remove("grab");
    if (d.mode === "key") {
      if (KEYS[d.i].kind === "hold") {
        window.clearTimeout(d.hold);
        store.dispatch({ type: "release" });
      } else {
        keyUp(d.i);
        const over = !cancel && ev ? pick(ev) : null;
        const a = over?.t === "key" && over.i === d.i ? actionFor(d.i) : null;
        if (a) store.dispatch(a);
        draw();
      }
    } else if (d.mode === "dial" && !d.moved && !cancel) {
      const g = gainDial(d.i);
      if (g !== null) store.dispatch({ type: "mute", dial: g });
    } else if (d.mode === "strip" && !d.moved && !cancel) {
      const g = gainDial(d.i);
      if (g !== null) store.dispatch({ type: "zero", dial: g });
    } else if (d.mode === "orbit") {
      orbit.ty = 0;
      orbit.tp = 0;
      draw();
    }
  };
  const onUp = (ev: PointerEvent) => release(ev, false);
  const onCancel = () => release(null, true);
  const onWheel = (ev: WheelEvent) => {
    const p = pick(ev);
    if (p?.t !== "dial") return;
    const g = gainDial(p.i);
    if (g === null) return;
    ev.preventDefault();
    store.dispatch({ type: "gain", dial: g, steps: ev.deltaY < 0 ? 1 : -1 });
  };
  teardown.push(() => window.clearTimeout(dragging?.hold));
  teardown.push(() => {
    canvas.removeEventListener("pointermove", onMove);
    canvas.removeEventListener("pointerleave", onLeave);
    canvas.removeEventListener("pointerdown", onDown);
    canvas.removeEventListener("pointerup", onUp);
    canvas.removeEventListener("pointercancel", onCancel);
    canvas.removeEventListener("wheel", onWheel);
  });
  canvas.addEventListener("pointermove", onMove);
  canvas.addEventListener("pointerleave", onLeave);
  canvas.addEventListener("pointerdown", onDown);
  canvas.addEventListener("pointerup", onUp);
  canvas.addEventListener("pointercancel", onCancel);
  canvas.addEventListener("wheel", onWheel, { passive: false });
  syncFromState(store.get());
  draw();

  const dispose = releaseAll;
  /* A lost context blanks the canvas for good: tear the scene down (which brings
   * the still back), say so once, and tell the section so it can rest the legend. */
  function onLost(ev: Event) {
    ev.preventDefault();
    console.warn("polyemesis: the Stream Deck + scene lost its WebGL context; showing the still image instead.");
    dispose();
    el.dispatchEvent(new CustomEvent("deck:down"));
  }
  teardown.push(() => canvas.removeEventListener("webglcontextlost", onLost));
  canvas.addEventListener("webglcontextlost", onLost);
  return dispose;
}
