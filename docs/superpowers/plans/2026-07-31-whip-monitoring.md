# WebRTC Self-Monitoring (WHEP) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let an operator watch their own feed with sub-second latency, so that
"is my audio clipping / did the guest just drop / is the overlay in the right
place" is answered about *now* rather than about 2–3 seconds ago. The existing
HLS preview stays exactly as it is and remains the compatibility floor.

---

## The name is wrong in the brief, and it matters

The task was written as "a WHIP endpoint so an operator can watch their own
feed". **WHIP is the wrong protocol for that.** The two are not
interchangeable:

| | Direction | Spec | What it does here |
|---|---|---|---|
| **WHIP** | browser → server | RFC 9725 | An *ingest* mode: publish from a laptop with no encoder installed. A fourth `IngestMode`. |
| **WHEP** | server → browser | `draft-murillo-whep` | An *egress*: the operator's browser receives the feed. **This is what "watch your own feed" means.** |

This plan builds **WHEP**. The file keeps the requested path
(`2026-07-31-whip-monitoring.md`) so nobody loses it, but every identifier,
route, package and setting says WHEP, because shipping code that calls the
receive path "whip" would mislead every future reader and would collide the day
somebody actually builds ingest. WHIP ingest is out of scope and is not designed
here; see [docs/roadmap/WEBRTC.md](../../roadmap/WEBRTC.md) §"WHIP design" for
the prior research, and the *Not covered* list below for why it is a separate
decision.

**Prior art:** [docs/roadmap/WEBRTC.md](../../roadmap/WEBRTC.md) is a complete,
current research document (status DEFERRED, 2026-07-28) including a **measured**
dependency audit. This plan implements its recommendation and does not repeat
its measurements. Read it before Task 1.

---

## Architecture

```
relay.Hub (ingest, MPEG-TS datagrams)
  │
  ├── hub.Subscribe("preview", port)  → supervised ffmpeg → HLS   (unchanged)
  │
  └── hub.Subscribe("whep", inPort)   → supervised ffmpeg, Kind "whep"
         -i udp://127.0.0.1:<inPort>
         -map 0:v:0  -c:v copy       -bsf:v dump_extra -payload_type 96
                                     -f rtp rtp://127.0.0.1:<vPort>
         -map 0:a:0? -c:a libopus    -payload_type 111
                                     -f rtp rtp://127.0.0.1:<aPort>
              │                              │
              ▼                              ▼
        internal/webrtcx.Source: two net.UDPConn read loops
              │  rtp.Packet → TrackLocalStaticRTP.Write
              ▼
        N × *webrtc.PeerConnection  (N capped, default 3)
              ▲
              │  POST /api/v1/whep  (application/sdp, session cookie + CSRF)
              │  DELETE /api/v1/whep/{id}
        ui/src/components/WhepPlayer.tsx
```

**Three loopback ports per running WHEP tier**, all from the existing
`relay.PortAllocator` (`relayPortBase = 21000`, `relayPortSpan = 500`,
`internal/engine/engine.go:50-52`): one for the hub subscription that FFmpeg
reads, two for the RTP outputs that Go reads. The span is shared across engines,
so with 8 sources that is 24 ports out of 500 — ample.

**The FFmpeg child is ref-counted on live peers**, mirroring
`Engine.PreviewRequested` / `previewLoop` / `sweepPreview`
(`engine.go:1095/:1128/:1141`) and using the same `previewMu → e.mu` lock order.
The difference: HLS *infers* a viewer from playlist polls and waits 30 s to be
sure; WebRTC gives an explicit `OnConnectionStateChange` disconnect, so the
sweep is 2 s and the grace window is 10 s (a page reload is a disconnect
immediately followed by a new offer, and cycling FFmpeg on every reload feels
broken).

**Which hub it reads: `e.hub`, the ingest hub — the same one the HLS preview
reads.** Deliberately *not* `e.downstreamHub()`, which is what playout uses.
The WHEP monitor sits beside the HLS preview in the same dashboard card showing
the same picture with less delay; if the two read different hubs, an operator
toggling between them would see two different pictures with nothing on screen
to explain why. This is a real limitation with failover enabled — see *Not
covered*.

---

## Global Constraints

- **This is self-monitoring, not distribution. Say so in the code, the docs and
  the UI.** One SRTP encryption per peer at full ingest bitrate does not scale
  the way a segment on disk does. Concrete consequences, all enforced rather
  than merely stated:
  - The routes live inside the existing `requireAuth` + `requireCSRF` group.
    They are **never** wired into `internal/playout`'s anonymous token path.
  - `MaxPeers` defaults to **3** and is validated at **1–8**. Over the cap the
    server answers **429** with a body naming the count, not a queue.
  - No ABR, no DVR, no seeking, no recording from this path, no `/watch` embed.
- **Video is COPIED. Audio is RE-ENCODED. Both are stated loudly in
  `WHEPArgs`.** See "The copy promise" below — this is the one constraint that
  needs its own section.
- **H.264 only.** VP8, VP9 and AV1 each mandate a video re-encode, which is
  precisely the cost this feature exists to avoid.
- **No TURN, ever. No STUN by default.** If a viewer is behind a UDP-blocking
  firewall the honest answer is "the HLS preview still works" — which is exactly
  why WHEP sits beside HLS rather than replacing it.
- **No new *runtime* logging vocabulary.** `pion/logging` is adapted onto
  `*slog.Logger` so pion never writes to stdout on its own.
- **`CGO_ENABLED=0` across all four targets** (linux/amd64, linux/arm64,
  darwin/arm64, windows/amd64) is a gate in Task 2 and a permanent item on the
  dependency-bump checklist.
- Every guard in this plan has a named mutation test: the step that proves the
  guard fails when removed.
- CI gates, in the order CI runs them: `gofmt -l ./cmd ./internal` must print
  nothing; `go build ./...`; `go vet ./...`; `go test -race ./...`; then
  `cd ui && npx tsc -b --noEmit && npm run lint && npm run build`.
- British spelling. No emoji. Comments explain *why*, and name the failure that
  motivated the decision.

### The copy promise

polyemesis's central promise is that **video is copied, never re-encoded, on a
destination path.** This feature does not break it, and the reason is worth
stating precisely rather than waving at:

1. **Video is `-c:v copy`.** The ingest is already H.264 inside MPEG-TS; RFC
   6184 RTP payloading is a repackaging, not a transcode. WHEP therefore costs
   approximately **zero CPU for video** — which makes it *cheaper* than the HLS
   preview it sits beside, and `PreviewArgs` is currently documented in
   `internal/ffmpeg/build.go:905-907` as "the one place polyemesis re-encodes
   video". After this change that sentence is still true.
2. **Audio is re-encoded, unavoidably.** No shipping browser registers an AAC
   payload format in its default WebRTC `MediaEngine` — not Chrome, not Firefox,
   not Safari. AAC → Opus is not a choice; it is the price of admission. It
   costs single-digit percent of one core.
3. **Neither is a destination path.** Nothing an audience receives is touched.
   The monitor is a private, authenticated, capped, on-demand read of the same
   hub the meters sidecar already reads.
4. **There is one opt-in escape hatch that DOES re-encode video**, for the
   ingest whose H.264 profile a browser refuses (Task 4). It is
   `ForceReencode bool`, defaults **false**, and the UI labels it
   "re-encodes video (uses CPU)". A `WHEPSpec` with `ForceReencode` false must
   never emit a video encoder, and `TestWHEPArgsCopiesVideoByDefault` is the
   guard that proves it.

If anyone ever removes point 4's default, or lets `ForceReencode` default true,
they have quietly made the monitor cost as much as the HLS preview while
claiming otherwise. That is what the guard is for.

### What is NOT covered, and what could go wrong

Stated up front, not buried:

- **With failover enabled, WHEP shows the INGEST, not the on-air source.** It
  reads `e.hub` for consistency with the HLS preview, which has always had this
  property. An operator monitoring during a failover switch will see the primary
  even after the selector has moved to the backup. This is a genuine wart. Fixing
  it means moving *both* preview and WHEP onto `e.downstreamHub()`, which is a
  separate change with its own restart-signature consequences for the preview.
  Listed as an open question, not silently accepted.
- **Join latency is time-to-next-IDR.** On a 2 s GOP that is up to two seconds
  of black before the first frame. There is no fix that does not re-encode. The
  player shows a spinner; the docs recommend a short GOP on the encoder.
- **Chromium is the only browser in CI.** Safari and Firefox negotiate H.264
  profiles differently, and Playwright's WebKit/Firefox builds are not the
  shipping browsers. Safari is manual-test only. If Firefox refuses a common
  ingest profile we will find out from a user, not from CI.
- **The H.264 pass-through is the entire value proposition and the least
  certain part.** Task 1 is a spike that must pass before anything else is
  built. If common ingest profiles turn out un-negotiable, WHEP degrades to a
  re-encode — a lower-latency preview at the *same* CPU cost as HLS, a much
  weaker pitch — and the whole feature should be reconsidered rather than
  finished.
- **+7.5 MiB binary and 18 net-new modules**, on a project whose identity is one
  small static binary. Non-negotiable and permanent.
- **Goroutine and socket leak per peer.** A closed laptop lid leaves a
  PeerConnection alive until ICE times out. Mitigated by the peer cap, a
  disconnect timeout and a goroutine-count assertion in Task 3 — not eliminated.
- **WHIP ingest is not designed here.** It has the same limitation RTMP has (one
  audio track, so it cannot feed the per-destination multitrack routing that is
  the reason this product exists) and deserves its own decision.
- **No TLS means no WHEP**, except on `localhost`. Browsers gate WebRTC on a
  secure context. Task 7 surfaces this as a precondition the UI explains, so it
  does not present as a bug.

---

### Task 1: Spike — prove `-c:v copy` reaches a browser

**Files:**
- Create: `docs/roadmap/WEBRTC-SPIKE.md` (findings; deleted or folded into
  `WEBRTC.md` at the end of Task 9)
- Create: `/tmp/whep-spike/` (throwaway, **not** committed)

**Interfaces:** none. This task produces a go/no-go decision and a recorded
`profile-level-id`, nothing else.

**Why first:** risk 1 in `docs/roadmap/WEBRTC.md:250-255`. If a real ingest's
H.264 will not negotiate with Chrome under `-c:v copy`, every estimate in this
plan roughly doubles and the pitch changes from "cheaper than the HLS preview"
to "a second re-encode". Half a day here is cheap.

- [ ] **Step 1: Record what the ingest actually is**

With a real ingest running (or the project's synthetic feed — see
`scripts/acceptance-*.sh` for how the other suites generate one), capture the
video parameters:

```bash
ffprobe -v error -select_streams v:0 \
  -show_entries stream=codec_name,profile,level,pix_fmt,width,height,r_frame_rate \
  -of default=noprint_wrappers=1 \
  'udp://127.0.0.1:21000?timeout=5000000'
```

Write the output verbatim into `docs/roadmap/WEBRTC-SPIKE.md` under
`## Ingest under test`. A spike whose input is not recorded proves nothing.

- [ ] **Step 2: Payload it to RTP and read it back**

```bash
mkdir -p /tmp/whep-spike && cd /tmp/whep-spike
ffmpeg -hide_banner -nostdin -loglevel warning \
  -i 'udp://127.0.0.1:21000?fifo_size=5000&overrun_nonfatal=1' \
  -map 0:v:0 -c:v copy -bsf:v dump_extra -payload_type 96 \
  -f rtp -sdp_file video.sdp rtp://127.0.0.1:22000
```

Expected: `video.sdp` names `H264/90000`, and an `a=fmtp:96` line carrying
`packetization-mode` and `profile-level-id`. **Record the exact
`profile-level-id`** — Task 3 registers a codec that must match it.

If FFmpeg refuses with "Only H.264 and H.265 are supported" or similar, the
ingest is not H.264 and this whole feature does not apply to it: note that and
continue with a known-H.264 source.

- [ ] **Step 3: Feed a real Chrome**

Use any off-the-shelf WHEP server you can point at that RTP (`mediamtx` is the
usual choice; this is a throwaway, nothing is being adopted). Open Chrome,
connect, and read `chrome://webrtc-internals`.

Record in `WEBRTC-SPIKE.md`:
- `framesDecoded` rising across two samples (not merely non-zero);
- `bytesReceived` rising;
- the negotiated `profile-level-id` in the answer;
- **time from POST to first decoded frame** — this is the IDR wait, and it is
  the number the UI's spinner has to be designed around.

- [ ] **Step 4: Write the verdict**

Two possible outcomes, both explicit:

- **PASS** — Chrome decodes the copied stream. Record the profile and proceed.
- **FAIL** — record *which* parameter Chrome refused. If it is profile, Task 4's
  `ForceReencode` fallback becomes the default path for that ingest class and
  the feature's CPU claim must be rewritten in `docs/` before Task 9. **Stop and
  escalate rather than continuing quietly.**

- [ ] **Step 5: Commit the findings only**

```bash
rm -rf /tmp/whep-spike
git add docs/roadmap/WEBRTC-SPIKE.md
git commit -m "docs(webrtc): spike result for H.264 pass-through to a browser

Records the ingest under test, the profile-level-id FFmpeg emits under -c:v
copy, whether Chrome decodes it, and the measured time from connect to first
decoded frame.

This is risk 1 in docs/roadmap/WEBRTC.md: H.264 pass-through is the entire
value proposition of WHEP and the least certain part of it. Measuring it before
writing any code is what stops the feature being finished before anyone finds
out it costs a re-encode after all."
```

---

### Task 2: The dependency, and the slog adapter

**Files:**
- Modify: `go.mod`, `go.sum`
- Create: `internal/webrtcx/logging.go`
- Create: `internal/webrtcx/logging_test.go`
- Modify: `docs/DEPENDENCIES.md`

**Interfaces:**
- Consumes: `pion/logging.LoggerFactory`, `*slog.Logger`.
- Produces: `webrtcx.SlogFactory(*slog.Logger) logging.LoggerFactory`.

- [ ] **Step 1: Add the dependency and measure it**

```bash
go get github.com/pion/webrtc/v4@v4.2.18
go mod tidy
go list -m all | wc -l
```

Record the module count before and after. `docs/roadmap/WEBRTC.md:46-48` measured
22 modules linked and a 9.97 MiB binary; confirm the same order of magnitude
rather than trusting the note.

- [ ] **Step 2: Prove the cgo-free four-target build**

This is constraint 2 and 3 of `docs/DEPENDENCIES.md`. A dependency that breaks
either is not a candidate, and finding out at release time is too late:

```bash
for t in linux/amd64 linux/arm64 darwin/arm64 windows/amd64; do
  GOOS=${t%/*} GOARCH=${t#*/} CGO_ENABLED=0 go build ./... \
    && echo "OK $t" || echo "FAIL $t"
done
```

Expected: four `OK` lines. Any `FAIL` stops this task — do not proceed and do
not commit the `go.mod` change.

- [ ] **Step 3: Write the failing test**

Create `internal/webrtcx/logging_test.go`:

```go
package webrtcx

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

// pion writes to stdout through its own default LoggerFactory. In a binary
// whose logs are structured slog and are shipped to a file sink, a library
// printing unstructured lines to stdout is not a cosmetic problem: stdout is
// where the supervisor's progress parser looks (supervisor.go:466), and it is
// what an operator tails. Everything pion says must arrive as slog.
func TestPionLogsArriveAsSlog(t *testing.T) {
	var buf bytes.Buffer
	base := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	f := SlogFactory(base)
	lg := f.NewLogger("ice")
	lg.Warn("candidate gathering failed")

	got := buf.String()
	if !strings.Contains(got, "candidate gathering failed") {
		t.Fatalf("pion's message never reached slog; buffer = %q", got)
	}
	if !strings.Contains(got, "ice") {
		t.Errorf("the pion scope is missing, so a log line cannot be traced back "+
			"to the subsystem that wrote it; buffer = %q", got)
	}
	if !strings.Contains(got, "level=WARN") {
		t.Errorf("severity was flattened; a pion Warn must not arrive as Info. "+
			"buffer = %q", got)
	}
}

// Trace and Debug are separate levels in pion and both map to slog Debug.
// Trace at Info would flood the log of every install that has WebRTC enabled.
func TestPionTraceIsNotPromoted(t *testing.T) {
	var buf bytes.Buffer
	base := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))

	SlogFactory(base).NewLogger("dtls").Trace("handshake packet")

	if buf.Len() != 0 {
		t.Fatalf("a pion Trace line survived an Info-level handler: %q", buf.String())
	}
}
```

