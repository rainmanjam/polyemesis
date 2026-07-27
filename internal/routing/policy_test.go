package routing

import (
	"reflect"
	"sort"
	"strings"
	"testing"
)

// annotated builds a source of n stereo tracks carrying the given annotations.
func annotated(n int, anns ...TrackAnnotation) Source {
	return stereoSource(n).WithAnnotations(anns)
}

func roled(track int, r TrackRole) TrackAnnotation {
	return TrackAnnotation{Track: track, Role: r}
}

func spoken(track int, r TrackRole, lang string) TrackAnnotation {
	return TrackAnnotation{Track: track, Role: r, Language: lang}
}

// --------------------------------------------------------------- the table

func TestPlatformPolicyTable(t *testing.T) {
	tests := []struct {
		name         string
		platform     Platform
		wantExclude  bool
		wantReason   string
		wantLUFS     float64
		wantSummary  string
		wantPlatform Platform
	}{
		{
			name: "youtube excludes music for Content ID", platform: PlatformYouTube,
			wantExclude: true, wantReason: "YouTube Content ID", wantLUFS: LUFSStreaming,
			wantSummary: "music excluded (YouTube Content ID)", wantPlatform: PlatformYouTube,
		},
		{
			name: "twitch excludes music for DMCA", platform: PlatformTwitch,
			wantExclude: true, wantReason: "Twitch DMCA policy", wantLUFS: LUFSStreaming,
			wantSummary: "music excluded (Twitch DMCA policy)", wantPlatform: PlatformTwitch,
		},
		{
			name: "kick excludes music for DMCA", platform: PlatformKick,
			wantExclude: true, wantReason: "Kick DMCA policy", wantLUFS: LUFSStreaming,
			wantSummary: "music excluded (Kick DMCA policy)", wantPlatform: PlatformKick,
		},
		{
			name: "facebook excludes music for Rights Manager", platform: PlatformFacebook,
			wantExclude: true, wantReason: "Facebook Rights Manager", wantLUFS: LUFSStreaming,
			wantSummary: "music excluded (Facebook Rights Manager)", wantPlatform: PlatformFacebook,
		},
		{
			name: "a local recording keeps everything", platform: PlatformFile,
			wantExclude: false, wantReason: "local recording", wantLUFS: 0,
			wantSummary: "music included (local recording)", wantPlatform: PlatformFile,
		},
		{
			name: "a custom destination keeps everything", platform: PlatformCustom,
			wantExclude: false, wantReason: "custom destination", wantLUFS: 0,
			wantSummary: "music included (custom destination)", wantPlatform: PlatformCustom,
		},
		{
			name: "an unknown platform fails open and keeps everything", platform: Platform("mixer"),
			wantExclude: false, wantReason: "custom destination", wantLUFS: 0,
			wantSummary: "music included (custom destination)", wantPlatform: PlatformCustom,
		},
		{
			name: "an empty platform fails open and keeps everything", platform: Platform(""),
			wantExclude: false, wantReason: "custom destination", wantLUFS: 0,
			wantSummary: "music included (custom destination)", wantPlatform: PlatformCustom,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pol := PolicyFor(tc.platform)
			if pol.ExcludeMusic != tc.wantExclude {
				t.Errorf("ExcludeMusic = %v, want %v", pol.ExcludeMusic, tc.wantExclude)
			}
			if pol.Reason != tc.wantReason {
				t.Errorf("Reason = %q, want %q", pol.Reason, tc.wantReason)
			}
			if pol.TargetLUFS != tc.wantLUFS {
				t.Errorf("TargetLUFS = %v, want %v", pol.TargetLUFS, tc.wantLUFS)
			}
			if pol.Name == "" {
				t.Error("policy has no display name")
			}

			dec := ResolveMusicPolicy(tc.platform, MusicPolicyDefault)
			if dec.Summary != tc.wantSummary {
				t.Errorf("Summary = %q, want %q", dec.Summary, tc.wantSummary)
			}
			if dec.Exclude != tc.wantExclude {
				t.Errorf("decision Exclude = %v, want %v", dec.Exclude, tc.wantExclude)
			}
			if dec.Overridden {
				t.Error("a decision straight off the table must not claim to be an override")
			}
			if dec.Platform != tc.wantPlatform {
				t.Errorf("decision Platform = %q, want %q", dec.Platform, tc.wantPlatform)
			}
		})
	}
}

