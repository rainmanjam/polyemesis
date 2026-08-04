# Facebook Declarative Metadata Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** send Facebook the declarative fields it documents — privacy,
`content_tags`, `crossposting_actions`, `donate_button_charity_id` — instead of
only `title` and `description`.

**Architecture:** fields split by how often they change. Privacy, crossposting
and the donate charity are per-destination and applied when the `LiveVideo` is
created (`IngestFor`, widened with an `IngestOptions` struct). Tags are typed per
broadcast in the composer and resolved from words to Facebook's numeric
ad-interest IDs at push time, the way `Category` already works.

**Tech Stack:** Go 1.x, `internal/db` (SQLite, JSON blob columns),
`internal/oauth` (hand-rolled Graph API calls, no SDK), React + TypeScript UI.

## Global Constraints

- **Empty means LEAVE IT ALONE.** No field in this plan is sent when the operator
  has not set it. Tests assert the **parameter is ABSENT from the query**, never
  that it is present-and-empty.
- **Every guard must be shown to fail against a named one-line mutation.** A
  mutation that fails to COMPILE is not a result.
- **Commit before you mutate.** `git checkout --` reverts uncommitted work.
- **Comments must never assert what the code does not do.** This repo's most
  recurring defect.
- **Capability checks are reported, never enforced** — see `MetadataCaps.Scope`.
  The platform's own error is the authority.
- Facebook privacy values, least exposure first: `SELF`, `ALL_FRIENDS`,
  `FRIENDS_OF_FRIENDS`, `EVERYONE`. Empty is "unchanged".
- Graph API version stays pinned at **v24.0** (`fbGraphBase`). Do not bump it.
- `go test -race ./...` green, `gofmt -l ./internal ./cmd` empty, `go vet ./...`
  clean, `cd ui && npx tsc --noEmit && npx oxlint` clean.

---

## File Structure

| File | Responsibility |
|---|---|
| `internal/db/compliance.go` | `FacebookPrivacy` type, values, validation; `Compliance.FacebookPrivacy` |
| `internal/db/facebook.go` (new) | `FacebookSettings`, `CrosspostTarget` — per-destination create-time config |
| `internal/db/destinations.go` | `Destination.Facebook` field, migration column, marshal/scan |
| `internal/oauth/metadata.go` | `Metadata.Tags` |
| `internal/oauth/facebook.go` | `IngestOptions`, widened `IngestFor`, create-time params, tag resolution, best-effort privacy on push |
| `internal/api/oauth_handlers.go` | maps the destination's stored values into `IngestOptions` |
| `ui/src/lib/types.ts` | names every new field (required by the drift guards) |

**Two drift guards will fail the build until the UI types are updated**, and that
is deliberate: `TestUITypesCanNameEveryDestinationField`
(`internal/db/settings_drift_test.go:139`) walks `Destination{}` **recursively**,
so `Facebook.Crosspost[].PageID` and `Compliance.FacebookPrivacy` all need names
in `ui/src/lib/types.ts`. Expect red before green.

---

### Task 1: The privacy vocabulary

**Files:**
- Modify: `internal/db/compliance.go`
- Modify: `ui/src/lib/types.ts`
- Test: `internal/db/compliance_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `db.FacebookPrivacy` (string type), constants
  `FBPrivacyUnchanged`/`FBPrivacySelf`/`FBPrivacyFriends`/`FBPrivacyFriendsOfFriends`/`FBPrivacyEveryone`,
  `db.FacebookPrivacies []FacebookPrivacy`, `db.ValidFacebookPrivacy(FacebookPrivacy) bool`,
  and the field `Compliance.FacebookPrivacy`.

- [ ] **Step 1: Write the failing test**

Add to `internal/db/compliance_test.go`:

```go
func TestFacebookPrivacyIsOfferedLeastExposureFirst(t *testing.T) {
	// The safe choice must be the near one, matching PrivacyStatuses. An
	// operator scanning a list and picking the first item must not thereby
	// broadcast to everyone.
	want := []FacebookPrivacy{
		FBPrivacySelf, FBPrivacyFriends, FBPrivacyFriendsOfFriends, FBPrivacyEveryone,
	}
	if len(FacebookPrivacies) != len(want) {
		t.Fatalf("FacebookPrivacies = %v, want %v", FacebookPrivacies, want)
	}
	for i := range want {
		if FacebookPrivacies[i] != want[i] {
			t.Fatalf("FacebookPrivacies = %v, want %v", FacebookPrivacies, want)
		}
	}
}

func TestAnUnknownFacebookPrivacyIsRefusedAtSaveTime(t *testing.T) {
	c := Compliance{FacebookPrivacy: "PUBLIC"} // a YouTube word, not a Facebook one
	probs := c.Problems()
	if len(probs) == 0 {
		t.Fatal("an unknown Facebook privacy was accepted; the operator finds out " +
			"when the broadcast goes to the wrong audience")
	}
	if !strings.Contains(probs[0], "PUBLIC") {
		t.Errorf("problem %q does not name the offending value", probs[0])
	}
}

func TestComplianceWithOnlyAFacebookPrivacyIsNotEmpty(t *testing.T) {
	// Empty() gates whether a push happens at all. A Compliance carrying only
	// this field must not be mistaken for nothing to do.
	c := Compliance{FacebookPrivacy: FBPrivacySelf}
	if c.Empty() {
		t.Error("Compliance carrying a Facebook privacy reported Empty; the push " +
			"is skipped and the setting silently never applies")
	}
}
```

- [ ] **Step 2: Run it and watch it fail**

Run: `go test ./internal/db/ -run 'FacebookPrivacy|OnlyAFacebookPrivacy' -v`
Expected: FAIL — `undefined: FacebookPrivacy`.

- [ ] **Step 3: Implement**

In `internal/db/compliance.go`, below the `PrivacyStatus` block:

```go
// FacebookPrivacy is Facebook's audience for a live video.
//
// Empty means LEAVE IT ALONE, exactly as PrivacyStatus does and for the same
// reason: a privacy write that happens by accident is one the operator finds out
// about from the audience.
//
// Deliberately NOT PrivacyStatus. That type is documented as YouTube's
// visibility and its values are public/unlisted/private. Facebook has no
// unlisted and YouTube has no friends, so sharing the type would need a lossy
// mapping in the one field here where being wrong cannot be taken back -- and a
// translation layer is somewhere for that wrongness to hide.
type FacebookPrivacy string

