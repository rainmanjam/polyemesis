package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/engine"
	"github.com/rainmanjam/polyemesis/internal/eventstream"
	"github.com/rainmanjam/polyemesis/internal/eventstream/streamtest"
	"github.com/rainmanjam/polyemesis/internal/obsbridge"
)

// Stop-on-ingest-loss, host half. NO REAL WAIT: the engine's clock is tested in
// internal/engine; here the loss is handed over directly.

func putAutoStop(t *testing.T, o *obsRig, src int64, as map[string]any, want int) []byte {
	t.Helper()
	return send(t, o.h, o.sign, http.MethodPut, "/api/v1/sources/"+itoa(src),
		map[string]any{"ingest": map[string]any{"autoStop": as}}, want)
}

func storedAutoStop(t *testing.T, o *obsRig, src int64) db.AutoStopSettings {
	t.Helper()
	row, err := o.s.store.GetSource(src)
	if err != nil {
		t.Fatal(err)
	}
	return row.Ingest.AutoStop
}

// GUARD 7, ON THE SERVER. Stopping ends a YouTube broadcast for good, so
// turning the fallback on needs its own acknowledgement, and the OBS bridge's
// stopAcknowledged (consent to stop WHEN OBS STOPS) is not it.
//
// Mutations: drop the Enabled && !Acknowledged branch of AutoStopSettings.problems
// (cases 1-2 then answer 200); drop the autoStopNewlyEnabledWithoutAck check in
// handleUpdateSource (case 4 then answers 200). Each observed to fail.
func TestEnablingStopOnIngestLossWithoutTheAcknowledgementIsA400(t *testing.T) {
	o := obsServer(t)
	src := onlySourceID(t, o.h, o.sign)

	// 1. No acknowledgement at all.
	body := putAutoStop(t, o, src, map[string]any{"enabled": true, "waitSeconds": 120}, http.StatusBadRequest)
	if !strings.Contains(string(body), "permanently") || !strings.Contains(string(body), "acknowledged") {
		t.Fatalf("the refusal does not carry stop-all's warning: %s", body)
	}
	// 2. Explicitly not acknowledged.
	putAutoStop(t, o, src, map[string]any{"enabled": true, "acknowledged": false}, http.StatusBadRequest)
	// 2b. A programme CREATED with it on and no acknowledgement: the validator
	// every save shares refuses it (the merge check below does not apply to a
	// create).
	body = send(t, o.h, o.sign, http.MethodPost, "/api/v1/sources", map[string]any{
		"name": "Backpack", "ingest": map[string]any{"autoStop": map[string]any{"enabled": true}},
	}, http.StatusBadRequest)
	if !strings.Contains(string(body), "permanently") {
		t.Fatalf("a create without the acknowledgement was refused for another reason: %s", body)
	}
	// 3. The OBS bridge's own acknowledgement, given, does not count.
	o.putConfig(t, map[string]any{"enabled": true, "sourceId": src, "destinations": []int64{},
		"stopOnStream": true, "stopAcknowledged": true}, http.StatusOK)
	putAutoStop(t, o, src, map[string]any{"enabled": true}, http.StatusBadRequest)
	if as := storedAutoStop(t, o, src); as.Enabled {
		t.Fatalf("a refused save changed the row: %+v", as)
	}

	// Accepted with it, and it reads back.
	putAutoStop(t, o, src, map[string]any{"enabled": true, "acknowledged": true, "waitSeconds": 300}, http.StatusOK)
	if got, want := storedAutoStop(t, o, src), (db.AutoStopSettings{Enabled: true, WaitSeconds: 300, Acknowledged: true}); got != want {
		t.Fatalf("stored %+v, want %+v", got, want)
	}

	// 4. A PUT merges over the stored row, so the acknowledgement given above is
	// still stored. Turning it OFF and then ON again by saying nothing must not
	// ride on it.
	putAutoStop(t, o, src, map[string]any{"enabled": false}, http.StatusOK)
	if as := storedAutoStop(t, o, src); !as.Acknowledged || as.Enabled {
		t.Fatalf("after switching off: %+v", as)
	}
	putAutoStop(t, o, src, map[string]any{"enabled": true}, http.StatusBadRequest)
	putAutoStop(t, o, src, map[string]any{"enabled": true, "acknowledged": true}, http.StatusOK)
}

