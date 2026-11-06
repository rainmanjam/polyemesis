package platform

import "testing"

func TestRowsAreUnique(t *testing.T) {
	keys, enums, presets := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, r := range All() {
		if r.Key == "" || r.Name == "" {
			t.Errorf("row %+v needs a key and a name", r)
		}
		if keys[r.Key] {
			t.Errorf("duplicate key %q", r.Key)
		}
		keys[r.Key] = true
		if r.Enum != "" {
			if enums[r.Enum] {
				t.Errorf("duplicate enum %q", r.Enum)
			}
			enums[r.Enum] = true
		}
		for _, p := range r.Presets {
			if presets[p] {
				t.Errorf("preset %q belongs to two rows", p)
			}
			presets[p] = true
		}
	}
}

func TestLookups(t *testing.T) {
	if r, ok := ByEnum(Kick); !ok || r.Key != Kick || !r.IVS || !r.PerChannelIngest {
		t.Errorf("ByEnum(kick) = %+v, %v", r, ok)
	}
	if r, ok := ByPreset("youtube-rtmps"); !ok || r.Enum != YouTube {
		t.Errorf("ByPreset(youtube-rtmps) = %+v, %v", r, ok)
	}
	if _, ok := ByEnum(""); ok {
		t.Error(`ByEnum("") must find nothing`)
	}
	if _, ok := ByEnum("nope"); ok {
		t.Error("an unknown platform has no row")
	}
	if _, ok := ByKey("instagram"); !ok {
		t.Error("instagram has a row even though destinations on it save as custom")
	}
	if r, _ := ByPreset("instagram"); r.Enum != "" {
		t.Error("instagram's row has no enum value")
	}
}

func TestEnumsAreTheOnesDestinationsMayCarry(t *testing.T) {
	want := []string{Custom, YouTube, Twitch, Kick, Facebook, X, Rumble, Vimeo, Trovo}
	got := Enums()
	if len(got) != len(want) {
		t.Fatalf("Enums() = %v, want %v", got, want)
	}
	seen := map[string]bool{}
	for _, e := range got {
		seen[e] = true
	}
	for _, w := range want {
		if !seen[w] {
			t.Errorf("Enums() is missing %q", w)
		}
	}
	if seen[TikTok] {
		t.Error("tiktok exists for clip publishing; destinations on it are refused")
	}
}

func TestAPIRoot(t *testing.T) {
	cases := map[string]string{
		Facebook: "https://graph.facebook.com/v24.0",
		YouTube:  "https://www.googleapis.com/youtube/v3",
		Twitch:   "https://api.twitch.tv/helix",
		Kick:     "https://api.kick.com",
		X:        "https://api.x.com",
		TikTok:   "https://open.tiktokapis.com",
	}
	for enum, want := range cases {
		r, _ := ByEnum(enum)
		if got := r.APIRoot(); got != want {
			t.Errorf("%s APIRoot = %q, want %q", enum, got, want)
		}
	}
}

func TestIsIVSIngestHost(t *testing.T) {
	for host, want := range map[string]bool{
		"fa723fc1b171.global-contribute.live-video.net":  true,
		"ingest.global-contribute.live-video.net.":       true,
		"INGEST.GLOBAL-CONTRIBUTE.LIVE-VIDEO.NET":        true,
		"global-contribute.live-video.net":               false,
		".global-contribute.live-video.net":              false,
		"evilglobal-contribute.live-video.net":           false,
		"ingest.global-contribute.live-video.net.evil.x": false,
		"live.twitch.tv": false,
	} {
		if got := IsIVSIngestHost(host); got != want {
			t.Errorf("IsIVSIngestHost(%q) = %v, want %v", host, got, want)
		}
	}
}
