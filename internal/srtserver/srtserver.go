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
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	srt "github.com/datarhei/gosrt"

	"github.com/rainmanjam/polyemesis/internal/authgate"
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
	// Backup marks the failover standby for a source rather than its primary.
	// Both are reached on this listener and told apart by token, so one source
	// can present two targets -- and a publisher must not be able to take over
	// the primary's slot by presenting the backup's token, or the other way
	// round.
	Backup bool
}

// PublisherKey is a publisher slot on this listener: one per (source, role).
//
// A source has at most two -- its primary and its failover backup -- and they
// are two independent sessions, not two contenders for one. They carry
// deliberately different payloads into deliberately different sinks
// (eng.Hub() vs eng.BackupHub()) and exist in order to run at the same time,
// so every gate here -- admission, takeover, store, delete, Publishing -- keys
// on the role as well as the source id. Keying on the source id alone makes
// the standby and the primary evict each other, which is the failover feature
// failing in the one situation it was built for.
type PublisherKey struct {
	SourceID int64
	Backup   bool
}

// Key identifies a target's publisher slot uniquely.
func (t Target) Key() PublisherKey {
	return PublisherKey{SourceID: t.SourceID, Backup: t.Backup}
}

// role names a slot for an operator. Without it a log line saying an encoder
// disconnected cannot say which encoder, and the two are the whole point.
func role(backup bool) string {
	if backup {
		return "backup"
	}
	return "primary"
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

	// srvs are the bound listeners. A wildcard address binds TWO -- one per
	// address family -- see Start.
	srvs []*srt.Server

	// report is what Start managed to bind, written once by Start and read by
	// anything that wants to describe the listener. Guarded by reportMu rather
	// than by mu, which belongs to the session map and is taken on every
	// accept: a status read must not queue behind a publisher connecting.
	reportMu sync.RWMutex
	report   BindReport

	mu   sync.Mutex
	live map[PublisherKey]*session // by (source id, role)

	started atomic.Bool

	// gate throttles a peer that has presented enough wrong tokens to look
	// like guessing. Shared with internal/rtmpserver -- see internal/authgate
	// for why this is not forked per protocol.
	gate *authgate.Gate
}

// BindReport is which address families the listener asked for and which it got.
//
// It exists because "the listener is up" and "the listener is doing its job"
// stopped being the same sentence the moment a wildcard started binding two
// sockets. Start survives one family failing on purpose -- a container with no
// IPv6 is a legitimate deployment, and refusing to boot there would trade a
// missing feature for an outage -- but survival was previously the whole of the
// record: a Warn in the log, and every programmatic answer afterwards saying
// the listener was bound. An encoder pointed at the family that did not come up
// then fails to connect to a server reporting itself healthy.
type BindReport struct {
	// Requested is every address Start tried, in order.
	Requested []string
	// Bound is the subset that came up.
	Bound []string
	// Failed pairs each address that did not come up with why, so the operator
	// gets the errno rather than a bare "degraded".
	Failed []BindFailure
}

// BindFailure is one address family that did not come up.
type BindFailure struct {
	Addr string
	Err  string
	// Unavailable means the HOST DOES NOT HAVE this address family, as opposed
	// to having it and something being wrong with it.
	//
	// This is the distinction the errno was already carrying and nothing was
	// reading. "address family not supported by protocol" on [::] is an
	// IPv4-only container, which is a completely ordinary way to run this
	// software; "address already in use" on the same address is another process
	// holding the port, which is a fault. Both produce an identical BindReport
	// otherwise, and #105's badge treated them identically -- a permanent orange
	// "Partly bound" on every legitimately IPv4-only host, which teaches the
	// operator to ignore the badge and so costs them the one time it means
	// something.
	//
	// The LOG LINE does not vary on this and deliberately so. It is read by
	// somebody trying to work out why an encoder will not connect, and for them
	// the IPv4-only case IS the answer.
	Unavailable bool
}

// Degraded reports whether some but not all of the requested addresses bound.
//
// Not an error: the server is serving. It is the difference between "serving
// everything it was asked to" and "serving what it could", which is a
// distinction only the operator can act on.
func (r BindReport) Degraded() bool {
	return len(r.Failed) > 0 && len(r.Bound) > 0
}

