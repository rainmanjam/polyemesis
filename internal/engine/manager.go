package engine

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/rainmanjam/polyemesis/internal/config"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/events"
	"github.com/rainmanjam/polyemesis/internal/ffmpeg"
	"github.com/rainmanjam/polyemesis/internal/hooks"
	"github.com/rainmanjam/polyemesis/internal/recording"
	"github.com/rainmanjam/polyemesis/internal/relay"
	"github.com/rainmanjam/polyemesis/internal/rtmpserver"
	"github.com/rainmanjam/polyemesis/internal/srtserver"
	"github.com/rainmanjam/polyemesis/internal/stats"
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

	// host is the one resource sampler for this process, and recman is the one
	// read-only view of the recordings directory. Both are INSTALL-WIDE state
	// that used to be reached through whichever engine happened to be default,
	// which made the answer depend on a programme running — and on WHICH one.
	// Neither is guarded by mu: both are set once in NewManager and are
	// internally synchronised.
	host   *stats.Host
	recman *recording.Manager
	// hostStop ends the sampler goroutine. Set by Start, called by Stop.
	hostStop context.CancelFunc

	// syncMu serialises Sync and reconcileSharedIngest end to end, the way
	// Engine.reconcileMu does for a single engine's Reconcile.
	//
	// It is NOT mu. mu guards the fields and is dropped in the middle of both
	// functions on purpose, because building an engine or binding a listener
	// takes far too long to hold a lock the status endpoints need. That gap is
	// the bug: Manager.Reconcile is reached from several HTTP handlers, so two
	// passes could both observe a source with no engine, both build and Start
	// one, and both write m.engines[id]. The second overwrote the first, and
	// the loser stayed RUNNING -- hub, ingest child and relay ports -- with
	// nothing holding a reference that could stop it. reconcileSharedIngest has
	// the same shape around the listener sockets.
	//
	// Always taken OUTSIDE mu. Nothing may acquire it while holding mu.
	syncMu sync.Mutex

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

	// lifecycle is remembered for the same reason and against a worse failure.
	// A source added after the API server was built would otherwise have no
	// lifecycle observer, so nothing on that programme ever crosses an edge the
	// coordinator can see -- and its YouTube destinations would stream perfectly
	// while their broadcasts stayed in "testing" for the whole show.
	lifecycle LifecycleObserver
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
		host:    stats.NewHost(),
		// NO ffprobe and NO storage guard, and both omissions are the point.
		// This instance answers reads — usage, resolve, delete — for the API,
		// which must be able to ask them on an install where no engine is
		// running. Measuring a segment belongs to the engine that recorded it,
		// and halting a recorder belongs to the engine that owns the child;
		// see the pair of comments on Engine's own recman in engine.go.
		recman: recording.New(log, store, cfg.RecordingsDir(), func() {
			bus.Publish(events.TypeRecordings, nil)
		}),
	}
}

// Tools is the FFmpeg this install detected.
//
// It is a property of the BOX, not of a programme: every engine is handed this
// same pointer. Reading it here rather than off an engine is what lets
// /system, /encoders, /fonts and the upload probe answer on an install with no
// engine running.
func (m *Manager) Tools() *ffmpeg.Tools { return m.tools }

// Host is the process-wide CPU/RAM sampler, running between Start and Stop.
func (m *Manager) Host() *stats.Host { return m.host }

// Recordings is the shared, read-only view of the recordings directory.
//
// READ-ONLY MEANS "does not drive a recorder": it indexes, measures usage,
// confines paths and deletes indexed files, all of which are properties of the
// directory rather than of a programme. It deliberately carries neither the
// ffprobe measurement nor the storage guard the engines' own managers do.
//
// Recordings outlive the source that made them -- recordings.source_id is ON
// DELETE SET NULL by design -- so the library, the clip editor and the
// downloads must keep working when the engine that wrote them is long gone.
func (m *Manager) Recordings() *recording.Manager { return m.recman }

// Start brings up an engine for every source.
//
// A source that fails to start does not stop the others. With several
// programmes on one install, one misconfigured ingest -- a port already taken,
// say -- must not take the rest off the air; that would make adding a source a
// risk to everything already running.
func (m *Manager) Start(ctx context.Context) error {
	hostCtx, stopHost := context.WithCancel(ctx)
	m.mu.Lock()
	m.ctx = ctx
	m.started = true
	m.hostStop = stopHost
	m.mu.Unlock()

	// Before the engines and outside the error paths below: the resource
	// sampler describes the box, so it is worth having on an install where not
	// one source came up -- that is exactly the install an operator is staring
	// at the monitoring page of.
	go m.host.Run(hostCtx)

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
	// TWO DIFFERENT STATES, and this used to refuse both.
	//
	// No sources at all is a fresh install, and it is now a normal way to
	// boot: the operator has not created their first programme yet, and the
	// screen that lets them do so is served by the API this refusal used to
	// prevent from starting. Failing here left them with a process that exits
	// and a log line, which is the one outcome from which they cannot recover.
	//
	// Sources that all failed to build is the other, and it stays an error.
	// Sync logs and continues per source (see the loop it runs), so without
	// this the process would come up looking healthy while publishing nothing,
	// and the operator would find out from the platform.
	if len(m.order) > 0 && len(m.engines) == 0 {
		return fmt.Errorf("no sources to start")
	}
	return nil
}

