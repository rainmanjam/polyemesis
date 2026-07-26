package routing

import "fmt"

// Preset IDs.
const (
	PresetEverything = "everything"
	PresetNoMusic    = "no-music"
	PresetMicOnly    = "mic-only"
	PresetSurround   = "surround-downmix"
)

// Preset is a named starting point for a routing profile.
type Preset struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	// NeedsMusicTrack / NeedsMicTrack / NeedsSurroundTrack tell the UI which
	// track picker to show alongside the preset button.
	NeedsMusicTrack    bool `json:"needsMusicTrack"`
	NeedsMicTrack      bool `json:"needsMicTrack"`
	NeedsSurroundTrack bool `json:"needsSurroundTrack"`
}

// Presets is the catalogue offered in the routing editor.
func Presets() []Preset {
	return []Preset{
		{
			ID:          PresetEverything,
			Name:        "Everything",
			Description: "Sum every audio track the ingest is carrying.",
		},
		{
			ID:              PresetNoMusic,
			Name:            "No music",
			Description:     "Sum every track except the one carrying copyrighted music.",
			NeedsMusicTrack: true,
		},
		{
			ID:            PresetMicOnly,
			Name:          "Mic only",
			Description:   "Just the microphone track. Commentary, no game or media audio.",
			NeedsMicTrack: true,
		},
		{
			ID:                 PresetSurround,
			Name:               "5.1 → stereo",
			Description:        "Downmix one surround track to stereo with standard ITU coefficients, as an editable matrix.",
			NeedsSurroundTrack: true,
		},
	}
}

// PresetOpts carries the track choices a preset needs. Defaults follow the
// common OBS convention: track 1 full mix, track 2 clean mix, track 3 mic.
type PresetOpts struct {
	MusicTrack    int `json:"musicTrack"`    // 0-based
	MicTrack      int `json:"micTrack"`      // 0-based
	SurroundTrack int `json:"surroundTrack"` // 0-based
}

// DefaultPresetOpts returns the OBS-convention defaults.
func DefaultPresetOpts() PresetOpts {
	return PresetOpts{MusicTrack: 0, MicTrack: 2, SurroundTrack: 0}
}

// ApplyPreset builds a profile from a preset against the live source layout.
func ApplyPreset(id string, src Source, opts PresetOpts) (Profile, error) {
	tracks := src.Tracks
	if len(tracks) == 0 {
		tracks = DefaultSource().Tracks
	}

	base := func() Profile {
		p := Profile{Mode: ModeSimple, Normalize: NormAuto, SampleRate: 48000}
		for i := 0; i < MaxTracks; i++ {
			p.Tracks = append(p.Tracks, TrackSel{Track: i, Gain: 1.0})
		}
		return p
	}

	enable := func(p *Profile, idx int) {
		for i := range p.Tracks {
			if p.Tracks[i].Track == idx {
				p.Tracks[i].Enabled = true
			}
		}
	}

	switch id {
	case PresetEverything:
		p := base()
		for _, t := range tracks {
			enable(&p, t.Index)
		}
		return p, nil

	case PresetNoMusic:
		p := base()
		for _, t := range tracks {
			if t.Index == opts.MusicTrack {
				continue
			}
			enable(&p, t.Index)
		}
		if len(p.SelectedTracks()) == 0 {
			return Profile{}, fmt.Errorf("preset %q would leave no audio: the ingest only carries the excluded track", id)
		}
		return p, nil

	case PresetMicOnly:
		p := base()
		enable(&p, opts.MicTrack)
		return p, nil

	case PresetSurround:
		// Emitted as an editable matrix rather than a checkbox, because the
		// point of this preset is to *show* the coefficients so the user can
		// then do things simple mode cannot — e.g. delete the front channels
		// and keep only the rears.
		ch := 6
		if t, ok := src.TrackByIndex(opts.SurroundTrack); ok && t.Channels > 0 {
			ch = t.Channels
		}
		p := Profile{Mode: ModeMatrix, Normalize: NormAuto, SampleRate: 48000}
		p.Matrix = CellsForTrack(opts.SurroundTrack, ch, 1.0)
		for i := 0; i < MaxTracks; i++ {
			p.Tracks = append(p.Tracks, TrackSel{Track: i, Gain: 1.0, Enabled: i == opts.SurroundTrack})
		}
		return p, nil
	}

	return Profile{}, fmt.Errorf("unknown routing preset %q", id)
}
