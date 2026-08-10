package api

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/config"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/uploads"
)

// These cover the HANDLER, not the prober.
//
// internal/ffmpeg already establishes that ProbeFile refuses a PDF. What that
// cannot show is whether POST /api/v1/media ever calls it -- and it did not
// show it: review deleted the probe call from the handler and every test in
// both packages stayed green. Server.probeBin exists so the rejection has
// somewhere to be asserted, and it supplies the binary rather than replacing
// the logic, so what runs here is the real handler path.

func ffprobeOrSkip(t *testing.T) string {
	t.Helper()
	bin, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("ffprobe is not installed")
	}
	return bin
}

// probeServer is mediaServer with the prober wired up, and returns the Server
// so a test can reach probeBin.
func probeServer(t *testing.T, ffprobe string) (*Server, http.Handler, string, func(*http.Request)) {
	t.Helper()
	dataDir := t.TempDir()
	s, h, _ := testServer(t, config.Config{DataDir: dataDir})
	s.probeBin = ffprobe
	return s, h, dataDir, login(t, h)
}

// uploadBytesRequest is uploadRequest for a body that is not a string, because
// real media is not.
func uploadBytesRequest(t *testing.T, filename string, content []byte) *http.Request {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("file", filename)
	if err != nil {
		t.Fatalf("CreateFormFile: %v", err)
	}
	if _, err := fw.Write(content); err != nil {
		t.Fatalf("write part: %v", err)
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	r := httptest.NewRequest(http.MethodPost, "/api/v1/media", &buf)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	r.RemoteAddr = "203.0.113.5:44444"
	return r
}

func TestUploadRejectsAFileThatIsNotMedia(t *testing.T) {
	ffprobe := ffprobeOrSkip(t)
	_, h, dataDir, auth := probeServer(t, ffprobe)

	// Named .mp4 on purpose: the extension was never the check, and this is the
	// exact shape that used to reach the Library looking like a video.
	r := uploadRequest(t, "file", "holiday.mp4", "%PDF-1.7\n1 0 obj\n<<>>\nendobj\n")
	auth(r)
	w := do(t, h, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("upload status = %d, want 400: %s", w.Code, w.Body.String())
	}
	// ffprobe's own words reach the operator. A generic "could not read this
	// file" leaves them with nothing to act on.
	if body := w.Body.String(); !strings.Contains(body, "could not be read as media") &&
		!strings.Contains(body, "no video or audio stream") {
		t.Errorf("the message does not say why: %s", body)
	}

	// And it is gone. A rejected upload left on disk is a file nothing will
	// ever list, quietly occupying the volume.
	store, err := uploads.New(dataDir)
	if err != nil {
		t.Fatalf("uploads.New: %v", err)
	}
	list, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("the rejected upload is still listed: %+v", list)
	}
	if entries, err := os.ReadDir(filepath.Join(dataDir, "uploads")); err == nil {
		for _, e := range entries {
			t.Errorf("left on disk after rejection: %s", e.Name())
		}
	}
}

// sampleMedia returns the bytes of a short 160x90 h264 file with TWO aac audio
// tracks, which is the shape the assertions below are written against.
//
// ONE SKIP SITE FOR EVERY TEST IN THIS FILE THAT NEEDS REAL MEDIA. Each of
// them used to carry its own "ffmpeg is not installed" and "could not build a
// sample" pair, and internal/testenv's ratchet is right that every one of those
// is a free pass: an FFmpeg without libx264 would stop exercising the upload
// gate several tests at a time and print ok.
func sampleMedia(t *testing.T) []byte {
	t.Helper()
	ffmpegBin, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not installed, so no real media can be built to upload")
	}
	src := filepath.Join(t.TempDir(), "src.mkv")
	mk := exec.Command(ffmpegBin, "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc2=size=160x90:rate=15",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000",
		"-f", "lavfi", "-i", "sine=frequency=880:sample_rate=48000",
		"-map", "0:v", "-map", "1:a", "-map", "2:a",
		"-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p",
		"-c:a", "aac", "-t", "1", "-y", src)
	if out, err := mk.CombinedOutput(); err != nil {
		t.Skipf("could not build a sample with this FFmpeg: %v: %s", err, out)
	}
	body, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("read sample: %v", err)
	}
	return body
}

