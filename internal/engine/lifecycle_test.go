package engine

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/config"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/ffmpeg"
	"github.com/rainmanjam/polyemesis/internal/meters"
	"github.com/rainmanjam/polyemesis/internal/relay"
	"github.com/rainmanjam/polyemesis/internal/routing"
	"github.com/rainmanjam/polyemesis/internal/supervisor"
	"github.com/rainmanjam/polyemesis/internal/testenv"
)

// The child lifecycle: startRendition, teardownRendition, stopAux, the preview
// pair, and the loudness pair.
//
// These are the functions that hand a child a port, a subscription and a hub,
// and the ones that are supposed to take all three back. Every failure they can
// have is a resource that outlives its owner -- a port the allocator believes
// is free while an encoder still writes to it, a subscription that forwards a
// programme to whoever was handed that number next, a *rendition published into
// e.rends after Stop already walked the map. None of it is visible in a status
// payload and none of it was covered: all ten were at 0.0% when this file was
// written.
//
// So the assertions here are about what is HELD afterwards, not about what was
// logged. The allocator is deliberately narrow -- one port, or none at all --
// because a leak in a 500-port range is invisible and a leak in a one-port
// range is the next Allocate() failing.

// ---------------------------------------------------------------- fixtures

// lifeEngine is an Engine in the shape a reconcile leaves one in: a hub, an
// allocator, tools, and the maps the lifecycle writes into.
func lifeEngine(t *testing.T) *Engine {
	t.Helper()
	e := failoverEngine(t)
	e.cfg = config.Config{DataDir: t.TempDir()}
	e.loud = map[int64]*loudnessMon{}
	e.loudStore = meters.NewStore()
	return e
}

// oneSlotAllocator holds exactly one port -- freeUDPPort (manager_test.go) asks
// the kernel for it, which is the same question relay.PortAllocator asks in
// portFree. A second Allocate() succeeds only if the first was given back,
// which is the whole leak assertion in this file: a leak in the production
// 500-port range is invisible, and a leak in a one-port range is the next
// Allocate() failing.
func oneSlotAllocator(t *testing.T) *relay.PortAllocator {
	t.Helper()
	return relay.NewPortAllocator(freeUDPPort(t), 1)
}

// emptyAllocator has no ports at all, which is how a machine with its relay
// range exhausted looks to a start path.
func emptyAllocator(t *testing.T) *relay.PortAllocator {
	t.Helper()
	return relay.NewPortAllocator(freeUDPPort(t), 0)
}

func lifeHub(t *testing.T) *relay.Hub {
	t.Helper()
	h, err := relay.New(slog.New(slog.NewTextHandler(io.Discard, nil)), 0)
	if err != nil {
		t.Fatalf("relay.New: %v", err)
	}
	t.Cleanup(func() { _ = h.Close() })
	return h
}

func hasSubscriber(h *relay.Hub, name string) bool {
	return slices.Contains(h.Subscribers(), name)
}

// mustAllocate fails the test rather than returning, because every caller is
// asserting that a port came BACK and "it did not" is the defect.
// enginePort takes a port THROUGH THE ENGINE, the way production does.
//
// #707/#708. The engine now keeps a ledger of the ports it holds so StopWithin
// can assert it gave them all back, and releasePort refuses to hand back one
// this engine does not hold -- which is what stops a double release from
// returning a port another engine has since been given.
//
// A test that allocates straight off e.alloc therefore sets up a state
// production cannot reach: a port in the pool but not in the ledger. Teardown
// then correctly refuses to release it, and the test fails for a reason that is
// about the fixture rather than the path under test. Going through the engine
// keeps the fixture honest and exercises the real door.
func enginePort(t *testing.T, e *Engine, why string) int {
	t.Helper()
	p, err := e.allocPort()
	if err != nil {
		t.Fatalf("%s: the allocator has no free port left (%v), so the one taken "+
			"by the path under test was never released", why, err)
	}
	return p
}

func mustAllocate(t *testing.T, a *relay.PortAllocator, why string) int {
	t.Helper()
	p, err := a.Allocate()
	if err != nil {
		t.Fatalf("%s: the allocator has no free port left (%v), so the one taken "+
			"by the path under test was never released", why, err)
	}
	return p
}

func lifeRendition(id int64, enc db.VideoEncoder) *db.Rendition {
	return &db.Rendition{
		ID: id, Name: "tier", Width: 1280, Height: 720, FPS: 30,
		VideoBitrate: 3000, Encoder: enc, Preset: "veryfast", GOPSeconds: 2,
	}
}

// --------------------------------------------------------------- renditions

