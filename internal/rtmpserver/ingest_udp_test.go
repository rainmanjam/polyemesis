package rtmpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/ffmpeg"
)

// THE ONLY DIFFERENCE BETWEEN THE REPRODUCTION AND THE RIG IS THE TRANSPORT.
//
// TestTheIngestArgvDiffersOnlyInItsOutputURL proves the shipped ingest argv and
// the one the file-based reproduction drives are identical in every argument
// except the last: the rig writes to udp://...?pkt_size=1316, the reproduction
// to a file. On ffmpeg 8.1.2 the file case carries all three AAC tracks once the
// interleave flags are in place -- and the acceptance rig, with those same
// flags, still shows ZERO audio on the relay for a whole E-RTMP window.
//
// So this runs the SHIPPED argv unchanged, to a real UDP socket, with a reader
// on the other end. If the tracks survive here, the ingest is not where the
// rig's audio is lost and the fault is downstream in the relay or its fan-out.
// If they do not, this is the reproduction -- eight seconds instead of twelve
// minutes. #674.
func TestTheIngestRemuxKeepsItsAudioOverUDP(t *testing.T) {
	if testing.Short() {
		t.Skip("runs three FFmpeg processes and an ffprobe")
	}
	ffmpegBin, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not installed")
	}
	ffprobeBin, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("ffprobe is not installed")
	}
	if major := ffmpegMajor(t, ffmpegBin); major != 8 {
		t.Skipf("ffmpeg %d.x; #674 reproduces on the shipped 8.x", major)
	}

	tg := Target{SourceID: 1, Name: "Main", Enabled: true, Ready: true}
	s := New(quiet(), "127.0.0.1:0", ConstantTimeLookup(map[string]Target{"mt": tg}))
	if err := s.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer s.Stop()
	s.mu.Lock()
	addr := s.ln.Addr().String()
	s.mu.Unlock()
	_, portStr, _ := strings.Cut(addr, ":")
	port, _ := strconv.Atoi(portStr)

	// A free UDP port for the relay leg.
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve udp port: %v", err)
	}
	// NOT closed: this very socket is the receiver below. Closing it and
	// handing the port to another process is what opened a race in which
	// nothing was listening when the ingest began sending.
	defer pc.Close()
	relayPort := pc.LocalAddr().(*net.UDPAddr).Port
	relayURL := fmt.Sprintf("udp://127.0.0.1:%d", relayPort)

	pubCtx, stopPub := context.WithCancel(context.Background())
	defer stopPub()
	pub := exec.CommandContext(pubCtx, ffmpegBin, "-nostdin", "-hide_banner", "-loglevel", "error", "-re",
		"-f", "lavfi", "-i", "testsrc2=size=320x240:rate=15",
		"-f", "lavfi", "-i", "sine=frequency=300:sample_rate=48000",
		"-f", "lavfi", "-i", "sine=frequency=900:sample_rate=48000",
		"-f", "lavfi", "-i", "sine=frequency=1700:sample_rate=48000",
		"-map", "0:v", "-map", "1:a", "-map", "2:a", "-map", "3:a",
		"-c:v", "libx264", "-preset", "ultrafast", "-tune", "zerolatency", "-b:v", "500k",
		"-c:a", "aac", "-ac", "2",
		"-f", "flv", "rtmp://"+addr+"/live/mt")
	var pubErr strings.Builder
	pub.Stderr = &pubErr
	if err := pub.Start(); err != nil {
		t.Fatalf("publisher: %v", err)
	}
	defer func() { _ = pub.Process.Kill(); _ = pub.Wait() }()
	waitPublishing(t, s, tg.SourceID, 25*time.Second)

	// A RAW SOCKET, NOT ANOTHER FFmpeg.
	//
	// Four attempts to receive this with `ffmpeg -i udp://...` produced a
	// 0-byte capture while the ingest was demonstrably muxing 779 KB to the
	// socket -- and a 0-byte capture is indistinguishable from "the ingest sent
	// no audio", which is the one thing this test must not get wrong. A plain
	// PacketConn cannot probe, cannot buffer, and cannot exit early: whatever
	// arrives is counted. The socket is bound BEFORE the sender starts, because
	// UDP has no backlog.
	tsPath := filepath.Join(t.TempDir(), "fromudp.ts")
	f, err := os.Create(tsPath)
	if err != nil {
		t.Fatalf("capture file: %v", err)
	}
	defer f.Close()

	var got int64
	recvDone := make(chan struct{})
	go func() {
		defer close(recvDone)
		buf := make([]byte, 65536)
		_ = pc.SetReadDeadline(time.Now().Add(14 * time.Second))
		for {
			n, _, rerr := pc.ReadFrom(buf)
			if n > 0 {
				got += int64(n)
				_, _ = f.Write(buf[:n])
			}
			if rerr != nil {
				return
			}
		}
	}()

	// THE SHIPPED ARGV, UNCHANGED. No tsOutput rewrite: this is the command
	// line the engine spawns, transport included.
	args := ffmpeg.IngestArgs(ffmpeg.IngestSpec{
		Kind: ffmpeg.IngestRTMP, RTMPPort: port, RTMPApp: "live", RTMPAddress: "mt",
		RelayURL: relayURL,
	})
	ingCtx, stopIngest := context.WithTimeout(context.Background(), 12*time.Second)
	defer stopIngest()
	ing := exec.CommandContext(ingCtx, ffmpegBin, args...)
	var ingErr, ingOut strings.Builder
	ing.Stderr = &ingErr
	// -progress pipe:1 is already in the shipped argv. total_size in it says
	// whether the muxer wrote ANYTHING, which is the difference between "the
	// ingest produced nothing" and "it produced bytes that never arrived".
	ing.Stdout = &ingOut
	if err := ing.Start(); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	// Wait for the READER to end on its own (-t 8), so its file is flushed and
	// closed, and only then stop the sender.
	<-recvDone
	_ = f.Sync()
	t.Logf("bytes received on the relay socket: %d", got)
	_ = ing.Process.Kill()
	ingWaitErr := ing.Wait()
	prog := ingOut.String()
	tail := prog
	if len(tail) > 400 {
		tail = tail[len(tail)-400:]
	}
	t.Logf("ingest exit: %v", ingWaitErr)
	t.Logf("ingest progress tail:\n%s", tail)

	// SIZE FIRST. A zero-byte capture and a malformed one both make ffprobe
	// exit 1, and they mean opposite things: nothing arrived over UDP versus
	// something arrived that cannot be parsed. Reporting the size separates
	// them without another run.
	fi, statErr := os.Stat(tsPath)
	size := int64(-1)
	if statErr == nil {
		size = fi.Size()
	}
	probe := exec.Command(ffprobeBin, "-hide_banner", "-loglevel", "error",
		"-select_streams", "a", "-show_streams", "-of", "json", tsPath)
	var probeErrBuf strings.Builder
	probe.Stderr = &probeErrBuf
	out, probeErr := probe.Output()
	if probeErr != nil {
		t.Fatalf("ffprobe: %v (capture %d bytes, stat err %v)\n\nffprobe stderr:\n%s\n\n"+
			"ingest stderr:\n%s\n\nreader stderr:\n%s",
			probeErr, size, statErr, probeErrBuf.String(), ingErr.String(), "")
	}
	t.Logf("UDP capture: %d bytes", size)
	var probed struct {
		Streams []struct {
			Channels   int    `json:"channels"`
			SampleRate string `json:"sample_rate"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(out, &probed); err != nil {
		t.Fatalf("ffprobe json: %v\n%s", err, out)
	}
	if len(probed.Streams) != 3 {
		t.Fatalf("expected 3 audio streams over UDP, got %d.\n\n"+
			"The SAME argv to a FILE carries all three on this FFmpeg. If this fails, the\n"+
			"ingest loses audio only when its output is a UDP socket, which is what the\n"+
			"acceptance rig does -- and that is #674 reproduced in eight seconds.\n\n"+
			"probe:\n%s\n\ningest stderr:\n%s\n\nreader stderr:\n%s",
			len(probed.Streams), out, ingErr.String(), "")
	}
	for i, st := range probed.Streams {
		if st.Channels == 0 || st.SampleRate == "" {
			t.Errorf("audio stream %d arrived over UDP without channel configuration "+
				"(channels=%d sample_rate=%q)", i, st.Channels, st.SampleRate)
		}
	}
}

// A DESTINATION JOINS MID-STREAM. Does that break audio characterisation? #674
//
// Everything that has parsed this audio successfully read it from the START: a
// complete capture file, or a socket bound before the sender began. A
// destination does neither -- it subscribes to a relay that is already running
// and begins reading at an arbitrary offset. The acceptance rig says such a
// reader gets 0 audio packets while the hub reports dropped=0 and 0.031% loss,
// so the bytes reach it and it cannot make sense of them.
//
// This starts the ingest, lets it run, and only THEN attaches a reader carrying
// the destination's own input options (ffmpeg.RelayInputArgs). If the audio
// streams resolve, mid-stream joining is not the fault. If they do not, this is
// #674 in eight seconds.
func TestADestinationShapedReaderJoiningMidStreamPublishesItsAudio(t *testing.T) {
	if testing.Short() {
		t.Skip("runs three FFmpeg processes and an ffprobe")
	}
	ffmpegBin, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not installed")
	}
	ffprobeBin, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("ffprobe is not installed")
	}
	if major := ffmpegMajor(t, ffmpegBin); major != 8 {
		t.Skipf("ffmpeg %d.x; #674 reproduces on the shipped 8.x", major)
	}

	tg := Target{SourceID: 1, Name: "Main", Enabled: true, Ready: true}
	s := New(quiet(), "127.0.0.1:0", ConstantTimeLookup(map[string]Target{"mt": tg}))
	if err := s.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer s.Stop()
	s.mu.Lock()
	addr := s.ln.Addr().String()
	s.mu.Unlock()
	_, portStr, _ := strings.Cut(addr, ":")
	port, _ := strconv.Atoi(portStr)

	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve udp port: %v", err)
	}
	relayPort := pc.LocalAddr().(*net.UDPAddr).Port
	_ = pc.Close() // the ingest sends here; a real reader binds it below
	relayURL := fmt.Sprintf("udp://127.0.0.1:%d", relayPort)

	pubCtx, stopPub := context.WithCancel(context.Background())
	defer stopPub()
	pub := exec.CommandContext(pubCtx, ffmpegBin, "-nostdin", "-hide_banner", "-loglevel", "error", "-re",
		"-f", "lavfi", "-i", "testsrc2=size=320x240:rate=15",
		"-f", "lavfi", "-i", "sine=frequency=300:sample_rate=48000",
		"-f", "lavfi", "-i", "sine=frequency=900:sample_rate=48000",
		"-f", "lavfi", "-i", "sine=frequency=1700:sample_rate=48000",
		"-map", "0:v", "-map", "1:a", "-map", "2:a", "-map", "3:a",
		"-c:v", "libx264", "-preset", "ultrafast", "-tune", "zerolatency", "-b:v", "500k",
		"-c:a", "aac", "-ac", "2",
		"-f", "flv", "rtmp://"+addr+"/live/mt")
	if err := pub.Start(); err != nil {
		t.Fatalf("publisher: %v", err)
	}
	defer func() { _ = pub.Process.Kill(); _ = pub.Wait() }()
	waitPublishing(t, s, tg.SourceID, 25*time.Second)

	ingCtx, stopIngest := context.WithTimeout(context.Background(), 40*time.Second)
	defer stopIngest()
	ing := exec.CommandContext(ingCtx, ffmpegBin, ffmpeg.IngestArgs(ffmpeg.IngestSpec{
		Kind: ffmpeg.IngestRTMP, RTMPPort: port, RTMPApp: "live", RTMPAddress: "mt",
		RelayURL: relayURL,
	})...)
	var ingErr strings.Builder
	ing.Stderr = &ingErr
	if err := ing.Start(); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	defer func() { _ = ing.Process.Kill(); _ = ing.Wait() }()

	// THE POINT: attach LATE, the way a destination does.
	time.Sleep(8 * time.Second)

	tsPath := filepath.Join(t.TempDir(), "late.ts")
	rdCtx, stopRd := context.WithTimeout(context.Background(), 40*time.Second)
	defer stopRd()
	// THE DESTINATION'S OWN SHAPE, not -c copy.
	//
	// Every reader that has parsed this audio so far used -c copy, which only
	// needs the streams IDENTIFIED. A destination runs a filtergraph and an
	// encoder, which needs them DECODED -- it routes one track through
	// pan/aresample and encodes AAC. That is the last configuration this
	// investigation has never reproduced, and the rig says it reads 0 audio
	// packets while -c copy readers on the same bytes read all three.
	rdArgs := append([]string{"-nostdin", "-hide_banner", "-loglevel", "error"},
		ffmpeg.RelayInputArgs()...)
	rdArgs = append(rdArgs, "-i", ffmpeg.RelayInputURL(relayURL),
		"-filter_complex", "[0:a:1]pan=stereo|c0=1*c0|c1=1*c1[a_t1];[a_t1]aresample=48000:async=1:first_pts=0[aout]",
		"-map", "0:v:0", "-c:v", "copy",
		"-map", "[aout]", "-c:a", "aac", "-b:a", "128k",
		"-f", "mpegts", "-flush_packets", "1",
		"-t", "10", "-y", tsPath)
	rd := exec.CommandContext(rdCtx, ffmpegBin, rdArgs...)
	var rdErr strings.Builder
	rd.Stderr = &rdErr
	if err := rd.Start(); err != nil {
		t.Fatalf("late reader: %v", err)
	}
	if err := rd.Wait(); err != nil {
		t.Logf("late reader exited: %v", err)
	}

	fi, _ := os.Stat(tsPath)
	var size int64 = -1
	if fi != nil {
		size = fi.Size()
	}
	out, probeErr := exec.Command(ffprobeBin, "-hide_banner", "-loglevel", "error",
		"-f", "mpegts", "-select_streams", "a", "-show_streams", "-of", "json", tsPath).Output()
	if probeErr != nil {
		t.Fatalf("ffprobe: %v (capture %d bytes)\n\nreader stderr:\n%s\n\ningest stderr:\n%s",
			probeErr, size, rdErr.String(), ingErr.String())
	}
	var probed struct {
		Streams []struct {
			Channels   int    `json:"channels"`
			SampleRate string `json:"sample_rate"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(out, &probed); err != nil {
		t.Fatalf("ffprobe json: %v\n%s", err, out)
	}
	t.Logf("late-join capture: %d bytes, %d audio streams", size, len(probed.Streams))
	// ONE stream out: the graph routes track 2 and encodes a single AAC pair.
	// Zero means the filter never initialised, which is the #674 signature.
	if len(probed.Streams) != 1 {
		t.Fatalf("a destination-shaped reader joining mid-stream produced %d of 1 audio streams.\n\n"+
			"A -c copy reader on these same bytes resolves all three tracks, and the hub\n"+
			"reports dropped=0. If DECODING is what fails where identifying succeeds, this\n"+
			"is #674 reproduced in seconds instead of a twelve-minute rig.\n\n"+
			"reader stderr:\n%s", len(probed.Streams), rdErr.String())
	}
	for i, st := range probed.Streams {
		if st.Channels == 0 || st.SampleRate == "" {
			t.Errorf("late-joined audio stream %d has no channel configuration "+
				"(channels=%d sample_rate=%q)", i, st.Channels, st.SampleRate)
		}
	}
}
