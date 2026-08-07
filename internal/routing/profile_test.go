package routing

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// ------------------------------------------------------- backwards compatibility

// legacyCase is a profile built the way profiles were built before track
// roles, loudness targets, audio delay, ducking and denoise existed, together
// with the exact filter string it compiled to then.
type legacyCase struct {
	name string
	p    Profile
	src  Source
	want string
}

// surroundSource is a 5.1 track alongside a stereo one, which is the layout
// that exercises the downmix coefficients inside a golden string.
func surroundSource() Source {
	return Source{Tracks: []Track{
		{Index: 0, Channels: 6, Codec: "aac", Layout: "5.1"},
		{Index: 1, Channels: 2, Codec: "aac", Layout: "stereo"},
	}}
}

func legacyCases(t *testing.T) []legacyCase {
	t.Helper()

	preset := func(id string, src Source) Profile {
		p, err := ApplyPreset(id, src, DefaultPresetOpts())
		if err != nil {
			t.Fatalf("ApplyPreset(%q): %v", id, err)
		}
		return p
	}

	gains := Profile{Mode: ModeSimple, Normalize: NormAuto, SampleRate: 48000, Tracks: []TrackSel{
		{Track: 0, Enabled: true, Gain: 0.5},
		{Track: 2, Enabled: true, Gain: 1.25},
	}}

	matrix := Profile{Mode: ModeMatrix, Normalize: NormAuto, SampleRate: 48000, Matrix: []Cell{
		{Track: 0, Channel: 0, Out: OutL, Gain: 1},
		{Track: 0, Channel: 1, Out: OutR, Gain: 1},
		{Track: 1, Channel: 0, Out: OutL, Gain: 0.5},
		{Track: 1, Channel: 1, Out: OutR, Gain: 0.5},
	}}

	return []legacyCase{
		{
			name: "default profile",
			p:    DefaultProfile(),
			src:  stereoSource(6),
			want: "[0:a:0]pan=stereo|c0=1*c0|c1=1*c1[a_t0];[a_t0]aresample=48000:async=1:first_pts=0[aout]",
		},
		{
			name: "single track auto adds no stage",
			p:    simple(NormAuto, 0),
			src:  stereoSource(6),
			want: "[0:a:0]pan=stereo|c0=1*c0|c1=1*c1[a_t0];[a_t0]aresample=48000:async=1:first_pts=0[aout]",
		},
		{
			name: "two tracks auto limits",
			p:    simple(NormAuto, 0, 1),
			src:  stereoSource(6),
			want: "[0:a:0]pan=stereo|c0=1*c0|c1=1*c1[a_t0];[0:a:1]pan=stereo|c0=1*c0|c1=1*c1[a_t1];[a_t0][a_t1]amix=inputs=2:duration=longest:normalize=0[a_mix];[a_mix]alimiter=limit=0.95:level=disabled[a_norm];[a_norm]aresample=48000:async=1:first_pts=0[aout]",
		},
		{
			name: "tracks 1 2 4",
			p:    simple(NormAuto, 0, 1, 3),
			src:  stereoSource(6),
			want: "[0:a:0]pan=stereo|c0=1*c0|c1=1*c1[a_t0];[0:a:1]pan=stereo|c0=1*c0|c1=1*c1[a_t1];[0:a:3]pan=stereo|c0=1*c0|c1=1*c1[a_t3];[a_t0][a_t1][a_t3]amix=inputs=3:duration=longest:normalize=0[a_mix];[a_mix]alimiter=limit=0.95:level=disabled[a_norm];[a_norm]aresample=48000:async=1:first_pts=0[aout]",
		},
		{
			name: "two tracks with normalization off",
			p:    simple(NormOff, 0, 1),
			src:  stereoSource(6),
			want: "[0:a:0]pan=stereo|c0=1*c0|c1=1*c1[a_t0];[0:a:1]pan=stereo|c0=1*c0|c1=1*c1[a_t1];[a_t0][a_t1]amix=inputs=2:duration=longest:normalize=0[a_mix];[a_mix]aresample=48000:async=1:first_pts=0[aout]",
		},
		{
			name: "single track forced limiter",
			p:    simple(NormLimiter, 0),
			src:  stereoSource(6),
			want: "[0:a:0]pan=stereo|c0=1*c0|c1=1*c1[a_t0];[a_t0]alimiter=limit=0.95:level=disabled[a_norm];[a_norm]aresample=48000:async=1:first_pts=0[aout]",
		},
		{
			// The one that matters most: loudnorm with no Loudness companion
			// must keep the fixed -16/-1.5/11 parameters forever.
			name: "loudnorm keeps its original fixed parameters",
			p:    simple(NormLoudnorm, 0, 1),
			src:  stereoSource(6),
			want: "[0:a:0]pan=stereo|c0=1*c0|c1=1*c1[a_t0];[0:a:1]pan=stereo|c0=1*c0|c1=1*c1[a_t1];[a_t0][a_t1]amix=inputs=2:duration=longest:normalize=0[a_mix];[a_mix]loudnorm=I=-16:TP=-1.5:LRA=11[a_norm];[a_norm]aresample=48000:async=1:first_pts=0[aout]",
		},
		{
			name: "all six tracks",
			p:    simple(NormAuto, 0, 1, 2, 3, 4, 5),
			src:  stereoSource(6),
			want: "[0:a:0]pan=stereo|c0=1*c0|c1=1*c1[a_t0];[0:a:1]pan=stereo|c0=1*c0|c1=1*c1[a_t1];[0:a:2]pan=stereo|c0=1*c0|c1=1*c1[a_t2];[0:a:3]pan=stereo|c0=1*c0|c1=1*c1[a_t3];[0:a:4]pan=stereo|c0=1*c0|c1=1*c1[a_t4];[0:a:5]pan=stereo|c0=1*c0|c1=1*c1[a_t5];[a_t0][a_t1][a_t2][a_t3][a_t4][a_t5]amix=inputs=6:duration=longest:normalize=0[a_mix];[a_mix]alimiter=limit=0.95:level=disabled[a_norm];[a_norm]aresample=48000:async=1:first_pts=0[aout]",
		},
		{
			name: "44100 destination",
			p:    Profile{Mode: ModeSimple, Normalize: NormAuto, SampleRate: 44100, Tracks: []TrackSel{{Track: 0, Enabled: true, Gain: 1}}},
			src:  stereoSource(6),
			want: "[0:a:0]pan=stereo|c0=1*c0|c1=1*c1[a_t0];[a_t0]aresample=44100:async=1:first_pts=0[aout]",
		},
		{
			name: "non-unity per-track gains",
			p:    gains,
			src:  stereoSource(6),
			want: "[0:a:0]pan=stereo|c0=0.5*c0|c1=0.5*c1[a_t0];[0:a:2]pan=stereo|c0=1.25*c0|c1=1.25*c1[a_t2];[a_t0][a_t2]amix=inputs=2:duration=longest:normalize=0[a_mix];[a_mix]alimiter=limit=0.95:level=disabled[a_norm];[a_norm]aresample=48000:async=1:first_pts=0[aout]",
		},
		{
			name: "matrix mode",
			p:    matrix,
			src:  stereoSource(6),
			want: "[0:a:0]pan=stereo|c0=1*c0|c1=1*c1[a_t0];[0:a:1]pan=stereo|c0=0.5*c0|c1=0.5*c1[a_t1];[a_t0][a_t1]amix=inputs=2:duration=longest:normalize=0[a_mix];[a_mix]alimiter=limit=0.95:level=disabled[a_norm];[a_norm]aresample=48000:async=1:first_pts=0[aout]",
		},
		{
			name: "5.1 downmix summed with a stereo track",
			p:    simple(NormAuto, 0, 1),
			src:  surroundSource(),
			want: "[0:a:0]pan=stereo|c0=0.4143*c0+0.2929*c2+0.2929*c4|c1=0.4143*c1+0.2929*c2+0.2929*c5[a_t0];[0:a:1]pan=stereo|c0=1*c0|c1=1*c1[a_t1];[a_t0][a_t1]amix=inputs=2:duration=longest:normalize=0[a_mix];[a_mix]alimiter=limit=0.95:level=disabled[a_norm];[a_norm]aresample=48000:async=1:first_pts=0[aout]",
		},
		{
			name: "preset everything",
			p:    preset(PresetEverything, stereoSource(6)),
			src:  stereoSource(6),
			want: "[0:a:0]pan=stereo|c0=1*c0|c1=1*c1[a_t0];[0:a:1]pan=stereo|c0=1*c0|c1=1*c1[a_t1];[0:a:2]pan=stereo|c0=1*c0|c1=1*c1[a_t2];[0:a:3]pan=stereo|c0=1*c0|c1=1*c1[a_t3];[0:a:4]pan=stereo|c0=1*c0|c1=1*c1[a_t4];[0:a:5]pan=stereo|c0=1*c0|c1=1*c1[a_t5];[a_t0][a_t1][a_t2][a_t3][a_t4][a_t5]amix=inputs=6:duration=longest:normalize=0[a_mix];[a_mix]alimiter=limit=0.95:level=disabled[a_norm];[a_norm]aresample=48000:async=1:first_pts=0[aout]",
		},
		{
			name: "preset no music",
			p:    preset(PresetNoMusic, stereoSource(6)),
			src:  stereoSource(6),
			want: "[0:a:1]pan=stereo|c0=1*c0|c1=1*c1[a_t1];[0:a:2]pan=stereo|c0=1*c0|c1=1*c1[a_t2];[0:a:3]pan=stereo|c0=1*c0|c1=1*c1[a_t3];[0:a:4]pan=stereo|c0=1*c0|c1=1*c1[a_t4];[0:a:5]pan=stereo|c0=1*c0|c1=1*c1[a_t5];[a_t1][a_t2][a_t3][a_t4][a_t5]amix=inputs=5:duration=longest:normalize=0[a_mix];[a_mix]alimiter=limit=0.95:level=disabled[a_norm];[a_norm]aresample=48000:async=1:first_pts=0[aout]",
		},
		{
			name: "preset mic only",
			p:    preset(PresetMicOnly, stereoSource(6)),
			src:  stereoSource(6),
			want: "[0:a:2]pan=stereo|c0=1*c0|c1=1*c1[a_t2];[a_t2]aresample=48000:async=1:first_pts=0[aout]",
		},
		{
			name: "preset surround downmix",
			p:    preset(PresetSurround, surroundSource()),
			src:  surroundSource(),
			want: "[0:a:0]pan=stereo|c0=0.4143*c0+0.2929*c2+0.2929*c4|c1=0.4143*c1+0.2929*c2+0.2929*c5[a_t0];[a_t0]aresample=48000:async=1:first_pts=0[aout]",
		},
	}
}