const (
	FBPrivacyUnchanged        FacebookPrivacy = ""
	FBPrivacySelf             FacebookPrivacy = "SELF"
	FBPrivacyFriends          FacebookPrivacy = "ALL_FRIENDS"
	FBPrivacyFriendsOfFriends FacebookPrivacy = "FRIENDS_OF_FRIENDS"
	FBPrivacyEveryone         FacebookPrivacy = "EVERYONE"
)

// FacebookPrivacies is every value an operator may pick, least exposure first,
// because the safe choice should be the near one.
var FacebookPrivacies = []FacebookPrivacy{
	FBPrivacySelf, FBPrivacyFriends, FBPrivacyFriendsOfFriends, FBPrivacyEveryone,
}

// ValidFacebookPrivacy reports whether p is a value Facebook accepts.
func ValidFacebookPrivacy(p FacebookPrivacy) bool {
	if p == FBPrivacyUnchanged {
		return true
	}
	for _, v := range FacebookPrivacies {
		if v == p {
			return true
		}
	}
	return false
}
```

Add the field to `Compliance` (after `Labels`):

```go
	// FacebookPrivacy is applied when the Facebook LiveVideo is CREATED, and
	// attempted again best-effort on a metadata push. Empty leaves it alone.
	FacebookPrivacy FacebookPrivacy `json:"facebookPrivacy,omitempty"`
```

Extend `Empty()`:

```go
func (c Compliance) Empty() bool {
	return c.Privacy == PrivacyUnchanged && c.MadeForKids == nil &&
		len(c.Labels) == 0 && c.FacebookPrivacy == FBPrivacyUnchanged
}
```

Extend `Problems()` — add before the label loop:

```go
	if !ValidFacebookPrivacy(c.FacebookPrivacy) {
		probs = append(probs, fmt.Sprintf(
			"unknown Facebook privacy %q (SELF, ALL_FRIENDS, FRIENDS_OF_FRIENDS, EVERYONE)",
			c.FacebookPrivacy))
	}
```

- [ ] **Step 4: Add the UI type name**

In `ui/src/lib/types.ts`, find the `compliance` object on the destination type and
add `facebookPrivacy?: string;`. Without it,
`TestUITypesCanNameEveryDestinationField` fails.

- [ ] **Step 5: Run the tests**

Run: `go test ./internal/db/ -run 'FacebookPrivacy|OnlyAFacebookPrivacy|UITypesCanNameEveryDestinationField' -v`
Expected: PASS, all four.

- [ ] **Step 6: Commit**

```bash
git add internal/db/compliance.go internal/db/compliance_test.go ui/src/lib/types.ts
git commit -m "feat(db): Facebook's privacy vocabulary, in Facebook's own words"
```

- [ ] **Step 7: Prove each guard can fail**

Run each mutation, record the failure, restore:

1. Reorder `FacebookPrivacies` to put `FBPrivacyEveryone` first →
   `TestFacebookPrivacyIsOfferedLeastExposureFirst` must fail.
2. Delete the `ValidFacebookPrivacy` clause from `Problems()` →
   `TestAnUnknownFacebookPrivacyIsRefusedAtSaveTime` must fail.
3. Drop `&& c.FacebookPrivacy == FBPrivacyUnchanged` from `Empty()` →
   `TestComplianceWithOnlyAFacebookPrivacyIsNotEmpty` must fail.

---

### Task 2: The destination's Facebook block

**Files:**
- Create: `internal/db/facebook.go`
- Modify: `internal/db/destinations.go` (the `Destination` struct; the migration
  `columns` list beside the `compliance` entry; the three marshal/scan sites that
  currently handle `compliance` — around `:498`, `:641`, `:696`)
- Modify: `ui/src/lib/types.ts`
- Test: `internal/db/destinations_test.go`

**Interfaces:**
- Consumes: nothing from Task 1.
- Produces: `db.FacebookSettings{Crosspost []CrosspostTarget, DonateCharityID string}`,
  `db.CrosspostTarget{PageID string, CreatePost bool}`, and the field
  `Destination.Facebook`.

- [ ] **Step 1: Write the failing test**

Add to `internal/db/destinations_test.go`:

```go
func TestADestinationRoundTripsItsFacebookBlock(t *testing.T) {
	d := testDB(t)
	created, err := d.CreateDestination(Destination{
		Name: "fb", Kind: DestRTMP, Platform: PlatformFacebook,
		URL: "rtmps://live-api.facebook.com:443/rtmp/", StreamKey: "k",
		Facebook: FacebookSettings{
			Crosspost: []CrosspostTarget{
				{PageID: "1234", CreatePost: true},
				{PageID: "5678"},
			},
			DonateCharityID: "999",
		},
	})
	if err != nil {
		t.Fatalf("CreateDestination: %v", err)
	}
	got, err := d.GetDestination(created.ID)
	if err != nil {
		t.Fatalf("GetDestination: %v", err)
	}
	if len(got.Facebook.Crosspost) != 2 {
		t.Fatalf("crosspost = %+v, want two targets", got.Facebook.Crosspost)
	}
	// CreatePost is what selects enable_crossposting_and_create_post over
	// enable_crossposting. Losing it posts as a Page nobody asked to post as.
	if !got.Facebook.Crosspost[0].CreatePost || got.Facebook.Crosspost[1].CreatePost {
		t.Errorf("createPost flags = %v/%v, want true/false",
			got.Facebook.Crosspost[0].CreatePost, got.Facebook.Crosspost[1].CreatePost)
	}
	if got.Facebook.DonateCharityID != "999" {
		t.Errorf("donateCharityId = %q, want 999", got.Facebook.DonateCharityID)
	}
}

