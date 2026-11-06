// Package platform is the one table of per-platform FACTS: the numbers, limits
// and flags that used to be copied between db (presets), routing (music
// policy), services (encoder ceilings, codecs), oauth (capabilities, limits,
// API bases), chat (send limits), automod (what can be moderated), publish
// (upload costs, scopes) and the engine (inline Kick/Twitch/YouTube checks).
//
// A LEAF PACKAGE, deliberately. It imports nothing from this module, so every
// one of those packages can read it without a cycle. The corollary: it cannot
// ask internal/chat what an adapter implements. chat publishes its own
// per-adapter profile (chat.Profiles) and TestRegistryAgreesWithTheChatAdapters
// asserts that the two descriptions match, in both directions.
//
// WHAT LIVES HERE AND WHAT DOES NOT. Facts a platform decides -- a limit, a
// cost, a host suffix, an API version -- are rows. Prose written for the
// operator (preset notes, capability summaries, warning sentences) stays with
// its consumer: the UI translates it, and moving English into this table would
// put it out of the translators' reach.
//
// ONE FIELD PER NUMBER A CONSUMER ACTUALLY USES. Where two consumers read two
// different numbers for what looks like one fact (YouTube's video bitrate is
// 12000 in the preset guidance and 51000 in the OBS-seeded ceiling), the row
// carries both, named for what they are, with their sources. Collapsing them is
// a product decision and is listed in the refactor report, not made here.
package platform

// Count says how a limit is counted. The same limit is counted differently by
// different callers (YouTube's title is checked in runes at the metadata API
// and in UTF-16 units by the clip publisher), so a row names the unit rather
// than leaving it to each caller.
type Count string

const (
	Runes Count = "runes"
	UTF16 Count = "utf16"
)

// Guidance is a platform's own published encoder advice, with where it was
// read and when. A zero field means the platform states no figure, which is not
// the same as a limit of zero.
type Guidance struct {
	Width, Height, FPS int
	KbpsMin, KbpsMax   int
	GOPSeconds         float64
	Source, Checked    string
}

// Ceilings are the encoder limits seeded from OBS's rtmp-services data, which
// CheckEncoder and the codec checks read. A different source from Guidance, and
// not always the same number; see the package comment.
type Ceilings struct {
	KeyintSeconds int
	MaxVideoKbps  int
	MaxAudioKbps  int
	MaxFps        int
	X264Opts      string
	Source        string
}

// Music is the default music-rights policy for destinations on the platform.
type Music struct {
	Exclude bool
	Reason  string
	// TargetLUFS is the loudness the platform's own normalizer aims at; zero
	// means no opinion.
	TargetLUFS float64
}

// Moderation is what chat moderation can do on the platform.
type Moderation struct {
	// Hide is an upstream, reversible hide.
	Hide bool
	// Delete removes a message; TimeoutBan covers timeout and ban, which route
	// through the same adapter method.
	Delete, TimeoutBan bool
}

// Chat is the platform's chat-send side.
type Chat struct {
	// SendMax is the longest message the platform accepts, in SendUnit. Zero
	// means polyemesis cannot send to this platform's chat.
	SendMax  int
	SendUnit Count
}

// Text is the title and description limits at go-live.
type Text struct {
	TitleMax, DescriptionMax int
	// StreamTitleMax is the title of the live stream resource, where the
	// platform distinguishes it from the broadcast's (YouTube: 128).
	StreamTitleMax int
	// GoLiveTitleMax is the title sent with a go-live announcement (X: 280).
	GoLiveTitleMax int
	// CaptionMax is a clip's caption or post text (TikTok).
	CaptionMax int
	// MetadataUnit is how the metadata API (api/metadata.go) counts; PublishUnit
	// is how the clip publisher counts. They differ for YouTube today.
	MetadataUnit, PublishUnit Count
}

// What a duration limit measures. Plain strings: internal/oauth serves them to
// the UI under its own typed names, and those typed values are the wire
// contract.
const (
	LimitBroadcastDuration = "broadcastDuration"
	LimitArchiveDuration   = "archiveDuration"
)

// How well a limit is established.
const (
	LimitDocumented = "documented"
	LimitReported   = "reported"
	LimitUnverified = "unverified"
)

// Limit is one published cap on a broadcast.
type Limit struct {
	Kind   string
	Hours  float64
	Status string
	Source string
}

