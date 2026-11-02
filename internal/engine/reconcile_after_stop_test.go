package engine

import (
	"context"
	"fmt"
	"net"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// A RECONCILE AFTER STOP BINDS NOTHING.
//
// The post-production tier can still be finishing a job while the server shuts
// down, and a finished normalisation asks the manager to Reconcile. StopWithin
// has already closed the shared SRT and RTMP listeners by then; a reconcile
// that ran reconcileSharedIngest anyway found them "missing" and bound them
// again -- on a server that is exiting, holding the ports against the next
// process to start.
//
// Deterministic: Stop has returned before Reconcile is called, so the only
// question is whether Reconcile honours it. Proved on the ports themselves --
// the test binds both after the reconcile, which fails while the manager holds
// either -- as well as on ListenerBound.
//
// Mutation: drop the stopped check from reconcileSharedIngest. Observed to fail
// here.
func TestAReconcileAfterStopBindsNoListener(t *testing.T) {
	m, store := managerFixture(t)
	st, err := store.GetSettings()
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	srtPort, rtmpPort := freeUDPPort(t), freeTCPPort(t)
	st.Listeners.SRTPort, st.Listeners.RTMPPort = srtPort, rtmpPort
	if err := store.PutSettings(st); err != nil {
		t.Fatalf("PutSettings: %v", err)
	}
	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if !m.ListenerBound(db.IngestSRT) || !m.ListenerBound(db.IngestRTMP) {
		t.Fatal("the listeners did not bind at Start, so there is nothing for Stop to close")
	}

	m.Stop()
	_ = m.Reconcile() // the late reconcile a finishing job triggers

	if m.ListenerBound(db.IngestSRT) || m.ListenerBound(db.IngestRTMP) {
		t.Fatalf("a reconcile after Stop re-bound the shared ingest: srt=%v rtmp=%v",
			m.ListenerBound(db.IngestSRT), m.ListenerBound(db.IngestRTMP))
	}
	tcp, err := net.Listen("tcp", fmt.Sprintf(":%d", rtmpPort))
	if err != nil {
		t.Fatalf("the RTMP port is still held after Stop and a late reconcile: %v", err)
	}
	_ = tcp.Close()
	udp, err := net.ListenPacket("udp", fmt.Sprintf(":%d", srtPort))
	if err != nil {
		t.Fatalf("the SRT port is still held after Stop and a late reconcile: %v", err)
	}
	_ = udp.Close()
}

// And a Manager started again after a Stop binds as before: the flag is the
// stop, not a permanent switch.
func TestStartAfterStopBindsAgain(t *testing.T) {
	m, store := managerFixture(t)
	st, err := store.GetSettings()
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	st.Listeners.SRTPort, st.Listeners.RTMPPort = freeUDPPort(t), freeTCPPort(t)
	if err := store.PutSettings(st); err != nil {
		t.Fatalf("PutSettings: %v", err)
	}
	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	m.Stop()
	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("second Start: %v", err)
	}
	if !m.ListenerBound(db.IngestSRT) || !m.ListenerBound(db.IngestRTMP) {
		t.Fatal("a Manager started again after Stop did not bind its listeners")
	}
}
