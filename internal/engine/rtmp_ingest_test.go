package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/ffmpeg"
	"github.com/rainmanjam/polyemesis/internal/playout"
	"github.com/rainmanjam/polyemesis/internal/routing"

	"github.com/rainmanjam/polyemesis/internal/testenv"
)

// The one-port RTMP listener is addressed by the source's publish TOKEN, the
// same way SRT is. These pin the decision, because the alternative on the table
// -- ingest.rtmp.streamKey -- is the one that looks obvious and is wrong.

func rtmpSource(t *testing.T, store *db.DB, name string) *db.Source {
	t.Helper()
	ing := db.DefaultSettings().Ingest
	ing.Mode = db.IngestRTMP
	ing.RTMP.App = "live"
	s := &db.Source{Name: name, Enabled: true, Ingest: ing}
	if err := store.CreateSource(s); err != nil {
		t.Fatalf("CreateSource(%s): %v", name, err)
	}
	return s
}

// Two sources created from the defaults must resolve to two different
// programmes. This is the failure that made streamKey unusable as an address:
// DefaultSettings used to hand every source the identical key "stream", nothing
// made it unique, and ConstantTimeLookup's map would have resolved one of them
// arbitrarily -- one source silently answering for another, which is the exact
// thing the one-port work exists to remove.
func TestEachRTMPSourceIsAddressedByItsOwnToken(t *testing.T) {
	m, store := managerFixture(t)
	first := rtmpSource(t, store, "Horizontal")
	second := rtmpSource(t, store, "Vertical")

	a, ok := m.lookupStreamKey(first.Token)
	if !ok || a.SourceID != first.ID {
		t.Fatalf("lookup(first token) = %+v, %v; want source %d", a, ok, first.ID)
	}
	b, ok := m.lookupStreamKey(second.Token)
	if !ok || b.SourceID != second.ID {
		t.Fatalf("lookup(second token) = %+v, %v; want source %d", b, ok, second.ID)
	}
	if _, ok := m.lookupStreamKey("stream"); ok {
		t.Error("the old default stream key resolved; it addresses nothing and must not")
	}
	if _, ok := m.lookupStreamKey(""); ok {
		t.Error("an empty key resolved: a publisher who sends nothing must never be admitted")
	}
}

// Rotation with a grace window is the whole reason a token is a better address
// than a hand-typed key. rtmpserver.ConstantTimeLookup takes a prebuilt map
// rather than srtserver's closures, so the grace has to be expressed as one map
// entry per valid token -- and if that is dropped, rotating a token cuts a live
// encoder off mid-broadcast, which is the failure TokenGrace exists to prevent.
func TestARotatedRTMPTokenKeepsWorkingDuringTheGrace(t *testing.T) {
	m, store := managerFixture(t)
	src := rtmpSource(t, store, "Horizontal")
	old := src.Token

	fresh, err := store.RotateSourceToken(src.ID)
	if err != nil {
		t.Fatalf("RotateSourceToken: %v", err)
	}
	if got, ok := m.lookupStreamKey(fresh); !ok || got.SourceID != src.ID {
		t.Errorf("the new token does not resolve: %+v, %v", got, ok)
	}
	if got, ok := m.lookupStreamKey(old); !ok || got.SourceID != src.ID {
		t.Errorf("the rotated-out token stopped working inside its grace window: %+v, %v", got, ok)
	}
}

