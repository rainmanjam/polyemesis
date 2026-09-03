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
	"github.com/rainmanjam/polyemesis/internal/relay"
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

// THE 4c ORDERING, EXACTLY: the reader starts first, into silence. #674
//
// Every reproduction that passes had the ingest ALREADY PRODUCING when the
// reader attached. The acceptance rig does the opposite: the destination is
// created and started while nothing is flowing at all, and the publisher
// arrives seconds later. That ordering is the last untested variable, and it is
// the original hypothesis -- discarded earlier on the grounds that the
// destination's probe ran 77 seconds and therefore covered the publisher's
// whole life. That reasoning assumed the child spawns when the engine logs
// "destination starting"; the supervisor has a StartDelay and the child had
// already been restarted three times, so the assumption was never sound.
//
// Reader first, into an empty socket. Publisher and ingest afterwards.
func TestAReaderStartedBeforeAnyDataStillPublishesItsAudio(t *testing.T) {
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
	_ = pc.Close()
	relayURL := fmt.Sprintf("udp://127.0.0.1:%d", relayPort)

	// THE READER FIRST, into a socket nothing is sending to yet.
	tsPath := filepath.Join(t.TempDir(), "early.ts")
	rdCtx, stopRd := context.WithTimeout(context.Background(), 60*time.Second)
	defer stopRd()
	rdArgs := append([]string{"-nostdin", "-hide_banner", "-loglevel", "error"},
		ffmpeg.RelayInputArgs()...)
	rdArgs = append(rdArgs, "-i", ffmpeg.RelayInputURL(relayURL),
		"-filter_complex", "[0:a:1]pan=stereo|c0=1*c0|c1=1*c1[a_t1];[a_t1]aresample=48000:async=1:first_pts=0[aout]",
		"-map", "0:v:0", "-c:v", "copy",
		"-map", "[aout]", "-c:a", "aac", "-b:a", "128k",
		"-f", "mpegts", "-flush_packets", "1", "-t", "12", "-y", tsPath)
	rd := exec.CommandContext(rdCtx, ffmpegBin, rdArgs...)
	var rdErr strings.Builder
	rd.Stderr = &rdErr
	if err := rd.Start(); err != nil {
		t.Fatalf("early reader: %v", err)
	}
	defer func() { _ = rd.Process.Kill(); _ = rd.Wait() }()

	// Nothing is flowing. This is the gap 4c puts a destination into.
	time.Sleep(5 * time.Second)

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

	ingCtx, stopIngest := context.WithTimeout(context.Background(), 45*time.Second)
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

	if err := rd.Wait(); err != nil {
		t.Logf("early reader exited: %v", err)
	}

	fi, _ := os.Stat(tsPath)
	var size int64 = -1
	if fi != nil {
		size = fi.Size()
	}
	out, probeErr := exec.Command(ffprobeBin, "-hide_banner", "-loglevel", "error",
		"-f", "mpegts", "-select_streams", "a", "-show_streams", "-of", "json", tsPath).Output()
	if probeErr != nil {
		t.Fatalf("a reader started before any data produced nothing parseable "+
			"(%d bytes): %v\n\nTHIS IS THE 4c ORDERING. If it reproduces here it "+
			"reproduces in seconds.\n\nreader stderr:\n%s\n\ningest stderr:\n%s",
			size, probeErr, rdErr.String(), ingErr.String())
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
	t.Logf("early-start capture: %d bytes, %d audio streams", size, len(probed.Streams))
	if len(probed.Streams) != 1 {
		t.Fatalf("a reader started BEFORE any data produced %d of 1 audio streams "+
			"(%d bytes).\n\nThe same reader attaching to an already-running ingest "+
			"produces one. If starting into silence is what breaks it, this is #674 "+
			"reproduced in seconds instead of a twelve-minute rig.\n\n"+
			"reader stderr:\n%s", len(probed.Streams), size, rdErr.String())
	}
}

// THE WHOLE CHAIN, in production shape: ingest -> HUB -> destination. #674
//
// Everything is cleared piecewise and the rig still fails, which means the
// fault is in a COMBINATION rather than a part. This is the combination never
// built: previous tests pointed the reader at the ingest's own port, and the
// hub hop was measured separately with synthetic datagrams. Here the ingest
// writes into a real relay.Hub, the hub fans out to a real subscription, and a
// destination-shaped reader consumes that subscription -- the exact path a
// destination takes, with nothing standing in for anything.
func TestTheWholeChainThroughARealHubCarriesTheRoutedAudio(t *testing.T) {
	if testing.Short() {
		t.Skip("runs three FFmpeg processes, a hub and an ffprobe")
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

	// A REAL HUB, as the engine builds one.
	hub, err := relay.New(quiet(), 0)
	if err != nil {
		t.Fatalf("hub: %v", err)
	}
	defer hub.Close()

	// A real subscription, on a real port, as a destination gets.
	subPC, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("subscriber port: %v", err)
	}
	subPort := subPC.LocalAddr().(*net.UDPAddr).Port
	_ = subPC.Close() // the reader binds it
	subURL := hub.Subscribe("dest:test", subPort)
	defer hub.Unsubscribe("dest:test")

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

	// The ingest writes into the HUB's input, not straight at the reader.
	ingCtx, stopIngest := context.WithTimeout(context.Background(), 45*time.Second)
	defer stopIngest()
	ing := exec.CommandContext(ingCtx, ffmpegBin, ffmpeg.IngestArgs(ffmpeg.IngestSpec{
		Kind: ffmpeg.IngestRTMP, RTMPPort: port, RTMPApp: "live", RTMPAddress: "mt",
		RelayURL: hub.InputURL(),
	})...)
	var ingErr strings.Builder
	ing.Stderr = &ingErr
	if err := ing.Start(); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	defer func() { _ = ing.Process.Kill(); _ = ing.Wait() }()

	time.Sleep(6 * time.Second)

	tsPath := filepath.Join(t.TempDir(), "chain.ts")
	rdCtx, stopRd := context.WithTimeout(context.Background(), 45*time.Second)
	defer stopRd()
	rdArgs := append([]string{"-nostdin", "-hide_banner", "-loglevel", "error"},
		ffmpeg.RelayInputArgs()...)
	rdArgs = append(rdArgs, "-i", ffmpeg.RelayInputURL(subURL),
		"-filter_complex", "[0:a:1]pan=stereo|c0=1*c0|c1=1*c1[a_t1];[a_t1]aresample=48000:async=1:first_pts=0[aout]",
		"-map", "0:v:0", "-c:v", "copy",
		"-map", "[aout]", "-c:a", "aac", "-b:a", "128k",
		"-f", "mpegts", "-flush_packets", "1", "-t", "10", "-y", tsPath)
	rd := exec.CommandContext(rdCtx, ffmpegBin, rdArgs...)
	var rdErr strings.Builder
	rd.Stderr = &rdErr
	if err := rd.Start(); err != nil {
		t.Fatalf("chain reader: %v", err)
	}
	if err := rd.Wait(); err != nil {
		t.Logf("chain reader exited: %v", err)
	}

	st := hub.Stats()
	t.Logf("hub: rx=%d tx=%d dropped=%d tsLost=%d loss=%.3f%%",
		st.RxPackets, st.TxPackets, st.Dropped, st.TSLost, st.LossPercent)

	fi, _ := os.Stat(tsPath)
	var size int64 = -1
	if fi != nil {
		size = fi.Size()
	}
	out, probeErr := exec.Command(ffprobeBin, "-hide_banner", "-loglevel", "error",
		"-f", "mpegts", "-select_streams", "a", "-show_streams", "-of", "json", tsPath).Output()
	if probeErr != nil {
		t.Fatalf("the full chain produced nothing parseable (%d bytes): %v\n\n"+
			"hub rx=%d tx=%d dropped=%d\n\nreader stderr:\n%s\n\ningest stderr:\n%s",
			size, probeErr, st.RxPackets, st.TxPackets, st.Dropped, rdErr.String(), ingErr.String())
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
	t.Logf("through-the-hub capture: %d bytes, %d audio streams", size, len(probed.Streams))
	if len(probed.Streams) != 1 {
		t.Fatalf("the full chain -- ingest -> hub -> destination -- produced %d of 1 "+
			"audio streams (%d bytes).\n\nEvery link passes on its own. If the "+
			"COMBINATION fails, this is #674 reproduced in seconds.\n\n"+
			"hub rx=%d tx=%d dropped=%d\n\nreader stderr:\n%s",
			len(probed.Streams), size, st.RxPackets, st.TxPackets, st.Dropped, rdErr.String())
	}
}

// THE 4b -> 4c SHAPE: a publisher ends, a gap, another begins. #674
//
// This is what the rig does and no reproduction has carried. The ingest is ONE
// long-lived process spanning both publishes -- the acceptance run shows it
// execing exactly once -- so the relay is a single continuous timeline whose
// PTS keeps advancing across the gap. A destination created in the gap joins a
// stream that has already been running for forty seconds and whose audio has
// STOPPED, and then audio resumes under it.
//
// Every earlier reproduction gave the reader a stream whose audio was either
// always present or had never yet started. Neither is the rig.
func TestAReaderJoiningBetweenTwoPublishesResolvesTheSecondOnesAudio(t *testing.T) {
	if testing.Short() {
		t.Skip("runs four FFmpeg processes and an ffprobe")
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

	hub, err := relay.New(quiet(), 0)
	if err != nil {
		t.Fatalf("hub: %v", err)
	}
	defer hub.Close()

	publish := func(seconds string) *exec.Cmd {
		c := exec.Command(ffmpegBin, "-nostdin", "-hide_banner", "-loglevel", "error", "-re",
			"-f", "lavfi", "-i", "testsrc2=size=320x240:rate=15",
			"-f", "lavfi", "-i", "sine=frequency=300:sample_rate=48000",
			"-f", "lavfi", "-i", "sine=frequency=900:sample_rate=48000",
			"-f", "lavfi", "-i", "sine=frequency=1700:sample_rate=48000",
			"-map", "0:v", "-map", "1:a", "-map", "2:a", "-map", "3:a",
			"-c:v", "libx264", "-preset", "ultrafast", "-tune", "zerolatency", "-b:v", "500k",
			"-c:a", "aac", "-ac", "2", "-t", seconds,
			"-f", "flv", "rtmp://"+addr+"/live/mt")
		return c
	}

	// FIRST publish, and the ingest that outlives it.
	pub1 := publish("10")
	if err := pub1.Start(); err != nil {
		t.Fatalf("publisher 1: %v", err)
	}
	waitPublishing(t, s, tg.SourceID, 25*time.Second)

	ingCtx, stopIngest := context.WithTimeout(context.Background(), 90*time.Second)
	defer stopIngest()
	ing := exec.CommandContext(ingCtx, ffmpegBin, ffmpeg.IngestArgs(ffmpeg.IngestSpec{
		Kind: ffmpeg.IngestRTMP, RTMPPort: port, RTMPApp: "live", RTMPAddress: "mt",
		RelayURL: hub.InputURL(),
	})...)
	var ingErr strings.Builder
	ing.Stderr = &ingErr
	if err := ing.Start(); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	defer func() { _ = ing.Process.Kill(); _ = ing.Wait() }()

	_ = pub1.Wait() // the first publish ends; audio stops

	// THE GAP: the destination is created here, as 4c does.
	subPC, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("subscriber port: %v", err)
	}
	subPort := subPC.LocalAddr().(*net.UDPAddr).Port
	_ = subPC.Close()
	subURL := hub.Subscribe("dest:test", subPort)
	defer hub.Unsubscribe("dest:test")

	tsPath := filepath.Join(t.TempDir(), "gap.ts")
	rdCtx, stopRd := context.WithTimeout(context.Background(), 70*time.Second)
	defer stopRd()
	rdArgs := append([]string{"-nostdin", "-hide_banner", "-loglevel", "error"},
		ffmpeg.RelayInputArgs()...)
	rdArgs = append(rdArgs, "-i", ffmpeg.RelayInputURL(subURL),
		"-filter_complex", "[0:a:1]pan=stereo|c0=1*c0|c1=1*c1[a_t1];[a_t1]aresample=48000:async=1:first_pts=0[aout]",
		"-map", "0:v:0", "-c:v", "copy",
		"-map", "[aout]", "-c:a", "aac", "-b:a", "128k",
		"-f", "mpegts", "-flush_packets", "1", "-t", "12", "-y", tsPath)
	rd := exec.CommandContext(rdCtx, ffmpegBin, rdArgs...)
	var rdErr strings.Builder
	rd.Stderr = &rdErr
	if err := rd.Start(); err != nil {
		t.Fatalf("gap reader: %v", err)
	}
	defer func() { _ = rd.Process.Kill(); _ = rd.Wait() }()

	// SECOND publish, arriving under a reader that is already probing.
	time.Sleep(4 * time.Second)
	pub2 := publish("25")
	if err := pub2.Start(); err != nil {
		t.Fatalf("publisher 2: %v", err)
	}
	defer func() { _ = pub2.Process.Kill(); _ = pub2.Wait() }()

	if err := rd.Wait(); err != nil {
		t.Logf("gap reader exited: %v", err)
	}
	st := hub.Stats()
	fi, _ := os.Stat(tsPath)
	var size int64 = -1
	if fi != nil {
		size = fi.Size()
	}
	out, probeErr := exec.Command(ffprobeBin, "-hide_banner", "-loglevel", "error",
		"-f", "mpegts", "-select_streams", "a", "-show_streams", "-of", "json", tsPath).Output()
	if probeErr != nil {
		t.Fatalf("a reader that joined between two publishes produced nothing parseable "+
			"(%d bytes): %v\n\nTHIS IS THE 4b->4c SHAPE. hub rx=%d tx=%d dropped=%d\n\n"+
			"reader stderr:\n%s\n\ningest stderr:\n%s",
			size, probeErr, st.RxPackets, st.TxPackets, st.Dropped, rdErr.String(), ingErr.String())
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
	t.Logf("gap-join capture: %d bytes, %d audio streams, hub rx=%d tx=%d dropped=%d",
		size, len(probed.Streams), st.RxPackets, st.TxPackets, st.Dropped)
	if len(probed.Streams) != 1 {
		t.Fatalf("a reader that joined BETWEEN two publishes produced %d of 1 audio "+
			"streams (%d bytes).\n\nThe same reader joining a continuously-audible "+
			"stream produces one. If a stopped-then-resumed audio track is what breaks "+
			"characterisation, this is #674 reproduced in seconds.\n\n"+
			"reader stderr:\n%s", len(probed.Streams), size, rdErr.String())
	}
}

// NINE SUBSCRIBERS, as the rig has. #674
//
// Every reproduction so far had ONE consumer. The acceptance run has nine:
// dest:1-4, loudness:1-4 and meters, all reading the same hub at once. That is
// one of only two differences left between a passing reproduction and the
// failing rig -- the other being the engine's own reconcile and restart
// behaviour. This adds eight competing readers and then asks the ninth, the
// destination-shaped one, whether it can still characterise its audio.
func TestADestinationStillResolvesItsAudioBesideEightOtherSubscribers(t *testing.T) {
	if testing.Short() {
		t.Skip("runs eleven FFmpeg processes and an ffprobe")
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

	hub, err := relay.New(quiet(), 0)
	if err != nil {
		t.Fatalf("hub: %v", err)
	}
	defer hub.Close()

	pub := exec.Command(ffmpegBin, "-nostdin", "-hide_banner", "-loglevel", "error", "-re",
		"-f", "lavfi", "-i", "testsrc2=size=320x240:rate=15",
		"-f", "lavfi", "-i", "sine=frequency=300:sample_rate=48000",
		"-f", "lavfi", "-i", "sine=frequency=900:sample_rate=48000",
		"-f", "lavfi", "-i", "sine=frequency=1700:sample_rate=48000",
		"-map", "0:v", "-map", "1:a", "-map", "2:a", "-map", "3:a",
		"-c:v", "libx264", "-preset", "ultrafast", "-tune", "zerolatency", "-b:v", "500k",
		"-c:a", "aac", "-ac", "2", "-t", "40",
		"-f", "flv", "rtmp://"+addr+"/live/mt")
	if err := pub.Start(); err != nil {
		t.Fatalf("publisher: %v", err)
	}
	defer func() { _ = pub.Process.Kill(); _ = pub.Wait() }()
	waitPublishing(t, s, tg.SourceID, 25*time.Second)

	ingCtx, stopIngest := context.WithTimeout(context.Background(), 70*time.Second)
	defer stopIngest()
	ing := exec.CommandContext(ingCtx, ffmpegBin, ffmpeg.IngestArgs(ffmpeg.IngestSpec{
		Kind: ffmpeg.IngestRTMP, RTMPPort: port, RTMPApp: "live", RTMPAddress: "mt",
		RelayURL: hub.InputURL(),
	})...)
	var ingErr strings.Builder
	ing.Stderr = &ingErr
	if err := ing.Start(); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	defer func() { _ = ing.Process.Kill(); _ = ing.Wait() }()

	// EIGHT competing readers, as loudness:1-4, meters and dest:1-3 are.
	othCtx, stopOth := context.WithTimeout(context.Background(), 60*time.Second)
	defer stopOth()
	for i := 0; i < 8; i++ {
		pc, perr := net.ListenPacket("udp", "127.0.0.1:0")
		if perr != nil {
			t.Fatalf("other subscriber port: %v", perr)
		}
		op := pc.LocalAddr().(*net.UDPAddr).Port
		_ = pc.Close()
		name := fmt.Sprintf("other:%d", i)
		ou := hub.Subscribe(name, op)
		defer hub.Unsubscribe(name)
		oa := append([]string{"-nostdin", "-hide_banner", "-loglevel", "error"},
			ffmpeg.RelayInputArgs()...)
		oa = append(oa, "-i", ffmpeg.RelayInputURL(ou), "-map", "0", "-c", "copy",
			"-f", "null", "-")
		oc := exec.CommandContext(othCtx, ffmpegBin, oa...)
		if serr := oc.Start(); serr != nil {
			t.Fatalf("other reader %d: %v", i, serr)
		}
		defer func() { _ = oc.Process.Kill(); _ = oc.Wait() }()
	}

	time.Sleep(5 * time.Second)

	// THE NINTH: the destination.
	subPC, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("subscriber port: %v", err)
	}
	subPort := subPC.LocalAddr().(*net.UDPAddr).Port
	_ = subPC.Close()
	subURL := hub.Subscribe("dest:test", subPort)
	defer hub.Unsubscribe("dest:test")

	tsPath := filepath.Join(t.TempDir(), "crowded.ts")
	rdCtx, stopRd := context.WithTimeout(context.Background(), 50*time.Second)
	defer stopRd()
	rdArgs := append([]string{"-nostdin", "-hide_banner", "-loglevel", "error"},
		ffmpeg.RelayInputArgs()...)
	rdArgs = append(rdArgs, "-i", ffmpeg.RelayInputURL(subURL),
		"-filter_complex", "[0:a:1]pan=stereo|c0=1*c0|c1=1*c1[a_t1];[a_t1]aresample=48000:async=1:first_pts=0[aout]",
		"-map", "0:v:0", "-c:v", "copy",
		"-map", "[aout]", "-c:a", "aac", "-b:a", "128k",
		"-f", "mpegts", "-flush_packets", "1", "-t", "12", "-y", tsPath)
	rd := exec.CommandContext(rdCtx, ffmpegBin, rdArgs...)
	var rdErr strings.Builder
	rd.Stderr = &rdErr
	if err := rd.Start(); err != nil {
		t.Fatalf("destination reader: %v", err)
	}
	if err := rd.Wait(); err != nil {
		t.Logf("destination reader exited: %v", err)
	}

	st := hub.Stats()
	fi, _ := os.Stat(tsPath)
	var size int64 = -1
	if fi != nil {
		size = fi.Size()
	}
	out, probeErr := exec.Command(ffprobeBin, "-hide_banner", "-loglevel", "error",
		"-f", "mpegts", "-select_streams", "a", "-show_streams", "-of", "json", tsPath).Output()
	if probeErr != nil {
		t.Fatalf("with eight other subscribers the destination produced nothing "+
			"parseable (%d bytes): %v\n\nhub rx=%d tx=%d dropped=%d subs=%d\n\n"+
			"reader stderr:\n%s", size, probeErr, st.RxPackets, st.TxPackets,
			st.Dropped, len(st.Subscribers), rdErr.String())
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
	t.Logf("crowded capture: %d bytes, %d audio streams, hub rx=%d tx=%d dropped=%d subs=%d",
		size, len(probed.Streams), st.RxPackets, st.TxPackets, st.Dropped, len(st.Subscribers))
	if len(probed.Streams) != 1 {
		t.Fatalf("beside eight other subscribers the destination produced %d of 1 audio "+
			"streams (%d bytes).\n\nAlone it produces one. If CONCURRENCY is what breaks "+
			"characterisation, this is #674 -- and the rig runs exactly nine.\n\n"+
			"hub rx=%d tx=%d dropped=%d\n\nreader stderr:\n%s",
			len(probed.Streams), size, st.RxPackets, st.TxPackets, st.Dropped, rdErr.String())
	}
}
