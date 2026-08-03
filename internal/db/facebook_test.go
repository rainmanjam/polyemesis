package db

import "testing"

// Task 3 uses Empty() to decide whether to send Facebook's create-time
// parameters at all -- a wrong answer here either sends a request with
// nothing in it or silently drops settings the operator actually set.
func TestFacebookSettingsEmpty(t *testing.T) {
	if !(FacebookSettings{}).Empty() {
		t.Error("a zero-value FacebookSettings reported not Empty")
	}
}

func TestFacebookSettingsWithOnlyCrosspostIsNotEmpty(t *testing.T) {
	f := FacebookSettings{Crosspost: []CrosspostTarget{{PageID: "1234"}}}
	if f.Empty() {
		t.Error("FacebookSettings carrying a crosspost target reported Empty; " +
			"the request is skipped and the crosspost silently never applies")
	}
}

func TestFacebookSettingsWithOnlyDonateCharityIDIsNotEmpty(t *testing.T) {
	f := FacebookSettings{DonateCharityID: "999"}
	if f.Empty() {
		t.Error("FacebookSettings carrying a donate charity id reported Empty; " +
			"the request is skipped and the donate button silently never appears")
	}
}
