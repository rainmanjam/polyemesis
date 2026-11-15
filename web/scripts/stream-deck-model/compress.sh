#!/usr/bin/env bash
# Shrink the model without a decoder: drop unused data, weld identical vertices,
# quantise positions and normals (KHR_mesh_quantization, which three.js reads
# natively). Not a rebuild: it takes the committed pre-compression model.
# usage: bash compress.sh in.glb out.glb   (see README.md for the input's SHA-256)
set -euo pipefail
[ "$#" -eq 2 ] || { echo "usage: compress.sh in.glb out.glb" >&2; exit 2; }
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
gt() { npx --yes @gltf-transform/cli@4.4.1 "$@"; }
gt prune "$1" "$tmp/a.glb"
gt weld "$tmp/a.glb" "$tmp/b.glb"
python3 -I "$here/wrap-nodes.py" "$tmp/b.glb" "$tmp/c.glb"
gt quantize "$tmp/c.glb" "$2" --quantize-position 16 --quantize-normal 8 --quantize-texcoord 12
