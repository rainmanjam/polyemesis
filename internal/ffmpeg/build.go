package ffmpeg

import (
	"fmt"
	"net/url"
	"path/filepath"
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
)

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

	// RelayURL is the loopback UDP address of the relay hub.
	RelayURL string
}

// IngestURL renders the listener URL. Exported so the UI can show the user the
// exact address to paste into OBS.
func (s IngestSpec) IngestURL() string {
	switch s.Kind {
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
func (s IngestSpec) PublicIngestURL(host string) string {
	switch s.Kind {
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
func IngestArgs(s IngestSpec) []string {
	args := commonArgs()
	args = append(args, progressArgs()...)

	if s.Kind == IngestRTMP {
		// The rtmp protocol's own listen option. FFmpeg accepts one publisher,
		// then exits; the supervisor respawns it, which is exactly the
		// "wait for the next session" behaviour we want.
		args = append(args, "-listen", "1")
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
)

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
}

// DestinationArgs builds one destination's command.
//
// The central promise of polyemesis lives in two lines here: video is
// `-c:v copy` (never re-encoded, never degraded, near-zero CPU) while audio is
// decoded, re-mixed through the routing graph, and re-encoded to the single
// stereo AAC track the platform will accept.
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
		"-i", RelayInputURL(s.RelayURL),
		"-filter_complex", s.FilterComplex,
		// Explicit maps only. Without them FFmpeg's default stream selection
		// would pick one arbitrary audio track and quietly ignore the routing
		// graph entirely — the single most damaging possible bug in this app.
		"-map", "0:v:0",
		"-c:v", "copy",
		"-map", "["+s.AudioOutLabel+"]",
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
	return args
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
