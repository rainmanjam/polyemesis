package engine

import (
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/config"
	"github.com/rainmanjam/polyemesis/internal/db/dbtest"
	"github.com/rainmanjam/polyemesis/internal/events"
	"github.com/rainmanjam/polyemesis/internal/ffmpeg"
)

// A SHUTDOWN MUST NOT UNSUBSCRIBE A CONSUMER THAT NEVER SUBSCRIBED.
//
// Stop collected the recorder, preview and meters into one list with a
// non-nil hub each and unsubscribed all three unconditionally. On an idle
// engine -- nothing publishing, so none of them ever started -- that asked the
// hub to remove three names it never had, and the hub answers that at ERROR,
// because #711 made a mismatched Unsubscribe loud: it is how a subscription
// used to outlive the process that owned it. So every clean restart of an idle
// server logged three ERROR lines claiming a teardown had named the wrong
// subscriber, which is the one message that must stay trustworthy. Seen on the
// staging box on the first restart after the v0.10.0 exploratory-testing fixes.
//
// A consumer holds a relay port exactly when it subscribed, so the port is the
// signal: no port, no subscription, nothing to remove.

const wrongSubscriberError = "asked to remove a relay subscriber this hub does not have"

// loggedEngine captures through failover_test.go's syncBuffer: the engine
// logs from supervisor and hub goroutines as well as the test's own.
func loggedEngine(t *testing.T) (*Engine, *syncBuffer) {
	t.Helper()
	dir := t.TempDir()
	store := dbtest.OpenAt(t, filepath.Join(dir, "polyemesis.db"))
	cfg := config.Config{DataDir: dir}
	if err := cfg.EnsureDirs(); err != nil {
		t.Fatalf("EnsureDirs: %v", err)
	}
	tools := &ffmpeg.Tools{
		FFmpeg:  filepath.Join(dir, "no-such-ffmpeg"),
		FFprobe: filepath.Join(dir, "no-such-ffprobe"),
	}
	id, err := store.DefaultSourceID()
	if err != nil {
		t.Fatalf("DefaultSourceID: %v", err)
	}
	buf := &syncBuffer{}
	log := slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	e, err := New(log, cfg, store, tools, events.NewBroker(), id, nil, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return e, buf
}

func TestStoppingAnIdleEngineDoesNotClaimAWrongSubscriber(t *testing.T) {
	e, logs := loggedEngine(t)

	// Nothing started: no recorder, preview or meters, so no port and no
	// subscription for any of them.
	e.Stop()

	if strings.Contains(logs.String(), wrongSubscriberError) {
		t.Errorf("stopping an engine whose recorder, preview and meters never started "+
			"logged %q. That line is #711's alarm for a teardown naming the wrong "+
			"subscriber; raised on every clean restart of an idle server, it trains "+
			"whoever reads the log to ignore the one message that means a "+
			"subscription is feeding a dead process.\n\nlog:\n%s", wrongSubscriberError, logs.String())
	}
}

// The positive control: a consumer that DID subscribe is still unsubscribed by
// Stop, so the fix cannot pass by skipping teardown altogether.
func TestStoppingAnEngineStillRemovesSubscribersThatExist(t *testing.T) {
	e, logs := loggedEngine(t)

	e.mu.Lock()
	e.recorder = loudTestProc()
	e.recorderPort = enginePort(t, e, "recorder")
	mustSubscribe(t, e.hub, "recorder", e.recorderPort)
	e.mu.Unlock()

	e.Stop()

	for _, s := range e.hub.Subscribers() {
		if s == "recorder" {
			t.Fatalf("Stop left the recorder subscribed to the hub; the fix for the " +
				"spurious ERROR must not skip a real unsubscribe")
		}
	}
	if strings.Contains(logs.String(), wrongSubscriberError) {
		t.Errorf("a real unsubscribe logged %q", wrongSubscriberError)
	}
}