- [ ] **Step 4: Run test to verify it fails**

Run: `go test ./internal/webrtcx/ -run TestPion -v`
Expected: FAIL to build, `undefined: SlogFactory`.

- [ ] **Step 5: Write the adapter**

Create `internal/webrtcx/logging.go`:

```go
// Package webrtcx is the WHEP self-monitoring transport.
//
// It exists because there is no FFmpeg path to a browser PeerConnection.
// FFmpeg has no ICE, no DTLS-SRTP and no offer/answer, so the rule
// docs/DEPENDENCIES.md sets for a protocol dependency -- "justified only when
// FFmpeg cannot do the job" -- is satisfied here in the same way it was for
// datarhei/gosrt. internal/srtserver is the shape this package copies.
//
// Scope, stated once and enforced throughout: this serves the OPERATOR
// watching their own feed. It is not an audience path. One SRTP encryption per
// peer at full ingest bitrate does not scale the way a segment on disk does,
// so the peer count is capped and the routes are authenticated.
package webrtcx

import (
	"log/slog"

	"github.com/pion/logging"
)

// slogLogger adapts one pion scope onto slog.
//
// pion's default factory prints to stdout. In this binary stdout is where the
// supervisor's FFmpeg progress parser reads (supervisor.go:466) and where an
// operator tails, so an unstructured library line there is not cosmetic: it is
// noise in a channel that has a parser on it.
type slogLogger struct{ l *slog.Logger }

func (s slogLogger) Trace(msg string)                          { s.l.Debug(msg) }
func (s slogLogger) Tracef(f string, a ...any)                 { s.l.Debug(sprintf(f, a...)) }
func (s slogLogger) Debug(msg string)                          { s.l.Debug(msg) }
func (s slogLogger) Debugf(f string, a ...any)                 { s.l.Debug(sprintf(f, a...)) }
func (s slogLogger) Info(msg string)                           { s.l.Info(msg) }
func (s slogLogger) Infof(f string, a ...any)                  { s.l.Info(sprintf(f, a...)) }
func (s slogLogger) Warn(msg string)                           { s.l.Warn(msg) }
func (s slogLogger) Warnf(f string, a ...any)                  { s.l.Warn(sprintf(f, a...)) }
func (s slogLogger) Error(msg string)                          { s.l.Error(msg) }
func (s slogLogger) Errorf(f string, a ...any)                 { s.l.Error(sprintf(f, a...)) }

type slogFactory struct{ l *slog.Logger }

// NewLogger is called by pion once per subsystem ("ice", "dtls", "sctp", ...).
// The scope becomes a slog attribute rather than a prefix so it can be filtered
// like any other field.
func (f slogFactory) NewLogger(scope string) logging.LeveledLogger {
	return slogLogger{l: f.l.With("pion", scope)}
}

// SlogFactory is handed to webrtc.SettingEngine.LoggerFactory. Passing it is
// not optional: without it pion constructs its own factory and writes to
// stdout.
func SlogFactory(l *slog.Logger) logging.LoggerFactory {
	return slogFactory{l: l.With("component", "webrtc")}
}
```

Add the `sprintf` helper in the same file (`fmt.Sprintf`, aliased so the
twelve one-line methods above stay readable):

```go
func sprintf(format string, a ...any) string { return fmt.Sprintf(format, a...) }
```

and `"fmt"` to the imports.

- [ ] **Step 6: Run the tests**

Run: `gofmt -w internal/webrtcx/ && go test ./internal/webrtcx/ -count=1 -v`
Expected: PASS, both tests.

- [ ] **Step 7: Mutation-test the adapter**

Change `func (s slogLogger) Warn(msg string) { s.l.Warn(msg) }` to
`{ s.l.Info(msg) }`.
Run: `go test ./internal/webrtcx/ -run TestPionLogsArriveAsSlog -count=1`
Expected: FAIL on "severity was flattened".
Restore by hand, re-run, confirm PASS.

- [ ] **Step 8: Update DEPENDENCIES.md honestly**

In `docs/DEPENDENCIES.md`, add a row to the "Direct Go dependencies" table
(`github.com/pion/webrtc/v4 | v4.2.18 | internal/webrtcx`), change "Ten,
deliberately" to "Eleven, deliberately", update "Last reviewed", and add a
section after the gosrt one:

```markdown
### `github.com/pion/webrtc/v4` — the second protocol dependency, and its costs

Added for WHEP self-monitoring (`internal/webrtcx`). It clears the bar gosrt
set — **FFmpeg cannot do this job**: there is no `ffmpeg` invocation that
performs ICE, DTLS-SRTP and an SDP offer/answer with a browser. That is the
same reason gosrt was accepted and, at the time this plan was written, the
opposite of why `yutopp/go-rtmp` was refused — `ffmpeg -listen 1` being taken
as a complete answer for RTMP.

> **Correction, 2026-08-06.** It was not a complete answer. `-listen 1` accepts
> one publisher and cannot demultiplex by `app/streamkey`, so RTMP failed the
> same bar SRT did, and `internal/rtmpserver` now wraps `bluenviron/gortmplib`.
> The paragraph above still holds for WebRTC — FFmpeg genuinely cannot serve a
> browser PeerConnection — but the RTMP contrast it leans on was mistaken, and
> pion would be the *fourth* protocol dependency rather than the second.

The costs, written down here rather than discovered later:

- **18 net-new modules.** Four of pion's 22 were already in the graph. All pion
  modules are MIT; `wlynxg/anet` is BSD-3-Clause. Compatible with this
  project's MIT.
- **Roughly +7.5 MiB** on a binary whose identity is that it is small.
- **`github.com/wlynxg/anet` is dead weight** — an Android netlink workaround
  for `net.Interfaces()`. polyemesis does not ship to Android. It rides along
  regardless.
- **`pion/logging` is a second logging vocabulary**, and this document names
  that as a rejection reason elsewhere. Own the counter-argument rather than
  glossing it: it is two files with no dependencies beyond the standard
  library, and its `LoggerFactory` is an interface built to be replaced —
  which `internal/webrtcx/logging.go` does, in about forty lines, so pion never
  writes to stdout.
- **`CGO_ENABLED=0` is verified today, not guaranteed forever.** Any pion
  version bump MUST re-run the four-target build loop:

  ```bash
  for t in linux/amd64 linux/arm64 darwin/arm64 windows/amd64; do
    GOOS=${t%/*} GOARCH=${t#*/} CGO_ENABLED=0 go build ./... || echo "FAIL $t"
  done
  ```
```

- [ ] **Step 9: Commit**

```bash
go mod tidy && gofmt -w internal/webrtcx/
git add go.mod go.sum internal/webrtcx/ docs/DEPENDENCIES.md
git commit -m "deps: pion/webrtc for WHEP, with a slog adapter and the honest bill

Clears the bar gosrt set: FFmpeg cannot do this job. There is no ffmpeg
invocation that performs ICE, DTLS-SRTP and an SDP offer/answer with a browser,
which is the opposite of why yutopp/go-rtmp was refused.

Costs are written into DEPENDENCIES.md rather than discovered later: 18
net-new modules, roughly +7.5 MiB, one module (wlynxg/anet) that is pure dead
weight for our targets, and a second logging vocabulary.

That last one this document names as a rejection reason, so it is neutralised
rather than excused: SlogFactory adapts pion onto slog in forty lines. Without
it pion writes unstructured lines to stdout, which is where the supervisor's
FFmpeg progress parser reads.

CGO_ENABLED=0 verified across all four targets; the loop is now on the
dependency-bump checklist."
```

---

### Task 3: `internal/webrtcx` — the Source, the peer registry and the idle rule

**Files:**
- Create: `internal/webrtcx/source.go`
- Create: `internal/webrtcx/source_test.go`
- Create: `internal/webrtcx/idle.go`
- Create: `internal/webrtcx/idle_test.go`

**Interfaces:**
- Consumes: `SlogFactory` (Task 2); `github.com/pion/webrtc/v4`;
  `github.com/pion/rtp`.
- Produces:
  - `type Config struct { Log *slog.Logger; VideoRTPPort, AudioRTPPort, ICEPort int; PublicIP string; MaxPeers int }`
  - `func NewSource(Config) (*Source, error)`
  - `func (*Source) Offer(ctx context.Context, sdp string) (id string, answer string, err error)`
  - `func (*Source) Hangup(id string) bool`
  - `func (*Source) Peers() int`
  - `func (*Source) Close() error`
  - `var ErrTooManyPeers error`
  - `func Idle(peers int, lastZero time.Time, grace time.Duration, now time.Time) bool`

- [ ] **Step 1: Write the failing idle test**

The lifecycle rule is a pure function so it can be table-tested with no network
at all, which is the idiom `internal/engine/preview_ondemand_test.go` already
uses.

Create `internal/webrtcx/idle_test.go`:

```go
package webrtcx

import (
	"testing"
	"time"
)

func TestIdle(t *testing.T) {
	now := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	const grace = 10 * time.Second

	tests := []struct {
		name     string
		peers    int
		lastZero time.Time
		want     bool
	}{
		{
			name:  "somebody is watching",
			peers: 1, lastZero: now.Add(-time.Hour), want: false,
		},
		{
			// A page reload is a disconnect immediately followed by a new
			// offer. Tearing FFmpeg down in that window and building it back
			// up two seconds later reads to the operator as a broken player.
			name:  "just dropped to zero, inside the grace window",
			peers: 0, lastZero: now.Add(-3 * time.Second), want: false,
		},
		{
			name:  "zero for longer than the grace window",
			peers: 0, lastZero: now.Add(-11 * time.Second), want: true,
		},
		{
			// The zero value means "never had a peer". A tier that started but
			// was never connected to must still be reclaimed, or a failed
			// first offer leaves an FFmpeg running for the session.
			name:  "never had a peer",
			peers: 0, lastZero: time.Time{}, want: true,
		},
		{
			name:  "exactly on the boundary counts as idle",
			peers: 0, lastZero: now.Add(-grace), want: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Idle(tc.peers, tc.lastZero, grace, now); got != tc.want {
				t.Fatalf("Idle(%d, %v, %v, now) = %v, want %v",
					tc.peers, tc.lastZero, grace, got, tc.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/webrtcx/ -run TestIdle -v`
Expected: FAIL to build, `undefined: Idle`.

- [ ] **Step 3: Implement Idle**

Create `internal/webrtcx/idle.go`:

```go
package webrtcx

import "time"

// Idle reports whether the WHEP tier should be torn down.
//
// Unlike the HLS preview, which infers a viewer from playlist polls and has to
// wait ~30s to be sure nobody is there, WebRTC gives an explicit disconnect.
// The signal is a fact rather than a guess, so the window is short.
//
// It is not zero, though, and that is the whole subtlety: a page reload is a
// disconnect immediately followed by a new offer. Cycling FFmpeg on every
// reload costs a fresh IDR wait and reads to the operator as a broken player.
//
// A zero lastZero means the tier has never had a peer at all -- a start whose
// first offer failed -- and must still be reclaimed, or one bad offer leaves an
// FFmpeg running for the rest of the session.
func Idle(peers int, lastZero time.Time, grace time.Duration, now time.Time) bool {
	if peers > 0 {
		return false
	}
	if lastZero.IsZero() {
		return true
	}
	return now.Sub(lastZero) >= grace
}
```

- [ ] **Step 4: Run the idle test**

Run: `go test ./internal/webrtcx/ -run TestIdle -count=1 -v`
Expected: PASS, all five subtests.

- [ ] **Step 5: Write the failing Source tests**

Create `internal/webrtcx/source_test.go`. These are **real** SRTP round trips
between two pion PeerConnections inside one `go test` — no browser, no external
network:

```go
package webrtcx

import (
	"context"
	"io"
	"log/slog"
	"net"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
)

func quietLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func freeUDPPort(t *testing.T) int {
	t.Helper()
	c, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	defer c.Close()
	return c.LocalAddr().(*net.UDPAddr).Port
}

func newSource(t *testing.T, maxPeers int) *Source {
	t.Helper()
	src, err := NewSource(Config{
		Log:          quietLog(),
		VideoRTPPort: freeUDPPort(t),
		AudioRTPPort: freeUDPPort(t),
		MaxPeers:     maxPeers,
	})
	if err != nil {
		t.Fatalf("NewSource: %v", err)
	}
	t.Cleanup(func() { _ = src.Close() })
	return src
}

// receiver is a test-only PeerConnection that offers to receive, exactly as a
// browser's WHEP client does.
func receiver(t *testing.T) (*webrtc.PeerConnection, string) {
	t.Helper()
	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatalf("NewPeerConnection: %v", err)
	}
	t.Cleanup(func() { _ = pc.Close() })
	if _, err := pc.AddTransceiverFromKind(webrtc.RTPCodecTypeVideo,
		webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionRecvonly}); err != nil {
		t.Fatalf("video transceiver: %v", err)
	}
	if _, err := pc.AddTransceiverFromKind(webrtc.RTPCodecTypeAudio,
		webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionRecvonly}); err != nil {
		t.Fatalf("audio transceiver: %v", err)
	}
	offer, err := pc.CreateOffer(nil)
	if err != nil {
		t.Fatalf("CreateOffer: %v", err)
	}
	gathered := webrtc.GatheringCompletePromise(pc)
	if err := pc.SetLocalDescription(offer); err != nil {
		t.Fatalf("SetLocalDescription: %v", err)
	}
	<-gathered
	return pc, pc.LocalDescription().SDP
}

// The answer must offer H.264 and Opus and nothing else. VP8 in the answer is
// not a cosmetic difference: a browser that picks it makes the video path a
// re-encode, which is the exact cost WHEP exists to avoid, and it would do so
// silently.
func TestAnswerNegotiatesH264AndOpusOnly(t *testing.T) {
	src := newSource(t, 3)
	_, offer := receiver(t)

	_, answer, err := src.Offer(context.Background(), offer)
	if err != nil {
		t.Fatalf("Offer: %v", err)
	}
	low := strings.ToLower(answer)
	if !strings.Contains(low, "h264/90000") {
		t.Errorf("no H.264 in the answer; the copy path is gone:\n%s", answer)
	}
	if !strings.Contains(low, "opus/48000") {
		t.Errorf("no Opus in the answer; WebRTC carries no AAC:\n%s", answer)
	}
	for _, banned := range []string{"vp8/90000", "vp9/90000", "av1/90000"} {
		if strings.Contains(low, banned) {
			t.Errorf("%s is negotiable, which makes the video path a re-encode "+
				"the moment a browser prefers it:\n%s", banned, answer)
		}
	}
	if !strings.Contains(low, "packetization-mode=1") {
		t.Errorf("packetization-mode=1 missing; browsers need it for fragmented "+
			"NALs and will show nothing without it:\n%s", answer)
	}
}

func TestPeerCapIsEnforced(t *testing.T) {
	src := newSource(t, 2)

	for i := range 2 {
		_, offer := receiver(t)
		if _, _, err := src.Offer(context.Background(), offer); err != nil {
			t.Fatalf("peer %d rejected under the cap: %v", i, err)
		}
	}
	_, offer := receiver(t)
	if _, _, err := src.Offer(context.Background(), offer); err == nil {
		t.Fatal("a third peer was accepted against a cap of 2. This is not a " +
			"nicety: each peer is a DTLS session plus one SRTP encryption of the " +
			"full ingest bitrate, competing with the encoders that are the product")
	} else if !errorsIsTooManyPeers(err) {
		t.Fatalf("err = %v, want ErrTooManyPeers so the API can answer 429 "+
			"rather than 500", err)
	}
}

func TestHangupFreesASlot(t *testing.T) {
	src := newSource(t, 1)

	_, offer := receiver(t)
	id, _, err := src.Offer(context.Background(), offer)
	if err != nil {
		t.Fatalf("first peer: %v", err)
	}
	if !src.Hangup(id) {
		t.Fatal("Hangup reported no such session for an id it had just issued")
	}
	if got := src.Peers(); got != 0 {
		t.Fatalf("Peers() = %d after Hangup, want 0", got)
	}

	_, offer2 := receiver(t)
	if _, _, err := src.Offer(context.Background(), offer2); err != nil {
		t.Fatalf("the slot was never released: %v", err)
	}
}

// An RTP packet written to the loopback port must reach a connected peer with
// its sequence number and payload intact. This is the whole data path in one
// test, over real SRTP.
func TestRTPReachesThePeer(t *testing.T) {
	src := newSource(t, 1)
	pc, offer := receiver(t)

	got := make(chan *rtp.Packet, 1)
	pc.OnTrack(func(tr *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		if tr.Kind() != webrtc.RTPCodecTypeVideo {
			return
		}
		p, _, err := tr.ReadRTP()
		if err == nil {
			select {
			case got <- p:
			default:
			}
		}
	})

	_, answer, err := src.Offer(context.Background(), offer)
	if err != nil {
		t.Fatalf("Offer: %v", err)
	}
	if err := pc.SetRemoteDescription(webrtc.SessionDescription{
		Type: webrtc.SDPTypeAnswer, SDP: answer,
	}); err != nil {
		t.Fatalf("SetRemoteDescription: %v", err)
	}

	conn, err := net.Dial("udp", src.VideoRTPAddr())
	if err != nil {
		t.Fatalf("dial the RTP port: %v", err)
	}
	defer conn.Close()

	pkt := &rtp.Packet{
		Header:  rtp.Header{Version: 2, PayloadType: 96, SequenceNumber: 4242, Timestamp: 90000, SSRC: 1},
		Payload: []byte{0x09, 0x10, 0x00, 0x00, 0x01, 0x67},
	}
	raw, err := pkt.Marshal()
	if err != nil {
		t.Fatal(err)
	}

	// ICE and DTLS take a moment; resend until it lands or the deadline passes.
	deadline := time.After(10 * time.Second)
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case p := <-got:
			if len(p.Payload) == 0 {
				t.Fatal("packet arrived with an empty payload")
			}
			if p.Payload[len(p.Payload)-1] != 0x67 {
				t.Fatalf("payload corrupted in transit: %v", p.Payload)
			}
			return
		case <-deadline:
			t.Fatal("no RTP reached the peer within 10s")
		case <-tick.C:
			_, _ = conn.Write(raw)
		}
	}
}

// A closed laptop lid leaves a PeerConnection alive until ICE times out, and
// each one is several goroutines plus a DTLS session. Close must reclaim them,
// or a long-running server accumulates them for the life of the process.
func TestCloseReturnsGoroutinesToBaseline(t *testing.T) {
	runtime.GC()
	before := runtime.NumGoroutine()

	src := newSource(t, 3)
	_, offer := receiver(t)
	if _, _, err := src.Offer(context.Background(), offer); err != nil {
		t.Fatalf("Offer: %v", err)
	}
	if err := src.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// pion's own teardown is asynchronous; poll rather than assert instantly.
	for range 100 {
		runtime.GC()
		if runtime.NumGoroutine() <= before+4 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("goroutines = %d after Close, started at %d (allowing +4 for the "+
		"test's own receiver)", runtime.NumGoroutine(), before)
}
```

