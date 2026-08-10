// Package playlistmedia turns one operator upload into the single normalised
// derivative a playlist item is played from.
//
// A playlist is played with FFmpeg's concat demuxer, and the concat demuxer has
// one hard requirement: every file in the list must share the same codecs, the
// same timebase, the same resolution and the same channel layout. Feed it a
// 4K 25 fps ProRes after a 1080p 30 fps H.264 and it either refuses the set or —
// worse — plays it and produces a stream whose audio drifts and whose picture
// tears at the join. Neither is something an operator can diagnose while they
// are on air.
//
// The old answer was to ask the operator to produce matching files by hand.
// That is the wall this package removes: every item is transcoded once, on
// import, to ONE fixed profile, so compatibility holds by construction rather
// than by instruction.
//
// Three rules run through the file:
//
//   - The profile is fixed, not derived. See normaliseArgs.
//   - The derivative is keyed on the UPLOAD, not on the playlist entry, so an
//     upload used twice in a playlist is normalised once. See DerivativePath.
//   - Derivatives live in their own directory under the data directory, never
//     in uploads/. uploads.Store.List reports every file it finds there as an
//     operator upload, so a derivative written beside its source would appear
//     in the media library as a file the operator supplied, be offered as a
//     playlist item in its own right, and be deletable as one. Same reasoning
//     as media.Subdir and clips.Subdir.
//
// Nothing here happens inline. The one worker is a jobs.Worker because a 1080p
// transcode saturates whatever it is given and the live stream owns the
// machine; the queue and its governor decide WHEN, and this package only knows
// HOW.
package playlistmedia

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/ffmpeg"
	"github.com/rainmanjam/polyemesis/internal/jobs"
	"github.com/rainmanjam/polyemesis/internal/media"
	"github.com/rainmanjam/polyemesis/internal/uploads"
)

// Dir is the data-directory child every derivative is written under. See the
// package comment for why this is not optional.
const Dir = "playlist-media"

// KindNormalise transcodes one upload to the fixed playlist profile. The queue
// never interprets a kind, but it is stored in the database and named in the
// resource policy, so it is spelled once here.
const KindNormalise jobs.Kind = "playlist.normalise"

// NormaliseLimit is how many normalisations may run at once.
//
// One, exactly as every kind in internal/media is one. This is an FFmpeg run
// that will take every core it is given, and the point of the queue is that
// heavy work yields to the live stream — two normalisations at once is not
// twice the throughput, it is twice the contention with the thing that must not
// stutter. The global concurrency limit bounds the total across kinds.
const NormaliseLimit = 1

// NormalisedExt is the derivative's extension. MPEG-TS, because it is the
// container the concat demuxer was designed around: every packet carries its
// own timing, so a file can be cut into the middle of a stream without a
// container index that says where anything is.
const NormalisedExt = ".ts"

// PartialSuffix marks a derivative that is still being written.
//
// Every output here is written to <final><PartialSuffix> and renamed into
// place, for the same reason media and clips do it: a half-written derivative
// is indistinguishable from a finished one to the readiness check that will
// decide whether the playlist can go to air, and "the playlist played half a
// file and stopped" is not a bug report anyone can act on. It also makes the
// crash story simple — anything ending in .partial is garbage from a dead
// process.
const PartialSuffix = ".partial"

// DefaultMinFreeBytes is the room that must remain free after a derivative has
// been written.
//
// A derivative is an ADDITIONAL copy of operator media: normalising a playlist
// of six hours of footage writes six hours of 1080p H.264 that was not there
// before. The same 2 GiB reserve the upload path keeps, and for the same
// reason — the database, the recorder and the HLS preview all live on this
// volume, and a transcode that fills it does not fail alone.
const DefaultMinFreeBytes = 2 << 30

// MaxDurationMS is the longest item this job will accept, at a generous
// twenty-four hours.
//
// It is a bound on ARITHMETIC, not a product opinion. estimateBytes multiplies
// the duration by the profile's bitrate, and an absurd value — a probe that
// returned nonsense, a caller that passed microseconds — overflows the int64
// and turns the disk guard's refusal into a message quoting a negative number
// of megabytes. The guard still fails closed, but an error a reader cannot act
// on is the failure this repo keeps having to fix.
const MaxDurationMS = 24 * 60 * 60 * 1000

const (
	defaultFileMode = 0o600
	defaultDirMode  = 0o755
)

// ErrNoSpace is returned when the volume lacks room for the derivative,
// checked BEFORE the transcode rather than discovered halfway through it.
// Mirrors uploads.ErrNoSpace, which is the guard on the way in.
var ErrNoSpace = errors.New("not enough free disk space to normalise this upload")

// ------------------------------------------------------------------ the profile

// The normalised profile. Fixed values, deliberately not derived from anything.
//
// A target derived from the live encoder's settings would move every time an
// operator changed a bitrate or a resolution, and every derivative already on
// disk would silently become incompatible with every derivative made after the
// change — stale with nothing saying so, discovered when a playlist that worked
// last week tears at the second join. A fixed target can only go stale when
// this constant block changes, which is a code review.
//
// 1080p30 is the profile because it is what the destinations this product
// pushes to actually ingest, and because upscaling a 720p source is cheap
// while downscaling a 4K one is the whole point.
const (
	NormaliseWidth  = 1920
	NormaliseHeight = 1080
	// NormaliseFPS is the output frame rate. Constant, not "whatever the source
	// had": the concat demuxer will splice a 25 fps file onto a 30 fps one and
	// let the timestamps collide.
	NormaliseFPS = 30
	// NormaliseGOPFrames is two seconds at NormaliseFPS. Every item starts on a
	// keyframe, which is what lets a join be a join rather than a smear of
	// macroblocks referring to frames from the previous file.
	NormaliseGOPFrames = 2 * NormaliseFPS

	NormaliseVideoEncoder = "libx264"
	NormalisePixFmt       = "yuv420p"
	// NormaliseH264Profile and NormaliseH264Level are what every derivative
	// declares in its bitstream. High at 4.0 is what 8-bit 4:2:0 1080p30 under
	// 25 Mbit/s is, and it is what every destination this product pushes to
	// decodes. See buildNormalise for why they are stated rather than derived.
	NormaliseH264Profile = "high"
	NormaliseH264Level   = "4.0"
	// NormalisePreset trades ratio for speed. This job runs ahead of air with
	// nobody watching it, but it still competes with whatever is live.
	NormalisePreset = "veryfast"
	// NormaliseVideoKbps is capped rather than CRF on purpose: a playlist's
	// items are pushed to the same destinations as a live stream, so the
	// derivative has to respect the same ceiling, and a bounded bitrate is also
	// what makes the disk estimate below meaningful.
	NormaliseVideoKbps = 6000

	NormaliseAudioEncoder = "aac"
	NormaliseAudioKbps    = 192
	NormaliseSampleRate   = 48000
	NormaliseChannels     = 2
)

