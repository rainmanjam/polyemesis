package transcribe

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/ffmpeg"
)

// The operator's model choice has to REACH the worker.
//
// transcribe.WithDefaultModel existed from the first release and nothing ever
// called it, so the hardware guess was the only answer an install could get --
// the survey in docs/roadmap/UNREACHABLE-KNOBS.md rated wiring it High for
// exactly that reason. It is wired now, and this is what stops it coming
// unwired: an Option that is accepted and then overwritten by the fallback is
// indistinguishable, from outside, from one that was never passed.
func TestAChosenDefaultModelSurvivesConstruction(t *testing.T) {
	p := NewProcessor(slog.New(slog.NewTextHandler(io.Discard, nil)),
		&ffmpeg.Tools{}, nil, t.TempDir(), t.TempDir(),
		WithDefaultModel("ggml-large-v3"))

	if p.defaultModel != "ggml-large-v3" {
		t.Fatalf("defaultModel = %q, want the operator's choice. The hardware "+
			"fallback has overwritten it, so the setting is stored and does "+
			"nothing.", p.defaultModel)
	}
}

// The negative half. An install that has NOT chosen must still get the
// hardware-derived answer, which is what every install ran before the setting
// was reachable.
func TestNoChoiceStillGetsTheHardwareDefault(t *testing.T) {
	p := NewProcessor(slog.New(slog.NewTextHandler(io.Discard, nil)),
		&ffmpeg.Tools{}, nil, t.TempDir(), t.TempDir())

	if p.defaultModel == "" {
		t.Fatal("no default model at all; a job naming none would resolve an " +
			"empty path rather than the machine's own choice")
	}
}

// And that the SETTING reaches the Option in the first place.
//
// The two halves fail differently and neither implies the other: the test above
// proves the Option survives NewProcessor, this proves cmd/polyemesis still
// passes the stored setting into it. Deleting that line would leave every test
// in this package green while the picker in Settings did nothing at all --
// which is precisely the state this knob was in for the whole of its first
// release.
func TestTheStoredSettingIsPassedToTheProcessor(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "cmd", "polyemesis", "postprod.go"))
	if err != nil {
		t.Fatalf("cannot read postprod.go: %v", err)
	}
	if !strings.Contains(string(raw), "transcribe.WithDefaultModel(m)") {
		t.Error("cmd/polyemesis no longer passes the stored WhisperModel into " +
			"the processor, so the model picker in Settings is decorative.")
	}
	if !strings.Contains(string(raw), "pp.WhisperModel") {
		t.Error("the value passed to WithDefaultModel no longer comes from the " +
			"stored post-production settings.")
	}
}
