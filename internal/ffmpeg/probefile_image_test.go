package ffmpeg

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// onePixelPNG is a real 1x1 PNG. Base64 rather than a binary fixture so the
// bytes are readable in the diff and cannot be quietly swapped for something
// else.
func onePixelPNG(t *testing.T) []byte {
	t.Helper()
	b, err := base64.StdEncoding.DecodeString(
		"iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==")
	if err != nil {
		t.Fatalf("decode the PNG fixture: %v", err)
	}
	return b
}

// #182 -- filed against a PNG reaching the media library, and #203 item 3 says
// it is stale because the format allowlist refuses image formats. It is stale,
// but NOT for the reason recorded there: #203 says
// TestProbeFileRefusesWhatIsNotMedia pins it, and that table has no image in
// it. Nothing anywhere pinned this. So the behaviour is right and the pin is
// this file.
//
// IT ASSERTS THE SPECIFIC REFUSAL, not "refused somehow". A generic error would
// be satisfied by an ffprobe that simply could not read the fixture, which
// would leave the allowlist untested and would go on passing if "png_pipe"
// were ever added to selfContainedFormats.
//
// BOTH EXTENSIONS, because the extension was never the check and a single
// still is detected under a different demuxer name from a numbered sequence:
// this repository's FFmpeg reports "png_pipe" for one PNG and reserves
// "image2" for the sequence case, which is why indirectFormats naming image2
// is not on its own an answer about a single file.
func TestProbeFileRefusesAStillImage(t *testing.T) {
	ffprobe := needFFmpeg(t, "ffprobe")[0]
	dir := t.TempDir()
	png := onePixelPNG(t)

	for _, name := range []string{"logo.png", "clip.mp4"} {
		t.Run(name, func(t *testing.T) {
			p := filepath.Join(dir, name)
			if err := os.WriteFile(p, png, 0o600); err != nil {
				t.Fatalf("write %s: %v", name, err)
			}
			res, err := ProbeFile(context.Background(), ffprobe, p)
			if !errors.Is(err, ErrUnsupportedContainer) {
				t.Fatalf("a PNG named %q was not refused as an unsupported container: "+
					"err=%v res=%+v -- an image in the Library looks like a video to "+
					"every consumer of the listing", name, err, res)
			}
			// And it is a refusal the handler turns into a rejection rather
			// than into "stored unchecked": Refused owns that distinction.
			if !Refused(err) {
				t.Errorf("ffmpeg.Refused says a PNG is not a verdict about the file, "+
					"so api.probeUpload would store it unchecked instead of refusing it")
			}
			if res != nil {
				t.Errorf("a refused image still produced a result: %+v", res)
			}
		})
	}
}
