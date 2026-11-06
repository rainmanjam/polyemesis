package automod

import "fmt"

// Confidence is a model's certainty on the 0..1 scale it actually reports.
//
// A TYPE RATHER THAN A float64, because the float64 was assigned without a
// guard while the field directly below it in the same struct literal carried
// one -- so the hazard was understood on that line and not on this one, which
// is exactly the asymmetry a shared type removes. Two values pass a plain
// float64 and destroy the feature in opposite directions:
//
//   - 0, whether typed deliberately or arriving because the field was never
//     set, removes the floor entirely. Every opinion the model has then acts,
//     including the ones it is least sure about.
//   - 80, from someone reading the scale as a percentage, is above every
//     verdict the model can return, so the checker silently never acts again.
//
// Both are silent: moderation gets more aggressive or stops, and nothing on
// screen says which. Neither is representable here.
type Confidence float64

// MinUsableConfidence is the lowest floor that still means something. Zero is
// excluded on purpose -- see the type comment; "act on everything" is a
// setting nobody reaches for deliberately and several people reach by accident.
const MinUsableConfidence Confidence = 0.01

// ParseConfidence converts a number that has come from outside -- JSON, a
// form, a config file -- into a floor that can be trusted.
//
// It is the ONLY way to make a Confidence from a float, so a caller cannot
// copy a raw number into a config the way modelConfigFrom used to.
func ParseConfidence(v float64) (Confidence, error) {
	c := Confidence(v)
	if c < MinUsableConfidence || c > 1 {
		return 0, fmt.Errorf(
			"confidence floor %v is outside 0.01..1; the model reports certainty on a "+
				"0..1 scale, so 0 would act on every opinion it has and anything above 1 "+
				"would stop it acting at all", v)
	}
	return c, nil
}

// Float returns the underlying number, for comparison against a verdict.
func (c Confidence) Float() float64 { return float64(c) }
