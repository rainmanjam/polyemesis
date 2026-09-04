package ffmpeg

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
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
	// DurationSeconds is how long this input is, and is 0 for a live input --
	// a relay that never ends has no duration to report, which is the case this
	// type was written for. It is meaningful for a file.
	//
	// IT IS NO LONGER ALWAYS THE CONTAINER'S OWN FIELD. A raw elementary stream
	// has no container to declare one, and ProbeFile now COUNTS the length of
	// such a file rather than refusing it -- see #218 and CountDurationSeconds.
	// DurationSource says which of the two happened, and a consumer for whom
	// the difference matters must ask; the number alone cannot tell it.
	DurationSeconds float64 `json:"durationSeconds"`
	// DurationSource says where DurationSeconds came from: declared by the
	// container, counted by decoding, or not established at all.
	//
	// A SEPARATE FIELD RATHER THAN A SEPARATE NUMBER, and that direction was the
	// decision. Putting a counted duration in a field of its own would leave
	// every existing consumer -- the Library listing, the disk estimate, the
	// -fs cap, the progress bar -- reading zero for a file that is perfectly
	// playable, which is precisely the "accepted at the door, unusable forever"
	// state #118 was written to end, re-created in the opposite direction. So
	// the number goes where the number goes and the PROVENANCE travels beside
	// it, in a field whose zero value is "unknown" rather than "measured".
	DurationSource DurationSource `json:"durationSource,omitempty"`
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

// ErrProbeTooVerbose is returned when ffprobe ran fine and printed more about
// this file than probeStdoutCap allows. It is a verdict about the FILE: nothing
// an operator uploads makes a correct ffprobe print megabytes, and the reply is
// truncated, so the alternative is parsing a fragment as though it were whole.
var ErrProbeTooVerbose = errors.New("ffprobe printed more about this file than polyemesis will read")

// Refused reports whether err is ProbeFile's verdict ABOUT THE FILE, as opposed
// to this server failing to inspect it.
//
// IT EXISTS BECAUSE THE CALLER'S DEFAULT IS FAIL-OPEN, and that is the right
// default for the thing it was chosen for. internal/api treats an error it
// cannot attribute to ffprobe having RUN AND DISAGREED as a fact about the
// server -- a fork that failed, a binary that is not there, output that is not
// JSON -- and stores the upload unchecked rather than deleting a completed
// transfer over it. Correct, and it means every NEW error shape this function
// learns to return is fail-open at that caller until somebody remembers to add
// an arm for it. Measured: disabling the ErrUnsupportedContainer arm did not
// turn a GIF into a refusal with a worse message, it turned it into a 201 with
// the file stored.
//
// So the list of "this is about the file" lives HERE, beside the errors it
// names, rather than as a sequence of arms in a handler that has no way to know
// when it has missed one. TestEveryProbeFileRefusalIsClassified is the pin.
func Refused(err error) bool {
	return errors.Is(err, ErrIndirectContainer) ||
		errors.Is(err, ErrUnsupportedContainer) ||
		errors.Is(err, ErrNoDuration) ||
		errors.Is(err, ErrProbeTooVerbose)
}

// ErrNoDuration is returned for a file whose format is accepted but whose length
// polyemesis could not establish. It is a refusal at the door rather than a
// permanent failure at the worker, which is where it used to land.
//
// ITS REACH NARROWED IN #218 AND ITS MEANING DID NOT. It used to fire for every
// file whose CONTAINER did not declare a duration, which swept in the raw
// elementary streams the allowlist admits -- a .h264 dump from an encoder has
// no container to declare anything, so it was refused for a property it can
// never have. ProbeFile now counts such a file's length instead. What is left
// here is the honest residual: no ffmpeg binary to count with, a count that
// failed or ran out of time, or a file that decoded to nothing. All three are
// still "polyemesis cannot work out how long this file is", and the two gates
// still agree, which is the guarantee #118 bought and this must not spend.
var ErrNoDuration = errors.New("polyemesis cannot work out how long this file is")

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
	//
	// These are the entries that made the duration branch below necessary and
	// then, in #218, made it count rather than refuse: none of them declares a
	// duration, because there is no container to declare it in.
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
func ProbeFile(ctx context.Context, bins Bins, path string) (*ProbeResult, error) {
	return probeFile(ctx, bins, path, probeStdoutCap)
}

