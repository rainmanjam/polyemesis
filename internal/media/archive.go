package media

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ------------------------------------------------------- archive compression
//
// This is the only job in polyemesis that DESTROYS DATA. Everything else adds a
// file; this one re-encodes an old recording with a lossy codec and, if asked,
// puts the result where the original was. The original is a bit-exact
// multitrack master that cannot be reconstructed from anything.
//
// So the whole file leans one way, and it is the OPPOSITE of the "fail open"
// rule the rest of this repo runs on. Fail-open is right when the cost of a
// wrong restrictive answer is a feature that does not start and a user who
// complains. Here the cost of a wrong permissive answer is an archive that is
// silently gone, and nobody complains because nobody notices until they go
// looking for it a year later. Four consequences:
//
//  1. It is opt-in at the settings level AND acknowledged per job.
//  2. It never runs on a recording younger than the configured age, and a
//     recording whose age cannot be established is treated as too young.
//  3. The output is verified against the source — duration, every audio track,
//     a full decode pass — before anything is deleted, and ANY doubt keeps the
//     original.
//  4. Every audio track survives. Losing one here would silently destroy the
//     multitrack master, which is the entire reason this product exists.

// ErrArchiveTooYoung is returned when a recording has not aged into the
// archive policy yet.
var ErrArchiveTooYoung = errors.New("recording is younger than the archive age")

// ArchiveCodec names the target family. Deliberately a family rather than an
// encoder: the operator's decision is "smaller H.265 or smaller AV1", and which
// encoder implements it is this package's problem.
type ArchiveCodec string

const (
	// ArchiveHEVC is H.265. Roughly half the bitrate of H.264 at the same
	// quality, decodes in hardware on essentially everything made this decade,
	// and encodes fast enough that a night is enough for a day of recordings.
	ArchiveHEVC ArchiveCodec = "hevc"
	// ArchiveAV1 is smaller again, at several times the encode cost. Worth it
	// for an archive nobody will re-encode a second time.
	ArchiveAV1 ArchiveCodec = "av1"
)

// DefaultArchiveCodec is what an unset codec means.
const DefaultArchiveCodec = ArchiveHEVC

// Archive defaults.
const (
	// DefaultArchiveMinAge is how old a recording must be. Thirty days is long
	// enough that anyone who was going to edit the master has done it.
	DefaultArchiveMinAge = 30 * 24 * time.Hour
	// MinArchiveMinAge is the floor an operator may configure. A same-day
	// archive is not an archive policy, it is a recording policy with a lossy
	// codec, and it should be configured as one on the recorder instead.
	MinArchiveMinAge = 24 * time.Hour

	// DefaultDurationToleranceSeconds and DefaultDurationTolerancePercent bound
	// how far the re-encode may drift. A container rewrite legitimately moves
	// the reported duration by a frame or two; anything past this means frames
	// were dropped, and dropped frames are the failure mode a verifier exists
	// to catch.
	DefaultDurationToleranceSeconds = 1.0
	DefaultDurationTolerancePercent = 0.5

	// MaxReportedDecodeErrors bounds what a failed verification quotes back. A
	// corrupt file produces thousands of identical lines and the operator needs
	// the first few, not the transcript.
	MaxReportedDecodeErrors = 10
)

// archiveProfile is how one encoder spells its quality knob.
//
// Only encoders whose knob counts in the SAME DIRECTION as x265's -crf are
// listed. hevc_videotoolbox is a deliberate omission: its -q:v counts up where
// crf counts down, so a shared "quality" field would mean "nearly lossless" on
// one machine and "unwatchable" on another, and on this code path the second
// one deletes the original afterwards.
type archiveProfile struct {
	encoder        string
	qualityFlag    string
	defaultQuality int
	presetFlag     string
	defaultPreset  string
	// extra are flags this encoder needs before its quality flag means
	// anything.
	extra []string
	// vaapi marks the encoder that needs a device and an upload filter.
	vaapi bool
}

