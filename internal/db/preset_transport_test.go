package db

// THE PLAINTEXT RTMP PRESETS ARE OFFERED ON PURPOSE, AND THE TLS ONE MUST EXIST.
//
// Sonar reports typescript:S5332 seven times against the destination dialog --
// "using rtmp protocol is insecure, use rtmps instead". The dialog mirrors this
// catalogue, and the catalogue deliberately carries BOTH: youtube-rtmp beside
// youtube-rtmps, twitch beside twitch-rtmps. That is not an oversight to clean
// up. RTMP on 1935 is what several ingests and several corporate networks
// actually accept, and a self-hosted PeerTube or Owncast box may have no TLS
// ingest at all -- removing the plaintext presets would leave those operators
// pasting URLs by hand, which is worse for them and no better for anyone.
//
// What WOULD be a defect is offering a platform only over plaintext when it
// publishes a TLS ingest, or letting a preset's declared transport drift from
// the URL it actually carries. Both are pinned here.
//
// CONTACT lens: the mistake this closes is a preset whose Transport says one
// thing and whose URL says another -- a PresetRTMPS entry that quietly ships an
// rtmp:// address would read as secure everywhere in the UI while not being.

import (
	"strings"
	"testing"
)

func TestAPresetsTransportMatchesTheSchemeItActuallyCarries(t *testing.T) {
	var checked int
	for _, p := range DestinationPresets() {
		if p.URL == "" || strings.Contains(p.URL, "{") {
			continue // a template the operator completes
		}
		switch p.Transport {
		case PresetRTMPS:
			checked++
			if !strings.HasPrefix(p.URL, "rtmps://") {
				t.Errorf("%s declares Transport RTMPS but its URL is %q. Every screen "+
					"that reads Transport would call this secure; the bytes would not be.",
					p.ID, p.URL)
			}
		case PresetRTMP:
			checked++
			if !strings.HasPrefix(p.URL, "rtmp://") {
				t.Errorf("%s declares Transport RTMP but its URL is %q -- the transport "+
					"field is what the UI filters and sorts on, so a mismatch mislabels it.",
					p.ID, p.URL)
			}
		}
	}
	if checked < 5 {
		t.Fatalf("only %d presets carried a concrete RTMP/RTMPS URL; platforms.go is "+
			"committed data, so this is a regression in the catalogue rather than a "+
			"reason to stop checking", checked)
	}
}

func TestAPlatformWithAPublishedTLSIngestOffersIt(t *testing.T) {
	// Platforms whose plaintext preset Sonar flags, and which publish a TLS
	// ingest of their own. If a preset for one of these loses its RTMPS
	// sibling, operators are left with only the plaintext route to a service
	// that does not require it.
	want := map[Platform]bool{PlatformYouTube: false, PlatformTwitch: false}
	for _, p := range DestinationPresets() {
		if p.Transport == PresetRTMPS && strings.HasPrefix(p.URL, "rtmps://") {
			if _, ok := want[p.Platform]; ok {
				want[p.Platform] = true
			}
		}
	}
	for platform, found := range want {
		if !found {
			t.Errorf("the catalogue offers no RTMPS preset for %s, which publishes one. "+
				"The plaintext preset may stay -- some networks need it -- but it must "+
				"not be the only way to reach a platform that accepts TLS.", platform)
		}
	}
}