// Sync makes the set of running engines match the sources table, starting
// engines for new sources and stopping those whose source has gone.
func (m *Manager) Sync() error {
	// One at a time, for the whole of it. See Manager.syncMu: the window
	// between deciding an engine is missing and publishing the one we built is
	// wide enough for a second caller to decide the same thing.
	m.syncMu.Lock()
	defer m.syncMu.Unlock()

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
		eng, err := New(m.log, m.cfg, m.store, m.tools, m.bus, id, m.alloc, m.host)
		if err != nil {
			m.log.Error("cannot build engine for source", "source", id, "err", err)
			continue
		}
		m.mu.RLock()
		tw, dir, nice := m.tw, m.modelsDir, m.nice
		attempts := m.alertAttempts
		hookd := m.hooks
		lifecycle := m.lifecycle
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
		// The half that is easy to forget, and forgetting it is the exact
		// failure SetAlertRetry's comment names: a value pushed only into the
		// engines running at wiring time is silently lost the moment an
		// operator adds a source.
		if lifecycle != nil {
			eng.SetLifecycle(lifecycle)
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
	// Same lock as Sync, for the same reason: the read of m.srt/m.rtmp and the
	// write back are separated by an actual bind, and two callers racing there
	// each start a listener while only one survives in the field.
	m.syncMu.Lock()
	defer m.syncMu.Unlock()

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
			// PUBLISHED BEFORE Start, because Start begins accepting and the
			// Ready gate in lookupStreamKey reads m.rtmp: between Start and the
			// assignment below, a publisher that arrived would find rtmp == nil,
			// score Ready false, and be refused by a listener that was up. The
			// window is microseconds, but before Ready consulted the listener it
			// did not exist at all.
			//
			// Setting it early is safe if Start fails: reconcileListener returns
			// nil for the server on error and the assignment below overwrites
			// this with that nil.
			m.mu.Lock()
			m.rtmp = s
			m.mu.Unlock()
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
				// THE BACKUP'S OWN PASSPHRASE, not the primary's.
				//
				// failover.backup.srt.passphrase is operator-settable and
				// VALIDATED (db.Settings, the same 10..79 rule SRT imposes), so
				// setting it looks exactly like configuring a distinct secret
				// for the standby. Enforcing s.Ingest.SRT.Passphrase here meant
				// it was stored, validated, reported applied -- and checked
				// against the wrong secret. A backup encoder holding the
				// passphrase the operator gave it was refused, and one holding
				// the PRIMARY's was admitted.
				//
				// Falls back to the primary when unset, which is what an install
				// that never configured a separate one already relies on.
				pass := s.Ingest.SRT.Passphrase
				if bp := eng.Settings().Failover.Backup.SRT.Passphrase; bp != "" {
					pass = bp
				}
				targets = append(targets, srtserver.Target{
					SourceID:   s.ID,
					Name:       s.Name + " (backup)",
					Enabled:    s.Enabled,
					Passphrase: pass,
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
		// Pending is the same expression WITHOUT the subscriber: an engine
		// exists and the mode says reconcileIngest will dial this listener, so
		// a missing subscriber is a child in the middle of respawning rather
		// than a permanent state. That distinction is the listener's whole
		// basis for deciding a not-ready verdict is worth waiting on -- an
		// SRT-mode source is registered here too, and waiting for one to become
		// RTMP-ready is waiting forever.
		expected := eng != nil && s.Ingest.Mode == db.IngestRTMP
		primary := rtmpserver.Target{
			SourceID: s.ID,
			Name:     s.Name,
			Enabled:  s.Enabled,
			Ready:    expected && subscribed,
			Pending:  expected,
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
			// Reaching here already means the engine exists and failover is
			// configured to use RTMP, which are exactly the conditions under
			// which a backup subscriber gets spawned. The two `continue`s above
			// are what would otherwise have made this false.
			Pending: true,
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
	stopHost := m.hostStop
	m.hostStop = nil
	m.mu.Unlock()

	// Nil when Stop is reached without a Start, which several tests do.
	if stopHost != nil {
		stopHost()
	}

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

// SetLifecycle attaches the broadcast-lifecycle coordinator to every engine, now
// and to any created later. The Sync half is in reconcile above; without it a
// source added later observes nothing.
func (m *Manager) SetLifecycle(o LifecycleObserver) {
	m.mu.Lock()
	m.lifecycle = o
	m.mu.Unlock()
	for _, eng := range m.Engines() {
		eng.SetLifecycle(o)
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

// ListenerHealth describes HOW WELL a listener is bound, where ListenerBound
// answers only whether it is.
//
// The two are separate because the boolean cannot be fixed without lying. A
// wildcard SRT listener binds one socket per address family and survives one of
// them failing on purpose -- see srtserver.Start -- so "bound" is true for a
// listener serving half the encoders pointed at it. Making ListenerBound answer
// false there would report a working IPv4 ingest as absent, which is worse.
// This reports the third state that actually exists.
type ListenerHealth struct {
	// State is "ok", "degraded", or "" when there is no listener at all. The
	// vocabulary is internal/chat's, deliberately: the UI already knows how to
	// render a degraded badge with a detail beside it, and a second set of
	// words for the same idea would be two things to keep in step.
	State string `json:"state,omitempty"`
	// Detail is why, in the operator's language, and is always set when State
	// is degraded. A bare "degraded" tells nobody what to do.
	Detail string `json:"detail,omitempty"`
}

// Degraded state values, matching internal/chat's State vocabulary.
const (
	listenerOK       = "ok"
	listenerDegraded = "degraded"
)

// ListenerHealth reports how completely the listener for a mode came up.
func (m *Manager) ListenerHealth(mode db.IngestMode) ListenerHealth {
	m.mu.RLock()
	defer m.mu.RUnlock()
	switch mode {
	case db.IngestSRT:
		if m.srt == nil {
			return ListenerHealth{}
		}
		return listenerHealthFor(m.srt.Report())
	case db.IngestRTMP:
		if m.rtmp == nil {
			return ListenerHealth{}
		}
		// RTMP has no half-bound state to report: it is a single TCP listener,
		// so it either came up or it did not, and "did not" is already the nil
		// above. Re-checked against rtmpserver.Start rather than assumed.
		return ListenerHealth{State: listenerOK}
	default:
		return ListenerHealth{}
	}
}

// listenerHealthFor turns a bind report into what the source card shows.
//
// A free function over the report, rather than the body of the switch above,
// because the interesting cases cannot be staged as real listeners. A test can
// occupy the IPv6 wildcard on a port and produce a genuine EADDRINUSE -- the
// api and srtserver suites both do -- but there is no way to make a CI runner
// that HAS IPv6 pretend it does not, and that absent-family case is the whole
// of #105's badge ruling. Separating the decision from the socket is what lets
// it be asserted at all. The wiring above it stays covered end to end by
// TestDegradedSRTListenerIsReportedOnTheSourceCard.
//
// ACTIONABLE RATHER THAN DEGRADED, and that is the ruling. Degraded answers
// "did every requested address bind", which is the right question for the log
// line and the wrong one for a badge. A wildcard expands to 0.0.0.0 AND [::],
// so on an IPv4-only host -- a container with IPv6 off, a completely ordinary
// way to run this -- Degraded is permanently true and there is nothing whatever
// to fix. The UI badges on this field, so such a host wore an orange "Partly
// bound" for its entire life. A warning that is always on is a warning nobody
// reads, and the cost of that lands on the one occasion it means something: the
// port held by another process, a permission denied.
//
// So: ok when every family that failed is a family this machine does not have,
// degraded when even one of them is present and refused the bind anyway. The
// errno already carried that distinction and nothing read it.
//
// The ERROR LOG in srtserver.Start is unchanged, on purpose and by ruling. It
// is read by somebody working out why an encoder will not connect, and for them
// "there is no IPv6 on this host" is the answer rather than the noise. The
// badge and the log line are for two different people at two different moments.
func listenerHealthFor(rep srtserver.BindReport) ListenerHealth {
	if !rep.Actionable() {
		return ListenerHealth{State: listenerOK}
	}
	// The first ACTIONABLE failure, not simply the first. In a mixed report --
	// [::] unavailable and 0.0.0.0 already in use -- naming Failed[0] would put
	// "address family not supported" in front of the operator while the thing
	// they can actually fix went unmentioned.
	f := rep.Failed[0]
	for _, cand := range rep.Failed {
		if !cand.Unavailable {
			f = cand
			break
		}
	}
	return ListenerHealth{
		State: listenerDegraded,
		Detail: fmt.Sprintf(
			"listening on %s but not %s (%s); encoders reaching this server over that address family will not connect",
			strings.Join(rep.Bound, ", "), f.Addr, f.Err),
	}
}
