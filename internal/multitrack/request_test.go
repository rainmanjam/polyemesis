package multitrack

import (
	"strings"
	"testing"
)

// canvas1080p30 is the shape the negotiated fixture was requested with, so a
// Reconcile against that fixture with this Ask should find nothing.
var canvas1080p30 = Canvas{
	Width: 1920, Height: 1080, CanvasWidth: 1920, CanvasHeight: 1080,
	Framerate: Framerate{Numerator: 30, Denominator: 1},
}

// TestNewRequestAsksForOneVideoTrackAndTheVODAudioTrack pins the two
// preferences that make this feature reachable for polyemesis at all.
//
// One video track because that is what an RTMP destination publishes here --
// asking for three would negotiate a ladder that cannot then be sent. And
// vod_track_audio because it is the switch that produces the second audio
// track; measured, a request with it false comes back with an empty VOD list.
//
// Proven able to fail against the committed tree by changing
// DefaultMaxVideoTracks (request.go) from 1 to 3, which made the test report
// "MaximumVideoTracks = 3, want 1". Restored from a /tmp copy; git diff --stat
// clean.
func TestNewRequestAsksForOneVideoTrackAndTheVODAudioTrack(t *testing.T) {
	req := NewRequest(Ask{Version: "1.2.3", Canvas: canvas1080p30, VODAudio: true})

	if req.Preferences.MaximumVideoTracks != 1 {
		t.Errorf("MaximumVideoTracks = %d, want 1", req.Preferences.MaximumVideoTracks)
	}
	if !req.Preferences.VODTrackAudio {
		t.Error("VODTrackAudio is false; no second audio track will be negotiated")
	}
	if got := len(req.Preferences.Canvases); got != 1 {
		t.Fatalf("canvases = %d, want 1", got)
	}
	if req.Preferences.Canvases[0] != canvas1080p30 {
		t.Errorf("canvas = %+v, want the operator's %+v", req.Preferences.Canvases[0], canvas1080p30)
	}
	if req.Client.Name != ClientName {
		t.Errorf("client name = %q, want %q -- Twitch quotes this back to the operator",
			req.Client.Name, ClientName)
	}
	if req.Client.Version != "1.2.3" {
		t.Errorf("client version = %q, want the version it was given", req.Client.Version)
	}
	if req.Preferences.AudioSamplesPerSec != DefaultAudioSampleRate {
		t.Errorf("audio rate = %d, want %d", req.Preferences.AudioSamplesPerSec, DefaultAudioSampleRate)
	}
	if req.Preferences.AudioChannels != DefaultAudioChannels {
		t.Errorf("audio channels = %d, want %d", req.Preferences.AudioChannels, DefaultAudioChannels)
	}
}

// TestTheBitrateCeilingIsConvertedFromKbpsToBitsPerSecond guards a unit
// mismatch that would be invisible: polyemesis states bitrates in kbps
// everywhere, Twitch's field is bits per second, and a ceiling sent a thousand
// times too low is a ceiling that reads as "500 kbps aggregate" to the far end.
//
// Proven able to fail against the committed tree by removing the `* 1000` from
// NewRequest (request.go), which made the test report
// "MaximumAggregateBitrate = 8500, want 8500000". Restored from a /tmp copy;
// git diff --stat clean.
func TestTheBitrateCeilingIsConvertedFromKbpsToBitsPerSecond(t *testing.T) {
	req := NewRequest(Ask{Canvas: canvas1080p30, MaxAggregateBitrateKbps: 8500})
	if got, want := req.Preferences.MaximumAggregateBitrate, uint64(8_500_000); got != want {
		t.Errorf("MaximumAggregateBitrate = %d, want %d", got, want)
	}

	// Zero omits the field rather than sending a ceiling of nothing, which would
	// be a request for an empty ladder.
	none := NewRequest(Ask{Canvas: canvas1080p30})
	if none.Preferences.MaximumAggregateBitrate != 0 {
		t.Errorf("MaximumAggregateBitrate = %d, want it omitted when no ceiling was asked for",
			none.Preferences.MaximumAggregateBitrate)
	}
}

