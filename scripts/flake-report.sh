#!/usr/bin/env bash
# What fraction of CI job runs on a branch fail for no reason.
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
# WHAT THE THRESHOLD CANNOT DO -- ISSUE #206, RESOLUTION POINT 3
#
# The 0.5% gate below is a DESIGN TARGET and it is not, at any LIMIT this script
# can reach, a certification. The arithmetic, in three steps:
#
#   1. To bound a rate at 0.5% with 95% confidence takes 598 clean observations
#      of that job (poly_runs_for_rate 0.5 in scripts/lib-observe.sh; the rule
#      of three gives the same answer, 3/0.005 = 600).
#
#   2. This script gets 70 to 85 observations per job at LIMIT=100. That bounds
#      a clean job at 300/80 = 3.75%, SEVEN TIMES the threshold. So "clean" here
#      has never meant "meets the gate", and the table now says so per row.
#
#   3. Raising LIMIT does not fix it, and cannot: the query below is a single
#      page and GitHub caps per_page at 100. MEASURED -- per_page=600 returns
#      100 items. There is no --paginate here, so a LIMIT above 100 is silently
#      the same run as LIMIT=100 while looking like six times the data.
#
# There is a second, worse consequence, which is that the middle verdict is
# unreachable. The smallest non-zero rate a job can show is 1/n, so at n=80 a
# job with a single failure reads 1.25% and is already OVER. "Under the 0.5%
# threshold" requires 0 < rate <= 0.5%, which requires n >= 200. Every job in
# every run of this script to date has therefore been either OVER on its first
# failure or clean-with-a-bound-7x-the-gate. That is a two-branch classifier,
# not a gate, and pretending otherwise is the thing #206 objects to.
#
# WHY 0.5% IS KEPT ANYWAY. It is not arbitrary and it is not retired: it is
# derived on #38 from the merge cost, where eleven gating suites at rate p make
# 1-(1-p)^11 of runs falsely red -- 5.4% at p=0.5%, the boundary published
# guidance calls manageable. That is a real objective and deleting it would lose
# it. What was wrong was the SILENCE about resolution, so the resolution is
# printed next to the verdict instead. Retiring a target because it cannot yet
# be measured, or raising LIMIT to a number the API ignores, would both be worse
# than saying plainly which of the two the reader is looking at.
#
# EXIT STATUS
#
#   0  no suite is demonstrably over THRESHOLD_PCT
#   1  a suite is over it, or the measurement could not be taken at all
#
# Note the wording: 0 is "not shown to be over", not "shown to be under". At the
# resolution above those are different statements and only the first is earned.
#
# A threshold whose verdict does not reach the exit status is a printed opinion.
# This one decides -- but it decides only the direction it can see.
#
# Usage:  ./scripts/flake-report.sh [number-of-runs-to-examine]
set -uo pipefail

REPO="${POLYEMESIS_REPO:-rainmanjam/polyemesis}"
LIMIT="${1:-100}"

# THE API CEILING, SAID OUT LOUD. LIMIT goes straight into per_page below, and
# GitHub caps per_page at 100 -- measured: per_page=600 returns 100 items. Asking
# for 600 therefore produced exactly the LIMIT=100 dataset while the operator
# believed they had six times the runs, which matters precisely because #206
# point 3 is answered with "then raise LIMIT". Raising it is a no-op, and a
# no-op that flatters the resolution is worse than a refusal.
case "$LIMIT" in ''|*[!0-9]*) echo "run limit must be a whole number, got: '$LIMIT'" >&2; exit 1 ;; esac
[ "$LIMIT" -ge 1 ] || { echo "run limit must be at least 1, got: '$LIMIT'" >&2; exit 1; }
if [ "$LIMIT" -gt 100 ]; then
  echo "note: GitHub caps this query at 100 runs per page and this script does not paginate." >&2
  echo "      Asked for $LIMIT; will receive at most 100. Clamping so the number reported" >&2
  echo "      matches the number fetched." >&2
  LIMIT=100
fi

# The fewest observations this will draw a conclusion from. Below it the answer
# is "not enough data", never "clean": at n observations the 95% upper bound on
# an unobserved event is 3/n, so ten observations bound the rate at 30% -- an
# interval that contains every rate anyone would call a problem.
MIN_OBSERVATIONS="${POLYEMESIS_MIN_OBSERVATIONS:-10}"

# The per-suite rate above which a suite is a bug rather than weather. Derived
# on #38: eleven suites at this rate is about 5% of CI runs falsely red -- to be
# exact, 1-(1-0.005)^11 = 5.4% -- which is the boundary published guidance calls
# manageable.
#
# #206 point 3: this is the TARGET, and certifying a job against it needs 598
# clean observations of that job. This script gets 70 to 85. See the header for
# why it is kept and what is printed instead of a certification.
THRESHOLD_PCT=0.5

