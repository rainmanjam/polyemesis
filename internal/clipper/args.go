package clipper

import (
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/rainmanjam/polyemesis/internal/ffmpeg"
	"github.com/rainmanjam/polyemesis/internal/routing"
)

// Command is one FFmpeg invocation, ready to run. The binary is not part of it:
// the caller holds the detected path and this package holds the arguments, the
// same split the rest of the product uses.
type Command struct {
	// Name is what this step is for, in a word: cut, head, tail, join. It is
	// what a job log line and a failure message are keyed on.
	Name   string   `json:"name"`
	Args   []string `json:"args"`
	Output string   `json:"output"`
	// Files are the sidecar files this command needs on disk before it runs —
	// concat lists, and nothing else so far. Carrying them here keeps the
	// argument builders pure functions of a Plan, which is what makes the
	// boundary-spanning case testable without a filesystem.
	Files []SidecarFile `json:"files,omitempty"`
}

// SidecarFile is a small text file a Command depends on.
type SidecarFile struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// ErrNoWorkDir is a plan that needs somewhere to put a concat list or an
// intermediate and was not given one.
var ErrNoWorkDir = errors.New("clipper: this cut needs a work directory")

// Commands turns a Plan into the FFmpeg invocations that carry it out, in
// order. The last one writes Plan.OutPath; everything before it writes into
// workDir and is disposable.
//
// Three shapes, and which one comes back is the whole story of the plan:
//
//   - ONE COPY. A fast cut, or a precise cut whose in-point was already on a
//     keyframe. Nothing is re-encoded.
//   - ONE ENCODE. A precise cut of a clip shorter than one GOP: there is no
//     keyframe inside it to resume copying at.
//   - HEAD, TAIL, JOIN. The normal precise cut. The head is the re-encoded
//     partial GOP, the tail is a straight copy of everything after the next
//     keyframe, and the join is a concat of the two — which is also a copy, so
//     the tail's packets reach the finished clip untouched.
func (p Plan) Commands(workDir string) ([]Command, error) {
	if len(p.Sources) == 0 {
		return nil, ErrNoSegments
	}
	if p.OutPath == "" {
		return nil, errors.New("clipper: no output path")
	}
	if p.Duration() <= 0 {
		return nil, ErrEmptyRange
	}
	needsWork := p.Concat || (p.HeadDuration > 0 && p.TailDuration > 0)
	if needsWork && workDir == "" {
		return nil, ErrNoWorkDir
	}

	in := p.input(workDir)

	switch {
	case p.HeadDuration <= 0:
		return []Command{p.copyCommand("cut", in, p.In-p.Base, p.Duration(), p.OutPath, p.Container, true)}, nil

	case p.TailDuration <= 0:
		// The whole clip is inside one GOP, so the "head" is the clip.
		return []Command{p.headCommand(in, p.OutPath, p.Container, true)}, nil

	default:
		head := filepath.Join(workDir, "head.mkv")
		tail := filepath.Join(workDir, "tail.mkv")
		join := SidecarFile{
			Path: filepath.Join(workDir, "join.txt"),
			// Order is load-bearing and is not the order of the arguments: the
			// re-encoded head first, then the copied tail.
			Content: ffmpeg.ConcatList([]ffmpeg.ConcatEntry{{Path: head}, {Path: tail}}),
		}
		return []Command{
			// Intermediates are always Matroska. It carries every codec and
			// every track count the recorder can produce, so the join never has
			// to think about whether the container it was handed can hold what
			// is in the file.
			p.headCommand(in, head, ContainerMatroska, false),
			p.copyCommand("tail", in, p.TailSeek, p.TailDuration, tail, ContainerMatroska, false),
			p.joinCommand(join),
		}, nil
	}
}

// input describes how FFmpeg opens the source: one file, or a concat list
// standing in for the several a boundary-spanning clip needs.
type input struct {
	args []string
	file []SidecarFile
}

