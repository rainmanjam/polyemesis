//go:build ignore

// Does a platform's RTMP ingest accept a SECOND audio track?
//
// Run:
//
//	go run scripts/probe_platform_ertmp_multitrack.go -selftest
//	TWITCH_INGEST_URL=rtmp://... TWITCH_STREAM_KEY=live_... \
//	  go run scripts/probe_platform_ertmp_multitrack.go -platform twitch
//	... -platform twitch -hls-url https://.../index.m3u8   # check playback too
//
// #141 asks a question nobody in this repo can answer from a document: Twitch
// publishes no statement about Enhanced RTMP multitrack audio that survives
// reading, and the only honest way to find out is to send one and watch. So
// this is a HARNESS, not an answer. It prints a verdict from what it measured
// and nothing else, and the issue closes when somebody with real credentials
// runs it and attaches the output.
//
// WHAT IT DOES
//
//	1. Preflight: ffmpeg >= 7.1, because E-RTMP multitrack muxing lands there.
//	   scripts/verify_ertmp_multitrack.go established that; below 7.1 -- which
//	   includes the stock build on Ubuntu 24.04 -- there is nothing to send and
//	   the only correct verdict is INCONCLUSIVE.
//	2. Starts an RTMP listener on loopback and has FFmpeg publish testsrc2 plus
//	   two AAC sine tracks (440 Hz and 880 Hz) into it.
//	3. Reads that session message by message and FORWARDS each message verbatim
//	   to the platform over a gortmplib publish connection, the same shape
//	   internal/rtmpserver's pump uses.
//	4. Counts the AudioExMultitrack messages actually forwarded, and watches for
//	   the platform hanging up.
//	5. With -hls-url, probes the platform's own playback and identifies the
//	   tracks by tone, which is the only step that can distinguish "accepted"
//	   from "accepted and silently discarded".
//
// WHY FFMPEG DOES NOT PUBLISH TO THE PLATFORM DIRECTLY
//
// The stream key would be an argv element, visible in `ps` to every user on the
// box for the whole run. Forwarding through this process keeps the key in
// memory: it is read from the environment and joined onto the ingest URL here,
// and every log line and error is redacted. That is the same rule the rest of
// the repo follows -- credentials come from the environment, never from argv
// and never from a committed file -- and it is why the extra hop exists.
//
// THERE ARE NO DEFAULT INGEST URLS. Every platform's ingest hostname must be
// supplied through <PLATFORM>_INGEST_URL. Hardcoding one would be asserting a
// fact I could not check, and a probe that quietly dials the wrong host reports
// REJECTED about a hostname rather than about multitrack audio -- which is a
// worse outcome than refusing to start.
//
// -selftest replaces the platform with a second loopback listener. It proves
// the harness itself works -- listener, publisher, forwarder, counter -- without
// any credentials and without touching a platform, and it is what you should
// run first when a real run says INCONCLUSIVE.
package main

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"net"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/bluenviron/gortmplib"
	"github.com/bluenviron/gortmplib/pkg/message"
)

// The two published tones. Far enough apart that a Goertzel bin cannot mistake
// one for its neighbour, and the same detector scripts/verify_ertmp_multitrack.go
// uses.
const (
	toneA = 440
	toneB = 880
	// sr is the rate the detector decodes at, not the rate anything is
	// published at.
	sr = 8000
)

// Verdicts. Printed verbatim on the last line so a CI job or a person pasting
// into the issue can grep for one token. NOTHING here is inferred: each one is
// reachable only from a measurement described beside it.
const (
	// Both tones were found in the platform's own playback. This is the only
	// verdict that answers #141 in the affirmative.
	verdictAccepted2 = "ACCEPTED_2_TRACKS"
	// The platform held the connection for the whole run and did not hang up,
	// and no downstream check was possible. It says NOTHING about whether the
	// second track survived: an ingest that discards track 2 and one that keeps
	// it look identical from this side.
	verdictIngestOnly = "ACCEPTED_INGEST_ONLY"
	// Playback was checked and carries one audio track, or two tracks carrying
	// the same tone.
	verdictDropped = "DROPPED_TRACK2"
	// The platform refused the connection or closed it during the run.
	verdictRejected = "REJECTED"
	// No conclusion is permitted. Missing environment, ffmpeg too old, a local
	// failure, or -- the important one -- zero multitrack messages generated
	// locally, in which case nothing about the platform was tested at all.
	verdictInconclusive = "INCONCLUSIVE"
)

