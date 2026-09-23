#!/usr/bin/env bash
# Red/green fixtures for scripts/cut-release.sh.
#
# cut-release.sh is the pre-tag device for staging-readiness rows 23 and 26: it
# runs release.yml's own gates before a tag exists, and refuses. A refusal that
# never fires is indistinguishable from one that works until the day it is
# needed, so each refusal is driven here against a throwaway repository with a
# bare local "origin" and a stub `gh` that answers from fixture files. The real
# release.yml is copied in, so the gates exercised are the ones that will run.
#
# Nothing here touches the network, the real repository's tags, or GitHub.
set -uo pipefail

SCRIPTS="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$SCRIPTS/.." && pwd)"

pass=0; fail=0
ok()  { printf "  \033[32mPASS\033[0m  %s\n" "$1"; pass=$((pass+1)); }
bad() { printf "  \033[31mFAIL\033[0m  %s\n" "$1"; fail=$((fail+1)); }
step(){ printf "\n\033[1m%s\033[0m\n" "$1"; }

for tool in git jq python3; do
  command -v "$tool" >/dev/null 2>&1 || { echo "test-cut-release: $tool is required"; exit 1; }
done

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT INT TERM

today="$(date -u +%Y-%m-%d)"
week_ago="$(date -u -d '7 days ago' +%Y-%m-%d 2>/dev/null || date -u -v-7d +%Y-%m-%d)"

# A stand-in for `gh api <url>`: prints $RUNS/<workflow file>.json, or fails
# like the API does when that file is absent. Anything else it is asked is a
# test bug, and says so.
mkdir -p "$work/bin"
cat > "$work/bin/gh" <<'STUB'
#!/usr/bin/env bash
[ "${1:-}" = api ] || { echo "stub gh: only 'gh api' is modelled, got: $*" >&2; exit 97; }
url="$2"
wf="${url#*/actions/workflows/}"; wf="${wf%%/*}"
f="$RUNS/$wf.json"
[ -f "$f" ] || { echo "HTTP 404: no fixture for $wf" >&2; exit 1; }
cat "$f"
STUB
chmod +x "$work/bin/gh"

green_main='{"workflow_runs":[{"status":"completed","conclusion":"success","event":"push","head_branch":"main"}]}'
red_main='{"workflow_runs":[{"status":"completed","conclusion":"failure","event":"push","head_branch":"main"}]}'
rehearsed='{"workflow_runs":[{"status":"completed","conclusion":"success","event":"workflow_dispatch","head_branch":"main"}]}'
scheduled='{"workflow_runs":[{"status":"completed","conclusion":"success","event":"schedule","head_branch":"main"}]}'
none='{"workflow_runs":[]}'

# fixture <heading line> [unreleased body]  -> a clone in $work/clone whose main
# is pushed to a bare $work/origin.git, with every API answer green.
fixture() {
  local heading="$1" unreleased="${2:-Nothing yet.}"
  rm -rf "$work/origin.git" "$work/clone" "$work/runs"
  mkdir -p "$work/runs" "$work/clone/.github/workflows" "$work/clone/scripts"
  git init -q --bare "$work/origin.git"
  cp "$ROOT/.github/workflows/release.yml" "$work/clone/.github/workflows/"
  cp "$SCRIPTS/cut-release.sh" "$SCRIPTS/lib-release-steps.sh" "$work/clone/scripts/"
  printf '# Changelog\n\n## [Unreleased]\n\n%s\n\n%s\n\n- something\n\n## [0.6.0] — 2026-08-09\n\n- older\n' \
    "$unreleased" "$heading" > "$work/clone/CHANGELOG.md"
  ( cd "$work/clone" || exit 1
    git init -q -b main . 2>/dev/null || { git init -q . && git checkout -q -b main; }
    git config user.email t@example.invalid
    git config user.name test
    git add -A
    git commit -qm fixture
    git remote add origin "$work/origin.git"
    git push -q origin main
  ) >/dev/null 2>&1
  printf '%s' "$green_main" > "$work/runs/ci.yml.json"
  printf '%s' "$green_main" > "$work/runs/security.yml.json"
  printf '%s' "$rehearsed"  > "$work/runs/release.yml.json"
}

RC=0
OUT=""
cut() { # cut <args...>  -> RC, OUT
  ( cd "$work/clone" && PATH="$work/bin:$PATH" RUNS="$work/runs" \
      CUT_RELEASE_UTC_HHMM="${HHMM:-1200}" bash scripts/cut-release.sh "$@" ) > "$work/out" 2>&1
  RC=$?
  OUT="$(cat "$work/out")"
}

expect_refusal() { # expect_refusal <label> <substring>
  if [ "$RC" -eq 0 ]; then
    bad "$1: cut-release.sh exited 0 -- the tag would have been cut"
    return
  fi
  case "$OUT" in
    *"$2"*) ok "$1, and it says why: \"$2\"" ;;
    *) bad "$1: refused, but never says \"$2\": $(printf '%s' "$OUT" | tr '\n' ' ')" ;;
  esac
}

no_tag_anywhere() { # no_tag_anywhere <label>
  if git -C "$work/clone" tag -l | grep -q . || git -C "$work/origin.git" tag -l | grep -q .; then
    bad "$1: a tag was created anyway"
  else
    ok "$1: and no tag exists, here or on origin"
  fi
}

