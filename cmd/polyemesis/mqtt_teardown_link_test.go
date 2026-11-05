package main

import (
	"bytes"
	"context"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/mqtt"
)

// THE TEARDOWN OVER A LINK THAT IS UP. reconcileFixture's broker is a port
// nothing listens on, so client.Connected() is false there and every clear,
// device removal and retire in teardown is skipped: those tests prove which
// mode is chosen and nothing about what it does. These hand the runner a fake
// link that is up and keeps the broker's retained set, so what a fresh
// subscriber would be handed after the teardown can be read back.

// fakeRetainBroker is a broker's retained set, shared by every link dialled to
// it, the way a real broker outlives the connections to it.
type fakeRetainBroker struct {
	mu       sync.Mutex
	retained map[string]string
	closes   int
	retires  int
	// block, when set, holds a publish to a matching topic until its context
	// ends: a broker that does not acknowledge it.
	block func(topic string) bool
	// down dials links that never come up.
	down bool
}

func newFakeRetainBroker() *fakeRetainBroker {
	return &fakeRetainBroker{retained: map[string]string{}}
}

func (b *fakeRetainBroker) dial(_ context.Context, cfg mqtt.Config, _ *slog.Logger) (mqttLink, error) {
	topics, err := mqtt.NewTopics(cfg.Prefix, cfg.Instance)
	if err != nil {
		return nil, err
	}
	l := &fakeRetainLink{b: b, topics: topics}
	b.mu.Lock()
	down := b.down
	b.mu.Unlock()
	if !down {
		l.up.Store(true)
		// What Connect does on a link that comes up: the birth message.
		_ = l.Publish(context.Background(), topics.Status(), 1, true, []byte(mqtt.Online))
	}
	return l, nil
}

func (b *fakeRetainBroker) set(topic, payload string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.retained[topic] = payload
}

func (b *fakeRetainBroker) get(topic string) (string, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	v, ok := b.retained[topic]
	return v, ok
}

// under is every retained topic in a link's tree, sorted.
func (b *fakeRetainBroker) under(topics *mqtt.Topics) []string {
	root := strings.TrimSuffix(topics.All(), "#")
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []string
	for t := range b.retained {
		if strings.HasPrefix(t, root) {
			out = append(out, t)
		}
	}
	slices.Sort(out)
	return out
}

func (b *fakeRetainBroker) counts() (closes, retires int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.closes, b.retires
}

type fakeRetainLink struct {
	b      *fakeRetainBroker
	topics *mqtt.Topics
	up     atomic.Bool
}

func (l *fakeRetainLink) Publish(ctx context.Context, topic string, _ byte, retain bool, payload []byte) error {
	l.b.mu.Lock()
	block := l.b.block
	l.b.mu.Unlock()
	if block != nil && block(topic) {
		<-ctx.Done()
		return ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if !retain {
		return nil
	}
	l.b.mu.Lock()
	defer l.b.mu.Unlock()
	if len(payload) == 0 {
		delete(l.b.retained, topic)
	} else {
		l.b.retained[topic] = string(payload)
	}
	return nil
}

func (l *fakeRetainLink) Connected() bool      { return l.up.Load() }
func (l *fakeRetainLink) Topics() *mqtt.Topics { return l.topics }

// Close and Retire are mqtt.Client's: a clean `offline`, or the status deleted
// for a tree that is being left.
func (l *fakeRetainLink) Close(ctx context.Context) error {
	if l.up.Load() {
		_ = l.Publish(ctx, l.topics.Status(), 1, true, []byte(mqtt.Offline))
	}
	l.up.Store(false)
	l.b.mu.Lock()
	l.b.closes++
	l.b.mu.Unlock()
	return nil
}

func (l *fakeRetainLink) Retire(ctx context.Context) error {
	if l.up.Load() {
		_ = l.Publish(ctx, l.topics.Status(), 1, true, nil)
	}
	l.up.Store(false)
	l.b.mu.Lock()
	l.b.retires++
	l.b.mu.Unlock()
	return nil
}

// linkFixture is reconcileFixture with the fake broker dialled, MQTT on at a
// one-second tick, and the first telemetry tick waited for: the host state is
// retained, and the device payload stands in for the announcer's (whose
// five-second tick a test should not wait for).
func linkFixture(t *testing.T) (*mqttRunner, *db.DB, context.Context, *fakeRetainBroker, *mqtt.Topics) {
	t.Helper()
	r, store, ctx := reconcileFixture(t)
	b := newFakeRetainBroker()
	r.dial = b.dial
	enableMQTT(t, store, func(m *db.MQTTSettings) { m.IntervalSecond = 1; m.Discovery = true })
	r.reconcile(ctx)
	c, _ := liveClient(r)
	if c == nil || !c.Connected() {
		t.Fatal("no live link to tear down")
	}
	topics := c.Topics()
	b.set(topics.Discovery(), `{"dev":{"name":"studio"}}`)
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, ok := b.get(topics.State()); ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the telemetry loop never published the host state")
		}
		time.Sleep(20 * time.Millisecond)
	}
	return r, store, ctx, b, topics
}

