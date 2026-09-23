package supervisor

import "time"

// How long each kind of child gets to exit before it is killed.
//
// ONE CONSTANT WAS PAYING FOR THIRTEEN CONSTRUCTION SITES. shutdownGrace is
// sized for its most expensive member -- the recorder, writing a Matroska
// trailer -- and until now every Spec got it unconditionally. A `meters`
// process whose entire output is `-f null` waited the same eight seconds as a
// recording with a file to finalise, and on the production host those waits
// stacked serially: two wedged children cost sixteen seconds of a mode switch,
// three cost twenty-four.
//
// THE DEFAULT IS THE SAFE ONE, AND THAT IS THE WHOLE DESIGN. This is a total
// function over an open set of strings: a kind that is not listed gets the full
// grace. So adding a process and forgetting this file costs latency on its
// teardown and nothing else, while the opposite arrangement -- an opt-in "give
// me longer" field on Spec -- would mean a forgotten flag truncates a
// recording, and a truncated Matroska file is exactly the right size on disk.
// The failure is invisible at the filesystem layer, which is why it must not be
// reachable by forgetting.
//
// MEMBERSHIP IS A CLAIM ABOUT OUTPUT, NOT ABOUT IMPORTANCE. A kind belongs
// here only if killing it mid-write destroys nothing a reader will ever look
// at. That was checked per kind against the argv actually built:
//
//	meters    ffmpeg.MetersArgs   -f null              writes nothing
//	loudness  meters.Args         -f null              writes nothing
//	silence   ffmpeg.SilenceArgs  -f mpegts -> udp://  a socket, not a file
//
// Deliberately ABSENT, with reasons, because the omissions are the load-bearing
// part of a list like this:
//
//	preview     -f hls, and HLS writes segments to disk. They are ephemeral and
//	            self-healing, so this is the marginal case -- but "writes a file"
//	            is the line, and drawing it anywhere softer makes the next
//	            judgement call easier to get wrong.
//	recorder    the reason shutdownGrace is eight seconds.
//	destination a file destination writes a file; the kind does not distinguish
//	            them from RTMP, so all of them keep the full window.
//	ingest      feeds every one of the above.
//	source, rendition, playout
//	            unexamined. Unexamined means default, which means safe.
//
// Adding a kind here requires reading its argv and saying what it writes. A
// guess belongs in the default.
var shortGraceKinds = map[string]time.Duration{
	"meters":   fastGrace,
	"loudness": fastGrace,
	"silence":  fastGrace,
}

// fastGrace is what a child gets when it has nothing to flush.
//
// One second is not tight. The measurement in internal/engine/manager.go puts a
// healthy FFmpeg's response to SIGTERM at 0.105s with its input still flowing,
// so this is roughly ten times the observed need. And it changes nothing for a
// WEDGED child, which is the case that actually costs time: one blocked in a
// timeout-less read does not answer SIGTERM at all, at one second or at eight.
// What this shortens is the wait before we stop pretending it might.
const fastGrace = 1 * time.Second

// wakeAfter is how long a stopping child with a Spec.WakeOnStop gets to answer
// SIGTERM by itself before it is woken, and wakeEvery how often the wake is
// repeated after that.
//
// THE FIRST WAKE MUST LAND AFTER FFMPEG HAS REGISTERED THE SIGTERM, because it
// is the only one that can do anything. A wake releases the last real PES the
// demuxer is holding; it opens an empty one in its place, and every later wake
// only completes an empty PES, which the demuxer never emits. So a wake that
// arrives while FFmpeg still thinks it is transcoding hands it a packet it
// simply forwards, the demux thread goes back to its timeout-less read, and no
// later wake can reach it again: SIGKILL at the grace, no trailer.
//
// When FFmpeg registers the signal is platform-dependent. Its handler only
// counts the signal; the main thread reads that count when sch_wait returns,
// and sch_wait is a pthread_cond_timedwait on the -stats_period tick (0.5s by
// default; nothing here overrides it). On macOS the signal cuts that wait
// short and FFmpeg acts in ~30ms. On Linux, glibc resumes the wait after the
// handler -- and the signal may land on any of FFmpeg's threads anyway -- so
// FFmpeg notices at the next tick, anywhere up to 0.5s later. Measured with
// input flowing, FFmpeg 8.1: Linux 14-446ms, macOS 9-34ms. The first wakeAfter
// here was 0.3s, which on Linux lost that race about half the time.
//
// So wakeAfter is FFmpeg's stats period plus a margin for the main thread to
// be scheduled after the tick. It is still far above the ~0.1s of an ordinary
// stop, which never reaches it, and far inside the 8s grace, which remains the
// backstop. The repeats cover a dropped loopback datagram, the one case in
// which a later wake still has a real packet to release.
const (
	ffmpegStatsPeriod = 500 * time.Millisecond
	wakeAfter         = ffmpegStatsPeriod + 250*time.Millisecond
	wakeEvery         = 500 * time.Millisecond
)

// graceFor returns the shutdown grace for a process kind. Unknown kinds -- and
// the empty string -- get shutdownGrace, which is the point.
func graceFor(kind string) time.Duration {
	if g, ok := shortGraceKinds[kind]; ok {
		return g
	}
	return shutdownGrace
}
