#!/usr/bin/env bash
# Rebuild web/public/models/stream-deck-plus.glb from work/deck.glb.
# Run from the repository root. See README.md for what deck.glb is and why it
# is not in the repo.
set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/../../.."

if [ ! -f web/scripts/stream-deck-model/work/deck.glb ]; then
  echo "regen.sh: web/scripts/stream-deck-model/work/deck.glb is missing." >&2
  echo "It is not in the repo and cannot be rebuilt from here; see web/scripts/stream-deck-model/README.md." >&2
  exit 2
fi

blender -b --python web/scripts/stream-deck-model/build.py -- web/scripts/stream-deck-model/work/deck.glb web/scripts/stream-deck-model/faces.json web/public/models/stream-deck-plus.glb 2

echo "wrote web/public/models/stream-deck-plus.glb ($(wc -c < web/public/models/stream-deck-plus.glb | tr -d ' ') bytes)"
