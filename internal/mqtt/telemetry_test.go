package mqtt

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sort"
	"strings"
	"testing"
	"time"
)

// message is one publish as it went on the wire.
type message struct {
	topic   string
	qos     byte
	retain  bool
	payload []byte
}

// fakeBroker records every publish. It is deliberately not a broker: the point
// is to assert the exact bytes and flags this package emits, without the
// timing, ports and cleanup a real broker drags in. A real mosquitto is
// covered by the acceptance suite, which confirms this rather than replacing
// it.
type fakeBroker struct {
	msgs []message
	up   bool
	// failOn makes a publish to this topic fail, so the partial-tick paths can
	// be exercised.
	failOn string
}

func newFakeBroker() *fakeBroker { return &fakeBroker{up: true} }

func (f *fakeBroker) Publish(_ context.Context, topic string, qos byte, retain bool, payload []byte) error {
	if topic == f.failOn {
		return errors.New("broker refused")
	}
	f.msgs = append(f.msgs, message{topic: topic, qos: qos, retain: retain, payload: payload})
	return nil
}

func (f *fakeBroker) Connected() bool { return f.up }

// topics returns every topic published, in order.
func (f *fakeBroker) topics() []string {
	out := make([]string, 0, len(f.msgs))
	for _, m := range f.msgs {
		out = append(out, m.topic)
	}
	return out
}

func (f *fakeBroker) reset() { f.msgs = nil }

func quietLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func newTelemetry(t *testing.T) (*Telemetry, *fakeBroker) {
	t.Helper()
	f := newFakeBroker()
	return NewTelemetry(f, newTopics(t, "", "studio"), quietLog()), f
}

func sampleSnapshot() Snapshot {
	at := time.Unix(1700000000, 0).UTC()
	return Snapshot{
		Host: HostState{Version: "test", Sources: 1, SourcesLive: 1, Dests: 1, DestsUp: 1, At: at},
		Sources: []SourceSnapshot{{
			State: SourceState{ID: 1, Name: "Cam 1", Slug: Slug("Cam 1"), Live: true, IngestMode: "srt", At: at},
			Dests: []DestState{
				{ID: 7, Name: "Twitch (main)", Slug: Slug("Twitch (main)"), Platform: "twitch", Enabled: true, Running: true, At: at},
			},
			Renditions: []RenditionState{
				{ID: 3, Name: "720p", Slug: Slug("720p"), Consumers: 1, Running: true, At: at},
			},
		}},
	}
}

// Retain is the entire feature and it is one bit. Assert the bit.
func TestEveryTelemetryPublishIsRetainedAtQoS1(t *testing.T) {
	tel, f := newTelemetry(t)
	if err := tel.Publish(context.Background(), sampleSnapshot()); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if len(f.msgs) == 0 {
		t.Fatal("nothing was published")
	}
	for _, m := range f.msgs {
		if !m.retain {
			t.Errorf("%s was published without retain; a consumer that reconnects learns nothing, which is the one thing this package exists for", m.topic)
		}
		if m.qos != 1 {
			t.Errorf("%s was published at QoS %d, want 1; a broker may decline to store a retained QoS 0 message", m.topic, m.qos)
		}
	}
}

