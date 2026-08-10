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
	//
	// RTMPPort is the shared one-port RTMP listener, which polyemesis binds
	// itself; this spec never binds it. The ingest child is a SUBSCRIBER that
	// dials that listener back out on loopback, the same shape every RTMP server
	// uses, so the two addresses below are a dial target and a display string
	// rather than a bind.
	//
	// RTMPApp is the cosmetic first path element -- the "live" in
	// rtmp://host/live/<key>. The listener discards it (rtmpserver.StreamKey),
	// so it decides nothing; it exists because encoders expect a server URL that
	// looks like one, and because it is what goes in OBS's "Server" box.
	//
	// RTMPAddress is what actually selects the source: the publish token, or
	// "<token>.backup" for the failover standby. It replaced a per-source
	// RTMPStreamKey, which used to be a playpath FFmpeg checked while listening
	// -- a mechanism that no longer exists, and one that could not tell two
	// sources apart because nothing made stream keys unique.
	RTMPPort    int
	RTMPApp     string
	RTMPAddress string

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
		// 127.0.0.1, and it matters that it is not 0.0.0.0. This is a DIAL
		// target now: polyemesis binds the RTMP port itself and this child
		// subscribes to it. 0.0.0.0 was correct while `-listen 1` made FFmpeg
		// the server, and as a dial target it is the wrong address rather than
		// the same one spelled differently.
		//
		// Loopback also because rtmpserver refuses any non-loopback subscriber:
		// a stream key is a publish credential and must not double as a viewing
		// one.
		return fmt.Sprintf("rtmp://127.0.0.1:%d/%s/%s",
			s.RTMPPort, strings.Trim(s.RTMPApp, "/"), s.RTMPAddress)
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
		// The app only, deliberately: this is OBS's "Server" box and the address
		// goes in its "Stream Key" box beside it. Appending the address here
		// too would produce /live/<token>/<token> for the operator who fills in
		// both, which is the one mistake this split exists to make impossible.
		// api.publishURLs emits the key as its own field for that reason.
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
		// Nothing. RTMP used to pass `-listen 1` here, which made FFmpeg the
		// server and is exactly what capped an install at ONE RTMP source: a
		// single-connection receiver cannot demultiplex by path. The listener is
		// now polyemesis's own, on one port for every source, and this child
		// dials it as an ordinary client -- no more flags than SRT needs, and
		// for the same reason (see pullDirect): it either reconnects itself or
		// exits, and the supervisor respawns it.
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

	// Transport is the optional muxer and socket tuning. Its zero value emits
	// nothing at all, so a destination that has not opted in produces exactly
	// the argv it always did.
	Transport TransportSpec

	// Audio is the optional output-encoding choice. Its zero value is AAC
	// stereo, which is what every destination emitted before this existed.
	Audio AudioSpec

	// CopyAudio forwards the selected ingest tracks with `-c:a copy` instead of
	// decoding them through FilterComplex and re-encoding. False for every
	// destination that has not opted in, which produces byte-for-byte the
	// command it produced before this field existed.
	CopyAudio bool
	// AudioTracks are the 0-based ingest track indices to map when CopyAudio is
	// set, and are ignored otherwise. This is routing.Result.Tracks passed
	// straight through: the compiler has already applied the profile's
	// selection and removed the roles the destination excludes, so this list is
	// the answer to "which tracks does this destination carry", copy or not.
	AudioTracks []int
}

// Audio codec names, spelled the way FFmpeg spells them because these strings
// are written straight onto the command line.
const (
	AudioCodecAAC  = "aac"
	AudioCodecOpus = "libopus"
)

// AudioSpec is the per-destination audio encoding choice.
//
// Deliberately small. The roadmap asked for three things here and one of them
// does not exist -- see AACProfileUnavailable below.
type AudioSpec struct {
	// Codec is empty for AAC, which is the only thing every platform takes.
	// AudioCodecOpus is meaningfully better below ~64 kbps and is the reason
	// this field exists at all.
	Codec string
	// Mono folds the routing graph's stereo output down to one channel.
	//
	// A DOWNMIX of the operator's mix, not a re-route. The routing matrix still
	// produces OutL and OutR and this sums them; wiring individual tracks to a
	// single channel would be a change to the matrix and a different feature.
	// For talk content that is exactly what is wanted, and it halves the
	// bitrate for no perceptual loss.
	Mono bool
}

