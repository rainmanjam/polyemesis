package main

import (
	"context"
	"crypto/sha256"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/engine"
	"github.com/rainmanjam/polyemesis/internal/mqtt"
	"github.com/rainmanjam/polyemesis/internal/secrets"
	"github.com/rainmanjam/polyemesis/internal/stats"
	"github.com/rainmanjam/polyemesis/internal/supervisor"
)

// The MQTT telemetry tier is assembled here and nowhere else.
//
// It lives at the composition root rather than inside internal/engine for the
// reason internal/alerts stays free of a socket: nothing in the streaming path
// should be able to block on, or fail because of, a message broker. The engine
// hands over a value; this file turns it into topics.
//
// The whole tier is optional. A broker that is down, misconfigured or gone is a
// line in the log and nothing else -- there is no path from here back into a
// running stream.

// mqttSettingsPoll is how often the stored configuration is re-read.
//
// Polled rather than event-driven because a settings change already goes
// through a reconcile everywhere else in this codebase, and a poll cannot miss
// an edit the way a dropped event can. Five seconds is well under the time it
// takes an operator to switch tabs and wonder why nothing happened.
const mqttSettingsPoll = 5 * time.Second

// mqttEngine is exactly the slice of *engine.Engine that snapshot reads.
//
// It exists so snapshot can be driven from a test. The alternative is a real
// engine.Manager, and a Manager only registers an engine that has STARTED --
// which means binding a listener and spawning children. That cost bought
// nothing here and, worse, it bought no coverage: the fields snapshot copies
// are ordinary status, and the one field that must not be copied verbatim
// (DestState.Error, below) is only ever non-empty on a destination that failed
// to start for reasons a unit test does not get to choose. So the wiring that
// matters most on this file was the wiring nothing could assert. This interface
// is what closes that.
type mqttEngine interface {
	SourceID() int64
	SourceName() string
	Settings() db.Settings
	Status() engine.Status
	// IngestLive and Monitor are the bitrate-based reading of whether the
	// programme is receiving, the one every other surface uses. An SRT source
	// has no ingest process for Status().Ingest to describe.
	IngestLive() bool
	Monitor() *stats.Monitor

	// ScrubDestinationText is on this interface, rather than being applied by
	// the caller, because DestState.Error goes to a RETAINED topic (#160) and
	// the scrub has to be un-droppable from the one place that builds it.
	ScrubDestinationText(id int64, text string) string
}

// mqttRunner keeps a broker connection matching the stored settings.
type mqttRunner struct {
	log   *slog.Logger
	store *db.DB
	box   *secrets.Box

	// engines is how snapshot reaches the live engines: a field rather than a
	// *engine.Manager, so a test can supply an engine whose destinations are in
	// states this process cannot be made to produce on demand.
	engines func() []mqttEngine

	version string
	started time.Time

	// audience is the analytics sampler's viewer picture, nil until the API
	// server exists (it is built after this runner starts) and for a test that
	// does not set it. See mqttAudienceFeed.
	audience *mqttAudienceFeed

	// dial opens a broker connection: mqtt.Connect, through dialMQTT. A field
	// so a test can hand the runner a link whose publishes it can read -- the
	// teardown's clears, its `offline` and its retire happen only on a link
	// that is up, and no test can bring up a real one on a port it knows will
	// answer.
	dial func(ctx context.Context, cfg mqtt.Config, log *slog.Logger) (mqttLink, error)
	// teardownStep bounds each step of a teardown; zero is mqttTeardownStep.
	// Shortened by a test that makes a step run out.
	teardownStep time.Duration

	mu     sync.Mutex
	client mqttLink
	cancel context.CancelFunc
	// tel is the live connection's Telemetry, kept so a teardown can clear the
	// retained tree it published. done closes once the telemetry loop and the
	// discovery announcer have both returned: Clear must not race a tick that
	// would put the state straight back.
	tel  *mqtt.Telemetry
	done chan struct{}
	// sig hashes the settings the live connection was built from. Comparing it
	// is what stops an unrelated settings save from cycling a healthy
	// connection -- the same trick the engine's *Sig fields play.
	sig string
}

// teardownMode is what a teardown does to the retained tree before it goes.
type teardownMode int

