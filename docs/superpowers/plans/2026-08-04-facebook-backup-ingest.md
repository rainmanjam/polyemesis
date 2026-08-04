# Facebook Backup Ingest Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Publish a second, redundant feed to Facebook's backup ingest endpoint, so a dropped primary connection does not drop the broadcast.

**Architecture:** A second supervised FFmpeg per backup-enabled destination, subscribed to the same relay hub under a **role-qualified** name, publishing to a backup URL stored when the broadcast was created. The backup carries its own restart signature and reconciles independently of the primary.

**Tech Stack:** Go 1.26, FFmpeg, Facebook Graph API v24.0, SQLite, React 19 + TypeScript.

## Global Constraints

- **The backup can never take the primary down.** A backup that crashes, stalls, or is refused by Facebook must leave the primary's process untouched — assert on the primary's PID, not on the destination still being "up".
- **Subscriber names must be role-qualified.** `Hub.Subscribe` is a map assignment: `h.subs[name] = ...`. Registering the backup as `dest:<id>` **replaces the primary's subscription and starves it**, while both processes look healthy with correct distinct URLs.
- **The backup toggle is absent from `destSpec`** — but absence alone does nothing. The backup needs its own signature (`backupSpec`) and its own reconciliation step.
- **A rotated backup key restarts ONLY the backup.**
- **Off by default.** No existing install changes behaviour on upgrade.
- **Enabling it costs one reconnect, and the UI must say so.** `IngestFor` creates a new `live_video` on every call, so obtaining a backup URL replaces `StreamKey`, which changes `Target()`, which is in `destSpec`. The primary necessarily cycles. Do not describe this as seamless.
- **Both creation paths store the backups**: `handleRefreshKey` and 10E's `announceOne`.
- Port pool is `[21000, 21500)`, shared across every source engine. **Exhaustion must refuse the backup, never the primary.**
- Every guard proven able to fail by a **named one-line mutation, run against a committed tree**.
- UI gate is `npm run build`, **not** `npx tsc --noEmit` — they are different checks and the weaker one passes code CI rejects.

---

## File Structure

| File | Responsibility |
|---|---|
| `internal/db/facebook.go` | `FacebookSettings.BackupIngest` — the toggle. Rides the existing `facebook` JSON column, so **no migration** |
| `internal/db/destinations.go` | `BackupURL`/`BackupStreamKey` + migration, `destColumns`, scan, insert, update |
| `internal/oauth/facebook.go` | `enable_backup_ingest` at create |
| `internal/api/oauth_handlers.go` | `ingestFor` carries backups; `handleRefreshKey` stores them and warns when none came |
| `internal/api/preannounce.go` | the scheduled path stores them too |
| `internal/engine/engine.go` | role-qualified names, the backup process, `backupSpec`, reconciliation, `DestStatus.BackupProcess` |
| `ui/src/lib/types.ts`, `ui/src/components/DestinationCard.tsx`, `DestinationDialog.tsx` | the toggle and the backup's state |

---

### Task 1: Storage

**Files:**
- Modify: `internal/db/facebook.go`, `internal/db/destinations.go`
- Test: `internal/db/facebook_settings_test.go`

**Interfaces:**
- Produces: `db.FacebookSettings.BackupIngest bool`, `db.Destination.BackupURL string`, `db.Destination.BackupStreamKey string`.

- [ ] **Step 1: Write the failing round-trip test**

