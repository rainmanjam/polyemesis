#!/usr/bin/env bash
# Red/green fixtures for the gates in .github/workflows/release.yml.
#
# WHY THIS EXISTS, and it is the same argument scripts/test-sbom-guard.sh makes
# about itself: these gates only ever run for real inside a release. That is
# exactly how one of them came to be wrong. changelog-gate's first branch read
#
#     if [ "${REF_TYPE}" != "tag" ]; then ... exit 0
#
# with a comment asserting "Nothing here would be published either way (see
# PUBLISH)" -- and PUBLISH is precisely what makes that false. A
# workflow_dispatch with dry_run: false publishes for real, from a branch, with
# the gate stepped aside: github.ref is then refs/heads/main, which contains no
# hyphen, so :latest is enabled, and install.sh writes `image: <IMAGE>:latest`
# into every docker operator's compose file. Nobody could have discovered that
# without cutting a release, which is the property this file removes.
#
# THE STEP BODIES ARE READ OUT OF release.yml, never transcribed. A test
# carrying its own copy of the gate would go on passing for years after the
# workflow stopped containing it.
set -uo pipefail

SCRIPTS="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$SCRIPTS/.." && pwd)"
WORKFLOW="$ROOT/.github/workflows/release.yml"

pass=0; fail=0
ok()  { printf "  \033[32mPASS\033[0m  %s\n" "$1"; pass=$((pass+1)); }
bad() { printf "  \033[31mFAIL\033[0m  %s\n" "$1"; fail=$((fail+1)); }
step(){ printf "\n\033[1m%s\033[0m\n" "$1"; }

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT INT TERM

# Standard library only -- no PyYAML. The shape is pinned by extract_step
# itself: if the step is renamed or stops being a `run: |` block it prints
# nothing and every case below fails loudly, which is the correct outcome for a
# test whose subject has moved.
extract_step() { # extract_step <step name> -> the step's run: body, dedented
  python3 - "$WORKFLOW" "$1" <<'PY'
import sys
path, want = sys.argv[1], sys.argv[2]
lines = open(path, encoding="utf-8").read().splitlines()
i = 0
while i < len(lines):
    s = lines[i].strip()
    if s in ("- name: " + want, '- name: "' + want + '"'):
        break
    i += 1
else:
    sys.exit("step not found: " + want)
while i < len(lines) and lines[i].strip() != "run: |":
    i += 1
    if i < len(lines) and lines[i].lstrip().startswith("- name:"):
        sys.exit("no `run: |` before the next step in: " + want)
i += 1
indent = len(lines[i]) - len(lines[i].lstrip())
out = []
while i < len(lines):
    ln = lines[i]
    if ln.strip() and (len(ln) - len(ln.lstrip())) < indent:
        break
    out.append(ln[indent:] if len(ln) >= indent else ln)
    i += 1
print("\n".join(out).rstrip())
PY
}

# ------------------------------------------------------------ changelog-gate

GATE="$work/changelog-gate.sh"
extract_step "Require the pushed tag to match CHANGELOG.md's top dated heading" > "$GATE"
if [ ! -s "$GATE" ]; then
  bad "could not extract changelog-gate's script from release.yml"
  printf "\n\033[1mSummary\033[0m\n  %d passed, %d failed\n" "$pass" "$fail"
  exit 1
fi

step "1. The extracted gate is the real one"
bash -n "$GATE" && ok "changelog-gate's script parses" || bad "changelog-gate's script has a syntax error"

# A repository with a real tag object, because the annotated-tag check reads one.
mkrepo() { # mkrepo <dir> <heading line> <tag> <annotated|lightweight>
  local d="$1" heading="$2" tag="$3" kind="$4"
  rm -rf "$d"; mkdir -p "$d"
  {
    printf '# Changelog\n\n## [Unreleased]\n\nNothing yet.\n\n%s\n\n- something\n\n## [0.6.0] — 2026-08-09\n\n- older\n' "$heading"
  } > "$d/CHANGELOG.md"
  ( cd "$d" || exit 1
    git init -q .
    git config user.email t@example.invalid
    git config user.name test
    git add CHANGELOG.md
    git commit -qm init
    if [ "$kind" = annotated ]; then git tag -a "$tag" -m "release $tag"; else git tag "$tag"; fi
  ) >/dev/null 2>&1
}