func TestUploadAcceptsRealMediaAndRecordsWhatItIs(t *testing.T) {
	ffprobe := ffprobeOrSkip(t)
	// Two audio tracks: the count is the field the Library exists to show.
	body := sampleMedia(t)
	_, h, _, auth := probeServer(t, ffprobe)

	r := uploadBytesRequest(t, "show.mkv", body)
	auth(r)
	w := do(t, h, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("upload status = %d, want 201: %s", w.Code, w.Body.String())
	}
	var got uploads.File
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.Media == nil {
		t.Fatal("the upload response carried no media info")
	}
	if got.Media.AudioTracks != 2 {
		t.Errorf("AudioTracks = %d, want 2", got.Media.AudioTracks)
	}
	if got.Media.Width != 160 || got.Media.Height != 90 {
		t.Errorf("resolution = %dx%d, want 160x90", got.Media.Width, got.Media.Height)
	}

	// EVERY FIELD MediaInfo CARRIES IS COMPARED, not just the two the feature
	// was pitched on. A review found that FrameRate, VideoCodec, AudioCodec,
	// AudioChannels, AudioLayout, DurationSeconds and ProbedAt could each be
	// zeroed or dropped with the whole repository still green -- evidence
	// computed and never compared, which is how a Library column starts showing
	// a plausible wrong number and nothing notices.
	if got.Media.VideoCodec != "h264" {
		t.Errorf("VideoCodec = %q, want h264", got.Media.VideoCodec)
	}
	if got.Media.FrameRate < 14 || got.Media.FrameRate > 16 {
		t.Errorf("FrameRate = %v, want about 15", got.Media.FrameRate)
	}
	if got.Media.AudioCodec != "aac" {
		t.Errorf("AudioCodec = %q, want aac", got.Media.AudioCodec)
	}
	if got.Media.AudioChannels != 1 {
		t.Errorf("AudioChannels = %d, want 1", got.Media.AudioChannels)
	}
	if got.Media.AudioLayout != "mono" {
		t.Errorf("AudioLayout = %q, want mono", got.Media.AudioLayout)
	}
	if got.Media.DurationSeconds < 0.5 || got.Media.DurationSeconds > 2 {
		t.Errorf("DurationSeconds = %v, want about 1", got.Media.DurationSeconds)
	}
	if got.Media.ProbedAt.IsZero() {
		t.Error("ProbedAt is zero; the reading is undated")
	}
	if age := time.Since(got.Media.ProbedAt); age < 0 || age > time.Hour {
		t.Errorf("ProbedAt = %v, which is not when this probe ran", got.Media.ProbedAt)
	}

	// The listing is separate code from the upload response, and it is what the
	// Library actually reads.
	lr := httptest.NewRequest(http.MethodGet, "/api/v1/media", nil)
	auth(lr)
	lw := do(t, h, lr)
	if lw.Code != http.StatusOK {
		t.Fatalf("list status = %d", lw.Code)
	}
	var listed []uploads.File
	if err := json.Unmarshal(lw.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode listing: %v", err)
	}
	if len(listed) != 1 {
		t.Fatalf("listing has %d entries, want 1", len(listed))
	}
	if listed[0].Media == nil || listed[0].Media.AudioTracks != 2 {
		t.Errorf("the listing lost the media info: %+v", listed[0].Media)
	}
}