// commonArgs are the flags every child process here gets. It mirrors
// internal/ffmpeg's unexported commonArgs rather than importing it, because
// that one is not exported; the -y is the addition. Every output is written to
// a .partial path, and a .partial left behind by a killed process must not make
// the retry hang on FFmpeg's interactive overwrite prompt.
func commonArgs() []string {
	return []string{"-hide_banner", "-nostdin", "-loglevel", "warning", "-y"}
}

// progressArgs routes machine-readable stats to stdout, leaving stderr as a
// pure human log — the same split internal/ffmpeg uses, so ffmpeg.ParseProgress
// reads our children too.
func progressArgs() []string {
	return []string{"-nostats", "-progress", "pipe:1"}
}

// normaliseArgs builds the transcode of one upload to the fixed profile.
//
// The hardware probe is deliberately NOT consulted, the same call internal/media
// makes about its proxies and for a sharper version of the same reason. The GPU
// is the one resource a live encoder cannot share: a normalisation that asked
// for it would either be held back by the governor until every ingest was down,
// or — if it ever slipped past — would contend with an encoder that is ON AIR.
// This job produces a file nobody is waiting on this second. Losing that race
// trades a stream for a file, so the cheapest way to never be in it is to never
// want the GPU.
func normaliseArgs(in, out string, videoSecs, audioSecs float64, maxBytes int64) []string {
	return buildNormalise(in, out, false, videoSecs, audioSecs, maxBytes)
}

// normaliseSilentArgs is the same profile for a source with no audio track.
//
// It is not a convenience. The concat demuxer matches streams by position, so a
// video-only item in a playlist of stereo items is exactly the mismatch this
// package exists to prevent — it does not merely play without sound, it breaks
// the set. Silence is synthesised at the profile's own sample rate and channel
// count so that a slate, a bumper or a title card is a first-class item.
//
// It takes no videoSecs or audioSecs, unlike normaliseArgs: -shortest already
// gives it a stop that costs nothing, because the audio here is synthesised TO
// MATCH the picture rather than recovered from operator media. See
// buildNormalise.
func normaliseSilentArgs(in, out string, maxBytes int64) []string {
	return buildNormalise(in, out, true, 0, 0, maxBytes)
}

// PadSlackSecs is added to the computed pad target below, to survive the gap
// between what a SOURCE probe reports and what the RE-ENCODE actually
// produces (AAC frame alignment, GOP boundaries at a different frame rate
// than the source). In the safe direction only: at most a few extra
// milliseconds of trailing silence or a repeated last frame, never a file
// shorter than the source. Kept small deliberately -- see buildNormalise's
// pad target computation for why a generous margin is the wrong instinct
// here, unlike an unbounded pad that needed a hard stop.
const PadSlackSecs = 0.05

// buildNormalise is the single source of the profile. The silent variant
// differs only in where the audio comes from and in the padding described
// below; every encoding flag shared between them lives in one place, so the
// two outputs cannot drift apart into two profiles.
func buildNormalise(in, out string, silent bool, videoSecs, audioSecs float64, maxBytes int64) []string {
	args := commonArgs()
	args = append(args, progressArgs()...)
	args = append(args, "-i", in)
	if silent {
		args = append(args, "-f", "lavfi", "-i",
			fmt.Sprintf("anullsrc=channel_layout=stereo:sample_rate=%d", NormaliseSampleRate))
	}

	// Explicit maps. Default stream selection would pick the "best" audio track
	// by its own rules on a source with several, and "whichever track FFmpeg
	// liked" is not something an operator can predict or a UI can label.
	args = append(args, "-map", "0:v:0")
	if silent {
		// -shortest, so the synthesised silence ends with the picture rather
		// than running until the heat death of the universe: anullsrc has no
		// duration of its own.
		//
		// This is the ONE place -shortest belongs. Everywhere else on the
		// operator-media path it is exactly the defect PAD, NEVER TRUNCATE
		// exists to remove -- see the pad filters below -- but here there is
		// no operator audio behind the shorter stream to lose: the silence
		// was generated FOR this picture, so ending it with the picture
		// discards nothing.
		args = append(args, "-map", "1:a:0", "-shortest")
	} else {
		args = append(args, "-map", "0:a:0")
	}
	// Subtitles, chapters and attachments have no business in a playout file,
	// and an attachment stream is a common reason a mux refuses to start.
	args = append(args, "-sn", "-dn", "-map_chapters", "-1")

	// Letterboxed, never stretched or cropped: the operator supplied the framing
	// and a 4:3 archive clip squeezed to 16:9 is a defect the operator will be
	// blamed for. setsar=1 is load-bearing — two files can agree on 1920x1080
	// and still disagree on sample aspect ratio, and the concat demuxer counts
	// that as a mismatch.
	// The pad target is the LONGER of the source's own two tracks, plus a
	// small slack for the re-encode's own rounding (PadSlackSecs). Computed
	// here, once, and used by both filters below: tpad only has something to
	// do when video is the shorter track, apad only when audio is, and
	// exactly one of those is ever true for a real mismatch.
	target := videoSecs
	if audioSecs > target {
		target = audioSecs
	}
	target += PadSlackSecs
	videoPad := target - videoSecs
	if videoPad < 0 {
		videoPad = 0
	}

	vf := fmt.Sprintf(
		"scale=%d:%d:force_original_aspect_ratio=decrease:force_divisible_by=2,"+
			"pad=%d:%d:(ow-iw)/2:(oh-ih)/2:black,setsar=1",
		NormaliseWidth, NormaliseHeight, NormaliseWidth, NormaliseHeight)
	if !silent {
		// tpad's stop_duration ADDS this many seconds of cloned final frames.
		// It is zero -- a genuine no-op -- whenever video is already the
		// longer or equal track, and the exact shortfall otherwise: unlike an
		// unbounded pad, this needs no -shortest and no external cutoff to
		// stop, because it is a fixed, finite amount of tape by construction.
		vf += ",tpad=stop_mode=clone:stop_duration=" +
			strconv.FormatFloat(videoPad, 'f', 3, 64)
	}
	args = append(args, "-vf", vf)

	args = append(args,
		"-c:v", NormaliseVideoEncoder,
		"-preset", NormalisePreset,
		"-pix_fmt", NormalisePixFmt,
		"-r", strconv.Itoa(NormaliseFPS),
		// Profile and level are STATED rather than left to be derived.
		//
		// x264 picks both from the preset, the pixel format and the resolution,
		// so today every derivative comes out High@4.0 whether or not we ask.
		// That is a derivation, and a derivation is exactly what the rest of
		// this block refuses: an FFmpeg upgrade that changed the default would
		// leave every derivative made after it at a different profile from
		// every derivative made before, with nothing saying so and a decoder
		// somewhere refusing the second half of a playlist. It is the one
		// remaining way the fixed target could move without a code review.
		"-profile:v", NormaliseH264Profile,
		"-level", NormaliseH264Level,
	)
	// A keyframe every GOP with no scene-cut exceptions and no open GOP. Counted
	// in frames rather than seconds because -r above has already fixed the frame
	// rate, so the two cannot disagree. An open GOP would let the first frames
	// of an item reference pictures that, after the splice, belong to a
	// different file.
	args = append(args,
		"-g", strconv.Itoa(NormaliseGOPFrames),
		"-keyint_min", strconv.Itoa(NormaliseGOPFrames),
		"-sc_threshold", "0",
		"-flags", "+cgop",
		"-b:v", strconv.Itoa(NormaliseVideoKbps)+"k",
		"-maxrate", strconv.Itoa(NormaliseVideoKbps)+"k",
		"-bufsize", strconv.Itoa(NormaliseVideoKbps*2)+"k",
	)

	// aresample with async and first_pts=0 fills the gap a source with a late or
	// ragged audio start would otherwise carry into the playlist as a drift that
	// accumulates across every item after it.
	af := "aresample=async=1:first_pts=0"
	if !silent {
		// apad's whole_dur is an ABSOLUTE target duration for the audio
		// stream, unlike tpad's additive stop_duration -- which is exactly
		// why the video side above computes a delta and this one does not.
		// It is a no-op whenever audio is already at least this long, and
		// extends it with silence otherwise. Same reasoning as tpad: a fixed,
		// finite target, so no -shortest and no external cutoff is needed to
		// stop it.
		af += ",apad=whole_dur=" + strconv.FormatFloat(target, 'f', 3, 64)
	}
	args = append(args,
		"-c:a", NormaliseAudioEncoder,
		"-b:a", strconv.Itoa(NormaliseAudioKbps)+"k",
		"-ac", strconv.Itoa(NormaliseChannels),
		"-ar", strconv.Itoa(NormaliseSampleRate),
		"-af", af,
	)

	// MPEG-TS is the container AND the timebase decision: TS timestamps are
	// 90 kHz by definition, so every derivative shares one whether or not the
	// sources did. -muxdelay/-muxpreload 0 remove the muxer's default 0.7 s
	// offset, which would otherwise appear at the front of every item and add
	// up over a long playlist.
	// -fs IS A HARD STOP ON THE WRITER, and it is here because nothing else in
	// this argv is one.
	//
	// Everything above bounds the output's SHAPE -- resolution, frame rate,
	// bitrate ceiling -- and nothing bounds its LENGTH, which is whatever the
	// input turns out to be. That was survivable only while the input was
	// assumed to be media. It is not assumed any more (see SourceVerifier), and
	// -fs is what makes the disk guard an enforced bound rather than a
	// prediction: checkSpace demands room for maxBytes and FFmpeg physically
	// cannot write more than maxBytes, so the two cannot disagree.
	//
	// FFmpeg stops CLEANLY at the limit and exits 0, so this alone would publish
	// a short derivative. RunNormalise compares the finished size against the
	// same figure and refuses to publish one that reached it -- the cap catches
	// the runaway, that check stops the runaway becoming an item that dies
	// halfway through on air.
	//
	// Zero means no cap, for a caller that has no estimate to give. Every
	// production path has one.
	if maxBytes > 0 {
		args = append(args, "-fs", strconv.FormatInt(maxBytes, 10))
	}
	args = append(args, "-muxdelay", "0", "-muxpreload", "0", "-f", "mpegts", out)
	return args
}

