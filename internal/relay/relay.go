// Package relay is the internal fan-out hub.
//
// The ingest process publishes the stream exactly once, as MPEG-TS over
// loopback UDP. This hub receives those datagrams and replicates each one to
// every registered subscriber — destinations, the recorder, the HLS preview,
// the metering sidecar — so that N consumers cost the source nothing and any
// one of them can restart without the ingest noticing.
//
// Why not multicast: loopback multicast needs an interface and a route, and
// gets that wrong differently on Linux, macOS and Windows. Why not a second
// SRT hop: an extra process and an extra latency budget for no gain. A Go
// map of *net.UDPAddr is both simpler and more portable than either.
//
// Tradeoff, accepted deliberately: UDP on loopback can drop a datagram under
// memory pressure and there is no retransmit. Each datagram is seven 188-byte
// TS packets, so a loss is a decoder glitch, not a dead stream. Sends are
// non-blocking and a slow subscriber is dropped rather than allowed to stall
// the hub, because one wedged consumer must never take down the ingest.
package relay

import (
	"fmt"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
)

// datagramSize is the largest packet we expect: 1316 bytes of TS payload plus
// slack. Reading into a buffer smaller than the datagram silently truncates.
const datagramSize = 2048

// Hub receives the ingest stream and replicates it to subscribers.
type Hub struct {
	log  *slog.Logger
	conn *net.UDPConn
	port int

	mu   sync.RWMutex
	subs map[string]*subscriber

	rxPackets atomic.Uint64
	rxBytes   atomic.Uint64
	txPackets atomic.Uint64
	dropped   atomic.Uint64

	closeOnce sync.Once
	done      chan struct{}
}

type subscriber struct {
	name string
	addr *net.UDPAddr
	// sendErrors counts consecutive failures. A consumer that has gone away
	// leaves an unreachable port behind; we notice and stop shouting at it.
	sendErrors int
}

// New binds the hub's receive socket on loopback. Port 0 picks a free one.
func New(log *slog.Logger, port int) (*Hub, error) {
	addr := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: port}
	conn, err := net.ListenUDP("udp4", addr)
	if err != nil {
		return nil, fmt.Errorf("relay: bind %s: %w", addr, err)
	}
	// A generous receive buffer is what absorbs a GC pause or a scheduling
	// hiccup without losing packets. Best effort: some systems cap it lower.
	_ = conn.SetReadBuffer(8 << 20)

	h := &Hub{
		log:  log,
		conn: conn,
		port: conn.LocalAddr().(*net.UDPAddr).Port,
		subs: map[string]*subscriber{},
		done: make(chan struct{}),
	}
	go h.run()
	return h, nil
}

// Port is the loopback port the ingest publishes to.
func (h *Hub) Port() int { return h.port }

// InputURL is the URL the ingest process writes to.
func (h *Hub) InputURL() string {
	return fmt.Sprintf("udp://127.0.0.1:%d", h.port)
}

// Subscribe registers a consumer and returns the URL it should read from.
//
// The consumer binds that port itself (FFmpeg does this when given a udp://
// input); the hub only sends to it. Subscribing before the consumer starts is
// fine — datagrams to an unbound port are simply discarded by the kernel.
func (h *Hub) Subscribe(name string, port int) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.subs[name] = &subscriber{
		name: name,
		addr: &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: port},
	}
	h.log.Debug("relay subscriber added", "name", name, "port", port, "total", len(h.subs))
	return fmt.Sprintf("udp://127.0.0.1:%d", port)
}

// Unsubscribe removes a consumer.
func (h *Hub) Unsubscribe(name string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.subs, name)
	h.log.Debug("relay subscriber removed", "name", name, "total", len(h.subs))
}

// Subscribers returns the current consumer names.
func (h *Hub) Subscribers() []string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make([]string, 0, len(h.subs))
	for k := range h.subs {
		out = append(out, k)
	}
	return out
}

// Stats is a snapshot of relay throughput, surfaced on the monitoring page.
type Stats struct {
	Port        int      `json:"port"`
	Subscribers []string `json:"subscribers"`
	RxPackets   uint64   `json:"rxPackets"`
	RxBytes     uint64   `json:"rxBytes"`
	TxPackets   uint64   `json:"txPackets"`
	Dropped     uint64   `json:"dropped"`
}

// Stats returns a throughput snapshot.
func (h *Hub) Stats() Stats {
	return Stats{
		Port:        h.port,
		Subscribers: h.Subscribers(),
		RxPackets:   h.rxPackets.Load(),
		RxBytes:     h.rxBytes.Load(),
		TxPackets:   h.txPackets.Load(),
		Dropped:     h.dropped.Load(),
	}
}

// RxBytes is the running total of bytes received from the ingest, used to
// derive the live ingest bitrate.
func (h *Hub) RxBytes() uint64 { return h.rxBytes.Load() }

func (h *Hub) run() {
	buf := make([]byte, datagramSize)
	for {
		select {
		case <-h.done:
			return
		default:
		}

		n, err := h.conn.Read(buf)
		if err != nil {
			select {
			case <-h.done:
				return
			default:
			}
			h.log.Warn("relay read error", "err", err)
			continue
		}
		h.rxPackets.Add(1)
		h.rxBytes.Add(uint64(n))
		h.fanout(buf[:n])
	}
}

func (h *Hub) fanout(pkt []byte) {
	h.mu.RLock()
	targets := make([]*subscriber, 0, len(h.subs))
	for _, s := range h.subs {
		targets = append(targets, s)
	}
	h.mu.RUnlock()

	for _, s := range targets {
		if _, err := h.conn.WriteToUDP(pkt, s.addr); err != nil {
			h.dropped.Add(1)
			// ECONNREFUSED on loopback just means the consumer has not bound
			// its port yet — normal during a restart. Only sustained failure
			// is worth a log line.
			s.sendErrors++
			if s.sendErrors == 1 || s.sendErrors%1000 == 0 {
				h.log.Debug("relay send failed", "subscriber", s.name, "errors", s.sendErrors, "err", err)
			}
			continue
		}
		s.sendErrors = 0
		h.txPackets.Add(1)
	}
}

// Close shuts the hub down.
func (h *Hub) Close() error {
	var err error
	h.closeOnce.Do(func() {
		close(h.done)
		err = h.conn.Close()
	})
	return err
}

// PortAllocator hands out loopback ports for subscribers.
//
// It verifies each port is actually free by binding it, which closes the
// obvious race where two subscribers are handed the same number and one of
// them silently receives the other's stream.
type PortAllocator struct {
	mu    sync.Mutex
	next  int
	base  int
	limit int
	held  map[int]bool
}

// NewPortAllocator allocates from [base, base+span).
func NewPortAllocator(base, span int) *PortAllocator {
	return &PortAllocator{next: base, base: base, limit: base + span, held: map[int]bool{}}
}

// Allocate returns a free loopback UDP port.
func (a *PortAllocator) Allocate() (int, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	for tried := 0; tried < a.limit-a.base; tried++ {
		p := a.next
		a.next++
		if a.next >= a.limit {
			a.next = a.base
		}
		if a.held[p] {
			continue
		}
		if !portFree(p) {
			continue
		}
		a.held[p] = true
		return p, nil
	}
	return 0, fmt.Errorf("relay: no free UDP port in range %d-%d", a.base, a.limit-1)
}

// Release returns a port to the pool.
func (a *PortAllocator) Release(p int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.held, p)
}

func portFree(p int) bool {
	c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: p})
	if err != nil {
		return false
	}
	c.Close()
	return true
}
