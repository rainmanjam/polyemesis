# Test coverage census — 2026-08-14

Measured, not estimated. This exists so "maximize coverage" becomes a finite
list someone can execute against, and so the next person does not start by
re-deriving what is already known.

**Decisions already taken** (owner, 2026-08-14): live credentials are available
and may be used; priority is **C → B → A** as defined below; a **stack of PRs,
one per area** is preferred over one large PR; and the OVH staging box **may be
disrupted** — that is what it is for.

---

## The headline: unit coverage is not the problem

43 packages measured. **Zero have no tests at all.** Only three sit under 60%:

| Coverage | Package | Worth raising? |
|---|---|---|
| 8.5% | `scripts` | **No.** Build-ignored operational tools. |
| 47.3% | `internal/testenv` | **No.** This is the guard harness itself. |
| 54.3% | `cmd/polyemesis` | **Marginal.** Mostly `main()` and flag wiring. |

Everything else is 70%+. So priority **A — raise Go unit coverage — is the
lowest-value work available**, and the data says so rather than an opinion. A
coverage-percentage push here would move a number and find nothing.

## Where the real risk is

**The site now advertises capabilities that have never run end to end.** That is
the gap, and it is not a coverage percentage.

---

## A. The suite × environment matrix

22 acceptance suites. **15 run in `ci.yml`. Four have no automation at all.**

> **CORRECTION, added after a subagent checked this rather than trusting it.**
> The first version of this document said "7 do not run in CI", derived by
> grepping `ci.yml` alone. That was wrong. `chat`, `oauth` and `automod` each
> have their **own workflow** — `chat-live.yml`, `oauth-live.yml`,
> `automod-live.yml` — on a weekly cron plus a `pull_request` trigger scoped to
> the paths they cover. That placement is deliberate and argued in the files: a
> third-party network dependency on every push is a flake generator, and the
> failures they catch (Twitch rewording a NOTICE, Kick moving a key URL, a cert
> expiring) arrive with **no commit of ours at all**, which a schedule catches
> and a per-push job does not.
>
> `acceptance-chat.sh` was also already credential-free for 15 of its 17 checks,
> with the remaining two skipped and each skip stating its reason. Nothing to
> do. The genuinely unautomated suites are **install, multistream,
> obs-multitrack and transcribe**.

| Suite | CI | Needs | Notes |
|---|---|---|---|
| audio | ✅ | — | ran on OVH: 40/40 |
| hooks | ✅ | — | |
| playlist-phase0 | ✅ | — | |
| pull | ✅ | — | |
| synth | ✅ | — | |
| browser | ✅ | docker, host | |
| docker | ✅ | docker | |
| encoders | ✅ | **gpu** | CI has none — runs degraded |
| failover | ✅ | docker, host | |
| mqtt | ✅ | docker, host | |
| multisource | ✅ | creds, docker, host | |
| postprod | ✅ | host | |
| recording-stop | ✅ | host | |
| renditions | ✅ | creds, host | ran on OVH: **39/39** (5 more than local) |
| tls | ✅ | host | |
| **automod** | ❌ | creds | |
| **chat** | ❌ | creds | five-platform chat is on the website |
| **install** | ❌ | host | needs a clean host; OVH can now be dirtied |
| **multistream** | ❌ | creds, docker, host | the product's core claim |
| **oauth** | ❌ | creds | |
| **obs-multitrack** | ❌ | creds, docker | E-RTMP multitrack ingest |
| **transcribe** | ❌ | docker, host | |

## B. Features the site claims that nothing exercises end to end

Ranked by exposure — this is priority **C**, and it should be done first.

1. **Two mixes to one destination / Twitch Enhanced Broadcasting.** `/features`
   now has a whole section. Never run end to end: the negotiation **requires a
   GPU** and the staging box has none, so Twitch refuses it by design. Needs a
   GPU host (RunPod is viable). One test is achievable with **no Twitch
   credentials at all** — the `GetClientConfiguration` probe already returns a
   GPU-less refusal credential-free, so on a GPU host the assertion is simply
   that it now returns a populated `audio_configurations.vod`.
2. **Capped VBR reaching a real encoder (#341).** Asserted through argv only.
   No test sets a ceiling above target and measures the resulting bitrate.
   `acceptance-renditions.sh` is the natural home.
3. **Five-platform chat — but not the gap this document first named.**
   `acceptance-chat.sh` is automated and credential-free, and it covers
   **Twitch and Kick only**. `internal/chat` ships five adapters; **YouTube,
   Facebook and Rumble have no live coverage in any suite.** That is the
   untested half of the five-platform claim, and it is new checks to write
   rather than CI to wire. YouTube and Facebook will need credentials to read
   at all, so they land as justified skips.
4. **Multistream to real platforms.** `acceptance-multistream.sh`, not in CI.
   The single most important claim the product makes.

## C. Go unit coverage

Do last, and only where a gap is real rather than numeric. `cmd/polyemesis` at
54% is the only candidate worth a look, and probably only for flag parsing.

---

## Proposed PR stack

One per area, in this order. Each should be independently reviewable and
independently revertable.

1. **`test(renditions)`** — capped VBR through a real encode. Smallest, closes a
   gap in something already merged, and needs no credentials.
2. **`test(chat)`** — put `acceptance-chat.sh` in CI for its credential-free
   checks (15 of its 17 were already credential-free) and run the rest on OVH.
3. **`test(multistream)`** — the core claim, on OVH, with real credentials.
4. **`test(install)`** — `acceptance-install.sh` on OVH now that the box may be
   dirtied. Should also cover the #348 upgrade guard: an upgrade whose backup
   lacks `secret.key` must **refuse**.
5. **`test(ertmp)`** — `acceptance-obs-multitrack.sh`, E-RTMP multitrack ingest.
6. **GPU host** — Enhanced Broadcasting end to end. Separate because it needs
   infrastructure that does not exist yet.

## What to be careful of

The failure mode this repo keeps finding is **a test that passes without
asserting anything**. Ten claims did not survive checking in the session that
produced this document, several of them the author's own. Specifically:

- `go test -run <mistyped-name>` exits **0** printing `[no tests to run]`.
  Always confirm the named test actually ran.
- Every new guard should be mutation-tested: break the behaviour, watch the
  **specific named** test fail, restore from a **file backup** — never
  `git checkout --`.
- Assert on **outcomes**, not mechanisms. A sticky column reported
  `position: sticky` and `stayedPinned: true` while rendering transparent; a
  VFR stream looked like it lost 1.6s because ffprobe *derives* durations
  MPEG-TS does not store.