// ------------------------------------------------------------------- the paths

// ProfileVersion identifies what a derivative CONTAINS, and it is part of the
// derivative's filename rather than a sidecar.
//
// DerivativePath is keyed on the upload's name, and the enqueue path
// (api.Server.enqueuePlaylistNormalisation) skips any upload whose derivative
// already exists. So without a version in the path, a change to the encode is
// invisible: every derivative written by the previous profile is silently
// reused while readiness reports the item ready. B2's padding and measured
// duration are exactly such a change, and B1's files have neither.
//
// Bump this whenever the encode changes what the output contains. Re-encoding
// is the cost; concatenating a file that predates the contract is the
// alternative.
const ProfileVersion = 2

// DerivativePath is where one upload's normalised copy lives.
//
// Keyed on the upload's stored name and nothing else, which is what makes "the
// same upload used twice in a playlist normalises once" fall out of the design
// rather than out of a check somebody has to remember to write. The playlist
// entry's identity — its position, the playlist it belongs to — deliberately
// does not appear.
//
// The upload's extension is KEPT and .ts appended, rather than replaced.
// Stripping it would map "show-1a2b.mp4" and "show-1a2b.mkv" onto one
// derivative, and the loser of that collision would play the other operator's
// file.
//
// upload is a stored upload name, never a path, so it is reduced to its base
// name before it is joined — the same defence media.LayoutFor applies to a
// recording name. ValidUploadName is the check; this function has to stay
// total because callers use it to build a URL and a readiness answer.
//
// db.PlaylistUploadName, never a private strings.TrimSpace: it is the one
// place a playlist item's name is trimmed, and this was its last recorded
// exception, carried because this package could not import internal/db when
// that rule was written. It can now, so the exception is closed rather than
// carried further.
func DerivativePath(dataDir, upload string) string {
	name := filepath.Base(db.PlaylistUploadName(upload))
	return filepath.Join(dataDir, Dir,
		fmt.Sprintf("%s.v%d%s", name, ProfileVersion, NormalisedExt))
}

// DerivativeVersions lists every version of one upload's derivative that is
// actually on disk, which is what deletion has to remove: a profile bump can
// leave more than one version of the same upload's derivative behind.
//
// IT READS THE DIRECTORY AND COMPARES NAMES. It does not build a glob, and that
// is the whole point of the function.
//
// The version this replaced returned `<name>.v*<ext>` for filepath.Glob, and the
// name reaching it comes from a URL path segment. ValidUploadName rejects
// separators and control characters but says nothing about `*`, `?` or `[` --
// they are legal in a filename -- so `DELETE /api/v1/media/%2A` produced the
// pattern `<dataDir>/playlist-media/*.v*.ts` and the caller deleted EVERY
// DERIVATIVE IN THE INSTALL, before the name was ever validated. A `[` would
// meanwhile fail the pattern parse and 500.
//
// Matching by equality means a metacharacter is just a character, which is what
// it always was on disk. There is no pattern for a caller to smuggle anything
// through, so nothing downstream has to remember to escape one.
func DerivativeVersions(dataDir, upload string) ([]string, error) {
	name := filepath.Base(db.PlaylistUploadName(upload))
	// A name that could escape the directory has no derivatives by definition,
	// and asking the filesystem about it is not this function's job.
	if name == "" || name == "." || name == ".." {
		return nil, nil
	}
	entries, err := os.ReadDir(filepath.Join(dataDir, Dir))
	if err != nil {
		if os.IsNotExist(err) {
			// No derivative directory yet is not an error: nothing has been
			// normalised, so there is nothing of this upload's to remove.
			return nil, nil
		}
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if !isDerivativeOf(e.Name(), name) {
			continue
		}
		out = append(out, filepath.Join(dataDir, Dir, e.Name()))
	}
	return out, nil
}

