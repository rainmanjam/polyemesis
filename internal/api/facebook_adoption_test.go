package api

import (
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/config"
	"github.com/rainmanjam/polyemesis/internal/db"
)

// linkFacebookAccount stores a connected Facebook account and returns its id.
func linkFacebookAccount(t *testing.T, s *Server, store *db.DB) int64 {
	t.Helper()
	acct, err := store.UpsertPlatformAccount(s.box, &db.PlatformAccount{
		Platform:    db.PlatformFacebook,
		AccountName: "A Profile",
		AccountRef:  "",
		AccessToken: "tok",
		ExpiresAt:   time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("UpsertPlatformAccount: %v", err)
	}
	return acct.ID
}

func linkedDestination(t *testing.T, store *db.DB, accountID int64, key string) {
	t.Helper()
	if _, err := store.CreateDestination(&db.Destination{
		Name: "fb", Kind: db.DestRTMP, Platform: db.PlatformFacebook,
		URL: "rtmps://live-api-s.facebook.com:443/rtmp", StreamKey: key,
		Enabled: true, AccountID: &accountID,
	}); err != nil {
		t.Fatalf("CreateDestination: %v", err)
	}
}

// A PASTED KEY GETS ITS BROADCAST FROM THE ACCOUNT. #725.
//
// A key polyemesis fetched carries the live-video id inside it. A persistent key
// pasted from Live Producer does not, so the chat pane had nothing to attach to
// and said so. Going live with that key creates a live video on the same target
// the connected account can see, and Facebook's own limit -- one live video at a
// time per persistent key -- is what makes asking a fact rather than a guess.
func TestAPastedFacebookKeyTakesItsLiveVideoFromTheAccount(t *testing.T) {
	s, _, store, _ := stubbedServer(t, config.Config{})
	id := linkFacebookAccount(t, s, store)

	// The shape Live Producer hands out: not an id, so nothing can be read off it.
	linkedDestination(t, store, id, "FB-10000000000000000-0-AbCdEf")

	got := s.facebookLiveVideoID(id)
	if got == "" {
		t.Fatal("a destination with a pasted key got no live-video id, so its chat " +
			"pane stays empty while the account is plainly running a broadcast")
	}
	if got != "fb-live-1" {
		t.Errorf("adopted %q, want the broadcast the account is running", got)
	}
}

// A FETCHED KEY IS NOT RE-ASKED. It carries the id, which is authoritative and
// costs no request; adopting over it would replace a fact with a lookup.
func TestAFetchedFacebookKeyIsUsedWithoutAskingThePlatform(t *testing.T) {
	s, _, store, stub := stubbedServer(t, config.Config{})
	id := linkFacebookAccount(t, s, store)
	linkedDestination(t, store, id, "778899")

	before := len(stub.calls())
	if got := s.facebookLiveVideoID(id); got != "778899" {
		t.Errorf("live video id = %q, want the one carried in the key", got)
	}
	if n := len(stub.calls()) - before; n != 0 {
		t.Errorf("%d platform call(s) were made for a key that already carries its "+
			"id; the key is the cheaper and more authoritative answer", n)
	}
}

// AN ACCOUNT WITH NO DESTINATION AT ALL ASKS NOTHING. Adoption is for a pasted
// key, and a request per chat rewire for an account nothing is linked to is a
// cost with no question behind it.
func TestAnAccountWithNoLinkedDestinationDoesNotAskThePlatform(t *testing.T) {
	s, _, store, stub := stubbedServer(t, config.Config{})
	id := linkFacebookAccount(t, s, store)

	before := len(stub.calls())
	if got := s.facebookLiveVideoID(id); got != "" {
		t.Errorf("live video id = %q, want empty", got)
	}
	if n := len(stub.calls()) - before; n != 0 {
		t.Errorf("%d platform call(s) for an account with nothing linked to it", n)
	}
}

// A REFUSAL IS EMPTY, NOT AN ERROR. Empty is the state this function already had
// and the adapter already explains, so an idle account or an ambiguous pair
// leaves the chat pane as it was rather than breaking the wiring for every other
// platform in the same loop.
func TestAnAdoptionThatCannotAnswerLeavesTheChatPaneAsItWas(t *testing.T) {
	s, _, store, stub := stubbedServer(t, config.Config{})
	id := linkFacebookAccount(t, s, store)
	linkedDestination(t, store, id, "FB-10000000000000000-0-AbCdEf")

	stub.setReject(func(stubCall) string { return "the token is no longer valid" })

	if got := s.facebookLiveVideoID(id); got != "" {
		t.Errorf("live video id = %q, want empty when the platform refuses", got)
	}
}
