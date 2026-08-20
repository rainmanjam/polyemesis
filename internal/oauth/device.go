package oauth

// Device code flow: connecting an account from a box the platform cannot call
// back.
//
// Every provider here runs the authorization-code flow, and that flow needs a
// redirect URI the platform will accept. polyemesis builds one from the request
// it is answering (internal/api.Server.redirectURI reads scheme + r.Host), so an
// operator reaching the UI as https://192.168.1.50 or on a self-signed
// certificate has no callback any platform will match. Device flow removes the
// callback from the problem: the box asks the authorization server for a code,
// the operator types that code into the platform's own site on a phone, and the
// box polls for the token.
//
// THIS IS ONE PLATFORM'S CAPABILITY AND IT IS DELIBERATELY NOT FOUR. Issue #442
// read each vendor's own reference page, and only Twitch's answer survives
// contact with what polyemesis needs:
//
//	TWITCH   supported, and the vendor's own device-flow example requests
//	         channel:manage:broadcast -- the scope this package already asks
//	         for. No second app registration. Implemented; see twitch_device.go.
//
//	YOUTUBE  NOT IMPLEMENTED, on purpose. Google's limited-input-device page
//	         states "The OAuth 2.0 flow for devices is supported only for the
//	         following scopes", and for YouTube that list is exactly `youtube`
//	         and `youtube.readonly`. `youtube.upload` is not on it, so
//	         thumbnails become unreachable, and it needs a separate "TVs and
//	         Limited Input devices" client on top. A loopback redirect gets
//	         YouTube the ORDINARY code flow with no scope ceiling and needs no
//	         code at all -- see docs/CONFIGURATION.md. Device flow would be a
//	         downgrade there, not a feature.
//
//	FACEBOOK NOT IMPLEMENTED. Device Login exists and is current, but which
//	         permissions it can grant is UNVERIFIED -- no page read so far says
//	         whether live-video publishing permissions are grantable this way.
//	         An unverified permission set is not something to build a connect
//	         button on; it fails at the moment somebody goes live.
//
//	KICK     NOT POSSIBLE. Its token endpoint documents authorization_code and
//	         nothing else; there is no device authorization endpoint and no
//	         urn:ietf:params:oauth:grant-type:device_code.
//
// So DeviceFlower is discovered, never assumed. Resolve it with DeviceFor (or
// Set.DeviceFor); "this platform has no device flow" is the answer for three of
// the four and has to be handled once, exactly like TargetsFor and StatsFor.
//
// ON THE CLIENT STAYING CONFIDENTIAL. Twitch, verbatim, on public clients:
// they "are only limited to the usage of device authorization grant flow to
// obtain OAuth tokens and cannot use any of the other flows". polyemesis
// supports both device flow AND today's code flow from one operator-registered
// app, so the app must stay confidential and keep its secret. Nothing in this
// file drops the secret; the device calls simply do not send one, because the
// vendor's own example does not.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// DeviceAuth is the first half of a device-code flow: what to put in front of
// the operator, and the handle to poll with.
//
// THE JSON TAGS ARE A SECURITY PROPERTY, NOT DECORATION. UserCode and
// VerificationURI are meant for a browser; DeviceCode is the bearer-equivalent
// secret that redeems the token and must never leave the server. Tagging it
// `json:"-"` here means a future handler that marshals this struct straight out
// of a response cannot leak it by omission -- which is the shape of mistake
// that only ever gets noticed after it ships.
type DeviceAuth struct {
	// DeviceCode is polled with. Secret; see the note above.
	DeviceCode string `json:"-"`
	// UserCode is what the operator types at VerificationURI.
	UserCode string `json:"userCode"`
	// VerificationURI is the platform's own page. It is returned by the
	// platform and passed through untouched -- never assembled here, for the
	// same reason kick.go refuses to invent an ingest URL: a hostname written
	// from memory publishes to nowhere and reads as our bug.
	VerificationURI string `json:"verificationUri"`
	// Interval is the minimum wait between polls, as the platform stated it,
	// floored by deviceMinPollInterval.
	Interval time.Duration `json:"-"`
	// ExpiresAt is when the device code dies and the operator has to start
	// again. Zero if the platform said nothing.
	ExpiresAt time.Time `json:"expiresAt"`
}

// deviceMinPollInterval is the floor under whatever the platform asks for.
//
// A platform that omits `interval`, or sends 0, would otherwise leave a caller
// polling a token endpoint as fast as the network allows -- which is how an
// operator's app gets rate-limited for the whole box, mid-connect, for a
// mistake nobody made. Twitch's own responses carry 5; this only ever applies
// when the answer is missing or absurd.
const deviceMinPollInterval = 5 * time.Second