// Ready is rtmpserver's counterpart to srtserver's `Sink != nil`. Without it a
// publisher whose engine failed to start is admitted into a stream with no
// subscriber: the encoder goes green, the bytes go nowhere, and the operator
// has a healthy OBS and no output with nothing saying why.
//
// THIS TEST USED TO ASSERT THE WRONG CONTRACT. It was called
// "...NotReadyUntilItsEngineIs" and required Ready to become true as soon as
// the manager started, which is precisely the gap: an engine record plus a
// stored mode says a subscriber SHOULD exist, never that one does. Everything
// in between — a child that never spawned, one crash-looping, the early return
// for a source with no publish token — still admitted a publisher and held it
// on a stream nobody read.
//
// The fixture makes the distinction visible for free: its FFmpeg path cannot
// exec, so the manager starts, the engine exists, and no ingest child ever
// dials in. Under the old rule that was Ready. Under the real one it is not.
func TestAnRTMPTargetIsNotReadyUntilSomethingIsSubscribed(t *testing.T) {
	m, store := managerFixture(t)
	src := rtmpSource(t, store, "Horizontal")

	before, ok := m.lookupStreamKey(src.Token)
	if !ok {
		t.Fatal("the token must resolve even with no engine, or the log cannot tell the two failures apart")
	}
	if before.Ready {
		t.Error("a source with no engine reported Ready; its publisher would feed nobody")
	}

	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	after, ok := m.lookupStreamKey(src.Token)
	if !ok {
		t.Fatalf("the token stopped resolving once the manager was up: %+v", after)
	}
	if after.Ready {
		t.Error("Ready with no subscriber attached. The engine exists and the mode is " +
			"rtmp, but this fixture's ffmpeg cannot exec so nothing ever dialled the " +
			"listener — admitting a publisher here is the green-encoder-no-output failure")
	}
}

// The standby is reached on the SAME listener at "<token>.backup", exactly as
// it is over SRT. Registering it unconditionally would accept a backup encoder
// into a stream nothing subscribes to, so it exists only when failover is
// actually configured to use RTMP.
func TestTheRTMPStandbyIsAddressedByTheTokenSuffix(t *testing.T) {
	m, store := managerFixture(t)
	src := rtmpSource(t, store, "Horizontal")

	st, err := store.GetSettings()
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	st.Listeners.SRTPort = freeUDPPort(t)
	st.Listeners.RTMPPort = freeTCPPort(t)
	if err := store.PutSettings(st); err != nil {
		t.Fatalf("PutSettings: %v", err)
	}
	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if _, ok := m.lookupStreamKey(src.Token + backupTokenSuffix); ok {
		t.Error("the standby address resolved with failover off; its publisher would feed nobody")
	}

	st.Failover.Enabled = true
	st.Failover.Backup.Enabled = true
	st.Failover.Backup.Mode = db.IngestRTMP
	st.Failover.Backup.RTMP.App = "live"
	if err := store.PutSettings(st); err != nil {
		t.Fatalf("PutSettings: %v", err)
	}
	if err := m.Reconcile(); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	got, ok := m.lookupStreamKey(src.Token + backupTokenSuffix)
	if !ok {
		t.Fatal("the standby is unreachable: an RTMP failover encoder has nowhere to publish")
	}
	// The standby answers to the SAME contract as the primary, and used to be a
	// hardcoded `true` while the comment above it described the opposite. The
	// fixture's ffmpeg cannot exec, so no backup child ever dials the listener --
	// which is precisely the crash-looping-backup case that must not be Ready.
	if got.Ready {
		t.Error("the standby reported Ready with nothing subscribed to it. " +
			"Failover being CONFIGURED for RTMP says a backup subscriber should " +
			"exist, never that one does; admitting a publisher here feeds nobody")
	}
	if !got.Backup || got.SourceID != src.ID {
		t.Errorf("standby target = %+v, want the backup slot of source %d", got, src.ID)
	}
	// Separate slots, or the standby and the primary evict each other -- the
	// failover feature failing in the one situation it was built for.
	primary, _ := m.lookupStreamKey(src.Token)
	if got.Key() == primary.Key() {
		t.Error("the standby shares the primary's publisher slot")
	}
}

// An RTMP source with no token has no address, and must not get a child.
//
// `rtmp://127.0.0.1:PORT/live/` resolves: rtmpserver.StreamKey falls back to
// the whole path when there is no second segment, so the subscriber would
// attach to the stream key "live" and receive whatever any publisher sent
// there. Reachable through effectiveSettings' fail-open path, which is the
// worst moment to quietly cross two programmes.
func TestAnRTMPSourceWithNoTokenSpawnsNoIngest(t *testing.T) {
	m, store := managerFixture(t)
	src := rtmpSource(t, store, "Horizontal")
	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	eng := m.Engine(src.ID)
	if eng == nil {
		t.Fatal("no engine for the source")
	}

	eng.mu.Lock()
	eng.sourceToken = ""
	eng.mu.Unlock()
	eng.reconcileIngest(eng.Settings(), eng.Settings())

	eng.mu.RLock()
	proc := eng.ingest
	eng.mu.RUnlock()
	if proc != nil {
		t.Error("an ingest child was spawned with no address; it would join the app path and read a stranger's stream")
	}
}

