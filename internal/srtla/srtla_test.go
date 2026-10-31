package srtla

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"log/slog"
	"net"
	"net/netip"
	"sync"
	"testing"
	"time"
)

// Wire-level tests: a fake sender (one plain UDP socket per simulated link)
// and a fake upstream (a UDP socket standing in for the SRT listener), so
// every byte the receiver emits is observed exactly.

// syncBuf is a goroutine-safe log sink; slog handlers write from whichever
// goroutine logs, and -race would rightly flag a bare bytes.Buffer.
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (w *syncBuf) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.Write(p)
}

func (w *syncBuf) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.String()
}

type harness struct {
	t    *testing.T
	srv  *Server
	up   *net.UDPConn // the fake SRT listener
	logs *syncBuf
}

// newHarness starts a receiver on loopback with a fake upstream. tweak runs
// before Start, which is where tests shrink timeouts and limits.
func newHarness(t *testing.T, tweak func(*Server)) *harness {
	t.Helper()
	up, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { up.Close() })
	logs := &syncBuf{}
	log := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	s := New(log, Config{Listen: "127.0.0.1:0", Upstream: up.LocalAddr().String()})
	// Generous enough that ordinary tests never trip the rate limit by
	// registering several groups from 127.0.0.1.
	s.reg1Burst = 100
	if tweak != nil {
		tweak(s)
	}
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Stop)
	return &harness{t: t, srv: s, up: up, logs: logs}
}

// newLink is one simulated uplink: its own socket, so its own source port.
func (h *harness) newLink() *net.UDPConn {
	h.t.Helper()
	c, err := net.DialUDP("udp", nil, h.srv.Addr().(*net.UDPAddr))
	if err != nil {
		h.t.Fatal(err)
	}
	h.t.Cleanup(func() { c.Close() })
	return c
}

func send(t *testing.T, c *net.UDPConn, b []byte) {
	t.Helper()
	if _, err := c.Write(b); err != nil {
		t.Fatal(err)
	}
}

// recv reads one datagram or fails the test.
func recv(t *testing.T, c *net.UDPConn) []byte {
	t.Helper()
	buf := make([]byte, 2048)
	c.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, err := c.Read(buf)
	if err != nil {
		t.Fatalf("expected a datagram: %v", err)
	}
	return buf[:n]
}

// expectSilence asserts nothing arrives on c for a short while.
func expectSilence(t *testing.T, c *net.UDPConn, what string) {
	t.Helper()
	buf := make([]byte, 2048)
	c.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	if n, err := c.Read(buf); err == nil {
		t.Fatalf("%s: expected no datagram, got %d bytes % x", what, n, buf[:min(n, 8)])
	}
}

// upRecv reads one datagram at the fake upstream and the address it came from.
func (h *harness) upRecv() ([]byte, *net.UDPAddr) {
	h.t.Helper()
	buf := make([]byte, 2048)
	h.up.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, from, err := h.up.ReadFromUDP(buf)
	if err != nil {
		h.t.Fatalf("expected a datagram upstream: %v", err)
	}
	return buf[:n], from
}

func randomID(_ *testing.T) *[idLen]byte {
	var id [idLen]byte
	rand.Read(id[:])
	return &id
}

// reg1 sends a REG1 on c and returns the group ID from the REG2 reply.
func reg1(t *testing.T, c *net.UDPConn, id *[idLen]byte) [idLen]byte {
	t.Helper()
	send(t, c, regPacket(typeREG1, id))
	r := recv(t, c)
	if len(r) != regLen || packetType(r) != typeREG2 {
		t.Fatalf("REG1 answered with type %#04x len %d, want REG2 len %d", packetType(r), len(r), regLen)
	}
	var gid [idLen]byte
	copy(gid[:], r[2:])
	return gid
}

func reg2(t *testing.T, c *net.UDPConn, gid *[idLen]byte) uint16 {
	t.Helper()
	send(t, c, regPacket(typeREG2, gid))
	r := recv(t, c)
	if len(r) != 2 {
		t.Fatalf("REG2 answered with %d bytes, want 2", len(r))
	}
	return packetType(r)
}

