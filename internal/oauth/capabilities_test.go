package oauth

import (
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// The matrix is a promise to the operator, so these tests pin the claims
// themselves rather than the plumbing. A row that quietly flips from "not
// possible" to "works" — or the reverse — is a support ticket either way.

func TestPlatformCapabilitiesReportVerifiedSupportPerCapability(t *testing.T) {
	tests := []struct {
		name    string
		preset  string
		cap     Capability
		want    Support
		because string
	}{
		{"kick signs in", "kick", CapSSO, SupportYes,
			"Kick's OAuth 2.1 flow is implemented, and losing it would lose chat and metadata with it"},
		{"kick fetches its own stream key", "kick", CapStreamKey, SupportYes,
			"stream.key on the channels resource, over the streamkey:read scope. This " +
				"read SupportManual for a long time on the belief that no Kick endpoint " +
				"returned a key -- there is no /streamkey endpoint, but the key rides on " +
				"the channels response we already fetch"},
		{"kick moderates", "kick", CapModeration, SupportYes,
			"DELETE /public/v1/chat/{id} plus the moderation scopes"},
		{"kick reports viewers", "kick", CapViewerStats, SupportYes,
			"the livestreams endpoints carry a viewer count"},

		{"facebook fetches its key", "facebook", CapStreamKey, SupportYes,
			"live_videos returns the RTMPS ingest, which polyemesis splits into URL and key"},
		{"facebook reads comments", "facebook", CapChatRead, SupportYes,
			"live-video comments are readable over the Graph API"},

		{"youtube fetches its key", "youtube", CapStreamKey, SupportYes, "Data API ingestion"},
		{"twitch fetches its key", "twitch", CapStreamKey, SupportYes, "Helix stream key"},

		{"x cannot sign in", "x", CapSSO, SupportNo,
			"the X API covers posts, not live-video ingest"},
		{"x key is pasted", "x", CapStreamKey, SupportManual,
			"the destination still works; only the automation is absent"},

		{"instagram cannot be streamed to", "instagram", CapStreamKey, SupportNo,
			"RTMP was removed for most accounts, so there is not even a key to paste"},
		{"instagram has no sign-in", "instagram", CapSSO, SupportNo,
			"Instagram publishes no Live broadcast API"},

		{"rumble sign-in is unverified, not refused", "rumble", CapSSO, SupportUnknown,
			"the API sits behind a login wall; undocumented is not the same as absent"},
		{"rumble key is pasted", "rumble", CapStreamKey, SupportManual, "Rumble Studio issues both fields"},

		{"dlive sign-in is unverified, not refused", "dlive", CapSSO, SupportUnknown,
			"the developer portal does not resolve, which tells us nothing about the API itself"},
		{"dlive key is pasted", "dlive", CapStreamKey, SupportManual, "dashboard → stream settings"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			row := CapabilityForPreset(tc.preset, "")
			if got := row.Get(tc.cap); got != tc.want {
				t.Fatalf("%s %s = %q, want %q (%s)", tc.preset, tc.cap, got, tc.want, tc.because)
			}
		})
	}
}

func TestPlatformCapabilitiesAssignTheDocumentedTier(t *testing.T) {
	tests := []struct {
		preset string
		want   Tier
	}{
		{"youtube", TierIntegrated},
		{"twitch", TierIntegrated},
		{"facebook", TierIntegrated},
		// The whole point of a third tier: Kick is neither fully integrated nor
		// a plain paste-the-key platform, and calling it either would mislead.
		{"kick", TierPartial},
		{"x", TierManual},
		{"rumble", TierManual},
		{"dlive", TierManual},
		{"instagram", TierUnsupported},
	}

	for _, tc := range tests {
		t.Run(tc.preset, func(t *testing.T) {
			row := CapabilityForPreset(tc.preset, "")
			if row.Tier != tc.want {
				t.Fatalf("%s tier = %q, want %q", tc.preset, row.Tier, tc.want)
			}
		})
	}
}

func TestOnlyInstagramIsMarkedUnstreamable(t *testing.T) {
	got := UnsupportedPresets()
	if len(got) != 1 || got[0] != "instagram" {
		t.Fatalf("unsupported presets = %v, want [instagram] — a platform marked unsupported "+
			"disappears from the picker's happy path, so the list must stay evidence-backed", got)
	}
	row := CapabilityForPreset("instagram", "")
	if row.Streamable() {
		t.Fatal("instagram reports streamable; a destination that never connects looks like our bug")
	}
	for _, id := range []string{"kick", "x", "rumble", "dlive"} {
		if !CapabilityForPreset(id, "").Streamable() {
			t.Fatalf("%s reports unstreamable; a manual key is a supported destination", id)
		}
	}
}

