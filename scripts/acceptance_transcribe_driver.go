//go:build ignore

// Driver for scripts/acceptance-transcribe.sh.
//
// internal/transcribe is not a network service, and the live-coverage note that
// ranked it third ("7,661 lines, 2 external hosts") counted its hosts by
// grepping for URLs. One of those two is a github.com link inside an error
// message and is never fetched. The other is real and load-bearing:
//
//	https://huggingface.co/ggerganov/whisper.cpp/resolve/main/
//
// Everything else in the package is local -- a whisper.cpp binary the operator
// installs, and an ffmpeg command line. So the untested risk here is not a
// protocol handshake. It is TEN HARDCODED CLAIMS ABOUT A REMOTE SERVER, plus
// two argument builders that are pure functions verified against strings we
// imagined rather than against the programs that have to accept them.
//
// WHY THE CATALOGUE IS THE DANGEROUS PART. models.go states, for each of ten
// models, a filename (from which the URL is composed) and a byte count. Nothing
// checks either against the host. Both fail silently and in opposite
// directions:
//
//   - A name upstream has renamed or withdrawn composes a URL that 404s. The
//     model picker offers a download that cannot succeed, and the only person
//     who finds out is a user who clicked it.
//   - A byte count that has gone stale is worse, because VerifyModelFile gates
//     on it: a file more than a tenth away from the published size is rejected
//     as "most likely an interrupted download". A re-upload upstream therefore
//     makes polyemesis reject a PERFECTLY GOOD model and blame the network.
//     download.go's own comment names this as the failure it is afraid of --
//     "a check that is wrong in the restrictive direction, which this codebase
//     has been bitten by before" -- and then leaves Bytes as exactly that kind
//     of check, one that no test can refute offline.
//
// WHY THE MAGIC BYTES ARE CHECKED OVER THE WIRE. download.go's first integrity
// check exists because "a proxy or a login wall serving an HTML error page with
// a 200" is the most common way this breaks. Asserting that the first four
// bytes of each catalogue object really are the ggml magic proves the URL
// resolves to a MODEL, not merely to a 200.
//
// NO CREDENTIALS ANYWHERE IN THIS FILE. The Hugging Face repo is public and
// every request here is anonymous. There is nothing to leak and nothing to put
// in argv.
//
//	go run scripts/acceptance_transcribe_driver.go <cmd>
package main

import (
	"context"
	"encoding/binary"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/rainmanjam/polyemesis/internal/transcribe"
)

func main() {
	if len(os.Args) < 2 {
		fail("usage: acceptance_transcribe_driver.go <cmd>")
	}
	switch os.Args[1] {
	case "catalogue":
		catalogue()
	case "refusal":
		refusal()
	case "tracks":
		tracks()
	case "whisper":
		whisperBuild()
	case "download":
		download()
	case "endtoend":
		endToEnd()
	default:
		fail("unknown command %q", os.Args[1])
	}
}

func fail(f string, a ...any) {
	fmt.Printf("ERR "+f+"\n", a...)
	os.Exit(1)
}

// emit prints one key=value result line. The suite greps these, so a command
// that cannot answer prints nothing rather than a plausible zero.
func emit(k string, v any) { fmt.Printf("%s=%v\n", k, v) }

// ---------------------------------------------------------------------------
// The model host.

// ggmlMagicWire is the four bytes a real whisper.cpp model starts with, taken
// from the format rather than from our own source: GGML_FILE_MAGIC is the
// uint32 0x67676d6c and the converter fwrites it, so disk carries its
// little-endian spelling, 6c 6d 67 67 -- "lmgg", not "ggml".
//
// DELIBERATELY RESTATED rather than imported. download.go's copy is unexported,
// but the real reason is that the question here is about the REMOTE object, and
// a check that fetched our own constant and compared it to itself would pass
// whatever the server sent. That independence is not theoretical: this check
// is what caught download.go's ggmlMagic being byte-reversed, a defect the
// package's own tests could not see because they built their fixtures by
// copying the same wrong constant.
var ggmlMagicWire = []byte{0x6c, 0x6d, 0x67, 0x67}

// sha256ETagRE restates the rule download.go's checksumFromHeaders applies.
//
// Restated for the same reason: the thing under test is whether the real server
// still satisfies that rule. Hugging Face serves LFS objects with the object's
// SHA-256 as a bare hex ETag, which is the ONLY reason Download can enforce a
// real end-to-end checksum -- with no such header it falls back to "length" and
// says so in DownloadResult.Verified, quietly, where nobody looks.
var sha256ETagRE = regexp.MustCompile(`^"?([0-9a-fA-F]{64})"?$`)

