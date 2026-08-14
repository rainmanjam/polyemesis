package engine

import (
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/ffmpeg"
)

// A rate-control value set on the stored rendition reaches the command line.
//
// This is #341, and the bug it closes is unusual in that nothing was broken:
// ffmpeg.RenditionSpec had MaxrateKbps and BufsizeKbps, RenditionArgs used them
// correctly, and the fields were reachable from nowhere. renditionSpecOf mapped
// VideoKbps and stopped, so both always fell through RenditionArgs' own
// defaults to the CBR relationship. The code described a capability the product
// did not have.
//
// The test therefore runs the WHOLE path -- stored row, through the mapping,
// into the argv -- because every individual piece already worked. Asserting on
// RenditionSpec alone would have passed before the fix.
func TestRateControlOnAStoredRenditionReachesTheArgv(t *testing.T) {
	argvFor := func(r *db.Rendition) []string {
		t.Helper()
		spec := renditionSpecOf(r, "udp://in", "udp://out", 30, "", t.TempDir())
		return ffmpeg.RenditionArgs(spec)
	}

	t.Run("a ceiling above the target survives to -maxrate", func(t *testing.T) {
		args := argvFor(&db.Rendition{
			VideoBitrate: 4500,
			MaxrateKbps:  8000,
			BufsizeKbps:  12000,
			GOPSeconds:   2,
		})
		for flag, want := range map[string]string{
			"-b:v":     "4500k",
			"-maxrate": "8000k",
			"-bufsize": "12000k",
		} {
			if got := argAfter(args, flag); got != want {
				t.Errorf("%s = %q, want %q — the stored value did not reach the encoder, which is the whole of #341\n  argv: %s",
					flag, got, want, strings.Join(args, " "))
			}
		}
	})

	// The default has to be byte-for-byte what it was, or #341 becomes a change
	// to every existing install rather than a new capability nobody asked for
	// yet.
	t.Run("zero still derives the CBR relationship", func(t *testing.T) {
		args := argvFor(&db.Rendition{VideoBitrate: 4500, GOPSeconds: 2})
		for flag, want := range map[string]string{
			"-b:v":     "4500k",
			"-maxrate": "4500k", // = bitrate
			"-bufsize": "9000k", // = 2 x maxrate
		} {
			if got := argAfter(args, flag); got != want {
				t.Errorf("%s = %q, want %q — an install that sets neither field must emit the command line it always did",
					flag, got, want)
			}
		}
	})
}

// A ceiling below the target is refused rather than quietly rewritten.
//
// Clamping is the tempting alternative and it is wrong: there is no way to
// resolve `-b:v 6000 -maxrate 4000` without overriding one of the two, and
// whichever is picked, the operator gets a stream at a bitrate they did not
// choose with no indication that a number they typed was ignored.
func TestARateCeilingBelowTheTargetIsRefused(t *testing.T) {
	tests := []struct {
		name    string
		r       db.Rendition
		wantErr bool
	}{
		{"zero derives, and is always legal", db.Rendition{MaxrateKbps: 0}, false},
		{"equal to the target is CBR, stated explicitly", db.Rendition{MaxrateKbps: 6000}, false},
		{"above the target allows burst", db.Rendition{MaxrateKbps: 9000}, false},
		{"below the target is a contradiction", db.Rendition{MaxrateKbps: 4000}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := tt.r
			r.Name, r.VideoBitrate, r.GOPSeconds = "tier", 6000, 2
			r.Encoder, r.Preset = db.EncoderX264, "veryfast"
			r.Height = 720

			err := r.Validate()
			if tt.wantErr && err == nil {
				t.Fatalf("maxrate %d against a %d target was accepted; the encoder cannot average above its own ceiling",
					r.MaxrateKbps, r.VideoBitrate)
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("maxrate %d against a %d target was refused: %v", r.MaxrateKbps, r.VideoBitrate, err)
			}
			// The message has to name both numbers, or the operator cannot see
			// which of the two fields to change.
			if tt.wantErr {
				msg := err.Error()
				if !strings.Contains(msg, "4000") || !strings.Contains(msg, "6000") {
					t.Errorf("the refusal named neither number, so it cannot be acted on: %q", msg)
				}
			}
		})
	}
}

func argAfter(args []string, flag string) string {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}
