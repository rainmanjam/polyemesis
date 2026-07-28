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
//
// That tradeoff is only defensible if it is measured, so the hub reads the
// MPEG-TS continuity counters on the way past and reports the loss it implies.
package relay

import (
	"fmt"
	"log/slog"
	"net"
	"strconv"
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
	// advertise is the address consumers are handed, which is not always the
	// address we bound: a wildcard bind is not dialable.
	advertise net.IP

	mu   sync.RWMutex
	subs map[string]*subscriber

	rxPackets atomic.Uint64
	rxBytes   atomic.Uint64
	txPackets atomic.Uint64
	dropped   atomic.Uint64
	// empty counts zero-length datagrams swallowed by run. See the comment
	// there: forwarding one takes down every consumer at once, so the count is
	// the only evidence left that it happened.
	empty atomic.Uint64

	// cc is touched only by run, so it needs no lock; the totals it feeds are
	// atomic because Stats reads them from the HTTP goroutine.
	cc              continuity
	tsPackets       atomic.Uint64
	tsLost          atomic.Uint64
	discontinuities atomic.Uint64

	closeOnce sync.Once
	done      chan struct{}
}

// Option customises the hub at construction. The zero set of options is the
// loopback IPv4 hub that everything in-process expects.
type Option func(*config)

type config struct {
	listen    net.IP
	advertise net.IP
}

// WithListenIP binds the receive socket somewhere other than IPv4 loopback: an
// IPv6 address, or the wildcard when the relay has to span hosts. The wildcard
// gets a dual-stack socket, so one hub serves IPv4 and IPv6 consumers.
func WithListenIP(ip net.IP) Option {
	return func(c *config) { c.listen = ip }
}

// WithAdvertiseIP overrides the address consumers are told to use, for the case
// where the hub binds a wildcard or sits behind a different route than it
// listens on.
func WithAdvertiseIP(ip net.IP) Option {
	return func(c *config) { c.advertise = ip }
}

type subscriber struct {
	name string
	addr *net.UDPAddr
	// sendErrors counts consecutive failures. A consumer that has gone away
	// leaves an unreachable port behind; we notice and stop shouting at it.
	sendErrors int
}

// New binds the hub's receive socket, on IPv4 loopback unless an option says
// otherwise. Port 0 picks a free one.
func New(log *slog.Logger, port int, opts ...Option) (*Hub, error) {
	cfg := config{listen: net.IPv4(127, 0, 0, 1)}
	for _, opt := range opts {
		opt(&cfg)
	}
	if cfg.advertise == nil {
		cfg.advertise = advertiseFor(cfg.listen)
	}

	addr := &net.UDPAddr{IP: cfg.listen, Port: port}
	// "udp" rather than "udp4" lets the address decide the family, so an IPv6
	// or wildcard bind works without a second code path — and a wildcard gets a
	// dual-stack socket, which "udp6" would not. A concrete IPv4 address still
	// yields an AF_INET socket, so the loopback default is unchanged.
	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		return nil, fmt.Errorf("relay: bind %s: %w", addr, err)
	}
	// A generous receive buffer is what absorbs a GC pause or a scheduling
	// hiccup without losing packets. Best effort: some systems cap it lower.
	_ = conn.SetReadBuffer(8 << 20)

	h := &Hub{
		log:       log,
		conn:      conn,
		port:      conn.LocalAddr().(*net.UDPAddr).Port,
		advertise: cfg.advertise,
		subs:      map[string]*subscriber{},
		done:      make(chan struct{}),
	}
	go h.run()
	return h, nil
}

// advertiseFor picks the address consumers should dial. Nothing can reach a
// wildcard bind by that name, so fall back to the loopback of the same family:
// local consumers are the case that exists, and a cross-host deployment has to
// say where it is reachable via WithAdvertiseIP anyway.
func advertiseFor(listen net.IP) net.IP {
	if listen == nil {
		return net.IPv4(127, 0, 0, 1)
	}
	if listen.IsUnspecified() {
		if listen.To4() == nil {
			return net.IPv6loopback
		}
		return net.IPv4(127, 0, 0, 1)
	}
	return listen
}

