#!/usr/bin/env bash
# Delete the GitHub Actions caches that can never be restored again.
#
# WHY THIS IS NOT JUST "LET GITHUB EVICT THEM". GitHub evicts least-recently-
# used across the WHOLE repository once the 10 GB limit is reached. That policy
# has no idea which caches are still reachable, so at the limit it will happily
# evict a live main-branch Go module cache -- restored by every pull request --
# in order to keep a cache belonging to a pull request that merged last week and
# whose ref no longer exists. Deleting the unreachable ones on a schedule means
# the eviction never has to make that choice.
#
# Measured on this repository the first time it was run: 150 caches at 9.65 GB,
# of which 6.40 GB belonged to merged pull requests, release tags and deleted
# branches. After: 41 caches at 3.25 GB, all of them on main.
#
# FOUR TIERS, SAFEST FIRST.
#
#   1. merged or closed pull requests -- the ref is gone, nothing can restore it
#   2. release tags -- a future release makes a new tag with a new ref
#   3. deleted branches -- same reasoning as 1
#   4. superseded caches on live refs -- same restore-key prefix, older entry
#
# Tiers 1-3 are unreachable by construction. Tier 4 needs judgement and is the
# only one that could cost anything, so it keeps the newest KEEP_PER_KEY of each
# prefix rather than only the newest.
#
#   ./scripts/prune-actions-caches.sh            # delete
#   ./scripts/prune-actions-caches.sh --dry-run  # report only
#
# Environment:
#   REPO             owner/name (default: derived from the git remote)
#   KEEP_PER_KEY     how many to keep per restore-key prefix (default 2)
set -uo pipefail

DRY=0
[ "${1:-}" = "--dry-run" ] && DRY=1

REPO="${REPO:-$(gh repo view --json nameWithOwner --jq .nameWithOwner 2>/dev/null)}"
[ -n "$REPO" ] || { echo "cannot determine the repository"; exit 1; }
KEEP_PER_KEY="${KEEP_PER_KEY:-2}"

say()  { printf '%s\n' "$*"; }
note() { printf '    %s\n' "$*"; }

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

say "repository: $REPO"
[ "$DRY" = 1 ] && say "DRY RUN -- nothing will be deleted"

# ---------------------------------------------------------------- gather
gh api --paginate "repos/$REPO/actions/caches?per_page=100" \
  --jq '.actions_caches[] | [.id, .key, .size_in_bytes, .created_at, .ref] | @tsv' \
  > "$TMP/caches.tsv" 2>/dev/null || { echo "could not list caches"; exit 1; }

TOTAL_N=$(wc -l < "$TMP/caches.tsv" | tr -d ' ')
[ "$TOTAL_N" -gt 0 ] || { say "no caches"; exit 0; }

# The live branches, so a cache on a deleted one can be told from a cache on a
# branch that simply has not been touched today.
git ls-remote --heads origin 2>/dev/null | sed 's|.*refs/heads/|refs/heads/|' > "$TMP/branches.txt"

# Pull request states, asked once per distinct PR rather than once per cache.
cut -f5 "$TMP/caches.tsv" | sed -n 's|^refs/pull/\([0-9]*\)/merge$|\1|p' | sort -u > "$TMP/prs.txt"
: > "$TMP/prstate.tsv"
while read -r pr; do
  [ -n "$pr" ] || continue
  st=$(gh api "repos/$REPO/pulls/$pr" --jq 'if .merged then "MERGED" else .state | ascii_upcase end' 2>/dev/null || echo GONE)
  printf '%s\t%s\n' "$pr" "$st" >> "$TMP/prstate.tsv"
done < "$TMP/prs.txt"

# ---------------------------------------------------------------- classify
python3 - "$TMP" "$KEEP_PER_KEY" <<'PY' > "$TMP/verdict.tsv"
import csv, os, re, sys
tmp, keep_n = sys.argv[1], int(sys.argv[2])

rows = list(csv.reader(open(f"{tmp}/caches.tsv"), delimiter="\t"))
branches = {l.strip() for l in open(f"{tmp}/branches.txt")}
state = {}
for line in open(f"{tmp}/prstate.tsv"):
    parts = line.rstrip("\n").split("\t")
    if len(parts) == 2:
        state[parts[0]] = parts[1]