// This is the guard for the whole track-roles workstream. Every profile any
// user has ever saved was built from the fields these cases use; if adding
// roles, loudness, delay, ducking or denoise moves a single byte of any of
// these strings, someone's stream changed without them asking.
func TestLegacyProfilesCompileToTheExactSameFilterString(t *testing.T) {
	for _, tc := range legacyCases(t) {
		t.Run(tc.name, func(t *testing.T) {
			res, err := Compile(tc.p, tc.src)
			if err != nil {
				t.Fatalf("Compile: %v", err)
			}
			if res.FilterComplex != tc.want {
				t.Errorf("filter string changed\n got: %s\nwant: %s", res.FilterComplex, tc.want)
			}
			if res.OutLabel != OutLabel {
				t.Errorf("out label = %q, want %q", res.OutLabel, OutLabel)
			}
		})
	}
}

// ApplyDefaults runs on every profile arriving from the API, so it is the other
// place a byte could move.
func TestApplyDefaultsDoesNotDisturbLegacyCompilation(t *testing.T) {
	for _, tc := range legacyCases(t) {
		t.Run(tc.name, func(t *testing.T) {
			p := tc.p
			p.ApplyDefaults()
			res, err := Compile(p, tc.src)
			if err != nil {
				t.Fatalf("Compile: %v", err)
			}
			if res.FilterComplex != tc.want {
				t.Errorf("ApplyDefaults changed the filter string\n got: %s\nwant: %s", res.FilterComplex, tc.want)
			}
		})
	}
}

