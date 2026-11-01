package captions

import (
	"context"
	"net"
	"os"
	"os/exec"
	"testing"
	"time"
)

// TestSEIStageProofHarness is the local Docker proof's middle hop, not a unit
// test. It is skipped unless POLYEMESIS_CAPD_PUBLISH is set, so `go test` never
// starts ffmpeg or binds these ports on its own.
//
// The chain, all local, all foreground:
//
//	ffmpeg testsrc2 --udp 19001--> this test (SEIStage) --udp 19002--> ffmpeg -c copy --rtmp--> receiver
//
// Environment:
//
//	POLYEMESIS_CAPD_PUBLISH  rtmp URL the publisher pushes to (the receiver)
//	POLYEMESIS_CAPD_FFMPEG   ffmpeg binary (default "ffmpeg")
//	POLYEMESIS_CAPD_TEXT     caption to queue at the start (default below)
//	POLYEMESIS_CAPD_SECONDS  source length in seconds (default 14)
//
// The report of what this proved is cap-d.md in the wave 3 scratchpad.
func TestSEIStageProofHarness(t *testing.T) {
	publish := os.Getenv("POLYEMESIS_CAPD_PUBLISH")
	if publish == "" {
		t.Skip("proof harness: set POLYEMESIS_CAPD_PUBLISH to run")
	}
	ffmpeg := os.Getenv("POLYEMESIS_CAPD_FFMPEG")
	if ffmpeg == "" {
		ffmpeg = "ffmpeg"
	}
	text := os.Getenv("POLYEMESIS_CAPD_TEXT")
	if text == "" {
		text = "HELLO TWITCH SEI"
	}
	secs := os.Getenv("POLYEMESIS_CAPD_SECONDS")
	if secs == "" {
		secs = "14"
	}
	const inAddr, outAddr = "127.0.0.1:19001", "127.0.0.1:19002"

	in, err := net.ListenPacket("udp", inAddr)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()

	// The publisher listens first, so nothing is lost when the stage starts writing.
	pub := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "warning",
		"-i", "udp://"+outAddr+"?listen=1&timeout=20000000",
		"-c", "copy", "-f", "flv", publish)
	pub.Stderr = os.Stderr
	if err := pub.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = pub.Process.Kill(); _ = pub.Wait() }()

	// The source is the copied programme: H.264 with a picture every frame, sent
	// as MPEG-TS, the shape the relay carries.
	src := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "warning", "-re",
		"-f", "lavfi", "-i", "testsrc2=size=640x360:rate=30",
		"-t", secs, "-c:v", "libx264", "-preset", "veryfast", "-tune", "zerolatency",
		"-g", "30", "-bf", "0", "-f", "mpegts", "udp://"+inAddr)
	src.Stderr = os.Stderr
	if err := src.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = src.Process.Kill(); _ = src.Wait() }()

	out, err := net.Dial("udp", outAddr)
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()

	s := NewSEIStage()
	s.Cue(text, 8*time.Second)

	secsD, _ := time.ParseDuration(secs + "s")
	ctx, cancel := context.WithTimeout(context.Background(), secsD+3*time.Second)
	defer cancel()
	var sent int
	err = s.Pump(ctx, in, func(d []byte) {
		if _, werr := out.Write(d); werr == nil {
			sent++
		}
	})
	if err != nil {
		t.Fatalf("Pump: %v", err)
	}
	t.Logf("harness sent %d datagrams, dropped %d captions", sent, s.Dropped())
}
