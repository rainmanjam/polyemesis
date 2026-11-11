package routing

import (
	"slices"
	"sort"
)

// PresetOptsFromRoles is the options a preset is applied with when the caller
// names none: the catalogue's defaults, with each picker moved onto the track
// the operator has annotated with its role.
//
// DefaultPresetOpts are generic OBS-convention slots (music 0, mic 2, clean 1)
// that know nothing about this ingest. The console has never applied a preset
// with them as they stand: ui/src/features/routing/presetTrackDefaults.ts presetOptsFromRoles
// opens each picker on the annotated track, so on a programme whose music sits
// on track 3 "No music" drops track 3. A client with no pickers -- a Stream Deck
// key, an agent -- sent no body and got track 0 dropped instead. This is that
// same derivation, so a bodyless apply means what the console would have sent.
//
// The rules are the console's, kept in step by TestPresetOptsFromRolesMatchTheConsole:
//
//   - musicTrack follows "music"; micTrack follows "mic", then "commentary";
//     cleanTrack follows "clean". Surround has no role and keeps its default.
//   - The lowest annotated index wins a tie.
//   - An annotation on a track the ingest does not carry is ignored. Before the
//     ingest is measured the placeholder layout stands in, as it does for
//     ApplyPreset itself.
//
// Language and MusicPolicy are not track picks and keep base's values.
func PresetOptsFromRoles(base PresetOpts, src Source) PresetOpts {
	tracks := src.Tracks
	if len(tracks) == 0 {
		tracks = DefaultSource().Tracks
	}
	present := make(map[int]bool, len(tracks))
	for _, t := range tracks {
		present[t.Index] = true
	}
	first := func(roles ...TrackRole) (int, bool) {
		return firstAnnotatedTrack(src, present, roles...)
	}
	out := base
	if i, ok := first(RoleMusic); ok {
		out.MusicTrack = i
	}
	if i, ok := first(RoleMic, RoleCommentary); ok {
		out.MicTrack = i
	}
	if i, ok := first(RoleClean); ok {
		out.CleanTrack = i
	}
	return out
}

// firstAnnotatedTrack returns the lowest track index annotated with the first
// role in roles that any present track carries, if there is one.
func firstAnnotatedTrack(src Source, present map[int]bool, roles ...TrackRole) (int, bool) {
	for _, role := range roles {
		var hits []int
		for _, a := range src.Annotations {
			if a.Role == role && present[a.Track] {
				hits = append(hits, a.Track)
			}
		}
		if len(hits) > 0 {
			sort.Ints(hits)
			return hits[0], true
		}
	}
	return 0, false
}

// DefaultPresetOptsFor is the options a preset is applied with when the caller
// names none, for this ingest: the one answer the compile route, apply, the OBS
// scene bridge and GET /routing/presets all use, so a preset previewed is the
// preset applied.
func DefaultPresetOptsFor(src Source) PresetOpts {
	return PresetOptsFromRoles(DefaultPresetOpts(), src)
}

// Kept is what KeepOperatorDecisions carried from the stored mix into a preset:
// the tracks that stay muted and the roles that stay excluded. Both nil when
// nothing was carried.
type Kept struct {
	Muted    []int       `json:"muted,omitempty"`
	Excluded []TrackRole `json:"excluded,omitempty"`
}

// KeepOperatorDecisions returns the preset's profile with the two decisions an
// operator made on the stored mix carried over, and says which it carried.
//
// A preset replaces a whole mix and arrives with every track unmuted and no
// role excluded. Two things in the stored mix are not part of a mix's shape but
// a decision about what may be heard, and a preset must not undo either of them
// as a side effect:
//
//   - A MUTE. The console keeps it on the live mix (ui/src/lib/liveTrackLevel.ts
//     carryMutes), and the MCP tool kept it on both mixes, because the live mute
//     controls mirror into the VOD mix (mirrorTrackLevel): a hot mic muted live
//     is muted on the VOD track too. Only a track present in both keeps it.
//   - A ROLE EXCLUSION, the DMCA switch (Profile.ExcludeRoles). The stored roles
//     are added to whatever the preset excludes, never removed: the rule
//     ApplyMusicPolicy already follows, that nothing quietly undoes a music
//     exclusion somebody made. The console's preset review shows an exclusion
//     leaving and lets the operator back out; a held Stream Deck key or an
//     agent has no such step, so here it is kept. Removing a guard is an edit
//     in the routing editor, where it is seen.
//
// The result shares no slice or pointer with either input, so one preset can be
// carried into many destinations and each result edited on its own.
func KeepOperatorDecisions(next Profile, stored *Profile) (Profile, Kept) {
	var kept Kept
	next = cloneProfile(next)
	if stored == nil {
		return next, kept
	}
	muted := make(map[int]bool)
	for _, s := range stored.Tracks {
		if s.Muted {
			muted[s.Track] = true
		}
	}
	for i := range next.Tracks {
		if muted[next.Tracks[i].Track] {
			if !next.Tracks[i].Muted {
				kept.Muted = append(kept.Muted, next.Tracks[i].Track)
			}
			next.Tracks[i].Muted = true
		}
	}
	for _, r := range stored.ExcludeRoles {
		before := len(next.ExcludeRoles)
		next.ExcludeRoles = withRole(next.ExcludeRoles, r)
		if len(next.ExcludeRoles) > before {
			kept.Excluded = append(kept.Excluded, r)
		}
	}
	if len(next.ExcludeRoles) == 0 {
		// nil rather than empty, so a mix that excludes nothing marshals as one
		// that never did (withoutRole's rule).
		next.ExcludeRoles = nil
	}
	sort.Ints(kept.Muted)
	return next, kept
}

// cloneProfile copies every slice and pointer a Profile holds.
func cloneProfile(p Profile) Profile {
	// slices.Clone keeps nil as nil and empty as empty, so the stored JSON
	// ("matrix": null or []) is the preset's own.
	p.Tracks = slices.Clone(p.Tracks)
	p.Matrix = slices.Clone(p.Matrix)
	p.ExcludeRoles = slices.Clone(p.ExcludeRoles)
	if p.Loudness != nil {
		l := *p.Loudness
		p.Loudness = &l
	}
	if p.Ducking != nil {
		d := *p.Ducking
		d.Trigger = slices.Clone(d.Trigger)
		d.Target = slices.Clone(d.Target)
		p.Ducking = &d
	}
	return p
}
