package multitrack

import (
	"errors"
	"net/url"
	"strings"
	"testing"
)

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
	if !strings.HasPrefix(joined, wantURL+"/live_424242_operatorkey?") {
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
	cfg := &Config{
		Meta:            Meta{ConfigID: "cfg-1"},
		IngestEndpoints: []IngestEndpoint{{Protocol: "RTMPS", URLTemplate: "rtmps://h/app/{stream_key}"}},
	}

	target, err := cfg.Resolve("live_1_key?bandwidthtest=true")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	base, rawQuery, _ := strings.Cut(target.Key, "?")
	if base != "live_1_key" {
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
		Meta:            Meta{ConfigID: "c"},
		IngestEndpoints: []IngestEndpoint{{Protocol: "RTMP", URLTemplate: "rtmp://h/app/{stream_key}"}},
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
			{Protocol: "RTMPS", URLTemplate: "rtmps://h/app/{stream_key}", Authentication: minted},
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
			endpoints: []IngestEndpoint{{Protocol: "RTMPS", URLTemplate: "rtmps://h/app/{stream_key}"}},
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
