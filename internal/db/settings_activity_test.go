package db

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/activity"
)

func TestActivityDefaultsAreOnWithoutReactions(t *testing.T) {
	a := DefaultSettings().Activity
	if !a.Enabled || !a.OverlayMessages || a.Reactions {
		t.Fatalf("defaults: %+v", a)
	}
	for _, k := range activity.AllKinds() {
		want := k != activity.KindReaction
		if got := a.KindEnabled("twitch", k); got != want {
			t.Errorf("%s enabled by default = %v, want %v", k, got, want)
		}
	}
	if got := len(DefaultActivityKinds()); got != len(activity.AllKinds())-1 {
		t.Errorf("default kinds %d", got)
	}
}

func TestActivityKindEnabledHonoursListSwitchAndReactions(t *testing.T) {
	a := ActivitySettings{Enabled: true, Kinds: map[string][]string{
		"kick":    {"follow", "tip"},
		"youtube": {},
		"twitch":  {"reaction", "follow"},
	}}
	if !a.KindEnabled("kick", activity.KindTip) || a.KindEnabled("kick", activity.KindRaid) {
		t.Error("an explicit list is exactly the list")
	}
	if a.KindEnabled("youtube", activity.KindFollow) {
		t.Error("an empty list switches the platform off")
	}
	if !a.KindEnabled("rumble", activity.KindRaid) {
		t.Error("an unlisted platform uses the defaults")
	}
	if a.KindEnabled("twitch", activity.KindReaction) {
		t.Error("reactions are off until Reactions is on, even when listed")
	}
	a.Reactions = true
	if !a.KindEnabled("twitch", activity.KindReaction) || !a.KindEnabled("rumble", activity.KindReaction) {
		t.Error("Reactions should enable the tally")
	}
	a.Enabled = false
	if a.KindEnabled("kick", activity.KindTip) {
		t.Error("the master switch is off")
	}
}

func TestActivityBelowMinimumIsPerCurrencyAndUnit(t *testing.T) {
	a := ActivitySettings{MinTip: []MinAmount{
		{Currency: "USD", Minor: 500}, {Unit: "bits", Units: 100}, {Unit: "kicks", Units: 50},
	}}
	cases := []struct {
		amt  *activity.Amount
		want bool
	}{
		{&activity.Amount{Currency: "USD", Minor: 499}, true},
		{&activity.Amount{Currency: "USD", Minor: 500}, false},
		{&activity.Amount{Currency: "JPY", Minor: 1}, false},
		{&activity.Amount{Unit: "bits", Units: 99}, true},
		{&activity.Amount{Unit: "bits", Units: 100}, false},
		{&activity.Amount{Unit: "kicks", Units: 10}, true},
		{&activity.Amount{Unit: "stars", Units: 1}, false},
		{nil, false},
	}
	for _, c := range cases {
		if got := a.BelowMinimum(c.amt); got != c.want {
			t.Errorf("%+v below = %v, want %v", c.amt, got, c.want)
		}
	}
}

func TestActivityKindsAreReplacedNotMergedOnDecode(t *testing.T) {
	a := DefaultActivitySettings()
	a.Kinds = map[string][]string{"twitch": {"follow"}, "kick": {"tip"}}
	if err := json.Unmarshal([]byte(`{"kinds":{"kick":["raid"]}}`), &a); err != nil {
		t.Fatal(err)
	}
	if _, still := a.Kinds["twitch"]; still || len(a.Kinds["kick"]) != 1 || a.Kinds["kick"][0] != "raid" {
		t.Errorf("kinds were merged: %v", a.Kinds)
	}
	// A document that does not mention kinds leaves them alone.
	if err := json.Unmarshal([]byte(`{"reactions":true}`), &a); err != nil {
		t.Fatal(err)
	}
	if len(a.Kinds) != 1 || !a.Reactions {
		t.Errorf("an untouched map was cleared: %v", a.Kinds)
	}
	if err := json.Unmarshal([]byte(`{"kinds":`), &a); err == nil {
		t.Error("broken json decoded")
	}
}

func TestActivitySettingsValidation(t *testing.T) {
	bad := map[string]ActivitySettings{
		"unknown platform": {Kinds: map[string][]string{"myspace": {"follow"}}},
		"unknown kind":     {Kinds: map[string][]string{"twitch": {"sneeze"}}},
		"duplicate kind":   {Kinds: map[string][]string{"twitch": {"follow", "follow"}}},
		"money and unit":   {MinTip: []MinAmount{{Currency: "USD", Minor: 1, Unit: "bits", Units: 1}}},
		"neither":          {MinTip: []MinAmount{{}}},
		"lower currency":   {MinTip: []MinAmount{{Currency: "usd", Minor: 1}}},
		"short currency":   {MinTip: []MinAmount{{Currency: "US", Minor: 1}}},
		"negative money":   {MinTip: []MinAmount{{Currency: "USD", Minor: -1}}},
		"negative units":   {MinTip: []MinAmount{{Unit: "bits", Units: -1}}},
		"units no name":    {MinTip: []MinAmount{{Units: 5}}},
		"upper unit":       {MinTip: []MinAmount{{Unit: "Bits", Units: 5}}},
		"long unit":        {MinTip: []MinAmount{{Unit: strings.Repeat("a", 17), Units: 5}}},
		"repeat":           {MinTip: []MinAmount{{Currency: "USD", Minor: 1}, {Currency: "USD", Minor: 2}}},
		"too many":         {MinTip: make([]MinAmount, MaxActivityMinAmounts+1)},
	}
	for name, a := range bad {
		s := DefaultSettings()
		s.Activity = a
		if err := s.Validate(); err == nil {
			t.Errorf("%s: validated", name)
		}
	}
	ok := DefaultSettings()
	ok.Activity = ActivitySettings{Enabled: true, Kinds: map[string][]string{"twitch": {"follow", "tip"}, "x": {}},
		MinTip: []MinAmount{{Currency: "USD", Minor: 500}, {Unit: "bits", Units: 100}}}
	if err := ok.Validate(); err != nil {
		t.Errorf("a good document was refused: %v", err)
	}
}

func TestActivitySettingsSurviveTheStore(t *testing.T) {
	d := testDB(t)
	got, err := d.GetSettings()
	if err != nil {
		t.Fatal(err)
	}
	if !got.Activity.Enabled {
		t.Fatalf("a fresh store reads activity off: %+v", got.Activity)
	}
	if _, err := d.UpdateSettings(func(s *Settings) error {
		s.Activity.Kinds = map[string][]string{"kick": {"follow"}}
		s.Activity.MinTip = []MinAmount{{Unit: "kicks", Units: 20}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	got, _ = d.GetSettings()
	if len(got.Activity.Kinds["kick"]) != 1 || got.Activity.MinTip[0].Units != 20 {
		t.Errorf("not persisted: %+v", got.Activity)
	}
}
