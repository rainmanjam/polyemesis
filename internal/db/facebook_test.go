package db

import "testing"

// Empty() has no production caller today -- oauth.IngestFor checks
// len(opts.Crosspost) > 0 and opts.DonateCharityID != "" individually rather
// than calling this method wholesale. These tests exist anyway because the
// method is exported and a wrong answer from it would mislead whichever
// caller reaches for it next.
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
