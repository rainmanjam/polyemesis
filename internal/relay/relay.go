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
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
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
	// targets is the same set as subs, as a slice, for fanout to walk without
	// taking a lock or allocating.
	//
	// fanout used to build a fresh []*subscriber under mu.RLock for EVERY
	// datagram, which at 20 Mbit/s is around 1,900 allocations a second per
	// hub, on the hottest path in the process, to produce a list that changes
	// perhaps twice an hour. Replaced wholesale on each membership change and
	// never mutated in place, so a reader holding the previous slice keeps
	// reading a consistent one.
	targets atomic.Pointer[[]*subscriber]

	rxPackets atomic.Uint64
	rxBytes   atomic.Uint64
	txPackets atomic.Uint64
	dropped   atomic.Uint64
	// capture is the #674 diagnostic sink; nil unless POLYEMESIS_RELAY_CAPTURE
	// names a path. Written under deliverMu, so it needs no lock of its own.
	capture *os.File
	// empty counts zero-length datagrams swallowed by run. See the comment
	// there: forwarding one takes down every consumer at once, so the count is
	// the only evidence left that it happened.
	empty atomic.Uint64

	// deliverMu serialises one datagram's whole trip through the hub.
	//
	// "cc is touched only by run, so it needs no lock" was false. Deliver is the
	// SRT ingest path and runs on srtserver's per-session read loop, and a
	// takeover deliberately overlaps two of those: closing the incumbent's
	// connection wakes its Read, but waking it is not the same as it having
	// LEFT Deliver, so the outgoing session can still be inside inspect() while
	// the new one enters it. Two goroutines then write c.last[pid] and
	// s.sendErrors with nothing between them.
	//
	// Held across fanout AND measure rather than around cc alone, because
	// continuity counting is order-dependent: two datagrams measured out of
	// order report discontinuities that never happened, and TSLost is the
	// figure the whole "UDP on loopback is defensible because it is measured"
	// argument rests on. One datagram at a time through a hub is also what the
	// single-reader run() already provided; this extends the same guarantee to
	// the injected path.
	deliverMu sync.Mutex
	// cc is guarded by deliverMu; the totals it feeds are atomic because Stats
	// reads them from the HTTP goroutine.
	cc              continuity
	tsPackets       atomic.Uint64
	tsLost          atomic.Uint64
	discontinuities atomic.Uint64

	closeOnce sync.Once
	done      chan struct{}
	// wg tracks the reader goroutine so Close can join it.
	//
	// Closing the connection wakes run() out of Read, but waking it is not the
	// same as it having finished: it may be part-way through fanout or measure,
	// still writing to subscriber sockets and still moving counters. Close
	// returning at that moment tells the caller the hub has stopped while it
	// demonstrably has not, which is how a Windows CI run caught a TxPackets
	// increment landing after Close had already returned.
	wg sync.WaitGroup
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
	//
	// Written only in fanout, which runs under Hub.deliverMu.
	sendErrors int
	// gotFirst latches the first successful send, so "relay first delivery" is
	// logged once per subscriber rather than per datagram.
	gotFirst bool
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
	// One file per hub port, so a
	// multi-hub install does not interleave two streams into one capture.
	if dir := os.Getenv("POLYEMESIS_RELAY_CAPTURE"); dir != "" {
		name := fmt.Sprintf("%s.%d.ts", dir, h.port)
		if f, err := os.Create(name); err == nil {
			h.capture = f
			log.Info("relay capture armed", "path", name)
		} else {
			log.Warn("relay capture could not be opened", "path", name, "err", err)
		}
	}
	h.wg.Add(1)
	go h.run()
	// #674: sampled by the clock so a STARVED window still produces lines.
	go h.sampleState(3 * time.Second)
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

