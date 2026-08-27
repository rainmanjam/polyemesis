package automod

import "testing"

/* THE TWO VALUES THAT USED TO DESTROY THE FEATURE, in opposite directions and
 * both in silence. Neither is a hypothetical: 0 is what an unset JSON field
 * arrives as, and 80 is what reading a 0..1 scale as a percentage produces. */
func TestParseConfidenceRejectsTheSilentFailures(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   float64
		why  string
	}{
		{"unset or explicitly zero", 0,
			"a zero floor means every model opinion acts, including the ones it " +
				"is least sure about -- and zero is what the field arrives as when " +
				"nobody has set it"},
		{"read as a percentage", 80,
			"80 is above every verdict the model can return, so the checker " +
				"never acts again and nothing says so"},
		{"just above the scale", 1.0001,
			"anything over 1 retires the checker for the same reason 80 does"},
		{"negative", -0.5,
			"below zero is not a floor at all"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseConfidence(tc.in); err == nil {
				t.Fatalf("ParseConfidence(%v) was accepted; %s", tc.in, tc.why)
			}
		})
	}
}

func TestParseConfidenceAcceptsTheUsableRange(t *testing.T) {
	for _, in := range []float64{0.01, 0.5, 0.8, 1} {
		got, err := ParseConfidence(in)
		if err != nil {
			t.Fatalf("ParseConfidence(%v): %v", in, err)
		}
		if got.Float() != in {
			t.Fatalf("ParseConfidence(%v) = %v, want the value unchanged", in, got.Float())
		}
	}
}

/* THE CONTROL CASE. A guard that refuses everything passes the test it was
 * written for, so the range has to be shown to still admit the default the
 * product ships with -- otherwise this file would keep passing after someone
 * tightened MinUsableConfidence to something no install could use. */
func TestDefaultConfigSurvivesItsOwnGuard(t *testing.T) {
	def := DefaultModelConfig()
	if _, err := ParseConfidence(def.MinConfidence.Float()); err != nil {
		t.Fatalf("the shipped default %v does not pass ParseConfidence: %v",
			def.MinConfidence.Float(), err)
	}
}