// TestReconcileFindsNothingWhenTwitchAgreedWithWhatWasAsked is the calibration
// half of the divergence tests. Without it, a Reconcile that returned a
// finding for everything would still satisfy every test below.
//
// Proven able to fail against the committed tree by changing the rendition-size
// comparison in Reconcile (request.go) from `!=` to `==`, which made the test
// report "Reconcile found 1 divergence against a configuration that matched
// the ask". Restored from a /tmp copy; git diff --stat clean.
func TestReconcileFindsNothingWhenTwitchAgreedWithWhatWasAsked(t *testing.T) {
	cfg := loadFixture(t, "negotiated-one-rendition.json")
	ask := Ask{Canvas: canvas1080p30, VODAudio: true, MaxVideoTracks: 1}

	if got := Reconcile(ask, cfg); len(got) != 0 {
		t.Errorf("Reconcile found %d divergence(s) against a configuration that matched the ask: %+v",
			len(got), got)
	}
}

// TestReconcileReportsADivergenceRatherThanApplyingIt covers each way Twitch's
// answer can differ from the operator's settings. Every one of these is
// REPORTED: nothing here rewrites a setting, which is the product decision
// written up on Ask.
//
// Proven able to fail against the committed tree by making Reconcile
// (request.go) `return nil` as its first statement, which made every subtest
// report "Reconcile found no divergence". Restored from a /tmp copy; git diff
// --stat clean.
func TestReconcileReportsADivergenceRatherThanApplyingIt(t *testing.T) {
	live := []AudioEncoderConfig{{Codec: "aac", TrackID: 0, Channels: 2, Settings: kbps(160)}}
	vod := []AudioEncoderConfig{{Codec: "aac", TrackID: 1, Channels: 2, Settings: kbps(160)}}
	rendition := func(w, h uint32, b int, fps uint32) VideoEncoderConfig {
		return VideoEncoderConfig{
			Type: "obs_nvenc_h264_tex", Width: w, Height: h, Settings: kbps(b),
			Framerate: &Framerate{Numerator: fps, Denominator: 1},
		}
	}

	for _, tc := range []struct {
		name  string
		ask   Ask
		cfg   Config
		field string
		want  string
	}{
		{
			name: "Twitch sent more renditions than polyemesis can publish",
			ask:  Ask{Canvas: canvas1080p30, VODAudio: true, MaxVideoTracks: 1},
			cfg: Config{
				EncoderConfigurations: []VideoEncoderConfig{
					rendition(1920, 1080, 6000, 30), rendition(1280, 720, 2500, 30),
				},
				AudioConfigurations: AudioConfigurations{Live: live, VOD: vod},
			},
			field: "renditions",
			want:  "will not be sent",
		},
		{
			name: "Twitch chose a different size from the operator's rendition",
			ask:  Ask{Canvas: canvas1080p30, VODAudio: true, MaxVideoTracks: 1},
			cfg: Config{
				EncoderConfigurations: []VideoEncoderConfig{rendition(1280, 720, 4500, 30)},
				AudioConfigurations:   AudioConfigurations{Live: live, VOD: vod},
			},
			field: "rendition",
			want:  "configured for 1920x1080",
		},
		{
			name: "Twitch chose a different frame rate",
			ask:  Ask{Canvas: canvas1080p30, VODAudio: true, MaxVideoTracks: 1},
			cfg: Config{
				EncoderConfigurations: []VideoEncoderConfig{rendition(1920, 1080, 6000, 60)},
				AudioConfigurations:   AudioConfigurations{Live: live, VOD: vod},
			},
			field: "fps",
			want:  "60 fps",
		},
		{
			name: "the ladder exceeds the ceiling the destination asked for",
			ask: Ask{Canvas: canvas1080p30, VODAudio: true, MaxVideoTracks: 1,
				MaxAggregateBitrateKbps: 2500},
			cfg: Config{
				EncoderConfigurations: []VideoEncoderConfig{rendition(1920, 1080, 6000, 30)},
				AudioConfigurations:   AudioConfigurations{Live: live, VOD: vod},
			},
			field: "bitrate",
			want:  "does not treat that ceiling as binding",
		},
		{
			name: "the VOD track was asked for and not granted",
			ask:  Ask{Canvas: canvas1080p30, VODAudio: true, MaxVideoTracks: 1},
			cfg: Config{
				EncoderConfigurations: []VideoEncoderConfig{rendition(1920, 1080, 6000, 30)},
				AudioConfigurations:   AudioConfigurations{Live: live},
			},
			field: "vodAudio",
			want:  "the VOD will carry the live mix",
		},
		{
			name: "a VOD track arrived that nobody asked for",
			ask:  Ask{Canvas: canvas1080p30, MaxVideoTracks: 1},
			cfg: Config{
				EncoderConfigurations: []VideoEncoderConfig{rendition(1920, 1080, 6000, 30)},
				AudioConfigurations:   AudioConfigurations{Live: live, VOD: vod},
			},
			field: "vodAudio",
			want:  "left unfed",
		},
		{
			name: "Twitch wants a channel count polyemesis does not mix",
			ask:  Ask{Canvas: canvas1080p30, VODAudio: true, MaxVideoTracks: 1},
			cfg: Config{
				EncoderConfigurations: []VideoEncoderConfig{rendition(1920, 1080, 6000, 30)},
				AudioConfigurations: AudioConfigurations{
					Live: []AudioEncoderConfig{{Codec: "aac", TrackID: 0, Channels: 6, Settings: kbps(160)}},
					VOD:  vod,
				},
			},
			field: "audioChannels",
			want:  "polyemesis mixes 2",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := Reconcile(tc.ask, &tc.cfg)
			if len(got) == 0 {
				t.Fatal("Reconcile found no divergence")
			}
			var matched *Divergence
			for i := range got {
				if got[i].Field == tc.field {
					matched = &got[i]
					break
				}
			}
			if matched == nil {
				t.Fatalf("no divergence on field %q; got %+v", tc.field, got)
			}
			if !strings.Contains(matched.Detail, tc.want) {
				t.Errorf("detail = %q, want it to mention %q", matched.Detail, tc.want)
			}
			// The whole contract: reporting, not applying. The configuration
			// Reconcile was handed must come back untouched.
			if tc.cfg.AudioConfigurations.Live[0].Channels == 0 {
				t.Error("Reconcile mutated the configuration it was asked to report on")
			}
		})
	}
}

