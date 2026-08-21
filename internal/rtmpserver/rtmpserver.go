// Package rtmpserver is the shared RTMP listener: one TCP port, many sources,
// told apart by the stream key in the publish URL.
//
// It exists so that how many programmes you can run does not depend on which
// protocol your encoder speaks. Before it, RTMP ingest was `ffmpeg -listen 1` —
// a single-connection receiver that cannot demultiplex by path — so an install
// could carry exactly one RTMP source while SRT carried as many as you liked.
// That asymmetry was an artifact of the implementation, not a decision anyone
// made.
//
// The shape is deliberately the same as internal/srtserver, down to the names:
//
//	srt://host:6000?streamid=<token>     addressed by token
//	rtmp://host:1935/live/<streamkey>    addressed by stream key
//
// ONE PORT, BOTH DIRECTIONS. Encoders publish to it and this install's own
// FFmpeg processes subscribe to it, on the same listener:
//
//	encoder  --publish--> rtmp://host:1935/live/<key>
//	ffmpeg   --play-----> rtmp://127.0.0.1:1935/live/<key>
//
// The first draft of this package had each publisher relayed OUTWARD to a
// per-source FFmpeg listening on its own loopback port. That works, and it is
// what `ffmpeg -listen 1` forced before this existed, but it is not how RTMP
// servers are built and it is not necessary: datarhei Core runs its internal
// RTMP server exactly this way — one port, publishers in, FFmpeg pulling
// `rtmp://127.0.0.1:1935/<path>` back out — and so does every other RTMP
// server. A port per source was solving a problem the pub/sub model does not
// have.
//
// MEDIA IS NEVER PARSED HERE. gortmplib offers a Reader that hands over decoded
// access units; using it would put a muxer in the critical path of every frame
// and make a whole class of bug ours that currently belongs to FFmpeg. This
// forwards RTMP *messages* — so the bytes reaching FFmpeg are the bytes the
// encoder sent, `-map 0 -c copy` downstream is untouched, and Enhanced RTMP
// multitrack works without this package knowing what a track is.
//
// The one thing it must inspect is which messages are STREAM SETUP — the
// onMetaData script message and the codec sequence headers. A subscriber that
// arrives after those have gone past cannot decode anything without them, so
// they are cached per stream and replayed to each new subscriber. That is
// looking at a message type and one byte, not decoding a frame.
package rtmpserver

import (
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/bluenviron/gortmplib"
	"github.com/bluenviron/gortmplib/pkg/message"
)

// handshakeTimeout bounds how long a connection may take to get through the
// RTMP handshake and announce what it wants.
//
// Without it a TCP connection that opens and says nothing holds a goroutine and
// a socket forever, which is a free denial of service against a port that is by
// definition reachable from wherever the encoder is.
const handshakeTimeout = 10 * time.Second

// subscriberQueue is how many messages a slow consumer may fall behind before
// it starts losing them. Deep enough to ride out a GOP, shallow enough that a
// stalled consumer cannot hold meaningful memory.
const subscriberQueue = 256

// Target is what a valid stream key resolves to.
type Target struct {
	SourceID int64
	Name     string
	Enabled  bool
	// Backup marks the failover standby for a source rather than its primary.
	// Both are reached on this listener and told apart by their keys.
	Backup bool
	// Ready reports that something is actually subscribed-or-about-to-be on the
	// other side: an engine exists for this source and its ingest child is
	// dialling this listener back out.
	//
	// srtserver.Target answers the same question with `Sink != nil`, and it has
	// to be answered here too. Without it a publisher whose engine failed to
	// start is admitted into a stream with no subscriber: the encoder goes
	// green, the bytes are fanned out to nobody, and the operator has a healthy
	// OBS and no output with nothing anywhere saying why. Refusing is the worse
	// experience and the better diagnosis.
	Ready bool

	// Pending says a subscriber is EXPECTED here, so a false Ready may be
	// transient and is worth waiting on. It is what makes the readiness grace
	// safe to have at all.
	//
	// Without it the grace applied to every not-ready verdict, and a target is
	// registered for every source whatever its ingest mode. So any valid token
	// for an SRT-mode source was found, enabled, and permanently not ready --
	// which meant every RTMP connect to it burned the full grace before being
	// refused, in parallel, for a state that could never change. The comment on
	// the grace claimed this could not be used to hold connections open. It
	// could, and this is what makes the claim true: only a target whose
	// subscriber is actually on its way is ever waited for.
	Pending bool
}