// The encoder check has to happen before anything is acquired. A rendition
// refused after the allocation is a refusal that costs a port every reconcile,
// and reconcile retries a failed rendition for ever.
func TestARenditionWhoseEncoderThisMachineCannotRunIsRefusedWithoutTakingAPort(t *testing.T) {
	e := lifeEngine(t)
	e.tools = slateTools(t, false) // nvenc registered by the build, fails its test encode
	e.alloc = emptyAllocator(t)    // ANY allocation at all would error and be reported instead

	e.startRendition(lifeRendition(1, db.EncoderNVENCH264), "sig", 30, 1)

	r := e.rends[1]
	if r == nil {
		t.Fatal("a rendition that could not start left nothing in e.rends, so the " +
			"destinations downstream are never told why they are not starting")
	}
	if r.proc != nil {
		t.Error("a rendition refused for its encoder was given a process anyway")
	}
	if !strings.Contains(r.err, string(db.EncoderNVENCH264)) {
		t.Errorf("recorded error = %q, want the encoder named so the operator can act on it", r.err)
	}
	// The allocator is empty, so an error mentioning ports proves the encoder
	// check ran AFTER the allocation rather than before it.
	if strings.Contains(r.err, "no free UDP port") {
		t.Errorf("recorded error = %q: the port was allocated before the encoder was "+
			"checked, so every retry of a permanently-refused rendition costs a port", r.err)
	}
}

func TestARenditionThatCannotGetARelayPortIsRecordedRatherThanHalfStarted(t *testing.T) {
	e := lifeEngine(t)
	e.alloc = emptyAllocator(t)

	e.startRendition(lifeRendition(1, db.EncoderX264), "sig", 30, 1)

	r := e.rends[1]
	if r == nil {
		t.Fatal("nothing recorded for a rendition that could not get a port")
	}
	if r.proc != nil || r.hub != nil || r.subName != "" {
		t.Fatalf("a rendition that never got a port was published with proc=%v hub=%v sub=%q, "+
			"which teardown would then try to unwind", r.proc != nil, r.hub != nil, r.subName)
	}
	if !strings.Contains(r.err, "no free UDP port") {
		t.Errorf("recorded error = %q, want the port exhaustion named", r.err)
	}
}

// A tier standing between the ingest and the renditions can be down. When one
// is, startRendition refuses -- and it refuses AFTER it has taken a port and
// built a hub, so these are the two branches where a leak actually lives.
func TestARenditionRefusedByABrokenUpstreamTierGivesItsPortBack(t *testing.T) {
	for _, tc := range []struct {
		name  string
		tier  func(*Engine)
		names string
	}{
		{
			// Enabled but with no hub: exactly what selectorProblem reports.
			name:  "the failover selector",
			tier:  func(e *Engine) { e.sel = &selector{err: "bind udp://127.0.0.1:21000: address already in use"} },
			names: "selector",
		},
		{
			name:  "the silence tier",
			tier:  func(e *Engine) { e.silence = &silenceTier{err: "anullsrc is not in this build"} },
			names: "silence",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := lifeEngine(t)
			e.alloc = oneSlotAllocator(t)
			tc.tier(e)

			e.startRendition(lifeRendition(1, db.EncoderX264), "sig", 30, 1)

			r := e.rends[1]
			if r == nil || r.err == "" {
				t.Fatal("a rendition refused by a broken upstream tier recorded no reason, so " +
					"its destinations are never told why they are not starting")
			}
			if !strings.Contains(r.err, tc.names) {
				t.Errorf("recorded error = %q, want %q named as the cause", r.err, tc.names)
			}
			enginePort(t, e, "after a rendition was refused by "+tc.name)
		})
	}
}

// Shutdown can land between the reconcile deciding to start a rendition and the
// rendition being published. Stop() collects processes under e.mu, so anything
// published after it has run is an encoder nothing will ever stop.
func TestARenditionStartingIntoAShutdownEngineLeavesNoOrphan(t *testing.T) {
	e := lifeEngine(t)
	e.alloc = oneSlotAllocator(t)
	e.stopped = true

	e.startRendition(lifeRendition(1, db.EncoderX264), "sig", 30, 1)

	if r, ok := e.rends[1]; ok {
		t.Fatalf("a rendition was published into a stopped engine (%+v); nothing will "+
			"ever stop it and it holds a UDP port for the life of the process", r)
	}
	if hasSubscriber(e.hub, "rendition:1") {
		t.Error("the ingest hub is still forwarding to a rendition that was never started")
	}
	enginePort(t, e, "after a rendition start was abandoned at shutdown")
}

