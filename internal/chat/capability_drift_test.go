package chat

import (
	"testing"

	"github.com/rainmanjam/polyemesis/internal/automod"
	"github.com/rainmanjam/polyemesis/internal/db"
)

/* THE GUARD TEST automod.PlatformCaps SAYS IT ALREADY HAS.
 *
 * Its doc comment claims: "It mirrors what internal/chat's adapters actually
 * implement. The pairing is asserted by a guard test rather than trusted: two
 * tables describing the same four platforms is exactly the drift this repo
 * already writes guards for elsewhere, and here the failure mode is an operator
 * believing a channel is moderated when nothing is wired to it."
 *
 * NO SUCH TEST EXISTED. The tables had drifted exactly as predicted:
 * PlatformCaps.Can returned true for ban and timeout on every platform, while
 * FacebookAdapter implements no Ban at all. An operator ticking "ban on
 * Facebook" got a tickable switch, a Summary that counted it as armed, and --
 * when a rule fired -- one error line in a log nobody reads.
 *
 * This is that test. It lives in internal/chat because chat imports automod and
 * not the other way round, so this is the only side that can see both tables.
 *
 * WHY TYPES AND NOT INSTANCES: what decides whether Hub.Ban works is whether the
 * adapter satisfies Banner, which is a property of the TYPE. Constructing live
 * adapters would need credentials and would test the constructor instead.
 */

// adapterTypes is one nil-valued pointer per platform adapter, purely so the
// interface assertions below have something to assert against.
var adapterTypes = map[db.Platform]any{
	db.PlatformKick:     (*KickAdapter)(nil),
	db.PlatformTwitch:   (*TwitchAdapter)(nil),
	db.PlatformYouTube:  (*YouTubeAdapter)(nil),
	db.PlatformFacebook: (*FacebookAdapter)(nil),
}

// implements reports what the platform's adapter can actually do.
func implements(t *testing.T, p db.Platform, a automod.Action) bool {
	t.Helper()
	ad, ok := adapterTypes[p]
	if !ok {
		t.Fatalf("no adapter type registered for %s — add it here when a platform "+
			"is added, or this guard silently stops covering it", p)
	}
	switch a {
	case automod.ActionDelete:
		_, ok := ad.(Deleter)
		return ok
	case automod.ActionBan, automod.ActionTimeout:
		// Both route through Hub.Ban, so both need Banner.
		_, ok := ad.(Banner)
		return ok
	case automod.ActionHide:
		_, ok := ad.(Hider)
		return ok
	}
	// Anything else is decided locally and needs no adapter.
	return true
}

func TestTheCapabilityTableMatchesWhatTheAdaptersImplement(t *testing.T) {
	caps := automod.PlatformCaps{}

	for _, p := range automod.Platforms {
		for _, a := range automod.Actions {
			claimed, reason := caps.Can(p, a)
			actual := implements(t, p, a)

			switch {
			case claimed && !actual:
				t.Errorf("%s/%s: the capability table says YES and the adapter cannot do it.\n"+
					"  The operator gets a tickable switch, Summary counts it as an armed\n"+
					"  automatic action, and when a rule fires nothing happens on the\n"+
					"  platform. That is precisely the silent failure the Capabilities doc\n"+
					"  comment says this gate exists to prevent.", p, a)
			case !claimed && actual:
				t.Errorf("%s/%s: the table says NO (%q) but the adapter implements it, so a\n"+
					"  working capability is being withheld from the operator.", p, a, reason)
			}

			if !claimed && reason == "" {
				t.Errorf("%s/%s is unavailable with no reason. The doc requires an\n"+
					"  unavailable cell be \"inert and explained\", and the reason is shown\n"+
					"  to the operator.", p, a)
			}
		}
	}
}
