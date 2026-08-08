package ffmpeg

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// ProbeResult is the parsed shape of the live ingest.
type ProbeResult struct {
	Video *VideoStream  `json:"video"`
	Audio []AudioStream `json:"audio"`
	// DurationSeconds is the container's own duration, and is 0 for a live
	// input -- a relay that never ends has no duration to report, which is the
	// case this type was written for. It is meaningful for a file.
	DurationSeconds float64 `json:"durationSeconds"`
	Raw             string  `json:"-"`
}

// VideoStream describes the (single) video track, which polyemesis only ever
// copies.
type VideoStream struct {
	Codec     string  `json:"codec"`
	Width     int     `json:"width"`
	Height    int     `json:"height"`
	FrameRate float64 `json:"frameRate"`
	Bitrate   int     `json:"bitrate"`
	PixFmt    string  `json:"pixFmt"`
}

// AudioStream describes one ingest audio track.
type AudioStream struct {
	Index      int    `json:"index"` // 0-based among audio streams: the a:N specifier
	Codec      string `json:"codec"`
	Channels   int    `json:"channels"`
	Layout     string `json:"layout"`
	SampleRate int    `json:"sampleRate"`
	Bitrate    int    `json:"bitrate"`
	Language   string `json:"language"`
	Title      string `json:"title"`
}

type ffprobeOutput struct {
	Streams []struct {
		CodecName     string            `json:"codec_name"`
		CodecType     string            `json:"codec_type"`
		Width         int               `json:"width"`
		Height        int               `json:"height"`
		PixFmt        string            `json:"pix_fmt"`
		Channels      int               `json:"channels"`
		ChannelLayout string            `json:"channel_layout"`
		SampleRate    string            `json:"sample_rate"`
		BitRate       string            `json:"bit_rate"`
		AvgFrameRate  string            `json:"avg_frame_rate"`
		Tags          map[string]string `json:"tags"`
	} `json:"streams"`
	Format struct {
		Duration string `json:"duration"`
	} `json:"format"`
}

// Probe inspects a live input and reports its track layout. It is what turns
// "the user configured six tracks" into "the stream actually carries three".
func Probe(ctx context.Context, ffprobeBin, input string, timeoutSeconds int) (*ProbeResult, error) {
	if timeoutSeconds <= 0 {
		timeoutSeconds = 5
	}
	cmd := exec.CommandContext(ctx, ffprobeBin, ProbeArgs(input, timeoutSeconds)...)
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		stderr := ""
		if ok := asExitError(err, &ee); ok {
			stderr = strings.TrimSpace(string(ee.Stderr))
		}
		if stderr != "" {
			return nil, fmt.Errorf("ffprobe %s: %s", input, truncate(stderr, 300))
		}
		return nil, fmt.Errorf("ffprobe %s: %w", input, err)
	}
	return ParseProbe(out)
}

// ProbeFile inspects a file on disk rather than a live relay.
//
// Separate from Probe because ProbeArgs is built for the ingest: it runs the
// input through RelayInputURL, which appends "?fifo_size=…&overrun_nonfatal=1".
// On a UDP URL those are options; on a path they become part of the filename,
// and ffprobe goes looking for a file whose name ends in a query string. It
// also inflates -analyzeduration for a stream that has to be watched before it
// will admit what it carries, which a file does not.
//
// The error is returned verbatim rather than folded into a generic "could not
// read this". Somebody who uploaded the wrong thing is best served by ffprobe's
// own words about it.
func ProbeFile(ctx context.Context, ffprobeBin, path string) (*ProbeResult, error) {
	cmd := exec.CommandContext(ctx, ffprobeBin,
		"-hide_banner",
		"-loglevel", "error",
		"-print_format", "json",
		"-show_streams",
		"-show_format",
		"-i", path,
	)
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		stderr := ""
		if ok := asExitError(err, &ee); ok {
			stderr = strings.TrimSpace(string(ee.Stderr))
		}
		if stderr != "" {
			return nil, fmt.Errorf("%s", truncate(stderr, 300))
		}
		return nil, err
	}
	return ParseProbe(out)
}