const (
	// teardownKeep leaves every retained topic as it is and publishes the
	// clean `offline`. Shutdown, restart, and every reconnect that keeps the
	// same topic tree (a password, broker, keep-alive or discovery change):
	// the tree is about to be written again, and clearing it would blank Home
	// Assistant in between.
	teardownKeep teardownMode = iota
	// teardownClearState deletes the retained state topics and keeps the
	// status (`offline`) and the discovery payload. MQTT switched off: other
	// consumers stop reading a stale "running", and Home Assistant shows the
	// entities unavailable rather than losing them.
	teardownClearState
	// teardownRetire is leaving the tree for good -- the prefix moved:
	// state topics and the status go.
	teardownRetire
	// teardownRetireDevice is teardownRetire when the instance moved too: the
	// old Home Assistant device payload goes with it, or HA keeps a device
	// nothing will ever update.
	teardownRetireDevice
)

// mqttTeardownStep bounds each step of a teardown -- waiting for the loop,
// clearing the state, removing the device, disconnecting -- separately, so a
// broker that is down costs seconds, not a stuck reconcile.
const mqttTeardownStep = 5 * time.Second

// mqttLink is the broker connection the runner drives: *mqtt.Client, or a
// fake in a test. Exactly what the runner and its telemetry call on it.
type mqttLink interface {
	mqtt.Publisher
	Topics() *mqtt.Topics
	Close(ctx context.Context) error
	Retire(ctx context.Context) error
}

// dialMQTT is mqtt.Connect as a dial. The nil check is the point of it: a
// failed Connect returns a nil *mqtt.Client, which as an mqttLink would be a
// non-nil interface holding nil.
func dialMQTT(ctx context.Context, cfg mqtt.Config, log *slog.Logger) (mqttLink, error) {
	c, err := mqtt.Connect(ctx, cfg, log)
	if err != nil {
		return nil, err
	}
	return c, nil
}

// startMQTT begins watching the settings and returns a shutdown function.
//
// It never returns an error, and never blocks on the broker. A publisher that
// refused to start because the broker was rebooting would take the rest of
// polyemesis with it, and the entire design assumes the link comes and goes.
func startMQTT(ctx context.Context, log *slog.Logger, store *db.DB, box *secrets.Box, eng *engine.Manager, version string,
	audience *mqttAudienceFeed) func(context.Context) {
	r := &mqttRunner{
		log: log, store: store, box: box, engines: managerEngines(eng),
		version: version, started: time.Now(), audience: audience, dial: dialMQTT,
	}
	go r.watch(ctx)
	return r.stop
}

// watch reconciles the connection with the settings until the context ends.
func (r *mqttRunner) watch(ctx context.Context) {
	tick := time.NewTicker(mqttSettingsPoll)
	defer tick.Stop()

	r.reconcile(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			r.reconcile(ctx)
		}
	}
}

// reconcile brings the live connection into line with the stored settings.
func (r *mqttRunner) reconcile(ctx context.Context) {
	settings, err := r.store.GetSettings()
	if err != nil {
		// Deliberately does NOT tear the connection down. An unreadable
		// database is a transient problem far more often than it is a decision
		// to stop publishing, and dropping the link would mark the instance
		// offline over a busy SQLite file.
		r.log.Warn("cannot read the MQTT settings; leaving the current connection alone", "err", err)
		return
	}
	cfg := settings.MQTT

	if !cfg.Enabled {
		// Switched off: the retained state goes with the connection. A no-op
		// on every later poll, because there is no connection left to clear.
		r.teardown(ctx, teardownClearState)
		return
	}
	password, err := r.store.GetMQTTPassword(r.box)
	if err != nil {
		r.log.Error("cannot read the MQTT broker password", "err", err)
		return
	}

	sig := mqttSig(cfg, password)

	every := time.Duration(cfg.IntervalSecond) * time.Second

	r.mu.Lock()
	unchanged := r.client != nil && r.sig == sig
	tel := r.tel
	r.mu.Unlock()
	if unchanged {
		// The interval is not part of the signature -- a new one is no reason
		// to cycle a healthy link -- so it is handed to the running loop here.
		if tel != nil && tel.Every() != every {
			tel.SetEvery(every)
			r.log.Info("mqtt publish interval changed", "everySeconds", int(every.Seconds()))
		}
		return
	}

	r.teardown(ctx, r.reconnectMode(cfg))
	r.connect(ctx, cfg, password, sig)
}

