package overlay

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"

	"github.com/rainmanjam/polyemesis/internal/activity"
	"github.com/rainmanjam/polyemesis/internal/platform"
)

// colorBlack is the black the overlay presets use for backgrounds.
const colorBlack = "#000000"

// colorWhite is the white the overlay presets use for text.
const colorWhite = "#ffffff"

// Style is how an overlay is drawn. Every value ends up in CSS on a page that
// is composited into a broadcast, so every field is CLOSED: a preset and a font
// from a fixed list, colours as #rrggbb, numbers within bounds. Nothing here is
// a free string the page interpolates.
type Style struct {
	Preset            string `json:"preset"`
	Font              string `json:"font"`
	FontSize          int    `json:"fontSize"`
	TextColor         string `json:"textColor"`
	BackgroundColor   string `json:"backgroundColor"`
	BackgroundOpacity int    `json:"backgroundOpacity"`
	// AuthorColors draws names in the colour the viewer picked on the
	// platform, where there is one.
	AuthorColors bool `json:"authorColors"`
	// FadeSeconds removes a line this long after it arrived; 0 keeps it until
	// MaxLines pushes it off.
	FadeSeconds  int  `json:"fadeSeconds"`
	MaxLines     int  `json:"maxLines"`
	ShowPlatform bool `json:"showPlatform"`
	ShowBadges   bool `json:"showBadges"`
	// Alerts is how the ALERTS overlay (/overlay/alerts) draws, on the same
	// overlay row and key. Optional: absent means DefaultAlertStyle, so every
	// stored row stays valid. NOTE FOR DOWNGRADES: ParseStyle refuses unknown
	// fields, so a build that predates this refuses a row saved with it; the
	// CHANGELOG says so.
	Alerts *AlertStyle `json:"alerts,omitempty"`
}

// AlertStyle is how viewer activity is drawn. As closed as Style: layouts and
// sounds come from fixed lists (there is no URL field -- nothing here fetches
// anything), the accent is #rrggbb, the duration is bounded.
type AlertStyle struct {
	// Layout is "banner", "card" or "minimal".
	Layout string `json:"layout"`
	// Kinds are the activity kinds this overlay draws, from
	// activity.OverlayKinds. A kind the operator switched off in the activity
	// settings never reaches any overlay; this narrows one overlay further.
	Kinds []string `json:"kinds"`
	// Seconds is how long one alert stays on screen, 3 to 15.
	Seconds int `json:"seconds"`
	// ShowMessage draws the viewer's own words, if they were released.
	ShowMessage bool `json:"showMessage"`
	// ShowAmount draws the amount of a tip.
	ShowAmount bool `json:"showAmount"`
	// Sound is "none" or one of the tones the page synthesises itself.
	Sound string `json:"sound"`
	// Accent is the highlight colour, #rrggbb.
	Accent string `json:"accent"`
}

// Alert style closed sets and bounds.
const (
	MinAlertSeconds = 3
	MaxAlertSeconds = 15
	// DefaultAlertAccent is Polyemesis's violet.
	DefaultAlertAccent = "#7c3aed"
)

// AlertLayouts and AlertSounds are the closed lists, in display order.
var (
	AlertLayouts = []string{"banner", "card", "minimal"}
	AlertSounds  = []string{"none", "chime", "pop", "bell"}
)

// DefaultAlertStyle is what an overlay with no alerts block draws: every
// overlay kind, a banner, six seconds, words and amounts shown, silent.
func DefaultAlertStyle() AlertStyle {
	kinds := activity.OverlayKinds()
	names := make([]string, len(kinds))
	for i, k := range kinds {
		names[i] = string(k)
	}
	return AlertStyle{
		Layout: "banner", Kinds: names, Seconds: 6,
		ShowMessage: true, ShowAmount: true, Sound: "none", Accent: DefaultAlertAccent,
	}
}

// Validate checks every field against its closed set or bounds.
func (a AlertStyle) Validate() error {
	if !slices.Contains(AlertLayouts, a.Layout) {
		return fmt.Errorf("style: unknown alert layout %q", a.Layout)
	}
	if !slices.Contains(AlertSounds, a.Sound) {
		return fmt.Errorf("style: unknown alert sound %q", a.Sound)
	}
	if a.Seconds < MinAlertSeconds || a.Seconds > MaxAlertSeconds {
		return fmt.Errorf("style: alert seconds must be %d to %d", MinAlertSeconds, MaxAlertSeconds)
	}
	if !hexColor.MatchString(a.Accent) {
		return fmt.Errorf("style: the alert accent must be #rrggbb in lower case")
	}
	if a.Kinds == nil {
		return fmt.Errorf("style: alert kinds are required (an empty list draws nothing)")
	}
	seen := map[string]bool{}
	for _, k := range a.Kinds {
		if !slices.Contains(overlayKindNames(), k) {
			return fmt.Errorf("style: alert kind %q is not one an overlay can draw", k)
		}
		if seen[k] {
			return fmt.Errorf("style: alert kind %q is listed twice", k)
		}
		seen[k] = true
	}
	return nil
}

func overlayKindNames() []string {
	var out []string
	for _, k := range activity.OverlayKinds() {
		out = append(out, string(k))
	}
	return out
}

