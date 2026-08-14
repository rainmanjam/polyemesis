// Package multitrack speaks Twitch Enhanced Broadcasting, which Amazon -- whose
// IVS runs it -- calls Multitrack Video, and which Twitch's own error text calls
// both names within a single response body.
//
// WHAT THIS IS FOR. polyemesis publishes one AAC stereo track to an RTMP
// destination, because that is what RTMP ingests were measured to take (see
// db.AudioEncoding.copyProblems, which refuses a copied multitrack RTMP
// destination in those words). Enhanced Broadcasting is the one path a platform
// has published that takes a SECOND audio track and says what it is for: a VOD
// mix, separate from the live mix, which is the ask in issue #141.
//
// It is reached by asking for it. A client POSTs its hardware, its canvas and
// its preferences to
//
//	https://ingest.twitch.tv/api/v3/GetClientConfiguration
//
// and Twitch answers with the ingest endpoint, the video renditions and the
// audio tracks it wants. That is the whole shape of the feature and it is why
// this package exists as a negotiation rather than as a constant: nothing here
// is knowable ahead of the call.
//
// THREE THINGS MEASURED AGAINST THE LIVE ENDPOINT, each of which shapes the
// code below and none of which is an assumption:
//
//  1. A REFUSAL ARRIVES AS HTTP 200. Every response observed -- valid, invalid,
//     unsupported hardware, unparseable schema version -- was 200. The verdict
//     is `status.result`, and on success the `status` object is ABSENT rather
//     than present saying "success". A client that reads the status code has
//     read the wrong field; see Config.Verdict.
//
//  2. THE INGEST IS A DIFFERENT HOST. ingest.global-contribute.live-video.net,
//     not live.twitch.tv. Everything else polyemesis knows about publishing to
//     Twitch -- oauth.twitchIngestURL, the services registry -- is about the
//     other host and stays true of it.
//
//  3. THE REQUEST AND THE RESPONSE BOTH CARRY A STREAM KEY. `authentication`
//     in the request IS the stream key -- not an OAuth token, which is what the
//     issue expected -- and the response carries one back in
//     ingest_endpoints[].authentication. On a REFUSAL that value is a plain
//     echo of what was sent. On a SUCCESSFUL negotiation it is something else
//     entirely: a 312-character signed key that embeds the negotiated ladder
//     and ends with the original key. See IngestEndpoint.Authentication. Either
//     way a response body is as sensitive as a request body and neither may be
//     logged as it stands; Config.Redacted exists for that and is the only
//     shape of a Config fit to print.
//
// A FOURTH, which is the reason the fallback matters more than it looks:
// Twitch refuses a client with no supported GPU. Measured refusals include "did
// not send GPU Information", "Your GPU is not currently supported" (an Intel
// iGPU, and a vendor ID it did not recognise) and "Your GPU driver version is
// not supported". A headless polyemesis host encoding with libx264 has nothing
// to send that Twitch will accept, so on that host the fallback to the ordinary
// ingest is the NORMAL path, not the exceptional one. It has to be quiet,
// correct, and it has to say what happened.
package multitrack

import (
	"bytes"
	"encoding/json"
	"strings"
)

// ConfigURL is where OBS's services.json points Twitch's
// multitrack_video_configuration_url, and the only endpoint this package talks
// to. It is a const rather than a setting for the reason endpoints.go gives one
// hostname over: a configurable platform host is a partially-redirected
// provider waiting to happen. Client.BaseURL is the test seam.
const ConfigURL = "https://ingest.twitch.tv/api/v3/GetClientConfiguration"

// SchemaVersion is the contract version this package encodes, and it is a
// version Twitch has to recognise: sending one it does not know is answered
// with `status.result: "error"` naming the version back, which was the first
// response this package was ever measured against. Taken from OBS's
// constructGoLivePost, which is the only published statement of a valid value.
//
// Bump it only alongside a re-read of the response types -- the schema version
// is what selects the RESPONSE shape, so changing it without checking the
// fields is how a config silently loses a track.
const SchemaVersion = "2025-01-25"

// ServiceIVS is the `service` discriminator. Twitch's own responses echo
// "IVS" in meta.service regardless of what was sent -- a request naming
// "NOTIVS" came back meta.service "IVS" -- so this is not a field the far end
// validates today. Sent correctly anyway, because a field that is ignored now
// is not a field that is ignored later.
const ServiceIVS = "IVS"