Add the small helper at the bottom of the file:

```go
func errorsIsTooManyPeers(err error) bool { return errors.Is(err, ErrTooManyPeers) }
```

and `"errors"` to the imports.

- [ ] **Step 6: Run tests to verify they fail**

Run: `go test ./internal/webrtcx/ -run 'TestAnswer|TestPeerCap|TestHangup|TestRTPReaches|TestCloseReturns' -v`
Expected: FAIL to build, `undefined: NewSource`, `undefined: Config`,
`undefined: ErrTooManyPeers`.

- [ ] **Step 7: Implement the Source**

Create `internal/webrtcx/source.go`:

```go
package webrtcx

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
)

// ErrTooManyPeers is returned when the cap is reached. A distinct error rather
// than a generic one so the API answers 429 -- "you, later" -- instead of 500,
// which reads as "we are broken".
var ErrTooManyPeers = errors.New("webrtcx: peer limit reached")

// readBuffer is one RTP packet plus slack. FFmpeg's rtp muxer keeps packets
// under the default 1200-byte MTU; reading into anything smaller would
// silently truncate a NAL fragment and show as green blocks rather than as an
// error.
const readBuffer = 1500

// peerTimeout is how long a PeerConnection may sit in failed/disconnected
// before it is reaped. Without it, a closed laptop lid holds a slot against the
// cap until ICE's own much longer timeout.
const peerTimeout = 15 * time.Second

// Config is everything the Source needs. The two RTP ports are allocated by the
// engine from the shared relay.PortAllocator, so that a WHEP tier cannot land
// on a port a destination is already using.
type Config struct {
	Log *slog.Logger
	// VideoRTPPort and AudioRTPPort are the loopback ports FFmpeg writes RTP
	// to and this package reads from.
	VideoRTPPort int
	AudioRTPPort int
	// ICEPort, when non-zero, muxes all ICE onto one fixed UDP port. That is
	// the DESIGN-ONE-PORT-ONLY doctrine applied to WebRTC: an operator off the
	// LAN forwards exactly one port. Zero means ephemeral, which is correct for
	// localhost and for a browser on the same LAN.
	ICEPort int
	// PublicIP advertises a host candidate at an address this process cannot
	// see, for the box behind 1:1 NAT. Empty is the normal case.
	PublicIP string
	// MaxPeers caps concurrent viewers. This is a monitoring path, not an
	// audience path: every peer costs one SRTP encryption of the full ingest
	// bitrate on the same box that is running the encoders.
	MaxPeers int
}

// Source owns the two shared tracks and every peer reading them.
//
// The tracks are shared deliberately: N peers cost one RTP read loop, not N,
// exactly as relay.Hub gives N consumers one ingest.
type Source struct {
	log      *slog.Logger
	api      *webrtc.API
	cfg      Config
	video    *webrtc.TrackLocalStaticRTP
	audio    *webrtc.TrackLocalStaticRTP
	vconn    *net.UDPConn
	aconn    *net.UDPConn
	wg       sync.WaitGroup
	done     chan struct{}
	closeOne sync.Once

	mu    sync.Mutex
	peers map[string]*webrtc.PeerConnection
}

// NewSource binds the RTP ports and builds the MediaEngine. Nothing is
// negotiated until Offer.
func NewSource(cfg Config) (*Source, error) {
	if cfg.Log == nil {
		return nil, errors.New("webrtcx: no logger")
	}
	if cfg.MaxPeers <= 0 {
		return nil, errors.New("webrtcx: MaxPeers must be positive")
	}

	me := &webrtc.MediaEngine{}
	// H.264 ONLY for video, and the fmtp line is load-bearing. The default
	// MediaEngine also registers VP8, VP9 and AV1; a browser preferring any of
	// them would make the video path a re-encode -- the exact cost this feature
	// exists to avoid -- and would do it silently, with the only symptom being
	// a CPU graph nobody is looking at.
	//
	// packetization-mode=1 is what allows fragmented NALs. Without it a browser
	// negotiates happily and then shows nothing for any frame over the MTU,
	// which is every keyframe.
	if err := me.RegisterCodec(webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{
			MimeType:  webrtc.MimeTypeH264,
			ClockRate: 90000,
			SDPFmtpLine: "level-asymmetry-allowed=1;packetization-mode=1;" +
				"profile-level-id=42e01f",
		},
		PayloadType: 96,
	}, webrtc.RTPCodecTypeVideo); err != nil {
		return nil, fmt.Errorf("webrtcx: register h264: %w", err)
	}
	// WebRTC carries no AAC in any shipping browser, so audio is transcoded to
	// Opus by FFmpeg upstream of here. This registration must match the
	// -payload_type the WHEPArgs command uses or the browser drops the track.
	if err := me.RegisterCodec(webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{
			MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2,
			SDPFmtpLine: "minptime=10;useinbandfec=1",
		},
		PayloadType: 111,
	}, webrtc.RTPCodecTypeAudio); err != nil {
		return nil, fmt.Errorf("webrtcx: register opus: %w", err)
	}

	se := webrtc.SettingEngine{}
	se.LoggerFactory = SlogFactory(cfg.Log)
	if cfg.PublicIP != "" {
		se.SetNAT1To1IPs([]string{cfg.PublicIP}, webrtc.ICECandidateTypeHost)
	}
	if cfg.ICEPort > 0 {
		mux, err := net.ListenUDP("udp", &net.UDPAddr{Port: cfg.ICEPort})
		if err != nil {
			return nil, fmt.Errorf("webrtcx: ice mux port %d: %w", cfg.ICEPort, err)
		}
		se.SetICEUDPMux(webrtc.NewICEUDPMux(nil, mux))
	}

	s := &Source{
		log:   cfg.Log.With("component", "whep"),
		api:   webrtc.NewAPI(webrtc.WithMediaEngine(me), webrtc.WithSettingEngine(se)),
		cfg:   cfg,
		done:  make(chan struct{}),
		peers: map[string]*webrtc.PeerConnection{},
	}

	var err error
	if s.video, err = webrtc.NewTrackLocalStaticRTP(
		webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264, ClockRate: 90000},
		"video", "polyemesis"); err != nil {
		return nil, err
	}
	if s.audio, err = webrtc.NewTrackLocalStaticRTP(
		webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2},
		"audio", "polyemesis"); err != nil {
		return nil, err
	}

	if s.vconn, err = listenRTP(cfg.VideoRTPPort); err != nil {
		return nil, err
	}
	if s.aconn, err = listenRTP(cfg.AudioRTPPort); err != nil {
		s.vconn.Close()
		return nil, err
	}
	s.wg.Add(2)
	go s.pump(s.vconn, s.video, "video")
	go s.pump(s.aconn, s.audio, "audio")
	return s, nil
}

func listenRTP(port int) (*net.UDPConn, error) {
	c, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: port})
	if err != nil {
		return nil, fmt.Errorf("webrtcx: bind rtp %d: %w", port, err)
	}
	// Same reasoning as relay.Hub: a generous receive buffer absorbs a GC pause
	// without losing packets. Best effort; some systems cap it lower.
	_ = c.SetReadBuffer(4 << 20)
	return c, nil
}

// VideoRTPAddr is where FFmpeg should send video RTP.
func (s *Source) VideoRTPAddr() string { return s.vconn.LocalAddr().String() }

// AudioRTPAddr is where FFmpeg should send audio RTP.
func (s *Source) AudioRTPAddr() string { return s.aconn.LocalAddr().String() }

// pump copies RTP from one loopback port onto its shared track.
//
// A malformed packet is dropped rather than fatal: FFmpeg restarting mid-write
// can leave a partial datagram on the wire, and one bad packet must not end the
// monitor for everybody watching.
func (s *Source) pump(conn *net.UDPConn, track *webrtc.TrackLocalStaticRTP, kind string) {
	defer s.wg.Done()
	buf := make([]byte, readBuffer)
	for {
		select {
		case <-s.done:
			return
		default:
		}
		n, err := conn.Read(buf)
		if err != nil {
			select {
			case <-s.done:
				return
			default:
			}
			s.log.Debug("whep rtp read error", "kind", kind, "err", err)
			continue
		}
		if _, err := track.Write(buf[:n]); err != nil {
			// io.ErrClosedPipe here means the last peer went away between the
			// read and the write, which is normal, not an incident.
			s.log.Debug("whep track write failed", "kind", kind, "err", err)
		}
	}
}

// Offer completes a WHEP negotiation and returns the resource id and the SDP
// answer.
func (s *Source) Offer(ctx context.Context, offerSDP string) (string, string, error) {
	s.mu.Lock()
	if len(s.peers) >= s.cfg.MaxPeers {
		n := len(s.peers)
		s.mu.Unlock()
		return "", "", fmt.Errorf("%w: %d of %d connected", ErrTooManyPeers, n, s.cfg.MaxPeers)
	}
	s.mu.Unlock()

	pc, err := s.api.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		return "", "", err
	}
	// Sendonly: this is a monitor. A peer that could send would be an ingest,
	// which is WHIP and is not what this endpoint is.
	if _, err := pc.AddTrack(s.video); err != nil {
		_ = pc.Close()
		return "", "", err
	}
	if _, err := pc.AddTrack(s.audio); err != nil {
		_ = pc.Close()
		return "", "", err
	}

	if err := pc.SetRemoteDescription(webrtc.SessionDescription{
		Type: webrtc.SDPTypeOffer, SDP: offerSDP,
	}); err != nil {
		_ = pc.Close()
		return "", "", fmt.Errorf("webrtcx: bad offer: %w", err)
	}
	answer, err := pc.CreateAnswer(nil)
	if err != nil {
		_ = pc.Close()
		return "", "", err
	}
	// Wait for gathering: WHEP's answer is returned in one HTTP response, so
	// trickle ICE has nowhere to go. On host candidates alone this completes in
	// milliseconds.
	gathered := webrtc.GatheringCompletePromise(pc)
	if err := pc.SetLocalDescription(answer); err != nil {
		_ = pc.Close()
		return "", "", err
	}
	select {
	case <-gathered:
	case <-ctx.Done():
		_ = pc.Close()
		return "", "", ctx.Err()
	}

	id := newID()
	s.mu.Lock()
	s.peers[id] = pc
	s.mu.Unlock()

	pc.OnConnectionStateChange(func(st webrtc.PeerConnectionState) {
		switch st {
		case webrtc.PeerConnectionStateFailed, webrtc.PeerConnectionStateClosed:
			s.Hangup(id)
		case webrtc.PeerConnectionStateDisconnected:
			// Not immediate: "disconnected" is often a transient ICE blip that
			// recovers. Reaping on it would drop a viewer on a Wi-Fi roam.
			time.AfterFunc(peerTimeout, func() {
				if pc.ConnectionState() != webrtc.PeerConnectionStateConnected {
					s.Hangup(id)
				}
			})
		}
	})

	s.log.Info("whep peer connected", "id", id, "peers", s.Peers())
	return id, pc.LocalDescription().SDP, nil
}

// Hangup closes one peer. It reports whether the id was live, so the API can
// answer 404 for a resource that has already gone rather than pretending.
func (s *Source) Hangup(id string) bool {
	s.mu.Lock()
	pc, ok := s.peers[id]
	delete(s.peers, id)
	s.mu.Unlock()
	if !ok {
		return false
	}
	_ = pc.Close()
	s.log.Info("whep peer gone", "id", id, "peers", s.Peers())
	return true
}

// Peers is the live count, which is the ref count the engine's idle sweep runs
// on.
func (s *Source) Peers() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.peers)
}

// Close tears down every peer and both read loops.
func (s *Source) Close() error {
	s.closeOne.Do(func() {
		close(s.done)
		s.mu.Lock()
		for id, pc := range s.peers {
			_ = pc.Close()
			delete(s.peers, id)
		}
		s.mu.Unlock()
		// Closing the sockets is what wakes pump out of Read.
		_ = s.vconn.Close()
		_ = s.aconn.Close()
	})
	// Outside the Once, so a second caller waits too rather than being told the
	// source is closed while the first caller's join is still in progress. Same
	// reasoning as relay.Hub.Close.
	s.wg.Wait()
	return nil
}

func newID() string {
	var b [16]byte
	// crypto/rand rather than math/rand: the resource id is the only thing
	// standing between one authenticated session and another's DELETE.
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// silence the unused import when rtp is only referenced by tests.
var _ = rtp.Packet{}
```

Remove that last line if `rtp` ends up genuinely unused in the non-test file —
`go vet` will say so.

- [ ] **Step 8: Run the package tests**

Run: `gofmt -w internal/webrtcx/ && go test ./internal/webrtcx/ -count=1 -race -v`
Expected: PASS. `TestRTPReachesThePeer` may take a few seconds while ICE and
DTLS complete; that is expected, not a hang.

- [ ] **Step 9: Mutation-test the codec restriction**

This is the guard that protects the copy promise at the negotiation layer.
Temporarily replace the `MediaEngine` construction with
`me := &webrtc.MediaEngine{}; if err := me.RegisterDefaultCodecs(); err != nil { return nil, err }`.
Run: `go test ./internal/webrtcx/ -run TestAnswerNegotiatesH264AndOpusOnly -count=1`
Expected: FAIL naming `vp8/90000`.
Restore by hand, re-run, confirm PASS.

- [ ] **Step 10: Mutation-test the peer cap**

Temporarily change `if len(s.peers) >= s.cfg.MaxPeers` to `if false`.
Run: `go test ./internal/webrtcx/ -run TestPeerCapIsEnforced -count=1`
Expected: FAIL on "a third peer was accepted against a cap of 2".
Restore by hand, re-run, confirm PASS.