// AACProfileUnavailable records why there is no HE-AAC option here.
//
// The roadmap listed "AAC profile (LC / HE-AAC v1 / v2)" as a Part B item, on
// the grounds that HE-AAC is meaningfully better below 64 kbps. It is, and it
// is not reachable: FFmpeg's native `aac` encoder exposes NO -profile option at
// all, and `-profile:a aac_he` makes it refuse to open with
//
//	[aac] Profile not supported!
//
// producing no output whatever rather than falling back. HE-AAC needs
// libfdk_aac, which is nonfree and cannot ship in a redistributable build.
//
// The underlying GOAL -- good audio well below 64 kbps -- is met by Opus
// instead, which is free, in the pinned build, and better than HE-AAC at those
// rates. So the item is answered rather than abandoned, by a different means
// than the one that was proposed.
const AACProfileUnavailable = "FFmpeg's native AAC encoder supports only LC; HE-AAC needs the nonfree libfdk_aac"

// audioCodecArgs renders the codec and channel count for a video destination.
//
// Opus is refused on RTMP, and that refusal is the interesting part. FFmpeg
// will happily WRITE Opus into FLV -- it produced a valid 8.6 KB file when
// probed -- because Enhanced RTMP defines a mapping for it. No mainstream
// ingest accepts it. A stream that muxes cleanly, uploads cleanly and is
// rejected by the platform is the worst failure mode available: it looks
// correct everywhere the operator can see.
func audioCodecArgs(s DestSpec) []string {
	codec := AudioCodecAAC
	if s.Audio.Codec == AudioCodecOpus && s.Kind != DestRTMP {
		codec = AudioCodecOpus
	}
	channels := "2"
	if s.Audio.Mono {
		channels = "1"
	}
	return []string{
		"-c:a", codec,
		"-b:a", strconv.Itoa(s.AudioBitrate) + "k",
		"-ac", channels,
		"-ar", strconv.Itoa(s.SampleRate),
	}
}

// TransportSpec is the per-destination muxer and socket tuning.
//
// Every field is off by default and every one was probed against the pinned
// FFmpeg before it was designed around -- a third of the first draft of this
// block did not survive `ffmpeg -h`, which is why the probe results are written
// down here rather than remembered.
type TransportSpec struct {
	// NoDurationFilesize sets `-flvflags no_duration_filesize`.
	//
	// FLV carries duration and filesize in its metadata, and for a live stream
	// both are necessarily zero. Some RTMP ingests treat a zero duration as a
	// malformed file rather than as a live stream. Confirmed `E..........` on
	// the flv muxer, and applied ONLY to RTMP -- handing it to the mpegts muxer
	// would be an unknown option, which FFmpeg warns about on every start.
	NoDurationFilesize bool

	// MuxQueuePackets sets `-max_muxing_queue_size` and MuxQueueBytes sets
	// `-muxing_queue_data_threshold`. They are a PAIR, and that is the
	// correction the probe produced.
	//
	// The roadmap doc had these as alternatives -- max_muxing_queue_size for
	// stream init, muxing_queue_data_threshold for the steady state. FFmpeg's
	// own help says otherwise: the threshold is "the threshold after which
	// max_muxing_queue_size is taken into account". So the packet cap applies
	// only once the queue has grown past the byte threshold, and setting the
	// threshold alone does nothing whatsoever.
	//
	// Both matter here because polyemesis's audio path has variable latency --
	// loudnorm has lookahead -- so the interleave between a copied video stream
	// and a filtered audio one genuinely can diverge.
	MuxQueuePackets int
	MuxQueueBytes   int

	// RWTimeoutSeconds sets `-rw_timeout`, in microseconds on the wire.
	//
	// A half-open TCP connection -- the far end gone without a FIN, which is
	// what a platform behind a load balancer does -- otherwise blocks the muxer
	// indefinitely. FFmpeg keeps running, the supervisor sees a live process,
	// and the stream is off air with nothing reporting it. Confirmed
	// `ED.........`, so it is settable on an output and not only on an input,
	// and confirmed to parse on an `rtmp://` target.
	RWTimeoutSeconds int
}