func TestPlatformPoliciesCatalogue(t *testing.T) {
	pols := PlatformPolicies()

	t.Run("every platform constant has a row", func(t *testing.T) {
		want := []Platform{
			PlatformYouTube, PlatformTwitch, PlatformKick,
			PlatformFacebook, PlatformFile, PlatformCustom,
		}
		for _, p := range want {
			found := false
			for _, pol := range pols {
				if pol.Platform == p {
					found = true
				}
			}
			if !found {
				t.Errorf("platform %q has no policy row", p)
			}
		}
		if len(pols) != len(want) {
			t.Errorf("catalogue has %d rows, want %d", len(pols), len(want))
		}
	})

	t.Run("every row is complete", func(t *testing.T) {
		for _, pol := range pols {
			if pol.Name == "" || pol.Reason == "" {
				t.Errorf("%q: incomplete row %+v", pol.Platform, pol)
			}
		}
	})

	t.Run("the catalogue is a copy the caller cannot corrupt", func(t *testing.T) {
		pols[0].ExcludeMusic = !pols[0].ExcludeMusic
		pols[0].Reason = "tampered"
		if again := PlatformPolicies(); again[0].Reason == "tampered" {
			t.Fatal("PlatformPolicies handed out the package's own table")
		}
	})
}

func TestPlatformFor(t *testing.T) {
	tests := []struct {
		name           string
		platform, kind string
		want           Platform
	}{
		{"a stored platform maps straight through", "twitch", "rtmp", PlatformTwitch},
		{"case and padding do not matter", "  YouTube ", "rtmp", PlatformYouTube},
		{"a file destination is a local recording whatever its branding", "youtube", "file", PlatformFile},
		{"kind wins even for an unknown platform", "somewhere", "FILE", PlatformFile},
		{"an SRT destination keeps its platform", "kick", "srt", PlatformKick},
		{"an unknown platform falls open to custom", "mixer", "rtmp", PlatformCustom},
		{"an empty platform falls open to custom", "", "rtmp", PlatformCustom},
		{"an empty kind is not a file", "twitch", "", PlatformTwitch},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := PlatformFor(tc.platform, tc.kind); got != tc.want {
				t.Errorf("PlatformFor(%q, %q) = %q, want %q", tc.platform, tc.kind, got, tc.want)
			}
		})
	}
}

// -------------------------------------------------------------- overriding

func TestMusicPolicyOverridesEveryTableEntry(t *testing.T) {
	for _, pol := range PlatformPolicies() {
		t.Run(string(pol.Platform)+" can be overridden to keep music", func(t *testing.T) {
			dec := ResolveMusicPolicy(pol.Platform, MusicPolicyKeep)
			if dec.Exclude {
				t.Error("an explicit keep still excluded music")
			}
			if !dec.Overridden {
				t.Error("override was not recorded")
			}
			if dec.Summary != "music included (operator override)" {
				t.Errorf("Summary = %q", dec.Summary)
			}
		})

		t.Run(string(pol.Platform)+" can be overridden to exclude music", func(t *testing.T) {
			dec := ResolveMusicPolicy(pol.Platform, MusicPolicyExclude)
			if !dec.Exclude {
				t.Error("an explicit exclude still carried music")
			}
			if !dec.Overridden {
				t.Error("override was not recorded")
			}
			if dec.Summary != "music excluded (operator override)" {
				t.Errorf("Summary = %q", dec.Summary)
			}
		})
	}
}

func TestUnknownChoiceFallsBackToTheTableRatherThanDroppingAudio(t *testing.T) {
	// A choice string polyemesis cannot parse arrives from JSON. Treating it as
	// "exclude" would silently delete audio on the strength of a typo.
	got := ResolveMusicPolicy(PlatformFile, MusicPolicyChoice("maybe"))
	want := ResolveMusicPolicy(PlatformFile, MusicPolicyDefault)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("garbage choice = %+v, want the table's answer %+v", got, want)
	}
	if got.Exclude {
		t.Error("an unparseable choice excluded music on a local recording")
	}
}