// The wait is bounded server-side, and the bounds are the db's.
func TestStopOnIngestLossWaitIsBoundedByTheServer(t *testing.T) {
	o := obsServer(t)
	src := onlySourceID(t, o.h, o.sign)
	for _, bad := range []int{1, db.MinAutoStopWaitSeconds - 1, db.MaxAutoStopWaitSeconds + 1, -1} {
		putAutoStop(t, o, src, map[string]any{"enabled": true, "acknowledged": true, "waitSeconds": bad}, http.StatusBadRequest)
	}
	for _, ok := range []int{db.MinAutoStopWaitSeconds, db.DefaultAutoStopWaitSeconds, db.MaxAutoStopWaitSeconds} {
		putAutoStop(t, o, src, map[string]any{"enabled": true, "acknowledged": true, "waitSeconds": ok}, http.StatusOK)
	}
}

func (o *obsRig) enable(t *testing.T, id int64) {
	t.Helper()
	if err := o.s.store.SetDestinationEnabled(id, true); err != nil {
		t.Fatal(err)
	}
}

func armFor(t *testing.T, o *obsRig, src int64) {
	t.Helper()
	putAutoStop(t, o, src, map[string]any{"enabled": true, "acknowledged": true, "waitSeconds": 120}, http.StatusOK)
}

// GUARD 6: ONLY THIS PROGRAMME'S LIVE DESTINATIONS. The engine hands over its
// own list, and the host still re-checks each row: one that has moved to
// another programme since, and one already off, are left alone, and a setting
// turned off while the stop waited stops nothing.
//
// Mutation: drop the sameProgramme check in stopForIngestLoss. Observed to fail
// with "a destination on another programme was stopped".
func TestIngestLossStopsOnlyThisProgrammesLiveDestinations(t *testing.T) {
	o := obsServer(t)
	src := onlySourceID(t, o.h, o.sign)
	live := o.createDest(t, "live")
	already := o.createDest(t, "already-off")
	o.enable(t, live)

	second := &db.Source{Name: "other programme", Enabled: true, Ingest: db.DefaultSettings().Ingest}
	if err := o.s.store.CreateSource(second); err != nil {
		t.Fatal(err)
	}
	var created struct {
		Destination struct {
			ID int64 `json:"id"`
		} `json:"destination"`
	}
	decodeInto(t, send(t, o.h, o.sign, http.MethodPost, "/api/v1/destinations", map[string]any{
		"sourceId": second.ID, "name": "elsewhere", "kind": "rtmp",
		"url": "rtmp://example.invalid/live", "streamKey": "k-else", "enabled": true,
	}, http.StatusCreated), &created)
	elsewhere := created.Destination.ID
	o.enable(t, elsewhere)

	loss := engine.IngestLoss{SourceID: src, SourceName: "Main", Wait: 120 * time.Second,
		LostFor: 120 * time.Second, Destinations: []int64{live, already, elsewhere}}

	// Not armed (the default): nothing is touched.
	res := o.s.stopForIngestLoss(context.Background(), loss)
	for _, r := range res {
		if r.Outcome != "skipped" {
			t.Fatalf("an unarmed programme's destinations were acted on: %+v", res)
		}
	}
	if !o.enabled(t, live) || !o.enabled(t, elsewhere) {
		t.Fatal("an unarmed setting stopped a destination")
	}

	armFor(t, o, src)
	res = o.s.stopForIngestLoss(context.Background(), loss)
	if o.enabled(t, live) {
		t.Fatal("the programme's live destination was not stopped")
	}
	if !o.enabled(t, elsewhere) {
		t.Fatal("a destination on another programme was stopped")
	}
	got := map[int64]string{}
	for _, r := range res {
		got[r.ID] = r.Outcome
	}
	if got[live] != "stopped" || got[already] != "skipped" || got[elsewhere] != "failed" {
		t.Fatalf("outcomes %v", got)
	}
}

// GUARD 6: THE LOG LINE AND THE EVENT. The event is emitted by the real
// producer, through the handler the engine calls.
func TestIngestLossStopIsLoggedAndAnnouncedOnTheEventStream(t *testing.T) {
	o := obsServer(t)
	src := onlySourceID(t, o.h, o.sign)
	live := o.createDest(t, "live")
	o.enable(t, live)
	armFor(t, o, src)
	sub := streamtest.Subscribe(t, o.hub)

	o.s.ingestLossStop(engine.IngestLoss{SourceID: src, SourceName: "Main", Wait: 120 * time.Second,
		LostFor: 124 * time.Second, Destinations: []int64{live}})

	env := streamtest.Expect(t, sub, eventstream.TypeIngestAutoStopped)
	raw, _ := json.Marshal(env)
	for _, want := range []string{`"waitSeconds":120`, `"lostForSeconds":124`, `"outcome":"stopped"`, `"name":"Main"`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("event %s lacks %s", raw, want)
		}
	}
	waitUntil(t, "the destination to stop", func() bool { return !o.enabled(t, live) })
	if !strings.Contains(o.logs.String(), "stop-on-ingest-loss stopped") {
		t.Errorf("no log line; log:\n%s", o.logs.String())
	}
	// Nothing left to stop: no second event, no second line.
	o.s.stopForIngestLoss(context.Background(), engine.IngestLoss{SourceID: src, Destinations: []int64{live}})
	if n := strings.Count(o.logs.String(), "stop-on-ingest-loss stopped"); n != 1 {
		t.Errorf("%d log lines for one stop", n)
	}
}