// contentRangeRE pulls the object's true total size out of a 206 response.
var contentRangeRE = regexp.MustCompile(`/(\d+)\s*$`)

// catalogue probes every model in the shipped catalogue.
//
// A four-byte ranged GET per model rather than a HEAD, because a HEAD is
// answered by the redirect at huggingface.co while Download follows that
// redirect to the CDN -- and the two do NOT carry the same headers. The CDN
// drops X-Linked-Etag and puts the SHA-256 in a plain Etag instead, so the
// two-key fallback list in checksumFromHeaders is what makes the checksum
// enforceable in production. Probing the redirect alone would test a response
// no download ever reads.
func catalogue() {
	models := transcribe.Models()
	emit("models", len(models))

	client := &http.Client{Timeout: 30 * time.Second}
	var resolved, magicOK, sha256OK, inBand, exact int
	var firstBad string
	note := func(s string) {
		if firstBad == "" {
			firstBad = s
		}
	}

	for _, m := range models {
		req, err := http.NewRequest(http.MethodGet, m.URL(), nil)
		if err != nil {
			note(m.Name + ": " + err.Error())
			continue
		}
		req.Header.Set("Range", "bytes=0-3")
		resp, err := client.Do(req)
		if err != nil {
			note(m.Name + ": " + err.Error())
			continue
		}
		var head [4]byte
		n, _ := readFull(resp.Body, head[:])
		resp.Body.Close()

		if resp.StatusCode != http.StatusPartialContent && resp.StatusCode != http.StatusOK {
			note(fmt.Sprintf("%s: server said %s", m.Name, resp.Status))
			continue
		}
		resolved++

		if n == 4 && string(head[:]) == string(ggmlMagicWire) {
			magicOK++
		} else {
			note(fmt.Sprintf("%s: served %q, not a ggml model", m.Name, head[:n]))
		}

		if sha256ETagRE.MatchString(strings.TrimSpace(resp.Header.Get("Etag"))) ||
			sha256ETagRE.MatchString(strings.TrimSpace(resp.Header.Get("X-Linked-Etag"))) {
			sha256OK++
		} else {
			note(fmt.Sprintf("%s: no sha256 etag; Download would fall back to a length check", m.Name))
		}

		// The size the server reports, from the Content-Range total. This is the
		// number VerifyModelFile's band is compared against on a real install.
		size := totalFromContentRange(resp.Header.Get("Content-Range"))
		if size <= 0 {
			note(m.Name + ": no Content-Range total to compare against")
			continue
		}
		// The SAME band VerifyModelFile enforces -- a tenth either way. Restated
		// here so this reports the outcome rather than the input.
		lo, hi := m.Bytes-m.Bytes/10, m.Bytes+m.Bytes/10
		if size >= lo && size <= hi {
			inBand++
		} else {
			note(fmt.Sprintf("%s: host serves %d bytes, catalogue says %d -- outside the band "+
				"VerifyModelFile accepts, so a good download would be rejected", m.Name, size, m.Bytes))
		}
		if size == m.Bytes {
			exact++
		}
	}

	emit("resolved", resolved)
	emit("magicOK", magicOK)
	emit("sha256OK", sha256OK)
	emit("inBand", inBand)
	emit("exact", exact)
	if firstBad != "" {
		emit("firstBad", firstBad)
	}
}

func totalFromContentRange(v string) int64 {
	m := contentRangeRE.FindStringSubmatch(strings.TrimSpace(v))
	if m == nil {
		return 0
	}
	n, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil {
		return 0
	}
	return n
}

