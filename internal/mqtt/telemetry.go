package mqtt

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"time"
)

// Snapshot is everything one telemetry tick publishes.
//
// It is a plain value assembled by the caller, exactly as alerts.Snapshot is.
// That is what keeps this package free of any import of internal/engine: the
// engine flattens itself into these shapes, and every field crossing the
// boundary is one somebody chose to expose.
type Snapshot struct {
	Host    HostState
	Sources []SourceSnapshot
}

// SourceSnapshot is one programme and everything beneath it.
type SourceSnapshot struct {
	State      SourceState
	Dests      []DestState
	Renditions []RenditionState
}

// Telemetry publishes a Snapshot to a topic tree and keeps the tree tidy.
//
// It is not safe for concurrent use: one telemetry loop owns one Telemetry, in
// the same way one alertLoop owns one alerts.Watcher.
type Telemetry struct {
	pub    Publisher
	topics *Topics
	log    *slog.Logger

	// published maps topic -> the last payload sent on it, with the timestamp
	// zeroed. Two jobs, both load-bearing:
	//
	//  1. Suppressing a republish of state that has not changed. Retained
	//     messages persist on the broker; resending an identical one every tick
	//     is pure traffic, and on a busy install it is the difference between
	//     a handful of messages a minute and hundreds.
	//  2. Finding orphans. A topic present last tick and absent this one
	//     belongs to a source or destination that has been deleted or renamed,
	//     and its retained message would otherwise sit on the broker forever
	//     with a Home Assistant entity attached to it. See sweep.
	published map[string][]byte

	// forceAll makes the next tick republish everything regardless of change.
	// Set on construction and on every reconnect: a broker restart loses every
	// retained message it was holding, and a change-suppressing publisher would
	// then never resend them. The connection dropping is the only signal we get
	// that this may have happened.
	forceAll bool
}

// NewTelemetry wires a publisher to a topic tree.
func NewTelemetry(pub Publisher, topics *Topics, log *slog.Logger) *Telemetry {
	return &Telemetry{
		pub: pub, topics: topics, log: log,
		published: map[string][]byte{},
		forceAll:  true,
	}
}

// Resync forces the next Publish to resend every topic.
//
// Called when the connection comes back up. A broker that restarted has lost
// the retained messages it was holding, and nothing tells us whether it did --
// so the reconnect is treated as though it did.
func (t *Telemetry) Resync() { t.forceAll = true }

// Announce publishes the retained `online` availability message.
//
// Its counterpart is the will message set at connect time, which the broker
// publishes if polyemesis dies without disconnecting cleanly.
func (t *Telemetry) Announce(ctx context.Context) error {
	return t.pub.Publish(ctx, t.topics.Status(), QoS, true, []byte(Online))
}

// Publish sends one tick's worth of state and clears anything orphaned.
//
// Errors are collected rather than returned on the first failure: a broker that
// rejects one topic should not stop the other thirty from being published, and
// the caller wants to know the tick was partial rather than where it stopped.
func (t *Telemetry) Publish(ctx context.Context, snap Snapshot) error {
	force := t.forceAll
	t.forceAll = false

	seen := make(map[string][]byte, len(t.published)+1)
	var errs []error

	send := func(topic string, v any) {
		body, err := json.Marshal(v)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", topic, err))
			return
		}
		key := stripTimestamp(body)
		seen[topic] = key
		if !force && bytes.Equal(t.published[topic], key) {
			return
		}
		if err := t.pub.Publish(ctx, topic, QoS, true, body); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", topic, err))
			// Deliberately NOT recorded as published. A failed publish that
			// updated the cache would be suppressed on every later tick, and
			// the topic would stay permanently stale with nothing retrying it.
			delete(seen, topic)
			return
		}
		t.published[topic] = key
	}

	send(t.topics.State(), snap.Host)
	for _, s := range snap.Sources {
		src := s.State.Slug
		send(t.topics.Source(src), s.State)
		for _, d := range s.Dests {
			send(t.topics.Dest(src, d.Slug), d)
		}
		for _, r := range s.Renditions {
			send(t.topics.Rendition(src, r.Slug), r)
		}
	}

	errs = append(errs, t.sweep(ctx, seen)...)

	if len(errs) > 0 {
		return fmt.Errorf("mqtt telemetry: %d of %d topics failed: %w",
			len(errs), len(seen)+len(errs), errs[0])
	}
	return nil
}