```go
// The endpoint is stored in its own columns and the toggle rides the existing
// facebook JSON blob. A field that marshals and does not scan back is a backup
// URL that disappears on restart, and the destination then silently publishes
// one feed while the card says two.
func TestTheBackupEndpointAndToggleSurviveTheDatabase(t *testing.T) {
	d := testDB(t)
	created, err := d.CreateDestination(&Destination{
		Name: "fb", Kind: "rtmp", Platform: PlatformFacebook,
		URL: "rtmps://live.example/rtmp", StreamKey: "primary-key",
		BackupURL: "rtmps://backup.example/rtmp", BackupStreamKey: "backup-key",
		Facebook: FacebookSettings{BackupIngest: true},
	})
	if err != nil {
		t.Fatalf("CreateDestination: %v", err)
	}
	got, err := d.GetDestination(created.ID)
	if err != nil {
		t.Fatalf("GetDestination: %v", err)
	}
	if got.BackupURL != "rtmps://backup.example/rtmp" || got.BackupStreamKey != "backup-key" {
		t.Errorf("backup endpoint = %q / %q, want it preserved", got.BackupURL, got.BackupStreamKey)
	}
	if !got.Facebook.BackupIngest {
		t.Error("the toggle did not survive")
	}
}

// UPDATE is a separate statement from INSERT and forgetting one of them is the
// realistic mistake: creating works, editing silently reverts.
func TestUpdatingADestinationKeepsItsBackupEndpoint(t *testing.T) {
	d := testDB(t)
	created, _ := d.CreateDestination(&Destination{
		Name: "fb", Kind: "rtmp", Platform: PlatformFacebook,
		URL: "rtmps://live.example/rtmp", StreamKey: "k",
		BackupURL: "rtmps://backup.example/rtmp", BackupStreamKey: "bk",
	})
	created.Name = "renamed"
	if _, err := d.UpdateDestination(created); err != nil {
		t.Fatalf("UpdateDestination: %v", err)
	}
	got, _ := d.GetDestination(created.ID)
	if got.BackupURL == "" || got.BackupStreamKey == "" {
		t.Fatal("an unrelated edit erased the backup endpoint")
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/db/ -run 'BackupEndpoint' -v`
Expected: FAIL to build — no such fields.

- [ ] **Step 3: Add the toggle** (no migration — the `facebook` column already exists)

In `internal/db/facebook.go`, on `FacebookSettings`:

```go
	// BackupIngest asks Facebook to provision a secondary ingest endpoint at
	// create time, and publishes a redundant feed to it.
	//
	// Off by default: it doubles this destination's upload bandwidth and its
	// audio encoding cost, which an operator on a thin or metered uplink must
	// choose deliberately.
	//
	// Turning it on costs one reconnect. A backup endpoint only exists on a
	// broadcast created with it, and IngestFor creates a new live_video on
	// every call, so obtaining one replaces the primary stream key -- which is
	// in destSpec, so the primary restarts. Enable it before going live.
	BackupIngest bool `json:"backupIngest,omitempty"`
```

Leave `Empty()` alone: it answers "is there anything to SEND at create time", and this *is* a create-time parameter — so unlike the announcement marker, `BackupIngest` **should** count. Add it:

```go
func (f FacebookSettings) Empty() bool {
	return len(f.Crosspost) == 0 && f.DonateCharityID == "" && !f.BackupIngest
}
```

Check `TestTheMarkerDoesNotMakeSettingsLookNonEmpty` still passes — it asserts the *marker* does not count, which is unaffected.

- [ ] **Step 4: Add the endpoint columns**

In `internal/db/destinations.go`, on `Destination`, beside `StreamKey`:

```go
	// BackupURL and BackupStreamKey are the platform's secondary ingest,
	// stored when the broadcast was created. Empty when the platform offered
	// none.
	//
	// On Destination rather than in FacebookSettings because the engine
	// consumes it, and the engine should not have to know which platform a
	// destination is. Nothing but Facebook populates it today.
	BackupURL       string `json:"backupUrl,omitempty"`
	BackupStreamKey string `json:"backupStreamKey,omitempty"`
```

Add to `destColumns` after `stream_key`:

```
	backup_url, backup_stream_key,
```

Add the migrations beside the existing ones (~line 885):

```go
		{"backup_url", `ALTER TABLE destinations ADD COLUMN backup_url TEXT NOT NULL DEFAULT ''`},
		{"backup_stream_key", `ALTER TABLE destinations ADD COLUMN backup_stream_key TEXT NOT NULL DEFAULT ''`},
```