func udpURL(ip net.IP, port int) string {
	return "udp://" + net.JoinHostPort(ip.String(), strconv.Itoa(port))
}

// Port is the UDP port the ingest publishes to.
func (h *Hub) Port() int { return h.port }

// InputURL is the URL the ingest process writes to.
func (h *Hub) InputURL() string {
	return udpURL(h.advertise, h.port)
}

// Subscribe registers a consumer and returns the URL it should read from.
//
// The consumer binds that port itself (FFmpeg does this when given a udp://
// input); the hub only sends to it. Subscribing before the consumer starts is
// fine — datagrams to an unbound port are simply discarded by the kernel.
func (h *Hub) Subscribe(name string, port int) string {
	return h.SubscribeAddr(name, h.advertise, port)
}

// SubscribeAddr registers a consumer at an explicit address, for the case where
// it is not on this host. The caller resolves any hostname itself so that
// registration cannot block the engine on DNS.
func (h *Hub) SubscribeAddr(name string, ip net.IP, port int) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.subs[name] = &subscriber{
		name: name,
		addr: &net.UDPAddr{IP: ip, Port: port},
	}
	h.log.Debug("relay subscriber added", "name", name, "addr", ip, "port", port, "total", len(h.subs))
	return udpURL(ip, port)
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
//
// Dropped counts sends that failed, which says nothing about what the wire
// lost. TSLost is the other half of the picture: TS packets that never arrived,
// inferred from the continuity counters, and the number the "UDP on loopback
// may drop" tradeoff has to be judged against. TSPackets is its denominator and
// so counts only the packets a counter can be read from — stuffing and
// adaptation-field-only packets are excluded, and both stay zero if the stream
// is not MPEG-TS.
type Stats struct {
	Port            int      `json:"port"`
	Subscribers     []string `json:"subscribers"`
	RxPackets       uint64   `json:"rxPackets"`
	RxBytes         uint64   `json:"rxBytes"`
	TxPackets       uint64   `json:"txPackets"`
	Dropped         uint64   `json:"dropped"`
	TSPackets       uint64   `json:"tsPackets"`
	TSLost          uint64   `json:"tsLost"`
	Discontinuities uint64   `json:"discontinuities"`
	LossPercent     float64  `json:"lossPercent"`
}

// Stats returns a throughput snapshot.
func (h *Hub) Stats() Stats {
	tsPackets := h.tsPackets.Load()
	tsLost := h.tsLost.Load()
	s := Stats{
		Port:            h.port,
		Subscribers:     h.Subscribers(),
		RxPackets:       h.rxPackets.Load(),
		RxBytes:         h.rxBytes.Load(),
		TxPackets:       h.txPackets.Load(),
		Dropped:         h.dropped.Load(),
		TSPackets:       tsPackets,
		TSLost:          tsLost,
		Discontinuities: h.discontinuities.Load(),
	}
	if sent := tsPackets + tsLost; sent > 0 {
		s.LossPercent = 100 * float64(tsLost) / float64(sent)
	}
	return s
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
		if n == 0 {
			// A zero-length datagram is not a packet, and forwarding it is not
			// harmless: FFmpeg's UDP demuxer reports a zero-length read as EOF,
			// so one empty datagram ends every consumer on this hub at once —
			// every destination, the meters sidecar and each loudness analyser,
			// all exiting 0 as though they had been asked to stop. They then
			// respawn, which makes it look like a restart loop with no error in
			// it anywhere. Dropping it here costs nothing: MPEG-TS carries no
			// information in an empty datagram.
			h.empty.Add(1)
			h.log.Debug("relay dropped a zero-length datagram",
				"reason", "forwarding it would signal EOF to every consumer",
				"total", h.empty.Load())
			continue
		}
		h.rxPackets.Add(1)
		h.rxBytes.Add(uint64(n))
		// Fan out first: measurement must never sit in front of delivery.
		h.fanout(buf[:n])
		h.measure(buf[:n])
	}
}