// PublisherKey is a publisher slot on this listener: one per (source, role).
//
// A source has at most two — its primary and its failover backup — and they are
// two independent sessions, not two contenders for one. They exist in order to
// run at the same time, so every gate here keys on the role as well as the
// source id. Keying on the source id alone makes the standby and the primary
// evict each other, which is the failover feature failing in the one situation
// it was built for.
type PublisherKey struct {
	SourceID int64
	Backup   bool
}

// Key identifies a target's publisher slot uniquely.
func (t Target) Key() PublisherKey {
	return PublisherKey{SourceID: t.SourceID, Backup: t.Backup}
}

func role(backup bool) string {
	if backup {
		return "backup"
	}
	return "primary"
}

// Lookup resolves a stream key to its target.
//
// Implementations MUST compare in constant time and MUST NOT reveal, through
// timing or through the returned error, whether an unmatched key was close to a
// real one. See ConstantTimeLookup.
type Lookup func(streamKey string) (Target, bool)

// ConstantTimeLookup builds a Lookup over a snapshot that compares every
// candidate regardless of when it matches.
//
// The obvious implementation — a map index, or a loop that returns on the first
// hit — leaks through timing: a key sharing a long prefix with a real one takes
// measurably longer to reject than one that differs at the first byte, and that
// is enough to recover a key a character at a time. The map is built by the
// caller and read here; the comparison is what has to be constant, not the
// bookkeeping around it.
func ConstantTimeLookup(targets map[string]Target) Lookup {
	return func(key string) (Target, bool) {
		var found Target
		var ok bool
		for candidate, t := range targets {
			if subtle.ConstantTimeCompare([]byte(candidate), []byte(key)) == 1 {
				found, ok = t, true
			}
		}
		return found, ok
	}
}

// subscriber is one consumer of a stream — in practice this install's own
// FFmpeg, pulling the programme back out of the listener the encoder pushed it
// into.
type subscriber struct {
	ch      chan message.Message
	dropped int
	// done is closed to wake a subscriber that is parked on ch with nothing
	// arriving. Without it the only way serveSubscriber noticed a dead peer was
	// a FAILED WRITE — and with no publisher live there are no writes, so a
	// subscriber whose FFmpeg had already exited stayed parked forever, holding
	// a goroutine and a socket and an entry that pump kept writing into. The
	// engine restarts its ingest child on every reconcile that changes the
	// ingest signature, so this leaked on an ordinary settings change.
	done chan struct{}
	// conn is kept so Stop can close the socket. Closing the channel alone
	// wakes the goroutine but leaves the TCP connection to the departed FFmpeg
	// open.
	conn net.Conn
}

// watchPeer closes the subscriber when its socket does. Run as a goroutine for
// the life of the subscription; returns as soon as the subscriber is closed by
// anything else.
//
// done closes on server Stop and when serveSubscriber's loop exits, which
// leaves the one case that happens on its own: the subscriber's FFmpeg exits
// while NOTHING IS PUBLISHING. There are no writes to fail, so the loop parks
// forever -- and HasSubscriber counts map entries, so Ready stayed true for a
// stream whose only reader was a closed socket, and a publisher was admitted
// into it. That is the green-encoder-no-output failure Ready exists to prevent,
// reached through the one door Ready cannot see.
//
// A read is the signal. Nothing else on a subscriber connection ever reads it
// -- serveSubscriber only writes -- so consuming what the client sends costs
// nothing, and an EOF or a reset is the peer saying it has gone. The bytes are
// discarded: they are window acknowledgements nothing here acts on, and today
// they simply sit unread in the receive buffer.
func (sub *subscriber) watchPeer() {
	if sub.conn == nil {
		return
	}
	buf := make([]byte, 512)
	for {
		if _, err := sub.conn.Read(buf); err != nil {
			sub.close()
			return
		}
		select {
		case <-sub.done:
			return
		default:
		}
	}
}

// close wakes the subscriber and drops its socket. Safe to call twice: Stop and
// the subscriber's own defer race on shutdown.
func (sub *subscriber) close() {
	select {
	case <-sub.done:
	default:
		close(sub.done)
	}
	if sub.conn != nil {
		_ = sub.conn.Close()
	}
}

// session is one live publisher.
type session struct {
	key       PublisherKey
	peer      string
	streamKey string
	started   time.Time
	conn      *gortmplib.ServerConn
}

// Server is the shared listener.
type Server struct {
	log    *slog.Logger
	addr   string
	lookup Lookup

	mu      sync.Mutex
	ln      net.Listener
	live    map[PublisherKey]*session
	streams map[PublisherKey]*stream
	// waiters counts connections currently sitting in the readiness grace, per
	// publisher slot. See maxWaitersPerKey.
	waiters map[PublisherKey]int
	// subChange is closed and replaced whenever the subscriber set changes, so
	// awaitReady is woken by the event it is waiting for rather than polling
	// for it. See subscriberChanged.
	subChange chan struct{}
	done      chan struct{}
	// gate throttles a peer that has presented enough wrong stream keys to
	// look like guessing. See authgate.go.
	gate *authGate
}