func main() {
	platform := flag.String("platform", "", "twitch, youtube or kick; selects the env var pair")
	hlsURL := flag.String("hls-url", "", "playback URL to probe downstream (optional but the only way to reach ACCEPTED_2_TRACKS)")
	seconds := flag.Int("duration", 60, "how long to publish, in seconds")
	selftest := flag.Bool("selftest", false, "forward to a second loopback listener instead of a platform")
	flag.Parse()

	if err := preflightFFmpeg(); err != nil {
		finish(verdictInconclusive, err.Error())
	}

	var target *url.URL
	var redact *regexp.Regexp
	if *selftest {
		fmt.Println("SELF TEST: forwarding to a loopback listener, not to a platform.")
	} else {
		var err error
		target, redact, err = platformTarget(*platform)
		if err != nil {
			finish(verdictInconclusive, err.Error())
		}
		fmt.Printf("target: %s (key redacted)\n", redactURL(target))
	}

	// run RETURNS its verdict rather than exiting from inside. os.Exit skips
	// deferred calls, and the first self-test run of this harness proved what
	// that costs: the FFmpeg child was orphaned and kept publishing after the
	// harness had printed its answer and gone. Every cleanup here matters --
	// a live child, a held port, and the self-test sink's own count, which was
	// printed by a defer that never ran.
	verdict, why := run(target, redact, *hlsURL, time.Duration(*seconds)*time.Second, *selftest)
	finish(verdict, why)
}

// ------------------------------------------------------------------ preflight

// preflightFFmpeg refuses to proceed below 7.1.
//
// Not politeness. Below 7.1 FFmpeg writes a legacy single-track FLV whatever it
// is asked for, so the harness would publish ONE track, the platform would
// accept it, and the run would report a happy result about a question it never
// asked. Refusing is the only outcome that cannot mislead.
func preflightFFmpeg() error {
	bin, err := exec.LookPath("ffmpeg")
	if err != nil {
		return errors.New("ffmpeg is not installed")
	}
	out, err := exec.Command(bin, "-hide_banner", "-version").Output()
	if err != nil {
		return fmt.Errorf("ffmpeg -version failed: %v", err)
	}
	m := regexp.MustCompile(`ffmpeg version n?(\d+)\.(\d+)`).FindStringSubmatch(string(out))
	if m == nil {
		return fmt.Errorf("cannot read the ffmpeg version from %q; refusing to guess",
			firstLine(string(out)))
	}
	major, _ := strconv.Atoi(m[1])
	minor, _ := strconv.Atoi(m[2])
	if major < 7 || (major == 7 && minor < 1) {
		return fmt.Errorf("ffmpeg %d.%d cannot mux E-RTMP multitrack audio (7.1 is "+
			"the first that can), so this run would publish one track and report "+
			"a result about a question it never asked", major, minor)
	}
	fmt.Printf("ffmpeg %d.%d: can mux E-RTMP multitrack\n", major, minor)
	return nil
}

// ---------------------------------------------------------------- credentials

// platformTarget builds the publish URL in memory from the environment.
//
// The key never becomes an argv element and never reaches a file. The returned
// regexp matches it so every log line and every error can be redacted -- a probe
// that prints a live stream key into a terminal, a CI log or an issue comment
// has leaked it, and the whole point of forwarding through this process was to
// stop that happening.
func platformTarget(platform string) (*url.URL, *regexp.Regexp, error) {
	p := strings.ToUpper(strings.TrimSpace(platform))
	switch p {
	case "TWITCH", "YOUTUBE", "KICK":
	case "":
		return nil, nil, errors.New("-platform is required (twitch, youtube or kick), " +
			"or -selftest to exercise the harness without one")
	default:
		return nil, nil, fmt.Errorf("unknown platform %q (twitch, youtube, kick)", platform)
	}

	raw := strings.TrimSpace(os.Getenv(p + "_INGEST_URL"))
	key := strings.TrimSpace(os.Getenv(p + "_STREAM_KEY"))
	if raw == "" {
		return nil, nil, fmt.Errorf("%s_INGEST_URL is not set. There is deliberately no "+
			"built-in default: a hardcoded ingest hostname I could not verify would turn "+
			"a wrong-host failure into a verdict about multitrack audio", p)
	}
	if key == "" {
		return nil, nil, fmt.Errorf("%s_STREAM_KEY is not set. It is read from the "+
			"environment only -- never a flag, never a file", p)
	}

	u, err := url.Parse(raw)
	if err != nil {
		return nil, nil, fmt.Errorf("malformed %s_INGEST_URL: %v", p, err)
	}
	if u.Scheme != "rtmp" && u.Scheme != "rtmps" {
		return nil, nil, fmt.Errorf("%s_INGEST_URL must be rtmp:// or rtmps:// (got %q)", p, u.Scheme)
	}
	// Refused rather than stripped. Any of these means the operator pasted
	// something other than the ingest endpoint -- most likely a URL that already
	// contains a key -- and silently rewriting it would publish somewhere they
	// did not choose.
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, nil, fmt.Errorf("%s_INGEST_URL must carry no credentials, query or "+
			"fragment: give the bare ingest endpoint and let %s_STREAM_KEY supply the key", p, p)
	}

	u.Path = strings.TrimRight(u.Path, "/") + "/" + key
	return u, regexp.MustCompile(regexp.QuoteMeta(key)), nil
}

