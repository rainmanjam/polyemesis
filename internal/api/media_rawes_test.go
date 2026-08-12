package api

import (
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/testenv"
	"github.com/rainmanjam/polyemesis/internal/uploads"
)

// ffmpegOrSkip is ffprobeOrSkip's sibling: the binary that COUNTS the length of
// a file whose container declares none.
func ffmpegOrSkip(t *testing.T) string {
	t.Helper()
	return testenv.FFmpegBinary(t, "ffmpeg",
		"ffmpeg is not installed, so a length nothing declared cannot be counted")
}

// rawStreamBytes returns a raw H.264 elementary stream: the Annex-B bitstream
// with no container around it, so there is nowhere in the file for a duration
// to be written down.
//
// 30 fps rather than 25, deliberately. ffprobe reports avg_frame_rate as 25/1
// for every raw H.264 stream whatever its real rate -- it is the demuxer's
// hardcoded fallback -- so a 25 fps fixture cannot tell a real count from that
// assumption. See internal/ffmpeg's
// TestACountedLengthIsTheRealOneAndNotTheDemuxersAssumption.
func rawStreamBytes(t *testing.T) []byte {
	t.Helper()
	ffmpegBin := ffmpegOrSkip(t)
	out := filepath.Join(t.TempDir(), "dump.h264")
	mk := exec.Command(ffmpegBin, "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc2=size=160x90:rate=30",
		"-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p",
		"-t", "2", "-f", "h264", "-y", out)
	if o, err := mk.CombinedOutput(); err != nil {
		t.Skipf("this FFmpeg cannot write a raw h264 stream (%v: %s)", err, o)
	}
	body, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read raw stream: %v", err)
	}
	return body
}

// THE UPLOAD GATE ACCEPTS A RAW ELEMENTARY STREAM AND RECORDS A COUNTED LENGTH.
//
// This is #218 at the first of the two gates. Before it, `selfContainedFormats`
// admitted h264/hevc/mpegvideo on the reasoning that an operator handed a .h264
// dump by an encoder has a real file -- and the duration branch then refused
// exactly those files, because a bitstream has no container to declare a length
// in. The operator was told to re-save it as MP4 and upload it again, which is
// manual work the product can do.
//
// THREE THINGS ARE ASSERTED AND EACH FAILS SEPARATELY. That the file is stored;
// that the length recorded is the real one; and that it is labelled as COUNTED
// rather than passed off as something the file declared. An "it was accepted"
// assertion alone is satisfied by a 201 carrying durationSeconds=0, which is a
// Library row that lies and a normalise job with an unbounded disk estimate.
func TestUploadAcceptsARawElementaryStreamAndSaysTheLengthWasCounted(t *testing.T) {
	ffprobe := ffprobeOrSkip(t)
	body := rawStreamBytes(t)
	s, h, _, auth := probeServer(t, ffprobe)
	// The seam that makes the counting branch reachable in this package: there
	// is no engine manager under `go test ./internal/api`, so without it
	// probeUpload has no ffmpeg and gives the pre-#218 refusal.
	s.encodeBin = ffmpegOrSkip(t)

	r := uploadBytesRequest(t, "dump.h264", body)
	auth(r)
	w := do(t, h, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("upload status = %d, want 201: %s\n"+
			"A raw .h264 dump is a real file the allowlist already admits; #218 is "+
			"that its length is counted rather than demanded from a header it "+
			"cannot have", w.Code, w.Body.String())
	}
	var got uploads.File
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !got.Verified {
		t.Fatalf("the upload was stored UNVERIFIED (%q). It was inspected and it "+
			"passed; recording it as un-inspected makes the settings validator "+
			"refuse any playlist item naming it", got.UnverifiedReason)
	}
	if got.Media == nil {
		t.Fatal("the upload response carried no media info")
	}
	if math.Abs(got.Media.DurationSeconds-2) > 0.25 {
		t.Errorf("durationSeconds = %v, want about 2", got.Media.DurationSeconds)
	}
	// THE PROVENANCE, which is what stops a counted length being read as a
	// container's own statement. This is the field an operator sees beside the
	// number when they decide whether to schedule the item.
	if got.Media.DurationSource != "counted" {
		t.Errorf("durationSource = %q, want %q: nothing in this file declared a "+
			"length, so reporting it as though something had is the laundering the "+
			"field exists to prevent", got.Media.DurationSource, "counted")
	}

	// AND IT SURVIVES THE SIDECAR. The response above is built in this process;
	// the Library reads a .probe- JSON file back off disk, and a wrong or absent
	// struct tag would leave the provenance correct in the reply that nobody
	// keeps and empty in every listing thereafter -- the field silently
	// degrading to "unknown" for exactly the files it was added for.
	lr := httptest.NewRequest(http.MethodGet, "/api/v1/media", nil)
	auth(lr)
	lw := do(t, h, lr)
	if lw.Code != http.StatusOK {
		t.Fatalf("list status = %d: %s", lw.Code, lw.Body.String())
	}
	var listed []uploads.File
	if err := json.Unmarshal(lw.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode listing: %v", err)
	}
	if len(listed) != 1 {
		t.Fatalf("listing has %d entries, want 1", len(listed))
	}
	if listed[0].Media == nil || listed[0].Media.DurationSource != "counted" {
		t.Errorf("the stored sidecar reports durationSource=%v, want %q",
			listed[0].Media, "counted")
	}
	if listed[0].Media != nil && math.Abs(listed[0].Media.DurationSeconds-2) > 0.25 {
		t.Errorf("the stored sidecar reports durationSeconds=%v, want about 2",
			listed[0].Media.DurationSeconds)
	}
}

