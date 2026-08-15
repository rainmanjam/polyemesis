package multitrack

import (
	"errors"
	"net/url"
	"strings"
	"testing"
)

// ingestHost is a host Resolve will accept, and every hand-built Config below
// uses it rather than the bare "h" they all used before.
//
// That "h" was not a shortcut, it was the bug: Resolve checked the scheme and
// that the host was non-empty, so "h" -- and equally "attacker.example" --
// passed, and engine/destinations.go then replaced the destination's stored
// target with whatever came back. The tests could not have caught it because
// they were written in the same shape as the hole. A per-channel Amazon IVS
// host is what the response really carries; see ingestHostSuffix.
const ingestHost = "fa723fc1b171.global-contribute.live-video.net"

// fixtureMintedKey is the `authentication` value in
// testdata/negotiated-one-rendition.json.
//
// THE FIXTURE DID NOT CARRY ONE UNTIL THIS CHANGE, and that omission is worth
// recording rather than quietly repairing: it is a fixture named "negotiated",
// standing in for a SUCCESSFUL negotiation, and IngestEndpoint.Authentication
// records as measured that a successful negotiation always mints a key. Every
// test that resolved against it was therefore exercising the operator's own key
// through a path that only a REFUSAL can reach in production -- which is
// precisely why nothing here noticed that Resolve fell back to that key.
//
// Short rather than the real 312 characters, for the reason
// TestResolveHonoursTheKeyTwitchMintsRatherThanTheOperatorsOwn already gives:
// a credential-shaped literal in the tree is what .gitleaks.toml is for. The
// shape is the documented one -- v1_<sig>_<salt>_<hex manifest>_<original key>.
const fixtureMintedKey = "v1_sig_salt_7b2276223a317d_live_424242_operatorkey"

// TestResolveSplitsTheTemplateWherePolyemesisSplitsAPublishURL pins the exact
// pair db.Destination.Target composes, because that composition is what FFmpeg
// eventually opens and getting the split wrong produces a URL that connects and
// then fails -- the failure mode internal/services was written about.
//
// Proven able to fail against the committed tree by returning the whole
// template as the server -- `Target{URL: ep.URLTemplate, ...}` in Config.Resolve
// (endpoint.go), which is the naive implementation this split exists instead of
// -- making the test report "the placeholder survived into the publish URL:
// rtmps://.../app/{stream_key}/live_424242_operatorkey?clientConfigId=...".
// Restored from a /tmp copy; git diff --stat clean.
//
// Worth recording what did NOT kill it: shortening keyPlaceholder to
// "{stream_key}" changes nothing, because the strings.TrimRight in Resolve
// absorbs the slash left behind. That is the constant being robust rather than
// the test being weak -- but it is why the mutation above is the one written
// down, and not the one that looks more obvious.
func TestResolveSplitsTheTemplateWherePolyemesisSplitsAPublishURL(t *testing.T) {
	cfg := loadFixture(t, "negotiated-one-rendition.json")

	target, err := cfg.Resolve("live_424242_operatorkey")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	const wantURL = "rtmps://ingest.global-contribute.live-video.net/app"
	if target.URL != wantURL {
		t.Errorf("URL = %q, want %q", target.URL, wantURL)
	}
	// The composition db.Destination.Target performs, reproduced here so the
	// assertion is about the string that actually reaches FFmpeg rather than
	// about the halves.
	joined := strings.TrimRight(target.URL, "/") + "/" + target.Key
	if strings.Contains(joined, "{stream_key}") {
		t.Errorf("the placeholder survived into the publish URL: %q", joined)
	}
	if !strings.HasPrefix(joined, wantURL+"/"+fixtureMintedKey+"?") {
		t.Errorf("composed publish URL = %q, want it to start with the server, the key, then a query", joined)
	}
}

