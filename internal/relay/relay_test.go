package relay

import (
	"bytes"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sort"
	"testing"
	"time"
)

// readWait bounds every receive in this file so a fan-out regression fails the
// test instead of hanging it.
const readWait = 500 * time.Millisecond

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func newTestHub(t *testing.T) *Hub {
	t.Helper()
	h, err := New(testLogger(), 0)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = h.Close() })
	return h
}

// boundSubscriber stands in for a consumer process: it binds its own loopback
// port, exactly as FFmpeg does when handed a udp:// input.
func boundSubscriber(t *testing.T) (*net.UDPConn, int) {
	t.Helper()
	c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatalf("bind subscriber: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c, c.LocalAddr().(*net.UDPAddr).Port
}

// unboundPort returns a port number that nothing is listening on.
func unboundPort(t *testing.T) int {
	t.Helper()
	c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatalf("probe port: %v", err)
	}
	p := c.LocalAddr().(*net.UDPAddr).Port
	_ = c.Close()
	return p
}

func publish(t *testing.T, h *Hub, payload []byte, times int) {
	t.Helper()
	c, err := net.DialUDP("udp4", nil, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: h.Port()})
	if err != nil {
		t.Fatalf("dial hub input: %v", err)
	}
	defer c.Close()
	for i := 0; i < times; i++ {
		if _, err := c.Write(payload); err != nil {
			t.Fatalf("publish %d: %v", i, err)
		}
	}
}

// receive drains up to want datagrams, returning early at the deadline.
func receive(t *testing.T, c *net.UDPConn, want int) [][]byte {
	t.Helper()
	return receiveWithin(t, c, want, readWait)
}

// receiveWithin exists for the "nothing should arrive" assertions, which would
// otherwise pay the full readWait for a result they expect to be empty.
func receiveWithin(t *testing.T, c *net.UDPConn, want int, d time.Duration) [][]byte {
	t.Helper()
	if err := c.SetReadDeadline(time.Now().Add(d)); err != nil {
		t.Fatalf("set deadline: %v", err)
	}
	var got [][]byte
	buf := make([]byte, datagramSize)
	for len(got) < want {
		n, err := c.Read(buf)
		if err != nil {
			break
		}
		got = append(got, append([]byte(nil), buf[:n]...))
	}
	return got
}

// waitForRx polls until the hub has accounted for want datagrams.
func waitForRx(t *testing.T, h *Hub, want uint64) {
	t.Helper()
	deadline := time.Now().Add(readWait)
	for time.Now().Before(deadline) {
		if h.Stats().RxPackets >= want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("hub received %d datagrams, want %d", h.Stats().RxPackets, want)
}

// assertDelivered requires that at least one copy reached the subscriber and
// that every copy that did arrive is byte-identical to what was published.
func assertDelivered(t *testing.T, name string, c *net.UDPConn, payload []byte, sends int) {
	t.Helper()
	got := receive(t, c, sends)
	if len(got) == 0 {
		t.Fatalf("subscriber %s received nothing", name)
	}
	for i, pkt := range got {
		if !bytes.Equal(pkt, payload) {
			t.Errorf("subscriber %s datagram %d = %d bytes %x, want %d bytes %x",
				name, i, len(pkt), pkt, len(payload), payload)
		}
	}
}

// tsDatagram is a realistic payload: seven 188-byte TS packets.
func tsDatagram(seed byte) []byte {
	p := make([]byte, 7*188)
	for i := range p {
		if i%188 == 0 {
			p[i] = 0x47
			continue
		}
		p[i] = seed + byte(i)
	}
	return p
}

// ------------------------------------------------------------------ binding

func TestNewBindsLoopbackPort(t *testing.T) {
	h := newTestHub(t)

	if h.Port() == 0 {
		t.Fatal("port 0 should have been resolved to a concrete port")
	}
	if want := fmt.Sprintf("udp://127.0.0.1:%d", h.Port()); h.InputURL() != want {
		t.Errorf("InputURL() = %q, want %q", h.InputURL(), want)
	}
	if got := h.conn.LocalAddr().(*net.UDPAddr).IP.String(); got != "127.0.0.1" {
		t.Errorf("hub bound %s, want loopback only", got)
	}
}

func TestNewRejectsAnAlreadyBoundPort(t *testing.T) {
	occupied, port := boundSubscriber(t)
	defer occupied.Close()

	h, err := New(testLogger(), port)
	if err == nil {
		_ = h.Close()
		t.Fatal("New succeeded on an occupied port, want error")
	}
}

// -------------------------------------------------------------- bookkeeping

func TestSubscriberBookkeeping(t *testing.T) {
	tests := []struct {
		name string
		act  func(h *Hub)
		want []string
	}{
		{
			name: "a fresh hub has no subscribers",
			act:  func(h *Hub) {},
			want: []string{},
		},
		{
			name: "subscribe registers by name",
			act: func(h *Hub) {
				h.Subscribe("rec", 5001)
				h.Subscribe("hls", 5002)
			},
			want: []string{"hls", "rec"},
		},
		{
			name: "re-subscribing the same name replaces rather than duplicates",
			act: func(h *Hub) {
				h.Subscribe("rec", 5001)
				h.Subscribe("rec", 5009)
			},
			want: []string{"rec"},
		},
		{
			name: "unsubscribe removes only the named consumer",
			act: func(h *Hub) {
				h.Subscribe("rec", 5001)
				h.Subscribe("hls", 5002)
				h.Unsubscribe("rec")
			},
			want: []string{"hls"},
		},
		{
			name: "unsubscribing an unknown name is a no-op",
			act: func(h *Hub) {
				h.Subscribe("hls", 5002)
				h.Unsubscribe("nobody")
			},
			want: []string{"hls"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newTestHub(t)
			tt.act(h)

			got := h.Subscribers()
			sort.Strings(got)
			if len(got) != len(tt.want) {
				t.Fatalf("Subscribers() = %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("Subscribers() = %v, want %v", got, tt.want)
				}
			}
		})
	}
}

func TestSubscribeReturnsTheConsumerReadURL(t *testing.T) {
	h := newTestHub(t)

	if got, want := h.Subscribe("rec", 41234), "udp://127.0.0.1:41234"; got != want {
		t.Errorf("Subscribe() = %q, want %q", got, want)
	}
}

// ------------------------------------------------------------------ fan-out

func TestFanoutDeliversEachDatagramToEverySubscriber(t *testing.T) {
	tests := []struct {
		name    string
		payload []byte
	}{
		{name: "short datagram", payload: []byte("hello relay")},
		{name: "full TS datagram of seven 188-byte packets", payload: tsDatagram(0x11)},
		{name: "single byte", payload: []byte{0x47}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newTestHub(t)
			connA, portA := boundSubscriber(t)
			connB, portB := boundSubscriber(t)
			h.Subscribe("a", portA)
			h.Subscribe("b", portB)

			const sends = 3
			publish(t, h, tt.payload, sends)

			assertDelivered(t, "a", connA, tt.payload, sends)
			assertDelivered(t, "b", connB, tt.payload, sends)
		})
	}
}

