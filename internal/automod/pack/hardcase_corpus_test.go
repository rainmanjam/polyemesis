package pack

// Signal recall on the corpus hardcase slice (spec section 6.2, D14).
//
// Under the default "hard" scope a message the signals miss never reaches Jev,
// so a miss here is a message the operator's paid model is never shown. The
// gate is per signal, on the lines Jev should flag (jev != none), with
// thresholds in testdata/targets.json. A second gate holds the false-hit rate
// on the benign slices, because a signal on ordinary chat costs a paid call and
// an overlay wait.
//
// Failure messages name file, line and signal, never a line's text.

import (
	"testing"
)

// corpusSignalNames maps the corpus's signal labels to the engine's.
var corpusSignalNames = map[string]Signal{
	"address_violence":    SignalThreat,
	"obfuscation_residue": SignalObfuscation,
	"self_harm":           SignalSelfHarm,
	"sexual_address":      SignalSexualAddressed,
	"insult_reply":        SignalInsultReply,
	"contextual_hate":     SignalContextualHate,
}

// signalsFor derives the signals for one corpus line the way the engine does:
// the checker's own findings feed ContextualHate and TermHit.
func signalsFor(t testing.TB, h *HardCase, chk *Checker, l corpusLine) []Signal {
	t.Helper()
	in := SignalInput{Raw: l.Text}
	for _, hit := range chk.Inspect(Message{Platform: l.Platform, AuthorName: l.Author, Text: l.Text}) {
		switch hit.Group {
		case GroupHateContextual:
			in.ContextualHate, in.TermHit = true, true
		case GroupHateSevere, GroupProfanityMild, GroupProfanityStrong:
			in.TermHit = true
		}
	}
	return h.Signals(NewForms(l.Text), in)
}

func TestCorpusHardCaseSignalRecall(t *testing.T) {
	tg := loadTargets(t)
	if len(tg.HardCaseRecall) == 0 {
		t.Fatal("targets.json has no hardcaseRecall thresholds")
	}
	h := hc(t)
	chk, err := New(corpusConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	type tally struct{ want, got int }
	by := map[string]*tally{}
	for _, l := range loadCorpus(t) {
		if l.Slice != "hardcase" || l.Jev == "none" {
			continue
		}
		want, ok := corpusSignalNames[l.Signal]
		if !ok {
			t.Errorf("%s: unknown signal %q", where(l), l.Signal)
			continue
		}
		c := by[l.Signal]
		if c == nil {
			c = &tally{}
			by[l.Signal] = c
		}
		c.want++
		if has(signalsFor(t, h, chk, l), want) {
			c.got++
		} else {
			t.Logf("miss: %s signal %s", where(l), l.Signal)
		}
	}
	for sig, min := range tg.HardCaseRecall {
		c := by[sig]
		if c == nil || c.want == 0 {
			t.Errorf("signal %s: no hardcase lines to measure", sig)
			continue
		}
		recall := float64(c.got) / float64(c.want)
		t.Logf("signal %-20s recall %d/%d = %.2f (target %.2f)", sig, c.got, c.want, recall, min)
		if recall < min {
			t.Errorf("signal %s recall %.2f is below the target %.2f", sig, recall, min)
		}
	}
}

// A signal on ordinary chat costs a paid call and an overlay wait, so the
// benign slices are held to a ceiling.
func TestCorpusHardCaseSignalsStayOffBenignChat(t *testing.T) {
	tg := loadTargets(t)
	h := hc(t)
	chk, err := New(corpusConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	benign := map[string]bool{}
	for _, s := range tg.BenignSlices {
		benign[s] = true
	}
	total, hit := 0, 0
	byslice := map[string]int{}
	for _, l := range loadCorpus(t) {
		if !benign[l.Slice] {
			continue
		}
		total++
		if sigs := signalsFor(t, h, chk, l); len(sigs) > 0 {
			hit++
			byslice[l.Slice]++
			t.Logf("benign hit: %s signals %v", where(l), sigs)
		}
	}
	t.Logf("benign signal rate %d/%d", hit, total)
	if total == 0 {
		t.Fatal("no benign lines")
	}
	if rate := float64(hit) / float64(total); rate > tg.HardCaseBenignCeiling {
		t.Errorf("benign signal rate %.4f exceeds the ceiling %.4f (%v)", rate, tg.HardCaseBenignCeiling, byslice)
	}
}
