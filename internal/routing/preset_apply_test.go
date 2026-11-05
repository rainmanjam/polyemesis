package routing

import "testing"

// The same cases as ui/src/lib/presetTrackDefaults.test.ts presetOptsFromRoles,
// less the operator's hand picks (a bodyless apply has none: a body replaces
// the options outright). If the console's rule moves, this is the twin that has
// to move with it.
//
// MUTATION: drop the present[a.Track] check. Observed: FAIL, "a role on a track
// the ingest does not carry" picked track 7.
// MUTATION: try "commentary" before "mic". Observed: FAIL, the tie case picked 1.
func TestPresetOptsFromRolesMatchTheConsole(t *testing.T) {
	four := Source{Tracks: []Track{{Index: 0}, {Index: 1}, {Index: 2}, {Index: 3}}}
	with := func(src Source, anns ...TrackAnnotation) Source {
		src.Annotations = anns
		return src
	}
	base := DefaultPresetOpts()

	// scripts/demo-seed.fixture.json, Studio A: music is index 2.
	studioA := with(four,
		TrackAnnotation{Track: 0, Role: RoleMic},
		TrackAnnotation{Track: 1, Role: RoleCommentary},
		TrackAnnotation{Track: 2, Role: RoleMusic},
		TrackAnnotation{Track: 3, Role: RoleOther},
	)
	cases := []struct {
		name string
		src  Source
		want PresetOpts
	}{
		{"music follows the music role", studioA,
			PresetOpts{MusicTrack: 2, MicTrack: 0, SurroundTrack: 0, CleanTrack: 1}},
		{"mic and clean follow their roles", with(four,
			TrackAnnotation{Track: 3, Role: RoleMic}, TrackAnnotation{Track: 1, Role: RoleClean}),
			PresetOpts{MusicTrack: 0, MicTrack: 3, SurroundTrack: 0, CleanTrack: 1}},
		{"commentary stands in for a missing mic", with(four, TrackAnnotation{Track: 3, Role: RoleCommentary}),
			PresetOpts{MusicTrack: 0, MicTrack: 3, SurroundTrack: 0, CleanTrack: 1}},
		{"a mic beats commentary, and the lowest index wins a tie", with(four,
			TrackAnnotation{Track: 1, Role: RoleCommentary},
			TrackAnnotation{Track: 3, Role: RoleMic},
			TrackAnnotation{Track: 2, Role: RoleMic}),
			PresetOpts{MusicTrack: 0, MicTrack: 2, SurroundTrack: 0, CleanTrack: 1}},
		{"no roles keeps the defaults", four, base},
		{"a role on a track the ingest does not carry is ignored", with(four, TrackAnnotation{Track: 7, Role: RoleMusic}), base},
		{"an unmeasured ingest reads the placeholder layout", with(Source{}, TrackAnnotation{Track: 4, Role: RoleMusic}),
			PresetOpts{MusicTrack: 4, MicTrack: 2, SurroundTrack: 0, CleanTrack: 1}},
	}
	for _, c := range cases {
		if got := PresetOptsFromRoles(base, c.src); got != c.want {
			t.Errorf("%s: got %+v, want %+v", c.name, got, c.want)
		}
	}

	// Not track picks: carried from base untouched.
	b := base
	b.Language, b.MusicPolicy = "es", MusicPolicyKeep
	if got := PresetOptsFromRoles(b, studioA); got.Language != "es" || got.MusicPolicy != MusicPolicyKeep {
		t.Errorf("language and music policy were not carried: %+v", got)
	}
}

// A preset keeps the stored mix's mutes and role exclusions, says which, and
// touches neither input.
//
// MUTATION: skip the ExcludeRoles carry. Observed: FAIL, the music guard was
// dropped by the "everything" preset.
// MUTATION: carry a mute onto a track the stored mix did not mute (mark every
// track). Observed: FAIL, track 1 came back muted.
func TestAPresetKeepsTheMixesMutesAndGuards(t *testing.T) {
	src := Source{Tracks: []Track{{Index: 0, Channels: 2}, {Index: 1, Channels: 2}, {Index: 2, Channels: 2}}}
	preset, err := ApplyPreset(PresetEverything, src, DefaultPresetOpts())
	if err != nil {
		t.Fatal(err)
	}
	stored := Profile{
		Mode: ModeSimple, Normalize: NormAuto, SampleRate: 48000,
		Tracks: []TrackSel{
			{Track: 0, Enabled: true, Gain: 1},
			{Track: 2, Enabled: true, Gain: 0.5, Muted: true},
			{Track: 5, Enabled: false, Gain: 1, Muted: true}, // a row the preset carries but does not enable
		},
		ExcludeRoles: []TrackRole{RoleMusic},
	}
	presetTracks := append([]TrackSel(nil), preset.Tracks...)

	got, kept := KeepOperatorDecisions(preset, &stored)

	for _, s := range got.Tracks {
		want := s.Track == 2 || s.Track == 5
		if s.Muted != want {
			t.Errorf("track %d muted = %v, want %v", s.Track, s.Muted, want)
		}
		if s.Track == 2 && s.Gain != 1 {
			t.Errorf("the stored gain travelled with the mute: %v", s.Gain)
		}
	}
	if len(got.ExcludeRoles) != 1 || got.ExcludeRoles[0] != RoleMusic {
		t.Errorf("the music guard was not kept: %v", got.ExcludeRoles)
	}
	if want := []int{2, 5}; len(kept.Muted) != 2 || kept.Muted[0] != want[0] || kept.Muted[1] != want[1] {
		t.Errorf("kept.Muted = %v, want %v", kept.Muted, want)
	}
	if len(kept.Excluded) != 1 || kept.Excluded[0] != RoleMusic {
		t.Errorf("kept.Excluded = %v, want [music]", kept.Excluded)
	}
	for i := range presetTracks {
		if preset.Tracks[i] != presetTracks[i] {
			t.Fatal("KeepOperatorDecisions wrote into the preset's own track rows")
		}
	}
	if preset.ExcludeRoles != nil {
		t.Fatal("KeepOperatorDecisions wrote into the preset's own exclusions")
	}

	// A preset that already excludes the role keeps one copy and reports nothing
	// kept; no stored mix carries nothing.
	except, err := ApplyPreset(PresetExceptMusic, src, DefaultPresetOpts())
	if err != nil {
		t.Fatal(err)
	}
	got, kept = KeepOperatorDecisions(except, &Profile{ExcludeRoles: []TrackRole{RoleMusic}})
	if len(got.ExcludeRoles) != 1 || kept.Excluded != nil {
		t.Errorf("an exclusion the preset already makes: roles %v, kept %v", got.ExcludeRoles, kept.Excluded)
	}
	if got, kept = KeepOperatorDecisions(preset, nil); kept.Muted != nil || kept.Excluded != nil || got.ExcludeRoles != nil {
		t.Errorf("no stored mix carried %+v", kept)
	}
}