func TestFanoutSkipsAnUnsubscribedConsumer(t *testing.T) {
	h := newTestHub(t)
	connA, portA := boundSubscriber(t)
	connB, portB := boundSubscriber(t)
	h.Subscribe("a", portA)
	h.Subscribe("b", portB)
	h.Unsubscribe("b")

	payload := []byte("only a")
	publish(t, h, payload, 2)

	if got := receive(t, connA, 2); len(got) == 0 {
		t.Fatal("remaining subscriber received nothing")
	}
	if got := receiveWithin(t, connB, 1, 50*time.Millisecond); len(got) != 0 {
		t.Fatalf("unsubscribed consumer received %d datagrams, want 0", len(got))
	}
}

// An unreachable consumer is the failure the design exists to survive: it must
// not cost the hub, nor the subscribers that are healthy, a single datagram.
func TestDeadSubscriberDoesNotStarveALiveOne(t *testing.T) {
	h := newTestHub(t)
	live, livePort := boundSubscriber(t)
	h.Subscribe("dead", unboundPort(t))
	h.Subscribe("live", livePort)

	payload := tsDatagram(0x22)
	const sends = 5
	publish(t, h, payload, sends)

	assertDelivered(t, "live", live, payload, sends)
	waitForRx(t, h, sends)
}

// ------------------------------------------------------------------- stats

func TestStatsCountsReceiveAndTransmit(t *testing.T) {
	h := newTestHub(t)
	connA, portA := boundSubscriber(t)
	connB, portB := boundSubscriber(t)
	h.Subscribe("a", portA)
	h.Subscribe("b", portB)

	payload := tsDatagram(0x33)
	const sends = 4
	publish(t, h, payload, sends)
	waitForRx(t, h, sends)
	receive(t, connA, sends)
	receive(t, connB, sends)

	s := h.Stats()
	if s.Port != h.Port() {
		t.Errorf("Stats().Port = %d, want %d", s.Port, h.Port())
	}
	if s.RxPackets != sends {
		t.Errorf("RxPackets = %d, want %d", s.RxPackets, sends)
	}
	if want := uint64(sends * len(payload)); s.RxBytes != want {
		t.Errorf("RxBytes = %d, want %d", s.RxBytes, want)
	}
	if want := uint64(sends * 2); s.TxPackets != want {
		t.Errorf("TxPackets = %d, want %d (one send per subscriber per datagram)", s.TxPackets, want)
	}
	if s.Dropped != 0 {
		t.Errorf("Dropped = %d, want 0 with two healthy subscribers", s.Dropped)
	}
	if len(s.Subscribers) != 2 {
		t.Errorf("Stats().Subscribers = %v, want 2 entries", s.Subscribers)
	}
	if h.RxBytes() != s.RxBytes {
		t.Errorf("RxBytes() = %d, want %d", h.RxBytes(), s.RxBytes)
	}
}