- [ ] **Step 11: Commit**

```bash
gofmt -w internal/webrtcx/
go test ./internal/webrtcx/ -count=1 -race
git add internal/webrtcx/
git commit -m "feat(webrtcx): shared H.264/Opus tracks, a peer cap and an idle rule

N peers share two TrackLocalStaticRTP and one RTP read loop each, the same
economy relay.Hub gives N consumers of one ingest.

The MediaEngine registers H.264 and Opus and NOTHING ELSE. The default engine
also offers VP8, VP9 and AV1, and a browser preferring any of them would turn
the video path into a re-encode -- the exact cost this feature exists to avoid
-- silently, with the only symptom a CPU graph nobody is watching.

packetization-mode=1 is equally load-bearing: without it a browser negotiates
happily and then displays nothing for any frame larger than the MTU, which is
every keyframe.

The cap has a named error so the API answers 429 rather than 500. Each peer is
a DTLS session plus one SRTP encryption of the full ingest bitrate, on the same
box running the encoders that are the actual product.

Idle is a pure function with a grace window, because a page reload is a
disconnect immediately followed by a new offer and cycling FFmpeg on every
reload costs a fresh IDR wait."
```

---

### Task 4: `ffmpeg.WHEPArgs` — and the guard on the copy promise

**Files:**
- Modify: `internal/ffmpeg/build.go` (append after `PreviewArgs`, which ends
  around line 985)
- Create: `internal/ffmpeg/whep_test.go`

**Interfaces:**
- Consumes: `commonArgs()` (build.go:20), `progressArgs()` (build.go:26),
  `RelayInputURL(string)` (build.go:351).
- Produces: `type WHEPSpec struct{...}`; `func WHEPArgs(WHEPSpec) []string`.

- [ ] **Step 1: Write the failing test**

Create `internal/ffmpeg/whep_test.go`:

```go
package ffmpeg

import (
	"slices"
	"strings"
	"testing"
)

func joined(args []string) string { return strings.Join(args, " ") }

// argAfter returns the value following flag, or "".
func argAfter(args []string, flag string) string {
	i := slices.Index(args, flag)
	if i < 0 || i+1 >= len(args) {
		return ""
	}
	return args[i+1]
}

// THE guard on polyemesis's central promise, applied to this path.
//
// Video is COPIED. A WHEP monitor that re-encodes costs exactly what the HLS
// preview costs, which removes the entire reason to prefer it, and it would do
// so invisibly -- the picture looks the same either way.
func TestWHEPArgsCopiesVideoByDefault(t *testing.T) {
	args := WHEPArgs(WHEPSpec{
		RelayURL: "udp://127.0.0.1:21000",
		VideoRTP: "rtp://127.0.0.1:22000",
		AudioRTP: "rtp://127.0.0.1:22001",
	})

	if got := argAfter(args, "-c:v"); got != "copy" {
		t.Fatalf("-c:v = %q, want copy. polyemesis copies video; a monitor that "+
			"re-encodes costs what the HLS preview costs and removes the reason "+
			"to prefer it", got)
	}
	for _, enc := range []string{"libx264", "libx265", "h264_nvenc", "h264_qsv", "h264_videotoolbox", "libvpx", "libvpx-vp9"} {
		if slices.Contains(args, enc) {
			t.Errorf("a video encoder (%s) is in the default WHEP command:\n%s", enc, joined(args))
		}
	}
	if slices.Contains(args, "-vf") || slices.Contains(args, "-filter_complex") {
		t.Errorf("a video filter forces a decode+encode:\n%s", joined(args))
	}
}

// Audio is the honest exception, and it must be an OPUS one. No shipping
// browser registers an AAC payload format, so "copy the audio too" is not an
// option that exists.
func TestWHEPArgsTranscodesAudioToOpus(t *testing.T) {
	args := WHEPArgs(WHEPSpec{
		RelayURL: "udp://127.0.0.1:21000",
		VideoRTP: "rtp://127.0.0.1:22000",
		AudioRTP: "rtp://127.0.0.1:22001",
	})
	if got := argAfter(args, "-c:a"); got != "libopus" {
		t.Fatalf("-c:a = %q, want libopus. WebRTC carries no AAC in any shipping "+
			"browser, so this transcode is the price of admission, not a choice", got)
	}
	if !slices.Contains(args, "lowdelay") {
		t.Error("-application lowdelay missing; the default 'audio' tuning adds " +
			"buffering to a path whose entire purpose is being early")
	}
}

// The payload types must match internal/webrtcx's MediaEngine registrations
// exactly, or the browser receives packets it has no codec mapping for and
// silently discards them.
func TestWHEPArgsPayloadTypesMatchTheMediaEngine(t *testing.T) {
	args := WHEPArgs(WHEPSpec{
		RelayURL: "udp://127.0.0.1:21000",
		VideoRTP: "rtp://127.0.0.1:22000",
		AudioRTP: "rtp://127.0.0.1:22001",
	})
	s := joined(args)
	if !strings.Contains(s, "-payload_type 96") {
		t.Errorf("video payload type is not 96; webrtcx registers H.264 as 96:\n%s", s)
	}
	if !strings.Contains(s, "-payload_type 111") {
		t.Errorf("audio payload type is not 111; webrtcx registers Opus as 111:\n%s", s)
	}
}

// SPS/PPS must repeat in band or a viewer joining between IDRs sees green until
// the next one.
func TestWHEPArgsRepeatsParameterSets(t *testing.T) {
	args := WHEPArgs(WHEPSpec{
		RelayURL: "udp://127.0.0.1:21000",
		VideoRTP: "rtp://127.0.0.1:22000",
		AudioRTP: "rtp://127.0.0.1:22001",
	})
	if !strings.Contains(joined(args), "dump_extra") {
		t.Errorf("no dump_extra bitstream filter; a viewer joining between IDRs "+
			"has no SPS/PPS and shows green:\n%s", joined(args))
	}
}

// The escape hatch exists, is opt-in, and is the ONLY way a video encoder gets
// into this command.
func TestWHEPArgsForceReencodeIsOptInAndLabelled(t *testing.T) {
	args := WHEPArgs(WHEPSpec{
		RelayURL:      "udp://127.0.0.1:21000",
		VideoRTP:      "rtp://127.0.0.1:22000",
		AudioRTP:      "rtp://127.0.0.1:22001",
		ForceReencode: true,
	})
	if got := argAfter(args, "-c:v"); got != "libx264" {
		t.Fatalf("-c:v = %q with ForceReencode set, want libx264", got)
	}
	if !slices.Contains(args, "zerolatency") {
		t.Error("the fallback encode is not tuned zerolatency, which defeats the " +
			"point of using it on a sub-second path")
	}
}

// A video-only ingest must not fail the whole command. The '?' on the audio map
// is what tolerates it, exactly as PreviewArgs does.
func TestWHEPArgsToleratesAVideoOnlyIngest(t *testing.T) {
	args := WHEPArgs(WHEPSpec{
		RelayURL: "udp://127.0.0.1:21000",
		VideoRTP: "rtp://127.0.0.1:22000",
		AudioRTP: "rtp://127.0.0.1:22001",
		AudioTrack: 2,
	})
	if !strings.Contains(joined(args), "0:a:2?") {
		t.Errorf("audio map is not optional; a video-only ingest would fail the "+
			"whole command and take the monitor down:\n%s", joined(args))
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/ffmpeg/ -run TestWHEPArgs -v`
Expected: FAIL to build, `undefined: WHEPArgs`, `undefined: WHEPSpec`.

- [ ] **Step 3: Implement WHEPArgs**

Append to `internal/ffmpeg/build.go`, immediately after `PreviewArgs`:

```go
// ------------------------------------------------------------------ whep

// WHEPSpec describes the sub-second monitoring payloader.
type WHEPSpec struct {
	RelayURL string
	// VideoRTP and AudioRTP are rtp:// URLs on loopback that internal/webrtcx
	// is already listening on.
	VideoRTP string
	AudioRTP string
	// AudioTrack is which ingest track the monitor carries. Defaults to 0.
	// WebRTC negotiates ONE audio m-line in practice, so this is a choice
	// between tracks rather than a mix of them -- which is the same limitation
	// RTMP has and is why this is a monitor, not a distribution path.
	AudioTrack int
	// AudioKbps is the Opus bitrate. 0 means the built-in default.
	AudioKbps int
	// ForceReencode turns the video path into an encode.
	//
	// It exists for the ingest whose H.264 profile a browser refuses to
	// negotiate, and it is OFF by default because turning it on costs exactly
	// what the HLS preview costs and removes the reason to prefer this path at
	// all. Anything that flips this default has quietly made the monitor
	// expensive while the UI still calls it cheap.
	ForceReencode bool
	// ReencodeKbps and ReencodeHeight apply only when ForceReencode is set.
	ReencodeKbps   int
	ReencodeHeight int
}

// WHEPArgs builds the RTP payloader that feeds internal/webrtcx.
//
// THE VIDEO IS COPIED. The ingest is already H.264 inside MPEG-TS and RFC 6184
// payloading is a repackaging, not a transcode, so the video path here costs
// approximately no CPU -- which makes the sub-second monitor CHEAPER than the
// HLS preview it sits beside, not more expensive.
//
// THE AUDIO IS RE-ENCODED, and there is no version of this that is not. No
// shipping browser registers an AAC payload format in its WebRTC MediaEngine,
// so AAC -> Opus is the price of admission. It costs single-digit percent of
// one core.
//
// Two outputs from one process, which is deliberate: two processes reading the
// same relay subscription would double the demux and could drift apart on a
// restart, leaving a viewer with video from one run and audio from the next.
func WHEPArgs(s WHEPSpec) []string {
	if s.AudioKbps == 0 {
		s.AudioKbps = 96
	}

	args := commonArgs()
	args = append(args, progressArgs()...)
	args = append(args,
		"-fflags", "+genpts",
		"-thread_queue_size", "1024",
		"-i", RelayInputURL(s.RelayURL),
	)

	args = append(args, "-map", "0:v:0")
	if s.ForceReencode {
		if s.ReencodeKbps == 0 {
			s.ReencodeKbps = 2500
		}
		if s.ReencodeHeight == 0 {
			s.ReencodeHeight = 720
		}
		args = append(args,
			"-c:v", "libx264",
			"-preset", "veryfast",
			"-tune", "zerolatency",
			"-profile:v", "baseline",
			"-b:v", strconv.Itoa(s.ReencodeKbps)+"k",
			"-maxrate", strconv.Itoa(s.ReencodeKbps)+"k",
			"-bufsize", strconv.Itoa(s.ReencodeKbps/2)+"k",
			"-vf", fmt.Sprintf("scale=-2:%d", s.ReencodeHeight),
			// A one-second GOP bounds the join wait, which is the point of
			// paying for an encode on this path at all.
			"-g", "60", "-sc_threshold", "0",
		)
	} else {
		args = append(args, "-c:v", "copy")
	}
	args = append(args,
		// SPS and PPS must repeat in band. MPEG-TS normally carries them ahead
		// of each IDR, but an ingest that does not leaves a viewer who joins
		// between keyframes with no decoder configuration at all -- green
		// blocks until the next IDR, which on a long GOP is seconds.
		"-bsf:v", "dump_extra",
		"-payload_type", "96",
		"-f", "rtp",
		s.VideoRTP,
	)

	args = append(args,
		// '?' tolerates a video-only ingest, exactly as PreviewArgs does: a
		// missing track must degrade the monitor to video, not fail the
		// command and take the whole monitor down.
		"-map", fmt.Sprintf("0:a:%d?", s.AudioTrack),
		"-c:a", "libopus",
		// lowdelay, not the default 'audio' tuning. The default adds lookahead
		// buffering to a path whose entire purpose is being early.
		"-application", "lowdelay",
		// 20ms frames: the WebRTC convention, and what every browser jitter
		// buffer is tuned for.
		"-frame_duration", "20",
		"-b:a", strconv.Itoa(s.AudioKbps)+"k",
		"-ar", "48000",
		"-ac", "2",
		"-payload_type", "111",
		"-f", "rtp",
		s.AudioRTP,
	)
	return args
}
```

`fmt` and `strconv` are already imported by this file.

- [ ] **Step 4: Run the tests**

Run: `gofmt -w internal/ffmpeg/ && go test ./internal/ffmpeg/ -run TestWHEPArgs -count=1 -v`
Expected: PASS, all six tests.

- [ ] **Step 5: Mutation-test the copy guard**

This is the most important mutation test in the plan.
Temporarily change the `else` branch to `args = append(args, "-c:v", "libx264")`.
Run: `go test ./internal/ffmpeg/ -run TestWHEPArgsCopiesVideoByDefault -count=1`
Expected: FAIL on `-c:v = "libx264", want copy`.
Restore by hand, re-run, confirm PASS.

- [ ] **Step 6: Mutation-test the payload types**

Temporarily change `"-payload_type", "96"` to `"-payload_type", "97"`.
Run: `go test ./internal/ffmpeg/ -run TestWHEPArgsPayloadTypes -count=1`
Expected: FAIL naming 96.
Restore by hand, re-run, confirm PASS.

- [ ] **Step 7: Commit**

```bash
go test ./internal/ffmpeg/ -count=1
git add internal/ffmpeg/build.go internal/ffmpeg/whep_test.go
git commit -m "feat(ffmpeg): WHEPArgs, the RTP payloader for sub-second monitoring

Video is COPIED. The ingest is already H.264 in MPEG-TS and RFC 6184 payloading
is a repackaging, so the video path costs approximately no CPU -- which makes
the sub-second monitor CHEAPER than the HLS preview beside it, not dearer.

Audio is re-encoded to Opus and there is no version of this that is not: no
shipping browser registers an AAC payload format in its WebRTC MediaEngine.
Stated in the function comment rather than left to be discovered.

TestWHEPArgsCopiesVideoByDefault is the guard on the central promise: it fails
if a video encoder, a scaler or a filter ever appears in the default command.
ForceReencode is the one opt-in escape hatch, for the ingest whose profile a
browser refuses.

dump_extra repeats SPS/PPS in band. Without it a viewer joining between IDRs
has no decoder configuration and shows green until the next keyframe.

One process with two outputs, not two processes: two readers of the same relay
subscription would double the demux and could drift apart on a restart, leaving
a viewer with video from one run and audio from the next."
```

---

### Task 5: Settings — the `WebRTC` block

**Files:**
- Modify: `internal/db/settings.go` (struct after `PreviewSettings` at :197-212;
  `Settings` field near :1071; defaults near :1245; `Validate` near :1427)
- Create: `internal/db/webrtc_settings_test.go`

**Interfaces:**
- Consumes: the `Settings.Validate()` `add(...)` idiom.
- Produces: `type WebRTCSettings struct{...}`; `Settings.WebRTC WebRTCSettings`
  with JSON tag `webrtc`; the constants `MinWHEPPeers`, `MaxWHEPPeers`,
  `DefaultWHEPPeers`.

- [ ] **Step 1: Write the failing test**

Create `internal/db/webrtc_settings_test.go`:

```go
package db

import (
	"strings"
	"testing"
)

func validateWebRTC(t *testing.T, mut func(*Settings)) []string {
	t.Helper()
	s := DefaultSettings()
	mut(&s)
	err := s.Validate()
	if err == nil {
		return nil
	}
	return strings.Split(err.Error(), "; ")
}

// The cap is the whole reason this is safe to ship. An operator who types 500
// into a box has not made a monitoring tool; they have made an unauthenticated-
// in-effect origin server that competes with their own encoders for CPU.
func TestWebRTCPeerCapIsBounded(t *testing.T) {
	if got := validateWebRTC(t, func(s *Settings) { s.WebRTC.MaxPeers = 500 }); got == nil {
		t.Fatal("500 concurrent WHEP peers was accepted. Each peer is a DTLS " +
			"session plus one SRTP encryption of the full ingest bitrate on the " +
			"same box running the encoders")
	}
	if got := validateWebRTC(t, func(s *Settings) { s.WebRTC.MaxPeers = 0 }); got == nil {
		t.Fatal("a peer cap of 0 was accepted; it must be at least 1 or the " +
			"feature is enabled and unusable")
	}
	if got := validateWebRTC(t, func(s *Settings) { s.WebRTC.MaxPeers = MaxWHEPPeers }); got != nil {
		t.Fatalf("the documented maximum was rejected: %v", got)
	}
}

func TestWebRTCICEPortIsBounded(t *testing.T) {
	// 0 is meaningful: ephemeral ports, which is right for localhost and LAN.
	if got := validateWebRTC(t, func(s *Settings) { s.WebRTC.ICEPort = 0 }); got != nil {
		t.Fatalf("0 (ephemeral) was rejected, but it is the correct default: %v", got)
	}
	if got := validateWebRTC(t, func(s *Settings) { s.WebRTC.ICEPort = 70000 }); got == nil {
		t.Fatal("port 70000 was accepted")
	}
	if got := validateWebRTC(t, func(s *Settings) { s.WebRTC.ICEPort = -1 }); got == nil {
		t.Fatal("port -1 was accepted")
	}
}

// Off by default. An upgrade of an existing install must not silently start
// binding a UDP port and offering a media path nobody asked for.
func TestWebRTCIsOffByDefault(t *testing.T) {
	s := DefaultSettings()
	if s.WebRTC.Enabled {
		t.Fatal("WHEP is enabled by default; an upgrade would start binding a " +
			"UDP port and offering a media path the operator never asked for")
	}
	if s.WebRTC.MaxPeers != DefaultWHEPPeers {
		t.Errorf("MaxPeers default = %d, want %d", s.WebRTC.MaxPeers, DefaultWHEPPeers)
	}
	if s.WebRTC.ForceReencode {
		t.Error("ForceReencode defaults on, which makes the monitor cost exactly " +
			"what the HLS preview costs while the UI still calls it cheap")
	}
}

func TestWebRTCIdleGraceIsBounded(t *testing.T) {
	if got := validateWebRTC(t, func(s *Settings) { s.WebRTC.IdleGraceSeconds = 1 }); got == nil {
		t.Fatal("a 1s grace window was accepted; a page reload takes longer than " +
			"that and would cycle FFmpeg on every refresh")
	}
	if got := validateWebRTC(t, func(s *Settings) { s.WebRTC.IdleGraceSeconds = 100000 }); got == nil {
		t.Fatal("a 100000s grace window was accepted, which means the encoder " +
			"never stops")
	}
	if got := validateWebRTC(t, func(s *Settings) { s.WebRTC.IdleGraceSeconds = 0 }); got != nil {
		t.Fatalf("0 (use the built-in default) was rejected: %v", got)
	}
}
```

If `DefaultSettings` is named differently in this package, read
`internal/db/settings.go` around line 1230 and use the real name; the defaults
block that Task 5 modifies is the same function.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/db/ -run TestWebRTC -v`
Expected: FAIL to build — `s.WebRTC undefined`, `undefined: MaxWHEPPeers`.

- [ ] **Step 3: Add the struct and bounds**

In `internal/db/settings.go`, immediately after the `PreviewSettings` struct
(ends line 212):

```go
// WHEP bounds. The peer cap is the one that matters: it is what keeps this a
// monitoring feature rather than an origin server.
const (
	MinWHEPPeers = 1
	// MaxWHEPPeers is deliberately small. Every peer is a DTLS session plus one
	// SRTP encryption of the FULL ingest bitrate, performed on the same box
	// that is running the encoders which are the actual product. At 6 Mbit/s,
	// eight peers is 48 Mbit/s of encryption competing with them. A number an
	// operator could mistake for a viewer count belongs on internal/playout,
	// which serves files off a disk and scales the way this cannot.
	MaxWHEPPeers = 8
	// DefaultWHEPPeers is an operator, a second screen, and one spare.
	DefaultWHEPPeers = 3
	// DefaultWHEPIdleGraceSeconds outlives a page reload -- which is a
	// disconnect immediately followed by a new offer -- without outliving an
	// operator closing the tab.
	DefaultWHEPIdleGraceSeconds = 10
	MinWHEPIdleGraceSeconds     = 3
	MaxWHEPIdleGraceSeconds     = 600
)

// WebRTCSettings controls the sub-second WHEP self-monitor.
//
// This is a MONITORING path, not a distribution one, and the shape of this
// struct says so: there is a peer cap and there is no public flag. Serving an
// audience is what internal/playout is for.
type WebRTCSettings struct {
	// Enabled is off by default so an upgrade never starts binding a port or
	// offering a media path the operator did not ask for.
	Enabled bool `json:"enabled"`
	// MaxPeers caps concurrent monitors. See MaxWHEPPeers for why it is small.
	MaxPeers int `json:"maxPeers"`
	// IdleGraceSeconds is how long the payloader outlives the last peer. Far
	// shorter than the HLS preview's 30s because WebRTC gives an explicit
	// disconnect rather than an inferred one -- but not zero, because a page
	// reload is a disconnect immediately followed by a new offer. 0 means the
	// built-in default.
	IdleGraceSeconds int `json:"idleGraceSeconds"`
	// AudioTrack is which ingest track the monitor carries. WebRTC negotiates
	// one audio m-line in practice, so this selects rather than mixes -- the
	// same limitation RTMP has, and another reason this is not a production
	// path.
	AudioTrack int `json:"audioTrack"`
	AudioKbps  int `json:"audioKbps"`
	// ICEPort muxes all ICE onto one fixed UDP port, so an operator off the LAN
	// forwards exactly one -- the DESIGN-ONE-PORT-ONLY doctrine applied to
	// WebRTC. 0 means ephemeral, which is correct for localhost and for a
	// browser on the same LAN and needs no firewall change at all.
	ICEPort int `json:"icePort"`
	// PublicIP advertises a host candidate at an address this process cannot
	// see, for a box behind 1:1 NAT. Empty is the normal case.
	//
	// There is deliberately no STUN setting and there will never be a TURN one.
	// STUN buys almost nothing for a server whose address the operator already
	// knows, and if a viewer's network blocks UDP the honest answer is that the
	// HLS preview still works -- which is why WHEP sits beside it rather than
	// replacing it.
	PublicIP string `json:"publicIp"`
	// ForceReencode turns the video path into an encode, for the ingest whose
	// H.264 profile a browser refuses. OFF by default: turning it on costs
	// exactly what the HLS preview costs and removes the reason to prefer this
	// path at all.
	ForceReencode bool `json:"forceReencode"`
}
```

- [ ] **Step 4: Add the field, the defaults and the validation**

In the `Settings` struct, immediately after `Preview PreviewSettings` (near
line 1071):

```go
	// WebRTC is the sub-second WHEP self-monitor, beside the HLS preview
	// rather than instead of it: HLS is the compatibility floor for any network
	// that blocks UDP.
	WebRTC WebRTCSettings `json:"webrtc"`
```

In the defaults, immediately after the `Preview:` block (ends near line 1260):

```go
		// Off, so an upgrade changes nothing at all, but described in full so
		// the settings page has something to render and an operator can see
		// what turning it on would do.
		WebRTC: WebRTCSettings{
			Enabled:          false,
			MaxPeers:         DefaultWHEPPeers,
			IdleGraceSeconds: DefaultWHEPIdleGraceSeconds,
			AudioTrack:       0,
			AudioKbps:        96,
			ICEPort:          0,
			ForceReencode:    false,
		},
```

In `Validate`, immediately after the preview idle-timeout check (line 1437):

```go
	if s.WebRTC.MaxPeers < MinWHEPPeers || s.WebRTC.MaxPeers > MaxWHEPPeers {
		add("webrtc peer limit %d out of range (%d-%d)", s.WebRTC.MaxPeers, MinWHEPPeers, MaxWHEPPeers)
	}
	if g := s.WebRTC.IdleGraceSeconds; g != 0 && (g < MinWHEPIdleGraceSeconds || g > MaxWHEPIdleGraceSeconds) {
		add("webrtc idle grace %ds out of range (%d-%d, or 0 for the default)",
			g, MinWHEPIdleGraceSeconds, MaxWHEPIdleGraceSeconds)
	}
	// 0 means ephemeral, which is the default and is right for localhost and
	// for a browser on the same LAN.
	if p := s.WebRTC.ICEPort; p != 0 && (p < 1 || p > 65535) {
		add("webrtc ICE port %d out of range (1-65535, or 0 for ephemeral)", p)
	}
	if s.WebRTC.AudioTrack < 0 {
		add("webrtc audio track %d is negative", s.WebRTC.AudioTrack)
	}
	if b := s.WebRTC.AudioKbps; b < 32 || b > 256 {
		add("webrtc audio bitrate %dkbps out of range (32-256)", b)
	}
```

- [ ] **Step 5: Run the tests**

Run: `gofmt -w internal/db/ && go test ./internal/db/ -run TestWebRTC -count=1 -v`
Expected: PASS, all four tests.

- [ ] **Step 6: Mutation-test the peer cap**

Temporarily change `s.WebRTC.MaxPeers > MaxWHEPPeers` to
`s.WebRTC.MaxPeers > 100000`.
Run: `go test ./internal/db/ -run TestWebRTCPeerCapIsBounded -count=1`
Expected: FAIL on "500 concurrent WHEP peers was accepted".
Restore by hand, re-run, confirm PASS.

- [ ] **Step 7: Run the whole db package**

Run: `go test ./internal/db/ -count=1`
Expected: PASS. If a settings round-trip or golden-JSON test fails, it is
because the new block changed the serialised shape; update that fixture — do
not remove the field from the JSON.

- [ ] **Step 8: Commit**

```bash
gofmt -w internal/db/
git add internal/db/settings.go internal/db/webrtc_settings_test.go
git commit -m "feat(db): WebRTC settings for the WHEP self-monitor

Off by default, so an upgrade never starts binding a UDP port or offering a
media path the operator did not ask for.

MaxWHEPPeers is 8 and that is deliberate, not timid. Every peer is a DTLS
session plus one SRTP encryption of the FULL ingest bitrate on the same box
running the encoders that are the actual product. A number an operator could
mistake for a viewer count belongs on internal/playout, which serves files off
a disk and scales the way this cannot.

There is no public flag, no STUN setting and there will never be a TURN one.
STUN buys almost nothing for a server whose address the operator already knows,
and if a viewer's network blocks UDP the honest answer is that the HLS preview
still works -- which is exactly why WHEP sits beside it."
```

---

### Task 6: Engine — the on-demand WHEP tier

**Files:**
- Modify: `internal/engine/engine.go`
- Create: `internal/engine/whep_test.go`

**Interfaces:**
- Consumes: `webrtcx.NewSource/Offer/Hangup/Peers/Close/Idle`,
  `ffmpeg.WHEPArgs`, `supervisor.New`, `relay.PortAllocator`.
- Produces: `(*Engine).WHEPOffer(ctx, sdp) (id, answer string, err error)`;
  `(*Engine).WHEPHangup(id) bool`; `(*Engine).WHEPPeers() int`;
  `Status.WHEP *WHEPStatus`.

- [ ] **Step 1: Write the failing test**

Create `internal/engine/whep_test.go`. These test the *decision* functions
without spawning FFmpeg, which is the same seam
`preview_ondemand_test.go` uses:

```go
package engine

import (
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
)

func TestWhepGraceWindow(t *testing.T) {
	tests := []struct {
		name string
		set  int
		want time.Duration
	}{
		{name: "unset means the built-in default", set: 0, want: whepIdleDefault},
		{name: "a stored value wins", set: 25, want: 25 * time.Second},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := db.Settings{WebRTC: db.WebRTCSettings{IdleGraceSeconds: tc.set}}
			if got := whepGrace(s); got != tc.want {
				t.Fatalf("whepGrace = %v, want %v", got, tc.want)
			}
		})
	}
}

