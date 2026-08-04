# A Facebook broadcast that survives losing its connection

**What:** roadmap item 10 Part F, first slice. Publish a second, redundant feed
to Facebook's backup ingest endpoint, so a dropped primary connection does not
drop the broadcast.

**Why:** a live broadcast has no second take. Every other resilience control in
polyemesis — backoff, give-up, mux queues — makes *reconnecting* better. None
helps during the seconds it takes, and for a live audience those seconds are the
whole problem.

> **This document was rewritten after an adversarial review.** The first draft's
> headline claim was wrong, and four of its eight test rows would have passed
> against a broken feature. Both are recorded below rather than quietly fixed,
> because the corrections are the useful part.

## The finding that started it

`oauth.Broadcast` already carries this:

```go
// Backups are the platform's secondary ingest endpoints, for a redundant
// encoder feed. Exposed even though nothing consumes them yet, because they
// arrive in the same response and re-fetching them means creating a second
// broadcast.
Backups []Ingest `json:"-"`
```

Parsed out of **every** Facebook create response and thrown away: `ingestFor`
returns `b.Ingest` and `b.ID` and drops the rest. The comment is honest that
nothing consumes them, so this is a gap rather than a lie — but it means the
platform half already works.

## Why 10F is four features and this is one

| Piece | Needs | Status |
|---|---|---|
| **Backup ingest, end to end** | a second output plus reconciliation | **this spec** |
| `stop_on_delete_stream` | one create-time boolean | trivial, deferred |
| 360 / spatial audio | create-time params | cheap, unverifiable without VR hardware |
| Frame-accurate go-live | an in-process RTMP relay | blocked |

**Frame-accurate go-live cannot be done with FFmpeg, and this was checked.** It
needs an AMF0 `onGoLive` message at a chosen frame. The FLV muxer's flags only
ever *suppress* metadata (`no_metadata`, `no_sequence_end`,
`no_duration_filesize`, `add_keyframe_index`). The near-miss is `-rtmp_conn`,
documented as *"Append arbitrary AMF data to the Connect message"* — **Connect
message only**, handshake time, not frame N. Its own spec.

## The correction that changed the promise

**The first draft claimed: "turning redundancy on must not interrupt the thing
it protects." For Facebook, that is not achievable, and the reason is
structural.**

- A backup URL only exists on a broadcast created with it. `Facebook.IngestFor`
  **creates a new `live_video` on every call** — there is no way to ask an
  existing broadcast for a backup endpoint it was not created with.
- Getting one therefore means a key refresh, which replaces `StreamKey`.
- `Destination.Target()` is `URL + "/" + StreamKey`, and `Target()` is the first
  element of `destSpec`.
- So the primary's restart hash changes, and the primary cycles. Necessarily.

**The feature is therefore "enable this before you go live", and the UI must say
so.** Promising seamlessness and delivering a reconnect is worse than saying
plainly that enabling redundancy costs one reconnect now.

What survives from the original reasoning is the other direction, and it still
matters: turning backup **off**, and every unrelated reconcile, must not touch
the primary. That is why the toggle stays out of `destSpec` — but that alone is
not enough, see below.

## Absence from `destSpec` is necessary and not sufficient

Leaving the toggle out of the hash stops it cycling the primary. It does **not**
make it take effect: nothing would ever read it.

Resilience works because `startDestinations` explicitly calls `applyDestPolicy`
on an already-running process. The backup needs the same treatment and more —
its own desired state, its own signature, and its own reconciliation step:

```go
// backupSpec is the backup process's own restart hash. Separate from destSpec
// so the two cycle independently: a rotated backup key must restart ONLY the
// backup, and nothing about the backup may ever restart the primary.
func backupSpec(row *db.Destination, compiled routing.Result, upstream string) string
```

It must cover: whether backup is enabled, the backup URL and key, the upstream
hub, the compiled routing result, transport tuning, and audio settings —
everything that appears on the backup's command line. A signature missing any
of those reproduces the bug `destSpec`'s own comment warns about: a stored
setting that never reaches the running process.

## The bug a naive implementation would ship

**`Hub.Subscribe` replaces by name.** It is a map assignment:

```go
h.subs[name] = &subscriber{name: name, addr: &net.UDPAddr{IP: ip, Port: port}}
```

and `startDest` hard-codes `subName := fmt.Sprintf("dest:%d", row.ID)`.

So "call `startDest` twice" registers the backup under the primary's name,
**replacing it**. Both processes are alive, both have correct and distinct
Facebook targets, both look healthy on the card — and the primary receives no
packets at all.

Subscriber names must be role-qualified: `dest:<id>` and `dest:<id>:backup`.
This is called out here because every obvious test passes while it is broken.

## Storage

**The toggle goes on `db.FacebookSettings`, not `DestResilience`.**

The first draft put it on `DestResilience`. That is wrong twice: that type means
supervisor backoff and give-up, and its `Active()` reports
`MinBackoffSeconds > 0 || MaxBackoffSeconds > 0 || GiveUpAfter > 0` — a new
boolean would be invisible to it. `FacebookSettings` is already documented as
"per-destination Facebook configuration applied when the broadcast is CREATED",
which is exactly what `enable_backup_ingest` is.

```go
// BackupIngest asks Facebook to provision a secondary ingest endpoint at
// create time, and publishes a redundant feed to it. Off by default: it
// doubles this destination's upload bandwidth and its audio encoding cost.
BackupIngest bool `json:"backupIngest,omitempty"`
```

