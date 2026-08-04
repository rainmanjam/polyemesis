# Compliance Dispatch Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** make `PushCompliance` reachable, so YouTube's COPPA declaration and
privacy status, Twitch's content labels, and Facebook's audience actually leave
the database.

**Architecture:** a `CompliancePusher` capability discovered like `MetadataFor`.
Compliance is stored per destination and the push is per account, so it is
resolved and conflict-checked once in the handler — before any goroutine — and
carried on `metadataTarget` into the existing per-account worker.

**Tech Stack:** Go, `internal/oauth` (hand-rolled platform calls, no SDK),
`internal/api`, `internal/db`.

## Global Constraints

- **Empty means LEAVE IT ALONE.** A destination with empty `Compliance` must
  produce exactly the API calls it produced before this existed. Assert the
  request is ABSENT, not that it carried empty values.
- **Every guard proven able to fail by a NAMED one-line mutation.** A mutation
  that fails to COMPILE is not a result. RUN it — on the previous branch, nine
  claims that a mutation would fire were made without running and five were
  wrong, from all three roles.
- **Commit before you mutate.** `git checkout --` reverts uncommitted work.
- **Comments must never assert what the code does not do.** One of the defects
  being fixed here IS such a comment.
- **The dispatch being CALLED is the thing under test.** A capability tested in
  isolation while nothing invokes it reproduces the exact bug this plan fixes.
  The previous branch did that twice.
- Kick has no compliance surface and must be ABSENT from results, not failing.

---

## File Structure

| File | Responsibility |
|---|---|
| `internal/oauth/compliance.go` | `ComplianceTarget`, `CompliancePusher`, `ComplianceFor`; YouTube and Twitch adapted to the new signature |
| `internal/oauth/facebook.go` | Facebook's `PushCompliance`, delegating to `UpdateLiveVideoPrivacy` |
| `internal/api/metadata.go` | resolve compliance per account from destinations, refuse conflicts, call the capability in `pushOne` |
| `internal/oauth/ui_drift_test.go` | a comment claiming compliance is pushed, which is currently false |

---

### Task 1: The capability

**Files:**
- Modify: `internal/oauth/compliance.go`
- Modify: `internal/oauth/facebook.go`
- Test: `internal/oauth/compliance_test.go` (exists)

**Interfaces:**
- Consumes: `db.Compliance`; `(*Facebook).UpdateLiveVideoPrivacy(ctx, clientID, accessToken, targetRef, liveVideoID string, p db.FacebookPrivacy)`; `oauth.FacebookLiveVideoID(streamKey string) string`.
- Produces: `oauth.ComplianceTarget{AccountRef, StreamKey string}`,
  `oauth.CompliancePusher`, `oauth.ComplianceFor(db.Platform) (CompliancePusher, bool)`.

- [ ] **Step 1: Write the failing test**

