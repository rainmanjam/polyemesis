package multitrack

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// keyPlaceholder is the token Twitch leaves in url_template where the stream
// key goes. Measured on every response, in both the RTMP and the RTMPS entry:
//
//	rtmps://ingest.global-contribute.live-video.net/app/{stream_key}
const keyPlaceholder = "/{stream_key}"

// ingestHostSuffix is the only DNS suffix a negotiated ingest host may have.
//
// WITHOUT THIS CHECK THE RESPONSE CHOOSES WHERE THE BROADCAST GOES. Resolve
// validated the scheme and that the host was non-empty and nothing else, so a
// body carrying `url_template: "rtmps://attacker.example/app/{stream_key}"`
// resolved cleanly, and engine/destinations.go replaces the destination's
// stored target wholesale with what Resolve returns -- nothing downstream
// reasserts the host the operator configured. The stream key travels in the
// RTMP connect as the stream name, so the first packet of that publish hands
// the credential over.
//
// A SUFFIX RATHER THAN AN EXACT HOST, and the evidence for where to cut is in
// the repository rather than in a guess. Three independently measured
// contribute hosts appear here -- ingest.global-contribute.live-video.net (the
// package doc's second measured fact, and both response fixtures),
// fa723fc1b171.global-contribute.live-video.net (services.go) and the
// per-channel <your-host>.global-contribute.live-video.net that db/platforms.go
// tells a Kick operator to expect. What varies between them is the LEFTMOST
// LABEL and nothing else: these are Amazon IVS contribute endpoints, issued per
// channel and routed regionally, so the host is exactly the part of the
// response that is not knowable ahead of the call. Pinning the one host that
// was measured would refuse a legitimate ingest the first time Twitch routed a
// broadcast anywhere else, and this package exists as a negotiation precisely
// because that value is Twitch's to choose.
//
// THE LEADING DOT IS LOAD-BEARING. Matching the suffix without it accepts
// "evilglobal-contribute.live-video.net", which is a different registrable
// domain that anyone may register and which HasSuffix cannot tell apart. With
// it, everything that matches is a subdomain of a name Amazon controls.
const ingestHostSuffix = ".global-contribute.live-video.net"

// Target is a publish destination, split the way polyemesis splits one.
//
// URL is the server and Key is the stream name, and they are separate because
// db.Destination.Target composes exactly this pair -- TrimRight(URL, "/") + "/"
// + Key -- when it builds what FFmpeg opens. Returning a single joined string
// would force the caller to take it apart again, and taking a publish URL apart
// is precisely the operation services.AnalyseURL exists because people get
// wrong.
//
// A NOTE ON THE OTHER CONVENTION IN THIS REPO, because getting it backwards has
// cost a whole verdict before: scripts/probe_platform_ertmp_multitrack.go puts
// the stream key in the URL FRAGMENT, and it is right to. That is gortmplib's
// interface -- its splitURL reads the key out of the fragment and treats the
// whole path as the RTMP app -- and gortmplib is what the probe publishes with.
// polyemesis's destinations publish with FFmpeg, which takes the last path
// segment as the stream name. Same protocol, two libraries, two spellings. This
// type is the FFmpeg one because db.Destination.Target is.
type Target struct {
	// URL is the server, application path included and no trailing slash:
	// "rtmps://ingest.global-contribute.live-video.net/app".
	URL string
	// Key is the stream key with the clientConfigId query parameter appended.
	// IT IS A SECRET. It is never logged; Redacted covers the Config, and this
	// is the value that Config protects.
	Key string
}

// Redacted renders the target for a log line. The server half is not secret and
// is the half an operator needs to see, since "which host am I publishing to"
// is the question this whole feature turns on.
func (t Target) Redacted() string { return t.URL + "/" + redactedPlaceholder }

// ErrNoUsableEndpoint is returned when the configuration carries no ingest
// endpoint this client can publish to. It is a sentinel because the caller's
// response is the same as for a Refused verdict -- fall back to the ordinary
// ingest and say so -- and telling the two apart in a switch beats matching on
// a message.
var ErrNoUsableEndpoint = errors.New("no usable multitrack ingest endpoint")