// Annotations describe the source; describing a track is never a routing
// decision on its own. Labelling a track must not change what any existing
// destination sends.
//
// Denoise is deliberately absent here. Role, Label and Language are
// descriptions; Denoise is a request, and the pair of tests below is where that
// distinction is written down.
func TestAnnotatingTheSourceChangesNoExistingMix(t *testing.T) {
	anns := []TrackAnnotation{
		{Track: 0, Role: RoleMusic, Label: "Spotify", Language: "en"},
		{Track: 1, Role: RoleCommentary, Label: "Comentario", Language: "es-419"},
		{Track: 2, Role: RoleMic},
	}
	for _, tc := range legacyCases(t) {
		t.Run(tc.name, func(t *testing.T) {
			res, err := Compile(tc.p, tc.src.WithAnnotations(anns))
			if err != nil {
				t.Fatalf("Compile: %v", err)
			}
			if res.FilterComplex != tc.want {
				t.Errorf("annotations leaked into the mix\n got: %s\nwant: %s", res.FilterComplex, tc.want)
			}
		})
	}
}

// The counterpart to the test above: Denoise is the operator asking for
// something, so it must reach the graph. A denoise checkbox that changed no
// filter string would be a control that does nothing.
func TestDenoiseIsTheOneAnnotationThatChangesTheMix(t *testing.T) {
	anns := []TrackAnnotation{{Track: 0, Role: RoleMic, Denoise: true}}
	var sawTrackZero bool
	for _, tc := range legacyCases(t) {
		t.Run(tc.name, func(t *testing.T) {
			res, err := Compile(tc.p, tc.src.WithAnnotations(anns))
			if err != nil {
				t.Fatalf("Compile: %v", err)
			}
			// Only profiles that actually route track 0 can show the stage;
			// the rest are here to prove denoise does not leak onto a track
			// nobody annotated.
			if !strings.Contains(tc.want, "[a_t0]") && !strings.Contains(tc.want, "[a_k0]") {
				if strings.Contains(res.FilterComplex, DenoiseFilter) {
					t.Errorf("denoise reached a profile that does not route track 0: %s", res.FilterComplex)
				}
				return
			}
			sawTrackZero = true
			if !strings.Contains(res.FilterComplex, DenoiseFilter) {
				t.Errorf("denoise never reached the graph: %s", res.FilterComplex)
			}
			if strings.Count(res.FilterComplex, DenoiseFilter) != 1 {
				t.Errorf("denoise applied to more than the annotated track: %s", res.FilterComplex)
			}
		})
	}
	if !sawTrackZero {
		t.Fatal("no legacy case routes track 0; this test proves nothing")
	}
}