// The success path and its undo, together: what start publishes is exactly what
// teardown needs to give back, and a field missing from either half is a leak
// that only shows up as a port the allocator will not hand out an hour later.
//
// Run with a silence tier in place, which is the configuration where the
// rendition's upstream is NOT the ingest hub. RenditionArgs copies audio through
// untouched, so a video-only ingest with a synthetic track is exactly when the
// distinction is load-bearing -- and it is the only configuration in which
// publishing or unsubscribing the wrong hub is visible at all.
func TestAStartedRenditionPublishesTheWiringItsTeardownNeeds(t *testing.T) {
	e := lifeEngine(t)
	e.alloc = oneSlotAllocator(t)
	e.silence = &silenceTier{hub: lifeHub(t), spec: "silence-spec"}
	if e.sourceHub() == e.hub {
		t.Fatal("the fixture did not put a silence tier between the ingest and the renditions")
	}
	// A decoy of the name the rendition will use, on the hub it must NOT touch.
	e.hub.Subscribe("rendition:1", freeUDPPort(t))

	e.startRendition(lifeRendition(1, db.EncoderX264), "spec-a", 30, 2)

	r := e.rends[1]
	if r == nil {
		t.Fatal("no rendition was published")
	}
	t.Cleanup(func() { e.teardownRendition(r) })
	if r.err != "" {
		t.Fatalf("start failed: %s", r.err)
	}
	if r.subName != "rendition:1" {
		t.Errorf("subName = %q, want %q", r.subName, "rendition:1")
	}
	if r.port == 0 {
		t.Error("the published rendition carries no port, so teardown releases nothing " +
			"and the number is spoken for until the process exits")
	}
	if r.hub == nil {
		t.Error("the published rendition carries no output hub, so its destinations have " +
			"nothing to read and teardown closes nothing")
	}
	if r.in != e.sourceHub() {
		t.Error("the published rendition does not record which hub it subscribed to, so " +
			"teardown unsubscribes from the wrong one and the orphaned subscription " +
			"forwards this programme to whoever the allocator hands that port to next")
	}
	if r.spec != "spec-a" {
		t.Errorf("spec = %q, want the signature the reconcile decided on; without it every "+
			"reconcile sees a changed signature and cycles a healthy encode", r.spec)
	}
	if !hasSubscriber(e.sourceHub(), "rendition:1") {
		t.Fatal("the encode was started without a subscription, so it reads nothing")
	}

	e.teardownRendition(r)
	if hasSubscriber(e.sourceHub(), "rendition:1") {
		t.Error("teardown left the subscription behind on the relay the encode was reading")
	}
	if !hasSubscriber(e.hub, "rendition:1") {
		t.Error("teardown removed the ingest hub's own consumer of that name, which this " +
			"rendition never subscribed to")
	}
	enginePort(t, e, "after a rendition was torn down")
}

// The rendition reads the silence tier's hub, not the ingest's, whenever a
// video-only ingest has one. Teardown has to unsubscribe from the hub it
// actually subscribed to: unsubscribing from e.hub instead removes a NAME that
// happens to match on the wrong relay and leaves the real one forwarding.
func TestTeardownUnsubscribesFromTheHubTheEncodeActuallyRead(t *testing.T) {
	e := lifeEngine(t)
	e.alloc = oneSlotAllocator(t)
	upstream := lifeHub(t) // stands in for the silence or selector tier's relay

	port := enginePort(t, e, "reserving the rendition's port")
	upstream.Subscribe("rendition:1", port)
	// A decoy of the SAME NAME on the ingest hub. If teardown unsubscribes from
	// e.hub it will look like it worked unless something is watching that name
	// on both relays.
	e.hub.Subscribe("rendition:1", port)

	own := lifeHub(t)
	e.teardownRendition(&rendition{
		row: lifeRendition(1, db.EncoderX264), hub: own, in: upstream,
		port: port, subName: "rendition:1",
	})

	if hasSubscriber(upstream, "rendition:1") {
		t.Error("teardown did not unsubscribe from the hub the encode was reading; that " +
			"relay keeps forwarding to a port the allocator has since handed to somebody else")
	}
	if !hasSubscriber(e.hub, "rendition:1") {
		t.Error("teardown unsubscribed from the ingest hub, which this rendition never " +
			"subscribed to -- it removed an unrelated consumer of the same name")
	}
	enginePort(t, e, "after a rendition reading a non-ingest hub was torn down")
}