step "1. A release that every gate accepts"
fixture "## [0.7.0] — $today"
cut v0.7.0
if [ "$RC" -eq 0 ] && printf '%s' "$OUT" | grep -q "Every gate passes"; then
  ok "check mode passes a correct release"
else
  bad "check mode refused a correct release: $OUT"
fi
no_tag_anywhere "check mode is only a check"

step "2. --tag cuts an ANNOTATED tag on HEAD and pushes it"
cut v0.7.0 --tag
if [ "$RC" -eq 0 ] && [ "$(git -C "$work/origin.git" cat-file -t refs/tags/v0.7.0 2>/dev/null)" = tag ]; then
  ok "origin has v0.7.0 as an annotated tag object"
else
  bad "--tag did not leave an annotated v0.7.0 on origin (rc $RC): $OUT"
fi
if [ "$(git -C "$work/origin.git" rev-parse 'v0.7.0^{commit}' 2>/dev/null)" = "$(git -C "$work/clone" rev-parse HEAD)" ]; then
  ok "and it points at the commit that was checked"
else
  bad "the pushed tag does not point at HEAD"
fi
cut v0.7.0
expect_refusal "a second run for a tag that now exists" "already exists"

step "3. changelog-gate, read out of release.yml, refuses before the tag exists"
fixture "## [0.7.0] — $week_ago"
cut v0.7.0
expect_refusal "a heading dated a week ago (the UTC date gate)" "changelog-gate would refuse"
case "$OUT" in
  *"$week_ago"*) ok "and the gate's own message, naming the stale date, is shown" ;;
  *) bad "the gate's own message was not passed through: $OUT" ;;
esac
case "$OUT" in
  *"Re-pushing a tag to the same commit cannot change"*) ok "and it does not advise re-pushing the same commit" ;;
  *) bad "the refusal does not say a re-push cannot fix a date: $OUT" ;;
esac
no_tag_anywhere "a stale date"

fixture "## [0.7.0] — $today"
cut v0.8.0
expect_refusal "a tag the changelog does not describe" "does not match"

step "4. [Unreleased] must be empty"
fixture "## [0.7.0] — $today" "- a fix nobody folded in"
cut v0.7.0
expect_refusal "an entry left under [Unreleased]" "[Unreleased] is not empty"
no_tag_anywhere "unfolded entries"

step "5. ci-gate: ci.yml AND security.yml green on main for HEAD"
fixture "## [0.7.0] — $today"
printf '%s' "$red_main" > "$work/runs/security.yml.json"
cut v0.7.0
expect_refusal "security.yml red on main" "Not proven green on main: security.yml"

fixture "## [0.7.0] — $today"
printf '%s' "$none" > "$work/runs/ci.yml.json"
cut v0.7.0
expect_refusal "no ci.yml run for this commit yet" "Not proven green on main: ci.yml"

step "6. A green rehearsal of this exact commit"
fixture "## [0.7.0] — $today"
printf '%s' "$none" > "$work/runs/release.yml.json"
cut v0.7.0
expect_refusal "no release.yml rehearsal at all" "no green release.yml rehearsal"

fixture "## [0.7.0] — $today"
printf '%s' "$green_main" > "$work/runs/release.yml.json"
cut v0.7.0
expect_refusal "a release.yml run from a push is a publish, not a rehearsal" "no green release.yml rehearsal"

fixture "## [0.7.0] — $today"
printf '%s' "$scheduled" > "$work/runs/release.yml.json"
cut v0.7.0
[ "$RC" -eq 0 ] && ok "the Tuesday scheduled rehearsal counts when it ran on this commit" \
                || bad "a green scheduled rehearsal of HEAD was refused: $OUT"

fixture "## [0.7.0] — $today"
rm -f "$work/runs/release.yml.json"
cut v0.7.0
expect_refusal "the API failing for release.yml (fails closed)" "could not ask the Actions API"

step "7. What gets tagged is exactly origin/main"
fixture "## [0.7.0] — $today"
echo "stray" >> "$work/clone/CHANGELOG.md"
cut v0.7.0
expect_refusal "uncommitted changes" "uncommitted changes"

fixture "## [0.7.0] — $today"
( cd "$work/clone" && git commit -q --allow-empty -m "not pushed" ) >/dev/null 2>&1
cut v0.7.0
expect_refusal "a local commit main does not have" "is not origin/main"

step "8. The clock and the argument"
fixture "## [0.7.0] — $today"
HHMM=2350 cut v0.7.0
expect_refusal "ten minutes before midnight UTC" "it is 23:50 UTC"
HHMM=0005 cut v0.7.0
[ "$RC" -eq 0 ] && ok "and five past midnight is fine (the leading zero is not octal)" \
                || bad "00:05 UTC was refused: $OUT"

cut 0.7.0
expect_refusal "a version without the v" "not a version tag"
cut v0.7.0 --push
[ "$RC" -eq 2 ] && ok "an unknown option prints usage and exits 2" || bad "an unknown option: rc $RC: $OUT"
no_tag_anywhere "bad arguments"

printf "\n\033[1mSummary\033[0m\n  %d passed, %d failed\n" "$pass" "$fail"
[ "$fail" -eq 0 ] || { printf "\n  \033[31mCUT-RELEASE TESTS FAILED\033[0m\n"; exit 1; }
printf "\n  \033[32mCUT-RELEASE TESTS PASSED\033[0m\n"