// ParseProbe converts ffprobe JSON into a ProbeResult. Split out so it can be
// tested against captured fixtures without a live stream.
func ParseProbe(raw []byte) (*ProbeResult, error) {
	var p ffprobeOutput
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("parse ffprobe output: %w", err)
	}

	res := &ProbeResult{Raw: string(raw), Audio: []AudioStream{}}
	// Absent, empty and "N/A" all mean the same thing here and all parse to
	// zero, which is the honest answer for a live input. A caller that needs to
	// distinguish "no duration" from "zero-length" has a file and should check
	// for a stream instead.
	if d, err := strconv.ParseFloat(strings.TrimSpace(p.Format.Duration), 64); err == nil && d > 0 {
		res.DurationSeconds = d
	}
	audioIdx := 0
	for _, s := range p.Streams {
		switch s.CodecType {
		case "video":
			if res.Video != nil {
				// polyemesis copies exactly one video track; extra ones are
				// ignored rather than treated as an error, because some
				// encoders attach cover art as a second video stream.
				continue
			}
			res.Video = &VideoStream{
				Codec:     s.CodecName,
				Width:     s.Width,
				Height:    s.Height,
				PixFmt:    s.PixFmt,
				FrameRate: parseRational(s.AvgFrameRate),
				Bitrate:   atoi(s.BitRate),
			}
		case "audio":
			a := AudioStream{
				Index:      audioIdx,
				Codec:      s.CodecName,
				Channels:   s.Channels,
				Layout:     s.ChannelLayout,
				SampleRate: atoi(s.SampleRate),
				Bitrate:    atoi(s.BitRate),
			}
			if a.Channels == 0 {
				a.Channels = 2
			}
			if a.Layout == "" {
				a.Layout = layoutName(a.Channels)
			}
			if s.Tags != nil {
				a.Language = s.Tags["language"]
				a.Title = s.Tags["title"]
			}
			res.Audio = append(res.Audio, a)
			audioIdx++
		}
	}
	return res, nil
}

// ChannelLayoutName returns the layout name FFmpeg's aformat filter accepts
// for a given channel count.
//
// These are the exact spellings libavutil parses; "4.0" and "quad" are both
// four channels but only some spellings are accepted in every position, and
// getting one wrong turns into a filter-graph negotiation failure at runtime
// rather than a parse error at startup.
func ChannelLayoutName(channels int) string {
	switch channels {
	case 1:
		return "mono"
	case 2:
		return "stereo"
	case 3:
		return "3.0"
	case 4:
		return "quad"
	case 5:
		return "5.0"
	case 6:
		return "5.1"
	case 7:
		return "6.1"
	case 8:
		return "7.1"
	default:
		// FFmpeg's "N channels, unspecified layout" spelling.
		return strconv.Itoa(channels) + "c"
	}
}

func layoutName(channels int) string {
	switch channels {
	case 1:
		return "mono"
	case 2:
		return "stereo"
	case 3:
		return "3.0"
	case 4:
		return "quad"
	case 5:
		return "5.0"
	case 6:
		return "5.1"
	case 7:
		return "6.1"
	case 8:
		return "7.1"
	default:
		return strconv.Itoa(channels) + " channels"
	}
}

func parseRational(s string) float64 {
	parts := strings.SplitN(s, "/", 2)
	if len(parts) != 2 {
		v, _ := strconv.ParseFloat(s, 64)
		return v
	}
	num, _ := strconv.ParseFloat(parts[0], 64)
	den, _ := strconv.ParseFloat(parts[1], 64)
	if den == 0 {
		return 0
	}
	return num / den
}

func atoi(s string) int {
	v, _ := strconv.Atoi(s)
	return v
}

func asExitError(err error, target **exec.ExitError) bool {
	ee, ok := err.(*exec.ExitError)
	if ok {
		*target = ee
	}
	return ok
}
