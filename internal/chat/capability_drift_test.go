package chat

import (
	"testing"

	"github.com/rainmanjam/polyemesis/internal/automod"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/oauth"
	"github.com/rainmanjam/polyemesis/internal/platform"
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
	case automod.ActionBan:
		_, ok := ad.(Banner)
		return ok
	case automod.ActionTimeout:
		// Routes through Hub.Ban too, so it needs Banner -- unless the Banner
		// says it cannot time anyone out (Facebook's block has no duration).
		_, ok := ad.(Banner)
		_, noTimeout := ad.(NoTimeouter)
		return ok && !noTimeout
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

// The platform registry describes the same platforms the adapters implement, in
// a package that cannot import chat. This is the join: every adapter's profile
// must equal what the registry says, and the registry must not describe an
// adapter that does not exist.
func TestRegistryAgreesWithTheChatAdapters(t *testing.T) {
	// The other direction: an adapter with no registry row is a platform chat
	// can talk to that the registry has never heard of.
	for p := range adapterTypes {
		if _, ok := platform.ByEnum(string(p)); !ok {
			t.Errorf("chat has an adapter for %s and the platform registry has no row for it", p)
		}
	}
	for _, row := range platform.All() {
		if row.Enum == "" {
			continue
		}
		pr, hasAdapter := ProfileOf(db.Platform(row.Enum))
		if !hasAdapter {
			if row.Chat.SendMax != 0 || row.Moderation != nil {
				t.Errorf("%s: the registry describes chat for a platform with no adapter", row.Key)
			}
			continue
		}
		if sends := row.Chat.SendMax > 0; sends != pr.Send {
			t.Errorf("%s: registry send=%v, adapter send=%v", row.Key, sends, pr.Send)
		}
		if row.Chat.SendMax != pr.SendMax {
			t.Errorf("%s: registry send max %d, adapter enforces %d", row.Key, row.Chat.SendMax, pr.SendMax)
		}
		var m platform.Moderation
		if row.Moderation != nil {
			m = *row.Moderation
		}
		if m.Delete != pr.Delete || m.TimeoutBan != pr.Ban || m.Hide != pr.Hide || m.NoTimeout != pr.NoTimeout {
			t.Errorf("%s: registry delete/ban/hide/no-timeout = %v/%v/%v/%v, adapter = %v/%v/%v/%v",
				row.Key, m.Delete, m.TimeoutBan, m.Hide, m.NoTimeout, pr.Delete, pr.Ban, pr.Hide, pr.NoTimeout)
		}
	}
}

// The oauth capability matrix is what the console and docs/PLATFORMS.md show an
// operator; the profile is what the adapter's type can actually do. oauth
// cannot import chat, so this is the join: a platform whose adapter sends must
// not read as unable to (or unverified), and the reverse.
func TestTheCapabilityMatrixAgreesWithTheChatProfiles(t *testing.T) {
	for p := range adapterTypes {
		row, ok := oauth.CapabilityForPlatform(p)
		if !ok {
			t.Errorf("%s has a chat adapter and no capability row", p)
			continue
		}
		pr, _ := ProfileOf(p)
		moderates := pr.Delete || pr.Ban || pr.Hide
		// X's adapter is built but only attached behind POLYEMESIS_X_LIVESTREAM
		// and never run against a live X account, so its cells may say
		// Unverified; the rule for it is only that they must not say No. Every
		// other platform's cell is Yes exactly when the adapter can do it.
		liveGated := p == db.PlatformX
		agrees := func(cell oauth.Support, can bool) bool {
			if liveGated {
				return !can || cell != oauth.SupportNo
			}
			return (cell == oauth.SupportYes) == can
		}
		if cell := row.Get(oauth.CapChatSend); !agrees(cell, pr.Send) {
			t.Errorf("%s: matrix chat send = %q, adapter sends=%v", p, cell, pr.Send)
		}
		if cell := row.Get(oauth.CapModeration); !agrees(cell, moderates) {
			t.Errorf("%s: matrix moderation = %q, adapter moderates=%v", p, cell, moderates)
		}
	}
}