// GUARD 5 THROUGH THE REAL STOP PATH: OBS stops and the ingest is lost at the
// same moment. Whichever order they reach the bridge's one worker, the
// destination is stopped once and lastActions has one stop.
//
// Mutations: make obsActions.Stop press stop on a destination that is already
// off (OBS-second then records a second stop); drop the all-skipped suppression
// in runJob (same). Each observed to fail with "2 stop records".
func TestOBSStopAndIngestLossAtTheSameMomentAreOneStopThroughTheRealPath(t *testing.T) {
	for _, order := range []string{"obs-first", "ingest-first"} {
		t.Run(order, func(t *testing.T) {
			o := obsServer(t)
			src := onlySourceID(t, o.h, o.sign)
			live := o.createDest(t, "live")
			o.putConfig(t, map[string]any{
				"enabled": true, "sourceId": src, "destinations": []int64{live},
				"startOnStream": true, "stopOnStream": true, "stopAcknowledged": true,
			}, http.StatusOK)
			armFor(t, o, src)
			if err := o.b.Handle(obsbridge.Event{Kind: obsbridge.KindStreamStarted, Via: obsbridge.ViaScript}); err != nil {
				t.Fatal(err)
			}
			waitUntil(t, "the start", func() bool { return o.enabled(t, live) && len(o.b.Status().LastActions) == 1 })

			loss := engine.IngestLoss{SourceID: src, Wait: 120 * time.Second, LostFor: 121 * time.Second, Destinations: []int64{live}}
			obsStop := func() {
				if err := o.b.Handle(obsbridge.Event{Kind: obsbridge.KindStreamStopped, Via: obsbridge.ViaScript}); err != nil {
					t.Fatal(err)
				}
			}
			if order == "obs-first" {
				obsStop()
				o.s.ingestLossStop(loss)
			} else {
				o.s.ingestLossStop(loss)
				obsStop()
			}
			waitUntil(t, "the destination to stop", func() bool { return !o.enabled(t, live) })
			// Settle: a barrier job after both.
			o.b.RunIngestStop(src, func(context.Context) []obsbridge.Outcome { return nil })
			time.Sleep(150 * time.Millisecond)

			var stops []obsbridge.ActionRecord
			for _, a := range o.b.Status().LastActions {
				if a.Action == "stop" {
					stops = append(stops, a)
				}
			}
			if len(stops) != 1 {
				t.Fatalf("%d stop records, want 1: %+v", len(stops), o.b.Status().LastActions)
			}
			if order == "ingest-first" && stops[0].Via != obsbridge.ViaIngest {
				t.Errorf("the one stop is via %q, want ingest", stops[0].Via)
			}
		})
	}
}

// GUARD 4, THE BROADCAST DELAY: THE STOP DRAINS, IT DOES NOT CUT. A destination
// holding a 600s delay buffer keeps airing its tail after the ingest stops;
// stop-all would cut it, the fallback lets it play out and then end.
//
// Mutation: pass true as cut in stopForIngestLoss. Observed to fail with
// `phase "", want draining`.
func TestIngestLossStopLetsABroadcastDelayTailPlayOut(t *testing.T) {
	s, h, sign, ids := delayServer(t, 1)
	id := ids[0]
	src := onlySourceID(t, h, sign)
	send(t, h, sign, http.MethodPut, "/api/v1/sources/"+itoa(src),
		map[string]any{"ingest": map[string]any{"autoStop": map[string]any{"enabled": true, "acknowledged": true}}}, http.StatusOK)

	res := s.stopForIngestLoss(context.Background(), engine.IngestLoss{
		SourceID: src, Wait: 120 * time.Second, LostFor: 120 * time.Second, Destinations: []int64{id}})
	if len(res) != 1 || res[0].Outcome == "failed" {
		t.Fatalf("stop: %+v", res)
	}
	if p := phaseOf(s, id); p != engine.DelayDraining {
		t.Fatalf("phase %q, want draining: the buffered tail must play out", p)
	}
}