// TestResolveCarriesTheConfigIDOnTheKeySoTheIngestCanMatchTheNegotiation is the
// step that is easy to leave out and impossible to notice: without
// clientConfigId the publish arrives at the right host with the right key and
// no way for Twitch to tell which ladder it agreed to.
//
// Proven able to fail against the committed tree by making withConfigID
// (endpoint.go) return `key` unchanged, which made the test report
// "clientConfigId is missing from the stream key". Restored from a /tmp copy;
// git diff --stat clean.
func TestResolveCarriesTheConfigIDOnTheKeySoTheIngestCanMatchTheNegotiation(t *testing.T) {
	cfg := loadFixture(t, "negotiated-one-rendition.json")

	target, err := cfg.Resolve("live_1_key")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	_, rawQuery, ok := strings.Cut(target.Key, "?")
	if !ok {
		t.Fatalf("clientConfigId is missing from the stream key: %q", target.Key)
	}
	q, err := url.ParseQuery(rawQuery)
	if err != nil {
		t.Fatalf("the query appended to the key does not parse: %v", err)
	}
	if got := q.Get("clientConfigId"); got != cfg.Meta.ConfigID {
		t.Errorf("clientConfigId = %q, want %q", got, cfg.Meta.ConfigID)
	}
	// It rides on the KEY, not on the server. A clientConfigId on the server
	// would make the RTMP app name wrong.
	if strings.Contains(target.URL, "clientConfigId") {
		t.Errorf("clientConfigId landed on the server URL: %q", target.URL)
	}
}

// TestAQueryAlreadyOnTheStreamKeyIsKeptAlongsideTheConfigID covers the real
// Twitch parameter "?bandwidthtest=true", which changes what the ingest does
// with the stream. Concatenating clientConfigId naively would produce two
// question marks and lose it.
//
// Proven able to fail against the committed tree by changing withConfigID
// (endpoint.go) to `return key + "?clientConfigId=" + configID`, which made the
// test report `bandwidthtest = "", want "true"`. Restored from a /tmp copy;
// git diff --stat clean.
func TestAQueryAlreadyOnTheStreamKeyIsKeptAlongsideTheConfigID(t *testing.T) {
	// The query rides on the MINTED key, because that is the key that is
	// published with -- Twitch echoes the operator's parameters into the value
	// it mints. Asserting against the operator's own key here would be
	// asserting against a value Resolve must never publish.
	const minted = "v1_sig_salt_7b2276223a317d_live_1_key?bandwidthtest=true"
	cfg := &Config{
		Meta: Meta{ConfigID: "cfg-1"},
		IngestEndpoints: []IngestEndpoint{{
			Protocol:       "RTMPS",
			URLTemplate:    "rtmps://" + ingestHost + "/app/{stream_key}",
			Authentication: minted,
		}},
	}

	target, err := cfg.Resolve("live_1_key?bandwidthtest=true")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	base, rawQuery, _ := strings.Cut(target.Key, "?")
	if base != "v1_sig_salt_7b2276223a317d_live_1_key" {
		t.Errorf("the key itself was rewritten: %q", base)
	}
	if strings.Count(target.Key, "?") != 1 {
		t.Errorf("the key carries more than one question mark: %q", target.Key)
	}
	q, err := url.ParseQuery(rawQuery)
	if err != nil {
		t.Fatalf("parse query: %v", err)
	}
	if got := q.Get("bandwidthtest"); got != "true" {
		t.Errorf("bandwidthtest = %q, want %q -- the operator's parameter was dropped", got, "true")
	}
	if got := q.Get("clientConfigId"); got != "cfg-1" {
		t.Errorf("clientConfigId = %q, want %q", got, "cfg-1")
	}
}

// TestResolvePrefersRTMPSEvenThoughTwitchListsRTMPFirst guards a one-line
// decision with a real consequence. The stream key travels as the RTMP stream
// name, so plain RTMP puts it on the wire in the clear -- and Twitch lists the
// cleartext endpoint FIRST on every measured response, so the obvious loop
// picks it every time.
//
// Proven able to fail against the committed tree by changing pickEndpoint
// (endpoint.go) to return the first RTMP-or-RTMPS entry it sees, which made the
// test report "scheme = rtmp, want rtmps". Restored from a /tmp copy; git diff
// --stat clean.
func TestResolvePrefersRTMPSEvenThoughTwitchListsRTMPFirst(t *testing.T) {
	cfg := loadFixture(t, "negotiated-one-rendition.json")

	// The premise: this test is only meaningful if the fixture really does put
	// RTMP first. If Twitch reorders them one day, this says so rather than
	// passing for the wrong reason.
	if len(cfg.IngestEndpoints) < 2 || !strings.EqualFold(cfg.IngestEndpoints[0].Protocol, "RTMP") {
		t.Fatalf("fixture no longer lists plain RTMP first, so this test proves nothing: %+v",
			cfg.IngestEndpoints)
	}

	target, err := cfg.Resolve("k")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if scheme, _, _ := strings.Cut(target.URL, ":"); scheme != "rtmps" {
		t.Errorf("scheme = %s, want rtmps", scheme)
	}

	// And RTMP is still used when it is all there is, because refusing would
	// turn a working-but-cleartext ingest into no ingest.
	only := &Config{
		Meta: Meta{ConfigID: "c"},
		IngestEndpoints: []IngestEndpoint{{
			Protocol:       "RTMP",
			URLTemplate:    "rtmp://" + ingestHost + "/app/{stream_key}",
			Authentication: fixtureMintedKey,
		}},
	}
	got, err := only.Resolve("k")
	if err != nil {
		t.Fatalf("Resolve with only RTMP available: %v", err)
	}
	if !strings.HasPrefix(got.URL, "rtmp://") {
		t.Errorf("URL = %q, want the plain RTMP endpoint when it is the only one", got.URL)
	}
}

