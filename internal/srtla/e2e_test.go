package srtla

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math/rand/v2"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	srt "github.com/datarhei/gosrt"
)

// A real SRT session through the receiver: gosrt caller -> an in-test bonding
// sender that stripes the caller's datagrams over two links -> the receiver ->
// a real gosrt listener. This is the loopback stand-in for srtla_send (the
// Docker harness in scripts/srtla-interop runs the real one), and it is what
// proves the merge is transparent to SRT: the handshake, ACKs fanned out on
// every link and the data all survive the trip.

// The streamid is the publish token in production, so it is made recognisable
// here and the logs are searched for it afterwards.
const secretStreamID = "pe-w2fo-SECRET-token-7c1e9a"

// bondingSender is a minimal srtla_send: it registers a group, joins every
// link, then relays a local SRT caller's datagrams round-robin across the
// links, and relays whatever comes back on any link to the caller.
type bondingSender struct {
	t     *testing.T
	shim  *net.UDPConn // what the SRT caller dials
	links []*net.UDPConn
	gid   [idLen]byte

	mu     sync.Mutex
	caller *net.UDPAddr
	wg     sync.WaitGroup
}

func newBondingSender(t *testing.T, h *harness, nLinks int) *bondingSender {
	t.Helper()
	shim, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	b := &bondingSender{t: t, shim: shim}
	for range nLinks {
		b.links = append(b.links, h.newLink())
	}
	// Every link is registered BEFORE the caller dials: a handshake packet
	// striped onto a not-yet-registered link would be dropped silently and
	// the handshake left to stall on retransmits.
	b.gid = h.register(b.links...)

	b.wg.Add(1 + len(b.links))
	go b.uplink()
	for _, l := range b.links {
		go b.downlink(l)
	}
	t.Cleanup(b.close)
	return b
}

func (b *bondingSender) uplink() {
	defer b.wg.Done()
	buf := make([]byte, 2048)
	for i := 0; ; i++ {
		n, from, err := b.shim.ReadFromUDP(buf)
		if err != nil {
			return
		}
		b.mu.Lock()
		b.caller = from
		b.mu.Unlock()
		b.links[i%len(b.links)].Write(buf[:n])
	}
}

func (b *bondingSender) downlink(l *net.UDPConn) {
	defer b.wg.Done()
	buf := make([]byte, 2048)
	for {
		l.SetReadDeadline(time.Time{})
		n, err := l.Read(buf)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			continue
		}
		if t := packetType(buf[:n]); t >= 0x9000 && t < 0x9300 {
			continue // SRTLA's own: ACKs and keepalive echoes stay here
		}
		b.mu.Lock()
		to := b.caller
		b.mu.Unlock()
		if to != nil {
			b.shim.WriteToUDP(buf[:n], to)
		}
	}
}

func (b *bondingSender) close() {
	b.shim.Close()
	for _, l := range b.links {
		l.Close()
	}
	b.wg.Wait()
}

func srtConfig() srt.Config {
	cfg := srt.DefaultConfig()
	// Generous latency and no too-late drop: the assertion is that every byte
	// arrives in order, and a loaded CI machine running -race must not be able
	// to turn scheduling delay into a dropped packet.
	cfg.Latency = 2 * time.Second
	cfg.ReceiverLatency = 2 * time.Second
	cfg.PeerLatency = 2 * time.Second
	cfg.TooLatePacketDrop = false
	return cfg
}

type received struct {
	mu       sync.Mutex
	streamID string
	data     bytes.Buffer
	done     chan struct{}
}

func (r *received) len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.data.Len()
}

// startSRTListener is the real upstream: a gosrt listener that accepts one
// publisher and keeps everything it reads.
func startSRTListener(t *testing.T) (string, *received) {
	t.Helper()
	ln, err := srt.Listen("srt", "127.0.0.1:0", srtConfig())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(ln.Close)
	r := &received{done: make(chan struct{})}
	go func() {
		defer close(r.done)
		req, err := ln.Accept2()
		if err != nil {
			return
		}
		r.mu.Lock()
		r.streamID = req.StreamId()
		r.mu.Unlock()
		conn, err := req.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		buf := make([]byte, 2048)
		for {
			n, err := conn.Read(buf)
			if n > 0 {
				r.mu.Lock()
				r.data.Write(buf[:n])
				r.mu.Unlock()
			}
			if err != nil {
				if !errors.Is(err, io.EOF) {
					t.Logf("listener read ended: %v", err)
				}
				return
			}
		}
	}()
	return ln.Addr().String(), r
}

