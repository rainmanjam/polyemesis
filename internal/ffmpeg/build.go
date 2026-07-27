package ffmpeg

import (
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// commonArgs are the flags every polyemesis child gets.
//
//   - -hide_banner   : the build banner is noise in the log tail
//   - -nostdin       : without it, a backgrounded ffmpeg can steal terminal
//     input and silently pause itself
//   - -loglevel      : warning keeps the log tail useful rather than a firehose
func commonArgs() []string {
	return []string{"-hide_banner", "-nostdin", "-loglevel", "warning"}
}

// progressArgs routes machine-readable stats to stdout, leaving stderr as a
// pure human log. -nostats suppresses the duplicate interactive line.
func progressArgs() []string {
	return []string{"-nostats", "-progress", "pipe:1"}
}

// --------------------------------------------------------------------- ingest

// IngestKind selects the listener protocol.
type IngestKind string

const (
	IngestSRT  IngestKind = "srt"
	IngestRTMP IngestKind = "rtmp"
	// IngestPull dials out to a source instead of waiting for an encoder to
	// arrive: an RTSP camera, another server's HLS or DASH, an MPEG-TS over
	// HTTP, or a local file looped as a test feed.
	IngestPull IngestKind = "pull"
)

// DefaultPullReconnectDelayMax is how far FFmpeg's HTTP reconnect backoff is
// allowed to grow, in seconds. Bounded so a source that comes back after a long
// outage is picked up in tens of seconds, not tens of minutes.
const DefaultPullReconnectDelayMax = 30

// DefaultPullRTSPTransport is TCP because RTSP-over-UDP through NAT is the
// classic silent failure: the RTSP handshake succeeds, the operator sees a
// connected camera, and not one RTP packet ever arrives.
const DefaultPullRTSPTransport = "tcp"

// pullFamily groups schemes by the input flags they need, not by protocol.
type pullFamily int

const (
	// pullDirect needs no extra flags: SRT and RTMP either reconnect
	// themselves or exit, and the supervisor respawns them.
	pullDirect pullFamily = iota
	pullHTTP
	pullRTSP
	pullFile
)

// pullSchemes is the allowlist. A pull URL is operator input that becomes an
// FFmpeg argument, and while os/exec means no shell ever sees it, the scheme
// still decides how far FFmpeg can reach: without a list, gopher:// and
// concat: are one settings write away.
//
// rtmps is here alongside rtmp because a destination may already use it and
// refusing to *read* a transport we happily write would be the restrictive kind
// of wrong. hls and dash are accepted as written even though FFmpeg usually
// wants the plain http URL — passing them through lets FFmpeg give the operator
// a real error rather than having polyemesis guess and refuse.
var pullSchemes = map[string]pullFamily{
	"http":  pullHTTP,
	"https": pullHTTP,
	"hls":   pullHTTP,
	"dash":  pullHTTP,
	"rtsp":  pullRTSP,
	"rtsps": pullRTSP,
	"srt":   pullDirect,
	"rtmp":  pullDirect,
	"rtmps": pullDirect,
	"file":  pullFile,
}

// PullSchemes lists the accepted pull source schemes, sorted, so an error
// message and a UI hint can both quote the same set.
func PullSchemes() []string {
	out := make([]string, 0, len(pullSchemes))
	for s := range pullSchemes {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// IngestSpec describes the listener that receives the encoder's stream and
// republishes it, untouched, into the relay.
type IngestSpec struct {
	Kind IngestKind

	// SRT
	SRTPort       int
	SRTPassphrase string
	SRTLatencyMS  int

	// RTMP
	RTMPPort      int
	RTMPApp       string
	RTMPStreamKey string

	// Pull
	//
	// PullURL is the source to dial. PullDataDir is the directory a relative
	// file:// source resolves against — the same confinement file destinations
	// get, because a file source pointing anywhere on disk is a read primitive
	// for whoever reaches the settings API.
	PullURL     string
	PullDataDir string
	// PullReconnectDelayMax is the HTTP reconnect backoff ceiling in seconds.
	// 0 means DefaultPullReconnectDelayMax.
	PullReconnectDelayMax int
	// PullRTSPTransport overrides DefaultPullRTSPTransport for the rare camera
	// that only speaks UDP.
	PullRTSPTransport string

	// RelayURL is the loopback UDP address of the relay hub.
	RelayURL string
}

// PullSource validates the configured pull URL and renders the exact string
// handed to FFmpeg's -i.
func (s IngestSpec) PullSource() (string, error) {
	src, _, err := pullSource(s.PullURL, s.PullDataDir)
	return src, err
}

// ValidatePullURL reports whether raw is a pull source polyemesis will dial.
// The settings layer calls this so the operator sees the problem in a form
// field instead of in an FFmpeg stderr line.
func ValidatePullURL(raw string) error {
	_, _, err := pullSource(raw, "")
	return err
}

// pullSource validates raw and returns the -i string plus its flag family.
//
// baseDir is where a relative file:// path is rooted; an empty baseDir leaves
// the path relative rather than refusing, so validation-only callers (which
// have no data directory to hand) still get a yes/no answer.
func pullSource(raw, baseDir string) (string, pullFamily, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", pullDirect, errors.New("pull source URL is required")
	}
	if strings.ContainsAny(raw, "\x00\n\r") {
		return "", pullDirect, errors.New("pull source URL contains control characters")
	}
	scheme, rest, ok := strings.Cut(raw, "://")
	if !ok {
		return "", pullDirect, fmt.Errorf("pull source URL must start with a scheme (one of %s)",
			strings.Join(PullSchemes(), ", "))
	}
	fam, ok := pullSchemes[strings.ToLower(scheme)]
	if !ok {
		return "", pullDirect, fmt.Errorf("unsupported pull source scheme %q (want one of %s)",
			scheme, strings.Join(PullSchemes(), ", "))
	}
	if fam != pullFile {
		// Split on "://" rather than url.Parse because file:// paths are not
		// URLs in any useful sense; everything else still gets parsed so a
		// mangled URL is reported here rather than by the child process.
		if _, err := url.Parse(raw); err != nil {
			return "", fam, fmt.Errorf("malformed pull source URL: %w", err)
		}
		return raw, fam, nil
	}

	// Backslashes are separators on Windows, so normalise before the traversal
	// check or "..\..\secret.key" walks straight past it.
	rel := strings.ReplaceAll(rest, `\`, "/")
	switch {
	case rel == "":
		return "", fam, errors.New("file pull source needs a path")
	case strings.HasPrefix(rel, "/"), strings.Contains(rel, ".."),
		filepath.IsAbs(rest), len(rel) > 1 && rel[1] == ':':
		return "", fam, errors.New("file pull source must be a relative path inside the data directory")
	}

	p := filepath.FromSlash(rel)
	if baseDir != "" {
		p = filepath.Join(baseDir, p)
	}
	// The "file:" prefix is not decoration: a bare path containing a colon
	// ("data/2026:01.ts") is re-read by FFmpeg as a protocol name, and the
	// prefix pins it to the file protocol whatever the name looks like.
	return "file:" + p, fam, nil
}

// pullInputArgs are the input-side flags a pull source needs to survive its
// first hiccup. They must precede -i or FFmpeg applies them to nothing.
func (s IngestSpec) pullInputArgs() []string {
	_, fam, err := pullSource(s.PullURL, s.PullDataDir)
	if err != nil {
		return nil
	}
	switch fam {
	case pullHTTP:
		delay := s.PullReconnectDelayMax
		if delay <= 0 {
			delay = DefaultPullReconnectDelayMax
		}
		return []string{
			"-reconnect", "1",
			// -reconnect alone only retries seekable inputs. A live HTTP-TS or
			// HLS source is not seekable, so without -reconnect_streamed the
			// first dropped connection ends the ingest for good.
			"-reconnect_streamed", "1",
			"-reconnect_delay_max", strconv.Itoa(delay),
		}
	case pullRTSP:
		transport := s.PullRTSPTransport
		if transport == "" {
			transport = DefaultPullRTSPTransport
		}
		return []string{"-rtsp_transport", transport}
	case pullFile:
		// -stream_loop -1 makes the file look like a feed that never ends, and
		// -re paces it at wall-clock speed. Without -re FFmpeg reads at disk
		// speed and buries the relay in an hour of stream in seconds.
		return []string{"-stream_loop", "-1", "-re"}
	}
	return nil
}

// IngestURL renders the listener URL. Exported so the UI can show the user the
// exact address to paste into OBS.
func (s IngestSpec) IngestURL() string {
	switch s.Kind {
	case IngestPull:
		// A rejected source renders empty rather than raw. For file:// the raw
		// string is the very thing being rejected, and handing it to FFmpeg
		// anyway would make the confinement check decorative.
		src, _ := s.PullSource()
		return src
	case IngestRTMP:
		u := fmt.Sprintf("rtmp://0.0.0.0:%d/%s", s.RTMPPort, strings.Trim(s.RTMPApp, "/"))
		if s.RTMPStreamKey != "" {
			u += "/" + s.RTMPStreamKey
		}
		return u
	default:
		q := url.Values{}
		q.Set("mode", "listener")
		// SRT's transtype=live picks the low-latency congestion control and
		// the live packet-drop policy, which is what a stream wants; the
		// default (file) would buffer instead of dropping and drift forever.
		q.Set("transtype", "live")
		// FFmpeg's srt latency option is in MICROseconds. Passing 200 here
		// instead of 200000 yields a 0.2 ms buffer and a stream that falls
		// apart on the first jitter.
		q.Set("latency", strconv.Itoa(s.SRTLatencyMS*1000))
		if s.SRTPassphrase != "" {
			q.Set("passphrase", s.SRTPassphrase)
		}
		return fmt.Sprintf("srt://0.0.0.0:%d?%s", s.SRTPort, q.Encode())
	}
}

// PublicIngestURL renders the URL a streamer points their encoder at.
//
// In pull mode nobody points anything anywhere, so it reports the source
// polyemesis dials instead — that is the address the operator needs to see, and
// returning nothing would leave the dashboard blank.
func (s IngestSpec) PublicIngestURL(host string) string {
	switch s.Kind {
	case IngestPull:
		return strings.TrimSpace(s.PullURL)
	case IngestRTMP:
		return fmt.Sprintf("rtmp://%s:%d/%s", host, s.RTMPPort, strings.Trim(s.RTMPApp, "/"))
	default:
		q := url.Values{}
		q.Set("mode", "caller")
		q.Set("transtype", "live")
		q.Set("latency", strconv.Itoa(s.SRTLatencyMS*1000))
		if s.SRTPassphrase != "" {
			q.Set("passphrase", s.SRTPassphrase)
		}
		return fmt.Sprintf("srt://%s:%d?%s", host, s.SRTPort, q.Encode())
	}
}

// IngestArgs builds the listener command.
//
// The whole job is: accept, do not decode, republish. `-map 0 -c copy` keeps
// every track and every byte, so the ingest costs almost no CPU regardless of
// resolution, and no destination can ever be blamed on a lossy first hop.
//
// Pull mode is the same command with a different input: dialling out instead of
// listening changes where the bytes come from, never what happens to them.
func IngestArgs(s IngestSpec) []string {
	args := commonArgs()
	args = append(args, progressArgs()...)

	switch s.Kind {
	case IngestRTMP:
		// The rtmp protocol's own listen option. FFmpeg accepts one publisher,
		// then exits; the supervisor respawns it, which is exactly the
		// "wait for the next session" behaviour we want.
		args = append(args, "-listen", "1")
	case IngestPull:
		args = append(args, s.pullInputArgs()...)
	}

	args = append(args,
		// The encoder's timestamps can start anywhere; genpts gives the relay
		// a monotonic base without touching the payload.
		"-fflags", "+genpts",
		"-i", s.IngestURL(),
		"-map", "0",
		"-c", "copy",
		"-f", "mpegts",
		// Without flush_packets the muxer holds partial TS packets, adding
		// unnecessary latency to a loopback hop that has none to spare.
		"-flush_packets", "1",
		RelayOutputURL(s.RelayURL),
	)
	return args
}

// RelayOutputURL adds the datagram sizing the relay hub expects. 1316 bytes is
// 7 x 188-byte TS packets, the largest whole number that fits a 1500-byte MTU.
func RelayOutputURL(base string) string {
	if base == "" {
		return base
	}
	if strings.Contains(base, "?") {
		return base + "&pkt_size=1316"
	}
	return base + "?pkt_size=1316"
}

// RelayInputURL adds the receive-side buffering every consumer needs.
//
// fifo_size is in 188-byte packets, so 5000 is ~940 KB of slack. overrun_nonfatal
// turns a momentary overflow into a logged glitch instead of a dead process —
// which matters because one slow consumer must never take a destination down.
func RelayInputURL(base string) string {
	if base == "" {
		return base
	}
	sep := "?"
	if strings.Contains(base, "?") {
		sep = "&"
	}
	return base + sep + "fifo_size=5000&overrun_nonfatal=1"
}

// ---------------------------------------------------------------- destination

// DestKind is the output transport.
type DestKind string

const (
	DestRTMP DestKind = "rtmp"
	DestSRT  DestKind = "srt"
	DestFile DestKind = "file"
	// DestAudio carries no video at all: an Icecast mount for a radio or
	// podcast feed, or an audio file. It is the routing graph with nothing
	// else attached, which makes it the purest expression of the per-
	// destination mix rather than a special case bolted onto a video output.
	DestAudio DestKind = "audio"
)

// audioCodec is the muxer/encoder pair one audio-only container needs.
type audioCodec struct {
	muxer   string
	encoder string
	// contentType is the Icecast Content-Type header. FFmpeg's icecast
	// protocol only defaults it to audio/mpeg, and a listener handed the wrong
	// one gets a download prompt instead of a stream.
	contentType string
	// lossless marks the containers where -b:a means nothing. Passing it
	// anyway is not fatal but it puts a warning in the log tail for every
	// single destination, which is how a log tail stops being read.
	lossless bool
}

// audioFormats maps an output extension to the codec that fills it.
//
// .ogg is Opus, not Vorbis, on purpose. libvorbis is an optional FFmpeg build
// flag and is missing from builds that carry libopus — Homebrew's is one — so
// selecting it would be this repo's recurring mistake in a new costume: a
// destination that refuses to start on a machine that is perfectly capable of
// running it. Opus in Ogg is what every Icecast client of the last decade
// plays, and it is what is actually present.
var audioFormats = map[string]audioCodec{
	".mp3":  {muxer: "mp3", encoder: "libmp3lame", contentType: "audio/mpeg"},
	".aac":  {muxer: "adts", encoder: "aac", contentType: "audio/aac"},
	".m4a":  {muxer: "ipod", encoder: "aac", contentType: "audio/mp4"},
	".ogg":  {muxer: "ogg", encoder: "libopus", contentType: "audio/ogg"},
	".opus": {muxer: "ogg", encoder: "libopus", contentType: "audio/ogg"},
	".flac": {muxer: "flac", encoder: "flac", contentType: "audio/flac", lossless: true},
	".wav":  {muxer: "wav", encoder: "pcm_s16le", contentType: "audio/wav", lossless: true},
}

// defaultAudioFormat is what a target with no recognisable extension gets. MP3,
// because an Icecast mount is conventionally written bare ("/live") and MP3 is
// the one audio codec every listener on the internet can already play.
var defaultAudioFormat = audioFormats[".mp3"]

// IcecastScheme is the URL prefix that marks an audio-only destination as a
// live Icecast mount rather than a file. Credentials and mount point ride in
// the URL itself: icecast://source:hackme@radio.example:8000/live.mp3
const IcecastScheme = "icecast://"

// isIcecast reports whether target is an Icecast mount.
func isIcecast(target string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(target)), IcecastScheme)
}

// audioCodecFor picks the container for an audio-only destination from its
// target: the file extension, or the extension of the Icecast mount point.
func audioCodecFor(target string) audioCodec {
	p := strings.TrimSpace(target)
	// For a URL the extension lives on the path. Parsing rather than taking
	// filepath.Ext of the whole string keeps a query string ("?x=y.wav") and a
	// password containing a dot from deciding the codec.
	if strings.Contains(p, "://") {
		if u, err := url.Parse(p); err == nil {
			p = u.Path
		}
	}
	if f, ok := audioFormats[strings.ToLower(filepath.Ext(p))]; ok {
		return f
	}
	return defaultAudioFormat
}

// DestSpec describes one outbound stream.
type DestSpec struct {
	Kind DestKind
	// Target is the full publish URL, or the absolute output path for files.
	Target string
	// RelayURL is this destination's private relay subscription.
	RelayURL string
	// FilterComplex is the compiled routing graph from internal/routing.
	FilterComplex string
	// AudioOutLabel is the graph's output label, normally "aout".
	AudioOutLabel string
	AudioBitrate  int // kbps
	SampleRate    int
	// CopyVideo is always true in v1 and is here to make the guarantee
	// explicit and testable rather than implicit in the arg list.
	CopyVideo bool
	// VideoDelayMS holds the picture back to satisfy a NEGATIVE routing delay,
	// i.e. audio that needs to arrive early. It is routing.Result.VideoDelayMS
	// passed straight through; the audio side of a negative delay is not
	// expressible as a filter, because audio cannot be moved earlier than it
	// arrives. Zero for every profile that does not use it, which is nearly all
	// of them, and zero produces byte-for-byte the command as before.
	VideoDelayMS int
	// ExtraInputArgs and ExtraOutputArgs are expert mode: arguments an operator
	// hand-wrote for this destination, already parsed and already checked by
	// the API. Empty for every destination that has not opted in, which is the
	// overwhelming majority.
	//
	// They are spliced by SpliceExtraArgs rather than appended, because FFmpeg
	// binds an option to the input or output that FOLLOWS it — appended to the
	// end they would attach to nothing and silently do nothing.
	ExtraInputArgs  []string
	ExtraOutputArgs []string
}

// SpliceExtraArgs inserts hand-written arguments into a generated FFmpeg
// command at the two positions FFmpeg reads them from: input options
// immediately before -i, output options immediately before the target.
//
// Exported because the expert-mode preview has to show the operator the exact
// command that will run, and for a LIVE destination it must splice into the
// argv the running process was started with — that one carries the relay port
// the hub actually assigned, which nothing outside the engine can reproduce.
// One function, so the command that was confirmed and the command that runs
// cannot drift apart.
//
// A base command that does not look the way this expects — no -i, or nothing
// after it — gets the arguments appended rather than an error. Refusing to
// build anything because the shape surprised us would be worse than producing
// a command the operator can read and judge.
func SpliceExtraArgs(base, in, out []string) []string {
	if len(in) == 0 && len(out) == 0 {
		return base
	}
	argv := make([]string, 0, len(base)+len(in)+len(out))

	inputAt := -1
	for i, a := range base {
		if a == "-i" {
			inputAt = i
			break
		}
	}
	if inputAt < 0 || len(base) == 0 {
		argv = append(argv, base...)
		argv = append(argv, in...)
		return append(argv, out...)
	}

	argv = append(argv, base[:inputAt]...)
	argv = append(argv, in...)
	// Everything from -i up to but not including the output target, which is
	// the final element of every command DestinationArgs builds.
	argv = append(argv, base[inputAt:len(base)-1]...)
	argv = append(argv, out...)
	return append(argv, base[len(base)-1])
}

// StripExtraArgs is the exact inverse of SpliceExtraArgs: given a command that
// was built with in and out spliced into it, it returns the generated command
// underneath.
//
// It exists so the expert-mode editor can preview a CANDIDATE edit against a
// destination that is already running with a previous one. The live process's
// argv is the only place the relay port the hub actually assigned appears, so
// it has to be the base — but splicing the candidate onto it directly would
// stack the new arguments on top of the old.
//
// The tokens are removed only where they match exactly. Anything else — a
// command that was not built by SpliceExtraArgs, arguments that were changed on
// disk since the process started — is returned untouched, so the operator is
// shown the real argv rather than a guess at what it should have been.
func StripExtraArgs(argv, in, out []string) []string {
	if len(in) == 0 && len(out) == 0 {
		return argv
	}
	out2 := argv

	if n := len(out); n > 0 && len(out2) >= n+1 {
		// Immediately before the target, which is the final element.
		at := len(out2) - 1 - n
		if slices.Equal(out2[at:len(out2)-1], out) {
			out2 = append(append([]string{}, out2[:at]...), out2[len(out2)-1])
		}
	}
	if n := len(in); n > 0 {
		inputAt := slices.Index(out2, "-i")
		if inputAt >= n && slices.Equal(out2[inputAt-n:inputAt], in) {
			out2 = append(append([]string{}, out2[:inputAt-n]...), out2[inputAt:]...)
		}
	}
	return out2
}

// DestinationArgs builds one destination's command.
//
// The central promise of polyemesis lives in two lines here: video is
// `-c:v copy` (never re-encoded, never degraded, near-zero CPU) while audio is
// decoded, re-mixed through the routing graph, and re-encoded to the single
// stereo track the platform will accept.
//
// DestAudio is the same command with the video half deleted rather than a
// second code path: same relay, same filter graph, same explicit maps. What it
// drops is the video map and the video codec flag, and what it gains is a
// container chosen from the target instead of from the transport.
func DestinationArgs(s DestSpec) []string {
	if s.AudioBitrate == 0 {
		s.AudioBitrate = 160
	}
	if s.SampleRate == 0 {
		s.SampleRate = 48000
	}
	if s.AudioOutLabel == "" {
		s.AudioOutLabel = "aout"
	}

	args := commonArgs()
	args = append(args, progressArgs()...)
	args = append(args,
		"-fflags", "+genpts",
		// The relay is a live source; a large input queue absorbs scheduler
		// jitter that would otherwise show up as dropped frames.
		"-thread_queue_size", "1024",
	)
	args = append(args, videoDelayArgs(s)...)
	args = append(args,
		"-i", RelayInputURL(s.RelayURL),
		"-filter_complex", s.FilterComplex,
	)

	// Explicit maps only. Without them FFmpeg's default stream selection would
	// pick one arbitrary audio track and quietly ignore the routing graph
	// entirely — the single most damaging possible bug in this app.
	if s.Kind != DestAudio {
		args = append(args, "-map", "0:v:0", "-c:v", "copy")
	}
	args = append(args, "-map", "["+s.AudioOutLabel+"]")

	if s.Kind == DestAudio {
		return SpliceExtraArgs(append(args, audioOutputArgs(s)...),
			s.ExtraInputArgs, s.ExtraOutputArgs)
	}

	args = append(args,
		"-c:a", "aac",
		"-b:a", strconv.Itoa(s.AudioBitrate)+"k",
		"-ac", "2",
		"-ar", strconv.Itoa(s.SampleRate),
	)
	switch s.Kind {
	case DestRTMP:
		args = append(args, "-f", "flv")
	case DestSRT:
		args = append(args, "-f", "mpegts")
	case DestFile:
		args = append(args, "-f", fileFormat(s.Target))
	}
	args = append(args, s.Target)
	return SpliceExtraArgs(args, s.ExtraInputArgs, s.ExtraOutputArgs)
}

// videoDelayArgs renders a negative routing delay — audio early — as the input
// offset that holds the picture back. Empty for every other profile.
//
// -itsoffset shifts EVERY stream of the input it precedes, so on its own it
// would move audio and video together and achieve exactly nothing. What makes
// it work here is the other end of the pipeline: every graph Compile emits ends
// in `aresample=...:first_pts=0`, which pins the mixed audio to zero however
// the input was shifted. Video keeps the offset, audio does not, and the
// difference is the delay. Verified against FFmpeg 8.1.2: with first_pts=0 the
// video start moved by exactly the offset and audio did not move at all;
// against a graph without it, both moved and the separation stayed at zero.
//
// -itsoffset and -c:v copy get along, which is the combination that matters
// here — the offset is applied by the demuxer, so there is no encoder in the
// video path to renormalise it, and the measured shift was the requested one to
// the microsecond. A re-encode also keeps the offset but snaps it to the frame
// grid (0.500 s came out as 0.480 at 25 fps), so copy is both the cheaper and
// the more accurate path. Do not "fix" this by moving the offset onto the video
// filter chain: there is no video filter chain, by design.
//
// Audio-only destinations get nothing. There is no picture to hold back, and
// offsetting the sole input would only shift timestamps the graph then pins.
func videoDelayArgs(s DestSpec) []string {
	if s.Kind == DestAudio || s.VideoDelayMS <= 0 {
		return nil
	}
	// FFmpeg wants seconds. Millisecond precision, fixed notation, because
	// %g would render 120 ms as "0.12" and 1 ms as "0.001" inconsistently.
	return []string{"-itsoffset", strconv.FormatFloat(float64(s.VideoDelayMS)/1000, 'f', 3, 64)}
}

// audioOutputArgs renders the codec, container and (for Icecast) the headers an
// audio-only destination needs, ending with the target.
func audioOutputArgs(s DestSpec) []string {
	f := audioCodecFor(s.Target)

	args := []string{"-c:a", f.encoder}
	if !f.lossless {
		args = append(args, "-b:a", strconv.Itoa(s.AudioBitrate)+"k")
	}
	// Still stereo, still at the profile's rate: an audio-only destination is a
	// destination, and the promise is one summed stereo mix per destination.
	args = append(args, "-ac", "2", "-ar", strconv.Itoa(s.SampleRate))

	if isIcecast(s.Target) {
		// FFmpeg only assumes audio/mpeg. Anything else has to be declared or
		// listeners get a file download where they expected a stream.
		args = append(args, "-content_type", f.contentType)
	}
	return append(args, "-f", f.muxer, s.Target)
}

// fileFormat picks a muxer from the extension. Matroska is the default because
// it survives an unclean exit: an interrupted MP4 has no moov atom and is
// unplayable, whereas an interrupted MKV plays right up to the cut.
func fileFormat(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".mp4", ".m4v":
		return "mp4"
	case ".flv":
		return "flv"
	case ".ts":
		return "mpegts"
	case ".mov":
		return "mov"
	default:
		return "matroska"
	}
}

// ------------------------------------------------------------------- recorder

// RecorderSpec describes the full-fidelity multitrack archive.
type RecorderSpec struct {
	RelayURL       string
	OutputPattern  string // strftime pattern, absolute
	SegmentSeconds int
}

// RecorderArgs builds the recorder command.
//
// `-map 0 -c copy` is the whole point: every audio track the encoder sent is
// preserved bit-exact, so the archive keeps the full mix even though every
// live destination got a reduced stereo fold-down. Segmenting bounds the
// damage from a crash to one segment.
func RecorderArgs(s RecorderSpec) []string {
	if s.SegmentSeconds == 0 {
		s.SegmentSeconds = 3600
	}
	args := commonArgs()
	args = append(args, progressArgs()...)
	args = append(args,
		"-fflags", "+genpts",
		"-thread_queue_size", "1024",
		"-i", RelayInputURL(s.RelayURL),
		"-map", "0",
		"-c", "copy",
		"-f", "segment",
		"-segment_time", strconv.Itoa(s.SegmentSeconds),
		"-segment_format", "matroska",
		// Each segment restarts at t=0, so any one file plays standalone.
		"-reset_timestamps", "1",
		// Lets the output pattern carry the wall-clock time, which is what
		// makes the recordings list readable without consulting the database.
		"-strftime", "1",
		s.OutputPattern,
	)
	return args
}

// -------------------------------------------------------------------- preview

// PreviewSpec describes the dashboard's low-latency HLS preview.
type PreviewSpec struct {
	RelayURL       string
	OutputDir      string
	SegmentSeconds int
	Height         int
	VideoKbps      int
	// AudioTrack is which ingest track the preview monitors. Defaults to 0.
	AudioTrack int
}

// PreviewArgs builds the HLS preview command.
//
// This is the one place polyemesis re-encodes video, and it is deliberately
// isolated: it reads its own relay subscription, so if the preview encoder
// falls over or is disabled it cannot affect a single destination.
func PreviewArgs(s PreviewSpec) []string {
	if s.SegmentSeconds == 0 {
		s.SegmentSeconds = 2
	}
	if s.Height == 0 {
		s.Height = 360
	}
	if s.VideoKbps == 0 {
		s.VideoKbps = 800
	}
	gop := s.SegmentSeconds * 30

	args := commonArgs()
	args = append(args, progressArgs()...)
	args = append(args,
		"-fflags", "+genpts",
		"-thread_queue_size", "1024",
		"-i", RelayInputURL(s.RelayURL),
		"-map", "0:v:0",
		"-map", fmt.Sprintf("0:a:%d?", s.AudioTrack), // '?' => tolerate a video-only ingest
		"-c:v", "libx264",
		"-preset", "veryfast",
		"-tune", "zerolatency",
		"-b:v", strconv.Itoa(s.VideoKbps)+"k",
		"-maxrate", strconv.Itoa(s.VideoKbps)+"k",
		"-bufsize", strconv.Itoa(s.VideoKbps*2)+"k",
		"-vf", fmt.Sprintf("scale=-2:%d", s.Height),
		// Keyframe interval must divide the segment length exactly, or HLS
		// segments drift off the GOP boundary and players stall on each one.
		"-g", strconv.Itoa(gop),
		"-keyint_min", strconv.Itoa(gop),
		"-sc_threshold", "0",
		"-c:a", "aac",
		"-b:a", "96k",
		"-ac", "2",
		"-f", "hls",
		"-hls_time", strconv.Itoa(s.SegmentSeconds),
		"-hls_list_size", "6",
		"-hls_flags", "delete_segments+independent_segments+omit_endlist",
		"-hls_segment_type", "mpegts",
		"-hls_segment_filename", filepath.Join(s.OutputDir, "seg_%05d.ts"),
		filepath.Join(s.OutputDir, "index.m3u8"),
	)
	return args
}

// --------------------------------------------------------------------- meters

// MetersSpec describes the audio-level sidecar.
type MetersSpec struct {
	RelayURL string
	// TrackChannels[i] is the channel count of ingest track i. Determines the
	// merged channel numbering the parser relies on.
	TrackChannels []int
	SampleRate    int
}

// TotalChannels is the width of the merged metering stream.
func (s MetersSpec) TotalChannels() int {
	n := 0
	for _, c := range s.TrackChannels {
		n += c
	}
	return n
}

// ChannelOffset returns the merged-stream index of track t's channel 0.
// astats numbers channels from 1, so the metadata key for track t channel c is
// lavfi.astats.<ChannelOffset(t)+c+1>.<metric>.
func (s MetersSpec) ChannelOffset(track int) int {
	n := 0
	for i := 0; i < track && i < len(s.TrackChannels); i++ {
		n += s.TrackChannels[i]
	}
	return n
}

// MetersArgs builds the metering sidecar command.
//
// One process meters every channel of every track. The trick is amerge: rather
// than running N processes (and pulling N copies of the video over the relay
// just to reach the audio), every track is merged into one wide stream and a
// single astats reports per-channel levels, numbered predictably. A 6x stereo
// ingest becomes one 12-channel stream and one parseable stdout.
//
// Output goes to stdout via ametadata, so this is the one child that must not
// use -progress pipe:1.
func MetersArgs(s MetersSpec) []string {
	if s.SampleRate == 0 {
		s.SampleRate = 48000
	}
	n := len(s.TrackChannels)

	var chains []string
	var labels []string
	for i := 0; i < n; i++ {
		label := fmt.Sprintf("mt%d", i)
		// Two constraints, both load-bearing:
		//
		//  - aresample: amerge requires every leg at the same sample rate, and
		//    a mixed-rate ingest would otherwise fail to negotiate.
		//  - aformat: amerge cannot pick an output layout when its inputs have
		//    ambiguous ones. Merging three mono tracks produces three channels,
		//    which could be 3.0 or 2.1, and FFmpeg refuses with "The following
		//    filters could not choose their formats". Pinning each INPUT leg's
		//    layout resolves it; constraining amerge's output does not.
		chains = append(chains, fmt.Sprintf("[0:a:%d]aresample=%d,aformat=channel_layouts=%s[%s]",
			i, s.SampleRate, ChannelLayoutName(s.TrackChannels[i]), label))
		labels = append(labels, "["+label+"]")
	}

	merged := labels[0]
	if n > 1 {
		chains = append(chains, fmt.Sprintf("%samerge=inputs=%d[mgd]", strings.Join(labels, ""), n))
		merged = "[mgd]"
	}
	// measure_overall=none and a narrow measure_perchannel keep this to two
	// lines per channel per frame instead of twenty-four.
	chains = append(chains, fmt.Sprintf(
		"%sastats=metadata=1:reset=1:length=0.1:measure_perchannel=Peak_level+RMS_level:measure_overall=none,"+
			"ametadata=mode=print:file=-[mout]", merged))

	args := commonArgs()
	args = append(args,
		"-fflags", "+genpts",
		"-thread_queue_size", "512",
		"-i", RelayInputURL(s.RelayURL),
		"-filter_complex", strings.Join(chains, ";"),
		"-map", "[mout]",
		"-f", "null", "-",
	)
	return args
}

// --------------------------------------------------------------------- probe

// ProbeArgs builds the ffprobe command used to learn the ingest's track layout.
func ProbeArgs(input string, timeoutSeconds int) []string {
	return []string{
		"-hide_banner",
		"-loglevel", "error",
		"-print_format", "json",
		"-show_streams",
		"-show_format",
		// A live UDP relay never ends, so ffprobe must be told how long to
		// look before reporting what it has seen.
		"-analyzeduration", strconv.Itoa(timeoutSeconds * 1000000),
		"-probesize", "5000000",
		"-i", RelayInputURL(input),
	}
}