// The signature decides when a running payloader is replaced. Anything in it
// that should not be there cycles the monitor -- and cycling it costs the
// viewer a fresh IDR wait, which is the one thing this feature is trying to
// avoid.
func TestWhepSigNoticesWhatChangesTheCommand(t *testing.T) {
	base := db.Settings{WebRTC: db.WebRTCSettings{
		Enabled: true, MaxPeers: 3, AudioTrack: 0, AudioKbps: 96,
	}}

	changed := []struct {
		name string
		mut  func(*db.Settings)
	}{
		{"audio track", func(s *db.Settings) { s.WebRTC.AudioTrack = 1 }},
		{"audio bitrate", func(s *db.Settings) { s.WebRTC.AudioKbps = 128 }},
		{"force reencode", func(s *db.Settings) { s.WebRTC.ForceReencode = true }},
	}
	for _, tc := range changed {
		t.Run(tc.name+" restarts", func(t *testing.T) {
			s := base
			tc.mut(&s)
			if whepSig(s) == whepSig(base) {
				t.Fatalf("%s does not change the signature, so the running "+
					"payloader would keep the old argument", tc.name)
			}
		})
	}

	unchanged := []struct {
		name string
		mut  func(*db.Settings)
	}{
		// The cap is enforced in webrtcx per offer. Restarting FFmpeg to apply
		// it would drop every peer that is under the new limit anyway.
		{"peer cap", func(s *db.Settings) { s.WebRTC.MaxPeers = 5 }},
		// Changing how long an idle monitor lingers must never cycle a live
		// one, for the same reason Preview.IdleTimeoutSeconds is absent from
		// previewSig.
		{"idle grace", func(s *db.Settings) { s.WebRTC.IdleGraceSeconds = 30 }},
	}
	for _, tc := range unchanged {
		t.Run(tc.name+" does not restart", func(t *testing.T) {
			s := base
			tc.mut(&s)
			if whepSig(s) != whepSig(base) {
				t.Fatalf("%s changes the signature, so a live monitor would be "+
					"cycled and every viewer would pay a fresh IDR wait for "+
					"nothing", tc.name)
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/engine/ -run TestWhep -v`
Expected: FAIL to build, `undefined: whepGrace`, `undefined: whepSig`.

- [ ] **Step 3: Add the constants and Engine fields**

In the `const` block at `internal/engine/engine.go:50-69`, after
`previewStartDebounce`:

```go
	// whepIdleDefault is how long the WHEP payloader outlives its last peer.
	// A third of the preview's window, because WebRTC gives an explicit
	// disconnect where HLS can only infer one from a poll that stopped.
	whepIdleDefault = 10 * time.Second
	// whepSweep is how often idleness is re-evaluated. Tight, because the
	// signal it acts on is a fact rather than a guess.
	whepSweep = 2 * time.Second
	// whepStartDebounce keeps a browser retrying a failing offer from turning
	// into a burst of spawns, exactly as previewStartDebounce does.
	whepStartDebounce = 2 * time.Second
	// whepSubName is the payloader's subscription on the ingest hub.
	whepSubName = "whep"
)
```

In the `Engine` struct, beside the preview fields (`engine.go:114`, `:196-233`):

```go
	whep *supervisor.Process
```
```go
	whepSrc  *webrtcx.Source
	whepSig  string
	whepIn   int // hub subscription port
	whepV    int // video RTP port
	whepA    int // audio RTP port
	whepZero time.Time
	whepAt   time.Time
```
```go
	// whepMu serialises WHEP lifecycle changes. Taken AHEAD of e.mu, matching
	// previewMu's order, because the same shape of work happens here: a start
	// allocates ports and binds sockets outside the engine lock and only then
	// publishes the result under it.
	whepMu sync.Mutex
```

Import `"github.com/rainmanjam/polyemesis/internal/webrtcx"`.

- [ ] **Step 4: Add the decision functions**

Append near `previewSig` (`engine.go:1230`):

```go
// whepSig hashes the arguments a running payloader was built with.
//
// The peer cap is deliberately absent: it is enforced per offer inside
// webrtcx, and restarting FFmpeg to apply it would drop every peer that is
// under the new limit anyway. The idle grace is absent for the same reason it
// is absent from previewSig -- changing how long an idle monitor lingers must
// never cycle a live one.
func whepSig(s db.Settings) string {
	return fmt.Sprintf("%d/%d/%t", s.WebRTC.AudioTrack, s.WebRTC.AudioKbps, s.WebRTC.ForceReencode)
}

func whepGrace(s db.Settings) time.Duration {
	if s.WebRTC.IdleGraceSeconds <= 0 {
		return whepIdleDefault
	}
	return time.Duration(s.WebRTC.IdleGraceSeconds) * time.Second
}
```

- [ ] **Step 5: Add the lifecycle**

Append a new section to `internal/engine/engine.go` (placed after the preview
block, so the two read together):

```go
// ------------------------------------------------------------------- whep
//
// The sub-second self-monitor. Structurally the preview's twin -- on demand,
// ref-counted, its own relay subscription so nothing it does can touch a
// destination -- with one difference that matters: the liveness signal is an
// explicit WebRTC disconnect rather than a playlist poll that stopped, so the
// idle window is 10s instead of 30 and the sweep is 2s instead of 5.

// WHEPOffer completes a WHEP negotiation, starting the payloader if it is down.
//
// The payloader starts BEFORE the answer is returned rather than after, because
// a browser that gets an answer and then waits for RTP that is not flowing yet
// shows a black rectangle with no explanation. Starting first means the first
// packet is at most an IDR away.
func (e *Engine) WHEPOffer(ctx context.Context, offerSDP string) (string, string, error) {
	e.mu.RLock()
	s, stopped := e.settings, e.stopped
	e.mu.RUnlock()

	if stopped {
		return "", "", errors.New("engine is stopping")
	}
	if !s.WebRTC.Enabled {
		return "", "", errors.New("webrtc monitoring is disabled")
	}

	e.whepMu.Lock()
	if err := e.ensureWHEPLocked(s); err != nil {
		e.whepMu.Unlock()
		return "", "", err
	}
	src := e.whepSource()
	e.whepMu.Unlock()

	if src == nil {
		return "", "", errors.New("webrtc monitor did not start")
	}
	id, answer, err := src.Offer(ctx, offerSDP)
	if err != nil {
		return "", "", err
	}
	e.publishStatus()
	return id, answer, nil
}

// WHEPHangup closes one monitor. False means the id was already gone, which the
// API reports as 404 rather than pretending it did something.
func (e *Engine) WHEPHangup(id string) bool {
	src := e.whepSource()
	if src == nil {
		return false
	}
	ok := src.Hangup(id)
	if ok {
		e.mu.Lock()
		if src.Peers() == 0 {
			e.whepZero = time.Now()
		}
		e.mu.Unlock()
		e.publishStatus()
	}
	return ok
}

// WHEPPeers is the live monitor count.
func (e *Engine) WHEPPeers() int {
	if src := e.whepSource(); src != nil {
		return src.Peers()
	}
	return 0
}

func (e *Engine) whepSource() *webrtcx.Source {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.whepSrc
}

// ensureWHEPLocked starts the tier if it is down. The caller must hold whepMu.
func (e *Engine) ensureWHEPLocked(s db.Settings) error {
	e.mu.RLock()
	running := e.whepSrc != nil
	last := e.whepAt
	e.mu.RUnlock()
	if running {
		return nil
	}
	// A browser retrying a failing offer must not become a burst of spawns.
	if !last.IsZero() && time.Since(last) < whepStartDebounce {
		return errors.New("webrtc monitor is starting; retry in a moment")
	}
	return e.startWHEPLocked(s)
}

// startWHEPLocked allocates three loopback ports, binds the two RTP sockets and
// spawns the payloader. The caller must hold whepMu.
func (e *Engine) startWHEPLocked(s db.Settings) error {
	e.mu.Lock()
	stopped := e.stopped
	// Recorded even when the start below fails, so a failing start backs off
	// rather than being retried on every offer.
	e.whepAt = time.Now()
	e.mu.Unlock()
	if stopped {
		return errors.New("engine is stopping")
	}

	in, err := e.alloc.Allocate()
	if err != nil {
		return fmt.Errorf("whep: no relay port: %w", err)
	}
	vport, err := e.alloc.Allocate()
	if err != nil {
		e.alloc.Release(in)
		return fmt.Errorf("whep: no video rtp port: %w", err)
	}
	aport, err := e.alloc.Allocate()
	if err != nil {
		e.alloc.Release(in)
		e.alloc.Release(vport)
		return fmt.Errorf("whep: no audio rtp port: %w", err)
	}

	src, err := webrtcx.NewSource(webrtcx.Config{
		Log:          e.log,
		VideoRTPPort: vport,
		AudioRTPPort: aport,
		ICEPort:      s.WebRTC.ICEPort,
		PublicIP:     s.WebRTC.PublicIP,
		MaxPeers:     s.WebRTC.MaxPeers,
	})
	if err != nil {
		e.alloc.Release(in)
		e.alloc.Release(vport)
		e.alloc.Release(aport)
		return fmt.Errorf("whep: %w", err)
	}

	// The INGEST hub, deliberately, and not downstreamHub(): this sits beside
	// the HLS preview in the same card showing the same picture with less
	// delay. Two monitors reading different hubs would show an operator two
	// different pictures with nothing on screen to say why.
	url := e.hub.Subscribe(whepSubName, in)

	proc := supervisor.New(e.log, supervisor.Spec{
		Name: "whep", Kind: "whep", Bin: e.tools.FFmpeg,
		Args: ffmpeg.WHEPArgs(ffmpeg.WHEPSpec{
			RelayURL:      url,
			VideoRTP:      "rtp://" + src.VideoRTPAddr(),
			AudioRTP:      "rtp://" + src.AudioRTPAddr(),
			AudioTrack:    s.WebRTC.AudioTrack,
			AudioKbps:     s.WebRTC.AudioKbps,
			ForceReencode: s.WebRTC.ForceReencode,
		}),
		AutoRestart: true, OnLog: e.onLog, OnState: e.onState, LogSink: logSink{e},
	})

	e.mu.Lock()
	// Re-check under the lock: Stop may have set e.stopped while we were
	// binding sockets, and publishing a process into a stopping engine orphans
	// it. Same guard as startRendition (engine.go:2320-2327).
	if e.stopped {
		e.mu.Unlock()
		_ = src.Close()
		e.hub.Unsubscribe(whepSubName)
		e.alloc.Release(in)
		e.alloc.Release(vport)
		e.alloc.Release(aport)
		return errors.New("engine is stopping")
	}
	e.whep, e.whepSrc = proc, src
	e.whepIn, e.whepV, e.whepA = in, vport, aport
	e.whepSig = whepSig(s)
	e.whepZero = time.Now()
	e.mu.Unlock()

	proc.Start()
	e.log.Info("whep monitor started on demand",
		"copy", !s.WebRTC.ForceReencode, "maxPeers", s.WebRTC.MaxPeers)
	return nil
}

// stopWHEPLocked tears the tier down. The caller must hold whepMu.
//
// Order matters and mirrors teardownRendition: the peers go first so nothing is
// mid-write, then the process, then the subscription, and the ports last.
func (e *Engine) stopWHEPLocked() {
	e.mu.Lock()
	src, in, vport, aport := e.whepSrc, e.whepIn, e.whepV, e.whepA
	e.whepSrc, e.whepIn, e.whepV, e.whepA = nil, 0, 0, 0
	e.whepSig = ""
	e.mu.Unlock()

	if src != nil {
		_ = src.Close()
	}
	e.stopAux(&e.whep, "whep")
	e.hub.Unsubscribe(whepSubName)
	if in > 0 {
		e.alloc.Release(in)
	}
	if vport > 0 {
		e.alloc.Release(vport)
	}
	if aport > 0 {
		e.alloc.Release(aport)
	}
}

// reconcileWHEP applies settings changes, but never conjures a monitor out of
// nothing: like the preview, the tier is demand-driven from WHEPOffer.
func (e *Engine) reconcileWHEP(s db.Settings) {
	e.whepMu.Lock()
	defer e.whepMu.Unlock()

	e.mu.RLock()
	src, sig := e.whepSrc, e.whepSig
	e.mu.RUnlock()

	if !s.WebRTC.Enabled {
		if src != nil {
			e.stopWHEPLocked()
		}
		return
	}
	if src == nil || sig == whepSig(s) {
		return
	}
	// A signature change means the command is wrong for every current viewer,
	// so there is no version of this that does not interrupt them. Stop and let
	// the next offer start it: restarting eagerly would spend an IDR wait on a
	// monitor nobody may reconnect to.
	e.stopWHEPLocked()
}

// whepLoop reclaims the tier once the last peer has been gone for the grace
// window.
func (e *Engine) whepLoop(ctx context.Context) {
	tick := time.NewTicker(whepSweep)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			e.sweepWHEP(time.Now())
		}
	}
}

func (e *Engine) sweepWHEP(now time.Time) {
	e.mu.RLock()
	s, src, zero := e.settings, e.whepSrc, e.whepZero
	e.mu.RUnlock()
	if src == nil {
		return
	}
	peers := src.Peers()
	if peers > 0 {
		// Reset the clock while somebody is watching, so the grace window is
		// measured from the moment the LAST viewer left rather than from the
		// first time the count touched zero.
		e.mu.Lock()
		e.whepZero = now
		e.mu.Unlock()
		return
	}
	if !webrtcx.Idle(peers, zero, whepGrace(s), now) {
		return
	}

	e.whepMu.Lock()
	// An offer may have landed while we were taking the lock, and that viewer
	// is now watching a monitor we were about to kill.
	e.mu.RLock()
	src = e.whepSrc
	e.mu.RUnlock()
	stop := src != nil && src.Peers() == 0
	if stop {
		e.stopWHEPLocked()
	}
	e.whepMu.Unlock()

	if stop {
		e.log.Info("whep idle; payloader stopped", "after", whepGrace(s))
		e.publishStatus()
	}
}
```

- [ ] **Step 6: Wire it into Start, Reconcile, Stop and Status**

1. In `Start` (`engine.go:494`, beside `previewLoop`):

```go
	e.wg.Add(1)
	go func() { defer e.wg.Done(); e.whepLoop(e.ctx) }()
```

2. In `Reconcile` (`engine.go:788`, immediately after `e.reconcilePreview(settings)`):

```go
	e.reconcileWHEP(settings)
```

3. In `Stop`, the lock block at `engine.go:540-544` takes
   `previewMu -> selMu -> mu`. Add `whepMu` immediately after `previewMu` and
   release it beside it at `:577`, keeping the documented order intact. Capture
   the process alongside the others at `:574`, add `e.whep = nil` and
   `e.whepSrc = nil` to the clearing line, and in phase 1 (`:601`, beside
   `stop(preview)`):

```go
	// The peers first, so no SRTP write lands on a track whose payloader has
	// already gone. Then the process, in the same parallel phase as the
	// preview: it is a hub consumer exactly as the preview is.
	if whepSrc != nil {
		_ = whepSrc.Close()
	}
	stop(whep)
```

4. In the `Status` struct (`engine.go:4736`, after `Preview`):

```go
	// WHEP is the sub-second monitor, absent unless it is running. It carries
	// the peer count as well as the process state, because "running with
	// nobody watching" is a state an operator will see for a few seconds after
	// closing the tab and must not read as a fault.
	WHEP *WHEPStatus `json:"whep,omitempty"`
```

and the type beside it:

```go
// WHEPStatus is the monitor tier's state.
type WHEPStatus struct {
	Process *supervisor.Status `json:"process,omitempty"`
	Peers   int                `json:"peers"`
	MaxPeers int               `json:"maxPeers"`
	// Copying is false when ForceReencode is on. Surfaced because an operator
	// who has switched the fallback on has doubled the monitor's cost and the
	// dashboard is the only place that can say so.
	Copying bool `json:"copying"`
}
```

5. In `Status()` (`engine.go:4816`), capture `e.whep, e.whepSrc` under the
   existing `RLock` and populate:

```go
	if whep != nil || whepSrc != nil {
		w := &WHEPStatus{Process: procStatus(whep), MaxPeers: s.WebRTC.MaxPeers,
			Copying: !s.WebRTC.ForceReencode}
		if whepSrc != nil {
			w.Peers = whepSrc.Peers()
		}
		st.WHEP = w
	}
```

reading `s` from `e.Settings()` as the surrounding code does.

- [ ] **Step 7: Run the tests**

Run: `gofmt -w internal/engine/ && go test ./internal/engine/ -run TestWhep -count=1 -v`
Expected: PASS.

- [ ] **Step 8: Run the whole engine package under race**

Run: `go test ./internal/engine/ -count=1 -race`
Expected: PASS. A deadlock here means the `whepMu` ordering was not inserted
consistently — it must be taken *before* `e.mu` everywhere, and `Stop` must take
it in the documented sequence.

- [ ] **Step 9: Mutation-test the signature**

Temporarily change `whepSig` to include `s.WebRTC.MaxPeers`.
Run: `go test ./internal/engine/ -run TestWhepSig -count=1`
Expected: FAIL on "peer cap does not restart".
Restore by hand, re-run, confirm PASS.

- [ ] **Step 10: Commit**

```bash
gofmt -w internal/engine/
go test ./internal/engine/ -count=1 -race
git add internal/engine/engine.go internal/engine/whep_test.go
git commit -m "feat(engine): on-demand WHEP monitor tier

Structurally the preview's twin -- demand-driven, ref-counted, its own relay
subscription so nothing it does can reach a destination -- with one difference
that matters: the liveness signal is an explicit WebRTC disconnect rather than
a playlist poll that stopped, so the idle window is 10s and the sweep 2s
instead of 30 and 5.

It reads the INGEST hub, not downstreamHub(). This sits beside the HLS preview
in the same card showing the same picture with less delay; two monitors on
different hubs would show an operator two different pictures with nothing on
screen to say why. The consequence -- with failover on, both show the ingest
rather than the on-air source -- is a known wart recorded in the plan, not an
oversight.

The payloader starts BEFORE the answer is returned: a browser that gets an
answer and then waits for RTP that is not flowing yet shows a black rectangle
with no explanation.

whepSig deliberately excludes the peer cap and the idle grace. The cap is
enforced per offer in webrtcx and restarting to apply it would drop every peer
already under the new limit; the grace is excluded for the same reason
Preview.IdleTimeoutSeconds is excluded from previewSig. Cycling this tier costs
every viewer a fresh IDR wait, which is the one thing it exists to avoid.

whepMu is taken ahead of e.mu, matching previewMu, and Stop closes the peers
before the process so no SRTP write lands on a track whose payloader has gone."
```

---

### Task 7: The API — POST an offer, DELETE the resource

**Files:**
- Create: `internal/api/whep.go`
- Create: `internal/api/whep_test.go`
- Modify: `internal/api/api.go` (register routes in the authenticated + CSRF
  group, after the `/processes` lines near :385)

**Interfaces:**
- Consumes: `(*Engine).WHEPOffer/WHEPHangup/WHEPPeers`,
  `webrtcx.ErrTooManyPeers`, `s.requireAuth`, `s.requireCSRF`.
- Produces: `POST /api/v1/whep` (request `application/sdp`, response 201 with
  `application/sdp` and a `Location` header); `DELETE /api/v1/whep/{id}`;
  `GET /api/v1/whep` returning `{enabled, secureContext, peers, maxPeers, reason}`.

- [ ] **Step 1: Write the failing test**

Create `internal/api/whep_test.go`, copying the fixture pattern from
`internal/api/token_handlers_test.go:42` (`newTestServer` and the authenticated
request helper) rather than inventing a new one:

```go
package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The monitor is behind the session, full stop. It is a media path off the
// operator's own ingest; an unauthenticated one is a stream key with extra
// steps. internal/playout is the anonymous path and it serves files off a disk,
// which is the difference.
func TestWHEPRequiresAuthentication(t *testing.T) {
	s := newTestServer(t)

	for _, tc := range []struct {
		method, path string
	}{
		{http.MethodPost, "/api/v1/whep"},
		{http.MethodDelete, "/api/v1/whep/abc123"},
		{http.MethodGet, "/api/v1/whep"},
	} {
		req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(""))
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, req)

		if w.Code != http.StatusUnauthorized && w.Code != http.StatusForbidden {
			t.Errorf("%s %s answered %d unauthenticated; want 401 or 403",
				tc.method, tc.path, w.Code)
		}
	}
}

