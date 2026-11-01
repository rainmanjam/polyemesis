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
