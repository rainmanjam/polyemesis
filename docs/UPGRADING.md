# Upgrading

## The short version

polyemesis migrates its own database on startup. In the normal case, upgrading
is: stop, replace the binary or pull the image, start.

**Back up `<dataDir>` first, and check that the backup contains `secret.key`.**
Migrations run forward only — there is no downgrade path, and a backup is the
only way back. From 0.7.0 a backup without that one file is not a backup; see
[Upgrading to 0.7.0](#upgrading-to-070-sealed-stream-keys--breaking-to-roll-back)
before you start.

**`install.sh` writes a guarded `update.sh` that does all of this for you** — it
takes the backup, refuses to proceed if the archive is empty or missing
`secret.key`, and only then pulls. If you installed with `install.sh`, run
`<installDir>/update.sh` rather than the manual steps below. Operators who
installed before 0.7.0 do not have it: re-run `install.sh` to regenerate it, or
follow the manual procedure and do the `secret.key` check by hand.

```sh
# Binary
systemctl stop polyemesis
cp -a /var/lib/polyemesis /var/lib/polyemesis.bak-$(date +%F)
# ... replace the binary ...
systemctl start polyemesis

# Docker
docker compose down
docker volume inspect polyemesis-data >/dev/null || exit 1   # see the warning below
docker run --rm -v polyemesis-data:/data -v "$PWD:/backup" alpine \
  tar czf /backup/polyemesis-$(date +%F).tar.gz -C /data .
tar tzf polyemesis-$(date +%F).tar.gz | wc -l                # more than 1 = real
docker compose pull && docker compose up -d
```

> **Check the volume name before you trust the backup.** `docker run -v` creates
> a missing volume instead of failing, so backing up a name that does not exist
> exits 0 and writes an empty archive — and the upgrade that follows cannot be
> undone. Compose prefixes volumes with the project name unless the volume pins
> its own; installs made before that pin was added carry
> `polyemesis_polyemesis-data` rather than `polyemesis-data`. Run
> `docker volume ls` and use the name you actually see. To adopt the pinned name
> permanently, copy the old volume across once:
>
> ```sh
> docker volume create polyemesis-data
> docker run --rm -v polyemesis_polyemesis-data:/from -v polyemesis-data:/to \
>   alpine sh -c 'cp -a /from/. /to/'
> ```

Watch the first minute of the log. Migrations report what they did; a failure
there is much easier to deal with before you start streaming on it.

## Before you upgrade

- **Read the [CHANGELOG](../CHANGELOG.md).**
- **Stop cleanly.** Recordings are finalised during shutdown, which takes up to
  about 30 seconds. Killing the process truncates whatever was being written.
  `stop_grace_period: 30s` is already set in the compose file — do not lower it.
- **Check the FFmpeg floor.** It is 6.0 today. If a future release raises it,
  the server refuses to start rather than failing later in a confusing way.

## What migrations do

They run automatically and forward only. Two properties they hold to:

- **A migration that reads does not write.** This sounds obvious; it was
  violated once, when a migration called a settings getter that *seeded* a
  settings row as a side effect. That is why the migration path now uses a
  read-only accessor.
- **New columns are added with a safe default**, so an older database keeps
  working without intervention.

There is **no downgrade**. An older binary against a newer database is
unsupported and may fail in ways that are not obvious. Restore the backup
instead.

## Version-specific notes

### Upgrading to 0.7.0: sealed stream keys — **breaking to roll back**

0.7.0 encrypts every destination stream key at rest. The key that opens them is
`secret.key`, in `<dataDir>`, and it is generated on first start.

**A restore without `secret.key` looks completely successful and is not.** The
server starts, the database opens, every destination is listed — and each one
comes back **disabled**, because a key that will not decrypt disables its
destination rather than failing open with a wrong key. Nothing is wrong until
you go live, which is the worst moment to find out.

It is easy to get wrong, because `secret.key` is generated silently when it is
absent. Restore the database without it and the server mints a fresh one, so
there is no error to notice — just a new key that cannot open the old rows.

```sh
# Check your backup before you rely on it.
tar tzf backup-<stamp>.tar.gz | grep secret.key
```

**Rolling back to 0.6.0 blanks every stream key.** The sealing migration clears
the plaintext column, and 0.6.0 has no concept of the encrypted one — so the
older binary reads every destination as having an empty key while still marked
enabled. There is no schema version for it to refuse on. Once you have started
0.7.0 against a database, treat the upgrade as one-way and go back via the
backup rather than by reinstalling the old version.

If you already have destinations showing as disabled after a restore, the
`keyUnreadable` field on `GET /api/v1/destinations` says which, and re-entering
the key on each one fixes it.

Also in 0.7.0: **a stream key containing a control character is now refused
when you save it** rather than being silently truncated. A destination carrying
such a key — most often from a terminal paste that appended an escape sequence
— must have its key re-entered before it can be saved again.

#### If you already upgraded to 0.7.0: one-off remediation required

**0.7.0's sealing migration blanked the `stream_key` column but left the
plaintext legible in the database file.** SQLite unlinks the bytes of a
shortened row without zeroing them, and it writes the new rows into the
write-ahead log rather than over the old ones, so both the freed pages of
`polyemesis.db` and the frames of `polyemesis.db-wal` kept readable copies of
every key the migration replaced. `SELECT stream_key FROM destinations` returns
empty; `grep` on the same file returns the key. Measured against a 60-destination
install: 60 plaintext copies still in the raw bytes after a clean-shutdown
upgrade, 122 in the `-wal` after an upgrade over a server that had been killed.

This defeats the one thing sealing at rest is for. A leaked database file was
still a leaked set of live streaming credentials.

**Fixed forward for new upgrades**, which now open the database with
`secure_delete` on and truncate the write-ahead log once the migration has
finished. Since the pre-tag review the checkpoint also runs on a boot that finds
nothing left to seal, which covers the case that was previously permanent: an
upgrade that sealed every row, committed, and then died — power loss, an OOM
kill, a restart landing in the wrong second — used to come back, find no work to
do, return before the checkpoint, and never truncate the log again.

So an install that upgrades now gets its `-wal` cleared on the next start,
whether or not the migration itself was interrupted. What upgrading cannot do is
undo the freed pages: `secure_delete` only governs writes made after it is set,
so **an install that already ran the 0.7.0 migration still needs `VACUUM` once by
hand.** Run the pair, which remains the safe order:

```sh
systemctl stop polyemesis                      # or: docker compose down
sqlite3 /var/lib/polyemesis/polyemesis.db "VACUUM; PRAGMA wal_checkpoint(TRUNCATE);"
systemctl start polyemesis                     # or: docker compose up -d
```

Both statements are still the right thing to run by hand, and neither is
sufficient alone — the newer automatic checkpoint clears the log, not the pages.
`VACUUM` rebuilds
the file without the freed pages but writes the result into the `-wal`, where
the old content stays until it is checkpointed; `wal_checkpoint(TRUNCATE)` on
its own copies the current pages back and empties the log but leaves whatever
was already stranded in freed pages. Run them in that order, in one session,
with the server stopped.

To confirm it worked, check for a key you know:

```sh
grep -c 'live_' /var/lib/polyemesis/polyemesis.db     # expect 0
```

**Every backup taken between upgrading to 0.7.0 and running the scrub still
contains the plaintext**, and so does every backup taken before the upgrade —
the whole point of those is that they predate sealing. The scrub cannot reach
them. Treat those archives as carrying live credentials: if they left the host,
or sit anywhere with a broader audience than the data directory, **rotate the
stream keys** on the platforms rather than trusting the archive. Rotating is the
only remedy for a copy that has already been made.

### Upgrading to multi-source

The existing configuration becomes the **default source**, automatically. All
your destinations, renditions and recordings attach to it. Nothing changes about
how it behaves and no action is required.

Two things worth knowing afterwards:

- Every source gets a **publish token**. The default source's token is minted
  during migration.
- Editing the ingest in **Settings** still works — it writes through to the
  default source. (There was a window where it silently did not; if you are
  coming from a build in that window, check your ingest settings after
  upgrading.)

### Upgrading to one-port ingest

**There is no switch — one port is the only mode.** Every source arrives on the
shared SRT listener (default `6000/udp`) and is told apart by its publish token,
which is the SRT `streamid`; RTMP works the same way, addressed by the stream
key.

An install upgraded from a pre-one-port build keeps its old RTMP stream key
working through a grandfather clause — the Sources page shows it as
`legacyRtmpKey` — but SRT publishers must present the token. Copy the new URL
for each source from the **Sources** page and update your encoders **before**
upgrading, not after.

### Session tokens gained an epoch

A `users.token_epoch` column is added, defaulting to 0, and existing sessions
carry that same value — so nobody is signed out by the upgrade itself. What
changes afterwards is that **changing the password now ends every existing
session**, immediately and everywhere, rather than leaving old cookies valid
until they expire. That is the point of it; it is only surprising once.

### Kick chat webhooks now require signature verification

Kick webhook deliveries are verified against Kick's published RSA key, which the
server fetches from `api.kick.com`. A request that cannot be verified is
refused — including when the key itself could not be retrieved.

If you run Kick chat on a host with restricted outbound access, allow
`https://api.kick.com/public/v1/public-key` before upgrading. Previously an
unverified delivery was accepted; an unauthenticated write path is not something
to fail open on, so it now fails closed instead.

### `failover.playlist.filePath` → `failover.playlist.items` — **breaking**

The playlist is now an ordered list of **stored uploads** rather than one file
path, and `filePath` is gone. This is the only change on this page that can
break an existing automation, so read it before upgrading if you set the
playlist through the API.

What changes:

- **Any payload that still sets `failover.playlist.filePath` is rejected with
  400.** The settings decoder refuses unknown fields, so there is no silent
  no-op — a script that has not been updated fails loudly on its next
  `PUT /api/v1/settings`. Send `failover.playlist.items` instead:
  `{"items": [{"upload": "filler.ts"}]}`.
- An item names an **upload**, not a path. Upload the file through the media
  page (or `POST /api/v1/media`) and use the stored name it returns. A name
  containing a `/`, a `\` or a `..` is refused, and so is a name that does not
  match an upload that actually exists.
- **The old value is migrated for you only when it can be**: at startup, a
  legacy `filePath` that is a bare filename already sitting in the uploads
  directory becomes the single item of the new list. Anything else — a
  `media/loop.mp4`-style path, or a name with no matching upload — is **left
  unmigrated with a WARN in the log**, because `filePath` was resolved relative
  to the data directory and an item is resolved inside `uploads/`, so copying
  the string across would have pointed at a different file. If you see that
  warning, upload the file and re-select it in the playlist.
- **Items must be normalised before a playlist goes on air.** Saving a playlist
  queues one `playlist.normalise` job per distinct upload, which transcodes it
  to the single profile every item has to share. The playlist is unavailable —
  and the slate stays on air — until every item's job has finished. Like all
  background work it yields to a live stream, so an item added while you are
  broadcasting normalises when the stream ends. Watch it on the Jobs page.
- Today the list may hold several items but only the **first** one plays.
  Sequencing is a later change; the list is stored, validated and normalised in
  full now so that nothing has to be re-entered when it arrives.

### `tls.enabled` → `tls.mode`

The old boolean still works, so an existing config keeps its behaviour:

| Old | Behaves as |
|---|---|
| `enabled: true` with `certFile`/`keyFile` | `mode: manual` |
| `enabled: false`, or no `tls` block | `mode: off` |

An explicit `tls.mode` always wins, so you can migrate without deleting the old
key. New installs starting from `config.example.yaml` get `mode: auto`.

### FFmpeg 6.x → 8.x

Supported and recommended; the project is developed against 8.1.2. No
configuration changes.

One thing to know if you are comparing behaviour across versions: `-itsoffset`
does **not** produce a per-stream delay — measured against 8.1.2 it moved audio
and video in lockstep and delivered 0 ms for every requested value. polyemesis
uses the `setts` bitstream filter instead, which shifts video alone and
preserves `-c:v copy`. If you had worked around the delay behaviour externally,
you can stop.

## Rolling back

1. Stop the server.
2. Restore `<dataDir>` from the backup.
3. Put the old binary or image back.
4. Start.

Restoring the data directory is not optional. The database will have been
migrated, and the older binary will not understand it.

**Restore the whole directory, including `secret.key`.** Restoring only
`polyemesis.db` is the mistake this section exists to prevent: from 0.7.0 the
database alone is not enough to publish, and the failure is silent until you go
live. See [Upgrading to 0.7.0](#upgrading-to-070-sealed-stream-keys--breaking-to-roll-back).

## Verifying an upgrade

```sh
polyemesis -version
curl -s localhost:8080/api/v1/health
```

**Check no destination came back disabled.** From 0.7.0 this is the first thing
to look at after an upgrade or a restore, because it is the one failure that
looks like success:

```sh
curl -s -H "Authorization: Bearer $TOKEN" localhost:8080/api/v1/destinations \
  | grep -o keyUnreadable | wc -l
```

`$TOKEN` is an API token from **Settings → API tokens** (see
[API.md](API.md)). Unauthenticated this endpoint answers `401`, and the count
would be a meaningless zero — which reads as an all-clear for the very failure
this check exists to find.

Anything above zero means those destinations could not decrypt their stream key
— almost always a restore that omitted `secret.key`. Re-enter the key on each,
or restore the file and restart.

Then, in the UI: the ingest goes live, each destination reports running, and the
**Meters** page shows loudness after routing. That last one is the real check —
it is measured from what the platforms actually receive, so if it looks right,
the path is right.

If you keep recordings, confirm a new segment appears and plays.
