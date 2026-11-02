package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/alerts"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/delayline"
)

// The dump routes at the REST boundary. The splice itself is proven in
// internal/delayline; here a fake stands in for the engine (this package's
// fixtures have no flowing feed to dump), so what is asserted is the
// handlers' own contract: who may call them, what they validate, which rows
// a dump-all reaches, what they answer and what they audit.

type fakeDumper struct {
	mu       sync.Mutex
	dumped   []int64
	want     []time.Duration
	dumpable map[int64]bool
	err      map[int64]error
}

func (f *fakeDumper) DumpDelay(id int64, want time.Duration) (delayline.DumpResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.err[id]; err != nil {
		return delayline.DumpResult{}, err
	}
	f.dumped = append(f.dumped, id)
	f.want = append(f.want, want)
	return delayline.DumpResult{Requested: want, Dumped: 10 * time.Second, Before: 30 * time.Second,
		After: 20 * time.Second, Complete: true}, nil
}

func (f *fakeDumper) DelayDumpable(id int64) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.dumpable[id]
}

func (f *fakeDumper) calls() []int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.dumped)
}

func dumpRowOn(t *testing.T, s *Server, name string, source int64, delay int, enabled bool) *db.Destination {
	t.Helper()
	row, err := s.store.CreateDestination(&db.Destination{
		Name: name, Kind: db.DestFile, URL: name + ".mkv",
		Enabled: enabled, AudioBitrate: 160, SourceID: &source, DelaySeconds: delay,
	})
	if err != nil {
		t.Fatalf("create %s: %v", name, err)
	}
	return row
}

func TestDumpingOneDestinationAnswersWhatItDidAndAuditsIt(t *testing.T) {
	s, h, _, sign := managerServer(t, defaultTools())
	src := s.eng().SourceID()
	row := dumpRowOn(t, s, "delayed", src, 30, true)
	fake := &fakeDumper{dumpable: map[int64]bool{row.ID: true}}
	s.delayDumper = fake
	var audit []alerts.Event
	s.auditSink = func(ev alerts.Event) { audit = append(audit, ev) }

	body := send(t, h, sign, http.MethodPost, fmt.Sprintf("/api/v1/destinations/%d/delay/dump", row.ID),
		map[string]any{"seconds": 10}, http.StatusOK)
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]any{
		"dumpedSeconds": 10.0, "delayBeforeSeconds": 30.0, "delayAfterSeconds": 20.0,
		"configuredSeconds": 30.0, "requestedSeconds": 10.0, "complete": true, "restoresOnNextStart": true,
	} {
		if got[k] != want {
			t.Errorf("%s = %v, want %v (body %s)", k, got[k], want, body)
		}
	}
	if c := fake.calls(); len(c) != 1 || c[0] != row.ID || fake.want[0] != 10*time.Second {
		t.Fatalf("the dumper was called %v with %v, want once for %d with 10s", c, fake.want, row.ID)
	}
	if len(audit) != 1 || audit[0].Type != alerts.TypeDelayDumped {
		t.Fatalf("audit = %+v, want one delay.dumped", audit)
	}

	// No body is the default dump: everything since the last dump point.
	send(t, h, sign, http.MethodPost, fmt.Sprintf("/api/v1/destinations/%d/delay/dump", row.ID), nil, http.StatusOK)
	if fake.want[1] != 0 {
		t.Errorf("an empty body asked for %s, want 0 (since the last dump point)", fake.want[1])
	}
}

func TestADumpRequestIsValidatedAndRefusedWhereThereIsNothingToDump(t *testing.T) {
	s, h, _, sign := managerServer(t, defaultTools())
	src := s.eng().SourceID()
	delayed := dumpRowOn(t, s, "delayed", src, 30, true)
	plain := dumpRowOn(t, s, "plain", src, 0, true)
	fake := &fakeDumper{err: map[int64]error{delayed.ID: delayline.ErrNotOnAir}}
	s.delayDumper = fake
	path := func(id int64) string { return fmt.Sprintf("/api/v1/destinations/%d/delay/dump", id) }

	for _, bad := range []any{
		map[string]any{"seconds": -1},
		map[string]any{"seconds": 901},
		map[string]any{"seconds": "ten"},
		map[string]any{"secs": 10},
	} {
		send(t, h, sign, http.MethodPost, path(delayed.ID), bad, http.StatusBadRequest)
	}
	send(t, h, sign, http.MethodPost, path(plain.ID), nil, http.StatusConflict)
	send(t, h, sign, http.MethodPost, path(delayed.ID), nil, http.StatusConflict)
	send(t, h, sign, http.MethodPost, path(999999), nil, http.StatusNotFound)
	if c := fake.calls(); len(c) != 0 {
		t.Fatalf("refused requests reached the dumper: %v", c)
	}
}

