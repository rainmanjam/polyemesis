# What tests each part of polyemesis

Measured on `main` @ `e35e2c4`, 2026-08-15. **3,077 Go test functions** across 40
packages, **24 acceptance suites**, **14 browser specs**.

Read the columns as three different questions, because they fail differently:

- **Unit** — does the code do what its author believed? Fast, offline, always run.
- **Acceptance** — does the *shipped binary* do it, driven through the same API
  the UI uses? Slower, runs a real FFmpeg, catches composition bugs where both
  halves were individually correct.
- **Live** — does the *far end* agree? Only a real service can refuse a bad
  composition, and every suite in this column found a defect on its first run.

---

## By area

| Area | Go tests | Acceptance suite | Where it runs | Live far end |
|---|---:|---|---|---|
| **HTTP API** | 524 | `acceptance` (13 checks) | every PR, required | — |
| **Database / schema** | 319 | covered via `acceptance` | every PR | — |
| **FFmpeg argv & encoders** | 261 | `acceptance-encoders`, `acceptance-renditions` (45), `acceptance-ladder` (45) | every PR, required | — |
| **Engine / reconcile** | 261 | `acceptance`, `acceptance-failover` (27) | every PR, required | — |
| **OAuth** | 159 | `acceptance-oauth` (46) | weekly cron | **yes** — every provider's discovery doc |
| **Chat** | 134 | `acceptance-chat` (17) | weekly cron | **yes** — real IRC/webhook hosts |
| **Transcribe** | 124 | `acceptance-transcribe` (19) | weekly cron | **yes** — model host catalogue |
| **Media / uploads** | 116 + 59 + 10 + 5 | `acceptance-postprod` (12) | every PR, required | — |
| **Audio routing** | 115 | `acceptance-audio` (35) | every PR, required | — |
| **Clipper** | 86 | — | — | — |
| **Redaction / secrets** | 76 + 15 | `acceptance-multistream` (44) | manual, needs keys | **yes** — real platform refusals |
| **Jobs / scheduler** | 64 + 33 | `acceptance-playlist-phase0` (15) | every PR, required | — |
| **Playout / playlist** | 63 + 42 | `acceptance-playlist-phase0` | every PR, required | — |
| **Supervisor** | 58 | `acceptance-recording-stop` | every PR, required | — |
| **Automod** | 52 | `acceptance-automod` (21) | weekly cron | **yes** — real inference endpoint |
| **Recording** | 49 | `acceptance-recording-stop` | every PR, required | — |
| **TLS** | 45 | `acceptance-tls` | every PR, required | — |
| **RTMP ingest** | 41 | `acceptance-obs-multitrack` | weekly cron | **yes** — real OBS in a container |
| **MQTT** | 34 | `acceptance-mqtt` (20) | every PR, required | — |
| **Multitrack (Twitch EB)** | 33 | — | `live_test.go` on every run | **yes** — `ingest.twitch.tv` |
| **Hooks** | 29 | `acceptance-hooks` (31) | manual | **yes** — a listener we control |
| **Self-upgrade** | 28 | `acceptance-install` | weekly cron | — |
| **Relay / SRT** | 28 + 23 | `acceptance-pull` (10), `acceptance-synth` (10) | every PR, required | — |
| **Clips** | 27 | — | — | — |
| **Auth** | 23 | `acceptance-browser` (14 specs) | container matrix | — |
| **Web UI (served)** | 6 | `acceptance-browser` | container matrix | — |
| **Multi-source** | — | `acceptance-multisource` (18) | container matrix | — |
| **Docker image** | — | `acceptance-docker` (51) | container matrix | — |

---

## The 13 required legs

Every pull request must pass these before it can merge:

```
acceptance · acceptance-audio · acceptance-encoders · acceptance-failover
acceptance-ladder · acceptance-mqtt · acceptance-playlist-phase0
acceptance-postprod · acceptance-pull · acceptance-recording-stop
acceptance-renditions · acceptance-synth · acceptance-tls
```

`acceptance-ladder` joined on 2026-08-15 and needed the branch-protection
ruleset updated by hand — a matrix leg that is not in that list runs, reports,
and gates nothing.

## What runs on a clock rather than on a push

`automod-live`, `chat-live`, `oauth-live`, `transcribe-live`, `obs-multitrack`
and `installer` are weekly crons. The reason is the same for all six: **they
measure something we do not control**, so a failure arrives with no commit of
ours attached. A push cannot catch a platform changing its API; only a clock
can.

## Where the coverage genuinely stops

Stated plainly rather than implied by absence:

| Gap | Why it matters |
|---|---|
| **No broadcast has been published through a minted Twitch key.** | The Enhanced Broadcasting *negotiation* runs against `ingest.twitch.tv` on every test run and succeeds. Everything after `Negotiate` returns has only been driven by a stand-in server. This is what the EXPERIMENTAL label names. |
| **No NVENC, QSV, VA-API or AMF encode has been observed.** | All twelve encoder profiles were read off real FFmpeg option tables, but only VideoToolbox has been run on silicon (this Mac). Needs a GPU host. |
| **Nothing runs longer than a few minutes.** | Backoff crawl, memory growth, disk fill and reconnect churn are duration bugs. The longest suite is 75 s. |
| **No concurrency test.** | N sources × M destinations is unmeasured; the "~4% of a core per destination" figure has no test behind it. |
| **No mid-stream failure injection.** | `acceptance-failover` covers a switch; nothing covers a platform refusing at minute 30 of a healthy broadcast. |
| **`clipper` and `clips` have no acceptance suite.** | 113 unit tests, no end-to-end. They have no external hosts, so this is an ordinary coverage gap rather than the composition class. |

## Why the acceptance column is where the bugs are

Every defect found in the last two days was a **composition** bug — both halves
individually correct, the join wrong — and none was reachable by a unit test:

- A suite asserted "OBS sends one audio track" on a host whose FFmpeg could not
  demux two. Green, on a machine that could not have produced another answer.
- A minted-key test passed under mutation because an unrelated redactor masked
  the value anyway.
- A fixture standing in for a *successful* negotiation omitted the field a
  success always carries, so every test exercised a path only failures reach.
- 49 tests skipped while their packages printed `ok`.

This is why every suite here is expected to be **watched failing** before it is
trusted, and why `EXPECTED_CHECKS` exists: a suite that silently runs fewer
checks than it used to is the same failure as one that never ran.
