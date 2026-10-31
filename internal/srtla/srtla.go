// Package srtla is an SRTLA receiver: a UDP proxy that sits in front of the
// one-port SRT ingest and lets a bonded encoder spread one SRT session over
// several network links at once.
//
// SRTLA ("SRT link aggregation") is what IRL streaming rigs use to survive a
// moving vehicle: a backpack with three modems, a phone with Wi-Fi and 5G. The
// sender (srtla_send, BELABOX, Moblin, IRL Pro) takes the packets of ONE SRT
// connection and stripes them over every uplink it has, each uplink arriving
// here from its own source address. This package merges them back into one
// stream and hands it to the local SRT listener as an ordinary SRT peer.
//
// WHY A PROXY RATHER THAN SRTLA INSIDE THE SRT LISTENER. The SRT listener is
// where publishers are authenticated (the streamid token), slotted against a
// programme and failed over; none of that should know or care how many radios
// the packets crossed. Done as a proxy, a bonded publisher is exactly the same
// thing to internal/srtserver as a plain one: one peer, one handshake, one
// token check. The only cost is a loopback hop, and what the proxy has to
// understand of SRT is two facts -- which packets are data (to count them for
// the sender's link-load estimate) and which are ACKs (to send them down every
// link). It never parses the handshake, so it never sees the token as anything
// but opaque bytes in transit.
//
// THE SHAPE. One UDP socket faces the senders. A sender registers a GROUP (one
// per SRT session) and then each of its links joins that group. Each group
// gets its own UDP socket towards the SRT listener, so the listener sees one
// peer per group however many links feed it, and the group's socket address is
// how the rest of the system tells which programme a bonded session belongs to
// (GroupStats.UpstreamLocal).
//
// THE THREAT MODEL is a public UDP port. Anyone can send anything to it,
// including with a forged source address, so: nothing is ever sent to an
// address that has not registered except the reply to its own REG packet, and
// no reply is larger than what provoked it (REG2-for-REG1 is equal); group
// creation is rate-limited per source IP and capped overall; and nothing that
// could carry the publish token -- payloads, and the group ID that stands in
// for a session -- is ever logged or reported.
package srtla

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"slices"
	"sync"
	"time"
)

// Defaults for the knobs below. They are package constants so the parent can
// document them; the Server copies them into unexported fields so tests can
// shrink the timeouts rather than sleep for ten seconds.
const (
	// MaxLinksPerGroup is how many uplinks one bonded session may have. Real
	// rigs have two to four; eight leaves room without letting one group
	// occupy unbounded state.
	MaxLinksPerGroup = 8
	// MaxGroups caps concurrent bonded sessions. It is the backstop behind the
	// per-IP REG1 rate limit: a REG1 costs a group and a socket, so the table
	// must have a ceiling even against an attacker with many addresses.
	MaxGroups = 200
	// LinkTimeout is how long a link may be silent before it is dropped.
	// Senders keepalive every link well inside this, so a link that has gone
	// quiet for ten seconds is a modem that is gone, and keeping it would
	// waste the ACK fan-out on it.
	LinkTimeout = 10 * time.Second
	// GroupTimeout is how long a group with no links survives -- long enough
	// for every link of a rig to drop and re-register through a tunnel, and
	// it also reaps a group whose REG1 was never followed by any REG2.
	GroupTimeout = 10 * time.Second
)

const (
	housekeepingEvery = time.Second
	rateWindow        = time.Second

	// REG1 rate limit, per source IP: a token bucket of reg1Burst refilling at
	// reg1PerSecond. A real sender sends one REG1 per connection attempt and
	// retries on the order of seconds, so this never bites one; it stops one
	// host filling the group table in a burst.
	reg1PerSecond = 2.0
	reg1Burst     = 4.0
	// reg1MaxTracked bounds the rate limiter's own memory. When it is full a
	// REG1 from an IP not already tracked is refused rather than evicting
	// someone's bucket: eviction would let a source-spoofing flood reset the
	// limit for a real attacker's address, while refusal only delays new
	// senders until the next housekeeping pass prunes idle buckets.
	reg1MaxTracked = 4096

	// halfID is the sender's share of the group ID.
	halfID = idLen / 2
)