// Stored profiles are JSON. A new field that marshals when unset would rewrite
// every row in the database on the next save and break byte-comparison of
// stored config.
func TestLegacyProfileJSONGainsNoNewKeys(t *testing.T) {
	b, err := json.Marshal(DefaultProfile())
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(b)
	for _, key := range []string{"loudness", "delayMs", "ducking", "excludeRoles"} {
		if strings.Contains(got, key) {
			t.Errorf("unset optional field %q is serialized: %s", key, got)
		}
	}

	sb, err := json.Marshal(stereoSource(2))
	if err != nil {
		t.Fatalf("marshal source: %v", err)
	}
	if strings.Contains(string(sb), "annotations") {
		t.Errorf("unset annotations are serialized: %s", sb)
	}
}

// A profile stored before any of this existed must still decode, still
// validate, and still carry nothing optional.
func TestPreFeatureProfileJSONStillDecodes(t *testing.T) {
	const stored = `{"mode":"simple","tracks":[{"track":0,"enabled":true,"gain":1}],` +
		`"matrix":null,"normalize":"auto","sampleRate":48000}`

	var p Profile
	if err := json.Unmarshal([]byte(stored), &p); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if err := p.Validate(); err != nil {
		t.Fatalf("stored profile no longer validates: %v", err)
	}
	if p.Loudness != nil || p.Ducking != nil || p.DelayMS != 0 || len(p.ExcludeRoles) != 0 {
		t.Errorf("decoding invented optional config: %+v", p)
	}
	if _, ok := p.EffectiveLoudness(); ok {
		t.Error("a profile with no loudness target must not arm one")
	}
	if _, ok := p.EffectiveDucking(); ok {
		t.Error("a profile with no ducking must not arm one")
	}
}

// ------------------------------------------------------------------ roles

func TestTrackRolesCatalogue(t *testing.T) {
	roles := TrackRoles()
	if len(roles) == 0 {
		t.Fatal("no roles offered")
	}
	seen := map[TrackRole]bool{}
	for _, r := range roles {
		if r == RoleUnset {
			t.Error("RoleUnset is the absence of a choice, not one of them")
		}
		if !ValidRole(r) {
			t.Errorf("catalogue offers %q which ValidRole rejects", r)
		}
		if seen[r] {
			t.Errorf("duplicate role %q", r)
		}
		seen[r] = true
	}
	if !ValidRole(RoleUnset) {
		t.Error("an annotation carrying only a label must be valid")
	}
	if ValidRole("karaoke") {
		t.Error("unknown roles must not validate")
	}
}

func TestSourceAnnotationLookups(t *testing.T) {
	src := Source{
		Tracks: []Track{
			{Index: 0, Channels: 2, Language: "eng", Title: "Program"},
			{Index: 1, Channels: 2, Language: "spa"},
			{Index: 2, Channels: 1},
		},
		Annotations: []TrackAnnotation{
			{Track: 0, Role: RoleMusic, Label: "Licensed bed"},
			{Track: 1, Role: RoleCommentary, Language: "es-419", Label: "Comentario"},
			{Track: 2, Role: RoleMic, Denoise: true},
		},
	}

	tests := []struct {
		name string
		got  any
		want any
	}{
		{"role comes from the annotation", src.RoleOf(1), RoleCommentary},
		{"an unannotated track has no role", src.RoleOf(5), RoleUnset},
		{"the operator's language beats the container's", src.LanguageOf(1), "es-419"},
		{"the container's language is the fallback", src.LanguageOf(0), "eng"},
		{"an unknown track has no language", src.LanguageOf(9), ""},
		{"the operator's label beats the container title", src.LabelOf(0), "Licensed bed"},
		{"the container title is the fallback", src.LabelOf(9), ""},
		{"denoise is read off the annotation", src.DenoiseTrack(2), true},
		{"denoise defaults off", src.DenoiseTrack(0), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.got != tc.want {
				t.Errorf("got %v, want %v", tc.got, tc.want)
			}
		})
	}

	if got := src.TracksWithRole(RoleMusic); len(got) != 1 || got[0] != 0 {
		t.Errorf("TracksWithRole(music) = %v, want [0]", got)
	}
	if got := src.TracksWithRole(RoleGame); len(got) != 0 {
		t.Errorf("TracksWithRole(game) = %v, want none", got)
	}
}

// "Route the Spanish commentary track to the Spanish destination" is the whole
// point of the language tag, so matching has to survive regional variants.
func TestTracksWithLanguage(t *testing.T) {
	src := Source{
		Tracks: []Track{
			{Index: 0, Channels: 2, Language: "en"},
			{Index: 1, Channels: 2, Language: "spa"},
			{Index: 2, Channels: 2},
			{Index: 3, Channels: 2},
		},
		Annotations: []TrackAnnotation{
			{Track: 1, Role: RoleCommentary, Language: "es-419"},
			{Track: 2, Role: RoleCommentary, Language: "ES"},
			{Track: 3, Role: RoleCommentary, Language: "pt-BR"},
		},
	}

	tests := []struct {
		name string
		tag  string
		want []int
	}{
		{"a primary subtag finds its regional variants", "es", []int{1, 2}},
		{"matching is case insensitive", "PT-br", []int{3}},
		// A Latin American Spanish destination should take the es-419 track,
		// and should still take a track labelled only "es" — that is the best
		// available answer, not a miss.
		{"a regional query finds its variant and the bare primary", "ES-419", []int{1, 2}},
		{"a regional query matches the bare primary too", "en-GB", []int{0}},
		{"no match is empty, not everything", "de", nil},
		{"an empty query matches nothing", "", nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := src.TracksWithLanguage(tc.tag)
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("got %v, want %v", got, tc.want)
				}
			}
		})
	}
}