// AND A CONTAINER IS STILL REPORTED AS DECLARING ITS OWN LENGTH.
//
// The control for the test above, and the reason the pair is differential: an
// implementation that stamped every upload "counted" would satisfy that one
// alone. Both run through the same handler with the same wiring, so the only
// difference between them is the file.
func TestUploadSaysAContainersLengthWasDeclaredRatherThanCounted(t *testing.T) {
	ffprobe := ffprobeOrSkip(t)
	body := sampleMedia(t)
	s, h, _, auth := probeServer(t, ffprobe)
	s.encodeBin = ffmpegOrSkip(t)

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
	if got.Media.DurationSource != "declared" {
		t.Errorf("durationSource = %q, want %q. A Matroska file writes its own "+
			"duration down; reporting it as counted would make the field say "+
			"nothing, since it would then say the same thing about every upload",
			got.Media.DurationSource, "declared")
	}
}

// AN INSTALL WITH NO FFMPEG REFUSES THE RAW STREAM RATHER THAN STORING IT.
//
// #118's guarantee, which #218 must not spend: the upload gate and the
// normalise worker must give the same answer about the same file. The count is
// what makes these files acceptable, so where there is nothing to count with,
// the answer has to be the refusal WITH ITS REMEDY -- not a 201 for a file the
// worker will then refuse forever.
//
// The difference from the first test is one field on the server, which is what
// makes this a control rather than a restatement.
func TestWithoutAnFFmpegTheUploadGateStillRefusesARawStream(t *testing.T) {
	ffprobe := ffprobeOrSkip(t)
	body := rawStreamBytes(t)
	_, h, dataDir, auth := probeServer(t, ffprobe)
	// s.encodeBin deliberately left empty.

	r := uploadBytesRequest(t, "dump.h264", body)
	auth(r)
	w := do(t, h, r)
	if w.Code == http.StatusCreated {
		t.Fatalf("a raw stream was STORED by an install with nothing to count it with: %s\n"+
			"Its length is still unknown and the normalise worker will refuse it "+
			"permanently, which is the accepted-at-the-door-and-dead-at-the-worker "+
			"state #118 closed", w.Body.String())
	}
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
	if !strings.Contains(w.Body.String(), "how long") ||
		!strings.Contains(w.Body.String(), "MP4") {
		t.Errorf("the refusal no longer says what is wrong or what to do: %s", w.Body.String())
	}
	// And the bytes are gone rather than left in the uploads directory as a
	// file nothing will ever list.
	entries, err := os.ReadDir(filepath.Join(dataDir, "uploads"))
	if err == nil {
		for _, e := range entries {
			if !e.IsDir() {
				t.Errorf("a refused upload was left on disk: %s", e.Name())
			}
		}
	}
}

// AND THE 400 BODY NAMES THE CAUSE RATHER THAN "<nil>".
//
// THE SAME REFUSAL AS THE TEST ABOVE, READ INSTEAD OF COUNTED. That one asserts
// the upload was rejected, that the bytes are gone, and that the sentence says
// "how long" and "MP4" -- and every one of those assertions passed against a
// body that read:
//
//	{"error":"this file could not be read as media: polyemesis cannot work out
//	how long this file is (ffprobe read it as \"h264\" and reported no duration,
//	and it could not be counted: <nil>; re-save it as MP4 or MPEG-TS and upload
//	it again)"}
//
// The refusal was right. The diagnostic was not, and only the diagnostic tells
// an operator whether the fix is on their disk or on this server -- here it is
// "this install has no ffmpeg", which is a thing THEY CANNOT SEE from the file
// and cannot infer from "re-save it as MP4". A test that stops at "it was
// refused" cannot distinguish the two, which is exactly why this one is
// separate rather than three more lines in the test above.
//
// At this level rather than only in internal/ffmpeg because this is where the
// string actually reaches a person: internal/api renders the error with %s into
// the response body, so a chain that carries the cause correctly and a message
// that does not are indistinguishable to everything except a reader.
func TestTheRefusedRawStreamsBodySaysWhyTheCountFailed(t *testing.T) {
	ffprobe := ffprobeOrSkip(t)
	body := rawStreamBytes(t)
	// s.encodeBin deliberately left empty: an install with no ffmpeg to count
	// with, which is the configuration Bins documents as supported and refusing.
	_, h, _, auth := probeServer(t, ffprobe)

	r := uploadBytesRequest(t, "dump.h264", body)
	auth(r)
	w := do(t, h, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", w.Code, w.Body.String())
	}
	var got struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode error body: %v (%s)", err, w.Body.String())
	}
	// THE ASSERTION THE PR WAS MISSING, at the surface an operator sees.
	if strings.Contains(got.Error, "<nil>") {
		t.Errorf("the 400 body reports the cause of the failed count as <nil>:\n  %s\n"+
			"The operator is told the count failed and given nothing about why -- "+
			"and this is the feature whose entire purpose is counting the length of "+
			"a raw elementary stream", got.Error)
	}
	if !strings.Contains(got.Error, "no ffmpeg binary") {
		t.Errorf("the 400 body does not say the count failed for want of an ffmpeg:\n  %s\n"+
			"That is the actual cause here, it is a fact about THIS SERVER rather "+
			"than about the operator's file, and nothing else in the message hints "+
			"at it -- \"re-save it as MP4\" sends them to their own disk", got.Error)
	}
}
