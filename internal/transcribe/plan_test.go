package transcribe

import (
	"reflect"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/ffmpeg"
	"github.com/rainmanjam/polyemesis/internal/routing"
)

func source(tracks int, ann ...routing.TrackAnnotation) routing.Source {
	var s routing.Source
	for i := 0; i < tracks; i++ {
		s.Tracks = append(s.Tracks, routing.Track{Index: i, Channels: 2, Codec: "aac"})
	}
	return s.WithAnnotations(ann)
}

// The differentiator, stated as a test: the mic tracks are the default, and the
// mix is never chosen for us.
func TestDefaultTracksPrefersTheMicrophones(t *testing.T) {
	tests := []struct {
		name string
		src  routing.Source
		want []int
	}{
		{
			name: "the mic tracks win when roles exist",
			src: source(4,
				routing.TrackAnnotation{Track: 0, Role: routing.RoleMusic},
				routing.TrackAnnotation{Track: 1, Role: routing.RoleMic},
				routing.TrackAnnotation{Track: 2, Role: routing.RoleMic},
				routing.TrackAnnotation{Track: 3, Role: routing.RoleGame},
			),
			want: []int{1, 2},
		},
		{
			name: "commentary counts as speech when there are no mics",
			src: source(3,
				routing.TrackAnnotation{Track: 0, Role: routing.RoleMusic},
				routing.TrackAnnotation{Track: 2, Role: routing.RoleCommentary},
			),
			want: []int{2},
		},
		{
			name: "with only the non-speech tracks annotated, the rest are transcribed",
			src: source(3,
				routing.TrackAnnotation{Track: 0, Role: routing.RoleMusic},
				routing.TrackAnnotation{Track: 1, Label: "Somebody"},
			),
			want: []int{1, 2},
		},
		{
			name: "the clean mix is never picked automatically, because a mix transcribes worse",
			src: source(2,
				routing.TrackAnnotation{Track: 0, Role: routing.RoleClean},
				routing.TrackAnnotation{Track: 1, Label: "Host"},
			),
			want: []int{1},
		},
		{
			name: "with no annotations at all, every track is transcribed separately",
			src:  source(3),
			want: []int{0, 1, 2},
		},
		{
			name: "a single unannotated track",
			src:  source(1),
			want: []int{0},
		},
		{
			name: "when every track is one we would rather skip, transcribe them anyway",
			src: source(2,
				routing.TrackAnnotation{Track: 0, Role: routing.RoleMusic},
				routing.TrackAnnotation{Track: 1, Role: routing.RoleGame},
			),
			want: []int{0, 1},
		},
		{
			name: "no tracks at all",
			src:  source(0),
			want: nil,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := DefaultTracks(tc.src); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("DefaultTracks = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestPlanTracksLabelsEachTrackWithItsSpeakerWhichIsTheFreeDiarization(t *testing.T) {
	src := source(3,
		routing.TrackAnnotation{Track: 0, Role: routing.RoleMic, Label: "Host", Language: "en"},
		routing.TrackAnnotation{Track: 1, Role: routing.RoleMic, Label: "Guest (Zoom)", Language: "pt-BR", Denoise: true},
		routing.TrackAnnotation{Track: 2, Role: routing.RoleMusic},
	)
	got := PlanTracks(src, nil)
	if len(got) != 2 {
		t.Fatalf("plan = %+v, want the two mic tracks", got)
	}
	if got[0].Speaker != "Host" || got[1].Speaker != "Guest (Zoom)" {
		t.Errorf("speakers = %q / %q, want the operator's labels", got[0].Speaker, got[1].Speaker)
	}
	if got[0].Language != "en" || got[1].Language != "pt-BR" {
		t.Errorf("languages = %q / %q", got[0].Language, got[1].Language)
	}
	if !got[1].Denoise {
		t.Error("the source's denoise flag must reach the extraction: a noisy room is noisy for the transcript too")
	}
	if got[0].Role != routing.RoleMic {
		t.Errorf("role = %q, want mic", got[0].Role)
	}
}

func TestPlanTracksDisambiguatesSpeakersThatWouldOtherwiseCollide(t *testing.T) {
	src := source(2,
		routing.TrackAnnotation{Track: 0, Role: routing.RoleMic},
		routing.TrackAnnotation{Track: 1, Role: routing.RoleMic},
	)
	got := PlanTracks(src, nil)
	if len(got) != 2 {
		t.Fatalf("plan = %+v", got)
	}
	if got[0].Speaker == got[1].Speaker {
		t.Fatalf("both tracks labelled %q: the entire point of per-track transcription is lost", got[0].Speaker)
	}
	// The label has to tie back to the "Track N" the operator sees in the UI.
	if got[0].Speaker != "Mic 1" || got[1].Speaker != "Mic 2" {
		t.Errorf("speakers = %q / %q, want them numbered by track", got[0].Speaker, got[1].Speaker)
	}
}

func TestPlanTracksHonoursAnExplicitSelection(t *testing.T) {
	tests := []struct {
		name string
		src  routing.Source
		want []int
		req  []int
	}{
		{
			name: "an explicit choice overrides the mic default",
			src:  source(4, routing.TrackAnnotation{Track: 0, Role: routing.RoleMic}),
			req:  []int{2, 3},
			want: []int{2, 3},
		},
		{
			name: "a track the recording does not have is dropped, not fatal",
			src:  source(2),
			req:  []int{0, 9},
			want: []int{0},
		},
		{
			name: "duplicates and negatives are discarded",
			src:  source(3),
			req:  []int{1, 1, -1, 0},
			want: []int{0, 1},
		},
		{
			name: "with no probe result the request is honoured verbatim",
			src:  source(0),
			req:  []int{0, 2},
			want: []int{0, 2},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var got []int
			for _, c := range PlanTracks(tc.src, tc.req) {
				got = append(got, c.Track)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("tracks = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestSpeakerLabelPrefersTheOperatorsWordsThenTheRoleThenTheNumber(t *testing.T) {
	tests := []struct {
		name, label, role string
		track             int
		want              string
	}{
		{name: "a label beats a role, because a person reads it", label: "Guest mic (Zoom)", role: "mic", track: 2, want: "Guest mic (Zoom)"},
		{name: "a role is better than a number", role: "commentary", track: 0, want: "Commentary 1"},
		{name: "nothing at all falls back to the track number", track: 3, want: "Track 4"},
		{name: "whitespace is not a label", label: "   ", role: "mic", track: 0, want: "Mic 1"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := SpeakerLabel(tc.label, tc.role, tc.track); got != tc.want {
				t.Errorf("SpeakerLabel = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSourceFromProbeCarriesTheTrackIndicesFFmpegWillBeToldToMap(t *testing.T) {
	probe := &ffmpeg.ProbeResult{
		Audio: []ffmpeg.AudioStream{
			{Index: 0, Codec: "aac", Channels: 2, Layout: "stereo", Language: "eng", Title: "Host"},
			{Index: 1, Codec: "aac", Channels: 1, Layout: "mono"},
		},
	}
	src := SourceFromProbe(probe)
	if len(src.Tracks) != 2 {
		t.Fatalf("tracks = %+v", src.Tracks)
	}
	if src.Tracks[0].Index != 0 || src.Tracks[1].Index != 1 {
		t.Errorf("indices = %d / %d", src.Tracks[0].Index, src.Tracks[1].Index)
	}
	if src.LabelOf(0) != "Host" {
		t.Errorf("the container title should stand in for a label, got %q", src.LabelOf(0))
	}
	if got := SourceFromProbe(nil); len(got.Tracks) != 0 {
		t.Error("a nil probe must yield an empty source rather than panicking")
	}
}
