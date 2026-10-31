package db

// YouTubeVerticalTiers are the 9:16 tiers a YouTube vertical half may pick (#7).
//
// They are separate from RenditionPresets on purpose. RenditionPresets is the
// landscape menu every destination sees, and adding portrait entries there would
// change a list other tests and screens count. These tiers are only read by the
// vertical pairing path.
//
// Each tier uses aspect mode "crop", so a landscape source fills the frame and
// shows no bars. A letterboxed vertical copy is the wrong product for a 9:16
// feed. Values are conservative, the same policy as RenditionPresets: an
// under-spec vertical stream is watchable, and an over-spec one is refused.
//
// PROVISIONAL: YouTube's vertical limits are not published in the API. These
// numbers are the starting point, and they must be checked against YouTube's
// own dual-format guidance before a release.
func YouTubeVerticalTiers() []RenditionPreset {
	tier := func(key, label string, w, h, fps, kbps int, note string) RenditionPreset {
		return RenditionPreset{
			Key:   key,
			Label: label,
			Rendition: &Rendition{
				Name:         label,
				Width:        w,
				Height:       h,
				FPS:          fps,
				VideoBitrate: kbps,
				Encoder:      EncoderX264,
				Preset:       "veryfast",
				GOPSeconds:   2,
				AspectMode:   "crop",
				Note:         note + " " + PresetDisclaimer,
			},
		}
	}
	return []RenditionPreset{
		tier("vertical1080p30", "1080x1920 (9:16) 30 fps 4500 kbps", 1080, 1920, 30, 4500,
			"Full-quality vertical copy for a 9:16 YouTube feed."),
		tier("vertical720p30", "720x1280 (9:16) 30 fps 2500 kbps", 720, 1280, 30, 2500,
			"The conservative vertical tier, for an uplink that cannot carry the 1080 copy."),
	}
}