```go
func TestComplianceForFindsOnlyThePlatformsThatHaveOne(t *testing.T) {
	// Kick has no compliance surface. It must be ABSENT rather than present
	// and refusing, so the caller handles "this platform does not do this"
	// once instead of at every call site.
	for _, p := range []db.Platform{db.PlatformYouTube, db.PlatformTwitch, db.PlatformFacebook} {
		if _, ok := ComplianceFor(p); !ok {
			t.Errorf("ComplianceFor(%s) found nothing; its stored compliance can never be sent", p)
		}
	}
	if _, ok := ComplianceFor(db.PlatformKick); ok {
		t.Error("ComplianceFor(kick) claims a capability Kick does not have")
	}
}

func TestFacebookComplianceGoesThroughTheConfirmedPrivacyPath(t *testing.T) {
	// Graph documents no update surface for LiveVideo, so the only honest
	// report is one the platform confirmed. This must not grow a second,
	// unconfirmed path just because it is reached from somewhere new.
	fbServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/9":
			writeJSONBody(t, w, http.StatusOK, map[string]any{"success": true})
		case r.Method == http.MethodGet && r.URL.Path == "/9":
			writeJSONBody(t, w, http.StatusOK, map[string]any{
				"id": "9", "privacy": map[string]any{"value": "SELF"},
			})
		default:
			http.Error(w, "{}", http.StatusNotFound)
		}
	})
	cp, ok := ComplianceFor(db.PlatformFacebook)
	if !ok {
		t.Fatal("Facebook has no compliance capability")
	}
	res, err := cp.PushCompliance(context.Background(), "cid", "user-token",
		ComplianceTarget{AccountRef: "user:1000", StreamKey: facebookKeyForLiveVideo("9")},
		db.Compliance{FacebookPrivacy: db.FBPrivacySelf})
	if err != nil {
		t.Fatalf("PushCompliance: %v", err)
	}
	if !slices.Contains(res.Applied, FieldPrivacy) {
		t.Errorf("applied = %v, want FieldPrivacy after a confirmed read-back", res.Applied)
	}
}

func TestAnEmptyComplianceSendsNothingAtAll(t *testing.T) {
	// A destination that has never been given a compliance setting must produce
	// exactly the API calls it produced before this existed.
	log := fbServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSONBody(t, w, http.StatusOK, map[string]any{"success": true})
	})
	cp, _ := ComplianceFor(db.PlatformFacebook)
	if _, err := cp.PushCompliance(context.Background(), "cid", "user-token",
		ComplianceTarget{AccountRef: "user:1000"}, db.Compliance{}); err != nil {
		t.Fatalf("PushCompliance: %v", err)
	}
	if len(*log) != 0 {
		t.Errorf("an empty compliance made %d requests: %+v", len(*log), *log)
	}
}
```

`facebookKeyForLiveVideo` is a helper you write beside these, producing a stream
key in the form `FacebookLiveVideoID` parses. **Read `FacebookLiveVideoID` and
`TestFacebookLiveVideoIDIsRecoverableFromTheStoredStreamKey` first** and build
the key the way that test does, rather than inventing a format.

- [ ] **Step 2: Run and watch fail**

Run: `go test ./internal/oauth/ -run 'ComplianceFor|FacebookCompliance|EmptyCompliance' -v`
Expected: FAIL — `undefined: ComplianceFor`.

- [ ] **Step 3: Implement**

In `internal/oauth/compliance.go`:

```go
// ComplianceTarget is what a compliance write needs BESIDES the token, which
// differs per platform -- which is why this is a struct rather than three more
// parameters that two of the three implementations would ignore.
type ComplianceTarget struct {
	// AccountRef is the channel id recorded when the account was connected.
	// Twitch needs it. YouTube ignores it, because the Live Streaming API
	// scopes every call to the authenticated channel.
	AccountRef string
	// StreamKey is the DESTINATION's, and only Facebook uses it: its live
	// video id is recoverable from the stored key, and privacy belongs to that
	// broadcast rather than to the account.
	StreamKey string
}

// CompliancePusher writes the obligation metadata -- the fields db.Compliance
// documents as "not a nicety".
//
// A capability rather than a method on every provider, for the reason
// MetadataPusher is one: Kick has no compliance surface at all, and a stub
// whose only behaviour is to refuse is worse than an absence a caller handles
// once.
type CompliancePusher interface {
	Provider
	PushCompliance(ctx context.Context, clientID, accessToken string,
		tgt ComplianceTarget, c db.Compliance) (*MetadataResult, error)
}

// ComplianceFor returns the capability, or false when a platform has none.
// Discover it here; never type-assert at a call site.
func ComplianceFor(p db.Platform) (CompliancePusher, bool) {
	pr, ok := Providers()[p]
	if !ok {
		return nil, false
	}
	cp, ok := pr.(CompliancePusher)
	return cp, ok
}
```

Change YouTube's and Twitch's existing `PushCompliance` to the new signature,
reading `tgt.AccountRef` where Twitch currently takes `accountRef`. **Their
bodies do not otherwise change** — including YouTube's two-endpoint split, whose
comment explains that made-for-kids is not settable on the update call and
"anyone who assumes symmetry here writes a call that returns 200 and changes
nothing".

Add Facebook's in `facebook.go`: return an empty result when
`c.FacebookPrivacy` is unset; otherwise recover the live video id with
`FacebookLiveVideoID(tgt.StreamKey)` and delegate to `UpdateLiveVideoPrivacy`.
If the key yields no id, record `Skipped` with a warning saying the destination
has no Facebook broadcast recorded — never an error, because a destination whose
key was typed by hand legitimately has none.