# Output to a FILE, not through a command substitution: `x="$(...)"; rc=$?`
# reads the substitution's own subshell, and stashing the status in a variable
# set inside it does not survive. Both halves have to come back, so the caller
# reads GATE_RC and GATE_OUT after the call rather than capturing either.
GATE_RC=0
GATE_OUT=""
run_gate() { # run_gate <dir> <ref_name> <ref_type> <publish>
  ( cd "$1" && REF_NAME="$2" REF_TYPE="$3" PUBLISH="$4" bash "$GATE" ) > "$work/gate.out" 2>&1
  GATE_RC=$?
  GATE_OUT="$(cat "$work/gate.out")"
}

expect_refusal() { # expect_refusal <label> <output> <substring>
  local label="$1" out="$2" want="$3"
  if [ "$GATE_RC" -eq 0 ]; then
    bad "$label: the gate exited 0 — the release would have published"
    return
  fi
  case "$out" in
    *"$want"*) ok "$label, and the message names it: \"$want\"" ;;
    *) bad "$label: it refused, but never says \"$want\": $(printf '%s' "$out" | tr '\n' ' ')" ;;
  esac
}

today="$(date -u +%Y-%m-%d)"
week_ago="$(date -u -d '7 days ago' +%Y-%m-%d 2>/dev/null || date -u -v-7d +%Y-%m-%d)"

step "2. A publishing run from anything but a tag is refused"
# THE ONE THIS FILE EXISTS FOR. `gh workflow run release.yml --ref main
# -f dry_run=false` -- to "test a real publish", or to re-drive a release
# without re-pushing the tag -- used to walk straight past this gate.
mkrepo "$work/r1" "## [0.7.0] — $today" v0.7.0 annotated
run_gate "$work/r1" main branch true; out="$GATE_OUT"
expect_refusal "a workflow_dispatch on a branch with dry_run: false" "$out" "Only a version tag can publish"

step "3. A rehearsal that publishes nothing is still allowed to rehearse"
run_gate "$work/r1" docs/accuracy-pass branch false; out="$GATE_OUT"
if [ "$GATE_RC" -eq 0 ]; then
  ok "a dry-run dispatch from a branch passes, as it must — that is what rehearsing is"
else
  bad "the gate refused a dry run, which blocks the one case workflow_dispatch exists to serve: $out"
fi

step "4. The heading must be dated TODAY, not merely dated"
# #530: the old check asserted only that a date was present. The heading is
# written while the release is prepared and the tag is often held for days --
# which is what happened to 0.7.0 -- so a heading dated last week published
# happily and the notes then asserted a ship date the artefacts contradict.
mkrepo "$work/r2" "## [0.7.0] — $week_ago" v0.7.0 annotated
run_gate "$work/r2" v0.7.0 tag true; out="$GATE_OUT"
expect_refusal "a heading dated a week ago" "$out" "$week_ago"

mkrepo "$work/r3" "## [0.7.0] — unreleased" v0.7.0 annotated
run_gate "$work/r3" v0.7.0 tag true; out="$GATE_OUT"
expect_refusal "an undated heading" "$out" "is not dated"

mkrepo "$work/r4" "## [0.7.0] — $today" v0.7.0 annotated
run_gate "$work/r4" v0.7.0 tag true; out="$GATE_OUT"
if [ "$GATE_RC" -eq 0 ]; then
  ok "a tag whose heading is dated today is accepted"
else
  bad "the gate refused a correct release: $out"
fi

step "5. The tag and the heading must name the same version"
# Its own repository, with a real annotated v0.8.0 in it: asking r4 about a tag
# it does not contain would be refused by the annotated-tag check first, and
# this case would pass for the wrong reason.
mkrepo "$work/r6" "## [0.7.0] — $today" v0.8.0 annotated
run_gate "$work/r6" v0.8.0 tag true; out="$GATE_OUT"
expect_refusal "a tag the changelog does not describe" "$out" "does not match"

step "6. The tag must be annotated"
# #588: a lightweight `git tag v0.7.0` publishes identically and leaves no
# tagger, date or message in the object, so the only record of who cut a
# release is an Actions run that eventually expires.
mkrepo "$work/r5" "## [0.7.0] — $today" v0.7.0 lightweight
run_gate "$work/r5" v0.7.0 tag true; out="$GATE_OUT"
expect_refusal "a lightweight tag" "$out" "not an annotated tag"