func TestValidMusicPolicyChoice(t *testing.T) {
	tests := []struct {
		in   MusicPolicyChoice
		want bool
	}{
		{MusicPolicyDefault, true},
		{MusicPolicyKeep, true},
		{MusicPolicyExclude, true},
		{MusicPolicyChoice("drop"), false},
		{MusicPolicyChoice("KEEP"), false},
	}
	for _, tc := range tests {
		if got := ValidMusicPolicyChoice(tc.in); got != tc.want {
			t.Errorf("ValidMusicPolicyChoice(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
	if len(MusicPolicyChoices()) != 3 {
		t.Errorf("MusicPolicyChoices() = %v, want three", MusicPolicyChoices())
	}
}

func TestMusicDecisionArmedAndWarning(t *testing.T) {
	withMusic := annotated(3, roled(1, RoleMusic))
	withoutMusic := annotated(3, roled(1, RoleMic))

	tests := []struct {
		name        string
		platform    Platform
		src         Source
		wantArmed   bool
		wantWarning bool
	}{
		{"an exclusion with a music track to bite on is armed", PlatformTwitch, withMusic, true, false},
		{"an exclusion with nothing marked as music warns", PlatformTwitch, withoutMusic, false, true},
		{"an exclusion on an un-annotated ingest warns", PlatformTwitch, stereoSource(3), false, true},
		{"keeping music is never armed and never warns", PlatformFile, withMusic, false, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dec := ResolveMusicPolicy(tc.platform, MusicPolicyDefault)
			if got := dec.Armed(tc.src); got != tc.wantArmed {
				t.Errorf("Armed = %v, want %v", got, tc.wantArmed)
			}
			if got := dec.Warning(tc.src) != ""; got != tc.wantWarning {
				t.Errorf("Warning(%q) presence = %v, want %v", dec.Warning(tc.src), got, tc.wantWarning)
			}
		})
	}
}

// ------------------------------------------------------------- application

func TestApplyMusicPolicy(t *testing.T) {
	// Track 1 is the music; 0, 2 and 3 are not.
	src := annotated(4, roled(0, RoleGame), roled(1, RoleMusic), roled(2, RoleMic))

	tests := []struct {
		name         string
		platform     Platform
		choice       MusicPolicyChoice
		src          Source
		wantSelected []int
		wantExcluded []TrackRole
	}{
		{
			name: "twitch drops the annotated music track", platform: PlatformTwitch, src: src,
			wantSelected: []int{0, 2, 3}, wantExcluded: []TrackRole{RoleMusic},
		},
		{
			name: "youtube drops the annotated music track", platform: PlatformYouTube, src: src,
			wantSelected: []int{0, 2, 3}, wantExcluded: []TrackRole{RoleMusic},
		},
		{
			name: "kick drops the annotated music track", platform: PlatformKick, src: src,
			wantSelected: []int{0, 2, 3}, wantExcluded: []TrackRole{RoleMusic},
		},
		{
			name: "facebook drops the annotated music track", platform: PlatformFacebook, src: src,
			wantSelected: []int{0, 2, 3}, wantExcluded: []TrackRole{RoleMusic},
		},
		{
			name: "a local recording keeps the music", platform: PlatformFile, src: src,
			wantSelected: []int{0, 1, 2, 3}, wantExcluded: nil,
		},
		{
			name: "a custom destination keeps the music", platform: PlatformCustom, src: src,
			wantSelected: []int{0, 1, 2, 3}, wantExcluded: nil,
		},
		{
			name: "an unknown platform keeps the music", platform: Platform("mixer"), src: src,
			wantSelected: []int{0, 1, 2, 3}, wantExcluded: nil,
		},
		{
			name:     "an explicit keep sends music to Twitch anyway",
			platform: PlatformTwitch, choice: MusicPolicyKeep, src: src,
			wantSelected: []int{0, 1, 2, 3}, wantExcluded: nil,
		},
		{
			name:     "an explicit exclude keeps music out of a local recording",
			platform: PlatformFile, choice: MusicPolicyExclude, src: src,
			wantSelected: []int{0, 2, 3}, wantExcluded: []TrackRole{RoleMusic},
		},
		{
			name: "an un-annotated ingest loses nothing but still gets the standing rule",
			// This is the fail-open case that matters most: nobody said which
			// track is music, so nothing may be dropped.
			platform: PlatformTwitch, src: stereoSource(4),
			wantSelected: []int{0, 1, 2, 3}, wantExcluded: []TrackRole{RoleMusic},
		},
		{
			name:     "an ingest annotated with no music at all loses nothing",
			platform: PlatformTwitch, src: annotated(4, roled(0, RoleGame), roled(2, RoleMic)),
			wantSelected: []int{0, 1, 2, 3}, wantExcluded: []TrackRole{RoleMusic},
		},
		{
			name:         "every music track goes, not just the first",
			platform:     PlatformTwitch,
			src:          annotated(4, roled(1, RoleMusic), roled(3, RoleMusic)),
			wantSelected: []int{0, 2}, wantExcluded: []TrackRole{RoleMusic},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in, err := ApplyPreset(PresetEverything, tc.src, DefaultPresetOpts())
			if err != nil {
				t.Fatal(err)
			}

			out, dec := ApplyMusicPolicy(in, tc.src, tc.platform, tc.choice)
			if got := out.SelectedTracks(); !reflect.DeepEqual(got, tc.wantSelected) {
				t.Errorf("selected = %v, want %v", got, tc.wantSelected)
			}
			if got := out.ExcludeRoles; !reflect.DeepEqual(got, tc.wantExcluded) {
				t.Errorf("excludeRoles = %v, want %v", got, tc.wantExcluded)
			}
			if dec.Summary == "" {
				t.Error("decision carries no badge text")
			}
			if err := out.Validate(); err != nil {
				t.Errorf("policy produced an invalid profile: %v", err)
			}
		})
	}
}

func TestApplyMusicPolicyNeverMutatesTheCallersProfile(t *testing.T) {
	src := annotated(3, roled(1, RoleMusic))
	in, err := ApplyPreset(PresetEverything, src, DefaultPresetOpts())
	if err != nil {
		t.Fatal(err)
	}
	before := append([]TrackSel(nil), in.Tracks...)

	out, _ := ApplyMusicPolicy(in, src, PlatformTwitch, MusicPolicyDefault)

	if !reflect.DeepEqual(in.Tracks, before) {
		t.Errorf("caller's tracks were mutated: %v", in.Tracks)
	}
	if len(in.ExcludeRoles) != 0 {
		t.Errorf("caller's excludeRoles were mutated: %v", in.ExcludeRoles)
	}
	if got := out.SelectedTracks(); !reflect.DeepEqual(got, []int{0, 2}) {
		t.Errorf("selected = %v, want [0 2]", got)
	}
}

func TestApplyMusicPolicyOnAMatrixProfile(t *testing.T) {
	src := annotated(2, roled(1, RoleMusic))
	in := Profile{Mode: ModeMatrix, Normalize: NormAuto, SampleRate: 48000, Matrix: []Cell{
		{Track: 0, Channel: 0, Out: OutL, Gain: 1},
		{Track: 0, Channel: 1, Out: OutR, Gain: 1},
		{Track: 1, Channel: 0, Out: OutL, Gain: 1},
		{Track: 1, Channel: 1, Out: OutR, Gain: 1},
	}}

	out, _ := ApplyMusicPolicy(in, src, PlatformTwitch, MusicPolicyDefault)
	if got := out.SelectedTracks(); !reflect.DeepEqual(got, []int{0}) {
		t.Errorf("selected = %v, want [0]", got)
	}
	if len(in.Matrix) != 4 {
		t.Errorf("caller's matrix was mutated: %v", in.Matrix)
	}
	if err := out.Validate(); err != nil {
		t.Errorf("matrix policy produced an invalid profile: %v", err)
	}
}

func TestPolicyOnlyEverAddsAnExclusionUnlessOverruled(t *testing.T) {
	// Moving a destination from Twitch to a local file must not silently undo
	// the operator's music exclusion; only an explicit keep does that.
	src := annotated(3, roled(1, RoleMusic))
	protected, _ := ApplyMusicPolicy(
		mustPreset(t, PresetEverything, src), src, PlatformTwitch, MusicPolicyDefault)

	t.Run("a permissive platform leaves an existing exclusion alone", func(t *testing.T) {
		out, _ := ApplyMusicPolicy(protected, src, PlatformFile, MusicPolicyDefault)
		if !out.ExcludesRole(RoleMusic) {
			t.Error("switching to a local recording quietly dropped the exclusion")
		}
		if got := out.SelectedTracks(); !reflect.DeepEqual(got, []int{0, 2}) {
			t.Errorf("selected = %v, want [0 2]", got)
		}
	})

	t.Run("an explicit keep removes it", func(t *testing.T) {
		out, _ := ApplyMusicPolicy(protected, src, PlatformFile, MusicPolicyKeep)
		if out.ExcludesRole(RoleMusic) {
			t.Error("an explicit keep did not remove the exclusion")
		}
		if len(out.ExcludeRoles) != 0 {
			t.Errorf("excludeRoles = %v, want empty", out.ExcludeRoles)
		}
	})

	t.Run("an explicit keep leaves unrelated exclusions alone", func(t *testing.T) {
		p := protected
		p.ExcludeRoles = []TrackRole{RoleMusic, RoleGame}
		out, _ := ApplyMusicPolicy(p, src, PlatformFile, MusicPolicyKeep)
		if !reflect.DeepEqual(out.ExcludeRoles, []TrackRole{RoleGame}) {
			t.Errorf("excludeRoles = %v, want [game]", out.ExcludeRoles)
		}
	})

	t.Run("applying the same policy twice is idempotent", func(t *testing.T) {
		once, _ := ApplyMusicPolicy(protected, src, PlatformTwitch, MusicPolicyDefault)
		twice, _ := ApplyMusicPolicy(once, src, PlatformTwitch, MusicPolicyDefault)
		if !reflect.DeepEqual(once.ExcludeRoles, twice.ExcludeRoles) {
			t.Errorf("excludeRoles drifted: %v then %v", once.ExcludeRoles, twice.ExcludeRoles)
		}
		if !reflect.DeepEqual(once.SelectedTracks(), twice.SelectedTracks()) {
			t.Error("selection drifted on reapplication")
		}
	})
}

func TestApplyMusicPolicyCanLeaveNothingSelected(t *testing.T) {
	// The honest answer when the only track is music: there is no audio this
	// destination may carry. The profile says so rather than pretending, and
	// builders check for it — see the preset test below.
	src := annotated(1, roled(0, RoleMusic))
	out, _ := ApplyMusicPolicy(mustPreset(t, PresetEverything, src), src, PlatformTwitch, MusicPolicyDefault)
	if got := out.SelectedTracks(); len(got) != 0 {
		t.Errorf("selected = %v, want nothing", got)
	}
}

// ------------------------------------------------------------- the presets

func mustPreset(t *testing.T, id string, src Source) Profile {
	t.Helper()
	p, err := ApplyPreset(id, src, DefaultPresetOpts())
	if err != nil {
		t.Fatalf("ApplyPreset(%q): %v", id, err)
	}
	return p
}

func TestLanguagePresetsFallBackWhenTheIngestCarriesNoRoles(t *testing.T) {
	// The whole point: an ingest nobody has annotated must still get an
	// audible, index-based mix out of every role-aware preset.
	bare := stereoSource(4)
	opts := DefaultPresetOpts() // music 0, clean 1, mic 2

	tests := []struct {
		name string
		id   string
		opts PresetOpts
		want []int
	}{
		{"mic-only falls back to the mic picker", PresetMicOnly, opts, []int{2}},
		{"commentary falls back to the mic picker", PresetCommentary, opts, []int{2}},
		{
			"commentary with a language nobody tagged still falls back",
			PresetCommentary, PresetOpts{MicTrack: 2, Language: "es"}, []int{2},
		},
		{"clean feed falls back to the clean picker", PresetCleanFeed, opts, []int{1}},
		{"except-music falls back to the music picker", PresetExceptMusic, opts, []int{1, 2, 3}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p, err := ApplyPreset(tc.id, bare, tc.opts)
			if err != nil {
				t.Fatalf("un-annotated ingest must not error: %v", err)
			}
			if got := p.SelectedTracks(); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("selected = %v, want %v", got, tc.want)
			}
			if err := p.Validate(); err != nil {
				t.Errorf("invalid profile: %v", err)
			}
			if _, err := Compile(p, bare); err != nil {
				t.Errorf("does not compile: %v", err)
			}
		})
	}
}