// Config is what the parent supplies.
type Config struct {
	Listen   string // UDP listen address, e.g. ":5000"
	Upstream string // the local SRT listener, e.g. "127.0.0.1:6000"
}

// GroupStats is one bonded session, for the UI and metrics. It deliberately
// has no ID field: the group ID is the session's only credential on the SRTLA
// side (whoever knows it can add links to the session), so it never leaves
// this package.
type GroupStats struct {
	// UpstreamLocal is the local address of the group's socket towards the
	// SRT listener (e.g. "127.0.0.1:53211"). The SRT listener sees this as the
	// publisher's peer address, which is how the parent joins a group to a
	// programme's SRT session.
	UpstreamLocal string      `json:"upstreamLocal"`
	Since         time.Time   `json:"since"`
	Links         []LinkStats `json:"links"`
}

// LinkStats is one uplink of a group.
type LinkStats struct {
	Peer        string    `json:"peer"` // sender address IP:port
	Since       time.Time `json:"since"`
	LastSeen    time.Time `json:"lastSeen"`
	Packets     uint64    `json:"packets"` // datagrams received on this link
	Bytes       uint64    `json:"bytes"`
	BitrateKbps float64   `json:"bitrateKbps"` // over the last >=1 s interval
	Share       float64   `json:"share"`       // this link's fraction of the group's bytes over that interval, 0-1
}

// Server is the receiver. Build it with New, then Start; Stop releases
// everything.
type Server struct {
	log *slog.Logger
	cfg Config

	// Knobs, defaulted by New and overridden only by tests in this package.
	maxLinks     int
	maxGroups    int
	linkTimeout  time.Duration
	groupTimeout time.Duration
	tick         time.Duration
	window       time.Duration
	reg1Rate     float64
	reg1Burst    float64
	reg1MaxIPs   int

	conn     *net.UDPConn
	upstream *net.UDPAddr
	done     chan struct{}
	wg       sync.WaitGroup
	stopOnce sync.Once

	// mu guards everything below. One lock for the whole table is enough: the
	// work under it is a map lookup and a few counters per datagram, and every
	// socket write happens after it is released.
	mu      sync.Mutex
	started bool
	stopped bool
	groups  []*group
	links   map[netip.AddrPort]*link
	buckets map[netip.Addr]*bucket
}

type group struct {
	id    [idLen]byte
	up    *net.UDPConn
	since time.Time
	links []*link
	// creator is the address the group's REG1 came from. The SRT listener sees
	// every bonded publisher as a loopback peer -- this receiver's own socket --
	// so its wrong-token lockout, keyed by peer host, would otherwise lock out
	// every bonded encoder at once on one attacker's guesses. SenderHost hands
	// it this address instead.
	creator netip.Addr
	// lastActive is where every upstream packet that is not an ACK goes: the
	// link that most recently carried data. SRT's own replies (handshake,
	// NAKs, keepalives) need to reach the sender once, by a path that is known
	// to be working right now.
	lastActive netip.AddrPort
	// emptySince is when the group last had no links, or zero while it has
	// some. A group is born empty (REG1 makes the group, REG2 adds links).
	emptySince  time.Time
	windowStart time.Time
}

type link struct {
	group    *group
	addr     netip.AddrPort
	since    time.Time
	lastSeen time.Time
	packets  uint64
	bytes    uint64
	// seqs collects SRT data sequence numbers for the next SRTLA ACK.
	seqs []uint32
	// windowBytes is this rate window's byte count; kbps and share are the
	// last completed window's results.
	windowBytes uint64
	kbps        float64
	share       float64
}

type bucket struct {
	tokens float64
	last   time.Time
}

// New builds a Server. It does no I/O; Start binds.
func New(log *slog.Logger, cfg Config) *Server {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Server{
		log:          log,
		cfg:          cfg,
		maxLinks:     MaxLinksPerGroup,
		maxGroups:    MaxGroups,
		linkTimeout:  LinkTimeout,
		groupTimeout: GroupTimeout,
		tick:         housekeepingEvery,
		window:       rateWindow,
		reg1Rate:     reg1PerSecond,
		reg1Burst:    reg1Burst,
		reg1MaxIPs:   reg1MaxTracked,
		done:         make(chan struct{}),
		links:        map[netip.AddrPort]*link{},
		buckets:      map[netip.Addr]*bucket{},
	}
}