var archiveProfiles = map[string]archiveProfile{
	// 28 is x265's own "visually transparent enough for an archive" region; it
	// is not x264's 28, which is noticeably worse.
	"libx265": {
		encoder: "libx265", qualityFlag: "-crf", defaultQuality: 28,
		presetFlag: "-preset", defaultPreset: "medium",
		// x265 prints a banner and a per-frame summary on stderr at any
		// loglevel, which would drown the job log tail it shares with the
		// errors that matter.
		extra: []string{"-x265-params", "log-level=error"},
	},
	// SVT-AV1 preset 6 is the knee of its speed/size curve; below it the encode
	// time grows much faster than the file shrinks.
	"libsvtav1": {
		encoder: "libsvtav1", qualityFlag: "-crf", defaultQuality: 32,
		presetFlag: "-preset", defaultPreset: "6",
	},
	// libaom needs -b:v 0 or its CRF is treated as a ceiling on a bitrate
	// target rather than a quality target.
	"libaom-av1": {
		encoder: "libaom-av1", qualityFlag: "-crf", defaultQuality: 32,
		extra: []string{"-b:v", "0", "-cpu-used", "4", "-row-mt", "1"},
	},
	"hevc_nvenc": {
		encoder: "hevc_nvenc", qualityFlag: "-cq", defaultQuality: 28,
		presetFlag: "-preset", defaultPreset: "p5",
		extra: []string{"-rc", "vbr", "-b:v", "0"},
	},
	"hevc_qsv": {
		encoder: "hevc_qsv", qualityFlag: "-global_quality", defaultQuality: 28,
		presetFlag: "-preset", defaultPreset: "medium",
	},
	"hevc_vaapi": {
		encoder: "hevc_vaapi", qualityFlag: "-qp", defaultQuality: 28,
		vaapi: true,
	},
	"av1_nvenc": {
		encoder: "av1_nvenc", qualityFlag: "-cq", defaultQuality: 32,
		presetFlag: "-preset", defaultPreset: "p5",
		extra: []string{"-rc", "vbr", "-b:v", "0"},
	},
}

// archiveEncoders is the preference order per codec family.
//
// Software first, which is the reverse of the rendition ladder's order and is
// not an oversight. A rendition optimises for throughput because it runs live;
// an archive optimises for quality per bit because it is the last encode this
// footage will ever get, and fixed-function hardware encoders are meaningfully
// worse per bit than x265 or SVT-AV1 at any speed. Hardware is offered for the
// operator with a thousand hours to get through, not chosen for them.
var archiveEncoders = map[ArchiveCodec][]string{
	ArchiveHEVC: {"libx265", "hevc_nvenc", "hevc_qsv", "hevc_vaapi"},
	ArchiveAV1:  {"libsvtav1", "libaom-av1", "av1_nvenc"},
}

// ArchiveEncoder picks the encoder for a codec family.
//
// has reports whether the FFmpeg build registers an encoder — normally
// ffmpeg.Tools.HasEncoder. A nil has, or a build that registers none of the
// candidates, still yields the software encoder rather than an empty string:
// this is a capability check, and a capability check that is wrong in the
// restrictive direction is worse than no check. FFmpeg will say so plainly if
// it really is missing, and the job fails with that message instead of with one
// we invented.
func ArchiveEncoder(codec ArchiveCodec, has func(string) bool) string {
	list := archiveEncoders[codec]
	if len(list) == 0 {
		list = archiveEncoders[DefaultArchiveCodec]
	}
	if has != nil {
		for _, name := range list {
			if has(name) {
				return name
			}
		}
	}
	return list[0]
}

// ArchiveSpec describes one re-encode.
type ArchiveSpec struct {
	Input  string
	Output string

	// Codec is the target family; empty means DefaultArchiveCodec.
	Codec ArchiveCodec
	// Encoder overrides the family's choice. An encoder this package has no
	// profile for is still usable: it gets the flags every encoder understands
	// and a bitrate if one was given.
	Encoder string
	// Quality is the encoder's CRF-equivalent; 0 takes the profile's default.
	Quality int
	// Preset is the encoder's speed knob.
	Preset string
	// VideoKbps forces average-bitrate rate control. Chiefly for an encoder
	// with no quality flag we know.
	VideoKbps int
	// VAAPIDevice overrides the render node for the VAAPI encoders.
	VAAPIDevice string
}

// DefaultVAAPIDevice is the first render node on a typical Linux box.
const DefaultVAAPIDevice = "/dev/dri/renderD128"

