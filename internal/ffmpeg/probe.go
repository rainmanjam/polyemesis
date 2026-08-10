package ffmpeg

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// ProbeResult is the parsed shape of the live ingest.
type ProbeResult struct {
	Video *VideoStream  `json:"video"`
	Audio []AudioStream `json:"audio"`
	// FormatName is ffprobe's format.format_name verbatim: the comma-joined
	// list of names the chosen demuxer registers under, e.g.
	// "mov,mp4,m4a,3gp,3g2,mj2" for an MP4 and "matroska,webm" for an MKV.
	// ProbeFile gates on it; Probe reports it and nothing reads it.
	FormatName string `json:"formatName"`
	// DurationSeconds is the container's own duration, and is 0 for a live
	// input -- a relay that never ends has no duration to report, which is the
	// case this type was written for. It is meaningful for a file.
	DurationSeconds float64 `json:"durationSeconds"`
}

// There was a Raw string here holding ffprobe's whole JSON reply, and nothing
// in the repository ever read it. It was not free: ffprobe's output scales with
// the STREAM COUNT rather than the file size, and a legitimate, allowlisted
// Matroska carrying 300 audio tracks was measured producing 464 KB of JSON from
// a 121 KB container -- 3.8x, before this field kept a second copy of it in the
// same process. Removing it halves that, and probeStdoutCap bounds what is left.

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
		Duration   string `json:"duration"`
		FormatName string `json:"format_name"`
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

// ErrIndirectContainer is returned by ProbeFile for a file whose "streams" are
// not in the file at all -- a playlist or script that names other inputs.
//
// A sentinel rather than a message because the caller answers an HTTP request
// and needs to tell this apart from "ffprobe could not read your file": these
// are bytes ffprobe read perfectly well and reported somebody ELSE's streams
// for. Use errors.Is; the wrapped text carries the format name.
var ErrIndirectContainer = errors.New("this file names other files instead of carrying media")

// ErrUnsupportedContainer is returned by ProbeFile for a format that is real,
// self-contained media and simply is not on the allowlist.
//
// SPLIT OUT BECAUSE THE OTHER SENTENCE WAS A LIE ABOUT MOST OF THE FILES IT WAS
// SHOWN FOR. ErrIndirectContainer was returned for EVERY format the allowlist
// did not name, and the handler renders it as "this file is a playlist or
// script naming other files, not media itself". Measured on files muxed for the
// purpose: AIFF, DV, y4m, IVF, CAF and GIF all got that sentence. Not one of
// them names another file. An operator handed it about their own camera's DV
// footage is being told something untrue about their file, by the feature whose
// entire purpose is that the product stops asserting things about uploads it
// has not established -- and the honest answer, "polyemesis does not accept
// this container", is also the actionable one, because it says the fix is a
// remux rather than a hunt for a script that does not exist.
//
// The allowlist is still the gate and it still fails closed. What changed is
// which of the two sentences a refusal gets.
var ErrUnsupportedContainer = errors.New("polyemesis does not accept this container format")

// indirectFormats are the demuxer names whose whole job is resolving a NAME to
// bytes somewhere else. They are the reason selfContainedFormats is an
// allowlist, and naming them here buys exactly one thing: a refusal that says
// what is actually wrong.
//
// IT IS NOT A DENYLIST AND MUST NEVER BECOME ONE. Anything not on
// selfContainedFormats is refused whether or not it appears below; this map
// only chooses the WORDING. A demuxer this list has never heard of is still
// refused, which is the property a denylist cannot have.
var indirectFormats = map[string]bool{
	"concat": true, "hls": true, "applehttp": true, "dash": true,
	"sdp": true, "rtsp": true, "image2": true, "m3u8": true,
}

// indirect reports whether ffprobe's format_name names a known indirection
// demuxer. Element-wise, for the same reason selfContained is.
func indirect(formatName string) bool {
	for _, n := range strings.Split(formatName, ",") {
		if indirectFormats[strings.TrimSpace(n)] {
			return true
		}
	}
	return false
}

