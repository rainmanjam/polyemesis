package engine

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/rainmanjam/polyemesis/internal/config"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/events"
	"github.com/rainmanjam/polyemesis/internal/ffmpeg"
	"github.com/rainmanjam/polyemesis/internal/hooks"
	"github.com/rainmanjam/polyemesis/internal/relay"
	"github.com/rainmanjam/polyemesis/internal/rtmpserver"
	"github.com/rainmanjam/polyemesis/internal/srtserver"
	"github.com/rainmanjam/polyemesis/internal/transcribe"
)

// Manager runs one Engine per source.
//
// The alternative was to make a single Engine internally multi-source, which
// would have meant reworking the hub, the reconciler, the destination and
// rendition maps, the silence tier and the failover selector all at once --
// every one of which is already correct for exactly one programme. Running N
// engines reuses all of that as-is, and the only genuinely shared state is the
// relay port allocator.
//
// That allocator is the reason this type has to exist rather than main.go
// keeping a slice: two engines minting their own allocators over the same base
// and span would hand out identical relay ports, and the second programme's
// destinations would quietly bind onto the first programme's traffic. One
// allocator, handed to every engine.
type Manager struct {
	log   *slog.Logger
	cfg   config.Config
	store *db.DB
	tools *ffmpeg.Tools
	bus   *events.Broker
	alloc *relay.PortAllocator

	mu sync.RWMutex
	// srt and rtmp are the one-port listeners, one per protocol, each shared by
	// every source. Nil when the port could not be bound, which is logged
	// rather than fatal: one protocol failing to bind must not take the other
	// -- or the engines -- down with it.
	//
	// Two slots rather than one because they are two sockets with two different
	// jobs on the far side: srtserver delivers into an engine's hub, rtmpserver
	// re-publishes to a subscriber the engine spawns. What they share is the
	// bookkeeping, which is why reconcileListener is written once.
	srt      *srtserver.Server
	srtAddr  string
	rtmp     *rtmpserver.Server
	rtmpAddr string
	engines  map[int64]*Engine
	order    []int64 // source ids in display order, so Default is deterministic
	ctx      context.Context
	started  bool

	// transcriber is remembered rather than applied once, because engines are
	// created after Start whenever a source is added, and a programme whose
	// recordings silently never transcribe is a bug nobody reports.
	tw        *transcribe.Tools
	modelsDir string
	nice      func(name string, args []string) (string, []string)

	// alertAttempts is remembered for the same reason and against the same
	// failure: a source added after the setting was saved would otherwise chase
	// a dead webhook for a different number of tries than every other source,
	// with nothing to show an operator that the two disagree. Zero means
	// "never set", and leaves the alerts package default in place.
	alertAttempts int

	// hooks is remembered for the same reason the transcriber is: engines are
	// created after Start whenever a source is added, and a programme whose
	// hooks silently never fire is a bug nobody reports.
	hooks *hooks.Dispatcher
}

// NewManager builds the manager. No engines exist until Start.
func NewManager(log *slog.Logger, cfg config.Config, store *db.DB, tools *ffmpeg.Tools, bus *events.Broker) *Manager {
	return &Manager{
		log:     log,
		cfg:     cfg,
		store:   store,
		tools:   tools,
		bus:     bus,
		alloc:   relay.NewPortAllocator(relayPortBase, relayPortSpan),
		engines: map[int64]*Engine{},
	}
}

