package relay

import (
	"bytes"
	"log/slog"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// THE HOP ITSELF: does a subscriber receive exactly what the hub received? #674
//
// Everything either side of this has been cleared by measurement. The ingest
// writes all three AAC tracks to UDP; a reader with a destination's own options,
// filtergraph and encoder resolves them even when it joins mid-stream; and the
// hub reports dropped=0 with 0.031% loss on a nine-subscriber run. Yet a real
// destination reading a real hub gets 0 audio packets.
//
// The one link never measured is this one: bytes in versus bytes out, over the
// fan-out, byte for byte. fanout() writes the datagram it was handed, so they
// should be identical -- and "should be" is what this whole investigation has
// been wrong about six times.
func TestASubscriberReceivesEveryByteTheHubReceived(t *testing.T) {
	h, err := New(slog.New(slog.DiscardHandler), 0)
	if err != nil {
		t.Fatalf("hub: %v", err)
	}
	defer h.Close()

	// A subscriber socket, bound before anything is sent: UDP has no backlog.
	sub, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("subscriber socket: %v", err)
	}
	defer sub.Close()
	subPort := sub.LocalAddr().(*net.UDPAddr).Port
	h.Subscribe("test-dest", subPort)

	// Datagrams shaped like the relay's: 1316 bytes, seven 188-byte TS packets,
	// each carrying a recognisable PID so a dropped or reordered one shows.
	const n = 400
	sent := make([][]byte, 0, n)
	for i := 0; i < n; i++ {
		d := make([]byte, 1316)
		for p := 0; p < 7; p++ {
			off := p * 188
			d[off] = 0x47
			pid := 0x100 + (p % 4) // 0x100 video, 0x101-0x103 audio
			d[off+1] = byte(pid >> 8)
			d[off+2] = byte(pid & 0xff)
			d[off+3] = byte(i % 256) // a sequence marker
		}
		sent = append(sent, d)
	}

	got := make(chan []byte, n*2)
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 65536)
		_ = sub.SetReadDeadline(time.Now().Add(5 * time.Second))
		for {
			c, _, rerr := sub.ReadFrom(buf)
			if c > 0 {
				cp := make([]byte, c)
				copy(cp, buf[:c])
				got <- cp
			}
			if rerr != nil {
				return
			}
		}
	}()

	in, err := net.Dial("udp", h.InputURL()[len("udp://"):])
	if err != nil {
		t.Fatalf("dial hub input: %v", err)
	}
	defer in.Close()
	for _, d := range sent {
		if _, werr := in.Write(d); werr != nil {
			t.Fatalf("write to hub: %v", werr)
		}
		time.Sleep(time.Millisecond) // paced, as a real ingest is
	}
	time.Sleep(300 * time.Millisecond)
	_ = sub.SetReadDeadline(time.Now())
	<-done
	close(got)

	var recv [][]byte
	for d := range got {
		recv = append(recv, d)
	}
	if len(recv) != n {
		t.Fatalf("the subscriber received %d of %d datagrams.\n\n"+
			"Hub.dropped only counts WriteToUDP errors; a datagram lost after a "+
			"successful write is invisible to it, and the acceptance rig shows a "+
			"destination reading about a fifth of the stream while the hub reports "+
			"dropped=0. #674.", len(recv), n)
	}
	for i := range recv {
		if len(recv[i]) != len(sent[i]) {
			t.Fatalf("datagram %d changed length across the hop: sent %d, received %d",
				i, len(sent[i]), len(recv[i]))
		}
		for b := range recv[i] {
			if recv[i][b] != sent[i][b] {
				t.Fatalf("datagram %d differs at byte %d: sent 0x%02x, received 0x%02x.\n\n"+
					"fanout() is expected to forward the datagram verbatim. If it does not, "+
					"every consumer downstream is parsing something the capture never showed.",
					i, b, sent[i][b], recv[i][b])
			}
		}
	}
}