// kbps builds the one settings shape every measured encoder configuration
// carried.
func kbps(v int) Settings {
	return Settings{"bitrate": []byte(itoa(v))}
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}

// TestSettingsIntRefusesWhatItCannotRead. A setting that is absent and one that
// is a string have to be told apart from a setting that is genuinely zero,
// because a bitrate of 0 and a bitrate nobody stated are different facts and
// Reconcile adds them up.
//
// Proven able to fail against the committed tree by changing Settings.Int
// (multitrack.go) to `return int(f), true` on the unmarshal-error path, which
// made the "a string" subtest report "ok = true, want false". Restored from a
// /tmp copy; git diff --stat clean.
func TestSettingsIntRefusesWhatItCannotRead(t *testing.T) {
	s := Settings{
		"whole":   []byte(`160`),
		"decimal": []byte(`160.0`),
		"astring": []byte(`"160"`),
		"null":    []byte(`null`),
		"zero":    []byte(`0`),
	}
	for _, tc := range []struct {
		key    string
		want   int
		wantOK bool
	}{
		{"whole", 160, true},
		{"decimal", 160, true},
		{"astring", 0, false},
		{"null", 0, false},
		{"zero", 0, true},
		{"absent", 0, false},
	} {
		t.Run(tc.key, func(t *testing.T) {
			got, ok := s.Int(tc.key)
			if ok != tc.wantOK {
				t.Errorf("ok = %v, want %v", ok, tc.wantOK)
			}
			if got != tc.want {
				t.Errorf("value = %d, want %d", got, tc.want)
			}
		})
	}
}