# The observations a job needs before "under the threshold" is a verdict this
# script could ever reach, rather than a branch that cannot execute. The
# smallest non-zero rate at n observations is 1/n, so 0 < rate <= THRESHOLD_PCT
# needs n >= 100/THRESHOLD_PCT = 200. Computed rather than written as 200 so it
# tracks the threshold if anyone moves it.
CERTIFIABLE_N=$(awk -v t="$THRESHOLD_PCT" 'BEGIN{ printf "%d", (100/t) + 0.5 }')

# The observations needed to BOUND a clean job at the threshold with 95%
# confidence -- 598 at 0.5%. Derived by the same tested function the flake-rate
# workflow uses rather than written as a constant here, so the two instruments
# cannot drift and so moving THRESHOLD_PCT moves this with it.
#
# CHECKED, not sourced hopefully. `set -e` is deliberately not on in this file,
# so a missing lib would leave poly_runs_for_rate undefined, the resolution line
# would vanish, and the report would print a verdict with the caveat silently
# removed -- which is precisely the failure mode #206 is about.
LIB="$(dirname "$0")/lib-observe.sh"
[ -r "$LIB" ] || { echo "cannot read $LIB; the resolution caveat needs it" >&2; exit 1; }
. "$LIB"
command -v poly_runs_for_rate >/dev/null \
  || { echo "$LIB defines no poly_runs_for_rate; refusing to report without it" >&2; exit 1; }
CERTIFYING_N=$(poly_runs_for_rate "$THRESHOLD_PCT" 95) \
  || { echo "the threshold '$THRESHOLD_PCT' is not a rate this can size" >&2; exit 1; }

command -v gh >/dev/null || { echo "gh is required"; exit 1; }
command -v jq >/dev/null || { echo "jq is required"; exit 1; }

# BRANCH is a variable now, and the default is still main.
#
# #179 happened on a branch, and this script queried branch=main only, so the
# incident it was later used to reason about contributed zero observations. A
# default is the right shape for a trend line; a hard-coded value is not, when
# the question is sometimes "is THIS branch flaky before I merge it".
BRANCH="${POLYEMESIS_FLAKE_BRANCH:-main}"

echo "Examining up to $LIMIT ci runs on $BRANCH in $REPO ..."

runs=$(gh api "repos/$REPO/actions/workflows/ci.yml/runs?branch=$BRANCH&per_page=$LIMIT" \
  --jq '.workflow_runs[] | "\(.id) \(.run_attempt)"' 2>/dev/null)
if [ -z "$runs" ]; then
  echo "no runs found on $BRANCH; is the workflow name still ci.yml?" >&2
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
    #
    # EVERY JOB, NOT ONLY `acceptance:`. This filter used to be
    # `startswith("acceptance:")`, and that is a structural blindness rather than
    # a coarse one. MEASURED on run 31422948245: 22 jobs exist and 12 matched.
    # Not matched: `test: ubuntu-latest`, `test: macos-latest`,
    # `test: windows-latest`, `go build, vet, test`, the three `container:` jobs,
    # `ui`, `cross-compile`.
    #
    # Which means this script could not, IN PRINCIPLE AND AT ANY N, observe the
    # two incidents it was then used to reason about: #180 was a Go test in
    # `test: windows-latest` and #179 was two steps of the same job. Both
    # contributed exactly zero observations. A discipline that cannot see the
    # class it is prosecuting is not a weak measurement, it is the wrong
    # instrument, and no number of runs fixes it.
    #
    # The `acceptance: ` prefix is still stripped so those rows read as they
    # always did; every other job keeps its own name as the key. Note the
    # consequence, so nobody reads the first wider run as a regression: the table
    # gains ten rows, and jobs that were never measured before may well appear
    # over the threshold on their first appearance.
    if ! gh api "repos/$REPO/actions/runs/$id/attempts/$a/jobs?per_page=100" \
      --jq '.jobs[]? | "\(.name|sub("^acceptance: ";""))\t\(.conclusion)"' \
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
  echo "The likeliest causes are a branch with little history (try" >&2
  echo "POLYEMESIS_FLAKE_BRANCH=main) or a renamed/rescoped workflow file." >&2
  exit 1
fi

