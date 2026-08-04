package api

import (
	"reflect"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/oauth"
)

// TestIngestOptionsForCarriesTheStoredFacebookChoicesToTheProvider is the
// regression this fix round exists for: refresh-key's mapping from a stored
// destination to oauth.IngestOptions had no test at all, so a hardcoded
// db.FBPrivacyEveryone -- the most-exposing value Facebook offers -- or the
// mapping being deleted outright both left the whole suite green. This pins
// the mapping at the one function that produces it, which is also the one the
// handler actually calls, so there is no second copy to drift from it.
func TestIngestOptionsForCarriesTheStoredFacebookChoicesToTheProvider(t *testing.T) {
	dest := &db.Destination{
		Platform: db.PlatformFacebook,
		Compliance: db.Compliance{
			FacebookPrivacy: db.FBPrivacySelf,
		},
		Facebook: db.FacebookSettings{
			Crosspost:       []db.CrosspostTarget{{PageID: "1234", CreatePost: true}},
			DonateCharityID: "999",
		},
	}

	got := ingestOptionsFor(dest)
	want := oauth.IngestOptions{
		Privacy:         db.FBPrivacySelf,
		Crosspost:       []db.CrosspostTarget{{PageID: "1234", CreatePost: true}},
		DonateCharityID: "999",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ingestOptionsFor = %+v, want %+v", got, want)
	}
}

// TestIngestOptionsForSendsNothingWhenTheDestinationChoseNothing is the other
// half: a destination that never touched Facebook's declarative fields must
// produce the zero IngestOptions, which is what tells IngestFor to leave
// every field alone rather than sending an empty-but-present value.
func TestIngestOptionsForSendsNothingWhenTheDestinationChoseNothing(t *testing.T) {
	dest := &db.Destination{Platform: db.PlatformFacebook}

	got := ingestOptionsFor(dest)
	if !reflect.DeepEqual(got, oauth.IngestOptions{}) {
		t.Errorf("ingestOptionsFor(unconfigured) = %+v, want the zero value", got)
	}
}
