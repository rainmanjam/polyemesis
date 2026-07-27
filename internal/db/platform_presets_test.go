package db

import (
	"net/url"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/routing"
)

// The catalogue is data, so the tests pin the invariants that make the data
// safe rather than the contents of any one entry: a preset must never produce a
// destination that Validate() rejects, and must never present a hostname we
// only guessed at.

func TestDestinationPresetsAreWellFormed(t *testing.T) {
	seen := map[string]bool{}
	groups := map[PresetGroup]bool{}
	for _, g := range PresetGroups() {
		groups[g.Key] = true
	}

	for _, p := range DestinationPresets() {
		t.Run(p.ID, func(t *testing.T) {
			if p.ID == "" {
				t.Fatal("preset has an empty id")
			}
			if seen[p.ID] {
				t.Fatalf("duplicate preset id %q", p.ID)
			}
			seen[p.ID] = true

			if strings.TrimSpace(p.Name) == "" {
				t.Error("preset has no display name")
			}
			if !groups[p.Group] {
				t.Errorf("preset group %q is not in PresetGroups()", p.Group)
			}
			switch p.Transport {
			case PresetRTMP, PresetRTMPS, PresetSRT, PresetHLS:
			default:
				t.Errorf("unknown transport %q", p.Transport)
			}
			// Notes are the whole value of an entry whose URL is empty, and
			// still the place a URL's placeholders get explained.
			if strings.TrimSpace(p.Notes) == "" {
				t.Error("preset has no notes")
			}
			if p.HelpURL != "" && !strings.HasPrefix(p.HelpURL, "https://") {
				t.Errorf("help URL %q is not https", p.HelpURL)
			}
		})
	}
}

func TestDestinationPresetKindMatchesTransport(t *testing.T) {
	// A preset's Kind is what the dialog writes into the destination, so a
	// mismatch here is a destination that fails Validate() the moment it is
	// saved. HLS has no Kind at all: that is the entry admitting we cannot
	// publish it, not an oversight.
	for _, p := range DestinationPresets() {
		t.Run(p.ID, func(t *testing.T) {
			want := map[PresetTransport]DestKind{
				PresetRTMP:  DestRTMP,
				PresetRTMPS: DestRTMP,
				PresetSRT:   DestSRT,
				PresetHLS:   "",
			}[p.Transport]
			if p.Kind != want {
				t.Errorf("transport %q maps to kind %q, want %q", p.Transport, p.Kind, want)
			}
			if p.Supported() != (p.Kind != "") {
				t.Errorf("Supported() = %v but kind is %q", p.Supported(), p.Kind)
			}
		})
	}
}

func TestDestinationPresetURLsMatchTheirTransport(t *testing.T) {
	// An empty URL is the correct answer for a platform whose ingest is issued
	// per account or per event; a wrong scheme is never correct.
	wantScheme := map[PresetTransport]string{
		PresetRTMP:  "rtmp",
		PresetRTMPS: "rtmps",
		PresetSRT:   "srt",
	}

	for _, p := range DestinationPresets() {
		t.Run(p.ID, func(t *testing.T) {
			if p.URL == "" {
				if p.HasURL() {
					t.Error("HasURL() is true for an empty URL")
				}
				return
			}
			scheme, ok := wantScheme[p.Transport]
			if !ok {
				t.Fatalf("transport %q must not prefill a URL", p.Transport)
			}
			if !strings.HasPrefix(p.URL, scheme+"://") {
				t.Errorf("URL %q does not start with %s://", p.URL, scheme)
			}
			// A template is only honest if its placeholders are visible. A URL
			// with no braces is one we are asserting works as written.
			if strings.Count(p.URL, "{") != strings.Count(p.URL, "}") {
				t.Errorf("URL %q has unbalanced placeholder braces", p.URL)
			}
			// Placeholders would trip url.Parse on some inputs, so only
			// concrete URLs are parsed — those are the ones we are claiming.
			if !strings.Contains(p.URL, "{") {
				if _, err := url.Parse(p.URL); err != nil {
					t.Errorf("URL %q does not parse: %v", p.URL, err)
				}
			}
		})
	}
}

func TestConcretePresetsBuildValidDestinations(t *testing.T) {
	// The end-to-end invariant: take every preset that prefills a usable URL,
	// build the destination the dialog would build from it, and require that
	// Validate() accepts it. This is what stops a catalogue entry from being a
	// trap that only shows itself at save time.
	for _, p := range DestinationPresets() {
		if !p.Supported() || !p.HasURL() || strings.Contains(p.URL, "{") {
			continue
		}
		t.Run(p.ID, func(t *testing.T) {
			d := Destination{
				Name:         p.Name,
				Kind:         p.Kind,
				Platform:     p.Platform,
				URL:          p.URL,
				AudioBitrate: 160,
				Profile:      routing.DefaultProfile(),
			}
			if d.Platform == "" {
				d.Platform = PlatformCustom
			}
			if err := d.Validate(); err != nil {
				t.Errorf("preset does not build a valid destination: %v", err)
			}
		})
	}
}

func TestPresetPlatformsAreKnownIntegrations(t *testing.T) {
	// Platform is validated by Destination.Validate(), so the catalogue may
	// only reference values that exist. Everything else saves as custom, which
	// is what lets the catalogue grow without touching validation.
	known := map[Platform]bool{
		PlatformYouTube:  true,
		PlatformTwitch:   true,
		PlatformKick:     true,
		PlatformFacebook: true,
		PlatformCustom:   true,
		"":               true,
	}
	for _, p := range DestinationPresets() {
		if !known[p.Platform] {
			t.Errorf("preset %q names unknown platform %q", p.ID, p.Platform)
		}
	}
}

func TestOAuthPlatformsKeepAPreset(t *testing.T) {
	// The OAuth-capable platforms shipped before the catalogue existed and must
	// still be reachable from it, or adding presets would have removed a
	// feature.
	for _, want := range []Platform{PlatformYouTube, PlatformTwitch, PlatformKick, PlatformFacebook} {
		if got := DestinationPresetsForPlatform(want); len(got) == 0 {
			t.Errorf("no preset for platform %q", want)
		}
	}
}

func TestDestinationPresetByID(t *testing.T) {
	tests := []struct {
		name  string
		id    string
		found bool
	}{
		{"known id resolves", "youtube", true},
		{"generic entries resolve", "generic-srt", true},
		{"unknown id is a miss, not an error", "myspace", false},
		{"empty id is a miss", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, ok := DestinationPresetByID(tt.id)
			if ok != tt.found {
				t.Fatalf("found = %v, want %v", ok, tt.found)
			}
			if ok && p.ID != tt.id {
				t.Errorf("returned preset %q for id %q", p.ID, tt.id)
			}
		})
	}
}

func TestDestinationPresetsCopyIsIndependent(t *testing.T) {
	// Callers serialise this straight out of a handler; one that sorts the
	// slice in place must not change what the next request sees.
	first := DestinationPresets()
	original := first[0].Name
	first[0].Name = "mutated"

	if second := DestinationPresets(); second[0].Name != original {
		t.Errorf("catalogue leaked a mutation: got %q, want %q", second[0].Name, original)
	}
}

func TestPresetGroupsCoverTheCatalogue(t *testing.T) {
	// A group with no presets is a dead heading in the picker; a preset whose
	// group has no heading is a preset the operator can never scroll to.
	used := map[PresetGroup]int{}
	for _, p := range DestinationPresets() {
		used[p.Group]++
	}
	for _, g := range PresetGroups() {
		if used[g.Key] == 0 {
			t.Errorf("group %q (%s) has no presets", g.Key, g.Label)
		}
	}
}
