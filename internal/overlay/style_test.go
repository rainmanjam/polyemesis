package overlay

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/chat"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/platform"
)

func TestEveryPresetIsValidAndNamed(t *testing.T) {
	if len(PresetNames) != len(presets) {
		t.Fatalf("PresetNames lists %d, presets has %d", len(PresetNames), len(presets))
	}
	for _, n := range PresetNames {
		p, ok := presets[n]
		if !ok || p.Preset != n {
			t.Fatalf("preset %q missing or misnamed", n)
		}
		if err := p.Validate(); err != nil {
			t.Errorf("preset %s: %v", n, err)
		}
	}
}

func TestAStyleIsClosedEverywhereItReachesCSS(t *testing.T) {
	ok := DefaultStyle()
	b, _ := json.Marshal(ok)
	if _, err := ParseStyle(b); err != nil {
		t.Fatalf("default does not round-trip: %v", err)
	}
	bad := map[string]string{
		"unknown preset":    `{"preset":"neon"}`,
		"css injection":     `"red;background:url(//evil)"`,
		"upper-case hex":    `"#FFFFFF"`,
		"font from nowhere": `"Comic Sans"`,
		"huge font":         `200`,
		"zero lines":        `0`,
		"negative fade":     `-1`,
		"opacity > 100":     `101`,
	}
	mutate := map[string]string{
		"css injection": "textColor", "upper-case hex": "backgroundColor",
		"font from nowhere": "font", "huge font": "fontSize", "zero lines": "maxLines",
		"negative fade": "fadeSeconds", "opacity > 100": "backgroundOpacity",
	}
	for name, v := range bad {
		var raw json.RawMessage
		if f, ok := mutate[name]; ok {
			var m map[string]any
			_ = json.Unmarshal(b, &m)
			var val any
			_ = json.Unmarshal([]byte(v), &val)
			m[f] = val
			raw, _ = json.Marshal(m)
		} else {
			raw = json.RawMessage(v)
		}
		if _, err := ParseStyle(raw); err == nil {
			t.Errorf("%s: %s was accepted", name, raw)
		}
	}
	if _, err := ParseStyle(json.RawMessage(`{"preset":"classic","onload":"x"}`)); err == nil {
		t.Error("an unknown field was accepted")
	}
}

func TestAPlatformFilterNamesOnlyChatPlatforms(t *testing.T) {
	if err := ValidPlatforms(nil); err != nil {
		t.Error(err)
	}
	if err := ValidPlatforms([]string{"twitch", "youtube"}); err != nil {
		t.Error(err)
	}
	for _, p := range [][]string{{"custom"}, {"twitch", "twitch"}, {""}} {
		if err := ValidPlatforms(p); err == nil {
			t.Errorf("%v accepted", p)
		}
	}
}

// A NEW CHAT PLATFORM IS NOT REJECTED. Platforms is derived from the platform
// registry, so a platform chat has an adapter for is offered by the overlay
// filter and accepted by ValidPlatforms without anyone editing a list here.
func TestEveryPlatformChatHasAnAdapterForIsAnOverlayFilter(t *testing.T) {
	offered := map[string]bool{}
	for _, name := range Platforms {
		offered[name] = true
	}
	adapters := 0
	for _, row := range platform.All() {
		if row.Enum == "" {
			continue
		}
		if _, ok := chat.ProfileOf(db.Platform(row.Enum)); !ok {
			continue
		}
		adapters++
		if !offered[row.Enum] {
			t.Errorf("chat has an adapter for %s and Platforms does not offer it", row.Enum)
		}
		if err := ValidPlatforms([]string{row.Enum}); err != nil {
			t.Errorf("an overlay filtered to %s is rejected: %v", row.Enum, err)
		}
	}
	if adapters == 0 || len(offered) != adapters {
		t.Errorf("Platforms offers %d, chat has %d adapters", len(offered), adapters)
	}
}

// The order is part of GET /overlays.
func TestOverlayPlatformsKeepTheirListedOrder(t *testing.T) {
	want := []string{"twitch", "youtube", "kick", "facebook", "rumble", "x"}
	if !slices.Equal(Platforms, want) {
		t.Errorf("Platforms = %v, want %v", Platforms, want)
	}
}
