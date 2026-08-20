package oauth

// Twitch's device code grant flow (DCF), the one device flow polyemesis
// implements. device.go says why the other three platforms do not get one.
//
// Every parameter name, response field and error string below was read off
// https://dev.twitch.tv/docs/authentication/getting-tokens-oauth/ rather than
// inferred from RFC 8628, and the difference is not pedantry: Twitch's flow is
// RFC-shaped but not RFC-spelled in two places that would each be a silent bug.
//
//  1. The authorization endpoint takes `scopes`, PLURAL. The token endpoint,
//     in the same vendor example, takes `scope`, SINGULAR. Sending the RFC's
//     spelling to either one asks for no scopes at all, and the failure does
//     not arrive until a Helix write 401s during a broadcast.
//
//  2. Pending is not `{"error":"authorization_pending"}`. Twitch answers
//     `{"status":400,"message":"authorization_pending"}` -- its own envelope,
//     with the RFC's token inside it. A classifier keyed on the `error` field
//     would read every poll as a hard failure and abandon the connection five
//     seconds in.

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// twitchDeviceGrant is the RFC 8628 grant type, which IS the spelling Twitch
// documents for the token call.
const twitchDeviceGrant = "urn:ietf:params:oauth:grant-type:device_code"

// deviceEndpoint is the authorization server's device endpoint. Built from the
// instance base like every other call -- a production constant written inline
// here would make a provider built with WithBaseURL reach id.twitch.tv from a
// test that believed it was stubbed. See endpoints.go.
func (t *Twitch) deviceEndpoint() string { return t.authBase(twitchIDBase) + "/oauth2/device" }

// The capability is resolved by type assertion in DeviceFor, so a signature
// that drifts would make Twitch silently stop offering device flow rather than
// fail to build. This turns that into a compile error, next to the methods it
// constrains -- the same guard twitch.go puts on LiveStatter.
var _ DeviceFlower = (*Twitch)(nil)

// StartDeviceAuth begins the flow. It asks for exactly Scopes(), so an account
// connected this way is indistinguishable from a code-flow one afterwards and
// ScopeVersion keeps meaning what it means.
//
// NO CLIENT SECRET IS SENT, and that is a decision rather than an omission. The
// vendor's example passes client_id and scopes and nothing else. polyemesis's
// app stays CONFIDENTIAL -- it must, because a public Twitch client "cannot use
// any of the other flows" and the code flow is what every existing operator is
// connected through -- but a confidential client is not obliged to present its
// secret here, and sending a parameter the documented request does not contain
// risks a rejection nobody could debug. Same doctrine as the PKCE decision in
// twitch.go: do not send Twitch parameters Twitch has not written down.
func (t *Twitch) StartDeviceAuth(ctx context.Context, clientID string) (*DeviceAuth, error) {
	var out struct {
		DeviceCode      string `json:"device_code"`
		ExpiresIn       int    `json:"expires_in"`
		Interval        int    `json:"interval"`
		UserCode        string `json:"user_code"`
		VerificationURI string `json:"verification_uri"`
	}
	// `scopes`, plural, space-delimited -- see the header. url.Values encodes
	// the spaces, which is what "You must URL encode the list" asks for.
	if err := postFormJSON(ctx, t.deviceEndpoint(), url.Values{
		"client_id": {clientID},
		"scopes":    {strings.Join(t.Scopes(), " ")},
	}, &out); err != nil {
		return nil, err
	}

	// All three are refused rather than defaulted. A missing verification_uri
	// is the one that matters: the only recovery would be to write
	// twitch.tv/activate from memory, and a hostname assembled here instead of
	// returned by the platform is the fabricated-URL failure kick.go's Ingest
	// exists to refuse. A code with nowhere to type it is not a usable flow.
	if out.DeviceCode == "" || out.UserCode == "" || out.VerificationURI == "" {
		return nil, fmt.Errorf("Twitch's device endpoint answered without a device code, " +
			"user code or verification address; the flow cannot be started")
	}

	auth := &DeviceAuth{
		DeviceCode:      out.DeviceCode,
		UserCode:        out.UserCode,
		VerificationURI: out.VerificationURI,
		Interval:        devicePollInterval(out.Interval),
	}
	if out.ExpiresIn > 0 {
		auth.ExpiresAt = time.Now().Add(time.Duration(out.ExpiresIn) * time.Second)
	}
	return auth, nil
}

// PollDeviceAuth redeems the device code once. See DeviceFlower for why it does
// not loop.
//
// The token it returns carries a refresh token that is ONE TIME USE -- whoever
// stores it must store the replacement every refresh returns. That property
// belongs to the token, not to this call; device.go's DeviceFlower comment
// carries the full note.
func (t *Twitch) PollDeviceAuth(ctx context.Context, clientID, deviceCode string) (*Token, error) {
	// Refused before the request, because the caller is a polling loop. An
	// empty device_code cannot succeed, so sending it would burn a request
	// every interval forever against an endpoint that rate-limits the
	// operator's whole app.
	if deviceCode == "" {
		return nil, fmt.Errorf("no device code to poll with; start the device flow first")
	}
	tok, err := postForm(ctx, t.tokenEndpoint(), url.Values{
		"client_id": {clientID},
		// `scope`, SINGULAR, on this call. See the header.
		"scope":       {strings.Join(t.Scopes(), " ")},
		"device_code": {deviceCode},
		"grant_type":  {twitchDeviceGrant},
	}, nil)
	if err != nil {
		return nil, classifyTwitchDeviceError(err)
	}
	return tok, nil
}

// classifyTwitchDeviceError turns Twitch's two documented device-flow refusals
// into the sentinels a polling caller branches on, and leaves everything else
// alone.
//
// IT MATCHES THE TOKEN, NOT THE ENVELOPE. Twitch documents
// `{"status":400,"message":"authorization_pending"}`; RFC 8628 specifies
// `{"error":"authorization_pending"}`. Searching the body for the token itself
// reads both, so a future day where Twitch aligns its envelope with the RFC
// changes nothing here -- whereas a matcher keyed on `"message":` would go on
// compiling, go on passing every test written against today's body, and start
// reporting a pending authorization as a hard failure the moment the wording
// moved.
//
// The status code is checked as well as the body so that a 500 whose HTML
// happens to contain the phrase cannot be read as a pending authorization.
//
// Everything else is returned untouched, including 429 and 5xx. A caller that
// cannot tell "Twitch is down" from "keep waiting" would poll through an outage
// forever; those are real errors and stop the loop.
func classifyTwitchDeviceError(err error) error {
	var se *tokenStatusError
	if !errors.As(err, &se) {
		return err
	}
	if se.code < 400 || se.code >= 500 {
		return err
	}
	body := strings.ToLower(se.body)
	switch {
	case strings.Contains(body, "authorization_pending"):
		return fmt.Errorf("%w: %s", ErrDeviceAuthPending, se.Error())
	// "invalid device code" is Twitch's answer to a code already redeemed AND
	// to one that expired; device.go's ErrDeviceCodeSpent covers both because
	// Twitch does not separate them.
	case strings.Contains(body, "invalid device code"):
		return fmt.Errorf("%w: %s", ErrDeviceCodeSpent, se.Error())
	}
	return err
}
