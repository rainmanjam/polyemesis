package engine

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/captions"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/ffmpeg"
	"github.com/rainmanjam/polyemesis/internal/routing"
	"github.com/rainmanjam/polyemesis/internal/transcribe"
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

// TestATierThatBurnsCaptionsInIsEnoughToStartTheCaptioner is burn-in's own
// demand. Burn-in is the answer for a platform with no caption route (Kick, a
// custom RTMP server), so a programme whose only captions are a burned-in tier
// must still run the captioner, or the burn file stays empty forever. The tier
// counts only while an enabled destination uses it, and its setting is part of
// the intent's signature, so a save of it re-arms a degraded captioner.
//
// MUTATION: captionIntentNow ignores burned-in tiers. Observed: FAIL, the
// intent stayed off.
func TestATierThatBurnsCaptionsInIsEnoughToStartTheCaptioner(t *testing.T) {
	e := failoverEngine(t)
	src := e.sourceID
	rend, err := e.store.CreateRendition(&db.Rendition{Name: "720p", Width: 1280, Height: 720, FPS: 30,
		VideoBitrate: 3000, Encoder: db.EncoderX264, Preset: "veryfast", GOPSeconds: 2, SourceID: &src,
		CaptionBurn: db.CaptionBurnBottom})
	if err != nil {
		t.Fatalf("CreateRendition: %v", err)
	}
	dest, err := e.store.CreateDestination(&db.Destination{
		Name: "kick", Kind: db.DestRTMP, Platform: db.PlatformCustom,
		URL: "rtmp://127.0.0.1:1/live", StreamKey: "key", Enabled: false,
		AudioBitrate: 128, Profile: routing.DefaultProfile(), SourceID: &src, RenditionID: &rend.ID,
	})
	if err != nil {
		t.Fatalf("CreateDestination: %v", err)
	}
	off, err := e.captionIntentNow()
	if err != nil {
		t.Fatal(err)
	}
	if off.on {
		t.Fatal("a burned-in tier no enabled destination uses switched the captioner on")
	}
	if err := e.store.SetDestinationEnabled(dest.ID, true); err != nil {
		t.Fatalf("enable: %v", err)
	}
	on, err := e.captionIntentNow()
	if err != nil {
		t.Fatal(err)
	}
	if !on.on {
		t.Fatal("a burned-in tier an enabled destination uses did not switch the captioner on")
	}
	if len(on.targets) != 0 {
		t.Errorf("a burned-in tier became a caption target: %+v", on.targets)
	}
	rend.CaptionBurn = db.CaptionBurnTop
	if _, err := e.store.UpdateRendition(rend); err != nil {
		t.Fatalf("UpdateRendition: %v", err)
	}
	moved, err := e.captionIntentNow()
	if err != nil {
		t.Fatal(err)
	}
	if moved.sig == on.sig {
		t.Error("a burn-in save did not change the intent signature, so it cannot re-arm a degraded captioner")
	}
}

// TestABurnTierStartingWithNoCaptionerShowsNoLeftoverCaption covers a crash in
// a caption's hold window: the clear timer died with the process, so the burn
// file still holds the last caption, and a restarted tier would draw it with
// nobody speaking. A tier starting while no captioner runs for the programme
// therefore starts from an empty file; one starting beside a running captioner
// leaves the caption on screen alone.
//
// MUTATION: prepareBurnFile only ensures the file exists. Observed: FAIL, the
// leftover caption was kept.
func TestABurnTierStartingWithNoCaptionerShowsNoLeftoverCaption(t *testing.T) {
	e := failoverEngine(t)
	path := filepath.Join(t.TempDir(), "captions", "burn-source-1.txt")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("left over from before the crash"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := e.prepareBurnFile(path); err != nil {
		t.Fatalf("prepareBurnFile: %v", err)
	}
	if b, _ := os.ReadFile(path); len(b) != 0 {
		t.Fatalf("burn file after a start with no captioner = %q, want empty", b)
	}
	if err := os.WriteFile(path, []byte("on screen now"), 0o600); err != nil {
		t.Fatal(err)
	}
	e.capt = &transcribe.LiveCaptioner{}
	if err := e.prepareBurnFile(path); err != nil {
		t.Fatalf("prepareBurnFile: %v", err)
	}
	if b, _ := os.ReadFile(path); string(b) != "on screen now" {
		t.Fatalf("a tier starting beside a running captioner blanked its caption: %q", b)
	}
	e.capt = nil
}