// Actionable reports a degradation the operator can DO something about, as
// opposed to one that is just what this host is.
//
// Degraded answers "did every requested address bind". That is the right
// question for the log and the wrong one for a badge, because the answer is
// permanently no on an IPv4-only host and there is nothing to fix. This asks
// the question a badge is actually for: is some address family present on this
// machine that this listener asked for and did not get.
//
// False when every failure is an absent family. True when even one of them is
// a port already held, a permission denied, or anything else that says the
// family exists and the bind still failed.
func (r BindReport) Actionable() bool {
	if !r.Degraded() {
		return false
	}
	for _, f := range r.Failed {
		if !f.Unavailable {
			return true
		}
	}
	return false
}

// familyUnavailable reports whether an error from Listen means the host has no
// such address family.
//
// Three errnos, because three kernels spell it differently. EAFNOSUPPORT is the
// straightforward one -- socket(AF_INET6) on a kernel built without it.
// EPFNOSUPPORT is the same refusal from a protocol-family check on the BSDs.
// EADDRNOTAVAIL is what Linux gives when IPv6 is present in the kernel but
// disabled by sysctl, which is how most IPv4-only containers are actually
// configured: the socket opens and there is no [::] to bind it to.
//
// Matched with errors.Is against the syscall values rather than by comparing
// the message text, so a translated or reworded strerror does not turn a normal
// host into an alarm.
//
// EADDRNOTAVAIL against a SPECIFIC address would mean something else -- an IP
// this host does not own, which is a real configuration error. It cannot mean
// that here: this classification is only ever consulted through Actionable,
// which requires Degraded, which requires two or more requested addresses, and
// the only case that produces two is the wildcard expansion into 0.0.0.0 and
// [::]. See bindAddrs.
func familyUnavailable(err error) bool {
	return errors.Is(err, syscall.EAFNOSUPPORT) ||
		errors.Is(err, syscall.EPFNOSUPPORT) ||
		errors.Is(err, syscall.EADDRNOTAVAIL)
}

// Report returns what Start bound. The zero value means Start has not run.
func (s *Server) Report() BindReport {
	s.reportMu.RLock()
	defer s.reportMu.RUnlock()
	return s.report
}

// session is one established publisher.
type session struct {
	// key is the slot this session occupies, role included. A session is
	// identified by (source, role) and never by source alone: the primary and
	// the backup for one source are two sessions that coexist.
	key      PublisherKey
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
		live:   map[PublisherKey]*session{},
		gate:   authgate.New(),
	}
}

// Start binds the port and serves until Stop.
func (s *Server) Start() error {
	if s.lookup == nil {
		return errors.New("srtserver: no lookup configured")
	}
	report := BindReport{Requested: s.bindAddrs()}
	for _, addr := range report.Requested {
		srv, err := s.listenOn(addr)
		if err != nil {
			// One family failing is survivable and common: a host with IPv6
			// disabled cannot bind [::], and refusing to start there would
			// trade a macOS bug for a Linux outage. Both failing is fatal,
			// which is checked after the loop.
			//
			// ERROR RATHER THAN WARN, which is the part #105 was about. The
			// severity was chosen for the common case -- a container without
			// IPv6, where nothing is wrong -- but the log level is read by
			// whoever is trying to work out why an encoder will not connect,
			// and for them this line IS the answer. A half-bound listener is
			// also a half-enforced one: everything downstream reported the
			// listener as up, because "up" was a nil check on a slice that had
			// one entry in it. Recorded in the report as well as logged, so
			// something other than a human tailing stderr can act on it.
			s.log.Error("srt ingest could not bind one address family",
				"addr", addr, "err", err,
				"consequence", "encoders reaching this server over that family will not connect")
			report.Failed = append(report.Failed, BindFailure{
				Addr: addr, Err: err.Error(), Unavailable: familyUnavailable(err),
			})
			continue
		}
		s.srvs = append(s.srvs, srv)
		report.Bound = append(report.Bound, addr)
	}

	s.reportMu.Lock()
	s.report = report
	s.reportMu.Unlock()

	if len(s.srvs) == 0 {
		return fmt.Errorf("srt listen on %s: no address family could be bound", s.addr)
	}

	s.started.Store(true)
	for _, srv := range s.srvs {
		go func(srv *srt.Server) {
			if err := srv.Serve(); err != nil && s.started.Load() {
				s.log.Error("srt server stopped", "err", err)
			}
		}(srv)
	}
	s.log.Info("one-port srt ingest listening",
		"addr", s.addr, "bound", report.Bound, "degraded", report.Degraded())
	return nil
}

