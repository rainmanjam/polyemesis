package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// The same, through the scrape: the fixture's default programme is an SRT
// source on a bound listener with no ingest process, which is every SRT
// install. Mutation: drop the else branch in ingestSnapshots and this reads
// ingest_up 0 / state="stopped", as it did.
func TestTheScrapeReportsAnSRTProgrammesListenerAsItsIngest(t *testing.T) {
	s, h, _, auth := managerServer(t, defaultTools())
	e := s.eng()
	if e == nil || e.Settings().Ingest.Mode != db.IngestSRT || e.Status().Ingest != nil ||
		!s.mgr.ListenerBound(db.IngestSRT) {
		t.Fatal("the fixture's default programme is no longer an SRT source on a bound listener " +
			"with no ingest process, so this test would observe nothing")
	}
	r := jsonRequest(t, http.MethodGet, "/api/v1/metrics", nil)
	auth(r)
	w := do(t, h, r)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/metrics: %d: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	id := itoa(e.SourceID())
	for _, want := range []string{
		`polyemesis_ingest_up{id="` + id + `",`,
		`polyemesis_ingest_state{id="` + id + `",`,
	} {
		found := false
		for _, line := range strings.Split(body, "\n") {
			if !strings.HasPrefix(line, want) {
				continue
			}
			if strings.HasPrefix(want, "polyemesis_ingest_up") {
				found = true
				if !strings.HasSuffix(line, " 1") {
					t.Errorf("SRT programme on a bound listener: %s, want 1", line)
				}
				continue
			}
			if strings.Contains(line, `state="running"`) {
				found = true
				if !strings.HasSuffix(line, " 1") {
					t.Errorf("SRT programme on a bound listener: %s, want 1", line)
				}
			}
		}
		if !found {
			t.Errorf("no %s... sample for the SRT programme", want)
		}
	}
}

// A DISABLED SRT SOURCE READS STOPPED, through the reconcile that reads the
// row. The listener refuses a disabled source's encoder, so "running" would
// claim an ingest that cannot receive. Mutation: drop the line in
// Engine.effectiveSettings that caches the row's Enabled, and this reads
// running.
func TestTheScrapeReportsADisabledSRTProgrammeStopped(t *testing.T) {
	s, h, _, auth := managerServer(t, defaultTools())
	e := s.eng()
	if e == nil || e.Settings().Ingest.Mode != db.IngestSRT || !s.mgr.ListenerBound(db.IngestSRT) {
		t.Fatal("the fixture's default programme is no longer an SRT source on a bound listener")
	}
	row, err := s.store.GetSource(e.SourceID())
	if err != nil {
		t.Fatalf("GetSource: %v", err)
	}
	row.Enabled = false
	if err := s.store.UpdateSource(row); err != nil {
		t.Fatalf("UpdateSource: %v", err)
	}
	if err := e.Reconcile(); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	r := jsonRequest(t, http.MethodGet, "/api/v1/metrics", nil)
	auth(r)
	w := do(t, h, r)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/metrics: %d: %s", w.Code, w.Body.String())
	}
	want := `polyemesis_ingest_up{id="` + itoa(e.SourceID()) + `",`
	found := false
	for _, line := range strings.Split(w.Body.String(), "\n") {
		if strings.HasPrefix(line, want) {
			found = true
			if !strings.HasSuffix(line, " 0") {
				t.Errorf("a disabled SRT programme: %s, want 0", line)
			}
		}
	}
	if !found {
		t.Errorf("no %s... sample", want)
	}
}
