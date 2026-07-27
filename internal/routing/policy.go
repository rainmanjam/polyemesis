package routing

import "strings"

// Music-rights policy.
//
// NONE OF THIS IS LEGAL ADVICE. The table below encodes what these platforms
// are widely observed to *do* to a stream carrying recorded music — mute the
// VOD, block the replay, strike the channel — so that the common case ("my
// licensed music must not reach Twitch, but my local archive should keep it")
// is one click instead of six checkboxes and a good memory. It is a
// convenience default, it will go stale the day a platform changes its mind,
// and every entry is overridable per destination via MusicPolicyChoice.
//
// On its own the table can only ever *add* an exclusion. Removing one takes an
// explicit MusicPolicyKeep, and a platform the table has never heard of keeps
// everything. This repo has three times shipped a check that was wrong in the
// restrictive direction; audio dropped from a mix nobody asked us to touch is
// that same failure wearing a different hat.

// Platform identifies where a destination publishes, for policy purposes.
//
// The string values match internal/db's Platform so a stored destination's
// platform can be handed straight to PolicyFor. It is a separate type rather
// than an import because db imports routing, and because routing is meant to
// stay a pure, dependency-free package.
type Platform string

const (
	PlatformCustom   Platform = "custom"
	PlatformYouTube  Platform = "youtube"
	PlatformTwitch   Platform = "twitch"
	PlatformKick     Platform = "kick"
	PlatformFacebook Platform = "facebook"
	// PlatformFile is a local recording. In db's model that is a destination
	// *kind* rather than a platform, but where the bytes land is the only thing
	// a rights policy cares about, so PlatformFor folds the two together.
	PlatformFile Platform = "file"
)

// PlatformPolicy is one row of the music-rights table: what polyemesis assumes
// about a platform until the operator says otherwise.
type PlatformPolicy struct {
	Platform Platform `json:"platform"`
	// Name is the display name for the policy badge and the preset button.
	Name string `json:"name"`
	// ExcludeMusic is the default answer to "may a track marked as music reach
	// this destination?". It is a default, never an enforcement.
	ExcludeMusic bool `json:"excludeMusic"`
	// Reason is the short phrase that goes in the badge's parentheses, e.g.
	// "Twitch DMCA policy". It names the mechanism, not a legal conclusion.
	Reason string `json:"reason"`
	// TargetLUFS is the programme loudness this platform's own normalizer aims
	// at, so a preset can start there instead of at whatever the ingest
	// happened to be. Zero means the platform has no opinion and polyemesis
	// should not invent one.
	TargetLUFS float64 `json:"targetLufs,omitempty"`
}

// platformPolicies is the table. Ordered for the UI: the platforms that
// restrict music first, then the ones that do not.
var platformPolicies = []PlatformPolicy{
	{
		Platform:     PlatformYouTube,
		Name:         "YouTube",
		ExcludeMusic: true,
		Reason:       "YouTube Content ID",
		TargetLUFS:   LUFSStreaming,
	},
	{
		Platform:     PlatformTwitch,
		Name:         "Twitch",
		ExcludeMusic: true,
		Reason:       "Twitch DMCA policy",
		TargetLUFS:   LUFSStreaming,
	},
	{
		Platform:     PlatformKick,
		Name:         "Kick",
		ExcludeMusic: true,
		Reason:       "Kick DMCA policy",
		TargetLUFS:   LUFSStreaming,
	},
	{
		Platform:     PlatformFacebook,
		Name:         "Facebook",
		ExcludeMusic: true,
		Reason:       "Facebook Rights Manager",
		TargetLUFS:   LUFSStreaming,
	},
	{
		Platform:     PlatformFile,
		Name:         "Local recording",
		ExcludeMusic: false,
		Reason:       "local recording",
		// An archive keeps the dynamics the operator sent it. Loudness
		// normalization is a delivery decision, and a file is not a delivery.
		TargetLUFS: 0,
	},
	{
		Platform:     PlatformCustom,
		Name:         "Custom",
		ExcludeMusic: false,
		Reason:       "custom destination",
		TargetLUFS:   0,
	},
}

