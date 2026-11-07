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

import (
	"slices"
	"sort"
)

// Guidance is a platform's own published encoder advice (what it RECOMMENDS), with where it was
// read and when. A zero field means the platform states no figure, which is not
// the same as a limit of zero.
type Guidance struct {
	Width, Height, FPS int
	KbpsMin, KbpsMax   int
	GOPSeconds         float64
	Source, Checked    string
}

// Ceilings are the encoder MAXIMUMS the platform enforces seeded from OBS's rtmp-services data, which
// CheckEncoder (audio only) and the codec checks read. A different source from Guidance, and
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
	// NoTimeout is a platform whose TimeoutBan is permanent only: it can
	// remove a person and cannot time one out (Facebook's Page block list has
	// no duration). It only means something with TimeoutBan set.
	NoTimeout bool
	// Order is the platform's position in automod's platform list, whose order
	// is part of the API response.
	Order int
}

// Chat is the platform's chat-send side.
type Chat struct {
	// SendMax is the longest message the platform accepts, in runes. Zero
	// means polyemesis cannot send to this platform's chat.
	SendMax int
	// Order is the platform's position in the chat send-limits list, whose
	// order is part of the API response.
	Order int
	// OverlayOrder is the platform's position in the chat overlay's platform
	// filter, whose order is part of the API response. Zero means chat has no
	// adapter for the platform, so an overlay cannot filter on it.
	OverlayOrder int
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

	// PublishScopes are the OAuth scopes clip publishing needs on top of
	// sign-in. Sign-in reads them from here (X: tweet.write and media.write, from
	// scope version 2), so an account that reconnects holds them; one connected
	// earlier has to reconnect once.
	PublishScopes []string
	// PublishAnyScope is the other shape: any ONE of these is enough (YouTube's
	// videos.insert documents four sufficient scopes, one of which the sign-in
	// already asks for).
	PublishAnyScope []string

	Music      *Music
	Moderation *Moderation
	Chat       Chat
	Limits     []Limit
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

// All returns every row in registry order. Every row is a deep copy.
func All() []Row {
	out := make([]Row, len(rows))
	for i, r := range rows {
		out[i] = r.clone()
	}
	return out
}

// clone deep-copies a row, so no caller can reach back into the registry by
// editing a slice or struct it was handed.
func (r Row) clone() Row {
	r.Presets = slices.Clone(r.Presets)
	r.VideoCodecs = slices.Clone(r.VideoCodecs)
	r.AudioCodecs = slices.Clone(r.AudioCodecs)
	r.RefusesVideoCodecs = slices.Clone(r.RefusesVideoCodecs)
	r.PublishScopes = slices.Clone(r.PublishScopes)
	r.PublishAnyScope = slices.Clone(r.PublishAnyScope)
	r.Limits = slices.Clone(r.Limits)
	if r.Guidance != nil {
		g := *r.Guidance
		r.Guidance = &g
	}
	if r.Ceilings != nil {
		c := *r.Ceilings
		r.Ceilings = &c
	}
	if r.Music != nil {
		m := *r.Music
		r.Music = &m
	}
	if r.Moderation != nil {
		m := *r.Moderation
		r.Moderation = &m
	}
	return r
}

// ByKey finds a row by its key.
func ByKey(key string) (Row, bool) {
	for _, r := range rows {
		if r.Key == key {
			return r.clone(), true
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
			return r.clone(), true
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
				return r.clone(), true
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

// LUFSStreaming is the programme loudness YouTube, Twitch, Spotify and the
// other streaming normalizers target. routing re-exports it for the mixer.
const LUFSStreaming = -14.0

// Moderated returns the rows chat moderation can act on, in automod's list
// order.
func Moderated() []Row {
	return ordered(func(r Row) (int, bool) {
		if r.Moderation == nil {
			return 0, false
		}
		return r.Moderation.Order, true
	})
}

// ChatSenders returns the rows polyemesis can send chat to, in the send-limits
// list order.
func ChatSenders() []Row {
	return ordered(func(r Row) (int, bool) {
		return r.Chat.Order, r.Chat.SendMax > 0
	})
}

// ChatOverlays returns the rows an overlay can filter chat by: the platforms
// chat has an adapter for, in the overlay filter's list order.
func ChatOverlays() []Row {
	return ordered(func(r Row) (int, bool) {
		return r.Chat.OverlayOrder, r.Chat.OverlayOrder > 0
	})
}

func ordered(key func(Row) (int, bool)) []Row {
	var out []Row
	for _, r := range rows {
		if _, ok := key(r); ok {
			out = append(out, r.clone())
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, _ := key(out[i])
		b, _ := key(out[j])
		return a < b
	})
	return out
}