// Resolve turns the negotiated endpoints and the operator's stream key into
// something publishable.
//
// It does NOT do a string substitution of {stream_key}, which is the obvious
// implementation and the wrong one: the result would be a single URL, and
// polyemesis needs the server and the key apart in order to keep the key out of
// the value it logs, out of argv, and out of the signature it hashes a
// destination by. obs-studio splits at the same point for the same reason --
// create_service cuts the template at "/{stream_key}" and sets the server and
// the key as separate service properties.
//
// The clientConfigId query parameter is the part that is easy to miss and not
// optional. It rides on the KEY, not on the server, and it is how the ingest
// knows which negotiated ladder is arriving on this connection. A publish
// without it is a publish Twitch cannot match to the configuration it just
// issued.
func (c *Config) Resolve(streamKey string) (Target, error) {
	if c == nil {
		return Target{}, ErrNoUsableEndpoint
	}

	ep, err := c.pickEndpoint()
	if err != nil {
		return Target{}, err
	}

	// A destination with no stream key of its own never reaches a negotiation
	// worth completing, and saying so first keeps that mistake from being
	// reported as one of Twitch's below.
	if streamKey == "" {
		return Target{}, fmt.Errorf("%w: neither the destination nor Twitch supplied a stream key",
			ErrNoUsableEndpoint)
	}

	server, ok := strings.CutSuffix(ep.URLTemplate, keyPlaceholder)
	if !ok {
		// REFUSED RATHER THAN PUBLISHED AS-IS. obs-studio leaves an unmatched
		// template alone, which would have polyemesis publish to a path with a
		// literal "{stream_key}" in it. A template we do not recognise is a
		// template whose meaning we do not know, and the failure mode of
		// guessing is a connection to somewhere the operator did not choose --
		// the same failure the services registry was written to prevent.
		return Target{}, fmt.Errorf("%w: Twitch's ingest template %q does not end in %q, so where the "+
			"stream key belongs in it is not established", ErrNoUsableEndpoint, ep.URLTemplate, keyPlaceholder)
	}
	server = strings.TrimRight(server, "/")

	u, err := url.Parse(server)
	if err != nil {
		return Target{}, fmt.Errorf("%w: Twitch's ingest template is not a URL: %v", ErrNoUsableEndpoint, err)
	}
	// Checked even though pickEndpoint already filtered on the protocol field,
	// because the protocol field and the scheme are two different statements and
	// nothing makes them agree. This is the one that decides what goes on the
	// wire.
	if u.Scheme != "rtmp" && u.Scheme != "rtmps" {
		return Target{}, fmt.Errorf("%w: Twitch's ingest template is %s://, which is not an RTMP publish URL",
			ErrNoUsableEndpoint, u.Scheme)
	}
	if u.Host == "" {
		return Target{}, fmt.Errorf("%w: Twitch's ingest template names no host", ErrNoUsableEndpoint)
	}
	// WHICH host, not merely that there is one. See ingestHostSuffix.
	//
	// Hostname() rather than Host: the port is not part of the name, and the
	// measured Kick form carries an explicit ":443" that would make every
	// suffix comparison against the raw Host fail. Lowercased because DNS is
	// case-insensitive and a response spelling the host "INGEST.Global-..."
	// names the same machine -- a check that refused it would be rejecting the
	// legitimate ingest on a technicality while an attacker simply sends
	// lowercase.
	if host := strings.ToLower(u.Hostname()); !strings.HasSuffix(host, ingestHostSuffix) {
		return Target{}, fmt.Errorf("%w: Twitch's ingest template names host %q, which is not under %q -- "+
			"publishing there would send this broadcast, and the stream key it opens with, to a host "+
			"the response chose rather than one Twitch operates",
			ErrNoUsableEndpoint, host, ingestHostSuffix)
	}

	// AN ABSENT MINTED KEY IS A REFUSAL, NOT A REASON TO SUBSTITUTE THE
	// OPERATOR'S. The previous spelling of this was
	//
	//	key := streamKey
	//	if ep.Authentication != "" { key = ep.Authentication }
	//
	// which reads as a safe default and is the opposite of one. Outcome.Use
	// says the minted key is MANDATORY when a negotiation succeeds, and gives
	// the reason: the minted value carries the agreed ladder signed inside it,
	// so publishing with the operator's key instead connects anyway and sends a
	// ladder the ingest never agreed to. The fallback did exactly that, quietly,
	// whenever `authentication` was missing from the response.
	//
	// It is worse than a correctness bug because of WHAT THE TWO KEYS ARE. The
	// minted key is per-broadcast and dies with the negotiation; the operator's
	// is the long-lived channel credential that is rarely rotated and is worth
	// whatever the channel is worth. The case where the fallback fired -- a
	// response that omits the field -- is therefore the case where it shipped
	// the permanent credential rather than the disposable one.
	//
	// AFTER the host check, deliberately. Both orders refuse the same responses,
	// but this one refuses a hostile host by naming the host, which is the fact
	// an operator reading the log needs; the other would report a missing field
	// and say nothing about where the broadcast was nearly sent.
	key := ep.Authentication
	if key == "" {
		return Target{}, fmt.Errorf("%w: Twitch's %s endpoint carries no minted stream key, and the "+
			"destination's own key is not a substitute for one -- publishing with it would send a "+
			"ladder this negotiation never agreed to, using the long-lived channel credential "+
			"rather than the per-broadcast one",
			ErrNoUsableEndpoint, strings.ToUpper(strings.TrimSpace(ep.Protocol)))
	}

	return Target{URL: server, Key: withConfigID(key, c.Meta.ConfigID)}, nil
}

