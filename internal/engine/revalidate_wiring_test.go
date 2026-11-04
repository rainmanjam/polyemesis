package engine

import (
	"testing"
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
