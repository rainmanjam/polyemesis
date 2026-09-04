package engine

import (
	"fmt"
	"sort"
	"strings"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/services"
)

// AN HEVC OR AV1 INGEST IS STREAM-COPIED TO RTMP DESTINATIONS THAT REJECT IT. #627
//
// Selecting HEVC or AV1 in OBS produces Enhanced RTMP, which this server ingests
// -- internal/rtmpserver is codec-agnostic by construction, "MEDIA IS NEVER
// PARSED HERE" -- and video is then stream-copied end to end. So HEVC in means
// HEVC out to every destination, and FFmpeg will mux it into FLV quite happily
// because Enhanced RTMP defines the mapping. Most mainstream RTMP ingests take
// H.264 only.
//
// internal/ffmpeg/build.go names this failure mode exactly: "A stream that muxes
// cleanly, uploads cleanly and is rejected by the platform is the worst failure
// mode available: it looks correct everywhere the operator can see."
//
// WHY THIS IS A WARNING AND NOT THE CONTROL WE USE FOR OPUS.
//
// db.Validate REFUSES Opus on an RTMP destination at save time, so the bad state
// cannot be stored. That works because the audio codec is a setting the operator
// picked in polyemesis. The video codec is not: it is whatever the ENCODER sends,
// discovered at probe time, long after every destination was saved. There is no
// save to refuse. The reachable rung is to say so at the moment it becomes true,
// before the operator hears it from the platform.
//
// WHAT IS SOURCED AND WHAT IS NOT. services.json carries videoCodecs for four
// services (twitch, youtube, facebook, kick) out of far more presets. Where the
// registry has an opinion this names the platform; where it does not,
// services.Lookup returns false and that is reported as UNKNOWN rather than
// guessed. A confident wrong answer here is worse than no answer: it would send
// an operator to change an encoder setting that was never the problem.

// videoCodecConcern is one destination that may reject the ingest's video codec.
type videoCodecConcern struct {
	dest     string
	platform string
	// accepts is what the registry says the platform takes, empty when it has
	// no entry -- which is "unknown", not "nothing".
	accepts []string
	known   bool
}

// videoCodecConcerns reports the RTMP destinations that a given ingest video
// codec is a problem for, or might be.
//
// Only RTMP: an SRT or file destination carries MPEG-TS or Matroska, neither of
// which cares. h264 concerns nobody, and is the overwhelmingly common case, so
// it returns nothing at all rather than a list of reassurances.
func videoCodecConcerns(codec string, rows []*db.Destination) []videoCodecConcern {
	c := strings.ToLower(strings.TrimSpace(codec))
	if c == "" || c == "h264" || c == "avc1" {
		return nil
	}
	var out []videoCodecConcern
	for _, row := range rows {
		if row == nil || row.Kind != db.DestRTMP {
			continue
		}
		svc, ok := services.Lookup(string(row.Platform))
		if ok && accepts(svc.VideoCodecs, c) {
			continue // the registry says this platform takes it
		}
		out = append(out, videoCodecConcern{
			dest:     row.Name,
			platform: string(row.Platform),
			accepts:  svc.VideoCodecs,
			known:    ok && len(svc.VideoCodecs) > 0,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].dest < out[j].dest })
	return out
}

func accepts(list []string, codec string) bool {
	for _, v := range list {
		if strings.EqualFold(strings.TrimSpace(v), codec) {
			return true
		}
	}
	return false
}

// warnVideoCodec says, at the moment the layout is known, which destinations
// will or may reject what the encoder is sending. #627
func (e *Engine) warnVideoCodec(codec string, rows []*db.Destination) {
	for _, c := range videoCodecConcerns(codec, rows) {
		if c.known {
			e.log.Warn("the ingest video codec is not one this platform accepts; "+
				"the stream will upload and be rejected",
				"dest", c.dest, "platform", c.platform, "ingestCodec", codec,
				"accepts", strings.Join(c.accepts, ","))
			continue
		}
		// SAID AS UNKNOWN, NOT AS A VERDICT. services.json has no entry for this
		// platform, so naming it a failure would be inventing a fact.
		e.log.Warn("the ingest video codec is not H.264 and this platform's support "+
			"is not recorded; if it is rejected, this is why",
			"dest", c.dest, "platform", c.platform, "ingestCodec", codec)
	}
}

// videoCodecSummary renders the same finding for a human, used by the API so the
// console can show it rather than leaving it in a log nobody reads.
func videoCodecSummary(codec string, rows []*db.Destination) string {
	cs := videoCodecConcerns(codec, rows)
	if len(cs) == 0 {
		return ""
	}
	names := make([]string, 0, len(cs))
	for _, c := range cs {
		names = append(names, c.dest)
	}
	return fmt.Sprintf("the encoder is sending %s; %s may be rejected by the platform",
		strings.ToUpper(codec), strings.Join(names, ", "))
}

// warnRenditionAgainstPlatform says, at the moment a destination goes up, where
// its rendition disagrees with what its platform publishes. #661
//
// platforms.go carries researched, dated, sourced figures per preset and
// nothing read them, so a 4K60 rendition at 40 Mbps could be attached to a
// platform publishing 1080p60/12000 with no objection anywhere. The stream is
// then accepted, encoded, published, and dropped by the platform mid-broadcast.
//
// Warning and not refusal, deliberately: the catalogue is a snapshot of someone
// else's documentation -- X's own two pages disagree materially -- so every line
// carries the Source and Checked date and lets the operator judge which is
// stale, theirs or ours.
//
// Per DESTINATION rather than per rendition, because a rendition is shared: one
// figure can be wrong for a destination on a platform that publishes 12000 and
// right for another on a platform that publishes 51000.
func (e *Engine) warnRenditionAgainstPlatform(row *db.Destination) {
	if row == nil || row.RenditionID == nil {
		return
	}
	e.mu.RLock()
	r := e.rends[*row.RenditionID]
	e.mu.RUnlock()
	if r == nil || r.row == nil {
		return
	}
	for _, c := range db.RenditionConcerns(r.row, row.Platform) {
		e.log.Warn("this rendition is outside what the platform publishes",
			"dest", row.Name, "platform", string(row.Platform), "rendition", r.row.Name,
			"field", c.Field, "detail", c.Detail, "source", c.Source, "checked", c.Checked)
	}
}
