package multitrack

import "fmt"

// ClientName is what polyemesis calls itself to Twitch. Twitch quotes it back
// inside status.html_en_us, so an operator reading "Your broadcast software
// (polyemesis) did not send GPU Information" is reading about the program they
// are actually running. Claiming to be obs-studio would make that sentence a
// lie, and would make polyemesis invisible in whatever Twitch counts.
const ClientName = "polyemesis"

// Ask is what polyemesis wants, expressed in polyemesis's own terms. NewRequest
// turns it into the wire Request.
//
// ------------------------------------------------------------------
// HOW A NEGOTIATED CONFIG RECONCILES WITH THE OPERATOR'S OWN SETTINGS
// ------------------------------------------------------------------
//
// This is the question issue #326 raises and it is a product decision, so it is
// written down here rather than left implicit in the code.
//
// THE OPERATOR'S SETTINGS ARE THE INPUT TO THE NEGOTIATION, NOT SOMETHING IT
// OVERRIDES. That is not a compromise position, it is what the endpoint
// actually does, and it was measured:
//
//   - Asking with a 1920x1080@30 canvas returned a 1080p/720p/360p ladder.
//     Asking with 1280x720@60 returned 720p/480p/360p. The renditions are
//     DERIVED from the canvas the client says it is producing, so an operator
//     who picks 720p gets a 720p negotiation. Their choice is honoured by being
//     asked in the first place.
//
//   - Asking for maximum_video_tracks 1 returned exactly one rendition -- and
//     still returned BOTH audio tracks. So polyemesis, which publishes a single
//     video track to an RTMP destination, does not have to pretend otherwise to
//     get the VOD audio track. It asks for one and gets one.
//
// Where Twitch's answer nonetheless differs from what was asked -- and it does;
// a maximum_aggregate_bitrate of 2500 kbps was simply ignored -- the difference
// is REPORTED, by Reconcile, and never silently applied. That follows the house
// rule already written into services.URLProblem: "Offered rather than applied:
// silently rewriting what somebody typed is how you get a bug report that says
// 'it changed my URL'."
//
// So the contract is: the operator's rendition decides what we ASK for; Twitch
// decides what it will ACCEPT; and any gap between the two is shown to the
// operator rather than resolved on their behalf.
type Ask struct {
	// Version is polyemesis's own version string, sent as client.version.
	Version string
	// Canvas is the composition the operator configured -- their rendition, in
	// this package's terms. It is the field the returned ladder is derived from.
	Canvas Canvas
	// VODAudio asks for the second audio track. This is the switch that
	// populates AudioConfigurations.VOD and it is the whole reason polyemesis
	// makes this call at all.
	VODAudio bool
	// MaxVideoTracks caps the ladder. Zero means DefaultMaxVideoTracks, which is
	// 1, because one video track is what polyemesis publishes to an RTMP
	// destination today -- see db.AudioEncoding.copyProblems. Asking for more
	// renditions than can be published would negotiate a configuration that
	// cannot then be honoured, and Twitch would be right to expect all of them.
	MaxVideoTracks uint32
	// MaxAggregateBitrateKbps is the operator's ceiling across the whole ladder,
	// in the unit the rest of polyemesis states bitrates in. Zero omits it.
	// Twitch was observed to ignore this; it is sent because saying nothing is
	// worse than saying something that is ignored, and because Reconcile can
	// only report an overrun it asked about.
	MaxAggregateBitrateKbps int
	// AudioSampleRate and AudioChannels describe the mix polyemesis will feed
	// in. Zero means the defaults below.
	AudioSampleRate uint32
	AudioChannels   uint32
	// SupportedCodecs is what polyemesis can encode. Empty means
	// DefaultSupportedCodecs.
	SupportedCodecs []string
	// Hardware is this machine's inventory, and IT HAS TO BE REAL. Twitch
	// validates the GPU: no GPU, a vendor ID of zero, an unrecognised vendor and
	// an out-of-date driver were each refused, by name, in testing. Nothing in
	// this package measures hardware -- see Capabilities for why not -- so a
	// caller that cannot supply a supported GPU should expect Refused and should
	// take the fallback. That is a supported outcome, not a bug.
	Hardware Capabilities
}

