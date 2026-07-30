package engine

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/ffmpeg"
	"github.com/rainmanjam/polyemesis/internal/recording"
	"github.com/rainmanjam/polyemesis/internal/routing"
)

func testRendition(id int64, name string) *db.Rendition {
	return &db.Rendition{
		ID: id, Name: name, Width: 1920, Height: 1080, FPS: 60,
		VideoBitrate: 6000, Encoder: db.EncoderX264, Preset: "veryfast", GOPSeconds: 2,
	}
}

// constantSig stands in for renditionSig so the ref-count tests are about which
// renditions are wanted, not about what would restart one.
func constantSig(*db.Rendition) string { return "sig" }

func TestWantedRenditions(t *testing.T) {
	rows := []*db.Rendition{
		testRendition(1, "1080p60"),
		testRendition(2, "720p30"),
		testRendition(3, "unused"),
	}

	tests := []struct {
		name   string
		counts map[int64]int
		want   []int64
	}{
		{
			name:   "a rendition with no enabled destination gets no process",
			counts: map[int64]int{},
			want:   nil,
		},
		{
			name:   "one enabled destination is enough to start a rendition",
			counts: map[int64]int{2: 1},
			want:   []int64{2},
		},
		{
			name:   "several destinations on one rendition still cost one encode",
			counts: map[int64]int{1: 4},
			want:   []int64{1},
		},
		{
			name:   "each referenced rendition gets its own encode",
			counts: map[int64]int{1: 2, 2: 1},
			want:   []int64{1, 2},
		},
		{
			name:   "a rendition explicitly counted zero does not run",
			counts: map[int64]int{3: 0},
			want:   nil,
		},
		{
			name:   "a count for a rendition that no longer exists is ignored",
			counts: map[int64]int{99: 3},
			want:   nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := wantedRenditions(rows, tt.counts, constantSig)
			ids := make([]int64, 0, len(got))
			for id := range got {
				ids = append(ids, id)
			}
			slices.Sort(ids)
			if !slices.Equal(ids, tt.want) {
				t.Errorf("wantedRenditions() = %v, want %v", ids, tt.want)
			}
		})
	}
}

func TestDiffRenditions(t *testing.T) {
	tests := []struct {
		name      string
		want      map[int64]string
		running   map[int64]string
		wantStart []int64
		wantStop  []int64
	}{
		{
			name:      "the first enabled destination starts the encode",
			want:      map[int64]string{1: "a"},
			running:   map[int64]string{},
			wantStart: []int64{1},
		},
		{
			name:     "the last destination releasing it stops the encode",
			want:     map[int64]string{},
			running:  map[int64]string{1: "a"},
			wantStop: []int64{1},
		},
		{
			name:    "an unchanged rendition is left alone",
			want:    map[int64]string{1: "a", 2: "b"},
			running: map[int64]string{1: "a", 2: "b"},
		},
		{
			name:      "editing a rendition replaces only that encode",
			want:      map[int64]string{1: "a2", 2: "b"},
			running:   map[int64]string{1: "a", 2: "b"},
			wantStart: []int64{1},
			wantStop:  []int64{1},
		},
		{
			// A failed start is recorded with an empty spec so the next
			// reconcile retries it rather than treating it as healthy.
			name:      "a rendition that failed to start is retried",
			want:      map[int64]string{1: "a"},
			running:   map[int64]string{1: ""},
			wantStart: []int64{1},
			wantStop:  []int64{1},
		},
		{
			name:      "an unrelated change touches neither list",
			want:      map[int64]string{1: "a", 3: "c"},
			running:   map[int64]string{1: "a", 2: "b"},
			wantStart: []int64{3},
			wantStop:  []int64{2},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			start, stop := diffRenditions(tt.want, tt.running)
			if !slices.Equal(start, tt.wantStart) {
				t.Errorf("start = %v, want %v", start, tt.wantStart)
			}
			if !slices.Equal(stop, tt.wantStop) {
				t.Errorf("stop = %v, want %v", stop, tt.wantStop)
			}
		})
	}
}