// redactURL renders the target with the key replaced. The key is always the
// final path element, because platformTarget is the only thing that ever
// appends one.
//
// Assembled by hand rather than through url.URL.String(), which percent-encodes
// the placeholder into rtmp://host/app/%3CKEY%3E -- unreadable, and unreadable
// in the one line an operator uses to check they are pointed at the right
// ingest.
func redactURL(u *url.URL) string {
	p := u.Path
	if i := strings.LastIndex(p, "/"); i >= 0 {
		p = p[:i+1]
	}
	return u.Scheme + "://" + u.Host + p + "<KEY>"
}

// scrub removes the stream key from anything about to be printed.
func scrub(s string, redact *regexp.Regexp) string {
	if redact == nil {
		return s
	}
	return redact.ReplaceAllString(s, "<KEY>")
}

// --------------------------------------------------------------------- the run

type counts struct {
	multitrackSetup int
	multitrackMedia int
	trackIDs        map[uint8]int
	legacyAudio     int
	video           int
	other           int
}

// audioTracks is how many distinct audio tracks were forwarded.
//
// IT IS NOT len(trackIDs), AND THE FIRST SELF-TEST RUN PROVED IT. Publishing
// two AAC tracks, this harness forwarded 470 AudioExMultitrack messages carrying
// exactly ONE track ID, plus 471 ordinary audio messages -- and concluded, on a
// perfectly good two-track stream, that a second track had never left the
// machine.
//
// The reason is written down in scripts/verify_ertmp_multitrack.go and I had to
// rediscover it: with AAC, track 0 keeps a LEGACY FLV tag and only the
// additional tracks are wrapped in AudioExMultitrack. (With Opus there is no
// legacy SoundFormat, so track 0 is wrapped too -- which is why this counts both
// shapes rather than assuming either.) A guard built on the multitrack count
// alone would have reported INCONCLUSIVE on every AAC run for ever, which is the
// most expensive kind of wrong: it looks like caution.
func (c counts) audioTracks() int {
	n := len(c.trackIDs)
	// Only when track 0 was NOT also seen wrapped, or the Opus shape would be
	// counted twice.
	if _, wrapped := c.trackIDs[0]; c.legacyAudio > 0 && !wrapped {
		n++
	}
	return n
}

