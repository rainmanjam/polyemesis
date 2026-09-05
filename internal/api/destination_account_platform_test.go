package api

import (
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// A DESTINATION MUST NOT HOLD ANOTHER PLATFORM'S ACCOUNT.
//
// platform and account_id are two independent columns and nothing compared
// them, while everything that acts on a destination reads BOTH: preannounce.go
// resolves ScheduledBroadcastsFor(d.Platform) and then calls it with
// tokenFor(*d.AccountID).AccessToken, and lifecycle.go carries the same pair
// side by side. So "platform: facebook, account: <a YouTube account>" is a
// Google OAuth bearer token sent to graph.facebook.com by a background sweep --
// a live credential for one company handed to another, with nobody watching.
//
// THE DIALOG IS NOT WHERE IT COMES FROM. applyPreset in DestinationDialog.tsx
// calls setAccountId("none") unconditionally on every preset change, and the
// picker carries an explicit "Not linked" item, so the current dialog cannot
// produce this pairing. It arrives from direct API clients, which have no
// dialog and no picker, and from rows already in the database, written before
// anything compared the two columns.
//
// The device clears the link and says so, rather than refusing, for the reason
// dropUnsendableSettings records: the rows already mismatched in the database
// would be permanently uneditable under a 400, because every PUT decodes over
// the stored row and so carries the mismatch into the write whether or not the
// client mentioned it. (Disconnecting an account is not a second source of it:
// account_id's foreign key is ON DELETE SET NULL and foreign_keys is on in the
// DSN, so a disconnect writes NULL rather than leaving a stale id, and NULL is
// exactly what the clause skips.)
//
// WHAT THESE TESTS PIN, AND WHAT THEY DO NOT. The check lives in one API
// helper, not in the store, so these prove it for the routes that call it --
// create, update, and the expert-args save -- and prove nothing about a route
// that does not. db.CreateDestination and db.UpdateDestination will still write
// the pair for anyone who hands it to them; TestAnExpertSaveCannotStoreAnother
// PlatformsAccount below relies on exactly that to build its fixture.

// accountLinkedBody is a create body for a destination pointed at one platform
// and carrying an account id.
func accountLinkedBody(name string, platform db.Platform, accountID int64) map[string]any {
	return map[string]any{
		"name": name, "kind": "rtmp", "platform": string(platform),
		"url": "rtmps://ingest.example/live", "streamKey": "typed-by-hand",
		"accountId": accountID,
	}
}

type destinationReply struct {
	Destination *db.Destination `json:"destination"`
	Warnings    []string        `json:"warnings"`
}

// unlinkWarning returns the warning about the connected account, or "".
func unlinkWarning(warnings []string) string {
	for _, w := range warnings {
		if strings.Contains(w, "connected account was unlinked") {
			return w
		}
	}
	return ""
}

func TestCreatingADestinationCannotKeepAnotherPlatformsAccount(t *testing.T) {
	s, h, store, sign := engineServer(t, defaultTools(), Options{})
	youtube := connectAccount(t, store, s.box, db.PlatformYouTube, "the channel")

	var resp destinationReply
	decodeInto(t, send(t, h, sign, http.MethodPost, "/api/v1/destinations",
		accountLinkedBody("facebook one", db.PlatformFacebook, youtube), http.StatusCreated), &resp)

	if resp.Destination.AccountID != nil {
		t.Fatalf("a Facebook destination was stored holding account %d, which is a "+
			"YouTube account: its token would be sent to graph.facebook.com",
			*resp.Destination.AccountID)
	}
	msg := unlinkWarning(resp.Warnings)
	if msg == "" {
		t.Fatalf("the account was unlinked silently; the operator is never told why "+
			"the key stopped refreshing: %v", resp.Warnings)
	}
	// The warning has to name BOTH sides. "Account removed" leaves the operator
	// with no idea which of the two fields they got wrong.
	if !strings.Contains(msg, string(db.PlatformYouTube)) || !strings.Contains(msg, string(db.PlatformFacebook)) {
		t.Fatalf("the warning does not name both platforms: %q", msg)
	}

	// Unlinking must not take the destination out with it. The URL and key are
	// the operator's own and still work; only the automatic refresh is gone.
	if resp.Destination.URL == "" || resp.Destination.StreamKey == "" {
		t.Fatalf("clearing the account link also cleared the endpoint: url=%q key=%q",
			resp.Destination.URL, resp.Destination.StreamKey)
	}
}

func TestADestinationKeepsAnAccountFromItsOwnPlatform(t *testing.T) {
	// The positive control, and the one that matters most: clearing account_id
	// unconditionally would satisfy every assertion above while removing the
	// stream-key fetch this product connects accounts for at all.
	s, h, store, sign := engineServer(t, defaultTools(), Options{})
	youtube := connectAccount(t, store, s.box, db.PlatformYouTube, "the channel")

	var resp destinationReply
	decodeInto(t, send(t, h, sign, http.MethodPost, "/api/v1/destinations",
		accountLinkedBody("youtube one", db.PlatformYouTube, youtube), http.StatusCreated), &resp)

	if resp.Destination.AccountID == nil {
		t.Fatal("a YouTube destination lost its YouTube account link")
	}
	if *resp.Destination.AccountID != youtube {
		t.Fatalf("accountId = %d, want %d", *resp.Destination.AccountID, youtube)
	}
	if msg := unlinkWarning(resp.Warnings); msg != "" {
		t.Fatalf("warned about a link with nothing wrong with it: %q", msg)
	}
}

func TestADestinationWithNoAccountIsLeftAlone(t *testing.T) {
	// The second control. A destination whose key is typed by hand is the
	// normal shape for most of the preset catalogue, and a check that read a
	// missing link as a mismatch would warn on every one of them.
	_, h, _, sign := engineServer(t, defaultTools(), Options{})

	body := accountLinkedBody("hand typed", db.PlatformKick, 0)
	delete(body, "accountId")

	var resp destinationReply
	decodeInto(t, send(t, h, sign, http.MethodPost, "/api/v1/destinations",
		body, http.StatusCreated), &resp)

	if resp.Destination.AccountID != nil {
		t.Fatalf("accountId = %d on a destination that named none", *resp.Destination.AccountID)
	}
	if msg := unlinkWarning(resp.Warnings); msg != "" {
		t.Fatalf("warned about an account that was never linked: %q", msg)
	}
}

func TestRetypingADestinationUnlinksTheAccountItLeavesBehind(t *testing.T) {
	// THE PATH THAT ACTUALLY PRODUCES THIS. The body says nothing about the
	// account -- {"platform":"facebook"} is the whole edit, exactly as the
	// dialog sends it after a preset change -- and the update handler decodes
	// over the stored row, so the YouTube link arrives at the write without the
	// client ever mentioning it. Checking the request body instead of the
	// merged row would see nothing at all here.
	s, h, store, sign := engineServer(t, defaultTools(), Options{})
	youtube := connectAccount(t, store, s.box, db.PlatformYouTube, "the channel")

	var created destinationReply
	decodeInto(t, send(t, h, sign, http.MethodPost, "/api/v1/destinations",
		accountLinkedBody("was youtube", db.PlatformYouTube, youtube), http.StatusCreated), &created)
	if created.Destination.AccountID == nil {
		t.Fatal("the fixture did not store the account link it is about to retype")
	}
	id := strconv.FormatInt(created.Destination.ID, 10)

	var resp destinationReply
	decodeInto(t, send(t, h, sign, http.MethodPut, "/api/v1/destinations/"+id,
		map[string]any{"platform": string(db.PlatformFacebook)}, http.StatusOK), &resp)

	if resp.Destination.AccountID != nil {
		t.Fatalf("retyping to Facebook kept YouTube account %d", *resp.Destination.AccountID)
	}
	if msg := unlinkWarning(resp.Warnings); msg == "" {
		t.Fatalf("the link was dropped silently on the edit path: %v", resp.Warnings)
	}

	// It has to be GONE FROM THE ROW, not just from the response. The sweeps
	// that would send the token read the database, never this reply.
	stored, err := store.GetDestination(created.Destination.ID)
	if err != nil {
		t.Fatalf("GetDestination: %v", err)
	}
	if stored.AccountID != nil {
		t.Fatalf("the stored row still holds account %d after the retype", *stored.AccountID)
	}
}

func TestAnUnrelatedEditKeepsTheAccountLink(t *testing.T) {
	// The control for the edit path. Renaming a destination must not cost it
	// the connected account, or every save through the dialog would unlink one.
	s, h, store, sign := engineServer(t, defaultTools(), Options{})
	youtube := connectAccount(t, store, s.box, db.PlatformYouTube, "the channel")

	var created destinationReply
	decodeInto(t, send(t, h, sign, http.MethodPost, "/api/v1/destinations",
		accountLinkedBody("still youtube", db.PlatformYouTube, youtube), http.StatusCreated), &created)
	id := strconv.FormatInt(created.Destination.ID, 10)

	var resp destinationReply
	decodeInto(t, send(t, h, sign, http.MethodPut, "/api/v1/destinations/"+id,
		map[string]any{"name": "renamed"}, http.StatusOK), &resp)

	if resp.Destination.AccountID == nil {
		t.Fatal("renaming a destination unlinked its account")
	}
	if *resp.Destination.AccountID != youtube {
		t.Fatalf("accountId = %d, want %d", *resp.Destination.AccountID, youtube)
	}
	if msg := unlinkWarning(resp.Warnings); msg != "" {
		t.Fatalf("warned about a link that did not change: %q", msg)
	}
}

func TestACustomDestinationKeepsAnAccountFromARealPlatform(t *testing.T) {
	// CUSTOM IS THE PAIRING THAT MAKES STREAM-KEY REFRESH WORK, and comparing
	// it like any other platform would unlink it.
	//
	// Nothing resolves a platform API off a custom destination: oauth.Set has
	// no custom entry, so ScheduledBroadcastsFor and LifecycleFor both answer
	// false and preannounce.go and lifecycle.go skip the row -- there is no
	// token to send to the wrong company, which is the only thing this check
	// exists to stop. handleRefreshStreamKey, meanwhile, picks the provider off
	// acct.Platform rather than dest.Platform, so this exact shape is how a
	// hand-typed ingest URL gets its key fetched. All but four of the preset
	// catalogue's entries save as custom, so unlinking here would be the COMMON
	// case rather than the corner one.
	//
	// MUTATION: delete the `if want == db.PlatformCustom { break }` clause in
	// dropUnsendableSettings and this fails on the nil AccountID.
	s, h, store, sign := engineServer(t, defaultTools(), Options{})
	youtube := connectAccount(t, store, s.box, db.PlatformYouTube, "the channel")

	var resp destinationReply
	decodeInto(t, send(t, h, sign, http.MethodPost, "/api/v1/destinations",
		accountLinkedBody("hand typed ingest", db.PlatformCustom, youtube), http.StatusCreated), &resp)

	if resp.Destination.AccountID == nil {
		t.Fatal("a custom destination lost the account that refreshes its stream key; " +
			"the refresh route resolves the provider off the ACCOUNT's platform, so " +
			"this pairing is the working one, not a leak")
	}
	if *resp.Destination.AccountID != youtube {
		t.Fatalf("accountId = %d, want %d", *resp.Destination.AccountID, youtube)
	}
	if msg := unlinkWarning(resp.Warnings); msg != "" {
		t.Fatalf("warned about a link that is doing its job: %q", msg)
	}
}

func TestAnExpertSaveCannotStoreAnotherPlatformsAccount(t *testing.T) {
	// THE ROUTE THE CLAIM USED TO SKIP. saveExpertArgs re-reads the stored row
	// and writes it back, so before it called dropUnsendableSettings it was a
	// third way for the mismatched pair to reach db.UpdateDestination -- it
	// merely happened never to assign Platform or AccountID itself, which is
	// inspection rather than a device.
	//
	// MUTATION: remove the dropUnsendableSettings call from saveExpertArgs and
	// this fails on the stored AccountID.
	s, h, store, sign := engineServer(t, defaultTools(), Options{})
	youtube := connectAccount(t, store, s.box, db.PlatformYouTube, "the channel")

	// Built through the STORE, which has no such check. That is the point: the
	// invariant is not enforced at the store, so this is exactly the row an
	// install carries from before the check existed, and the expert route is
	// the one that re-reads it.
	row, err := store.CreateDestination(&db.Destination{
		Name: "legacy row", Kind: db.DestRTMP, Platform: db.PlatformFacebook,
		URL: "rtmps://ingest.example/live", StreamKey: "typed-by-hand",
		AccountID: &youtube,
	})
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	if row.AccountID == nil {
		t.Fatal("the store refused the mismatched pair this fixture needs, which " +
			"means the invariant moved into db and this test is now testing nothing")
	}

	id := strconv.FormatInt(row.ID, 10)
	var resp expertResponse
	decodeInto(t, send(t, h, sign, http.MethodPut, "/api/v1/destinations/"+id+"/expert",
		map[string]any{"inputArgs": "-analyzeduration 10M", "confirm": true},
		http.StatusOK), &resp)

	stored, err := store.GetDestination(row.ID)
	if err != nil {
		t.Fatalf("GetDestination: %v", err)
	}
	if stored.AccountID != nil {
		t.Fatalf("an expert-args save wrote the row back still holding account %d, "+
			"which is a YouTube account on a Facebook destination", *stored.AccountID)
	}
	if msg := unlinkWarning(resp.Warnings); msg == "" {
		t.Fatalf("the link was dropped silently on the expert path; the operator is "+
			"never told why the key stopped refreshing: %v", resp.Warnings)
	}
	// The control: the save it was actually asked for still happened. A repair
	// that swallowed the write would satisfy every assertion above.
	if stored.ExtraInputArgs != "-analyzeduration 10M" {
		t.Fatalf("extraInputArgs = %q; the expert save itself was lost", stored.ExtraInputArgs)
	}
}
