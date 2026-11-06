package api

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/clipper"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/ffmpeg"
)

// libassNoise is what FFmpeg prints when a captioned short fails: libass's
// font chatter -- a non-font file in the fonts directory, and one line per
// caption character no font has, naming its code point -- ahead of the real
// failure. The second subtitles line stands in for anything libass might echo
// of an event.
const libassNoise = "[Parsed_subtitles_4 @ 0x1] Error opening memory font 'LICENSE-Inter.txt'\n" +
	"[Parsed_subtitles_4 @ 0x1] fontselect: failed to find any fallback with glyph 0x4F60 for font: (Inter, 700, 0)\n" +
	"[Parsed_subtitles_4 @ 0x1] Event at 1200, 2400: hello there\n" +
	"[vost#0:0 @ 0x2] Unknown encoder 'h264_nonexistent264'\n" +
	"Error opening output files: Encoder not found"

// A FAILED SHORT SAYS WHY WITHOUT SAYING WHAT WAS SAID. A captioned short whose
// FFmpeg fails records FFmpeg's own failure as the job's error -- and a read
// token, which may list jobs but never read a transcript, sees that failure
// and no caption code point, no libass line, no caption text.
//
// MUTATION: the failure is not scrubbed. Observed: FAIL, "glyph 0x4F60" and
// "hello there" in the job a read token reads.
func TestAFailedShortsErrorCarriesNoCaptionContent(t *testing.T) {
	h, sign, srv, rec := highlightFixture(t)
	var job struct {
		ID int64 `json:"id"`
	}
	decodeInto(t, send(t, h, sign, http.MethodPost, recPath(rec, "/export"), map[string]any{
		"inMs": 10_000, "outMs": 20_000, "audioMode": "tracks", "tracks": []int{0},
		"vertical": map[string]any{"layout": "center", "captions": map[string]any{"enabled": true}},
	}, http.StatusAccepted), &job)

	encodes := 0
	run := func(_ context.Context, name string, args []string) ([]byte, error) {
		if strings.Contains(name, "ffprobe") {
			return nil, fmt.Errorf("no probe in this test")
		}
		encodes++
		return []byte(libassNoise), fmt.Errorf("exit status 8: %s", libassNoise)
	}
	cutter := clipper.New(srv.log, "ffmpeg", "ffprobe", clipper.WithRunner(run))
	if err := srv.jobq.Register(clipper.JobKind, 1, clipper.NewWorker(cutter, nil,
		clipper.WithTranscript(clipper.StoreTranscript(srv.store)),
		clipper.WithCaptionFonts(func(name string) (string, error) { return ffmpeg.FontPath(srv.cfg.FontsDir(), name) }),
		clipper.WithCaptionBurn(true))); err != nil {
		t.Fatal(err)
	}
	srv.jobq.Tick(context.Background())
	var text string
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if j, err := srv.store.GetJob(job.ID); err == nil && j.Error != "" {
			text = j.Error
			break
		}
	}
	if text == "" {
		t.Fatal("the job never failed")
	}

	read := createScopedToken(t, h, sign, "monitoring", db.ScopeRead)
	r := jsonRequest(t, http.MethodGet, fmt.Sprintf("/api/v1/jobs/%d", job.ID), nil)
	bearer(read)(r)
	w := do(t, h, r)
	if w.Code != http.StatusOK {
		t.Fatalf("a read token got %d listing its job: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	var view struct {
		Error string   `json:"error"`
		Log   []string `json:"log"`
	}
	decodeInto(t, w.Body.Bytes(), &view)
	// POSITIVE CONTROL: the real failure is there, so the absences mean
	// something.
	if !strings.Contains(view.Error, "Encoder not found") {
		t.Fatalf("the job's error lost FFmpeg's real failure: %q", view.Error)
	}
	for _, leak := range []string{"0x4F60", "glyph", "fontselect", "Parsed_subtitles", "hello there", "LICENSE-Inter"} {
		if strings.Contains(body, leak) {
			t.Errorf("a read token sees %q in the failed job: error %q, log %q", leak, view.Error, view.Log)
		}
	}
}