// register makes a group with REG1 on the first link and joins every link.
func (h *harness) register(links ...*net.UDPConn) [idLen]byte {
	h.t.Helper()
	gid := reg1(h.t, links[0], randomID(h.t))
	for _, c := range links {
		if got := reg2(h.t, c, &gid); got != typeREG3 {
			h.t.Fatalf("REG2 answered %#04x, want REG3", got)
		}
	}
	return gid
}

// dataPkt is an SRT data packet: high bit clear, sequence number first.
func dataPkt(seq uint32, payload string) []byte {
	b := make([]byte, srtHeaderLen, srtHeaderLen+len(payload))
	binary.BigEndian.PutUint32(b, seq&0x7fffffff)
	return append(b, payload...)
}

// ctrlPkt is an SRT control packet of the given type.
func ctrlPkt(typ uint16, payload string) []byte {
	b := make([]byte, srtHeaderLen, srtHeaderLen+len(payload))
	binary.BigEndian.PutUint16(b, typ)
	return append(b, payload...)
}

func TestREG1IsAnsweredWithREG2KeepingTheSendersHalfAndAFreshReceiverHalfPerGroup(t *testing.T) {
	h := newHarness(t, nil)
	a, b := h.newLink(), h.newLink()
	idA, idB := randomID(t), randomID(t)
	copy(idB[halfID:], idA[halfID:]) // same proposed second half: the receiver must ignore it
	gA, gB := reg1(t, a, idA), reg1(t, b, idB)

	if !bytes.Equal(gA[:halfID], idA[:halfID]) || !bytes.Equal(gB[:halfID], idB[:halfID]) {
		t.Fatal("the sender's half of the group ID was not preserved")
	}
	if bytes.Equal(gA[halfID:], idA[halfID:]) {
		t.Fatal("the receiver's half was copied from the sender rather than generated")
	}
	if bytes.Equal(gA[halfID:], gB[halfID:]) {
		t.Fatal("two groups got the same receiver half")
	}
	if n := len(h.srv.Stats()); n != 2 {
		t.Fatalf("Stats shows %d groups, want 2", n)
	}
}

func TestREG1ReusingAnExistingGroupsSenderHalfIsRefusedRatherThanGivenTheGroup(t *testing.T) {
	h := newHarness(t, nil)
	a, b := h.newLink(), h.newLink()
	id := randomID(t)
	reg1(t, a, id)
	send(t, b, regPacket(typeREG1, id))
	if r := recv(t, b); packetType(r) != typeRegErr || len(r) != 2 {
		t.Fatalf("replayed REG1 answered % x, want REG_ERR", r)
	}
}

func TestREG2WithAnUnknownGroupIDIsAnsweredWithREGNGP(t *testing.T) {
	h := newHarness(t, nil)
	c := h.newLink()
	if got := reg2(t, c, randomID(t)); got != typeRegNGP {
		t.Fatalf("got %#04x, want REG_NGP", got)
	}
}

func TestREG2WithTheGroupIDRegistersTheLinkAndADuplicateREG2IsAnsweredREG3Again(t *testing.T) {
	h := newHarness(t, nil)
	c := h.newLink()
	gid := reg1(t, c, randomID(t))
	for i := range 3 {
		if got := reg2(t, c, &gid); got != typeREG3 {
			t.Fatalf("REG2 #%d answered %#04x, want REG3", i+1, got)
		}
	}
	st := h.srv.Stats()
	if len(st) != 1 || len(st[0].Links) != 1 {
		t.Fatalf("duplicate REG2 should not add links: %+v", st)
	}
	if st[0].Links[0].Peer != c.LocalAddr().String() {
		t.Fatalf("link peer %q, want %q", st[0].Links[0].Peer, c.LocalAddr())
	}
}

