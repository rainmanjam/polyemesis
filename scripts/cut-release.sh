#!/usr/bin/env bash
# cut-release.sh -- run release.yml's gates HERE, before the tag exists.
#
#   scripts/cut-release.sh v0.11.0          check only; tags nothing
#   scripts/cut-release.sh v0.11.0 --tag    check, then create and push the
#                                           annotated tag (the push publishes)
#   make tag VERSION=v0.11.0 [TAG=1]        the same, through make
#
# WHY THIS EXISTS. Every gate in release.yml runs after `git push origin vX.Y.Z`,
# so every refusal costs a deleted tag, a re-dated CHANGELOG, a PR, a 20-27
# minute CI run on main and a re-tag -- and the date gate (UTC, exactly today)
# is the one that refuses a tag held overnight. v0.7.0, v0.8.0 and v0.9.0 all
# failed their first tag run. RELEASE-RUNBOOK.md listed the checks as a
# checklist, which is the rung that failed. Staging-readiness rows 23 and 26.
#
# WHAT IT RUNS, and where each comes from:
#
#   1. The working tree is clean and HEAD is origin/main, freshly fetched, and
#      the tag does not exist here or on origin. What you check is what you tag.
#   2. changelog-gate and "[Unreleased] must be empty before a tag", READ OUT OF
#      release.yml (scripts/lib-release-steps.sh) and run against HEAD's
#      CHANGELOG.md in a scratch repository carrying an annotated tag of the
#      same name -- the gate reads the tag object, and the real one does not
#      exist yet. The date check therefore uses the same `date -u` the runner
#      will, a few minutes from now.
#   3. ci-gate, read out of release.yml the same way and run with PUBLISH=true
#      against the real Actions API: ci.yml AND security.yml green, from a push
#      to main, for HEAD's SHA.
#   4. A green non-publishing release.yml run (a workflow_dispatch rehearsal or
#      the Tuesday schedule) for HEAD's SHA. The runbook has always asked for
#      this; nothing enforced it, and the last rehearsal before 0.10.0 was six
#      weeks old. release.yml cannot check it itself without re-running what it
#      is about to run anyway.
#   5. Not within 15 minutes of midnight UTC, because changelog-gate runs a few
#      minutes after the push and compares against ITS today.
#
# Needs git, gh (authenticated), jq and python3. Nothing here needs Docker.
set -uo pipefail

SCRIPTS="$(cd "$(dirname "$0")" && pwd)"
# shellcheck source=scripts/lib-release-steps.sh
. "$SCRIPTS/lib-release-steps.sh"

REPO="${CUT_RELEASE_REPO:-rainmanjam/polyemesis}"
REMOTE="${CUT_RELEASE_REMOTE:-origin}"

die()  { printf '\033[31mREFUSED\033[0m  %s\n' "$*" >&2; exit 1; }
pass() { printf '  \033[32mok\033[0m  %s\n' "$*"; }

usage() {
  sed -n '2,8p' "$0" | sed 's/^# \{0,1\}//'
  exit 2
}

tag="${1:-}"
do_tag=false
case "${2:-}" in
  "") ;;
  --tag) do_tag=true ;;
  *) usage ;;
esac
[ -n "$tag" ] || usage
[ $# -le 2 ] || usage
case "$tag" in
  v[0-9]*.[0-9]*.[0-9]*) ;;
  *) die "'$tag' is not a version tag. Expected vX.Y.Z (or vX.Y.Z-rc1)." ;;
esac
printf '%s' "$tag" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?$' \
  || die "'$tag' is not a version tag. Expected vX.Y.Z (or vX.Y.Z-rc1)."

for tool in git gh jq python3; do
  command -v "$tool" >/dev/null 2>&1 || die "$tool is not installed; this script needs git, gh, jq and python3."
done

ROOT="$(git rev-parse --show-toplevel 2>/dev/null)" || die "not inside a git repository."
cd "$ROOT" || die "cannot cd to $ROOT"
WORKFLOW="$ROOT/.github/workflows/release.yml"
[ -f "$WORKFLOW" ] || die "no .github/workflows/release.yml under $ROOT."

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT INT TERM

echo "Checking ${tag} against release.yml's gates, before it exists."

# ---------------------------------------------------------------- 5. the clock
#
# First, because it is the cheapest and the one that has no fix but waiting.
# CUT_RELEASE_UTC_HHMM exists for scripts/test-cut-release.sh; nothing else sets it.
hhmm="${CUT_RELEASE_UTC_HHMM:-$(date -u +%H%M)}"
if [ "$((10#$hhmm))" -ge 2345 ]; then
  die "it is ${hhmm:0:2}:${hhmm:2:2} UTC. changelog-gate runs a few minutes after the push and requires the heading to carry ITS date, which will be tomorrow's. Wait until after 00:00 UTC, re-date the heading, and run this again."
fi
pass "not within 15 minutes of midnight UTC (${hhmm:0:2}:${hhmm:2:2})"

# ---------------------------------------------------------- 1. what gets tagged
[ -z "$(git status --porcelain)" ] \
  || die "the working tree has uncommitted changes. Tag a commit, not a checkout: commit or discard them first."
git fetch --quiet --tags "$REMOTE" main 2>/dev/null \
  || die "could not fetch main from $REMOTE, so HEAD cannot be compared with it."
head_sha="$(git rev-parse HEAD)"
main_sha="$(git rev-parse "$REMOTE/main")"
[ "$head_sha" = "$main_sha" ] \
  || die "HEAD ($head_sha) is not $REMOTE/main ($main_sha). ci-gate only accepts a commit that passed CI as main: git checkout main && git pull --ff-only."
pass "HEAD is $REMOTE/main: $head_sha"