// sweep clears the retained message on every topic that was published before
// and is absent now.
//
// This is designed in rather than bolted on because the failure it prevents is
// permanent and silent. A retained message outlives the process that sent it;
// renaming a destination publishes a new topic and abandons the old one, and
// the abandoned one keeps reporting the last state it ever saw. A Home
// Assistant dashboard built on it shows a destination that has not existed for
// months, still "running", forever.
//
// A zero-length payload with retain set is the specified way to delete a
// retained message. It is not a normal publish and must not be confused with
// one: a subscriber receives it as a message with an empty body.
func (t *Telemetry) sweep(ctx context.Context, seen map[string][]byte) []error {
	// Sorted so the log line and any test see a deterministic order. Map
	// iteration order would make a failure reproduce differently each run.
	orphans := make([]string, 0)
	for topic := range t.published {
		if _, ok := seen[topic]; !ok {
			orphans = append(orphans, topic)
		}
	}
	sort.Strings(orphans)

	var errs []error
	for _, topic := range orphans {
		if err := t.pub.Publish(ctx, topic, QoS, true, nil); err != nil {
			errs = append(errs, fmt.Errorf("clearing %s: %w", topic, err))
			continue
		}
		delete(t.published, topic)
		t.log.Info("cleared an orphaned retained topic", "topic", topic)
	}
	return errs
}

// Clear removes every retained topic this instance has published, used on a
// clean shutdown when the operator has asked for it and by the acceptance
// suite. It does not touch the status topic, which must survive to say
// `offline`.
func (t *Telemetry) Clear(ctx context.Context) error {
	return t.Publish(ctx, Snapshot{})
}

// stripTimestamp returns a change-detection key: the payload with every `"at"`
// value blanked.
//
// Without this the comparison is worthless -- every payload carries a fresh
// timestamp, so nothing ever compares equal and the suppression never fires.
// Done textually on the marshalled bytes rather than by reflecting over the
// structs because it has to work for all four payload types and stay correct
// when a fifth is added.
func stripTimestamp(body []byte) []byte {
	const key = `"at":"`
	out := make([]byte, len(body))
	copy(out, body)
	i := bytes.Index(out, []byte(key))
	if i < 0 {
		return out
	}
	start := i + len(key)
	end := bytes.IndexByte(out[start:], '"')
	if end < 0 {
		return out
	}
	for j := start; j < start+end; j++ {
		out[j] = '0'
	}
	return out
}

// Loop runs Publish on a ticker until the context is cancelled.
//
// snapshot is called once per tick and only when the connection is up. It is a
// function rather than a channel because assembling a Snapshot walks every
// engine, and doing that work while the broker is unreachable would be paid for
// nothing.
func (t *Telemetry) Loop(ctx context.Context, every time.Duration, snapshot func() Snapshot) {
	tick := time.NewTicker(every)
	defer tick.Stop()

	wasUp := false
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			if !t.pub.Connected() {
				wasUp = false
				continue
			}
			if !wasUp {
				// The link has just come up, either for the first time or after
				// an outage. Announce availability and assume the broker lost
				// its retained set.
				wasUp = true
				t.Resync()
				if err := t.Announce(ctx); err != nil {
					t.log.Warn("mqtt: could not announce availability", "err", err)
				}
			}
			if err := t.Publish(ctx, snapshot()); err != nil {
				t.log.Warn("mqtt: telemetry tick was partial", "err", err)
			}
		}
	}
}
