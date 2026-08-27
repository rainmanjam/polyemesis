# Test coverage: what exists, and what is missing

Per-suite check counts were measured 2026-07-28 against a running Docker
instance on FFmpeg 8.1.2. The Go totals and the suite list were re-counted from
the tree and from `.github/workflows/ci.yml` on 2026-08-26 — **a date at the top
of a page is not a guard**, and these numbers had drifted a long way past it.

## What runs today

| Layer | Suite | Checks | Status |
|---|---|---|---|
| Unit + integration | `go test ./...` | **3,591** top-level `func Test…` across 541 files, 45 packages | green |
| Integration (host binary + real FFmpeg) | `acceptance.sh` | 13 | green |
| | `acceptance-audio.sh` | 35 | green |
| | `acceptance-encoders.sh` | 32 | green |
| | `acceptance-renditions.sh` | 29 | green |
| | `acceptance-ladder.sh` | 45 | green |
| | `acceptance-tls.sh` | 35 | green |
| | `acceptance-postprod.sh` | 12 | green |
| | `acceptance-pull.sh` | not counted | green |
| | `acceptance-playlist-phase0.sh` | not counted | green |
| | `acceptance-synth.sh` | not counted | green |
| | `acceptance-failover.sh` | not counted | green |
| | `acceptance-recording-stop.sh` | not counted | green |
| | `acceptance-mqtt.sh` | not counted | green |
| E2E (shipped container) | `acceptance-docker.sh` | 29 | green |
| | `acceptance-multisource.sh` | 18 | green |
| **E2E (browser)** | `acceptance-browser.sh` | **30** | green |
| Cross-platform broadcast | `scripts/smoketest.go` | 8 | Linux, macOS, Windows |

Total: **3,591 Go tests**, 201 counted checks across seven of the **thirteen**
host acceptance suites CI runs, 77 container checks, and the broadcast smoke
test on all three platforms. The six suites marked *not counted* run in CI on
every commit; nobody has re-totalled their assertions since they were added, and
inventing a number for them here is what produced the row above this paragraph.

Browser E2E is **not** a gap. `acceptance-browser.sh` drives Playwright against
the shipped image, `ui/e2e/` holds fifteen spec files, and it is one of the three
container suites required by CI. This page said "absent, 0 checks" long after
that shipped; [TESTING.md](TESTING.md) has always documented the same suite
correctly.

`smoketest.go` is the only suite that runs anywhere but Linux. It injects into
the relay hub rather than publishing over SRT, so it needs nothing of a runner's
FFmpeg beyond `libx264` and AAC — which is what lets it run on Windows and on a
Homebrew build with no libsrt. It proves the broadcast path, not SRT ingest;
that stays with the Linux acceptance suites.

## Unit coverage, by package

| Band | Packages |
|---|---|
| 90–100% | metrics, events, stats, routing, media, clipper, ffmpeg, playout, alerts, relay, jobs, secrets, meters |
| 80–90% | auth, config, tlsx, clips, chat, supervisor, transcribe |
| 70–80% | oauth, scheduler, db, recording |
| **under 70%** | **srtserver 68.2%, engine 44.6%, api 40.7%, cmd 13.1%** |
| none | `internal/web` (a `go:embed` wrapper — there is little to test) |

The audio core — the part the product exists for — is the best covered, which is
the right shape. The weak band is where behaviour is hardest to reach from a
unit test, which is also where the acceptance suites do their work.

## The gap that mattered most: browser tests — now closed

For a long time this was the headline gap, and the sections below are kept in
that order because the argument is what justified the suite. **It has since been
built and it runs in CI**; read "no browser tests at all" as history, not as the
current state.

Every UI verification in this project used to be a human driving a browser once
and moving on. That was not a theoretical risk. Four real bugs were found in the
UI by hand, each of which a browser test would have caught and pinned:

| Bug | How it presented |
|---|---|
| `useConfirm` called after an early return | Hook-order crash for the first operator with an empty library |
| Sources page PUT sent server-computed fields | Every save 400'd; the toggle flipped and silently reverted |
| A new source was created **disabled** | Encoder refused with "source disabled"; nothing on screen said why |
| Port fields committed on **blur** | Tabbing out of a field restarted the ingest and dropped the stream |

None of those are visible to `tsc`, and all four are trivially assertable in a
browser. That was the argument for building the suite described below.

### What was built

`ui/e2e/` with Playwright, run against the shipped container so it exercises the
same artefact users get. It runs in CI as the `acceptance-browser` suite, 30
checks across fifteen spec files, and covers the four bugs above plus the guards
added for them:

1. First-run setup, login, and that setup cannot be replayed.
2. Every nav route renders without a console error.
3. Language switch changes the DOM and `<html lang>`, for one CJK and one
   accented catalogue.
4. Creating a source: enabled by default, ports auto-moved off a clash.
5. Editing a port does **not** commit on blur; Apply/Discard appears.
6. The delete dialog starts locked, and typing the WRONG name keeps it locked.
7. A save that fails surfaces an error rather than silently reverting.

The one thing it still cannot reach is the chat pane: the timeline is empty
without a connected platform account, and the suite has no way to supply one.
The moderation controls are unit- and mutation-tested underneath, but their
click-through has never run.

## Integration gaps

Ranked by what a failure would cost. Pull ingest, synthetic sources and failover
have since left this table: `acceptance-pull.sh`, `acceptance-synth.sh` and
`acceptance-failover.sh` are all in CI's acceptance matrix now.

| Gap | Why it matters | Cost to build |
|---|---|---|
| **Recording lifecycle** | Record → segment → index → retention sweep → session grouping, as one arc. `acceptance-recording-stop.sh` covers the stop; the rest of the arc is not | medium |
| **Alerts delivery** | Rule → event → webhook POST, against a local sink | low |
| **Scheduler** | A schedule firing and starting the thing it names | low |
| **Chat** | Four adapters, unit-tested with fakes, never against a real IRC/webhook server | medium — a local IRCd is feasible; the rest need mocks |
| **OAuth** | No integration test at all. Needs a mock provider standing in for YouTube/Twitch/Kick | medium |

## What cannot be tested here, and why

Worth stating so nobody reads absence as oversight:

| Area | Blocker |
|---|---|
| GPU encode (NVENC/VAAPI/QSV) | No GPU on this machine. The probe path is tested; a *successful* hardware encode has never run |
| Windows **service** | Recording truncation on service stop is known-broken by construction: the graceful stop is a `CTRL_BREAK_EVENT`, which Windows delivers only through a console, and a service has none. CI covers the *console* path — full suite plus a measured broadcast on `windows-latest` every push — but a service-hosted run needs a real host and is unproven. |
| ACME / Let's Encrypt | Needs a public DNS name and inbound 80 |
| RTL languages | Blocked on converting 102 physical Tailwind utilities; shipping them untested would be worse than not shipping |
| linux/arm64 runtime | The image *builds* for both arches and is verified at the ELF level, but only amd64 has been *run* |

## Flakiness found

`acceptance-postprod.sh` gives 12 checks on an idle machine and 7 on a loaded
one, because the job governor defers work under CPU pressure and starves the
crash-recovery section of the job it needs to kill. The governor is behaving as
designed.

It previously reported that as **"7 passed, 0 failed — PASSED"**. It now checks
the count as well as the tally and exits 1, naming the cause. Any suite that can
run fewer assertions than it contains needs the same guard; these two container
suites should get one too.

## Recommended order

The first three items of this list have been done: browser E2E, the pull and
synthetic ingest suites, and failover integration all run in CI. What is left:

1. **Fixed-value guards** on the container suites, matching the postprod fix — a
   suite that can run fewer assertions than it contains and still report PASSED
   is the defect that guard exists for.
2. `internal/api` unit coverage, which is the largest single package gap and
   mostly needs handler-level table tests.
3. **Alerts delivery, scheduler, chat and OAuth** integration, per the table
   above.