func TestRenditionSig(t *testing.T) {
	base := testRendition(1, "1080p60")

	tests := []struct {
		name    string
		apply   func(*db.Rendition)
		restart bool
	}{
		{"renaming a rendition does not cycle the encode", func(r *db.Rendition) { r.Name = "renamed" }, false},
		{"editing the note does not cycle the encode", func(r *db.Rendition) { r.Note = "for twitch" }, false},
		{"a resolution change restarts the encode", func(r *db.Rendition) { r.Height = 720 }, true},
		{"a width change restarts the encode", func(r *db.Rendition) { r.Width = 1280 }, true},
		{"a frame rate change restarts the encode", func(r *db.Rendition) { r.FPS = 30 }, true},
		{"a bitrate change restarts the encode", func(r *db.Rendition) { r.VideoBitrate = 4500 }, true},
		{"an encoder change restarts the encode", func(r *db.Rendition) { r.Encoder = db.EncoderNVENCH264 }, true},
		{"a preset change restarts the encode", func(r *db.Rendition) { r.Preset = "fast" }, true},
		{"a keyframe interval change restarts the encode", func(r *db.Rendition) { r.GOPSeconds = 4 }, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			changed := *base
			tt.apply(&changed)
			same := renditionSig(base, 60, "", "") == renditionSig(&changed, 60, "", "")
			if same == tt.restart {
				t.Errorf("signature equal = %v, want %v", same, !tt.restart)
			}
		})
	}
}

func TestRenditionSigFollowsSourceFPSOnlyWhenInherited(t *testing.T) {
	tests := []struct {
		name    string
		fps     int
		restart bool
	}{
		{
			// The keyframe interval is configured in seconds and emitted in
			// frames, so a rendition inheriting the source rate is running with
			// a stale GOP until it is restarted.
			name:    "learning the ingest rate restarts a rendition that inherits it",
			fps:     0,
			restart: true,
		},
		{
			name:    "a rendition that pins its own rate ignores the ingest's",
			fps:     60,
			restart: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := testRendition(1, "tier")
			r.FPS = tt.fps
			same := renditionSig(r, 0, "", "") == renditionSig(r, 60, "", "")
			if same == tt.restart {
				t.Errorf("signature equal across source rates = %v, want %v", same, !tt.restart)
			}
		})
	}
}

func testDestination(id int64, rendition *int64) *db.Destination {
	return &db.Destination{
		ID: id, Name: "twitch", Kind: db.DestRTMP,
		URL: "rtmp://ingest.example/app", StreamKey: "key", Enabled: true,
		AudioBitrate: 160, RenditionID: rendition,
		Profile: routing.Profile{SampleRate: 48000},
	}
}

func TestDestSpec(t *testing.T) {
	one, two := int64(1), int64(2)
	compiled := routing.Result{FilterComplex: "[0:a:0]anull[out]", OutLabel: "[out]"}

	tests := []struct {
		name     string
		mutate   func(*db.Destination) *db.Destination
		upstream string
		restart  bool
	}{
		{
			name:    "renaming a destination does not restart it",
			mutate:  func(d *db.Destination) *db.Destination { d.Name = "twitch main"; return d },
			restart: false,
		},
		{
			// A destination doing -c:v copy off a different encode is reading a
			// different relay and a different bitstream.
			name:    "moving a destination onto a rendition restarts it",
			mutate:  func(d *db.Destination) *db.Destination { d.RenditionID = &one; return d },
			restart: true,
		},
		{
			name:    "changing the stream key restarts it",
			mutate:  func(d *db.Destination) *db.Destination { d.StreamKey = "rotated"; return d },
			restart: true,
		},
		{
			name:    "changing the audio bitrate restarts it",
			mutate:  func(d *db.Destination) *db.Destination { d.AudioBitrate = 320; return d },
			restart: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			base := destSpec(testDestination(7, nil), compiled, "")
			got := destSpec(tt.mutate(testDestination(7, nil)), compiled, tt.upstream)
			if (base == got) == tt.restart {
				t.Errorf("spec equal = %v, want %v", base == got, !tt.restart)
			}
		})
	}

	t.Run("moving between two renditions restarts the destination", func(t *testing.T) {
		a := destSpec(testDestination(7, &one), compiled, "sig-a")
		b := destSpec(testDestination(7, &two), compiled, "sig-b")
		if a == b {
			t.Error("spec unchanged across renditions; the destination would keep reading the old relay")
		}
	})

	t.Run("editing a rendition restarts the destinations downstream of it", func(t *testing.T) {
		before := destSpec(testDestination(7, &one), compiled, "sig-a")
		after := destSpec(testDestination(7, &one), compiled, "sig-a2")
		if before == after {
			t.Error("spec unchanged after the rendition was edited; the destination would copy a stream that no longer exists")
		}
	})

	t.Run("an unrelated rendition edit leaves a passthrough destination alone", func(t *testing.T) {
		before := destSpec(testDestination(7, nil), compiled, "")
		after := destSpec(testDestination(7, nil), compiled, "")
		if before != after {
			t.Error("passthrough destination spec changed; it would be cycled by an edit it does not read")
		}
	})
}

