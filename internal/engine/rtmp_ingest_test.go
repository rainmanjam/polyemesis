package engine

import (
	"context"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
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
func TestAnRTMPTargetIsNotReadyUntilItsEngineIs(t *testing.T) {
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
	if !ok || !after.Ready {
		t.Errorf("target = %+v, %v; want Ready once the engine is up", after, ok)
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
