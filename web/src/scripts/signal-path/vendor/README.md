Three.js 0.187.0 (MIT), from the official npm tarball:
https://registry.npmjs.org/three/-/three-0.187.0.tgz

The tarball SHA-512 was verified against npm's `dist.integrity` before extraction.

- `three.js` bundles the named exports the scene needs (core classes plus the ones the addons import) from build/three.module.js with esbuild, minified as ES2022 ESM.
- `three-addons.js` bundles, from the same 0.187.0 tarball's `examples/jsm`: `lines/Line2`, `LineGeometry`, `LineMaterial` and `postprocessing/EffectComposer`, `RenderPass`, `UnrealBloomPass` (plus their shader and pass dependencies). Its `three` import points at `./three.js`, so there is a single copy of the core.
- Each bundle opens with a `/*! … */` legal comment naming the licence. The site's minifier drops comments from the shipped chunks, so the notice travels as a file instead: `web/public/third-party-licenses.txt` is served at `/third-party-licenses.txt` and carries THREE-LICENSE.txt. Update it with any version bump. These files are minified third-party code: they are excluded from `astro check` (tsconfig) and, by their `vendor/` directory name, from SonarCloud's analysis.

The site (web/src/components/SignalPath.astro) imports these through its own lazy module graph; nothing is fetched from a CDN at runtime, and the site's CSP is `script-src 'self'`.

Retain THREE-LICENSE.txt when redistributing these bundles; it covers the core and the examples/jsm addons (all MIT).
