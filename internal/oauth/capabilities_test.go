package oauth

import (
	"strings"
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

		// X's row said SupportNo on the false belief that "the X API covers
		// posts, not live-video ingest". X's served spec declares a Broadcasts
		// family of 13 operations and a Chat family of 16. It then said
		// SupportYes for an hour, which was the opposite error: the API exists,
		// the provider is not registered, and a "Works" an operator cannot act
		// on is worse than an "Unverified" that invites them to try.
		{"x sign-in is documented at the platform and unbuilt here", "x", CapSSO, SupportUnknown,
			"the API is real; nothing wires it yet, and the enum cannot say that directly"},
		{"x key is pasted", "x", CapStreamKey, SupportManual,
			"the destination still works; only the automation is absent"},

		{"instagram cannot be streamed to", "instagram", CapStreamKey, SupportNo,
			"RTMP was removed for most accounts, so there is not even a key to paste"},
		{"instagram has no sign-in", "instagram", CapSSO, SupportNo,
			"Instagram publishes no Live broadcast API"},

		// Was SupportUnknown on the sound argument that undocumented is not
		// absent. Now refused on affirmative evidence rather than more looking:
		// rumble.com/oauth/authorize answers an honest 404, and Rumble's one
		// API article states authentication is not required for it.
		{"rumble sign-in is refused, on a probe rather than a search", "rumble", CapSSO, SupportNo,
			"oauth/authorize is a genuine 404 and the published API declares no auth"},
		{"rumble key is pasted", "rumble", CapStreamKey, SupportManual, "Rumble Studio issues both fields"},
		// Rumble's chat came from the live-stream API, which is a different
		// surface from the account API page the row used to be written about.
		{"rumble chat is read", "rumble", CapChatRead, SupportYes,
			"the live-stream API carries chat and is keyed from the operator's own settings, not a partner programme"},
		// The half of that row that did NOT move, and the more important half to
		// pin: shipping chat read is not evidence about sending, and this row
		// must not drift into a yes because the platform now feels integrated.
		// Both were SupportUnknown because the surface had "barely been seen".
		// It has now been enumerated -- a complete 158-article knowledge base,
		// whose entire published API is one read-only snapshot GET -- so the
		// absence is read off a small surface fully covered, not a large one
		// sampled.
		{"rumble chat send is refused once the whole surface has been read", "rumble", CapChatSend, SupportNo,
			"the published API is a single read-only request; no send path exists to document"},
		{"rumble moderation is refused, and Rumble says so in its own words", "rumble", CapModeration, SupportNo,
			"Rumble documents moderation as clicking three dots in live chat, and never mentions an API"},
		{"rumble metadata is a template set in the account UI", "rumble", CapMetadata, SupportManual,
			"no write API exists, but a live-stream template applies title and category automatically"},

		{"dlive sign-in is unverified, not refused", "dlive", CapSSO, SupportUnknown,
			"the developer portal does not resolve, which tells us nothing about the API itself"},
		{"dlive key is pasted", "dlive", CapStreamKey, SupportManual, "dashboard → stream settings"},

		// ---- broadcastLifecycle. The column exists because the answer differs
		// per platform in a way that changes which platform somebody picks, and
		// these four rows are that difference.
		{"facebook can be told to end a broadcast", "facebook", CapBroadcastLifecycle, SupportYes,
			"end_live_video is documented, wired to a route, and reachable from the destination card"},
		// Documented and NOT WIRED. TransitionBroadcast exists in
		// internal/oauth with no caller: no route, no UI, nothing that can
		// invoke it. The house rule this matrix keeps -- a capability nothing
		// implements is not a capability -- is why this is not SupportYes, and
		// it is the same rule that held Rumble's viewer stats at unverified and
		// that rolled X's five cells back after they were briefly set to yes.
		{"youtube lifecycle is documented and unwired", "youtube", CapBroadcastLifecycle, SupportUnknown,
			"the transition call exists as a provider method with no caller"},
		// Established by ENUMERATION, which is what this matrix requires before
		// a cell may read Not possible: all 149 endpoints in the Helix
		// reference were listed and swept, and all 27 operations in Kick's
		// published API were parsed. Neither has a lifecycle call to build.
		{"twitch cannot be told to go live", "twitch", CapBroadcastLifecycle, SupportNo,
			"149 Helix endpoints enumerated; the stream itself is the trigger"},
		{"kick cannot be told to go live", "kick", CapBroadcastLifecycle, SupportNo,
			"27 documented operations parsed; no lifecycle scope or endpoint exists"},
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
		// Rumble resolves for CHAT while its stream key is still pasted by
		// hand, which is the first time those two have come apart. The row has
		// to be reachable by platform or a chat message stamped
		// db.PlatformRumble describes nothing.
		{"rumble", db.PlatformRumble, true, "rumble"},
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

// The direction TestMatrixPresetIDsExistInTheDestinationCatalogue does not
// check. That one asks "does every matrix row name a real preset"; this asks
// "does every preset an operator can pick have a matrix row", and only the
// second catches a platform shipping with no answer to "what else works here".
//
// TikTok LIVE and LinkedIn Live shipped that way. Both had destination presets
// -- you could stream to them -- and neither had a row, so the product could
// tell an operator how to send pixels and nothing about whether chat, metadata
// or moderation would follow. Instagram is in the matrix precisely so that the
// answer "none of it" is visible; the two that were missing gave no answer at
// all, which reads as "nobody thought about it" and is worse than a no.
//
// Scoped to the consumer platforms. GroupSelfHosted, GroupCloud and
// GroupGeneric are infrastructure -- an operator pointing polyemesis at their
// own Wowza is not wondering whether polyemesis can moderate its chat.
//
// Proven able to fail against the committed tree by deleting the tiktok row
// from capabilities.go.
func TestEveryConsumerPresetHasACapabilityRow(t *testing.T) {
	have := map[string]bool{}
	for _, row := range PlatformCapabilities() {
		have[row.PresetID] = true
	}
	checked := 0
	for _, p := range db.DestinationPresets() {
		if p.Group != db.GroupMajor && p.Group != db.GroupVideo {
			continue
		}
		// Transport variants inherit their base platform's row: "Twitch
		// (RTMPS)" is Twitch with a different scheme, and a second identical
		// row would be two things to keep in step for no added truth.
		if base, _, found := strings.Cut(p.ID, "-"); found && have[base] {
			continue
		}
		checked++
		if !have[p.ID] {
			t.Errorf("preset %q (%s) is offered in the destination picker and has no "+
				"capability row.\nThe operator can stream there and cannot find out "+
				"whether chat, metadata or moderation work. If the answer is \"none of "+
				"it\", say so in a row -- that is what the Instagram row is for.", p.ID, p.Name)
		}
	}
	if checked == 0 {
		t.Fatal("no consumer presets checked; this test would pass on an empty catalogue")
	}
}

// The defect the drift tests could not see. Both catalogues had a Kick entry,
// both entries had the same id, and the preset-drift check passed -- while the
// note an operator reads said
//
//	Kick is the one platform where the key stays manual: its public API exposes
//	the channel, chat and viewer counts but no stream key anywhere.
//
// and the capability matrix, corrected later, said the key is fetched over the
// streamkey:read scope. The limitation was real, was lifted, and the sentence
// the operator actually sees still described the old world -- telling them to
// copy by hand a key polyemesis would fetch for them.
//
// DELIBERATELY NARROW. It matches a short list of phrases that assert the key
// cannot be fetched, and only for platforms whose matrix row says it can. A
// general "is this prose true" check is not a thing a test can be; this catches
// the one contradiction that has actually shipped.
//
// Proven able to fail against the committed tree by restoring any of the
// phrases below to the kick preset's Notes.
func TestNoPresetNoteDeniesAKeyTheMatrixSaysWeFetch(t *testing.T) {
	denials := []string{
		"no stream key anywhere",
		"key stays manual",
		"stream key is manual",
		"there is no way to fetch",
	}
	byID := map[string]db.DestinationPreset{}
	for _, p := range db.DestinationPresets() {
		byID[p.ID] = p
	}
	checked := 0
	for _, row := range PlatformCapabilities() {
		if row.Caps[CapStreamKey] != SupportYes {
			continue
		}
		p, ok := byID[row.PresetID]
		if !ok {
			continue
		}
		checked++
		notes := strings.ToLower(p.Notes)
		for _, d := range denials {
			if strings.Contains(notes, d) {
				t.Errorf("the %s preset note says %q, but the capability matrix says "+
					"polyemesis fetches the stream key for this platform.\n"+
					"The note is what the operator reads, so they will copy by hand a "+
					"key we would have fetched.", p.Name, d)
			}
		}
	}
	if checked == 0 {
		t.Fatal("no platform has CapStreamKey=SupportYes, so this test checked " +
			"nothing and would pass on any note at all")
	}
}
