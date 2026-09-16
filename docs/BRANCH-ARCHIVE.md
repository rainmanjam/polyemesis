# Branch archive

On 2026-09-16 the repository had 21 stale remote branches and 40 local ones.
`origin` is now `main` alone. This file records where the work went, because a
deleted branch is not a deleted commit and the difference is worth writing down.

## How a commit survives its branch

Three mechanisms, and only the first two are automatic:

| Mechanism | Covers | Durable? |
|---|---|---|
| `refs/pull/N/head` | any branch that ever had a pull request | yes — GitHub keeps it after the branch and the PR are gone |
| reachable from `main` | merged work | yes |
| an `archive/*` tag | everything else | only because it was made deliberately |

**A branch that never had a pull request has none of the first two.** Delete its
remote and its local copy and the commits are reachable from nothing; git will
garbage-collect them and no amount of recording the SHA brings them back. A SHA
written in a file is a receipt, not a backup.

## Checking which case a branch is in

Tip-SHA equality is the wrong test and gives a confidently wrong answer: a local
branch often sits at an *ancestor* of what was pushed, so its tip is not any PR
head while every one of its commits is preserved. Ask about reachability:

```sh
git fetch origin "refs/pull/$N/head:refs/prheads/$N"
git merge-base --is-ancestor "$SHA" refs/prheads/$N   # exit 0 = preserved
```

Run against tip equality instead, the same 24 branches came back 16 "local only".
Run against ancestry, 11. Five branches were one command away from being deleted
as expendable.

## Archived under `archive/*` — nothing else holds these

No pull request was ever opened, so these existed on one laptop. Tagged
2026-09-16 before the local branches were deleted.

| tag | tip |
|---|---|
| `archive/chore/wiki-publisher` | `55779b56` |
| `archive/feat/442-device-flow-ui` | `2b2ca432` |
| `archive/feat/442-twitch-device-flow` | `807d1d6f` |
| `archive/fix/398-header-only-detection` | `22d58310` |
| `archive/fix/398-relay-probe-window` | `9998786f` |
| `archive/fix/398-seam-pin` | `7c27e912` |
| `archive/fix/440-unsafe-pointer` | `188344e4` |
| `archive/fix/460-relay-consumer-probe` | `eb3c2ef3` |
| `archive/fix/656-coverage` | `eaf37ec3` |
| `archive/measure/398-rtmp` | `af84b780` |
| `archive/test/reconcile-teardown-suite` | `1a6ee751` |

Restore one with:

```sh
git checkout -b <branch> archive/<branch>
```

`archive/*` cannot fire a release: `release.yml` triggers on `tags: ["v*"]`, and
nothing else in `.github/workflows` is tag-triggered. `git describe --tags` on
`main` cannot reach them either, so `Footer.astro`'s version line is unaffected.

## Recoverable from GitHub — no tag needed

Each had a pull request, so `refs/pull/N/head` holds it. Restore with
`git fetch origin refs/pull/N/head:<branch>`.

| branch | PR |
|---|---|
| `_pr733` | #733 |
| `docs/status-and-path-to-production` | #650 |
| `feat/660-version-in-header` | #665 |
| `fix/643-backup-integrity` | #655 |
| `fix/644-config-explicit-missing` | #652 |
| `fix/647-throttle-xff-rightmost` | #653 |
| `fix/650-classify-audit-docs` | #650 |
| `fix/651-doc-drift-on-docs-only` | #654 |
| `fix/656-coverage-real` | #656 |
| `fix/663-loading-vs-empty` | #664 |
| `fix/readiness-criticals` | #656 |
| `fix/release-blockers` | #500 |
| `fix/sidebar-collapse-tooltips` | #662 |

## Not touched

`majors/ci`, `majors/docs`, `majors/ops`, `majors/sec`, `majors/site`,
`majors/srv`, `majors/ui`, `design/foundation`, `pr491`, `pr799` — local, never
pushed, never had a pull request, and deliberately left alone. `majors/ops`
alone is 20 commits. Nothing on GitHub holds any of them; if they are ever to be
deleted they want `archive/*` tags first, the same as the table above.

## The tag namespaces

`origin` carries three, and the split is the point:

| namespace | holds | who reads it |
|---|---|---|
| `v*` | releases | `release.yml`, `git describe --tags` on `main`, the site footer |
| `evidence/*` | measurement artifacts from #126 | anyone re-deriving that result |
| `archive/*` | commits nothing else holds | whoever needs a deleted branch back |

Only `v*` is load-bearing for automation. `release.yml` triggers on `tags: ["v*"]`
and nothing else under `.github/workflows` is tag-triggered, so neither of the
other two namespaces can fire a build. None of them is reachable from `main`, so
`git describe --tags` there cannot select one either.

### `archive/backup-pre-github` and `archive/rescue-main-17caf8b`

These two arrived by accident and are worth recording as such. `git push origin
--tags` pushes EVERY local tag, not the ones just created, so two July rescue
points — `5a902c6e` and `17caf8b6`, neither on `main` — went up alongside the
archive set under their bare names, sitting in the tag list beside the releases.

They were not deleted, because by then `origin` was their only remote copy and
removing them would have recreated the exact problem this file is about. They
were renamed under `archive/` instead, which is where the `evidence/*` precedent
says such a thing belongs.

**Push tags by name.** `--tags` is not a way of saying "the tags I just made".