// transportOutputArgs renders the muxer tuning that belongs immediately before
// the target. Empty for a destination that has not opted in.
func transportOutputArgs(s DestSpec) []string {
	var args []string
	t := s.Transport
	// FLV only. The flag does not exist on any other muxer, and passing an
	// option a muxer does not own makes FFmpeg warn on every single start --
	// noise that trains an operator to ignore the log.
	if t.NoDurationFilesize && s.Kind == DestRTMP {
		args = append(args, "-flvflags", "no_duration_filesize")
	}
	if t.MuxQueuePackets > 0 {
		args = append(args, "-max_muxing_queue_size", strconv.Itoa(t.MuxQueuePackets))
	}
	if t.MuxQueueBytes > 0 {
		args = append(args, "-muxing_queue_data_threshold", strconv.Itoa(t.MuxQueueBytes))
	}
	if t.RWTimeoutSeconds > 0 {
		// Microseconds on the wire; seconds in the settings, because nobody
		// reasons about a socket timeout in microseconds and a stray factor of
		// a thousand is a timeout that never fires.
		args = append(args, "-rw_timeout", strconv.Itoa(t.RWTimeoutSeconds*1_000_000))
	}
	return args
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
// CopyAudio is the one destination that opts out of the audio half of that, for
// the outputs where the platform is not the constraint: an SRT contribution feed
// or a local file can take the ingest's own tracks untouched. See copyAudioArgs,
// which is a separate function rather than a set of conditionals threaded
// through this one, because it shares only the input arguments -- everything
// after them differs.
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
	args = append(args, "-i", RelayInputURL(s.RelayURL))

	// The copy path branches BEFORE -filter_complex, and it has to. A graph
	// that is compiled and never mapped is not ignored: FFmpeg 8.1.2 answers
	// it with
	//
	//	Filter 'anull:default' has output 0 (aout) unconnected
	//	Error binding filtergraph inputs/outputs: Invalid argument
	//
	// and exits 234 having written nothing. Leaving the graph in as a harmless
	// leftover would mean a copy destination that never starts.
	if s.CopyAudio && s.Kind != DestAudio {
		return copyAudioArgs(s, args)
	}

	args = append(args, "-filter_complex", s.FilterComplex)

	// Explicit maps only. Without them FFmpeg's default stream selection would
	// pick one arbitrary audio track and quietly ignore the routing graph
	// entirely — the single most damaging possible bug in this app.
	if s.Kind != DestAudio {
		args = append(args, "-map", "0:v:0", "-c:v", "copy")
		// After -c:v copy, because it is a filter on the copied bitstream.
		args = append(args, videoDelayArgs(s)...)
	}
	args = append(args, "-map", "["+s.AudioOutLabel+"]")

	if s.Kind == DestAudio {
		return SpliceExtraArgs(append(args, audioOutputArgs(s)...),
			s.ExtraInputArgs, s.ExtraOutputArgs)
	}

	args = append(args, audioCodecArgs(s)...)
	args = append(args, transportOutputArgs(s)...)
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

// copyAudioArgs finishes the command for a destination that forwards its audio
// bit-for-bit: no filter graph, no decoder, no encoder, just maps and copies.
//
// IT SELECTS, IT DOES NOT FORWARD EVERYTHING. `-map 0 -c copy` is the shorter
// spelling and it is the wrong one. It would take every track the ingest
// carries, which destroys the two things a destination is FOR: the profile's
// track selection, and ExcludeRoles -- the DMCA switch that marks the licensed
// music track and keeps it out of the archive. A copy destination that silently
// re-admitted the music track would be a compliance failure that looks like a
// working feature, so the maps stay explicit and come from the compiled result.
//
// The maps carry no '?' suffix. An optional map would turn "the track the
// operator selected is not on this ingest" into silence; routing.Compile has
// already dropped every track that is not present in the measured layout, so a
// track named here and missing at runtime means the layout changed under us and
// is worth failing loudly over.
//
// audioCodecArgs is omitted rather than adapted. -b:a, -ac and -ar are encoder
// options and there is no encoder; FFmpeg warns about each of them next to a
// copy, and -ac in particular reads as an instruction the output will not obey.
func copyAudioArgs(s DestSpec, args []string) []string {
	args = append(args, "-map", "0:v:0", "-c:v", "copy")
	// Kept on this path for the same reason it exists on the other: the field
	// means "hold the picture back by this much" and must not mean two
	// different things depending on how the audio travels. Unreachable today,
	// because a destination that copies its audio is refused at save time if it
	// carries any delay at all.
	args = append(args, videoDelayArgs(s)...)

	for _, t := range s.AudioTracks {
		args = append(args, "-map", "0:a:"+strconv.Itoa(t))
	}
	args = append(args, "-c:a", "copy")

	args = append(args, transportOutputArgs(s)...)
	// Every kind that reaches here names its container, RTMP included. Copy is
	// refused on an RTMP destination at save time -- see AudioEncoding.copyProblems
	// -- so this case should be unreachable, and it is spelled out anyway rather
	// than left to fall through to no -f at all. A builder that silently drops
	// the muxer for one kind is how a validation gap turns into an unreadable
	// FFmpeg error instead of a refused save.
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

// videoDelayArgs renders a negative routing delay — audio early — as a
// timestamp shift on the copied video stream. Empty for every other profile.
//
// This was -itsoffset first, and -itsoffset does not work here. It shifts EVERY
// stream of the input it precedes, so audio and video move together and the
// separation between them stays at exactly zero. The idea that
// `aresample=...:first_pts=0` at the end of the graph would pin the audio back
// and leave only the video moved is wrong: measured against FFmpeg 8.1.2 the
// two moved in lockstep and the delivered offset was 0 ms for every requested
// value. A negative delay was emitting a flag and changing nothing.
//
// setts shifts the video alone, and it is a BITSTREAM filter, so it does it
// without decoding — `-c:v copy` survives, which is the whole promise of a
// destination. It needs FFmpeg 5.0; the startup check already demands 6.0.
//
// pts and dts are set SEPARATELY and deliberately. setts also accepts a single
// `ts=` that writes both from one expression, which silently collapses them
// into each other — with B-frames, where DTS legitimately runs ahead of PTS,
// that measured -232 ms for a requested -300 ms, while the separate form
// measured -298 ms. A stream with no B-frames cannot tell the two apart, so
// this is exactly the kind of bug that survives a test on synthetic footage and
// appears on a real encoder.
//
// Audio-only destinations get nothing: there is no picture to hold back.
func videoDelayArgs(s DestSpec) []string {
	if s.Kind == DestAudio || s.VideoDelayMS <= 0 {
		return nil
	}
	// Seconds, divided by the stream timebase to reach ticks. Millisecond
	// precision in fixed notation, because %g would render 120 ms as "0.12" and
	// 1 ms as "0.001" inconsistently.
	d := strconv.FormatFloat(float64(s.VideoDelayMS)/1000, 'f', 3, 64)
	return []string{"-bsf:v", "setts=pts=PTS+" + d + "/TB:dts=DTS+" + d + "/TB"}
}

// audioOutputArgs renders the codec, container and (for Icecast) the headers an
// audio-only destination needs, ending with the target.
func audioOutputArgs(s DestSpec) []string {
	f := audioCodecFor(s.Target)

	args := []string{"-c:a", f.encoder}
	if !f.lossless {
		args = append(args, "-b:a", strconv.Itoa(s.AudioBitrate)+"k")
	}
	// One summed mix per destination, at the profile's rate. Stereo unless the
	// operator asked for mono -- the codec here is still chosen by the target's
	// extension, because an Icecast mount's format is part of its URL.
	channels := "2"
	if s.Audio.Mono {
		channels = "1"
	}
	args = append(args, "-ac", channels, "-ar", strconv.Itoa(s.SampleRate))

	if isIcecast(s.Target) {
		// FFmpeg only assumes audio/mpeg. Anything else has to be declared or
		// listeners get a file download where they expected a stream.
		args = append(args, "-content_type", f.contentType)
	}
	// Transport tuning applies to an audio-only destination too: an Icecast
	// mount behind a dead proxy hangs exactly the way an RTMP one does.
	args = append(args, transportOutputArgs(s)...)
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
	// The live window has to stay wide enough that a player sitting at the far
	// edge of its allowed latency can still fetch the segment it is asking for.
	// hls.js will seek when it falls liveMaxLatencyDurationCount (6) target
	// durations behind, so a window of listSize x SegmentSeconds against a
	// threshold of 6 x SegmentSeconds is exactly equal at every segment length
	// -- the ratio is scale-invariant, and a constant 6 gives no margin at all.
	// Derived so shorter segments buy some.
	listSize := max(6, (8+s.SegmentSeconds-1)/s.SegmentSeconds)

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
		//
		// Expressed in SECONDS rather than frames. `-g SegmentSeconds*30` was
		// a 30 fps assumption baked into a field the operator never sets: on a
		// 25 fps ingest every segment overshot by 20%, measured as EXTINF
		// 1.200000 for a requested 1s. force_key_frames states the interval
		// exactly and is right at every frame rate.
		"-force_key_frames", fmt.Sprintf("expr:gte(t,n_forced*%d)", s.SegmentSeconds),
		// Still needed: it is what stops x264 inserting its own keyframes
		// between the forced ones on a scene change.
		"-sc_threshold", "0",
		"-c:a", "aac",
		"-b:a", "96k",
		"-ac", "2",
		"-f", "hls",
		"-hls_time", strconv.Itoa(s.SegmentSeconds),
		"-hls_list_size", strconv.Itoa(listSize),
		// program_date_time is not decoration. It stamps the first segment with
		// a wall-clock time, so live-edge latency becomes a NUMBER an operator
		// and a test can both read -- EXT-X-PROGRAM-DATE-TIME plus the
		// cumulative EXTINF, subtracted from now. Without it, latency can only
		// be claimed. internal/playout already sets it, for the same reason.
		"-hls_flags", "delete_segments+independent_segments+omit_endlist+program_date_time",
		// Default is 1, meaning a segment is unlinked the moment it leaves the
		// playlist. Keeping one extra lets a player's already-issued request
		// for the segment that just aged out still complete.
		"-hls_delete_threshold", "2",
		// MPEG-TS, deliberately. fMP4 measured ~8-9% smaller and buys ZERO
		// latency, while costing an init.mp4 lifecycle that has to survive the
		// on-demand stop/start cycle and an explicit content type for Safari's
		// native path. Its only strategic argument was as the prerequisite for
		// a Go-side LL-HLS packager, which docs/roadmap/LL-HLS.md declines to
		// build.
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
// MeterChannelLimit is amerge's ceiling. Beyond 64 channels it refuses outright
// with "Too many channels (max 64)", so this is a hard boundary of the
// one-process meters design rather than a tuning choice.
//
// Duplicated from routing.MaxMeterChannels rather than imported: this package
// builds command lines and deliberately does not depend on routing. The
// constants are asserted equal in the tests.
const MeterChannelLimit = 64

// meterableTracks reports how many leading tracks fit under MeterChannelLimit.
//
// Whole tracks only -- metering half of a 5.1 track would report channels
// against the wrong labels, which is worse than not reporting them. Returning
// fewer than len(chans) is a real outcome for a wide ingest, and MetersDropped
// is what tells the operator so.
//
// The first track is always included, however wide it is. The limit belongs to
// amerge, and a lone track is never merged -- it goes straight to astats. This
// also keeps the count non-zero, which the caller depends on: it indexes
// labels[0] unconditionally.
func meterableTracks(chans []int) int {
	total := 0
	for i, c := range chans {
		if c <= 0 {
			c = 1 // a track whose width never probed still occupies a leg
		}
		if i > 0 && total+c > MeterChannelLimit {
			return i
		}
		total += c
	}
	return len(chans)
}

// MetersDropped reports how many trailing tracks MetersArgs could not cover.
//
// Zero for every ingest anyone is likely to send -- 32 stereo tracks fit. It
// exists so a wide ingest degrades visibly instead of silently metering a
// prefix and letting an operator believe a track is silent when it is merely
// unmeasured.
func MetersDropped(chans []int) int {
	return len(chans) - meterableTracks(chans)
}

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
	n := meterableTracks(s.TrackChannels)

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