// Bins names the binaries ProbeFile may run.
//
// Not the detected Tools struct above it: that is a startup snapshot with a
// mutex and an encoder census attached, and internal/playlistmedia has two
// configured paths and no Tools at all. This is the pair, and only the pair.
//
// A STRUCT, AND ProbeFile'S SIGNATURE CHANGED RATHER THAN GAINING AN OPTIONAL
// EXTRA, because there are TWO gates on this path and they must not be able to
// drift apart. internal/api's upload handler and internal/playlistmedia's
// verifySource both call ProbeFile, and #118's whole guarantee is that they
// give the same answer about the same file. A variadic option or a second
// ProbeFileDeriving entry point would have let one of them be updated and the
// other quietly keep refusing -- an upload accepted at the door and refused
// forever at the worker, which is the exact state that issue closed. Changing
// the signature makes the compiler visit both.
//
// Two adjacent strings would have been swappable by accident and the swap would
// compile; named fields cannot be.
type Bins struct {
	// FFprobe reads the file's shape. Required.
	FFprobe string
	// FFmpeg counts the length of a file whose container declares none.
	// OPTIONAL, and an install without it is not broken -- it gets exactly the
	// behaviour it had before #218: such a file is refused with ErrNoDuration
	// and its remedy. Every other file is unaffected, because a container that
	// states its own duration never reaches the count.
	FFmpeg string
}