func TestANinthLinkIsRefusedWithREGERRAndTheGroupKeepsEight(t *testing.T) {
	h := newHarness(t, nil)
	links := make([]*net.UDPConn, 9)
	for i := range links {
		links[i] = h.newLink()
	}
	gid := h.register(links[:8]...)
	if got := reg2(t, links[8], &gid); got != typeRegErr {
		t.Fatalf("9th link answered %#04x, want REG_ERR", got)
	}
	if n := len(h.srv.Stats()[0].Links); n != 8 {
		t.Fatalf("group has %d links, want 8", n)
	}
}

func TestAnAddressAlreadyLinkedToOneGroupCannotJoinOrCreateAnother(t *testing.T) {
	h := newHarness(t, nil)
	a, b := h.newLink(), h.newLink()
	h.register(a)
	gB := h.register(b)
	if got := reg2(t, a, &gB); got != typeRegErr {
		t.Fatalf("REG2 to a second group answered %#04x, want REG_ERR", got)
	}
	send(t, a, regPacket(typeREG1, randomID(t)))
	if r := recv(t, a); packetType(r) != typeRegErr {
		t.Fatalf("REG1 from a registered link answered % x, want REG_ERR", r)
	}
}

func TestKeepaliveIsEchoedByteForByteOnlyToRegisteredLinks(t *testing.T) {
	h := newHarness(t, nil)
	reg, stranger := h.newLink(), h.newLink()
	h.register(reg)
	ka := append(ctrl(typeKeepalive), 1, 2, 3, 4, 5, 6, 7, 8, 0xff)
	send(t, reg, ka)
	if got := recv(t, reg); !bytes.Equal(got, ka) {
		t.Fatalf("echo % x, want % x", got, ka)
	}
	send(t, stranger, ka)
	expectSilence(t, stranger, "keepalive from an unregistered address")
}

func TestDatagramsFromUnregisteredAddressesAreDroppedWithoutReplyOrForwarding(t *testing.T) {
	h := newHarness(t, nil)
	stranger := h.newLink()
	send(t, stranger, dataPkt(1, "hello"))
	send(t, stranger, ctrlPkt(0x8000, "handshake"))
	expectSilence(t, stranger, "data from an unregistered address")
	h.up.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	if n, _, err := h.up.ReadFromUDP(make([]byte, 2048)); err == nil {
		t.Fatalf("unregistered data reached upstream (%d bytes)", n)
	}
}

func TestDataIsForwardedUnchangedAndEveryLinkOfAGroupSharesOneUpstreamSourcePort(t *testing.T) {
	h := newHarness(t, nil)
	a1, a2, b1 := h.newLink(), h.newLink(), h.newLink()
	h.register(a1, a2)
	h.register(b1)

	var froms []string
	for i, c := range []*net.UDPConn{a1, a2, b1} {
		pkt := dataPkt(uint32(100+i), "payload-bytes")
		send(t, c, pkt)
		got, from := h.upRecv()
		if !bytes.Equal(got, pkt) {
			t.Fatalf("link %d: forwarded % x, want % x", i, got, pkt)
		}
		froms = append(froms, from.String())
	}
	if froms[0] != froms[1] {
		t.Fatalf("two links of one group reached upstream from %s and %s; want one peer", froms[0], froms[1])
	}
	if froms[0] == froms[2] {
		t.Fatal("two groups share an upstream socket")
	}
	// And Stats names that socket, which is how the parent joins a group to
	// an SRT session.
	found := false
	for _, g := range h.srv.Stats() {
		found = found || g.UpstreamLocal == froms[0]
	}
	if !found {
		t.Fatalf("no group's UpstreamLocal is %s", froms[0])
	}
}

func TestAnUpstreamSRTACKGoesToEveryLinkAndAnythingElseOnlyToTheLastActiveLink(t *testing.T) {
	h := newHarness(t, nil)
	a, b, c := h.newLink(), h.newLink(), h.newLink()
	h.register(a, b, c)
	// b carries data last, so it is the last active link.
	send(t, a, dataPkt(1, "x"))
	_, group := h.upRecv()
	send(t, b, dataPkt(2, "y"))
	h.upRecv()

	ack := ctrlPkt(srtControlACK, "ack-body")
	h.up.WriteToUDP(ack, group)
	for i, l := range []*net.UDPConn{a, b, c} {
		if got := recv(t, l); !bytes.Equal(got, ack) {
			t.Fatalf("link %d got % x, want the SRT ACK", i, got)
		}
	}

	nak := ctrlPkt(0x8003, "nak-body")
	h.up.WriteToUDP(nak, group)
	if got := recv(t, b); !bytes.Equal(got, nak) {
		t.Fatalf("last active link got % x, want the NAK", got)
	}
	expectSilence(t, a, "non-ACK on a link that is not last active")
	expectSilence(t, c, "non-ACK on a link that is not last active")
}

