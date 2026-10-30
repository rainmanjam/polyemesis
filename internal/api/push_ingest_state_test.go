package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/supervisor"
)

// THE SCRAPE'S INGEST STATE FOR A PROGRAMME WITH NO INGEST PROCESS.
//
// An SRT source is received by the manager's shared SRT listener, which hands
// datagrams to the programme's relay hub with no FFmpeg child in between. The
// scrape read the state off that child alone, so an SRT programme read
// polyemesis_ingest_up 0 and ingest_state{state="stopped"} 1 for its whole
// life -- including while its encoder was pushing -- and the "failed" half of
// PolyemesisIngestDown could never fire for it, whatever happened to the port.
//
// The listener IS that programme's ingest, so it carries the state: running
// while it is bound (waiting for or receiving the encoder, exactly as an RTMP
// source's child reads running while it waits), failed when a port is
// configured and nothing holds it, and stopped only when the operator turned
// the port off. Other modes have nothing here: their ingest is the child.
func TestAnSRTProgrammesIngestStateIsItsListeners(t *testing.T) {
	for _, tc := range []struct {
		name  string
		mode  db.IngestMode
		port  int
		bound bool
		want  supervisor.State
	}{
		{"srt, listener up", db.IngestSRT, 6000, true, supervisor.StateRunning},
		{"srt, port configured and not bound", db.IngestSRT, 6000, false, supervisor.StateFailed},
		{"srt, port turned off", db.IngestSRT, 0, false, supervisor.StateStopped},
		{"rtmp with no child", db.IngestRTMP, 6000, true, supervisor.StateStopped},
		{"pull with no child", db.IngestPull, 6000, true, supervisor.StateStopped},
		{"unset", db.IngestUnset, 6000, true, supervisor.StateStopped},
	} {
		if got := childlessIngestState(tc.mode, tc.port, tc.bound); got != tc.want {
			t.Errorf("%s: childlessIngestState = %q, want %q", tc.name, got, tc.want)
		}
	}
}

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
