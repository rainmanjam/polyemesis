// Package startupenv reads the process environment switches polyemesis honours
// ONCE, at startup, into one value that is handed down. Nothing else in the
// server calls os.Getenv for these, so every consumer sees the same answer and
// they all parse a boolean the same way.
//
// Two variables are still read where they are used, on purpose, and neither is
// a switch two parts of the server must agree on: POLYEMESIS_RELAY_CAPTURE
// (internal/relay) and POLYEMESIS_RTMP_DROP_LOG (internal/rtmpserver) are
// developer diagnostics read where the capture file is opened. They change no
// behaviour, and threading them through the relay and RTMP constructors would
// put a parameter on hot paths for a debug aid.
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
	SetupCodeVar         = "POLYEMESIS_SETUP_CODE"
	// FFmpegLogLevelVar, IngestCaptureVar and FFmpegAssumeMajorVar belong to
	// FFmpeg: internal/ffmpeg takes them from here and nothing there calls
	// Getenv.
	FFmpegLogLevelVar    = "POLYEMESIS_FFMPEG_LOGLEVEL"
	IngestCaptureVar     = "POLYEMESIS_INGEST_CAPTURE"
	FFmpegAssumeMajorVar = "POLYEMESIS_FFMPEG_ASSUME_MAJOR"
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

	// SetupCode presets the first-run setup code (see auth.PrepareSetupCode). It
	// is passed on exactly as set: the auth package owns its rules.
	SetupCode string

	// FFmpegLogLevel is the -loglevel every FFmpeg child gets in place of its own
	// default. Empty leaves each child's default alone.
	FFmpegLogLevel string
	// IngestCapture names a file the ingest child also writes the identical
	// stream to, for examining what the relay fans out. Empty is off.
	IngestCapture string
	// FFmpegAssumeMajor states the major version of a build whose own version
	// string cannot be read. It is passed on as set: internal/ffmpeg parses it
	// and owns the floor check.
	FFmpegAssumeMajor string
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
		SetupCode:         get(SetupCodeVar),
		FFmpegLogLevel:    strings.TrimSpace(get(FFmpegLogLevelVar)),
		IngestCapture:     strings.TrimSpace(get(IngestCaptureVar)),
		FFmpegAssumeMajor: strings.TrimSpace(get(FFmpegAssumeMajorVar)),
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
	return fmt.Sprintf("startupenv{xPublish:%t xLivestream:%t facebookCaptions:%t xAppBearerSet:%t rumbleKeySet:%t rumbleChannel:%q setupCodeSet:%t ffmpegLogLevel:%q ingestCapture:%q ffmpegAssumeMajor:%q}",
		e.XPublish, e.XLivestream, e.FacebookCaptions, e.XAppBearer != "", e.RumbleChatKey != "", e.RumbleChatChannel, e.SetupCode != "",
		e.FFmpegLogLevel, e.IngestCapture, e.FFmpegAssumeMajor)
}

// GoString keeps %#v from printing the secrets that String keeps out.
func (e Env) GoString() string { return e.String() }
