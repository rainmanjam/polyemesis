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

// stream is one SOURCE SLOT: the live publisher's setup messages and everyone
// currently reading it.
//
// Keyed by PublisherKey, NOT by the string the publisher typed. A source has
// several valid keys at once — the current token, the previous one during a
// rotation grace window, and any grandfathered legacy key — and they all mean
// the same programme. Keying this table by the raw string put a publisher who
// used a still-valid old key into a different bucket from the FFmpeg subscribed
// under the new one: admitted, counted as publishing, UI green, bytes fanned
// out to nobody. A refusal would have been better, because a refusal shows red
// in OBS.
type stream struct {
	// setup is replayed, in order, to every new subscriber. Order matters:
	// metadata before sequence headers is what a decoder expects.
	setup []message.Message
	// slots maps a setup message's identity to its position in setup, so a
	// republished sequence start overwrites the one it supersedes rather than
	// being appended after it. Without this the replay list grew for the life of
	// the broadcast and ended with stale configuration ahead of current.
	slots map[string]int
	subs  map[*subscriber]struct{}
}

// resetSetup forgets the previous session's stream configuration.
//
// setup and slots are ONE structure in two fields — slots holds indices into
// setup — so they are cleared together, here, rather than at the call site.
// Clearing only setup left every index dangling and the next sequence start
// wrote past the end of an empty slice, panicking the whole listener on the
// ordinary event of an encoder reconnecting.
func (st *stream) resetSetup() {
	st.setup = nil
	st.slots = map[string]int{}
}

// cacheSetup records a stream-configuration message for replay to late
// subscribers, replacing any earlier message occupying the same slot.
func (st *stream) cacheSetup(msg message.Message) {
	slot, ok := setupSlot(msg)
	if !ok || !isSetup(msg) {
		return
	}
	if st.slots == nil {
		st.slots = map[string]int{}
	}
	if at, seen := st.slots[slot]; seen {
		st.setup[at] = msg
		return
	}
	st.slots[slot] = len(st.setup)
	st.setup = append(st.setup, msg)
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
	done    chan struct{}
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
		for sub := range st.subs {
			subs = append(subs, sub)
		}
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
	case refuseDisabled:
		return "source disabled"
	case refuseNotReady:
		return "no pipeline for source"
	case refuseUnknownKey:
		return "unrecognised"
	}
	return "admitted"
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
	st.subs[sub] = struct{}{}
	replay := append([]message.Message(nil), st.setup...)
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
		s.mu.Lock()
		if cur := s.streams[key]; cur != nil {
			delete(cur.subs, sub)
			// Reap an idle slot. Without this every rotated-out token left a
			// permanent entry holding its cached setup messages, so the table
			// grew for the life of the process.
			if len(cur.subs) == 0 && s.live[key] == nil {
				delete(s.streams, key)
			}
		}
		// Read under the lock that guards the writes, then log outside it.
		// This is safe either way — the delete above means pump can no longer
		// reach this subscriber — but that argument spans two functions, and
		// the copy costs nothing. Logging is I/O and does not belong under a
		// mutex the publisher needs for every message.
		lost := sub.dropped
		s.mu.Unlock()
		if lost > 0 {
			s.log.Warn("rtmp subscriber fell behind and lost messages",
				"component", "rtmp-ingest", "peer", peer, "dropped", lost)
		}
	}()

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

		s.mu.Lock()
		st := s.streams[key]
		if st != nil {
			st.cacheSetup(msg)
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
		}
		s.mu.Unlock()
	}
}

// isSetup reports whether a message is stream setup that a subscriber joining
// later cannot do without.
//
// A subscriber that arrives mid-stream has missed the metadata and the codec
// sequence headers, and without them it has a byte stream it cannot interpret.
// Caching these and replaying them on subscribe is what makes the order of
// "encoder connects" and "FFmpeg connects" not matter — and it WILL vary, since
// an encoder reconnecting mid-session is routine.
//
// Deliberately generous: replaying a message that was not strictly needed costs
// a few bytes once, while missing one costs a subscriber that never decodes.
func isSetup(msg message.Message) bool {
	switch m := msg.(type) {
	case *message.DataAMF0:
		return true // onMetaData and friends
	case *message.Video:
		return m.Type == message.VideoTypeConfig
	case *message.Audio:
		return m.AACType == message.AudioAACTypeConfig
	case *message.VideoExSequenceStart, *message.AudioExSequenceStart,
		*message.AudioExMultichannelConfig:
		return true // Enhanced RTMP setup, including multitrack channel config
	// THE WRAPPER, which is how every track after the first one arrives.
	//
	// E-RTMP multitrack does not send a bare AudioExSequenceStart per track: it
	// sends AudioExMultitrack carrying a TrackID and a Wrapped message, and the
	// sequence start for tracks 2..N is inside that. Matching only the unwrapped
	// types cached the LEGACY track's config and nothing else, so a late-joining
	// subscriber got decoder config for one track and never for the rest — and
	// ffprobe, which is exactly such a subscriber, hung forever instead of
	// failing, because it was still waiting to identify streams it had the data
	// for but no configuration for.
	//
	// That is the whole multitrack feature failing for anything that attaches
	// after the publisher, which is the normal case: the engine's ingest child
	// subscribes when the source is enabled, and the operator hits Start in OBS
	// whenever they like.
	case *message.AudioExMultitrack:
		return isSetup(m.Wrapped)
	case *message.VideoExMultitrack:
		return isSetup(m.Wrapped)
	}
	return false
}

// setupSlot identifies WHICH piece of setup a message is, so a republished one
// replaces its predecessor instead of being appended beside it.
//
// Encoders resend configuration: OBS repeats sequence starts, and any publisher
// that changes a track mid-stream sends a fresh one. Appending blindly grew the
// replay list for the lifetime of the broadcast and handed every new subscriber
// a longer and longer prologue, ending with stale configuration replayed BEFORE
// the current one. Slot-keyed, the list stays at one entry per track per kind
// and always holds the newest.
func setupSlot(msg message.Message) (string, bool) {
	switch m := msg.(type) {
	case *message.DataAMF0:
		return "meta", true
	case *message.Video:
		return "video", true
	case *message.Audio:
		return "audio", true
	case *message.VideoExSequenceStart:
		return "video-ex", true
	case *message.AudioExSequenceStart:
		return "audio-ex", true
	case *message.AudioExMultichannelConfig:
		return "audio-ex-channels", true
	// Per TRACK, which is the point: two tracks' sequence starts are different
	// setup, not the same setup sent twice.
	case *message.AudioExMultitrack:
		inner, ok := setupSlot(m.Wrapped)
		return fmt.Sprintf("audio-mt-%d-%s", m.TrackID, inner), ok
	case *message.VideoExMultitrack:
		inner, ok := setupSlot(m.Wrapped)
		return fmt.Sprintf("video-mt-%d-%s", m.TrackID, inner), ok
	}
	return "", false
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
	for time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
		fresh, found := s.lookup(key)
		if found && fresh.Ready {
			return fresh, true
		}
	}
	return Target{}, false
}

func (s *Server) HasSubscriber(sourceID int64, backup bool) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.streams[PublisherKey{SourceID: sourceID, Backup: backup}]
	return st != nil && len(st.subs) > 0
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