func TestLanguagePresetsSelectByRoleAndLanguage(t *testing.T) {
	// 0 game, 1 English commentary, 2 Spanish commentary, 3 mic, 4 music.
	multilingual := annotated(5,
		roled(0, RoleGame),
		spoken(1, RoleCommentary, "en"),
		spoken(2, RoleCommentary, "es-419"),
		roled(3, RoleMic),
		roled(4, RoleMusic),
	)

	tests := []struct {
		name string
		id   string
		src  Source
		opts PresetOpts
		want []int
	}{
		{
			name: "commentary in Spanish picks the Spanish commentary",
			id:   PresetCommentary, src: multilingual,
			opts: PresetOpts{MicTrack: 3, Language: "es"}, want: []int{2},
		},
		{
			name: "commentary in English picks the English commentary",
			id:   PresetCommentary, src: multilingual,
			opts: PresetOpts{MicTrack: 3, Language: "en"}, want: []int{1},
		},
		{
			name: "a regional tag finds the track labelled only with the primary subtag",
			id:   PresetCommentary,
			src:  annotated(3, spoken(1, RoleCommentary, "es"), roled(2, RoleMic)),
			opts: PresetOpts{MicTrack: 2, Language: "es-MX"}, want: []int{1},
		},
		{
			name: "no language asked for takes every commentary track",
			id:   PresetCommentary, src: multilingual,
			opts: PresetOpts{MicTrack: 3}, want: []int{1, 2},
		},
		{
			name: "a language nobody tagged degrades to the commentary that exists",
			id:   PresetCommentary, src: multilingual,
			opts: PresetOpts{MicTrack: 3, Language: "pt-BR"}, want: []int{1, 2},
		},
		{
			name: "a language on a track nobody roled is still that language",
			id:   PresetCommentary,
			src:  annotated(3, TrackAnnotation{Track: 1, Language: "es"}, roled(2, RoleMic)),
			opts: PresetOpts{MicTrack: 2, Language: "es"}, want: []int{1},
		},
		{
			name: "mic-only prefers the roled mic over the picker",
			id:   PresetMicOnly, src: multilingual,
			opts: PresetOpts{MicTrack: 0}, want: []int{3},
		},
		{
			name: "clean feed uses an explicitly roled clean track",
			id:   PresetCleanFeed,
			src:  annotated(4, roled(0, RoleGame), roled(1, RoleMic), roled(2, RoleClean)),
			opts: PresetOpts{CleanTrack: 0}, want: []int{2},
		},
		{
			name: "clean feed subtracts the voices when nothing is roled clean",
			id:   PresetCleanFeed, src: multilingual,
			opts: PresetOpts{CleanTrack: 0}, want: []int{0, 4},
		},
		{
			name: "except-music drops every music track",
			id:   PresetExceptMusic,
			src:  annotated(5, roled(1, RoleMusic), roled(4, RoleMusic)),
			opts: DefaultPresetOpts(), want: []int{0, 2, 3},
		},
		{
			name: "except-music ignores the picker once a role says otherwise",
			id:   PresetExceptMusic, src: multilingual,
			opts: PresetOpts{MusicTrack: 0}, want: []int{0, 1, 2, 3},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p, err := ApplyPreset(tc.id, tc.src, tc.opts)
			if err != nil {
				t.Fatal(err)
			}
			if got := p.SelectedTracks(); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("selected = %v, want %v", got, tc.want)
			}
			if err := p.Validate(); err != nil {
				t.Errorf("invalid profile: %v", err)
			}
		})
	}
}

