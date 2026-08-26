package oauth

// Where a provider sends its HTTP calls, and the one seam that changes it.
//
// Every provider here talks to at most two hosts: an authorization server,
// where tokens are minted and consent is granted, and a data API, which is
// everything else. In production both are the platform's real hostnames, pinned
// as constants beside each provider. A test needs them pointed at something it
// controls, and that -- not configurability -- is the only reason this file
// exists. Nothing at runtime calls WithBaseURL.
//
// It is a per-instance field rather than a package var, and that distinction is
// the whole point. The package vars this replaced had two failure modes:
//
//  1. A test that rewrites a package var cannot run in parallel with any other
//     test in the package, and its restore is order-dependent under -count=N.
//     facebook_test.go carried exactly that shape.
//
//  2. Worse, Facebook had grown a SECOND mechanism -- an unexported graphBase
//     field with a graphEndpoint() accessor -- that looked like the provider's
//     test seam and covered one endpoint out of thirteen. A test written as
//     &Facebook{graphBase: srv.URL} redirected the credential check and sent
//     IngestFor, PushMetadata and RescheduleBroadcast to the real
//     graph.facebook.com. Two mechanisms for one concept is how that happened,
//     so there is now one.
//
// The option deliberately redirects EVERYTHING a provider touches. There is no
// option that moves only the data API or only the token endpoint, because a
// partially redirected provider is precisely the bug above: it looks stubbed
// and is not. TestAStubbedProviderReachesNoRealHost enumerates the calls rather
// than sampling one, so a new call site wired straight to a production constant
// fails the guard instead of quietly reaching the internet.