func TestAnSRTLAACKFollowsExactlyTenDataPacketsOnALinkListingThemInOrderAndIgnoresControlPackets(t *testing.T) {
	h := newHarness(t, nil)
	a, b := h.newLink(), h.newLink()
	h.register(a, b)
	seqs := []uint32{7, 3, 1000, 1001, 5, 0x7fffffff, 42, 43, 44, 9}
	for i, s := range seqs {
		send(t, a, dataPkt(s, "d"))
		send(t, b, dataPkt(s+1, "other link")) // b's packets count for b, not a
		if i%3 == 0 {
			send(t, a, ctrlPkt(0x8001, "srt keepalive")) // control: not counted
		}
		if i == 8 {
			expectSilence(t, a, "SRTLA ACK before the tenth data packet")
		}
	}
	got := recv(t, a)
	want := ackPacket(seqs)
	if len(got) != ackLen || !bytes.Equal(got, want) {
		t.Fatalf("SRTLA ACK\n got % x\nwant % x", got, want)
	}
	if !bytes.Equal(got[:4], []byte{0x91, 0, 0, 0}) {
		t.Fatalf("ACK header % x", got[:4])
	}
	// b sent ten too, so b has its own ACK, with its own numbers.
	gotB := recv(t, b)
	if s := binary.BigEndian.Uint32(gotB[4:]); s != 8 {
		t.Fatalf("b's ACK starts with seq %d, want 8", s)
	}
	expectSilence(t, a, "a second SRTLA ACK without ten more packets")
}

func TestASilentLinkIsReapedAndAGroupLeftEmptyIsRemovedWithItsUpstreamSocket(t *testing.T) {
	h := newHarness(t, func(s *Server) {
		s.tick = 20 * time.Millisecond
		s.linkTimeout = 300 * time.Millisecond
		s.groupTimeout = 300 * time.Millisecond
	})
	alive, quiet := h.newLink(), h.newLink()
	gid := h.register(alive, quiet)
	upLocal := h.srv.Stats()[0].UpstreamLocal

	// Keep one link talking for longer than the link timeout.
	for range 12 {
		send(t, alive, ctrl(typeKeepalive))
		recv(t, alive)
		time.Sleep(50 * time.Millisecond)
	}
	st := h.srv.Stats()
	if len(st) != 1 || len(st[0].Links) != 1 || st[0].Links[0].Peer != alive.LocalAddr().String() {
		t.Fatalf("after the quiet link's timeout: %+v", st)
	}
	// The reaped link is a stranger again.
	send(t, quiet, ctrl(typeKeepalive))
	expectSilence(t, quiet, "keepalive from a reaped link")

	// Now let everything go quiet: the link goes, then the group.
	waitFor(t, 3*time.Second, func() bool { return len(h.srv.Stats()) == 0 })
	if got := reg2(t, alive, &gid); got != typeRegNGP {
		t.Fatalf("REG2 to a removed group answered %#04x, want REG_NGP", got)
	}
	// Its upstream socket is closed: the port can be bound again.
	if c, err := net.ListenPacket("udp", upLocal); err != nil {
		t.Fatalf("group's upstream socket %s still bound: %v", upLocal, err)
	} else {
		c.Close()
	}
}

func TestAGroupCreatedByREG1ThatNeverGetsAREG2IsRemoved(t *testing.T) {
	h := newHarness(t, func(s *Server) {
		s.tick = 20 * time.Millisecond
		s.groupTimeout = 200 * time.Millisecond
	})
	reg1(t, h.newLink(), randomID(t))
	if len(h.srv.Stats()) != 1 {
		t.Fatal("REG1 made no group")
	}
	waitFor(t, 3*time.Second, func() bool { return len(h.srv.Stats()) == 0 })
}