/* ------------------------------------------------------- the upgrade path */

// legacyRTMPKeys keeps an install that upgrades with a live RTMP encoder on the
// air. RTMP carries no typed rejection reason, so moving the address to the
// token without this takes a working broadcast down and shows the streamer
// nothing but "could not connect".
func TestALegacyStreamKeyStillReachesItsSource(t *testing.T) {
	ing := db.DefaultSettings().Ingest
	ing.Mode = db.IngestRTMP
	ing.RTMP.App = "live"
	ing.RTMP.StreamKey = "stream" // what an older build wrote
	rows := []*db.Source{{ID: 1, Name: "Main", Enabled: true, Ingest: ing, Token: "tok-main"}}

	if got := legacyRTMPKeys(rows)[1]; got != "stream" {
		t.Errorf("legacy key = %q, want %q: the operator's encoder goes off air without it", got, "stream")
	}
}

// Two sources claiming one key means NEITHER gets it. Resolving it arbitrarily
// is one programme answering for another, which is worse than the refusal --
// and it is reachable by hand, because two operator-typed keys can match.
func TestALegacyStreamKeyClaimedTwiceReachesNobody(t *testing.T) {
	ing := db.DefaultSettings().Ingest
	ing.Mode = db.IngestRTMP
	ing.RTMP.App = "live"
	ing.RTMP.StreamKey = "stream"
	rows := []*db.Source{
		{ID: 1, Name: "Main", Enabled: true, Ingest: ing, Token: "tok-one"},
		{ID: 2, Name: "Vertical", Enabled: true, Ingest: ing, Token: "tok-two"},
	}

	got := legacyRTMPKeys(rows)
	if len(got) != 0 {
		t.Errorf("legacy keys = %v, want none: a contested key must address nothing", got)
	}
}

// A legacy key that matches any token, live or lapsed, primary or standby, is
// refused. Otherwise it could shadow the address the Sources page is telling
// someone to use, and the two would disagree forever.
func TestALegacyStreamKeyNeverShadowsAToken(t *testing.T) {
	ing := db.DefaultSettings().Ingest
	ing.Mode = db.IngestRTMP
	ing.RTMP.App = "live"
	ing.RTMP.StreamKey = "tok-other"
	rows := []*db.Source{
		{ID: 1, Name: "Main", Enabled: true, Ingest: ing, Token: "tok-main"},
		{ID: 2, Name: "Vertical", Enabled: true, Ingest: db.DefaultSettings().Ingest, Token: "tok-other"},
	}

	if got, ok := legacyRTMPKeys(rows)[1]; ok {
		t.Errorf("legacy key %q was honoured while it is source 2's token", got)
	}
}

// A source that is not on RTMP has no legacy address, whatever its settings
// blob still carries. The field round-trips for compatibility; it must not
// become a second way in.
func TestALegacyStreamKeyOnANonRTMPSourceIsIgnored(t *testing.T) {
	ing := db.DefaultSettings().Ingest
	ing.Mode = db.IngestSRT
	ing.RTMP.StreamKey = "stream"
	rows := []*db.Source{{ID: 1, Name: "Main", Enabled: true, Ingest: ing, Token: "tok-main"}}

	if got, ok := legacyRTMPKeys(rows)[1]; ok {
		t.Errorf("legacy key %q was honoured on an SRT source", got)
	}
}

// A grandfathered key resolves to the same programme, in the same state, as the
// token does. Two addresses that disagreed about Enabled or Ready would be two
// different answers to "is this source receiving".
func TestALegacyKeyAndTheTokenResolveIdentically(t *testing.T) {
	m, store := managerFixture(t)
	src := rtmpSource(t, store, "Main")
	src.Ingest.RTMP.StreamKey = "stream"
	if err := store.UpdateSource(src); err != nil {
		t.Fatalf("UpdateSource: %v", err)
	}

	byToken, okToken := m.lookupStreamKey(src.Token)
	byLegacy, okLegacy := m.lookupStreamKey("stream")
	if !okToken || !okLegacy {
		t.Fatalf("token resolved=%v, legacy key resolved=%v; both must", okToken, okLegacy)
	}
	if byToken != byLegacy {
		t.Errorf("token gives %+v, legacy key gives %+v; they must be the same target", byToken, byLegacy)
	}
	if got := m.LegacyRTMPKey(src.ID); got != "stream" {
		t.Errorf("LegacyRTMPKey = %q, want %q so the UI can flag the grandfathered address", got, "stream")
	}
}

