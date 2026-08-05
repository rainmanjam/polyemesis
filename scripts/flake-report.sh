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
# WHAT IT DOES WHEN IT CANNOT MEASURE
#
# It exits non-zero and prints nothing that looks like a rate. Before this it
# printed a bare table header and "N runs examined", exit 0 -- which is what a
# clean CI history and a renamed job produce alike. Same defect as the one the
# flake-rate workflow had (audit C5): a report assembled from zero observations
# reads as a clean suite. The floor below is what stops that.
#
# EXIT STATUS
#
#   0  every suite is at or under THRESHOLD_PCT
#   1  a suite is over it, or the measurement could not be taken at all
#
# A threshold whose verdict does not reach the exit status is a printed opinion.
# This one decides.
#
# Usage:  ./scripts/flake-report.sh [number-of-runs-to-examine]
set -uo pipefail

REPO="${POLYEMESIS_REPO:-rainmanjam/polyemesis}"
LIMIT="${1:-100}"

# The fewest observations this will draw a conclusion from. Below it the answer
# is "not enough data", never "clean": at n observations the 95% upper bound on
# an unobserved event is 3/n, so ten observations bound the rate at 30% -- an
# interval that contains every rate anyone would call a problem.
MIN_OBSERVATIONS="${POLYEMESIS_MIN_OBSERVATIONS:-10}"

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
verdict=$(mktemp)
trap 'rm -f "$tally" "$verdict"' EXIT

nruns=0
nfetch=0
nfetchfail=0
while read -r id attempts; do
  [ -n "$id" ] || continue
  nruns=$((nruns + 1))
  # `${attempts:-1}` defaults only when the field is UNSET OR EMPTY, so a
  # run_attempt arriving as "0" or as anything non-numeric went straight into
  # seq -- and `seq 1 0` prints nothing on coreutils, which drops the run from
  # the dataset in silence. Validate it as a count instead.
  case "$attempts" in ''|*[!0-9]*) attempts=1 ;; esac
  [ "$attempts" -ge 1 ] || attempts=1
  # EVERY attempt, not just the last. The last is the one a rerun leaves
  # behind, and reading only that is what makes the rate look like zero.
  for a in $(seq 1 "$attempts"); do
    # The run+attempt key is prefixed here rather than inside jq: `gh api`
    # passes --jq an expression, not jq's own --arg, so the key has to arrive
    # from the shell.
    #
    # Counted, because 2>/dev/null over a network call means a rate-limited or
    # expired-token run yields an empty tally indistinguishable from a CI
    # history with no failures in it. A partial dataset must not be presented
    # as a complete one.
    nfetch=$((nfetch + 1))
    if ! gh api "repos/$REPO/actions/runs/$id/attempts/$a/jobs?per_page=100" \
      --jq '.jobs[]? | select(.name | startswith("acceptance:")) | "\(.name|sub("acceptance: ";""))\t\(.conclusion)"' \
      2>/dev/null | sed "s/^/$id-$a\t/" >> "$tally"; then
      nfetchfail=$((nfetchfail + 1))
    fi
  done
done <<< "$runs"

# ------------------------------------------------------ can this be measured?
#
# Each check below refuses to print a rate rather than printing a flattering
# one, and names its own cause -- because "no data" and "no failures" are the
# two readings this script must never let anyone confuse.
if [ "$nfetchfail" -gt 0 ]; then
  echo "$nfetchfail of $nfetch attempt queries failed; the dataset is incomplete." >&2
  echo "Check \`gh auth status\` and the API rate limit, then run it again." >&2
  exit 1
fi

observations=$(grep -c . "$tally" || true)
if [ "${observations:-0}" -lt "$MIN_OBSERVATIONS" ]; then
  echo "Only ${observations:-0} observation(s) from $nruns run(s) -- under the floor of $MIN_OBSERVATIONS." >&2
  echo "This is NOT a clean bill of health; it is too little data to have one." >&2
  echo "The likeliest cause is that the acceptance jobs are no longer named" >&2
  echo "\"acceptance: <suite>\", which is what this script matches on." >&2
  exit 1
fi

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
    nsuites = 0
    over = 0
    for (s in total) {
      nsuites++
      b = (s in bad) ? bad[s] : 0
      r = 100.0 * b / total[s]
      # A clean suite is not "0%" -- it is "below whatever this many runs can
      # see". The rule-of-three upper bound is 3/n, and saying so is the
      # difference between a measurement and a wish.
      if (b == 0) { verdict = sprintf("clean; bound <%.2f%% (95%%)", 300.0/total[s]) }
      else if (r > thr) { verdict = sprintf("OVER the %.1f%% threshold", thr); over++ }
      else { verdict = sprintf("under the %.1f%% threshold", thr) }
      printf "%-32s %6d %8d %8.2f%%   %s\n", s, total[s], b, r, verdict
    }
    # The two numbers the shell needs in order to decide anything, on a line it
    # can find. Everything above is for a person; this line is the verdict.
    printf "STATUS\t%d\t%d\t%d\n", nsuites, over, n
  }
' "$tally" "$tally" > "$verdict"

# `sort` after peeling the header, and the STATUS line kept out of the table.
grep -v '^STATUS' "$verdict" | (read -r hdr; echo "$hdr"; sort)

status_line=$(grep '^STATUS' "$verdict" || true)
if [ -z "$status_line" ]; then
  echo "the analysis produced no verdict line; awk did not finish" >&2
  exit 1
fi
nsuites=$(printf '%s' "$status_line" | cut -f2)
over=$(printf '%s' "$status_line" | cut -f3)
nskipped=$(printf '%s' "$status_line" | cut -f4)

echo
echo "$nruns runs examined, every attempt of each; $observations observations, $nskipped attempt(s) set aside as superseded."
echo "An observation is one suite in one attempt; a retried run contributes more than one."

# Every attempt can be discarded as superseded and leave a table with a header
# and no rows -- which is the C5 reading again, arriving from the other end.
# The observation floor above cannot catch it, because those observations
# existed; they were just all thrown away.
if [ "${nsuites:-0}" -lt 1 ]; then
  echo >&2
  echo "No suite had a usable attempt: every one was set aside as superseded." >&2
  echo "There is no rate here, clean or otherwise." >&2
  exit 1
fi

if [ "${over:-0}" -gt 0 ]; then
  echo >&2
  echo "$over suite(s) are over the ${THRESHOLD_PCT}% threshold." >&2
  exit 1
fi
