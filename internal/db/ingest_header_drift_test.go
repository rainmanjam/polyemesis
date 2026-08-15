package db

import (
	"strings"
	"testing"
)

// WHY THIS IS IN internal/db, stated rather than left to be discovered.
//
// Its two siblings are here because the contract is: limits_drift_test.go and
// facebook_ui_drift_test.go guard UI copies of db.Rendition, db.Destination and
// db.FacebookSettings. This file guards no db type at all. What it watches is
// owned by internal/engine (reconcileIngest, which returns early for SRT) and
// internal/stats (the received-byte counter the bitrate series is sampled from).
//
// It lives here to reuse readUI and stripJSComments from facebook_ui_drift_test.go,
// which is helper locality rather than a reason, and the honest consequence is
// that someone changing engine's ingest reconciliation will not see a db test in
// their package. `go test ./...` still runs it, so the guard holds; only its
// discoverability is worse. Moving it to internal/engine means copying both
// helpers there, so the fix is to promote them to a shared test helper first.
//
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
// Comments are stripped for the reason facebook_ui_drift_test.go states at
// length and this guard originally did not honour: measured on this tree,
// deleting both lines below and leaving their text in a `// was: ...` comment
// kept this test green. The honest way to keep a substring guard green while
// removing the thing it watches is to leave the words behind, so the words are
// not what is read.
func TestTheHeaderAsksTheAppsOneQuestionAboutBeingLive(t *testing.T) {
	src := stripJSComments(readUI(t, "components", "AppLayout.tsx"))

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
	src := stripJSComments(readUI(t, "hooks", "useLiveData.ts"))

	// Not `src[strings.Index(...):]` unguarded: a rename made that a slice-bounds
	// panic rather than a failure anyone could read, which is the guard going
	// quiet in the loudest possible way for the least informative reason.
	start := strings.Index(src, "export function useIngestLive")
	if start < 0 {
		t.Fatalf("useLiveData.ts no longer exports useIngestLive. It is the app's single " +
			"definition of 'a broadcast is going out'; if it moved, point this guard at " +
			"wherever that definition now lives rather than deleting the guard.")
	}
	block := src[start:]
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