// A source created today carries no stream key, so it can never claim a legacy
// address. That is what stops the grandfather clause from colliding with a
// source added after the upgrade -- the trap that would otherwise take the
// upgraded encoder off air the moment a second RTMP source appeared.
func TestANewSourceHasNoLegacyAddress(t *testing.T) {
	m, store := managerFixture(t)
	src := rtmpSource(t, store, "Vertical")

	if got := src.Ingest.RTMP.StreamKey; got != "" {
		t.Fatalf("a new source was created with stream key %q, want none", got)
	}
	if got := m.LegacyRTMPKey(src.ID); got != "" {
		t.Errorf("LegacyRTMPKey = %q on a source created today, want none", got)
	}
}

// A routing graph is never compiled against a layout nobody has measured.
//
// Until the probe lands, e.source is routing.DefaultSource() — six stereo
// tracks that exist so the routing editor has something to draw, not a claim
// about what is arriving. reconcileMeters and stemPlanFor have always refused
// on that; destinations read e.source raw and did not, which is the one that
// matters most because per-destination routing is the product.
//
// Two failures came out of it. A profile naming a track the stream lacks emits
// `[0:a:5]`, FFmpeg refuses, and the destination crash-loops — loud, and
// findable. The quieter one is worse: the placeholder claims Channels: 2 on
// every track, so a real 5.1 track compiles to `pan=stereo|c0=c0|c1=c1`, which
// is perfectly valid FFmpeg. The destination starts, stays up, and publishes
// front L/R only — centre, where dialogue lives, silently discarded.
func TestDestinationsAreNotPlannedAgainstAnUnprobedLayout(t *testing.T) {
	// The hazard is a property of the placeholder itself: it has tracks, so a
	// zero-track check cannot catch it, and it claims two channels on all of
	// them, so a downmix built from it is wrong for anything wider.
	ph := routing.DefaultSource()
	if len(ph.Tracks) == 0 {
		t.Fatal("the placeholder has no tracks; the guard under test would be unnecessary")
	}
	for _, tr := range ph.Tracks {
		if tr.Channels != 2 {
			t.Fatalf("placeholder track %d claims %d channels; this test's premise "+
				"is that every placeholder track claims stereo", tr.Index, tr.Channels)
		}
	}

	// And the guard itself: reconcileOutputs must hold rather than plan when the
	// layout is unmeasured and no silence tier is standing in with a real one.
	src := readFile(t, "engine.go")
	guard := "holdDests := !measured && silenceSig == \"\""
	if !strings.Contains(src, guard) {
		t.Error("reconcileOutputs no longer holds destination planning on an unmeasured " +
			"layout; a routing graph compiled from the placeholder can publish the " +
			"wrong channels without erroring")
	}
	// It must read the flag, not infer it from the track count.
	if !strings.Contains(src, "measured := e.measured") {
		t.Error("reconcileOutputs no longer reads e.measured. Inferring 'unmeasured' " +
			"from len(tracks)==0 is the mistake this guard exists to avoid: the " +
			"placeholder HAS six tracks")
	}
	// measured goes false ONLY where the placeholder goes back into e.source.
	//
	// This used to be a text count over engine.go, which was a proxy for the
	// invariant rather than the invariant itself -- and it broke the moment a
	// second legitimate invalidation site appeared. It is now structural:
	// sourceState.invalidate is the ONLY thing in the package that does either
	// half, and it does both, so the pairing cannot be broken by adding a site.
	//
	// It still forbids the mistake the flag was split out to fix: probeLoop's
	// idle branch calls clearProbed, which drops `probed` WITHOUT restoring the
	// placeholder, because an encoder that stopped a moment ago still has a real
	// layout. Clearing `measured` there too would strand any destination added
	// during a failover until the primary returned.
	stateSrc := readEngineFile(t, "sourcestate.go")
	inv := funcBody(t, stateSrc, "func (s *sourceState) invalidate() {")
	for _, half := range []string{"s.measured = false", "s.source = routing.DefaultSource()"} {
		if !strings.Contains(inv, half) {
			t.Errorf("sourceState.invalidate no longer does %q. Clearing measured "+
				"without restoring the placeholder holds destinations over a layout "+
				"that is still real; restoring it without clearing measured plans "+
				"against the placeholder", half)
		}
	}

	// And nowhere else may do either half on its own.
	for _, name := range engineGoFiles(t) {
		body := readEngineFile(t, name)
		if name == "sourcestate.go" {
			continue
		}
		for _, half := range []string{"measured = false", "source = routing.DefaultSource()"} {
			if strings.Contains(body, half) {
				t.Errorf("%s assigns %q directly. Both halves belong to "+
					"sourceState.invalidate, which is what keeps them paired", name, half)
			}
		}
	}
}