// Defaults. Named rather than inlined so a test can assert on the value and a
// reader can see there is a decision here.
const (
	// DefaultMaxVideoTracks is 1 because polyemesis publishes one video track to
	// an RTMP destination.
	DefaultMaxVideoTracks = 1
	// DefaultAudioSampleRate matches routing.Profile's compiled output, whose
	// graph ends in aresample=48000.
	DefaultAudioSampleRate = 48000
	// DefaultAudioChannels matches routing.OutChannels: destinations are stereo.
	DefaultAudioChannels = 2
	// defaultAudioMaxBufferingMS is obs-studio's own default. Twitch reads it
	// and nothing observed depends on it; sending obs-studio's value is the only
	// defensible choice for a field whose meaning is not published.
	defaultAudioMaxBufferingMS = 960
)

// DefaultSupportedCodecs is what polyemesis will encode video as. h264 alone,
// because that is what every RTMP destination in the services registry accepts
// and what the FFmpeg encoder path builds today. Claiming av1 would invite a
// ladder polyemesis cannot produce.
func DefaultSupportedCodecs() []string { return []string{"h264"} }

// NewRequest builds the wire body. It deliberately does NOT take the stream
// key: Client.Fetch puts that in, so that exactly one function in this package
// ever handles it.
func NewRequest(a Ask) Request {
	codecs := a.SupportedCodecs
	if len(codecs) == 0 {
		codecs = DefaultSupportedCodecs()
	}
	tracks := a.MaxVideoTracks
	if tracks == 0 {
		tracks = DefaultMaxVideoTracks
	}
	rate := a.AudioSampleRate
	if rate == 0 {
		rate = DefaultAudioSampleRate
	}
	channels := a.AudioChannels
	if channels == 0 {
		channels = DefaultAudioChannels
	}

	prefs := Preferences{
		MaximumVideoTracks:  tracks,
		VODTrackAudio:       a.VODAudio,
		AudioSamplesPerSec:  rate,
		AudioChannels:       channels,
		AudioMaxBufferingMS: defaultAudioMaxBufferingMS,
		Canvases:            []Canvas{a.Canvas},
	}
	if a.MaxAggregateBitrateKbps > 0 {
		// Twitch's field is bits per second; polyemesis states bitrates in kbps
		// everywhere else (db.Destination.AudioBitrate, services.Recommended).
		// The conversion lives here, once, rather than at every call site --
		// which is how a ceiling ends up a thousand times too low.
		prefs.MaximumAggregateBitrate = uint64(a.MaxAggregateBitrateKbps) * 1000
	}

	return Request{
		Service:       ServiceIVS,
		SchemaVersion: SchemaVersion,
		Client: ClientInfo{
			Name:            ClientName,
			Version:         a.Version,
			SupportedCodecs: codecs,
		},
		Capabilities: a.Hardware,
		Preferences:  prefs,
	}
}

// ---------------------------------------------------------------- divergence

// Divergence is one place where what Twitch returned is not what was asked for.
//
// Shaped after services.URLProblem on purpose, including the reason: these are
// advisory findings written for an operator, and the point of having a type for
// them is that they get SHOWN rather than acted on. Nothing in this package
// changes a setting because of one.
type Divergence struct {
	// Field names the thing to blame, in the operator's vocabulary where one
	// exists.
	Field string
	// Detail is written for a person, not for a log parser.
	Detail string
}