func TestADestinationWithNoFacebookBlockReadsBackEmpty(t *testing.T) {
	// Every destination that existed before this column did reads '{}'. An
	// unreadable or non-empty default would make every pre-existing Facebook
	// destination start sending parameters nobody set.
	d := testDB(t)
	created, err := d.CreateDestination(Destination{
		Name: "plain", Kind: DestRTMP, Platform: PlatformCustom,
		URL: "rtmp://example.invalid/live", StreamKey: "k",
	})
	if err != nil {
		t.Fatalf("CreateDestination: %v", err)
	}
	got, err := d.GetDestination(created.ID)
	if err != nil {
		t.Fatalf("GetDestination: %v", err)
	}
	if len(got.Facebook.Crosspost) != 0 || got.Facebook.DonateCharityID != "" {
		t.Errorf("facebook = %+v, want zero", got.Facebook)
	}
}
```

- [ ] **Step 2: Run it and watch it fail**

Run: `go test ./internal/db/ -run 'FacebookBlock' -v`
Expected: FAIL — `unknown field Facebook in struct literal`.

- [ ] **Step 3: Create the types**

`internal/db/facebook.go`:

```go
package db

// FacebookSettings is per-destination Facebook configuration applied when the
// broadcast is CREATED rather than pushed afterwards.
//
// These live on the destination and not in the composer because they are opaque
// ids an operator fetches once from Facebook's own console and then reuses --
// which Page to share with, which charity to collect for -- and because the
// create edge is the surface Meta documents. The Graph API reference has no
// Updating section for LiveVideo at all, so pushing them later would be building
// on an endpoint whose accepted parameters are written down nowhere.
type FacebookSettings struct {
	// Crosspost names the Pages this broadcast is shared with.
	Crosspost []CrosspostTarget `json:"crosspost,omitempty"`
	// DonateCharityID adds a donate button for one charity.
	DonateCharityID string `json:"donateCharityId,omitempty"`
}

// CrosspostTarget is one Page and what to do with it.
type CrosspostTarget struct {
	PageID string `json:"pageId"`
	// CreatePost also publishes a post as that Page rather than only enabling
	// the share. Facebook's two actions -- enable_crossposting and
	// enable_crossposting_and_create_post -- differ by exactly this, so a lost
	// flag is a post nobody asked for.
	CreatePost bool `json:"createPost,omitempty"`
}

// Empty reports whether there is nothing to send.
func (f FacebookSettings) Empty() bool {
	return len(f.Crosspost) == 0 && f.DonateCharityID == ""
}
```

- [ ] **Step 4: Wire it into the destination row**

Add to `Destination` (immediately after `Compliance`):

```go
	// Facebook is create-time configuration for a Facebook destination. Empty
	// for every other platform, and for a Facebook destination that has not set
	// any of it.
	Facebook FacebookSettings `json:"facebook"`
```

Add the migration beside the `compliance` entry in the `columns` list:

```go
		// Facebook's create-time block, one JSON blob for the same reason
		// compliance is one: a slice plus a scalar, edited as a unit, and '{}'
		// is "send nothing".
		{"facebook", `ALTER TABLE destinations ADD COLUMN facebook TEXT NOT NULL DEFAULT '{}'`},
```

Then mirror `compliance` in all three places it appears:

1. **Scan** (near `:498`): add `facebookJSON` to the `SELECT` column list and the
   `Scan` destinations, then:

```go
	if facebookJSON != "" {
		if err := json.Unmarshal([]byte(facebookJSON), &d.Facebook); err != nil {
			return nil, fmt.Errorf("destination %d has unreadable Facebook settings: %w", d.ID, err)
		}
	}
```

2. **Create** (near `:641`) and 3. **Update** (near `:696`): beside each
   `compliance, err := json.Marshal(dst.Compliance)`:

```go
	facebook, err := json.Marshal(dst.Facebook)
	if err != nil {
		return nil, err
	}
```

and add `facebook` to that statement's column list, placeholders and arguments.

- [ ] **Step 5: Add the UI type names**

In `ui/src/lib/types.ts`, on the destination type:

```ts
  facebook?: {
    crosspost?: { pageId: string; createPost?: boolean }[];
    donateCharityId?: string;
  };
```

- [ ] **Step 6: Run the tests**

Run: `go test ./internal/db/ -run 'FacebookBlock|UITypesCanNameEveryDestinationField' -v`
Expected: PASS.

Then the whole package: `go test ./internal/db/`

- [ ] **Step 7: Commit**

```bash
git add internal/db/facebook.go internal/db/destinations.go \
        internal/db/destinations_test.go ui/src/lib/types.ts
git commit -m "feat(db): a destination remembers its Facebook create-time settings"
```

- [ ] **Step 8: Prove each guard can fail**

1. Delete the `json.Unmarshal` into `d.Facebook` in the scan →
   `TestADestinationRoundTripsItsFacebookBlock` must fail.
2. Change `CreatePost bool` to be dropped from the marshalled JSON (rename its
   tag to `json:"-"`) → the createPost-flags assertion must fail.
3. Change the column default from `'{}'` to `'null'` →
   `TestADestinationWithNoFacebookBlockReadsBackEmpty` must still pass (a null
   unmarshals to zero); if it does, note in the report that this mutation does
   NOT bite and say why, rather than inventing a different one.

---

### Task 3: IngestOptions, and the create-time parameters

**Files:**
- Modify: `internal/oauth/facebook.go` (interface at `:226`; `Ingest` at `:446`;
  `IngestFor` at `:456`)
- Modify: `internal/api/oauth_handlers.go` (`ingestFor` helper, and its single
  call site at `:394` which already holds `dest`)
- Test: `internal/oauth/facebook_test.go`

**Interfaces:**
- Consumes: `db.FacebookPrivacy`, `db.FacebookSettings`, `db.CrosspostTarget`
  from Tasks 1 and 2.
- Produces: `oauth.IngestOptions{Privacy db.FacebookPrivacy, Crosspost []db.CrosspostTarget, DonateCharityID string}`
  and the widened `IngestFor(ctx, clientID, accessToken, targetRef string, opts IngestOptions) (*Broadcast, error)`.

- [ ] **Step 1: Write the failing tests**

Add to `internal/oauth/facebook_test.go`:

```go
func TestFacebookSendsTheStoredPrivacyWhenTheBroadcastIsCreated(t *testing.T) {
	log := fbServer(t, graphStub(t, fbLiveResponse("77")))
	_, err := (&Facebook{}).IngestFor(context.Background(), "cid", "user-token", "user:1000",
		IngestOptions{Privacy: db.FBPrivacySelf})
	if err != nil {
		t.Fatalf("IngestFor: %v", err)
	}
	post := fbCall(*log, http.MethodPost, "/user:1000/live_videos")
	if post == nil {
		t.Fatalf("no create; calls were %+v", *log)
	}
	if !strings.Contains(post.Query, "privacy") || !strings.Contains(post.Query, "SELF") {
		t.Errorf("create query %q carries no SELF privacy; Facebook documents "+
			"LIVE_VIDEO__PRIVACY_REQUIRED and the operator's choice never arrives", post.Query)
	}
}