// fakeProbe writes an executable standing in for ffprobe and returns its path.
//
// A script rather than a stub function because probeUpload reaches ffprobe by
// spawning a PROCESS, and the two behaviours being pinned below -- a probe that
// is still running when the client goes away, and a probe that is still running
// when somebody else calls GET /api/v1/media -- only exist because it is a
// process that takes real time. A function seam would delete the very thing
// under test.
//
// It touches `started` on entry so a test can wait for the probe to actually be
// in flight instead of sleeping and hoping.
//
// POSIX ONLY, and the cost is stated rather than hidden: on Windows this is a
// text file with a #! line that CreateProcess will not run, so every test
// below that needs a probe with a controllable lifetime is skipped there. What
// goes unverified on Windows is the client-disconnect survival, the probe
// timeout, the staged-not-listable window and the two WARN lines -- all of
// which are platform-independent Go over a platform-independent os/exec, but
// "should be fine" is not a measurement. Issue filed; the fix is a small
// helper binary built by the test rather than a shell script, which is real
// work and is not being guessed at here.
func fakeProbe(t *testing.T, started string, body string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake probe is a POSIX shell script; see the comment on fakeProbe " +
			"for what this leaves unverified on Windows")
	}
	p := filepath.Join(t.TempDir(), "fake-ffprobe")
	script := "#!/bin/sh\ntouch " + started + "\n" + body + "\n"
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake ffprobe: %v", err)
	}
	return p
}