// SAMPLING A SILENCE. #674
//
// The first version of this sampler fired every 500 DATAGRAMS, which cannot
// describe a window containing no datagrams: a starved hub simply stopped
// logging, so every sample came from AFTER the starvation ended and the one
// period under investigation was the one with no data about it. Sampling by the
// clock is the whole point, so this asserts it emits while NOTHING is arriving.
func TestTheHubSamplesItsStateWhileNothingIsArriving(t *testing.T) {
	var buf syncBuffer
	h, err := New(slog.New(slog.NewTextHandler(&buf, nil)), 0)
	if err != nil {
		t.Fatalf("hub: %v", err)
	}
	defer h.Close()

	// No sender at all: rxPackets stays 0 for the whole window.
	go h.sampleState(20 * time.Millisecond)
	time.Sleep(200 * time.Millisecond)

	out := buf.String()
	n := strings.Count(out, "relay fanout state")
	if n < 3 {
		t.Fatalf("emitted %d samples in 200ms at a 20ms interval while idle, want at "+
			"least 3.\n\nA sampler that goes quiet exactly when the thing it watches goes "+
			"quiet reports nothing about the only window that matters.", n)
	}
	if !strings.Contains(out, "targets=") || !strings.Contains(out, "rxPackets=") {
		t.Fatalf("a sample carries neither targets nor rxPackets:\n%s\n\n"+
			"Both in ONE line is the point: correlating two separate logs is how two "+
			"wrong conclusions were reached during #674.", out)
	}
}

// The sampler must stop with the hub, or it outlives it and logs for ever.
func TestTheStateSamplerStopsWithTheHub(t *testing.T) {
	var buf syncBuffer
	h, err := New(slog.New(slog.NewTextHandler(&buf, nil)), 0)
	if err != nil {
		t.Fatalf("hub: %v", err)
	}
	go h.sampleState(10 * time.Millisecond)
	time.Sleep(60 * time.Millisecond)
	_ = h.Close()
	time.Sleep(30 * time.Millisecond)
	before := strings.Count(buf.String(), "relay fanout state")
	time.Sleep(120 * time.Millisecond)
	if after := strings.Count(buf.String(), "relay fanout state"); after != before {
		t.Fatalf("the sampler kept logging after Close: %d -> %d samples", before, after)
	}
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// FIRST DELIVERY IS LOGGED ONCE, AND A DEAD CONSUMER IS COUNTED. #674
//
// "relay first delivery" is the line that located the fault: it is the sender's
// own record of when a NAMED consumer first got a byte, which no counter could
// give -- Dropped counts only WriteToUDP errors and txPackets is a total across
// every subscriber, so a consumer receiving everything and one receiving nothing
// are identical in the aggregate. It has to fire exactly once per subscriber, or
// it is unreadable at a relay's packet rate.
func TestFirstDeliveryIsLoggedOncePerSubscriber(t *testing.T) {
	var buf syncBuffer
	h, err := New(slog.New(slog.NewTextHandler(&buf, nil)), 0)
	if err != nil {
		t.Fatalf("hub: %v", err)
	}
	defer h.Close()

	sub, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("subscriber socket: %v", err)
	}
	defer sub.Close()
	h.Subscribe("dest:1", sub.LocalAddr().(*net.UDPAddr).Port)

	pkt := make([]byte, 188)
	pkt[0] = 0x47
	for i := 0; i < 25; i++ {
		h.fanout(pkt)
	}

	if n := strings.Count(buf.String(), "relay first delivery"); n != 1 {
		t.Fatalf("logged first delivery %d times for 25 datagrams, want exactly 1.\n\n"+
			"At a relay's packet rate anything but once is unreadable, and the whole "+
			"value of the line is that it marks the INSTANT a consumer started "+
			"receiving.", n)
	}
	if got := h.Stats().TxPackets; got != 25 {
		t.Fatalf("txPackets = %d after 25 sends, want 25", got)
	}
}

// A consumer that has gone away must be counted, not silently skipped: on
// loopback a departed FFmpeg gives ECONNREFUSED on every write.
func TestASendToADepartedConsumerIsCounted(t *testing.T) {
	h, err := New(slog.New(slog.DiscardHandler), 0)
	if err != nil {
		t.Fatalf("hub: %v", err)
	}
	defer h.Close()

	// Bind and immediately release, so nothing is listening on that port.
	gone, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("port: %v", err)
	}
	port := gone.LocalAddr().(*net.UDPAddr).Port
	_ = gone.Close()
	h.Subscribe("departed", port)

	pkt := make([]byte, 188)
	pkt[0] = 0x47
	for i := 0; i < 10; i++ {
		h.fanout(pkt)
	}
	st := h.Stats()
	if st.Dropped == 0 && st.TxPackets == 0 {
		t.Fatal("ten sends to a departed consumer produced neither a drop nor a tx: " +
			"the counters cannot both be silent, or a hub shedding every send looks " +
			"identical to a healthy one")
	}
}