// Switching MQTT off: the state goes, `offline` stays, the device stays (Home
// Assistant shows its entities unavailable rather than losing them), and the
// link is closed, not retired.
func TestSwitchingMQTTOffLeavesOnlyOfflineAndTheDevice(t *testing.T) {
	r, store, ctx, b, topics := linkFixture(t)

	enableMQTT(t, store, func(m *db.MQTTSettings) { m.Enabled = false; m.IntervalSecond = 1; m.Discovery = true })
	r.reconcile(ctx)

	if got, want := b.under(topics), []string{topics.Status()}; !slices.Equal(got, want) {
		t.Errorf("a fresh subscriber to %s is handed %v, want only %v", topics.All(), got, want)
	}
	if v, _ := b.get(topics.Status()); v != mqtt.Offline {
		t.Errorf("status %q, want %q", v, mqtt.Offline)
	}
	if _, ok := b.get(topics.Discovery()); !ok {
		t.Error("switching MQTT off deleted the Home Assistant device; only an instance move may")
	}
	if closes, retires := b.counts(); closes != 1 || retires != 0 {
		t.Errorf("closes %d, retires %d; want the link closed once and never retired", closes, retires)
	}
}

// Moving the instance leaves the old tree for good: state, status and the old
// device all go, and the link is retired.
func TestMovingTheInstanceRetiresTheOldTreeAndItsDevice(t *testing.T) {
	r, store, ctx, b, old := linkFixture(t)

	enableMQTT(t, store, func(m *db.MQTTSettings) { m.Instance = "studio two"; m.IntervalSecond = 1; m.Discovery = true })
	r.reconcile(ctx)

	if got := b.under(old); len(got) != 0 {
		t.Errorf("the old tree %s still holds %v", old.All(), got)
	}
	if _, ok := b.get(old.Discovery()); ok {
		t.Error("the old Home Assistant device survived the instance move")
	}
	if _, retires := b.counts(); retires != 1 {
		t.Errorf("retires %d, want the old link retired once", retires)
	}
}

// #5 of the review: the device removal has a step of its own. A broker that
// does not acknowledge the state clears spends the whole of that step on them,
// and the device removal must still get its turn.
func TestTheDeviceRemovalOutlivesASlowStateClear(t *testing.T) {
	r, store, ctx, b, old := linkFixture(t)
	r.teardownStep = 150 * time.Millisecond
	b.mu.Lock()
	b.block = func(topic string) bool { return topic == old.State() }
	b.mu.Unlock()

	enableMQTT(t, store, func(m *db.MQTTSettings) { m.Instance = "studio two"; m.IntervalSecond = 1; m.Discovery = true })
	r.reconcile(ctx)

	if _, ok := b.get(old.Discovery()); ok {
		t.Error("the old Home Assistant device survived an instance move because the state clear " +
			"used up the context the device removal shared")
	}
}

// A link that is down can delete nothing. The teardown says what it left.
func TestSwitchingMQTTOffOverADownLinkSaysWhatWasLeft(t *testing.T) {
	r, store, ctx := reconcileFixture(t)
	b := newFakeRetainBroker()
	b.down = true
	r.dial = b.dial
	var buf lockedBuffer
	r.log = slog.New(slog.NewTextHandler(&buf, nil))
	enableMQTT(t, store, nil)
	r.reconcile(ctx)

	enableMQTT(t, store, func(m *db.MQTTSettings) { m.Enabled = false })
	r.reconcile(ctx)

	if got := buf.String(); !strings.Contains(got, "were left on it") {
		t.Errorf("no warning that the retained topics were left; the log was:\n%s", got)
	}
}

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}
