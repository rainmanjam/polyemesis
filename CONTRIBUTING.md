# Contributing to polyemesis

Thanks for wanting to help. This document is the short version of how the
project works, so you can spend your time on the change rather than on guessing
the conventions.

## Before you start

**Open an issue first for anything larger than a bug fix.** Not as a formality —
this project has some strong opinions baked into it (video is never re-encoded,
the stack is fixed, FFmpeg is driven through `os/exec` and nothing else), and it
is much kinder to find out in an issue that a design conflicts with one of them
than after you have written it.

Small, obvious fixes need no ceremony. Send the pull request.

## The constraints that are not negotiable

These are what the project *is*. A change that breaks one of them will be
declined however good it is otherwise.

| Constraint | Why |
|---|---|
| **Video is never re-encoded** on a destination path | It is the whole performance story. `-c:v copy` is what makes a dozen destinations cost almost nothing. |
| **One static binary**, no cgo | `CGO_ENABLED=0` everywhere. It is why the thing runs on a NAS, a Pi, and a cloud box from the same release. |
| **FFmpeg via `os/exec`** — no bindings, no libav linkage | The command line is a stable, inspectable, debuggable interface. Bindings are none of those. |
| **SQLite through `modernc.org/sqlite`** | Pure Go. `mattn/go-sqlite3` would reintroduce cgo. |
| **The UI ships embedded** via `go:embed` | One artefact. No separate web server, no asset CDN, no "did you build the frontend?" |

If you think one of these is wrong, that is a conversation worth having in an
issue — but have it before writing the code.

## Setting up

```sh
git clone https://github.com/rainmanjam/polyemesis
cd polyemesis
make build          # builds the UI, embeds it, produces ./polyemesis
./polyemesis
```

You need Go 1.26+, Node 24+, and FFmpeg 6.0+ (8.x recommended). See
[docs/INSTALL.md](docs/INSTALL.md) for platform detail and
[docs/DEPENDENCIES.md](docs/DEPENDENCIES.md) for what is pinned and why.

For UI work, `make dev` runs Vite against a running server so you get hot reload
instead of a rebuild per change.

## Tests

```sh
make test                       # Go unit tests
go test ./... -race             # what CI runs; the race detector finds real bugs here
./scripts/acceptance.sh         # end-to-end against a real binary and real FFmpeg
```

[docs/TESTING.md](docs/TESTING.md) lists every suite and what it covers.
[docs/TEST-STRATEGY.md](docs/TEST-STRATEGY.md) explains what is deliberately
*not* covered and why.

### What a good test looks like here

This project cares more than most about tests that **measure** rather than
assert. Some of that is the domain — you cannot tell whether audio routing works
by checking that a function returned `nil` — and some of it is hard-won.

- **Measure the effect, not the call.** The audio suites route a distinct tone
  into each track and then measure the RMS of each output through a bandpass
  filter. That catches a mis-wired mix; asserting that `Compile()` returned no
  error does not.
- **Include the positive case.** A confinement test that only tries traversals
  passes just as happily when the feature is broken and refuses *everything*.
- **Prefer a fixed-value guard on the count.** Several suites here have checks
  behind `if the file exists` branches. One suite once reported *"7 passed, 0
  failed — PASSED"* having silently skipped five checks. Suites now assert how
  many checks ran, not just that none failed.
- **Comment the failure, not the code.** `// the destination restarted, which
  drops the platform connection` is worth ten lines describing what the function
  does.

## Code style

Run `make fmt` and `make lint` before pushing. Beyond that:

**Comments explain *why*, and especially *why not*.** The codebase is full of
comments recording the approach that was tried and rejected, with the
measurement that settled it — for example, why the video delay uses the `setts`
bitstream filter rather than `-itsoffset` (measured: `-itsoffset` moved audio and
video in lockstep and delivered 0 ms for every requested value). Those comments
are the most valuable thing in the repo. If you discover something surprising,
leave the note.

**Name things after what they are for.** `refuseIfSilent` beats `checkAudio`.

**Errors are for the operator.** An error string ends up in a toast in front of
somebody whose stream is broken. `"source disabled"` with nothing on screen
explaining it was a real bug here. Say what happened and what to do.

## Commits and pull requests

Write commit messages that explain the *problem*, not just the change. If you
fixed something subtle, the message is where the next person learns what was
subtle about it.

A pull request should say:

- what problem it solves,
- how you know it works (which suite, what you measured),
- anything you deliberately did not do.

CI runs build, vet, unit tests with `-race`, and the container acceptance suite.
Please make sure it is green — and if a test is flaky, say so rather than
re-running until it passes. A flaky test is a bug report.

## Reporting bugs

Use the issue templates. The most useful bug report for this project includes:

- the output of `polyemesis -version` and `ffmpeg -version`,
- your ingest mode and roughly what your destinations look like,
- the relevant part of the server log (`-log debug` if you can reproduce it),
- for anything about audio or timing: what you *measured*, not just what it
  sounded like.

Security issues go to [SECURITY.md](SECURITY.md) instead — please do not open a
public issue for those.

## Licence

By contributing you agree that your contribution is licensed under the MIT
Licence, the same as the rest of the project.