func TestExceptMusicLeavesAStandingInstruction(t *testing.T) {
	t.Run("set even when nothing is annotated yet", func(t *testing.T) {
		// The guarantee has to be written down before it can be acted on: the
		// operator marks the music track later, and the exclusion is already
		// waiting for it.
		p := mustPreset(t, PresetExceptMusic, stereoSource(4))
		if !p.ExcludesRole(RoleMusic) {
			t.Fatal("no standing music exclusion")
		}
		if got := p.ExcludedTracks(stereoSource(4)); len(got) != 0 {
			t.Errorf("excluded %v on an un-annotated ingest", got)
		}
	})

	t.Run("it bites once a track is marked", func(t *testing.T) {
		later := annotated(4, roled(3, RoleMusic))
		p := mustPreset(t, PresetExceptMusic, stereoSource(4))
		if got := p.ExcludedTracks(later); !reflect.DeepEqual(got, []int{3}) {
			t.Errorf("excluded = %v, want [3] once track 3 is marked as music", got)
		}
	})

	t.Run("it refuses to produce silence", func(t *testing.T) {
		only := annotated(1, roled(0, RoleMusic))
		if _, err := ApplyPreset(PresetExceptMusic, only, DefaultPresetOpts()); err == nil {
			t.Fatal("want an error when every track is music")
		}
	})
}