// PlatformPolicies returns the whole table, for the settings UI to render and
// for the operator to see exactly what polyemesis is assuming on their behalf.
func PlatformPolicies() []PlatformPolicy {
	return append([]PlatformPolicy(nil), platformPolicies...)
}

// PolicyFor returns the policy for a platform.
//
// An empty or unrecognised platform gets the custom-destination row, which
// keeps every track. A platform this build has never heard of is not evidence
// that its music is unwelcome, and guessing "exclude" would silently delete
// audio from a stream the operator configured by hand.
func PolicyFor(p Platform) PlatformPolicy {
	for _, pol := range platformPolicies {
		if pol.Platform == p {
			return pol
		}
	}
	for _, pol := range platformPolicies {
		if pol.Platform == PlatformCustom {
			return pol
		}
	}
	return PlatformPolicy{Platform: PlatformCustom, Name: "Custom", Reason: "custom destination"}
}

// PlatformFor maps a stored destination's platform and kind onto a policy
// platform. Kind wins: a destination writing to a local file carries no rights
// exposure whatever its branding says, and "file" is how db spells that.
func PlatformFor(platform, kind string) Platform {
	if strings.EqualFold(strings.TrimSpace(kind), "file") {
		return PlatformFile
	}
	p := Platform(strings.ToLower(strings.TrimSpace(platform)))
	for _, pol := range platformPolicies {
		if pol.Platform == p {
			return p
		}
	}
	return PlatformCustom
}

// MusicPolicyChoice is the operator's override of the table for one
// destination. It exists so that no entry above is ever the last word.
type MusicPolicyChoice string

const (
	// MusicPolicyDefault follows the platform table. It is the zero value, so
	// every destination saved before this feature existed follows it.
	MusicPolicyDefault MusicPolicyChoice = ""
	// MusicPolicyKeep sends music regardless of the platform's reputation —
	// the operator holds a licence, or the platform changed its mind before we
	// did.
	MusicPolicyKeep MusicPolicyChoice = "keep"
	// MusicPolicyExclude never sends music, whatever the table says.
	MusicPolicyExclude MusicPolicyChoice = "exclude"
)

// MusicPolicyChoices is the catalogue the UI offers, in menu order.
func MusicPolicyChoices() []MusicPolicyChoice {
	return []MusicPolicyChoice{MusicPolicyDefault, MusicPolicyKeep, MusicPolicyExclude}
}

// ValidMusicPolicyChoice reports whether c is a choice this build understands.
// MusicPolicyDefault counts: it is the absence of an override.
func ValidMusicPolicyChoice(c MusicPolicyChoice) bool {
	switch c {
	case MusicPolicyDefault, MusicPolicyKeep, MusicPolicyExclude:
		return true
	}
	return false
}

// MusicDecision is the resolved answer for one destination, carrying enough
// context for the UI to explain itself without re-deriving anything.
type MusicDecision struct {
	Platform Platform `json:"platform"`
	// Exclude is the answer: true means tracks roled music are kept out.
	Exclude bool `json:"exclude"`
	// Overridden records that the operator, not the table, decided this.
	Overridden bool `json:"overridden"`
	// Reason is the parenthesised phrase in Summary.
	Reason string `json:"reason"`
	// Summary is the badge text, e.g. "music excluded (Twitch DMCA policy)".
	Summary string `json:"summary"`
}

// reasonOverride is what a decision cites when the operator overruled the
// table. Naming the operator rather than the platform matters: a muted VOD is
// then traceable to a person who made a call, not to a stale default.
const reasonOverride = "operator override"

// ResolveMusicPolicy answers "does this destination carry music?" for a
// platform and the operator's override.
//
// An unrecognised choice string is treated as MusicPolicyDefault rather than
// rejected. It arrives from JSON, and a payload polyemesis cannot parse is not
// a reason to start dropping the operator's audio.
func ResolveMusicPolicy(plat Platform, choice MusicPolicyChoice) MusicDecision {
	pol := PolicyFor(plat)
	d := MusicDecision{Platform: pol.Platform, Exclude: pol.ExcludeMusic, Reason: pol.Reason}

	switch choice {
	case MusicPolicyKeep:
		d.Exclude, d.Overridden, d.Reason = false, true, reasonOverride
	case MusicPolicyExclude:
		d.Exclude, d.Overridden, d.Reason = true, true, reasonOverride
	}

	if d.Exclude {
		d.Summary = "music excluded (" + d.Reason + ")"
	} else {
		d.Summary = "music included (" + d.Reason + ")"
	}
	return d
}

