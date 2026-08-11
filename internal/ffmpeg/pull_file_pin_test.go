package ffmpeg

import (
	"bytes"
	"context"
	"net"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// #201's pin: the engine's file-input path carries -protocol_whitelist file.
//
// TWO THINGS HAVE TO BE TRUE AT ONCE and each has its own test below, because a
// single one of them is satisfiable by a change that breaks the other.
//
//  1. The flag is there, with the value "file", before -i. After -i it applies
//     to nothing, which is the same trap pullInputArgs' own comment names.
//  2. The ingest STILL WORKS with it on. This flag bounds what the input
//     demuxer may open, and the ingest's output is a UDP relay URL; a pin that
//     refused the input, or the output, would turn a hardening change into a
//     dead stream. That is measured against a real FFmpeg rather than asserted,
//     because the question is about FFmpeg's behaviour and not about ours.
//
// WHAT NEITHER TEST CLAIMS. This is a pin, not a fix, and pretending otherwise
// here would be the vacuous guard all over again. Measured with FFmpeg 8.1.2: an
// ffconcat script naming "http://..." is refused IDENTICALLY with and without
// the flag (concat's safe=1 default), and a script naming a SIBLING file is
// still resolved WITH the flag on. The substitution hole is closed by the format
// allowlist, which is not a flag and does not live here -- see pullInputArgs.

func TestFilePullPinsTheFileProtocolBeforeTheInput(t *testing.T) {
	args := IngestArgs(IngestSpec{
		Kind:        IngestPull,
		PullURL:     "file://uploads/show.ts",
		PullDataDir: "/srv/data",
		RelayURL:    "udp://127.0.0.1:20000",
	})

	i := indexOf(args, "-protocol_whitelist")
	if i < 0 {
		t.Fatalf("a file:// pull carries no -protocol_whitelist pin, so what the input "+
			"demuxer may open is whatever this FFmpeg build enables: %s", join(args))
	}
	if i+1 >= len(args) || args[i+1] != "file" {
		t.Fatalf("-protocol_whitelist must be pinned to \"file\" on a file pull: %s", join(args))
	}
	if ii := indexOf(args, "-i"); ii < 0 || i > ii {
		t.Fatalf("-protocol_whitelist must precede -i or FFmpeg applies it to nothing: %s", join(args))
	}
}

func TestFilePullReachesTheRelayWithTheProtocolPinOn(t *testing.T) {
	ffmpegBin := needFFmpeg(t, "ffmpeg")[0]

	// t.TempDir before anything opens a handle under it: on Windows the cleanup
	// cannot remove a directory something still has open, and the child process
	// below holds the sample for as long as it runs.
	dir := t.TempDir()
	buildSample(t, filepath.Join(dir, "sample.ts"), "-t", "1")

	// The relay socket is bound HERE, and the port comes out of the bind rather
	// than out of a guess: a hard-coded port is a test that fails on whichever
	// machine is already using it.
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("cannot bind a loopback relay socket: %v", err)
	}
	defer conn.Close()

	args := IngestArgs(IngestSpec{
		Kind: IngestPull,
		// Relative, as a Library pull URL is. PullDataDir is the confinement
		// root the engine passes.
		PullURL:     "file://sample.ts",
		PullDataDir: dir,
		RelayURL:    "udp://" + conn.LocalAddr().String(),
	})

	// Stderr goes to a BUFFER, and this is the second attempt at it.
	//
	// It was a file under dir, for a reason that reads well and is wrong: that
	// os/exec copies into cmd.Stderr from its own goroutine, so reading the
	// buffer "before Wait returns" would be a data race. True -- but every read
	// site below calls stop() first, and stop() is `cancel(); <-done` where done
	// closes after cmd.Wait returns. Wait returning is precisely the
	// happens-before that makes the buffer safe to read; it is the guarantee
	// os/exec documents. So the race the file avoided was never reachable.
	//
	// The file, meanwhile, broke windows-latest twice:
	//
	//	TempDir RemoveAll cleanup: unlinkat ...\ffmpeg.stderr:
	//	The process cannot access the file because it is being used by another
	//	process.
	//
	// FFmpeg was already reaped both times. A retrying RemoveAll registered
	// ahead of t.TempDir's own cleanup did not help either, which rules out the
	// brief on-access-scanner window uploads.renameStaged retries for and says
	// the handle outlives the process by longer than a test should wait.
	//
	// A buffer has no handle for anything to hold. That is the whole fix: not a
	// longer wait for the file to be released, but no file.
	var stderr bytes.Buffer

	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, ffmpegBin, args...)
	cmd.Stderr = &stderr
	// -stream_loop -1 means this never ends on its own. WaitDelay bounds the
	// gap between the kill and the pipes closing, the same reason ProbeFile
	// sets one.
	cmd.WaitDelay = 5 * time.Second
	if err := cmd.Start(); err != nil {
		cancel()
		t.Fatalf("cannot start FFmpeg with the ingest argv: %v\nargs: %s", err, join(args))
	}
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	var once sync.Once
	stop := func() { once.Do(func() { cancel(); <-done }) }
	defer stop()

	// Generous on purpose. This bound exists so a broken argv fails instead of
	// hanging; it is not a performance assertion, and a slow CI box reading a
	// one-second sample is not a signal about anything.
	if err := conn.SetReadDeadline(time.Now().Add(45 * time.Second)); err != nil {
		t.Fatalf("cannot set a read deadline: %v", err)
	}
	buf := make([]byte, 2048)
	n, _, readErr := conn.ReadFrom(buf)
	if readErr != nil {
		stop()
		said := stderr.String()
		t.Fatalf("no bytes reached the relay from a file:// pull: %v\n"+
			"the -protocol_whitelist pin on the input must not refuse the source or the relay output\n"+
			"args: %s\nffmpeg said: %s", readErr, join(args), said)
	}
	// 0x47 is the MPEG-TS sync byte. Asserting only "a datagram arrived" would
	// pass on any noise that happened to land on the port.
	if n == 0 || buf[0] != 0x47 {
		stop()
		said := stderr.String()
		t.Fatalf("the relay received %d bytes whose first byte is %#x, not an MPEG-TS packet\n"+
			"args: %s\nffmpeg said: %s", n, buf[0], join(args), said)
	}
}