func TestTelemetryPublishesTheDocumentedTopics(t *testing.T) {
	tel, f := newTelemetry(t)
	if err := tel.Publish(context.Background(), sampleSnapshot()); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	got := f.topics()
	sort.Strings(got)
	want := []string{
		"polyemesis/studio/source/cam-1-" + hashOf("Cam 1") + "/dest/twitch-main-" + hashOf("Twitch (main)") + "/state",
		"polyemesis/studio/source/cam-1-" + hashOf("Cam 1") + "/rendition/720p/state",
		"polyemesis/studio/source/cam-1-" + hashOf("Cam 1") + "/state",
		"polyemesis/studio/state",
	}
	sort.Strings(want)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("topics published:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// hashOf mirrors what Slug appends, so the expectations above name the exact
// topic rather than matching a prefix.
func hashOf(name string) string {
	s := Slug(name)
	return s[len(s)-hashHexLen:]
}

// Republishing an identical retained message every tick is pure traffic: the
// broker is already holding it.
func TestUnchangedStateIsNotRepublished(t *testing.T) {
	tel, f := newTelemetry(t)
	ctx := context.Background()
	snap := sampleSnapshot()

	if err := tel.Publish(ctx, snap); err != nil {
		t.Fatalf("first Publish: %v", err)
	}
	first := len(f.msgs)
	f.reset()

	// Same state, later timestamp -- which is what a real tick looks like.
	later := snap
	later.Host.At = later.Host.At.Add(time.Minute)
	if err := tel.Publish(ctx, later); err != nil {
		t.Fatalf("second Publish: %v", err)
	}
	if len(f.msgs) != 0 {
		t.Errorf("second identical tick published %d messages (%v), want 0; the timestamp alone must not count as a change",
			len(f.msgs), f.topics())
	}

	// And a real change must still get through, or the suppression above would
	// be satisfied by a publisher that had simply stopped working.
	changed := snap
	changed.Sources[0].State.Live = false
	if err := tel.Publish(ctx, changed); err != nil {
		t.Fatalf("third Publish: %v", err)
	}
	if len(f.msgs) != 1 {
		t.Errorf("a changed source published %d messages, want exactly 1", len(f.msgs))
	}
	if first == 0 {
		t.Error("the first tick published nothing")
	}
}

// A broker restart loses every retained message it held. The connection
// dropping is the only signal we get, so a reconnect must resend everything.
func TestResyncRepublishesEverything(t *testing.T) {
	tel, f := newTelemetry(t)
	ctx := context.Background()
	snap := sampleSnapshot()

	if err := tel.Publish(ctx, snap); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	want := len(f.msgs)
	f.reset()

	tel.Resync()
	if err := tel.Publish(ctx, snap); err != nil {
		t.Fatalf("Publish after Resync: %v", err)
	}
	if len(f.msgs) != want {
		t.Errorf("after Resync %d messages were published, want all %d; a broker that restarted would be left with nothing",
			len(f.msgs), want)
	}
}

// Risk 1 from the design: a renamed or deleted source leaves a retained topic
// on the broker forever, and a Home Assistant entity attached to it.
func TestDeletedThingsHaveTheirRetainedTopicCleared(t *testing.T) {
	tel, f := newTelemetry(t)
	ctx := context.Background()

	if err := tel.Publish(ctx, sampleSnapshot()); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	before := f.topics()
	f.reset()

	// The destination is deleted; everything else stays.
	snap := sampleSnapshot()
	snap.Sources[0].Dests = nil
	if err := tel.Publish(ctx, snap); err != nil {
		t.Fatalf("Publish after delete: %v", err)
	}

	destTopic := ""
	for _, topic := range before {
		if strings.Contains(topic, "/dest/") {
			destTopic = topic
		}
	}
	if destTopic == "" {
		t.Fatal("the fixture published no destination topic")
	}

	var cleared *message
	for i, m := range f.msgs {
		if m.topic == destTopic {
			cleared = &f.msgs[i]
		}
	}
	if cleared == nil {
		t.Fatalf("nothing was published to %s after the destination was deleted; its retained state would sit on the broker forever", destTopic)
	}
	if len(cleared.payload) != 0 {
		t.Errorf("the clearing message carried %d bytes, want 0; a zero-length retained publish is the only thing that deletes a retained message", len(cleared.payload))
	}
	if !cleared.retain {
		t.Error("the clearing message was published without retain, which deletes nothing")
	}
}

// A topic whose publish failed must be retried on the next tick. Caching it as
// published would leave it permanently stale with nothing noticing.
func TestAFailedPublishIsRetriedRatherThanCachedAsSent(t *testing.T) {
	tel, f := newTelemetry(t)
	ctx := context.Background()
	snap := sampleSnapshot()
	f.failOn = tel.topics.State()

	if err := tel.Publish(ctx, snap); err == nil {
		t.Error("a failing broker produced no error; the caller cannot tell the tick was partial")
	}
	f.failOn = ""
	f.reset()

	// Nothing about the state changed, so only the previously-failed topic
	// should go out.
	if err := tel.Publish(ctx, snap); err != nil {
		t.Fatalf("second Publish: %v", err)
	}
	if got := f.topics(); len(got) != 1 || got[0] != tel.topics.State() {
		t.Errorf("after a failure the next tick published %v, want exactly [%s]", got, tel.topics.State())
	}
}

func TestAnnouncePublishesRetainedOnline(t *testing.T) {
	tel, f := newTelemetry(t)
	if err := tel.Announce(context.Background()); err != nil {
		t.Fatalf("Announce: %v", err)
	}
	if len(f.msgs) != 1 {
		t.Fatalf("Announce published %d messages, want 1", len(f.msgs))
	}
	m := f.msgs[0]
	if m.topic != "polyemesis/studio/status" || string(m.payload) != Online || !m.retain || m.qos != 1 {
		t.Errorf("Announce published %+v on %q, want a retained QoS 1 %q", string(m.payload), m.topic, Online)
	}
}
