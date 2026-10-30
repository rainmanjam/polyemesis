package overlay

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"

	"github.com/rainmanjam/polyemesis/internal/db"
)

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
}

// Presets. Each is a complete Style; picking one in the editor resets the
// knobs to it, and the knobs then adjust from there.
var presets = map[string]Style{
	"classic": {Preset: "classic", Font: "sans", FontSize: 24, TextColor: "#ffffff",
		BackgroundColor: "#000000", BackgroundOpacity: 0, AuthorColors: true,
		FadeSeconds: 30, MaxLines: 12, ShowPlatform: true, ShowBadges: true},
	"bubble": {Preset: "bubble", Font: "rounded", FontSize: 22, TextColor: "#ffffff",
		BackgroundColor: "#111827", BackgroundOpacity: 75, AuthorColors: true,
		FadeSeconds: 45, MaxLines: 8, ShowPlatform: true, ShowBadges: true},
	"outline": {Preset: "outline", Font: "sans", FontSize: 28, TextColor: "#ffffff",
		BackgroundColor: "#000000", BackgroundOpacity: 0, AuthorColors: false,
		FadeSeconds: 20, MaxLines: 6, ShowPlatform: false, ShowBadges: false},
	"compact": {Preset: "compact", Font: "mono", FontSize: 16, TextColor: "#e5e7eb",
		BackgroundColor: "#000000", BackgroundOpacity: 55, AuthorColors: true,
		FadeSeconds: 0, MaxLines: 20, ShowPlatform: true, ShowBadges: false},
}

// PresetNames is the closed list, in display order.
var PresetNames = []string{"classic", "bubble", "outline", "compact"}

var fonts = map[string]bool{"sans": true, "serif": true, "mono": true, "rounded": true}

// Platforms an overlay can filter on: the ones chat adapters exist for.
var Platforms = []string{
	string(db.PlatformTwitch), string(db.PlatformYouTube), string(db.PlatformKick),
	string(db.PlatformFacebook), string(db.PlatformRumble),
}

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