func TestWithAnnotationsDoesNotAliasTheCaller(t *testing.T) {
	anns := []TrackAnnotation{{Track: 0, Role: RoleMic}}
	src := stereoSource(2).WithAnnotations(anns)
	anns[0].Role = RoleMusic
	if src.RoleOf(0) != RoleMic {
		t.Error("Source shares the caller's annotation slice; a later edit rewrites the ingest")
	}
}

func TestValidateAnnotations(t *testing.T) {
	tests := []struct {
		name    string
		anns    []TrackAnnotation
		wantErr string // substring; "" means valid
	}{
		{name: "no annotations at all is valid"},
		{
			name: "a full annotation",
			anns: []TrackAnnotation{{Track: 2, Role: RoleCommentary, Label: "Spanish desk", Language: "es-419", Denoise: true}},
		},
		{
			name: "label only, no role",
			anns: []TrackAnnotation{{Track: 0, Label: "Whatever this is"}},
		},
		{
			name:    "unknown role",
			anns:    []TrackAnnotation{{Track: 0, Role: "karaoke"}},
			wantErr: `unknown role "karaoke"`,
		},
		{
			// Relative to MaxTracks, not a literal. This case asserted track 6
			// when the ceiling was 6; raising it to 32 made track 6 legal and
			// the test silently stopped testing a ceiling at all.
			name:    "track above the ceiling",
			anns:    []TrackAnnotation{{Track: MaxTracks, Role: RoleMic}},
			wantErr: fmt.Sprintf("track %d out of range", MaxTracks),
		},
		{
			name: "the highest legal track is accepted",
			anns: []TrackAnnotation{{Track: MaxTracks - 1, Role: RoleMic}},
		},
		{
			name:    "negative track",
			anns:    []TrackAnnotation{{Track: -1}},
			wantErr: "track -1 out of range",
		},
		{
			name:    "two annotations for one track",
			anns:    []TrackAnnotation{{Track: 1, Role: RoleMic}, {Track: 1, Role: RoleMusic}},
			wantErr: "duplicate annotation for track 1",
		},
		{
			name:    "label longer than the ceiling",
			anns:    []TrackAnnotation{{Track: 0, Label: strings.Repeat("x", MaxLabelLen+1)}},
			wantErr: "label is 65 characters",
		},
		{
			name: "label exactly at the ceiling",
			anns: []TrackAnnotation{{Track: 0, Label: strings.Repeat("x", MaxLabelLen)}},
		},
		// Fail open on language: anything shaped like a tag is accepted, because
		// refusing a legitimate regional or private-use tag is far worse than
		// storing a typo nobody reads.
		{name: "plain primary subtag", anns: []TrackAnnotation{{Track: 0, Language: "es"}}},
		{name: "region subtag", anns: []TrackAnnotation{{Track: 0, Language: "pt-BR"}}},
		{name: "script and region", anns: []TrackAnnotation{{Track: 0, Language: "zh-Hant-TW"}}},
		{name: "un m49 region", anns: []TrackAnnotation{{Track: 0, Language: "es-419"}}},
		{name: "private use", anns: []TrackAnnotation{{Track: 0, Language: "x-booth2"}}},
		{name: "legacy three letter iso code", anns: []TrackAnnotation{{Track: 0, Language: "spa"}}},
		{
			name:    "a sentence is not a language tag",
			anns:    []TrackAnnotation{{Track: 0, Language: "Spanish (Latin America)"}},
			wantErr: "is not a language tag",
		},
		{
			name:    "a dangling hyphen is not a language tag",
			anns:    []TrackAnnotation{{Track: 0, Language: "es-"}},
			wantErr: "is not a language tag",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateAnnotations(tc.anns)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("want valid, got %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("want error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error %q does not contain %q", err.Error(), tc.wantErr)
			}
		})
	}
}

func TestValidateAnnotationsReportsEveryProblemAtOnce(t *testing.T) {
	err := ValidateAnnotations([]TrackAnnotation{
		{Track: 0, Role: "karaoke", Language: "not a tag"},
		{Track: 0, Label: strings.Repeat("x", 200)},
	})
	ve, ok := err.(*ValidationError)
	if !ok {
		t.Fatalf("want *ValidationError, got %T", err)
	}
	if len(ve.Problems) < 4 {
		t.Errorf("want every problem at once, got %d: %v", len(ve.Problems), ve.Problems)
	}
	if !strings.HasPrefix(ve.Error(), "invalid track annotations") {
		t.Errorf("annotation errors should name their subject: %s", ve.Error())
	}
}