func TestTheGroupLimitRefusesFurtherREG1WithREGERR(t *testing.T) {
	h := newHarness(t, func(s *Server) { s.maxGroups = 2 })
	reg1(t, h.newLink(), randomID(t))
	reg1(t, h.newLink(), randomID(t))
	c := h.newLink()
	send(t, c, regPacket(typeREG1, randomID(t)))
	if r := recv(t, c); packetType(r) != typeRegErr || len(r) != 2 {
		t.Fatalf("third REG1 answered % x, want REG_ERR", r)
	}
	if n := len(h.srv.Stats()); n != 2 {
		t.Fatalf("%d groups, want 2", n)
	}
}

func TestREG1IsRateLimitedPerSourceIPAndRefusedSilently(t *testing.T) {
	h := newHarness(t, func(s *Server) {
		s.reg1Burst = 3
		s.reg1Rate = 0.001
	})
	for range 3 {
		reg1(t, h.newLink(), randomID(t)) // different ports, same IP
	}
	c := h.newLink()
	send(t, c, regPacket(typeREG1, randomID(t)))
	expectSilence(t, c, "REG1 over the rate limit")
	if n := len(h.srv.Stats()); n != 3 {
		t.Fatalf("%d groups, want 3", n)
	}
}

func TestTheRateLimiterRefusesNewIPsOnceItsTableIsFull(t *testing.T) {
	s := New(nil, Config{})
	s.reg1MaxIPs = 2
	now := time.Now()
	for _, ip := range []string{"192.0.2.1", "192.0.2.2"} {
		if !s.allowREG1(netip.MustParseAddr(ip), now) {
			t.Fatalf("%s refused below the table limit", ip)
		}
	}
	if s.allowREG1(netip.MustParseAddr("192.0.2.3"), now) {
		t.Fatal("a new IP was admitted to a full table")
	}
	// Once idle buckets refill, housekeeping prunes them and makes room.
	s.sweep(now.Add(time.Hour))
	if !s.allowREG1(netip.MustParseAddr("192.0.2.3"), now.Add(time.Hour)) {
		t.Fatal("pruning did not make room")
	}
}

func TestMalformedAndOversizedDatagramsAreIgnored(t *testing.T) {
	h := newHarness(t, nil)
	c := h.newLink()
	id := randomID(t)
	short := regPacket(typeREG1, id)[:regLen-1]
	long := append(regPacket(typeREG1, id), 0)
	send(t, c, short)
	send(t, c, long)
	send(t, c, regPacket(typeREG2, id)[:100])
	send(t, c, []byte{0x92})
	expectSilence(t, c, "malformed REG packets")
	if n := len(h.srv.Stats()); n != 0 {
		t.Fatalf("malformed REG1 made %d groups", n)
	}

	// A registered link's datagram over the MTU is dropped, not truncated
	// and forwarded.
	h.register(c)
	send(t, c, dataPkt(1, string(make([]byte, mtu))))
	h.up.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	if n, _, err := h.up.ReadFromUDP(make([]byte, 4096)); err == nil {
		t.Fatalf("oversized datagram forwarded (%d bytes)", n)
	}
}