// New builds a server. It binds nothing until Start.
func New(log *slog.Logger, addr string, lookup Lookup) *Server {
	return &Server{
		log:     log,
		addr:    addr,
		lookup:  lookup,
		live:    map[PublisherKey]*session{},
		streams: map[PublisherKey]*stream{},
		waiters: map[PublisherKey]int{},
		done:    make(chan struct{}),
		gate:    newAuthGate(),
	}
}

// Start binds the listener and serves until Stop.
func (s *Server) Start() error {
	ln, err := net.Listen("tcp", s.addr)
	if err != nil {
		return fmt.Errorf("rtmp listen %s: %w", s.addr, err)
	}
	s.mu.Lock()
	s.ln = ln
	s.mu.Unlock()

	go s.acceptLoop(ln)
	s.log.Info("one-port rtmp ingest listening", "component", "rtmp-ingest", "addr", s.addr)
	return nil
}

func (s *Server) acceptLoop(ln net.Listener) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			select {
			case <-s.done:
				return // Stop closed the listener; not an error.
			default:
			}
			// A transient accept error must not end the loop: the listener is
			// the only way any RTMP encoder reaches this install, and dropping
			// out of the loop would take every source down until a restart.
			s.log.Warn("rtmp accept failed", "component", "rtmp-ingest", "err", err)
			continue
		}
		go s.handle(conn)
	}
}

// Stop closes the listener and every live session.
func (s *Server) Stop() {
	s.mu.Lock()
	select {
	case <-s.done:
	default:
		close(s.done)
	}
	ln := s.ln
	s.ln = nil
	sessions := make([]*session, 0, len(s.live))
	for _, sess := range s.live {
		sessions = append(sessions, sess)
	}
	// Subscribers too. internal/engine/manager.go justifies its shutdown
	// ordering on the claim that "rtmpserver.Stop closes every subscriber
	// connection, and those subscribers ARE the engines' ingest children".
	// That claim was false: Stop dropped the streams map and left every
	// subscriber goroutine parked on a channel nothing would ever close.
	subs := []*subscriber{}
	for _, st := range s.streams {
		st.mu.Lock()
		for sub := range st.subs {
			subs = append(subs, sub)
		}
		st.mu.Unlock()
	}
	s.live = map[PublisherKey]*session{}
	s.streams = map[PublisherKey]*stream{}
	s.mu.Unlock()

	for _, sub := range subs {
		sub.close()
	}

	if ln != nil {
		_ = ln.Close()
	}
	for _, sess := range sessions {
		if c, ok := sess.conn.RW.(net.Conn); ok {
			_ = c.Close()
		}
	}
}

// StreamKey extracts the addressing key from a publish URL path.
//
// RTMP URLs are `rtmp://host/app/key`, and encoders disagree about which half
// they put where: OBS splits them into "server" and "stream key" fields, others
// take one string. Everything after the first path element is the key, joined
// back together, so `/live/abc` and `/live/sub/abc` both address something
// unambiguous rather than one of them silently addressing nothing.
func StreamKey(u *url.URL) string {
	if u == nil {
		return ""
	}
	p := strings.Trim(u.Path, "/")
	if p == "" {
		return ""
	}
	parts := strings.SplitN(p, "/", 2)
	if len(parts) < 2 {
		// No app segment: the whole path is the key. Accepting this costs
		// nothing and means an encoder that omits /live still reaches its
		// source instead of failing in a way nobody can diagnose.
		return parts[0]
	}
	return parts[1]
}