func run(target *url.URL, redact *regexp.Regexp, hlsURL string, dur time.Duration, selftest bool) (string, string) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return verdictInconclusive, "cannot listen on loopback: " + err.Error()
	}
	defer func() { _ = ln.Close() }()
	local := fmt.Sprintf("rtmp://%s/live/probe", ln.Addr().String())

	// The far end. In self-test it is a second loopback listener that counts
	// what reaches it; otherwise it is the platform.
	var sink sinkConn
	if selftest {
		sink, err = newLoopbackSink()
	} else {
		sink, err = dialPlatform(target)
	}
	if err != nil {
		if selftest {
			return verdictInconclusive, "self-test sink: " + err.Error()
		}
		// A refusal at connect time is a real answer about the platform and the
		// only one that needs no multitrack message to be meaningful -- but ONLY
		// if the platform is what refused. A name that does not resolve, or a
		// host that never answers, is this machine's network and says nothing
		// about multitrack audio. Reported separately, because a probe that
		// answers REJECTED to a typo in a hostname would have closed #141 with
		// a fabricated result.
		return classifyDialError(err, redact)
	}
	defer sink.Close()

	pub, err := publishMultitrack(local, dur)
	if err != nil {
		return verdictInconclusive, "cannot start ffmpeg: " + err.Error()
	}
	defer func() {
		if pub.Process != nil {
			_ = pub.Process.Kill()
		}
		_ = pub.Wait()
	}()

	_ = ln.(*net.TCPListener).SetDeadline(time.Now().Add(20 * time.Second))
	conn, err := ln.Accept()
	if err != nil {
		return verdictInconclusive, "FFmpeg never connected to the local listener: " + err.Error()
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Time{})

	sc := &gortmplib.ServerConn{RW: conn}
	if err := sc.Initialize(); err != nil {
		return verdictInconclusive, "local RTMP handshake failed: " + err.Error()
	}
	if err := sc.Accept(); err != nil {
		return verdictInconclusive, "local RTMP accept failed: " + err.Error()
	}

	c := counts{trackIDs: map[uint8]int{}}
	deadline := time.Now().Add(dur + 15*time.Second)
	var sinkErr error

	for time.Now().Before(deadline) {
		msg, err := sc.Read()
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
				break
			}
			fmt.Println("local read ended:", err)
			break
		}
		tally(&c, msg)
		// Verbatim, exactly as rtmpserver's pump forwards to a subscriber.
		// Nothing here decodes a frame or rewrites a header: the question is
		// what the platform does with what FFmpeg produced, so anything this
		// process changed would be a different question.
		if err := sink.Write(msg); err != nil {
			sinkErr = err
			break
		}
	}

	report(&c)

	// A platform that hung up mid-run has answered. Distinguish it from the
	// local publisher simply finishing.
	if sinkErr != nil {
		switch {
		case selftest:
			return verdictInconclusive, "the self-test sink went away: " + sinkErr.Error()
		case c.audioTracks() < 2:
			return verdictInconclusive, "the platform closed the connection before a " +
				"second audio track was forwarded, so nothing was tested: " +
				scrub(sinkErr.Error(), redact)
		default:
			return verdictRejected, fmt.Sprintf("the platform closed the connection after "+
				"%d audio tracks had been forwarded: %s",
				c.audioTracks(), scrub(sinkErr.Error(), redact))
		}
	}

	// THE GUARD THAT MAKES EVERY VERDICT BELOW HONEST. If fewer than two audio
	// tracks left this machine, the platform was sent an ordinary stream and
	// accepting it says nothing whatsoever about #141.
	if n := c.audioTracks(); n < 2 {
		return verdictInconclusive, fmt.Sprintf("only %d audio track(s) reached the "+
			"forwarder, so the platform was never asked the question. Run -selftest "+
			"to find out why before believing anything about the platform.", n)
	}

	if selftest {
		fmt.Printf("\nself test: the harness published, forwarded and counted %d audio "+
			"tracks (%d multitrack media messages).\n", c.audioTracks(), c.multitrackMedia)
		return verdictInconclusive, "self test only: no platform was contacted, so there " +
			"is no platform verdict to give."
	}

	if hlsURL == "" {
		return verdictIngestOnly, "the platform held the connection for the whole run. " +
			"THIS IS NOT AN ANSWER ABOUT PLAYBACK: an ingest that discards the second " +
			"track and one that keeps it are identical from this side. Re-run with " +
			"-hls-url to find out which."
	}
	return checkPlayback(hlsURL)
}

// ------------------------------------------------------------------ the sinks

type sinkConn interface {
	Write(message.Message) error
	Close()
}

type clientSink struct{ c *gortmplib.Client }

func (s clientSink) Write(m message.Message) error { return s.c.Write(m) }
func (s clientSink) Close()                        { s.c.Close() }

// classifyDialError separates "the platform said no" from "this machine could
// not reach it".
//
// Only the first is a verdict about #141. DNS failure, a timeout and an
// unreachable network are all facts about the box the probe ran on, and the
// issue must not close on any of them. ECONNREFUSED is kept as REJECTED: a host
// that answered a SYN with an RST is a peer making a decision, which is the
// weakest thing that still counts as one.
func classifyDialError(err error, redact *regexp.Regexp) (string, string) {
	msg := scrub(err.Error(), redact)

	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return verdictInconclusive, "the ingest hostname did not resolve, so nothing " +
			"was asked of any platform: " + msg
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return verdictInconclusive, "the connection attempt timed out; that is this " +
			"machine's network, not a platform decision: " + msg
	}
	if errors.Is(err, syscall.ECONNREFUSED) {
		return verdictRejected, "the platform refused the connection: " + msg
	}
	if errors.Is(err, syscall.EHOSTUNREACH) || errors.Is(err, syscall.ENETUNREACH) {
		return verdictInconclusive, "the ingest host was unreachable from this machine: " + msg
	}
	// Everything after a successful TCP connect -- a TLS failure, a rejected
	// RTMP handshake, a refused publish -- is the platform declining.
	return verdictRejected, "the platform refused the connection: " + msg
}

