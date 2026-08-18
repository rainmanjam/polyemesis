package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/automod"
	"github.com/rainmanjam/polyemesis/internal/chat"
	"github.com/rainmanjam/polyemesis/internal/config"
	"github.com/rainmanjam/polyemesis/internal/db"
)

/* THE SPEND COUNTERS WERE A HARDCODED ZERO.
 *
 * handleAutomodStats returned automod.ModelStats{} behind a comment promising
 * "an honest zero rather than inventing numbers" until the wiring landed. The
 * wiring HAD landed -- automod.Engine.ModelStats() exists and the hub holds the
 * engine -- so the honesty had expired. A zero is honest while the number is
 * unknowable; once it is knowable, a zero is a claim that nothing has been
 * spent, which is exactly how an operator watching their model bill reads it.
 */

// countingModerator is a Moderator that reports a known spend.
type countingModerator struct{ stats automod.ModelStats }

func (c *countingModerator) CheckFast(db.Platform, string, string) automod.Verdict {
	return automod.Verdict{}
}
func (c *countingModerator) CheckModel(context.Context, db.Platform, string) (automod.Verdict, error) {
	return automod.Verdict{}, nil
}
func (c *countingModerator) ModelEnabled() bool             { return true }
func (c *countingModerator) ModelStats() automod.ModelStats { return c.stats }

func TestAutomodStatsComeFromTheLiveEngine(t *testing.T) {
	hub := chat.New()
	want := automod.ModelStats{CallsThisHour: 17, Ceiling: 100}
	hub.SetModerator(&countingModerator{stats: want})

	s, _, _ := testServerWith(t, Options{Config: config.Config{}, Chat: hub})
	if got := s.automodStats(); got.CallsThisHour != want.CallsThisHour {
		t.Errorf("stats report %d calls, want %d — a zero here reads to an "+
			"operator as \"nothing has been spent\"", got.CallsThisHour, want.CallsThisHour)
	}
}

// The zero is still correct where the number is genuinely unknowable.
func TestAutomodStatsAreZeroWhenNothingIsModerating(t *testing.T) {
	t.Run("no chat hub", func(t *testing.T) {
		s, _, _ := testServer(t, config.Config{})
		if got := s.automodStats(); got != (automod.ModelStats{}) {
			t.Errorf("stats = %+v with no hub, want the zero value", got)
		}
	})
	t.Run("hub with no moderator", func(t *testing.T) {
		s, _, _ := testServerWith(t, Options{Config: config.Config{}, Chat: chat.New()})
		if got := s.automodStats(); got != (automod.ModelStats{}) {
			t.Errorf("stats = %+v with no moderator attached, want the zero value", got)
		}
	})
}

// And the route serves them, so the wiring is pinned end to end.
func TestTheAutomodStatsRouteServesTheEnginesCounters(t *testing.T) {
	hub := chat.New()
	hub.SetModerator(&countingModerator{stats: automod.ModelStats{CallsThisHour: 5, Ceiling: 100}})

	_, h, _ := testServerWith(t, Options{Config: config.Config{}, Chat: hub})
	sign := login(t, h)

	r := httptest.NewRequest(http.MethodGet, "/api/v1/automod/stats", nil)
	sign(r)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("GET /automod/stats = %d, want 200", w.Code)
	}
	var got automod.ModelStats
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.CallsThisHour != 5 {
		t.Errorf("route reported %d calls, want 5", got.CallsThisHour)
	}
}

/* SETTING THE API KEY DID NOT TAKE EFFECT UNTIL THE NEXT RESTART.
 *
 * handlePutAutomodKey sealed the key, stored it, and answered
 * {"hasApiKey": true} without rebuilding anything. ApplyAutomod reads the
 * sealed key when it constructs the model checker, and nothing here called it
 * -- so an operator who pasted a key saw "configured" and got no model
 * moderation, and one who rotated a key kept sending the old one to their
 * provider. Every other settings write already calls ApplyAutomod; setting the
 * key is a settings write that forgot to say so.
 *
 * Observed through the moderator the Hub is holding afterwards: before the fix
 * the hub still had whatever it started with, because nothing rebuilt it.
 */
func TestStoringTheAutomodKeyRebuildsTheModelChecker(t *testing.T) {
	hub := chat.New()
	s, h, store := testServerWith(t, Options{Config: config.Config{}, Chat: hub})
	sign := login(t, h)

	// Automod on with a model endpoint, so a rebuild has something to build.
	if _, err := store.UpdateSettings(func(set *db.Settings) error {
		set.Automod = modelSettings()
		return nil
	}); err != nil {
		t.Fatalf("UpdateSettings: %v", err)
	}

	// Nothing is moderating yet.
	if hub.Moderator() != nil {
		t.Fatal("fixture: a moderator was already attached, so a rebuild would " +
			"not be observable")
	}

	body := strings.NewReader(`{"key":"sk-test-value"}`)
	r := httptest.NewRequest(http.MethodPut, "/api/v1/settings/automod-key", body)
	r.Header.Set("Content-Type", "application/json")
	sign(r)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("PUT /automod/key = %d (%s), want 200", w.Code, w.Body.String())
	}
	if hub.Moderator() == nil {
		t.Error("the key was stored and reported as configured, but no model " +
			"checker was built. It would take effect only at the next restart, " +
			"and a rotated key would keep sending the old one until then.")
	}
	_ = s
}
