package automod

import (
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// Facebook can block a person from the Page and cannot time one out: a block has
// no duration. The table must say so per action, with a reason for the one it
// refuses.
func TestFacebookCanBanButNotTimeOut(t *testing.T) {
	c := PlatformCaps{}
	if ok, why := c.Can(db.PlatformFacebook, ActionBan); !ok {
		t.Errorf("Facebook ban refused: %q", why)
	}
	ok, why := c.Can(db.PlatformFacebook, ActionTimeout)
	if ok || !strings.Contains(why, "cannot time anyone out") {
		t.Errorf("Facebook timeout = %v, %q; want a refusal that says why", ok, why)
	}
	// Everyone else is untouched.
	for _, p := range []db.Platform{db.PlatformYouTube, db.PlatformTwitch, db.PlatformKick} {
		if ok, _ := c.Can(p, ActionTimeout); !ok {
			t.Errorf("%s timeout lost", p)
		}
	}
	// Rumble still can do neither.
	if ok, why := c.Can(db.PlatformRumble, ActionBan); ok || why == "" {
		t.Errorf("Rumble ban = %v, %q", ok, why)
	}
}