// isDerivativeOf reports whether file is `<upload>.v<digits><ext>`.
//
// The digits are checked rather than skipped: `show.mp4.vNOPE.ts` is not a
// derivative this package ever wrote, and a deletion that removed it would be
// deleting somebody else's file on the strength of a prefix match.
func isDerivativeOf(file, upload string) bool {
	rest, ok := strings.CutPrefix(file, upload+".v")
	if !ok {
		return false
	}
	digits, ok := strings.CutSuffix(rest, NormalisedExt)
	if !ok || digits == "" {
		return false
	}
	for _, r := range digits {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// DerivativeDir is where every upload's derivative lives.
func DerivativeDir(dataDir string) string { return filepath.Join(dataDir, Dir) }

// ValidUploadName reports whether a name is one this package will touch.
//
// Deliberately narrow and checked before any path is built: the name arrives in
// a job's params, which came from an HTTP request, and everything downstream
// creates and removes files. The separator check tests BOTH separators on every
// platform rather than os.PathSeparator, because that constant's meaning
// changes with GOOS and the bug it caused in internal/recording is written up
// in uploads.Store.Resolve.
func ValidUploadName(name string) bool {
	if name == "" || name == "." || name == ".." || len(name) > 255 {
		return false
	}
	if strings.ContainsAny(name, `/\`) {
		return false
	}
	return !strings.ContainsAny(name, "\x00\n\r")
}

// NormaliseTarget is the canonical jobs.Target for a normalisation.
//
// It names the UPLOAD, which is what makes the queue's Unique fold do the
// deduplication for us: submitting a playlist whose second and fifth items are
// the same file produces one job, not two, because both submissions carry the
// same kind and the same target.
func NormaliseTarget(upload string) string { return "upload:" + upload }

// --------------------------------------------------------------- the processor

// Registry is the part of *jobs.Queue this package needs. An interface so the
// processor can be registered against a fake in tests, and so this package does
// not depend on the queue's construction.
type Registry interface {
	Register(kind jobs.Kind, limit int, w jobs.Worker) error
}

var _ Registry = (*jobs.Queue)(nil)

// Resolver turns a stored upload name into an absolute path, refusing anything
// that escapes the uploads directory. An interface so the worker is testable
// without a Store, and so this package never re-derives the confinement rule
// that internal/uploads already owns.
type Resolver interface {
	Resolve(name string) (string, error)
}

var _ Resolver = (*uploads.Store)(nil)

// Config is what the processor needs from the rest of the server.
type Config struct {
	// FFmpeg and FFprobe are the detected binaries. Empty means detection never
	// ran or failed, which fails the job with a clear message rather than
	// preventing startup — a playlist is one feature among many.
	FFmpeg  string
	FFprobe string
	// DataDir is the server's data directory; derivatives live under it.
	DataDir string
	// Uploads resolves a stored upload name to a path, normally *uploads.Store.
	Uploads Resolver
	// MinFreeBytes is the room that must remain after the derivative is
	// written. There is no "off": the reserve protects the database and the
	// recorder, not this job, so zero means the default rather than no guard.
	MinFreeBytes uint64
}

// Normalized fills the defaults.
func (c Config) Normalized() Config {
	if c.MinFreeBytes == 0 {
		c.MinFreeBytes = DefaultMinFreeBytes
	}
	return c
}

// Stream kinds, spelled as ffprobe's -select_streams spells them.
const (
	streamVideo = "v"
	streamAudio = "a"
)

// StreamProber reports whether a file carries at least one stream of a kind. A
// field on Processor so every path through the worker — the silent source, the
// audio-only source, the file FFprobe cannot read — is reachable in a test on a
// machine with no media and no FFprobe on it.
type StreamProber func(ctx context.Context, path, kind string) (bool, error)

// DurationProber reports a source's own video and audio track durations, in
// seconds. A field on Processor for the same reason StreamProber is one:
// chooseProfile calls it on the operator-media path to compute the pad
// filters described at buildNormalise, and a test exercising that path must
// be able to answer without a real FFprobe on the machine.
//
// Per-stream rather than a single container-level number: buildNormalise has
// to know WHICH of the two tracks is shorter to decide which filter has
// anything to do, and by how much -- a single combined duration cannot
// answer either question.
type DurationProber func(ctx context.Context, path string) (videoSecs, audioSecs float64, err error)

// SourceVerifier re-establishes, at the moment of use, that a file is
// self-contained media -- and reports its duration, which is the number the
// disk guard was previously guessing at.
//
// THE SECOND GATE, and the reason it exists is that the first one is not
// reachable from here and cannot be relied on to have run.
// internal/ffmpeg.ProbeFile's format allowlist had EXACTLY ONE production call
// site, the upload handler, and that handler's gate is triggerable by the
// client: the probe runs under the request's context, so a caller that sends a
// complete body and drops the connection gets the file stored with no
// inspection at all. The upload path now records that, and the settings
// validator refuses an item naming such a file -- but neither of those covers
// an item the operator inherited, a file that predates verdicts, or a file put
// in the uploads directory by hand, and all three arrive here.
//
// What arrived here before was `ffmpeg -i <path>` with no -f and no
// -protocol_whitelist (see buildNormalise), and a 3 KB ffconcat script naming
// one clip two hundred times was measured producing a 50 MB derivative in 8
// seconds at 1143% CPU -- 15,517x amplification, on the box that is carrying a
// live broadcast, past a free-space guard that had been told to expect 3 KB.
//
// A field on Processor for the same reason StreamProber is one: the worker's
// paths must be reachable in a test on a machine with no media and no FFprobe.
type SourceVerifier func(ctx context.Context, path string) (durationSecs float64, err error)

// Processor runs the normalisation job.
type Processor struct {
	log    *slog.Logger
	cfg    Config
	exec   media.Execer
	stream StreamProber
	dur    DurationProber
	verify SourceVerifier
	free   func(path string) (uint64, error)
	// beforePublish is a test seam only -- see RunNormalise's pre-publish
	// recheck for why the hook exists at all.
	beforePublish func()
}

// Option customises a Processor, chiefly for tests.
type Option func(*Processor)

// WithExecer replaces the subprocess runner, so the worker can be exercised
// without FFmpeg on the machine.
func WithExecer(e media.Execer) Option { return func(p *Processor) { p.exec = e } }

// WithStreamProber replaces the stream-presence check.
func WithStreamProber(s StreamProber) Option { return func(p *Processor) { p.stream = s } }

// WithDurationProber replaces the source-duration check.
func WithDurationProber(d DurationProber) Option { return func(p *Processor) { p.dur = d } }

// WithSourceVerifier replaces the format re-check. See SourceVerifier for why
// there is a second gate here at all.
func WithSourceVerifier(v SourceVerifier) Option { return func(p *Processor) { p.verify = v } }

// WithFreeSpace replaces the free-space reporter, so a full disk can be
// simulated without one.
func WithFreeSpace(fn func(path string) (uint64, error)) Option {
	return func(p *Processor) { p.free = fn }
}

// New builds a Processor.
//
// media.Exec is imported rather than copied. It is the only piece of
// internal/media used here — nothing recording-shaped comes with it — and it
// carries the cancellation behaviour that keeps a killed job from leaving an
// FFmpeg behind competing with the live stream. A second copy of that would be
// a second place for it to be subtly wrong.
func New(log *slog.Logger, cfg Config, opts ...Option) *Processor {
	p := &Processor{log: log, cfg: cfg.Normalized(), exec: media.Exec, free: uploads.FreeBytes}
	p.stream = p.probeStream
	p.dur = p.probeDuration
	p.verify = p.verifySource
	for _, opt := range opts {
		opt(p)
	}
	return p
}

// Register wires the worker into a queue.
func (p *Processor) Register(r Registry) error {
	return r.Register(KindNormalise, NormaliseLimit, jobs.WorkerFunc(p.RunNormalise))
}

// ------------------------------------------------------------------ job params

// NormaliseParams is a normalisation job's payload.
type NormaliseParams struct {
	// Upload is the STORED UPLOAD NAME, not a path and not a playlist position.
	Upload string `json:"upload"`
	// DurationMS is the source's duration, used for the progress bar and for
	// the disk estimate.
	//
	// NOTHING POPULATES IT TODAY, and that is a stated cost rather than an
	// oversight. The only production submitter is the settings handler
	// (api.Server.enqueuePlaylistNormalisation), which would have to run
	// ffprobe on operator media inside an HTTP request to fill it in -- a
	// synchronous subprocess in the request path, on a machine that may be
	// carrying a live stream. So every real job arrives with zero, and two
	// things are weaker for it: the progress bar moves without a percentage,
	// and checkSpace estimates the derivative from the SOURCE'S SIZE instead of
	// duration x bitrate, which estimateBytes reports as bounded=false -- a
	// guess that is logged and still fails closed, not a guard that is skipped.
	// A caller that has a probed duration to hand should pass it.
	DurationMS int64 `json:"durationMs,omitempty"`
}

// Validate rejects params no attempt can succeed with.
func (p NormaliseParams) Validate() error {
	if !ValidUploadName(p.Upload) {
		return fmt.Errorf("invalid upload name %q", p.Upload)
	}
	if p.DurationMS < 0 || p.DurationMS > MaxDurationMS {
		return fmt.Errorf("duration %d ms is not a playlist item's duration", p.DurationMS)
	}
	return nil
}

// NormaliseResult is what the worker hands back.
type NormaliseResult struct {
	// Path is the derivative on disk.
	Path  string `json:"path"`
	Bytes int64  `json:"bytes"`
	// Silent records that the source had no audio and the profile's silence was
	// synthesised, so an operator who expected sound can see why there is none.
	Silent bool `json:"silent,omitempty"`
	// Reused records that the derivative was already there and nothing was
	// re-encoded.
	Reused bool `json:"reused,omitempty"`
	// DurationMS is the ENCODED OUTPUT's own duration, measured by
	// ProbeOutputDurationMS after publication -- never NormaliseParams.
	// DurationMS, which is a source-side estimate that is never populated in
	// production (see that field) and in any case describes a file the
	// padding and re-encode have already changed.
	//
	// This is not needed for concat correctness: Task 1 measured that a
	// concat list's per-entry duration directive makes no difference to the
	// packet stream FFmpeg actually plays. What it IS needed for is Task 7's
	// operator playlist editor, which shows each item's length -- the
	// difference between building a programme and guessing at one. A reused
	// derivative carries zero here rather than re-probing a file this run did
	// not write; see RunNormalise.
	DurationMS int64 `json:"durationMs,omitempty"`
}

// NewNormaliseJob builds the queue entry.
//
// Unique on the upload, so the same file appearing three times in a playlist
// produces one transcode. Normal priority rather than bulk: nobody is watching
// it, but the playlist cannot go to air until it lands, so it must not sit
// behind an archive sweep.
func NewNormaliseJob(p NormaliseParams) (jobs.Job, error) {
	if err := p.Validate(); err != nil {
		return jobs.Job{}, err
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return jobs.Job{}, fmt.Errorf("encode %s params: %w", KindNormalise, err)
	}
	return jobs.Job{
		Kind:     KindNormalise,
		Target:   NormaliseTarget(p.Upload),
		Params:   raw,
		Priority: jobs.PriorityNormal,
		Unique:   true,
	}.Normalized(), nil
}

// ---------------------------------------------------------------- the worker

// RunNormalise transcodes one upload to the fixed profile.
func (p *Processor) RunNormalise(ctx context.Context, job jobs.Job, rep jobs.Reporter) error {
	var params NormaliseParams
	if err := decodeParams(job, &params); err != nil {
		return err
	}
	if err := params.Validate(); err != nil {
		return jobs.Permanent(err)
	}
	if p.cfg.FFmpeg == "" || p.cfg.FFprobe == "" {
		return jobs.Permanent(errors.New(
			"FFmpeg was not detected, so playlist items cannot be normalised"))
	}
	if p.cfg.Uploads == nil {
		return jobs.Permanent(errors.New("no upload store is configured"))
	}

	input, err := p.cfg.Uploads.Resolve(params.Upload)
	if err != nil {
		return jobs.Permanent(err)
	}
	info, err := os.Stat(input)
	if err != nil {
		// An upload that is not on disk is not coming back; retrying would burn
		// attempts on a file the operator deleted.
		return jobs.Permanent(fmt.Errorf("upload %s: %w", params.Upload, err))
	}

	final := DerivativePath(p.cfg.DataDir, params.Upload)
	// The work already done is not done again. The derivative is keyed on the
	// upload and an upload's stored name is unique and its bytes never change,
	// so an existing non-empty derivative is the derivative for these bytes —
	// re-encoding it would be an hour of CPU spent reproducing a file we
	// already have, once per playlist that mentions the same item.
	if st, err := os.Stat(final); err == nil && st.Size() > 0 {
		rep.Logf("%s is already normalised; nothing to do", params.Upload)
		rep.SetResult(NormaliseResult{Path: final, Bytes: st.Size(), Reused: true})
		rep.Progress(1)
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(final), defaultDirMode); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(final), err)
	}

	// VERIFY BEFORE ANY FFMPEG IS BUILT, let alone run. See SourceVerifier: the
	// upload gate is triggerable by the client, and three routes reach this
	// worker without ever passing it. Everything below -- the disk estimate, the
	// output cap, the profile choice -- assumes the input is media, and this is
	// the line that makes the assumption true rather than hopeful.
	durationSecs, err := p.verify(ctx, input)
	if err != nil {
		return err
	}

	// THE DISK GUARD NOW HAS A REAL DURATION, which is what makes it a bound
	// rather than a guess.
	//
	// It used to be handed params.DurationMS, which the only production
	// submitter deliberately leaves at zero (see NormaliseParams.DurationMS), so
	// estimateBytes always fell back to the SOURCE'S SIZE and reported
	// bounded=false. That is not a weak bound, it is the wrong quantity: the
	// source's size and the derivative's size are related only for ordinary
	// media, and the input that matters is precisely the one where they are not.
	// The probe above already had to read the container, so the duration costs
	// nothing extra.
	durationMS := params.DurationMS
	if ms := int64(durationSecs * 1000); ms > durationMS {
		durationMS = ms
	}
	if durationMS > MaxDurationMS {
		// MaxDurationMS was inert while nothing populated a duration. Now that
		// something does, it is what it always said it was: a bound on the
		// arithmetic below, and a refusal for an item no playlist wants.
		return jobs.Permanent(fmt.Errorf(
			"%s is %d hours long, which is past the %d-hour limit for a playlist item",
			params.Upload, durationMS/3600000, MaxDurationMS/3600000))
	}
	estimate, bounded := estimateBytes(durationMS, info.Size())
	if !bounded {
		// REFUSED RATHER THAN GUESSED AT. The fallback -- estimate the
		// derivative from the SOURCE'S size -- is not a weaker bound, it is a
		// measurement of the wrong thing, and it is the number the 15,517x
		// amplification walked straight past. It is also useless as a cap: a
		// short low-resolution source normalises LARGER than it arrived, so
		// handing the source's size to -fs would truncate honest media.
		//
		// Nothing legitimate should land here any more. The probe above has
		// already read the container; a container that will not say how long it
		// is, after being accepted as self-contained media, is a file this
		// worker cannot size, cannot cap and cannot schedule -- and the operator
		// can act on that sentence, which is more than a silent guess gave them.
		return jobs.Permanent(fmt.Errorf(
			"polyemesis could not work out how long %s is, so it cannot be normalised "+
				"safely; re-save it as MP4 or MPEG-TS and upload it again", params.Upload))
	}
	if err := p.checkSpace(filepath.Dir(final), estimate, bounded, rep); err != nil {
		return err
	}

	partial := final + PartialSuffix
	args, silent, err := p.chooseProfile(ctx, params.Upload, input, partial, estimate)
	if err != nil {
		return err
	}
	if silent {
		rep.Logf("%s has no audio track; synthesising silence so the item still matches the playlist profile", params.Upload)
	}

	rep.Logf("normalising %s to %dx%d %d fps", params.Upload,
		NormaliseWidth, NormaliseHeight, NormaliseFPS)
	if err := p.run(ctx, rep, media.Command{Name: p.cfg.FFmpeg, Args: args}, durationMS); err != nil {
		// A failed encode has already lost; a leftover .partial is then a
		// disk-space problem the next attempt inherits.
		p.discard(partial)
		return err
	}

	// DID THE OUTPUT CAP FIRE? -fs stops FFmpeg cleanly, so a run that hit it
	// exits 0 with a SHORT file, and publishing that would put a derivative on
	// air that stops in the middle. The cap is far above what the profile
	// produces for a source of the measured duration, so reaching it means the
	// estimate and the reality have parted company -- which is the state the cap
	// exists to catch, and is not a state to paper over by publishing anyway.
	if st, err := os.Stat(partial); err == nil && st.Size() >= estimate {
		p.discard(partial)
		return jobs.Permanent(fmt.Errorf(
			"normalising %s produced more than the %d MiB its duration allows for; "+
				"nothing was published", params.Upload, estimate>>20))
	}

	// The upload may have been deleted while this job ran -- a transcode of a
	// 1080p file is not instant, and DELETE /media/{name} does not know or wait
	// for a job it did not start. Publishing now would recreate exactly the
	// orphan that delete rule exists to remove, with no upload left on disk to
	// explain where the derivative came from.
	//
	// The check is HERE, at the last atomic step, rather than at dequeue: it
	// closes the window without the queue needing to support cancellation, and
	// checking any earlier would still leave the whole transcode's duration as
	// a window the earlier check could not close.
	if p.beforePublish != nil {
		p.beforePublish() // test seam only
	}
	if _, err := os.Stat(input); err != nil {
		p.discard(partial)
		return jobs.Permanent(fmt.Errorf("upload %s was deleted while it was being "+
			"normalised; nothing was published", params.Upload))
	}

	if err := publish(partial, final); err != nil {
		return err
	}
	// 0600, matching uploads.Save. Nothing outside this process reads a
	// derivative: the playout FFmpeg runs as the same user, and widening the
	// mode on a copy of operator media buys no reader that exists.
	if err := os.Chmod(final, defaultFileMode); err != nil {
		return fmt.Errorf("chmod %s: %w", filepath.Base(final), err)
	}

	res := NormaliseResult{Path: final, Silent: silent}
	if st, err := os.Stat(final); err == nil {
		res.Bytes = st.Size()
	}
	// Measured from the file this process just wrote, never from
	// params.DurationMS -- see NormaliseResult.DurationMS. Best-effort: a
	// derivative that FFmpeg just finished writing and this process just
	// renamed into place is about as likely to fail a probe as any file this
	// package touches, but a probe failure here is not a reason to undo a
	// publish that has already succeeded.
	if ms, err := ProbeOutputDurationMS(ctx, p.cfg.FFprobe, final); err == nil {
		res.DurationMS = ms
	} else if p.log != nil {
		p.log.Warn("could not measure a published derivative's duration",
			"upload", params.Upload, "err", err)
	}
	rep.SetResult(res)
	rep.Logf("normalised copy written to %s", filepath.Base(final))
	return nil
}

// chooseProfile picks the argv for one source, and refuses what cannot be made
// into a playlist item at all.
//
// Video is REQUIRED. uploads.allowedExt accepts .wav, .flac, .aac, .mp3 and
// .m4a, so an audio-only upload is reachable straight from the media library.
// Without this check FFmpeg exits with "Stream map '0:v:0' matches no streams",
// which comes back through p.run unclassified and therefore RETRYABLE: the
// queue would spend every attempt re-running a transcode that can never
// succeed, contending with the live stream each time, and the operator would be
// shown FFmpeg's sentence instead of the answer.
//
// It is deliberately NOT the mirror of the silent case below. Synthesising
// silence for a video keeps a real picture on air; synthesising black for an
// audio-only file would put a black screen on air, which is the exact thing an
// operator reaches for a slate to avoid. Refusing permanently is the honest
// answer, and it is what stops the retry burn.
func (p *Processor) chooseProfile(ctx context.Context, upload, input, out string, maxBytes int64) (args []string, silent bool, err error) {
	hasVideo, err := p.stream(ctx, input, streamVideo)
	if err != nil {
		return nil, false, err
	}
	if !hasVideo {
		return nil, false, jobs.Permanent(fmt.Errorf(
			"%s has no video track, and a playlist item must contain video; "+
				"give it a picture or leave it out of the playlist", upload))
	}
	hasAudio, err := p.stream(ctx, input, streamAudio)
	if err != nil {
		return nil, false, err
	}
	if !hasAudio {
		return normaliseSilentArgs(input, out, maxBytes), true, nil
	}
	// The source's own two track durations are what buildNormalise's pad
	// filters are computed from -- see the comment there. A file that has
	// already answered the two stream-presence probes above but cannot
	// answer this one is exactly as unreadable as one that fails the first:
	// permanent, for the same reason probeStream is.
	videoSecs, audioSecs, err := p.dur(ctx, input)
	if err != nil {
		return nil, false, err
	}
	return normaliseArgs(input, out, videoSecs, audioSecs, maxBytes), false, nil
}

// checkSpace refuses the transcode when the volume could not survive it.
//
// Checked before the write, and FAIL CLOSED when the check itself errors —
// the same direction uploads.Save takes, and for the same reason: the one case
// where you cannot tell how much room is left is not the case to start writing
// gigabytes.
func (p *Processor) checkSpace(dir string, estimate int64, bounded bool, rep jobs.Reporter) error {
	if p.free == nil {
		return nil
	}
	free, err := p.free(dir)
	if err != nil {
		return fmt.Errorf("%w: could not read free space: %v", ErrNoSpace, err)
	}
	if !bounded {
		rep.Logf("this source reports no duration, so the free-space check is working " +
			"from its size; the encode is capped at that figure regardless")
	}
	// The floor has to survive the transcode, not merely precede it: checking
	// `free < floor` alone accepts a two-hour item onto a volume with exactly
	// the reserve free, writes until ENOSPC, and eats the reserve the database
	// and the recorder depend on.
	needed := p.cfg.MinFreeBytes + uint64(estimate)
	if free < needed {
		return fmt.Errorf("%w: %d MiB free, about %d MiB needed",
			ErrNoSpace, free>>20, needed>>20)
	}
	return nil
}

// estimateBytes bounds what the derivative will cost on disk.
//
// The profile caps its video bitrate and its audio is constant, so duration
// alone is a real upper bound. When no duration was supplied the source's own
// size is the only other number available, and it is a GUESS rather than a
// bound — a short, low-resolution source normalises LARGER than it arrived —
// so the caller says so in the job log rather than pretending the guard is as
// strong as it looks.
func estimateBytes(durationMS, sourceBytes int64) (n int64, bounded bool) {
	if durationMS > 0 {
		// DURATION HEADROOM, because this figure is now a HARD CAP on the
		// writer (buildNormalise's -fs) and not only a disk demand. A cap that
		// is merely accurate trips on the honest cases: the derivative is padded
		// to the LONGER of the source's two track durations plus PadSlackSecs,
		// and a container's own duration field is not always the longer of them
		// -- MPEG-TS estimates it, and a stream can outlast what the header
		// claims. A trip means an operator's legitimate item is refused rather
		// than played, so the margin is deliberately larger than the error it is
		// covering: ten percent, plus thirty seconds for short items where a
		// percentage is nothing.
		//
		// It costs ten percent of a disk demand and buys a safety valve that
		// only ever fires on something genuinely wrong. Both figures are far
		// inside the int64 multiply below at MaxDurationMS.
		durationMS += durationMS/10 + 30_000
		bits := int64(NormaliseVideoKbps+NormaliseAudioKbps) * durationMS
		// +5% for TS packet and PSI overhead, which is real at 188-byte packets.
		return bits / 8 * 21 / 20, true
	}
	if sourceBytes < 0 {
		sourceBytes = 0
	}
	return sourceBytes, false
}

// verifySource is the real SourceVerifier: internal/ffmpeg.ProbeFile, which is
// the SAME allowlist and the same -protocol_whitelist the upload gate uses.
//
// Deliberately the same function rather than a second copy of the rule. A
// re-implementation here would be a second place for the allowlist to drift
// from the one internal/ffmpeg documents and tests, and the whole argument for
// an allowlist is that it is a closed set somebody owns.
//
// THE CLASSIFICATION MIRRORS THE UPLOAD HANDLER'S, which is not a coincidence
// and is the point: a fact about the FILE is permanent, because no number of
// retries makes an ffconcat script into media; a fact about THIS SERVER --
// ffprobe missing, a fork that failed, output that is not JSON -- is retryable,
// because it is not a verdict about the operator's bytes and the next attempt
// may well be on a less busy machine. Getting that backwards is how a queue
// burns every attempt on a file that can never succeed, or gives up permanently
// on a file that is fine.
func (p *Processor) verifySource(ctx context.Context, path string) (float64, error) {
	res, err := ffmpeg.ProbeFile(ctx, p.cfg.FFprobe, path)
	if err != nil {
		name := filepath.Base(path)
		switch {
		case errors.Is(err, ffmpeg.ErrIndirectContainer):
			return 0, jobs.Permanent(fmt.Errorf(
				"%s is a playlist or script naming other files, not media itself, "+
					"so it cannot be a playlist item; remove it and upload the file it names", name))
		case errors.Is(err, ffmpeg.ErrUnsupportedContainer):
			return 0, jobs.Permanent(fmt.Errorf(
				"polyemesis cannot use %s; re-save it as MP4 or MPEG-TS (%v)", name, err))
		}
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return 0, jobs.Permanent(fmt.Errorf("ffprobe could not read %s: %v", name, err))
		}
		return 0, fmt.Errorf("could not inspect %s: %w", name, err)
	}
	if res.Video == nil && len(res.Audio) == 0 {
		return 0, jobs.Permanent(fmt.Errorf(
			"%s carries no video or audio stream", filepath.Base(path)))
	}
	return res.DurationSeconds, nil
}

// probeStream is the real StreamProber: one ffprobe that prints the codec type
// of the first stream of a kind, or nothing at all when there is not one.
//
// A failure here is PERMANENT. Reaching this point means the file is on disk
// and readable, so the only remaining reasons FFprobe cannot answer are that
// the file is truncated, corrupt or not media at all — and none of those become
// untrue on the next attempt. FFprobe's own words are preserved, because "this
// upload is broken" is only actionable if the operator can see how.
func (p *Processor) probeStream(ctx context.Context, path, kind string) (bool, error) {
	out, stderr, err := output(ctx, media.Command{Name: p.cfg.FFprobe, Args: streamArgs(path, kind)})
	if err != nil {
		if s := strings.TrimSpace(stderr); s != "" {
			return false, jobs.Permanent(fmt.Errorf("ffprobe could not read %s: %s",
				filepath.Base(path), s))
		}
		return false, jobs.Permanent(fmt.Errorf("ffprobe could not read %s: %w",
			filepath.Base(path), err))
	}
	return strings.TrimSpace(string(out)) != "", nil
}

// streamArgs asks only the question that changes the argv, for one stream kind.
// A full probe would tempt a later reader into deriving the profile from the
// source, which is the one thing this package must not do.
func streamArgs(path, kind string) []string {
	return []string{
		"-hide_banner", "-v", "error",
		"-select_streams", kind + ":0",
		"-show_entries", "stream=codec_type",
		"-of", "csv=p=0",
		path,
	}
}

// probeDuration is the real DurationProber: one ffprobe reading a source's own
// video and audio track durations, which chooseProfile uses to compute the
// pad filters described at buildNormalise.
//
// PERMANENT on failure, for the same reason probeStream is: chooseProfile has
// already confirmed this file's video and audio tracks answer FFprobe, so a
// failure here means the same unreadable file, not a transient one.
func (p *Processor) probeDuration(ctx context.Context, path string) (videoSecs, audioSecs float64, err error) {
	videoSecs, audioSecs, err = probeStreamDurationsSecs(ctx, p.cfg.FFprobe, path)
	if err != nil {
		return 0, 0, jobs.Permanent(err)
	}
	return videoSecs, audioSecs, nil
}

// probeStreamDurationsSecs reads a source's own video and audio track
// durations in one ffprobe call.
//
// Per-stream, unlike probeFormatDurationSecs below (which ProbeOutputDurationMS
// uses on the finished OUTPUT): buildNormalise has to know WHICH of the two
// tracks is shorter, and by how much, to compute its pad filters, and a single
// combined container duration cannot answer either question. The finished
// output has no such question left to answer -- it is already one profile,
// one duration -- which is why that path reads the simpler, container-level
// field instead.
func probeStreamDurationsSecs(ctx context.Context, ffprobe, path string) (videoSecs, audioSecs float64, err error) {
	out, stderr, err := output(ctx, media.Command{Name: ffprobe, Args: []string{
		"-hide_banner", "-v", "error",
		"-show_entries", "stream=codec_type,duration",
		"-of", "csv=p=0",
		path,
	}})
	if err != nil {
		if s := strings.TrimSpace(stderr); s != "" {
			return 0, 0, fmt.Errorf("ffprobe could not read the duration of %s: %s", filepath.Base(path), s)
		}
		return 0, 0, fmt.Errorf("ffprobe could not read the duration of %s: %w", filepath.Base(path), err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		kind, secsField, found := strings.Cut(line, ",")
		if !found {
			continue
		}
		secs, perr := strconv.ParseFloat(strings.TrimSpace(secsField), 64)
		if perr != nil {
			continue // e.g. "N/A" -- treated as unanswered, checked below.
		}
		switch kind {
		case "video":
			videoSecs = secs
		case "audio":
			audioSecs = secs
		}
	}
	if videoSecs <= 0 {
		return 0, 0, fmt.Errorf("%s has no readable video duration", filepath.Base(path))
	}
	if audioSecs <= 0 {
		return 0, 0, fmt.Errorf("%s has no readable audio duration", filepath.Base(path))
	}
	return videoSecs, audioSecs, nil
}

// ProbeOutputDurationMS reads the duration of a file this process just wrote.
//
// From the OUTPUT, never the source: the derivative has been re-encoded,
// padded and remuxed, and the source's duration describes a file that no
// longer plays. NormaliseParams.DurationMS is the source-side estimate and is
// not this.
//
// This is NOT needed for concat correctness -- Task 1 measured that a concat
// list's per-entry duration directive makes no difference to the packet
// stream FFmpeg actually plays, with or without it. What this number is for is
// NormaliseResult.DurationMS: Task 7's operator playlist editor showing each
// item's real, padded length, which is the difference between building a
// programme and guessing at one.
func ProbeOutputDurationMS(ctx context.Context, ffprobe, path string) (int64, error) {
	secs, err := probeFormatDurationSecs(ctx, ffprobe, path)
	if err != nil {
		return 0, err
	}
	return int64(secs*1000 + 0.5), nil // round rather than truncate
}

// probeFormatDurationSecs is ProbeOutputDurationMS's plumbing: one ffprobe
// reading a file's CONTAINER-level duration. See probeStreamDurationsSecs
// above for why the source-side probe reads a different, per-stream field
// instead.
func probeFormatDurationSecs(ctx context.Context, ffprobe, path string) (float64, error) {
	out, stderr, err := output(ctx, media.Command{Name: ffprobe, Args: []string{
		"-hide_banner", "-v", "error",
		"-show_entries", "format=duration",
		"-of", "default=nw=1:nk=1",
		path,
	}})
	if err != nil {
		if s := strings.TrimSpace(stderr); s != "" {
			return 0, fmt.Errorf("ffprobe could not read the duration of %s: %s", filepath.Base(path), s)
		}
		return 0, fmt.Errorf("ffprobe could not read the duration of %s: %w", filepath.Base(path), err)
	}
	secs, perr := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	if perr != nil || secs <= 0 {
		return 0, fmt.Errorf("%s has no readable duration", filepath.Base(path))
	}
	return secs, nil
}

// ------------------------------------------------------------------- plumbing

func decodeParams(job jobs.Job, into any) error {
	if len(job.Params) == 0 {
		return jobs.Permanent(fmt.Errorf("%s job %d has no parameters", job.Kind, job.ID))
	}
	if err := json.Unmarshal(job.Params, into); err != nil {
		// Params that will not parse will not parse on the next attempt either.
		return jobs.Permanent(fmt.Errorf("%s job %d has unreadable parameters: %w", job.Kind, job.ID, err))
	}
	return nil
}

// run executes the transcode, mapping FFmpeg's progress onto the job's bar.
func (p *Processor) run(ctx context.Context, rep jobs.Reporter, cmd media.Command, durationMS int64) error {
	sink := media.Sink{Line: func(l string) { rep.Logf("%s", l) }}
	if durationMS > 0 {
		sink.Progress = func(pr ffmpeg.Progress) {
			rep.Progress(float64(pr.OutTimeMS) / float64(durationMS))
		}
	}
	if err := p.exec(ctx, cmd, sink); err != nil {
		// Cancellation is not a failure worth retrying — the operator or the
		// governor asked for it — but it is not this package's call either, so
		// it goes back unwrapped and the queue decides.
		return err
	}
	rep.Progress(1)
	return nil
}

// output runs a command and returns its stdout, for ffprobe. Separate from
// media.Exec because a probe's answer IS its stdout, where a transcode's stdout
// is a progress stream nobody keeps.
func output(ctx context.Context, cmd media.Command) ([]byte, string, error) {
	c := exec.CommandContext(ctx, cmd.Name, cmd.Args...)
	var stderr strings.Builder
	c.Stderr = &stderr
	out, err := c.Output()
	return out, stderr.String(), err
}

// discard removes a partial output, best effort.
func (p *Processor) discard(path string) {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) && p.log != nil {
		p.log.Warn("could not remove a partial derivative", "path", path, "err", err)
	}
}

// publish renames a finished .partial into place. Nothing here writes to a
// final path directly, so a half-written derivative can never be read as a
// finished one. Same convention as media.publish and clips.Capture.
func publish(partial, final string) error {
	if err := os.Rename(partial, final); err != nil {
		_ = os.Remove(partial)
		return fmt.Errorf("publish %s: %w", filepath.Base(final), err)
	}
	return nil
}
