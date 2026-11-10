package api

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/alerts"
	"github.com/rainmanjam/polyemesis/internal/engine"
)

// The OPTIONAL ALERT for stop-on-ingest-loss (alerts.TypeIngestAutoStopped).
// It is raised where the stop has actually happened (announceIngestStop), so
// every case a stop can skip is a case that sends nothing.

func autoStopAlerts(o *obsRig) []alerts.Event {
	o.amu.Lock()
	defer o.amu.Unlock()
	var out []alerts.Event
	for _, ev := range *o.audit {
		if ev.Type == alerts.TypeIngestAutoStopped {
			out = append(out, ev)
		}
	}
	return out
}

func alertFieldOf(ev alerts.Event, name string) string {
	for _, f := range ev.Fields {
		if f.Name == name {
			return f.Value
		}
	}
	return ""
}

// FIRES ON A REAL STOP, with the programme, the wait, how long it was lost and
// how many destinations were stopped.
//
// Mutation: drop the publishAudit call in announceIngestStop. Observed to fail
// with "want one ingest.autostopped alert, got 0".
func TestAnIngestLossStopRaisesOneAlertWithTheFacts(t *testing.T) {
	o := obsServer(t)
	src := onlySourceID(t, o.h, o.sign)
	a, b := o.createDest(t, "a"), o.createDest(t, "b")
	o.enable(t, a)
	o.enable(t, b)
	armFor(t, o, src)

	o.s.stopForIngestLoss(context.Background(), engine.IngestLoss{SourceID: src, SourceName: "Main",
		Wait: 120 * time.Second, LostFor: 124 * time.Second, Destinations: []int64{a, b}})

	got := autoStopAlerts(o)
	if len(got) != 1 {
		t.Fatalf("want one ingest.autostopped alert, got %d: %+v", len(got), got)
	}
	ev := got[0]
	if ev.Severity != alerts.SeverityCritical {
		t.Errorf("severity = %q, want critical", ev.Severity)
	}
	if ev.Key != "ingest.autostopped:"+itoa(src) {
		t.Errorf("key = %q: one subject per programme", ev.Key)
	}
	for name, want := range map[string]string{
		"sourceId": itoa(src), "sourceName": "Main", "wait": "2m0s", "lostFor": "2m4s", "stopped": "2",
	} {
		if v := alertFieldOf(ev, name); v != want {
			t.Errorf("field %s = %q, want %q (fields %+v)", name, v, want, ev.Fields)
		}
	}
	if alertFieldOf(ev, "failed") != "" {
		t.Errorf("a clean stop carries a failed count: %+v", ev.Fields)
	}
	if !strings.Contains(ev.Title, "Main") {
		t.Errorf("title %q does not name the programme", ev.Title)
	}
	// Once per stop: a second call finds both destinations already off.
	o.s.stopForIngestLoss(context.Background(), engine.IngestLoss{SourceID: src, SourceName: "Main",
		Wait: 120 * time.Second, LostFor: 130 * time.Second, Destinations: []int64{a, b}})
	if n := len(autoStopAlerts(o)); n != 1 {
		t.Errorf("a second stop with nothing left to stop raised another alert (%d total)", n)
	}
}

// NOT WHEN THE RUN-TIME RE-CHECK FINDS THE INGEST BACK: the stop skips every
// destination and there is nothing to be told.
//
// Mutation: raise the alert at the top of stopForIngestLoss instead of after the
// stop. Observed to fail with "an alert was raised for a stop that did nothing".
func TestNoAlertWhenTheIngestReturnedBeforeTheStopRan(t *testing.T) {
	o := obsServer(t)
	src := onlySourceID(t, o.h, o.sign)
	live := o.createDest(t, "live")
	o.enable(t, live)
	armFor(t, o, src)

	res := o.s.stopForIngestLoss(context.Background(), engine.IngestLoss{SourceID: src, SourceName: "Main",
		Wait: 120 * time.Second, LostFor: 120 * time.Second, Destinations: []int64{live},
		Arriving: func() bool { return true }})
	if acted(res) || !o.enabled(t, live) {
		t.Fatalf("the stop was not skipped: %+v", res)
	}
	if got := autoStopAlerts(o); len(got) != 0 {
		t.Fatalf("an alert was raised for a stop that did nothing: %+v", got)
	}
}

// NOT WHEN AUTO-STOP IS OFF (the default, or turned off while the stop waited).
//
// Mutation: as above. Observed to fail the same way.
func TestNoAlertWhenStopOnIngestLossIsOff(t *testing.T) {
	o := obsServer(t)
	src := onlySourceID(t, o.h, o.sign)
	live := o.createDest(t, "live")
	o.enable(t, live)
	// Not armed: the setting is off.
	o.s.stopForIngestLoss(context.Background(), engine.IngestLoss{SourceID: src, SourceName: "Main",
		Wait: 120 * time.Second, LostFor: 120 * time.Second, Destinations: []int64{live}})
	if !o.enabled(t, live) {
		t.Fatal("an unarmed setting stopped a destination")
	}
	if got := autoStopAlerts(o); len(got) != 0 {
		t.Fatalf("an alert was raised with stop-on-ingest-loss off: %+v", got)
	}
}