func TestFacebookSendsNoPrivacyParameterWhenNoneWasChosen(t *testing.T) {
	// ABSENT, not empty. A request carrying privacy= is a different request from
	// one that omits it, and only the second means "leave it alone".
	log := fbServer(t, graphStub(t, fbLiveResponse("77")))
	_, err := (&Facebook{}).IngestFor(context.Background(), "cid", "user-token", "user:1000",
		IngestOptions{})
	if err != nil {
		t.Fatalf("IngestFor: %v", err)
	}
	post := fbCall(*log, http.MethodPost, "/user:1000/live_videos")
	if post == nil {
		t.Fatalf("no create; calls were %+v", *log)
	}
	if strings.Contains(post.Query, "privacy") {
		t.Errorf("create query %q sends a privacy nobody chose", post.Query)
	}
}

func TestFacebookCrosspostingCarriesTheActionEachPageAskedFor(t *testing.T) {
	log := fbServer(t, graphStub(t, fbLiveResponse("77")))
	_, err := (&Facebook{}).IngestFor(context.Background(), "cid", "user-token", "user:1000",
		IngestOptions{Crosspost: []db.CrosspostTarget{
			{PageID: "1234", CreatePost: true},
			{PageID: "5678"},
		}})
	if err != nil {
		t.Fatalf("IngestFor: %v", err)
	}
	post := fbCall(*log, http.MethodPost, "/user:1000/live_videos")
	if post == nil {
		t.Fatalf("no create; calls were %+v", *log)
	}
	raw, err := url.QueryUnescape(post.Query)
	if err != nil {
		t.Fatalf("unescape: %v", err)
	}
	if !strings.Contains(raw, `"page_id":"1234"`) ||
		!strings.Contains(raw, `"action":"enable_crossposting_and_create_post"`) {
		t.Errorf("query %q does not ask 1234 to create a post", raw)
	}
	if !strings.Contains(raw, `"page_id":"5678"`) ||
		!strings.Contains(raw, `"action":"enable_crossposting"`) {
		t.Errorf("query %q does not share with 5678 without posting", raw)
	}
	// The dangerous direction: a page that did NOT ask for a post must not get
	// one. Count the two action spellings rather than trusting the pair above.
	if strings.Count(raw, "enable_crossposting_and_create_post") != 1 {
		t.Errorf("query %q posts as more Pages than asked", raw)
	}
}

func TestFacebookSendsNoCrosspostingOrDonateWhenNoneIsStored(t *testing.T) {
	log := fbServer(t, graphStub(t, fbLiveResponse("77")))
	_, err := (&Facebook{}).IngestFor(context.Background(), "cid", "user-token", "user:1000",
		IngestOptions{})
	if err != nil {
		t.Fatalf("IngestFor: %v", err)
	}
	post := fbCall(*log, http.MethodPost, "/user:1000/live_videos")
	if post == nil {
		t.Fatalf("no create; calls were %+v", *log)
	}
	if strings.Contains(post.Query, "crossposting_actions") {
		t.Errorf("create query %q crossposts to Pages nobody named", post.Query)
	}
	if strings.Contains(post.Query, "donate_button_charity_id") {
		t.Errorf("create query %q adds a donate button nobody asked for", post.Query)
	}
}
```

And the one that keeps a bad broadcast from reaching air:

```go
func TestARefusedCreateFailsTheKeyFetchRatherThanReturningAKey(t *testing.T) {
	// A broadcast created with the wrong privacy is the unrecoverable case this
	// whole sub-project exists for. Handing back a stream key for one is worse
	// than handing back an error: the operator streams to it.
	fbServer(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/live_videos") {
			http.Error(w, `{"error":{"message":"(#100) Invalid privacy setting"}}`,
				http.StatusBadRequest)
			return
		}
		http.Error(w, "{}", http.StatusNotFound)
	})
	b, err := (&Facebook{}).IngestFor(context.Background(), "cid", "user-token", "user:1000",
		IngestOptions{Privacy: db.FBPrivacySelf})
	if err == nil {
		t.Fatalf("a refused create returned a broadcast %+v instead of an error", b)
	}
	if b != nil {
		t.Errorf("broadcast = %+v, want nil alongside the error", b)
	}
}
```

Add `"net/url"` and `"github.com/rainmanjam/polyemesis/internal/db"` to the test
file's imports if absent.

- [ ] **Step 2: Run them and watch them fail**

Run: `go test ./internal/oauth/ -run 'FacebookSendsThe|FacebookSendsNo|FacebookCrossposting' -v`
Expected: FAIL — `too many arguments in call to IngestFor`.

- [ ] **Step 3: Add the options struct and widen the interface**

In `internal/oauth/facebook.go`, above the interface at `:226`:

```go
// IngestOptions carries what a platform needs when the broadcast is CREATED,
// which is not the same set the composer pushes afterwards.
//
// A struct rather than more parameters because the create-time surface is going
// to grow: scheduling (event_params) and backup ingest both land here, and three
// signature changes to one interface is three chances to miss a call site. The
// zero value sends nothing, which is what every caller without a destination in
// hand passes.
type IngestOptions struct {
	Privacy         db.FacebookPrivacy
	Crosspost       []db.CrosspostTarget
	DonateCharityID string
}
```

Widen the interface method and `IngestFor` itself to take
`opts IngestOptions` as a fifth parameter. At `:446`, `Ingest` passes
`IngestOptions{}`.

- [ ] **Step 4: Build the create parameters**

Replace the create call in `IngestFor`:

```go
	// status=LIVE_NOW is what makes this a broadcast rather than a scheduled
	// post. The video only appears once bytes arrive, so creating it ahead of
	// the encoder is safe.
	//
	// overlay_url is deliberately absent: Graph removed it in v24.0 and sending
	// it now is an error rather than a no-op.
	params := url.Values{"status": {"LIVE_NOW"}}
	// Every field below is sent ONLY when the operator chose it. Facebook treats
	// a present-but-empty parameter as a value, so "leave it alone" has to mean
	// an absent key rather than an empty one.
	//
	// Privacy is applied HERE rather than on the metadata push because Facebook
	// documents LIVE_VIDEO__PRIVACY_REQUIRED -- "You need to set a privacy
	// before going live" -- and because the reference documents no Updating
	// section for LiveVideo at all. This is the surface Meta describes.
	if opts.Privacy != db.FBPrivacyUnchanged && tgt.kind != fbKindPage {
		params.Set("privacy", fbPrivacyParam(opts.Privacy))
	}
	if len(opts.Crosspost) > 0 {
		enc, err := fbCrosspostParam(opts.Crosspost)
		if err != nil {
			return nil, err
		}
		params.Set("crossposting_actions", enc)
	}
	if opts.DonateCharityID != "" {
		params.Set("donate_button_charity_id", opts.DonateCharityID)
	}

	var created fbLiveVideo
	err = fbPost(ctx, tgt.token, "/"+tgt.node+"/live_videos", params, &created)