func (p Plan) input(workDir string) input {
	if !p.Concat {
		return input{args: []string{"-i", p.Sources[0].Path}}
	}
	entries := make([]ffmpeg.ConcatEntry, 0, len(p.Sources))
	for _, s := range p.Sources {
		// DurationMS stays zero: this package has never measured a source's
		// duration ahead of time, and the demuxer's own estimate beats a
		// number nobody computed.
		entries = append(entries, ffmpeg.ConcatEntry{Path: s.Path})
	}
	list := filepath.Join(workDir, "sources.txt")
	return input{
		// -safe 0 because the list holds absolute paths, which the demuxer
		// refuses by default. They are paths this process chose, not paths a
		// request supplied, so there is nothing here for the check to protect.
		args: []string{"-f", "concat", "-safe", "0", "-i", list},
		file: []SidecarFile{{Path: list, Content: ffmpeg.ConcatList(entries)}},
	}
}

// copyCommand is a pure stream copy of one span. No frame is decoded, so it
// runs at disk speed and the output's packets are the source's.
func (p Plan) copyCommand(name string, in input, seek, dur time.Duration, out string, c Container, final bool) Command {
	pre, post := seekArgs(p.Concat, seek, 0)
	args := baseArgs()
	args = append(args, pre...)
	args = append(args, in.args...)
	args = append(args, post...)
	args = append(args, "-t", secs(dur))
	args = append(args, p.mapArgs()...)
	args = append(args, "-c:v", "copy")
	args = append(args, p.audioCodecArgs()...)
	args = append(args, p.outputArgs(c, final)...)
	args = append(args, out)
	return Command{Name: name, Args: args, Output: out, Files: in.file}
}

// seekArgs decides where the seek goes, which is not a matter of taste.
//
// For a single file it belongs BEFORE -i: FFmpeg jumps straight to the right
// block, and reading a forty-minute clip out of an hour-long segment costs
// nothing it does not have to.
//
// For a concat list it must go AFTER, because THE CONCAT DEMUXER CANNOT SEEK.
// Verified against ffmpeg 8.1: an input-side -ss over a concat list logs
// "could not seek to position", silently reads from the beginning, and writes a
// clip with the wrong content and a duration equal to the whole span. An output
// seek costs a demux of everything before the in-point — no decode, so it is
// disk speed — and is exact.
//
// The two-stage form (seek the input to a keyframe, drop the remainder on the
// output side) is what makes the precise head land on the frame the user chose
// rather than on whatever FFmpeg's accurate-seek heuristics decide.
func seekArgs(concat bool, at, trim time.Duration) (pre, post []string) {
	if concat {
		return nil, []string{"-ss", secs(at + trim)}
	}
	pre = []string{"-ss", secs(at)}
	if trim > 0 {
		post = []string{"-ss", secs(trim)}
	}
	return pre, post
}

// headCommand re-encodes the leading partial GOP.
//
// Nothing here pins the pixel format, the resolution or the frame rate. They are
// negotiated from the source, which is what makes the encoded head splice onto
// the copied tail: a head forced to yuv420p in front of a 10-bit tail is a join
// that fails, or worse, one that succeeds and plays wrong.
func (p Plan) headCommand(in input, out string, c Container, final bool) Command {
	pre, post := seekArgs(p.Concat, p.HeadSeek, p.HeadTrim)
	args := baseArgs()
	args = append(args, pre...)
	args = append(args, in.args...)
	args = append(args, post...)
	args = append(args, "-t", secs(p.HeadDuration))
	args = append(args, p.mapArgs()...)
	args = append(args, "-c:v", p.VideoEncoder)
	args = append(args, headQualityArgs(p.VideoEncoder, p.HeadCRF, p.HeadThreads)...)
	args = append(args, p.audioCodecArgs()...)
	args = append(args, p.outputArgs(c, final)...)
	args = append(args, out)
	return Command{Name: "head", Args: args, Output: out, Files: in.file}
}

// joinCommand splices the head onto the tail. -c copy, so the tail arrives in
// the finished clip exactly as it left the recording.
func (p Plan) joinCommand(list SidecarFile) Command {
	args := baseArgs()
	args = append(args, "-f", "concat", "-safe", "0", "-i", list.Path)
	// -map 0 rather than the selective mapping used upstream: both parts were
	// built with the mapping already applied, and re-applying it here would
	// renumber tracks that are already the ones the caller asked for.
	args = append(args, "-map", "0", "-c", "copy")
	args = append(args, p.outputArgs(p.Container, true)...)
	args = append(args, p.OutPath)
	return Command{Name: "join", Args: args, Output: p.OutPath, Files: []SidecarFile{list}}
}

