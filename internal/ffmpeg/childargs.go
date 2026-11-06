package ffmpeg

import "sync/atomic"

// The log level a kind of child runs at when nothing overrides it. The
// long-running children (ingest, destinations, renditions, playout, the
// selector's feeds) keep the log tail useful with warnings; the one-shot
// post-production passes, whose stderr is quoted back in an error, run at
// error.
const (
	LogWarning = "warning"
	LogError   = "error"
)

// logLevelOverride is POLYEMESIS_FFMPEG_LOGLEVEL, installed once at startup by
// SetLogLevel.
//
// A process-wide value on purpose: it is one setting that applies to every
// FFmpeg child in the server, and the argv builders that read it are stateless
// functions spread over eight packages. Carrying it in each spec struct would
// add the same field, with the same meaning, to twenty of them. It is installed
// before the first child is built and never changes while one runs.
var logLevelOverride atomic.Pointer[string]

// SetLogLevel installs the -loglevel every FFmpeg child gets in place of its own
// default (startupenv.Env.FFmpegLogLevel). Empty leaves each child's default
// alone.
func SetLogLevel(level string) {
	if level == "" {
		logLevelOverride.Store(nil)
		return
	}
	logLevelOverride.Store(&level)
}

// LogLevel is the -loglevel for a child whose own default is def.
//
// Raisable without a rebuild: at `debug` the demuxer reports its PID and PES
// parsing decisions -- which streams it saw, how many frames per PID, and why it
// gave up resolving codec parameters. Nothing has ever asked the demuxer for its
// own account of the failure.
func LogLevel(def string) string {
	if v := logLevelOverride.Load(); v != nil {
		return *v
	}
	return def
}

// CommonArgs are the flags every FFmpeg child polyemesis starts gets.
//
//   - -hide_banner : the build banner is noise in the log tail
//   - -nostdin     : without it, a backgrounded ffmpeg can steal terminal
//     input and silently pause itself
//   - -loglevel    : defaultLevel (LogWarning or LogError) unless
//     POLYEMESIS_FFMPEG_LOGLEVEL says otherwise
//   - -y           : when overwrite, for a child that writes a temporary name it
//     owns and renames on success, so a .partial left by a killed process must
//     not make the retry hang on FFmpeg's interactive overwrite prompt
func CommonArgs(defaultLevel string, overwrite bool) []string {
	args := []string{flagHideBanner, "-nostdin", "-loglevel", LogLevel(defaultLevel)}
	if overwrite {
		args = append(args, "-y")
	}
	return args
}

// ProgressArgs routes machine-readable stats to stdout, leaving stderr as a
// pure human log. -nostats suppresses the duplicate interactive line.
func ProgressArgs() []string {
	return []string{"-nostats", "-progress", "pipe:1"}
}