// Row is one platform. Key is unique and is the preset id of the platform's
// primary preset. Enum is the value the destination's platform column stores,
// or "" when a destination made from this row's presets saves as "custom"
// (Instagram, LinkedIn, Dailymotion, Owncast): those rows exist so their facts
// have a home, and reaching one from a stored destination is the open problem
// the refactor report describes.
type Row struct {
	Key  string
	Enum string
	// Name is the display name; ShortName is the one routing's music-policy
	// table uses, which drops "Live".
	Name, ShortName string
	// Presets are the catalogue ids that belong to this row.
	Presets []string
	// PresetsSaveAsEnum reports whether a destination made from those presets
	// carries Enum. False for Rumble: the enum exists for chat only.
	PresetsSaveAsEnum bool
	// AcceptsDestinations reports whether db.Validate lets a destination carry
	// Enum. TikTok exists for clip publishing and is refused.
	AcceptsDestinations bool
	// Retired is why the platform can no longer be streamed to, or "".
	Retired string

	// Ingest shape.
	PerChannelIngest bool // the host is issued per channel; nothing to prefill
	IVS              bool // ingests through Amazon IVS: application path must be /app

	Guidance *Guidance
	Ceilings *Ceilings

	VideoCodecs []string
	AudioCodecs []string
	// RefusesVideoCodecs are codecs the platform's own page says it refuses.
	// Narrower than "not in VideoCodecs": only these get the certain wording.
	RefusesVideoCodecs []string
	// AudioTracksMax is how many audio tracks the ingest takes; zero means no
	// stated limit.
	AudioTracksMax int
	// HealthAPI reports that the platform exposes a stream-health endpoint.
	HealthAPI bool

	Music      *Music
	Moderation *Moderation
	Chat       Chat
	Text       Text
	Limits     []Limit

	// APIHost is the scheme and host; APIPath the versioned prefix beneath it
	// ("/youtube/v3", "/v24.0", "/helix"), empty where there is none. APIVersion
	// is the pinned version on its own, for the callers that spell it elsewhere
	// (Facebook's OAuth dialog URL). APIRoot joins the first two.
	APIHost, APIPath, APIVersion string
}

// Platform enum values. Untyped so db and routing can type them to their own
// Platform types without a conversion in every use.
const (
	Custom   = "custom"
	YouTube  = "youtube"
	Twitch   = "twitch"
	Kick     = "kick"
	Facebook = "facebook"
	Rumble   = "rumble"
	X        = "x"
	Trovo    = "trovo"
	Vimeo    = "vimeo"
	TikTok   = "tiktok"
)

var rows = buildRows()

// All returns every row in registry order. The slice is a copy; the rows share
// their inner slices with the registry and must be treated as read-only.
func All() []Row {
	out := make([]Row, len(rows))
	copy(out, rows)
	return out
}

// ByKey finds a row by its key.
func ByKey(key string) (Row, bool) {
	for _, r := range rows {
		if r.Key == key {
			return r, true
		}
	}
	return Row{}, false
}

// ByEnum finds the row for a stored platform value. "" and "custom" have rows
// only if one is declared; unknown values return false.
func ByEnum(enum string) (Row, bool) {
	if enum == "" {
		return Row{}, false
	}
	for _, r := range rows {
		if r.Enum == enum {
			return r, true
		}
	}
	return Row{}, false
}

// Must is ByEnum for package-level initialisers, where a missing row is a
// programming error and should stop the build's tests rather than read as zero.
func Must(enum string) Row {
	r, ok := ByEnum(enum)
	if !ok {
		panic("platform: no row for " + enum)
	}
	return r
}

// ByPreset finds the row a catalogue preset belongs to.
func ByPreset(id string) (Row, bool) {
	for _, r := range rows {
		for _, p := range r.Presets {
			if p == id {
				return r, true
			}
		}
	}
	return Row{}, false
}

// Enums returns the platform values that destinations may carry, in registry
// order.
func Enums() []string {
	var out []string
	for _, r := range rows {
		if r.Enum != "" && r.AcceptsDestinations {
			out = append(out, r.Enum)
		}
	}
	return out
}

// APIRoot is the API root callers append resource paths to.
func (r Row) APIRoot() string { return r.APIHost + r.APIPath }

// LUFSStreaming is the programme loudness YouTube, Twitch, Spotify and the
// other streaming normalizers target. routing re-exports it for the mixer.
const LUFSStreaming = -14.0