```

And the two helpers, below `IngestFor`:

```go
// fbPrivacyParam is Graph's privacy object, which is a JSON document in a query
// parameter rather than a bare value.
func fbPrivacyParam(p db.FacebookPrivacy) string {
	return `{"value":"` + string(p) + `"}`
}

// fbCrosspostParam encodes the crossposting changes Graph documents.
//
// The two actions differ by whether a post is published as the Page. Defaulting
// to the quieter one is deliberate: a share nobody notices is recoverable, and a
// post published as somebody else's Page is not.
func fbCrosspostParam(targets []db.CrosspostTarget) (string, error) {
	type action struct {
		PageID string `json:"page_id"`
		Action string `json:"action"`
	}
	out := make([]action, 0, len(targets))
	for _, t := range targets {
		a := "enable_crossposting"
		if t.CreatePost {
			a = "enable_crossposting_and_create_post"
		}
		out = append(out, action{PageID: t.PageID, Action: a})
	}
	b, err := json.Marshal(out)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
```

**Note on the Page branch:** `tgt.kind != fbKindPage` suppresses privacy for Page
targets, because a Page broadcast is public by nature and sending a personal
audience value to one is a request Facebook has no meaning for. `fbKind` and
`fbKindPage` already exist — see `publishScopes` at `facebook.go:519`.

- [ ] **Step 5: Thread the destination through the API**

In `internal/api/oauth_handlers.go`, widen the `ingestFor` helper to take
`opts oauth.IngestOptions` and pass it to `tp.IngestFor`. At the call site
(`:394`), build it from the destination already in scope:

```go
	ing, broadcastID, err := s.ingestFor(ctx, provider, creds.ClientID, acct, oauth.IngestOptions{
		Privacy:         dest.Compliance.FacebookPrivacy,
		Crosspost:       dest.Facebook.Crosspost,
		DonateCharityID: dest.Facebook.DonateCharityID,
	})
```

- [ ] **Step 6: Run the tests**

Run: `go test ./internal/oauth/ ./internal/api/ -run 'Facebook|Ingest'`
Expected: PASS. Then `go build ./...` to catch any missed call site.

- [ ] **Step 7: Commit**

```bash
git add internal/oauth/facebook.go internal/oauth/facebook_test.go internal/api/oauth_handlers.go
git commit -m "feat(facebook): a broadcast is created with the privacy the operator chose"
```

- [ ] **Step 8: Prove each guard can fail**

1. Drop the `opts.Privacy != db.FBPrivacyUnchanged` condition so privacy is always
   set → `TestFacebookSendsNoPrivacyParameterWhenNoneWasChosen` must fail.
2. Make `fbCrosspostParam` always use `enable_crossposting_and_create_post` →
   `TestFacebookCrosspostingCarriesTheActionEachPageAskedFor` must fail on the
   count assertion.
3. Stop setting `privacy` at all →
   `TestFacebookSendsTheStoredPrivacyWhenTheBroadcastIsCreated` must fail.

---

### Task 4: Tag words, resolved to Facebook's ids

**Files:**
- Modify: `internal/oauth/metadata.go` (`Metadata`, `Empty`, `Trimmed`)
- Modify: `internal/oauth/facebook.go` (`MetadataCaps`, `writeLiveVideo`, new
  resolver)
- Modify: `ui/src/lib/types.ts`
- Test: `internal/oauth/facebook_test.go`

**Interfaces:**
- Consumes: the widened `IngestFor` from Task 3 (unchanged here).
- Produces: `Metadata.Tags []string`; Facebook's `MetadataCaps().Fields` gains
  `FieldTags`.

- [ ] **Step 1: Write the failing tests**

```go
func TestFacebookResolvesTagWordsIntoContentTagIDs(t *testing.T) {
	log := fbServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/search":
			writeJSONBody(t, w, http.StatusOK, map[string]any{"data": []map[string]any{
				{"id": "6003", "name": "Cooking"},
			}})
		case r.URL.Path == "/9":
			writeJSONBody(t, w, http.StatusOK, map[string]any{"success": true})
		default:
			http.Error(w, "{}", http.StatusNotFound)
		}
	})
	res, err := (&Facebook{}).UpdateLiveVideo(context.Background(), "cid", "user-token", "user:1000", "9",
		Metadata{Tags: []string{"cooking"}})
	if err != nil {
		t.Fatalf("UpdateLiveVideo: %v", err)
	}
	post := fbCall(*log, http.MethodPost, "/9")
	if post == nil {
		t.Fatalf("no edit; calls were %+v", *log)
	}
	if !strings.Contains(post.Query, "content_tags") || !strings.Contains(post.Query, "6003") {
		t.Errorf("edit query %q carries no resolved tag id", post.Query)
	}
	if !slices.Contains(res.Applied, FieldTags) {
		t.Errorf("applied = %v, want FieldTags", res.Applied)
	}
}