// ErrSubscriberExists is returned when a name is already registered on this hub.
//
// #711. THE MAP ASSIGNMENT USED TO BE BARE. `h.subs[name] = ...` REPLACES the
// existing entry: the first consumer keeps running, keeps a correct command
// line and keeps a green card on the monitoring page -- and receives nothing.
// Worse, the hub logged "relay subscriber added" either way, so the log
// positively confirmed the wrong thing.
//
// Three devices already existed to avoid the collision and all three were rung
// zero: a naming convention in destinations.go, a lock in engine.go, and a
// comment in setup.go. It had bitten twice. The sink itself now refuses.
var ErrSubscriberExists = errors.New("a relay subscriber with that name is already registered on this hub")

// Subscribe registers a consumer and returns the URL it should read from.
//
// The consumer binds that port itself (FFmpeg does this when given a udp://
// input); the hub only sends to it. Subscribing before the consumer starts is
// fine — datagrams to an unbound port are simply discarded by the kernel.
//
// REFUSES AN OCCUPIED NAME (ErrSubscriberExists). Every caller already has a
// release-and-bail path for the allocator refusing a port, which is the same
// shape, and a caller that ignores this error is back to the silent replacement
// the error exists to prevent.
func (h *Hub) Subscribe(name string, port int) (string, error) {
	return h.SubscribeAddr(name, h.advertise, port)
}

// SubscribeAddr registers a consumer at an explicit address, for the case where
// it is not on this host. The caller resolves any hostname itself so that
// registration cannot block the engine on DNS.
func (h *Hub) SubscribeAddr(name string, ip net.IP, port int) (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if prev, taken := h.subs[name]; taken {
		// AT ERROR AND BEFORE THE STORE, so the log says what happened rather
		// than confirming an add that is not going to happen. Both addresses go
		// in the line: the whole difficulty of the old failure was that
		// everything downstream looked healthy, so the one place it can be seen
		// is here, naming the consumer that would have been cut off.
		h.log.Error("refusing a relay subscription: that name is already registered on "+
			"this hub, and taking it would leave the consumer holding it running and "+
			"receiving nothing",
			"name", name, "hubPort", h.port,
			"heldBy", prev.addr.String(), "refused", (&net.UDPAddr{IP: ip, Port: port}).String())
		return "", fmt.Errorf("%q on hub port %d: %w", name, h.port, ErrSubscriberExists)
	}
	h.subs[name] = &subscriber{
		name: name,
		addr: &net.UDPAddr{IP: ip, Port: port},
	}
	h.rebuildTargets()
	// AT INFO, WITH THE HUB'S OWN PORT. #674
	//
	// Every subscriber on this hub received its first byte in the same
	// millisecond, 73 seconds after their children started -- so the hub was
	// receiving (its capture proves it) while its target list was empty. The
	// only way to tell "subscribed late" from "subscribed to a different hub"
	// is to record WHICH hub each Subscribe landed on, and when. At Debug this
	// said nothing, because the acceptance suite never shows Debug.
	h.log.Info("relay subscriber added", "name", name, "hubPort", h.port,
		"subscriberPort", port, "total", len(h.subs))
	return udpURL(ip, port), nil
}

// Unsubscribe removes a consumer.
func (h *Hub) Unsubscribe(name string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	// THE MIRROR OF THE COLLISION, and it was silent in the same way. #711.
	// delete() on an absent key is a no-op, and the line below said "removed"
	// regardless -- so a mismatched name removed nothing and reported success,
	// which is exactly how a subscription outlives the process that owned it.
	//
	// Not an error return: every caller is a teardown path, and a teardown that
	// has to branch on this would either ignore it or abandon the rest of its
	// cleanup. Reported instead, at the level that gets read.
	if _, had := h.subs[name]; !had {
		h.log.Error("asked to remove a relay subscriber this hub does not have; "+
			"nothing was removed. A teardown naming the wrong subscriber leaves the "+
			"real one forwarding into a process that is gone",
			"name", name, "hubPort", h.port, "total", len(h.subs))
		return
	}
	delete(h.subs, name)
	h.rebuildTargets()
	// AT INFO, for the same reason as "added". #674: a destination subscribed at
	// 08:24:52 and its child started 1ms later, on a hub that was receiving --
	// and it got nothing until 08:26:05. That is only possible if the
	// subscription was REMOVED while the child kept running, and at Debug this
	// line could never show it.
	h.log.Info("relay subscriber removed", "name", name, "hubPort", h.port, "total", len(h.subs))
}