// ArchiveArgs builds the re-encode.
//
// Two lines carry the whole guarantee:
//
//	-map 0:a  -c:a copy
//
// EVERY audio track, COPIED. Not re-encoded, not downmixed, not "the first
// one". A per-microphone multitrack master that comes back with a stereo mix is
// not a smaller archive, it is a deleted archive with a video file where it
// used to be — and because the video still plays, nobody finds out.
//
// Audio is also the wrong thing to squeeze. On a recording that is worth
// archiving at all the video is the overwhelming majority of the bytes, so
// copying the audio costs a few percent of the saving and buys the guarantee
// outright.
func ArchiveArgs(s ArchiveSpec) []string {
	if s.Codec == "" {
		s.Codec = DefaultArchiveCodec
	}
	if s.Encoder == "" {
		s.Encoder = ArchiveEncoder(s.Codec, nil)
	}
	prof, known := archiveProfiles[s.Encoder]

	args := commonArgs()
	args = append(args, progressArgs()...)
	if prof.vaapi {
		dev := s.VAAPIDevice
		if dev == "" {
			dev = DefaultVAAPIDevice
		}
		// Must precede -i: the device has to exist before the filter graph that
		// uploads into it is configured.
		args = append(args, "-vaapi_device", dev)
	}
	args = append(args, "-i", s.Input)

	// Explicit maps rather than -map 0. A bare -map 0 drags attachments, data
	// streams and cover art into the new file, and one unmappable stream fails
	// the whole mux — on this path that means a failed job rather than a lost
	// archive, but a failed job that recurs nightly is its own kind of damage.
	args = append(args, "-map", "0:v:0", "-map", "0:a")
	args = append(args, "-c:v", s.Encoder)

	// A KNOWN encoder with no preset flag — hevc_vaapi — is silently given no
	// preset even when one was asked for: passing one makes FFmpeg complain
	// about an unused AVOption on every run. An UNKNOWN encoder gets the preset
	// through, since the operator who named both presumably knows it takes one.
	switch preset := s.Preset; {
	case prof.presetFlag != "":
		if preset == "" {
			preset = prof.defaultPreset
		}
		if preset != "" {
			args = append(args, prof.presetFlag, preset)
		}
	case !known && preset != "":
		args = append(args, "-preset", preset)
	}

	args = append(args, prof.extra...)
	args = append(args, archiveQualityArgs(s, prof, known)...)

	if prof.vaapi {
		// VAAPI encodes from GPU surfaces, so even an unscaled copy needs the
		// frames converted and uploaded.
		args = append(args, "-vf", "format=nv12,hwupload")
	}

	// No -pix_fmt anywhere. A 10-bit master must stay 10-bit: silently
	// flattening it to 8-bit would be a second, invisible loss on top of the
	// lossy encode, and unlike the encode itself nobody chose it.
	args = append(args,
		"-c:a", "copy",
		// Track titles and languages are how an operator knows which of six
		// tracks is the host's microphone. They are metadata, they cost
		// nothing, and the verifier refuses to delete the original without
		// them.
		"-map_metadata", "0",
		"-map_chapters", "0",
		s.Output,
	)
	return args
}

func archiveQualityArgs(s ArchiveSpec, prof archiveProfile, known bool) []string {
	if s.VideoKbps > 0 || !known || prof.qualityFlag == "" {
		kbps := s.VideoKbps
		if kbps > 0 {
			return []string{"-b:v", strconv.Itoa(kbps) + "k"}
		}
		// An unknown encoder with no bitrate gets neither flag, and encodes at
		// its own default. Guessing a quality flag for an encoder we do not
		// know is how a job either fails to start or, worse, starts with the
		// number meaning something else entirely.
		return nil
	}
	q := s.Quality
	if q <= 0 {
		q = prof.defaultQuality
	}
	return []string{prof.qualityFlag, strconv.Itoa(q)}
}

// ----------------------------------------------------------------- age gating

// ArchiveEligible reports whether a recording is old enough to archive.
//
// A zero recordedAt is NOT eligible. Everywhere else in this repo an unknown
// answer means "assume the best and carry on"; here the best case is deleting a
// recording made this morning, so unknown means no.
func ArchiveEligible(recordedAt, now time.Time, minAge time.Duration) bool {
	return CheckArchiveAge(recordedAt, now, minAge) == nil
}

