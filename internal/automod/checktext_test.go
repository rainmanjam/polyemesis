package automod

import (
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
)

func textLimits() HistoryLimits {
	return HistoryLimits{Window: time.Minute, MaxMessages: 2, MaxRepeats: 1, MaxLinks: 1, MaxMentionsPerMessage: 2,
		MinLengthForCaps: 10, MaxCapsRatio: 0.6, Action: ActionHideLocal, Retain: 10, IdleEviction: time.Hour, MaxAuthors: 10}
}

// CheckText runs the per-message shape detectors with no author and records
// nothing, so repeating a text never becomes a window finding.
func TestCheckTextJudgesShapeAndRecordsNothing(t *testing.T) {
	h := NewHistory(textLimits())
	for name, text := range map[string]string{
		"caps": "THIS IS SHOUTING TEXT", "mentions": "@a @b @c hi", "links": "http://a.example http://b.example",
	} {
		if got := h.CheckText(text); len(got) == 0 || got[0].Checker != CheckerHistory || got[0].Action != ActionHideLocal {
			t.Errorf("%s: %+v", name, got)
		}
	}
	for i := 0; i < 20; i++ {
		if got := h.CheckText("thanks a lot"); len(got) != 0 {
			t.Fatalf("repeated clean text flagged on pass %d: %+v", i, got)
		}
	}
	if len(h.authors) != 0 {
		t.Error("CheckText recorded an author")
	}
	var nilH *History
	if nilH.CheckText("X") != nil {
		t.Error("nil history")
	}
}

func TestEngineCheckTextDecidesThroughTheMatrix(t *testing.T) {
	m := DefaultMatrix()
	m.Set(Key{Platform: db.PlatformTwitch, Action: ActionHideLocal, Checker: CheckerHistory}, true)
	e := New(m, PlatformCaps{}, nil, NewHistory(textLimits()), nil)
	v := e.CheckText(db.PlatformTwitch, "THIS IS SHOUTING TEXT")
	if len(v.Findings) == 0 || len(v.Act) == 0 {
		t.Errorf("verdict %+v", v)
	}
	if v := e.CheckText(db.PlatformKick, "THIS IS SHOUTING TEXT"); len(v.Findings) == 0 || len(v.Act) != 0 {
		t.Errorf("a platform the matrix did not enable acted: %+v", v)
	}
	var nilE *Engine
	if nilE.CheckText(db.PlatformTwitch, "x").Flagged() {
		t.Error("nil engine flagged")
	}
}