// ---------------------------------------------------------------- the response

// Config is a GetClientConfiguration response. Field names and optionality
// follow obsproject/obs-studio frontend/utility/models/multitrack-video.hpp,
// which is Twitch's own client and therefore the only normative statement of
// this wire format that exists.
type Config struct {
	Meta   Meta    `json:"meta"`
	Status *Status `json:"status,omitempty"`
	// IngestEndpoints is where to publish. Both an RTMP and an RTMPS entry were
	// returned on every measured response, in that order -- which is exactly
	// why Resolve does not take the first one.
	IngestEndpoints []IngestEndpoint `json:"ingest_endpoints"`
	// EncoderConfigurations is the video ladder Twitch chose. It is EMPTY on
	// every refusal, which is what makes an empty ladder meaningful on its own
	// -- see Verdict.
	EncoderConfigurations []VideoEncoderConfig `json:"encoder_configurations"`
	AudioConfigurations   AudioConfigurations  `json:"audio_configurations"`
}

// Meta identifies the negotiation. ConfigID is not decoration: it goes back to
// Twitch on the publish, as a `clientConfigId` query parameter on the stream
// key, and that is how the ingest knows which negotiated ladder is arriving.
// Resolve does that; nothing else should.
type Meta struct {
	Service       string `json:"service"`
	SchemaVersion string `json:"schema_version"`
	ConfigID      string `json:"config_id"`
	// RequiredEncodeResourceEstimatePercent is Twitch's estimate of how much of
	// the client's encode capacity the returned ladder will use. Observed 0 on
	// every refusal and 12 on a one-rendition 1080p30 NVENC negotiation. Carried
	// because it is the only figure in the response that speaks to whether the
	// machine can actually do what was negotiated; nothing acts on it yet.
	RequiredEncodeResourceEstimatePercent int `json:"required_encode_resource_estimate_percent,omitempty"`
}

// StatusResult is the verdict field. The zero value is the ABSENT case, not an
// error case: a successful negotiation omits the whole status object.
type StatusResult string

const (
	// StatusSuccess and the rest are the values obs-studio enumerates. Only
	// StatusError has been observed from the live endpoint; the others are
	// handled because obs-studio handles them, and because the failure mode of
	// an unhandled one is to be read as the zero value and treated as success.
	StatusSuccess StatusResult = "success"
	StatusWarning StatusResult = "warning"
	StatusError   StatusResult = "error"
)

// Status is Twitch's verdict on the request, and HTMLEnUS is the only
// explanation of a refusal that exists -- there is no error code. It is HTML
// and it is English; both are Twitch's choice, and the field name says so.
type Status struct {
	Result StatusResult `json:"result"`
	// HTMLEnUS carries the operator-facing sentence. Real examples, verbatim:
	// "Your GPU is not currently supported by Twitch Enhanced Broadcasting" and
	// "The schema_version (1999-01-01) being used by your broadcast software
	// (obs-studio) is invalid or no longer supported". It quotes fields from the
	// request back, which is why it is scrubbed before it is shown anywhere.
	HTMLEnUS string `json:"html_en_us,omitempty"`
}

// IngestEndpoint is one publish target. URLTemplate carries a literal
// "{stream_key}" placeholder rather than a key -- see Resolve for what is done
// with it, and why not simply substituting it is the right call.
type IngestEndpoint struct {
	Protocol    string `json:"protocol"`
	URLTemplate string `json:"url_template"`
	// Authentication, when present, REPLACES the stream key that was sent, and
	// on a successful negotiation it is REQUIRED, not advisory.
	//
	// This was nearly read the wrong way round, so the measurement is written
	// down. On a REFUSED request the field is a plain echo -- send a key, the
	// same key comes back, send an empty string and the field is absent -- which
	// makes it look like decoration. On a SUCCESSFUL negotiation it is a
	// 312-character minted credential of the form
	//
	//	v1_<64 hex signature>_<8 hex>_<hex-encoded manifest>_<the original key>
	//
	// where the manifest hex decodes to the ladder that was just agreed:
	//
	//	{"v":1,"b":4820,"t":[{"w":1280,"h":720,"b":4500,"c0":1}],
	//	 "a":[{"b":160},{"b":160,"v":1,"t":1}]}
	//
	// -- b the aggregate bitrate, t the video tracks, a the audio tracks, and
	// the second audio entry carrying "v":1 for VOD and "t":1 for its track id.
	// So the negotiated configuration travels to the ingest INSIDE the key. A
	// client that published with the operator's original key would be publishing
	// a ladder the ingest had never agreed to.
	//
	// It is, for that reason, a secret twice over: it is a credential in its own
	// right AND it has the operator's original key as its last segment. Scrubbing
	// the original key out of a log leaves the signature and the manifest behind,
	// so this value has to be registered as a secret in its own right rather than
	// assumed covered.
	Authentication string `json:"authentication,omitempty"`
}