// waitFor blocks until cond holds, and fails rather than hanging.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// uploadsDirEntries lists the raw directory, including the names List hides.
func uploadsDirEntries(t *testing.T, dataDir string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(dataDir, "uploads"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("read uploads dir: %v", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func listMedia(t *testing.T, h http.Handler, auth func(*http.Request)) []uploads.File {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/media", nil)
	auth(r)
	w := do(t, h, r)
	if w.Code != http.StatusOK {
		t.Fatalf("list status = %d: %s", w.Code, w.Body.String())
	}
	var listed []uploads.File
	if err := json.Unmarshal(w.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode listing: %v", err)
	}
	return listed
}

// MUST-FIX 1. A 44-byte text file must not be able to wear another upload's
// metadata.
//
// "ffconcat version 1.0" plus a filename is a complete input to the concat
// demuxer: it opens the file named and reports that file's streams as its own.
// Driven through the real handler, this used to answer 201 and appear in the
// Library carrying the referenced video's h264/aac codecs, resolution and
// duration -- which is precisely the "authoritative-looking metadata for
// content that will die at air" the feature exists to prevent, re-created by
// the feature itself.
func TestUploadRefusesAScriptThatNamesAnotherUpload(t *testing.T) {
	ffprobe := ffprobeOrSkip(t)
	body := sampleMedia(t)
	_, h, dataDir, auth := probeServer(t, ffprobe)

	r := uploadBytesRequest(t, "victim.mkv", body)
	auth(r)
	w := do(t, h, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("the victim upload failed, so this proves nothing: %d %s", w.Code, w.Body.String())
	}
	var victim uploads.File
	if err := json.Unmarshal(w.Body.Bytes(), &victim); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if victim.Media == nil || victim.Media.VideoCodec == "" {
		t.Fatalf("the victim carries no metadata to steal, so this proves nothing: %+v", victim.Media)
	}

	// The stored name is unguessable, so a real attacker gets it from the
	// Library listing -- which is token-reachable. The test does the same.
	script := "ffconcat version 1.0\nfile " + victim.Name + "\n"
	if len(script) > 128 {
		t.Fatalf("the script grew to %d bytes", len(script))
	}
	sr := uploadRequest(t, "file", "innocent.mp4", script)
	auth(sr)
	sw := do(t, h, sr)
	if sw.Code != http.StatusBadRequest {
		t.Fatalf("an ffconcat script was accepted: status = %d, body = %s", sw.Code, sw.Body.String())
	}
	if b := sw.Body.String(); !strings.Contains(b, "playlist or script") {
		t.Errorf("the refusal does not say what was wrong: %s", b)
	}

	// The Library still holds exactly the one real file, and the script left
	// nothing behind -- not under its own name and not as a stray temp file.
	listed := listMedia(t, h, auth)
	if len(listed) != 1 || listed[0].Name != victim.Name {
		t.Fatalf("listing = %+v, want only %s", listed, victim.Name)
	}
	names := uploadsDirEntries(t, dataDir)
	for _, n := range names {
		if n != victim.Name && n != ".probe-"+victim.Name+".json" {
			t.Errorf("the refused script left %q on disk", n)
		}
	}
}

// The handler's own stream check, which internal/ffmpeg cannot exercise.
//
// A no-tracks MP4 is read perfectly by ffprobe and reports zero streams, so it
// reaches probeUpload with a nil error and an empty result. That is a different
// branch from the ffprobe-said-no path above, and it is the branch a renamed
// archive with a plausible header arrives on.
func TestUploadRefusesAContainerWithNoStreams(t *testing.T) {
	ffprobe := ffprobeOrSkip(t)
	_, h, dataDir, auth := probeServer(t, ffprobe)

	r := uploadBytesRequest(t, "empty.mp4", emptyMP4Bytes())
	auth(r)
	w := do(t, h, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", w.Code, w.Body.String())
	}
	if b := w.Body.String(); !strings.Contains(b, "no video or audio stream") {
		t.Errorf("wrong reason: %s", b)
	}
	if names := uploadsDirEntries(t, dataDir); len(names) != 0 {
		t.Errorf("left on disk after rejection: %v", names)
	}
}

// emptyMP4Bytes is a valid MPEG-4 file with no tracks: ftyp plus a moov holding
// only an mvhd. ffprobe exits 0 and reports zero streams for it.
func emptyMP4Bytes() []byte {
	box := func(kind string, payload []byte) []byte {
		out := make([]byte, 4, 8+len(payload))
		binary.BigEndian.PutUint32(out, uint32(8+len(payload)))
		out = append(out, kind...)
		return append(out, payload...)
	}
	ftyp := box("ftyp", append([]byte("isom"), append([]byte{0, 0, 2, 0}, "isomiso2mp41"...)...))
	return append(ftyp, box("moov", box("mvhd", make([]byte, 100)))...)
}

// MUST-FIX 2. A client that goes away mid-probe must not lose its file.
//
// The bytes have already landed; the transfer succeeded. What failed is an
// inspection nobody is waiting for the result of. The first version of this
// feature treated any non-nil probe error as a verdict about the file, so a
// dropped connection -- routine on an 8 GiB limit with no WriteTimeout and
// proxies in front -- answered 400 "this file could not be read as media:
// context canceled" and DELETED a valid upload. Two falsehoods and a data loss
// from one network event.
//
// This asserts the file survives, not merely that the error branch changed.
func TestUploadSurvivesAClientDisconnectDuringTheProbe(t *testing.T) {
	dataDir := t.TempDir()
	s, h, _ := testServer(t, config.Config{DataDir: dataDir})
	auth := login(t, h)
	started := filepath.Join(t.TempDir(), "started")
	// Sleeps far longer than the disconnect below, so the probe is guaranteed
	// to be in flight when the context is cancelled.
	// `exec` so the sleep REPLACES the shell rather than being a child of it.
	// Without it the sleep survives the kill holding ffprobe's stdout pipe, and
	// the handler waits out cmd.WaitDelay -- correct behaviour, but it is
	// internal/ffmpeg's to assert, not this test's.
	s.probeBin = fakeProbe(t, started, "exec sleep 60")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := uploadRequest(t, "file", "long-show.mkv", "pretend media bytes")
	auth(r)
	r = r.WithContext(ctx)

	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		done <- w
	}()

	waitFor(t, "the probe to start", func() bool {
		_, err := os.Stat(started)
		return err == nil
	})
	cancel() // the client hangs up, after every byte has already arrived

	var w *httptest.ResponseRecorder
	select {
	case w = <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("the handler never returned after the client disconnected")
	}

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201: an interrupted probe is not a verdict "+
			"about the file: %s", w.Code, w.Body.String())
	}

	// THE FILE IS STILL THERE. This is the assertion the whole test exists for;
	// checking only the status code would pass against a version that answered
	// 201 and deleted the bytes anyway.
	listed := listMedia(t, h, auth)
	if len(listed) != 1 {
		t.Fatalf("the surviving upload is not listed: %+v", listed)
	}
	full := filepath.Join(dataDir, "uploads", listed[0].Name)
	st, err := os.Stat(full)
	if err != nil {
		t.Fatalf("the upload is listed but not on disk: %v", err)
	}
	if st.Size() != int64(len("pretend media bytes")) {
		t.Errorf("size on disk = %d, want %d", st.Size(), len("pretend media bytes"))
	}
	// Accepted unchecked, which is the documented could-not-check outcome: no
	// probe ran to completion, so there is nothing to record.
	if listed[0].Media != nil {
		t.Errorf("an interrupted probe recorded metadata anyway: %+v", listed[0].Media)
	}
}

