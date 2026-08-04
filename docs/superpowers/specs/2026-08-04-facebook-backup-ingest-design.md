# A Facebook broadcast that survives losing its connection

**What:** roadmap item 10 Part F, first slice. Publish a second, redundant feed
to Facebook's backup ingest endpoint, so a dropped primary connection does not
drop the broadcast.

**Why:** a live broadcast has no second take. Every other resilience control in
polyemesis — backoff, give-up, mux queues — makes *reconnecting* better. None of
them helps during the seconds it takes, and for a live audience those seconds
are the whole problem.

## The finding that started it

`oauth.Broadcast` already carries this:

```go
// Backups are the platform's secondary ingest endpoints, for a redundant
// encoder feed. Exposed even though nothing consumes them yet, because they
// arrive in the same response and re-fetching them means creating a second
// broadcast.
Backups []Ingest `json:"-"`
```

They are parsed out of **every** Facebook create response and thrown away.
`ingestFor` returns `b.Ingest` and `b.ID` and drops the rest, so the backup
URLs never leave `internal/oauth`. The comment is honest that nothing consumes
them — this is a gap, not a lie — but it means the platform half already works.

## Why 10F is four features and this is one of them

Recorded because the decomposition is the decision, and one part of it was
verified rather than assumed.

| Piece | Needs | Status |
|---|---|---|
| **Backup ingest, end to end** | a second output plus failover | **this spec** |
| `stop_on_delete_stream` | one create-time boolean | trivial, deferred |
| 360 / spatial audio | create-time params | cheap, and unverifiable without VR hardware |
| Frame-accurate go-live | an in-process RTMP relay | blocked, see below |

**Frame-accurate go-live cannot be done with FFmpeg, and this was checked.**
It needs an AMF0 data message (`onGoLive`) emitted at a chosen frame. The FLV
muxer's flags only ever *suppress* metadata:

```
-flvflags   no_metadata | no_sequence_end | no_duration_filesize | add_keyframe_index
```

There is no option to emit a custom data message mid-stream. The near-miss that
will mislead the next person is `-rtmp_conn`, documented as *"Append arbitrary
AMF data to the Connect message"* — **Connect message only**, i.e. handshake
time, not a packet at frame N. Doing it properly means putting a Go RTMP relay
in the path of every Facebook broadcast, which is a large risk for a niche
capability and belongs in its own spec.

## The design

**A second supervised process.** A backup-enabled destination gets a second
FFmpeg, subscribed to the same relay hub, publishing to the backup URL.

One FFmpeg with two outputs was rejected. It is cheaper — one audio encode
rather than two — but both outputs share a process, so a crash takes down the
redundancy along with the thing it was protecting. The supervisor already
restarts processes independently, which is exactly the property this feature
needs, and using it is what makes the redundancy real rather than nominal.

**Off by default, per destination.** It doubles upload bandwidth for that
destination, and an operator on a thin or metered uplink must choose that
deliberately. No existing install changes behaviour on upgrade — the same rule
the compliance work followed.

## The decision this design turns on

**Turning redundancy on must not interrupt the thing it protects.**

`destSpec` hashes everything that requires a restart, and its own comment says
why that matters:

> a setting that is stored and never reaches the running process is the failure
> this repo keeps paying for

The obvious move is therefore to add the backup toggle to that hash. **That
would be wrong.** The hash governs the PRIMARY process, so toggling backup on
would cycle the primary — dropping the operator's live connection in order to
add a spare. An operator reaching for redundancy mid-broadcast is the person
least able to afford a reconnect.

`destSpec` already records the precedent, about resilience:

> The reasoning that first put it here was right about the danger and wrong
> about the remedy: the remedy was to deliver it, not to drop the operator's
> connection in order to deliver it.

So the backup toggle is **absent from `destSpec`**, exactly like resilience. The
backup process is started and stopped on its own, and the primary never notices.

## Storage

Two fields on `db.Destination`, beside `URL` and `StreamKey`:

```go
// BackupURL and BackupStreamKey are the platform's secondary ingest, stored
// when the broadcast was created. Empty when the platform offered none.
BackupURL       string `json:"backupUrl,omitempty"`
BackupStreamKey string `json:"backupStreamKey,omitempty"`
```

On `db.Destination` rather than in `FacebookSettings`, because a backup ingest
endpoint is not a Facebook idea — a custom RTMP destination with a redundant
endpoint means the same thing — and because the engine reads it, which should
not require the engine to know which platform a destination is.

The toggle goes on `DestResilience`, which is where "how this destination
survives trouble" already lives:

```go
// BackupIngest publishes a second, redundant feed to the platform's backup
// endpoint. Off by default: it doubles this destination's upload bandwidth.
BackupIngest bool `json:"backupIngest,omitempty"`
```

## What has to stop discarding the backups

`ingestFor` returns `(*oauth.Ingest, string, error)`. The backups are on the
`*oauth.Broadcast` it throws away. It widens to carry them, and
`handleRefreshKey` stores them.

`enable_backup_ingest` is sent at create when the toggle is on.

## An uncertainty this design must survive

**It is not established whether `enable_backup_ingest` is REQUIRED before
Facebook returns secondary URLs, or merely requests more of them.** Our own test
fixture returns `stream_secondary_urls` unconditionally, but a fixture is not
evidence about Meta.

So the design must not assume either: when the toggle is on and the create
response carries no backup URL, the destination stores none and **the operator
is told the platform offered no backup endpoint**. A toggle that silently does
nothing is the failure this project keeps finding, most recently a COPPA
declaration that reached the database and stopped.

## Failure behaviour

**The backup never fails the primary.** It is a second supervised process; if it
cannot start, or dies and cannot be restarted, the primary is untouched and the
destination stays live. Anything else would make a redundancy feature into a new
way to lose a broadcast.

**Both are reported, separately.** The card shows the backup's state beside the
primary's. A backup that has been dead for an hour while the card shows a
healthy destination is worse than no backup, because the operator believes they
have one.

**Give-up applies to each independently.** A backup endpoint that refuses
forever must reach `failed` and say so, rather than retrying invisibly.

## Testing

Every guard proven able to fail by a named one-line mutation.

| Case | Why it matters |
|---|---|
| A backup-enabled destination starts TWO processes, publishing to different URLs | The feature; asserted on the second process's target, not on a count |
| The second publishes to the BACKUP url, not the primary | Two feeds to one endpoint is not redundancy, and would look identical on a count |
| Toggling backup on does NOT restart the primary | The decision this design turns on |
| Toggling backup off stops the backup and leaves the primary running | The same claim in the other direction |
| A backup that dies does not stop the primary | Redundancy must not become a way to lose a broadcast |
| `enable_backup_ingest` is sent only when the toggle is on | Empty means leave alone, as everywhere else here |
| A create response with no backup URL stores none and warns | The uncertainty above; a toggle that silently does nothing is the failure mode this repo keeps hitting |
| A destination with the toggle off starts exactly one process | The rule must not widen |

## Out

- **Automatic failover between primary and backup.** Facebook decides which feed
  it takes; polyemesis publishes both. Choosing for it would need to know what
  Facebook is currently ingesting, which no endpoint reports.
- **Backup ingest for platforms other than Facebook.** The storage is general on
  purpose, but nothing else offers one today and inventing a UI for a field no
  platform populates is how unreachable settings appear.
- **The other three parts of 10F**, per the table above.
