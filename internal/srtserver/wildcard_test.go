package srtserver

import (
	"fmt"
	"io"
	"log/slog"
	"net"
	"strconv"
	"sync"
	"testing"
)

// Issue #28 was filed as "the SRT listener may bind IPv6-only", on the evidence
// that lsof showed only an IPv6 socket. That evidence was misread: a Go
// dual-stack socket looks exactly like that, and a plain net.ListenPacket on
// the same address receives IPv4 datagrams fine.
//
// The real behaviour, measured against gosrt v0.11.0: a wildcard listener on
// macOS never completes the handshake for an IPv4 caller. gosrt replies through
// x/net's ipv4.PacketConn with an IPv4 control message on an AF_INET6 socket,
// Darwin rejects it with "sendmsg: invalid argument", and writeToFrom has no
// error return -- so the failure is discarded, handleConnect is never reached,
// and none of its typed refusals fire. Both ends stay silent.
//
// Fixed HERE rather than waited for upstream: gosrt chooses its network from
// the address, and only the empty-host form takes the broken path.

func TestAWildcardBindsBothFamiliesExplicitly(t *testing.T) {
	s := New(slog.New(slog.NewTextHandler(io.Discard, nil)), ":6000", nil)
	got := s.bindAddrs()

	if len(got) != 2 {
		t.Fatalf("a wildcard produced %v; it must bind BOTH families explicitly, "+
			"because the empty-host form is the one gosrt maps to a dual-stack "+
			"socket and cannot reply to IPv4 peers on Darwin", got)
	}
	// The concrete forms matter: they are what make gosrt choose udp4 and udp6
	// instead of udp. Asserting on the count alone would pass with two
	// wildcards.
	want := map[string]bool{"0.0.0.0:6000": true, "[::]:6000": true}
	for _, a := range got {
		if !want[a] {
			t.Errorf("bound %q, which is not a concrete address family", a)
		}
		delete(want, a)
	}
	for missing := range want {
		t.Errorf("never bound %s, so that family's publishers cannot connect", missing)
	}
}

// An explicit host already picks a family and was never the failing shape.
// Splitting it would bind an address the operator did not ask for.
func TestAnExplicitHostIsLeftAlone(t *testing.T) {
	for _, addr := range []string{"0.0.0.0:6000", "127.0.0.1:6000", "[::]:6000", "[::1]:6000"} {
		s := New(slog.New(slog.NewTextHandler(io.Discard, nil)), addr, nil)
		got := s.bindAddrs()
		if len(got) != 1 || got[0] != addr {
			t.Errorf("bindAddrs(%q) = %v, want just the address it was given", addr, got)
		}
	}
}

// The claim the whole fix rests on: a udp4 socket and a udp6 socket can hold
// the same port, because Go sets IPV6_V6ONLY for the "udp6" network. If that
// were false, the wildcard split would fail to bind its second family on every
// platform and the fix would be worse than the bug.
func TestBothFamiliesCanHoldTheSamePort(t *testing.T) {
	// THE FIRST BIND IS THE RESERVATION. The port is whatever the kernel hands
	// the udp4 wildcard socket, and that socket is the one under test, so there
	// is no moment between choosing the number and holding it. See
	// heldWildcardPort for why the old "pick a free port, release it, bind it
	// again" failed on Windows, and for the one case this can still meet: an
	// IPv6-only socket elsewhere already on the number IPv4 handed out.
	p4, err := net.ListenPacket("udp4", "0.0.0.0:0")
	if err != nil {
		t.Fatalf("udp4 bind: %v", err)
	}
	defer p4.Close()
	port := p4.LocalAddr().(*net.UDPAddr).Port
	p6, err := net.ListenPacket("udp6", "[::]:"+strconv.Itoa(port))
	if err != nil {
		t.Fatalf("udp6 bind on the same port failed, so the two families conflict "+
			"and the wildcard split cannot work: %v", err)
	}
	defer p6.Close()
}