// CheckArchiveAge is ArchiveEligible with the reason attached, so a refusal can
// be shown to the operator instead of being a silent no-op.
func CheckArchiveAge(recordedAt, now time.Time, minAge time.Duration) error {
	if minAge < MinArchiveMinAge {
		return fmt.Errorf("%w: the archive age is %s, below the %s floor",
			ErrArchiveTooYoung, minAge.Round(time.Hour), MinArchiveMinAge)
	}
	if recordedAt.IsZero() {
		return fmt.Errorf("%w: the recording's date is unknown, so its age cannot be established",
			ErrArchiveTooYoung)
	}
	age := now.Sub(recordedAt)
	if age < minAge {
		return fmt.Errorf("%w: the recording is %s old and the archive age is %s",
			ErrArchiveTooYoung, age.Round(time.Hour), minAge.Round(time.Hour))
	}
	return nil
}

// ---------------------------------------------------------------- verification

// TrackSummary is one audio track as ffprobe described it.
type TrackSummary struct {
	// Index is 0-based among audio streams, i.e. the a:N specifier.
	Index    int    `json:"index"`
	Codec    string `json:"codec"`
	Channels int    `json:"channels"`
	Language string `json:"language,omitempty"`
	Title    string `json:"title,omitempty"`
}

// FileSummary is everything the verifier needs to know about one file.
type FileSummary struct {
	Path            string         `json:"path"`
	Bytes           int64          `json:"bytes"`
	DurationSeconds float64        `json:"durationSeconds"`
	VideoCodec      string         `json:"videoCodec,omitempty"`
	Audio           []TrackSummary `json:"audio"`
}

// VerifyOptions tunes the comparison.
//
// Every field's ZERO VALUE is the strict one. That is deliberate on a code path
// whose next step is an irreversible delete: a caller that forgot to fill this
// in gets the careful behaviour, not the permissive one.
type VerifyOptions struct {
	// DurationToleranceSeconds and DurationTolerancePercent are how far the
	// re-encode may drift; 0 means the defaults, not "no tolerance".
	DurationToleranceSeconds float64
	DurationTolerancePercent float64
	// AllowLarger permits an archive that did not shrink. Off by default:
	// replacing a lossless master with a LARGER lossy file is a pure loss, and
	// it means the settings are wrong.
	AllowLarger bool
	// AllowMetadataLoss permits missing track titles and languages. Off by
	// default, because on a per-microphone master those labels are how anyone
	// tells the tracks apart, and they cost nothing to carry.
	AllowMetadataLoss bool
	// AllowMissingSourceDuration permits a source whose duration ffprobe could
	// not read. Off by default, because "we could not measure the original" is
	// not a basis for deleting it.
	AllowMissingSourceDuration bool
}

// Verification is the verdict, and the only thing that may authorise a delete.
type Verification struct {
	// OK is true only when Reasons is empty.
	OK bool `json:"ok"`
	// Reasons are why the original must be kept. Plain sentences: this text
	// ends up in a job log an operator reads at 2am.
	Reasons []string `json:"reasons,omitempty"`
	// Notes are observations that did not block anything.
	Notes []string `json:"notes,omitempty"`

	SourceBytes  int64   `json:"sourceBytes"`
	ArchiveBytes int64   `json:"archiveBytes"`
	SavedBytes   int64   `json:"savedBytes"`
	SavedPercent float64 `json:"savedPercent"`
}

// VerifyArchive compares a re-encode against its source and decides whether the
// original may be deleted.
//
// decodeErrors is what a full decode pass of the OUTPUT reported; see
// DecodeCheckArgs. A container can be perfectly well-formed and still contain a
// truncated frame that only a decode finds, and a truncated frame in the middle
// of the archive is exactly the thing nobody notices until it is the only copy.
func VerifyArchive(src, out FileSummary, decodeErrors []string, opt VerifyOptions) Verification {
	v := &Verification{SourceBytes: src.Bytes, ArchiveBytes: out.Bytes}

	if out.Bytes <= 0 {
		v.fail("the archive copy is empty")
	}
	if out.VideoCodec == "" {
		v.fail("the archive copy has no video stream")
	}
	v.checkDuration(src, out, opt)
	v.checkAudio(src, out, opt)
	v.checkDecode(decodeErrors)
	v.checkSize(src, out, opt)

	v.OK = len(v.Reasons) == 0
	return *v
}

func (v *Verification) fail(format string, args ...any) {
	v.Reasons = append(v.Reasons, fmt.Sprintf(format, args...))
}

func (v *Verification) note(format string, args ...any) {
	v.Notes = append(v.Notes, fmt.Sprintf(format, args...))
}

