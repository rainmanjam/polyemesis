#!/usr/bin/env bash
# What fraction of acceptance-suite runs on main fail for no reason.
#
# WHY THIS CAN EXIST AT ALL
#
# Issue #38 is titled "reruns erase the evidence", and reports that
# `gh run rerun --failed` overwrites the job's recorded conclusion so the flake
# rate cannot be reconstructed after the fact. That is half right, and the half
# that is wrong is what this script runs on.
#
# A rerun DOES overwrite the run's headline conclusion. It does NOT delete the
# earlier attempt: /actions/runs/<id>/attempts/<n>/jobs still returns attempt 1
# in full. Verified on run 30955293999, whose conclusion reads "success" at
# attempt 2 while attempt 1's cancelled acceptance-audio is still there.
#
# So the rate needs no recorder, no stored dataset and no bot committing daily
# numbers. It needs a query, bounded by however long GitHub retains runs.
#
# WHAT IT MEASURES, AND WHY PER SUITE
#
# One observation is one suite in one attempt. A run that was retried three
# times contributes three observations per suite, which is correct: each was a
# real execution that either worked or did not.
#
# The threshold is per SUITE rather than per run because eleven of them gate
# every merge. At rate p each, the chance a whole run is falsely red is
# 1-(1-p)^11 -- so 1.5%, the figure Google publishes for test executions, would
# put roughly one merge in six red for nothing. See the criterion on #38.
#
# Usage:  ./scripts/flake-report.sh [number-of-runs-to-examine]
set -uo pipefail

REPO="${POLYEMESIS_REPO:-rainmanjam/polyemesis}"
LIMIT="${1:-100}"

# The per-suite rate above which a suite is a bug rather than weather. Derived
# on #38: eleven suites at this rate is about 5% of CI runs falsely red, which
# is the boundary published guidance calls manageable.
THRESHOLD_PCT=0.5

command -v gh >/dev/null || { echo "gh is required"; exit 1; }
command -v jq >/dev/null || { echo "jq is required"; exit 1; }

echo "Examining up to $LIMIT ci runs on main in $REPO ..."

runs=$(gh api "repos/$REPO/actions/workflows/ci.yml/runs?branch=main&per_page=$LIMIT" \
  --jq '.workflow_runs[] | "\(.id) \(.run_attempt)"' 2>/dev/null)
if [ -z "$runs" ]; then
  echo "no runs found; is the workflow name still ci.yml?" >&2
  exit 1
fi

tally=$(mktemp)
trap 'rm -f "$tally"' EXIT

nruns=0
while read -r id attempts; do
  [ -n "$id" ] || continue
  nruns=$((nruns + 1))
  # EVERY attempt, not just the last. The last is the one a rerun leaves
  # behind, and reading only that is what makes the rate look like zero.
  for a in $(seq 1 "${attempts:-1}"); do
    # The run+attempt key is prefixed here rather than inside jq: `gh api`
    # passes --jq an expression, not jq's own --arg, so the key has to arrive
    # from the shell.
    gh api "repos/$REPO/actions/runs/$id/attempts/$a/jobs?per_page=100" \
      --jq '.jobs[]? | select(.name | startswith("acceptance:")) | "\(.name|sub("acceptance: ";""))\t\(.conclusion)"' \
      2>/dev/null | sed "s/^/$id-$a\t/" >> "$tally"
  done
done <<< "$runs"

echo
awk -F'\t' -v thr="$THRESHOLD_PCT" '
  # PASS 1: decide which attempts are usable at all.
  #
  # A superseded run has EVERY acceptance job cancelled at once -- that is the
  # concurrency group killing a run someone replaced, not eleven suites
  # independently deciding to fail. Counting those produced an 11-suite
  # "14% flake rate" on the first version of this script, which was entirely
  # this repository merging six PRs in an afternoon.
  #
  # So an attempt counts only if at least one acceptance suite SUCCEEDED in it.
  # That is the difference between "this run was replaced" and "this run ran,
  # and something in it went wrong".
  NR==FNR {
    if ($3 == "success") ok[$1] = 1
    seen[$1] = 1
    next
  }
  !($1 in ok) { skipped[$1] = 1; next }
  {
    total[$2]++
    if ($3 != "success" && $3 != "skipped") bad[$2]++
  }
  END {
    n = 0
    for (k in skipped) n++
    printf "%-32s %6s %8s %9s   %s\n", "suite", "runs", "not-ok", "rate", "verdict"
    for (s in total) {
      b = (s in bad) ? bad[s] : 0
      r = 100.0 * b / total[s]
      # A clean suite is not "0%" -- it is "below whatever this many runs can
      # see". The rule-of-three upper bound is 3/n, and saying so is the
      # difference between a measurement and a wish.
      if (b == 0) { verdict = sprintf("clean; bound <%.2f%% (95%%)", 300.0/total[s]) }
      else if (r > thr) { verdict = sprintf("OVER the %.1f%% threshold", thr) }
      else { verdict = sprintf("under the %.1f%% threshold", thr) }
      printf "%-32s %6d %8d %8.2f%%   %s\n", s, total[s], b, r, verdict
    }
    printf "SKIPPED\t%d\n", n > "/dev/stderr"
  }
' "$tally" "$tally" | (read -r hdr; echo "$hdr"; sort)

echo
echo "$nruns runs examined, every attempt of each."
echo "An observation is one suite in one attempt; a retried run contributes more than one."
