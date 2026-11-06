package db

import (
	"fmt"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/jobs"
)

// collect runs one validation helper and returns what it reported.
func collect(run func(add problemAdder)) []string {
	var probs []string
	run(func(f string, a ...any) { probs = append(probs, fmt.Sprintf(f, a...)) })
	return probs
}

// expectProblems checks got holds exactly one problem per want, each
// containing its substring, in order. An empty want means no problem.
func expectProblems(t *testing.T, got []string, want ...string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d problems %q, want %d %q", len(got), got, len(want), want)
	}
	for i, w := range want {
		if !strings.Contains(got[i], w) {
			t.Errorf("problem %d = %q, want it to mention %q", i, got[i], w)
		}
	}
}

func TestAudioTargetProblems(t *testing.T) {
	cases := []struct {
		target string
		want   []string
	}{
		{"", []string{"an Icecast URL or an output filename is required"}},
		{"icecast://host:8000/live.mp3", nil},
		{"icecast://host:8000/", []string{"needs a mount point"}},
		{"http://host:8000/live.mp3", []string{"must start with icecast://"}},
		{"icecast://ho st/%zz", []string{"malformed audio destination URL"}},
		{"../escape.mp3", []string{"relative name inside the recordings directory"}},
		{"/abs.mp3", []string{"relative name inside the recordings directory"}},
		{"show.mp3", nil},
	}
	for _, tc := range cases {
		t.Run(tc.target, func(t *testing.T) {
			expectProblems(t, collect(func(add problemAdder) { audioTargetProblems(tc.target, add) }), tc.want...)
		})
	}
}

func TestRenditionRateControlProblems(t *testing.T) {
	cases := []struct {
		name string
		r    Rendition
		want []string
	}{
		{"both derived", Rendition{VideoBitrate: 4000}, nil},
		{"maxrate out of range", Rendition{VideoBitrate: 4000, MaxrateKbps: 1}, []string{"maxrate 1 kbps out of range"}},
		{"maxrate below target", Rendition{VideoBitrate: 4000, MaxrateKbps: 3000}, []string{"below the target bitrate"}},
		{"bufsize under half the target", Rendition{VideoBitrate: 4000, BufsizeKbps: 1000}, []string{"less than half the 4000 kbps ceiling"}},
		{"bufsize under half the maxrate", Rendition{VideoBitrate: 4000, MaxrateKbps: 6000, BufsizeKbps: 2000}, []string{"less than half the 6000 kbps ceiling"}},
		{"bufsize too large", Rendition{VideoBitrate: 4000, BufsizeKbps: MaxRenditionBitrate*4 + 1}, []string{"out of range (max"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			expectProblems(t, collect(tc.r.rateControlProblems), tc.want...)
		})
	}
}

func TestRenditionPictureProblems(t *testing.T) {
	cases := []struct {
		name string
		r    Rendition
		want []string
	}{
		{"defaults", Rendition{}, nil},
		{"unknown aspect mode", Rendition{Width: 1280, Height: 720, AspectMode: "squash"}, []string{"unknown aspect mode"}},
		{"aspect with a free axis", Rendition{Width: 1280, AspectMode: "pad"}, []string{"needs both a width and a height"}},
		{"pad colour with a comma", Rendition{PadColor: "black,red"}, []string{"pad colour"}},
		{"unknown deinterlace mode", Rendition{Deinterlace: "sometimes"}, []string{"unknown deinterlace mode"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			expectProblems(t, collect(tc.r.pictureProblems), tc.want...)
		})
	}
}

func TestPlayoutVariantAndSessionProblems(t *testing.T) {
	seen := map[string]bool{}
	got := collect(func(add problemAdder) {
		for _, v := range []PlayoutVariant{
			{Name: "bad name!"},
			{Name: "Main"},
			{Name: "main"},
			{Name: "audio", AudioTrack: 64},
		} {
			v.problems(seen, add)
		}
	})
	expectProblems(t, got, "playout variant name", "duplicate playout variant name", "audio track 64 out of range")

	expectProblems(t, collect(PlayoutSettings{SessionIdleSeconds: 1, MaxSessions: 0}.sessionProblems),
		"session idle timeout", "session cap")
	expectProblems(t, collect(PlayoutSettings{SessionIdleSeconds: 60, MaxSessions: 10}.sessionProblems))
}

func TestPostProdHelperProblems(t *testing.T) {
	expectProblems(t, collect(PostProdSettings{CPUCeilingPercent: 101, CPUResumePercent: -1,
		CPUSustainedSeconds: -1, CPUSettleSeconds: 3601}.cpuProblems),
		"cpu ceiling", "cpu resume level", "cpu sustained window", "cpu settle window")
	expectProblems(t, collect(PostProdSettings{CPUCeilingPercent: 50, CPUResumePercent: 60}.cpuProblems),
		"must be below the ceiling")
	expectProblems(t, collect(PostProdSettings{RetainDays: -1, RetainJobs: -1}.retentionProblems),
		"job history retention", "job history floor")

	seen := map[string]bool{}
	got := collect(func(add problemAdder) {
		for _, k := range []PostProdKindSettings{
			{Kind: " "},
			{Kind: strings.Repeat("k", jobs.MaxKindLen+1)},
			{Kind: "clip", Mode: "whenever"},
			{Kind: "clip"},
			{Kind: "export", Windows: []jobs.Window{{StartMinutes: 60, EndMinutes: 60}}},
		} {
			k.problems(seen, add)
		}
	})
	expectProblems(t, got, "has no job kind", "is longer than", "unknown mode", "duplicate post-production policy",
		"job kind \"export\":")

	kinds := make([]PostProdKindSettings, MaxPostProdKinds+1)
	for i := range kinds {
		kinds[i].Kind = fmt.Sprintf("k%d", i)
	}
	expectProblems(t, collect(PostProdSettings{Kinds: kinds}.kindsProblems), "job kinds (maximum")
}

func TestSettingsSectionProblems(t *testing.T) {
	expectProblems(t, collect(RecordingSettings{SegmentSeconds: 5, MaxGB: -1, MaxAgeHours: -1, MinFreeGB: -1}.problems),
		"segment length", "size cap", "age cap", "free-space floor")
	expectProblems(t, collect(DisplaySettings{TimeZone: "Mars/Olympus"}.problems), "unknown display time zone")
	expectProblems(t, collect(DisplaySettings{TimeZone: "Europe/London"}.problems))
	expectProblems(t, collect(PreviewSettings{SegmentSeconds: 0, VideoHeight: 100, IdleTimeoutSeconds: 1}.problems),
		"preview segment length", "preview height", "preview idle timeout")
	expectProblems(t, collect(AutomodSettings{
		Model:     AutomodModel{Enabled: true, MinConfidence: 2},
		Secondary: AutomodModel{Protocol: "smtp"},
	}.problems), "automod confidence floor", "automod secondary protocol")
}