// TestResolveHonoursTheKeyTwitchMintsRatherThanTheOperatorsOwn is the one that
// would be silently wrong. On a successful negotiation Twitch returns a signed
// key carrying the agreed ladder; publishing with the operator's original key
// instead connects, and sends a stream the ingest never agreed the shape of.
//
// Proven able to fail against the committed tree by deleting the
// `if ep.Authentication != ""` block from Config.Resolve (endpoint.go), which
// made the test report that the operator's key was used. Restored from a /tmp
// copy; git diff --stat clean.
func TestResolveHonoursTheKeyTwitchMintsRatherThanTheOperatorsOwn(t *testing.T) {
	// Shaped like the real thing -- v1_<sig>_<salt>_<hex manifest>_<the
	// original key> -- but short, and short on purpose: a 312-character
	// credential-shaped literal in the tree is what .gitleaks.toml is for.
	const minted = "v1_sig_salt_7b2276223a317d_live_1_operatorkey"

	cfg := &Config{
		Meta: Meta{ConfigID: "cfg-1"},
		IngestEndpoints: []IngestEndpoint{
			{Protocol: "RTMPS", URLTemplate: "rtmps://" + ingestHost + "/app/{stream_key}", Authentication: minted},
		},
	}

	target, err := cfg.Resolve("live_1_operatorkey")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	base, _, _ := strings.Cut(target.Key, "?")
	if base != minted {
		t.Errorf("published key = %q, want the key Twitch minted (%q)", base, minted)
	}
}

// TestResolveRefusesATemplateItDoesNotUnderstandRatherThanGuessing. obs-studio
// leaves an unmatched template alone, which would have polyemesis publish to a
// path containing a literal "{stream_key}". A publish to somewhere the operator
// did not choose is worse than no publish, and the fallback exists.
//
// Proven able to fail against the committed tree by changing the CutSuffix
// failure branch in Config.Resolve (endpoint.go) to `server = ep.URLTemplate`
// instead of returning an error, which made the "template with no placeholder"
// subtest report "Resolve succeeded". Restored from a /tmp copy; git diff
// --stat clean.
func TestResolveRefusesATemplateItDoesNotUnderstandRatherThanGuessing(t *testing.T) {
	for _, tc := range []struct {
		name      string
		endpoints []IngestEndpoint
		key       string
		want      string
	}{
		{
			name:      "template with no placeholder",
			endpoints: []IngestEndpoint{{Protocol: "RTMPS", URLTemplate: "rtmps://h/app"}},
			key:       "k",
			want:      "does not end in",
		},
		{
			name:      "placeholder somewhere other than the end",
			endpoints: []IngestEndpoint{{Protocol: "RTMPS", URLTemplate: "rtmps://h/{stream_key}/app"}},
			key:       "k",
			want:      "does not end in",
		},
		{
			name:      "an https endpoint is not a publish URL",
			endpoints: []IngestEndpoint{{Protocol: "RTMPS", URLTemplate: "https://h/app/{stream_key}"}},
			key:       "k",
			want:      "not an RTMP publish URL",
		},
		{
			name:      "no host",
			endpoints: []IngestEndpoint{{Protocol: "RTMPS", URLTemplate: "rtmps:///app/{stream_key}"}},
			key:       "k",
			want:      "names no host",
		},
		{
			name:      "a protocol nothing here speaks",
			endpoints: []IngestEndpoint{{Protocol: "WHIP", URLTemplate: "https://h/whip/{stream_key}"}},
			key:       "k",
			want:      "none of them speaks RTMP",
		},
		{
			name:      "no endpoints at all",
			endpoints: nil,
			key:       "k",
			want:      "none of them speaks RTMP",
		},
		{
			name:      "neither side supplied a key",
			endpoints: []IngestEndpoint{{Protocol: "RTMPS", URLTemplate: "rtmps://" + ingestHost + "/app/{stream_key}"}},
			key:       "",
			want:      "neither the destination nor Twitch supplied a stream key",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &Config{Meta: Meta{ConfigID: "c"}, IngestEndpoints: tc.endpoints}
			target, err := cfg.Resolve(tc.key)
			if err == nil {
				t.Fatalf("Resolve succeeded and returned %+v", target)
			}
			// The sentinel is what a caller switches on to take the fallback, so
			// every one of these has to carry it.
			if !errors.Is(err, ErrNoUsableEndpoint) {
				t.Errorf("error does not wrap ErrNoUsableEndpoint: %v", err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v, want it to mention %q", err, tc.want)
			}
		})
	}
}