// The same rule for the other interruption: probeUploadTimeout expiring.
//
// Asserted separately because it is a DIFFERENT context error arriving by a
// different route, and because this one is a real hole rather than a kindness:
// a file ffprobe takes longer than probeUploadTimeout on is accepted unchecked.
// That is stated in probeUpload's comment and pinned here so it cannot become
// accidental.
func TestUploadIsAcceptedUncheckedWhenTheProbeTimesOut(t *testing.T) {
	dataDir := t.TempDir()
	s, h, _ := testServer(t, config.Config{DataDir: dataDir})
	auth := login(t, h)
	started := filepath.Join(t.TempDir(), "started")
	// `exec` so the sleep REPLACES the shell rather than being a child of it.
	// Without it the sleep survives the kill holding ffprobe's stdout pipe, and
	// the handler waits out cmd.WaitDelay -- correct behaviour, but it is
	// internal/ffmpeg's to assert, not this test's.
	s.probeBin = fakeProbe(t, started, "exec sleep 60")
	s.probeTimeout = 150 * time.Millisecond

	r := uploadRequest(t, "file", "slow.mkv", "pretend media bytes")
	auth(r)
	if w := do(t, h, r); w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %s", w.Code, w.Body.String())
	}
	if listed := listMedia(t, h, auth); len(listed) != 1 {
		t.Fatalf("the upload did not survive a probe timeout: %+v", listed)
	}
}

// A probe that RUNS and disagrees still rejects. Without this the two tests
// above would be satisfied by a handler that had simply stopped rejecting.
func TestUploadStillRejectsWhenTheProbeRunsAndFails(t *testing.T) {
	dataDir := t.TempDir()
	s, h, _ := testServer(t, config.Config{DataDir: dataDir})
	auth := login(t, h)
	started := filepath.Join(t.TempDir(), "started")
	s.probeBin = fakeProbe(t, started, "echo 'Invalid data found when processing input' >&2; exit 1")

	r := uploadRequest(t, "file", "junk.mkv", "not media at all")
	auth(r)
	w := do(t, h, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", w.Code, w.Body.String())
	}
	if names := uploadsDirEntries(t, dataDir); len(names) != 0 {
		t.Errorf("left on disk after rejection: %v", names)
	}
}

