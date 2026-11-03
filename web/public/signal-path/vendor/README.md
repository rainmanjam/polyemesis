Three.js 0.187.0 (MIT), from the official npm tarball:
https://registry.npmjs.org/three/-/three-0.187.0.tgz

The tarball SHA-512 was verified against npm's `dist.integrity` before extraction.

- `three.js` bundles the named exports the scene needs (core classes plus the ones the addons import) from build/three.module.js with esbuild, minified as ES2022 ESM.
- `three-addons.js` bundles, from the same 0.187.0 tarball's `examples/jsm`: `lines/Line2`, `LineGeometry`, `LineMaterial` and `postprocessing/EffectComposer`, `RenderPass`, `UnrealBloomPass` (plus their shader and pass dependencies). Its `three` import points at `./three.js`, so there is a single copy of the core.
- `three-field.js` belongs to the alternate experience page and is unchanged.

The preview serves all of this locally and makes no CDN requests.

Retain THREE-LICENSE.txt when redistributing these bundles; it covers the core and the examples/jsm addons (all MIT).