// Start binds the listen socket and starts serving. The upstream address is
// resolved once here, so a typo fails at startup rather than at the first
// sender's REG1.
func (s *Server) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started || s.stopped {
		return errors.New("srtla: Start called twice, or after Stop; build a new Server")
	}
	up, err := net.ResolveUDPAddr("udp", s.cfg.Upstream)
	if err != nil {
		return fmt.Errorf("srtla: upstream %q: %w", s.cfg.Upstream, err)
	}
	la, err := net.ResolveUDPAddr("udp", s.cfg.Listen)
	if err != nil {
		return fmt.Errorf("srtla: listen %q: %w", s.cfg.Listen, err)
	}
	conn, err := net.ListenUDP("udp", la)
	if err != nil {
		return fmt.Errorf("srtla: listen %q: %w", s.cfg.Listen, err)
	}
	s.conn, s.upstream, s.started = conn, up, true
	s.wg.Add(2)
	go s.readLoop()
	go s.housekeeping()
	s.log.Info("srtla receiver listening", "addr", conn.LocalAddr().String(), "upstream", up.String())
	return nil
}

// Stop closes every socket and waits for every goroutine. Safe to call more
// than once, and before Start.
func (s *Server) Stop() {
	s.stopOnce.Do(func() {
		s.mu.Lock()
		s.stopped = true
		if s.conn != nil {
			_ = s.conn.Close()
		}
		for _, g := range s.groups {
			_ = g.up.Close()
		}
		s.groups = nil
		clear(s.links)
		s.mu.Unlock()
		close(s.done)
		s.wg.Wait()
	})
}

// Addr is the bound listen address, or nil before Start.
func (s *Server) Addr() net.Addr {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conn == nil {
		return nil
	}
	return s.conn.LocalAddr()
}

// Stats is a snapshot for the UI and metrics.
func (s *Server) Stats() []GroupStats {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]GroupStats, 0, len(s.groups))
	for _, g := range s.groups {
		gs := GroupStats{
			UpstreamLocal: g.up.LocalAddr().String(),
			Since:         g.since,
			Links:         make([]LinkStats, 0, len(g.links)),
		}
		for _, l := range g.links {
			gs.Links = append(gs.Links, LinkStats{
				Peer:        l.addr.String(),
				Since:       l.since,
				LastSeen:    l.lastSeen,
				Packets:     l.packets,
				Bytes:       l.bytes,
				BitrateKbps: l.kbps,
				Share:       l.share,
			})
		}
		out = append(out, gs)
	}
	return out
}

// SenderHost reports the IP address that created the group whose socket
// towards the SRT listener has the local address upstreamLocal -- the address
// the SRT listener sees as the publisher's peer. False when no group has it.
//
// For the SRT listener's per-peer lockout (srtserver.Server.SetPeerHost): a
// bonded publisher arrives from loopback, and without this every bonded
// encoder would share one lockout bucket with whoever is guessing tokens.
func (s *Server) SenderHost(upstreamLocal string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, g := range s.groups {
		if g.up != nil && g.up.LocalAddr().String() == upstreamLocal {
			return g.creator.String(), true
		}
	}
	return "", false
}

// readLoop serves the sender-facing socket. Every datagram is handled to
// completion here, in arrival order, which is what keeps one link's SRTLA ACK
// batches in the order its packets came in.
func (s *Server) readLoop() {
	defer s.wg.Done()
	buf := make([]byte, mtu+1)
	for {
		n, from, err := s.conn.ReadFromUDPAddrPort(buf)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			// Anything else on an unconnected UDP socket is transient (an
			// ICMP error surfacing on some platforms); the socket still works.
			continue
		}
		if n > mtu || n < 2 {
			continue
		}
		// A dual-stack socket reports IPv4 peers as ::ffff:a.b.c.d. Unmapped
		// here, once, so the link table, the per-IP rate limit and the logs
		// all agree on what one sender's address is.
		from = netip.AddrPortFrom(from.Addr().Unmap(), from.Port())
		s.handle(buf[:n], from)
	}
}