// probeFile is ProbeFile with the stdout cap as an argument, so a test can
// reach the over-the-cap branch against real media and a real ffprobe instead
// of building a file that makes ffprobe print eight megabytes -- which would
// take longer than the rest of the suite and would be measuring the fixture.
// Nothing else passes anything but probeStdoutCap.
func probeFile(ctx context.Context, bins Bins, path string, stdoutCap int) (*ProbeResult, error) {
	cmd := exec.CommandContext(ctx, bins.FFprobe,
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
	stdout := &cappedBuffer{max: stdoutCap}
	stderrBuf := &cappedBuffer{max: probeStderrCap}
	cmd.Stdout = stdout
	cmd.Stderr = stderrBuf
	err := cmd.Run()
	if err != nil {
		stderr := strings.TrimSpace(stderrBuf.buf.String())
		// THE CONTEXT ERROR FIRST, and this branch did not have it while the
		// comment below claimed the property it provides.
		//
		// %w on the run error was supposed to let a caller "tell an interrupted
		// probe from a file ffprobe disliked". It cannot: killing a child
		// through CommandContext yields a plain *exec.ExitError saying "signal:
		// killed" and carrying NO context error to match on -- which the count
		// branch further down measured and says so in its own comment, and
		// which this branch then did not act on. So an interruption HERE
		// arrived as an unrecognisable exec failure.
		//
		// The consequence is the same one the count branch spells out: a probe
		// that was cut short is not a verdict about the file. It is milder here
		// only because a bare *exec.ExitError matches none of Refused's
		// sentinels, so the upload survives -- the caller simply cannot tell
		// why the probe ended, and neither can a log reader.
		//
		// Found as an intermittent macOS CI failure in
		// TestACountThatWasCutShortIsNotAVerdictAboutTheFile: that test cancels
		// at 200ms intending to land inside the count, and on a loaded runner
		// ffprobe is still going, so the cancellation landed here instead and
		// the assertion about the chain failed against the wrong branch.
		if ctx.Err() != nil {
			return nil, fmt.Errorf("reading %q was cut short: %w (%v)",
				filepath.Base(path), ctx.Err(), err)
		}
		// %w on both branches. The stderr branch keeps ffprobe's words AND the
		// chain that says why the process ended.
		if stderr != "" {
			return nil, fmt.Errorf("%s: %w", truncate(stderr, 300), err)
		}
		return nil, err
	}
	if stdout.over {
		return nil, fmt.Errorf("%w (over %d bytes)", ErrProbeTooVerbose, stdoutCap)
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
	// AND A DURATION HAS TO EXIST, because the worker that will normalise this
	// file cannot proceed without one and the two gates must not disagree.
	//
	// The allowlist admits raw elementary streams on the reasoning that a .h264
	// dump from an encoder is a real file. Measured with ffprobe 8.1.2: h264,
	// hevc and mpegvideo all report duration=None, while ac3 carries one. So the
	// upload gate ACCEPTED such a file and the normalise worker then refused it
	// PERMANENTLY -- "polyemesis could not work out how long it is" -- leaving a
	// playlist that can never go on air. Accepted at the door, unusable forever,
	// which is worse than either answer given consistently.
	//
	// #118 fixed that by refusing at the door. #218 fixes it the other way for
	// the files that deserve it: a length polyemesis can COUNT is a length
	// polyemesis has, and the count now happens here -- at the one place both
	// gates already come through, so neither can be updated without the other.
	//
	// Keyed on the duration rather than on the format name deliberately, and
	// that key is unchanged: the gate's real requirement is the one the worker
	// has, so any future format with this property gets counted too without a
	// second list to keep in step. ac3 and every container that carries a
	// duration never reach this branch and pay nothing for it.
	//
	// Ordered AFTER the caller's no-streams diagnosis in effect, by asking only
	// when there is something to measure: a no-tracks MP4 reaches here read
	// perfectly, with zero streams and no duration, and "no video or audio
	// stream" is the sentence that tells its owner what is wrong. Measured -- the
	// first version of this check preempted it and TestUploadRefusesAContainer
	// WithNoStreams failed with the less useful reason. It also means the count
	// is never spent on a file with nothing in it to count.
	if (res.Video != nil || len(res.Audio) > 0) && res.DurationSeconds <= 0 {
		// countErr, NOT err, AND NOT AN `if secs, err := …` SHORT FORM. That form
		// was here and it shipped a lie: `err` is already declared at the top of
		// this function (`err := cmd.Run()`, nil by the time execution reaches
		// here), so the short form declared a SECOND err scoped to the if/else
		// chain -- and the ErrNoDuration return below sits AFTER that chain, so it
		// read the outer, nil one. The operator was handed "it could not be
		// counted: <nil>" for the one feature whose entire job is counting, with
		// the real cause -- no ffmpeg binary, ffmpeg's own stderr, a decode that
		// reached no positive output time -- discarded on the way out.
		//
		// A distinct name rather than a rearrangement, because a rearrangement is
		// re-breakable by the next edit that adds a branch: with `countErr` there
		// is no scope in which the wrong variable is spelled the same as the right
		// one. TestARefusalNamesWhyTheCountFailedRatherThanNil is the pin.
		secs, countErr := countDuration(ctx, bins.FFmpeg, res.FormatName, path)
		if countErr == nil {
			res.DurationSeconds = secs
			res.DurationSource = DurationCounted
			return res, nil
		}
		if ctx.Err() != nil {
			// THE COUNT WAS CUT SHORT, WHICH IS NOT A VERDICT ABOUT THE FILE.
			// Returning ErrNoDuration here would make it one, and ErrNoDuration
			// is in Refused -- so a client who disconnects mid-upload, or a
			// deadline that expires on a busy box, would get the operator's
			// bytes deleted with a sentence blaming their file. Both callers
			// already have the right handling for an interrupted probe; the
			// context error is what routes them to it.
			//
			// %w ON ctx.Err() AND NOT ON THE RUN ERROR, which is the opposite
			// of what looks natural and is what actually works. Killing a child
			// through CommandContext yields a plain *exec.ExitError saying
			// "signal: killed" and carrying NO context error to match on --
			// measured here, and the same thing media.go's probeUpload
			// documents about its own arms. Wrapping the run error would put a
			// chain in the caller's hands that answers a different question.
			// FFmpeg's own words are kept beside it with %v, because they are
			// what a log reader needs.
			return nil, fmt.Errorf("counting the length of %q was cut short: %w (%v)",
				res.FormatName, ctx.Err(), countErr)
		}
		return nil, fmt.Errorf("%w (ffprobe read it as %q and reported no duration, "+
			"and it could not be counted: %v; re-save it as MP4 or MPEG-TS and "+
			"upload it again)", ErrNoDuration, res.FormatName, countErr)
	}
	return res, nil
}

// countDuration is CountDurationSeconds with the one precondition ProbeFile
// needs and the exported function cannot check for itself: that the format name
// ffprobe reported names EXACTLY ONE demuxer, so it can be handed back as -f.
//
// format_name is the comma-joined list of names one demuxer registers under, so
// "mov,mp4,m4a,3gp,3g2,mj2" is one demuxer with six names and there is no way
// to know which of the six FFmpeg will accept. Every raw elementary stream
// reports a single name -- "h264", "hevc", "mpegvideo" -- which is not a
// coincidence: a format with no container has no family of related ones to
// share a demuxer with.
//
// Refusing to guess is the conservative direction here. A multi-name format
// that declares no duration is a CONTAINER that would not say how long it is,
// which is a different and more suspicious thing than a stream that has nowhere
// to put the answer, and it keeps its existing refusal.
func countDuration(ctx context.Context, ffmpegBin, formatName, path string) (float64, error) {
	name := strings.TrimSpace(formatName)
	if name == "" || strings.Contains(name, ",") {
		return 0, fmt.Errorf("%q names %d demuxers, so there is none to pin the count to",
			formatName, len(strings.Split(formatName, ",")))
	}
	return CountDurationSeconds(ctx, ffmpegBin, name, path)
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
		res.DurationSource = DurationDeclared
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
