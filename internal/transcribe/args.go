package transcribe

import (
	"path/filepath"
	"strconv"
	"strings"
)

// Command-line builders.
//
// Pure functions from a spec to a []string, in the style of internal/ffmpeg:
// nothing here runs a process, which is what lets the exact argument order be
// pinned by tests instead of discovered in production.

// Whisper's input requirements. It resamples internally if it has to, but its
// front end is trained on 16 kHz mono and feeding it anything else costs both
// accuracy and time. Extracting to exactly this is the whole point of the
// intermediate WAV.
const (
	WhisperSampleRate = 16000
	WhisperChannels   = 1
)

// ExtractSpec describes pulling ONE audio track out of a recording.
type ExtractSpec struct {
	// FFmpeg is the binary, from ffmpeg.Tools.
	FFmpeg string
	// Input is the MKV segment.
	Input string
	// Track is the 0-based audio track index: the N in -map 0:a:N.
	Track int
	// Output is the WAV to write.
	Output string
	// Denoise applies FFmpeg's spectral denoiser before transcription. It is
	// off by default: whisper is robust to noise and afnlmdn is expensive, so
	// this is for the track the operator has already flagged as a bad room.
	Denoise bool
	// StartMS and DurationMS transcribe a slice of the recording. Zero means
	// the whole thing.
	StartMS    int64
	DurationMS int64
	// Progress asks for -progress on stdout so the caller can drive a progress
	// bar with ffmpeg.ParseProgress.
	Progress bool
}

// ExtractArgs builds the FFmpeg command line that produces whisper's input.
//
// Two decisions worth stating. The seek is placed BEFORE -i so FFmpeg seeks by
// index rather than decoding and discarding everything up to the in-point,
// which on a long segment is the difference between a second and a minute. And
// the map is `0:a:N` — the audio-stream-relative form — not `0:N`, because the
// absolute stream index counts the video track and every track would be off by
// one, silently transcribing the wrong microphone.
func ExtractArgs(s ExtractSpec) []string {
	args := []string{"-nostdin", "-hide_banner", "-loglevel", "error", "-y"}
	if s.Progress {
		args = append(args, "-progress", "pipe:1", "-nostats")
	}
	if s.StartMS > 0 {
		args = append(args, "-ss", msToSeconds(s.StartMS))
	}
	args = append(args, "-i", s.Input)
	if s.DurationMS > 0 {
		args = append(args, "-t", msToSeconds(s.DurationMS))
	}
	args = append(args, "-map", "0:a:"+strconv.Itoa(s.Track))
	// Explicitly drop everything that is not this audio track. -vn alone leaves
	// subtitle and data streams that a WAV muxer refuses outright.
	args = append(args, "-vn", "-sn", "-dn")
	if s.Denoise {
		args = append(args, "-af", "afftdn=nf=-25")
	}
	args = append(args,
		"-ac", strconv.Itoa(WhisperChannels),
		"-ar", strconv.Itoa(WhisperSampleRate),
		"-c:a", "pcm_s16le",
		"-f", "wav",
		s.Output,
	)
	return args
}

// WhisperSpec describes one transcription run.
type WhisperSpec struct {
	// Model is the path to the ggml model file.
	Model string
	// Input is the 16 kHz mono WAV from ExtractArgs.
	Input string
	// OutputPrefix is whisper's -of: it appends its own extension.
	OutputPrefix string
	// Language is a two-letter code, or "" / "auto" to detect.
	Language string
	// Translate asks for English output from a non-English source.
	Translate bool
	// Threads is -t. Zero lets whisper choose.
	Threads int
	// Backend selects where the work runs. Only BackendCPU changes the command
	// line; everything else is whisper's own default behaviour.
	Backend Backend
	// JSON asks for the machine-readable output file, which is what gets parsed.
	JSON bool
	// FullJSON additionally asks for per-token probabilities, which is where
	// segment confidence comes from.
	FullJSON bool
	// Progress asks whisper to print percentages while it works.
	Progress bool
	// MaxLen wraps segments at a character count. Zero leaves whisper's own
	// segmentation alone, which is what a transcript wants; a subtitle-only
	// caller sets it.
	MaxLen int
	// Flags is the detected build's flag set, used to skip options this build
	// does not have. A nil Tools means "assume it has everything" — see
	// Tools.HasFlag.
	Flags *Tools
}

// WhisperArgs builds the whisper.cpp command line.
//
// Every optional flag is gated on the build advertising it. whisper.cpp exits
// with a usage dump on an unknown option, so passing --output-json-full to a
// build from before that flag existed does not lose the confidences, it loses
// the whole job.
func WhisperArgs(s WhisperSpec) []string {
	args := []string{"-m", s.Model, "-f", s.Input}

	if s.OutputPrefix != "" {
		args = append(args, "-of", s.OutputPrefix)
	}
	if s.JSON {
		args = append(args, "-oj")
	}
	if s.FullJSON && s.Flags.HasFlag("output-json-full") {
		args = append(args, "-ojf")
	}
	if s.Threads > 0 {
		args = append(args, "-t", strconv.Itoa(s.Threads))
	}
	if lang := normalizeLanguage(s.Language); lang != "" {
		args = append(args, "-l", lang)
	}
	if s.Translate {
		args = append(args, "-tr")
	}
	if s.MaxLen > 0 {
		args = append(args, "-ml", strconv.Itoa(s.MaxLen))
	}
	if s.Progress && s.Flags.HasFlag("print-progress") {
		args = append(args, "-pp")
	}
	// Only the CPU choice is expressible. There is no "use Metal" flag — a
	// build either has a GPU backend compiled in and uses it, or it does not —
	// so the picker's other values are recorded for the operator's benefit and
	// deliberately change nothing on the command line.
	if s.Backend == BackendCPU && s.Flags.HasFlag("no-gpu") {
		args = append(args, "-ng")
	}
	return args
}

// normalizeLanguage turns the UI's notion of a language into whisper's.
//
// Whisper wants "auto" or a short code, and it wants the base language: it has
// no notion of "pt-BR" and rejects it, so a BCP-47 tag from a track annotation
// has to be cut down to its primary subtag rather than passed through.
func normalizeLanguage(lang string) string {
	lang = strings.TrimSpace(strings.ToLower(lang))
	if lang == "" || lang == "auto" {
		return "auto"
	}
	if base, _, found := strings.Cut(lang, "-"); found {
		lang = base
	}
	return lang
}

// JSONPath is where whisper writes its JSON for a given -of prefix.
func JSONPath(outputPrefix string) string { return outputPrefix + ".json" }

// msToSeconds renders milliseconds as the decimal seconds FFmpeg's -ss and -t
// take. Milliseconds matter: a clip in-point rounded to the nearest second
// clips a word in half.
func msToSeconds(ms int64) string {
	if ms < 0 {
		ms = 0
	}
	return strconv.FormatFloat(float64(ms)/1000, 'f', 3, 64)
}

// WAVName is the intermediate filename for one track of one recording, inside a
// scratch directory. It is only ever a temporary, so it optimises for being
// obvious in a directory listing when a job has been left mid-flight.
func WAVName(recording string, track int) string {
	base := strings.TrimSuffix(filepath.Base(recording), filepath.Ext(recording))
	return base + "-a" + strconv.Itoa(track) + ".wav"
}
