# GitNexus impact analysis: what it does not see

**`impact` reports `0` callers and `risk: LOW` for most Go symbols in this
repository. A zero from it is not an answer.** Measured 2026-09-15 against a
freshly built index. Tracked as #810.

This matters because `CLAUDE.md` makes running it mandatory:

> **MUST run impact analysis before editing any symbol.** … **MUST warn the
> user** if impact analysis returns HIGH or CRITICAL risk.

Followed literally, that produces a confident wrong answer rather than no
answer, which is the worse of the two.

## The measurement

Callers counted by text search in `internal/`, excluding `_test.go` and the
definition itself.

| symbol | real callers | `impact` | risk |
|---|---|---|---|
| `releasePort` | 40 | **0** | LOW |
| `setHealth` | 31 | **0** | LOW |
| `hashStrings` | 16 | 8 | LOW |
| `downstreamHub` | 13 | **0** | LOW |
| `allocPort` | 12 | **0** | LOW |
| `previewFlowing` | 4 | **0** | LOW |
| `clampQuota` | 2 | 2 | LOW |

## The cause: a method body contributes no call edges

Pinned to single nodes by `filePath` — which matters, see below — top-level
functions carry their outgoing calls and methods carry none:

| symbol | kind | outgoing edges |
|---|---|---|
| `New` (`internal/engine/engine.go`) | func | 19 |
| `Open` (`internal/clips/clips.go`) | func | 7 |
| `startPreviewLocked` (engine.go) | method | **0** |
| `reconcileClips` (engine.go) | method | **0** |
| `allocPort` (engine.go) | method | **0** |
| `pump`, `setHealth` (`internal/chat/youtube.go`) | method | **0** |
| `Start` (`internal/srtserver/srtserver.go`) | method | **0** |

Most of this codebase is methods on a receiver, so most of its call graph is
absent. `hashStrings` returning 8 of 16 is the same fact from the other side:
the 8 found are the calls made from top-level functions.

`releasePort`'s only recorded caller is a *test function* — a top-level
`func TestX()`. Its forty callers inside `Engine` methods are not edges.

### Pin queries by file, or the numbers lie

An unpinned `MATCH (a) WHERE a.name = 'Start'` reported 24 outgoing edges and
appeared to refute all of the above. `Start` is the name of many methods across
many packages, and the query was aggregating them. Adding
`AND a.filePath = '…'` returned 0.

## What to do until #810 closes

- Treat `0` and `LOW` on a Go symbol exactly as `CLAUDE.md` says to treat
  `UNKNOWN`: **unresolved**. Confirm with a text search.
- `CLAUDE.md` says "never substitute grep for graph analysis". For Go symbols
  here, grep is currently the more accurate of the two. Use both and prefer
  the larger answer.
- The graph is still reliable for what it *does* record: file membership, type
  membership, and calls made from top-level functions.

## Why this note is here and not in CLAUDE.md

`CLAUDE.md` and `AGENTS.md` are generated end to end — every line sits inside
the `<!-- gitnexus:start -->` block, and the next `gitnexus analyze` overwrites
them. `.claude/skills/` is not tracked by git, so a note there reaches nobody
else. `docs/` is the only durable, shared surface, which is why a caveat about
a tool is filed as documentation.

**There is no CI guard for this**, and that is a limit rather than an
oversight: `.gitnexus/` is gitignored and no workflow runs gitnexus, so a test
that measured the tool's accuracy would have no index to measure.
