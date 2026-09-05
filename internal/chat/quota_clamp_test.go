package chat

import "testing"

// ONE STORED PAIR, ONE BUDGET, WHICHEVER DOOR IT COMES IN THROUGH.
//
// The two doors are construction (NewYouTube at boot) and setLimits (a settings
// save reaching a connection that is already up). They read the same two
// numbers out of the same settings row, so an install where they disagree is an
// install whose behaviour changes when the operator presses Save without
// changing anything -- the least explicable bug there is.
//
// They did disagree. NewYouTube mapped a zero reserve to DefaultQuotaReserve on
// the way past, before calling newBudget; setLimits went straight to
// clampQuota, which kept the zero. So the stored pair (10,000, 0) -- which is
// what every row written before the reserve field existed holds -- meant a
// 200-unit reserve at boot and no reserve at all after a save, and the
// operator's ability to say "we are moving to Twitch" on stream disappeared
// silently the first time they touched Settings.
//
// The wanted values are spelled out per row rather than only compared between
// the two doors, because a clampQuota that returned (0, 0) for everything would
// make the two doors agree perfectly and would be completely wrong.
func TestClampQuotaIsTheOnlyRuleAndBothDoorsObeyIt(t *testing.T) {
	cases := []struct {
		name        string
		limit       QuotaUnits
		reserve     QuotaReserve
		wantLimit   int
		wantReserve int
	}{
		{
			name:        "a zero reserve means unset, not spend everything on reading",
			limit:       10_000,
			reserve:     0,
			wantLimit:   10_000,
			wantReserve: DefaultQuotaReserve,
		},
		{
			name:        "a zero allowance means unset as well",
			limit:       0,
			reserve:     0,
			wantLimit:   DefaultQuotaUnits,
			wantReserve: DefaultQuotaReserve,
		},
		{
			name:        "a negative reserve cannot be honoured and is not a refusal either",
			limit:       10_000,
			reserve:     -1,
			wantLimit:   10_000,
			wantReserve: DefaultQuotaReserve,
		},
		{
			name:        "a reserve equal to the allowance would pause reading forever",
			limit:       5_000,
			reserve:     5_000,
			wantLimit:   5_000,
			wantReserve: 0,
		},
		{
			name:        "a reserve larger than the allowance, which is what a swapped pair looks like",
			limit:       200,
			reserve:     10_000,
			wantLimit:   200,
			wantReserve: 0,
		},
		{
			name:        "an allowance too small to afford the default reserve keeps none",
			limit:       100,
			reserve:     0,
			wantLimit:   100,
			wantReserve: 0,
		},
		{
			name:        "an ordinary raised allowance passes through untouched",
			limit:       1_000_000,
			reserve:     500,
			wantLimit:   1_000_000,
			wantReserve: 500,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			built := newBudget(tc.limit, tc.reserve, nil)
			built.mu.Lock()
			bLimit, bReserve := built.limit, built.reserve
			built.mu.Unlock()

			if bLimit != tc.wantLimit || bReserve != tc.wantReserve {
				t.Fatalf("newBudget(%d, %d) = limit %d reserve %d, want %d/%d",
					tc.limit, tc.reserve, bLimit, bReserve, tc.wantLimit, tc.wantReserve)
			}

			// The save door starts from an unrelated budget, so anything that
			// merely survived from construction cannot be mistaken for the
			// answer setLimits produced.
			saved := newBudget(777_777, 333, nil)
			saved.setLimits(tc.limit, tc.reserve)
			saved.mu.Lock()
			sLimit, sReserve := saved.limit, saved.reserve
			saved.mu.Unlock()

			if sLimit != tc.wantLimit || sReserve != tc.wantReserve {
				t.Fatalf("setLimits(%d, %d) = limit %d reserve %d, want %d/%d",
					tc.limit, tc.reserve, sLimit, sReserve, tc.wantLimit, tc.wantReserve)
			}
			if sLimit != bLimit || sReserve != bReserve {
				t.Fatalf("construction and a settings save disagree from the same stored pair "+
					"(%d, %d): new(%d, %d) set(%d, %d) -- this is chat behaving differently "+
					"after Save than after a restart", tc.limit, tc.reserve, bLimit, bReserve, sLimit, sReserve)
			}
		})
	}
}

// The adapter is the caller that actually holds a stored pair, so the agreement
// above is asserted once more through it: NewYouTube must produce the same
// budget SetQuota would, from the same two numbers. This is the arm that fails
// if the normalisation ever creeps back upstream into NewYouTube.
func TestNewYouTubeAndSetQuotaBuildTheSameBudgetFromTheSamePair(t *testing.T) {
	const units, reserve = 10_000, 0

	fresh := ytAdapter(t, "http://example.invalid", func(c *YouTubeConfig) {
		c.QuotaUnits = units
		c.QuotaReserve = reserve
	})
	saved := ytAdapter(t, "http://example.invalid", func(c *YouTubeConfig) {
		c.QuotaUnits = 1_000_000
		c.QuotaReserve = 9_999
	})
	saved.SetQuota(units, reserve)

	fresh.budget.mu.Lock()
	fLimit, fReserve := fresh.budget.limit, fresh.budget.reserve
	fresh.budget.mu.Unlock()

	saved.budget.mu.Lock()
	sLimit, sReserve := saved.budget.limit, saved.budget.reserve
	saved.budget.mu.Unlock()

	if fLimit != sLimit || fReserve != sReserve {
		t.Fatalf("a fresh adapter and a saved one disagree from (%d, %d): new(%d, %d) saved(%d, %d)",
			units, reserve, fLimit, fReserve, sLimit, sReserve)
	}
	// And the shared answer is the right one: a stored zero reserve still keeps
	// the operator able to speak. Without this the test would pass on two
	// adapters that both lost the reserve.
	if fReserve != DefaultQuotaReserve {
		t.Fatalf("reserve = %d from a stored zero, want the default %d -- "+
			"the operator can no longer reply on stream", fReserve, DefaultQuotaReserve)
	}
}