// The existing message is what the API and the UI already render.
func TestProfileValidationErrorMessageIsUnchanged(t *testing.T) {
	ve := &ValidationError{Problems: []string{"nope"}}
	if got, want := ve.Error(), "invalid routing profile: nope"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// ------------------------------------------------------------- optional stages

func TestValidateOptionalFields(t *testing.T) {
	base := func() Profile { return simple(NormAuto, 0) }

	withLoudness := func(l Loudness) Profile {
		p := base()
		p.Loudness = &l
		return p
	}
	withDucking := func(d Ducking) Profile {
		p := base()
		p.Ducking = &d
		return p
	}
	duck := func() Ducking { return Ducking{Trigger: []int{2}, Target: []int{0}} }

	tests := []struct {
		name    string
		profile Profile
		wantErr string // substring; "" means valid
	}{
		{name: "no optional fields", profile: base()},

		{name: "streaming loudness target", profile: withLoudness(Loudness{TargetLUFS: LUFSStreaming, TruePeakDB: -1})},
		{name: "broadcast loudness target", profile: withLoudness(Loudness{TargetLUFS: LUFSBroadcast, TruePeakDB: -1, RangeLU: 7})},
		{name: "unset true peak and range mean defaults", profile: withLoudness(Loudness{TargetLUFS: LUFSPodcast})},
		{
			name:    "loudness target louder than loudnorm accepts",
			profile: withLoudness(Loudness{TargetLUFS: 0}),
			wantErr: "loudness target 0.0 LUFS out of range",
		},
		{
			name:    "loudness target quieter than loudnorm accepts",
			profile: withLoudness(Loudness{TargetLUFS: -90}),
			wantErr: "loudness target -90.0 LUFS out of range",
		},
		{
			name:    "true peak above full scale",
			profile: withLoudness(Loudness{TargetLUFS: -14, TruePeakDB: 3}),
			wantErr: "true-peak ceiling 3.0 dBTP out of range",
		},
		{
			name:    "true peak below what loudnorm accepts",
			profile: withLoudness(Loudness{TargetLUFS: -14, TruePeakDB: -20}),
			wantErr: "true-peak ceiling -20.0 dBTP out of range",
		},
		{
			name:    "loudness range out of bounds",
			profile: withLoudness(Loudness{TargetLUFS: -14, RangeLU: 99}),
			wantErr: "loudness range 99.0 LU out of range",
		},

		{name: "no delay", profile: base()},
		{name: "lip sync trim", profile: func() Profile { p := base(); p.DelayMS = -120; return p }()},
		{name: "moderation delay", profile: func() Profile { p := base(); p.DelayMS = 7000; return p }()},
		{name: "delay at the positive ceiling", profile: func() Profile { p := base(); p.DelayMS = MaxDelayMS; return p }()},
		{name: "delay at the negative floor", profile: func() Profile { p := base(); p.DelayMS = MinDelayMS; return p }()},
		{
			name:    "delay past the positive ceiling",
			profile: func() Profile { p := base(); p.DelayMS = MaxDelayMS + 1; return p }(),
			wantErr: "audio delay 30001 ms out of range",
		},
		{
			name:    "delay past the negative floor",
			profile: func() Profile { p := base(); p.DelayMS = MinDelayMS - 1; return p }(),
			wantErr: "audio delay -2001 ms out of range",
		},

		{name: "mic ducks music", profile: withDucking(duck())},
		{
			name:    "ducking with no trigger",
			profile: withDucking(Ducking{Target: []int{0}}),
			wantErr: "ducking trigger selects no track",
		},
		{
			name:    "ducking with no target",
			profile: withDucking(Ducking{Trigger: []int{2}}),
			wantErr: "ducking target selects no track",
		},
		{
			name:    "a track cannot duck itself",
			profile: withDucking(Ducking{Trigger: []int{2}, Target: []int{0, 2}}),
			wantErr: "track 2 cannot duck itself",
		},
		{
			name:    "ducking trigger out of range",
			profile: withDucking(Ducking{Trigger: []int{MaxTracks}, Target: []int{0}}),
			wantErr: fmt.Sprintf("ducking trigger track %d out of range", MaxTracks),
		},
		{
			name:    "duplicate ducking target",
			profile: withDucking(Ducking{Trigger: []int{2}, Target: []int{0, 0}}),
			wantErr: "duplicate ducking target track 0",
		},
		{
			name: "ducking threshold too low",
			profile: withDucking(func() Ducking {
				d := duck()
				d.ThresholdDB = -90
				return d
			}()),
			wantErr: "ducking threshold -90.0 dB out of range",
		},
		{
			name: "ducking ratio out of range",
			profile: withDucking(func() Ducking {
				d := duck()
				d.Ratio = 100
				return d
			}()),
			wantErr: "ducking ratio 100.0 out of range",
		},
		{
			name: "ducking attack out of range",
			profile: withDucking(func() Ducking {
				d := duck()
				d.AttackMS = 9000
				return d
			}()),
			wantErr: "ducking attack 9000.00 ms out of range",
		},
		{
			name: "ducking release out of range",
			profile: withDucking(func() Ducking {
				d := duck()
				d.ReleaseMS = 20000
				return d
			}()),
			wantErr: "ducking release 20000.00 ms out of range",
		},

		{
			name:    "excluding the music role",
			profile: func() Profile { p := base(); p.ExcludeRoles = []TrackRole{RoleMusic}; return p }(),
		},
		{
			name:    "excluding an unknown role",
			profile: func() Profile { p := base(); p.ExcludeRoles = []TrackRole{"karaoke"}; return p }(),
			wantErr: `unknown track role "karaoke" in excludeRoles`,
		},
		{
			// Excluding "no role" would drop every track nobody has described,
			// which on a fresh install is all of them.
			name:    "excluding the empty role",
			profile: func() Profile { p := base(); p.ExcludeRoles = []TrackRole{RoleUnset}; return p }(),
			wantErr: "excludeRoles cannot contain the empty role",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.profile.Validate()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("want valid, got %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("want error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error %q does not contain %q", err.Error(), tc.wantErr)
			}
		})
	}
}