if git rev-parse -q --verify "refs/tags/$tag" >/dev/null; then
  die "$tag already exists locally. A tag that already exists was either released or abandoned; bump the version, or delete it deliberately first."
fi
if git ls-remote --exit-code --tags "$REMOTE" "refs/tags/$tag" >/dev/null 2>&1; then
  die "$tag already exists on $REMOTE."
fi
pass "$tag exists neither here nor on $REMOTE"

# --------------------------------------------- 2. changelog-gate + [Unreleased]
gate="$work/changelog-gate.sh"
unrel="$work/unreleased.sh"
extract_workflow_step "$WORKFLOW" "$STEP_CHANGELOG_GATE" > "$gate" 2>"$work/err" && [ -s "$gate" ] \
  || die "could not read changelog-gate out of release.yml ($(cat "$work/err")). Has the step been renamed? Update scripts/lib-release-steps.sh with it."
extract_workflow_step "$WORKFLOW" "$STEP_UNRELEASED_EMPTY" > "$unrel" 2>"$work/err" && [ -s "$unrel" ] \
  || die "could not read the [Unreleased] check out of release.yml ($(cat "$work/err"))."

# A scratch repository holding HEAD's CHANGELOG.md and an ANNOTATED tag of the
# real name, because the gate asks git what kind of object the tag is.
scratch="$work/repo"
mkdir -p "$scratch"
git show HEAD:CHANGELOG.md > "$scratch/CHANGELOG.md" || die "HEAD has no CHANGELOG.md."
(
  cd "$scratch" || exit 1
  git init -q .
  git config user.email cut-release@example.invalid
  git config user.name cut-release
  git add CHANGELOG.md
  git commit -qm "HEAD's CHANGELOG.md"
  git tag -a "$tag" -m "rehearsal of $tag"
) >/dev/null 2>&1 || die "could not build the scratch repository for changelog-gate."

if ! ( cd "$scratch" && REF_NAME="$tag" REF_TYPE=tag PUBLISH=true bash "$gate" ) > "$work/out" 2>&1; then
  cat "$work/out" >&2
  die "changelog-gate would refuse $tag (above). Fix CHANGELOG.md on main through a PR, let CI go green on that commit, then run this again. Re-pushing a tag to the same commit cannot change what the gate reads."
fi
pass "changelog-gate: $(tail -1 "$work/out")"

# AS A TAG, ALWAYS. The step reports rather than refuses when GITHUB_REF_TYPE
# says the run is not a tag (a release.yml rehearsal from main), and this script
# can itself run where GitHub has set that variable -- a Codespace, a CI job,
# this script's own test on a pull request, which is where it was caught. What
# this script is about to cut IS a tag, so it says so.
if ! ( cd "$scratch" && GITHUB_REF_TYPE=tag bash "$unrel" ) > "$work/out" 2>&1; then
  cat "$work/out" >&2
  die "[Unreleased] is not empty (above). Fold those entries under the $tag heading on main first."
fi
pass "[Unreleased] is empty"

# ----------------------------------------------------------------- 3. ci-gate
cigate="$work/ci-gate.sh"
extract_workflow_step "$WORKFLOW" "$STEP_CI_GATE" > "$cigate" 2>"$work/err" && [ -s "$cigate" ] \
  || die "could not read ci-gate out of release.yml ($(cat "$work/err"))."
if ! ( PUBLISH=true REPO="$REPO" SHA="$head_sha" bash "$cigate" ) > "$work/out" 2>&1; then
  cat "$work/out" >&2
  die "ci-gate would refuse $head_sha (above). Wait for main's runs to finish green, or fix what is red."
fi
pass "ci-gate: $(tail -1 "$work/out")"

# --------------------------------------------------------------- 4. rehearsal
#
# A workflow_dispatch run that went green is a rehearsal: one with dry_run:
# false from a branch is refused by changelog-gate, so it cannot have gone green
# by publishing. The Tuesday schedule never publishes (test-release-gates.sh 11).
runs="$(gh api "repos/${REPO}/actions/workflows/release.yml/runs?head_sha=${head_sha}&per_page=50" 2>"$work/err")" \
  || die "could not ask the Actions API for release.yml runs on $head_sha: $(cat "$work/err")"
rehearsals="$(printf '%s' "$runs" | jq '[.workflow_runs[]? | select(.status == "completed" and .conclusion == "success" and (.event == "workflow_dispatch" or .event == "schedule"))] | length')" \
  || die "could not read the release.yml run listing."
if [ "${rehearsals:-0}" -lt 1 ]; then
  die "no green release.yml rehearsal for $head_sha. The GPU images, the arm64 image and the SBOM are built only by release.yml. Rehearse this exact commit first: gh workflow run release.yml --ref main -f dry_run=true, wait for it to go green, and run this again."
fi
pass "release.yml rehearsed green on this commit (${rehearsals} run(s))"

# ------------------------------------------------------------------ the tag
if [ "$do_tag" != true ]; then
  echo
  echo "Every gate passes for $tag on $head_sha. To publish, run this again with --tag, or:"
  echo "  git tag -a $tag -m \"$tag\" $head_sha && git push $REMOTE $tag"
  echo "The date gate is UTC: do it today ($(date -u +%Y-%m-%d) UTC)."
  exit 0
fi

git tag -a "$tag" -m "$tag" "$head_sha" || die "git tag failed."
if ! git push "$REMOTE" "refs/tags/$tag"; then
  git tag -d "$tag" >/dev/null 2>&1
  die "the push failed; the local tag was removed so a retry starts clean."
fi
echo
echo "Pushed $tag ($head_sha). release.yml is now publishing it:"
echo "  gh run list --workflow release.yml --limit 1"