// readEngineFile reads one of this package's source files.
func readEngineFile(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(b)
}

// engineGoFiles lists the package's non-test source files.
func engineGoFiles(t *testing.T) []string {
	t.Helper()
	entries, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	var out []string
	for _, e := range entries {
		if !strings.HasSuffix(e, "_test.go") {
			out = append(out, e)
		}
	}
	return out
}

// funcBody returns the source of the function whose signature line is given.
func funcBody(t *testing.T, src, signature string) string {
	t.Helper()
	at := strings.Index(src, signature)
	if at < 0 {
		t.Fatalf("cannot find %q", signature)
	}
	rest := src[at:]
	if end := strings.Index(rest, "\n}\n"); end > 0 {
		return rest[:end]
	}
	return rest
}

// A stereo graph that happens to match the placeholder must not survive.
//
// stopDestinations KEEPS a destination whose running spec equals its planned
// one. While the hold was planning against the placeholder, a destination
// compiled from a real stereo layout produced the identical spec -- the
// placeholder is six stereo tracks -- so it was kept, still running, over an
// ingest nobody had measured. If the new stream was 5.1 it went on publishing
// front L/R and discarding centre, which is the precise failure the hold exists
// to prevent, reached through the hold itself.
//
// The fix is that a held pass plans NOTHING: an unmeasured layout means no
// destination's graph can be vouched for, so none may keep running.
func TestAPlaceholderShapedDestinationDoesNotSurviveAnUnmeasuredWindow(t *testing.T) {
	e := failoverEngine(t)
	e.settings = failoverOnSettings()
	e.play = playout.New(playout.Deps{Dir: t.TempDir()})

	dest, err := e.store.CreateDestination(&db.Destination{
		Name: "stereo", Kind: db.DestRTMP, Platform: db.PlatformCustom,
		URL: "rtmp://127.0.0.1:1/rtmp", StreamKey: "key", Enabled: true,
		AudioBitrate: 128, Profile: routing.DefaultProfile(),
	})
	if err != nil {
		t.Fatalf("CreateDestination: %v", err)
	}
	if dest.SourceID == nil {
		t.Fatal("the created destination has no source")
	}
	e.sourceID = *dest.SourceID

	// Measured first, so the running spec is the one a real stereo layout
	// produces -- the spec the placeholder would also produce.
	e.mu.Lock()
	e.measured, e.probed = true, true
	e.source = routing.Source{Tracks: []routing.Track{{Index: 0, Channels: 2}}}
	e.mu.Unlock()
	if err := e.reconcileOutputs(); err != nil {
		t.Fatalf("reconcileOutputs (measured): %v", err)
	}
	e.mu.RLock()
	started := len(e.dests)
	e.mu.RUnlock()
	if started == 0 {
		// Was a bare t.Skip, and it is the SAME SHAPE as #150's /processes
		// finding: a fixture that started nothing, an assertion with nothing
		// to look at, and a green run. Quarantined so it is counted rather
		// than invisible; the fix is to assert the fixture built something.
		testenv.Quarantine(t, "engine-rtmp-ingest-fixture-started-no-destination")
	}

	// Now the ingest restarts: placeholder back, nothing measured.
	e.mu.Lock()
	e.measured, e.probed = false, false
	e.source = routing.DefaultSource()
	e.mu.Unlock()
	if err := e.reconcileOutputs(); err != nil {
		t.Fatalf("reconcileOutputs (unmeasured): %v", err)
	}

	e.mu.RLock()
	left := len(e.dests)
	e.mu.RUnlock()
	if left != 0 {
		t.Errorf("%d destination(s) survived an unmeasured window. Its spec matched "+
			"a plan compiled from the PLACEHOLDER, which is stereo -- so a graph "+
			"built for a real stereo stream looks identical and is kept. The next "+
			"stream being 5.1 would then publish front L/R with centre discarded, "+
			"no error anywhere", left)
	}
}