func (s *Server) handle(conn net.Conn) {
	peer := conn.RemoteAddr().String()
	defer func() { _ = conn.Close() }()

	// Checked before the handshake is even attempted, so a peer already
	// blocked for guessing wrong keys does not get to spend a fresh handshake
	// on every subsequent try. See authgate.go for why this is scoped per
	// peer rather than global.
	host := peerHost(conn.RemoteAddr())
	if s.gate.blocked(host) {
		s.log.Debug("rtmp connect refused: peer is rate-limited after repeated wrong keys",
			"component", "rtmp-ingest", "peer", peer)
		return
	}

	_ = conn.SetDeadline(time.Now().Add(handshakeTimeout))

	sc := &gortmplib.ServerConn{RW: conn}
	if err := sc.Initialize(); err != nil {
		s.log.Debug("rtmp handshake failed", "component", "rtmp-ingest", "peer", peer, "err", err)
		return
	}
	if err := sc.Accept(); err != nil {
		s.log.Debug("rtmp accept failed", "component", "rtmp-ingest", "peer", peer, "err", err)
		return
	}

	key := StreamKey(sc.URL)

	// Subscribers are how this install's own FFmpeg reads a programme back out,
	// on the same port the encoder pushed it into. LOOPBACK ONLY: the public
	// surface of this listener stays publish-only, because playback for viewers
	// is the playout page's job and lives behind authentication. A stream key
	// is a publish credential; it must not double as a viewing one.
	if !sc.Publish {
		if !isLoopback(conn.RemoteAddr()) {
			s.log.Info("rtmp play refused: not from loopback",
				"component", "rtmp-ingest", "peer", peer)
			return
		}
		_ = conn.SetDeadline(time.Time{})
		s.serveSubscriber(sc, key, peer)
		return
	}

	target, found := s.lookup(key)
	// A "not ready" verdict gets a GRACE WAIT before it is believed.
	//
	// Ready means a subscriber is attached, and the ingest child that provides
	// one is a supervised FFmpeg with no reconnect flags: it EXITS whenever its
	// publisher does, and the supervisor respawns it on a 500ms-5s backoff. So
	// the ordinary case of an encoder reconnecting after a network blip arrives
	// exactly while there is no subscriber -- and refusing it there turns a
	// recoverable hiccup into a dropped broadcast, because RTMP carries no
	// typed rejection and the encoder just sees a failed connect.
	//
	// Waiting costs a held TCP connection for at most readyGrace. Refusing
	// costs an operator their stream.
	//
	// Three things keep that from being a way to hold connections open. An
	// unknown key or a disabled source is answered immediately. Target.Pending
	// means only a source whose RTMP subscriber is genuinely on its way is ever
	// waited for -- an SRT-mode source's token is refused at once, because no
	// amount of waiting will make it ready. And waiters are capped per
	// publisher slot, so the one legitimate reconnect gets its grace while a
	// flood against the same key does not multiply it.
	if verdict := admit(target, found); verdict == refuseNotReady && target.Pending && s.enterWait(target.Key()) {
		// The handshake deadline has to be pushed out first. It is set before
		// the handshake and not cleared until admission succeeds, so waiting
		// here spends the SAME budget the handshake already drew on: a slow
		// handshake plus a full grace would blow it, and the session would be
		// admitted and then fail its first read with i/o timeout. Extended by
		// the grace rather than cleared, so a publisher that never becomes
		// ready is still bounded.
		_ = conn.SetDeadline(time.Now().Add(handshakeTimeout + readyGrace))
		t2, ok := s.awaitReady(key, readyGrace)
		s.leaveWait(target.Key())
		if ok {
			target, found = t2, true
		}
	}
	if verdict := admit(target, found); verdict != admitPublish {
		if verdict == refuseUnknownKey {
			// Deliberately says nothing about WHY, and never logs the key nor
			// the source. An unrecognised key and a well-formed key for a
			// source that does not exist have to be the same event, or the log
			// becomes an oracle for whoever can read it.
			s.log.Info("rtmp publish refused", "component", "rtmp-ingest", "peer", peer)
			// Only an unrecognised key counts as a guess. A found-but-disabled
			// or found-but-not-ready target already proved this caller holds a
			// real credential, so neither of those branches reaches here. See
			// authgate.go.
			if s.gate.fail(host) {
				s.log.Warn("rtmp: peer rate-limited after repeated wrong stream keys",
					"component", "rtmp-ingest", "peer", peer)
			}
		} else {
			// Past here the publisher has already proved it holds a valid key,
			// so naming the reason leaks nothing it does not already know --
			// and these are the two failures an operator actually has to
			// diagnose. RTMP carries no typed rejection back to the encoder the
			// way SRT does, so this log line is the ONLY place they can be told
			// apart, and collapsing them leaves "my encoder connects and
			// nothing comes out" with no next step.
			s.log.Info("rtmp publish refused: "+verdict.String(),
				"component", "rtmp-ingest", "peer", peer, "source", target.Name)
		}
		return
	}
	// A real key was just presented successfully; nothing this peer guessed
	// wrong earlier should still count against it.
	s.gate.succeed(host)

	// The handshake deadline must not survive into the session: a live stream
	// is legitimately quiet between keyframes, and an unrenewed deadline would
	// drop the encoder mid-broadcast.
	_ = conn.SetDeadline(time.Time{})

	s.admitSession(sc, target, peer, key)
}

// verdict is what handle decides about a would-be publisher.
type verdict int

const (
	admitPublish verdict = iota
	refuseUnknownKey
	refuseDisabled
	refuseNotReady
)

