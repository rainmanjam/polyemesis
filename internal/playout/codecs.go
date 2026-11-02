package playout

import "strings"

// Video codec names, as ffprobe reports them and as the API carries them.
const (
	CodecH264 = "h264"
	CodecHEVC = "hevc"
	CodecAV1  = "av1"
)

// normalizeCodec maps the spellings an upstream may use onto the three names
// the API speaks, and anything else onto "" (unknown).
func normalizeCodec(c string) string {
	switch strings.ToLower(strings.TrimSpace(c)) {
	case "h264", "avc", "avc1":
		return CodecH264
	case "hevc", "h265", "hvc1", "hev1":
		return CodecHEVC
	case "av1", "av01":
		return CodecAV1
	}
	return ""
}

// hlsCodecs is the RFC 8216 CODECS value for a rung: its video codec and the
// AAC audio mapArgs always encodes.
//
// The video profile strings are representative, not measured: video is
// copied, and the real profile and level live in the upstream's SPS, which
// nothing here parses. They name the common case -- H.264 High 4.0, HEVC Main
// 4.0, AV1 Main 8-bit -- which is what a browser's support check needs to
// answer the question that matters, "can this browser decode that codec
// family at all". Audio is listed because a CODECS value that names only video
// tells a player the rung has no audio.
func hlsCodecs(codec string) string {
	var video string
	switch codec {
	case CodecH264:
		video = "avc1.640028"
	case CodecHEVC:
		video = "hvc1.1.6.L120.90"
	case CodecAV1:
		video = "av01.0.08M.08"
	default:
		return ""
	}
	return video + ",mp4a.40.2"
}