// A layout belongs to the transport that delivered it.
//
// reconcileIngest returns early for SRT, for IngestUnset, and for an RTMP source
// with no token -- all three before the reset that starting an ingest performs.
// So switching a probed RTMP source to SRT left the RTMP stream's track list in
// e.source, still flagged measured, and destinations were planned against the
// previous transport's layout until the new probe landed.
//
// The tempting fix -- clear it in those early returns -- is worse than the bug:
// the SRT branch runs on EVERY reconcile, so it would hold destinations forever
// on the most common ingest there is. The invalidation has to be conditional on
// the mode actually changing, which is what measuredMode records.
func TestChangingTheIngestModeInvalidatesTheMeasuredLayout(t *testing.T) {
	for _, to := range []db.IngestMode{db.IngestSRT, db.IngestUnset} {
		t.Run(string(to), func(t *testing.T) {
			e := failoverEngine(t)
			real := routing.Source{Tracks: []routing.Track{
				{Index: 0, Channels: 6}, {Index: 1, Channels: 2},
			}}
			s := db.DefaultSettings()
			s.Ingest.Mode = db.IngestRTMP
			e.settings = s

			e.mu.Lock()
			e.measured, e.probed = true, true
			e.measuredMode = db.IngestRTMP
			e.source = real
			e.mu.Unlock()

			next := db.DefaultSettings()
			next.Ingest.Mode = to
			e.settings = next
			e.reconcileIngest(next, s)

			e.mu.RLock()
			measured, src := e.measured, e.source
			e.mu.RUnlock()
			if measured {
				t.Errorf("still measured after switching from rtmp to %s. The layout "+
					"belongs to the transport that delivered it; planning a routing "+
					"graph against the old one maps tracks the new stream may not have", to)
			}
			if len(src.Tracks) == len(real.Tracks) && src.Tracks[0].Channels == 6 {
				t.Error("e.source still holds the previous transport's layout")
			}
		})
	}
}

// The silence tier's layout is a measured one, so it lifts the hold.
//
// `holdDests := !measured && silenceSig == ""` has two ways to be false and the
// other tests only cover one of them. This is the second: nothing has ever been
// probed, but a silence tier is standing in, and reconcileOutputs has already
// substituted synthTrack() for e.source above the guard. That IS a real layout —
// synthesised rather than observed, but exact and known — so there is nothing
// for the placeholder to mislead and destinations must start normally.
//
// Without this, deleting `&& silenceSig == ""` from the guard would pass every
// other test in this file: a video-only ingest would simply stop publishing
// audio, silently, which is the failure mode the silence tier exists to prevent.
func TestASilenceTierLiftsTheHoldOnAnUnmeasuredLayout(t *testing.T) {
	e := failoverEngine(t)
	s := failoverOnSettings()
	// Slate off, so the selector is not the thing standing in; the silence tier
	// is what this test is about.
	s.Failover.Enabled = false
	s.Failover.Slate.Enabled = false
	e.settings = s
	e.play = playout.New(playout.Deps{Dir: t.TempDir()})

	dest, err := e.store.CreateDestination(&db.Destination{
		Name: "silent-src", Kind: db.DestRTMP, Platform: db.PlatformCustom,
		URL: "rtmp://127.0.0.1:1/rtmp", StreamKey: "key", Enabled: true,
		AudioBitrate: 128, Profile: routing.DefaultProfile(),
	})
	if err != nil {
		t.Fatalf("CreateDestination: %v", err)
	}
	if dest.SourceID == nil {
		t.Fatal("the created destination has no source; nothing would list it")
	}
	e.sourceID = *dest.SourceID

	// Unmeasured, which on its own would hold. A video-only probe is what puts
	// the silence tier on: no audio arriving, so one is synthesised.
	e.mu.Lock()
	e.measured = false
	e.probed = true
	e.source = routing.Source{}
	e.videoInfo = &ffmpeg.VideoStream{Width: 1280, Height: 720}
	e.mu.Unlock()

	if err := e.reconcileOutputs(); err != nil {
		t.Fatalf("reconcileOutputs: %v", err)
	}

	if e.wantSilence(e.settings) == "" {
		// Was a bare t.Skip. It fires when the behaviour under test stopped
		// happening, which is the finding rather than the exemption.
		testenv.Quarantine(t, "engine-rtmp-ingest-no-silence-tier")
	}
	if len(e.dests) == 0 {
		t.Error("the hold was applied even though a silence tier was standing in. " +
			"reconcileOutputs substitutes synthTrack() for e.source when silenceSig " +
			"is set, and that is a measured layout — holding against it publishes " +
			"nothing while a perfectly good synthesised bed is on air")
	}
}

