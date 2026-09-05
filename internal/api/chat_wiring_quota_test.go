package api

import (
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// THE ONE LINE THE COMPILER CANNOT CHECK.
//
// chat.YouTubeConfig holds the allowance and the reserve as two distinct types
// precisely so that writing them into that literal the wrong way round does not
// build. But db.ChatSettings holds both as int, so a conversion has to happen
// somewhere, and youtubeQuotaPair is where -- which makes it the last place in
// this path where a swap still compiles.
//
// A swap there is not cosmetic. 10,000 units with a 200 reserve becoming a 200
// allowance with a 10,000 reserve reaches clampQuota, which drops the
// impossible reserve and leaves the adapter pacing against 200 units a day:
// roughly a hundred times slower than the install is entitled to, with a
// healthy-looking chat pane and nothing in the logs. That is #732 exactly,
// restored in silence by a two-word edit.
//
// The two numbers below are chosen so that no swap can look right: they differ
// by three orders of magnitude, and neither is a default.
func TestYouTubeQuotaPairKeepsTheAllowanceAndTheReserveApart(t *testing.T) {
	cs := db.ChatSettings{
		YouTubeQuotaUnits:   1_000_000,
		YouTubeQuotaReserve: 500,
	}

	units, reserve := youtubeQuotaPair(cs)

	if int(units) != cs.YouTubeQuotaUnits {
		t.Fatalf("the allowance arrived as %d, want %d -- the stored reserve is %d, "+
			"so this is the pair the wrong way round", units, cs.YouTubeQuotaUnits, cs.YouTubeQuotaReserve)
	}
	if int(reserve) != cs.YouTubeQuotaReserve {
		t.Fatalf("the reserve arrived as %d, want %d", reserve, cs.YouTubeQuotaReserve)
	}
}

// The saved allowance has to survive the trip from the settings row to the
// values chatAdapter hands NewYouTube, defaults included: youtubeQuota falls
// back to db.DefaultSettings when the store cannot be read, and a pair that
// went astray there would pace every install that ever had a bad read.
func TestYouTubeQuotaPairCarriesTheDefaultsThroughUnchanged(t *testing.T) {
	cs := db.DefaultSettings().Chat

	units, reserve := youtubeQuotaPair(cs)

	if int(units) != cs.YouTubeQuotaUnits || int(reserve) != cs.YouTubeQuotaReserve {
		t.Fatalf("the default pair (%d, %d) arrived as (%d, %d)",
			cs.YouTubeQuotaUnits, cs.YouTubeQuotaReserve, units, reserve)
	}
	// A positive control on the fixture itself: if the defaults were ever both
	// zero, or equal, the assertion above would hold no matter what this
	// function did with them.
	if cs.YouTubeQuotaUnits == cs.YouTubeQuotaReserve {
		t.Fatalf("the default allowance and reserve are both %d, so this test "+
			"cannot tell them apart and proves nothing", cs.YouTubeQuotaUnits)
	}
}