func (v verdict) String() string {
	switch v {
	case admitPublish:
		return "admitted"
	case refuseDisabled:
		return "source disabled"
	case refuseNotReady:
		return "no pipeline for source"
	case refuseUnknownKey:
		return "unrecognised"
	default:
		// Never falls through to "admitted". The old default did exactly
		// that -- an unhandled value read as a successful publish on the log
		// line that is the ONLY place a refusal reason is told apart (#14,
		// poka-yoke audit) -- which is the one direction a refusal must never
		// silently become. TestVerdictStringHasNoSilentAdmit and
		// TestEveryVerdictConstantIsHandled keep this switch honest as the
		// const block above it grows.
		return fmt.Sprintf("BUG: unhandled verdict %d", int(v))
	}
}

// admit is the whole admission decision, separated from the connection so every
// branch of it is a table test rather than a live RTMP handshake.
//
// Order matters and is not arbitrary: "not ready" is only reachable by a
// publisher that already presented a real key for an enabled source, so it is
// the last thing checked and the only one that describes a fault on this side
// of the wire rather than the encoder's.
func admit(t Target, found bool) verdict {
	switch {
	case !found:
		return refuseUnknownKey
	case !t.Enabled:
		return refuseDisabled
	case !t.Ready:
		return refuseNotReady
	}
	return admitPublish
}

// admitSession takes the publisher slot and relays until the connection ends.
func (s *Server) admitSession(sc *gortmplib.ServerConn, target Target, peer, streamKey string) {
	sess := &session{
		key:       target.Key(),
		peer:      peer,
		streamKey: streamKey,
		started:   time.Now(),
		conn:      sc,
	}

	s.mu.Lock()
	old, busy := s.live[sess.key]
	s.live[sess.key] = sess
	s.mu.Unlock()

	if busy {
		// Last writer wins, which is the behaviour an operator expects when
		// they restart an encoder: the new connection is the one they are
		// looking at. Refusing it would leave a dead session holding the slot
		// until its TCP timeout, and the stream would look broken for reasons
		// invisible from the encoder side.
		s.log.Info("rtmp publisher replaced", "component", "rtmp-ingest",
			"source", target.Name, "role", role(target.Backup), "peer", peer)
		if c, ok := old.conn.RW.(net.Conn); ok {
			_ = c.Close()
		}
	}

	s.log.Info("rtmp publisher connected", "component", "rtmp-ingest",
		"source", target.Name, "role", role(target.Backup), "peer", peer)

	s.mu.Lock()
	if s.streams[sess.key] == nil {
		s.streams[sess.key] = &stream{subs: map[*subscriber]struct{}{}, slots: map[string]int{}}
	}
	// A reconnecting encoder starts a new stream: the old setup messages
	// describe an encode that has ended, and replaying them to a subscriber
	// that joins after the reconnect would describe the wrong thing.
	//
	// BOTH, and it has to be both. slots holds indices INTO setup, so dropping
	// the slice while keeping the map leaves every index dangling — the next
	// sequence start finds its old slot, writes to setup[at], and panics with
	// "index out of range [0] with length 0" against a slice that is now empty.
	// A publisher reconnecting is the ordinary case, not an edge one, so this
	// took down the listener the first time an encoder came back.
	s.streams[sess.key].resetSetup()
	s.mu.Unlock()

	err := s.pump(sc, sess.key)

	s.mu.Lock()
	if cur, ok := s.live[sess.key]; ok && cur == sess {
		delete(s.live, sess.key)
		// Reap the stream slot too, if nothing is reading it.
		//
		// Only serveSubscriber's defer did this, so a publisher that
		// disconnected with no subscriber attached left its stream struct --
		// and the setup messages cached in it -- until the process restarted.
		// Bounded at one entry per PublisherKey and reused by any later
		// subscriber on the same key, so this was small; it was also a stale
		// onMetaData waiting to be replayed to whoever attached next.
		if st := s.streams[sess.key]; st != nil && st.subscriberCount() == 0 {
			delete(s.streams, sess.key)
		}
	}
	s.mu.Unlock()

	s.log.Info("rtmp publisher disconnected", "component", "rtmp-ingest",
		"source", target.Name, "role", role(target.Backup), "peer", peer,
		"held", time.Since(sess.started).Round(time.Second), "err", err)
}

