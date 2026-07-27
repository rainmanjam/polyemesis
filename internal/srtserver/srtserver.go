// Package srtserver is the one-port SRT ingest: a single listener that serves
// every source, demultiplexed by the publish token an encoder puts in its
// streamid.
//
// The shape is datarhei Core's — one port, many programmes, decided at accept
// time — but the addressing is deliberately different. Core's streamid is
// "<resource>,mode:publish,token:<token>", where the resource is a name the
// PUBLISHER chooses and the token is one value shared by the whole server.
// That splits "which programme" from "may I publish", and it means learning the
// single token lets you publish to any resource, or occupy a name and lock the
// real publisher out. Their own tracker has "Single Token per RTMP Endpoint"
// open against exactly this.
//
// Here the token IS the address:
//
//	streamid=<per-source token>
//
// One opaque value that names the programme and authorises it together. That
// is simpler — no grammar for an encoder to disagree about — and stronger: no
// shared secret, no publisher-chosen names to squat, and an unknown token is
// indistinguishable from a wrong one, so probing cannot tell whether a
// programme exists.
package srtserver

import (
	"crypto/subtle"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	srt "github.com/datarhei/gosrt"
)

const (
	// MaxStreamIDLength bounds what is examined before any lookup. SRT's own
	// streamid limit is 512 bytes; anything near that is not one of our tokens
	// and is refused without touching the store.
	MaxStreamIDLength = 512

	// StaleAfter is how long an established publisher may deliver nothing
	// before a new publisher holding the same token may take its place.
	//
	// This is the difference from Core, which refuses any second publisher for
	// a resource already publishing. That is right for genuine
	// double-publishing and wrong for the case that actually happens: an
	// uplink drops, the dead socket has not been reaped, the encoder reconnects
	// and is locked out with no way to see why. Three seconds is comfortably
	// longer than any real inter-packet gap in a live stream and far shorter
	// than the operator's patience.
	StaleAfter = 3 * time.Second
)

// Sink receives whole MPEG-TS datagrams for one source. relay.Hub satisfies it.
type Sink interface{ Deliver(pkt []byte) }

// Target is what a valid token resolves to.
type Target struct {
	SourceID int64
	Name     string
	Enabled  bool
	// Passphrase is this source's SRT encryption key, or empty. Per-source
	// rather than per-server, which Core's shared configuration cannot express.
	Passphrase string
	// Sink is where this source's bytes go. Nil means the source exists but no
	// engine is running for it, which must be refused rather than accepted into
	// a void.
	Sink Sink
}

// Lookup resolves a publish token to its target.
//
// Implementations MUST compare in constant time and MUST NOT reveal, through
// timing or through the returned error, whether an unmatched token was close to
// a real one. See ConstantTimeLookup.
type Lookup func(token string) (Target, bool)

// Server is the shared listener.
type Server struct {
	log    *slog.Logger
	addr   string
	lookup Lookup

	srv *srt.Server

	mu   sync.Mutex
	live map[int64]*session // by source id

	started atomic.Bool
}

// session is one established publisher.
type session struct {
	sourceID int64
	peer     string
	streamID string
	conn     srt.Conn
	started  time.Time
	// lastData is the unix-nano timestamp of the most recent read. Atomic
	// because the takeover check runs on an accepting goroutine while the read
	// loop is updating it.
	lastData atomic.Int64
	bytes    atomic.Uint64
	// evicted marks a session displaced by a takeover, so its read loop knows
	// not to log the close as a failure.
	evicted atomic.Bool
}

func (s *session) fresh(now time.Time) bool {
	last := s.lastData.Load()
	if last == 0 {
		// Accepted but nothing read yet. Treat the accept itself as activity,
		// so two encoders racing at startup do not evict each other.
		return now.Sub(s.started) < StaleAfter
	}
	return now.Sub(time.Unix(0, last)) < StaleAfter
}

// New builds a server. Nothing binds until Start.
func New(log *slog.Logger, addr string, lookup Lookup) *Server {
	return &Server{
		log:    log.With("component", "srt-ingest"),
		addr:   addr,
		lookup: lookup,
		live:   map[int64]*session{},
	}
}

// Start binds the port and serves until Stop.
func (s *Server) Start() error {
	if s.lookup == nil {
		return errors.New("srtserver: no lookup configured")
	}
	cfg := srt.DefaultConfig()
	// The listener never publishes outward, so a caller asking to subscribe is
	// always refused; see handleConnect.
	s.srv = &srt.Server{
		Addr:            s.addr,
		Config:          &cfg,
		HandleConnect:   s.handleConnect,
		HandlePublish:   s.handlePublish,
		HandleSubscribe: s.handleSubscribe,
	}
	if err := s.srv.Listen(); err != nil {
		return fmt.Errorf("srt listen on %s: %w", s.addr, err)
	}
	s.started.Store(true)
	go func() {
		if err := s.srv.Serve(); err != nil && s.started.Load() {
			s.log.Error("srt server stopped", "err", err)
		}
	}()
	s.log.Info("one-port srt ingest listening", "addr", s.addr)
	return nil
}