// checkDuration is how dropped frames are caught. A re-encode that lost the
// last ten minutes still produces a file that opens and plays.
func (v *Verification) checkDuration(src, out FileSummary, opt VerifyOptions) {
	tolAbs := opt.DurationToleranceSeconds
	if tolAbs <= 0 {
		tolAbs = DefaultDurationToleranceSeconds
	}
	tolPct := opt.DurationTolerancePercent
	if tolPct <= 0 {
		tolPct = DefaultDurationTolerancePercent
	}

	switch {
	case src.DurationSeconds <= 0:
		if opt.AllowMissingSourceDuration {
			v.note("the original's duration was not measured; the duration check was skipped")
			return
		}
		v.fail("the original's duration could not be measured, so the copy cannot be checked against it")
	case out.DurationSeconds <= 0:
		v.fail("the archive copy reports no duration")
	default:
		tol := math.Max(tolAbs, src.DurationSeconds*tolPct/100)
		if diff := math.Abs(src.DurationSeconds - out.DurationSeconds); diff > tol {
			v.fail("the archive copy is %.2fs long and the original is %.2fs, a difference of %.2fs "+
				"which is outside the %.2fs tolerance",
				out.DurationSeconds, src.DurationSeconds, diff, tol)
		}
	}
}

// checkAudio is the guarantee this whole feature stands on.
func (v *Verification) checkAudio(src, out FileSummary, opt VerifyOptions) {
	if len(src.Audio) != len(out.Audio) {
		v.fail("the original has %d audio track(s) and the archive copy has %d",
			len(src.Audio), len(out.Audio))
	}
	if len(src.Audio) == 0 {
		v.note("the original has no audio tracks")
	}

	// Sorted by stream index, so the comparison never depends on the order
	// ffprobe happened to list streams in.
	srcAudio := sortedTracks(src.Audio)
	outAudio := sortedTracks(out.Audio)
	for i, want := range srcAudio {
		if i >= len(outAudio) {
			break
		}
		got := outAudio[i]
		if want.Channels > 0 && want.Channels != got.Channels {
			v.fail("audio track %d has %d channel(s) in the original and %d in the archive copy",
				want.Index, want.Channels, got.Channels)
		}
		if want.Codec != "" && got.Codec != "" && want.Codec != got.Codec {
			// -c:a copy means this can never differ. When it does, something
			// re-encoded the audio, which is a bug in the arguments and not a
			// thing to shrug at on the delete path.
			v.fail("audio track %d is %s in the original and %s in the archive copy; "+
				"audio must be copied, never re-encoded", want.Index, want.Codec, got.Codec)
		}
		if opt.AllowMetadataLoss {
			continue
		}
		if want.Language != "" && got.Language != want.Language {
			v.fail("audio track %d lost its language tag (%q became %q)",
				want.Index, want.Language, got.Language)
		}
		if want.Title != "" && got.Title != want.Title {
			v.fail("audio track %d lost its title (%q became %q)",
				want.Index, want.Title, got.Title)
		}
	}
}

// checkDecode reports what a full decode pass of the output found. A container
// can be perfectly well-formed and still hold a truncated frame that only a
// decode finds — and that frame is in the middle of the only copy.
func (v *Verification) checkDecode(decodeErrors []string) {
	n := len(decodeErrors)
	if n == 0 {
		return
	}
	shown := decodeErrors
	if n > MaxReportedDecodeErrors {
		shown = shown[:MaxReportedDecodeErrors]
	}
	v.fail("the archive copy did not decode cleanly (%d error(s)): %s", n, strings.Join(shown, "; "))
}

func (v *Verification) checkSize(src, out FileSummary, opt VerifyOptions) {
	if src.Bytes <= 0 || out.Bytes <= 0 {
		return
	}
	v.SavedBytes = src.Bytes - out.Bytes
	v.SavedPercent = float64(v.SavedBytes) / float64(src.Bytes) * 100
	if out.Bytes >= src.Bytes && !opt.AllowLarger {
		v.fail("the archive copy is %d bytes and the original is %d, so nothing would be reclaimed",
			out.Bytes, src.Bytes)
	}
}

// sortedTracks orders by the audio-stream index, so the comparison never
// depends on the order ffprobe happened to list streams in.
func sortedTracks(in []TrackSummary) []TrackSummary {
	out := append([]TrackSummary(nil), in...)
	sort.Slice(out, func(i, j int) bool { return out[i].Index < out[j].Index })
	return out
}