func TestValidateReportsOldAndNewProblemsTogether(t *testing.T) {
	p := Profile{
		Mode: ModeSimple, Normalize: NormAuto, SampleRate: 48000,
		Tracks:       []TrackSel{{Track: 0, Enabled: true, Gain: 1}},
		DelayMS:      99999,
		Loudness:     &Loudness{TargetLUFS: 12},
		Ducking:      &Ducking{Trigger: []int{0}, Target: []int{0}},
		ExcludeRoles: []TrackRole{"nope"},
	}
	err := p.Validate()
	ve, ok := err.(*ValidationError)
	if !ok {
		t.Fatalf("want *ValidationError, got %T", err)
	}
	if len(ve.Problems) < 4 {
		t.Errorf("want every problem at once, got %d: %v", len(ve.Problems), ve.Problems)
	}
}

func TestApplyDefaultsFillsOptionalStagesOnlyWhenPresent(t *testing.T) {
	t.Run("absent stages stay absent", func(t *testing.T) {
		p := Profile{Tracks: []TrackSel{{Track: 0, Enabled: true, Gain: 1}}}
		p.ApplyDefaults()
		if p.Loudness != nil || p.Ducking != nil {
			t.Fatalf("ApplyDefaults invented optional stages: %+v", p)
		}
		if p.DelayMS != 0 || len(p.ExcludeRoles) != 0 {
			t.Fatalf("ApplyDefaults invented optional config: %+v", p)
		}
	})

	t.Run("a present loudness target is completed", func(t *testing.T) {
		p := simple(NormAuto, 0)
		p.Loudness = &Loudness{TargetLUFS: LUFSStreaming}
		p.ApplyDefaults()
		if p.Loudness.TruePeakDB != DefaultTruePeakDB {
			t.Errorf("true peak = %v, want %v", p.Loudness.TruePeakDB, DefaultTruePeakDB)
		}
		if p.Loudness.RangeLU != DefaultLoudnessLRA {
			t.Errorf("range = %v, want %v", p.Loudness.RangeLU, DefaultLoudnessLRA)
		}
		if p.Loudness.TargetLUFS != LUFSStreaming {
			t.Errorf("target was overwritten: %v", p.Loudness.TargetLUFS)
		}
	})

	t.Run("explicit loudness parameters survive", func(t *testing.T) {
		p := simple(NormAuto, 0)
		p.Loudness = &Loudness{TargetLUFS: LUFSBroadcast, TruePeakDB: -2, RangeLU: 5}
		p.ApplyDefaults()
		if p.Loudness.TruePeakDB != -2 || p.Loudness.RangeLU != 5 {
			t.Errorf("explicit parameters overwritten: %+v", *p.Loudness)
		}
	})

	t.Run("a present duck is completed", func(t *testing.T) {
		p := simple(NormAuto, 0, 2)
		p.Ducking = &Ducking{Trigger: []int{2}, Target: []int{0}}
		p.ApplyDefaults()
		d := *p.Ducking
		if d.ThresholdDB != DefaultDuckThresholdDB || d.Ratio != DefaultDuckRatio ||
			d.AttackMS != DefaultDuckAttackMS || d.ReleaseMS != DefaultDuckReleaseMS {
			t.Errorf("duck defaults not applied: %+v", d)
		}
	})

	t.Run("a completed duck still validates", func(t *testing.T) {
		p := simple(NormAuto, 0, 2)
		p.Ducking = &Ducking{Trigger: []int{2}, Target: []int{0}}
		p.ApplyDefaults()
		if err := p.Validate(); err != nil {
			t.Fatalf("defaults produce an invalid profile: %v", err)
		}
	})
}