func (s *Server) handle(b []byte, from netip.AddrPort) {
	switch packetType(b) {
	case typeREG1:
		s.handleREG1(b, from)
	case typeREG2:
		s.handleREG2(b, from)
	case typeKeepalive:
		s.handleKeepalive(b, from)
	case typeACK, typeREG3, typeRegErr, typeRegNGP, typeRegNAK:
		// Receiver-to-sender types arriving the wrong way. Not SRT, so not
		// forwarded; not a request, so not answered.
	default:
		s.handleData(b, from)
	}
}

// handleREG1 creates a group. The sender proposes the first half of the ID
// and this side fills the second half from crypto/rand, so a group ID is
// never wholly chosen by whoever sent the REG1: knowing a sender's half (it
// crosses the network in the clear) is not enough to join its session.
//
// A REG1 whose sender half matches a group that already exists is refused
// with REG_ERR rather than given a second group. A real sender draws 128
// random bytes, so a collision is either a replayed REG1 or someone probing;
// answering it with a fresh group would turn one captured packet into
// unlimited groups, and answering it with the EXISTING group's full ID would
// hand the session's credential to whoever replayed it.
func (s *Server) handleREG1(b []byte, from netip.AddrPort) {
	if len(b) != regLen {
		return
	}
	now := time.Now()
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return
	}
	// Rate-limited before anything else, and refused SILENTLY: a reply to a
	// rate-limited REG1 would still be a packet sent to an address that may be
	// forged.
	if !s.allowREG1(from.Addr(), now) {
		s.mu.Unlock()
		s.log.Debug("srtla REG1 rate-limited", "peer", from.String())
		return
	}
	refuse := ""
	switch {
	case s.links[from] != nil:
		refuse = "address is already a link of a group"
	case len(s.groups) >= s.maxGroups:
		refuse = "group limit reached"
	case s.senderHalfTaken(b[2 : 2+halfID]):
		refuse = "sender half of the group ID is already in use"
	}
	if refuse != "" {
		s.mu.Unlock()
		s.log.Debug("srtla REG1 refused", "peer", from.String(), "reason", refuse)
		s.reply(ctrl(typeRegErr), from)
		return
	}
	// Dialling UDP is a local syscall (no packets), so it is cheap enough to
	// do under the lock, which keeps the limit and collision checks above
	// atomic with the group's creation.
	up, err := net.DialUDP("udp", nil, s.upstream)
	if err != nil {
		s.mu.Unlock()
		s.log.Warn("srtla could not open a socket towards the SRT listener",
			"peer", from.String(), "err", err)
		s.reply(ctrl(typeRegErr), from)
		return
	}
	g := &group{up: up, since: now, creator: from.Addr(), lastActive: from, emptySince: now, windowStart: now}
	copy(g.id[:halfID], b[2:2+halfID])
	_, _ = rand.Read(g.id[halfID:])
	s.groups = append(s.groups, g)
	// The readLoop goroutine is counted in wg for as long as this runs, so
	// this Add can never race a Wait that has already seen zero.
	s.wg.Add(1)
	go s.upstreamLoop(g)
	reply := regPacket(typeREG2, &g.id)
	s.mu.Unlock()
	s.log.Info("srtla group created", "peer", from.String(), "upstreamLocal", up.LocalAddr().String())
	s.reply(reply, from)
}

// senderHalfTaken reports whether any group's ID starts with half. Caller
// holds mu.
func (s *Server) senderHalfTaken(half []byte) bool {
	taken := 0
	for _, g := range s.groups {
		taken |= subtle.ConstantTimeCompare(g.id[:halfID], half)
	}
	return taken == 1
}