# ------------------------------------------------------------------- ci-gate

step "7. ci-gate accepts only a green ci.yml run from a push to main"
# #555: ci.yml's suite picker keeps the full acceptance set only for `schedule`
# or refs/heads/main. Every other ref falls through to `suites=[]`, the
# container matrix is empty, those jobs are skipped, and the run's conclusion is
# still `success` -- so a manual workflow_dispatch on a hotfix branch satisfied
# a gate whose name promises the matrix ran.
if ! command -v jq >/dev/null 2>&1; then
  bad "jq is not installed, so ci-gate's selector cannot be exercised"
else
  SEL="$(grep -o 'select([^)]*)' "$WORKFLOW" | grep 'conclusion == "success"' | head -1)"
  if [ -z "$SEL" ]; then
    bad "could not find ci-gate's jq selector in release.yml"
  else
    count_with() { # count_with <json>
      printf '%s' "$1" | jq "[.workflow_runs[]? | ${SEL}] | length"
    }
    n="$(count_with '{"workflow_runs":[{"status":"completed","conclusion":"success","event":"push","head_branch":"main"}]}')"
    [ "$n" = 1 ] && ok "a green push-to-main run counts" || bad "a green push-to-main run was not counted (got $n)"

    n="$(count_with '{"workflow_runs":[{"status":"completed","conclusion":"success","event":"workflow_dispatch","head_branch":"hotfix"}]}')"
    [ "$n" = 0 ] && ok "a manual dispatch on a branch does NOT count — it never ran the acceptance matrix" \
                 || bad "a workflow_dispatch run satisfied the CI gate (got $n)"

    n="$(count_with '{"workflow_runs":[{"status":"completed","conclusion":"success","event":"pull_request","head_branch":"feature"}]}')"
    [ "$n" = 0 ] && ok "a pull_request run does not count either" || bad "a pull_request run satisfied the CI gate (got $n)"

    n="$(count_with '{"workflow_runs":[{"status":"completed","conclusion":"failure","event":"push","head_branch":"main"}]}')"
    [ "$n" = 0 ] && ok "a red run on main does not count" || bad "a failed run satisfied the CI gate (got $n)"
  fi
fi

# -------------------------------------------------------------- GPU image tags

step "8. The floating GPU tags are withheld from a prerelease, like :latest"
# #556: :latest is correctly withheld for a tag containing a hyphen, but :cuda
# and :vaapi were unconditional literals, so tagging v0.7.0-rc1 to rehearse a
# release silently made an rc build the tag `docker pull …:cuda` resolves.
# #585: and the versioned GPU tags used VERSION (v0.7.0) while metadata-action's
# {{version}} strips the v (0.7.0), so :0.7.0-cuda -- the spelling a reader
# infers from :0.7.0 -- did not exist.
DERIVE="$work/derive.sh"
extract_step "Derive a ref-safe version and the GPU image tags" > "$DERIVE"
if [ ! -s "$DERIVE" ]; then
  bad "could not extract the GPU tag derivation from release.yml"
else
  bash -n "$DERIVE" && ok "the derivation script parses" || bad "the derivation script has a syntax error"

  derive() { # derive <ref name>  -> prints the resulting GITHUB_ENV
    local env_file="$work/ghenv"
    : > "$env_file"
    ( export GITHUB_REF_NAME="$1" \
             GITHUB_REF="refs/tags/$1" \
             GITHUB_ENV="$env_file" \
             IMAGE="rainmanjam/polyemesis" \
             GHCR_IMAGE="ghcr.io/rainmanjam/polyemesis"
      bash "$DERIVE" ) >/dev/null 2>&1
    cat "$env_file"
  }

  got="$(derive v0.7.0)"
  case "$got" in
    *"rainmanjam/polyemesis:cuda"*) ok "a release tag publishes the floating :cuda" ;;
    *) bad "a release tag did not produce the floating :cuda tag: $(printf '%s' "$got" | tr '\n' ' ')" ;;
  esac
  case "$got" in
    *"rainmanjam/polyemesis:0.7.0-cuda"*) ok "and :0.7.0-cuda, in metadata-action's namespace" ;;
    *) bad "the versioned GPU tag is not :0.7.0-cuda: $(printf '%s' "$got" | tr '\n' ' ')" ;;
  esac
  case "$got" in
    *":v0.7.0-cuda"*) bad "still publishing :v0.7.0-cuda, a second spelling of the same release" ;;
    *) ok "and not :v0.7.0-cuda, which is the spelling nothing else in the registry uses" ;;
  esac

  got="$(derive v0.7.0-rc1)"
  case "$got" in
    *"polyemesis:cuda"*) bad "a PRERELEASE still publishes the floating :cuda — an rc becomes what docker pull …:cuda resolves" ;;
    *) ok "a prerelease withholds the floating :cuda, exactly as it withholds :latest" ;;
  esac
  case "$got" in
    *"polyemesis:vaapi"$'\n'*|*"polyemesis:vaapi") bad "a prerelease still publishes the floating :vaapi" ;;
    *) ok "and the floating :vaapi with it — the guard covers the whole set, not one member" ;;
  esac
  case "$got" in
    *"rainmanjam/polyemesis:0.7.0-rc1-cuda"*) ok "the rc still gets its own versioned tag, so rehearsing still produces something pullable" ;;
    *) bad "the prerelease produced no versioned GPU tag at all: $(printf '%s' "$got" | tr '\n' ' ')" ;;
  esac
