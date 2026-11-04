package engine

import (
	"context"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// Both shared listeners must be given the error-aware lookup for publisher
// revalidation. Without it a locked database answers "not found", which reads
// as a revoked token, and every live encoder is dropped (poka-yoke audit
// 2026-11-04 review).
func TestTheSharedListenersRevalidateWithAnErrorAwareLookup(t *testing.T) {
	m, store := managerFixture(t)
	st, err := store.GetSettings()
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	st.Listeners.SRTPort = freeUDPPort(t)
	st.Listeners.RTMPPort = freeTCPPort(t)
	if err := store.PutSettings(st); err != nil {
		t.Fatalf("PutSettings: %v", err)
	}
	m.reconcileSharedIngest()

	m.mu.RLock()
	srt, rtmp := m.srt, m.rtmp
	m.mu.RUnlock()
	if srt == nil || rtmp == nil {
		t.Fatalf("listeners not up: srt=%v rtmp=%v", srt != nil, rtmp != nil)
	}
	if !srt.HasRevalidateLookup() {
		t.Error("the SRT listener revalidates through the admission lookup, which cannot tell a locked database from a revoked token")
	}
	if !rtmp.HasRevalidateLookup() {
		t.Error("the RTMP listener revalidates through the admission lookup, which cannot tell a locked database from a revoked token")
	}
}

// Revalidation closes an SRT session whose token now resolves to a different
// sink. That is only right if an unchanged engine hands back the SAME sink on
// every lookup -- a fresh value per call would close every publisher every ten
// seconds. Pinned across a reconcile that does not restart the engine.
func TestAnUnchangedEngineResolvesToTheSameSinkEveryTime(t *testing.T) {
	m, store := managerFixture(t)
	src := addSource(t, store, "Studio")
	src.Ingest.Mode = db.IngestSRT
	if err := store.UpdateSource(src); err != nil {
		t.Fatalf("UpdateSource: %v", err)
	}
	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	fresh, err := store.GetSource(src.ID)
	if err != nil {
		t.Fatal(err)
	}
	tokens := fresh.ValidTokens(time.Now())
	if len(tokens) == 0 {
		t.Fatal("the source has no publish token; this test would check nothing")
	}
	first, ok, err := m.lookupTokenE(tokens[0])
	if err != nil || !ok || first.Sink == nil {
		t.Fatalf("lookup = %+v ok=%v err=%v; want a running engine's sink", first, ok, err)
	}
	if err := m.Reconcile(); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	second, ok, err := m.lookupTokenE(tokens[0])
	if err != nil || !ok {
		t.Fatalf("second lookup ok=%v err=%v", ok, err)
	}
	if first.Sink != second.Sink {
		t.Error("the same engine resolved to a different sink; revalidation would close every SRT publisher")
	}
}
