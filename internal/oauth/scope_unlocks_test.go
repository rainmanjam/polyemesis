package oauth

import "testing"

// A ScopeVersion bump forces every connected operator to reconnect, and the
// console's "reconnect to enable" banner has to say what for. The note lives in
// the provider's ScopeUnlocks, beside ScopeVersion; this fails a bump that
// forgot it. An empty value is the explicit "no banner for this bump".
func TestEveryBumpedScopeVersionHasAnUnlockNote(t *testing.T) {
	for plat, p := range Providers() {
		cur := p.ScopeVersion()
		if cur <= 1 {
			continue // version 1 is the first grant, not a bump
		}
		n, ok := p.(ScopeNoter)
		if !ok {
			t.Errorf("%s is at ScopeVersion %d but has no ScopeUnlocks; add one beside ScopeVersion", plat, cur)
			continue
		}
		notes := n.ScopeUnlocks()
		if _, has := notes[cur]; !has {
			t.Errorf("%s: ScopeVersion is %d but ScopeUnlocks has no entry for it (use \"\" to opt out of the banner)", plat, cur)
		}
		for v := range notes {
			if v > cur {
				t.Errorf("%s: ScopeUnlocks has an entry for version %d, ahead of ScopeVersion %d", plat, v, cur)
			}
		}
	}
}

func TestScopeUnlocksSince(t *testing.T) {
	tw := &Twitch{}
	if got := ScopeUnlocksSince(tw, 5); len(got) != 1 || got[0] != "reconnect.unlock.twitch6" {
		t.Errorf("5->6 = %v", got)
	}
	if got := ScopeUnlocksSince(tw, 6); len(got) != 0 {
		t.Errorf("current account = %v, want none", got)
	}
	if got := ScopeUnlocksSince(tw, 0); len(got) != 0 {
		t.Errorf("legacy row = %v, want none", got)
	}
	if got := ScopeUnlocksSince(&Kick{}, 1); len(got) != 0 {
		t.Errorf("opted-out bump = %v, want none", got)
	}
}