func readFull(r interface{ Read([]byte) (int, error) }, buf []byte) (int, error) {
	var total int
	for total < len(buf) {
		n, err := r.Read(buf[total:])
		total += n
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

// ---------------------------------------------------------------------------
// A model the host does not have.

// refusal asks the real host for a model that does not exist there.
//
// This is the 404 arm of the catalogue risk, exercised deliberately instead of
// waited for. The failure it guards is specific and silent: Download writes to
// a .part file BEFORE it has verified anything, so an implementation that
// mishandled a non-200 would leave an error page on disk under the model's real
// name, and InstalledModels would offer it. A whisper model that is really an
// HTML page does not fail to load loudly -- download.go's opening comment is
// entirely about that class of failure.
func refusal() {
	dir, err := os.MkdirTemp("", "poly-transcribe-refusal")
	if err != nil {
		fail("mkdtemp: %v", err)
	}
	defer os.RemoveAll(dir)

	// A name the catalogue does not contain and the repo does not serve. Built
	// through the ordinary Model type so it composes its URL exactly as a real
	// entry would -- the point is that the URL is well formed and the OBJECT is
	// absent, not that the URL is malformed.
	ghost := transcribe.Model{Name: "polyemesis-no-such-model", Size: transcribe.SizeTiny, Bytes: 77_691_713}
	emit("url", ghost.URL())

	d := &transcribe.Downloader{Dir: dir}
	res, err := d.Download(context.Background(), ghost, nil)
	emit("refused", err != nil)
	if err != nil {
		emit("error", firstLine(err.Error()))
	} else {
		emit("acceptedPath", res.Path)
		emit("acceptedBytes", res.Bytes)
	}

	// Nothing may be left behind: not the final name, not a .part file.
	entries, rerr := os.ReadDir(dir)
	if rerr != nil {
		fail("readdir: %v", rerr)
	}
	var left []string
	for _, e := range entries {
		left = append(left, e.Name())
	}
	emit("filesLeft", len(left))
	if len(left) > 0 {
		emit("leftNames", strings.Join(left, ","))
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return s
}

// ---------------------------------------------------------------------------
// The right microphone out of the right track.

// tracks proves ExtractArgs pulls the track it was asked for.
//
// THE PACKAGE'S WHOLE DIFFERENTIATOR RESTS ON THIS. The doc comment says "the
// track index IS the speaker attribution", and ExtractArgs' own comment names
// the way that goes wrong:
//
//	the map is `0:a:N` -- the audio-stream-relative form -- not `0:N`, because
//	the absolute stream index counts the video track and every track would be
//	off by one, silently transcribing the wrong microphone.
//
// "Silently" is right, and it is why a unit test on the string is not enough: a
// transcript attributed to the wrong speaker is fluent, plausible and wrong,
// and no assertion downstream of it can tell. So this builds a recording whose
// tracks are TELLABLE APART -- a 440 Hz tone on track 0 and 880 Hz on track 1 --
// runs the real ExtractArgs through the real ffmpeg, and measures what came
// out. Under the absolute-index form both extractions would yield 440 Hz,
// because `0:1` is the first audio stream while `0:a:1` is the second.
//
// Deterministic by construction: lavfi sine sources, no fixture committed.
func tracks() {
	ff, err := exec.LookPath("ffmpeg")
	if err != nil {
		emit("ffmpeg", false)
		return
	}
	emit("ffmpeg", true)

	dir, err := os.MkdirTemp("", "poly-transcribe-tracks")
	if err != nil {
		fail("mkdtemp: %v", err)
	}
	defer os.RemoveAll(dir)

	mkv := filepath.Join(dir, "recording.mkv")
	if err := buildTwoTrackMKV(ff, mkv); err != nil {
		fail("build fixture: %v", err)
	}

	// The video stream is not decoration. Without it, absolute and
	// stream-relative indices agree and the check could not tell them apart.
	emit("videoStreams", countStreams(mkv, "v"))
	emit("audioStreams", countStreams(mkv, "a"))

	for _, tc := range []struct {
		track int
		want  float64
	}{{0, 440}, {1, 880}} {
		out := filepath.Join(dir, fmt.Sprintf("track%d.wav", tc.track))
		args := transcribe.ExtractArgs(transcribe.ExtractSpec{
			FFmpeg: ff, Input: mkv, Track: tc.track, Output: out,
		})
		if o, err := exec.Command(ff, args...).CombinedOutput(); err != nil {
			emit(fmt.Sprintf("track%dError", tc.track), firstLine(string(o)))
			continue
		}
		hz, samples, err := dominantHz(out, transcribe.WhisperSampleRate)
		if err != nil {
			emit(fmt.Sprintf("track%dError", tc.track), err.Error())
			continue
		}
		emit(fmt.Sprintf("track%dHz", tc.track), fmt.Sprintf("%.0f", hz))
		emit(fmt.Sprintf("track%dSamples", tc.track), samples)
		// A generous window: the measurement only has to distinguish 440 from
		// 880, and a tolerance tight enough to be fragile would buy nothing.
		emit(fmt.Sprintf("track%dCorrect", tc.track), hz > tc.want*0.9 && hz < tc.want*1.1)
	}
}

// buildTwoTrackMKV writes a recording shaped like the ones this package
// transcribes: a video stream and two separate microphone tracks.
func buildTwoTrackMKV(ff, path string) error {
	args := []string{
		"-nostdin", "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=size=320x240:rate=10:duration=3",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=16000:duration=3",
		"-f", "lavfi", "-i", "sine=frequency=880:sample_rate=16000:duration=3",
		"-map", "0:v", "-map", "1:a", "-map", "2:a",
		"-c:v", "libx264", "-preset", "ultrafast",
		// FLAC because it is lossless: a lossy codec would smear the tone and
		// weaken the only thing distinguishing the two tracks.
		"-c:a", "flac", "-t", "3", path,
	}
	if out, err := exec.Command(ff, args...).CombinedOutput(); err != nil {
		return fmt.Errorf("%v: %s", err, firstLine(string(out)))
	}
	return nil
}

func countStreams(path, kind string) int {
	out, err := exec.Command("ffprobe", "-v", "error",
		"-select_streams", kind, "-show_entries", "stream=index",
		"-of", "csv=p=0", path).Output()
	if err != nil {
		return -1
	}
	var n int
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if strings.TrimSpace(line) != "" {
			n++
		}
	}
	return n
}

// dominantHz estimates a mono 16-bit PCM WAV's frequency by counting zero
// crossings. Enough to tell one tone from another, which is all that is asked
// of it, and it needs no dependency an acceptance run might not have.
func dominantHz(path string, rate int) (float64, int, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, 0, err
	}
	const wavHeader = 44
	if len(b) <= wavHeader {
		return 0, 0, fmt.Errorf("%s is %d bytes, too short to hold audio", filepath.Base(path), len(b))
	}
	pcm := b[wavHeader:]
	samples := len(pcm) / 2
	var crossings int
	var prev int16
	for i := range samples {
		s := int16(binary.LittleEndian.Uint16(pcm[i*2:]))
		if i > 0 && ((prev < 0 && s >= 0) || (prev >= 0 && s < 0)) {
			crossings++
		}
		prev = s
	}
	if samples == 0 {
		return 0, 0, fmt.Errorf("no samples")
	}
	seconds := float64(samples) / float64(rate)
	return float64(crossings) / seconds / 2, samples, nil
}

// ---------------------------------------------------------------------------
// The real whisper.cpp build.

// gatedFlags are the long options WhisperArgs will only pass when the detected
// build advertises them. Each is a real capability difference, and each is a
// job-killer if the gate is wrong: whisper.cpp answers an unknown option with a
// usage dump and a non-zero exit, so -- as args.go puts it -- passing
// --output-json-full to a build that lacks it "does not lose the confidences,
// it loses the whole job".
var gatedFlags = []string{"output-json-full", "print-progress", "no-gpu"}

// knownBinaryNames restates the names whisper.cpp's CLI ships under, so this
// driver can tell "nothing is installed" from "something is installed and
// Detect did not find it".
//
// DELIBERATELY NOT transcribe.BinaryNames. Searching with the list under test
// makes the two agree by construction: a name dropped from BinaryNames would
// disappear from the search too, and the step would report a clean skip for a
// machine that has whisper installed and a product that can no longer see it.
var knownBinaryNames = []string{"whisper-cli", "whisper-cpp", "whisper", "main"}

// whisperBuild checks the detector against a real installed binary.
func whisperBuild() {
	// Asked first and independently, because it is what makes a skip honest.
	var onPath string
	for _, n := range knownBinaryNames {
		if p, err := exec.LookPath(n); err == nil {
			onPath = p
			break
		}
	}
	emit("onPath", onPath != "")
	if onPath != "" {
		emit("onPathBinary", onPath)
	}

	t, err := transcribe.Detect(context.Background(), "")
	if err != nil {
		emit("found", false)
		emit("error", firstLine(err.Error()))
		return
	}
	emit("found", t.Available())
	emit("binary", t.Binary)
	emit("version", t.Version)

	// THE ANTI-VACUITY GUARD, and the reason this check is worth anything.
	// Tools.HasFlag returns true for EVERY name when the flag set is empty --
	// deliberately, so an unreadable help text fails open rather than disabling
	// features. The consequence is that "every gated flag is advertised" is
	// trivially true for a build whose help we failed to parse. Reporting the
	// count separately is what stops this step passing on a parse that produced
	// nothing.
	emit("flagCount", len(t.Flags))

	var missing []string
	for _, f := range gatedFlags {
		if !t.HasFlag(f) {
			missing = append(missing, f)
		}
	}
	emit("gated", len(gatedFlags))
	emit("missing", len(missing))
	if len(missing) > 0 {
		emit("missingNames", strings.Join(missing, ","))
	}
}

// ---------------------------------------------------------------------------
// A real model, and a real transcription.

// modelDir is where the opt-in steps keep their model, so a second run does not
// download it again. Download itself short-circuits on an already-valid file.
func modelDir() string {
	if d := os.Getenv("POLY_TRANSCRIBE_MODEL_DIR"); d != "" {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "polyemesis-acceptance-models")
	}
	return filepath.Join(home, ".cache", "polyemesis-acceptance", "models")
}