// pickEndpoint prefers RTMPS.
//
// Not a preference about style. The stream key travels in the RTMP connect as
// the stream name, so on plain RTMP it crosses the network unencrypted -- and
// Twitch offers both, listing RTMP FIRST on every measured response. Taking the
// first entry, which is the obvious loop, picks the cleartext one every time.
func (c *Config) pickEndpoint() (IngestEndpoint, error) {
	var fallback *IngestEndpoint
	for i := range c.IngestEndpoints {
		switch strings.ToUpper(strings.TrimSpace(c.IngestEndpoints[i].Protocol)) {
		case "RTMPS":
			return c.IngestEndpoints[i], nil
		case "RTMP":
			if fallback == nil {
				fallback = &c.IngestEndpoints[i]
			}
		}
	}
	if fallback != nil {
		return *fallback, nil
	}
	return IngestEndpoint{}, fmt.Errorf("%w: Twitch listed %d ingest endpoints and none of them speaks RTMP",
		ErrNoUsableEndpoint, len(c.IngestEndpoints))
}

// withConfigID appends clientConfigId to a stream key, preserving any query the
// key already carries.
//
// Twitch stream keys really do carry query parameters -- "?bandwidthtest=true"
// is the documented one, and it changes what the ingest does with the stream --
// so a naive key+"?clientConfigId=..." would produce two question marks and
// lose the operator's parameter. obs-studio merges them for the same reason.
//
// The key itself is never re-encoded: only the query half goes through
// url.Values, and the two are joined by hand. Percent-encoding a stream key
// would change the credential, which is the defect #306 landed on from the
// other direction.
func withConfigID(key, configID string) string {
	if configID == "" {
		// Nothing to add. Not an error: a response with no config_id is one
		// Twitch cannot be expecting a correlated publish for either.
		return key
	}
	base, rawQuery, hasQuery := strings.Cut(key, "?")
	q := url.Values{}
	if hasQuery {
		// A key whose query does not parse is left with its query intact rather
		// than dropped -- it is the operator's value and we do not understand it
		// well enough to discard it.
		parsed, err := url.ParseQuery(rawQuery)
		if err != nil {
			return key + "&clientConfigId=" + url.QueryEscape(configID)
		}
		q = parsed
	}
	q.Set("clientConfigId", configID)
	return base + "?" + q.Encode()
}