// reconnectMode is how a reconnect leaves the tree it was publishing: kept
// when the new connection writes the same topics, retired when the prefix or
// instance moved them (and the device with them, when the instance moved).
// Settings that will not build topics are left to connect to refuse.
func (r *mqttRunner) reconnectMode(cfg db.MQTTSettings) teardownMode {
	r.mu.Lock()
	client := r.client
	r.mu.Unlock()
	if client == nil {
		return teardownKeep
	}
	next, err := mqtt.NewTopics(cfg.Prefix, cfg.Instance)
	if err != nil {
		return teardownKeep
	}
	old := client.Topics()
	switch {
	case next.Discovery() != old.Discovery():
		return teardownRetireDevice
	case next.Status() != old.Status():
		return teardownRetire
	}
	return teardownKeep
}

// mqttSig hashes everything a live connection was built from. Comparing it is
// what stops an unrelated settings save from cycling a healthy connection --
// the same trick the engine's *Sig fields play.
//
// The password is HASHED, not measured. A first draft used len(password), which
// cannot see a change from one password to a different one of the same length
// -- the overwhelmingly common case, since operators rotate to another password
// of the same shape. The runner would then keep the old connection alive
// forever, and the symptom is a broker refusing a credential nobody can see is
// stale. The hash never reaches a log line or an error string.
//
// A free function so it can be tested without a database behind it.
func mqttSig(cfg db.MQTTSettings, password string) string {
	pwSum := sha256.Sum256([]byte(password))
	return fmt.Sprintf("%s\x00%s\x00%x\x00%s\x00%s\x00%s\x00%d\x00%t\x00%t\x00%t",
		cfg.BrokerURL, cfg.Username, pwSum, cfg.Prefix, cfg.Instance,
		cfg.ClientID, cfg.KeepAliveSec, cfg.TLSSkipVerify, cfg.Discovery, cfg.Enabled)
}

// connect opens a new link and starts its telemetry loop.
func (r *mqttRunner) connect(ctx context.Context, cfg db.MQTTSettings, password, sig string) {
	// Said every time the publisher connects, so an operator reading the log
	// for "why is MQTT doing X" finds the risk beside the connection.
	for _, c := range cfg.Cautions() {
		r.log.Warn(c)
	}
	clientID := cfg.ClientID
	if clientID == "" {
		// Derived from the instance, which is already the thing that has to be
		// unique per install. A collision here is the number-one cause of an
		// unexplained reconnect loop: the broker disconnects the older session
		// on every connect and both clients reconnect forever.
		clientID = "polyemesis-" + mqtt.Slug(cfg.Instance)
	}
	loopCtx, cancel := context.WithCancel(ctx)

	dial := r.dial
	if dial == nil {
		dial = dialMQTT
	}
	client, err := dial(loopCtx, mqtt.Config{
		BrokerURL:     cfg.BrokerURL,
		Username:      cfg.Username,
		Password:      password,
		ClientID:      clientID,
		Prefix:        cfg.Prefix,
		Instance:      cfg.Instance,
		KeepAliveSec:  uint16(min(max(cfg.KeepAliveSec, 1), 65535)), //nolint:gosec // clamped into the 16-bit field the protocol defines
		TLSSkipVerify: cfg.TLSSkipVerify,
	}, r.log)
	if err != nil {
		cancel()
		// Configuration this bad was already refused by Settings.Validate, so
		// reaching here means something changed underneath it. Logged and
		// retried on the next poll rather than retried in a tight loop.
		r.log.Error("cannot start the MQTT publisher", "err", err)
		return
	}

	tel := mqtt.NewTelemetry(client, client.Topics(), r.log)
	tel.SetEvery(time.Duration(cfg.IntervalSecond) * time.Second)

	done := make(chan struct{})
	r.mu.Lock()
	r.client, r.cancel, r.sig, r.tel, r.done = client, cancel, sig, tel, done
	r.mu.Unlock()

	var wg sync.WaitGroup
	wg.Go(func() { tel.Loop(loopCtx, r.snapshot) })
	if cfg.Discovery {
		wg.Go(func() { r.announceDiscovery(loopCtx, client, tel) })
	}
	go func() {
		wg.Wait()
		close(done)
	}()
	r.log.Info("mqtt telemetry started",
		"instance", client.Topics().Instance(), "everySeconds", int(tel.Every().Seconds()))
}