// Stop closes the listener and every established publisher.
func (s *Server) Stop() {
	if !s.started.Swap(false) {
		return
	}
	if s.srv != nil {
		s.srv.Shutdown()
	}
	s.mu.Lock()
	sessions := make([]*session, 0, len(s.live))
	for _, sess := range s.live {
		sessions = append(sessions, sess)
	}
	s.live = map[int64]*session{}
	s.mu.Unlock()
	for _, sess := range sessions {
		_ = sess.conn.Close()
	}
}

// handleConnect decides, before the connection is established, whether this
// publisher may proceed — and says why when it may not.
//
// Every refusal carries a specific SRT rejection reason. Core rejects and the
// streamer sees a generic failure in OBS; a code that distinguishes "wrong
// credentials" from "already publishing" from "disabled" is the difference
// between fixing it and guessing.
func (s *Server) handleConnect(req srt.ConnRequest) srt.ConnType {
	peer := req.RemoteAddr().String()
	streamID := req.StreamId()

	if l := len(streamID); l == 0 || l > MaxStreamIDLength {
		req.SetRejectionReason(srt.REJ_ROGUE)
		s.log.Warn("srt publish refused: unusable streamid", "peer", peer, "length", l)
		return srt.REJECT
	}

	target, ok := s.lookup(strings.TrimSpace(streamID))
	if !ok {
		// Deliberately the same outcome as a well-formed token for a source
		// that does not exist: an attacker must not be able to tell the two
		// apart. The token is never logged.
		req.SetRejectionReason(srt.REJ_BADSECRET)
		s.log.Warn("srt publish refused: token not recognised", "peer", peer)
		return srt.REJECT
	}
	if !target.Enabled {
		req.SetRejectionReason(srt.REJ_CLOSE)
		s.log.Warn("srt publish refused: source disabled", "peer", peer, "source", target.Name)
		return srt.REJECT
	}
	if target.Sink == nil {
		// The source exists but no engine is running for it. Accepting would
		// swallow the stream silently, which is worse than refusing.
		req.SetRejectionReason(srt.REJ_RESOURCE)
		s.log.Warn("srt publish refused: no pipeline for source", "peer", peer, "source", target.Name)
		return srt.REJECT
	}

	// Takeover, or refusal. Only a genuinely live incumbent blocks a newcomer.
	s.mu.Lock()
	incumbent, busy := s.live[target.SourceID]
	stillLive := busy && incumbent.fresh(time.Now())
	s.mu.Unlock()
	if stillLive {
		req.SetRejectionReason(srt.REJ_RESOURCE)
		s.log.Warn("srt publish refused: source already publishing",
			"peer", peer, "source", target.Name, "incumbent", incumbent.peer)
		return srt.REJECT
	}

	if req.IsEncrypted() {
		if target.Passphrase == "" {
			req.SetRejectionReason(srt.REJ_UNSECURE)
			s.log.Warn("srt publish refused: encrypted, but the source has no passphrase",
				"peer", peer, "source", target.Name)
			return srt.REJECT
		}
		if err := req.SetPassphrase(target.Passphrase); err != nil {
			req.SetRejectionReason(srt.REJ_BADSECRET)
			s.log.Warn("srt publish refused: passphrase rejected",
				"peer", peer, "source", target.Name)
			return srt.REJECT
		}
	} else if target.Passphrase != "" {
		// The source requires encryption and this publisher offered none.
		req.SetRejectionReason(srt.REJ_UNSECURE)
		s.log.Warn("srt publish refused: source requires encryption",
			"peer", peer, "source", target.Name)
		return srt.REJECT
	}

	return srt.PUBLISH
}

// handleSubscribe refuses every playback request.
//
// This port is an ingest. Playback is HLS through the playout origin, which is
// authenticated and rate-limited; an unauthenticated SRT pull of any programme
// on the box is not something to expose by accident.
func (s *Server) handleSubscribe(conn srt.Conn) {
	s.log.Warn("srt subscribe refused: this port is ingest only",
		"peer", conn.RemoteAddr().String())
	_ = conn.Close()
}

