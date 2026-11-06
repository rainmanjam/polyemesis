package api

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/logtz"
)

// restoreLogZone puts the process-wide log zone back after a test that moves
// it. logtz is a global, so none of these tests may run in parallel.
func restoreLogZone(t *testing.T) {
	t.Helper()
	prev := logtz.Location()
	t.Cleanup(func() { logtz.Set(prev) })
}

// applyServer is a server whose log goes to the returned buffer.
func applyServer(t *testing.T) (*Server, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	hub := newTestHub(t)
	s, _, _ := testServerWith(t, Options{Chat: hub})
	s.log = slog.New(slog.NewTextHandler(&buf, nil))
	return s, &buf
}

// Boot and save are the same function, so a zone that does not resolve is said
// out loud on both. Before, boot warned and the save path fell back to UTC
// without a word.
func TestApplySettingsSaysSoWhenTheDisplayZoneDoesNotResolve(t *testing.T) {
	restoreLogZone(t)
	s, buf := applyServer(t)
	set := db.DefaultSettings()
	set.Display.TimeZone = "Not/AZone"

	s.ApplySettings(set)

	if !strings.Contains(buf.String(), "display time zone does not resolve") {
		t.Errorf("an unresolvable zone fell back to UTC silently; log: %q", buf.String())
	}
	if got := logtz.Location(); got != time.UTC {
		t.Errorf("log zone = %v, want UTC", got)
	}
}

func TestApplySettingsMovesTheLogZone(t *testing.T) {
	restoreLogZone(t)
	s, buf := applyServer(t)
	set := db.DefaultSettings()
	set.Display.TimeZone = "Asia/Tokyo"

	s.ApplySettings(set)

	if got := logtz.Location().String(); got != "Asia/Tokyo" {
		t.Errorf("log zone = %q, want Asia/Tokyo", got)
	}
	if buf.Len() != 0 {
		t.Errorf("a resolvable zone logged: %q", buf.String())
	}
}

// The shared YouTube allowance and the Hub's retention both follow the stored
// settings through the one function, so a boot reaches them the way a save does.
func TestApplySettingsReachesTheQuotaAndTheHub(t *testing.T) {
	restoreLogZone(t)
	s, _ := applyServer(t)
	set := db.DefaultSettings()
	set.Chat.YouTubeQuotaUnits = 4321
	set.Chat.HistoryMessages = 7

	s.ApplySettings(set)

	if got := s.ytQuotaShared.Status().Limit; got != 4321 {
		t.Errorf("shared YouTube allowance = %d, want 4321", got)
	}
}

func TestApplySettingsToleratesNothingToApplyTo(t *testing.T) {
	restoreLogZone(t)
	s := &Server{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	s.ApplySettings(db.DefaultSettings()) // no hub, no manager, no quota: must not panic
}

// Boot applies the stored settings before AnalyticsLoop starts. A wake queued
// then makes the sampler skip its initial delay and sample before chat has
// attached, so boot must leave none behind.
func TestApplySettingsAtBootQueuesNoSamplerWake(t *testing.T) {
	restoreLogZone(t)
	s, _ := applyServer(t)
	if s.audience == nil {
		t.Fatal("the server has no audience sampler, so this test would prove nothing")
	}

	s.ApplySettings(db.DefaultSettings())

	if s.audience.WakePending() {
		t.Error("ApplySettings queued a sampler wake, so the first sample would skip its initial delay")
	}
}

// A save still wakes the running sampler: a lowered interval applies at once.
func TestASettingsSaveWakesTheSampler(t *testing.T) {
	restoreLogZone(t)
	s, h, _, sign := engineServer(t, defaultTools(), Options{})
	var cur map[string]any
	decodeInto(t, send(t, h, sign, http.MethodGet, "/api/v1/settings", nil, http.StatusOK), &cur)
	if s.audience.WakePending() {
		t.Fatal("a wake was already pending before the save")
	}

	send(t, h, sign, http.MethodPut, "/api/v1/settings", cur, http.StatusOK)

	if !s.audience.WakePending() {
		t.Error("a settings save did not wake the sampler")
	}
}
