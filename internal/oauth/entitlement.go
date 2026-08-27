package oauth

// The gate that is not a permission: a platform whose API is complete,
// documented and reachable only by an account that pays for it.
//
// Every other refusal this package handles is something an operator can fix.
// A missing scope is fixed by reconnecting. A bad client secret is fixed in the
// platform console. App Review is slow but it is a process with an end. This
// one is different in kind: the request is well formed, the token is valid,
// every scope is granted, and the platform still says no because of the
// contract behind the account. Nothing the operator does in polyemesis changes
// it, and nothing polyemesis does in code changes it either.
//
// WHY THAT NEEDS A MECHANISM RATHER THAN A SENTENCE IN A GUIDE. The failure
// mode is not "the feature does not work". It is "the feature does not work AND
// the error does not say why", arriving at the worst possible moment. Vimeo's
// live API answers a non-Enterprise token without ever using the word
// Enterprise, so an operator who has connected an account successfully, granted
// every scope, and checked their credentials three times has no way to reach
// the actual explanation from anything polyemesis shows them. capabilities.go
// already records that the most expensive failure a self-hosted tool can ship
// is one that looks like a bug in the tool. This is that failure with a
// commercial cause.
//
// SO THE GATE IS PROBED, NOT ASSUMED. EntitlementGated is discovered like every
// other optional capability here (TargetsFor, DeviceFor, ManualKeyFor): absent
// is the answer for every platform but Vimeo today, and the caller handles it
// once. What the probe asserts is narrow and worth stating precisely -- it asks
// the GATED API ITSELF, with the operator's own freshly minted token, whether
// it will answer. It does not read a plan name, a tier string or an
// entitlements list, because:
//
//   - a plan name is an inference. "Enterprise" is the plan Vimeo's
//     documentation names, but the mapping from a plan string to an API's
//     behaviour is Vimeo's business rule, not a documented contract, and a
//     rename or a trial or a grandfathered account would make polyemesis wrong
//     about somebody's own account.
//   - the API's own answer is evidence. A 2xx from the gated endpoint means
//     this token reaches it. Anything else means it does not, today, for this
//     account -- which is exactly the question an operator is asking.
//
// THREE OUTCOMES, AND THE THIRD IS THE ONE THAT USUALLY GETS DROPPED:
//
//	nil                       the gated API answered; this account reaches it.
//	wraps ErrNotEntitled      the gated API refused; report the platform's own
//	                          words plus what it actually replied.
//	any other error           the probe did not complete -- DNS, a timeout, a
//	                          5xx. This is NOT evidence of a gate and must never
//	                          be reported as one. It is also not nothing: the
//	                          operator has to know the check did not run, or a
//	                          silent success reads as a clean bill of health.
//
// Collapsing the third into either of the first two is the same defect
// credcheck.go describes for CheckUnreachable -- "telling somebody their secret
// is wrong when the platform merely had a bad minute" -- one layer along.
//
// ONE IMPLEMENTATION, DELIBERATELY, AND SITED WHERE THE NEXT ONE WILL LOOK.
// The same argument oauth.go makes for TargetedProvider applies: an interface
// that appears in the same commit as its second implementer can never say which
// was shaped to fit the other. The catalogue already holds at least two more
// platforms of exactly this shape, recorded in capabilities.go's own words --
// LinkedIn Live ("requires approved broadcast-partner status ... access to it
// is granted, not requested") and TikTok LIVE ("gated behind a partner
// programme, not open registration"). Neither has a provider. When one gets
// there, it fits this, or this changes on purpose.

import (
	"context"
	"errors"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// ErrNotEntitled marks a refusal that no amount of setup will lift: the
// platform published the API, the token is good, and the account's commercial
// arrangement is what says no.
//
// Callers errors.Is against it to choose their WORDING, never to block an
// attempt. Nothing in this repository refuses to try an operation because of
// this error -- capabilities.go's rule that a check wrong in the restrictive
// direction is worse than no check applies here more than anywhere, because the
// gate is exactly the kind of thing a platform grants to one account and not
// another without telling anybody.
var ErrNotEntitled = errors.New("the platform reserves this capability for a paid tier or partner programme the connected account does not have")

// EntitlementGated is the optional capability for a platform whose sign-in is
// open to everyone and whose useful surface is not.
//
// It embeds Provider because the gate is discovered from an ordinary connected
// account: there is no separate credential, no second app registration and no
// different flow. Only the answer differs.
type EntitlementGated interface {
	Provider
	// EntitlementReason is the gate in the PLATFORM's own words, and it must be
	// usable before anyone has connected anything -- it is what the setup guide,
	// the capability matrix and the destination preset say to somebody who is
	// still deciding. A reason that can only be produced after a probe is a
	// reason that arrives too late to be worth having.
	EntitlementReason() string
	// CheckEntitlement probes the gated API once, with a token that has just
	// been minted, and returns one of the three outcomes in this file's header.
	//
	// SINGLE-SHOT, like PollDeviceAuth and for the same reason: it runs inside
	// the OAuth callback, which is a request handler an operator's browser is
	// blocked on. It does not retry, and a caller must not fail the connection
	// on its result -- the account connected, and that is true whatever the gate
	// says.
	CheckEntitlement(ctx context.Context, clientID, accessToken string) error
}

// EntitlementFor resolves the gate capability for a platform against the
// production providers. False means the platform has no commercial gate
// polyemesis knows how to ask about -- which covers both "there is no such
// gate" and "there is no provider at all", because neither needs a probe.
//
// Prefer Set.EntitlementFor wherever a Set is in hand, for the reason
// endpoints.go states: resolving one capability through the package function
// while the rest of the world is stubbed is how a test posts a real operator's
// token at a real platform.
func EntitlementFor(p db.Platform) (EntitlementGated, bool) {
	pr, ok := Providers()[p]
	if !ok {
		return nil, false
	}
	eg, ok := pr.(EntitlementGated)
	return eg, ok
}

// EntitlementFor is the Set-resolved twin. See endpoints.go: every capability
// lookup in this package needs one, and this one carries a live access token to
// a platform host, which is the worst kind to leave resolving against
// production from a stubbed caller.
func (s Set) EntitlementFor(p db.Platform) (EntitlementGated, bool) {
	pr, ok := s.All()[p]
	if !ok {
		return nil, false
	}
	eg, ok := pr.(EntitlementGated)
	return eg, ok
}