// announceDiscovery publishes the Home Assistant device payload once the link
// is up, again after every reconnect, and again whenever the set of entities
// changes -- a destination added or deleted mid-connection. PublishDiscovery
// sends nothing when the set is the one already announced, and sends a removal
// stub for each entity that went, so asking every tick costs one snapshot and
// usually no publish.
//
// Separate from the telemetry loop because discovery is a different contract:
// it describes the entities, it changes only when the topology does, and a
// consumer that is not Home Assistant does not want it at all.
func (r *mqttRunner) announceDiscovery(ctx context.Context, client mqttLink, tel *mqtt.Telemetry) {
	tick := time.NewTicker(mqttSettingsPoll)
	defer tick.Stop()
	wasUp := false
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			up := client.Connected()
			if !up {
				wasUp = false
				continue
			}
			if !wasUp {
				// A broker that restarted lost the retained payload.
				tel.ForgetDiscovery()
				wasUp = true
			}
			if err := tel.PublishDiscovery(ctx, r.snapshot()); err != nil {
				r.log.Warn("could not publish Home Assistant discovery", "err", err)
			}
		}
	}
}

// teardown ends the live connection, first clearing what mode says to.
//
// THE LOOP STOPS FIRST, AND IS WAITED FOR. Clearing while the telemetry loop
// can still tick would have the next tick republish the state just deleted.
// Each step is bounded by mqttTeardownStep on a context of its own, not
// derived from ctx: this runs on the shutdown path too, where ctx is already
// cancelled and a clean `offline` is exactly what must still get out. A broker
// that is unreachable is logged and the teardown goes on.
//
// THE DEVICE REMOVAL IS A STEP OF ITS OWN, not the tail of the state clear.
// Clear waits for an acknowledgement per retained topic, and a slow broker can
// spend the whole step on them; sharing that context handed ClearDiscovery one
// already expired, and the old Home Assistant device outlived the instance
// move it was there for.
func (r *mqttRunner) teardown(ctx context.Context, mode teardownMode) {
	r.mu.Lock()
	client, cancel, tel, done := r.client, r.cancel, r.tel, r.done
	r.client, r.cancel, r.sig, r.tel, r.done = nil, nil, "", nil, nil
	r.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if client == nil {
		return
	}
	bound := r.teardownStep
	if bound <= 0 {
		bound = mqttTeardownStep
	}
	step := func() (context.Context, context.CancelFunc) {
		return context.WithTimeout(context.WithoutCancel(ctx), bound)
	}
	if done != nil {
		wctx, finish := step()
		select {
		case <-done:
		case <-wctx.Done():
			r.log.Warn("the MQTT telemetry loop did not stop in time; tearing the connection down anyway")
		}
		finish()
	}
	if mode != teardownKeep && tel != nil {
		if client.Connected() {
			cctx, finish := step()
			if err := tel.Clear(cctx); err != nil {
				r.log.Warn("could not clear every retained MQTT topic", "err", err)
			}
			finish()
			if mode == teardownRetireDevice {
				dctx, finish := step()
				if err := tel.ClearDiscovery(dctx); err != nil {
					r.log.Warn("could not remove the old Home Assistant device", "err", err)
				}
				finish()
			}
		} else {
			// Nothing can be deleted over a link that is down, and the next
			// process does not know this tree was published. Said, so the
			// operator who switched MQTT off or moved it knows what to remove.
			r.log.Warn("the MQTT broker was not connected, so the retained topics this install published "+
				"were left on it; remove them with mosquitto_sub --remove-retained (see docs/MQTT.md)",
				"topics", client.Topics().All())
		}
	}
	closeCtx, finish := step()
	defer finish()
	closeFn := client.Close
	if mode == teardownRetire || mode == teardownRetireDevice {
		closeFn = client.Retire
	}
	if err := closeFn(closeCtx); err != nil {
		r.log.Warn("mqtt disconnect was not clean", "err", err)
	}
}