func TestEffectiveLoudness(t *testing.T) {
	target := &Loudness{TargetLUFS: LUFSStreaming}

	tests := []struct {
		name       string
		norm       NormMode
		loudness   *Loudness
		wantArmed  bool
		wantTarget float64
	}{
		{name: "no target under auto", norm: NormAuto},
		{name: "no target under loudnorm", norm: NormLoudnorm},
		{
			name: "a target arms auto", norm: NormAuto, loudness: target,
			wantArmed: true, wantTarget: LUFSStreaming,
		},
		{
			name: "a target parameterizes loudnorm", norm: NormLoudnorm, loudness: target,
			wantArmed: true, wantTarget: LUFSStreaming,
		},
		// "off" and "limiter" are decisions the operator already made.
		{name: "off is not overridden", norm: NormOff, loudness: target},
		{name: "limiter is not overridden", norm: NormLimiter, loudness: target},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := simple(tc.norm, 0)
			p.Loudness = tc.loudness
			got, armed := p.EffectiveLoudness()
			if armed != tc.wantArmed {
				t.Fatalf("armed = %v, want %v", armed, tc.wantArmed)
			}
			if !armed {
				return
			}
			if got.TargetLUFS != tc.wantTarget {
				t.Errorf("target = %v, want %v", got.TargetLUFS, tc.wantTarget)
			}
			if got.TruePeakDB != DefaultTruePeakDB || got.RangeLU != DefaultLoudnessLRA {
				t.Errorf("defaults not filled: %+v", got)
			}
		})
	}
}

func TestEffectiveLoudnessDoesNotMutateTheProfile(t *testing.T) {
	p := simple(NormAuto, 0)
	p.Loudness = &Loudness{TargetLUFS: LUFSStreaming}
	if _, ok := p.EffectiveLoudness(); !ok {
		t.Fatal("want armed")
	}
	if p.Loudness.TruePeakDB != 0 || p.Loudness.RangeLU != 0 {
		t.Errorf("EffectiveLoudness wrote defaults back into the stored profile: %+v", *p.Loudness)
	}
}

func TestEffectiveDucking(t *testing.T) {
	t.Run("absent", func(t *testing.T) {
		if _, ok := simple(NormAuto, 0).EffectiveDucking(); ok {
			t.Error("want no duck")
		}
	})

	t.Run("half configured is not a duck", func(t *testing.T) {
		p := simple(NormAuto, 0)
		p.Ducking = &Ducking{Trigger: []int{2}}
		if _, ok := p.EffectiveDucking(); ok {
			t.Error("a duck with no target must not arm")
		}
	})

	t.Run("defaults are filled without touching the profile", func(t *testing.T) {
		p := simple(NormAuto, 0, 2)
		p.Ducking = &Ducking{Trigger: []int{2}, Target: []int{0}}
		d, ok := p.EffectiveDucking()
		if !ok {
			t.Fatal("want a duck")
		}
		if d.Ratio != DefaultDuckRatio || d.ThresholdDB != DefaultDuckThresholdDB {
			t.Errorf("defaults not filled: %+v", d)
		}
		if p.Ducking.Ratio != 0 {
			t.Error("EffectiveDucking wrote defaults back into the stored profile")
		}
		d.Trigger[0] = 5
		if p.Ducking.Trigger[0] != 2 {
			t.Error("EffectiveDucking aliases the stored track groups")
		}
	})
}

// ------------------------------------------------------------ role exclusion

func TestExcludedTracks(t *testing.T) {
	src := Source{
		Tracks: []Track{
			{Index: 0, Channels: 2}, {Index: 1, Channels: 2}, {Index: 2, Channels: 2},
		},
		Annotations: []TrackAnnotation{
			{Track: 2, Role: RoleMusic},
			{Track: 0, Role: RoleMusic},
			{Track: 1, Role: RoleMic},
		},
	}

	tests := []struct {
		name    string
		exclude []TrackRole
		src     Source
		want    []int
	}{
		{name: "no policy excludes nothing", src: src},
		{name: "the DMCA switch", exclude: []TrackRole{RoleMusic}, src: src, want: []int{0, 2}},
		{name: "two roles", exclude: []TrackRole{RoleMusic, RoleMic}, src: src, want: []int{0, 1, 2}},
		{name: "a role nothing carries", exclude: []TrackRole{RoleGame}, src: src},
		{
			// An unannotated ingest is the common case, and it must never lose
			// audio to a policy it has no information about.
			name:    "an unannotated source is never filtered",
			exclude: []TrackRole{RoleMusic},
			src:     stereoSource(3),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := simple(NormAuto, 0)
			p.ExcludeRoles = tc.exclude
			got := p.ExcludedTracks(tc.src)
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("got %v, want %v", got, tc.want)
				}
			}
		})
	}
}

func TestExcludesRoleNeverMatchesTheEmptyRole(t *testing.T) {
	p := simple(NormAuto, 0)
	p.ExcludeRoles = []TrackRole{RoleUnset, RoleMusic}
	if p.ExcludesRole(RoleUnset) {
		t.Error("an undescribed track must never be excluded by policy")
	}
	if !p.ExcludesRole(RoleMusic) {
		t.Error("music should still be excluded")
	}
}