func TestPlatformPresets(t *testing.T) {
	src := annotated(4, roled(0, RoleGame), roled(1, RoleMusic), roled(2, RoleMic))

	tests := []struct {
		name         string
		platform     Platform
		choice       MusicPolicyChoice
		wantSelected []int
		wantLUFS     float64 // 0 means "no loudness target at all"
		wantExcluded bool
	}{
		{"youtube", PlatformYouTube, "", []int{0, 2, 3}, LUFSStreaming, true},
		{"twitch", PlatformTwitch, "", []int{0, 2, 3}, LUFSStreaming, true},
		{"kick", PlatformKick, "", []int{0, 2, 3}, LUFSStreaming, true},
		{"facebook", PlatformFacebook, "", []int{0, 2, 3}, LUFSStreaming, true},
		{"local recording keeps music and sets no loudness target",
			PlatformFile, "", []int{0, 1, 2, 3}, 0, false},
		{"custom keeps music and sets no loudness target",
			PlatformCustom, "", []int{0, 1, 2, 3}, 0, false},
		{"an override sends music to Twitch",
			PlatformTwitch, MusicPolicyKeep, []int{0, 1, 2, 3}, LUFSStreaming, false},
		{"an override keeps music out of the archive",
			PlatformFile, MusicPolicyExclude, []int{0, 2, 3}, 0, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			opts := DefaultPresetOpts()
			opts.MusicPolicy = tc.choice

			p, err := ApplyPreset(PlatformPresetID(tc.platform), src, opts)
			if err != nil {
				t.Fatal(err)
			}
			if got := p.SelectedTracks(); !reflect.DeepEqual(got, tc.wantSelected) {
				t.Errorf("selected = %v, want %v", got, tc.wantSelected)
			}
			if got := p.ExcludesRole(RoleMusic); got != tc.wantExcluded {
				t.Errorf("excludes music = %v, want %v", got, tc.wantExcluded)
			}

			switch {
			case tc.wantLUFS == 0:
				if p.Loudness != nil {
					t.Errorf("loudness = %+v, want none", *p.Loudness)
				}
			case p.Loudness == nil:
				t.Errorf("no loudness target, want %.1f LUFS", tc.wantLUFS)
			case p.Loudness.TargetLUFS != tc.wantLUFS:
				t.Errorf("target = %.1f LUFS, want %.1f", p.Loudness.TargetLUFS, tc.wantLUFS)
			}

			if err := p.Validate(); err != nil {
				t.Errorf("invalid profile: %v", err)
			}
			if _, err := Compile(p, src); err != nil {
				t.Errorf("does not compile: %v", err)
			}
		})
	}
}

