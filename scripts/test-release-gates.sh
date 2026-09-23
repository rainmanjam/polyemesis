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

# The extractor lives in lib-release-steps.sh because scripts/cut-release.sh
# runs these same step bodies against the real repository before a tag exists.
# shellcheck source=scripts/lib-release-steps.sh
. "$SCRIPTS/lib-release-steps.sh"
extract_step() { extract_workflow_step "$WORKFLOW" "$1"; }

# ------------------------------------------------------------ changelog-gate

GATE="$work/changelog-gate.sh"
extract_step "$STEP_CHANGELOG_GATE" > "$GATE"
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

step "7b. ci-gate requires security.yml as well as ci.yml, driven for real"
# Staging-readiness row 37. ci-gate asked about ci.yml alone, and branch
# protection is not strict, so a commit whose security.yml run on main was
# red -- a new CVE in a dependency, a secret that gitleaks caught -- could be
# tagged and published. The step is extracted and run against a stub `gh` that
# answers per workflow file, so what is tested is the gate, not a copy of it.
CIGATE="$work/ci-gate.sh"
extract_step "$STEP_CI_GATE" > "$CIGATE"
if [ ! -s "$CIGATE" ]; then
  bad "could not extract ci-gate's step from release.yml"
elif ! command -v jq >/dev/null 2>&1; then
  bad "jq is not installed, so ci-gate cannot be driven"
else
  stub="$work/stub-bin"; mkdir -p "$stub" "$work/runs"
  # A stand-in for `gh api <url>`: prints $work/runs/<workflow file>.json, or
  # fails like the API does when that file is absent.
  cat > "$stub/gh" <<STUB