// isLoopback reports whether an address is this machine talking to itself.
//
// The gate on subscribing. A stream key is a PUBLISH credential — it is what an
// encoder is configured with and what appears in an operator's OBS settings —
// and letting it also authorise playback would quietly turn every ingest key
// into a viewing key for anyone who learned it. Playback for viewers is the
// playout page's job, behind authentication.
func isLoopback(a net.Addr) bool {
	host, _, err := net.SplitHostPort(a.String())
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// serveSubscriber attaches a consumer to a stream and writes to it until it
// goes away.
func (s *Server) serveSubscriber(sc *gortmplib.ServerConn, streamKey, peer string) {
	// Resolve through the same Lookup the publisher went through. That is what
	// makes "any key that is valid for this source" reach the same stream: the
	// subscriber and the publisher may legitimately be using different strings.
	target, ok := s.lookup(streamKey)
	if !ok {
		s.log.Info("rtmp subscribe refused: unknown key", "component", "rtmp-ingest", "peer", peer)
		return
	}
	key := target.Key()

	sub := &subscriber{ch: make(chan message.Message, subscriberQueue), done: make(chan struct{})}
	// conn is set BEFORE the subscriber is published into s.streams, and never
	// again. Assigning it after registration is a data race, and not a
	// theoretical one: Stop walks st.subs and reads sub.conn, so the moment
	// this pointer is in the map another goroutine may read the field. The
	// race detector caught it in CI on the very first run.
	//
	// Publish-then-initialise is the bug; the mutex around the map does not
	// help, because the write it needs to order was outside it. Everything
	// mutable on a subscriber after this point lives behind s.mu or is owned
	// by the single goroutine below.
	if c, isConn := sc.RW.(net.Conn); isConn {
		sub.conn = c
	}

	s.mu.Lock()
	st := s.streams[key]
	if st == nil {
		// Subscribing before anything publishes is the NORMAL order: the engine
		// starts FFmpeg when the source is enabled, which is usually well before
		// the operator hits Start in OBS. The stream is created empty and waits.
		st = &stream{subs: map[*subscriber]struct{}{}, slots: map[string]int{}}
		s.streams[key] = st
	}
	st.mu.Lock()
	st.subs[sub] = struct{}{}
	replay := append([]message.Message(nil), st.setup...)
	st.mu.Unlock()
	// This is the event a held publisher is waiting for.
	s.noteSubscriberChange()
	s.mu.Unlock()

	// No play-response sequence is written here, and that is deliberate.
	//
	// A 40-line StreamBegin + onStatus/NetStream.Play.Start sequence lived here
	// on the theory that a player will not surface media without it. Tested
	// against the consumer that actually matters — a real ffprobe subscribing
	// while a real FFmpeg published — it made no difference: removing it left
	// h264/aac arriving exactly the same. It was speculation, so it is gone
	// rather than left in place implying it had been verified.
	//
	// If a stricter client ever turns up that does need it, the message types
	// are in gortmplib/pkg/message (UserControlStreamBegin, CommandAMF0) and
	// TestRealFFmpegPublishesAndRealFFprobeSubscribes is where to prove it.

	defer func() {
		sub.close()
		var lost int
		s.mu.Lock()
		if cur := s.streams[key]; cur != nil {
			// stream.mu inside Server.mu, which is the order everything that
			// needs both uses. Read `dropped` here too: it is written by pump
			// under this same lock.
			cur.mu.Lock()
			delete(cur.subs, sub)
			empty := len(cur.subs) == 0
			lost = sub.dropped
			cur.mu.Unlock()
			// Reap an idle slot. Without this every rotated-out token left a
			// permanent entry holding its cached setup messages, so the table
			// grew for the life of the process.
			if empty && s.live[key] == nil {
				delete(s.streams, key)
			}
			s.noteSubscriberChange()
		}
		s.mu.Unlock()
		// Logged outside both locks: it is I/O, and it does not belong under a
		// mutex the publisher needs for every message.
		if lost > 0 {
			s.log.Warn("rtmp subscriber fell behind and lost messages",
				"component", "rtmp-ingest", "peer", peer, "dropped", lost)
		}
	}()

	// The select below can only notice a dead peer through a FAILED WRITE, and
	// with no publisher live there are no writes. See watchPeer.
	go sub.watchPeer()

	// Catch the late joiner up before anything live: metadata and sequence
	// headers first, in the order the publisher sent them.
	for _, msg := range replay {
		if err := sc.Write(msg); err != nil {
			return
		}
	}
	for {
		select {
		case <-sub.done:
			return
		case msg, open := <-sub.ch:
			if !open {
				return
			}
			if err := sc.Write(msg); err != nil {
				return
			}
		}
	}
}

// pump forwards the publisher's messages to whoever is subscribed.
//
// Message level, not frame level. The only inspection is isSetup below, which
// looks at a message's TYPE to decide whether a late subscriber will need it
// replayed. Nothing here decodes a frame or understands a codec.
func (s *Server) pump(sc *gortmplib.ServerConn, key PublisherKey) error {
	for {
		msg, err := sc.Read()
		if err != nil {
			if errors.Is(err, net.ErrClosed) || errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}

		// The server lock is held only to FIND the stream; the fan-out runs
		// under the stream's own. See stream.mu for why.
		s.mu.Lock()
		st := s.streams[key]
		s.mu.Unlock()
		if st != nil {
			st.mu.Lock()
			st.cacheSetup(msg)
			// READINESS, ENFORCED MID-SESSION.
			//
			// Ready was checked once, at admission, and never again -- so an
			// ingest child that died a second later left this loop dropping
			// every message into an empty subscriber set for the rest of the
			// session. The encoder stays green, the bytes go nowhere, and
			// nothing reports a fault: the green-encoder-no-output failure that
			// Ready exists to prevent, reached after Ready had already said yes.
			//
			// A grace first, because an empty set is ordinary and transient --
			// the ingest child exits whenever its publisher does and is
			// respawned on a 500ms-5s backoff, and a reconcile restarts it on
			// any settings change. Only a sustained absence means nobody is
			// coming.
			//
			// The policy call, stated plainly: the publisher is DROPPED rather
			// than left running. RTMP carries no way to tell an encoder "keep
			// sending, nobody is listening yet", so the alternatives were to
			// stream into the void indefinitely or to disconnect and let the
			// encoder retry into the readiness grace, which is built for exactly
			// this. A disconnect is visible in the encoder and recoverable
			// without anyone intervening; silence is neither.
			if len(st.subs) == 0 {
				if st.emptySince.IsZero() {
					st.emptySince = time.Now()
				} else if time.Since(st.emptySince) > subscriberGrace {
					st.mu.Unlock()
					s.log.Warn("rtmp publisher dropped: nothing has been reading this stream",
						"component", "rtmp-ingest", "for", subscriberGrace)
					return nil
				}
			} else {
				st.emptySince = time.Time{}
			}
			for sub := range st.subs {
				// Non-blocking: a subscriber that cannot keep up is dropped
				// rather than allowed to stall the publisher. One slow consumer
				// must not become backpressure on the encoder, which has
				// nowhere to put the frames it is still producing.
				select {
				case sub.ch <- msg:
				default:
					sub.dropped++
				}
			}
			st.mu.Unlock()
		}
	}
}

// HasSubscriber reports whether anything is currently reading this source's
// stream — in practice, whether the engine's ingest child has dialled in and is
// waiting on the far end.
//
// This exists so Target.Ready can mean what its comment always claimed: that an
// RTMP SUBSCRIBER exists, not merely that an engine record does. Without it a
// publisher was admitted whenever the database said "rtmp", held a full clean
// session, and delivered into a stream nobody read — encoder green, no output,
// and nothing anywhere saying why. That is the exact failure Ready was invented
// to prevent, and it could still happen whenever the ingest child was absent:
// crash-looping, or bailed out early for want of a publish token.
//
// SAFE TO CALL FROM A Lookup. The lookup runs before this server takes s.mu on
// both the publish and the subscribe path, so re-entering here does not
// deadlock. Anything that changes that ordering has to revisit this.
// readyGrace bounds how long a publisher holding a valid key is held while its
// ingest child comes back. Chosen to cover the supervisor's MaxBackoff of 5s
// for the ingest process, plus a moment for the subscriber to attach.
const readyGrace = 6 * time.Second

// subscriberGrace is how long a live publisher may have NOBODY reading it before
// it is disconnected. See the mid-session readiness check in pump.
//
// Longer than readyGrace on purpose. That one is a publisher waiting to be let
// in and costs a held socket; this one ends a broadcast that is already on air.
//
// Sized against the SLOWEST legitimate gap, which is a token rotation, not an
// ordinary respawn. Rotating a token changes the ingest signature, so the engine
// stops the old child on a 12-second deadline and only then starts its
// replacement, which must boot and subscribe. At 15 seconds that left about
// three seconds of margin and a slow stop would have disconnected a perfectly
// healthy encoder for an administrative change nobody thought was risky. 45
// seconds clears the whole sequence with room, and still turns "streaming into
// the void forever" into something bounded.
const subscriberGrace = 45 * time.Second

// maxWaitersPerKey bounds how many connections may sit in the readiness grace
// for the same publisher slot at once.
//
// The grace exists for one encoder reconnecting into the window where its
// ingest child is respawning, which needs exactly one waiter -- two allows for
// an encoder that retried before its first attempt gave up. Past that the extra
// connections are not a reconnect, and holding them multiplies one valid key
// into an arbitrary number of held sockets. Beyond the cap the old behaviour
// applies: refused immediately, which is what happened to every connection
// before the grace existed.
const maxWaitersPerKey = 2

// enterWait claims a waiter slot for a publisher key, reporting whether one was
// available. Every true must be paired with a leaveWait.
func (s *Server) enterWait(k PublisherKey) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.waiters == nil {
		s.waiters = map[PublisherKey]int{}
	}
	if s.waiters[k] >= maxWaitersPerKey {
		return false
	}
	s.waiters[k]++
	return true
}