func TestStatsReportsPerLinkCountersBitrateAndShare(t *testing.T) {
	// The housekeeping ticker is parked and the window closed by hand, so
	// the window holds exactly the burst below: a tick landing mid-burst on
	// a loaded machine would otherwise report a partial window.
	h := newHarness(t, func(s *Server) { s.tick = time.Hour })
	a, b := h.newLink(), h.newLink()
	h.register(a, b)
	payload := string(make([]byte, 984)) // 1000-byte datagrams
	// Sent in rounds, each counted before the next: 160 KB of loopback UDP
	// in one burst overflows a small default receive buffer (Windows), and
	// a dropped datagram is never coming, however long the wait.
	for i := range 40 {
		send(t, a, dataPkt(1, payload))
		send(t, a, dataPkt(2, payload))
		send(t, a, dataPkt(3, payload))
		send(t, b, dataPkt(4, payload))
		wantA, wantB := uint64(3*(i+1)), uint64(i+1)
		waitFor(t, 3*time.Second, func() bool {
			st := h.srv.Stats()
			return st[0].Links[0].Packets >= wantA && st[0].Links[1].Packets >= wantB
		})
	}
	h.srv.sweep(time.Now().Add(h.srv.window))
	st := h.srv.Stats()
	la, lb := st[0].Links[0], st[0].Links[1]
	if la.Packets < 120 || lb.Packets < 40 || la.Bytes < 120000 {
		t.Fatalf("counters: a=%+v b=%+v", la, lb)
	}
	if la.Share < 0.6 || la.Share > 0.9 || lb.Share < 0.1 || lb.Share > 0.4 {
		t.Fatalf("shares a=%.2f b=%.2f, want about 0.75/0.25", la.Share, lb.Share)
	}
	if s := la.Share + lb.Share; s < 0.999 || s > 1.001 {
		t.Fatalf("shares sum to %f", s)
	}
	if la.BitrateKbps <= 0 || la.BitrateKbps <= lb.BitrateKbps {
		t.Fatalf("bitrates a=%.1f b=%.1f", la.BitrateKbps, lb.BitrateKbps)
	}
	if la.LastSeen.Before(la.Since) || la.Since.IsZero() {
		t.Fatalf("times: %+v", la)
	}
}

func TestStopIsIdempotentStartTwiceFailsAndABusyPortFailsStart(t *testing.T) {
	h := newHarness(t, nil)
	if err := h.srv.Start(); err == nil {
		t.Fatal("second Start succeeded")
	}
	busy := New(nil, Config{Listen: h.srv.Addr().String(), Upstream: "127.0.0.1:1"})
	if err := busy.Start(); err == nil {
		busy.Stop()
		t.Fatal("Start on a bound port succeeded")
	}
	busy.Stop()
	New(nil, Config{}).Stop() // Stop before Start
	h.srv.Stop()
	h.srv.Stop()
	if h.srv.Start() == nil {
		t.Fatal("Start after Stop succeeded")
	}
}

func TestAWildcardListenServesIPv4SendersOnTheDualStackSocket(t *testing.T) {
	h := newHarness(t, func(s *Server) { s.cfg.Listen = ":0" })
	port := h.srv.Addr().(*net.UDPAddr).Port
	c, err := net.DialUDP("udp", nil, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: port})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	h.register(c)
	if p := h.srv.Stats()[0].Links[0].Peer; p != c.LocalAddr().String() {
		t.Fatalf("peer recorded as %q, want the unmapped %q", p, c.LocalAddr())
	}
	ka := append(ctrl(typeKeepalive), 9)
	send(t, c, ka)
	if got := recv(t, c); !bytes.Equal(got, ka) {
		t.Fatalf("echo % x", got)
	}
}

func waitFor(t *testing.T, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met in time")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// The SRT listener sees a bonded publisher as a loopback peer. SenderHost maps
// that peer back to the address that created the group, so the listener's
// wrong-token lockout lands on the sender doing the guessing rather than on
// loopback -- which would lock out every bonded encoder at once.
func TestSenderHostMapsTheUpstreamSocketBackToTheGroupsCreator(t *testing.T) {
	h := newHarness(t, nil)
	a := h.newLink()
	h.register(a)
	stats := h.srv.Stats()
	if len(stats) != 1 {
		t.Fatalf("Stats has %d groups, want 1", len(stats))
	}
	host, ok := h.srv.SenderHost(stats[0].UpstreamLocal)
	if !ok {
		t.Fatal("SenderHost did not recognise the group's own upstream address")
	}
	want := a.LocalAddr().(*net.UDPAddr).IP.String()
	if host != want {
		t.Errorf("SenderHost = %q, want the creator %q", host, want)
	}
	if _, ok := h.srv.SenderHost("127.0.0.1:1"); ok {
		t.Error("SenderHost answered for an address no group owns")
	}
}