import (
	"fmt"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// ProviderOption configures a provider at construction.
type ProviderOption func(*endpoints)

// endpoints is the per-instance endpoint set every provider embeds. The zero
// value means "the platform's real hosts", so a provider built with no options
// -- which is every provider in production -- behaves exactly as before.
type endpoints struct {
	api  string
	auth string
}

// WithBaseURL points every HTTP call the provider makes, on both the
// authorization server and the data API, at base. This is the seam internal/api
// uses to aim a whole provider set at a stub; see ProvidersWith.
func WithBaseURL(base string) ProviderOption {
	return func(e *endpoints) { e.api, e.auth = base, base }
}

// newEndpoints folds the options. A nil option is ignored rather than panicking
// because the caller is usually a test assembling a variadic list.
func newEndpoints(opts []ProviderOption) endpoints {
	var e endpoints
	for _, o := range opts {
		if o != nil {
			o(&e)
		}
	}
	return e
}

// apiBase and authBase take the platform's production host as the fallback, so
// the production constant stays written at the call site it belongs to and a
// zero-value provider needs no construction ceremony.
func (e endpoints) apiBase(production string) string {
	if e.api != "" {
		return e.api
	}
	return production
}

func (e endpoints) authBase(production string) string {
	if e.auth != "" {
		return e.auth
	}
	return production
}

// NewYouTube, NewTwitch, NewFacebook, NewKick and NewVimeo build a single provider. With
// no options they are identical to the zero value, which is what production
// uses; with WithBaseURL they are aimed at a stub.
func NewYouTube(opts ...ProviderOption) *YouTube {
	return &YouTube{endpoints: newEndpoints(opts)}
}

func NewTwitch(opts ...ProviderOption) *Twitch {
	return &Twitch{endpoints: newEndpoints(opts)}
}

func NewFacebook(opts ...ProviderOption) *Facebook {
	return &Facebook{endpoints: newEndpoints(opts)}
}

func NewKick(opts ...ProviderOption) *Kick {
	return &Kick{endpoints: newEndpoints(opts)}
}

// NewVimeo is in the set too, unlike NewX. Vimeo is registered in
// ProvidersWith; X is not.
func NewVimeo(opts ...ProviderOption) *Vimeo {
	return &Vimeo{endpoints: newEndpoints(opts)}
}

// ---------------------------------------------------------------- the set

// Set is a resolved group of providers plus the capability lookups that go with
// it. It is the injection point internal/api was missing.
//
// The package-level Get, MetadataFor, ComplianceFor, TargetsFor, ManualKeyFor
// and ScheduledBroadcastsFor all resolve against the production set, which is
// why a caller outside this package could not aim them anywhere else -- and
// why internal/api grew five function-pointer fields on Server (ingestForFn,
// pushMetadataFn, pushComplianceFn, pushBroadcastFn, rescheduleFn) to stub out
// the calls those lookups returned. A caller that holds a Set instead of
// calling the package functions can be handed one built with WithBaseURL, and
// every one of those fields becomes a closure over a provider the test already
// controls.
//
// The zero Set resolves to production rather than panicking on a nil map. That
// is deliberately unlike the nil-hook pattern credcheck.go warns about: a
// forgotten assignment there silently DISABLED signature verification, whereas
// a forgotten assignment here yields the real platform hosts -- correct in
// production, and loudly wrong in a test, which will try to reach the internet
// and fail.
type Set struct {
	byPlatform map[db.Platform]Provider
}

// NewSet builds a provider set. NewSet() is production; NewSet(WithBaseURL(u))
// aims every provider in it at u.
func NewSet(opts ...ProviderOption) Set {
	return Set{byPlatform: ProvidersWith(opts...)}
}

// All returns the providers keyed by platform.
func (s Set) All() map[db.Platform]Provider {
	if s.byPlatform == nil {
		return Providers()
	}
	return s.byPlatform
}

// Get returns a provider, or an error naming the platform.
func (s Set) Get(p db.Platform) (Provider, error) {
	if pr, ok := s.All()[p]; ok {
		return pr, nil
	}
	return nil, fmt.Errorf("no OAuth provider for platform %q", p)
}

// MetadataFor, ComplianceFor, TargetsFor, ManualKeyFor and
// ScheduledBroadcastsFor mirror the package-level functions of the same names,
// resolved against this set. False means the platform has no such capability,
// which is a supported answer.
//
// Every capability the package grows needs its twin here, and the reason is
// mechanical: a caller that resolves one capability through the Set and another
// through the package function is holding a stubbed provider and a production
// one at the same time, which is the partially-redirected provider this file
// opens by warning about.
func (s Set) MetadataFor(p db.Platform) (MetadataPusher, bool) {
	pr, ok := s.All()[p]
	if !ok {
		return nil, false
	}
	mp, ok := pr.(MetadataPusher)
	return mp, ok
}

func (s Set) ComplianceFor(p db.Platform) (CompliancePusher, bool) {
	pr, ok := s.All()[p]
	if !ok {
		return nil, false
	}
	cp, ok := pr.(CompliancePusher)
	return cp, ok
}

func (s Set) TargetsFor(p db.Platform) (TargetedProvider, bool) {
	pr, ok := s.All()[p]
	if !ok {
		return nil, false
	}
	tp, ok := pr.(TargetedProvider)
	return tp, ok
}

func (s Set) ManualKeyFor(p db.Platform) (ManualKey, bool) {
	pr, ok := s.All()[p]
	if !ok {
		return nil, false
	}
	mk, ok := pr.(ManualKey)
	return mk, ok
}

// StatsFor is the Set twin of the package-level StatsFor in stats.go. Without
// it a caller holding a stubbed Set would silently fall through to the
// production providers for viewer numbers alone.
func (s Set) StatsFor(p db.Platform) (LiveStatter, bool) {
	pr, ok := s.All()[p]
	if !ok {
		return nil, false
	}
	ls, ok := pr.(LiveStatter)
	return ls, ok
}

// LifecycleFor is the Set twin of the package-level LifecycleFor in
// lifecycle.go. The twin matters more here than for a read: without it a caller
// holding a stubbed Set would resolve the production YouTube provider and send a
// real transition -- a POST that starts or ENDS a broadcast on somebody's actual
// channel -- from a test that believed everything was pointed at a stub.
func (s Set) LifecycleFor(p db.Platform) (BroadcastLifecycler, bool) {
	pr, ok := s.All()[p]
	if !ok {
		return nil, false
	}
	bl, ok := pr.(BroadcastLifecycler)
	return bl, ok
}

// DeviceFor is the Set twin of the package-level DeviceFor in device.go.
//
// The twin is not optional here even though only Twitch implements the
// capability. A caller holding a stubbed Set that resolved device flow through
// the package function would get the PRODUCTION Twitch provider, and the first
// thing it would do is POST an operator's real client id to id.twitch.tv and
// then poll it every five seconds -- from a test that believed the whole world
// was pointed at a stub.
func (s Set) DeviceFor(p db.Platform) (DeviceFlower, bool) {
	pr, ok := s.All()[p]
	if !ok {
		return nil, false
	}
	df, ok := pr.(DeviceFlower)
	return df, ok
}

func (s Set) ScheduledBroadcastsFor(p db.Platform) (ScheduledBroadcaster, bool) {
	pr, ok := s.All()[p]
	if !ok {
		return nil, false
	}
	sb, ok := pr.(ScheduledBroadcaster)
	return sb, ok
}