// Framerate is a rational, matching OBS's media_frames_per_second.
type Framerate struct {
	Numerator   uint32 `json:"numerator"`
	Denominator uint32 `json:"denominator"`
}

// VideoEncoderConfig is one rendition Twitch wants.
//
// Type is an OBS ENCODER ID -- "obs_nvenc_h264_tex" on every measured response
// -- not a codec name. That is a real obstacle for polyemesis, which encodes
// with FFmpeg and has no such identifier, and it is why nothing here maps Type
// to an FFmpeg encoder: a mapping guessed from one observed value would be a
// table that looks authoritative and is not. Settings is likewise OBS's
// property bag, keyed by OBS property names ("keyint_sec", "rate_control",
// "multipass"), and translating it is the scoped-out half of this work.
type VideoEncoderConfig struct {
	Type         string     `json:"type"`
	Width        uint32     `json:"width"`
	Height       uint32     `json:"height"`
	Framerate    *Framerate `json:"framerate,omitempty"`
	GPUScaleType string     `json:"gpu_scale_type,omitempty"`
	Colorspace   string     `json:"colorspace,omitempty"`
	Range        string     `json:"range,omitempty"`
	Format       string     `json:"format,omitempty"`
	// BitrateInterpolationPoints stays RAW on purpose. obs-studio types it as
	// free-form JSON, and the live endpoint returned a flat array of four
	// integers. Decoding it into []int would make any other shape Twitch chooses
	// -- an array of objects, say -- fail the unmarshal of the ENTIRE Config,
	// turning a field nothing reads into a total loss of the negotiation. A
	// json.RawMessage cannot fail.
	BitrateInterpolationPoints json.RawMessage `json:"bitrate_interpolation_points,omitempty"`
	Settings                   Settings        `json:"settings,omitempty"`
	CanvasIndex                uint32          `json:"canvas_index"`
}

// AudioEncoderConfig is one audio track. TrackID is the position it occupies in
// the published stream: live audio came back as track_id 0 and the VOD track as
// track_id 1, which is the numbering the second mix has to land on.
type AudioEncoderConfig struct {
	Codec    string   `json:"codec"`
	TrackID  uint32   `json:"track_id"`
	Channels uint32   `json:"channels"`
	Settings Settings `json:"settings,omitempty"`
}

// AudioConfigurations splits the tracks by what they are for. This split is the
// entire point of the feature for polyemesis.
type AudioConfigurations struct {
	Live []AudioEncoderConfig `json:"live"`
	// VOD is populated purely from the request's preferences.vod_track_audio.
	// Measured: asking with vod_track_audio true returns one aac track at
	// track_id 1; asking with it false returns an empty list. It does NOT depend
	// on the account, on a token, or on anything Twitch knows about the channel
	// -- which is the open question issue #326 recorded as unknown, answered.
	VOD []AudioEncoderConfig `json:"vod"`
}

// Settings is an encoder property bag. It is a map rather than a struct because
// its keys are OBS encoder property names, which differ per encoder Type, and a
// struct would silently drop the ones it had not heard of.
type Settings map[string]json.RawMessage

// Int reads a numeric setting. The bool is false for absent and for
// not-a-number alike, because both mean the same thing to a caller: this
// setting cannot be used, so do not use it. Numbers arrive as JSON numbers and
// may legitimately be written 160 or 160.0, so both decode.
func (s Settings) Int(key string) (int, bool) {
	raw, ok := s[key]
	if !ok {
		return 0, false
	}
	// JSON null is checked BEFORE the unmarshal, not after, because
	// json.Unmarshal(null) into a float64 succeeds and leaves the target at its
	// zero value. Without this, a setting Twitch explicitly nulled would read as
	// a stated bitrate of 0 -- and Reconcile adds these up.
	if string(bytes.TrimSpace(raw)) == "null" {
		return 0, false
	}
	var f float64
	if err := json.Unmarshal(raw, &f); err != nil {
		return 0, false
	}
	return int(f), true
}