// MUST-FIX 3. Nothing may see, or reference, an upload that is still being
// probed.
//
// The file used to be renamed into place BEFORE the probe, so for the length of
// the probe it was listed by GET /api/v1/media with a working pullUrl and PUT
// /api/v1/settings would accept it as a playlist item. The reject then removed
// it with a raw store.Delete, taking neither settingsMu nor the in-use check --
// landing on the stored-item-names-nothing state handleDeleteMedia holds a
// global lock to prevent and answers 409 for.
//
// Both halves are asserted DURING the probe, from a second goroutine, because
// the window is what the bug was.
func TestARejectedUploadIsNeverVisibleOrReferenceableWhileItIsProbed(t *testing.T) {
	dataDir := t.TempDir()
	s, h, _ := testServer(t, config.Config{DataDir: dataDir})
	auth := login(t, h)
	started := filepath.Join(t.TempDir(), "started")
	release := filepath.Join(t.TempDir(), "release")
	// Blocks until the test says so, then refuses the file. The handler is held
	// inside the probe for exactly as long as the assertions need.
	s.probeBin = fakeProbe(t, started,
		"while [ ! -f "+release+" ]; do sleep 0.01; done; echo 'Invalid data found' >&2; exit 1")

	r := uploadRequest(t, "file", "pending.mkv", "pretend media bytes")
	auth(r)
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		done <- w
	}()
	waitFor(t, "the probe to start", func() bool {
		_, err := os.Stat(started)
		return err == nil
	})

	// (a) NOT VISIBLE. The bytes are on disk right now -- assert that too, so a
	// handler that had simply not written anything yet could not pass this.
	staged := uploadsDirEntries(t, dataDir)
	if len(staged) != 1 {
		t.Fatalf("expected exactly one staged file on disk mid-probe, got %v", staged)
	}
	if !strings.HasPrefix(staged[0], ".partial-") {
		t.Errorf("the file being probed is published under %q, not staged", staged[0])
	}
	if listed := listMedia(t, h, auth); len(listed) != 0 {
		t.Fatalf("a file still being probed is listed by GET /api/v1/media: %+v", listed)
	}

	// (b) NOT REFERENCEABLE. playlistUploadProblems is the exact gate
	// handlePutSettings runs a playlist through, called directly so the answer
	// cannot be confused with the many other reasons a whole-document PUT can
	// be refused -- an earlier version of this test asserted on the PUT status
	// and passed for one of those other reasons, with the check under test
	// removed. The end-to-end HTTP shape is covered by
	// TestSavingAPlaylistItemNamingAStagedOrSidecarFileIsRefused.
	//
	// An API client cannot learn the staged name; that is the point. Handing
	// the validator the real one read off the disk is the stronger statement.
	if err := s.playlistUploadProblems(
		db.PlaylistSettings{Items: []db.PlaylistItem{{Upload: staged[0]}}},
		db.PlaylistSettings{}); err == nil {
		t.Errorf("a playlist item may name %q, a file that is still being probed", staged[0])
	}
	// THE CONTROL. Without it the assertion above is satisfied by a validator
	// that refuses everything, which is a different bug and would look
	// identical here.
	if err := os.WriteFile(filepath.Join(dataDir, "uploads", "control.ts"), []byte("x"), 0o600); err != nil {
		t.Fatalf("seed control: %v", err)
	}
	if err := s.playlistUploadProblems(
		db.PlaylistSettings{Items: []db.PlaylistItem{{Upload: "control.ts"}}},
		db.PlaylistSettings{}); err != nil {
		t.Fatalf("the validator refuses an ordinary upload too, so the "+
			"assertion above proves nothing: %v", err)
	}
	if err := os.Remove(filepath.Join(dataDir, "uploads", "control.ts")); err != nil {
		t.Fatalf("remove control: %v", err)
	}

	if err := os.WriteFile(release, nil, 0o600); err != nil {
		t.Fatalf("release: %v", err)
	}
	var w *httptest.ResponseRecorder
	select {
	case w = <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("the upload never finished")
	}
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", w.Code, w.Body.String())
	}
	if names := uploadsDirEntries(t, dataDir); len(names) != 0 {
		t.Errorf("left on disk after rejection: %v", names)
	}
}

// NOTHING AN UPLOAD SAYS MAY MAKE THE SERVER FETCH A URL.
//
// A review before this change drove HLS, ffconcat and bare-http shapes at a
// canary listener and got zero requests -- but that was a property of the
// FFmpeg build, not of anything in this repository, and the build is not ours
// to freeze. ProbeFile now passes -protocol_whitelist file so it is pinned in
// the argv; this is the check that notices if the pin is ever removed, or if a
// future build starts fetching something the last one did not.
//
// The listener is real and the assertion is that it was never touched. A test
// that only asserted a 400 would pass just as happily while the server had
// already made the request.
//
// SAY PLAINLY WHAT THIS DOES NOT COVER. Deleting -protocol_whitelist from
// ProbeFile's argv leaves this test passing, because the FFmpeg it runs against
// does not fetch these URLs by default either. So this is not coverage of the
// flag; it is coverage of the PROPERTY, on whatever build is present. Do not
// remove the flag on the strength of this test staying green -- the flag is
// what makes the property ours rather than the build's, and the two only agree
// today.
func TestAnUploadCannotMakeTheServerFetchAURL(t *testing.T) {
	ffprobe := ffprobeOrSkip(t)

	var hits atomic.Int64
	var paths []string
	var mu sync.Mutex
	canary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		mu.Lock()
		paths = append(paths, r.URL.String())
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer canary.Close()

	_, h, _, auth := probeServer(t, ffprobe)
	for _, tc := range []struct{ name, body string }{
		{"an ffconcat script naming a URL",
			"ffconcat version 1.0\nfile " + canary.URL + "/stolen.ts\n"},
		{"an HLS playlist naming a URL",
			"#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-TARGETDURATION:4\n#EXTINF:4.0,\n" +
				canary.URL + "/seg0.ts\n#EXT-X-ENDLIST\n"},
		{"an HLS playlist with a relative segment",
			"#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-TARGETDURATION:4\n#EXTINF:4.0,\nseg0.ts\n#EXT-X-ENDLIST\n"},
		{"a bare URL in a text file", canary.URL + "/plain\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := uploadRequest(t, "file", "payload.mp4", tc.body)
			auth(r)
			w := do(t, h, r)
			if w.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400: %s", w.Code, w.Body.String())
			}
		})
	}

	mu.Lock()
	defer mu.Unlock()
	if n := hits.Load(); n != 0 {
		t.Fatalf("the canary listener was reached %d times from an upload probe: %v", n, paths)
	}
}