// runBondedSession publishes `dur` worth of paced 1316-byte chunks through a
// two-link bonded sender and returns what was sent, what the listener got,
// and the harness (for its logs and Stats).
func runBondedSession(t *testing.T, dur time.Duration) (sent []byte, r *received, h *harness, s *bondingSender, stats []GroupStats) {
	t.Helper()
	upAddr, r := startSRTListener(t)
	h = newHarness(t, nil)
	// Point the receiver at the real listener instead of the fake upstream:
	// a fresh Server, since Start resolves the upstream once.
	h.srv.Stop()
	h.srv = New(h.srv.log, Config{Listen: "127.0.0.1:0", Upstream: upAddr})
	if err := h.srv.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(h.srv.Stop)
	s = newBondingSender(t, h, 2)

	cfg := srtConfig()
	cfg.StreamId = secretStreamID
	conn, err := srt.Dial("srt", s.shim.LocalAddr().String(), cfg)
	if err != nil {
		t.Fatalf("SRT dial through the bonded receiver: %v", err)
	}

	// Deterministic, incompressible-looking payload with the secret woven in,
	// so a payload that reached a log line would be caught too.
	rng := rand.New(rand.NewChaCha8([32]byte{7}))
	const chunk = 1316
	const perSecond = 200 // ~2.1 Mbit/s
	n := int(dur.Seconds() * perSecond)
	tick := time.NewTicker(time.Second / perSecond)
	defer tick.Stop()
	for i := range n {
		p := make([]byte, chunk)
		for j := range p {
			p[j] = byte(rng.Uint32())
		}
		if i%50 == 0 {
			copy(p, secretStreamID)
		}
		if _, err := conn.Write(p); err != nil {
			t.Fatalf("write chunk %d: %v", i, err)
		}
		sent = append(sent, p...)
		<-tick.C
	}
	waitFor(t, 10*time.Second, func() bool { return r.len() >= len(sent) })
	stats = h.srv.Stats()
	conn.Close()
	select {
	case <-r.done:
	case <-time.After(5 * time.Second):
		t.Log("listener did not see the close in time")
	}
	return sent, r, h, s, stats
}

func TestARealSRTSessionBondedOverTwoLinksDeliversTheWholeStreamInOrder(t *testing.T) {
	sent, r, _, s, stats := runBondedSession(t, 2500*time.Millisecond)

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.streamID != secretStreamID {
		t.Fatalf("listener saw streamid %q", r.streamID)
	}
	if got := r.data.Bytes(); !bytes.Equal(got, sent) {
		i := 0
		for i < min(len(got), len(sent)) && got[i] == sent[i] {
			i++
		}
		t.Fatalf("received %d bytes, sent %d; first difference at byte %d", len(got), len(sent), i)
	}
	if len(stats) != 1 || len(stats[0].Links) != 2 {
		t.Fatalf("Stats: %+v", stats)
	}
	for i, l := range stats[0].Links {
		if l.Packets == 0 || l.Bytes == 0 {
			t.Fatalf("link %d carried nothing: %+v", i, l)
		}
		if l.Peer != s.links[i].LocalAddr().String() {
			t.Fatalf("link %d peer %q, want %q", i, l.Peer, s.links[i].LocalAddr())
		}
	}
	// Round-robin striping: neither link should carry the lion's share.
	a, b := stats[0].Links[0].Packets, stats[0].Links[1].Packets
	if a < (a+b)/4 || b < (a+b)/4 {
		t.Fatalf("striping lopsided: %d vs %d packets", a, b)
	}
}

func TestNeitherTheLogsNorStatsEverContainTheStreamIDPayloadOrGroupID(t *testing.T) {
	sent, r, h, s, stats := runBondedSession(t, 500*time.Millisecond)
	if r.len() < len(sent) {
		t.Fatal("session did not complete; the leak check would prove nothing")
	}
	// Drive every log path the session did not: a refusal, a reap.
	stranger := h.newLink()
	send(t, stranger, regPacket(typeREG2, randomID(t)))
	recv(t, stranger)
	h.srv.Stop()

	logs := h.logs.String()
	if logs == "" {
		t.Fatal("no log output captured; the check would prove nothing")
	}
	gid := s.gid[:]
	for what, needle := range map[string]string{
		"streamid":            secretStreamID,
		"group ID (hex)":      hex.EncodeToString(gid),
		"receiver half (hex)": hex.EncodeToString(gid[halfID:]),
		"group ID (base64)":   base64.StdEncoding.EncodeToString(gid),
		"group ID (raw)":      string(gid[halfID : halfID+16]),
	} {
		if strings.Contains(logs, needle) {
			t.Errorf("logs contain the %s", what)
		}
	}
	js, err := json.Marshal(stats)
	if err != nil {
		t.Fatal(err)
	}
	for _, needle := range []string{
		hex.EncodeToString(gid[halfID:]),
		base64.StdEncoding.EncodeToString(gid),
		secretStreamID,
	} {
		if bytes.Contains(js, []byte(needle)) {
			t.Error("Stats JSON contains the group ID or streamid")
		}
	}
}