// BitrateKbps is the one setting every measured audio track carried, named for
// the unit Twitch sends it in. The live and VOD tracks both came back at 160.
func (a AudioEncoderConfig) BitrateKbps() (int, bool) { return a.Settings.Int("bitrate") }

// BitrateKbps is the same for video; measured 6000, 2500 and 500 across a
// three-rendition 1080p30 ladder.
func (v VideoEncoderConfig) BitrateKbps() (int, bool) { return v.Settings.Int("bitrate") }

// ---------------------------------------------------------------- the request

// Request is the POST body. It mirrors obs-studio's GoLiveApi::PostData,
// because there is no other specification of it and the far end validates
// fields that no documentation mentions.
type Request struct {
	Service       string `json:"service"`
	SchemaVersion string `json:"schema_version"`
	// Authentication is THE STREAM KEY. Named as Twitch names it, and flagged
	// here because the name does not say so: this field is a credential, it is
	// what makes the whole Request unloggable, and it is why Client.Fetch takes
	// the key separately and never accepts a pre-built body.
	Authentication string       `json:"authentication"`
	Client         ClientInfo   `json:"client"`
	Capabilities   Capabilities `json:"capabilities"`
	Preferences    Preferences  `json:"preferences"`
}

// ClientInfo names the broadcast software. Twitch quotes Name back inside
// status.html_en_us -- "Your broadcast software (polyemesis) did not send GPU
// Information" -- which is the only reason to send an honest one: an operator
// reading that sentence should see the program they are actually running.
//
// SupportedCodecs was not observed to change the answer: asking with only "av1"
// still returned an h264 ladder. Sent regardless, since a request that lies
// about what it can encode has no defence when that stops being true.
type ClientInfo struct {
	Name            string   `json:"name"`
	Version         string   `json:"version"`
	SupportedCodecs []string `json:"supported_codecs"`
}

// Capabilities is the hardware inventory. GPU IS MANDATORY AND IS VALIDATED:
// omit it and Twitch refuses; send vendor_id 0 and it refuses naming the value;
// send a vendor it does not recognise and it refuses; send an AMD card with an
// old driver and it refuses naming the Mesa version to upgrade to. There is no
// software-encoder path through this endpoint.
//
// Nothing in this package MEASURES any of it. That is deliberate and it is the
// honest boundary: reading a GPU's PCI vendor ID is per-platform work with its
// own failure modes, and a package that guessed would send a plausible-looking
// inventory that was not this machine's. The caller supplies it or the call is
// refused, and a refusal here is survivable -- see Verdict.
type Capabilities struct {
	CPU    CPU    `json:"cpu"`
	Memory Memory `json:"memory"`
	System System `json:"system"`
	GPU    []GPU  `json:"gpu,omitempty"`
}

type CPU struct {
	PhysicalCores int32  `json:"physical_cores"`
	LogicalCores  int32  `json:"logical_cores"`
	Speed         uint32 `json:"speed,omitempty"`
	Name          string `json:"name,omitempty"`
}

type Memory struct {
	Total uint64 `json:"total"`
	Free  uint64 `json:"free"`
}

type System struct {
	Version      string `json:"version"`
	Name         string `json:"name"`
	Build        int    `json:"build"`
	Release      string `json:"release"`
	Revision     string `json:"revision"`
	Bits         int    `json:"bits"`
	ARM          bool   `json:"arm"`
	ARMEmulation bool   `json:"armEmulation"`
}

// GPU is one adapter. VendorID is the PCI vendor ID as a decimal integer --
// 4318 for NVIDIA (0x10DE), 4098 for AMD (0x1002), 32902 for Intel (0x8086) --
// and Twitch validates it against a list it does not publish.
type GPU struct {
	Model                string `json:"model"`
	VendorID             uint32 `json:"vendor_id"`
	DeviceID             uint32 `json:"device_id"`
	DedicatedVideoMemory uint64 `json:"dedicated_video_memory"`
	SharedSystemMemory   uint64 `json:"shared_system_memory"`
	DriverVersion        string `json:"driver_version,omitempty"`
}