**The endpoint itself goes on `db.Destination`**, beside `URL` and `StreamKey`,
because the engine consumes it and should not have to know which platform a
destination is:

```go
BackupURL       string `json:"backupUrl,omitempty"`
BackupStreamKey string `json:"backupStreamKey,omitempty"`
```

**Persistence is not free.** Destination reads use an explicit column list and
scan order; create and update are explicit SQL. This needs a migration,
`destColumns`, the scan, the insert, the update, and a round-trip test. Missing
any one produces a toggle that is accepted and a backup URL that disappears on
reload — which is the same class of defect as a stored COPPA declaration that
never left the database.

## Both creation paths must store the backups

`ingestFor` returns `(*oauth.Ingest, string, error)` and drops the rest. It
widens to carry the backups, and **both** callers store them:

- `handleRefreshKey` — the manual path.
- `preannounceOnce`/`announceOne` — the scheduled path added by 10E, which today
  persists only the primary key and the broadcast id.

A refresh-key-only implementation silently loses backup ingest for every
scheduled broadcast, which is the shape this repository keeps finding: the
feature works on the path someone tested and not on the one they forgot.

## Where the backup process lives

`destination` holds one `proc *supervisor.Process`, and `DestStatus` exposes one
`Process`. There is nowhere to keep a backup's process, its port, its
subscription, its error or its failed state.

So this needs model work, not just another `supervisor.Process`:

```go
type destination struct {
    // ... existing fields
    backup     *supervisor.Process
    backupPort int
    backupSub  string
    backupSpec string
    backupErr  string
}
```

and a `BackupProcess *supervisor.Status` on `DestStatus`. "The card shows the
backup's state" and "give-up applies independently" both depend on this
existing; the first draft asserted them without it.

## What isolation actually buys, stated honestly

The first draft said "a dead backup never affects the primary". **That is
overstated.** Process supervision does isolate a crash — the backup exiting
never calls `Stop` on the primary — but the two share:

- **The hub's single fanout goroutine and socket.** One per hub, for all
  consumers.
- **The port allocator: 500 ports, `[21000, 21500)`, shared across every source
  engine.** Every backup-enabled destination consumes one more. Exhaustion must
  degrade the **backup** and never the primary, which means the backup asks for
  its port last and treats failure as "no backup today", not as an error on the
  destination.
- **CPU, memory, file descriptors and uplink.** A second FFmpeg decodes, filters
  and re-encodes audio again, and uploads a second copy.

So the honest claim is: **a backup that crashes, stalls or is refused by Facebook
cannot take the primary down.** A backup that saturates the machine or the uplink
can degrade it, exactly as any other second output would.

## The two feeds are redundant, not identical

Each process is an independent UDP receiver with its own kernel and FFmpeg
queues, and `RelayInputURL` deliberately treats overflow as nonfatal loss. The
hub fans packets out serially, so both see the same order — but under pressure
they can lose *different* packets and then encode audio independently.

This is redundancy against a lost connection. It is **not** a frame-identical
mirror, and nothing here should imply Facebook can cut between them invisibly.

## An uncertainty this design must survive

It is **not established** whether `enable_backup_ingest` is required before
Facebook returns secondary URLs, or merely requests more of them. Our own test
fixture returns `stream_secondary_urls` unconditionally, and a fixture is not
evidence about Meta.

So: toggle on, no backup URL in the response → store none and **tell the
operator the platform offered no backup endpoint**, through the response's
`warnings` array, the same channel destination writes already use. A log line is
not enough; a toggle that silently does nothing is the failure this project
keeps finding.

## Testing

Every guard proven able to fail by a named one-line mutation. **Four rows from
the first draft were removed for passing against a broken feature**; each
replacement says what it watches instead.

| Case | What makes it able to fail |
|---|---|
| Both processes receive relay packets, under DISTINCT subscriber names | The replace-by-name bug: asserting "two processes, two URLs" passes while the primary is starved. Asserts on the hub's subscriber set, not on process count |
| A rotated backup key restarts ONLY the backup | Watches the primary's PID across the change. "Toggle does not restart primary" alone passes when the toggle is ignored entirely |
| Enabling backup starts a backup process AND the card reports it | Pairs the effect with its report. Either alone passes while the other is missing |
| Disabling it stops the process, unsubscribes the hub, and releases the port | Leak-shaped: stopping the process alone leaves a stale subscriber and a held port, and nothing else would notice until the pool ran out |
| A backup that exits does not stop the primary, and the primary's PID is unchanged | Asserts on the primary rather than on the destination still being "up" |
| Port exhaustion refuses the BACKUP and leaves the primary running | The failure mode the shared 500-port pool creates |
| `enable_backup_ingest` is sent only when the toggle is on, and the returned backups are STORED | The send and the store together; the send alone passes while `ingestFor` discards them |
| The SCHEDULED path stores backups too | The path a refresh-key-only implementation forgets |
| A response with no backup URL stores none and returns a warning | Asserts on the response's `warnings`, not on a log line |
| A destination with the toggle off starts exactly one process and holds one port | The rule must not widen |

## Out

- **Automatic failover between primary and backup.** Facebook decides which feed
  it takes; polyemesis publishes both. Choosing for it would need to know what
  Facebook is currently ingesting, which no endpoint reports.
- **Backup ingest for other platforms.** The storage is general on purpose, but
  nothing else offers one today, and a UI for a field no platform populates is
  how unreachable settings appear.
- **The other three parts of 10F**, per the table above.