echo
awk -F'\t' -v thr="$THRESHOLD_PCT" '
  # PASS 1: decide which attempts are usable at all.
  #
  # A superseded run has EVERY job cancelled at once -- that is the
  # concurrency group killing a run someone replaced, not eleven suites
  # independently deciding to fail. Counting those produced an 11-suite
  # "14% flake rate" on the first version of this script, which was entirely
  # this repository merging six PRs in an afternoon.
  #
  # So an attempt counts only if at least one job SUCCEEDED in it -- AND most of
  # its jobs were not cancelled.
  #
  # The second clause is not belt-and-braces; without it the first one stopped
  # working when this the script filter widened from the 12 acceptance jobs to all
  # 22. `which container suites` takes about five seconds and therefore almost
  # always finishes before a concurrency cancellation lands, so a wholly
  # superseded run now contains exactly one success and certifies itself.
  #
  # MEASURED over all 34 cancelled runs on main: three attempts leaked through
  # the one-success gate and contributed 60 not-ok observations against a table
  # whose total not-ok was 140. Run 31293861794 is the clearest -- 21 cancelled
  # jobs, one success, and every one of those 21 counted as flakiness. 43% of
  # the reported rate was the thing this gate was written to exclude, which is
  # how `cross-compile all release targets` came to read 5.88%.
  #
  # A ratio rather than a run-level conclusion because the input has none: the
  # fields are attempt, job, conclusion. A genuine flake cancels nothing, so the
  # rule costs real observations nothing.
  NR==FNR {
    if ($3 == "success") ok[$1] = 1
    if ($3 == "cancelled") ncancel[$1]++
    njobs[$1]++
    seen[$1] = 1
    next
  }
  !($1 in ok) { skipped[$1] = 1; next }
  ncancel[$1] * 2 >= njobs[$1] { skipped[$1] = 1; next }
  {
    total[$2]++
    if ($3 != "success" && $3 != "skipped") bad[$2]++
  }
  END {
    n = 0
    for (k in skipped) n++
    printf "%-32s %6s %8s %9s   %s\n", "job", "runs", "not-ok", "rate", "verdict"
    nsuites = 0
    over = 0
    for (s in total) {
      nsuites++
      b = (s in bad) ? bad[s] : 0
      r = 100.0 * b / total[s]
      # A clean suite is not "0%" -- it is "below whatever this many runs can
      # see". The rule-of-three upper bound is 3/n, and saying so is the
      # difference between a measurement and a wish.
      #
      # #206 POINT 3: AND THE BOUND IS COMPARED TO THE GATE, per row. Printing
      # "clean; bound <3.75%" beside a 0.5% threshold invites the reader to
      # carry the word "clean" and drop the bound -- which is how a job that is
      # merely unmeasured comes to be described as one that passed. When the
      # bound is above the gate, the row says so in the same breath.
      if (b == 0) {
        bound = 300.0/total[s]
        if (bound > thr)
          verdict = sprintf("clean; bound <%.2f%% (95%%) -- %.0fx the %.1f%% gate, NOT certified",
                            bound, bound/thr, thr)
        else
          verdict = sprintf("clean; bound <%.2f%% (95%%) -- at or under the %.1f%% gate", bound, thr)
      }
      else if (r > thr) { verdict = sprintf("OVER the %.1f%% threshold", thr); over++ }
      else { verdict = sprintf("under the %.1f%% threshold", thr) }
      printf "%-32s %6d %8d %8.2f%%   %s\n", s, total[s], b, r, verdict
    }
    # The two numbers the shell needs in order to decide anything, on a line it
    # can find. Everything above is for a person; this line is the verdict.
    #
    # maxn is carried too, because #206 point 3 is a statement about resolution
    # and resolution is a function of the observation count. The best-observed
    # job is the right one to quote: if even IT cannot certify, none can.
    maxn = 0
    for (s in total) if (total[s] > maxn) maxn = total[s]
    printf "STATUS\t%d\t%d\t%d\t%d\n", nsuites, over, n, maxn
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
maxn=$(printf '%s' "$status_line" | cut -f5)

echo
echo "$nruns runs examined, every attempt of each; $observations observations, $nskipped attempt(s) set aside as superseded."
echo "An observation is one suite in one attempt; a retried run contributes more than one."

# ------------------------------------------------------- #206 POINT 3: RESOLUTION
#
# Printed on every run, next to the verdict, because the verdict alone is the
# thing that misleads. A reader who sees "0 suite(s) over the 0.5% threshold"
# and no resolution line concludes the gate was met. It was not tested.
if [ "${maxn:-0}" -ge 1 ]; then
  smallest=$(awk -v n="$maxn" 'BEGIN{ printf "%.2f", 100.0/n }')
  bound=$(awk -v n="$maxn" 'BEGIN{ printf "%.2f", 300.0/n }')
  echo
  echo "RESOLUTION. The best-observed job has $maxn observations. That means:"
  echo "  - the smallest non-zero rate any job can show is 1/$maxn = ${smallest}%;"
  # Conditional, because it is only true while the resolution is coarser than
  # the gate -- which is the situation this repository is in and not a law.
  if [ "$maxn" -lt "$CERTIFIABLE_N" ]; then
    echo "    a single failure therefore already reads OVER the ${THRESHOLD_PCT}% threshold,"
    echo "    and \"under the ${THRESHOLD_PCT}% threshold\" is UNREACHABLE below $CERTIFIABLE_N observations,"
    echo "    so no job here can be certified against the gate -- only shown to violate it;"
  fi
  echo "  - a job with no failures is bounded at ${bound}%, not at ${THRESHOLD_PCT}%."
  echo "Bounding a clean job at ${THRESHOLD_PCT}% (95%) needs $CERTIFYING_N clean observations of it."
  echo "See issue #206 and the header of this script."
fi

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

# The success message is worded to the resolution as well. "No suite was shown
# to be over" is what was measured; "every suite is under" is what this cannot
# see at $maxn observations, and the two were previously the same sentence.
echo
echo "No suite was shown to be over the ${THRESHOLD_PCT}% threshold."
echo "That is not the same as every suite being under it -- see RESOLUTION above."