// rebuildTargets republishes the fanout list. Caller must hold mu for writing.
//
// A NEW slice every time, never an edit of the live one: fanout reads it with
// no lock at all, so the slice it is walking has to stay valid for as long as
// it holds it.
func (h *Hub) rebuildTargets() {
	next := make([]*subscriber, 0, len(h.subs))
	for _, s := range h.subs {
		next = append(next, s)
	}
	h.targets.Store(&next)
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
	defer h.wg.Done()

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
		// Under the same lock as Deliver: a hub reached by both its UDP socket
		// and in-process injection has two writers, not one.
		h.deliverMu.Lock()
		h.fanout(buf[:n])
		h.measure(buf[:n])
		// A BYTE-EXACT RECORD OF WHAT EVERY SUBSCRIBER RECEIVES.
		//
		// Off unless POLYEMESIS_RELAY_CAPTURE is set. Exactly the bytes fanout() forwards to every subscriber. The earlier
		// capture was a SECOND OUTPUT on the ingest child, which FFmpeg muxes
		// independently -- it proved the ingest could produce good audio, not
		// that these datagrams carry it. This is the stream destinations
		// actually read.
		if h.capture != nil {
			_, _ = h.capture.Write(buf[:n])
		}
		h.deliverMu.Unlock()
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
	h.deliverMu.Lock()
	defer h.deliverMu.Unlock()
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

// sampleState reports rxPackets and the target count on a TIME ticker. #674
//
// The first version of this sampled every 500 DATAGRAMS, which cannot describe
// a window with no datagrams: a starved hub simply stopped logging, so every
// sample came from after the starvation ended and the interesting period was
// the one part with no data about it. A period of silence has to be sampled by
// the clock, not by the thing that has gone silent.
func (h *Hub) sampleState(every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-h.done:
			return
		case <-t.C:
			count := 0
			if tp := h.targets.Load(); tp != nil {
				count = len(*tp)
			}
			h.log.Info("relay fanout state", "hubPort", h.port,
				"rxPackets", h.rxPackets.Load(), "txPackets", h.txPackets.Load(),
				"targets", count)
		}
	}
}

func (h *Hub) fanout(pkt []byte) {
	// No lock and no allocation: the list is republished by rebuildTargets on
	// the rare occasions it changes. Nil until the first subscriber.
	tp := h.targets.Load()
	if tp == nil {
		return
	}

	for _, s := range *tp {
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
		// FIRST DELIVERY, ONCE PER SUBSCRIBER. #674
		//
		// A destination that reads nothing for 77 seconds is either not being
		// sent to, or not receiving what is sent. Nothing distinguished those:
		// dropped counts only WriteToUDP ERRORS, and txPackets is a total
		// across every subscriber. This is the sender's own record of when a
		// named consumer first got a byte, which is the number the whole
		// investigation needed and never had.
		if !s.gotFirst {
			s.gotFirst = true
			h.log.Info("relay first delivery", "subscriber", s.name, "port", s.addr.Port)
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
	// Outside the Once, so a second caller waits too rather than being told the
	// hub is closed while the first caller's join is still in progress.
	//
	// This cannot deadlock: closing the connection is what unblocks run() out
	// of Read, and run() checks h.done before every iteration, so it is already
	// on its way out by the time anything waits for it. Nothing on the read
	// path calls Close.
	h.wg.Wait()
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