// Reconcile reports where the negotiated configuration departs from the Ask.
//
// It is called on a configuration that has already passed Verdict; a refusal
// has nothing to reconcile. An empty result means Twitch agreed to what was
// asked, which was the common case in testing and is not something to celebrate
// in a log line.
func Reconcile(a Ask, c *Config) []Divergence {
	if c == nil {
		return nil
	}
	var out []Divergence
	add := func(field, format string, args ...any) {
		out = append(out, Divergence{Field: field, Detail: fmt.Sprintf(format, args...)})
	}

	wantTracks := a.MaxVideoTracks
	if wantTracks == 0 {
		wantTracks = DefaultMaxVideoTracks
	}
	if got := uint32(len(c.EncoderConfigurations)); got > wantTracks {
		add("renditions", "Twitch negotiated %d video renditions but polyemesis asked for at most %d "+
			"and can publish %d. The extra renditions will not be sent.", got, wantTracks, wantTracks)
	}

	// The FIRST rendition is the one polyemesis would publish, because Twitch
	// returns the ladder highest-first on every measured response. Comparing the
	// whole ladder against one canvas would be comparing the wrong things: the
	// lower rungs are SUPPOSED to be smaller.
	if len(c.EncoderConfigurations) > 0 && a.Canvas.Width > 0 && a.Canvas.Height > 0 {
		top := c.EncoderConfigurations[0]
		if top.Width != a.Canvas.Width || top.Height != a.Canvas.Height {
			add("rendition", "this destination is configured for %dx%d, but Twitch's Enhanced Broadcasting "+
				"configuration asks for %dx%d. The operator's size is what polyemesis will encode; "+
				"Twitch may transcode or refuse it.",
				a.Canvas.Width, a.Canvas.Height, top.Width, top.Height)
		}
		if top.Framerate != nil && a.Canvas.Framerate.Denominator != 0 && top.Framerate.Denominator != 0 {
			want := float64(a.Canvas.Framerate.Numerator) / float64(a.Canvas.Framerate.Denominator)
			got := float64(top.Framerate.Numerator) / float64(top.Framerate.Denominator)
			if want != got {
				add("fps", "this destination is configured for %.3g fps, but Twitch's Enhanced Broadcasting "+
					"configuration asks for %.3g fps.", want, got)
			}
		}
	}

	if a.MaxAggregateBitrateKbps > 0 {
		total := 0
		for _, e := range c.EncoderConfigurations {
			if kbps, ok := e.BitrateKbps(); ok {
				total += kbps
			}
		}
		for _, e := range c.AudioConfigurations.Live {
			if kbps, ok := e.BitrateKbps(); ok {
				total += kbps
			}
		}
		for _, e := range c.AudioConfigurations.VOD {
			if kbps, ok := e.BitrateKbps(); ok {
				total += kbps
			}
		}
		if total > a.MaxAggregateBitrateKbps {
			add("bitrate", "Twitch's Enhanced Broadcasting configuration totals %d kbps across every track, "+
				"above the %d kbps ceiling this destination asked for. Twitch does not treat that ceiling "+
				"as binding.", total, a.MaxAggregateBitrateKbps)
		}
	}

	// The two that matter most to issue #141, stated in both directions. Asking
	// for the VOD track and not getting it is the failure the whole feature
	// turns on; getting one nobody asked for means a mix would have to be routed
	// to it that polyemesis has not been told to produce.
	switch {
	case a.VODAudio && len(c.AudioConfigurations.VOD) == 0:
		add("vodAudio", "this destination asked for a separate VOD audio track and Twitch's configuration "+
			"contains none, so the VOD will carry the live mix.")
	case !a.VODAudio && len(c.AudioConfigurations.VOD) > 0:
		add("vodAudio", "Twitch's configuration contains a VOD audio track that this destination did not "+
			"ask for. It will be left unfed.")
	}

	wantChannels := a.AudioChannels
	if wantChannels == 0 {
		wantChannels = DefaultAudioChannels
	}
	for _, track := range append(append([]AudioEncoderConfig{}, c.AudioConfigurations.Live...),
		c.AudioConfigurations.VOD...) {
		if track.Channels != 0 && track.Channels != wantChannels {
			add("audioChannels", "Twitch asks for %d audio channels on track %d; polyemesis mixes %d.",
				track.Channels, track.TrackID, wantChannels)
		}
	}
	return out
}