// Canvas is the composition the client is producing. This is the field the
// negotiated ladder is derived FROM, which is the answer to how a negotiated
// config reconciles with an operator's own rendition choice -- see NewRequest.
type Canvas struct {
	Width        uint32    `json:"width"`
	Height       uint32    `json:"height"`
	CanvasWidth  uint32    `json:"canvas_width"`
	CanvasHeight uint32    `json:"canvas_height"`
	Framerate    Framerate `json:"framerate"`
}

// Preferences is what the client asks for. Twitch is free to ignore any of it
// and demonstrably ignores some -- a MaximumAggregateBitrate of 2500 kbps still
// returned a ladder totalling 9000 -- so nothing here may be treated as a
// guarantee about the response.
type Preferences struct {
	MaximumAggregateBitrate uint64 `json:"maximum_aggregate_bitrate,omitempty"`
	// MaximumVideoTracks caps the ladder, and unlike the bitrate ceiling it IS
	// honoured: asking for 1 returned exactly one rendition, 2 returned two, 3
	// returned three -- each still alongside BOTH audio tracks. That measurement
	// answers the second thing issue #326 recorded as unknown: multi-rendition
	// video is NOT a precondition of the second audio track. One video track
	// plus a live and a VOD audio track is a configuration Twitch will hand out.
	MaximumVideoTracks uint32 `json:"maximum_video_tracks,omitempty"`
	// VODTrackAudio is the switch that produces AudioConfigurations.VOD. It is
	// the single most important field in this struct for polyemesis.
	VODTrackAudio       bool     `json:"vod_track_audio"`
	CompositionGPUIndex uint32   `json:"composition_gpu_index,omitempty"`
	AudioSamplesPerSec  uint32   `json:"audio_samples_per_sec"`
	AudioChannels       uint32   `json:"audio_channels"`
	AudioMaxBufferingMS uint32   `json:"audio_max_buffering_ms"`
	AudioFixedBuffering bool     `json:"audio_fixed_buffering"`
	Canvases            []Canvas `json:"canvases"`
}

// ---------------------------------------------------------------- redaction

// redactedPlaceholder is what a stream key is replaced BY. It is deliberately
// not empty: a log line reading `authentication: ""` is indistinguishable from
// one where the field was genuinely absent, and telling those apart is the
// whole reason to look at a redacted config.
const redactedPlaceholder = "<stream key>"

// Redacted returns a copy of the config safe to log or to put in an issue.
//
// It exists because Twitch echoes the stream key back in
// ingest_endpoints[].authentication, so the naive thing -- marshal the response
// and log it -- publishes the credential. That is the exact shape of the defect
// in #310 (a refused destination wrote its key to server.log) and #324 (the
// automod endpoint key), and the exact shape this repo has now paid for twice.
//
// The copy is deep enough to matter: IngestEndpoints is reallocated rather than
// aliased, so redacting does not reach back into the caller's Config and blank
// the key it is about to publish with.
func (c *Config) Redacted() *Config {
	if c == nil {
		return nil
	}
	out := *c
	if c.Status != nil {
		s := *c.Status
		out.Status = &s
	}
	out.IngestEndpoints = make([]IngestEndpoint, len(c.IngestEndpoints))
	copy(out.IngestEndpoints, c.IngestEndpoints)
	for i := range out.IngestEndpoints {
		if out.IngestEndpoints[i].Authentication != "" {
			out.IngestEndpoints[i].Authentication = redactedPlaceholder
		}
	}
	return &out
}

// scrub removes secrets from a string about to be shown to somebody.
//
// Every error this package returns goes through it. A *url.Error carries the
// request URL, an unmarshal error can carry a fragment of the body, and
// status.html_en_us quotes request fields back -- three routes by which a key
// that was never meant to be printed becomes a line in a log. Empty secrets are
// skipped, or every message would be shredded into placeholders.
func scrub(s string, secrets ...string) string {
	for _, sec := range secrets {
		if sec == "" {
			continue
		}
		s = strings.ReplaceAll(s, sec, redactedPlaceholder)
	}
	return s
}
