package main

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/mqtt"
	"github.com/rainmanjam/polyemesis/internal/secrets"
)

// reconcileFixture builds a runner over a real database.
//
// NO NETWORK, AND NO NAME RESOLUTION. The broker URL is 127.0.0.1 on a port
// nothing listens on: autopaho's NewConnection does not dial synchronously, so
// connect() returns as soon as the configuration is valid and the background
// dialler fails locally and instantly. A hostname -- even a reserved one like
// broker.example -- would send every one of these tests through the system
// resolver, which is the single most common source of a slow, machine-dependent
// unit test.
//
// WINDOWS CLEANUP ORDER. t.TempDir() is registered FIRST and store.Close
// SECOND, so the LIFO cleanup closes the database before the directory is
// removed. Reversed, Windows refuses to remove a directory holding an open file
// and the failure names neither the test nor the reason. Same rule as
// bannerFixture; see also captureStdout's note that this package must never use
// t.Parallel().
func reconcileFixture(t *testing.T) (*mqttRunner, *db.DB, context.Context) {
	t.Helper()

	dir := t.TempDir()
	store, err := db.Open(filepath.Join(dir, "polyemesis.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	box, err := secrets.LoadOrCreate(filepath.Join(dir, "secret.key"))
	if err != nil {
		t.Fatalf("secrets.LoadOrCreate: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	r := &mqttRunner{
		log: newLogger("error"), store: store, box: box,
		engines: func() []mqttEngine { return nil },
		version: "test",
	}
	// The shutdown path, driven as the cleanup. It is also the only coverage
	// stop() gets, and it is honest coverage: stop is a one-line delegate to
	// disconnect and this is exactly how main calls it.
	t.Cleanup(func() { r.stop(ctx) })

	return r, store, ctx
}

// enableMQTT writes an enabled MQTT block. mutate runs last so a test can vary
// one field.
func enableMQTT(t *testing.T, store *db.DB, mutate func(*db.MQTTSettings)) {
	t.Helper()
	s := db.DefaultSettings()
	s.MQTT = db.MQTTSettings{
		Enabled: true, BrokerURL: "mqtt://127.0.0.1:1",
		Prefix: "polyemesis", Instance: "studio",
		KeepAliveSec: 30, IntervalSecond: 10,
	}
	if mutate != nil {
		mutate(&s.MQTT)
	}
	// PutSettings is the raw, non-validating write; see bannerFixture.
	if err := store.PutSettings(s); err != nil {
		t.Fatalf("PutSettings: %v", err)
	}
}

// liveClient reads the runner's connection under its own lock.
func liveClient(r *mqttRunner) (*mqtt.Client, string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.client, r.sig
}

// TestDisablingMQTTTearsTheConnectionDown is the round trip: on, off, on again.
//
// The middle step is the one that matters and the one nothing asserted. When an
// operator moves the MQTT switch to off in the web UI, the ONLY thing that acts
// on that is `if !cfg.Enabled { r.disconnect(ctx); return }` in reconcile. If
// that disconnect goes away, reconcile returns having done nothing, the live
// client keeps publishing, and the telemetry keeps arriving on a RETAINED topic
// tree that the broker will replay to every future subscriber. The switch in
// the UI reports success. Nothing logs. The operator's evidence that they
// stopped publishing is a toggle that moved.
//
// THE THIRD STEP IS WHY THIS IS A ROUND TRIP RATHER THAN TWO ASSERTIONS. A
// disconnect that tore down the client but left `sig` behind would pass an
// off-state check and then refuse to reconnect: re-enabling identical settings
// produces the same signature, the `unchanged` short-circuit sees it, and the
// link never comes back. Re-enabling and demanding a NEW client pointer is what
// catches that, and it is why disconnect clears sig in the same critical
// section as the client.
func TestDisablingMQTTTearsTheConnectionDown(t *testing.T) {
	r, store, ctx := reconcileFixture(t)

	enableMQTT(t, store, nil)
	r.reconcile(ctx)
	first, sig := liveClient(r)
	if first == nil {
		t.Fatal("reconcile with MQTT enabled built no client, so every assertion below " +
			"would be vacuous")
	}
	if sig == "" {
		t.Fatal("a live connection carries no signature; the unchanged check cannot work")
	}

	// Off.
	s := db.DefaultSettings()
	s.MQTT.Enabled = false
	if err := store.PutSettings(s); err != nil {
		t.Fatalf("PutSettings: %v", err)
	}
	r.reconcile(ctx)

	off, offSig := liveClient(r)
	if off != nil {
		t.Errorf("MQTT was switched off and the runner still holds a live client (%p).\n\n"+
			"reconcile's `if !cfg.Enabled { r.disconnect(ctx) }` is the only thing that "+
			"acts on that switch. Without it the publisher keeps running: telemetry keeps "+
			"flowing to a RETAINED topic tree, the broker replays it to every subscriber "+
			"that connects afterwards, and the operator's only evidence that they stopped "+
			"is a toggle that moved.", off)
	}
	if offSig != "" {
		t.Errorf("the connection signature survived the disconnect as %q. Re-enabling the "+
			"same settings would then produce a matching signature, the `unchanged` "+
			"short-circuit would fire, and the link would never be rebuilt.", offSig)
	}

	// On again, with byte-identical settings.
	enableMQTT(t, store, nil)
	r.reconcile(ctx)

	again, _ := liveClient(r)
	if again == nil {
		t.Fatal("re-enabling MQTT with the same settings did not rebuild the connection. " +
			"This is the failure a disconnect that forgot to clear `sig` produces: the " +
			"signature still matches, so reconcile decides nothing changed.")
	}
	if again == first {
		t.Error("re-enabling MQTT handed back the SAME client pointer that was supposedly " +
			"torn down, which means it was never torn down at all")
	}
}

// TestUnreadableSettingsLeaveTheConnectionAlone pins the deliberate non-action
// in reconcile's error path.
//
// An unreadable settings row is a transient problem far more often than it is a
// decision to stop publishing -- a busy SQLite file, a moment of IO pressure.
// Tearing the link down on it publishes `offline` to the retained availability
// topic, which is polyemesis telling every subscriber that the streaming host
// is down because a database read took too long. The correct response is to log
// and leave everything exactly as it was, and reconcile's own comment says so.
//
// The signature is the specific thing under test. Clearing `sig` while keeping
// the client would be an equally silent bug in the other direction: the next
// successful poll would see a mismatch and cycle a connection that was healthy
// the entire time.
func TestUnreadableSettingsLeaveTheConnectionAlone(t *testing.T) {
	r, store, ctx := reconcileFixture(t)

	// A closed database is the cheapest honest way to make GetSettings fail:
	// it returns `sql: database is closed`, which is a real error from the real
	// call rather than a fake injected through a seam invented for this test.
	if err := store.Close(); err != nil {
		t.Fatalf("closing the store: %v", err)
	}

	const preset = "PRESET"
	r.mu.Lock()
	r.sig = preset
	r.mu.Unlock()

	r.reconcile(ctx)

	if _, sig := liveClient(r); sig != preset {
		t.Errorf("after a failed settings read the signature is %q, want %q.\n\n"+
			"reconcile must return WITHOUT touching the connection when it cannot read "+
			"the settings. Disconnecting there publishes `offline` to the retained "+
			"availability topic -- telling every subscriber the streaming host is down -- "+
			"because a SQLite read failed. Clearing the signature alone is the same bug "+
			"quieter: the next healthy poll sees a mismatch and cycles a link that was "+
			"never unhealthy.", sig, preset)
	}
}

// TestAHealthySettingsPollDoesNotCycleTheLink closes the gap #160 left open.
//
// mqttSig is already well tested AS A FUNCTION: TestEveryConnectionChangingFieldIsInTheMQTTSignature
// enumerates every field that must change it. What no test covered is whether
// reconcile CONSULTS it. Delete the four-line `unchanged` short-circuit and
// every one of those tests still passes -- while the runner tears down and
// rebuilds the broker connection every five seconds, forever.
//
// That failure is close to invisible and expensive. Each cycle publishes a
// clean `offline` to the retained status topic and then `online` again, so
// every subscriber sees the instance flapping twelve times a minute; a Home
// Assistant availability sensor built on that topic is unusable. Nothing errors.
// The logs show a publisher starting, which is what a healthy publisher does.
//
// The second half is the complement, and it is what keeps the first from being
// satisfiable by a reconcile that never reconnects at all: a settings change
// that DOES affect the link must still rebuild it.
func TestAHealthySettingsPollDoesNotCycleTheLink(t *testing.T) {
	r, store, ctx := reconcileFixture(t)

	enableMQTT(t, store, nil)
	r.reconcile(ctx)
	first, _ := liveClient(r)
	if first == nil {
		t.Fatal("reconcile built no client, so the identity assertions below are vacuous")
	}

	// The five-second poll, with nothing changed.
	r.reconcile(ctx)
	second, _ := liveClient(r)
	if second != first {
		t.Errorf("two reconciles over unchanged settings produced two different clients "+
			"(%p then %p).\n\n"+
			"The `unchanged` short-circuit in reconcile is what stops this. Without it the "+
			"runner rebuilds the broker connection on EVERY five-second settings poll: the "+
			"retained status topic flaps between `offline` and `online` twelve times a "+
			"minute, every availability sensor built on it becomes unusable, and nothing "+
			"anywhere reports an error. mqttSig is tested as a function; this is the test "+
			"that it is consulted.", first, second)
	}

	// A change that genuinely alters the link. Instance is in the signature and
	// is also the topic root, so keeping the old connection would publish to a
	// tree the operator just renamed away from.
	enableMQTT(t, store, func(c *db.MQTTSettings) { c.Instance = "studio-b" })
	r.reconcile(ctx)

	third, _ := liveClient(r)
	if third == nil {
		t.Fatal("changing the instance left the runner with no connection at all")
	}
	if third == first {
		t.Error("changing the MQTT instance did not rebuild the connection, so telemetry " +
			"keeps publishing under the OLD topic root and the rename silently does " +
			"nothing. This is the complement of the assertion above: without it, a " +
			"reconcile that never reconnects would pass the same-pointer check.")
	}
}