// A caption must never cost the picture. An FFmpeg built without libfreetype
// rejects the whole filtergraph with "No such filter: 'drawtext'", the encode
// dies, the supervisor restarts it, and it dies again -- so the text is dropped
// and the rendition runs.
func TestARenditionWithNoDrawtextInTheBuildStartsWithoutTheCaptionRatherThanNotAtAll(t *testing.T) {
	withText := func(t *testing.T, filters []string) []string {
		t.Helper()
		e := lifeEngine(t)
		e.alloc = oneSlotAllocator(t)
		e.tools = &ffmpeg.Tools{FFmpeg: "polyemesis-no-such-binary", Filters: filters}

		fonts := filepath.Join(e.cfg.DataDir, ffmpeg.FontsDirName)
		if err := os.MkdirAll(fonts, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(fonts, "Test.ttf"), []byte("not really a font"), 0o644); err != nil {
			t.Fatal(err)
		}

		row := lifeRendition(1, db.EncoderX264)
		row.Text = db.RenditionText{Content: "LIVE", Font: "Test.ttf", Anchor: "top-left", SizePct: 0.05}

		e.startRendition(row, "sig", 30, 1)
		r := e.rends[1]
		if r == nil || r.err != "" {
			t.Fatalf("the rendition did not start: %+v", r)
		}
		t.Cleanup(func() { e.teardownRendition(r) })
		if r.proc == nil {
			t.Fatal("the rendition was published with no process")
		}
		return r.proc.Args()
	}

	// The positive half first. Without it, "no drawtext on the command line"
	// would also be satisfied by a build that had stopped drawing text at all.
	if args := strings.Join(withText(t, []string{"scale", "drawtext"}), " "); !strings.Contains(args, "drawtext") {
		t.Fatalf("an FFmpeg that HAS drawtext was not asked to draw the operator's text: %s", args)
	}

	args := strings.Join(withText(t, []string{"scale", "overlay"}), " ")
	if strings.Contains(args, "drawtext") {
		t.Errorf("an FFmpeg with no drawtext filter was handed one, so the graph is "+
			"rejected at start and the whole rendition -- and every destination on "+
			"it -- is off air over a caption: %s", args)
	}
}

// Both teardowns fall back to the ingest hub when the child did not record
// which relay it read -- a child published by a path that predates the field,
// or by one that failed between subscribing and publishing. The fallback is
// what keeps the subscription from being orphaned; without it the teardown
// dereferences nil and takes the whole reconcile down with it.
func TestATeardownWithNoRecordedUpstreamFallsBackToTheIngestRatherThanPanicking(t *testing.T) {
	t.Run("rendition", func(t *testing.T) {
		e := lifeEngine(t)
		e.alloc = oneSlotAllocator(t)
		port := enginePort(t, e, "reserving the rendition's port")
		e.hub.Subscribe("rendition:1", port)

		e.teardownRendition(&rendition{
			row: lifeRendition(1, db.EncoderX264), port: port, subName: "rendition:1",
		})

		if hasSubscriber(e.hub, "rendition:1") {
			t.Error("the ingest hub still forwards to a torn-down rendition that never " +
				"recorded which relay it read")
		}
		enginePort(t, e, "after a rendition with no recorded upstream was torn down")
	})

	t.Run("loudness monitor", func(t *testing.T) {
		e := lifeEngine(t)
		e.alloc = oneSlotAllocator(t)
		port := enginePort(t, e, "reserving the analyser's port")
		e.hub.Subscribe(loudnessSubPrefix+"7", port)

		e.teardownLoudness(&loudnessMon{port: port, subName: loudnessSubPrefix + "7"})

		if hasSubscriber(e.hub, loudnessSubPrefix+"7") {
			t.Error("the ingest hub still forwards to a torn-down analyser that never " +
				"recorded which relay it read")
		}
		enginePort(t, e, "after an analyser with no recorded upstream was torn down")
	})
}

// A reconcile hands these whatever the previous step produced, and Stop can
// have emptied the map underneath it. A nil must end the one child, not the
// reconcile: the panic would abort every teardown queued behind it and leave
// those children running with their ports held.
func TestANilChildEndsOneTeardownRatherThanTheWholeReconcile(t *testing.T) {
	e := lifeEngine(t)
	e.startRendition(nil, "sig", 30, 1)
	if len(e.rends) != 0 {
		t.Errorf("a nil rendition row produced %d entries in e.rends", len(e.rends))
	}
	e.teardownRendition(nil)
	e.teardownLoudness(nil)
}

// -------------------------------------------------------------------- stopAux

