package api

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
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
	// The file goes on disk first. The engine's startup recording scan drops
	// every row whose file is missing, so a row with none is deleted whenever
	// that scan lands after this upsert -- the retry route then 404s.
	const name = "rec-20261101-100000.mkv"
	dir := serverUnderTest(t, h).recordings().Dir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte("MASTER"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertRecording(&db.Recording{Filename: name, DurationMS: 1000}); err != nil {
		t.Fatal(err)
	}
	// Run the sweep now, after the upsert: the worst timing the startup scan
	// can land in, on every run rather than on a slow one.
	if _, err := serverUnderTest(t, h).recordings().Scan(); err != nil {
		t.Fatal(err)
	}
	recs, err := store.ListRecordings()
	if err != nil {
		t.Fatal(err)
	}
	var recID int64
	for _, r := range recs {
		if r.Filename == name {
			recID = r.ID
		}
	}
	if recID == 0 {
		t.Fatalf("recording %q is not in the index: %+v", name, recs)
	}
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

func TestAnOffloadStuckAlertNeverCarriesASecret(t *testing.T) {
	ev := auditOffloadStuck(offload.StuckEvent{TargetID: 3, TargetName: "archive", RelPath: "rec-a.mkv", Attempts: 8,
		Error: "the service said: rtmps://live.twitch.tv/app/" + plantedSecret})
	if ev.Key == "" {
		t.Error("no coalescing key: a bucket refusing every file would be one message per file")
	}
	for _, format := range []alerts.Format{alerts.FormatJSON, alerts.FormatDiscord, alerts.FormatSlack} {
		rule := alerts.Rule{ID: 1, Name: "ops", Enabled: true, URL: "https://example.test/hook", Format: format}
		body, _, err := alerts.Encode(alerts.Delivery{Rule: rule.Normalized(), Items: []alerts.Item{{Event: ev.Redacted(), Count: 1}}})
		if err != nil {
			t.Fatalf("Encode(%s): %v", format, err)
		}
		if strings.Contains(string(body), plantedSecret) {
			t.Fatalf("a %s offload.stuck alert carried a secret:\n%s", format, body)
		}
		if !strings.Contains(string(body), "rec-a.mkv") {
			t.Errorf("a %s offload.stuck alert lost the file it is about:\n%s", format, body)
		}
	}
}
