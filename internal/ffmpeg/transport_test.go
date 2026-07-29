package ffmpeg

import (
	"strings"
	"testing"
)

func transportDest(kind DestKind, target string, t TransportSpec) []string {
	return DestinationArgs(DestSpec{
		Kind: kind, Target: target, RelayURL: "udp://127.0.0.1:20001",
		FilterComplex: "[0:a:0]pan=stereo|c0=1*c0|c1=1*c1[a_t0];[a_t0]aresample=48000:async=1:first_pts=0[aout]",
		AudioBitrate:  160, CopyVideo: true, Transport: t,
	})
}

// The zero value must emit nothing. Every destination that predates this
// produces exactly the argv it always did, which is the only reason the change
// is safe to make to the builder every stream depends on.
func TestTransportZeroValueChangesNothing(t *testing.T) {
	for _, kind := range []struct {
		k      DestKind
		target string
	}{
		{DestRTMP, "rtmp://a.example/live/key"},
		{DestSRT, "srt://a.example:9000"},
		{DestFile, "/data/recordings/out.mkv"},
		{DestAudio, "icecast://source:pw@a.example:8000/live.mp3"},
	} {
		with := join(transportDest(kind.k, kind.target, TransportSpec{}))
		for _, flag := range []string{
			"-flvflags", "-max_muxing_queue_size", "-muxing_queue_data_threshold", "-rw_timeout",
		} {
			if strings.Contains(with, flag) {
				t.Errorf("%s: a zero TransportSpec emitted %s: %s", kind.target, flag, with)
			}
		}
	}
}

func TestTransportFlagsReachTheCommandLine(t *testing.T) {
	line := join(transportDest(DestRTMP, "rtmp://a.example/live/key", TransportSpec{
		NoDurationFilesize: true,
		MuxQueuePackets:    2048,
		MuxQueueBytes:      8 << 20,
		RWTimeoutSeconds:   10,
	}))
	for _, want := range []string{
		"-flvflags no_duration_filesize",
		"-max_muxing_queue_size 2048",
		"-muxing_queue_data_threshold 8388608",
		// Seconds in the settings, microseconds on the wire. A stray factor of
		// a thousand here is a timeout that never fires, which looks exactly
		// like the hang it was meant to prevent.
		"-rw_timeout 10000000",
	} {
		if !strings.Contains(line, want) {
			t.Errorf("missing %q in: %s", want, line)
		}
	}
	// Everything belongs BEFORE the target: FFmpeg binds an output option to
	// the output that follows it, so anything after the URL attaches to
	// nothing and silently does nothing.
	target := strings.Index(line, "rtmp://a.example/live/key")
	for _, flag := range []string{"-flvflags", "-max_muxing_queue_size", "-rw_timeout"} {
		if i := strings.Index(line, flag); i < 0 || i > target {
			t.Errorf("%s is not before the target: %s", flag, line)
		}
	}
}

// no_duration_filesize is an FLV option. Handing it to the mpegts or matroska
// muxer makes FFmpeg warn on every start, and a log full of warnings an
// operator has learned to ignore is worse than no log.
func TestNoDurationFilesizeIsRTMPOnly(t *testing.T) {
	on := TransportSpec{NoDurationFilesize: true}
	for _, kind := range []struct {
		k      DestKind
		target string
	}{
		{DestSRT, "srt://a.example:9000"},
		{DestFile, "/data/recordings/out.mkv"},
		{DestAudio, "icecast://source:pw@a.example:8000/live.mp3"},
	} {
		if line := join(transportDest(kind.k, kind.target, on)); strings.Contains(line, "flvflags") {
			t.Errorf("%s got an FLV-only flag: %s", kind.target, line)
		}
	}
	if line := join(transportDest(DestRTMP, "rtmp://a.example/live/key", on)); !strings.Contains(line, "flvflags") {
		t.Errorf("RTMP did NOT get the flag it is for: %s", line)
	}
}

// An audio-only destination hangs on a dead socket exactly the way a video one
// does, so the timeout has to reach it too.
func TestTransportReachesAudioOnlyDestinations(t *testing.T) {
	line := join(transportDest(DestAudio, "icecast://source:pw@a.example:8000/live.mp3",
		TransportSpec{RWTimeoutSeconds: 15}))
	if !strings.Contains(line, "-rw_timeout 15000000") {
		t.Errorf("audio-only destination has no socket timeout: %s", line)
	}
	if i, j := strings.Index(line, "-rw_timeout"), strings.Index(line, "icecast://"); i < 0 || i > j {
		t.Errorf("-rw_timeout is not before the target: %s", line)
	}
}

// The audio contract, asserted here because this file changed the builder that
// carries it.
func TestTransportDoesNotDisturbTheStreamMaps(t *testing.T) {
	line := join(transportDest(DestRTMP, "rtmp://a.example/live/key", TransportSpec{
		NoDurationFilesize: true, RWTimeoutSeconds: 10, MuxQueuePackets: 512,
	}))
	if !strings.Contains(line, "-map 0:v:0 -c:v copy") {
		t.Errorf("the video passthrough is gone: %s", line)
	}
	if !strings.Contains(line, "-map [aout]") {
		t.Errorf("the routing graph output is no longer mapped: %s", line)
	}
}