// stop is the shutdown hook handed back to main. It never clears anything: a
// restart is about to publish the same tree again.
func (r *mqttRunner) stop(ctx context.Context) { r.teardown(ctx, teardownKeep) }

// managerEngines adapts the real manager to mqttEngine.
//
// The loop is the whole of it: Go will not convert []*engine.Engine to
// []mqttEngine, and there is nothing else this indirection does.
func managerEngines(m *engine.Manager) func() []mqttEngine {
	return func() []mqttEngine {
		live := m.Engines()
		out := make([]mqttEngine, 0, len(live))
		for _, e := range live {
			out = append(out, e)
		}
		return out
	}
}

// snapshot flattens every engine into the shape the publisher sends.
//
// Nothing secret crosses this boundary. A destination contributes its name,
// platform and error, never its URL or stream key -- the same rule
// alertSnapshot follows, and the reason mqtt.DestState has no field for one.
func (r *mqttRunner) snapshot() mqtt.Snapshot {
	now := time.Now()
	engines := r.engines()

	snap := mqtt.Snapshot{
		Host: mqtt.HostState{
			Version:   r.version,
			StartedAt: r.started,
			UptimeSec: now.Sub(r.started).Seconds(),
			Sources:   len(engines),
			At:        now,
		},
	}

	snap.Storage = r.storageStates(now)

	for _, e := range engines {
		st := e.Status()
		src := mqtt.SourceState{
			ID:          e.SourceID(),
			Name:        e.SourceName(),
			Slug:        mqtt.Slug(e.SourceName()),
			LossPercent: st.Relay.LossPercent,
			Dests:       len(st.Destinations),
			At:          now,
		}
		// Liveness from the relay's bitrate, not from process state: an RTMP
		// ingest child sits in "running" for as long as it waits for a
		// publisher, and an SRT source has no ingest process at all -- the
		// shared listener feeds its hub directly. This was "RxBytes > 0 and the
		// child is running", which read an SRT programme never live and an
		// RTMP one live for ever after its first show (RxBytes is a total).
		// IngestLive is the answer the dashboard, webhooks and alerts use.
		src.Live = e.IngestLive()
		if st.Ingest != nil {
			src.IngestError = st.Ingest.LastError
			src.UptimeSec = st.Ingest.UptimeSec
			src.Restarts = st.Ingest.Restarts
			src.BitrateKbps = st.Ingest.Progress.BitrateKbps
		} else if b := e.Monitor().Bitrate(); len(b) > 0 {
			// No child to report a bitrate: an SRT source. The relay's own
			// 1 Hz sample, the figure the scrape publishes.
			src.BitrateKbps = b[len(b)-1].Kbps
		}
		src.IngestMode = string(e.Settings().Ingest.Mode)
		src.Recording = st.Recorder != nil && st.Recorder.State == supervisor.StateRunning
		if st.Failover != nil {
			src.Failover = string(st.Failover.Active)
			src.FailoverReason = st.Failover.Reason
			src.Degraded = st.Failover.Degraded
		}

		item := mqtt.SourceSnapshot{}
		for _, d := range st.Destinations {
			// The engine's own verdict, the one alerts and hooks judge,
			// delay rule included. Nothing here decides what "up" means.
			judged := d.AlertState()
			running := judged.Running
			if running {
				src.DestsUp++
			}
			ds := mqtt.DestState{
				ID: d.ID, Name: d.Name, Slug: mqtt.Slug(d.Name),
				Platform: string(d.Platform), Kind: string(d.Kind),
				Enabled: judged.Enabled, Running: running,
				// Scrubbed HERE and not in Status (#160). Everything else on
				// this topic tree was already covered -- SourceState.IngestError
				// comes from supervisor Status.LastError, which Process.scrub
				// has already put through the ingest's exact secret set -- and
				// this one field arrived from the engine untouched. Same
				// retained topic, so the same rule has to hold: a retained
				// message outlives the process and is redelivered to every
				// future subscriber, which makes a credential landing here
				// unfixable by rotation.
				Error:     e.ScrubDestinationText(d.ID, d.Error),
				Rendition: d.RenditionName, At: now,
			}
			// The classified failure. The line is scrubbed by the supervisor
			// already and scrubbed again here, for Error's reason above: it is
			// FFmpeg stderr, and this topic is retained.
			if r := d.Reason; r != nil {
				ds.Cause, ds.Held = string(r.Cause), r.Held
				ds.CauseDetail = e.ScrubDestinationText(d.ID, r.Line)
			}
			if d.Process != nil {
				ds.BitrateKbps = d.Process.Progress.BitrateKbps
				ds.UptimeSec = d.Process.UptimeSec
				ds.Restarts = d.Process.Restarts
			}
			item.Dests = append(item.Dests, ds)
		}
		for _, rd := range st.Renditions {
			rs := mqtt.RenditionState{
				ID: rd.ID, Name: rd.Name, Slug: mqtt.Slug(rd.Name),
				Consumers: rd.Consumers, Width: rd.Width, Height: rd.Height,
				FPS: rd.FPS, Codec: rd.Codec, Encoder: string(rd.Encoder),
				Error: rd.Error, At: now,
			}
			if rd.Process != nil {
				rs.Running = rd.Process.State == supervisor.StateRunning
				rs.BitrateKbps = rd.Process.Progress.BitrateKbps
			}
			item.Renditions = append(item.Renditions, rs)
		}

		if src.Live {
			snap.Host.SourcesLive++
		}
		snap.Host.Dests += src.Dests
		snap.Host.DestsUp += src.DestsUp

		item.State = src
		snap.Sources = append(snap.Sources, item)
	}
	snap.Audience = r.audience.state()
	return snap
}