// selfContainedFormats is the set of demuxer names ProbeFile will admit.
//
// AN ALLOWLIST, and that direction is the whole point. The alternative --
// denying "concat" and whatever else we can name today -- is a guard over a set
// we do not control: the demuxers a given FFmpeg build auto-detects are a
// property of that build, and a denylist silently opens every time somebody
// links in a new one. What we can state is the closed set of formats whose
// streams live in the bytes we were handed.
//
// Every entry was produced by muxing a file with this repo's own FFmpeg and
// reading back format.format_name, not from memory. format_name is the
// comma-joined list of names one demuxer registers under, so this is checked
// element-wise: an MP4 reports "mov,mp4,m4a,3gp,3g2,mj2" and matches on "mp4".
//
// Widening it is fine and expected -- an operator refused a legitimate .avi
// should get an entry here. Widening it with an indirection demuxer (concat,
// hls, dash, sdp, image2, or anything else that resolves a NAME to bytes) is
// the one change that must not be made, because that is the hole this closes.
var selfContainedFormats = map[string]bool{
	// Proven by execution: muxed with ffmpeg, format_name read back with
	// ffprobe. Containers.
	"mov": true, "mp4": true, "m4a": true, "3gp": true, "3g2": true, "mj2": true,
	"matroska": true, "webm": true, "mpegts": true, "flv": true, "avi": true,
	"asf": true, "ogg": true, "mpeg": true, "mxf": true, "nut": true, "wtv": true,
	// Bare audio files.
	"wav": true, "flac": true, "aac": true, "mp3": true, "ac3": true, "eac3": true,
	// Raw elementary streams. An operator handed a .h264 dump by an encoder
	// has a real file, and it is as self-contained as a container is.
	"h264": true, "hevc": true, "mpegvideo": true,
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
// TWO THINGS ARE PINNED IN THE ARGV, and neither was here before, and the
// argument for each is different.
//
// -protocol_whitelist file bounds what a demuxer may OPEN. Without it, what
// this reaches is whatever protocols the build enables. This is a pin, not a
// fix: it was measured that no shape tested (HLS, ffconcat, http) actually
// reached a canary listener on this build either way, so the flag changes no
// observed behaviour here -- it stops the absence of that behaviour from being
// an accident of the build.
//
// The format check below bounds what a demuxer may BE, and it is a fix. It was
// measured that -protocol_whitelist file alone leaves the hole wide open: a
// 44-byte text file starting "ffconcat version 1.0" is still read by the concat
// demuxer, which opens the sibling upload it names and reports THAT file's
// h264/aac streams and duration as this file's. Protocol whitelisting does not
// touch it, because "file" is exactly the protocol it uses.
//
// The error is returned verbatim rather than folded into a generic "could not
// read this". Somebody who uploaded the wrong thing is best served by ffprobe's
// own words about it.
func ProbeFile(ctx context.Context, ffprobeBin, path string) (*ProbeResult, error) {
	cmd := exec.CommandContext(ctx, ffprobeBin,
		"-hide_banner",
		"-loglevel", "error",
		"-protocol_whitelist", "file",
		"-print_format", "json",
		"-show_streams",
		"-show_format",
		"-i", path,
	)
	// WAITDELAY IS WHAT MAKES CANCELLING THIS CONTEXT ACTUALLY END IT.
	//
	// exec.CommandContext kills the process it started, and cmd.Output() then
	// waits for the stdout pipe to close -- which is a different event. Any
	// process still holding the write end keeps Wait blocked forever, and the
	// killed child is not necessarily the only one: a probe binary that is a
	// wrapper script leaves the real work as a grandchild that inherits the
	// pipe and does not get the signal. Measured, not theorised: without this
	// line an upload handler whose client had disconnected sat in cmd.Output()
	// until the grandchild finished on its own, holding the request goroutine
	// and the staged file for as long as that took.
	//
	// Real ffprobe forks nothing, so this costs a correct install nothing. It
	// bounds the case where the configured binary is not the one assumed.
	cmd.WaitDelay = 5 * time.Second
	// BOTH PIPES ARE CAPPED, which cmd.Output() does for only one of them.
	//
	// Output() gives stdout an uncapped bytes.Buffer and reserves its 32 KiB cap
	// for stderr, which is backwards for this call: ffprobe's stdout is JSON
	// whose size scales with the STREAM COUNT, not with the file. Measured on
	// legitimate, allowlisted media -- a 121 KB Matroska carrying 300 audio
	// tracks -- ffprobe printed 464 KB of JSON, 3.8x the container, at 1.5 KB
	// per stream. That is heap in this process, not in the child, it arrives
	// once per concurrent upload, and nothing in the shape of the file warns
	// that it is coming.
	//
	// Over the cap the excess is DRAINED AND DROPPED rather than the pipe being
	// closed. Closing it would kill the child with EPIPE mid-write, which is a
	// second way for a probe to fail that the caller would have to learn to tell
	// from the first; dropping it means the JSON is truncated, ParseProbe says
	// so, and probeStdoutCap is far above anything real media produces.
	stdout := &cappedBuffer{max: probeStdoutCap}
	stderrBuf := &cappedBuffer{max: probeStderrCap}
	cmd.Stdout = stdout
	cmd.Stderr = stderrBuf
	err := cmd.Run()
	if err != nil {
		stderr := strings.TrimSpace(stderrBuf.buf.String())
		// %w on both branches. The caller has to be able to tell an interrupted
		// probe from a file ffprobe disliked -- one of those must not delete the
		// operator's upload -- and flattening the error with %s took that
		// distinction away. The stderr branch keeps ffprobe's words AND the
		// chain that says why the process ended.
		if stderr != "" {
			return nil, fmt.Errorf("%s: %w", truncate(stderr, 300), err)
		}
		return nil, err
	}
	if stdout.over {
		return nil, fmt.Errorf("ffprobe printed more than %d bytes about this file", probeStdoutCap)
	}
	res, err := ParseProbe(stdout.buf.Bytes())
	if err != nil {
		return nil, err
	}
	if !selfContained(res.FormatName) {
		if indirect(res.FormatName) {
			return nil, fmt.Errorf("%w (ffprobe read it as %q)", ErrIndirectContainer, res.FormatName)
		}
		return nil, fmt.Errorf("%w (ffprobe read it as %q)", ErrUnsupportedContainer, res.FormatName)
	}
	return res, nil
}

// probeStdoutCap and probeStderrCap bound what one ffprobe may hand back.
//
// 8 MiB of stdout is roughly 5,000 streams at the 1.5 KB per stream that was
// measured, which is far past any file an operator has and far short of a
// number that matters to a server's heap. 32 KiB of stderr is what
// exec.Cmd.Output() already used, kept so the message an operator sees does not
// change.
const (
	probeStdoutCap = 8 << 20
	probeStderrCap = 32 << 10
)

// cappedBuffer collects up to max bytes and drops the rest, recording that it
// did. It never errors, so the writer on the other end is never killed by a
// short write -- see ProbeFile for why that matters.
type cappedBuffer struct {
	buf  bytes.Buffer
	max  int
	over bool
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	if room := c.max - c.buf.Len(); room > 0 {
		if len(p) > room {
			c.buf.Write(p[:room])
			c.over = true
		} else {
			c.buf.Write(p)
		}
	} else if len(p) > 0 {
		c.over = true
	}
	return len(p), nil
}

// selfContained reports whether an ffprobe format_name is on the allowlist.
//
// Element-wise, because format_name is the demuxer's whole name list joined
// with commas. An empty name is NOT self-contained: ffprobe naming no format
// while exiting 0 is a state this code has no reading of, and the safe reading
// of a state with no reading is "no".
func selfContained(formatName string) bool {
	for _, n := range strings.Split(formatName, ",") {
		if selfContainedFormats[strings.TrimSpace(n)] {
			return true
		}
	}
	return false
}

// ParseProbe converts ffprobe JSON into a ProbeResult. Split out so it can be
// tested against captured fixtures without a live stream.
func ParseProbe(raw []byte) (*ProbeResult, error) {
	var p ffprobeOutput
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("parse ffprobe output: %w", err)
	}

	res := &ProbeResult{
		Audio:      []AudioStream{},
		FormatName: strings.TrimSpace(p.Format.FormatName),
	}
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
