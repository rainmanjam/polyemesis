# Stream Deck + model scripts

The homepage shows a 3D Stream Deck +. The model is `web/public/models/stream-deck-plus.glb` (about 1.3 MB). That file is the artefact of record; these scripts are how it was slimmed and split, then compressed.

## Where the model comes from

The model is derived from Elgato's free "Stream Deck + STP" 3D file, from the Elgato Marketplace. It is not covered by this repository's MIT licence, and the original STP file is not included. Stream Deck + model: Elgato. polyemesis is not affiliated with or endorsed by Elgato. The same notice ships beside the model in `web/public/models/NOTICE.txt`.

Get the STP yourself from the Elgato Marketplace (search "Stream Deck + STP"). Do not copy it into this repository.

## What is not in the repo, and must not be committed

- The STP (or STEP) file: not in the repo. `*.stp` and `*.step` are git-ignored.
- `work/deck.glb`, the 14 MB intermediate converted from the STP: not in the repo. `work/` and every `.glb` under `web/scripts/stream-deck-model/` are git-ignored.

Do not commit either, and do not widen what is committed here: a test (`web/src/lib/stream-deck-model.test.ts`) fails on an STP, a STEP file or a `deck.glb` in this folder.

## What is not recorded

The step from the STP to `deck.glb` was done once by hand and is not recorded. These scripts therefore start from `deck.glb` and cannot rebuild the model from the STP alone. Anyone who needs a fresh `deck.glb` has to repeat that conversion themselves.

## What is here

- `faces.json` holds measured face rectangles (the key, strip and dial outlines, in metres) and two part names taken from the CAD file's object tree, `STREAM_DECK-PLUS_MODULE_1_1_2` and `WD050-REAR-CHASSIS`. `build.py` and three probes use those names to select parts. It holds no geometry.
- `build.py` splits the model's parts and slims them, using the face rectangles in `faces.json` (the eight keys `k00`..`k13` in two rows of four, the strip and the four dials). The last argument is the smallest feature kept, in millimetres (2 was used).
- `regen.sh` runs `build.py` on `work/deck.glb` and writes the model. It exits 2 with a pointer to this file when `work/deck.glb` is missing.
- `probes/bl_*.py` are the seven Blender scripts used to inspect and measure the CAD when `faces.json` was derived. They are not needed to rebuild.

## Compression (no decoder)

The committed model was compressed after `build.py` made it, in a way three.js's `GLTFLoader` reads with no decoder (the site's Content-Security-Policy has no `wasm-unsafe-eval`, so Draco, Meshopt and Basis cannot run). `compress.sh` does it in four steps: `prune` (unused accessors), `weld` (merges bitwise-identical vertices), `wrap-nodes.py` (see below), then `quantize` to 16-bit positions, 8-bit normals and 12-bit texture coordinates (`KHR_mesh_quantization`). It uses `@gltf-transform/cli@4.4.1` through `npx`; that tool is not a dependency of `web/`.

`quantize` moves each mesh's scale and offset into its node's transform. `scene.ts` moves the key and dial nodes, adds children to them and reads their position, so `wrap-nodes.py` first makes each `key*` and `dial*` node a plain parent (same name, same translation) with an unnamed child that holds the mesh; the quantisation transform lands on the child. A test pins this.

The input is the model as committed before the compression, `main` at `32c8d0c2`: 2,930,116 bytes, SHA-256 `bbc517f71c090afe90aa0b711bc10d8875f3897f3972a578f102541a97625eff` (`git show 32c8d0c2:web/public/models/stream-deck-plus.glb`). The step is not idempotent on its own output (the wrap step expects a mesh on each key node), so always start from that input:

```
bash web/scripts/stream-deck-model/compress.sh in.glb web/public/models/stream-deck-plus.glb
```

Result: 1,300,724 bytes (welding takes 92,966 vertices to 78,801).

## Regenerating

You need a `deck.glb` you already have, placed at `web/scripts/stream-deck-model/work/deck.glb`. From the repository root:

```
blender -b --python web/scripts/stream-deck-model/build.py -- web/scripts/stream-deck-model/work/deck.glb web/scripts/stream-deck-model/faces.json web/public/models/stream-deck-plus.glb 2
```

or `bash web/scripts/stream-deck-model/regen.sh`. Always pass `-b`; without it Blender opens its window.

Blender 5.2.3 was the version used. The output of a rebuild is not promised to be byte-identical to the committed model.