// ErrDeviceAuthPending is the ordinary answer to nearly every poll: the
// operator has not finished at the verification URI yet. A caller sleeps for
// DeviceAuth.Interval and asks again. It is NOT a failure and must never be
// shown to an operator as one.
var ErrDeviceAuthPending = errors.New("device authorization is still pending")

// ErrDeviceCodeSpent means this device code will never produce a token: it has
// already been redeemed, or it expired. A caller stops polling and starts the
// flow over.
//
// One sentinel for two causes, because the platform gives one answer for both.
// Twitch replies "invalid device code" whether the code was used or timed out,
// and inventing a distinction it does not draw would mean showing an operator a
// reason we made up.
var ErrDeviceCodeSpent = errors.New("this device code is no longer valid; start the flow again")

// DeviceFlower is the optional capability for a platform that can mint tokens
// without a redirect URI.
//
// It embeds Provider because a device-flow connection produces the same
// Account, Ingest and Refresh work afterwards as a code-flow one: the flow
// differs only in how the FIRST token is obtained. A caller that has polled to
// a Token holds something indistinguishable from an Exchange result and stores
// it the same way.
//
// ON REFRESH, WHICH IS WHERE THIS CAN LOSE SOMEBODY'S ACCOUNT. Twitch's device
// tokens are described verbatim as "one time use only, meaning if they are used
// in refreshing a token they will become invalid after use", with an inactive
// refresh token expiring after 30 days and access tokens lasting hours. The
// consequence is not in this package: whoever refreshes must persist the
// REPLACEMENT refresh token, because the one it was traded for is already dead.
// internal/api.Server.tokenFor does that -- it upserts the account in the same
// call that refreshed it -- so a device-flow token needs no separate refresh
// path and none is added here. The residual hazard is a crash in the window
// between Twitch's 200 and that write, which orphans the account and is fixed
// by reconnecting; it is named here so the next person reads it as known rather
// than discovering it from a support ticket.
type DeviceFlower interface {
	Provider
	// StartDeviceAuth asks the authorization server for a device code and the
	// code the operator will type. It requests this provider's Scopes(), the
	// same set the code flow asks for, so an account connected either way
	// carries the same ScopeVersion.
	StartDeviceAuth(ctx context.Context, clientID string) (*DeviceAuth, error)
	// PollDeviceAuth attempts to redeem the device code exactly once.
	//
	// SINGLE-SHOT ON PURPOSE: it does not sleep, retry or loop. Every other
	// provider call in this package is one HTTP request, and a loop hidden
	// inside one would hold a request handler for up to the code's lifetime
	// (Twitch: 1800 seconds) with no way for the operator to cancel. Pacing
	// belongs to the caller, which has the DeviceAuth carrying Interval and
	// ExpiresAt and a context to honour.
	//
	// Returns ErrDeviceAuthPending while the operator has not finished, and
	// ErrDeviceCodeSpent once the code can never work. Anything else is a real
	// failure.
	PollDeviceAuth(ctx context.Context, clientID, deviceCode string) (*Token, error)
}

// DeviceFor resolves the device-flow capability for a platform against the
// production providers. False is a supported answer and means the platform has
// no device flow polyemesis can use -- see the per-platform reasoning at the
// top of this file. Prefer Set.DeviceFor wherever a Set is in hand, or the
// lookup reaches the real platforms from a test that stubbed everything else.
func DeviceFor(p db.Platform) (DeviceFlower, bool) {
	pr, ok := Providers()[p]
	if !ok {
		return nil, false
	}
	df, ok := pr.(DeviceFlower)
	return df, ok
}

// postFormJSON is postForm's sibling for a response that is NOT a token.
//
// The device authorization endpoint answers with device_code/user_code/
// verification_uri and no access_token, so postForm rejects it ("token response
// contained no access_token") before the caller ever sees the body. This is the
// same request -- form-encoded POST to the authorization server, on the shared
// httpClient so the endpoints_test.go escape guard can see it -- decoded into
// whatever the caller asked for.
//
// It returns *tokenStatusError on a non-2xx for the reason that type exists:
// the caller classifies by comparing an int and reading a body, not by parsing
// a status code back out of a formatted string. Device flow needs exactly that,
// because "still waiting for the operator" arrives as an HTTP 400.
func postFormJSON(ctx context.Context, endpoint string, form url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &tokenStatusError{code: resp.StatusCode, body: snippet(body)}
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("decode device response: %w", err)
	}
	return nil
}

// devicePollInterval turns the platform's `interval` seconds into a duration
// that cannot produce a hot loop. See deviceMinPollInterval.
func devicePollInterval(seconds int) time.Duration {
	d := time.Duration(seconds) * time.Second
	if d < deviceMinPollInterval {
		return deviceMinPollInterval
	}
	return d
}
