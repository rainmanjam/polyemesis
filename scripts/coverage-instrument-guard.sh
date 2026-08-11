#!/usr/bin/env bash
#
# coverage-instrument-guard.sh -- prove that `go test -cover ./internal/api`
# measures the tests that were selected, and not a constant.
#
# #217/#223. internal/api runs its tests TWICE (internal/api/main_test.go): the
# caller's own selection, then a forced ^TestLedgerPreflight$ so that no -run,
# -skip or -count filter can switch the route-coverage ledger off. The coverage
# PROFILE, however, is written by testing.M.after, which is guarded by
# m.afterOnce and runs on a defer inside the FIRST m.Run to return. Whichever
# pass goes first is therefore the only pass in the profile.
#
# With the preflight first -- which is how it shipped, and how it stayed for four
# releases -- the profile was the preflight's own execution and nothing else:
#
#   go test ./internal/api -run XXXNoSuchTestAtAll            -covermode=set  ->  22.0%
#   go test ./internal/api -run TestUploadStoresTheFile...    -covermode=set  ->  22.0%
#   go test ./internal/api                                    -covermode=set  ->  22.0%
#
# Zero tests, one test and the whole suite reported the same number. That is not
# a coverage figure; it is a constant that looks like one, and #219 spent a round
# on a false alarm from it. After the reorder the same three probes report 0.1%,
# 7.0% and 69.5%.
#
# THIS GUARD RUNS THE PROBES. It does not read main_test.go and it does not
# assert on its text -- #107 -- because the property is "the number moves when
# the selection moves", and the only way to know that is to move the selection
# and look at the number.
#
# BUDGET, go1.26.5/darwin-arm64: 0.9s + 0.8s + 57.2s = about a minute, dominated
# by the full-package probe. That probe is the one the issue is about (the
# unfiltered number is the number anybody would quote), so it is not dropped;
# the two cheap probes come first so a collapse is usually reported in under two
# seconds.
set -euo pipefail

cd "$(dirname "$0")/.."

pct() {
	# Runs one probe and prints its coverage percentage as a bare number.
	# `[no tests to run]` lands on the same line as the percentage, so the
	# percentage is matched rather than positionally indexed.
	local label="$1"
	shift
	local out
	if ! out="$(go test -count=1 -covermode=set "$@" ./internal/api 2>&1)"; then
		echo "coverage-instrument-guard: the probe '$label' did not pass:" >&2
		echo "$out" >&2
		exit 1
	fi
	local n
	n="$(printf '%s\n' "$out" | grep -oE 'coverage: [0-9]+\.[0-9]+% of statements' | tail -1 | grep -oE '[0-9]+\.[0-9]+')"
	if [ -z "$n" ]; then
		echo "coverage-instrument-guard: the probe '$label' printed no coverage figure:" >&2
		echo "$out" >&2
		exit 1
	fi
	printf '%s' "$n"
}

lt() {
	# Strictly-less on two decimal percentages, without bc.
	awk -v a="$1" -v b="$2" 'BEGIN { exit !(a < b) }'
}

fail() {
	echo "THE COVERAGE INSTRUMENT ON internal/api IS MEASURING THE PREFLIGHT, NOT THE TESTS."
	echo
	echo "  $1"
	echo
	echo "Coverage on this package does not depend on which tests were selected,"
	echo "which means it is not coverage. The cause is the order of the two m.Run"
	echo "calls in internal/api/main_test.go: testing.M.after writes the profile"
	echo "under m.afterOnce, on the way out of whichever pass returns FIRST, so a"
	echo "forced ^TestLedgerPreflight\$ pass placed first is the only thing that"
	echo "ever reaches the profile. The caller's selection must run first and the"
	echo "forced preflight second. See #217, and the comment above TestMain."
	exit 1
}

none="$(pct 'no tests selected' -run XXXNoSuchTestAtAllXXX)"
one="$(pct 'one test selected' -run '^TestUploadStoresTheFileAndTagsItsOrigin$')"

lt "$none" "$one" || fail "no tests selected reported ${none}%, one test reported ${one}%; expected the first to be strictly less."

full="$(pct 'the whole package' )"

lt "$one" "$full" || fail "one test reported ${one}%, the whole package reported ${full}%; expected the first to be strictly less."
lt "$none" "$full" || fail "no tests selected reported ${none}%, the whole package reported ${full}%; expected the first to be strictly less."

echo "coverage-instrument-guard: internal/api coverage tracks the selection -- ${none}% for no tests, ${one}% for one, ${full}% for the package"
