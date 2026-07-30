# Test coverage: what exists, and what is missing

Measured 2026-07-28 against a running Docker instance on FFmpeg 8.1.2.

## What runs today

| Layer | Suite | Checks | Status |
|---|---|---|---|
| Unit + integration | `go test ./...` | **4,451** across 141 files, 28 packages | green |
| Integration (host binary + real FFmpeg) | `acceptance.sh` | 13 | green |
| | `acceptance-audio.sh` | 35 | green |
| | `acceptance-encoders.sh` | 32 | green |
| | `acceptance-renditions.sh` | 29 | green |
| | `acceptance-tls.sh` | 35 | green |
| | `acceptance-postprod.sh` | 12 | green |
| E2E (shipped container) | `acceptance-docker.sh` | 28 | green |
| | `acceptance-multisource.sh` | 18 | green |
| **E2E (browser)** | — | **0** | **absent** |

Total: **4,451 Go tests + 202 acceptance checks.**

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

## The gap that matters most: no browser tests at all

Zero. Every UI verification in this project has been a human driving a browser
once and moving on.

That is not a theoretical risk. Four real bugs were found in the UI this
session, by hand, each of which a browser test would have caught and pinned:

| Bug | How it presented |
|---|---|
| `useConfirm` called after an early return | Hook-order crash for the first operator with an empty library |
| Sources page PUT sent server-computed fields | Every save 400'd; the toggle flipped and silently reverted |
| A new source was created **disabled** | Encoder refused with "source disabled"; nothing on screen said why |
| Port fields committed on **blur** | Tabbing out of a field restarted the ingest and dropped the stream |

None of those are visible to `tsc`, and all four are trivially assertable in a
browser. Until this exists, every UI change is verified once and never again.

### What was built

`ui/e2e/` with Playwright, run against the shipped container so it exercises the
same artefact users get. It runs in CI as the `acceptance-browser` suite, 24
checks, and covers the four bugs above plus the guards added for them:

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

Ranked by what a failure would cost.

| Gap | Why it matters | Cost to build |
|---|---|---|
| **Pull ingest** (`acceptance-pull.sh`) | A whole ingest mode with unit tests but no end-to-end proof. Cameras and file loops go through it. | low — mirror `acceptance.sh` with a `file://` loop |
| **Failover** | primary → backup → slate switching is the feature that runs *while everything else is going wrong*, and it is only unit-tested | medium — needs two publishers and a kill |
| **Synthetic sources** (`acceptance-synth.sh`) | Silence-on-video-only and slate; a video-only ingest is refused by every platform, so this is a real-world path | low |
| **Recording lifecycle** | Record → segment → index → retention sweep → session grouping, as one arc. Pieces are covered; the arc is not | medium |
| **Alerts delivery** | Rule → event → webhook POST, against a local sink | low |
| **Scheduler** | A schedule firing and starting the thing it names | low |
| **Chat** | Four adapters, unit-tested with fakes, never against a real IRC/webhook server | medium — a local IRCd is feasible; the rest need mocks |
| **OAuth** | No integration test at all. Needs a mock provider standing in for YouTube/Twitch/Kick | medium |

## What cannot be tested here, and why

Worth stating so nobody reads absence as oversight:

| Area | Blocker |
|---|---|
| GPU encode (NVENC/VAAPI/QSV) | No GPU on this machine. The probe path is tested; a *successful* hardware encode has never run |
| Windows service | Recording truncation on service stop is known-broken by construction, and there is no Windows host here |
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

1. **Browser E2E**, the seven cases above. Largest gap, cheapest per bug caught,
   and it protects work that is currently verified by memory.
2. **`acceptance-pull.sh` and `acceptance-synth.sh`** — both are near-copies of
   an existing suite, and both cover ingest modes that ship untested end to end.
3. **Failover integration** — it is the safety net, and a safety net nobody has
   pulled on is a decoration.
4. **Fixed-value guards** on the two container suites, matching the postprod fix.
5. `internal/api` unit coverage, which is the largest single package gap and
   mostly needs handler-level table tests.