func TestATagWordThatMatchesNothingIsNamedInAWarning(t *testing.T) {
	// A tag that vanishes without comment is indistinguishable from one that
	// worked, and the operator has no way to find out which happened.
	fbServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/search":
			writeJSONBody(t, w, http.StatusOK, map[string]any{"data": []map[string]any{}})
		case r.URL.Path == "/9":
			writeJSONBody(t, w, http.StatusOK, map[string]any{"success": true})
		default:
			http.Error(w, "{}", http.StatusNotFound)
		}
	})
	res, err := (&Facebook{}).UpdateLiveVideo(context.Background(), "cid", "user-token", "user:1000", "9",
		Metadata{Title: "t", Tags: []string{"zzzznotathing"}})
	if err != nil {
		t.Fatalf("UpdateLiveVideo: %v", err)
	}
	joined := strings.Join(res.Warnings, " | ")
	if !strings.Contains(joined, "zzzznotathing") {
		t.Errorf("warnings %q do not name the tag that matched nothing", joined)
	}
}

func TestARefusedTagSearchStillAppliesTheTitle(t *testing.T) {
	// The tag lookup is an ads-surface endpoint and may not be reachable with
	// publish_video. A 403 there must never cost the operator a title change
	// seconds before air.
	fbServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/search":
			http.Error(w, `{"error":{"message":"(#10) permission"}}`, http.StatusForbidden)
		case r.URL.Path == "/9":
			writeJSONBody(t, w, http.StatusOK, map[string]any{"success": true})
		default:
			http.Error(w, "{}", http.StatusNotFound)
		}
	})
	res, err := (&Facebook{}).UpdateLiveVideo(context.Background(), "cid", "user-token", "user:1000", "9",
		Metadata{Title: "Tonight", Tags: []string{"cooking"}})
	if err != nil {
		t.Fatalf("a refused tag search failed the whole push: %v", err)
	}
	if !slices.Contains(res.Applied, FieldTitle) {
		t.Errorf("applied = %v, want the title to have landed anyway", res.Applied)
	}
	if !slices.Contains(res.Skipped, FieldTags) {
		t.Errorf("skipped = %v, want FieldTags", res.Skipped)
	}
}

func TestFacebookSendsNoContentTagsWhenTheOperatorTypedNone(t *testing.T) {
	log := fbServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/9" {
			writeJSONBody(t, w, http.StatusOK, map[string]any{"success": true})
			return
		}
		http.Error(w, "{}", http.StatusNotFound)
	})
	_, err := (&Facebook{}).UpdateLiveVideo(context.Background(), "cid", "user-token", "user:1000", "9",
		Metadata{Title: "Only a title"})
	if err != nil {
		t.Fatalf("UpdateLiveVideo: %v", err)
	}
	if fbCall(*log, http.MethodGet, "/search") != nil {
		t.Error("searched for tags nobody typed, on the path that runs seconds before air")
	}
	post := fbCall(*log, http.MethodPost, "/9")
	if post != nil && strings.Contains(post.Query, "content_tags") {
		t.Errorf("edit query %q sends empty content_tags", post.Query)
	}
}
```

Add `"slices"` to the imports.

- [ ] **Step 2: Run and watch fail**

Run: `go test ./internal/oauth/ -run 'TagWord|ResolvesTag|RefusedTag|NoContentTags' -v`
Expected: FAIL — `unknown field Tags in struct literal of type Metadata`.

- [ ] **Step 3: Add Tags to the shared Metadata**

In `internal/oauth/metadata.go`:

```go
	// Tags are words, not ids. Facebook's content_tags wants numeric
	// ad-interest ids, and the adapter resolves them the same way Category is
	// resolved -- because the id a platform actually wants is something only
	// that platform's own console can tell you.
	Tags []string `json:"tags,omitempty"`
```

Extend `Empty()` with `&& len(m.Tags) == 0`, and `Trimmed()` to trim each tag and
drop the ones that trim to nothing.

Add `tags?: string[];` to the composer metadata type in `ui/src/lib/types.ts`.

- [ ] **Step 4: Resolve and send**

In `internal/oauth/facebook.go`, add `FieldTags` to `MetadataCaps().Fields`, and
in `writeLiveVideo` before the POST:

```go
	// Tag words become ids, and a failure here is REPORTED rather than fatal.
	// /search?type=adinterest is an ads-surface endpoint that may not be
	// reachable with publish_video -- unverified, because this repo has no live
	// Facebook account to check it against -- and a title change seconds before
	// air must not be lost to a tag lookup.
	if len(m.Tags) > 0 {
		ids, warns, err := f.resolveTags(ctx, tgt, m.Tags)
		switch {
		case err != nil:
			res.Skipped = append(res.Skipped, FieldTags)
			res.Warnings = append(res.Warnings,
				"Facebook would not search for tags, so none were set: "+err.Error())
		case len(ids) > 0:
			b, mErr := json.Marshal(ids)
			if mErr != nil {
				return mErr
			}
			params.Set("content_tags", string(b))
			res.Applied = append(res.Applied, FieldTags)
		}
		res.Warnings = append(res.Warnings, warns...)
	}
