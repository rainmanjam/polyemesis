package db

import "testing"

// TestEveryVerticalTierIsPortraitAndPassesRenditionValidation pins the tier list the
// YouTube vertical half picks from (#7). A tier that is landscape, or that fails the
// same Validate every other rendition passes, would be a vertical half that either
// publishes the wrong shape or refuses to start.
func TestEveryVerticalTierIsPortraitAndPassesRenditionValidation(t *testing.T) {
	tiers := YouTubeVerticalTiers()
	if len(tiers) == 0 {
		t.Fatal("no vertical tiers are offered; the vertical half would have nothing to pick")
	}
	for _, tier := range tiers {
		r := tier.Rendition
		if r == nil {
			t.Fatalf("tier %q has no rendition template", tier.Key)
		}
		if r.Height <= r.Width {
			t.Fatalf("tier %q is %dx%d, which is not portrait; the vertical half must be 9:16", tier.Key, r.Width, r.Height)
		}
		if err := r.Validate(); err != nil {
			t.Fatalf("tier %q fails rendition validation: %v", tier.Key, err)
		}
	}
}

// TestVerticalTiersNeverOfferPassthrough pins the rule that the vertical half always
// re-encodes. Passthrough copies the source's shape, which for a landscape source is the
// wrong shape for a vertical destination.
func TestVerticalTiersNeverOfferPassthrough(t *testing.T) {
	for _, tier := range YouTubeVerticalTiers() {
		if tier.Passthrough || tier.Rendition == nil {
			t.Fatalf("tier %q is passthrough; a vertical half must always re-encode to 9:16", tier.Key)
		}
	}
}
