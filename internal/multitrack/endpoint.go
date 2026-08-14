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

	// THE NEGOTIATED KEY WINS, and on a successful negotiation there always is
	// one. It is not the operator's key: it is a signed value carrying the
	// agreed ladder inside it, with the operator's key as its final segment (see
	// IngestEndpoint.Authentication). Publishing with the operator's key instead
	// would send a stream the ingest never agreed the shape of -- which is the
	// single easiest way to implement this feature so that it looks finished and
	// silently is not.
	key := streamKey
	if ep.Authentication != "" {
		key = ep.Authentication
	}
	if key == "" {
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
