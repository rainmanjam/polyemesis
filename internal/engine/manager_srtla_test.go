package engine

import (
	"context"
	"crypto/rand"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/srtla"
)

// The SRTLA bonding receiver (#6) is one more shared listener: off at port 0,
// up when a port is set, rebound when its port OR the SRT listener it forwards
// to moves, and down with the manager.
func TestTheSRTLAReceiverFollowsItsPortAndTheSRTListenerItFeeds(t *testing.T) {
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
	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { m.Stop() })
	bound := func() (string, string) {
		m.mu.RLock()
		defer m.mu.RUnlock()
		if m.srtla == nil {
			return "", ""
		}
		return m.srtlaAddr, m.srtlaUpstream
	}
	if a, _ := bound(); a != "" {
		t.Fatalf("the SRTLA receiver is bound at %q with its port at 0; it is off by default", a)
	}
	if m.SRTLAGroups() != nil {
		t.Error("SRTLAGroups answered with the receiver off")
	}

	st.Listeners.SRTLAPort = freeUDPPort(t)
	if err := store.PutSettings(st); err != nil {
		t.Fatalf("PutSettings: %v", err)
	}
	if err := m.Reconcile(); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	addr, up := bound()
	if want := fmt.Sprintf(":%d", st.Listeners.SRTLAPort); addr != want {
		t.Fatalf("SRTLA receiver at %q, want %q", addr, want)
	}
	if want := fmt.Sprintf("127.0.0.1:%d", st.Listeners.SRTPort); up != want {
		t.Fatalf("SRTLA forwards to %q, want the SRT listener %q", up, want)
	}

	// The SRT listener moves: the receiver must follow, or it forwards into a
	// port nothing listens on.
	st.Listeners.SRTPort = freeUDPPort(t)
	if err := store.PutSettings(st); err != nil {
		t.Fatalf("PutSettings: %v", err)
	}
	if err := m.Reconcile(); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if _, up := bound(); up != fmt.Sprintf("127.0.0.1:%d", st.Listeners.SRTPort) {
		t.Fatalf("after the SRT port moved the receiver still forwards to %q", up)
	}

	st.Listeners.SRTLAPort = 0
	if err := store.PutSettings(st); err != nil {
		t.Fatalf("PutSettings: %v", err)
	}
	if err := m.Reconcile(); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if a, _ := bound(); a != "" {
		t.Fatalf("port 0 left the receiver bound at %q", a)
	}
}

// The SRT listener's wrong-token lockout is keyed by peer host; a bonded
// publisher arrives from loopback, so it is renamed to the sender that made
// its group. Any other peer keeps its own host.
func TestABondedSRTPeerIsLockedOutUnderItsSendersAddressNotLoopback(t *testing.T) {
	m := &Manager{}
	lo := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 40000}
	if got := m.srtPeerHost(lo); got != "127.0.0.1" {
		t.Errorf("with no receiver, a loopback peer is %q, want 127.0.0.1", got)
	}
	remote := &net.UDPAddr{IP: net.IPv4(203, 0, 113, 9), Port: 5000}
	if got := m.srtPeerHost(remote); got != "203.0.113.9" {
		t.Errorf("a remote peer is %q, want its own host", got)
	}
}

func TestABondedPeerIsNamedAfterTheSenderThatCreatedItsGroup(t *testing.T) {
	up, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer up.Close()
	srv := srtla.New(nil, srtla.Config{Listen: "127.0.0.1:0", Upstream: up.LocalAddr().String()})
	if err := srv.Start(); err != nil {
		t.Fatalf("srtla Start: %v", err)
	}
	defer srv.Stop()
	m := &Manager{srtla: srv}

	sender, err := net.DialUDP("udp", nil, srv.Addr().(*net.UDPAddr))
	if err != nil {
		t.Fatal(err)
	}
	defer sender.Close()
	reg1 := make([]byte, 258)
	reg1[0], reg1[1] = 0x92, 0x00
	_, _ = rand.Read(reg1[2:])
	if _, err := sender.Write(reg1); err != nil {
		t.Fatal(err)
	}
	_ = sender.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := sender.Read(make([]byte, 1500)); err != nil {
		t.Fatalf("no REG2 for the group: %v", err)
	}
	groups := srv.Stats()
	if len(groups) != 1 {
		t.Fatalf("%d groups, want 1", len(groups))
	}
	peer, err := net.ResolveUDPAddr("udp", groups[0].UpstreamLocal)
	if err != nil {
		t.Fatal(err)
	}
	if got := m.srtPeerHost(peer); got != "srtla:127.0.0.1" {
		t.Errorf("the group's loopback peer is named %q, want srtla:127.0.0.1 -- its creator", got)
	}
}