func (s *Server) leaveWait(k PublisherKey) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.waiters[k] <= 1 {
		// Deleted rather than left at zero: the map is keyed by source and a
		// long-lived install would otherwise accumulate one entry per source
		// that ever reconnected.
		delete(s.waiters, k)
		return
	}
	s.waiters[k]--
}

// awaitReady re-asks the lookup until the target reports Ready or the grace
// expires. It returns the fresh target, because Ready is computed by the engine
// and a stale copy would say nothing new.
//
// Polls rather than waits on a condition: the state it is watching is owned by
// the engine, not by this package, and a channel here would mean the engine had
// to know a listener was waiting on it. 100ms is far below human perception of
// a stream start and far above the cost of a map lookup.
func (s *Server) awaitReady(key string, grace time.Duration) (Target, bool) {
	deadline := time.Now().Add(grace)
	for {
		// Taken BEFORE the lookup, so a subscriber attaching between the two is
		// still seen: the channel is already closed by the time we select.
		changed := s.subscriberChanged()

		if fresh, found := s.lookup(key); found && fresh.Ready {
			return fresh, true
		}

		remaining := time.Until(deadline)
		if remaining <= 0 {
			return Target{}, false
		}
		// Woken by the event, not by a clock. The thing being waited for is a
		// subscriber attaching to THIS listener, which this package knows the
		// moment it happens -- so the common case is one further lookup rather
		// than sixty, and the publisher is admitted as soon as its ingest child
		// arrives instead of up to a poll interval later.
		//
		// The slow tick stays as a backstop. Ready is computed by the engine
		// from things this package cannot observe -- whether an engine exists,
		// what the ingest mode is -- and none of those raise an event here.
		if remaining > readyRecheck {
			remaining = readyRecheck
		}
		timer := time.NewTimer(remaining)
		select {
		case <-changed:
		case <-timer.C:
		case <-s.done:
			timer.Stop()
			return Target{}, false
		}
		timer.Stop()
	}
}