// THROUGH THE HANDLER THE ENGINE CALLS, on the path that runs off the
// request: the alert is raised by the real ingestLossStop, not only by the
// helper it ends in.
//
// Mutation: drop the publishAudit call in announceIngestStop. Observed to fail.
func TestIngestLossStopHandlerRaisesTheAlert(t *testing.T) {
	o := obsServer(t)
	src := onlySourceID(t, o.h, o.sign)
	live := o.createDest(t, "live")
	o.enable(t, live)
	armFor(t, o, src)
	o.s.ingestLossStop(engine.IngestLoss{SourceID: src, SourceName: "Main", Wait: 120 * time.Second,
		LostFor: 125 * time.Second, Destinations: []int64{live}})
	waitUntil(t, "the alert", func() bool { return len(autoStopAlerts(o)) == 1 })
}

// A destination the stop could not end is said out loud: "stopped 0" alone would
// hide that something is still on air.
func TestTheAlertSaysWhenADestinationCouldNotBeStopped(t *testing.T) {
	o := obsServer(t)
	src := onlySourceID(t, o.h, o.sign)
	live := o.createDest(t, "live")
	o.enable(t, live)
	armFor(t, o, src)
	// An ID that does not exist fails rather than skips.
	o.s.stopForIngestLoss(context.Background(), engine.IngestLoss{SourceID: src, SourceName: "Main",
		Wait: 120 * time.Second, LostFor: 120 * time.Second, Destinations: []int64{live, 987654}})
	got := autoStopAlerts(o)
	if len(got) != 1 {
		t.Fatalf("want one alert, got %+v", got)
	}
	if alertFieldOf(got[0], "stopped") != "1" || alertFieldOf(got[0], "failed") != "1" {
		t.Errorf("fields %+v, want stopped=1 failed=1", got[0].Fields)
	}
}

// DELIVERY GOES THROUGH THE EXISTING ALERT PATH: the real server, a stored
// rule created through the real route, a real Notifier over the same store and a
// real HTTP receiver. The audit seam hands the event to that Notifier, because
// the engine's own alerter has no Run loop in this fixture (the same limit
// route_ledger_test.go's inspectOutboundAlertBody records).
//
// Mutation: drop the publishAudit call in announceIngestStop (nothing arrives),
// or drop TypeIngestAutoStopped from AllTypes (the rule's subscription is
// normalised away and nothing arrives). Each observed to fail.
func TestTheIngestAutoStoppedAlertIsDeliveredToARuleThatSubscribesToIt(t *testing.T) {
	o := obsServer(t)
	src := onlySourceID(t, o.h, o.sign)
	live := o.createDest(t, "live")
	o.enable(t, live)
	armFor(t, o, src)

	var (
		mu     sync.Mutex
		bodies []string
	)
	recv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(b))
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(recv.Close)
	createRule(t, o.h, o.sign, map[string]any{
		"name": "pager", "url": recv.URL + "/hook", "format": "json", "allowPrivateTarget": true,
		"events": []string{"ingest.autostopped"}, "debounceSeconds": 1, "minIntervalSeconds": 1,
	})

	// The Notifier is built AFTER the rule exists, as inspectOutboundAlertBody
	// does: it reads the stored rules when it starts, not on every event.
	n := alerts.New(slog.New(slog.NewTextHandler(io.Discard, nil)), o.s.store,
		alerts.WithFlushInterval(5*time.Millisecond),
		alerts.WithRetry(1, time.Millisecond, time.Millisecond))
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go n.Run(ctx)
	o.s.auditSink = n.Publish

	o.s.stopForIngestLoss(context.Background(), engine.IngestLoss{SourceID: src, SourceName: "Main",
		Wait: 120 * time.Second, LostFor: 124 * time.Second, Destinations: []int64{live}})

	// Polled and flushed forward rather than waiting out the debounce.
	waitUntil(t, "the webhook delivery", func() bool {
		n.Flush(time.Now().Add(time.Hour))
		mu.Lock()
		defer mu.Unlock()
		return len(bodies) > 0
	})
	mu.Lock()
	defer mu.Unlock()
	for _, want := range []string{"ingest.autostopped", "Main", "2m4s", "critical"} {
		if !strings.Contains(bodies[0], want) {
			t.Errorf("delivered body lacks %q:\n%s", want, bodies[0])
		}
	}
}

// The programme name is operator-typed text, and the alert outlives the incident
// in a chat channel: a secret in it never reaches any encoding.
func TestAnIngestAutoStoppedAlertNeverCarriesASecret(t *testing.T) {
	ev := auditIngestAutoStopped(engine.IngestLoss{SourceID: 4,
		SourceName: "show rtmps://live.twitch.tv/app/" + plantedSecret,
		Wait:       120 * time.Second, LostFor: 125 * time.Second}, 1, 0)
	if ev.Key == "" {
		t.Error("no coalescing key: two programmes would share one subject")
	}
	for _, format := range []alerts.Format{alerts.FormatJSON, alerts.FormatDiscord, alerts.FormatSlack} {
		rule := alerts.Rule{ID: 1, Name: "ops", Enabled: true, URL: "https://example.test/hook", Format: format}
		body, _, err := alerts.Encode(alerts.Delivery{Rule: rule.Normalized(), Items: []alerts.Item{{Event: ev.Redacted(), Count: 1}}})
		if err != nil {
			t.Fatalf("Encode(%s): %v", format, err)
		}
		if strings.Contains(string(body), plantedSecret) {
			t.Fatalf("a %s ingest.autostopped alert carried a secret:\n%s", format, body)
		}
		if !strings.Contains(string(body), "2m5s") {
			t.Errorf("a %s ingest.autostopped alert lost how long the ingest was gone:\n%s", format, body)
		}
	}
}
