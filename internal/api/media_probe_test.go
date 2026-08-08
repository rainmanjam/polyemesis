package api

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/config"
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

func TestUploadAcceptsRealMediaAndRecordsWhatItIs(t *testing.T) {
	ffprobe := ffprobeOrSkip(t)
	ffmpegBin, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not installed")
	}
	_, h, _, auth := probeServer(t, ffprobe)

	// Two audio tracks: the count is the field the Library exists to show.
	src := filepath.Join(t.TempDir(), "src.mkv")
	mk := exec.Command(ffmpegBin, "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc2=size=160x90:rate=15",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000",
		"-f", "lavfi", "-i", "sine=frequency=880:sample_rate=48000",
		"-map", "0:v", "-map", "1:a", "-map", "2:a",
		"-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p",
		"-c:a", "aac", "-t", "1", "-y", src)
	if out, err := mk.CombinedOutput(); err != nil {
		t.Skipf("could not build a sample: %v: %s", err, out)
	}
	body, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("read sample: %v", err)
	}

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
