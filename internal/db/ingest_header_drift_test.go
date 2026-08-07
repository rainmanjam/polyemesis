package db

import (
	"strings"
	"testing"
)

// The header's ingest indicator must not decide health from the ingest PROCESS.
//
// SRT has no ingest child. engine.reconcileIngest returns early for it, on
// purpose and with a long comment saying why: srtserver delivers datagrams
// straight into the hub, and spawning FFmpeg as well would put two things on
// one socket, where the Go listener binds first and the FFmpeg one crash-loops
// forever behind a listener that was working fine.
//
// The consequence reached the UI. AppLayout rendered
//
//	ingest?.state === "running" ? kbps(...) : stateLabel(ingest?.state)
//
// and for SRT `ingest` is null, so `stateLabel(undefined)` produced "Offline" —
// permanently, on every healthy SRT install, in the most prominent status in the
// application chrome.
//
// It was found in a screenshot rather than by a test: docs/media showed "Ingest
// Offline" beside three live destinations and three metering tracks, while the
// capture script had already confirmed through the API that bytes were arriving
// and the source was probed. It was also twice nearly dismissed — first as an
// artefact of the capture harness injecting into the relay instead of
// publishing, then as a stale container image. Both were real problems and both
// were fixed, and the header was still wrong underneath them.
//
// useIngestLive is the app's single definition of "a broadcast is going out",
// and its own doc comment already said the process state is not it. What this
// guards is that the chrome asks that one question rather than growing a second
// answer beside it.
//
// A source guard rather than a behavioural one because the harness in
// internal/api builds no Manager and drives no relay, so nothing there can make
// the bitrate series non-zero.
func TestTheHeaderAsksTheAppsOneQuestionAboutBeingLive(t *testing.T) {
	src := readUI(t, "components", "AppLayout.tsx")

	if !strings.Contains(src, "useIngestLive()") {
		t.Error("AppLayout no longer calls useIngestLive. An SRT source has no ingest " +
			"child by design, so a header reading the process state reports every " +
			"healthy SRT install as Offline.")
	}
	// The tone is what makes the dot green, and it is the part that was
	// inverted. Deriving it from the process alone is the original bug.
	if !strings.Contains(src, "ingestLive ? \"live\" : toneForState(ingest?.state)") {
		t.Error("the ingest status dot is no longer driven by useIngestLive. That dot " +
			"is the first thing an operator looks at to answer 'am I on air'.")
	}
}

// useIngestLive must keep deriving from the RELAY rather than from the ingest
// process, or moving the header onto it fixes nothing.
//
// The bitrate series it reads is sampled from the relay's received-byte counter
// in internal/stats, which is transport-independent: SRT datagrams land in the
// hub whether or not any FFmpeg child exists. A future edit that repoints this
// at status.ingest.progress would silently restore the original bug in a place
// nobody would think to look.
func TestIngestLiveIsDerivedFromArrivingBytes(t *testing.T) {
	src := readUI(t, "hooks", "useLiveData.ts")

	block := src[strings.Index(src, "export function useIngestLive"):]
	if end := strings.Index(block, "\n}"); end > 0 {
		block = block[:end]
	}
	if !strings.Contains(block, "source?.probed") || !strings.Contains(block, "bitrate") {
		t.Error("useIngestLive no longer derives from probed + arriving bitrate. " +
			"Those come from the relay and are the only signals true for a transport " +
			"that has no ingest child.")
	}
	if strings.Contains(block, "status?.ingest") {
		t.Error("useIngestLive now consults the ingest process, which is null for SRT " +
			"by design — this is the exact inversion it exists to avoid.")
	}
}
