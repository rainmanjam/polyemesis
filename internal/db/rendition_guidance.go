package db

import "fmt"

// THE CATALOGUE WAS RESEARCHED AND NOTHING READ IT. #661
//
// platforms.go carries a dated, sourced figure set per destination preset --
// Width, Height, FPS, KbpsMin/KbpsMax, GOPSeconds -- and a Rendition carries the
// matching fields. Nothing compared the two, so an operator could attach a 4K60
// rendition at 40 Mbps to a platform that publishes 1080p60/12000 and the
// console showed no objection. The stream is not refused at configure time: it
// is accepted, encoded, published, and dropped by the platform mid-broadcast,
// and the operator sees a destination that was working and now is not with
// nothing anywhere pointing at the bitrate.
//
// That is the shape every Critical in the readiness audit had: the protective
// data exists and is not connected to the thing it protects.
//
// WARNING, NOT CONTROL, AND DELIBERATELY SO. This catalogue is a snapshot of
// somebody else's documentation. X's own two pages disagree materially -- Live
// Studio says 1080p60 at 12000, the older Media Studio page says 720p60 at 9000
// -- and figures change without notice. An operator who knows better than a
// dated figure must stay able to proceed, so every concern carries its Source
// and Checked date and lets them judge which is stale: the catalogue or their
// choice.
//
// Eleven of the thirty-three presets publish guidance. The rest return nothing,
// which is "no opinion" and not "no problem".

// RenditionConcern is one way a rendition disagrees with what a platform
// publishes. It carries the evidence, because a warning an operator cannot
// check is one they can only obey or ignore.
type RenditionConcern struct {
	Field   string `json:"field"`
	Detail  string `json:"detail"`
	Source  string `json:"source"`
	Checked string `json:"checked"`
}

// RenditionConcerns compares a rendition against a platform's published
// guidance. Nil rendition, unknown platform, or a preset that publishes no
// guidance all yield nothing.
//
// A rendition field of 0 means "keep the source's", and the source is not known
// until something is streaming, so those are skipped rather than guessed at.
func RenditionConcerns(r *Rendition, platform Platform) []RenditionConcern {
	if r == nil {
		return nil
	}
	preset, ok := DestinationPresetByID(string(platform))
	if !ok || preset.Video == nil {
		return nil
	}
	g := preset.Video
	var out []RenditionConcern
	add := func(field, detail string) {
		out = append(out, RenditionConcern{
			Field: field, Detail: detail, Source: g.Source, Checked: g.Checked,
		})
	}

	if g.Width > 0 && r.Width > g.Width {
		add("width", fmt.Sprintf("%s publishes %d wide; this rendition is %d",
			preset.Name, g.Width, r.Width))
	}
	if g.Height > 0 && r.Height > g.Height {
		add("height", fmt.Sprintf("%s publishes %dp; this rendition is %dp",
			preset.Name, g.Height, r.Height))
	}
	if g.FPS > 0 && r.FPS > g.FPS {
		add("fps", fmt.Sprintf("%s publishes %d fps; this rendition is %d",
			preset.Name, g.FPS, r.FPS))
	}

	// THE BITRATE, and the one place the catalogue's own shape matters. Equal
	// Min and Max mean the platform publishes a SINGLE figure rather than a
	// range, so "between 12000 and 12000" would read as a range that happens to
	// be narrow. Said as the single figure it is.
	rate := r.VideoBitrate
	if r.MaxrateKbps > rate {
		rate = r.MaxrateKbps // the ceiling is what the platform will actually see
	}
	switch {
	case g.KbpsMax > 0 && rate > g.KbpsMax:
		if g.KbpsMin == g.KbpsMax {
			add("bitrate", fmt.Sprintf("%s publishes %d kbps; this rendition peaks at %d",
				preset.Name, g.KbpsMax, rate))
		} else {
			add("bitrate", fmt.Sprintf("%s publishes up to %d kbps; this rendition peaks at %d",
				preset.Name, g.KbpsMax, rate))
		}
	case g.KbpsMin > 0 && r.VideoBitrate > 0 && r.VideoBitrate < g.KbpsMin:
		add("bitrate", fmt.Sprintf("%s publishes at least %d kbps; this rendition is %d",
			preset.Name, g.KbpsMin, r.VideoBitrate))
	}

	if g.GOPSeconds > 0 && r.GOPSeconds > 0 && r.GOPSeconds > g.GOPSeconds {
		add("gop", fmt.Sprintf("%s asks for a %gs keyframe interval; this rendition uses %gs",
			preset.Name, g.GOPSeconds, r.GOPSeconds))
	}
	return out
}