// allowREG1 is the per-IP token bucket. Caller holds mu.
func (s *Server) allowREG1(ip netip.Addr, now time.Time) bool {
	bk := s.buckets[ip]
	if bk == nil {
		if len(s.buckets) >= s.reg1MaxIPs {
			return false
		}
		bk = &bucket{tokens: s.reg1Burst, last: now}
		s.buckets[ip] = bk
	}
	bk.refill(now, s.reg1Rate, s.reg1Burst)
	if bk.tokens < 1 {
		return false
	}
	bk.tokens--
	return true
}

func (b *bucket) refill(now time.Time, rate, burst float64) {
	b.tokens = min(burst, b.tokens+now.Sub(b.last).Seconds()*rate)
	b.last = now
}

// handleREG2 adds the sending address to a group as a link.
func (s *Server) handleREG2(b []byte, from netip.AddrPort) {
	if len(b) != regLen {
		return
	}
	now := time.Now()
	s.mu.Lock()
	g := s.groupByID(b[2:])
	var resp uint16
	switch l := s.links[from]; {
	case g == nil:
		// NGP tells the sender to start over with a REG1: the receiver
		// restarted, or the group timed out under it.
		resp = typeRegNGP
	case l != nil && l.group == g:
		// A sender re-sends REG2 until it sees REG3, so a lost REG3 makes this
		// normal; answering it again is the only thing that converges.
		l.lastSeen = now
		resp = typeREG3
	case l != nil:
		resp = typeRegErr
	case len(g.links) >= s.maxLinks:
		resp = typeRegErr
	default:
		l = &link{group: g, addr: from, since: now, lastSeen: now, seqs: make([]uint32, 0, ackBatch)}
		g.links = append(g.links, l)
		g.emptySince = time.Time{}
		s.links[from] = l
		s.log.Info("srtla link registered", "peer", from.String(),
			"upstreamLocal", g.up.LocalAddr().String(), "links", len(g.links))
		resp = typeREG3
	}
	s.mu.Unlock()
	if resp == typeRegErr {
		s.log.Debug("srtla REG2 refused", "peer", from.String())
	}
	s.reply(ctrl(resp), from)
}

// groupByID finds a group by its full ID in constant time per group. Caller
// holds mu.
func (s *Server) groupByID(id []byte) *group {
	var found *group
	for _, g := range s.groups {
		if subtle.ConstantTimeCompare(g.id[:], id) == 1 {
			found = g
		}
	}
	return found
}

// handleKeepalive echoes a registered link's keepalive unchanged. Senders time
// the round trip with whatever they put after the type, so the bytes must
// come back exactly as sent; and the echo is the same size as the request, so
// it amplifies nothing even if it could be provoked -- which it cannot from an
// unregistered address.
func (s *Server) handleKeepalive(b []byte, from netip.AddrPort) {
	s.mu.Lock()
	l := s.links[from]
	if l != nil {
		l.lastSeen = time.Now()
		l.count(len(b))
	}
	s.mu.Unlock()
	if l != nil {
		s.reply(b, from)
	}
}

// handleData forwards a registered link's SRT packet to the group's upstream
// socket.
//
// Every SRT DATA packet's sequence number is collected per link, and every
// tenth triggers an SRTLA ACK listing them back down that link. That is the
// sender's only evidence of what each link actually delivered: it counts what
// it has in flight per link and discounts it as these arrive, and without
// them it would think every link was congested and stop using them. Control
// packets are not data and are not counted.
func (s *Server) handleData(b []byte, from netip.AddrPort) {
	if len(b) < srtHeaderLen {
		// Shorter than any SRT packet and not one of ours: garbage, even from
		// a registered link, and not worth handing to the SRT listener.
		return
	}
	s.mu.Lock()
	l := s.links[from]
	if l == nil {
		s.mu.Unlock()
		return
	}
	l.lastSeen = time.Now()
	l.count(len(b))
	g := l.group
	g.lastActive = from
	var ack []byte
	if isSRTData(b) {
		l.seqs = append(l.seqs, binary.BigEndian.Uint32(b))
		if len(l.seqs) == ackBatch {
			ack = ackPacket(l.seqs)
			l.seqs = l.seqs[:0]
		}
	}
	up := g.up
	s.mu.Unlock()
	// A write error means the group was reaped or Stop ran between the unlock
	// and here; the packet has nowhere to go either way.
	_, _ = up.Write(b)
	if ack != nil {
		s.reply(ack, from)
	}
}