// baseArgs is the preamble every invocation shares. -y because the runner
// writes to a temporary name it owns and renames on success, so there is never
// a file here worth protecting.
func baseArgs() []string {
	return []string{"-hide_banner", "-nostdin", "-loglevel", "warning", "-y"}
}

// mapArgs selects the streams the clip keeps.
func (p Plan) mapArgs() []string {
	// Optional, with the trailing '?': a recording with no video is unusual but
	// not a reason to refuse to clip its audio.
	args := []string{"-map", "0:v:0?"}
	switch p.Audio.Mode {
	case AudioMix:
		args = append(args, "-map", "["+routing.OutLabel+"]")
	case AudioTracks:
		for _, t := range p.Audio.Tracks {
			// Also optional. A track the recording turned out not to have is a
			// clip with fewer tracks, not a failed export.
			args = append(args, "-map", "0:a:"+strconv.Itoa(t)+"?")
		}
	default:
		// Every audio track, which is the point of a multitrack master.
		args = append(args, "-map", "0:a?")
	}
	if p.FilterComplex != "" {
		// Prepended rather than appended so the graph is next to the -map that
		// consumes its label when a human reads the command back.
		return append([]string{"-filter_complex", p.FilterComplex}, args...)
	}
	return args
}

// audioCodecArgs is copy for every mode but the mix, which by definition
// produces samples that were not in the source.
func (p Plan) audioCodecArgs() []string {
	if p.Audio.Mode != AudioMix {
		return []string{"-c:a", "copy"}
	}
	args := []string{"-c:a", p.AudioCodec}
	if p.AudioKbps > 0 && !losslessAudio(p.AudioCodec) {
		args = append(args, "-b:a", strconv.Itoa(p.AudioKbps)+"k")
	}
	return args
}

func losslessAudio(codec string) bool {
	switch codec {
	case "flac", "alac", "pcm_s16le", "pcm_s24le", "pcm_s32le":
		return true
	}
	return false
}

// headQualityArgs tunes the encoder that produces the leading GOP.
//
// -bf 0 is NOT a quality choice and must not be removed as one. With B-frames
// the encoder emits its first packet with a DTS two frames ahead of its PTS,
// the muxer anchors the file on that DTS, and the head ends up with a lead-in
// of empty time in front of its first picture. Concatenated onto the tail, that
// lead-in becomes a gap at the seam and a clip longer than it was asked to be —
// measured at 67ms with libx264's defaults over the fixtures in this package.
// Without B-frames DTS equals PTS, the head starts at zero, and the join is
// tight. It costs a little bitrate over a fraction of a second, which is
// nothing.
//
// Only the x264/x265 family gets a CRF, because it is the only family where the
// flag means anything; a hardware encoder is handed its own defaults rather than
// a flag it will reject. The preset is fast on purpose: this is a fraction of a
// second of video and the wait is the thing being optimised, not the bitrate.
//
// -threads is emitted for every encoder, including the hardware ones where it
// mostly governs the frame-level parallelism around the fixed-function block.
// It is the only lever this package has over how much of the machine a cut
// takes, and the live stream's claim on the machine comes first.
func headQualityArgs(encoder string, crf, threads int) []string {
	args := []string{"-bf", "0"}
	switch encoder {
	case "libx264", "libx265":
		args = append(args, "-crf", strconv.Itoa(crf), "-preset", "veryfast")
	}
	if threads > 0 {
		args = append(args, "-threads", strconv.Itoa(threads))
	}
	return args
}

// outputArgs is the muxer and the flags that go with it.
func (p Plan) outputArgs(c Container, final bool) []string {
	// make_zero rather than the default: a cut that starts mid-file carries
	// timestamps that begin far from zero, and some players read that as a clip
	// that has not started yet.
	args := []string{"-avoid_negative_ts", "make_zero"}
	if final && p.Title != "" {
		args = append(args, "-metadata", "title="+p.Title)
	}
	switch c {
	case ContainerMP4:
		// The index at the front, so the clip can be scrubbed while it is still
		// being uploaded.
		args = append(args, "-movflags", "+faststart", "-f", "mp4")
	case ContainerMPEGTS:
		args = append(args, "-f", "mpegts")
	default:
		args = append(args, "-f", "matroska")
	}
	return args
}

// String renders a plan's commands the way a debug endpoint should show them.
func (c Command) String() string {
	return fmt.Sprintf("%s: ffmpeg %s", c.Name, strings.Join(c.Args, " "))
}