# CONTENT-ADDRESSED KEYS ARE NEVER SUPERSEDED. buildkit stores each Docker layer
# under its own sha256, so two entries sharing a prefix are two different layers
# and both are live. Treating them like a lockfile-hashed cache -- where only the
# newest match is ever restored -- would delete layers that are still in use, and
# would look like a tidy-up right up until container builds got slower.
CONTENT_ADDRESSED = re.compile(r"(^|-)sha256:")

live, dead = [], []
for cid, key, size, created, ref in rows:
    reason = None
    m = re.match(r"^refs/pull/(\d+)/merge$", ref)
    if m:
        if state.get(m.group(1), "GONE") != "OPEN":
            reason = f"pull request #{m.group(1)} is {state.get(m.group(1), 'gone')}"
    elif ref.startswith("refs/heads/refs/tags/"):
        reason = "release tag; a future release creates a new ref"
    elif ref.startswith("refs/heads/") and ref not in branches:
        reason = "branch no longer exists"
    (dead if reason else live).append((cid, key, int(size), created, ref, reason))

for cid, key, size, created, ref, reason in dead:
    print(f"DELETE\t{cid}\t{size}\t{ref}\t{reason}")

# Tier 4, over what survived: same restore-key prefix, keep the newest few.
groups = {}
for cid, key, size, created, ref, _ in live:
    if CONTENT_ADDRESSED.search(key):
        continue
    prefix = "-".join(key.split("-")[:-1]) or key
    groups.setdefault((ref, prefix), []).append((created, cid, size, key))

for (ref, prefix), entries in groups.items():
    entries.sort(reverse=True)
    for created, cid, size, key in entries[keep_n:]:
        print(f"DELETE\t{cid}\t{size}\t{ref}\tsuperseded; newer {keep_n} kept for {prefix[:48]}")
PY

# ---------------------------------------------------------------- report
python3 - "$TMP" <<'PY'
import csv, sys
tmp = sys.argv[1]
allrows = list(csv.reader(open(f"{tmp}/caches.tsv"), delimiter="\t"))
dead = [l.rstrip("\n").split("\t") for l in open(f"{tmp}/verdict.tsv") if l.strip()]
tot = sum(int(r[2]) for r in allrows)
kill = sum(int(r[2]) for r in dead)
print(f"    {len(allrows):3d} caches, {tot/1e9:5.2f} GB of the 10 GB limit")
print(f"    {len(dead):3d} unreachable, {kill/1e9:5.2f} GB")
print(f"    {len(allrows)-len(dead):3d} kept, {(tot-kill)/1e9:5.2f} GB")
from collections import defaultdict
g = defaultdict(lambda: [0, 0])
for r in dead:
    g[r[4]][0] += 1
    g[r[4]][1] += int(r[2])
if g:
    print("\n    why:")
    for k, (n, s) in sorted(g.items(), key=lambda kv: -kv[1][1]):
        print(f"      {s/1e9:5.2f} GB {n:3d}x  {k}")
PY

DEAD_N=$(wc -l < "$TMP/verdict.tsv" | tr -d ' ')
if [ "$DEAD_N" -eq 0 ]; then
  say ""
  say "nothing to prune"
  exit 0
fi

if [ "$DRY" = 1 ]; then
  say ""
  say "dry run; $DEAD_N caches would have been deleted"
  exit 0
fi

# ---------------------------------------------------------------- delete
ok=0; failed=0
while IFS=$'\t' read -r _ cid _ _ _; do
  [ -n "$cid" ] || continue
  if gh api -X DELETE "repos/$REPO/actions/caches/$cid" >/dev/null 2>&1; then
    ok=$((ok+1))
  else
    # A cache can vanish between listing and deleting -- GitHub's own eviction
    # runs concurrently with this. Counted, not fatal.
    failed=$((failed+1))
  fi
done < "$TMP/verdict.tsv"

say ""
say "deleted $ok, could not delete $failed"
[ "$failed" -eq 0 ]