// stopAux is shared by the recorder, the preview and the meters sidecar, and it
// decides which port and which signature to clear from the NAME it was given.
// Clearing the wrong one leaks the right one and cycles a healthy child.
func TestStoppingOneAuxiliaryChildClearsOnlyItsOwnPortAndSignature(t *testing.T) {
	type slot struct {
		aux  auxSlot
		port func(*Engine) int
		sig  func(*Engine) string
	}
	// AGAINST THE PRODUCTION SLOTS, not a parallel list. #714.
	//
	// This table used to carry its own copy of the recorder/preview/meters
	// correspondence -- which is the same duplication the production switch
	// had, and would have gone on passing if the two drifted apart. Naming the
	// real auxSlot values means a fourth consumer added to production without a
	// row here is a compile error at `aux`, not a silently unexercised path.
	slots := []slot{
		{auxRecorder, func(e *Engine) int { return e.recorderPort }, func(e *Engine) string { return e.recorderSig }},
		{auxPreview, func(e *Engine) int { return e.previewPort }, func(e *Engine) string { return e.previewSig }},
		{auxMeters, func(e *Engine) int { return e.metersPort }, func(e *Engine) string { return e.metersSig }},
	}

	for _, stopping := range slots {
		t.Run(stopping.aux.name, func(t *testing.T) {
			e := lifeEngine(t)
			// Three ports and no spare: a released port is the ONLY one
			// Allocate can return afterwards, which is what identifies it.
			//
			// A HELD WINDOW, not freeUDPPort. That probes one port, releases it,
			// and leaves base+1 and base+2 unchecked -- so under `go test ./...`
			// something else takes one of the three and this test fails during
			// SETUP, blaming the path under test for a port it never had. Three
			// times in one day on three different ranges before it was fixed.
			base, held := testenv.FreeUDPWindow(t, 3)
			// Released together, immediately before the allocator is built: the
			// window has to be free for Allocate to hand it out, and holding it
			// until this line is what stopped anything else from taking it.
			for _, r := range held {
				r.Release()
			}
			e.alloc = relay.NewPortAllocator(base, 3)
			ports := map[string]int{}
			for _, s := range slots {
				ports[s.aux.name] = enginePort(t, e, "reserving "+s.aux.name+"'s port")
				e.hub.Subscribe(s.aux.name, ports[s.aux.name])
			}
			e.recorder, e.preview, e.meters = loudTestProc(), loudTestProc(), loudTestProc()
			e.recorderPort, e.previewPort, e.metersPort = ports["recorder"], ports["preview"], ports["meters"]
			e.recorderSig, e.previewSig, e.metersSig = "rec-sig", "prev-sig", "met-sig"

			e.stopAux(stopping.aux)

			if got := *stopping.aux.proc(e); got != nil {
				t.Error("the slot still holds a process, so the next reconcile believes " +
					"the child is running and never starts a replacement")
			}
			if got := stopping.port(e); got != 0 {
				t.Errorf("%s port = %d after stopping it, want 0; the engine will try to "+
					"release it a second time when the next child takes it", stopping.aux.name, got)
			}
			if got := stopping.sig(e); got != "" {
				t.Errorf("%s signature = %q after stopping it, want empty; a stale signature "+
					"makes the next reconcile believe the stopped child is up to date", stopping.aux.name, got)
			}
			if hasSubscriber(e.hub, stopping.aux.name) {
				t.Errorf("the hub still forwards to %s after it was stopped", stopping.aux.name)
			}

			for _, other := range slots {
				if other.aux.name == stopping.aux.name {
					continue
				}
				if *other.aux.proc(e) == nil {
					t.Errorf("stopping %s also cleared %s's process slot", stopping.aux.name, other.aux.name)
				}
				if got := other.port(e); got != ports[other.aux.name] {
					t.Errorf("stopping %s changed %s's port to %d, want %d; that child's port "+
						"is now either leaked or about to be released twice",
						stopping.aux.name, other.aux.name, got, ports[other.aux.name])
				}
				if other.sig(e) == "" {
					t.Errorf("stopping %s cleared %s's signature, which cycles a healthy child",
						stopping.aux.name, other.aux.name)
				}
				if !hasSubscriber(e.hub, other.aux.name) {
					t.Errorf("stopping %s unsubscribed %s from the hub", stopping.aux.name, other.aux.name)
				}
			}

			// The released port is identifiable because nothing else is free.
			if got := enginePort(t, e, "after stopping "+stopping.aux.name); got != ports[stopping.aux.name] {
				t.Errorf("the allocator handed back port %d, want %d: the wrong port was released",
					got, ports[stopping.aux.name])
			}
		})
	}
}

// The meters sidecar is the one auxiliary child that does not always read the
// ingest -- on a video-only source it reads the silence tier's relay instead.
// Unsubscribing "meters" from e.hub leaves the real relay forwarding.
func TestTheMetersSidecarIsUnsubscribedFromTheHubItSubscribedTo(t *testing.T) {
	e := lifeEngine(t)
	e.alloc = oneSlotAllocator(t)
	silenceHub := lifeHub(t)

	port := enginePort(t, e, "reserving the sidecar's port")
	silenceHub.Subscribe("meters", port)
	e.hub.Subscribe("meters", port) // decoy of the same name on the ingest
	e.meters, e.metersPort, e.metersSig, e.metersHub = loudTestProc(), port, "met-sig", silenceHub

	e.stopAux(auxMeters)

	if hasSubscriber(silenceHub, "meters") {
		t.Error("the sidecar was not unsubscribed from the relay it was actually reading; " +
			"that relay keeps forwarding to a port the allocator has since reissued")
	}
	if !hasSubscriber(e.hub, "meters") {
		t.Error("the ingest hub's own consumer of that name was removed instead")
	}
	if e.metersHub != nil {
		t.Error("metersHub still points at the old relay, so the next stop unsubscribes " +
			"from a hub this sidecar never read")
	}
	enginePort(t, e, "after the meters sidecar was stopped")
}

// --------------------------------------------------------------------- preview

