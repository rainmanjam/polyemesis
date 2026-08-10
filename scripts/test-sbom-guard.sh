#!/usr/bin/env bash
# Tests for sbom-guard.sh.
#
# WHY THIS EXISTS
#
# The check it replaces was `packages >= 100` against a document containing
# 437, and the comment above it named the failure it was catching: a scan that
# found one manifest and missed the rest, which "would halve it, not empty it".
# 437 halved is 218. The guard was arithmetically incapable of the job it
# described, and it stayed that way through several releases because the only
# way to run it was to cut one.
#
# So the guard moved into a script, and this runs it against SBOMs that have
# had an ecosystem taken out of them. Every case below is the shape of a real
# accident -- a directory renamed, a manifest moved, a build step reordered so
# dist/ is not there yet -- rather than a mutation chosen to be easy to catch.
#
# These edit the SBOM rather than the tree because the tree version needs syft
# and four minutes; this needs jq and a second, so it can run on every PR. The
# tree version was run by hand when the guard was written: ui/ moved aside,
# rescanned with syft 1.50.0, npm fell from 523 to 0 and the guard failed on
# both the floor and the missing @radix-ui/react-dialog. What is automated
# here is that the guard still reacts that way.
#
# Usage:  ./scripts/test-sbom-guard.sh
set -uo pipefail

SCRIPTS="$(cd "$(dirname "$0")" && pwd)"
GUARD="$SCRIPTS/sbom-guard.sh"

pass=0; fail=0
ok()   { printf "  \033[32mPASS\033[0m  %s\n" "$1"; pass=$((pass+1)); }
bad()  { printf "  \033[31mFAIL\033[0m  %s\n" "$1"; fail=$((fail+1)); }
step() { printf "\n\033[1m%s\033[0m\n" "$1"; }

command -v jq >/dev/null 2>&1 || { echo "jq is required"; exit 1; }

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

# A synthetic pair of documents standing in for a healthy scan. Synthetic
# rather than a committed fixture from a real run, deliberately: a real SBOM is
# 764 packages of churn that would have to be regenerated on every dependency
# bump, and what is under test is the guard's reaction to a SHAPE, not any
# particular release's contents.
#
# The counts are the measured ones from sbom-guard.sh's header, so "healthy"
# here means what the release job actually produces.
build_healthy() {
	local dir="$1"
	mkdir -p "$dir"
	{
		echo '{"packages":['
		local first=1
		emit() { # emit <purl>
			[ $first -eq 1 ] || echo ','
			first=0
			printf '{"externalRefs":[{"referenceType":"purl","referenceLocator":"%s"}]}' "$1"
		}
		emit "pkg:npm/%40radix-ui/react-dialog@1.1.23"
		for i in $(seq 2 523); do emit "pkg:npm/filler-$i@1.0.0"; done
		emit "pkg:golang/modernc.org/sqlite@v1.55.0"
		for i in $(seq 2 211); do emit "pkg:golang/example.com/filler-$i@v1.0.0"; done
		emit "pkg:github/actions/checkout@v7.0.1"
		for i in $(seq 2 27); do emit "pkg:github/example/filler-$i@v1"; done
		echo ']}'
	} > "$dir/polyemesis-sbom.spdx.json"

	# The CycloneDX half carries the same purls in that format's shape, so a
	# guard that only ever learned to read SPDX fails these tests.
	jq '{components: [.packages[] | {purl: .externalRefs[0].referenceLocator}]}' \
		"$dir/polyemesis-sbom.spdx.json" > "$dir/polyemesis-sbom.cdx.json"
}

# Drop every purl of one ecosystem from both documents: what a scan that never
# saw the manifest produces.
drop_ecosystem() {
	local dir="$1" eco="$2"
	jq --arg e "pkg:$eco/" \
		'.packages |= map(select((.externalRefs[0].referenceLocator | startswith($e)) | not))' \
		"$dir/polyemesis-sbom.spdx.json" > "$dir/t" && mv "$dir/t" "$dir/polyemesis-sbom.spdx.json"
	jq --arg e "pkg:$eco/" \
		'.components |= map(select((.purl | startswith($e)) | not))' \
		"$dir/polyemesis-sbom.cdx.json" > "$dir/t" && mv "$dir/t" "$dir/polyemesis-sbom.cdx.json"
}

run_guard() {
	local dir="$1"
	"$GUARD" "$dir/polyemesis-sbom.spdx.json" "$dir/polyemesis-sbom.cdx.json" >"$dir/out" 2>&1
}

# --------------------------------------------------------------- the healthy case

step "A complete SBOM passes"
h="$work/healthy"
build_healthy "$h"
if run_guard "$h"; then
	ok "guard accepts a scan with all three ecosystems"
else
	bad "guard rejected a healthy SBOM -- every case below would pass for the wrong reason"
	cat "$h/out"
fi

# ------------------------------------------------- the failure the old check missed

# THE CENTRAL CASE. Under the old `total >= 100` rule every one of these three
# still had hundreds of packages left and sailed through.
for eco in npm golang github; do
	step "Losing the $eco manifest fails the guard"
	d="$work/no-$eco"
	build_healthy "$d"
	drop_ecosystem "$d" "$eco"

	remaining=$(jq '.packages|length' "$d/polyemesis-sbom.spdx.json")
	if run_guard "$d"; then
		bad "guard passed an SBOM with no $eco packages at all ($remaining left, which is why a single total cannot do this)"
	else
		ok "rejected ($remaining packages remained -- more than the old floor of 100)"
		grep -q "no pkg:\|has no " "$d/out" || bad "  ...but said nothing about a missing anchor"
	fi