func TestPlatformPresetRefusesToProduceSilence(t *testing.T) {
	only := annotated(1, roled(0, RoleMusic))
	_, err := ApplyPreset(PlatformPresetID(PlatformTwitch), only, DefaultPresetOpts())
	if err == nil {
		t.Fatal("want an error when every track the ingest carries is music")
	}
	if !strings.Contains(err.Error(), "Twitch DMCA policy") {
		t.Errorf("error should say why: %v", err)
	}
}

func TestPlatformPresetOnAnUnAnnotatedIngestKeepsEveryTrack(t *testing.T) {
	// Fail open. Twitch's policy is real, but nobody has said which track is
	// music, so there is nothing polyemesis may drop.
	bare := stereoSource(4)
	p := mustPreset(t, PlatformPresetID(PlatformTwitch), bare)
	if got := p.SelectedTracks(); !reflect.DeepEqual(got, []int{0, 1, 2, 3}) {
		t.Errorf("selected = %v, want every track", got)
	}
	if !p.ExcludesRole(RoleMusic) {
		t.Error("the standing rule should still be recorded for later")
	}
}

// ------------------------------------------------------------- catalogue

func TestPresetCatalogue(t *testing.T) {
	presets := Presets()

	t.Run("the original four keep their IDs and their order", func(t *testing.T) {
		want := []string{PresetEverything, PresetNoMusic, PresetMicOnly, PresetSurround}
		for i, id := range want {
			if presets[i].ID != id {
				t.Errorf("preset %d = %q, want %q", i, presets[i].ID, id)
			}
		}
	})

	t.Run("every platform gets a preset built from its policy row", func(t *testing.T) {
		for _, pol := range PlatformPolicies() {
			pre, ok := presetByID(PlatformPresetID(pol.Platform))
			if !ok {
				t.Fatalf("no preset for platform %q", pol.Platform)
			}
			if pre.Platform != pol.Platform {
				t.Errorf("%q: Platform = %q", pre.ID, pre.Platform)
			}
			if pre.Policy.Exclude != pol.ExcludeMusic {
				t.Errorf("%q: preset badge disagrees with the policy table", pre.ID)
			}
			if !strings.Contains(pre.Description, pre.Policy.Summary) {
				t.Errorf("%q: description does not state the policy: %q", pre.ID, pre.Description)
			}
			switch {
			case pol.TargetLUFS == 0 && pre.Loudness != nil:
				t.Errorf("%q: invented a loudness target", pre.ID)
			case pol.TargetLUFS != 0 && (pre.Loudness == nil || pre.Loudness.TargetLUFS != pol.TargetLUFS):
				t.Errorf("%q: loudness does not match the policy row", pre.ID)
			}
		}
	})

	t.Run("the presets that imply nothing carry nothing", func(t *testing.T) {
		for _, id := range []string{
			PresetEverything, PresetNoMusic, PresetMicOnly, PresetSurround,
			PresetCommentary, PresetCleanFeed, PresetExceptMusic,
		} {
			pre, _ := presetByID(id)
			if pre.Loudness != nil || pre.DelayMS != 0 {
				t.Errorf("%q: carries a destination default it should not: %+v", id, pre)
			}
		}
	})

	t.Run("IDs are unique and every one builds", func(t *testing.T) {
		seen := map[string]bool{}
		src := annotated(4, roled(1, RoleMusic), roled(2, RoleMic))
		for _, pre := range presets {
			if seen[pre.ID] {
				t.Errorf("duplicate preset ID %q", pre.ID)
			}
			seen[pre.ID] = true
			if pre.Name == "" || pre.Description == "" {
				t.Errorf("%q: incomplete catalogue entry", pre.ID)
			}
			p, err := ApplyPreset(pre.ID, src, DefaultPresetOpts())
			if err != nil {
				t.Fatalf("%s: %v", pre.ID, err)
			}
			if err := p.Validate(); err != nil {
				t.Fatalf("%s: %v", pre.ID, err)
			}
			if _, err := Compile(p, src); err != nil {
				t.Fatalf("%s: %v", pre.ID, err)
			}
		}
	})

	t.Run("an unknown platform preset ID is an error, not a silent custom", func(t *testing.T) {
		if _, err := ApplyPreset("platform:mixer", stereoSource(2), DefaultPresetOpts()); err == nil {
			t.Fatal("want an error")
		}
		if _, err := ApplyPreset(PresetPlatformPrefix, stereoSource(2), DefaultPresetOpts()); err == nil {
			t.Fatal("want an error for a bare prefix")
		}
	})
}