// Holding the START does not license skipping the STOP.
//
// The third bug in this guard, and the one no assertion caught. Everything below
// the guard replaces hubs -- renditions, the silence tier, the selector -- and
// stopDestinations is what takes a destination down BEFORE the hub it reads is
// closed under it. Skip it and the destination stays in e.dests subscribed to a
// hub that no longer delivers; closing a hub stops UDP, it does not end the
// process. FFmpeg then sits there "started", never restarting because nothing
// asked it to, receiving nothing.
//
// It cost a file destination its whole 76-second run: zero bytes, no error, and
// a suite that stayed green because the only line that would have noticed was a
// note rather than a check. It reproduced roughly one run in two, which is
// exactly the frequency at which a bug gets called a flake and lives forever.
//
// Nothing about the placeholder argument justifies suspending the teardown
// ordering: planning against an unmeasured layout is harmless, because the specs
// it produces are only ever read by startDestinations.
func TestAHeldPassStillStopsDestinations(t *testing.T) {
	e := failoverEngine(t)
	e.settings = failoverOnSettings()
	e.play = playout.New(playout.Deps{Dir: t.TempDir()})

	// The premise: unmeasured, so the guard is in force for this pass.
	if e.measured {
		t.Fatal("fixture is already measured; this test needs the held path")
	}

	// A destination the engine believes is running, carrying a spec no plan can
	// match -- which is what a destination looks like the moment its upstream
	// changes. stopDestinations must take it down; a held pass that skips the
	// call leaves it sitting on a hub about to be closed underneath it.
	e.mu.Lock()
	e.dests[7] = &destination{
		row:  &db.Destination{ID: 7, Name: "stale"},
		spec: "a-spec-no-plan-will-ever-match",
	}
	e.mu.Unlock()

	if err := e.reconcileOutputs(); err != nil {
		t.Fatalf("reconcileOutputs: %v", err)
	}

	e.mu.RLock()
	_, still := e.dests[7]
	e.mu.RUnlock()
	if still {
		t.Error("a destination with a stale spec survived a held reconcile. " +
			"The hold is on starting destinations against an unmeasured layout, " +
			"not on tearing them down: the tiers below still replace their hubs, " +
			"and a destination left subscribed to a closed hub receives nothing " +
			"for the rest of its life without ever erroring")
	}
}

