package engine

import (
	"path/filepath"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/captions"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/ffmpeg"
)

// TestARenditionThatBurnsCaptionsDrawsItsProgrammesBurnFile maps the stored
// burn-in setting onto the encode (#8). A tier set to burn captions draws the
// programme's burn file with the built-in font, at the position chosen; a tier
// set off draws nothing; and the setting is part of the tier's signature, so a
// save restarts the encode with it.
//
// MUTATION: renditionSpecOf leaves Captions unset. Observed: FAIL, no burn-in
// on the bottom tier.
func TestARenditionThatBurnsCaptionsDrawsItsProgrammesBurnFile(t *testing.T) {
	dataDir := t.TempDir()
	if err := ffmpeg.EnsureFonts(filepath.Join(dataDir, ffmpeg.FontsDirName)); err != nil {
		t.Fatal(err)
	}
	src := int64(4)
	r := &db.Rendition{Name: "720p", Width: 1280, Height: 720, VideoBitrate: 3000, SourceID: &src,
		CaptionBurn: db.CaptionBurnBottom}
	spec := renditionSpecOf(r, "udp://in", "udp://out", 30, "", dataDir)
	if spec.Captions == nil {
		t.Fatal("a tier set to burn captions has no caption burn-in")
	}
	if want := captions.BurnPath(dataDir, src); spec.Captions.TextFile != want {
		t.Errorf("burn-in reads %q, want the programme's burn file %q", spec.Captions.TextFile, want)
	}
	if spec.Captions.Anchor != ffmpeg.AnchorBottomCenter || !spec.Captions.Active() {
		t.Errorf("bottom burn-in = %+v, want an active spec at the bottom centre", spec.Captions)
	}
	r.CaptionBurn = db.CaptionBurnTop
	if top := renditionSpecOf(r, "udp://in", "udp://out", 30, "", dataDir); top.Captions == nil ||
		top.Captions.Anchor != ffmpeg.AnchorTopCenter {
		t.Errorf("top burn-in = %+v, want the top centre", top.Captions)
	}
	bottomSig := renditionSig(&db.Rendition{Name: "720p", Width: 1280, Height: 720, VideoBitrate: 3000,
		SourceID: &src, CaptionBurn: db.CaptionBurnBottom}, 30, "", dataDir)
	r.CaptionBurn = db.CaptionBurnOff
	if off := renditionSpecOf(r, "udp://in", "udp://out", 30, "", dataDir); off.Captions != nil {
		t.Errorf("a tier with burn-in off draws captions: %+v", off.Captions)
	}
	if renditionSig(r, 30, "", dataDir) == bottomSig {
		t.Error("switching burn-in off does not change the tier's signature, so the encode keeps drawing captions")
	}
}