// TestRenditionSpecOfIsVideoOnly is the guard on the rule the whole feature
// rests on. A rendition that mixed or re-encoded audio would destroy
// per-destination audio routing, so the mapping is pinned here as well as in
// the builder.
func TestRenditionSpecOfIsVideoOnly(t *testing.T) {
	r := testRendition(1, "1080p60")
	spec := renditionSpecOf(r, "udp://127.0.0.1:21000", "udp://127.0.0.1:21001", 59.94, "", "")

	if spec.Width != 1920 || spec.Height != 1080 {
		t.Errorf("size = %dx%d, want 1920x1080", spec.Width, spec.Height)
	}
	if spec.FPS != 60 {
		t.Errorf("FPS = %v, want 60", spec.FPS)
	}
	if spec.SourceFPS != 59.94 {
		t.Errorf("SourceFPS = %v, want the probed rate", spec.SourceFPS)
	}
	if spec.VideoKbps != 6000 {
		t.Errorf("VideoKbps = %d, want the rendition's video bitrate", spec.VideoKbps)
	}
	if spec.Encoder != string(db.EncoderX264) || spec.Preset != "veryfast" {
		t.Errorf("encoder = %q preset = %q, want the rendition's", spec.Encoder, spec.Preset)
	}
	if spec.GOPSeconds != 2 {
		t.Errorf("GOPSeconds = %v, want 2", spec.GOPSeconds)
	}

	args := strings.Join(ffmpeg.RenditionArgs(spec), " ")
	if !strings.Contains(args, "-c:a copy") {
		t.Fatalf("rendition does not copy audio through: %s", args)
	}
	for _, banned := range []string{"-c:a aac", "-filter_complex", "-b:a", "amerge", "pan="} {
		if strings.Contains(args, banned) {
			t.Errorf("rendition command line contains %q; audio must be copied, never mixed or encoded: %s", banned, args)
		}
	}
}

