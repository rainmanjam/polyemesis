package playout

import (
	"fmt"
	"strings"
)

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

// hlsCodecs is the RFC 8216 CODECS value for a rung: its exact video codec
// string and the AAC audio mapArgs always encodes -- or "" when the video
// string cannot be stated exactly.
//
// Exact, or nothing. Video is COPIED, so the profile and level are whatever
// the upstream sent; a representative string ("High 4.0" for everything)
// could tell a browser a 10-bit or high-level stream is one it can decode, or
// the reverse. Without CODECS a player still plays the rung, and the page's
// own fallback works from the codec FAMILY the public view carries, so
// omitting the attribute costs nothing a guess would buy. Audio is listed
// with it because a CODECS value naming only video says the rung has no
// audio.
func hlsCodecs(codec, profile string, level int, pixFmt string) string {
	var video string
	switch codec {
	case CodecH264:
		video = avcCodecString(profile, level)
	case CodecHEVC:
		video = hevcCodecString(profile, level)
	case CodecAV1:
		video = av1CodecString(profile, level, pixFmt)
	}
	if video == "" {
		return ""
	}
	return video + ",mp4a.40.2"
}

// avcCodecString is avc1.PPCCLL from ffprobe's profile name and level_idc.
func avcCodecString(profile string, level int) string {
	pc, ok := map[string]string{
		"Constrained Baseline":  "42E0",
		"Baseline":              "4200",
		"Main":                  "4D00",
		"High":                  "6400",
		"High 10":               "6E00",
		"High 4:2:2":            "7A00",
		"High 4:4:4 Predictive": "F400",
	}[profile]
	if !ok || level <= 0 || level > 0xFF {
		return ""
	}
	return fmt.Sprintf("avc1.%s%02X", pc, level)
}

// hevcCodecString is hvc1.<profile>.<compat>.L<level>.B0 for Main and Main 10,
// from ffprobe's profile name and general_level_idc. ffprobe does not report
// the tier, so the Main tier ("L") is assumed; it is the one every consumer
// encoder produces.
func hevcCodecString(profile string, level int) string {
	pc, ok := map[string]string{"Main": "1.6", "Main 10": "2.4"}[profile]
	if !ok || level <= 0 {
		return ""
	}
	return fmt.Sprintf("hvc1.%s.L%d.B0", pc, level)
}

// av1CodecString is av01.P.LLM.DD from ffprobe's profile name, seq_level_idx
// and the pixel format's bit depth.
func av1CodecString(profile string, level int, pixFmt string) string {
	p, ok := map[string]int{"Main": 0, "High": 1, "Professional": 2}[profile]
	if !ok || level < 0 || level > 31 || pixFmt == "" {
		return ""
	}
	depth := "08"
	switch {
	case strings.Contains(pixFmt, "12"):
		depth = "12"
	case strings.Contains(pixFmt, "10"):
		depth = "10"
	}
	return fmt.Sprintf("av01.%d.%02dM.%s", p, level, depth)
}
