package uploads

import (
	"path/filepath"
	"strings"
	"testing"
)

// A RAW ELEMENTARY STREAM KEEPS ITS EXTENSION, and this is not cosmetics.
//
// polyemesis accepts .h264/.hevc/.mpegvideo dumps as of #218, by counting their
// length rather than demanding it from a header a bitstream cannot have. Before
// this the stored name was "dump-a1b2c3.bin", because allowedExt listed only
// containers -- so the one class of file whose demuxer selection actually
// DEPENDS on its name was the class that lost it.
//
// An MP4 carries an ftyp box and an MKV an EBML header, so FFmpeg identifies
// them from their bytes whatever they are called. A raw H.264 stream is a start
// code followed by payload: no magic number, no header, and the demuxer is
// picked by a scoring heuristic over the first few kilobytes that the extension
// feeds. Keeping the extension makes that choice deterministic.
//
// THE CONTROL IS THE SECOND HALF OF THE TABLE. An allowlist that had simply
// stopped filtering would satisfy the first half alone, and that is the failure
// mode that matters here -- ".php" and ".sh" naming a file on disk is what this
// list exists to prevent, in a directory nothing executes from precisely
// because nobody should have to rely on that.
func TestARawElementaryStreamKeepsItsExtension(t *testing.T) {
	kept := []string{
		"dump.h264", "dump.264", "dump.hevc", "dump.h265", "dump.265",
		"dump.m2v", "dump.mpv",
		// Case is normalised, not rejected: an encoder that writes .H264 has
		// produced the same file.
		"DUMP.H264",
	}
	for _, hint := range kept {
		t.Run(hint, func(t *testing.T) {
			got := SafeName(hint)
			want := strings.ToLower(filepath.Ext(hint))
			if filepath.Ext(got) != want {
				t.Errorf("SafeName(%q) = %q, ext %q, want %q. A raw stream stored as "+
					".bin has lost the only hint FFmpeg has about which demuxer to "+
					"read it with", hint, got, filepath.Ext(got), want)
			}
		})
	}

	// STILL AN ALLOWLIST. Nothing above widened it into "keep whatever the
	// client sent".
	for _, hint := range []string{"payload.php", "script.sh", "x.h2640", "x.264x", "x.exe"} {
		t.Run(hint, func(t *testing.T) {
			if got := SafeName(hint); filepath.Ext(got) != ".bin" {
				t.Errorf("SafeName(%q) = %q, want a .bin extension: the raw-stream "+
					"entries must not have turned this list into a pass-through, and "+
					"they must not match as prefixes or suffixes either", hint, got)
			}
		})
	}
}