// markPreviewFlowing puts the engine in the state a live stream leaves behind:
// the hub the preview reads advanced a moment ago.
//
// Needed since the preview refuses to start against a hub that has carried
// nothing recently. These fixtures never publish, so without this every preview
// test would be asserting against a start the gate is right to refuse -- and the
// gate is what stops an encoder being spawned to transcode silence.
//
// It stamps the time rather than faking bytes because previewFlowing samples the
// real hub: with the fixture hub at zero and previewRxBytes at zero it sees no
// change and leaves the timestamp alone, which is exactly the "flowing a moment
// ago, quiet right now" state.
func markPreviewFlowing(e *Engine) {
	// The hub is adopted as well as the timestamp. previewFlowing keys its
	// baseline to the hub it sampled, so a stamp against no hub is discarded on
	// the next call as "this is a different hub, wait and see" -- which is the
	// behaviour that stops a selector swap starting an encoder against silence.
	h := e.downstreamHub()
	e.mu.Lock()
	e.previewRxHub = h
	e.previewRxAt = time.Now()
	e.mu.Unlock()
}

func previewOnSettings() db.Settings {
	s := db.DefaultSettings()
	s.Preview.Enabled = true
	s.Preview.IdleTimeoutSeconds = 30
	return s
}

// A viewer polling the playlist is the ONLY thing that keeps the encoder alive.
// A request that is not recorded because the encoder happened to be running
// already is a request the sweeper never hears about, and it kills an encoder
// somebody is watching.
func TestPreviewRequestedRecordsTheRequestEvenWhenItStartsNothing(t *testing.T) {
	e := lifeEngine(t)
	e.alloc = oneSlotAllocator(t)
	setSettings(e, previewOnSettings())
	markPreviewFlowing(e)

	// A preview that has been running, unwatched, for an hour.
	e.startPreviewLocked(e.Settings())
	if e.preview == nil {
		t.Fatal("the preview did not start")
	}
	t.Cleanup(func() { e.previewMu.Lock(); e.stopPreviewLocked(); e.previewMu.Unlock() })
	e.mu.Lock()
	e.previewSeen = time.Now().Add(-time.Hour)
	e.mu.Unlock()

	// Somebody opens the dashboard.
	e.PreviewRequested()

	// The sweeper runs a moment later. It must not stop what was just asked for.
	e.sweepPreview(time.Now())

	if e.preview == nil {
		t.Fatal("the sweeper stopped an encoder that had just been requested: the request " +
			"was not recorded because one was already running, so from the sweeper's point " +
			"of view nobody has watched this preview for an hour")
	}
}

// A burst of requests against a down encoder, or a start that keeps failing,
// must not turn into a burst of spawns.
func TestABurstOfPreviewRequestsAgainstADownEncoderSpawnsOne(t *testing.T) {
	e := lifeEngine(t)
	// TWO ports, deliberately. A one-port allocator would make the second spawn
	// fail for lack of a port and the test would pass with the debounce deleted
	// -- measured: that version of this test survived the mutation.
	base, held := testenv.FreeUDPWindow(t, 2)
	// Released together, immediately before the allocator is built: the window
	// has to be free for Allocate to hand it out, and holding it until this line
	// is what stopped anything else from taking it.
	for _, r := range held {
		r.Release()
	}
	e.alloc = relay.NewPortAllocator(base, 2)
	setSettings(e, previewOnSettings())
	markPreviewFlowing(e)

	e.PreviewRequested()
	if e.preview == nil {
		t.Fatal("the first request started nothing")
	}
	t.Cleanup(func() { e.previewMu.Lock(); e.stopPreviewLocked(); e.previewMu.Unlock() })

	// The encoder dies immediately -- the binary does not exist -- and the next
	// playlist poll arrives well inside the debounce window.
	e.mu.Lock()
	e.preview = nil
	e.mu.Unlock()
	e.PreviewRequested()

	if e.preview != nil {
		t.Fatal("a second preview was spawned inside the debounce window, so an encoder " +
			"that cannot start is retried on every playlist poll -- a burst of requests " +
			"against a down encoder becomes a burst of spawns")
	}
	// One port went to the first spawn. If a second spawn happened, both are
	// gone and the one that "did not start" is holding one for ever.
	if _, err := e.alloc.Allocate(); err != nil {
		t.Errorf("the allocator has no port left (%v): a second preview took one and was "+
			"then dropped on the floor, so the number is spoken for until the process exits", err)
	}
}