// Deliver injects one datagram from inside this process, as though it had
// arrived on the hub's UDP socket.
//
// It exists for the one-port SRT listener, which reads MPEG-TS straight off a
// gosrt connection in Go. Without it the listener would have to write to
// InputURL and have the run loop read it back, paying a loopback UDP hop and a
// copy for data that is already in memory — and inheriting the kernel's
// datagram-drop behaviour on a socket we control both ends of.
//
// Callers must pass whole datagrams. SRT in live mode preserves message
// boundaries, so one Read is one datagram and the 188-byte MPEG-TS framing the
// continuity counter depends on survives; splitting or coalescing reads here
// would make measure() report discontinuities that never happened.
func (h *Hub) Deliver(pkt []byte) {
	if len(pkt) == 0 {
		return
	}
	h.rxPackets.Add(1)
	h.rxBytes.Add(uint64(len(pkt)))
	// Same order as run(): fan out before measuring, because measurement must
	// never sit in front of delivery.
	h.fanout(pkt)
	h.measure(pkt)
}

func (h *Hub) measure(dgram []byte) {
	packets, discos, lost := h.cc.inspect(dgram)
	if packets == 0 {
		return
	}
	h.tsPackets.Add(packets)
	if discos > 0 {
		h.discontinuities.Add(discos)
		h.tsLost.Add(lost)
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

const (
	tsPacketSize = 188
	tsSyncByte   = 0x47
	// tsNullPID carries stuffing. Its continuity counter is undefined, so
	// reading it would manufacture loss out of padding.
	tsNullPID = 0x1FFF
	tsPIDs    = 0x2000
)

// continuity tracks the MPEG-TS continuity counter of every PID in the stream.
//
// Cost is one array index and a handful of branches per 188-byte packet: about
// 20ns for a full 1316-byte datagram, which at 20 Mbit/s is under a thousandth
// of a percent of a core. That is why it runs on every datagram rather than
// being sampled or made optional. The table is 8 KiB for the life of the hub.
type continuity struct {
	// last holds the counter plus one, so the zero value reads as "PID not seen
	// yet" and the first packet of a stream establishes a baseline instead of
	// being reported as a loss.
	last [tsPIDs]uint8
}

// inspect walks the TS packets in one datagram and returns how many
// payload-bearing packets it checked, how many continuity breaks it saw, and
// how many packets those breaks imply were lost.
func (c *continuity) inspect(dgram []byte) (packets, discontinuities, lost uint64) {
	for off := 0; off+tsPacketSize <= len(dgram); off += tsPacketSize {
		p := dgram[off : off+tsPacketSize]
		// Losing sync means the rest of the datagram cannot be trusted to be
		// packet-aligned, so stop rather than guess.
		if p[0] != tsSyncByte {
			return packets, discontinuities, lost
		}
		pid, ok := countedPID(p)
		if !ok {
			continue
		}
		packets++
		if gap := c.advance(pid, p); gap != 0 {
			discontinuities++
			lost += uint64(gap)
		}
	}
	return packets, discontinuities, lost
}

// countedPID returns the packet's PID and whether its continuity counter is
// evidence about loss at all.
func countedPID(p []byte) (uint16, bool) {
	// transport_error_indicator: an upstream hop already marked this packet
	// corrupt, so its counter says nothing.
	if p[1]&0x80 != 0 {
		return 0, false
	}
	// The payload bit of adaptation_field_control. The counter only advances on
	// packets that carry payload, so adaptation-field-only packets are skipped.
	if p[3]&0x10 == 0 {
		return 0, false
	}
	pid := uint16(p[1]&0x1F)<<8 | uint16(p[2])
	return pid, pid != tsNullPID
}

// advance folds one packet into the per-PID state and returns how many packets
// went missing ahead of it.
func (c *continuity) advance(pid uint16, p []byte) uint8 {
	cc := p[3] & 0x0F
	prev := c.last[pid]
	c.last[pid] = cc + 1
	if prev == 0 {
		return 0
	}
	prev--

	// The spec allows a packet to be repeated once with its counter unchanged;
	// that is redundancy, not loss.
	if cc == prev {
		return 0
	}
	// discontinuity_indicator in the adaptation field: the encoder is telling
	// us it jumped on purpose, as at a splice point.
	if p[3]&0x20 != 0 && p[4] > 0 && p[5]&0x80 != 0 {
		return 0
	}
	return (cc - prev - 1) & 0x0F
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