// mqttAudienceFeed hands the analytics sampler's picture to the MQTT runner.
//
// A holder rather than a function argument because of construction order: the
// runner starts before the API server that owns the sampler is built, and the
// feed is filled in once it is. Until then -- and on a runner a test built
// without one -- the snapshot carries no audience and nothing is published
// for it.
type mqttAudienceFeed struct {
	fn atomic.Pointer[func() *mqtt.AudienceState]
}

func (f *mqttAudienceFeed) set(fn func() *mqtt.AudienceState) {
	if f != nil {
		f.fn.Store(&fn)
	}
}

func (f *mqttAudienceFeed) state() *mqtt.AudienceState {
	if f == nil {
		return nil
	}
	fn := f.fn.Load()
	if fn == nil {
		// A feed that is coming but not set yet: an empty, off-air picture
		// rather than nil. Discovery is published once per connection, and a
		// nil here on the first connect would leave the viewer sensors out
		// until the next reconnect.
		return &mqtt.AudienceState{At: time.Now()}
	}
	return (*fn)()
}

// storageStates counts each offload target's uploads by state for telemetry.
// A store that cannot answer leaves the section out for this tick rather than
// publishing zeros that would read as "nothing stuck".
func (r *mqttRunner) storageStates(now time.Time) []mqtt.StorageState {
	if r.store == nil {
		return nil
	}
	targets, err := r.store.ListStorageTargets(nil)
	if err != nil {
		r.log.Warn("mqtt: storage targets unavailable", "err", err)
		return nil
	}
	items, err := r.store.ListOffloadItems()
	if err != nil {
		r.log.Warn("mqtt: offload state unavailable", "err", err)
		return nil
	}
	out := make([]mqtt.StorageState, 0, len(targets))
	for _, t := range targets {
		// Keyed by ID, not by name: two targets may share a name, and their
		// retained topics and Home Assistant entities must not collide. The
		// name stays the friendly name.
		st := mqtt.StorageState{ID: t.ID, Name: t.Name, Slug: mqtt.StorageSlug(t.ID), Enabled: t.Enabled, At: now}
		for _, it := range items {
			if it.TargetID != t.ID {
				continue
			}
			switch it.State {
			case db.OffloadStuck:
				st.Stuck++
			case db.OffloadFailed:
				st.Failed++
			case db.OffloadVerified:
				st.Verified++
			default:
				st.Pending++
			}
		}
		out = append(out, st)
	}
	return out
}