func (l *link) count(n int) {
	l.packets++
	l.bytes += uint64(n)
	l.windowBytes += uint64(n)
}

// upstreamLoop relays what the SRT listener sends to one group back to the
// sender.
//
// SRT ACKs go down EVERY link; everything else goes down the last active one.
// The ACK is what lets the sender's SRT release its send buffer and what its
// congestion control reads round-trip time from, so it must arrive promptly
// whichever links are working at the moment -- a single copy down a link that
// has just died would stall the whole session until a retransmit timer
// noticed. Everything else (handshake replies, NAKs, SRT keepalives) is
// needed once, by a path that just proved it works.
func (s *Server) upstreamLoop(g *group) {
	defer s.wg.Done()
	buf := make([]byte, mtu+1)
	var to []netip.AddrPort
	for {
		n, err := g.up.Read(buf)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			// ECONNREFUSED: an earlier write met an ICMP port-unreachable
			// because the SRT listener was not there. Not fatal -- it may be
			// restarting -- and the socket is still good.
			continue
		}
		if n > mtu || n == 0 {
			continue
		}
		pkt := buf[:n]
		to = to[:0]
		s.mu.Lock()
		if isSRTACK(pkt) {
			for _, l := range g.links {
				to = append(to, l.addr)
			}
		} else if l := s.links[g.lastActive]; l != nil && l.group == g {
			// Checked against the link table rather than trusted: lastActive
			// starts as the REG1 sender, which may never have registered, and
			// nothing goes to an address that is not a link.
			to = append(to, g.lastActive)
		}
		s.mu.Unlock()
		for _, a := range to {
			s.reply(pkt, a)
		}
	}
}

func (s *Server) reply(b []byte, to netip.AddrPort) {
	_, _ = s.conn.WriteToUDPAddrPort(b, to)
}

// housekeeping reaps silent links and empty groups, prunes idle rate-limit
// buckets, and closes each group's rate window for Stats.
func (s *Server) housekeeping() {
	defer s.wg.Done()
	t := time.NewTicker(s.tick)
	defer t.Stop()
	for {
		select {
		case <-s.done:
			return
		case now := <-t.C:
			s.sweep(now)
		}
	}
}

func (s *Server) sweep(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		return
	}
	s.groups = slices.DeleteFunc(s.groups, func(g *group) bool {
		g.links = slices.DeleteFunc(g.links, func(l *link) bool {
			if now.Sub(l.lastSeen) <= s.linkTimeout {
				return false
			}
			delete(s.links, l.addr)
			s.log.Info("srtla link timed out", "peer", l.addr.String(),
				"upstreamLocal", g.up.LocalAddr().String())
			return true
		})
		if len(g.links) == 0 {
			if g.emptySince.IsZero() {
				g.emptySince = now
			}
			if now.Sub(g.emptySince) > s.groupTimeout {
				s.log.Info("srtla group removed", "upstreamLocal", g.up.LocalAddr().String())
				_ = g.up.Close()
				return true
			}
		} else if l := s.links[g.lastActive]; l == nil || l.group != g {
			// The last active link was just reaped; point non-ACK traffic at
			// whichever remaining link was heard from most recently until data
			// says otherwise.
			best := g.links[0]
			for _, l := range g.links[1:] {
				if l.lastSeen.After(best.lastSeen) {
					best = l
				}
			}
			g.lastActive = best.addr
		}
		if el := now.Sub(g.windowStart); el >= s.window {
			var total uint64
			for _, l := range g.links {
				total += l.windowBytes
			}
			for _, l := range g.links {
				l.kbps = float64(l.windowBytes) * 8 / 1000 / el.Seconds()
				l.share = 0
				if total > 0 {
					l.share = float64(l.windowBytes) / float64(total)
				}
				l.windowBytes = 0
			}
			g.windowStart = now
		}
		return false
	})
	for ip, bk := range s.buckets {
		if bk.refill(now, s.reg1Rate, s.reg1Burst); bk.tokens >= s.reg1Burst {
			delete(s.buckets, ip)
		}
	}
}