// readyRecheck bounds how long awaitReady sleeps between lookups when nothing
// signals it. The event covers the case that actually happens; this covers the
// rest at 1/10th the old rate.
const readyRecheck = time.Second

// subscriberChanged returns a channel closed the next time the subscriber set
// changes. Taken before the caller's own check, so no change is missed.
func (s *Server) subscriberChanged() <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.subChange == nil {
		s.subChange = make(chan struct{})
	}
	return s.subChange
}

// noteSubscriberChange wakes everything waiting on the subscriber set. Caller
// must hold s.mu.
func (s *Server) noteSubscriberChange() {
	if s.subChange != nil {
		close(s.subChange)
		s.subChange = nil
	}
}

func (s *Server) HasSubscriber(sourceID int64, backup bool) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.streams[PublisherKey{SourceID: sourceID, Backup: backup}]
	return st != nil && st.subscriberCount() > 0
}

// LinkStats is one live publisher, for the API.
type LinkStats struct {
	SourceID  int64     `json:"sourceId"`
	Backup    bool      `json:"backup"`
	Peer      string    `json:"peer"`
	StreamKey string    `json:"-"` // never serialised: it is a credential
	Since     time.Time `json:"since"`
	BytesIn   uint64    `json:"bytesIn"`
}

// Stats reports the live publishers.
func (s *Server) Stats() []LinkStats {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make([]LinkStats, 0, len(s.live))
	for k, sess := range s.live {
		out = append(out, LinkStats{
			SourceID:  k.SourceID,
			Backup:    k.Backup,
			Peer:      sess.peer,
			StreamKey: sess.streamKey,
			Since:     sess.started,
			BytesIn:   sess.conn.BytesReceived(),
		})
	}
	return out
}

// Publishing reports whether a source has a live encoder on either of its two
// slots.
//
// Same pair of signatures as srtserver, on purpose: the API asks both listeners
// the same question about the same source and must not have to remember which
// protocol spells it which way.
func (s *Server) Publishing(sourceID int64) bool {
	return s.PublishingRole(sourceID, false) || s.PublishingRole(sourceID, true)
}

// PublishingRole reports whether one particular publisher -- a source's primary
// or its failover standby -- is currently live.
func (s *Server) PublishingRole(sourceID int64, backup bool) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.live[PublisherKey{SourceID: sourceID, Backup: backup}]
	return ok
}