- [ ] **Step 4: Run the tests, then the package**

Run: `go test ./internal/oauth/`

- [ ] **Step 5: Commit**

```bash
git add internal/oauth/compliance.go internal/oauth/facebook.go internal/oauth/compliance_test.go
git commit -m "feat(oauth): compliance becomes a capability something can find"
```

- [ ] **Step 6: Prove each guard can fail**

1. Make `ComplianceFor` return `nil, false` for Facebook →
   `TestComplianceForFindsOnlyThePlatformsThatHaveOne` must fail.
2. Have Facebook's `PushCompliance` report `Applied` without calling
   `UpdateLiveVideoPrivacy` → `TestFacebookComplianceGoesThroughTheConfirmedPrivacyPath`
   must fail.
3. Remove the `c.Empty()` early return →
   `TestAnEmptyComplianceSendsNothingAtAll` must fail.

---

### Task 2: Resolving compliance per account, and refusing conflicts

**Files:**
- Modify: `internal/api/metadata.go`
- Test: `internal/api/metadata_test.go` (exists)

**Interfaces:**
- Consumes: `db.Destination.Compliance`, `db.Destination.AccountID`, `db.Destination.StreamKey`.
- Produces: `complianceByAccount(dests []db.Destination) (map[int64]accountCompliance, []string)` —
  the map keyed by account id, and a slice of human-readable conflict messages.
  `accountCompliance{Compliance db.Compliance; StreamKey string; Destination string}`.

**Why a pure function:** the conflict rule is the decision this whole design
turns on, and it must be testable without a server, a store or a platform.

- [ ] **Step 1: Write the failing test**

```go
func TestTwoDestinationsOnOneAccountWithDifferentComplianceAreRefused(t *testing.T) {
	acct := int64(7)
	_, conflicts := complianceByAccount([]db.Destination{
		{Name: "main", AccountID: &acct, Compliance: db.Compliance{Privacy: db.PrivacyPrivate}},
		{Name: "backup", AccountID: &acct, Compliance: db.Compliance{Privacy: db.PrivacyPublic}},
	})
	if len(conflicts) == 0 {
		t.Fatal("two destinations asked one broadcast to be two things and it was allowed; " +
			"one of the operator's declarations would be discarded with nothing saying so")
	}
	// BOTH names, because "there is a conflict" the operator cannot locate is
	// barely better than the silence this replaces.
	if !strings.Contains(conflicts[0], "main") || !strings.Contains(conflicts[0], "backup") {
		t.Errorf("conflict %q does not name both destinations", conflicts[0])
	}
}

func TestTwoDestinationsAgreeingIsNotAConflict(t *testing.T) {
	// The rule refuses disagreement, not duplication. An install with a primary
	// and a backup destination on one account is ordinary.
	acct := int64(7)
	got, conflicts := complianceByAccount([]db.Destination{
		{Name: "main", AccountID: &acct, Compliance: db.Compliance{Privacy: db.PrivacyPrivate}},
		{Name: "backup", AccountID: &acct, Compliance: db.Compliance{Privacy: db.PrivacyPrivate}},
	})
	if len(conflicts) != 0 {
		t.Fatalf("identical compliance was refused: %v", conflicts)
	}
	if got[acct].Compliance.Privacy != db.PrivacyPrivate {
		t.Errorf("resolved privacy = %q, want private", got[acct].Compliance.Privacy)
	}
}

func TestADestinationWithNoComplianceContributesNothing(t *testing.T) {
	acct := int64(7)
	got, conflicts := complianceByAccount([]db.Destination{
		{Name: "configured", AccountID: &acct, Compliance: db.Compliance{Privacy: db.PrivacyPrivate}},
		{Name: "untouched", AccountID: &acct},
	})
	if len(conflicts) != 0 {
		t.Fatalf("an empty compliance was treated as disagreement: %v", conflicts)
	}
	if got[acct].Compliance.Privacy != db.PrivacyPrivate {
		t.Errorf("resolved privacy = %q, want the configured one", got[acct].Compliance.Privacy)
	}
}

func TestADestinationWithNoAccountIsIgnored(t *testing.T) {
	// A hand-typed destination has no token to push with.
	got, conflicts := complianceByAccount([]db.Destination{
		{Name: "manual", Compliance: db.Compliance{Privacy: db.PrivacyPrivate}},
	})
	if len(conflicts) != 0 || len(got) != 0 {
		t.Errorf("got %v / %v, want both empty", got, conflicts)
	}
}
```

