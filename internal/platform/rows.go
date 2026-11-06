package platform

const (
	obsSource  = "obsproject/obs-studio plugins/rtmp-services/data/services.json (schema v5), fetched 2026-08-13"
	kickSource = "Kick's own Stream Settings page, Recommended Encoding Settings, fetched 2026-08-13"
)

// API roots. Constants, because callers declare their own constants from them
// and a mutable global is the wrong shape for a host that tests must not
// redirect (they point an instance at a stub instead).
const (
	YouTubeAPIHost = "https://www.googleapis.com"
	YouTubeAPIRoot = YouTubeAPIHost + "/youtube/v3"
	TwitchAPIRoot  = "https://api.twitch.tv/helix"
	KickAPIHost    = "https://api.kick.com"
	XAPIHost       = "https://api.x.com"
	TikTokAPIHost  = "https://open.tiktokapis.com"

	// GraphVersion is the pinned Graph API version. A bump here is the whole
	// change: oauth and chat both build from it. v24.0 is the first version that
	// removed overlay_url, which polyemesis deliberately never sends.
	GraphVersion   = "v24.0"
	GraphAPIHost   = "https://graph.facebook.com"
	GraphAPIRoot   = GraphAPIHost + "/" + GraphVersion
	GraphDialogURL = "https://www.facebook.com/" + GraphVersion + "/dialog/oauth"
)

// Text limits, as constants for the callers that declare their own constants
// from them. Counted in each consumer's unit; see Text.
const (
	YouTubeTitleMax       = 100
	YouTubeDescriptionMax = 5000
	TwitchTitleMax        = 140
	TikTokCaptionMax      = 2200
	XGoLiveTitleMax       = 280

	// TikTokPublishScope is what Direct Post needs, and is also asked for at
	// sign-in.
	TikTokPublishScope = "video.publish"
)