// Old segments from a previous session would otherwise be served to the player
// before the new ones appear -- a viewer watching last night's stream.
func TestStartingThePreviewClearsThePreviousSessionsSegments(t *testing.T) {
	e := lifeEngine(t)
	e.alloc = oneSlotAllocator(t)

	// The source's OWN directory, which is where the preview writes now. Reading
	// the shared parent would assert against a path nothing uses.
	dir := e.cfg.HLSDirFor(e.sourceID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(dir, "preview0042.ts")
	if err := os.WriteFile(stale, []byte("last night"), 0o644); err != nil {
		t.Fatal(err)
	}

	markPreviewFlowing(e)
	e.startPreviewLocked(previewOnSettings())
	t.Cleanup(func() { e.previewMu.Lock(); e.stopPreviewLocked(); e.previewMu.Unlock() })

	if _, err := os.Stat(stale); err == nil {
		t.Error("a segment from the previous session survived the start, so the player " +
			"is served it before the new ones appear")
	}
	if e.preview == nil || e.previewPort == 0 {
		t.Fatalf("the preview did not come up: proc=%v port=%d", e.preview != nil, e.previewPort)
	}
	if e.previewSig != previewSig(previewOnSettings()) {
		t.Errorf("previewSig = %q, want the signature of the settings it was started with; "+
			"without it the next reconcile cycles a healthy encoder", e.previewSig)
	}
	if !hasSubscriber(e.hub, "preview") {
		t.Error("the preview encoder was started without a subscription, so it reads nothing")
	}
}

// A playlist request can land while the engine is shutting down. Stop() has
// already walked the process map, so an encoder started after it is one nothing
// will ever stop -- and it holds an HLS directory Stop has just cleared.
func TestAPreviewRequestedDuringShutdownStartsNothing(t *testing.T) {
	e := lifeEngine(t)
	e.alloc = oneSlotAllocator(t)
	e.stopped = true

	e.startPreviewLocked(previewOnSettings())

	if e.preview != nil {
		t.Fatal("a preview encoder was started into a stopped engine; nothing will stop it")
	}
	if e.previewPort != 0 {
		t.Errorf("previewPort = %d after a start into a stopped engine", e.previewPort)
	}
	if hasSubscriber(e.hub, "preview") {
		t.Error("the ingest hub is forwarding to a preview that was never started")
	}
	enginePort(t, e, "after a preview start into a stopped engine")
}

// The playlist left behind would be served to the next viewer, pointing at
// segments the next start is about to delete.
func TestStoppingThePreviewRemovesThePlaylistAndReturnsThePort(t *testing.T) {
	e := lifeEngine(t)
	e.alloc = oneSlotAllocator(t)

	markPreviewFlowing(e)
	e.startPreviewLocked(previewOnSettings())
	if e.preview == nil {
		t.Fatal("the preview did not start")
	}
	playlist := filepath.Join(e.cfg.HLSDirFor(e.sourceID), "preview.m3u8")
	if err := os.WriteFile(playlist, []byte("#EXTM3U"), 0o644); err != nil {
		t.Fatal(err)
	}

	e.previewMu.Lock()
	e.stopPreviewLocked()
	e.previewMu.Unlock()

	if _, err := os.Stat(playlist); err == nil {
		t.Error("the playlist survived the stop; the next viewer is handed it and asks for " +
			"segments the next start deletes")
	}
	if e.preview != nil || e.previewPort != 0 || e.previewSig != "" {
		t.Errorf("the preview slot was not cleared: proc=%v port=%d sig=%q",
			e.preview != nil, e.previewPort, e.previewSig)
	}
	if hasSubscriber(e.hub, "preview") {
		t.Error("the hub still forwards to a stopped preview encoder")
	}
	enginePort(t, e, "after the preview was stopped")
}

// The encoder exists to serve a dashboard nobody may be looking at. Both
// directions matter: never stopping wastes a core for ever, and stopping a
// watched one blanks the operator's picture.
func TestSweepPreviewStopsAnIdleEncoderAndKeepsAWatchedOne(t *testing.T) {
	now := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)

	for _, tc := range []struct {
		name string
		seen time.Duration // how long ago the last playlist request was
		stop bool
	}{
		{"a closed dashboard releases the encoder", 5 * time.Minute, true},
		{"a viewer polling the playlist keeps it", time.Second, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := lifeEngine(t)
			e.alloc = oneSlotAllocator(t)
			setSettings(e, previewOnSettings())
			markPreviewFlowing(e)

			e.startPreviewLocked(e.Settings())
			if e.preview == nil {
				t.Fatal("the preview did not start")
			}
			t.Cleanup(func() { e.previewMu.Lock(); e.stopPreviewLocked(); e.previewMu.Unlock() })
			e.mu.Lock()
			e.previewSeen = now.Add(-tc.seen)
			e.mu.Unlock()

			e.sweepPreview(now)

			if stopped := e.preview == nil; stopped != tc.stop {
				t.Fatalf("preview stopped = %v, want %v (last request %v ago, idle window %v)",
					stopped, tc.stop, tc.seen, previewIdleWindow(e.Settings()))
			}
			if !tc.stop {
				return
			}
			if hasSubscriber(e.hub, "preview") {
				t.Error("the idled-out encoder is still subscribed to the ingest")
			}
			enginePort(t, e, "after the preview idled out")
		})
	}
}