// TestPresetsThatPredateRolesAreUnchangedByRoleMetadata pins the backwards
// compatibility promise from the other direction: annotating a source must not
// move a preset that never looked at roles.
func TestPresetsThatPredateRolesAreUnchangedByRoleMetadata(t *testing.T) {
	bare := stereoSource(4)
	rich := annotated(4, roled(0, RoleMusic), roled(1, RoleClean), roled(3, RoleCommentary))

	for _, id := range []string{PresetEverything, PresetNoMusic, PresetSurround} {
		t.Run(id, func(t *testing.T) {
			before := mustPreset(t, id, bare)
			after := mustPreset(t, id, rich)
			if !reflect.DeepEqual(before, after) {
				t.Errorf("annotations changed %q:\n before %+v\n after  %+v", id, before, after)
			}
		})
	}

	t.Run("mic-only is unchanged when no track is roled mic", func(t *testing.T) {
		before := mustPreset(t, PresetMicOnly, bare)
		after := mustPreset(t, PresetMicOnly, rich)
		if !reflect.DeepEqual(before, after) {
			t.Errorf("before %+v, after %+v", before, after)
		}
	})
}

func TestRoleTracksIgnoresTracksTheIngestIsNotCarrying(t *testing.T) {
	// The annotation survives the track vanishing, but a preset cannot select a
	// stream that is not there — Compile would only warn, and the operator
	// would see a checkbox for audio nobody is sending.
	src := stereoSource(2).WithAnnotations([]TrackAnnotation{roled(4, RoleMic)})
	present := map[int]bool{0: true, 1: true}

	if got := roleTracks(src, present, RoleMic); len(got) != 0 {
		t.Errorf("roleTracks = %v, want none: track 4 is not being carried", got)
	}
	p := mustPreset(t, PresetMicOnly, src)
	if got := p.SelectedTracks(); !reflect.DeepEqual(got, []int{2}) {
		t.Errorf("selected = %v, want the picker fallback [2]", got)
	}
}

func TestRoleTracksIsSortedAndDeduplicated(t *testing.T) {
	src := annotated(6, roled(4, RoleMic), roled(1, RoleCommentary), roled(0, RoleMic))
	present := map[int]bool{}
	for i := 0; i < 6; i++ {
		present[i] = true
	}

	got := roleTracks(src, present, RoleMic, RoleCommentary, RoleMic)
	want := []int{0, 1, 4}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("roleTracks = %v, want %v", got, want)
	}
	if !sort.IntsAreSorted(got) {
		t.Error("result is not ascending")
	}
}