// AlertsOrDefault is the alert style this overlay draws with.
func (s Style) AlertsOrDefault() AlertStyle {
	if s.Alerts != nil {
		return *s.Alerts
	}
	return DefaultAlertStyle()
}

// Presets. Each is a complete Style; picking one in the editor resets the
// knobs to it, and the knobs then adjust from there.
var presets = map[string]Style{
	"classic": {Preset: "classic", Font: "sans", FontSize: 24, TextColor: colorWhite,
		BackgroundColor: colorBlack, BackgroundOpacity: 0, AuthorColors: true,
		FadeSeconds: 30, MaxLines: 12, ShowPlatform: true, ShowBadges: true},
	"bubble": {Preset: "bubble", Font: "rounded", FontSize: 22, TextColor: colorWhite,
		BackgroundColor: "#111827", BackgroundOpacity: 75, AuthorColors: true,
		FadeSeconds: 45, MaxLines: 8, ShowPlatform: true, ShowBadges: true},
	"outline": {Preset: "outline", Font: "sans", FontSize: 28, TextColor: colorWhite,
		BackgroundColor: colorBlack, BackgroundOpacity: 0, AuthorColors: false,
		FadeSeconds: 20, MaxLines: 6, ShowPlatform: false, ShowBadges: false},
	"compact": {Preset: "compact", Font: "mono", FontSize: 16, TextColor: "#e5e7eb",
		BackgroundColor: colorBlack, BackgroundOpacity: 55, AuthorColors: true,
		FadeSeconds: 0, MaxLines: 20, ShowPlatform: true, ShowBadges: false},
}

// PresetNames is the closed list, in display order.
var PresetNames = []string{"classic", "bubble", "outline", "compact"}

var fonts = map[string]bool{"sans": true, "serif": true, "mono": true, "rounded": true}

// Platforms an overlay can filter on: the ones chat adapters exist for, read
// from the platform registry so a new chat platform needs no edit here.
var Platforms = func() []string {
	var out []string
	for _, r := range platform.ChatOverlays() {
		out = append(out, r.Enum)
	}
	return out
}()

// Bounds.
const (
	MinFontSize = 10
	MaxFontSize = 72
	MaxFade     = 600
	MinLines    = 1
	MaxLines    = 50
)

var hexColor = regexp.MustCompile(`^#[0-9a-f]{6}$`)

// DefaultStyle is the classic preset.
func DefaultStyle() Style { return presets["classic"] }

// ParseStyle decodes and validates a stored or submitted style. An empty body
// is the default; unknown fields are refused so a typo is an error rather than
// a silently ignored setting.
func ParseStyle(raw json.RawMessage) (Style, error) {
	if len(raw) == 0 || string(raw) == "{}" || string(raw) == "null" {
		return DefaultStyle(), nil
	}
	var s Style
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&s); err != nil {
		return Style{}, fmt.Errorf("style: %w", err)
	}
	// An alerts block with no kinds (absent or null) means the default list,
	// not "none": left nil it would be stored as JSON null and the overlay page
	// would have no list to read. An explicit [] stays empty.
	if s.Alerts != nil && s.Alerts.Kinds == nil {
		s.Alerts.Kinds = DefaultAlertStyle().Kinds
	}
	return s, s.Validate()
}

// Validate checks every field against its closed set or bounds.
func (s Style) Validate() error {
	if _, ok := presets[s.Preset]; !ok {
		return fmt.Errorf("style: unknown preset %q", s.Preset)
	}
	if !fonts[s.Font] {
		return fmt.Errorf("style: unknown font %q", s.Font)
	}
	if s.FontSize < MinFontSize || s.FontSize > MaxFontSize {
		return fmt.Errorf("style: fontSize must be %d to %d", MinFontSize, MaxFontSize)
	}
	if !hexColor.MatchString(s.TextColor) || !hexColor.MatchString(s.BackgroundColor) {
		return fmt.Errorf("style: colours must be #rrggbb in lower case")
	}
	if s.BackgroundOpacity < 0 || s.BackgroundOpacity > 100 {
		return fmt.Errorf("style: backgroundOpacity must be 0 to 100")
	}
	if s.FadeSeconds < 0 || s.FadeSeconds > MaxFade {
		return fmt.Errorf("style: fadeSeconds must be 0 to %d", MaxFade)
	}
	if s.MaxLines < MinLines || s.MaxLines > MaxLines {
		return fmt.Errorf("style: maxLines must be %d to %d", MinLines, MaxLines)
	}
	if s.Alerts != nil {
		return s.Alerts.Validate()
	}
	return nil
}

// ValidPlatforms checks a platform filter. Empty means every platform.
func ValidPlatforms(p []string) error {
	known := map[string]bool{}
	for _, k := range Platforms {
		known[k] = true
	}
	seen := map[string]bool{}
	for _, x := range p {
		if !known[x] {
			return fmt.Errorf("unknown platform %q", x)
		}
		if seen[x] {
			return fmt.Errorf("platform %q is listed twice", x)
		}
		seen[x] = true
	}
	return nil
}

// PresetStyle returns a preset's complete style, or the default for an
// unknown name.
func PresetStyle(name string) Style {
	if p, ok := presets[name]; ok {
		return p
	}
	return DefaultStyle()
}
