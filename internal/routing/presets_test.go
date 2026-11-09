package routing

import (
	"errors"
	"testing"
)

// A preset that selects no audio against this ingest is a typed condition, so a
// caller that applies presets as levels can tell "would silence" from a real
// error without reading the message. The message itself is unchanged.
func TestApplyPresetSilenceMatchesErrPresetSilent(t *testing.T) {
	one := Source{Tracks: []Track{{Index: 0, Channels: 2}}}
	_, err := ApplyPreset(PresetNoMusic, one, PresetOpts{MusicTrack: 0})
	if !errors.Is(err, ErrPresetSilent) {
		t.Fatalf("err = %v, want it to match ErrPresetSilent", err)
	}
	if want := `preset "no-music" would leave no audio: the ingest only carries the excluded track`; err.Error() != want {
		t.Errorf("message changed: %q", err.Error())
	}
	if _, err := ApplyPreset("no-such-preset", one, PresetOpts{}); err == nil || errors.Is(err, ErrPresetSilent) {
		t.Errorf("an unknown preset is a real error, got %v", err)
	}
}