// buildRows is the table. Order is registry order: the platforms polyemesis
// integrates with first, then the ones that exist for one capability, then the
// presets that save as custom and are listed only so their facts have a home.
func buildRows() []Row {
	return []Row{
		{
			Key: Custom, Enum: Custom, Name: "Custom", ShortName: "Custom",
			AcceptsDestinations: true,
			Music:               &Music{Reason: "custom destination"},
		},
		{
			Key: YouTube, Enum: YouTube, Name: "YouTube Live", ShortName: "YouTube",
			Presets: []string{"youtube", "youtube-rtmps"}, PresetsSaveAsEnum: true, AcceptsDestinations: true,
			Guidance: &Guidance{
				Width: 1920, Height: 1080, FPS: 60, KbpsMin: 12000, KbpsMax: 12000, GOPSeconds: 2,
				Source: "https://support.google.com/youtube/answer/2853702", Checked: "2026-08-06",
			},
			Ceilings:    &Ceilings{KeyintSeconds: 2, MaxVideoKbps: 51000, MaxAudioKbps: 160, Source: obsSource},
			VideoCodecs: []string{"h264", "hevc", "av1"}, AudioCodecs: []string{"aac"},
			HealthAPI:  true,
			Music:      &Music{Exclude: true, Reason: "YouTube Content ID", TargetLUFS: LUFSStreaming},
			Moderation: &Moderation{Delete: true, TimeoutBan: true, Order: 1},
			Chat:       Chat{SendMax: 200, SendUnit: Runes, Order: 3},
			Text: Text{
				TitleMax: YouTubeTitleMax, DescriptionMax: YouTubeDescriptionMax, StreamTitleMax: 128,
				MetadataUnit: Runes, PublishUnit: UTF16,
			},
			Limits: []Limit{{
				Kind: LimitArchiveDuration, Hours: 12, Status: LimitDocumented,
				Source: "https://support.google.com/youtube/answer/6247592?hl=en",
			}},
			APIHost: YouTubeAPIHost, APIPath: "/youtube/v3", APIVersion: "v3",
		},
		{
			Key: Twitch, Enum: Twitch, Name: "Twitch", ShortName: "Twitch",
			Presets: []string{"twitch", "twitch-rtmps"}, PresetsSaveAsEnum: true, AcceptsDestinations: true,
			Guidance: &Guidance{
				Width: 1920, Height: 1080, FPS: 60, KbpsMin: 6000, KbpsMax: 6000, GOPSeconds: 2,
				Source: "https://help.twitch.tv/s/article/broadcasting-guidelines", Checked: "2026-08-06",
			},
			Ceilings: &Ceilings{
				KeyintSeconds: 2, MaxVideoKbps: 6000, MaxAudioKbps: 320, X264Opts: "scenecut=0", Source: obsSource,
			},
			VideoCodecs: []string{"h264"}, AudioCodecs: []string{"aac"},
			AudioTracksMax: 1,
			Music:          &Music{Exclude: true, Reason: "Twitch DMCA policy", TargetLUFS: LUFSStreaming},
			Moderation:     &Moderation{Delete: true, TimeoutBan: true, Order: 2},
			Chat:           Chat{SendMax: 500, SendUnit: Runes, Order: 2},
			Text:           Text{TitleMax: TwitchTitleMax, MetadataUnit: Runes},
			Limits: []Limit{{
				Kind: LimitBroadcastDuration, Hours: 48, Status: LimitReported,
				Source: "https://help.twitch.tv/s/article/broadcasting-guidelines?language=en_US",
			}},
			APIHost: "https://api.twitch.tv", APIPath: "/helix", APIVersion: "helix",
		},
		{
			Key: Kick, Enum: Kick, Name: "Kick", ShortName: "Kick",
			Presets: []string{"kick"}, PresetsSaveAsEnum: true, AcceptsDestinations: true,
			PerChannelIngest: true, IVS: true,
			Guidance: &Guidance{
				Width: 1920, Height: 1080, FPS: 60, KbpsMin: 1000, KbpsMax: 8000, GOPSeconds: 2,
				Source: "https://help.kick.com/en/articles/7066931-how-to-stream-on-kick-com", Checked: "2026-08-06",
			},
			Ceilings:    &Ceilings{KeyintSeconds: 2, MaxVideoKbps: 8000, MaxAudioKbps: 160, MaxFps: 60, Source: kickSource},
			VideoCodecs: []string{"h264"}, AudioCodecs: []string{"aac"},
			RefusesVideoCodecs: []string{"hevc"},
			Music:              &Music{Exclude: true, Reason: "Kick DMCA policy", TargetLUFS: LUFSStreaming},
			Moderation:         &Moderation{Delete: true, TimeoutBan: true, Order: 3},
			Chat:               Chat{SendMax: 500, SendUnit: Runes, Order: 1},
			Limits:             []Limit{{Kind: LimitBroadcastDuration, Status: LimitUnverified}},
			APIHost:            KickAPIHost,
		},
		{
			Key: Facebook, Enum: Facebook, Name: "Facebook Live", ShortName: "Facebook",
			Presets: []string{"facebook"}, PresetsSaveAsEnum: true, AcceptsDestinations: true,
			Guidance: &Guidance{
				Width: 1920, Height: 1080, FPS: 60, KbpsMin: 4500, KbpsMax: 9000, GOPSeconds: 2,
				Source: "https://www.facebook.com/business/help/162540111070395", Checked: "2026-08-06",
			},
			Ceilings: &Ceilings{
				KeyintSeconds: 2, MaxVideoKbps: 9000, MaxAudioKbps: 128, MaxFps: 60, Source: obsSource,
			},
			VideoCodecs: []string{"h264"}, AudioCodecs: []string{"aac"},
			Music:      &Music{Exclude: true, Reason: "Facebook Rights Manager", TargetLUFS: LUFSStreaming},
			Moderation: &Moderation{Hide: true, Delete: true, Order: 4},
			Limits: []Limit{{
				Kind: LimitBroadcastDuration, Hours: 8, Status: LimitDocumented,
				Source: "https://developers.facebook.com/docs/live-video-api/reference",
			}},
			APIHost: GraphAPIHost, APIPath: "/" + GraphVersion, APIVersion: GraphVersion,
		},
		{
			Key: X, Enum: X, Name: "X (Twitter) Live", ShortName: "X",
			Presets: []string{"x"}, PresetsSaveAsEnum: true, AcceptsDestinations: true,
			Guidance: &Guidance{
				Width: 1920, Height: 1080, FPS: 60, KbpsMin: 12000, KbpsMax: 12000, GOPSeconds: 3,
				Source: "https://help.x.com/en/using-x/live-studio", Checked: "2026-08-06",
			},
			Moderation:    &Moderation{Delete: true, TimeoutBan: true, Order: 5},
			Chat:          Chat{SendMax: 140, SendUnit: Runes, Order: 4},
			Text:          Text{GoLiveTitleMax: XGoLiveTitleMax},
			PublishScopes: []string{"tweet.write", "media.write"},
			APIHost:       XAPIHost,
		},
		{
			// Rumble's enum exists for chat only; the preset saves as custom.
			Key: Rumble, Enum: Rumble, Name: "Rumble", ShortName: "Rumble",
			Presets: []string{"rumble"}, AcceptsDestinations: true,
			Guidance: &Guidance{
				Width: 1920, Height: 1080, FPS: 60, KbpsMin: 4000, KbpsMax: 6000, GOPSeconds: 2,
				Source: "https://rumble.support/help/livestream-settings", Checked: "2026-08-06",
			},
		},
		{
			Key: Vimeo, Enum: Vimeo, Name: "Vimeo Livestream", ShortName: "Vimeo",
			Presets: []string{"vimeo"}, PresetsSaveAsEnum: true, AcceptsDestinations: true,
		},
		{
			Key: Trovo, Enum: Trovo, Name: "Trovo", ShortName: "Trovo",
			AcceptsDestinations: true, Retired: "Trovo shut down on 30 June 2026",
		},
		{
			// TikTok exists to publish clips; destinations on it are refused.
			Key: TikTok, Enum: TikTok, Name: "TikTok LIVE", ShortName: "TikTok",
			Presets:       []string{"tiktok"},
			Text:          Text{CaptionMax: TikTokCaptionMax},
			PublishScopes: []string{TikTokPublishScope},
			APIHost:       TikTokAPIHost,
		},

		// Presets that save as custom. No Enum: see Row.
		{
			Key: "instagram", Name: "Instagram Live", ShortName: "Instagram",
			Presets: []string{"instagram"},
			Guidance: &Guidance{
				Width: 720, Height: 1280, FPS: 30, KbpsMin: 2250, KbpsMax: 6000,
				Source:  "https://support.restream.io/en/articles/18180232-stream-to-instagram-with-a-stream-key",
				Checked: "2026-11-05",
			},
			Limits: []Limit{{
				Kind: LimitBroadcastDuration, Hours: 1, Status: LimitReported,
				Source: "https://support.restream.io/en/articles/18180232-stream-to-instagram-with-a-stream-key",
			}},
		},
		{
			Key: "linkedin", Name: "LinkedIn Live", ShortName: "LinkedIn",
			Presets: []string{"linkedin"},
			Guidance: &Guidance{
				Width: 1280, Height: 720, FPS: 30, KbpsMin: 3500, KbpsMax: 6000, GOPSeconds: 2,
				Source: "https://www.linkedin.com/help/linkedin/answer/a567498/", Checked: "2026-08-06",
			},
		},
		{
			Key: "dailymotion", Name: "Dailymotion", ShortName: "Dailymotion",
			Presets: []string{"dailymotion"},
			Guidance: &Guidance{
				Width: 1920, Height: 1080, FPS: 60, KbpsMin: 10000, KbpsMax: 10000, GOPSeconds: 2,
				Source:  "https://faq.dailymotion.com/hc/en-us/articles/203655666-Encoding-parameters",
				Checked: "2026-08-06",
			},
		},
		{
			Key: "owncast", Name: "Owncast", ShortName: "Owncast",
			Presets: []string{"owncast"},
			Guidance: &Guidance{
				Width: 1920, Height: 1080, FPS: 60, KbpsMin: 5000, KbpsMax: 5000, GOPSeconds: 2,
				Source: "https://owncast.online/docs/broadcasting/", Checked: "2026-08-06",
			},
		},
	}
}