Then update **the scan, the INSERT and the UPDATE** to match `destColumns`' order. All three are explicit; missing any one is the bug the tests above catch.

- [ ] **Step 5: Run to verify both pass**

Run: `go test ./internal/db/ -v` — the whole package, because the column list is shared.
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/db/
git commit -m "feat(db): store a destination's backup ingest endpoint"
```

- [ ] **Step 7: Mutate**

| Mutation | Expected |
|---|---|
| drop `backup_url` from the UPDATE statement | `TestUpdatingADestinationKeepsItsBackupEndpoint` red |
| drop both columns from `destColumns` | round-trip test red |
| `BackupIngest` json tag → `json:"-"` | round-trip test red |

---

### Task 2: Ask Facebook for the backup endpoint

**Files:**
- Modify: `internal/oauth/facebook.go` (`IngestOptions`, `IngestFor`)
- Test: `internal/oauth/facebook_test.go`

**Interfaces:**
- Consumes: `db.FacebookSettings.BackupIngest` (Task 1).
- Produces: `oauth.IngestOptions.BackupIngest bool`.

- [ ] **Step 1: Write the failing test**

Uses the existing `fbServer` / `graphStub` / `fbLiveResponse` / `fbCall` helpers. `fbReq.Query` is a raw string — parse it with `url.ParseQuery`.

```go
func TestBackupIngestIsRequestedOnlyWhenTheDestinationAsksForIt(t *testing.T) {
	for _, tc := range []struct{ name string; on bool; want string }{
		{"asked for", true, "true"},
		{"not asked for", false, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			log := fbServer(t, graphStub(t, fbLiveResponse("777")))
			if _, err := (&Facebook{}).IngestFor(context.Background(), "cid", "user-token", "",
				IngestOptions{BackupIngest: tc.on}); err != nil {
				t.Fatalf("IngestFor: %v", err)
			}
			post := fbCall(*log, http.MethodPost, "/me/live_videos")
			q, err := url.ParseQuery(post.Query)
			if err != nil {
				t.Fatalf("parse query: %v", err)
			}
			got, present := q["enable_backup_ingest"]
			if tc.want == "" {
				// ABSENT, not empty: Facebook reads a present-but-empty
				// parameter as a value.
				if present {
					t.Errorf("enable_backup_ingest was sent for a destination that did not ask")
				}
				return
			}
			if !present || got[0] != tc.want {
				t.Errorf("enable_backup_ingest = %v, want %q", got, tc.want)
			}
		})
	}
}

