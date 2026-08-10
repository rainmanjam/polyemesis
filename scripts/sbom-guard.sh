#!/usr/bin/env bash
# Does the SBOM actually describe this release, or only parse?
#
# WHAT WAS WRONG WITH THE OLD CHECK
#
# release.yml asserted `packages >= 100` against a document with 437 of them,
# and its own comment said the failure it was there to catch was "a scan that
# found one manifest and missed the rest -- a moved go.mod or a UI directory
# rename would halve it, not empty it". Halving 437 gives 218, which is not
# less than 100. The guard could not fail on the one scenario it was written
# for, and no arrangement of a single total could: any floor low enough not to
# false-positive on ordinary dependency churn is far below what remains when
# one ecosystem of three disappears.
#
# So the count is taken per ecosystem, and each ecosystem also has to produce a
# package that is definitely its own. A total cannot notice that npm vanished;
# a check for @radix-ui/react-dialog cannot miss it.
#
# WHERE THE NUMBERS COME FROM
#
# Measured with syft 1.50.0 -- the version release.yml pins -- against a tree
# in the state the release job scans it: after `make release`, so dist/ holds
# the six cross-compiled binaries, and after the UI build, so ui/node_modules
# exists.
#
#   npm     523    108 from ui/package-lock.json + 415 from web/package-lock.json
#   golang  211    32 without dist/, so the other ~179 are binary buildinfo
#   github   27    per-file dedupe of 52 `uses:` across 7 workflow files
#
# Three of those numbers correct the comment they replace, which claimed "437
# packages -- 216 npm, 168 Go, and 52 pinned GitHub Actions":
#
#   - 52 was never a package count. It is how many times `uses:` appears. syft
#     catalogues one package per action per workflow FILE, so the same pinned
#     checkout in seven workflows is seven packages, not one and not fifty-two.
#   - the Go figure is dominated by dist/. The comment says the scan is "from
#     the SOURCE TREE, not the binaries", but syft is pointed at `.` and the
#     cross-compile step ran first, so the binaries are very much in scope --
#     32 packages become 211 because of them.
#   - npm counts both lockfiles. ui/ alone is 108, because syft reads the lock
#     and skips its 161 dev entries; web/package-lock.json marks nothing dev
#     and contributes all 415 of its own.
#
# Floors are set near half of each measured value: high enough that losing an
# ecosystem cannot pass, low enough that ordinary dependency churn never trips
# them. They are a coarse net under the anchors, not the primary check.
#
# HOW TO KNOW THIS WORKS
#
# Run it against an SBOM built from a tree with one ecosystem removed and it
# must fail. scripts/test-sbom-guard.sh does exactly that, and it is the whole
# reason this is a script rather than fifteen more lines inside release.yml:
# the old check was unfalsifiable in practice, because exercising it meant
# cutting a release, so nobody ever did and it stayed wrong.
set -euo pipefail

usage() {
	echo "usage: $0 <spdx.json> <cdx.json>" >&2
	exit 2
}

[ $# -eq 2 ] || usage
spdx="$1"
cdx="$2"

# Floors, deliberately below the measured values above. Raising one because a
# dependency was added is fine; lowering one to make a red run green is the
# thing this file exists to make somebody argue for out loud.
floor_npm=250
floor_golang=100
floor_github=13

# One package that could only have come from that ecosystem's manifest. The
# npm anchor is percent-encoded in the purl (%40radix-ui), so match on the part
# after the scope, which survives either spelling.
anchor_npm="radix-ui/react-dialog"
anchor_golang="modernc.org/sqlite"
anchor_github="actions/checkout"

fail=0
note() {
	echo "SBOM GUARD: $*" >&2
	fail=1
}

# Every purl in a document, whichever format it is. SPDX hangs them off
# externalRefs; CycloneDX puts one on the component. `//empty` and `?` keep jq
# quiet about components that legitimately have neither -- the binaries syft
# lists as files, for instance.
purls() {
	case "$1" in
	*spdx*) jq -r '.packages[].externalRefs[]?|select(.referenceType=="purl")|.referenceLocator' "$1" ;;
	*) jq -r '.components[]?.purl//empty' "$1" ;;
	esac
}

for f in "$spdx" "$cdx"; do
	if [ ! -s "$f" ]; then
		note "$f is missing or empty"
		continue
	fi
	# Parse before counting: jq exiting non-zero on malformed JSON under
	# `set -e` inside a $( ) would abort the run with a jq error rather than a
	# sentence saying which document is broken.
	if ! jq -e . "$f" >/dev/null 2>&1; then
		note "$f is not valid JSON"
		continue
	fi

	all=$(purls "$f")
	for eco in npm golang github; do
		n=$(printf '%s\n' "$all" | grep -c "^pkg:${eco}/" || true)
		floor_var="floor_${eco}"
		floor="${!floor_var}"
		anchor_var="anchor_${eco}"
		anchor="${!anchor_var}"

		echo "$(basename "$f"): ${eco}=${n} (floor ${floor})"
		if [ "$n" -lt "$floor" ]; then
			note "$(basename "$f") has $n $eco packages, below the floor of $floor -- a manifest was probably not scanned"
		fi
		if ! printf '%s\n' "$all" | grep -q "^pkg:${eco}/.*${anchor}"; then
			note "$(basename "$f") has no $anchor -- the $eco manifest was not scanned at all"
		fi
	done
done

if [ "$fail" -ne 0 ]; then
	echo "SBOM GUARD: FAILED. A bill of materials that omits an ecosystem parses cleanly and tells a consumer this release does not depend on it, which is worse than shipping none." >&2
	exit 1
fi

echo "SBOM GUARD: ok"