#!/usr/bin/env bash
url="\$2"
wf="\${url#*/actions/workflows/}"; wf="\${wf%%/*}"
f="$work/runs/\$wf.json"
[ -f "\$f" ] || { echo "HTTP 404" >&2; exit 1; }
cat "\$f"
STUB
  chmod +x "$stub/gh"
  green='{"workflow_runs":[{"status":"completed","conclusion":"success","event":"push","head_branch":"main"}]}'
  red='{"workflow_runs":[{"status":"completed","conclusion":"failure","event":"push","head_branch":"main"}]}'
  none='{"workflow_runs":[]}'
  run_cigate() { # run_cigate <ci.yml json> <security.yml json>
    printf '%s' "$1" > "$work/runs/ci.yml.json"
    printf '%s' "$2" > "$work/runs/security.yml.json"
    ( PATH="$stub:$PATH" PUBLISH=true REPO=o/r SHA=abc123 GH_TOKEN=x bash "$CIGATE" ) > "$work/cigate.out" 2>&1
    GATE_RC=$?
    GATE_OUT="$(cat "$work/cigate.out")"
  }

  run_cigate "$green" "$green"
  [ "$GATE_RC" -eq 0 ] && ok "both green on main: the gate passes" \
                       || bad "both workflows green and the gate still refused: $GATE_OUT"

  run_cigate "$green" "$red"
  expect_refusal "ci.yml green but security.yml red" "$GATE_OUT" "Not proven green on main: security.yml"

  run_cigate "$green" "$none"
  expect_refusal "ci.yml green and no security.yml run at all" "$GATE_OUT" "Not proven green on main: security.yml"

  run_cigate "$red" "$green"
  expect_refusal "security.yml green but ci.yml red" "$GATE_OUT" "Not proven green on main: ci.yml"

  rm -f "$work/runs/security.yml.json"
  printf '%s' "$green" > "$work/runs/ci.yml.json"
  ( PATH="$stub:$PATH" PUBLISH=true REPO=o/r SHA=abc123 GH_TOKEN=x bash "$CIGATE" ) > "$work/cigate.out" 2>&1
  GATE_RC=$?; GATE_OUT="$(cat "$work/cigate.out")"
  expect_refusal "the API failing for security.yml (fails closed)" "$GATE_OUT" "Could not reach the Actions API to check security.yml"
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
import re, sys, yaml
w = yaml.safe_load(open(sys.argv[1], encoding="utf-8"))
try:
    print("yes" if eval(sys.argv[2], {"w": w, "re": re}) else "no")
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

  # THE WIRING IS THE GATE. Every assertion in this file drives a gate's BODY
  # standalone, extracted out of the YAML -- so all of them stay green whether
  # or not release.yml actually waits on the job that runs it. `binaries.needs`
  # is the only thing that makes a gate block a publish, and only installer-gate
  # was ever named here: deleting ci-gate or changelog-gate from that list
  # publishes a release past both of them with this suite still passing.
  #
  # Derived from the job names rather than a second hardcoded list, because a
  # list to keep in step with another list is the thing that drifted. A job
  # calling itself "(fail closed)" and not being waited on is now a failure.
  fail_closed='set(k for k, v in w["jobs"].items() if "(fail closed)" in v.get("name", ""))'
  # POSITIVE CONTROL FIRST: a subset test over an empty set is vacuously true,
  # so rename the jobs and the assertion below would pass over nothing.
  have "the workflow still labels its blocking gates (fail closed)" \
       "len(${fail_closed}) >= 3"
  have "and the publish waits on every one of them" \
       "${fail_closed} <= set(w[\"jobs\"][\"binaries\"][\"needs\"])"

  # #531/#557/#584: generate_release_notes alone is a list of merged PR titles.
  # The three facts an operator needs BEFORE they upgrade were in no note at all.
  body_step='[s for s in w["jobs"]["binaries"]["steps"] if s.get("name") == "Publish GitHub Release"][0]'
  have "the release body is written, not left to autogeneration" \
       "bool(${body_step}['with'].get('body'))"
  have "and generate_release_notes is still on, so the body adds rather than replaces" \
       "${body_step}['with'].get('generate_release_notes') is True"
  # "truncated", not "#440": #440 was fixed in 0.9.0, and requiring its number
  # here is what kept the release body calling it unresolved afterwards. The
  # Windows defect an operator still has to plan around is the truncation.
  for phrase in "one-way" "secret.key" "install.sh" "truncated"; do
    have "the release body warns about ${phrase}" \
         "'${phrase}' in ${body_step}['with']['body']"
  done

  step "10. What the release publishes carries build provenance"
  # Staging-readiness row 12. SHA256SUMS is published by the same release as
  # the binaries, so a replaced binary can come with a replaced sums line --
  # and install.sh and the in-app upgrade check against exactly that file. A
  # provenance attestation is signed with the workflow's OIDC identity and kept
  # by GitHub, outside the release. None of this can run without a real tag, so
  # its shape is what can be asserted here.
  attest='lambda job: [s for s in w["jobs"][job]["steps"] if s.get("uses", "").startswith("actions/attest-build-provenance@")]'
  names='lambda job: [s.get("name") for s in w["jobs"][job]["steps"]]'
  have "the binaries job attests what it publishes" \
       "len((${attest})('binaries')) == 1"
  have "every attest step is SHA-pinned, like every other action here" \
       "all(re.fullmatch(r'actions/attest-build-provenance@[0-9a-f]{40}', s['uses']) for j in ('binaries', 'images') for s in (${attest})(j))"
  have "and it attests every file SHA256SUMS lists" \
       "(${attest})('binaries')[0]['with'].get('subject-checksums') == 'dist/SHA256SUMS'"
  # After the checksums exist and before the release that ships them: an
  # attestation step placed after Publish GitHub Release would leave a window,
  # or a failed publish, with binaries out and nothing vouching for them.
  have "after Checksums and before the release is published" \
       "(${names})('binaries').index('Checksums') < (${names})('binaries').index((${attest})('binaries')[0]['name']) < (${names})('binaries').index('Publish GitHub Release')"
  # One attestation per pushed image, each tied to that build step's digest.
  # Counting attest steps alone would pass with three that all name the
  # default image's digest.
  builds='[s for s in w["jobs"]["images"]["steps"] if s.get("uses", "").startswith("docker/build-push-action@")]'
  have "the images job still builds three images (positive control)" \
       "len(${builds}) == 3"
  have "and attests each one's own pushed digest" \
       "sorted(s['with'].get('subject-digest', '') for s in (${attest})('images')) == sorted('\${{ steps.%s.outputs.digest }}' % b.get('id') for b in ${builds})"
  # Only when publishing: a dry run's artefacts are discarded, and attesting
  # them writes to the public transparency log about files nobody can fetch.
  have "and attests only when it publishes" \
       "all(s.get('if') == \"env.PUBLISH == 'true'\" for j in ('binaries', 'images') for s in (${attest})(j))"
  # The OIDC token is a signing identity. It belongs to the two jobs that
  # attest and to nothing else -- not the gates, not the workflow as a whole.
  have "id-token: write is granted to the two publishing jobs and no others" \
       "'id-token' not in (w.get('permissions') or {}) and {k for k, v in w['jobs'].items() if (v.get('permissions') or {}).get('id-token') == 'write'} == {'binaries', 'images'}"
  have "and so is attestations: write" \
       "{k for k, v in w['jobs'].items() if (v.get('permissions') or {}).get('attestations') == 'write'} == {'binaries', 'images'}"

  step "11. The whole release is rehearsed every week, whether or not anyone asks"
  # Staging-readiness row 23. RELEASE-RUNBOOK.md asks for a dry run on the
  # commit being tagged; the last one before 0.10.0 was six weeks old, and the
  # v0.7.0, v0.8.0 and v0.9.0 tag runs all failed first. The GPU images, the
  # arm64 build and the SBOM are built ONLY here -- ci.yml and security.yml
  # build none of them -- so a week of drift in any of them surfaced as a
  # failed release. A schedule turns that into a failed Tuesday.
  #
  # PyYAML reads the bare key `on:` as the boolean True (YAML 1.1), hence the
  # fallback.
  trig='(w.get("on") or w.get(True) or {})'
  have "release.yml runs on a schedule" \
       "bool(${trig}.get('schedule')) and all(c.get('cron') for c in ${trig}['schedule'])"
  have "and the version tag is still a trigger (positive control)" \
       "'v*' in ${trig}['push']['tags']"