// DUMP-ALL MEANS THIS PROGRAMME'S DELAYED DESTINATIONS, AND ONLY THOSE.
//
// Mutation: drop the OutputDelaySeconds or Enabled test in dumpAllTargets, or
// list every row instead of the named programme's. Each fails below.
func TestDumpAllTouchesOnlyTheNamedProgrammesDelayedDestinations(t *testing.T) {
	s, h, _, sign := managerServer(t, defaultTools())
	first := s.eng().SourceID()
	second := secondProgramme(t, s)

	mineDelayed := dumpRowOn(t, s, "a delayed", first, 30, true)
	minePlain := dumpRowOn(t, s, "a plain", first, 0, true)
	mineOff := dumpRowOn(t, s, "a delayed but stopped", first, 30, false)
	mineFilling := dumpRowOn(t, s, "a delayed still filling", first, 30, true)
	theirs := dumpRowOn(t, s, "b delayed", second.ID, 30, true)
	fake := &fakeDumper{dumpable: map[int64]bool{
		mineDelayed.ID: true, minePlain.ID: true, mineOff.ID: true, theirs.ID: true,
	}}
	s.delayDumper = fake
	var audit []alerts.Event
	var auditMu sync.Mutex
	s.auditSink = func(ev alerts.Event) { auditMu.Lock(); audit = append(audit, ev); auditMu.Unlock() }

	url := fmt.Sprintf("/api/v1/destinations/delay/dump-all?source=%d", first)
	send(t, h, sign, http.MethodPost, url, map[string]any{"seconds": 5}, http.StatusBadRequest)
	if c := fake.calls(); len(c) != 0 {
		t.Fatalf("an unconfirmed dump-all dumped %v", c)
	}
	body := send(t, h, sign, http.MethodPost, url, map[string]any{"seconds": 5, "confirm": true}, http.StatusOK)
	var out struct {
		Results []struct {
			ID      int64  `json:"id"`
			Outcome string `json:"outcome"`
		} `json:"results"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	if c := fake.calls(); len(c) != 1 || c[0] != mineDelayed.ID {
		t.Fatalf("dump-all on programme %d dumped %v; want only %d (%q). Not the undelayed %d, "+
			"not the stopped %d, not programme %d's %d", first, c, mineDelayed.ID, mineDelayed.Name,
			minePlain.ID, mineOff.ID, second.ID, theirs.ID)
	}
	outcomes := map[int64]string{}
	for _, r := range out.Results {
		outcomes[r.ID] = r.Outcome
	}
	if outcomes[mineDelayed.ID] != "dumped" || outcomes[mineFilling.ID] != "skipped" {
		t.Errorf("outcomes %v: want %d dumped and %d (still filling) skipped", outcomes, mineDelayed.ID, mineFilling.ID)
	}
	for _, id := range []int64{minePlain.ID, mineOff.ID, theirs.ID} {
		if _, listed := outcomes[id]; listed {
			t.Errorf("destination %d is listed in a dump-all it is not part of: %v", id, outcomes)
		}
	}
	if len(audit) != 1 {
		t.Errorf("%d audit events for one dump, want 1", len(audit))
	}
}

// Both dump routes are writes. A read token -- a dashboard, a scraper -- is
// refused before anything is dumped.
func TestAReadTokenCannotDump(t *testing.T) {
	s, h, _, sign := managerServer(t, defaultTools())
	src := s.eng().SourceID()
	row := dumpRowOn(t, s, "delayed", src, 30, true)
	fake := &fakeDumper{dumpable: map[int64]bool{row.ID: true}}
	s.delayDumper = fake
	read := createScopedToken(t, h, sign, "monitoring", db.ScopeRead)
	for _, path := range []string{
		fmt.Sprintf("/api/v1/destinations/%d/delay/dump", row.ID),
		"/api/v1/destinations/delay/dump-all",
	} {
		r := jsonRequest(t, http.MethodPost, path, map[string]any{"confirm": true})
		bearer(read)(r)
		if w := do(t, h, r); w.Code != http.StatusForbidden {
			t.Errorf("a read token got %d from POST %s, want 403", w.Code, path)
		}
	}
	if c := fake.calls(); len(c) != 0 {
		t.Fatalf("a read token dumped %v", c)
	}
}
