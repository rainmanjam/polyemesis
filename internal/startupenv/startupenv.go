// Package startupenv reads the process environment switches polyemesis honours
// ONCE, at startup, into one value that is handed down. Nothing else in the
// server calls os.Getenv for these, so every consumer sees the same answer and
// they all parse a boolean the same way.
package startupenv

import (
	"fmt"
	"os"
	"strings"
)

// The variable names. Operator docs (CONFIGURATION, PLATFORMS, CAPTIONS,
// CLIP-PUBLISHING) cite them.
const (
	XPublishVar          = "POLYEMESIS_X_PUBLISH"
	XLivestreamVar       = "POLYEMESIS_X_LIVESTREAM"
	FacebookCaptionsVar  = "POLYEMESIS_CAPTIONS_FACEBOOK"
	XAppBearerVar        = "POLYEMESIS_X_APP_BEARER"
	RumbleChatKeyVar     = "RUMBLE_CHAT_API_KEY"
	RumbleChatChannelVar = "RUMBLE_CHAT_CHANNEL"
)

// Env is the startup environment. The zero value has every switch off and every
// string empty.
type Env struct {
	// XPublish opens clip publishing to X.
	XPublish bool
	// XLivestream opens X's Livestream family (go-live, chat), once X has
	// whitelisted the app.
	XLivestream bool
	// FacebookCaptions opens the provisional facebook_sei caption path.
	FacebookCaptions bool

	// XAppBearer is the app-only bearer token X's activity stream needs.
	XAppBearer string
	// RumbleChatKey is the Rumble live-stream API key; RumbleChatChannel names
	// the channel. Both are only ever read from the environment: argv is
	// world-readable in ps.
	RumbleChatKey     string
	RumbleChatChannel string
}

// FromEnv reads the process environment.
func FromEnv() Env { return FromLookup(os.Getenv) }

// FromLookup is FromEnv over any lookup, so a test needs no process state.
func FromLookup(get func(string) string) Env {
	return Env{
		XPublish:          Flag(get(XPublishVar)),
		XLivestream:       Flag(get(XLivestreamVar)),
		FacebookCaptions:  Flag(get(FacebookCaptionsVar)),
		XAppBearer:        strings.TrimSpace(get(XAppBearerVar)),
		RumbleChatKey:     strings.TrimSpace(get(RumbleChatKeyVar)),
		RumbleChatChannel: strings.TrimSpace(get(RumbleChatChannelVar)),
	}
}

// Flag is the one rule for a boolean switch: "1" or "true", ignoring case and
// surrounding space. Anything else, including "yes" and "on", is off.
func Flag(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true":
		return true
	}
	return false
}

// String names the switches and says which secrets are set, never their values.
func (e Env) String() string {
	return fmt.Sprintf("startupenv{xPublish:%t xLivestream:%t facebookCaptions:%t xAppBearerSet:%t rumbleKeySet:%t rumbleChannel:%q}",
		e.XPublish, e.XLivestream, e.FacebookCaptions, e.XAppBearer != "", e.RumbleChatKey != "", e.RumbleChatChannel)
}

// GoString keeps %#v from printing the secrets that String keeps out.
func (e Env) GoString() string { return e.String() }