done

# ------------------------------------------------------- an ecosystem that thinned

# Not a missing manifest but a partly-read one: the anchor survives, the count
# collapses. Only the floor can catch this, which is why the guard has both.
step "An ecosystem that kept its anchor but lost its bulk fails on the floor"
d="$work/thin-npm"
build_healthy "$d"
jq '.packages |= map(select((.externalRefs[0].referenceLocator | startswith("pkg:npm/") and (contains("radix-ui") | not)) | not))' \
	"$d/polyemesis-sbom.spdx.json" > "$d/t" && mv "$d/t" "$d/polyemesis-sbom.spdx.json"
jq '.components |= map(select((.purl | startswith("pkg:npm/") and (contains("radix-ui") | not)) | not))' \
	"$d/polyemesis-sbom.cdx.json" > "$d/t" && mv "$d/t" "$d/polyemesis-sbom.cdx.json"
if run_guard "$d"; then
	bad "guard passed an SBOM with one npm package"
else
	if grep -q "below the floor" "$d/out"; then
		ok "rejected on the floor, with the anchor still present"
	else
		bad "rejected, but not for the floor -- the floor half is untested"
		cat "$d/out"
	fi
fi

# ------------------------------------------------- the case only the anchor catches

# THE OTHER CENTRAL CASE, and the one that justifies having anchors at all.
#
# npm is two lockfiles, not one. Losing ui/ -- the directory whose rename the
# old comment named as its example -- leaves web/package-lock.json's 415
# packages behind. Measured, not reasoned: syft 1.50.0 on a tree with ui/
# moved aside reports 414 npm, which is comfortably ABOVE this guard's floor of
# 250. No floor that tolerates ordinary churn can catch this. The anchor can,
# because @radix-ui/react-dialog only ever came from ui/.
step "Losing one npm lockfile of two fails on the anchor, above the floor"
d="$work/half-npm"
build_healthy "$d"
# Keep 414 npm packages and drop the one that identifies ui/, which is the
# shape the real rescan produced.
jq '.packages |= map(select((.externalRefs[0].referenceLocator | contains("radix-ui")) | not)) | .packages |= .[0:523-109+1+0]' \
	"$d/polyemesis-sbom.spdx.json" > "$d/t" && mv "$d/t" "$d/polyemesis-sbom.spdx.json"
jq '{components: [.packages[] | {purl: .externalRefs[0].referenceLocator}]}' \
	"$d/polyemesis-sbom.spdx.json" > "$d/polyemesis-sbom.cdx.json"
npm_left=$(jq '[.packages[]|select(.externalRefs[0].referenceLocator|startswith("pkg:npm/"))]|length' "$d/polyemesis-sbom.spdx.json")
if [ "$npm_left" -lt 250 ]; then
	bad "fixture bug: only $npm_left npm packages left, so this would trip the floor and prove nothing about the anchor"
elif run_guard "$d"; then
	bad "guard passed with ui/'s lockfile gone ($npm_left npm packages left, over the floor of 250)"
else
	if grep -q "radix-ui" "$d/out" && ! grep -q "npm packages, below the floor" "$d/out"; then
		ok "rejected on the anchor alone ($npm_left npm packages, over the floor)"
	else
		bad "rejected, but not on the anchor -- this case is the anchor's whole justification"
		cat "$d/out"
	fi
fi

# ------------------------------------------------------------ one document, not both

# A release ships two formats and a consumer reads whichever one their tooling
# speaks. A guard that checked only the SPDX would let a broken CycloneDX out.
step "A broken CycloneDX fails even when the SPDX is complete"
d="$work/cdx-only"
build_healthy "$d"
jq '.components |= map(select((.purl | startswith("pkg:golang/")) | not))' \
	"$d/polyemesis-sbom.cdx.json" > "$d/t" && mv "$d/t" "$d/polyemesis-sbom.cdx.json"
if run_guard "$d"; then
	bad "guard passed on a CycloneDX with no Go packages because the SPDX was fine"
else
	grep -q "cdx" "$d/out" && ok "rejected, naming the cdx document" || {
		bad "rejected without naming which document"; cat "$d/out"; }
fi

# --------------------------------------------------------------- unreadable inputs

step "An empty or malformed document fails rather than counting zero quietly"
d="$work/empty"
build_healthy "$d"
: > "$d/polyemesis-sbom.cdx.json"
run_guard "$d" && bad "guard passed an empty cdx" || ok "rejected an empty document"

d="$work/malformed"
build_healthy "$d"
echo 'not json at all' > "$d/polyemesis-sbom.cdx.json"
if run_guard "$d"; then
	bad "guard passed a malformed cdx"
else
	grep -q "not valid JSON" "$d/out" && ok "rejected malformed JSON by name" || {
		bad "rejected, but not with a message naming the malformed document"; cat "$d/out"; }
fi

printf "\n%d passed, %d failed\n" "$pass" "$fail"
[ "$fail" -eq 0 ]