// handlePublish runs one publisher's read loop.
func (s *Server) handlePublish(conn srt.Conn) {
	peer := conn.RemoteAddr().String()
	target, ok := s.lookup(strings.TrimSpace(conn.StreamId()))
	if !ok || target.Sink == nil {
		// The token was valid at connect and is not now: rotated, or the source
		// was deleted in between. Nothing to deliver into.
		_ = conn.Close()
		return
	}

	sess := &session{
		sourceID: target.SourceID,
		peer:     peer,
		streamID: conn.StreamId(),
		conn:     conn,
		started:  time.Now(),
	}

	// Displace a stale incumbent. handleConnect already established that any
	// incumbent is stale, but it is re-checked under the lock because the two
	// run on different goroutines and the incumbent may have recovered.
	s.mu.Lock()
	if old, busy := s.live[target.SourceID]; busy {
		if old.fresh(time.Now()) {
			s.mu.Unlock()
			s.log.Warn("srt publish dropped: incumbent recovered before takeover",
				"peer", peer, "source", target.Name)
			_ = conn.Close()
			return
		}
		old.evicted.Store(true)
		s.log.Info("srt publisher taken over: the previous connection had gone quiet",
			"source", target.Name, "was", old.peer, "now", peer,
			"quietFor", time.Since(time.Unix(0, old.lastData.Load())).Round(time.Millisecond))
		_ = old.conn.Close()
	}
	s.live[target.SourceID] = sess
	s.mu.Unlock()

	s.log.Info("srt publisher connected", "source", target.Name, "peer", peer)

	buf := make([]byte, 2048)
	for {
		n, err := conn.Read(buf)
		if n > 0 {
			sess.lastData.Store(time.Now().UnixNano())
			sess.bytes.Add(uint64(n))
			// One Read is one datagram: SRT in live mode preserves message
			// boundaries, which is what keeps the hub's MPEG-TS continuity
			// measurement meaningful.
			target.Sink.Deliver(buf[:n])
		}
		if err != nil {
			break
		}
	}

	s.mu.Lock()
	if cur, ok := s.live[target.SourceID]; ok && cur == sess {
		delete(s.live, target.SourceID)
	}
	s.mu.Unlock()

	if !sess.evicted.Load() {
		s.log.Info("srt publisher disconnected", "source", target.Name, "peer", peer,
			"ranFor", time.Since(sess.started).Round(time.Second),
			"bytes", sess.bytes.Load())
	}
	_ = conn.Close()
}

// LinkStats is one publisher's uplink health.
//
// Surfaced per source because with several programmes on one install, "why is
// it breaking up" is a question about one encoder's uplink and not about the
// server.
type LinkStats struct {
	SourceID   int64     `json:"sourceId"`
	Peer       string    `json:"peer"`
	Since      time.Time `json:"since"`
	Bytes      uint64    `json:"bytes"`
	RTTMs      float64   `json:"rttMs"`
	LossPkts   uint64    `json:"lossPackets"`
	RetransPkt uint64    `json:"retransPackets"`
}

// Stats reports every established publisher.
func (s *Server) Stats() []LinkStats {
	s.mu.Lock()
	sessions := make([]*session, 0, len(s.live))
	for _, sess := range s.live {
		sessions = append(sessions, sess)
	}
	s.mu.Unlock()

	out := make([]LinkStats, 0, len(sessions))
	for _, sess := range sessions {
		var st srt.Statistics
		sess.conn.Stats(&st)
		out = append(out, LinkStats{
			SourceID:   sess.sourceID,
			Peer:       sess.peer,
			Since:      sess.started,
			Bytes:      sess.bytes.Load(),
			RTTMs:      st.Instantaneous.MsRTT,
			LossPkts:   st.Accumulated.PktRecvLoss,
			RetransPkt: st.Accumulated.PktRetrans,
		})
	}
	return out
}

// Publishing reports whether a source currently has a live publisher.
func (s *Server) Publishing(sourceID int64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.live[sourceID]
	return ok && sess.fresh(time.Now())
}

// ConstantTimeLookup builds a Lookup that compares a presented token against
// every candidate without short-circuiting.
//
// The scan never breaks early and never returns before examining every
// candidate, so the time taken does not depend on which token matched or on how
// many leading bytes a wrong guess got right. A plain map lookup or a SQL
// WHERE token = ? leaks both.
func ConstantTimeLookup(candidates func() []Target, tokenOf func(Target) []string) Lookup {
	return func(presented string) (Target, bool) {
		var (
			found Target
			ok    bool
		)
		want := []byte(presented)
		for _, c := range candidates() {
			for _, tok := range tokenOf(c) {
				if tok == "" {
					continue
				}
				if subtle.ConstantTimeCompare([]byte(tok), want) == 1 {
					found, ok = c, true
				}
			}
		}
		return found, ok
	}
}