// modelName is the model the opt-in steps use. tiny is 74 MB -- the smallest
// thing that is really a whisper model, which is what these steps need. They
// are not measuring transcription quality.
func modelName() string {
	if n := os.Getenv("POLY_TRANSCRIBE_MODEL"); n != "" {
		return n
	}
	return "tiny"
}

// download runs the real downloader against the real host.
//
// The cheap header probe in `catalogue` says a SHA-256 is on offer. This says
// it was ENFORCED: DownloadResult.Verified names the strongest check that
// actually ran, and "length" instead of "sha256" is the silent downgrade that
// probe is an early warning for.
func download() {
	m, ok := transcribe.FindModel(modelName())
	if !ok {
		fail("model %q is not in the catalogue", modelName())
	}
	dir := modelDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fail("mkdir %s: %v", dir, err)
	}
	emit("model", m.Name)
	emit("dir", dir)

	// FORCE A REAL TRANSFER. Download returns immediately with Verified
	// "existing" when a valid model is already on disk, which is right for the
	// product -- re-fetching three gigabytes because a job was retried would be
	// its own failure -- and useless here. This step's entire claim is that the
	// checksum was ENFORCED against the host's own figure, and a second run that
	// skipped the transfer would report that claim as proven having done nothing.
	// The step is opt-in precisely because it moves real weight; so it moves it.
	if err := os.Remove(filepath.Join(dir, m.Filename())); err != nil && !os.IsNotExist(err) {
		fail("clear cached model: %v", err)
	}

	start := time.Now()
	d := &transcribe.Downloader{Dir: dir}
	res, err := d.Download(context.Background(), m, nil)
	emit("elapsedMs", time.Since(start).Milliseconds())
	if err != nil {
		emit("ok", false)
		emit("error", firstLine(err.Error()))
		return
	}
	emit("ok", true)
	emit("bytes", res.Bytes)
	emit("verified", res.Verified)
	emit("sha1", res.SHA1)

	// The file is checked again by the function that gates a model before it is
	// handed to whisper -- the same call Download makes when it decides a
	// re-download can be skipped. A download that verified in flight but is
	// rejected at rest would mean the two checks disagree, and the model would
	// be re-downloaded on every job.
	emit("verifyAtRest", transcribe.VerifyModelFile(res.Path, m) == nil)
	if err := transcribe.VerifyModelFile(res.Path, m); err != nil {
		emit("verifyError", firstLine(err.Error()))
	}

	installed, err := transcribe.InstalledModels(dir)
	if err != nil {
		emit("installedError", firstLine(err.Error()))
		return
	}
	var found bool
	for _, im := range installed {
		if im.Name == m.Name && im.Known {
			found = true
		}
	}
	emit("listedAsKnown", found)
}

