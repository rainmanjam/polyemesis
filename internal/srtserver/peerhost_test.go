package srtserver

import (
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/authgate"
)

// A publisher bonded through the SRTLA receiver (#6) reaches this listener from
// LOOPBACK: the receiver's own socket. Keyed by peer host, the wrong-token
// lockout would then be one bucket for every bonded encoder, and one sender
// guessing tokens would lock all of them out. SetPeerHost lets the receiver
// name the real sender instead.
//
// Mutation: handleConnect, use authgate.PeerHost(req.RemoteAddr()) instead of
// s.peerHostOf(...). Expected to fail: the victim is refused.
func TestThePeerHostResolverKeepsOneBondedSendersGuessesFromLockingOutAnother(t *testing.T) {
	tg := Target{SourceID: 1, Name: "bonded", Enabled: true, Sink: &recorder{}}
	s, addr := serve(t, tg)
	var who atomic.Value
	who.Store("attacker")
	var seen atomic.Value
	s.SetPeerHost(func(a net.Addr) string {
		seen.CompareAndSwap(nil, a)
		return who.Load().(string)
	})

	for i := 0; i < authgate.Threshold; i++ {
		if c, err := dial(t, addr, "not-a-real-token"); err == nil {
			c.Close()
			t.Fatalf("attempt %d: an unknown token was accepted", i)
		}
	}
	deadline := time.Now().Add(2 * time.Second)
	for !s.gate.Blocked("attacker") {
		if time.Now().After(deadline) {
			t.Fatal("the gate never blocked the resolved attacker host")
		}
		time.Sleep(5 * time.Millisecond)
	}
	// The Manager's resolver (engine.Manager.srtPeerHost) type-asserts the
	// peer to *net.UDPAddr and asks the SRTLA receiver by its String(); if
	// gosrt handed back anything else the rename would silently never happen.
	ua, ok := seen.Load().(*net.UDPAddr)
	if !ok {
		t.Fatalf("the resolver was handed a %T, not a *net.UDPAddr", seen.Load())
	}
	if !ua.IP.IsLoopback() || ua.Port == 0 {
		t.Errorf("the resolver was handed %v, want this dialler's loopback address and port", ua)
	}
	if s.gate.Blocked("127.0.0.1") {
		t.Fatal("the lockout landed on loopback, which every bonded encoder shares")
	}

	who.Store("victim")
	c, err := dial(t, addr, tokenFor(tg))
	if err != nil {
		t.Fatalf("a different bonded sender with the right token was refused: %v", err)
	}
	c.Close()
}