// MUST-FIX 4a. The bypass has to be VISIBLE, because the documentation tells
// an operator to go and look.
//
// docs/TROUBLESHOOTING.md used to say "the startup log says which binaries were
// found and whether the engine came up". Neither line exists: cmd/polyemesis's
// only binary line is `log.Info("ffmpeg detected", ..., "path", tools.FFmpeg)`
// which never names ffprobe, and the only engine lifecycle line in the process
// is "engine stopped". So the single documented way to notice that uploads were
// being accepted unchecked was a diagnostic nobody had written.
//
// These assert the lines the documentation now points at. A doc sentence about
// a log line is a claim about behaviour, and this is the test that makes it one.
func TestTheUncheckedBypassSaysSoInTheLog(t *testing.T) {
	t.Run("when there is nothing to probe with", func(t *testing.T) {
		var buf bytes.Buffer
		dataDir := t.TempDir()
		s, h, _ := testServer(t, config.Config{DataDir: dataDir})
		s.log = slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
		auth := login(t, h)

		r := uploadRequest(t, "file", "whatever.mp4", "%PDF-1.7\n")
		auth(r)
		if w := do(t, h, r); w.Code != http.StatusCreated {
			t.Fatalf("status = %d, want 201: %s", w.Code, w.Body.String())
		}
		if got := buf.String(); !strings.Contains(got, "accepting this upload unchecked") {
			t.Errorf("an upload was accepted unchecked and the log does not say so: %s", got)
		}
	})

	t.Run("when the probe is interrupted", func(t *testing.T) {
		var buf bytes.Buffer
		dataDir := t.TempDir()
		s, h, _ := testServer(t, config.Config{DataDir: dataDir})
		s.log = slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
		auth := login(t, h)
		s.probeBin = fakeProbe(t, filepath.Join(t.TempDir(), "started"), "exec sleep 60")
		s.probeTimeout = 150 * time.Millisecond

		r := uploadRequest(t, "file", "slow.mkv", "pretend media bytes")
		auth(r)
		if w := do(t, h, r); w.Code != http.StatusCreated {
			t.Fatalf("status = %d, want 201: %s", w.Code, w.Body.String())
		}
		if got := buf.String(); !strings.Contains(got, "upload probe was interrupted") {
			t.Errorf("a probe was interrupted and the log does not say so: %s", got)
		}
	})
}

// With nothing to probe with, the upload is accepted rather than refused.
//
// This is the documented bypass, asserted so that it stays deliberate: there is
// nothing to judge with, and refusing all media because the server cannot
// inspect it would break a working install to enforce a check it cannot
// perform. It is also why the reject tests above need the seam -- without it
// this is the path every test in the package takes.
func TestUploadIsAcceptedUncheckedWhenThereIsNoProber(t *testing.T) {
	h, _, auth := mediaServer(t)
	r := uploadRequest(t, "file", "whatever.mp4", "%PDF-1.7\n")
	auth(r)
	if w := do(t, h, r); w.Code != http.StatusCreated {
		t.Fatalf("upload status = %d, want 201 when nothing can probe: %s", w.Code, w.Body.String())
	}
}