- [ ] **Step 2: Run and watch fail**

Run: `go test ./internal/api/ -run 'Compliance' -v`
Expected: FAIL — `undefined: complianceByAccount`.

- [ ] **Step 3: Implement**

```go
// accountCompliance is one account's resolved compliance, and the destination
// it came from so a message can name it.
type accountCompliance struct {
	Compliance  db.Compliance
	StreamKey   string
	Destination string
}

// complianceByAccount resolves per-destination compliance onto the accounts a
// push actually addresses, and reports disagreements rather than resolving
// them.
//
// The mismatch is real and not incidental: compliance is stored per
// DESTINATION, a push is per ACCOUNT, and a compliance write targets whatever
// the token owns -- YouTube's takes no account reference at all. So two
// destinations on one account with different values are asking one broadcast to
// be two things, and picking one would discard a COPPA declaration with nothing
// anywhere saying so.
//
// Destinations with no account are skipped: a hand-typed key has no token to
// push with. Destinations with empty compliance contribute nothing and never
// conflict, because "not set" is not a disagreement with anything.
func complianceByAccount(dests []db.Destination) (map[int64]accountCompliance, []string) {
	out := map[int64]accountCompliance{}
	var conflicts []string
	for _, d := range dests {
		if d.AccountID == nil || d.Compliance.Empty() {
			continue
		}
		id := *d.AccountID
		prev, seen := out[id]
		if !seen {
			out[id] = accountCompliance{
				Compliance: d.Compliance, StreamKey: d.StreamKey, Destination: d.Name,
			}
			continue
		}
		if !reflect.DeepEqual(prev.Compliance, d.Compliance) {
			conflicts = append(conflicts, fmt.Sprintf(
				"%q and %q share one connected account but ask for different compliance "+
					"settings, and the platform has only one broadcast to apply them to. "+
					"Make them match, or point one at a different account.",
				prev.Destination, d.Name))
			delete(out, id)
		}
	}
	return out, conflicts
}
```

