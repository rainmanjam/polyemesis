package captions

import "time"

// Mode is how captions reach one destination. It is the value stored in
// destinations.caption_mode, and "off" is the default.
type Mode string

const (
	ModeOff         Mode = "off"
	ModeSEI         Mode = "sei"
	ModeYouTubePOST Mode = "youtube_post"
	ModeFacebookSEI Mode = "facebook_sei"
)

// Support is the state of one platform's caption path, as the code knows it.
type Support struct {
	// Mode is the path this platform uses, or "" when none is built.
	Mode Mode
	// Status says how far the path is verified. "go" has a documented route and
	// is built; "provisional" is built behind a flag or with an unverified detail
	// and needs a live check; "skipped" is not built, and Reason says why.
	Status string
	// Reason is the sentence shown to the operator. It is required for "skipped"
	// and for "provisional", and empty for "go".
	Reason string
	// Embedded is a second route the platform accepts, carried in the video
	// itself, and is "" when there is none. It is allowed with the same rules as
	// Mode, and it is what the operator uses while Mode is provisional.
	Embedded Mode
}

// platforms is the decision table from the #8 spike, in one place so the UI and
// the API read the same answer. Kick is skipped because no documented route
// exists, and a path built without one would be a guess shown as a feature.
var platforms = map[string]Support{
	// Status "go" is the build state. The SEI route is documented by Twitch, but
	// whether Twitch displays the injected CEA-608 on the stream is still a live
	// check on a test channel with a minted key. That display check is recorded in
	// the CHANGELOG and the docs, and it does not change this status.
	"twitch": {
		Mode:   ModeSEI,
		Status: "go",
	},
	// YouTube has two documented routes. The embedded CEA-608 route is the same
	// SEI carriage Twitch uses, and it is GO. The caption POST is PROVISIONAL, so
	// the platform's status and Reason describe the POST route.
	"youtube": {
		Mode:     ModeYouTubePOST,
		Embedded: ModeSEI,
		Status:   "provisional",
		Reason:   "the caption POST body format is not yet confirmed against a live YouTube ingest; the embedded CEA-608 path is the working fallback",
	},
	"facebook": {
		Mode:   ModeFacebookSEI,
		Status: "provisional",
		Reason: "carriage of CEA-608 in the SEI is not documented by Facebook; it stays off until a live check passes",
	},
	"kick": {
		Status: "skipped",
		Reason: "Kick documents no caption ingest route for live streams, so captions are not offered for it",
	},
}

// SupportFor returns what the captions path for a platform is. An unknown
// platform is off with no mode, so a new platform is never captioned by accident.
func SupportFor(platform string) Support {
	if s, ok := platforms[platform]; ok {
		return s
	}
	return Support{Status: "skipped", Reason: "captions are not built for this platform"}
}

// Allowed reports whether an operator may turn on a mode for a platform. The
// facebook_sei path is behind a flag, so it is refused until the flag is set,
// and a skipped platform refuses every mode.
func Allowed(platform string, mode Mode, facebookFlag bool) bool {
	if mode == ModeOff {
		return true
	}
	s := SupportFor(platform)
	if s.Status == "skipped" || (s.Mode != mode && s.Embedded != mode) {
		return false
	}
	if mode == ModeFacebookSEI {
		return facebookFlag
	}
	return true
}

// youTubeDelays are the broadcast delays YouTube accepts for the HTTP POST
// caption path. The embedded path has no such requirement.
var youTubeDelays = []time.Duration{30 * time.Second, 60 * time.Second}

// YouTubePOSTDelayOK reports whether a destination's delay is one YouTube takes
// for the POST path. The caller passes the delay the destination actually runs
// with, so captions are refused for a stream with some other delay rather than
// posted late.
func YouTubePOSTDelayOK(delay time.Duration) bool {
	for _, d := range youTubeDelays {
		if delay == d {
			return true
		}
	}
	return false
}
