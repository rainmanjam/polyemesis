package api

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/alerts"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/eventstream"
	"github.com/rainmanjam/polyemesis/internal/eventstream/streamtest"
	"github.com/rainmanjam/polyemesis/internal/offload"
)

// A stuck item reaches the events stream as offload.stuck and the alert
// notifier as an offload.stuck alert, naming the target and the file.
func TestAStuckOffloadItemIsRaisedOnTheStreamAndAsAnAlert(t *testing.T) {
	hub := eventstream.NewHub()
	sub := streamtest.Subscribe(t, hub)
	var audits []alerts.Event
	s := &Server{stream: hub, log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		auditSink: func(ev alerts.Event) { audits = append(audits, ev) }}
	s.OffloadStuck(offload.StuckEvent{TargetID: 7, TargetName: "archive", RelPath: "rec-a.mkv",
		Attempts: 8, Error: "PutObject: HTTP 403 AccessDenied"})
	env := streamtest.Expect(t, sub, eventstream.TypeOffloadStuck)
	if !strings.Contains(env.Reason, "archive") || !strings.Contains(env.Reason, "rec-a.mkv") {
		t.Fatalf("event reason %q", env.Reason)
	}
	if len(audits) != 1 || audits[0].Type != alerts.TypeOffloadStuck || audits[0].Key != "offload.stuck:7" {
		t.Fatalf("alerts %+v", audits)
	}
}

func TestRetryNowRoutesResetStuckItems(t *testing.T) {
	h, store, sign := sourceServer(t)
	rec := doJSON(t, h, sign, http.MethodPost, "/api/v1/storage/targets", map[string]any{
		"name": "x", "provider": "s3", "endpoint": "https://s3.example", "bucket": "media-archive",
		"accessKeyId": "fake-access-key", "secretAccessKey": "fake-secret-value-not-real"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	r := &db.Recording{Filename: "rec-20261101-100000.mkv", DurationMS: 1000}
	if err := store.UpsertRecording(r); err != nil {
		t.Fatal(err)
	}
	recs, _ := store.ListRecordings()
	for _, k := range []string{"rec-20261101-100000.mkv", "other.mkv"} {
		_ = store.PutOffloadItem(db.OffloadItem{TargetID: 1, RelPath: k, Kind: db.OffloadKindRecording, Key: "k/" + k,
			State: db.OffloadStuck, Attempts: 8})
	}
	rec = doJSON(t, h, sign, http.MethodPost, fmt.Sprintf("/api/v1/library/recordings/%d/offload/retry", recs[0].ID), map[string]any{})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"retried":1`) {
		t.Fatalf("per item: %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, h, sign, http.MethodPost, "/api/v1/storage/targets/1/retry", map[string]any{})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"retried":1`) {
		t.Fatalf("per target: %d %s", rec.Code, rec.Body.String())
	}
	its, _ := store.ListOffloadItems()
	for _, it := range its {
		if it.State != db.OffloadPending || it.Attempts != 0 {
			t.Fatalf("%+v", it)
		}
	}
	if rec := doJSON(t, h, sign, http.MethodPost, "/api/v1/storage/targets/99/retry", map[string]any{}); rec.Code != http.StatusNotFound {
		t.Fatalf("missing target: %d", rec.Code)
	}
}