// An encoder that stopped a moment ago is not an encoder nobody has measured.
//
// probeLoop clears e.probed after three idle rounds, and that is right for what
// probed means: nothing is arriving, so the UI must stop claiming tracks. But it
// leaves e.source holding the last real layout, on purpose. Guarding
// destinations on !probed therefore refused to plan against a layout that had
// been measured perfectly well seconds earlier — so a destination added during
// a failover, while the slate was on air, could not start until the primary came
// back.
//
// The failover suite caught it as `no mismatch destination process was
// reported; nothing was measured`: the destination did start, one millisecond
// after the probe landed, roughly a minute after the step that added it had
// already given up looking.
//
// The split is the fix: probed is "arriving now", measured is "ever measured for
// this ingest", and only ingest start puts the placeholder back.
func TestAnIdleButAlreadyMeasuredLayoutStillPlansDestinations(t *testing.T) {
	e := failoverEngine(t)
	e.settings = failoverOnSettings()
	e.play = playout.New(playout.Deps{Dir: t.TempDir()})

	dest, err := e.store.CreateDestination(&db.Destination{
		Name: "mismatch", Kind: db.DestRTMP, Platform: db.PlatformCustom,
		URL: "rtmp://127.0.0.1:1/rtmp", StreamKey: "key", Enabled: true,
		AudioBitrate: 128, Profile: routing.DefaultProfile(),
	})
	if err != nil {
		t.Fatalf("CreateDestination: %v", err)
	}
	// CreateDestination attaches the row to the default source rather than to
	// zero, and failoverEngine's sourceID is zero, so without this the engine
	// lists no destinations at all and the assertion below passes or fails for a
	// reason that has nothing to do with the guard.
	if dest.SourceID == nil {
		t.Fatal("the created destination has no source; nothing would list it")
	}
	e.sourceID = *dest.SourceID

	// The state probeLoop leaves behind when a live encoder goes away: no layout
	// arriving, but the one it sent is still on file and still true.
	e.mu.Lock()
	e.probed = false
	e.measured = true
	e.source = routing.Source{Tracks: []routing.Track{{Index: 0, Channels: 2}}}
	e.mu.Unlock()

	if err := e.reconcileOutputs(); err != nil {
		t.Fatalf("reconcileOutputs: %v", err)
	}

	if len(e.dests) == 0 {
		t.Error("no destination was planned against a layout that HAS been measured. " +
			"probed=false only means nothing is arriving right now; e.source still " +
			"holds the real layout, and a destination added during a failover has to " +
			"be able to start and carry the slate")
	}
}

// The hold is destinations-only, and this is the test that says so.
//
// The first version of the guard above returned from reconcileOutputs outright.
// That reads as a small difference and is not: everything below the guard --
// the silence tier, the SELECTOR, the renditions, playout -- stopped being
// reconciled too, for as long as the layout was unmeasured.
//
// Which is not a rare window. probeLoop clears e.probed after three idle rounds
// whenever the ingest stops delivering, so killing the primary encoder puts the
// engine in exactly this state -- and the selector is the tier whose whole job
// is to carry the slate through that moment. It never ran. The reconcile
// arrived late, once the primary returned, and landed a backwards decode
// timestamp in the destination's output: the discontinuity a receiving platform
// drops the connection on. CI found it as `1 backwards DTS step(s)` in the
// failover suite, on the commit that added the guard and no other.
//
// Asserted against selectorHub() rather than the source text because the shape
// of the fix is not the point -- a later refactor may hold destinations some
// other way, and this must still hold.
func TestHoldingAnUnprobedLayoutDoesNotHoldTheSelector(t *testing.T) {
	e := failoverEngine(t)
	e.settings = failoverOnSettings()
	// reconcileOutputs runs the playout tier on its way past, and failoverEngine
	// leaves e.play nil because nothing else in that file reaches this far down
	// the function. A nil *Manager dereferences inside Reconcile.
	e.play = playout.New(playout.Deps{Dir: t.TempDir()})

	// The precondition the whole test rests on. Left unstated, a fixture that
	// happened to arrive probed would make this pass without exercising
	// anything.
	if e.probed {
		t.Fatal("fixture starts probed; this test is about the unprobed window")
	}
	if wantSelector(e.settings) == "" {
		t.Fatal("settings do not ask for a selector; there would be nothing to hold")
	}

	if err := e.reconcileOutputs(); err != nil {
		t.Fatalf("reconcileOutputs: %v", err)
	}

	if e.selectorHub() == nil {
		t.Error("the selector was not reconciled while the layout was unprobed. " +
			"Destinations are the only tier that compiles a routing graph against " +
			"e.source, so they are the only tier the placeholder can mislead -- " +
			"holding the rest takes the slate off air during exactly the outage " +
			"the selector exists to cover")
	}
	// The other half: the hold must still be in force, or this test would pass
	// on a build that simply deleted the guard.
	if len(e.dests) != 0 {
		t.Errorf("%d destination(s) planned against an unprobed layout; the hold "+
			"is not in force and this test is no longer proving anything", len(e.dests))
	}
}

// readFile reads a file from this package's directory.
func readFile(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("cannot read %s: %v", name, err)
	}
	return string(b)
}