fi

# A scheduled run must PUBLISH NOTHING. That property lives in one expression,
# env.PUBLISH, and a schedule has no dry_run input at all -- so the question is
# what that expression makes of an event with no inputs. It is evaluated here
# for each event rather than asserted as a string, so a rewrite that reads the
# same and behaves differently is caught.
publish_for() { # publish_for <event> <dry_run: true|false|none> -> true|false
  python3 - "$WORKFLOW" "$1" "$2" <<'PY'
import re, sys, yaml
w = yaml.safe_load(open(sys.argv[1], encoding="utf-8"))
expr = w["env"]["PUBLISH"].strip()
m = re.fullmatch(r"\$\{\{(.*)\}\}", expr, re.S)
if not m:
    sys.exit("PUBLISH is not a single ${{ }} expression: " + expr)
body = m.group(1)
# Only the operators and names this expression is written with; anything else
# is an error, not a guess.
allowed = re.sub(r"github\.event_name|inputs\.dry_run|'[a-z_]+'|==|\|\||&&|!|\(|\)|\s", "", body)
if allowed:
    sys.exit("PUBLISH uses something this evaluator does not model: " + allowed)
py = (body.replace("||", " or ").replace("&&", " and ")
          .replace("!inputs.dry_run", " (not dry_run) ")
          .replace("github.event_name", "event"))
dry = {"true": True, "false": False, "none": None}[sys.argv[3]]
# eval over a string built above from release.yml's own PUBLISH expression,
# after the allow-list check: names, quotes, comparisons and boolean operators
# only.
print("true" if eval(py, {"__builtins__": {}}, {"event": sys.argv[2], "dry_run": dry}) else "false")
PY
}
if python3 -c 'import yaml' 2>/dev/null; then
  got="$(publish_for schedule none 2>&1)"
  [ "$got" = false ] && ok "a scheduled run publishes nothing (PUBLISH evaluates false)" \
                     || bad "a scheduled run would PUBLISH: got $got"
  # Controls, so the evaluator cannot pass by answering false to everything.
  got="$(publish_for push none 2>&1)"
  [ "$got" = true ] && ok "while a tag push still publishes" || bad "a tag push no longer publishes: got $got"
  got="$(publish_for workflow_dispatch false 2>&1)"
  [ "$got" = true ] && ok "and so does a dispatch with dry_run: false" || bad "dispatch dry_run=false: got $got"
  got="$(publish_for workflow_dispatch true 2>&1)"
  [ "$got" = false ] && ok "and a dispatch with dry_run: true does not" || bad "dispatch dry_run=true: got $got"
fi

printf "\n\033[1mSummary\033[0m\n  %d passed, %d failed\n" "$pass" "$fail"
[ "$fail" -eq 0 ] || { printf "\n  \033[31mRELEASE GATE TESTS FAILED\033[0m\n"; exit 1; }
printf "\n  \033[32mRELEASE GATE TESTS PASSED\033[0m\n"