// endToEnd runs a recording through the whole local pipeline.
//
// WHAT THIS ESTABLISHES, precisely: that the two argument builders produce
// command lines the REAL programs accept, and that the output lands where the
// package says it will. args.go and worker.go agree on JSONPath(prefix) by
// convention, and whisper.cpp is the only thing that can confirm the convention
// -- a build that changed where -of puts the JSON would leave worker.go reading
// a path that is not there.
//
// WHAT IT DOES NOT ESTABLISH: transcription accuracy. The audio is a tone, so
// there are no words to get right, and this makes NO assertion about the text.
// Proving that speech comes back as the right words needs a speech sample, and
// committing an audio blob to this repo to get one is a trade this suite does
// not make on its own.
func endToEnd() {
	ff, err := exec.LookPath("ffmpeg")
	if err != nil {
		emit("ready", false)
		emit("error", "ffmpeg not on PATH")
		return
	}
	t, err := transcribe.Detect(context.Background(), "")
	if err != nil || !t.Available() {
		emit("ready", false)
		emit("error", "whisper.cpp not installed")
		return
	}
	m, ok := transcribe.FindModel(modelName())
	if !ok {
		emit("ready", false)
		emit("error", "model "+modelName()+" not in catalogue")
		return
	}
	model, err := transcribe.ResolveModel(modelDir(), m.Name)
	if err != nil {
		emit("ready", false)
		emit("error", firstLine(err.Error()))
		return
	}
	emit("ready", true)
	emit("model", model)

	dir, err := os.MkdirTemp("", "poly-transcribe-e2e")
	if err != nil {
		fail("mkdtemp: %v", err)
	}
	defer os.RemoveAll(dir)

	mkv := filepath.Join(dir, "recording.mkv")
	if err := buildTwoTrackMKV(ff, mkv); err != nil {
		fail("build fixture: %v", err)
	}

	// Track 1, not track 0, so the extraction under test is one the absolute
	// index form would get wrong.
	wav := filepath.Join(dir, "track1.wav")
	extract := transcribe.ExtractArgs(transcribe.ExtractSpec{
		FFmpeg: ff, Input: mkv, Track: 1, Output: wav,
	})
	if out, err := exec.Command(ff, extract...).CombinedOutput(); err != nil {
		emit("extracted", false)
		emit("error", firstLine(string(out)))
		return
	}
	emit("extracted", true)

	prefix := filepath.Join(dir, "track1")
	spec := transcribe.WhisperSpec{
		Model: model, Input: wav, OutputPrefix: prefix,
		JSON: true, FullJSON: true, Language: "en", Threads: 2, Flags: t,
	}
	args := transcribe.WhisperArgs(spec)
	emit("argc", len(args))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	start := time.Now()
	out, runErr := exec.CommandContext(ctx, t.Binary, args...).CombinedOutput()
	emit("elapsedMs", time.Since(start).Milliseconds())

	// THE EXIT STATUS IS NOT ENOUGH, and finding that out is why this step
	// exists. args.go reasons that "whisper.cpp exits with a usage dump on an
	// unknown option, so passing --output-json-full to a build from before that
	// flag existed does not lose the confidences, it loses the whole job" -- and
	// the usage dump is real, but the non-zero exit is not. Handed an argument it
	// does not recognise, whisper-cli prints "error: unknown argument: X",
	// prints its usage, and EXITS 0.
	//
	// So a check that read only the exit status would report a refused argument
	// list as accepted, which is the vacuous-guard shape: passing while the thing
	// it names is broken. The output is what settles it.
	//
	// worker.go has the same blind spot and it is not fixed here: whisperRun
	// treats cmd.Wait() == nil as success, finds no JSON, falls back to the
	// stdout segments it never got, and returns an EMPTY transcript with a nil
	// error -- a job that reports success and produced nothing.
	text := string(out)
	refused := strings.Contains(text, "error: unknown argument") || strings.Contains(text, "usage: whisper")
	emit("usageDump", refused)
	emit("exitOK", runErr == nil)
	emit("accepted", runErr == nil && !refused)
	if runErr != nil || refused {
		if runErr != nil {
			emit("exitError", firstLine(runErr.Error()))
		}
		emit("tail", firstLine(lastNonEmpty(text)))
		return
	}

	jsonPath := transcribe.JSONPath(prefix)
	raw, err := os.ReadFile(jsonPath)
	if err != nil {
		emit("jsonWritten", false)
		emit("jsonError", firstLine(err.Error()))
		return
	}
	emit("jsonWritten", true)
	emit("jsonBytes", len(raw))

	var lang string
	segs, err := transcribe.ParseJSON(raw, &lang)
	if err != nil {
		emit("parsed", false)
		emit("parseError", firstLine(err.Error()))
		return
	}
	emit("parsed", true)
	emit("segments", len(segs))
	emit("language", lang)

	// No assertion on the text -- see this function's comment. What IS checked
	// is that whatever whisper did say sits inside the three seconds of audio it
	// was given: a segment past the end of the input would mean the offsets were
	// misread, which is a parse bug the text cannot reveal.
	const audioMS = 3000
	inRange := true
	for _, s := range segs {
		if s.StartMS < 0 || s.EndMS > audioMS*2 {
			inRange = false
		}
	}
	emit("offsetsSane", inRange)
}

func lastNonEmpty(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.TrimSpace(lines[i]) != "" {
			return lines[i]
		}
	}
	return ""
}