func dialPlatform(u *url.URL) (sinkConn, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c := &gortmplib.Client{
		URL:     u,
		Publish: true,
		// Only consulted for rtmps. Left at the default verification, because a
		// probe that skipped certificate checks would be measuring a different
		// connection than the product makes.
		TLSConfig: &tls.Config{MinVersion: tls.VersionTLS12},
	}
	if err := c.Initialize(ctx); err != nil {
		return nil, err
	}
	return clientSink{c}, nil
}

// loopbackSink is the -selftest far end: a second RTMP listener in this same
// process, dialled by a gortmplib publish client exactly as the platform would
// be. It exercises the identical forwarding path, so a green self test means
// the harness works and a red one means the harness is broken rather than the
// platform.
type loopbackSink struct {
	ln   net.Listener
	c    *gortmplib.Client
	done chan struct{}
	mu   sync.Mutex
	got  int
}

func newLoopbackSink() (sinkConn, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	s := &loopbackSink{ln: ln, done: make(chan struct{})}
	go func() {
		defer close(s.done)
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		sc := &gortmplib.ServerConn{RW: conn}
		if sc.Initialize() != nil || sc.Accept() != nil {
			return
		}
		for {
			if _, err := sc.Read(); err != nil {
				return
			}
			s.mu.Lock()
			s.got++
			s.mu.Unlock()
		}
	}()

	u, _ := url.Parse(fmt.Sprintf("rtmp://%s/live/sink", ln.Addr().String()))
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	c := &gortmplib.Client{URL: u, Publish: true}
	if err := c.Initialize(ctx); err != nil {
		_ = ln.Close()
		return nil, err
	}
	s.c = c
	return s, nil
}

func (s *loopbackSink) Write(m message.Message) error { return s.c.Write(m) }

func (s *loopbackSink) Close() {
	s.c.Close()
	_ = s.ln.Close()
	<-s.done
	s.mu.Lock()
	fmt.Printf("self-test sink received %d messages\n", s.got)
	s.mu.Unlock()
}

// ---------------------------------------------------------------- the publisher

// publishMultitrack starts FFmpeg publishing video plus two distinct tones as
// E-RTMP multitrack, to the LOOPBACK listener only. No credential is anywhere
// in this argv, by construction: the only URL it is given is 127.0.0.1.
func publishMultitrack(local string, dur time.Duration) (*exec.Cmd, error) {
	secs := strconv.Itoa(int(dur.Seconds()) + 2)
	argv := []string{"-hide_banner", "-loglevel", "warning", "-re",
		"-f", "lavfi", "-i", "testsrc2=size=640x360:rate=30:duration=" + secs,
		"-f", "lavfi", "-i", fmt.Sprintf("sine=frequency=%d:duration=%s:sample_rate=48000", toneA, secs),
		"-f", "lavfi", "-i", fmt.Sprintf("sine=frequency=%d:duration=%s:sample_rate=48000", toneB, secs),
		"-map", "0:v", "-map", "1:a", "-map", "2:a",
		"-c:v", "libx264", "-preset", "veryfast", "-tune", "zerolatency", "-g", "60",
		"-c:a", "aac", "-b:a", "128k", "-ar", "48000",
		"-f", "flv", local}
	cmd := exec.Command("ffmpeg", argv...)
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return cmd, nil
}

// ------------------------------------------------------------------ accounting

func tally(c *counts, msg message.Message) {
	switch m := msg.(type) {
	case *message.AudioExMultitrack:
		c.trackIDs[m.TrackID]++
		switch m.Wrapped.(type) {
		case *message.AudioExSequenceStart, *message.AudioExMultichannelConfig:
			c.multitrackSetup++
		default:
			c.multitrackMedia++
		}
	case *message.Audio, *message.AudioExCodedFrames, *message.AudioExSequenceStart:
		c.legacyAudio++
	case *message.Video, *message.VideoExCodedFrames, *message.VideoExFramesX,
		*message.VideoExSequenceStart:
		c.video++
	default:
		c.other++
	}
}