```

and the resolver:

```go
// resolveTags turns operator words into Facebook's ad-interest ids, returning
// one warning per word that matched nothing.
//
// An unmatched word is a WARNING NAMING THE WORD, never a silent drop: a tag
// that disappears without comment looks exactly like one that worked.
func (f *Facebook) resolveTags(ctx context.Context, tgt *fbTarget, words []string) ([]string, []string, error) {
	var ids, warns []string
	for _, w := range words {
		var found struct {
			Data []struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"data"`
		}
		err := fbGet(ctx, tgt.token, "/search",
			url.Values{"type": {"adinterest"}, "q": {w}, "limit": {"1"}}, &found)
		if err != nil {
			return nil, nil, err
		}
		if len(found.Data) == 0 {
			warns = append(warns, fmt.Sprintf("no Facebook interest matches %q, so it was not set as a tag", w))
			continue
		}
		ids = append(ids, found.Data[0].ID)
	}
	return ids, warns, nil
}
```

`writeLiveVideo` currently has the signature
`writeLiveVideo(ctx, tgt *fbTarget, id string, m Metadata) error` and no access
to a result. **Widen it to
`writeLiveVideo(ctx, tgt *fbTarget, id string, m Metadata, res *MetadataResult) error`**
and pass the caller's `res`. Both callers — `PushMetadata` and `UpdateLiveVideo`
— already have one in scope. Keeping the tag work here rather than duplicating it
in both callers is the point: two copies is where the second one drifts.

**AND FIX THE EARLY RETURN IN BOTH CALLERS, WHICH WOULD OTHERWISE MAKE THIS
FEATURE UNREACHABLE.** `PushMetadata` (`facebook.go:~639`) and `UpdateLiveVideo`
(`facebook.go:~679`) each carry:

```go
	if m.Title == "" && m.Description == "" {
		return res, nil
	}
```

A push carrying only tags returns there and never reaches `writeLiveVideo` at
all. Both become:

```go
	// Tags alone are a reason to write. Without them in this condition a push
	// carrying only tags returns here having done nothing, and the composer
	// reports success for a request that never left.
	if m.Title == "" && m.Description == "" && len(m.Tags) == 0 {
		return res, nil
	}
```

This is why `TestFacebookResolvesTagWordsIntoContentTagIDs` sends **only** tags:
with the old condition it fails, which is what makes the guard worth having.

- [ ] **Step 5: Run the tests**

Run: `go test ./internal/oauth/ -run 'TagWord|ResolvesTag|RefusedTag|NoContentTags' -v`
Expected: PASS. Then the whole package.

- [ ] **Step 6: Commit**

```bash
git add internal/oauth/metadata.go internal/oauth/facebook.go \
        internal/oauth/facebook_test.go ui/src/lib/types.ts
git commit -m "feat(facebook): tag words become content tags, and say so when they do not"
```

- [ ] **Step 7: Prove each guard can fail**

1. Drop the `len(found.Data) == 0` branch's warning (just `continue`) →
   `TestATagWordThatMatchesNothingIsNamedInAWarning` must fail.
2. Return the search error from `writeLiveVideo` instead of recording it →
   `TestARefusedTagSearchStillAppliesTheTitle` must fail.
3. Call `resolveTags` unconditionally →
   `TestFacebookSendsNoContentTagsWhenTheOperatorTypedNone` must fail on the
   `/search` assertion.

---

### Task 4b: An operator can actually type the tags

**Added after Task 4 shipped**, because Task 4 built the whole resolution path
and left it unreachable: the composer's push body is `{title, description,
category, broadcast}` and there is no tags input anywhere. `Metadata.Tags` can
only ever be empty in production.

That is roadmap item 0's class exactly — a feature no operator can reach — and
it is a planning omission, not an implementer's: the spec puts tags in the
composer and the plan's File Structure never listed the composer.

**Note why nothing caught it.** `MetaField` already carried `"tags"` from an
earlier unrelated task, so `TestUITypesCanNameEveryMetadataField` passed
throughout. That guard proves the UI can NAME a field arriving in a push
RESULT; it says nothing about whether an operator can SET one. Same asymmetry
`internal/scheduler/action_drift_test.go` documents for schedule actions.

**Files:**
- Modify: `ui/src/pages/Dashboard.tsx` (the composer state, the input, and the
  push body around `metaFetch<MetaJob>("/metadata/push", …)`)
- Test: `internal/oauth/facebook_test.go` or a sibling drift test

**Interfaces:**
- Consumes: `Metadata.Tags []string` from Task 4.
- Produces: nothing Go-side.

- [ ] **Step 1: Write the failing guard**

A drift guard in the shape `internal/scheduler/action_drift_test.go` established
— read that file first, including its limitation note, and copy the reasoning
rather than only the code:

```go
// The composer must be able to SEND tags, not merely render them back.
//
// TestUITypesCanNameEveryMetadataField walks the field NAMES a push result can
// carry, and MetaField already listed "tags" before anything could set one --
// so that guard was green while the feature was unreachable. Naming a field in
// a result and offering an operator a way to fill it are different claims, and
// only the second one is what makes a feature exist.
//
// Matches the push body specifically, not the file: "tags" appears in
// Dashboard.tsx for unrelated reasons, so a whole-file search would pass on a
// composer that still cannot send them.
func TestTheComposerCanSendFacebookTags(t *testing.T) {
	path := filepath.Join("..", "..", "ui", "src", "pages", "Dashboard.tsx")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("cannot read %s: %v", path, err)
	}
	src := string(raw)
	body := strings.Index(src, `metaFetch<MetaJob>("/metadata/push"`)
	if body < 0 {
		t.Fatal("cannot find the metadata push call in Dashboard.tsx; this guard " +
			"is no longer looking where the push body lives, so it asserts nothing")
	}
	window := src[body:min(body+400, len(src))]
	if !strings.Contains(window, "tags") {
		t.Error("the composer's push body carries no tags field, so Metadata.Tags " +
			"is always empty in production and every line of tag resolution is " +
			"unreachable. Add it to the body and give the operator an input.")
	}
}
```

- [ ] **Step 2: Run it and watch it fail**

Run: `go test ./internal/oauth/ -run TestTheComposerCanSendFacebookTags -v`
Expected: FAIL naming the missing tags field.

- [ ] **Step 3: Add the input and send it**

In `ui/src/pages/Dashboard.tsx`: a `tags` state beside `title`/`description`/
`category`, an input in the same group as those three, and `tags` in the push
body. Split the typed value on commas, trim each, and drop empties — the server
trims too, but sending `["", "cooking"]` would make the resolver search for
nothing.

Gate the input on the same capability signal the other fields use, so it appears
only where the platform accepts tags. Read how `category` decides to render
before choosing the mechanism.

- [ ] **Step 4: Run the guard and the UI gates**

Run: `go test ./internal/oauth/ -run TestTheComposerCanSendFacebookTags -v`,
then `cd ui && npx tsc --noEmit && npx oxlint`.

- [ ] **Step 5: Commit**

```bash
git add ui/src/pages/Dashboard.tsx internal/oauth/
git commit -m "feat(ui): the operator can type the tags Facebook resolves"
```

- [ ] **Step 6: Prove the guard can fail**

Remove `tags` from the push body only, leaving the input in place → the guard
must fail. That is the mutation that matters: an input the operator can type
into whose value never leaves the browser is the same unreachable feature with
a better disguise.

---

### Task 5: Best-effort privacy on the push

**Files:**
- Modify: `internal/oauth/facebook.go` (`MetadataCaps`, the push path)
- Test: `internal/oauth/facebook_test.go`

**Interfaces:**
- Consumes: `db.FacebookPrivacy` (Task 1), the push path (Task 4).
- Produces: `(*Facebook).UpdateLiveVideoPrivacy(ctx context.Context, clientID,
  accessToken, targetRef, liveVideoID string, p db.FacebookPrivacy) (*MetadataResult, error)`,
  and `MetadataCaps().Fields` gains `FieldPrivacy`.

**Why this task exists:** privacy is applied at create, which means an operator
who changes it afterwards is editing a value that has already been used. The push
is the convenience that lets them change it without deleting the broadcast. It is
**never an error** when refused — the stored value is the guarantee.

**A deviation from the spec, decided while planning:** the spec says a Page
destination reports privacy as unsupported "up front through `MetadataCaps`".
`MetadataCaps()` takes no target, so it cannot vary by Page versus profile
without widening an interface every provider implements. Instead the Page case is
reported at push time as `Skipped` with a warning. Record this in the report.

- [ ] **Step 1: Write the failing tests**

```go
func TestFacebookPushAttemptsThePrivacyAndReportsIt(t *testing.T) {
	log := fbServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/9" {
			writeJSONBody(t, w, http.StatusOK, map[string]any{"success": true})
			return
		}
		http.Error(w, "{}", http.StatusNotFound)
	})
	res, err := (&Facebook{}).UpdateLiveVideoPrivacy(context.Background(), "cid", "user-token",
		"user:1000", "9", db.FBPrivacyFriends)
	if err != nil {
		t.Fatalf("UpdateLiveVideoPrivacy: %v", err)
	}
	post := fbCall(*log, http.MethodPost, "/9")
	if post == nil || !strings.Contains(post.Query, "ALL_FRIENDS") {
		t.Fatalf("privacy never reached the edit; calls were %+v", *log)
	}
	if !slices.Contains(res.Applied, FieldPrivacy) {
		t.Errorf("applied = %v, want FieldPrivacy", res.Applied)
	}
}