func TestProbedFPS(t *testing.T) {
	tests := []struct {
		name  string
		video *ffmpeg.VideoStream
		want  float64
	}{
		{"an unprobed ingest reports no rate", nil, 0},
		{"a probed ingest reports its rate", &ffmpeg.VideoStream{FrameRate: 59.94}, 59.94},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := probedFPS(tt.video); got != tt.want {
				t.Errorf("probedFPS() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestVAAPIRenditionsCarryTheChosenRenderNode(t *testing.T) {
	// Until this was wired, GPUInfo.VAAPIDevice was computed at detection and
	// then never asked for, so every VAAPI rendition fell back to FFmpeg's
	// default node and a machine with a second GPU could not be pointed at it.
	spec := renditionSpecOf(
		&db.Rendition{Name: "vaapi", Width: 1280, Height: 720, VideoBitrate: 3000,
			Encoder: db.EncoderVAAPIH264},
		"udp://127.0.0.1:1", "udp://127.0.0.1:2", 30, "/dev/dri/renderD129", "")
	if spec.VAAPIDevice != "/dev/dri/renderD129" {
		t.Errorf("VAAPIDevice = %q, want the node that was chosen", spec.VAAPIDevice)
	}
	args := ffmpeg.RenditionArgs(spec)
	if got := strings.Join(args, " "); !strings.Contains(got, "renderD129") {
		t.Errorf("the render node never reached the command line: %s", got)
	}
}

func TestNonVAAPIRenditionsCarryNoRenderNode(t *testing.T) {
	// Software and every other hardware family must be untouched by this.
	spec := renditionSpecOf(
		&db.Rendition{Name: "x264", Width: 1280, Height: 720, VideoBitrate: 3000,
			Encoder: db.EncoderX264},
		"udp://127.0.0.1:1", "udp://127.0.0.1:2", 30, "", "")
	if spec.VAAPIDevice != "" {
		t.Errorf("VAAPIDevice = %q on a software rendition, want empty", spec.VAAPIDevice)
	}
}

func TestDeinterlaceModeReachesTheSpec(t *testing.T) {
	spec := renditionSpecOf(
		&db.Rendition{Name: "sdi", Width: 1280, Height: 720, VideoBitrate: 3000,
			Encoder: db.EncoderX264, Deinterlace: "auto"},
		"udp://127.0.0.1:1", "udp://127.0.0.1:2", 30, "", "")
	if spec.Deinterlace != ffmpeg.DeinterlaceAuto {
		t.Errorf("Deinterlace = %q, want auto", spec.Deinterlace)
	}
}

// The signature is what decides whether a saved change reaches the running
// encoder. A field that changes the filter graph and is absent from the
// signature is stored, shown in the UI, and never encoded -- and nothing
// anywhere reports it.
//
// Deinterlace was exactly that until the overlay work went looking, which is
// why this test names every field rather than only the new ones.
func TestEveryFilterChangingFieldIsInTheRenditionSignature(t *testing.T) {
	base := func() *db.Rendition {
		return &db.Rendition{
			Name: "tier", Width: 1920, Height: 1080, FPS: 60, VideoBitrate: 6000,
			Encoder: db.EncoderX264, Preset: "veryfast", GOPSeconds: 2,
			Overlay: db.RenditionOverlay{
				Image: "overlays/logo.png", Anchor: "bottom-right",
				WidthPct: 0.12, MarginXPct: 0.04, MarginYPct: 0.04, Opacity: 1,
			},
		}
	}
	for _, tc := range []struct {
		name string
		mut  func(*db.Rendition)
	}{
		{"deinterlace", func(r *db.Rendition) { r.Deinterlace = "all" }},
		{"aspect mode", func(r *db.Rendition) { r.AspectMode = "crop" }},
		{"pad colour", func(r *db.Rendition) { r.AspectMode, r.PadColor = "pad", "0x101010" }},
		{"overlay image", func(r *db.Rendition) { r.Overlay.Image = "overlays/other.png" }},
		{"overlay anchor", func(r *db.Rendition) { r.Overlay.Anchor = "top-left" }},
		{"overlay width", func(r *db.Rendition) { r.Overlay.WidthPct = 0.25 }},
		{"overlay margin x", func(r *db.Rendition) { r.Overlay.MarginXPct = 0.1 }},
		{"overlay margin y", func(r *db.Rendition) { r.Overlay.MarginYPct = 0.1 }},
		{"overlay opacity", func(r *db.Rendition) { r.Overlay.Opacity = 0.5 }},
		{"overlay removed", func(r *db.Rendition) { r.Overlay = db.RenditionOverlay{} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := renditionSig(base(), 60, "", "")
			changed := base()
			tc.mut(changed)
			if renditionSig(changed, 60, "", "") == before {
				t.Errorf("changing the %s does not change the signature, so the "+
					"encoder is never restarted and the change is saved but never encoded", tc.name)
			}
		})
	}
}

// Replacing the image file changes the encode without changing a database row.
// Without the stat the running encoder keeps compositing the old picture until
// something unrelated restarts it.
func TestReplacingTheOverlayFileChangesTheSignature(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "overlays"), 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "overlays", "logo.png")
	r := &db.Rendition{
		Name: "tier", Width: 1920, Height: 1080, VideoBitrate: 6000,
		Encoder: db.EncoderX264, GOPSeconds: 2,
		Overlay: db.RenditionOverlay{Image: "overlays/logo.png", Anchor: "top-left", WidthPct: 0.12},
	}

	missing := renditionSig(r, 60, "", dir)
	if err := os.WriteFile(path, []byte("first"), 0o644); err != nil {
		t.Fatal(err)
	}
	present := renditionSig(r, 60, "", dir)
	if present == missing {
		t.Error("the signature did not change when the image appeared, so an encode " +
			"started before the file was uploaded would never pick it up")
	}

	// A different file at the same path, with a different size.
	if err := os.WriteFile(path, []byte("second and longer"), 0o644); err != nil {
		t.Fatal(err)
	}
	if renditionSig(r, 60, "", dir) == present {
		t.Error("replacing the image did not change the signature, so the encoder " +
			"keeps compositing the old picture")
	}
}

// The stored path is relative and must only become absolute at process-build
// time, exactly as the slate image does.
func TestOverlayPathIsJoinedToTheDataDirectoryOnlyAtBuildTime(t *testing.T) {
	spec := overlaySpecOf(db.RenditionOverlay{
		Image: "overlays/logo.png", Anchor: "top-left", WidthPct: 0.12, Opacity: 1,
	}, "/var/lib/polyemesis")
	if spec == nil {
		t.Fatal("an active overlay produced no spec")
	}
	if want := filepath.Join("/var/lib/polyemesis", "overlays", "logo.png"); spec.ImagePath != want {
		t.Errorf("ImagePath = %q, want %q", spec.ImagePath, want)
	}
	if overlaySpecOf(db.RenditionOverlay{}, "/var/lib/polyemesis") != nil {
		t.Error("an empty overlay produced a spec, which would add a second -i to every encode")
	}
}

// A row saved before opacity existed stores 0. Rendering that as fully
// transparent would give the operator an invisible watermark that is
// indistinguishable from a broken one.
func TestZeroOpacityIsTreatedAsOpaque(t *testing.T) {
	spec := overlaySpecOf(db.RenditionOverlay{
		Image: "overlays/logo.png", Anchor: "top-left", WidthPct: 0.12, Opacity: 0,
	}, "/data")
	if spec == nil || spec.Opacity != 1 {
		t.Errorf("Opacity = %v, want 1", spec.Opacity)
	}
}

// The acceptance-audio flake, pinned.
//
// Until the ingest is probed, e.source is routing.DefaultSource() -- six
// placeholder tracks that exist so the routing editor has something to render.
// Planning stems from it makes the recorder ask FFmpeg to map audio streams
// that do not exist, and FFmpeg treats that as fatal rather than skipping them:
//
//	Stream map '0:a:3' matches no streams.
//
// The recorder then crash-loops until a later reconcile replaces the plan.
func TestStemsAreNotPlannedBeforeTheIngestIsProbed(t *testing.T) {
	rec := db.RecordingSettings{Enabled: true, Stems: true, StemCodec: ffmpeg.StemFLAC}

	if plan := stemPlanFor(rec, routing.DefaultSource(), false); len(plan) != 0 {
		t.Errorf("planned %d stems from the UNPROBED placeholder layout: %v. "+
			"Every one of those becomes a -map for a stream that may not exist, "+
			"and FFmpeg refuses to start rather than skipping it.", len(plan), plan)
	}

	// The positive case. Without it this would be satisfied by never planning
	// stems at all, which would remove the feature rather than fix the bug.
	probedSrc := routing.Source{Tracks: []routing.Track{
		{Index: 0, Channels: 2, Codec: "aac", Layout: "stereo"},
		{Index: 1, Channels: 2, Codec: "aac", Layout: "stereo"},
		{Index: 2, Channels: 2, Codec: "aac", Layout: "stereo"},
	}}
	plan := stemPlanFor(rec, probedSrc, true)
	if len(plan) != 3 {
		t.Fatalf("planned %d stems for a probed 3-track ingest, want 3", len(plan))
	}
	for _, st := range plan {
		if st.Track < 0 || st.Track >= 3 {
			t.Errorf("stem %q maps track %d, which the probed ingest does not have",
				st.Name, st.Track)
		}
	}

	// Stems switched off means no plan, probed or not.
	off := rec
	off.Stems = false
	if plan := stemPlanFor(off, probedSrc, true); len(plan) != 0 {
		t.Errorf("planned %d stems with stems disabled", len(plan))
	}
}

// The reason the guard has to exist at all: the placeholder layout claims six
// tracks, so a plan built from it references streams a normal ingest lacks.
func TestTheUnprobedPlaceholderLayoutWouldMapTracksThatDoNotExist(t *testing.T) {
	plan := recording.PlanStems(routing.DefaultSource(), ffmpeg.StemFLAC)
	if len(plan) <= 3 {
		t.Skipf("DefaultSource now yields %d tracks; the hazard this documents is gone", len(plan))
	}
	var beyond int
	for _, st := range plan {
		if st.Track >= 3 {
			beyond++
		}
	}
	if beyond == 0 {
		t.Error("expected the placeholder layout to reference tracks beyond a typical ingest")
	}
	t.Logf("the placeholder layout plans %d stems, %d of them beyond a 3-track ingest",
		len(plan), beyond)
}