Sort `dests` by name before iterating, or the conflict message names the two in
whatever order the store returned — which makes the test flaky and the message
inconsistent between runs.

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/api/ -run 'Compliance' -v`

- [ ] **Step 5: Commit**

```bash
git add internal/api/metadata.go internal/api/metadata_test.go
git commit -m "feat(api): resolve compliance onto accounts, and refuse disagreement"
```

- [ ] **Step 6: Prove each guard can fail**

1. Drop the `reflect.DeepEqual` check so the first destination wins →
   `TestTwoDestinationsOnOneAccountWithDifferentComplianceAreRefused` must fail.
2. Report a conflict whenever an account is seen twice →
   `TestTwoDestinationsAgreeingIsNotAConflict` must fail.
3. Remove the `d.Compliance.Empty()` skip →
   `TestADestinationWithNoComplianceContributesNothing` must fail.

---

### Task 3: Calling it — the task the whole plan exists for

**Files:**
- Modify: `internal/api/metadata.go` (`metadataTarget`, `metadataTargets`, `handlePushMetadata`, `pushOne`)
- Test: `internal/api/metadata_test.go`

**Interfaces:**
- Consumes: Tasks 1 and 2.
- Produces: `metadataTarget` gains `Compliance db.Compliance` and `StreamKey string`.

**Read this before writing anything.** The defect being fixed is that
`PushCompliance` existed, was tested, and was never called. The previous branch
answered that same class of finding twice by testing the extracted function and
leaving the CALL unguarded, and both times a mutation at the call site stayed
green. **The first test below is the one that matters; write it first and make
sure it fails for the right reason.**

- [ ] **Step 1: Write the failing test**

```go
func TestAPushSendsTheStoredComplianceToTheProvider(t *testing.T) {
	// THE GUARD THIS PLAN EXISTS FOR. PushCompliance was implemented, tested
	// and never invoked; a test of the capability alone would have passed
	// throughout that. This one fails if the push stops calling it.
	//
	// Mutation: delete the ComplianceFor branch from pushOne. Run it.
	...
}
```

Write it against whatever seam `internal/api` already has for observing a
provider call — read `oauth_handlers_test.go`'s
`TestRefreshKeySendsTheDestinationsStoredFacebookOptionsToTheProvider`, which
solved this exact problem on the previous branch with a function field on
`Server`, and follow that shape rather than inventing a second one. If the
existing seam does not reach `pushOne`, add one in the same style and say so in
your report.

Also write: a conflict returns **400 before any request is made** (assert no
provider call happened, not merely that the status was 400), and a Kick target
produces no compliance attempt and no error.

- [ ] **Step 2: Run and watch them fail**

- [ ] **Step 3: Implement**

`metadataTargets` gains the destinations read and calls `complianceByAccount`,
attaching each account's resolved compliance and stream key to its target.
`handlePushMetadata` refuses with 400 and every conflict message when
`complianceByAccount` reports any. `pushOne` calls `ComplianceFor` and merges
the result's `Applied`/`Skipped`/`Warnings` into the outcome it already builds.

- [ ] **Step 4: Run the package**

Run: `go test ./internal/api/ ./internal/oauth/`

- [ ] **Step 5: Commit**

```bash
git add internal/api/metadata.go internal/api/metadata_test.go
git commit -m "feat(api): a metadata push finally sends the compliance settings"
```

- [ ] **Step 6: Prove each guard can fail**

1. Delete the `ComplianceFor` branch from `pushOne` →
   `TestAPushSendsTheStoredComplianceToTheProvider` must fail. **This is the
   mutation that matters. If it stays green, the guard is watching the
   capability rather than the call, and the whole task has reproduced the bug.**
2. Refuse conflicts AFTER starting the job → the no-request assertion must fail.
3. Include Kick in the compliance loop → the Kick test must fail.

---

### Task 4: The comment that says this already works

**Files:**
- Modify: `internal/oauth/ui_drift_test.go`
- Modify: `docs/PLATFORMS.md`
- Modify: `docs/roadmap/DESTINATION-SETTINGS.md`

- [ ] **Step 1: Fix the false comment**

`ui_drift_test.go` says *"The compliance fields are pushed through
PushCompliance"*. That was untrue when written and is true only after Task 3.
Make it describe what the code does now, and say where the push happens.

- [ ] **Step 2: Read both documents end to end**

Not only the compliance sections. Item 10's own "what remains" table has been
wrong twice, and a docs pass on the previous roadmap item corrected one file
while leaving the identical claim standing in another. If you find nothing else
stale, say so explicitly — "I found nothing" is a result; silence is not.

- [ ] **Step 3: State the behaviour change**

`docs/PLATFORMS.md` must say that compliance is now sent on a metadata push,
per platform, and that two destinations sharing an account must agree.

**And it must say that this is new.** Every install with stored compliance
starts writing it to platforms on the next push — including a destination
configured months ago with a setting nobody remembers. That is the point of the
work and it is still a change an operator did not ask for today.

- [ ] **Step 4: Commit**

```bash
git add internal/oauth/ui_drift_test.go docs/PLATFORMS.md docs/roadmap/DESTINATION-SETTINGS.md
git commit -m "docs: compliance is sent now, and that is a change"
```

---

## Final verification

- [ ] `gofmt -l ./internal ./cmd` — empty
- [ ] `go build ./...`, `go vet ./...` — clean
- [ ] `go test -race -timeout 20m ./...` — every package ok
- [ ] `cd ui && npx tsc --noEmit && npx oxlint` — clean
- [ ] Golden tables byte-unchanged
- [ ] `grep -rn '\.PushCompliance(' internal/ --include='*.go' | grep -v _test` returns **a caller**. That command returning nothing is the bug this branch exists to fix.
