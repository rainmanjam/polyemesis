package api

import (
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/config"
	"github.com/rainmanjam/polyemesis/internal/db"
)

// TestTheXGoLiveTweetSettingIsRemovedFromADestinationThatIsNotX: only an X
// PUBLISH reads it. A destination moved to another platform keeps a value that
// would never be acted on, and every PUT decodes over the stored row, so it is
// cleared with a warning rather than refused, the way the go-live title is. An X
// destination keeps it.
//
// MUTATION: the strip in dropUnsendableSettings removed. Observed: FAIL, the
// Twitch row kept "never".
func TestTheXGoLiveTweetSettingIsRemovedFromADestinationThatIsNotX(t *testing.T) {
	s, _, _ := testServer(t, config.Config{})
	twitch := &db.Destination{Platform: db.PlatformTwitch, XGoLiveTweet: db.XTweetNever}
	warnings, err := s.dropUnsendableSettings(twitch)
	if err != nil {
		t.Fatalf("dropUnsendableSettings: %v", err)
	}
	if twitch.XGoLiveTweet != "" {
		t.Fatalf("a Twitch destination kept the X announcement setting %q", twitch.XGoLiveTweet)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "announcement") {
		t.Fatalf("warnings = %q, want one naming the announcement setting", warnings)
	}

	x := &db.Destination{Platform: db.PlatformX, XGoLiveTweet: db.XTweetNever}
	warnings, err = s.dropUnsendableSettings(x)
	if err != nil {
		t.Fatalf("dropUnsendableSettings: %v", err)
	}
	if x.XGoLiveTweet != db.XTweetNever || len(warnings) != 0 {
		t.Fatalf("an X destination lost its setting (%q) or was warned: %q", x.XGoLiveTweet, warnings)
	}
}

// TestTheXChatControlsSurviveOnXAndAreRemovedElsewhere: Compliance.X lives in the
// compliance blob, but X is not a CompliancePusher, so the clearing of a platform's
// unsendable compliance must not take it, while any other platform loses it.
//
// MUTATION: the X controls not carried through the compliance clearing. Observed: FAIL, the X row lost them.
func TestTheXChatControlsSurviveOnXAndAreRemovedElsewhere(t *testing.T) {
	s, _, _ := testServer(t, config.Config{})
	opt := 2
	x := &db.Destination{Platform: db.PlatformX, Compliance: db.Compliance{
		Privacy: db.PrivacyPublic, X: &db.XBroadcastControls{ChatOption: &opt, Locale: "en"},
	}}
	if _, err := s.dropUnsendableSettings(x); err != nil {
		t.Fatalf("dropUnsendableSettings: %v", err)
	}
	if x.Compliance.X.Empty() || x.Compliance.Privacy != "" {
		t.Fatalf("X row compliance = %+v, want the privacy cleared and the chat controls kept", x.Compliance)
	}
	tw := &db.Destination{Platform: db.PlatformTwitch, Compliance: db.Compliance{X: &db.XBroadcastControls{ChatOption: &opt}}}
	warnings, err := s.dropUnsendableSettings(tw)
	if err != nil {
		t.Fatalf("dropUnsendableSettings: %v", err)
	}
	if tw.Compliance.X != nil || len(warnings) != 1 || !strings.Contains(warnings[0], "chat access mode") {
		t.Fatalf("Twitch row kept %+v, warnings %q", tw.Compliance.X, warnings)
	}
}
