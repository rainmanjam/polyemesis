#!/usr/bin/env bash
# Sourced, never run. Reads a step's `run: |` body out of a workflow file.
#
# Shared by scripts/test-release-gates.sh, which drives release.yml's gates
# against fixtures, and scripts/cut-release.sh, which runs the same gates
# against the real repository before a tag exists. Both read the step bodies
# out of release.yml rather than carrying a copy: a copy of a gate goes on
# passing for years after the workflow stops containing it.
#
# Standard library only -- no PyYAML. The shape is pinned by extract_workflow_step
# itself: if the step is renamed or stops being a `run: |` block it prints
# nothing and exits non-zero, which is the correct outcome for a caller whose
# subject has moved.

extract_workflow_step() { # extract_workflow_step <workflow file> <step name> -> the step's run: body, dedented
  python3 - "$1" "$2" <<'PY'
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

# The names of the release.yml steps these scripts run. One spelling, here, so a
# rename in release.yml breaks both callers the same way. Used by the scripts
# that source this file, which shellcheck cannot see from here; the braces are
# only so one directive covers all three.
# shellcheck disable=SC2034
{
  STEP_CHANGELOG_GATE="Require the pushed tag to match CHANGELOG.md's top dated heading"
  STEP_UNRELEASED_EMPTY="[Unreleased] must be empty before a tag"
  STEP_CI_GATE="Require successful ci.yml and security.yml runs for this commit before publishing"
}