// Start brings up an engine for every source.
//
// A source that fails to start does not stop the others. With several
// programmes on one install, one misconfigured ingest -- a port already taken,
// say -- must not take the rest off the air; that would make adding a source a
// risk to everything already running.
func (m *Manager) Start(ctx context.Context) error {
	m.mu.Lock()
	m.ctx = ctx
	m.started = true
	m.mu.Unlock()

	// Listeners BEFORE engines. This used to be the other way round, with a
	// comment about the token lookup needing to see the engines -- but the
	// lookups resolve m.Engine(id) at connect time, so they were always late-
	// bound and the dependency did not exist.
	//
	// The order does matter, in the opposite direction, and only for RTMP: an
	// RTMP source's ingest child DIALS rtmp://127.0.0.1:1935/live/<token>, so
	// starting the engines first means every one of those children gets
	// connection-refused and crash-loops against a 500ms backoff until the
	// listener binds. Transient at startup, permanent if 1935 never binds at
	// all, and in both cases it fills the log with a failure that has nothing
	// to do with the source.
	m.reconcileSharedIngest()
	if err := m.Sync(); err != nil {
		return err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if len(m.engines) == 0 {
		return fmt.Errorf("no sources to start")
	}
	return nil
}

// Sync makes the set of running engines match the sources table, starting
// engines for new sources and stopping those whose source has gone.
func (m *Manager) Sync() error {
	rows, err := m.store.ListSources()
	if err != nil {
		return err
	}

	m.mu.Lock()
	ctx, started := m.ctx, m.started
	want := make(map[int64]bool, len(rows))
	order := make([]int64, 0, len(rows))
	for _, s := range rows {
		want[s.ID] = true
		order = append(order, s.ID)
	}
	m.order = order

	// Stop first, so a source that was deleted releases its ports and its
	// listener before a new one possibly claims the same numbers.
	var stopping []*Engine
	for id, eng := range m.engines {
		if !want[id] {
			stopping = append(stopping, eng)
			delete(m.engines, id)
		}
	}
	var missing []int64
	for _, s := range rows {
		if _, ok := m.engines[s.ID]; !ok {
			missing = append(missing, s.ID)
		}
	}
	m.mu.Unlock()

	for _, eng := range stopping {
		eng.Stop()
	}
	if !started {
		return nil
	}

	for _, id := range missing {
		eng, err := New(m.log, m.cfg, m.store, m.tools, m.bus, id, m.alloc)
		if err != nil {
			m.log.Error("cannot build engine for source", "source", id, "err", err)
			continue
		}
		m.mu.RLock()
		tw, dir, nice := m.tw, m.modelsDir, m.nice
		attempts := m.alertAttempts
		hookd := m.hooks
		m.mu.RUnlock()
		if tw != nil {
			eng.SetTranscriber(tw, dir, nice)
		}
		// Zero means the operator never set one, which leaves the alerts
		// package default rather than clamping this engine to something no
		// other engine is using. SetRetry tolerates a nil Notifier.
		if attempts > 0 {
			eng.Alerts().SetRetry(attempts)
		}
		if hookd != nil {
			eng.SetHooks(hookd)
		}
		if err := eng.Start(ctx); err != nil {
			m.log.Error("cannot start engine for source", "source", id, "err", err)
			eng.Stop()
			continue
		}
		m.mu.Lock()
		m.engines[id] = eng
		m.mu.Unlock()
	}
	return nil
}

// reconcileSharedIngest brings the one-port listeners up or down to match the
// settings, and rebinds them when a port changes.
//
// They live on the manager because each is ONE listener for every source: an
// engine could not own one without owning the other engines' traffic.
func (m *Manager) reconcileSharedIngest() {
	st, err := m.store.GetSettings()
	if err != nil {
		m.log.Warn("cannot read shared-ingest settings", "err", err)
		return
	}

	m.mu.Lock()
	srt, srtAddr := m.srt, m.srtAddr
	rtmp, rtmpAddr := m.rtmp, m.rtmpAddr
	m.mu.Unlock()

	srt, srtAddr = reconcileListener(m.log, "srt", st.Listeners.SRTPort, true, srt, srtAddr,
		(*srtserver.Server).Stop,
		func(addr string) (*srtserver.Server, error) {
			s := srtserver.New(m.log, addr, m.lookupToken)
			return s, s.Start()
		})
	// BOTH LISTENERS BIND, ALWAYS. The port is the switch, not the source list.
	//
	// RTMP used to bind only when some enabled source was configured for it,
	// which preserved what `ffmpeg -listen 1` did before this package existed.
	// SRT bound unconditionally, which preserved what IT did. Neither was a
	// policy: they were two different histories, and the asymmetry showed. A
	// fresh install that has not chosen an ingest mode still opened 6000 — the
	// exact thing the old comment here justified NOT doing for 1935.
	//
	// Symmetry is also what the ecosystem does and what this project already
	// documents: datarhei Restreamer publishes 1935 and 6000 together, and
	// docs/HARDWARE.md and docs/TROUBLESHOOTING.md have always told operators to
	// run `-p 6000:6000/udp -p 1935:1935`. Binding only one of them made our own
	// install instructions describe a port that might not be listening.
	//
	// The exposure this adds is small and bounded. Both listeners refuse an
	// unknown token or key in constant time, both require Target.Ready before
	// admitting anything, and a connection that opens and says nothing dies on
	// the handshake timeout. What changes is that a host with no firewall now
	// has 1935 reachable where the source list used to close it by accident —
	// so `install.sh`'s RTMP prompt, which controls the ufw rule and the
	// compose publish, is now the only thing that decides reachability. It was
	// always the thing that SAID it did.
	//
	// Port 0 remains the explicit off switch for either protocol.
	rtmpPort := st.Listeners.RTMPPort
	rtmp, rtmpAddr = reconcileListener(m.log, "rtmp", rtmpPort, true, rtmp, rtmpAddr,
		(*rtmpserver.Server).Stop,
		func(addr string) (*rtmpserver.Server, error) {
			s := rtmpserver.New(m.log, addr, m.lookupStreamKey)
			return s, s.Start()
		})

	m.mu.Lock()
	m.srt, m.srtAddr = srt, srtAddr
	m.rtmp, m.rtmpAddr = rtmp, rtmpAddr
	m.mu.Unlock()
}

// reconcileListener brings one shared listener to match its configured port,
// returning what is bound afterwards and at which address.
//
// Written once and instantiated twice rather than copied, because the port
// validation, the rebind-on-change and the bind-failure handling are the same
// three decisions for both protocols. Two near-copies drift on the next fix to
// any of them, and what drift produces here -- one protocol quietly not
// rebinding after a port change -- is invisible until an operator's encoder
// cannot connect and the settings page says it should.
//
// Generic over the server type rather than over an interface so that `cur` can
// be compared against nil: a nil *srtserver.Server placed in an interface is
// not a nil interface, and that trap is exactly how a "no listener" state turns
// into a nil dereference.
func reconcileListener[T any](
	log *slog.Logger,
	proto string,
	port int,
	// wanted separates "this listener is deliberately off" from "this port is
	// wrong". Overloading port 0 for both conflated a fresh install that simply
	// has no RTMP source with a misconfiguration, and logged an ERROR about a
	// port nobody set on every startup.
	wanted bool,
	cur *T, curAddr string,
	stop func(*T),
	start func(addr string) (*T, error),
) (*T, string) {
	// The store does not validate -- Settings.Validate runs in the API handler
	// -- so the manager is the last place between a stored value and a bound
	// socket, and it has to check. Port 0 is the one that matters: it is not
	// an error to the kernel, it means "any free port", so a zero here would
	// bind something random, report itself as listening, and tell the operator
	// their token is enforced while nothing they could publish to exists.
	//
	// There is no longer an "off" to fall back to: these listeners ARE the
	// ingest, on both protocols. An out-of-range port is therefore an error
	// that leaves nothing bound, not a quiet downgrade to something else.
	ok := wanted && port >= 1 && port <= 65535
	if wanted && !ok {
		log.Error("ingest not started: listener port out of range", "proto", proto, "port", port)
	}
	addr := fmt.Sprintf(":%d", port)

	if cur != nil && (!ok || curAddr != addr) {
		stop(cur)
		log.Info("one-port ingest stopped", "proto", proto)
		cur = nil
	}
	if !ok {
		return nil, ""
	}
	if cur != nil {
		return cur, curAddr
	}

	srv, err := start(addr)
	if err != nil {
		// A listener that cannot bind must not take the engines -- or the other
		// protocol's listener -- down with it. An install whose 1935 is held by
		// something else still ingests over SRT, and saying so in the log is
		// the only way anyone finds out.
		log.Error("one-port ingest could not start", "proto", proto, "addr", addr, "err", err)
		return nil, ""
	}
	return srv, addr
}

// lookupToken resolves a publish token to the source that owns it, in constant
// time across every candidate.
//
// The scan covers both the live token and a rotated-out one inside its grace
// window, which is what lets a rotation happen without cutting off an encoder
// already publishing.
func (m *Manager) lookupToken(token string) (srtserver.Target, bool) {
	rows, err := m.store.ListSources()
	if err != nil {
		m.log.Warn("cannot read sources for an srt publish attempt", "err", err)
		return srtserver.Target{}, false
	}
	now := time.Now()
	type key struct {
		id     int64
		backup bool
	}
	targets := make([]srtserver.Target, 0, len(rows))
	tokens := make(map[key][]string, len(rows))
	for _, s := range rows {
		var sink srtserver.Sink
		// A nil *relay.Hub in an interface is not a nil interface, so the engine
		// lookup has to be spelled out rather than assigned straight through --
		// otherwise a source with no engine would present a non-nil Sink and the
		// listener would accept a stream into nothing.
		if eng := m.Engine(s.ID); eng != nil {
			sink = eng.Hub()
		}
		targets = append(targets, srtserver.Target{
			SourceID:   s.ID,
			Name:       s.Name,
			Enabled:    s.Enabled,
			Passphrase: s.Ingest.SRT.Passphrase,
			Sink:       sink,
		})
		valid := s.ValidTokens(now)
		tokens[key{s.ID, false}] = valid

		// The failover standby, addressed by "<token>.backup" on this same
		// listener. Derived rather than stored: one secret per source is one
		// thing to rotate, one thing to leak, and one thing to explain -- and
		// rotating the source's token moves the backup's address with it.
		if eng := m.Engine(s.ID); eng != nil {
			if bh := eng.BackupHub(); bh != nil {
				suffixed := make([]string, 0, len(valid))
				for _, t := range valid {
					suffixed = append(suffixed, t+backupTokenSuffix)
				}
				targets = append(targets, srtserver.Target{
					SourceID:   s.ID,
					Name:       s.Name + " (backup)",
					Enabled:    s.Enabled,
					Passphrase: s.Ingest.SRT.Passphrase,
					Sink:       bh,
					Backup:     true,
				})
				tokens[key{s.ID, true}] = suffixed
			}
		}
	}
	return srtserver.ConstantTimeLookup(
		func() []srtserver.Target { return targets },
		func(t srtserver.Target) []string { return tokens[key{t.SourceID, t.Backup}] },
	)(token)
}

// backupTokenSuffix turns a source's publish token into its standby's address.
// A suffix rather than a second secret: see lookupToken.
const backupTokenSuffix = ".backup"

// lookupStreamKey resolves an RTMP publish path to the source that owns it.
//
// The SAME addressing as SRT, deliberately: `rtmp://host:1935/live/<token>` and
// `srt://host:6000?streamid=<token>` are one concept in two spellings, and
// `<token>.backup` reaches the standby on either. The alternative on the table
// was ingest.rtmp.streamKey, and it cannot be an address: DefaultSettings used
// to hand every source the identical key "stream", nothing in the schema or the
// validator made it unique, and it cannot be rotated without an operator
// editing a text field and cutting their encoder off mid-broadcast. A token is
// 192 bits of crypto/rand, unique, rotatable with a grace window, and never
// logged.
//
// Structural difference from lookupToken worth knowing about:
// rtmpserver.ConstantTimeLookup takes a PREBUILT map while srtserver's takes
// two closures and a token slice per target. The rotation grace has to survive
// either way, so it is expressed here by inserting ONE MAP ENTRY PER VALID
// TOKEN, all pointing at the same Target. Still constant-time -- the map is
// bookkeeping, the comparison inside is what has to not short-circuit.
func (m *Manager) lookupStreamKey(key string) (rtmpserver.Target, bool) {
	rows, err := m.store.ListSources()
	if err != nil {
		m.log.Warn("cannot read sources for an rtmp publish attempt", "err", err)
		return rtmpserver.Target{}, false
	}
	now := time.Now()
	targets := make(map[string]rtmpserver.Target, len(rows)*2)
	// Snapshotted once, outside the loop and outside this server's own lock.
	// Taken before the loop so every target in one lookup answers against the
	// same listener.
	m.mu.RLock()
	rtmp := m.rtmp
	m.mu.RUnlock()

	primaries := make(map[int64]rtmpserver.Target, len(rows))
	for _, s := range rows {
		// Ready is the counterpart of srtserver's `Sink != nil`: it must mean an
		// RTMP SUBSCRIBER exists for this source, not merely that an engine
		// does.
		//
		// `eng != nil` alone was wrong, and wrong in the direction that hides
		// itself. Every source got a Ready RTMP target regardless of its ingest
		// mode, but reconcileIngest takes an early return for IngestSRT and
		// spawns no RTMP subscriber — so publishing to an SRT-only source's
		// token was ADMITTED into a stream with no reader, and flipped the
		// Sources page to "publishing" while that source's real SRT encoder
		// might be down. The backup branch below already gated on mode; this
		// one did not, and its own comment described the bug it had.
		// CONFIRMED SUBSCRIBED, not "the database says rtmp".
		//
		// The comment above is the contract and the expression below used to
		// miss it by one step: an engine record plus a stored mode says a
		// subscriber SHOULD exist, never that one does. Between the two sits
		// every state where reconcileIngest spawned nothing or the child is
		// crash-looping — including its own early return for a source with no
		// publish token. In all of them a publisher was admitted, held a clean
		// session for as long as it liked, and delivered into a stream with no
		// reader. Nothing logged an error, because from the server's side
		// nothing had gone wrong.
		//
		// Asking the listener whether anyone is actually reading closes it. The
		// ingest child dials in when the source is enabled, well before an
		// operator hits Start in their encoder, so the ordinary case is that the
		// subscriber is already waiting and this is true — see the note in
		// serveSubscriber about subscribe-before-publish being the normal order.
		eng := m.Engine(s.ID)
		subscribed := rtmp != nil && rtmp.HasSubscriber(s.ID, false)
		primary := rtmpserver.Target{
			SourceID: s.ID,
			Name:     s.Name,
			Enabled:  s.Enabled,
			Ready:    eng != nil && s.Ingest.Mode == db.IngestRTMP && subscribed,
		}
		primaries[s.ID] = primary
		for _, tok := range s.ValidTokens(now) {
			targets[tok] = primary
		}

		// The standby, on the same listener, addressed by "<token>.backup". Its
		// subscriber is a child of this engine, so it only exists when the
		// engine does and when failover is actually configured to use RTMP --
		// registering it otherwise would accept a backup encoder into a stream
		// with no reader, which is the failure Ready exists to prevent.
		if eng == nil {
			continue
		}
		fo := eng.Settings().Failover
		if !fo.Enabled || !fo.Backup.Enabled || fo.Backup.Mode != db.IngestRTMP {
			continue
		}
		backup := rtmpserver.Target{
			SourceID: s.ID,
			Name:     s.Name + " (backup)",
			Enabled:  s.Enabled,
			Backup:   true,
			// Asked of the listener, exactly as the primary above asks it. This
			// was an unconditional `true` while the comment directly above
			// described the opposite contract: the configuration says a backup
			// subscriber SHOULD exist, never that one does. A backup ingest child
			// that never spawned or is crash-looping left the standby address
			// admitting publishers into a stream with no reader -- the same
			// green-encoder-no-output failure Ready exists to prevent, fixed for
			// the primary in this same change and missed here.
			Ready: rtmp != nil && rtmp.HasSubscriber(s.ID, true),
		}
		for _, tok := range s.ValidTokens(now) {
			targets[tok+backupTokenSuffix] = backup
		}
	}
	// Copied from the primary rather than rebuilt, so a source's two addresses
	// cannot disagree about Enabled or Ready.
	for id, legacy := range legacyRTMPKeys(rows) {
		if primary, ok := primaries[id]; ok {
			targets[legacy] = primary
		}
	}
	return rtmpserver.ConstantTimeLookup(targets)(key)
}

// legacyRTMPKeys maps each source that still answers to its pre-one-port stream
// key. Sources without one are absent.
//
// It exists to keep an upgrading install's encoder on the air. Before
// internal/rtmpserver an RTMP source was addressed by
// `rtmp://host:1935/<app>/<ingest.rtmp.streamKey>`, and checkRTMPExclusive
// guaranteed at most one such source per install. Moving the address to the
// token breaks that encoder on restart, and breaks it SILENTLY: RTMP carries no
// typed rejection reason, so OBS says "could not connect" and nothing anywhere
// says why. Honouring the stored key as a second address costs one map entry
// and takes nobody's broadcast off the air.
//
// It cannot grow into a liability. DefaultSettings mints no stream key, so only
// rows written by an older build carry one, and clearing the field retires it.
//
// Two gates, both load-bearing:
//
//   - A key claimed by more than one source answers for NONE of them.
//     Resolving it arbitrarily is exactly the "one source answering for
//     another" failure this whole change exists to remove, and it is reachable
//     by hand because two operator-typed keys can match.
//   - A key that matches any token, live or lapsed, primary or standby, is
//     refused. Otherwise it could shadow the address the Sources page is
//     telling someone to use. Costs an upgrading install nothing: a 192-bit
//     random string is not what anyone typed into a stream-key box.
func legacyRTMPKeys(rows []*db.Source) map[int64]string {
	claims := map[string]int{}
	reserved := map[string]bool{}
	for _, s := range rows {
		if s.Ingest.Mode == db.IngestRTMP && s.Ingest.RTMP.StreamKey != "" {
			claims[s.Ingest.RTMP.StreamKey]++
		}
		for _, t := range []string{s.Token, s.PrevToken} {
			if t == "" {
				continue
			}
			reserved[t] = true
			reserved[t+backupTokenSuffix] = true
		}
	}
	out := map[int64]string{}
	for _, s := range rows {
		key := s.Ingest.RTMP.StreamKey
		if s.Ingest.Mode != db.IngestRTMP || key == "" {
			continue
		}
		if claims[key] != 1 || reserved[key] {
			continue
		}
		out[s.ID] = key
	}
	return out
}

// LegacyRTMPKey reports the pre-one-port stream key that still reaches this
// source, or "" when none does.
//
// Surfaced so the Sources page can say so. An operator publishing to a
// grandfathered address needs to be told it is one, or the URL on their screen
// and the URL in their encoder disagree forever with nothing to say which is
// real.
func (m *Manager) LegacyRTMPKey(sourceID int64) string {
	rows, err := m.store.ListSources()
	if err != nil {
		return ""
	}
	return legacyRTMPKeys(rows)[sourceID]
}

// SRTLinks reports uplink health for every publisher on the shared SRT
// listener.
func (m *Manager) SRTLinks() []srtserver.LinkStats {
	m.mu.RLock()
	srv := m.srt
	m.mu.RUnlock()
	if srv == nil {
		return nil
	}
	return srv.Stats()
}

// RTMPLinks reports the live publishers on the shared RTMP listener.
//
// Separate from SRTLinks, and returning a different type, because the two
// carry genuinely different facts rather than the same ones under different
// names: SRT reports RTT, loss and retransmits because the protocol measures
// them, and TCP has no such numbers to report. Merging them would mean a
// half-empty struct for every RTMP source and a reader unable to tell "zero
// loss" from "loss is not a thing here".
func (m *Manager) RTMPLinks() []rtmpserver.LinkStats {
	m.mu.RLock()
	srv := m.rtmp
	m.mu.RUnlock()
	if srv == nil {
		return nil
	}
	return srv.Stats()
}

// SharedIngestPublishing reports whether one source has a live publisher on
// either shared listener.
//
// Protocol-neutral on purpose, and it now means what its name says: the caller
// is asking "is an encoder feeding this programme", which is one question
// regardless of what the encoder speaks. It used to consult SRT only, which was
// the same answer only because RTMP had no shared listener to consult.
func (m *Manager) SharedIngestPublishing(sourceID int64) bool {
	m.mu.RLock()
	srt, rtmp := m.srt, m.rtmp
	m.mu.RUnlock()
	return (srt != nil && srt.Publishing(sourceID)) ||
		(rtmp != nil && rtmp.Publishing(sourceID))
}

// Reconcile syncs the engine set, then reconciles each engine.
//
// Every engine is reconciled even when one fails, and the first error is
// returned afterwards: stopping at the first failure would leave the
// programmes after it in the map un-reconciled for reasons that have nothing
// to do with them.
func (m *Manager) Reconcile() error {
	// Before Sync, for the reason Start gives: an engine started against an
	// unbound RTMP port has its ingest child crash-looping on connection-
	// refused. The lookups are late-bound, so nothing is lost by binding first.
	m.reconcileSharedIngest()
	if err := m.Sync(); err != nil {
		return err
	}
	var firstErr error
	for _, eng := range m.Engines() {
		if err := eng.Reconcile(); err != nil {
			m.log.Error("reconcile failed", "source", eng.SourceID(), "err", err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

// Stop shuts every engine down.
func (m *Manager) Stop() {
	m.mu.Lock()
	// Captured here, stopped at the BOTTOM of this function. The ordering is
	// the whole point and it is not obvious, so:
	//
	// srtserver.Stop closes every established publisher, and that publisher's
	// read loop is the only thing calling Sink.Deliver -- it is what feeds the
	// engine hub, and through it every relay subscriber. Stopping it here, as
	// this used to, cut the feed BEFORE Engine.Stop had signalled a single
	// child. Each of those children is an FFmpeg reading udp://127.0.0.1:...,
	// and an FFmpeg blocked in a read on a source that has gone quiet does not
	// act on SIGTERM. All of them missed the 8s grace and were killed as a
	// group.
	//
	// The recorder is where that showed. Measured twice on a live host: the
	// .mkv did not grow by a single byte across the stop -- no trailer -- and
	// ffprobe reported duration=N/A on a file that had been recording for
	// nearly two minutes. deploy/polyemesis.service promises the opposite in
	// as many words, and PLATFORMS.md files the truncation as Windows-only.
	//
	// An A/B against a private port isolates it to this and nothing else:
	// SIGTERM with the input still flowing exits in 0.105s and writes a
	// finalised file; SIGTERM with the input already silent is still alive
	// 15s later. The only variable is whether packets were arriving.
	//
	// So the publishers stay up while the engines come down, and each child
	// gets its SIGTERM on a feed that is still delivering. Once an engine
	// closes its own hub the deliveries become counted drops in Hub.fanout --
	// a write to a closed UDP socket, already handled there -- rather than
	// anything that can panic.
	//
	// Nothing new is accepted in that window: m.engines is emptied and
	// m.started cleared under this lock, so a publisher arriving mid-teardown
	// finds no target and is refused exactly as it would be at any other time.
	//
	// THE SAME INVARIANT HOLDS FOR RTMP, and for the same reason with one link
	// added. rtmpserver.Stop closes every subscriber connection, and those
	// subscribers ARE the engines' ingest children -- the FFmpeg reading
	// rtmp://127.0.0.1:1935/live/<token>. Stopping it first kills each child's
	// input before the child has been signalled, which is the "SIGTERM against
	// a source that has gone quiet" case measured above: 15s and still alive,
	// killed as a group, recording left with no trailer. So it comes down at
	// the bottom too.
	srt, rtmp := m.srt, m.rtmp
	m.srt, m.srtAddr = nil, ""
	m.rtmp, m.rtmpAddr = nil, ""
	engines := make([]*Engine, 0, len(m.engines))
	for _, eng := range m.engines {
		engines = append(engines, eng)
	}
	m.engines = map[int64]*Engine{}
	m.started = false
	m.mu.Unlock()

	for _, eng := range engines {
		eng.Stop()
	}

	// After the engines, so the children finalised against a live feed.
	if srt != nil {
		srt.Stop()
	}
	if rtmp != nil {
		rtmp.Stop()
	}
}

// Engine returns the engine for one source, or nil.
func (m *Manager) Engine(id int64) *Engine {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.engines[id]
}

// Engines returns every running engine in source display order.
func (m *Manager) Engines() []*Engine {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]*Engine, 0, len(m.engines))
	for _, id := range m.order {
		if eng, ok := m.engines[id]; ok {
			out = append(out, eng)
		}
	}
	// Anything started but not yet in order (a source added between a Sync and
	// a list) still has to appear, or it is invisible until the next reconcile.
	for id, eng := range m.engines {
		found := false
		for _, known := range m.order {
			if known == id {
				found = true
				break
			}
		}
		if !found {
			out = append(out, eng)
		}
	}
	return out
}

// Default is the engine an unscoped API call operates on: the first source in
// display order.
//
// Every endpoint that predates sources routes here, which is what lets the
// existing API and UI keep working untouched while multi-source runs
// underneath. It returns nil when nothing is running, and callers must say so
// rather than dereference it.
func (m *Manager) Default() *Engine {
	if engines := m.Engines(); len(engines) > 0 {
		return engines[0]
	}
	return nil
}

// SetTranscriber applies speech transcription to every engine, now and to any
// engine created later.
func (m *Manager) SetTranscriber(w *transcribe.Tools, modelsDir string, nice func(name string, args []string) (string, []string)) {
	m.mu.Lock()
	m.tw, m.modelsDir, m.nice = w, modelsDir, nice
	m.mu.Unlock()
	for _, eng := range m.Engines() {
		eng.SetTranscriber(w, modelsDir, nice)
	}
}

// LastReload is what each engine's most recent reconcile did, in display order.
//
// One report per engine rather than a merged list: a settings save is
// install-wide, and an operator with three programmes needs to know which one
// lost a destination.
func (m *Manager) LastReload() []ReloadReport {
	engines := m.Engines()
	out := make([]ReloadReport, 0, len(engines))
	for _, eng := range engines {
		out = append(out, eng.LastReload())
	}
	return out
}

// SetHooks attaches the shared lifecycle-hook dispatcher to every engine, now
// and to any created later.
func (m *Manager) SetHooks(d *hooks.Dispatcher) {
	m.mu.Lock()
	m.hooks = d
	m.mu.Unlock()
	for _, eng := range m.Engines() {
		eng.SetHooks(d)
	}
}

// SetAlertRetry applies the alert delivery budget to every engine, now and to
// any engine created later.
//
// Remembered rather than applied once, for the same reason SetTranscriber is:
// engines are created and destroyed as sources come and go, so a value pushed
// only into the engines running at save time is silently lost the moment an
// operator adds a source. That failure is invisible -- the new source's alerts
// simply chase a dead endpoint for a different length of time than every other
// source's -- which makes it exactly the kind worth designing out.
func (m *Manager) SetAlertRetry(attempts int) {
	if attempts <= 0 {
		return
	}
	m.mu.Lock()
	m.alertAttempts = attempts
	m.mu.Unlock()
	for _, eng := range m.Engines() {
		eng.Alerts().SetRetry(attempts)
	}
}

// IngestLive reports whether ANY programme is receiving.
//
// Any rather than all, because this gates the post-production governor's
// yield-to-stream behaviour: one live programme is reason enough to keep heavy
// background work off the CPU, and waiting for every source to go live would
// mean an install with two sources never yields at all.
func (m *Manager) IngestLive() bool {
	for _, eng := range m.Engines() {
		if eng.IngestLive() {
			return true
		}
	}
	return false
}

// GPUBusy reports whether any programme is using the GPU.
func (m *Manager) GPUBusy() bool {
	for _, eng := range m.Engines() {
		if eng.GPUBusy() {
			return true
		}
	}
	return false
}

// ListenerBound reports whether the one-port listener for a protocol is
// actually bound.
//
// Distinct from the setting: a listener whose port was already taken leaves the
// setting on while enforcing nothing, and the UI has to be able to tell those
// apart before it tells anyone their token protects an ingest.
//
// It takes the mode rather than answering for "the" listener because there are
// two now, and they fail independently -- 1935 being held by something else
// says nothing about 6000. A caller that did not have to name a protocol would
// get one listener's answer for the other's question.
func (m *Manager) ListenerBound(mode db.IngestMode) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	switch mode {
	case db.IngestSRT:
		return m.srt != nil
	case db.IngestRTMP:
		return m.rtmp != nil
	default:
		// Pull dials out and unset has nothing to bind. Neither has a listener
		// to be enforced by, and saying "yes" would tell an operator a token
		// gates an ingest that no publisher ever reaches.
		return false
	}
}