func report(c *counts) {
	fmt.Println("\n--- what was forwarded ---")
	fmt.Printf("  audio tracks forwarded    : %d\n", c.audioTracks())
	fmt.Printf("  multitrack setup messages : %d\n", c.multitrackSetup)
	fmt.Printf("  multitrack media messages : %d\n", c.multitrackMedia)
	fmt.Printf("  distinct multitrack IDs   : %d %v\n", len(c.trackIDs), c.trackIDs)
	fmt.Printf("  legacy-tagged audio       : %d (track 0, with AAC — see audioTracks)\n", c.legacyAudio)
	fmt.Printf("  video                     : %d\n", c.video)
	fmt.Printf("  everything else           : %d\n", c.other)
}

// ------------------------------------------------------------------- playback

// checkPlayback is the only step that can tell "accepted" from "accepted and
// silently discarded". Everything before it is about what left this machine.
func checkPlayback(hlsURL string) (string, string) {
	out, err := exec.Command("ffprobe", "-hide_banner", "-loglevel", "error",
		"-select_streams", "a", "-show_entries", "stream=index",
		"-of", "csv=p=0", "-i", hlsURL).Output()
	if err != nil {
		return verdictInconclusive, "could not probe the playback URL, so nothing is " +
			"known about what the platform delivers: " + err.Error()
	}
	seen := map[string]bool{}
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			seen[l] = true
		}
	}
	fmt.Printf("playback carries %d audio track(s)\n", len(seen))
	if len(seen) < 2 {
		return verdictDropped, "the platform delivers fewer than two audio tracks"
	}

	// Two tracks is not two DIFFERENT tracks. Identify them by content, for the
	// same reason verify_ertmp_multitrack.go does: a duplicated track and a
	// preserved one look identical to a stream count.
	tones := make([]int, 0, len(seen))
	for i := range len(seen) {
		tones = append(tones, toneOf(hlsURL, i))
	}
	fmt.Println("playback tones:", tones)
	if !contains(tones, toneA) || !contains(tones, toneB) {
		return verdictDropped, fmt.Sprintf("playback carries %d tracks but not both "+
			"published tones (%d and %d); got %v", len(seen), toneA, toneB, tones)
	}
	return verdictAccepted2, fmt.Sprintf("both published tones (%d Hz and %d Hz) came "+
		"back out of the platform's own playback", toneA, toneB)
}

// toneOf decodes a second of one playback track and reports which published
// tone it carries. A Goertzel filter rather than an FFT, so there is no numeric
// dependency -- the same choice, and the same reasoning, as
// scripts/verify_ertmp_multitrack.go.
func toneOf(path string, stream int) int {
	raw, err := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-i", path,
		"-map", fmt.Sprintf("0:a:%d", stream), "-t", "1", "-f", "s16le", "-ac", "1",
		"-ar", strconv.Itoa(sr), "-").Output()
	if err != nil || len(raw) < sr {
		return 0
	}
	n := len(raw) / 2
	s := make([]int16, n)
	for i := range n {
		s[i] = int16(binary.LittleEndian.Uint16(raw[i*2:]))
	}
	best, bestPower := 0, math.Inf(-1)
	for _, f := range []int{toneA, toneB} {
		if p := goertzel(s, f); p > bestPower {
			best, bestPower = f, p
		}
	}
	return best
}

func goertzel(samples []int16, freq int) float64 {
	w := 2 * math.Pi * float64(freq) / float64(sr)
	coeff := 2 * math.Cos(w)
	var s1, s2 float64
	for _, x := range samples {
		s0 := float64(x) + coeff*s1 - s2
		s2, s1 = s1, s0
	}
	return s1*s1 + s2*s2 - coeff*s1*s2
}

func contains(xs []int, v int) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------- output

// finish prints the machine-parseable verdict and exits. One exit point, so
// there is no path that ends without saying what was concluded and why.
func finish(verdict, why string) {
	fmt.Printf("\nVERDICT: %s\n%s\n", verdict, why)
	if verdict == verdictAccepted2 || verdict == verdictIngestOnly {
		os.Exit(0)
	}
	os.Exit(1)
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