func TestStatsStartAtZero(t *testing.T) {
	s := newTestHub(t).Stats()

	if s.RxPackets != 0 || s.RxBytes != 0 || s.TxPackets != 0 || s.Dropped != 0 {
		t.Errorf("fresh hub stats = %+v, want all counters zero", s)
	}
}

// -------------------------------------------------------------------- close

func TestCloseIsIdempotent(t *testing.T) {
	h, err := New(testLogger(), 0)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if err := h.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := h.Close(); err != nil {
		t.Fatalf("second Close: %v, want nil", err)
	}
}

func TestCloseStopsTheReader(t *testing.T) {
	h, err := New(testLogger(), 0)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	sub, subPort := boundSubscriber(t)
	h.Subscribe("a", subPort)
	port := h.Port()

	publish(t, h, []byte("before close"), 1)
	waitForRx(t, h, 1)
	receive(t, sub, 1)
	if err := h.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	before := h.Stats()

	// The socket is gone, so this goes nowhere; a reader still running would
	// otherwise spin on the closed connection and keep counting.
	c, err := net.DialUDP("udp4", nil, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: port})
	if err == nil {
		_, _ = c.Write([]byte("after close"))
		_ = c.Close()
	}
	time.Sleep(50 * time.Millisecond)

	if after := h.Stats(); after.RxPackets != before.RxPackets || after.TxPackets != before.TxPackets {
		t.Errorf("counters moved after Close: %+v -> %+v", before, after)
	}
	if got := receiveWithin(t, sub, 1, 50*time.Millisecond); len(got) != 0 {
		t.Errorf("subscriber received %d datagrams after Close, want 0", len(got))
	}
}

// ----------------------------------------------------------- port allocator

// freeBase finds a base with span consecutive unused loopback UDP ports.
func freeBase(t *testing.T, span int) int {
	t.Helper()
	for attempt := 0; attempt < 50; attempt++ {
		base := unboundPort(t)
		ok := true
		for p := base; p < base+span; p++ {
			if !portFree(p) {
				ok = false
				break
			}
		}
		if ok {
			return base
		}
	}
	t.Skipf("no run of %d free loopback UDP ports available", span)
	return 0
}

func TestPortAllocatorHandsOutDistinctFreePorts(t *testing.T) {
	base := freeBase(t, 2)
	a := NewPortAllocator(base, 2)

	first, err := a.Allocate()
	if err != nil {
		t.Fatalf("first Allocate: %v", err)
	}
	second, err := a.Allocate()
	if err != nil {
		t.Fatalf("second Allocate: %v", err)
	}
	if first == second {
		t.Fatalf("Allocate returned %d twice", first)
	}
	for _, p := range []int{first, second} {
		if p < base || p >= base+2 {
			t.Errorf("Allocate returned %d, outside range [%d,%d)", p, base, base+2)
		}
	}
}

func TestPortAllocatorErrorsWhenRangeIsExhausted(t *testing.T) {
	tests := []struct {
		name string
		span int
	}{
		{name: "span of one", span: 1},
		{name: "span of two", span: 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			base := freeBase(t, tt.span)
			a := NewPortAllocator(base, tt.span)

			for i := 0; i < tt.span; i++ {
				if _, err := a.Allocate(); err != nil {
					t.Fatalf("Allocate %d of %d: %v", i+1, tt.span, err)
				}
			}
			if p, err := a.Allocate(); err == nil {
				t.Fatalf("Allocate returned %d past the end of the range, want error", p)
			}
		})
	}
}

func TestPortAllocatorReusesAReleasedPort(t *testing.T) {
	base := freeBase(t, 1)
	a := NewPortAllocator(base, 1)

	p, err := a.Allocate()
	if err != nil {
		t.Fatalf("Allocate: %v", err)
	}
	if _, err := a.Allocate(); err == nil {
		t.Fatal("second Allocate succeeded on an exhausted range, want error")
	}

	a.Release(p)

	got, err := a.Allocate()
	if err != nil {
		t.Fatalf("Allocate after Release: %v", err)
	}
	if got != p {
		t.Errorf("Allocate after Release = %d, want the released port %d", got, p)
	}
}

func TestPortAllocatorSkipsAPortSomethingElseIsUsing(t *testing.T) {
	base := freeBase(t, 2)
	squatter, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: base})
	if err != nil {
		t.Skipf("could not occupy port %d: %v", base, err)
	}
	defer squatter.Close()

	a := NewPortAllocator(base, 2)

	got, err := a.Allocate()
	if err != nil {
		t.Fatalf("Allocate: %v", err)
	}
	if got != base+1 {
		t.Errorf("Allocate = %d, want %d (port %d is in use)", got, base+1, base)
	}
}
