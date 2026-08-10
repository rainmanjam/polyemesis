package main

import (
	"context"
	"crypto/sha256"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/engine"
	"github.com/rainmanjam/polyemesis/internal/mqtt"
	"github.com/rainmanjam/polyemesis/internal/secrets"
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

// mqttRunner keeps a broker connection matching the stored settings.
type mqttRunner struct {
	log   *slog.Logger
	store *db.DB
	box   *secrets.Box
	eng   *engine.Manager

	version string
	started time.Time

	mu     sync.Mutex
	client *mqtt.Client
	cancel context.CancelFunc
	// sig hashes the settings the live connection was built from. Comparing it
	// is what stops an unrelated settings save from cycling a healthy
	// connection -- the same trick the engine's *Sig fields play.
	sig string
}

// startMQTT begins watching the settings and returns a shutdown function.
//
// It never returns an error, and never blocks on the broker. A publisher that
// refused to start because the broker was rebooting would take the rest of
// polyemesis with it, and the entire design assumes the link comes and goes.
func startMQTT(ctx context.Context, log *slog.Logger, store *db.DB, box *secrets.Box, eng *engine.Manager, version string) func(context.Context) {
	r := &mqttRunner{
		log: log, store: store, box: box, eng: eng,
		version: version, started: time.Now(),
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
		r.disconnect(ctx)
		return
	}
	password, err := r.store.GetMQTTPassword(r.box)
	if err != nil {
		r.log.Error("cannot read the MQTT broker password", "err", err)
		return
	}

	sig := mqttSig(cfg, password)

	r.mu.Lock()
	unchanged := r.client != nil && r.sig == sig
	r.mu.Unlock()
	if unchanged {
		return
	}

	r.disconnect(ctx)
	r.connect(ctx, cfg, password, sig)
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
	clientID := cfg.ClientID
	if clientID == "" {
		// Derived from the instance, which is already the thing that has to be
		// unique per install. A collision here is the number-one cause of an
		// unexplained reconnect loop: the broker disconnects the older session
		// on every connect and both clients reconnect forever.
		clientID = "polyemesis-" + mqtt.Slug(cfg.Instance)
	}
	loopCtx, cancel := context.WithCancel(ctx)

	client, err := mqtt.Connect(loopCtx, mqtt.Config{
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
	every := time.Duration(cfg.IntervalSecond) * time.Second
	if every <= 0 {
		every = 10 * time.Second
	}

	r.mu.Lock()
	r.client, r.cancel, r.sig = client, cancel, sig
	r.mu.Unlock()

	go tel.Loop(loopCtx, every, r.snapshot)
	if cfg.Discovery {
		go r.announceDiscovery(loopCtx, client, tel)
	}
	r.log.Info("mqtt telemetry started",
		"instance", client.Topics().Instance(), "everySeconds", int(every.Seconds()))
}

// announceDiscovery publishes the Home Assistant device payload once the link
// is up, and again after every reconnect.
//
// Separate from the telemetry loop because discovery is a different contract:
// it describes the entities, it changes only when the topology does, and a
// consumer that is not Home Assistant does not want it at all.
func (r *mqttRunner) announceDiscovery(ctx context.Context, client *mqtt.Client, tel *mqtt.Telemetry) {
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
			if wasUp {
				continue
			}
			wasUp = true
			if err := tel.PublishDiscovery(ctx, r.snapshot()); err != nil {
				r.log.Warn("could not publish Home Assistant discovery", "err", err)
			}
		}
	}
}

// disconnect tears down the live connection, publishing a clean `offline`.
func (r *mqttRunner) disconnect(ctx context.Context) {
	r.mu.Lock()
	client, cancel := r.client, r.cancel
	r.client, r.cancel, r.sig = nil, nil, ""
	r.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if client == nil {
		return
	}
	// Bounded, and not derived from ctx: this runs on the shutdown path too,
	// where ctx is already cancelled and a clean `offline` is exactly what must
	// still get out.
	closeCtx, done := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer done()
	if err := client.Close(closeCtx); err != nil {
		r.log.Warn("mqtt disconnect was not clean", "err", err)
	}
}

// stop is the shutdown hook handed back to main.
func (r *mqttRunner) stop(ctx context.Context) { r.disconnect(ctx) }

// snapshot flattens every engine into the shape the publisher sends.
//
// Nothing secret crosses this boundary. A destination contributes its name,
// platform and error, never its URL or stream key -- the same rule
// alertSnapshot follows, and the reason mqtt.DestState has no field for one.
func (r *mqttRunner) snapshot() mqtt.Snapshot {
	now := time.Now()
	engines := r.eng.Engines()

	snap := mqtt.Snapshot{
		Host: mqtt.HostState{
			Version:   r.version,
			StartedAt: r.started,
			UptimeSec: now.Sub(r.started).Seconds(),
			Sources:   len(engines),
			At:        now,
		},
	}

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
		// Liveness from bytes on the relay, not from process state: an SRT or
		// RTMP listener sits in "running" for as long as it waits for a
		// publisher, which is a different question from whether the source is
		// arriving.
		src.Live = st.Relay.RxBytes > 0 && st.Ingest != nil &&
			st.Ingest.State == supervisor.StateRunning
		if st.Ingest != nil {
			src.IngestError = st.Ingest.LastError
			src.UptimeSec = st.Ingest.UptimeSec
			src.Restarts = st.Ingest.Restarts
			src.BitrateKbps = st.Ingest.Progress.BitrateKbps
		}
		src.IngestMode = string(e.Settings().Ingest.Mode)
		src.Recording = st.Recorder != nil && st.Recorder.State == supervisor.StateRunning
		if st.Failover != nil {
			src.Failover = string(st.Failover.Active)
		}

		item := mqtt.SourceSnapshot{}
		for _, d := range st.Destinations {
			running := d.Error == "" && d.Process != nil && d.Process.State == supervisor.StateRunning
			if running {
				src.DestsUp++
			}
			ds := mqtt.DestState{
				ID: d.ID, Name: d.Name, Slug: mqtt.Slug(d.Name),
				Platform: string(d.Platform), Kind: string(d.Kind),
				Enabled: d.Enabled, Running: running,
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
	return snap
}
