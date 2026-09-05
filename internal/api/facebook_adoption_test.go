package api

import (
	"context"
	"strings"
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

// TURNING ON BACKUP INGEST NO LONGER COSTS THE BROADCAST. #727.
//
// enable_backup_ingest is a CREATE parameter, so the only route to a backup
// endpoint used to be Refresh key -- which starts a new live video and discards
// the one the destination is configured against, with its comment thread and
// its title.
func TestTurningOnBackupIngestFillsItFromTheExistingBroadcast(t *testing.T) {
	s, _, store, stub := stubbedServer(t, config.Config{})
	id := linkFacebookAccount(t, s, store)

	// A FETCHED key: it carries the live-video id, which is what says the
	// broadcast exists and is ours to modify.
	dest, err := store.CreateDestination(&db.Destination{
		Name: "fb", Kind: db.DestRTMP, Platform: db.PlatformFacebook,
		URL: "rtmps://live-api-s.facebook.com:443/rtmp", StreamKey: "778899",
		Enabled: true, AccountID: &id, BackupIngestWanted: true,
	})
	if err != nil {
		t.Fatalf("CreateDestination: %v", err)
	}

	if warn := s.fillFacebookBackupIngest(context.Background(), dest); warn != "" {
		t.Fatalf("unexpected warning: %s", warn)
	}
	if dest.BackupURL == "" || dest.BackupStreamKey == "" {
		t.Fatal("no backup endpoint was filled in, so the operator's only remaining " +
			"route is Refresh key -- which destroys the broadcast they are configured " +
			"against")
	}
	// AND NOT BY CREATING A NEW BROADCAST, which is the whole point.
	for _, c := range stub.calls() {
		if c.Method == "POST" && strings.HasSuffix(c.Path, "/live_videos") {
			t.Errorf("a new live video was created; the existing one should have been "+
				"modified in place: %s %s", c.Method, c.Path)
		}
	}
}

// A PASTED KEY IS TOLD WHY, rather than silently getting no redundant feed.
// #725's adoption is deliberately read-only: adding an ingest to a broadcast
// this process merely inferred is a write against something that may not be
// ours.
func TestAPastedKeyIsToldWhyItCannotGetABackupAdded(t *testing.T) {
	s, _, store, _ := stubbedServer(t, config.Config{})
	id := linkFacebookAccount(t, s, store)

	dest, err := store.CreateDestination(&db.Destination{
		Name: "fb", Kind: db.DestRTMP, Platform: db.PlatformFacebook,
		URL: "rtmps://live-api-s.facebook.com:443/rtmp",
		// The Live Producer shape: not an id.
		StreamKey: "FB-10000000000000000-0-AbCdEf",
		Enabled:   true, AccountID: &id, BackupIngestWanted: true,
	})
	if err != nil {
		t.Fatalf("CreateDestination: %v", err)
	}

	warn := s.fillFacebookBackupIngest(context.Background(), dest)
	if warn == "" {
		t.Fatal("a pasted key got no backup and no explanation, which is the silence " +
			"this whole change is about")
	}
	if !strings.Contains(warn, "Backup stream") {
		t.Errorf("the warning does not name the remedy the operator actually has "+
			"(turn on Backup stream in Live Producer and paste the second key): %q", warn)
	}
	if dest.BackupURL != "" {
		t.Errorf("a backup was filled in for a broadcast that could not be named: %q",
			dest.BackupURL)
	}
}

// AND NOTHING IS SPENT WHEN NOTHING WAS ASKED FOR. Each condition is a reason
// not to make a platform call during an ordinary destination edit.
func TestNoBackupCallIsMadeForADestinationThatDidNotAskForOne(t *testing.T) {
	s, _, store, stub := stubbedServer(t, config.Config{})
	id := linkFacebookAccount(t, s, store)

	for _, tc := range []struct {
		name string
		dest *db.Destination
	}{
		{"the toggle is off", &db.Destination{
			Name: "a", Kind: db.DestRTMP, Platform: db.PlatformFacebook,
			URL: "rtmps://x/y", StreamKey: "778899", AccountID: &id,
		}},
		{"a backup is already configured", &db.Destination{
			Name: "b", Kind: db.DestRTMP, Platform: db.PlatformFacebook,
			URL: "rtmps://x/y", StreamKey: "778899", AccountID: &id,
			BackupIngestWanted: true, BackupURL: "rtmps://x/z", BackupStreamKey: "k",
		}},
		{"it is not a Facebook destination", &db.Destination{
			Name: "c", Kind: db.DestRTMP, Platform: db.PlatformCustom,
			URL: "rtmp://x/y", StreamKey: "778899", BackupIngestWanted: true,
		}},
		{"no account is linked", &db.Destination{
			Name: "d", Kind: db.DestRTMP, Platform: db.PlatformFacebook,
			URL: "rtmps://x/y", StreamKey: "778899", BackupIngestWanted: true,
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := len(stub.calls())
			if warn := s.fillFacebookBackupIngest(context.Background(), tc.dest); warn != "" {
				t.Errorf("unexpected warning: %s", warn)
			}
			if n := len(stub.calls()) - before; n != 0 {
				t.Errorf("%d platform call(s) made during an ordinary edit", n)
			}
		})
	}
}