// TestTheRedactedTargetShowsTheHostAndHidesTheKey. Which host a broadcast is
// publishing to is the question this whole feature turns on, so it has to be
// printable; the key never is.
//
// Proven able to fail against the committed tree by changing Target.Redacted
// (endpoint.go) to `return t.URL + "/" + t.Key`, which made the test report
// "the redacted target contains the key". Restored from a /tmp copy; git diff
// --stat clean.
func TestTheRedactedTargetShowsTheHostAndHidesTheKey(t *testing.T) {
	cfg := loadFixture(t, "negotiated-one-rendition.json")
	// A needle, not a key-shaped literal. The assertion below only needs a
	// string distinctive enough that finding it in the output means the redactor
	// missed it -- and a realistic-looking one buys nothing except a finding in
	// gitleaks, which is right to flag it and which this repo runs in CI.
	const key = "this-value-must-never-appear-in-a-log"

	target, err := cfg.Resolve(key)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	red := target.Redacted()
	if strings.Contains(red, key) {
		t.Errorf("the redacted target contains the key: %q", red)
	}
	if !strings.Contains(red, "ingest.global-contribute.live-video.net") {
		t.Errorf("the redacted target hides the host, which is the part worth printing: %q", red)
	}
}

// TestResolveRefusesAnIngestHostTwitchDoesNotOperate.
//
// Resolve checked the SCHEME of the negotiated template and that the host was
// non-empty, and nothing checked WHICH host. The response is attacker-shaped
// input the moment anything can answer for ingest.twitch.tv -- a compromised
// endpoint, a hostile resolver, a proxy an operator was told to configure -- and
// engine/destinations.go replaces the destination's stored target wholesale
// with whatever Resolve returns, so nothing downstream reasserts the host the
// operator chose. The stream key travels in the RTMP connect as the stream
// name, which means the first packet of that publish hands the credential over.
//
// Mutation: delete the ingestHostSuffix check from Config.Resolve
// (endpoint.go). Observed to fail on every hostile subtest with "Resolve
// accepted host ... and returned target rtmps://attacker.example/app/<redacted>"
// -- and, notably, PASS on all four legitimate ones, which is what says the
// check is not simply refusing everything. Restored with `command cp -f` from a
// file backup; `diff` against the backup reported IDENTICAL.
//
// Mutation: drop the leading dot, `strings.HasSuffix(host,
// "global-contribute.live-video.net")`. Observed to fail ONLY on the
// "sibling domain that merely ends in the same letters" subtest, which is the
// one written for it -- a suffix match without a label boundary accepts a
// registrable domain anybody can buy.
//
// Mutation: compare u.Host instead of u.Hostname(). Observed to fail on the
// "legitimate host with an explicit port" subtest, which is the measured Kick
// form in db/platforms.go.
func TestResolveRefusesAnIngestHostTwitchDoesNotOperate(t *testing.T) {
	const minted = "v1_sig_salt_7b2276223a317d_live_1_operatorkey"

	for _, tc := range []struct {
		name   string
		host   string
		accept bool
	}{
		{name: "the measured Twitch ingest", host: "ingest.global-contribute.live-video.net", accept: true},
		{name: "a per-channel Amazon IVS host", host: ingestHost, accept: true},
		{name: "legitimate host with an explicit port", host: ingestHost + ":443", accept: true},
		{name: "DNS is case-insensitive", host: "INGEST.Global-Contribute.Live-Video.NET", accept: true},

		{name: "a host the response simply chose", host: "attacker.example"},
		{name: "the config API host is not an ingest host", host: "ingest.twitch.tv"},
		{name: "sibling domain that merely ends in the same letters",
			host: "evilglobal-contribute.live-video.net"},
		{name: "the suffix as a path on somebody else's host",
			host: "attacker.example/.global-contribute.live-video.net"},
		{name: "the suffix as a userinfo prefix",
			host: "ingest.global-contribute.live-video.net@attacker.example"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &Config{
				Meta: Meta{ConfigID: "cfg-1"},
				IngestEndpoints: []IngestEndpoint{{
					Protocol:       "RTMPS",
					URLTemplate:    "rtmps://" + tc.host + "/app/{stream_key}",
					Authentication: minted,
				}},
			}
			target, err := cfg.Resolve("live_1_operatorkey")
			if tc.accept {
				if err != nil {
					t.Fatalf("Resolve refused a legitimate ingest host %q: %v", tc.host, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("Resolve accepted host %q and returned target %s -- the broadcast, and the "+
					"stream key the RTMP connect opens with, would go to a host chosen by the "+
					"response rather than by the operator", tc.host, target.Redacted())
			}
			if !errors.Is(err, ErrNoUsableEndpoint) {
				t.Errorf("error does not wrap ErrNoUsableEndpoint, so the caller cannot take the "+
					"ordinary-ingest fallback on it: %v", err)
			}
			if !strings.Contains(err.Error(), "which is not under") {
				t.Errorf("error = %v, want it to name the host it refused", err)
			}
		})
	}
}

// TestResolveRefusesASuccessfulNegotiationThatMintedNoKey.
//
// `key := streamKey; if ep.Authentication != "" { key = ep.Authentication }`
// reads as a safe default and is the opposite of one. Outcome.Use records that
// the minted key is MANDATORY on a successful negotiation, so a response
// without one is not a response to publish against -- and the fallback did not
// merely publish the wrong ladder, it published the operator's LONG-LIVED
// channel credential in place of the per-broadcast one that was supposed to
// replace it.
//
// The two halves of SEC-1 compose: the host check refuses a hostile endpoint,
// and this refuses to hand it the permanent credential if it is ever reached by
// a route the host check does not cover.
//
// Mutation: restore the fallback -- `key := streamKey; if ep.Authentication !=
// "" { key = ep.Authentication }` -- in Config.Resolve (endpoint.go). Observed
// to fail with "Resolve published the operator's own long-lived key ...".
// Restored with `command cp -f`; `diff` against the backup reported IDENTICAL.
func TestResolveRefusesASuccessfulNegotiationThatMintedNoKey(t *testing.T) {
	const operatorKey = "live_1_the_operators_permanent_channel_credential"

	cfg := &Config{
		Meta: Meta{ConfigID: "cfg-1"},
		IngestEndpoints: []IngestEndpoint{{
			Protocol:    "RTMPS",
			URLTemplate: "rtmps://" + ingestHost + "/app/{stream_key}",
			// No Authentication. On a refusal Twitch echoes what was sent, and
			// sends nothing back when nothing was sent; on a success it always
			// mints. Absent is therefore never "use yours instead".
		}},
	}

	target, err := cfg.Resolve(operatorKey)
	if err == nil {
		t.Fatalf("Resolve published the operator's own long-lived key against an endpoint that "+
			"minted none: target key %q. The minted key is per-broadcast and dies with the "+
			"negotiation; this one is the channel credential", target.Key)
	}
	if !errors.Is(err, ErrNoUsableEndpoint) {
		t.Errorf("error does not wrap ErrNoUsableEndpoint: %v", err)
	}
	if !strings.Contains(err.Error(), "no minted stream key") {
		t.Errorf("error = %v, want it to say the endpoint minted no key", err)
	}
	// The refusal must not leak what it refused to publish.
	if strings.Contains(err.Error(), operatorKey) {
		t.Errorf("the refusal echoes the operator's stream key: %v", err)
	}
}