fi

# ---------------------------------------------- properties of the file itself
#
# Four things that are not scripts and so cannot be driven, only asserted. Each
# is a fact a release depends on and nothing else in this repository states.

step "9. The workflow's own structure"

# eval() over an expression written a few lines below in this same file, never
# over anything read from the workflow, the environment or a caller. The only
# untrusted thing here is release.yml, and it arrives as parsed DATA in `w`.
have() { # have <label> <python expression over the parsed workflow>
  local label="$1" expr="$2" got
  got="$(python3 - "$WORKFLOW" "$expr" <<'PY'
import sys, yaml
w = yaml.safe_load(open(sys.argv[1], encoding="utf-8"))
try:
    print("yes" if eval(sys.argv[2], {"w": w}) else "no")
except Exception as e:  # a missing key is a "no", not a crash
    print("no (%s)" % e)
PY
)"
  [ "$got" = yes ] && ok "$label" || bad "$label — got $got"
}

if ! python3 -c 'import yaml' 2>/dev/null; then
  bad "PyYAML is unavailable, so the structural assertions cannot run"
else
  # #554: delete and re-push a tag while the first run is in flight, or press
  # "Re-run all jobs", and two runs execute the same publish steps at once --
  # softprops/action-gh-release UPDATES an existing release rather than refusing
  # one, so both upload dist/* to it. Every other workflow here has a group.
  have "release.yml has a concurrency group" \
       'bool(w.get("concurrency", {}).get("group"))'
  # cancel-in-progress must stay FALSE: cancelling mid-push leaves a container
  # tag with no release behind it, which is what `needs: binaries` prevents.
  have "and it queues rather than cancelling a publish in flight" \
       'w["concurrency"].get("cancel-in-progress") is False'

  # #559: the installer is what every operator uses, and it was the one thing
  # on the critical path with nothing asserting it.
  have "an installer-gate job exists" '"installer-gate" in w["jobs"]'
  have "and the publish waits on it" \
       '"installer-gate" in w["jobs"]["binaries"]["needs"]'

  # #531/#557/#584: generate_release_notes alone is a list of merged PR titles.
  # The three facts an operator needs BEFORE they upgrade were in no note at all.
  body_step='[s for s in w["jobs"]["binaries"]["steps"] if s.get("name") == "Publish GitHub Release"][0]'
  have "the release body is written, not left to autogeneration" \
       "bool(${body_step}['with'].get('body'))"
  have "and generate_release_notes is still on, so the body adds rather than replaces" \
       "${body_step}['with'].get('generate_release_notes') is True"
  for phrase in "one-way" "secret.key" "install.sh" "#440"; do
    have "the release body warns about ${phrase}" \
         "'${phrase}' in ${body_step}['with']['body']"
  done
fi

printf "\n\033[1mSummary\033[0m\n  %d passed, %d failed\n" "$pass" "$fail"
[ "$fail" -eq 0 ] || { printf "\n  \033[31mRELEASE GATE TESTS FAILED\033[0m\n"; exit 1; }
printf "\n  \033[32mRELEASE GATE TESTS PASSED\033[0m\n"