// heldWildcardPort reserves a port on BOTH wildcard families -- exactly the two
// sockets Start is about to bind -- and returns it with the function that hands
// it back. Call release on the line before Start.
//
// WHY NOT testenv.FreeUDPPort, which both tests in this file used until they
// failed together on windows-latest in one run (a udp4 0.0.0.0 bind refused
// with WSAEADDRINUSE, and an IPv4 dial answered with a REJECT by somebody
// else's SRT server):
//
//   - FreeUDPPort draws from a LOW BAND that every test process scans, and
//     internal/engine and internal/api start real wildcard SRT listeners on
//     those numbers. Each process walks the band in steps of 8 from a start
//     derived from its PID. Windows PIDs are multiples of 4, so every Windows
//     process starts on one of only two residues mod 8, and two packages
//     running at once are walking the same lattice of ports.
//   - The probe asks a narrower question than the bind. It binds 127.0.0.1;
//     the test then binds 0.0.0.0. With the other process's socket bound by
//     gosrt (which sets SO_REUSEADDR), Windows let the probe's 127.0.0.1 bind
//     through and refused the 0.0.0.0 one -- the failure seen, on 24588.
//   - gosrt's SO_REUSEADDR also means Start's own binds can succeed on a port
//     another socket is using: on Windows over an active socket, and on macOS
//     as a wildcard over another socket's 127.0.0.1 (checked: Start reported
//     both families bound, and the IPv4 dial went to the other socket). The
//     publisher's handshake is then answered by whoever holds 127.0.0.1.
//
// An ephemeral (:0) draw avoids the lattice: the kernel hands out a port no
// IPv4 socket holds, and on a stock range (Linux 32768+, macOS and Windows
// 49152+) that is above the 20000-30720 band the scans walk. Holding it on the
// two wildcard sockets asks the same question Start's bind does.
//
// WHAT IS LEFT, stated rather than retried around. The IPv4 draw does not
// consult IPv6-only sockets, so the udp6 bind can find the port taken; that
// fails loudly here, naming the cause. And release() opens the same
// one-statement window before Start that serve() in srtserver_test.go has
// (see internal/testenv/ports.go); in the ephemeral range nothing else in the
// suite aims at it.
func heldWildcardPort(t *testing.T) (port int, release func()) {
	t.Helper()
	p4, err := net.ListenPacket("udp4", "0.0.0.0:0")
	if err != nil {
		t.Fatalf("reserve a udp4 wildcard port: %v", err)
	}
	port = p4.LocalAddr().(*net.UDPAddr).Port
	p6, err := net.ListenPacket("udp6", "[::]:"+strconv.Itoa(port))
	if err != nil {
		_ = p4.Close()
		t.Fatalf("reserve udp6 on port %d, which the kernel had just handed out for "+
			"udp4: another socket holds it on IPv6 only: %v", port, err)
	}
	var once sync.Once
	release = func() {
		once.Do(func() {
			_ = p4.Close()
			_ = p6.Close()
		})
	}
	t.Cleanup(release)
	return port, release
}

// The end-to-end proof, and the case issue #28 is actually about: an IPv4
// publisher reaching a WILDCARD listener.
//
// Before the split bind this failed on darwin and passed on linux, with the
// publisher seeing only an I/O error and this server logging nothing -- because
// gosrt's reply was discarded inside the dependency and handleConnect was never
// reached. It runs on every platform because the fix has to hold on every
// platform, and because a guard that only executes where the bug was is a guard
// nobody runs.
func TestAWildcardListenerAcceptsAnIPv4Publisher(t *testing.T) {
	port, release := heldWildcardPort(t)
	tg := Target{SourceID: 7, Name: "horizontal", Enabled: true, Sink: &recorder{}}
	lookup := ConstantTimeLookup(
		func() []Target { return []Target{tg} },
		func(Target) []string { return []string{tokenFor(tg)} },
	)

	s := New(quietLog(), fmt.Sprintf(":%d", port), lookup)
	release()
	if err := s.Start(); err != nil {
		t.Fatalf("Start on a wildcard address: %v", err)
	}
	t.Cleanup(s.Stop)

	// 127.0.0.1 specifically: an IPv4 peer is the half that was broken.
	conn, err := dial(t, fmt.Sprintf("127.0.0.1:%d", port), tokenFor(tg))
	if err != nil {
		t.Fatalf("an IPv4 publisher could not reach a wildcard listener: %v\n"+
			"This is issue #28. gosrt replies to a v4-mapped peer through an IPv4 "+
			"control message on an AF_INET6 socket, which Darwin refuses, and the "+
			"error is discarded -- so the handshake never completes and nothing is "+
			"logged on either side.", err)
	}
	conn.Close()
}