func TestUnknownPresetFallsOpenToManualRatherThanRefusing(t *testing.T) {
	row := CapabilityForPreset("some-platform-we-have-never-heard-of", "Some Platform")

	if row.Tier != TierManual {
		t.Fatalf("tier = %q, want %q: an unlisted platform still streams fine", row.Tier, TierManual)
	}
	if row.Name != "Some Platform" {
		t.Fatalf("name = %q, want the caller's name", row.Name)
	}
	if got := row.Get(CapStreamKey); got != SupportManual {
		t.Fatalf("stream key = %q, want %q", got, SupportManual)
	}
	// Every capability we have not investigated must read as unverified. A
	// SupportNo here would be a claim about an API nobody in this repo has
	// read, which is exactly the restrictive-check mistake.
	for _, c := range []Capability{CapSSO, CapMetadata, CapChatRead, CapChatSend, CapModeration, CapViewerStats} {
		if got := row.Get(c); got != SupportUnknown {
			t.Errorf("%s = %q, want %q for a platform we have not researched", c, got, SupportUnknown)
		}
	}
	if !row.Streamable() {
		t.Fatal("an unlisted platform reports unstreamable; that would refuse a working RTMP endpoint")
	}
}

func TestMissingCapabilityKeyReadsAsUnverifiedNotUnsupported(t *testing.T) {
	row := PlatformCapability{PresetID: "sparse", Caps: map[Capability]Support{}}
	for _, c := range []Capability{CapSSO, CapStreamKey, CapMetadata, CapChatRead} {
		if got := row.Get(c); got != SupportUnknown {
			t.Errorf("absent %s = %q, want %q", c, got, SupportUnknown)
		}
	}
}

func TestCapabilityForPlatformResolvesIntegratedPlatformsOnly(t *testing.T) {
	tests := []struct {
		name     string
		platform db.Platform
		wantOK   bool
		wantID   string
	}{
		{"youtube", db.PlatformYouTube, true, "youtube"},
		{"twitch", db.PlatformTwitch, true, "twitch"},
		{"kick", db.PlatformKick, true, "kick"},
		// Facebook became a real Platform once it had integration code behind
		// it; the matrix must resolve it like the other three.
		{"facebook", db.PlatformFacebook, true, "facebook"},
		{"custom has no platform capabilities", db.PlatformCustom, false, ""},
		{"empty platform has no row", db.Platform(""), false, ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			row, ok := CapabilityForPlatform(tc.platform)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if ok && row.PresetID != tc.wantID {
				t.Fatalf("presetId = %q, want %q", row.PresetID, tc.wantID)
			}
		})
	}
}

func TestEveryMatrixRowIsRenderableAndHonest(t *testing.T) {
	columns := CapabilityColumns()

	for _, row := range PlatformCapabilities() {
		t.Run(row.PresetID, func(t *testing.T) {
			if row.Name == "" || row.Summary == "" || row.Tier == "" {
				t.Fatal("a row missing its name, summary or tier renders as an empty table cell")
			}
			// Every column must carry an explicit value on a researched row:
			// this is where the sourcing rule is enforced, since a forgotten
			// key would silently become "unverified" and look researched.
			for _, col := range columns {
				if _, ok := row.Caps[col.Key]; !ok {
					t.Errorf("no value for %q", col.Key)
				}
			}
			for c := range row.Caps {
				known := false
				for _, col := range columns {
					if col.Key == c {
						known = true
					}
				}
				if !known {
					t.Errorf("capability %q has no column, so it renders nowhere", c)
				}
			}
			// A reason attached to a capability the row does not declare is a
			// tooltip on a cell that does not exist.
			for c := range row.Reasons {
				if _, ok := row.Caps[c]; !ok {
					t.Errorf("reason for %q, which the row does not declare", c)
				}
			}
			// Anything we say polyemesis cannot do at all owes the operator a
			// sentence explaining why, or it reads as an arbitrary refusal.
			if row.Tier == TierUnsupported && row.ReadFirst == "" {
				t.Error("an unsupported platform with no explanation is indistinguishable from a bug")
			}
		})
	}
}

func TestLegendsCoverEverySupportAndTierUsedInTheMatrix(t *testing.T) {
	inLegend := map[Support]bool{}
	for _, s := range SupportLegend() {
		inLegend[s.Key] = true
	}
	tiers := map[Tier]bool{}
	for _, ti := range TierLegend() {
		tiers[ti.Key] = true
	}

	for _, row := range PlatformCapabilities() {
		if !tiers[row.Tier] {
			t.Errorf("%s: tier %q has no legend entry", row.PresetID, row.Tier)
		}
		for c, s := range row.Caps {
			if !inLegend[s] {
				t.Errorf("%s %s: support %q has no legend entry, so the cell has no meaning",
					row.PresetID, c, s)
			}
		}
	}
}

func TestMatrixPresetIDsExistInTheDestinationCatalogue(t *testing.T) {
	// The matrix joins to db.DestinationPresets() by id. A typo here would
	// leave a row that never renders next to the platform it describes.
	known := map[string]bool{}
	for _, p := range db.DestinationPresets() {
		known[p.ID] = true
	}
	for _, row := range PlatformCapabilities() {
		if !known[row.PresetID] {
			t.Errorf("preset id %q is not in the destination catalogue", row.PresetID)
		}
	}
}

func TestPlatformCapabilitiesCannotBeMutatedByCallers(t *testing.T) {
	first := PlatformCapabilities()
	original := first[0].Name
	first[0].Name = "tampered"

	if PlatformCapabilities()[0].Name != original {
		t.Fatal("the matrix is shared mutable state; one handler could rewrite it for every other")
	}
}