// -------------------------------------------------------------------- loudness

// lifePlan installs the destination the plan is FOR and then derives the plan
// from it, rather than hand-writing a plan with a literal signature.
//
// It used to do the latter, which built a state that cannot occur: a plan for
// destination 7 while the engine held no destination 7 at all. startLoudness now
// re-checks that the destination is still there and still matches before it
// publishes -- the #453 guard -- so a plan with no destination behind it is
// refused, correctly, and the fixture had to stop constructing one.
func lifePlan(t *testing.T, e *Engine, hub *relay.Hub) loudnessPlan {
	t.Helper()
	d := &destination{
		row:      &db.Destination{ID: 7, Name: "yt", Kind: db.DestRTMP, Platform: db.PlatformYouTube},
		proc:     supervisor.New(slog.New(slog.NewTextHandler(io.Discard, nil)), supervisor.Spec{Name: "dest:7", Bin: "true"}),
		hub:      hub,
		compiled: routing.Result{FilterComplex: "[0:a:0]anull[aout]", OutLabel: routing.OutLabel},
		spec:     "life-spec",
	}
	e.mu.Lock()
	if e.dests == nil {
		e.dests = map[int64]*destination{}
	}
	e.dests[7] = d
	e.mu.Unlock()

	p, ok := loudnessPlanFor(7, d)
	if !ok {
		t.Fatal("fixture: this destination must earn an analyser or every test below proves nothing")
	}
	return p
}

// A meter that cannot run must never read as a destination that is compliant.
// Absent from the list is indistinguishable from "not monitored".
func TestALoudnessMonitorThatCannotGetAPortIsReportedAsFailed(t *testing.T) {
	e := lifeEngine(t)
	e.alloc = emptyAllocator(t)
	hub := lifeHub(t)

	e.startLoudness(lifePlan(t, e, hub))

	rep, ok := e.loudStore.Get(7)
	if !ok {
		t.Fatal("an analyser that could not start left no report, so the dashboard shows " +
			"nothing at all where a broken meter should be")
	}
	if rep.Error == "" {
		t.Errorf("report = %+v, want the failure recorded; a meter that is broken must "+
			"never read as a destination that is compliant", rep)
	}
	if _, running := e.loud[7]; running {
		t.Error("an analyser that never started was published as running")
	}
}

func TestALoudnessMonitorStartingIntoAShutdownEngineLeavesNoOrphan(t *testing.T) {
	e := lifeEngine(t)
	e.alloc = oneSlotAllocator(t)
	e.stopped = true
	hub := lifeHub(t)

	e.startLoudness(lifePlan(t, e, hub))

	if m, ok := e.loud[7]; ok {
		t.Fatalf("an analyser was published into a stopped engine (%+v); nothing will stop "+
			"it and it holds a UDP port for the life of the process", m)
	}
	if hasSubscriber(hub, loudnessSubPrefix+"7") {
		t.Error("the hub still forwards to an analyser that was never started")
	}
	enginePort(t, e, "after an analyser start was abandoned at shutdown")
}

// The round trip. The analyser reads the destination's own upstream hub, which
// is not the ingest's on a rendition-fed destination, so teardown has the same
// wrong-hub hazard the renditions do.
func TestALoudnessMonitorRoundTripsItsPortAndSubscription(t *testing.T) {
	e := lifeEngine(t)
	e.alloc = oneSlotAllocator(t)
	hub := lifeHub(t) // the destination's upstream, NOT e.hub

	plan := lifePlan(t, e, hub)
	e.startLoudness(plan)

	m := e.loud[7]
	if m == nil {
		t.Fatal("the analyser was not published")
	}
	if m.subName != loudnessSubPrefix+"7" || m.port == 0 || m.hub != hub || m.sig != plan.sig {
		t.Fatalf("published analyser = %+v, want the subscription, port, hub and signature "+
			"teardown and the next reconcile both need", m)
	}
	if !hasSubscriber(hub, m.subName) {
		t.Fatal("the analyser was started without a subscription, so it measures silence")
	}
	if rep, ok := e.loudStore.Get(7); !ok || rep.Error != "" {
		t.Errorf("report = %+v ok=%v, want a 'starting' placeholder so the destination reads "+
			"as monitored-and-waiting rather than missing", rep, ok)
	}
	// A decoy of the same name on the ingest hub, so unsubscribing from the
	// wrong relay cannot look like success.
	e.hub.Subscribe(m.subName, m.port)

	e.teardownLoudness(m)

	if hasSubscriber(hub, m.subName) {
		t.Error("teardown left the analyser subscribed to the relay it was reading; that " +
			"relay keeps forwarding to a port the allocator has since reissued")
	}
	if !hasSubscriber(e.hub, m.subName) {
		t.Error("teardown unsubscribed from the ingest hub, which this analyser never read")
	}
	enginePort(t, e, "after an analyser was torn down")
}