func TestARefusedPrivacyPushIsSkippedRatherThanAnError(t *testing.T) {
	// The stored value was already applied when the broadcast was created. A
	// refusal here costs a convenience, not the setting.
	fbServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/9" {
			http.Error(w, `{"error":{"message":"(#100) unsupported"}}`, http.StatusBadRequest)
			return
		}
		http.Error(w, "{}", http.StatusNotFound)
	})
	res, err := (&Facebook{}).UpdateLiveVideoPrivacy(context.Background(), "cid", "user-token",
		"user:1000", "9", db.FBPrivacyFriends)
	if err != nil {
		t.Fatalf("a refused privacy push became an error: %v", err)
	}
	if !slices.Contains(res.Skipped, FieldPrivacy) {
		t.Errorf("skipped = %v, want FieldPrivacy", res.Skipped)
	}
}
```

- [ ] **Step 2: Run and watch fail**

Run: `go test ./internal/oauth/ -run 'PrivacyPush|AttemptsThePrivacy' -v`
Expected: FAIL — undefined method.

- [ ] **Step 3: Implement**

Add `FieldPrivacy` to `MetadataCaps().Fields`, and a method that posts
`privacy={"value":"..."}` to `/<id>`, recording `Applied` on success and
`Skipped` plus the platform's own message on failure. Return an error only when
the caller gave a value `db.ValidFacebookPrivacy` rejects.

For a Page target, skip the call entirely and record:

```go
		res.Skipped = append(res.Skipped, FieldPrivacy)
		res.Warnings = append(res.Warnings,
			"a Page broadcast is public to everyone; privacy applies to profile broadcasts only")
```

- [ ] **Step 4: Run the tests, then the package**

Run: `go test ./internal/oauth/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/oauth/facebook.go internal/oauth/facebook_test.go
git commit -m "feat(facebook): privacy can be changed without recreating the broadcast"
```

- [ ] **Step 6: Prove each guard can fail**

1. Return the post error instead of recording `Skipped` →
   `TestARefusedPrivacyPushIsSkippedRatherThanAnError` must fail.
2. Stop appending `FieldPrivacy` to `Applied` →
   `TestFacebookPushAttemptsThePrivacyAndReportsIt` must fail.

---

### Task 6: Say what changed, where an operator meets it

**Files:**
- Modify: `docs/PLATFORMS.md` (Facebook's row in the capability matrix)
- Modify: `docs/roadmap/DESTINATION-SETTINGS.md` (the "What remains" table)

- [ ] **Step 1: Read both documents end to end**

Not only the sections named above. Roadmap item 5 shipped a sub-project whose
docs pass corrected one document and left the identical stale claim standing in
another, and item 10's own "What remains" table has already been wrong once. If
you find nothing else stale, say so explicitly in your report.

- [ ] **Step 2: Update `docs/PLATFORMS.md`**

Facebook's metadata entry currently reflects title and description only. State
what it now sends, and **state the two limits plainly**: privacy is applied when
the broadcast is created, so changing it later relies on an update endpoint Meta
does not document; and tag resolution needs an ads-surface search that may not be
reachable with `publish_video`.

- [ ] **Step 3: Update `docs/roadmap/DESTINATION-SETTINGS.md`**

Mark sub-project D shipped in the "What remains" table. Leave the rows for E
(scheduling, seven-day bound) and F (ingest-shaped fields) exactly as they are.

- [ ] **Step 4: Commit**

```bash
git add docs/PLATFORMS.md docs/roadmap/DESTINATION-SETTINGS.md
git commit -m "docs: what Facebook now receives, and the two limits on it"
```

---

## Final verification

- [ ] `gofmt -l ./internal ./cmd` — empty
- [ ] `go build ./...` and `go vet ./...` — clean
- [ ] `go test -race -timeout 15m ./...` — every package ok
- [ ] `cd ui && npx tsc --noEmit && npx oxlint` — clean
- [ ] Golden tables byte-unchanged: `git diff --stat main -- '*golden*' 'testdata/'`
- [ ] `git status` clean of anything unintended