// bindAddrs is the list of addresses to listen on.
//
// A WILDCARD BINDS BOTH FAMILIES EXPLICITLY, and that is the fix for issue #28
// rather than a tidiness preference. gosrt picks its network from the address:
//
//	""        -> "udp"   dual-stack, v6only=0
//	0.0.0.0   -> "udp4"  AF_INET
//	::        -> "udp6"  AF_INET6, v6only=1
//
// Only the first is broken, and only on Darwin: gosrt replies through
// golang.org/x/net's ipv4.PacketConn with an IPv4 control message on an
// AF_INET6 socket, which Darwin rejects with "sendmsg: invalid argument" --
// and packetConn.writeToFrom has no error return, so the failure is discarded.
// The datagrams arrive, the handshake never completes, and neither side says
// anything. See datarhei/gosrt#148.
//
// Two sockets on one port is not a conflict: Go sets IPV6_V6ONLY for the
// "udp6" network, so the pair coexists. Measured on darwin before this was
// written, and pinned by TestAWildcardBindsBothFamiliesExplicitly.
//
// An explicit host is left exactly alone -- it already picks a concrete family
// and was never the failing shape.
func (s *Server) bindAddrs() []string {
	host, port, err := net.SplitHostPort(s.addr)
	if err != nil || host != "" {
		return []string{s.addr}
	}
	return []string{net.JoinHostPort("0.0.0.0", port), net.JoinHostPort("::", port)}
}

func (s *Server) listenOn(addr string) (*srt.Server, error) {
	cfg := srt.DefaultConfig()
	// The listener never publishes outward, so a caller asking to subscribe is
	// always refused; see handleConnect.
	srv := &srt.Server{
		Addr:            addr,
		Config:          &cfg,
		HandleConnect:   s.handleConnect,
		HandlePublish:   s.handlePublish,
		HandleSubscribe: s.handleSubscribe,
	}
	if err := srv.Listen(); err != nil {
		return nil, err
	}
	return srv, nil
}