// Armed reports whether the decision has anything to act on: an exclusion is
// only a guarantee once some track has actually been marked as music.
func (d MusicDecision) Armed(src Source) bool {
	return d.Exclude && len(src.TracksWithRole(RoleMusic)) > 0
}

// Warning returns the caveat the UI should show beside the badge, or "" when
// there is none. An exclusion nobody can act on is a promise about nothing,
// and the operator would rather hear that now than after the VOD is muted.
func (d MusicDecision) Warning(src Source) string {
	if d.Exclude && !d.Armed(src) {
		return "no ingest track is marked as music, so nothing is being excluded yet"
	}
	return ""
}

// ApplyMusicPolicy returns a copy of p carrying the platform's music-rights
// decision, plus the decision itself for the badge.
//
// It does two things, and they have different lifetimes. Tracks currently
// marked as music are switched off, which is what makes the guarantee true for
// the mix as it stands. ExcludeRoles gains RoleMusic, which is what keeps it
// true after the streamer moves music to another track — the whole reason roles
// exist rather than a "no music" checkbox against an index.
//
// The asymmetry around removal is deliberate. Only an explicit MusicPolicyKeep
// strips an existing RoleMusic exclusion; a platform whose table entry happens
// to permit music never quietly undoes a decision someone already made.
//
// The returned profile can select nothing at all, when every track the ingest
// carries is music. That is honest — there is no audio this destination may
// have — but it will not Validate, so builders should check SelectedTracks and
// report it rather than saving a silent destination.
func ApplyMusicPolicy(p Profile, src Source, plat Platform, choice MusicPolicyChoice) (Profile, MusicDecision) {
	d := ResolveMusicPolicy(plat, choice)
	out := cloneForPolicy(p)

	if !d.Exclude {
		if choice == MusicPolicyKeep {
			out.ExcludeRoles = withoutRole(out.ExcludeRoles, RoleMusic)
		}
		return out, d
	}

	out.ExcludeRoles = withRole(out.ExcludeRoles, RoleMusic)
	return withoutTracks(out, src.TracksWithRole(RoleMusic)), d
}

// cloneForPolicy copies every slice a policy might touch, so that applying one
// can never reach back into the caller's saved profile.
func cloneForPolicy(p Profile) Profile {
	p.Tracks = append([]TrackSel(nil), p.Tracks...)
	p.Matrix = append([]Cell(nil), p.Matrix...)
	p.ExcludeRoles = append([]TrackRole(nil), p.ExcludeRoles...)
	return p
}

// withoutTracks drops source tracks from the mix in whichever mode the profile
// is in. It mutates p's slices, so callers pass a clone.
func withoutTracks(p Profile, drop []int) Profile {
	if len(drop) == 0 {
		return p
	}
	dropped := make(map[int]bool, len(drop))
	for _, d := range drop {
		dropped[d] = true
	}

	switch p.Mode {
	case ModeMatrix:
		cells := make([]Cell, 0, len(p.Matrix))
		for _, c := range p.Matrix {
			if !dropped[c.Track] {
				cells = append(cells, c)
			}
		}
		p.Matrix = cells
	default:
		for i := range p.Tracks {
			if dropped[p.Tracks[i].Track] {
				p.Tracks[i].Enabled = false
			}
		}
	}
	return p
}

func withRole(roles []TrackRole, r TrackRole) []TrackRole {
	for _, x := range roles {
		if x == r {
			return roles
		}
	}
	return append(roles, r)
}

func withoutRole(roles []TrackRole, r TrackRole) []TrackRole {
	out := roles[:0]
	for _, x := range roles {
		if x != r {
			out = append(out, x)
		}
	}
	if len(out) == 0 {
		// nil rather than an empty slice, so a profile that ends up with no
		// policy marshals identically to one that never had one.
		return nil
	}
	return out
}
