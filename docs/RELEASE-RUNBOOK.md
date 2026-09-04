# Release runbook

The procedure for cutting a polyemesis release, and what each gate will refuse.

This exists because "a release runbook" was the last open item of Stage 4 in the
readiness audit, and because the gates below are the only part of the release
that has ever been exercised. Everything here is read out of
`.github/workflows/release.yml` and `scripts/test-release-gates.sh`; where this
page and those files disagree, **they are right and this page is stale** — say so
in an issue rather than working around it.

## Before you tag

**Rehearse.** `release.yml` has a `workflow_dispatch` with `dry_run`, defaulting
to `true`. Run it from the Actions tab on the commit you intend to tag. It builds
the images, cross-compiles every binary, produces the SBOM and the checksums, and
pushes **nothing**. A rehearsal that fails is a release that would have failed
halfway, which is the expensive way to find out.

> `dry_run` was once declared and read by nothing: setting it to `false` and
> expecting a publish got a silent no-op. `PUBLISH` is now derived explicitly
> (`release.yml:77`), which is why the rehearsal is trustworthy.

**Get the CHANGELOG right first, because a gate checks it.** `changelog-gate`
requires the pushed tag to match the top **dated** heading in `CHANGELOG.md`, and
a separate step requires `[Unreleased]` to be **empty** before a tag. Both fail
closed. `v0.7.0` exists as a tag precisely because CHANGELOG.md once claimed a
version had shipped when it had not (#499), so this is not ceremony.

**Checklist, in order:**

1. `main` is green — `ci-gate` requires a *successful* `ci.yml` run for the exact
   commit you are tagging, not merely a recent one.
2. `CHANGELOG.md`'s top heading is `## [X.Y.Z] — YYYY-MM-DD` with today's date
   and no `[Unreleased]` content beneath it.
3. `docs/UPGRADING.md` has a section for the version **if** it needs one — a
   migration that cannot be rolled back, a mandatory remediation, a changed
   default. 0.7.0's sealed stream keys are the worked example.
4. The rehearsal above is green.

## Cutting it

```sh
git checkout main && git pull --ff-only
git tag -a vX.Y.Z -m "vX.Y.Z"
git push origin vX.Y.Z
```

The tag push is what publishes. There is no button.

## What will stop you, and what each means

| Gate | Refuses when | What to do |
|---|---|---|
| `ci-gate` | no successful `ci.yml` run exists for this commit | do not re-run the release; fix the commit and tag again |
| `installer-gate` | `install.sh` does not parse, or its argument validator accepts bad input | a real defect in the installer — fix it, it is what operators run |
| `changelog-gate` | the tag and `CHANGELOG.md`'s top dated heading disagree | correct the CHANGELOG, delete the tag, re-tag |
| `[Unreleased] must be empty` | unreleased notes remain | move them under the dated heading |

**Deleting and re-pushing a tag is supported and is the normal fix** for a wrong
CHANGELOG date. The workflow's concurrency group is per-ref for exactly this
reason: a delete-and-re-push, or pressing "Re-run all jobs", once ran two
publishes of the same ref concurrently.

## Afterwards

- The GitHub Release, images on Docker Hub and GHCR (default, NVENC, VA-API),
  cross-compiled binaries, an SBOM and checksums are published by the run.
- **No host is upgraded by any of this.** A published release is not a deployed
  one, and the readiness audit tracks the two separately for that reason. The
  upgrade an operator performs — including the production host this project runs
  on, where it is deliberately manual — is `<installDir>/update.sh`, and
  `docs/UPGRADING.md` is the authority on what a given version costs.
- If a step fails *after* something was pushed, the release is partial. Say so in
  the GitHub Release notes rather than deleting it quietly: an operator who
  already pulled the image is in a different state from one who has not.

## What this page does not cover

Rolling back a release. There is no downgrade path — migrations run forward only,
and `docs/UPGRADING.md` is the authority on what that costs for a given version.
The rollback story is the operator's backup, which `update.sh` takes and verifies
before it does anything else.