// Stop closes the listener and every established publisher.
func (s *Server) Stop() {
	if !s.started.Swap(false) {
		return
	}
	for _, srv := range s.srvs {
		srv.Shutdown()
	}
	s.srvs = nil
	s.mu.Lock()
	sessions := make([]*session, 0, len(s.live))
	for _, sess := range s.live {
		sessions = append(sessions, sess)
	}
	s.live = map[PublisherKey]*session{}
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

	// Checked before the token is even looked at, so a peer already blocked
	// for guessing wrong tokens does not get a fresh lookup on every retry.
	// See internal/authgate for why this is scoped per peer rather than global.
	host := authgate.PeerHost(req.RemoteAddr())
	if s.gate.Blocked(host) {
		req.SetRejectionReason(srt.REJ_BADSECRET)
		s.log.Debug("srt connect refused: peer is rate-limited after repeated wrong tokens",
			"peer", peer)
		return srt.REJECT
	}

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
		// Only an unrecognised token counts as a guess -- every refusal below
		// this point already proved the caller holds a real one.
		if s.gate.Fail(host) {
			s.log.Warn("srt: peer rate-limited after repeated wrong tokens", "peer", peer)
		}
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

	// Takeover, or refusal. Only a genuinely live incumbent in THIS publisher's
	// own slot blocks it. A source's primary and its standby hold separate
	// slots, so a live primary must never be the reason a backup is refused.
	s.mu.Lock()
	incumbent, busy := s.live[target.Key()]
	stillLive := busy && incumbent.fresh(time.Now())
	s.mu.Unlock()
	if stillLive {
		req.SetRejectionReason(srt.REJ_RESOURCE)
		s.log.Warn("srt publish refused: source already publishing",
			"peer", peer, "source", target.Name, "role", role(target.Backup),
			"incumbent", incumbent.peer)
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

	// A real token was just presented successfully; nothing this peer guessed
	// wrong earlier should still count against it.
	s.gate.Succeed(host)
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
	// Every gate handleConnect applied, applied again.
	//
	// This is a SECOND resolution, not a cached verdict: connect and publish are
	// separate callbacks and the source can change in between. It re-checked
	// the token and the sink but not Enabled, so a source disabled in that
	// window was still admitted and delivered into the hub -- the encoder stays
	// green and the operator's "off" does nothing until the session ends by
	// itself. Silently ignoring the one control an operator has is the failure
	// mode; keep the three checks in the same order as handleConnect so a
	// future gate added there is visibly missing here.
	if !ok || !target.Enabled || target.Sink == nil {
		// Valid at connect and not now: rotated, disabled, or the source was
		// deleted in between. Nothing to deliver into, or nothing that should be.
		_ = conn.Close()
		return
	}

	sess := &session{
		key:      target.Key(),
		peer:     peer,
		streamID: conn.StreamId(),
		conn:     conn,
		started:  time.Now(),
	}

	// Displace a stale incumbent -- the incumbent of this publisher's OWN slot.
	// A quiet primary is never grounds for closing the standby's connection,
	// nor the other way round: the two feeds are independent, and evicting one
	// because the other went quiet takes a working encoder off the air.
	// handleConnect already established that any incumbent is stale, but it is
	// re-checked under the lock because the two run on different goroutines and
	// the incumbent may have recovered.
	s.mu.Lock()
	if old, busy := s.live[target.Key()]; busy {
		if old.fresh(time.Now()) {
			s.mu.Unlock()
			s.log.Warn("srt publish dropped: incumbent recovered before takeover",
				"peer", peer, "source", target.Name, "role", role(target.Backup))
			_ = conn.Close()
			return
		}
		old.evicted.Store(true)
		s.log.Info("srt publisher taken over: the previous connection had gone quiet",
			"source", target.Name, "role", role(target.Backup), "was", old.peer, "now", peer,
			"quietFor", time.Since(time.Unix(0, old.lastData.Load())).Round(time.Millisecond))
		_ = old.conn.Close()
	}
	s.live[target.Key()] = sess
	s.mu.Unlock()

	s.log.Info("srt publisher connected",
		"source", target.Name, "role", role(target.Backup), "peer", peer)

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
	if cur, ok := s.live[target.Key()]; ok && cur == sess {
		delete(s.live, target.Key())
	}
	s.mu.Unlock()

	if !sess.evicted.Load() {
		s.log.Info("srt publisher disconnected",
			"source", target.Name, "role", role(target.Backup), "peer", peer,
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
	SourceID int64 `json:"sourceId"`
	// Backup says which of a source's two publishers this is. One source can
	// report two links at once, and "the uplink is breaking up" is a question
	// about one encoder; without this a reader cannot tell which.
	Backup     bool      `json:"backup"`
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
			SourceID:   sess.key.SourceID,
			Backup:     sess.key.Backup,
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

// Publishing reports whether a source currently has a live publisher in EITHER
// role.
//
// A source with a standby has two slots, so "is this source publishing" has two
// answers and no single one of them is the honest summary. Bytes are arriving
// for this source if either encoder is up, which is what a caller asking about
// the source -- rather than about an encoder -- means. A caller that needs to
// know which encoder wants PublishingRole, or Stats, which now reports both
// links separately.
func (s *Server) Publishing(sourceID int64) bool {
	return s.PublishingRole(sourceID, false) || s.PublishingRole(sourceID, true)
}

// PublishingRole reports whether one particular publisher -- a source's primary
// or its failover standby -- is currently live.
func (s *Server) PublishingRole(sourceID int64, backup bool) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.live[PublisherKey{SourceID: sourceID, Backup: backup}]
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
