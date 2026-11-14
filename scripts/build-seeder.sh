#!/usr/bin/env bash
#
# Build scripts/seed_demo.go into a binary: build-seeder.sh <output-path>
#
# The seeder imports packages of this module (internal/demorig), so it has to be
# built from the module. The legacy capture scripts keep their work directory in
# $TMPDIR, outside it, and `cd $WORK && go run <path>` fails there with
# "go.mod file not found". This builds from the module whatever the caller's
# working directory is.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
[ $# -eq 1 ] && [ -n "$1" ] || { echo "usage: build-seeder.sh <output-path>" >&2; exit 2; }
out="$1"
case "$out" in /*) ;; *) out="$PWD/$out" ;; esac
go -C "$ROOT" build -o "$out" ./scripts/seed_demo.go