// The backups have to reach the caller. They are parsed today and dropped by
// everything above; this pins that IngestFor itself returns them.
func TestTheCreateResponsesBackupEndpointsReachTheCaller(t *testing.T) {
	fbServer(t, graphStub(t, fbLiveResponse("777")))
	b, err := (&Facebook{}).IngestFor(context.Background(), "cid", "user-token", "",
		IngestOptions{BackupIngest: true})
	if err != nil {
		t.Fatalf("IngestFor: %v", err)
	}
	if len(b.Backups) == 0 {
		t.Fatal("no backup ingest was returned; fbLiveResponse carries " +
			"secure_stream_secondary_urls and it is being dropped")
	}
	if b.Backups[0].Key == "" || b.Backups[0].URL == "" {
		t.Errorf("backup ingest is not split into URL and key: %+v", b.Backups[0])
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/oauth/ -run 'BackupIngestIsRequested|BackupEndpointsReach' -v`
Expected: FAIL to build on `IngestOptions.BackupIngest`.

- [ ] **Step 3: Implement**

Add to `IngestOptions`:

```go
	// BackupIngest asks Facebook to provision a secondary ingest endpoint.
	//
	// Whether the secondary URLs come back WITHOUT this is not established --
	// our own fixture returns them unconditionally, and a fixture is not
	// evidence about Meta. So the caller must handle an empty Backups even
	// when this was true, rather than assuming the request guarantees them.
	BackupIngest bool
```

In `IngestFor`, beside the other create-time parameters:

```go
	if opts.BackupIngest {
		params.Set("enable_backup_ingest", "true")
	}
```

`Backups` is already populated — confirm with the second test rather than changing the parsing.

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./internal/oauth/ -run 'BackupIngestIsRequested|BackupEndpointsReach' -v`
Expected: PASS.

- [ ] **Step 5: Commit and mutate**

```bash
git add internal/oauth/
git commit -m "feat(oauth): ask Facebook for a backup ingest endpoint"
```

| Mutation | Expected |
|---|---|
| send `enable_backup_ingest` unconditionally | the "not asked for" subtest red |
| never send it | the "asked for" subtest red |
| return `nil` for `b.Backups` | `TestTheCreateResponsesBackupEndpointsReachTheCaller` red |

---

### Task 3: Stop discarding the backups — on BOTH paths

**Files:**
- Modify: `internal/api/oauth_handlers.go` (`ingestFor`, `ingestOptionsFor`, `handleRefreshKey`)
- Modify: `internal/api/preannounce.go` (`announceOne`)
- Test: `internal/api/oauth_handlers_test.go`, `internal/api/preannounce_test.go`

**Interfaces:**
- Consumes: Task 2's `IngestOptions.BackupIngest`.
- Produces: `ingestFor` returns the backups. Change its signature to return `*oauth.Broadcast` rather than growing a fourth value:

```go
func (s *Server) ingestFor(ctx context.Context, provider oauth.Provider, clientID string,
    acct *db.PlatformAccount, opts oauth.IngestOptions) (*oauth.Broadcast, error)
```

Non-targeted providers synthesise a `&oauth.Broadcast{Ingest: *ing}` with no ID and no backups, so every caller reads one shape. **`ingestForFn` changes with it**, and so does every test that replaces that seam — including 10E's `stubAnnounce`.

- [ ] **Step 1: Write the failing tests**

Three, and the second is the one a refresh-key-only implementation fails:

1. `TestRefreshKeyStoresTheBackupEndpoint` — POST the refresh-key route with a stubbed `ingestForFn` returning backups; assert the stored destination carries `BackupURL`/`BackupStreamKey`.
2. `TestTheScheduledPathStoresTheBackupEndpointToo` — drive `s.preannounceOnce` the way 10E's tests do; assert the same. **This is the path a refresh-key-only implementation silently loses.**
3. `TestABackupWasAskedForAndNoneCameBackWarnsTheOperator` — stub the seam to return no backups with the toggle on; assert the response's `warnings` names it, and that `BackupURL` is empty rather than garbage.

Write the bodies against the existing fixtures: `renditionServer` for the handler path (it needs an engine), `testServer` for `preannounceOnce` (it does not).

- [ ] **Step 2: Run to verify they fail**

Expected: FAIL — no backup is stored on either path.

- [ ] **Step 3: Implement**

Widen `ingestFor` and `ingestForFn`, thread `BackupIngest` through `ingestOptionsFor`, and store on both paths. In `handleRefreshKey`, when the toggle is on and no backups came back:

```go
	warnings = append(warnings, "Facebook did not offer a backup ingest endpoint "+
		"for this broadcast, so no redundant feed will be published. The "+
		"destination is otherwise configured correctly.")
```

surfaced through the same `warnings` array destination writes already use.

- [ ] **Step 4: Run, commit, mutate**

| Mutation | Expected |
|---|---|
| `handleRefreshKey` drops the backups | test 1 red |
| `announceOne` drops the backups | **test 2 red** — the row that catches the forgotten path |
| store the backups but emit no warning when none came | test 3 red |

---

### Task 4: The backup process

The largest task. Read all of it before starting.

**Files:**
- Modify: `internal/engine/engine.go`
- Test: `internal/engine/engine_test.go` (follow the existing destination tests for fixture setup)

**Interfaces:**
- Consumes: Tasks 1 and 3.
- Produces: `destination.backup`, `backupSpec()`, and role-qualified subscriber names.

- [ ] **Step 1: Role-qualify the subscriber name FIRST, on its own**

This is a prerequisite and a hazard in one. `startDest` currently does:

```go
	subName := fmt.Sprintf("dest:%d", row.ID)
```

Make the role explicit at every call site rather than defaulting it, so a future third output cannot silently collide:

```go
// destSubName is the relay subscription for one of a destination's outputs.
//
// Hub.Subscribe is a MAP ASSIGNMENT keyed by this name -- registering two
// outputs under one name replaces the first, and the replaced process then
// receives no packets at all while still running and still showing a correct
// command line. Nothing about the process, the card or the target URL reveals
// it. So the role is part of the name, always.
func destSubName(id int64, role string) string {
	if role == "" {
		return fmt.Sprintf("dest:%d", id)
	}
	return fmt.Sprintf("dest:%d:%s", id, role)
}
```

Keep the primary's name byte-identical (`dest:<id>`) so nothing about existing subscriptions changes.

- [ ] **Step 2: Write the guard that would have caught the collision**

```go
// The bug this exists for: both outputs registered under one name, so the
// primary silently receives nothing while looking entirely healthy.
//
// Asserts on the HUB's subscriber set, not on process count or target URLs --
// those are correct in the broken case, which is what makes it dangerous.
func TestBothOutputsAreSubscribedUnderDistinctNames(t *testing.T) {
	// Start a backup-enabled destination, then assert the hub holds both
	// dest:<id> and dest:<id>:backup, on different ports.
}
```

- [ ] **Step 3: Add the backup to the `destination` struct**

```go
	// backup is the redundant output, when this destination has one. It is a
	// separate supervised process on a separate subscription and port, which
	// is what makes "the backup cannot take the primary down" true rather
	// than hopeful.
	backup     *supervisor.Process
	backupPort int
	backupSub  string
	// backupSpec is the backup's OWN restart hash. Separate from spec so the
	// two cycle independently: a rotated backup key must restart only the
	// backup, and nothing about the backup may ever restart the primary.
	backupSpec string
	backupErr  string
```

- [ ] **Step 4: `backupSpec`**

```go
// backupSpec hashes everything on the BACKUP's command line.
//
// Deliberately not derived from destSpec: they share most inputs but must not
// share a verdict. Enabling backup mid-broadcast already costs a reconnect for
// an unavoidable reason (a new broadcast means a new primary key); it must not
// cost a second one for an avoidable reason.
func backupSpec(row *db.Destination, compiled routing.Result, upstream string) string {
	return hashStrings([]string{
		strconv.FormatBool(row.Facebook.BackupIngest),
		row.BackupURL, row.BackupStreamKey,
		compiled.FilterComplex, strconv.Itoa(row.AudioBitrate),
		strconv.Itoa(row.Profile.SampleRate), upstream,
		strconv.Itoa(compiled.VideoDelayMS),
		row.ExtraInputArgs, row.ExtraOutputArgs,
		strconv.FormatBool(row.Transport.NoDurationFilesize),
		strconv.Itoa(row.Transport.MuxQueuePackets),
		strconv.Itoa(row.Transport.MuxQueueBytes),
		strconv.Itoa(row.Transport.RWTimeoutSeconds),
		row.Audio.Codec, strconv.FormatBool(row.Audio.Mono),
	})
}
```

- [ ] **Step 5: Reconcile the backup separately**

In the destination reconcile loop, after the primary is settled: start the backup when the toggle is on and an endpoint exists; stop it when either goes away; restart it when `backupSpec` changed. **The primary is never consulted.**

Port allocation asks **last** and treats failure as "no backup", not as a destination error:

```go
	port, err := e.alloc.Allocate()
	if err != nil {
		// 500 ports, shared across every source engine. Exhaustion must cost
		// the redundancy and never the broadcast, so this is a warning on the
		// backup and nothing else.
		d.backupErr = "no relay port available for the backup feed"
		e.log.Warn("backup ingest has no port", "destination", row.Name)
		return
	}
```

- [ ] **Step 6: The remaining guards**

| Test | What makes it able to fail |
|---|---|
| `TestARotatedBackupKeyRestartsOnlyTheBackup` | captures the primary's PID across the change |
| `TestDisablingBackupUnsubscribesAndReleasesThePort` | asserts the hub subscriber is gone AND the port is released — stopping the process alone leaks both |
| `TestABackupThatExitsLeavesThePrimaryRunning` | asserts the primary's PID is unchanged, not that the destination is "up" |
| `TestPortExhaustionRefusesTheBackupNotThePrimary` | exhaust the allocator, assert the primary starts |
| `TestBackupOffStartsExactlyOneProcessAndHoldsOnePort` | the rule must not widen |

- [ ] **Step 7: Commit and mutate**

| Mutation | Expected |
|---|---|
| `destSubName` ignores `role` | `TestBothOutputsAreSubscribedUnderDistinctNames` red |
| `backupSpec` returns `destSpec`'s value | rotated-key test red (or the primary cycles) |
| the backup's port failure returns an error for the destination | port-exhaustion test red |
| stopping the backup skips `Unsubscribe`/`Release` | disable test red |

---

### Task 5: Report the backup

**Files:**
- Modify: `internal/engine/engine.go` (`DestStatus`), `ui/src/lib/types.ts`, `ui/src/components/DestinationCard.tsx`
- Test: `internal/db/facebook_ui_drift_test.go`

- [ ] **Step 1: Failing drift guard**

```go
// A backup nobody can see is worse than no backup: the operator believes they
// have redundancy. Watches the RENDERED state, not the type.
func TestTheCardShowsTheBackupFeedsState(t *testing.T) {
	path := filepath.Join("..", "..", "ui", "src", "components", "DestinationCard.tsx")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("cannot read %s: %v", path, err)
	}
	if !strings.Contains(string(raw), "backupProcess") {
		t.Error("the card does not render the backup feed's state, so a backup " +
			"that has been dead for an hour looks identical to a healthy one.")
	}
}
```

- [ ] **Step 2–5:** Add `BackupProcess *supervisor.Status \`json:"backupProcess,omitempty"\`` to `DestStatus`, populate it from `d.backup`, mirror it in `types.ts`, render it on the card beside the primary's state. Run `npm run build` and `npx oxlint`. Commit. Mutate by deleting the render — the guard must go red.

---

### Task 6: The toggle in the dialog

**Files:**
- Modify: `ui/src/components/DestinationDialog.tsx`, `internal/db/facebook_ui_drift_test.go`

- [ ] **Steps:** A checkbox in the Facebook section, shown only for Facebook destinations, with copy that states the cost plainly:

> Publishes a second copy of this stream to Facebook's backup endpoint, so a dropped connection does not drop the broadcast. **Doubles this destination's upload bandwidth**, and turning it on reconnects the stream once — enable it before you go live.

Guard it the way the Page-privacy control is guarded: a drift test matching the rendered control, not the type. Mutate by deleting the control.

---

## Final gates

```bash
gofmt -l ./internal ./cmd          # must print nothing
go build ./... && go vet ./...
go test -race -timeout 20m ./...
cd ui && npm run build && npx oxlint
```

`gofmt -l` exits 0 even when it lists files — read its output. And the UI gate is `npm run build`; `npx tsc --noEmit` is a weaker check that has already passed code CI rejected.

## Docs to update before the PR

- `docs/roadmap/DESTINATION-SETTINGS.md` — `enable_backup_ingest` moves out of "still missing".
- `docs/PLATFORMS.md` — if it carries a Facebook capability list.

## Known weakness in this plan

Tasks 3, 4 (steps 2 and 6), 5 and 6 name their tests and assertions but do not
give every body as code. They depend on engine fixtures whose exact shape has to
be read off the existing destination tests, and speculative code here would be
rewritten.

**The mutation table is the contract.** If a test cannot be made to fail against
its named mutation, the test is wrong — not the mutation. That rule caught two
non-guards in 10E and one in the chat work, both of which looked fine.