// An SDP offer is not JSON and must not be accepted as if it were: handing a
// JSON body to pion's SDP parser produces a 500 and a stack trace where a 415
// would have told the client exactly what was wrong.
func TestWHEPRejectsANonSDPBody(t *testing.T) {
	s := newTestServer(t)
	enableWHEP(t, s)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/whep",
		strings.NewReader(`{"sdp":"v=0"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, authed(t, s, req))

	if w.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("status = %d, want 415 for a JSON body on an application/sdp "+
			"endpoint", w.Code)
	}
}

func TestWHEPRefusedWhenDisabled(t *testing.T) {
	s := newTestServer(t) // WebRTC.Enabled defaults false

	req := httptest.NewRequest(http.MethodPost, "/api/v1/whep",
		strings.NewReader("v=0\r\n"))
	req.Header.Set("Content-Type", "application/sdp")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, authed(t, s, req))

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 when the feature is off. A 404 would be "+
			"indistinguishable from a build without the feature, and a 500 would "+
			"read as broken", w.Code)
	}
}

// The status route is what lets the UI explain "you are on plain HTTP so your
// browser will not allow this" instead of showing a player that silently never
// starts.
func TestWHEPStatusReportsTheSecureContextPrecondition(t *testing.T) {
	s := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/whep", nil)
	req.Host = "box.lan:8080" // plain http, not loopback
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, authed(t, s, req))

	var got struct {
		Enabled       bool   `json:"enabled"`
		SecureContext bool   `json:"secureContext"`
		Reason        string `json:"reason"`
		MaxPeers      int    `json:"maxPeers"`
	}
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.SecureContext {
		t.Fatal("plain http on a routable host was reported as a secure context; " +
			"browsers refuse WebRTC there and the player would fail silently")
	}
	if got.Reason == "" {
		t.Error("no reason given, so the UI has nothing to show but a broken player")
	}
}

func TestWHEPStatusAcceptsLoopbackOverPlainHTTP(t *testing.T) {
	s := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/whep", nil)
	req.Host = "localhost:8080"
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, authed(t, s, req))

	var got struct {
		SecureContext bool `json:"secureContext"`
	}
	_ = json.NewDecoder(w.Body).Decode(&got)
	if !got.SecureContext {
		t.Fatal("localhost over http was reported insecure; browsers treat " +
			"loopback as a secure context and this is the single most common way " +
			"an operator runs polyemesis")
	}
}
```

Add the two helpers at the bottom:

```go
// enableWHEP flips the setting on the test server's store and reconciles, so
// the handler is exercised in its enabled state.
func enableWHEP(t *testing.T, s *Server) {
	t.Helper()
	set, err := s.store.GetSettings()
	if err != nil {
		t.Fatal(err)
	}
	set.WebRTC.Enabled = true
	if err := s.store.PutSettings(set); err != nil {
		t.Fatal(err)
	}
	if err := s.mgr.Reconcile(); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/api/ -run TestWHEP -v`
Expected: FAIL — 404 for every route.

- [ ] **Step 3: Write the handlers**

Create `internal/api/whep.go`:

```go
package api

// WHEP — the sub-second self-monitor.
//
// Scope, because the route table is where somebody will one day be tempted to
// widen it: this is the OPERATOR watching their own feed. It is authenticated,
// CSRF-protected and capped, and it is deliberately NOT wired into
// internal/playout's anonymous token path. Playout serves segment files off a
// disk, which is a thing that scales; this performs one SRTP encryption of the
// full ingest bitrate per peer on the same box that is running the encoders.
// Those are different economics and they get different doors.
//
// WHEP, not WHIP. WHIP publishes INTO a server; WHEP receives FROM one. This is
// the receive side. If a publish side is ever built it is an ingest mode and it
// goes somewhere else entirely.

import (
	"errors"
	"io"
	"net"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/rainmanjam/polyemesis/internal/webrtcx"
)

// maxOfferBytes bounds the SDP body. A real offer is a couple of kilobytes;
// anything near this is not one, and reading an unbounded body into memory on
// an authenticated route is still a way to be knocked over by an authenticated
// mistake.
const maxOfferBytes = 64 << 10

// handleWHEPOffer completes a negotiation and returns the SDP answer.
//
// The WHEP draft specifies 201 Created with the answer as the body and a
// Location header naming the resource to DELETE. Following it means an
// off-the-shelf WHEP client works against this endpoint even though the UI
// ships its own.
func (s *Server) handleWHEPOffer(w http.ResponseWriter, r *http.Request) {
	eng := s.eng()
	if eng == nil {
		writeError(w, http.StatusServiceUnavailable, "no engine is running")
		return
	}
	set, err := s.store.GetSettings()
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if !set.WebRTC.Enabled {
		// 503, not 404: a 404 is indistinguishable from a build without the
		// feature, and a 500 reads as broken. This is "configured off".
		writeError(w, http.StatusServiceUnavailable,
			"webrtc monitoring is disabled in settings")
		return
	}

	// An SDP offer is not JSON. Accepting a JSON body would hand it to pion's
	// SDP parser and produce a 500 with a stack trace, where 415 says exactly
	// what is wrong.
	if ct := r.Header.Get("Content-Type"); ct != "" &&
		!strings.HasPrefix(strings.ToLower(ct), "application/sdp") {
		writeError(w, http.StatusUnsupportedMediaType,
			"a WHEP offer must be sent as application/sdp")
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, maxOfferBytes))
	if err != nil {
		writeError(w, http.StatusBadRequest, "could not read the offer")
		return
	}
	if len(body) == 0 {
		writeError(w, http.StatusBadRequest, "empty offer")
		return
	}

	id, answer, err := eng.WHEPOffer(r.Context(), string(body))
	switch {
	case errors.Is(err, webrtcx.ErrTooManyPeers):
		// 429, not 503. "You, later" rather than "we are broken" -- and the
		// body says how many are connected so an operator can tell whether it
		// is their own second tab.
		writeError(w, http.StatusTooManyRequests, err.Error())
		return
	case err != nil:
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/sdp")
	w.Header().Set("Location", "/api/v1/whep/"+id)
	// The answer is a live media session, not a document.
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusCreated)
	_, _ = io.WriteString(w, answer)
}

// handleWHEPHangup closes one monitor.
func (s *Server) handleWHEPHangup(w http.ResponseWriter, r *http.Request) {
	eng := s.eng()
	if eng == nil {
		writeError(w, http.StatusServiceUnavailable, "no engine is running")
		return
	}
	if !eng.WHEPHangup(chi.URLParam(r, "id")) {
		// 404 rather than 204: a client that thinks it hung something up when
		// it did not will not retry, and a leaked peer holds a slot against
		// the cap.
		writeError(w, http.StatusNotFound, "no such monitor session")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleWHEPStatus tells the UI whether it can even try.
//
// Browsers refuse WebRTC outside a secure context, so on plain HTTP against a
// routable hostname the player would fail with nothing on screen to explain it.
// Answering that question here rather than in the browser means the UI can say
// "serve polyemesis over HTTPS" instead of showing a black rectangle.
func (s *Server) handleWHEPStatus(w http.ResponseWriter, r *http.Request) {
	set, err := s.store.GetSettings()
	if err != nil {
		writeStoreError(w, err)
		return
	}
	secure, reason := secureContext(s.cfg.ResolvedTLSMode() != "", r)

	peers := 0
	if eng := s.eng(); eng != nil {
		peers = eng.WHEPPeers()
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled":       set.WebRTC.Enabled,
		"secureContext": secure,
		"reason":        reason,
		"peers":         peers,
		"maxPeers":      set.WebRTC.MaxPeers,
		"copying":       !set.WebRTC.ForceReencode,
	})
}

// secureContext reports whether the browser will permit WebRTC on this origin,
// and why not when it will not.
//
// Loopback is the carve-out that matters: it is how most operators run
// polyemesis, browsers treat it as secure with no certificate, and forgetting
// it would gate the feature off for the most common deployment there is.
func secureContext(tlsOn bool, r *http.Request) (bool, string) {
	host := r.Host
	if h, _, err := net.SplitHostPort(r.Host); err == nil {
		host = h
	}
	if strings.EqualFold(host, "localhost") {
		return true, ""
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return true, ""
	}
	if r.TLS != nil || tlsOn {
		return true, ""
	}
	return false, "Your browser only allows WebRTC on a secure origin. " +
		"polyemesis is being served over plain HTTP on " + host + ", so the " +
		"sub-second monitor cannot start. Serve it over HTTPS, reach it through " +
		"a proxy that does, or open it on localhost. The HLS preview works either way."
}
```

- [ ] **Step 4: Register the routes**

In `internal/api/api.go`, in the authenticated + CSRF group after
`r.Get("/processes/{name}/logs", s.handleProcessLogs)` (line 386):

```go
			// WHEP: the sub-second self-monitor. Inside requireAuth AND
			// requireCSRF on purpose. Unlike the HLS preview -- where hls.js
			// cannot attach headers and the route has to ride the session
			// cookie alone -- the WHEP client is our own fetch() call, so it
			// can send the CSRF header like every other state-changing route.
			// That is a real advantage of this transport and it is taken.
			//
			// It is deliberately NOT on the anonymous playout path. See the
			// scope note at the top of whep.go.
			r.Get("/whep", s.handleWHEPStatus)
			r.Post("/whep", s.handleWHEPOffer)
			r.Delete("/whep/{id}", s.handleWHEPHangup)
```

- [ ] **Step 5: Run the tests**

Run: `gofmt -w internal/api/ && go test ./internal/api/ -run TestWHEP -count=1 -v`
Expected: PASS, all five tests.

- [ ] **Step 6: Mutation-test the auth guard**

Temporarily move the three route registrations outside the
`r.Use(s.requireAuth)` group (into the top-level `/api/v1` block).
Run: `go test ./internal/api/ -run TestWHEPRequiresAuthentication -count=1`
Expected: FAIL naming at least one route.
Restore by hand, re-run, confirm PASS.

- [ ] **Step 7: Mutation-test the secure-context carve-out**

Temporarily delete the `strings.EqualFold(host, "localhost")` branch.
Run: `go test ./internal/api/ -run TestWHEPStatusAcceptsLoopback -count=1`
Expected: FAIL.
Restore by hand, re-run, confirm PASS.

- [ ] **Step 8: Commit**

```bash
gofmt -w internal/api/
go test ./internal/api/ -count=1
git add internal/api/whep.go internal/api/whep_test.go internal/api/api.go
git commit -m "feat(api): WHEP offer, hangup and precondition routes

Behind requireAuth AND requireCSRF. Unlike the HLS preview -- where hls.js
cannot attach headers, so the route has to ride the session cookie alone -- the
WHEP client is our own fetch(), which can send the CSRF header like every other
state-changing route. That advantage of the transport is taken rather than
wasted.

Deliberately NOT on internal/playout's anonymous path. Playout serves segment
files off a disk; this performs one SRTP encryption of the full ingest bitrate
per peer on the box running the encoders. Different economics, different doors.

Status codes chosen so the client can act on them: 503 for configured-off
(404 would be indistinguishable from a build without the feature, 500 reads as
broken), 429 with the connected count for the peer cap (\"you, later\" rather
than \"we are broken\"), 415 for a JSON body (which would otherwise reach pion's
SDP parser and return a 500 with a stack trace), 404 for a hangup on an id
already gone (204 would let a client believe it released a slot it did not).

GET /whep answers the secure-context question server-side, so the UI can say
'serve this over HTTPS' instead of showing a player that silently never starts.
Loopback is carved out because it is how most operators run polyemesis and
browsers treat it as secure with no certificate."
```

---

### Task 8: The UI — a monitor toggle beside the preview

**Files:**
- Create: `ui/src/components/WhepPlayer.tsx`
- Modify: `ui/src/lib/types.ts` (add `WebRTCSettings` to `Settings`, `WHEPStatus`
  to `Status`, and a `WhepPrecondition` type)
- Modify: `ui/src/lib/api.ts` (add `whepStatus`, and raw SDP POST/DELETE)
- Modify: the dashboard component that renders `<PreviewPlayer>` (find it with
  `grep -rn "PreviewPlayer" ui/src`)

**Interfaces:**
- Consumes: `GET/POST/DELETE /api/v1/whep` from Task 7.
- Produces: `<WhepPlayer active fallback={<PreviewPlayer .../>} />`.

- [ ] **Step 1: Add the types**

In `ui/src/lib/types.ts`, beside `PreviewSettings`:

```ts
/** The sub-second WHEP self-monitor. A monitoring path, not a distribution
 *  one: there is a peer cap and there is deliberately no public flag. */
export interface WebRTCSettings {
  enabled: boolean;
  maxPeers: number;
  idleGraceSeconds: number;
  audioTrack: number;
  audioKbps: number;
  icePort: number;
  publicIp: string;
  /** Turns the video path into an encode, for an ingest whose H.264 profile a
   *  browser refuses. Off by default: on, the monitor costs exactly what the
   *  HLS preview costs and the reason to prefer it disappears. */
  forceReencode: boolean;
}

export interface WHEPStatus {
  process?: ProcessStatus;
  peers: number;
  maxPeers: number;
  /** False when forceReencode is on. */
  copying: boolean;
}

/** Answered server-side, because a browser refusing WebRTC on an insecure
 *  origin presents as a player that never starts rather than as an error. */
export interface WhepPrecondition {
  enabled: boolean;
  secureContext: boolean;
  reason: string;
  peers: number;
  maxPeers: number;
  copying: boolean;
}
```

Add `webrtc: WebRTCSettings;` to the `Settings` interface and
`whep?: WHEPStatus;` to `Status`. If the existing process-status type is named
something other than `ProcessStatus`, use the real name.

- [ ] **Step 2: Add the API calls**

In `ui/src/lib/api.ts`, beside the existing entries:

```ts
  whepStatus: () => get<WhepPrecondition>("/whep"),
```

and, because the existing `post` helper serialises JSON and an SDP offer is not
JSON, a pair of raw calls next to it:

```ts
  /** WHEP negotiation. Raw SDP in, raw SDP out, per the WHEP draft — so an
   *  off-the-shelf client works against the same endpoint. The CSRF header is
   *  attached the same way every other state-changing call attaches it; unlike
   *  hls.js, a fetch() can. */
  whepOffer: async (sdp: string): Promise<{ id: string; answer: string }> => {
    const res = await fetch(`${BASE}/whep`, {
      method: "POST",
      credentials: "same-origin",
      headers: { "Content-Type": "application/sdp", ...csrfHeader() },
      body: sdp,
    });
    if (!res.ok) throw new Error(await res.text());
    const location = res.headers.get("Location") ?? "";
    return { id: location.split("/").pop() ?? "", answer: await res.text() };
  },

  whepHangup: (id: string) =>
    fetch(`${BASE}/whep/${id}`, {
      method: "DELETE",
      credentials: "same-origin",
      headers: { ...csrfHeader() },
      // keepalive so a hangup fired from beforeunload actually leaves. Without
      // it, closing the tab leaks a peer that holds a slot against the cap
      // until ICE times out.
      keepalive: true,
    }),
```

Read `ui/src/lib/api.ts` around the `get`/`post` helpers and reuse whatever it
already calls `BASE` and whatever it already uses to attach the CSRF header —
do not introduce a second mechanism. If there is no `csrfHeader()` helper,
extract one from the existing `post` implementation in the same commit.

- [ ] **Step 3: Verify types compile**

Run: `cd ui && npx tsc -b --noEmit`
Expected: clean.

- [ ] **Step 4: Write the player**

Create `ui/src/components/WhepPlayer.tsx`:

```tsx
import { useEffect, useRef, useState } from "react";
import { api } from "@/lib/api";
import type { WhepPrecondition } from "@/lib/types";
import { cn } from "@/lib/utils";

/** Sub-second WHEP monitor of the operator's own feed.
 *
 *  This is a MONITOR, not a player for an audience. The server caps concurrent
 *  peers at a handful, the route is authenticated, and if it will not connect
 *  the answer is the HLS preview -- which is why this component takes a
 *  `fallback` rather than trying to be the only option.
 *
 *  Video arrives H.264 stream-copied from the ingest, so it costs the server
 *  almost nothing; audio is transcoded to Opus because WebRTC carries no AAC.
 *
 *  The first frame waits for the next IDR. On a two-second GOP that is up to
 *  two seconds of nothing, and there is no fix that does not re-encode -- hence
 *  the explicit "waiting for a keyframe" state rather than a black rectangle. */
export function WhepPlayer({
  active,
  fallback,
  className,
}: {
  active: boolean;
  fallback: React.ReactNode;
  className?: string;
}) {
  const videoRef = useRef<HTMLVideoElement>(null);
  const [pre, setPre] = useState<WhepPrecondition | null>(null);
  const [state, setState] = useState<"idle" | "connecting" | "waiting" | "playing" | "failed">("idle");
  const [error, setError] = useState("");

  useEffect(() => {
    void api.whepStatus().then(setPre).catch(() => setPre(null));
  }, [active]);

  useEffect(() => {
    if (!active || !pre?.enabled || !pre.secureContext) return;
    const video = videoRef.current;
    if (!video) return;

    let pc: RTCPeerConnection | null = new RTCPeerConnection();
    let resourceId = "";
    let cancelled = false;

    // Receive only. A monitor that could send would be an ingest, which is a
    // different protocol (WHIP) and a different endpoint.
    pc.addTransceiver("video", { direction: "recvonly" });
    pc.addTransceiver("audio", { direction: "recvonly" });
    pc.ontrack = (ev) => {
      video.srcObject = ev.streams[0];
      void video.play().catch(() => {});
    };
    pc.onconnectionstatechange = () => {
      if (!pc) return;
      if (pc.connectionState === "connected") setState("waiting");
      if (pc.connectionState === "failed") {
        setState("failed");
        setError(
          "The connection failed. This usually means UDP is blocked between " +
            "your browser and the server. The HLS preview still works.",
        );
      }
    };
    // framesDecoded, not a visibility check: a black frame and a wedged
    // connection both look "visible".
    const onProgress = () => setState("playing");
    video.addEventListener("timeupdate", onProgress, { once: true });

    const connect = async () => {
      setState("connecting");
      try {
        const offer = await pc!.createOffer();
        await pc!.setLocalDescription(offer);
        // Wait for gathering: the WHEP answer comes back in one HTTP response,
        // so trickle ICE has nowhere to go.
        await new Promise<void>((resolve) => {
          if (pc!.iceGatheringState === "complete") return resolve();
          const check = () => {
            if (pc!.iceGatheringState === "complete") {
              pc!.removeEventListener("icegatheringstatechange", check);
              resolve();
            }
          };
          pc!.addEventListener("icegatheringstatechange", check);
        });

        const { id, answer } = await api.whepOffer(pc!.localDescription!.sdp);
        if (cancelled) return;
        resourceId = id;
        await pc!.setRemoteDescription({ type: "answer", sdp: answer });
      } catch (e) {
        setState("failed");
        setError(e instanceof Error ? e.message : String(e));
      }
    };
    void connect();

    // Closing the tab must release the slot. Without this the peer lives until
    // ICE times out, holding a slot against a cap that is only a handful wide.
    const bye = () => {
      if (resourceId) void api.whepHangup(resourceId);
    };
    window.addEventListener("beforeunload", bye);

    return () => {
      cancelled = true;
      window.removeEventListener("beforeunload", bye);
      video.removeEventListener("timeupdate", onProgress);
      bye();
      pc?.close();
      pc = null;
      video.srcObject = null;
      setState("idle");
    };
  }, [active, pre?.enabled, pre?.secureContext]);

  if (!pre?.enabled) return <>{fallback}</>;
  if (!pre.secureContext) {
    return (
      <div className={cn("space-y-2", className)}>
        <p className="rounded border border-warn/30 bg-warn-dim px-2 py-1 text-[10px] text-warn">
          {pre.reason}
        </p>
        {fallback}
      </div>
    );
  }
  if (state === "failed") {
    return (
      <div className={cn("space-y-2", className)}>
        <p className="rounded border border-warn/30 bg-warn-dim px-2 py-1 text-[10px] text-warn">
          {error}
        </p>
        {fallback}
      </div>
    );
  }

  return (
    <div className={cn("relative", className)}>
      <video ref={videoRef} muted playsInline className="w-full" />
      {state !== "playing" && (
        <div className="absolute inset-0 grid place-items-center text-[10px] text-muted-foreground">
          {state === "waiting"
            ? "Waiting for a keyframe…"
            : "Connecting…"}
        </div>
      )}
    </div>
  );
}
```

Match the warning classes to whatever `ui/src/components/DestinationCard.tsx:203`
actually uses — the goal is that this warning looks like every other warning in
the product.

- [ ] **Step 5: Wire it into the dashboard**

Find the render site and wrap rather than replace:

```bash
grep -rn "PreviewPlayer" ui/src
```

Replace `<PreviewPlayer active={...} />` with:

```tsx
<WhepPlayer
  active={...}
  fallback={<PreviewPlayer active={...} />}
/>
```

The nesting is the design: WHEP is the latency path and HLS is the
compatibility floor, so the HLS player is what is shown whenever WHEP is off,
insecure, or has failed. There is no state in which the operator sees nothing.

- [ ] **Step 6: Run the UI gates**

Run: `cd ui && npx tsc -b --noEmit && npm run lint && npm run build`
Expected: all three clean.

- [ ] **Step 7: Commit**

```bash
git add ui/src/components/WhepPlayer.tsx ui/src/lib/types.ts ui/src/lib/api.ts
git add ui/src/pages ui/src/components   # whatever the render site turned out to be
git commit -m "feat(ui): sub-second WHEP monitor, with the HLS preview as its fallback

The nesting is the design. WHEP is the latency path; HLS is the compatibility
floor. The HLS player renders whenever WHEP is off, the origin is insecure, or
the connection failed, so there is no state in which the operator sees nothing.

The secure-context precondition comes from the server, not from the browser: a
browser refusing WebRTC on plain HTTP presents as a player that never starts,
which is indistinguishable from a broken feed. Answering it server-side lets
the UI say 'serve this over HTTPS' instead.

'Waiting for a keyframe' is an explicit state rather than a black rectangle.
The first frame waits for the next IDR -- up to two seconds on a two-second GOP
-- and there is no fix for that which does not re-encode.

beforeunload fires a keepalive DELETE. Without it, closing the tab leaks a peer
that holds a slot against a cap only a handful wide until ICE times out."
```

---

### Task 9: Acceptance, documentation, and full verification

**Files:**
- Create: `scripts/acceptance-whep.sh`
- Create: `cmd/whepcheck/main.go` (a Go WHEP client used only by that script;
  gated behind a build tag so it is not in the shipped binary)
- Modify: `docs/roadmap/WEBRTC.md` (status DEFERRED → WHEP DONE)
- Modify: `docs/MONITORING.md`, `docs/COMPARISON.md`
- Delete: `docs/roadmap/WEBRTC-SPIKE.md` (folded into `WEBRTC.md`)

**Interfaces:** consumes every route and setting above; produces no new API.

- [ ] **Step 1: Write the acceptance driver**

`cmd/whepcheck/main.go` acts as a WHEP client with pion as the receiver, and
prints measurements rather than asserting thresholds — this project's style.
Model the script on the existing `scripts/acceptance-*.sh`; read one first, and
reuse the same synthetic `testsrc2` three-tone feed the other suites generate so
the audio assertion below can be the same one `acceptance-audio.sh` already
makes.

It must report, in this order:

1. **The SDP answer carries exactly one H.264 m-line and one Opus m-line**, with
   payload types 96 and 111 — the numbers `internal/webrtcx` registers.
2. **`framesDecoded` rising across two samples**, not merely non-zero. A wedged
   connection and a single decoded frame both pass "non-zero".
3. **An IDR arrives within 10 s of connecting** — count H.264 NAL type 5 in the
   depayloaded stream.
4. **The correct audio track was selected**: decode the received Opus and run
   the same `astats` bandpass tone check `acceptance-audio.sh` uses. Proving
   *audio arrived* is not the same as proving the *right track* arrived, and on
   a multitrack ingest that difference is the whole feature.
5. **Ref-count proof**: no FFmpeg child named `whep` before the first POST; one
   after; **gone within `grace + whepSweep + 2 s`** of the DELETE. Read it from
   `GET /api/v1/processes`.
6. **Peer cap proof**: `MaxPeers + 1` simultaneous offers, the last answered
   **429**.
7. **Latency, reported as a number, not asserted**: RTP timestamp against
   arrival wall clock. In this project's style — the HLS figure in
   `docs/roadmap/LL-HLS.md` is a measurement, and this one must be too.

- [ ] **Step 2: Run it**

Run: `bash scripts/acceptance-whep.sh`
Expected: every check reports PASS, and the latency line prints a figure.
**Record that figure** — it is the only honest thing that can be written in the
docs, and "sub-second" is a claim until it is measured.

- [ ] **Step 3: Update the roadmap honestly**

In `docs/roadmap/WEBRTC.md`, change the status line to:

```markdown
**Status: WHEP DONE (2026-07-31). WHIP still DEFERRED.**

WHEP shipped as the sub-second self-monitor: `internal/webrtcx`,
`ffmpeg.WHEPArgs`, the engine's on-demand tier and `POST /api/v1/whep`.
Measured latency: <FIGURE FROM STEP 2>, against 2.2–3.2 s for the tuned HLS
preview it sits beside.

Scope shipped is narrower than the design above and deliberately so: it is an
OPERATOR MONITOR, capped at 8 concurrent peers, behind the session. It is not
an audience path and it is not on the anonymous playout route.

WHIP -- browser-to-server ingest -- remains deferred and undesigned. It has the
same limitation RTMP has (one audio track, so it cannot feed the
per-destination multitrack routing that is the reason this product exists) and
deserves its own decision rather than riding in behind WHEP.
```

Fold the spike findings into a `## Measured` section and delete
`WEBRTC-SPIKE.md`.

- [ ] **Step 4: Document it for operators**

Add a section to `docs/MONITORING.md`:

```markdown
## Sub-second monitoring (WebRTC / WHEP)

Off by default. Turn it on in Settings → Preview.

**What it is for:** watching your own feed with roughly a quarter of the HLS
preview's delay, so that "is my audio clipping" is a question about now.

**What it is not for:** an audience. The peer limit is 8 and defaults to 3.
Every viewer costs the server one encryption pass over the full ingest bitrate
on the same machine that is running your encoders. If you want people to watch,
use Playout — it serves files off a disk and scales the way this cannot.

**Cost:** approximately none for video, which is stream-copied from the ingest
exactly as your destinations are. Audio is re-encoded to Opus, which costs a few
percent of one core, because no browser can play AAC over WebRTC.

**Requirements:**

- **HTTPS, or localhost.** Browsers refuse WebRTC on an insecure origin. If you
  reach polyemesis over plain `http://` on a hostname or an IP, the monitor will
  say so and the HLS preview will be shown instead.
- **UDP between your browser and the server.** On the same LAN, nothing to
  configure. Off the LAN, set an ICE port in settings and forward that one UDP
  port. There is no TURN relay and there will not be one — if UDP is blocked,
  the HLS preview still works, which is why both are there.

**The first two seconds:** the picture starts at the next keyframe, so on a
2-second GOP you may see "Waiting for a keyframe…" for up to two seconds after
connecting. Shortening the GOP on your encoder shortens that wait. There is no
server-side fix that does not re-encode the video.
```

Add a row to `docs/COMPARISON.md` next to the existing preview-latency line.

- [ ] **Step 5: Run every gate CI runs, in CI's order**

```bash
gofmt -l ./cmd ./internal | tee /tmp/fmt && test ! -s /tmp/fmt
go build ./...
go vet ./...
go test -race ./...
cd ui && npx tsc -b --noEmit && npm run lint && npm run build
```

Expected: gofmt prints nothing; build and vet silent; every package `ok`; all
three UI gates clean.

- [ ] **Step 6: Re-run the four-target cgo-free build**

```bash
for t in linux/amd64 linux/arm64 darwin/arm64 windows/amd64; do
  GOOS=${t%/*} GOARCH=${t#*/} CGO_ENABLED=0 go build ./... \
    && echo "OK $t" || echo "FAIL $t"
done
```

Expected: four `OK` lines. This is constraint 2 and 3 of
`docs/DEPENDENCIES.md`, and pion is the first dependency in this repo with a
realistic chance of breaking it.

- [ ] **Step 7: Confirm the copy promise is not quietly broken**

```bash
grep -n "libx264\|libx265\|nvenc\|_qsv\|videotoolbox\|libvpx" internal/ffmpeg/build.go | grep -i -A2 -B2 whep
```

Expected: the ONLY hits inside `WHEPArgs` are inside the `if s.ForceReencode`
branch. Any encoder on the default path is the promise being broken silently.

- [ ] **Step 8: Confirm the monitor cannot be reached anonymously**

```bash
grep -n "whep" internal/api/api.go
grep -rn "whep" internal/playout/
```

Expected: three route registrations, all inside the `requireAuth` +
`requireCSRF` group; **zero** hits in `internal/playout`. A hit in playout means
the monitor has been wired onto the anonymous path, which is the one thing this
design refuses.

- [ ] **Step 9: Commit**

```bash
gofmt -w ./cmd ./internal
git add scripts/acceptance-whep.sh cmd/whepcheck docs/
git rm docs/roadmap/WEBRTC-SPIKE.md
git commit -m "test(whep): acceptance suite, and the docs that state the scope

The acceptance suite measures rather than asserts where it can: it prints the
RTP-to-wall-clock latency as a figure, in the style docs/roadmap/LL-HLS.md set,
because 'sub-second' is a claim until somebody has measured it.

It also proves the things that could pass wrongly. framesDecoded must RISE
across two samples, since a wedged connection and one decoded frame both clear
'non-zero'. The received Opus goes through the same astats bandpass tone check
acceptance-audio.sh already uses, because proving audio arrived is not the same
as proving the RIGHT track arrived -- and on a multitrack ingest that difference
is the whole feature.

The docs state the scope in the operator's words: this is for watching your own
feed, the peer limit is 8, and if you want an audience the answer is Playout,
which serves files off a disk and scales the way this cannot."
```

---

## Self-Review

**Brief coverage:**

| Asked for | Where |
|---|---|
| WHIP endpoint for self-monitoring | Renamed to WHEP with the reason stated up front; Tasks 3, 7 |
| Scope it honestly — monitoring, not audience | Global Constraints; peer cap in Tasks 3, 5, 7; `docs/MONITORING.md` in Task 9 |
| pion/webrtc as the likely library | Task 2, with the measured dependency bill written into `DEPENDENCIES.md` |
| MPEG-TS → WebRTC tracks: depayload or re-encode | Task 4 — FFmpeg `-c:v copy -f rtp` payloads; nothing is depayloaded in Go |
| Does it break the copy promise | "The copy promise" section; guard `TestWHEPArgsCopiesVideoByDefault` (Task 4) and its mutation test |
| H.264 vs VP8 | H.264 only; `TestAnswerNegotiatesH264AndOpusOnly` fails if VP8/VP9/AV1 become negotiable |
| Opus vs AAC — AAC is not a WebRTC codec | Task 4: `-c:a libopus`, stated as the price of admission; `TestWHEPArgsTranscodesAudioToOpus` |
| Authentication | Task 7: `requireAuth` + `requireCSRF`, never the playout anonymous path; two mutation tests |
| How many viewers is sane | 3 default, 8 hard maximum, with the reasoning (one SRTP pass per peer over the full bitrate); Tasks 3 and 5 |

**Mutation tests, one per guard:** codec restriction (3.9), peer cap in webrtcx
(3.10), settings peer cap (5.6), `-c:v copy` (4.5), payload types (4.6),
restart signature (6.9), route authentication (7.6), loopback secure-context
carve-out (7.7). Every one names the expected failure and the file to restore.

**Placeholder scan:** no TBD, no "add appropriate error handling". Three steps
deliberately point at existing code rather than inventing: the API test fixture
(`internal/api/token_handlers_test.go:42`), the warning styling
(`ui/src/components/DestinationCard.tsx:203`), and the acceptance harness
(`scripts/acceptance-*.sh` plus the `astats` tone check
`acceptance-audio.sh` already performs). Two values are genuinely unknown until
measured and are marked as such rather than guessed: the spike's
`profile-level-id` (Task 1 Step 2) and the acceptance latency figure (Task 9
Step 2, written into the docs at Step 3).

**Type consistency:** payload types 96/111 appear in `internal/webrtcx`'s
`MediaEngine`, in `ffmpeg.WHEPArgs`, and in the acceptance assertion — a single
test (`TestWHEPArgsPayloadTypesMatchTheMediaEngine`) pins the pair. JSON tags
match across Go and TypeScript for `WebRTCSettings` (`enabled`, `maxPeers`,
`idleGraceSeconds`, `audioTrack`, `audioKbps`, `icePort`, `publicIp`,
`forceReencode`) and `WHEPStatus` (`process`, `peers`, `maxPeers`, `copying`).

**The one thing this plan does not resolve:** with failover enabled, the WHEP
monitor and the HLS preview both show the *ingest*, not the on-air source.
That is pre-existing behaviour inherited from the preview rather than something
this feature introduces, and changing it means moving both onto
`e.downstreamHub()` — a separate change with its own restart-signature
consequences. It is listed under *Not covered*, not hidden.
