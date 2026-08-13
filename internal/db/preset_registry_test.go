// Two catalogues describe the same platforms: DestinationPresets here, which
// says what to create, and internal/services, which says what the platform
// accepts. Two catalogues drift, and this file is the seam that stops them.
//
// The drift that prompted it was not cosmetic. The Kick preset shipped
//
//	rtmps://fa723fc1b171.global-contribute.live-video.net
//
// -- one particular person's per-channel ingest host, in a public repository,
// with no application path. Every operator who picked the preset got somebody
// else's address in the wrong shape, and a Kick destination in that shape does
// not publish: Amazon IVS treats the stream key as the RTMP application name
// and drops the connection without explanation.

package db

import (
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/services"
)

// Proven able to fail against the committed tree by restoring the old Kick
// URL: the preset trips the same warning polyemesis now shows operators, which
// is the contradiction this test exists to name.
func TestNoPresetShipsAURLTheRegistryWarnsAbout(t *testing.T) {
	checked := 0
	for _, p := range DestinationPresets() {
		if p.URL == "" {
			continue // per-channel or per-event; the note carries the instructions
		}
		checked++
		for _, w := range services.AnalyseURL(p.URL) {
			t.Errorf("preset %q ships %s, which trips the warning the API shows "+
				"operators:\n  %s\n\nA preset is where an operator learns the "+
				"right shape. Shipping one the product warns about teaches the "+
				"wrong one.", p.ID, p.URL, w.Detail)
		}
	}
	if checked == 0 {
		t.Fatal("no preset URLs checked -- this test would pass on an empty catalogue")
	}
}

// A per-channel host cannot be prefilled for anyone, so no preset may carry
// one. The registry already knows which platforms those are; this asserts the
// two agree about it rather than trusting them to.
func TestNoPresetPrefillsAPerChannelIngestHost(t *testing.T) {
	for _, p := range DestinationPresets() {
		if p.Platform == "" || p.URL == "" {
			continue
		}
		svc, ok := services.Lookup(string(p.Platform))
		if !ok || !svc.PerChannelIngest {
			continue
		}
		t.Errorf("preset %q prefills %s, but %s issues its ingest host per "+
			"channel. Whatever address is here belongs to one particular "+
			"account, and shipping it sends every other operator to the wrong "+
			"host -- besides publishing that account's identifier.",
			p.ID, p.URL, svc.Name)
	}
}

// The complement: a platform with no prefill must say why, or the operator who
// picks it gets an empty box and no idea what belongs in it.
func TestAPresetWithNoURLExplainsItself(t *testing.T) {
	for _, p := range DestinationPresets() {
		if p.URL != "" || p.Kind == "" {
			continue
		}
		if strings.TrimSpace(p.Notes) == "" {
			t.Errorf("preset %q has no URL and no notes, so it offers the "+
				"operator an empty field and no instructions.", p.ID)
		}
	}
}

// Kick specifically, because it is the preset that failed and because the fix
// is a sentence rather than a value: the dashboard's URL needs completing.
func TestTheKickPresetSaysToAppendTheApplicationPath(t *testing.T) {
	var kick *DestinationPreset
	for i, p := range DestinationPresets() {
		if p.ID == "kick" {
			kick = &DestinationPresets()[i]
		}
	}
	if kick == nil {
		t.Fatal("no kick preset")
	}
	if kick.URL != "" {
		t.Errorf("the kick preset prefills %q; its host is per channel.", kick.URL)
	}
	if !strings.Contains(kick.Notes, "/app") {
		t.Errorf("the kick preset does not tell the operator to append /app.\n"+
			"Kick's dashboard prints the URL without one, so an operator who "+
			"copies it exactly gets a destination that cannot publish.\ngot: %s",
			kick.Notes)
	}
}
