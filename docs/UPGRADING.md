# Upgrading

## The short version

polyemesis migrates its own database on startup. In the normal case, upgrading
is: stop, replace the binary or pull the image (rebuild it, from a clone), start.

**Back up `<dataDir>` first, and check that the backup contains `secret.key`.**
Migrations run forward only — there is no downgrade path, and a backup is the
only way back. **From 0.7.0 onward a backup without that one file is not a
backup**, because the stream keys in the database are sealed with it and nothing
else can open them. 0.7.0 was released on 2026-08-28 and every release since
carries the same rule, so this applies to you now; see
[Upgrading to 0.7.0](#upgrading-to-070-sealed-stream-keys--breaking-to-roll-back)
before you start, including its **mandatory** remediation if you have already
upgraded.

**`install.sh` writes a guarded `update.sh` that does most of this for you** —
it stops the service, takes the backup, refuses to proceed if the copy is
missing `secret.key` or does not open, and then:

- **docker mode:** pulls the new image and brings the container back up. That
  is the whole upgrade.
- **binary mode:** fetch the release asset first
  (`polyemesis-<tag>-linux-<arch>`) and pass it with its tag:

  ```sh
  sudo /opt/polyemesis/update.sh --binary ./polyemesis-v0.10.0-linux-amd64 --version v0.10.0
  ```

  Before stopping anything it refuses a file whose sha256 is not the one the
  release's `SHA256SUMS` publishes for this host's architecture, one that will
  not run here, and one whose `-version` is not the tag. It then takes the
  backup, installs the file, starts the service and checks it stayed up; if it
  did not, it names `rollback.sh`. `--sums FILE` checks against a local
  `SHA256SUMS` on a host without GitHub access. Without `--binary`, it stops
  after the backup **with the service still stopped** and prints the two
  commands that finish it by hand.

If you installed with `install.sh`, run `sudo <installDir>/update.sh` rather
than the manual steps below — including for a binary you copied in by hand
afterwards. Operators who installed before 0.7.0 do not have it: re-run
`install.sh` to regenerate it, or follow the manual procedure.

> This page had said 0.7.0 was *"not yet released"* here while saying seventy
> lines further down that it was tagged and that its remediation was mandatory.
> An operator who read only the summary above concluded the `secret.key`
> requirement was not theirs yet. Corrected 2026-09-03; the version-specific
> notes below have been the authority throughout.

**The manual procedure, binary install.** The same guards `update.sh` applies,
in a form you can paste: it runs in its own shell, so a refusal stops the
procedure without closing your terminal, and nothing after a failed check runs.
Put the name of the binary you downloaded on the first line; the paths inside
are the defaults `install.sh` and the shipped unit use.

```sh
sudo sh -eu -s -- "$PWD/polyemesis-v0.10.0-linux-amd64" <<'EOF'
NEW="$1"                               # the binary you downloaded, as an absolute path
DATA=/var/lib/polyemesis
BIN=/usr/local/bin/polyemesis
test -f "$NEW" || { echo "no file at $NEW" >&2; exit 1; }
chmod 0755 "$NEW"                      # a curl -fLO or browser download arrives 0644
dest="$DATA.bak-$(date +%F-%H%M)"      # minutes, so a second upgrade today gets its own copy
if [ -e "$dest" ]; then                # cp -a would nest the copy INSIDE the old one
  echo "refusing: $dest already exists" >&2; exit 1
fi
systemctl stop polyemesis              # a live WAL database does not copy consistently
cp -a "$DATA" "$dest"
cp -a "$BIN" "$dest/polyemesis.previous"   # the way back, kept beside the data
if [ ! -f "$dest/secret.key" ]; then
  echo "refusing: $dest has no secret.key. Service is stopped: systemctl start polyemesis" >&2
  exit 1
fi
V="$BIN"                               # the installed version checks the copy...
if ! "$BIN" -help 2>&1 | grep -q -- -verify-backup; then
  V="$NEW"                             # ...unless it predates 0.9.0, which has no -verify-backup
  echo "installed binary predates -verify-backup (0.9.0); checking the copy with $NEW"
fi
if ! "$V" -verify-backup "$dest"; then
  echo "refusing: $dest does not verify. Service is stopped: systemctl start polyemesis" >&2
  exit 1
fi
install -m 0755 "$NEW" "$BIN"
systemctl start polyemesis
echo "upgraded; backup and previous binary in $dest"
EOF
```

`-verify-backup` runs before the binary is replaced, deliberately: if the check
fails you still have a working binary and an intact data directory. It uses the
installed binary when that has the flag, and the new one when it does not:
`-verify-backup` first shipped in 0.9.0, so an 0.8.x or older install would
otherwise stop on `flag provided but not defined` and report a good backup as
bad. Checking with the newer binary is sound because the check never migrates
and never reads the schema version; it opens the copy read-only, runs SQLite's
`integrity_check`, and looks for polyemesis's tables, which every release has.

To go back afterwards, restore the **state** from the backup and leave the
media alone. The data directory also holds `recordings/`, `uploads/`, `hls/`,
`playout/`, `models/`, `fonts/` and `logs/`; deleting it and copying the backup
over it would lose everything written there since the upgrade. The restored
database does not list those newer files, but they stay on disk.

```sh
sudo sh -eu -s -- /var/lib/polyemesis.bak-<stamp> <<'EOF'
BAK="${1%/}"
DATA=/var/lib/polyemesis
for f in polyemesis.db secret.key polyemesis.previous; do
  test -f "$BAK/$f" || { echo "refusing: $BAK has no $f" >&2; exit 1; }
done
systemctl stop polyemesis
# the newer database's log must not be replayed into the older file
rm -f "$DATA/polyemesis.db-wal" "$DATA/polyemesis.db-shm"
for src in "$BAK"/* "$BAK"/.[!.]*; do
  [ -e "$src" ] || continue
  name="${src##*/}"
  case "$name" in
    polyemesis.previous|recordings|uploads|hls|playout|models|fonts|logs) continue ;;
  esac
  rm -rf "${DATA:?}/$name"             # polyemesis.db, secret.key, tls/ and other state
  cp -a "$src" "$DATA/$name"
done
install -m 0755 "$BAK/polyemesis.previous" /usr/local/bin/polyemesis
systemctl start polyemesis
echo "rolled back to $BAK; media directories kept as they were"
EOF
```

Always restore `secret.key` with the database; see
[Rolling back](#rolling-back).

**The manual procedure, Docker.** Two shapes, and they upgrade differently:

- **`install.sh --mode docker`** runs a published image, so the new version
  arrives with `docker compose pull`. Use its `update.sh`.
- **`docker compose` from a clone of this repository** *builds* its image — the
  service has `build:`, not `image:` — so `docker compose pull` fetches nothing
  and `up -d` restarts the version you already had, with no error. The new
  version arrives with `git pull`, and reaches the container only with
  `up -d --build`.

  Such a build reports its version as `compose`, so the in-app update check
  says the versions cannot be compared and `GET /upgrade/plan` tells it to
  rebuild rather than pull. For an update check that can compare versions,
  switch the service to `image: rainmanjam/polyemesis:<version>` -- image tags
  have no leading `v` (`0.10.0`, not `v0.10.0`).

For the clone, from its directory:

```sh
sh -eu <<'EOF'
backups="$HOME/polyemesis-backups"     # OUTSIDE the clone -- see below
vol=polyemesis-data                    # check with `docker volume ls` -- see the warning below
docker volume inspect "$vol" >/dev/null
mkdir -p "$backups"
archive="$backups/polyemesis-$(date +%F-%H%M).tar.gz"
[ ! -e "$archive" ] || { echo "refusing: $archive already exists" >&2; exit 1; }
docker compose down                    # stopped, so the database is not copied mid-write
docker run --rm -v "$vol:/data:ro" -v "$backups:/backup" alpine \
  tar czf "/backup/${archive##*/}" -C /data .
tar tzf "$archive" | grep -qx './secret.key' || {
  echo "refusing: $archive has no secret.key. Bring it back with: docker compose up -d" >&2
  exit 1; }
git pull --ff-only
docker compose up -d --build
echo "upgraded; backup at $archive"
EOF
```

**Keep the archive out of the clone.** The image build is `COPY . .` from the
clone, and `.dockerignore` does not know about backup tarballs — an archive
written into the working directory (as this page used to say, with
`-v "$PWD:/backup"`) is copied into the build stage on the next `--build`,
`secret.key` and all.

The paste-safe `sh -eu <<'EOF'` form matters here too: the old snippet's
`docker volume inspect … || exit 1` closed the terminal it was pasted into when
the volume was missing — the moment you most need that terminal.

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
- **Stop cleanly.** Recordings are finalised during shutdown, which can take
  up to 35 seconds. Killing the process truncates whatever was being written.
  The compose files set `stop_grace_period: 45s` and the systemd unit sets
  `TimeoutStopSec=45`. Do not lower either. A compose file written before the
  release after 0.10.0 says `30s`: raise it to `45s`.
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

> **Everything in this section is released and applies to you.** Read every
> note between the version you run and the one you are moving to, newest
> first. The newest heading in [CHANGELOG.md](../CHANGELOG.md) is the authority
> on what a tag contains, and `.github/workflows/release.yml`'s changelog-gate
> refuses to let a tag publish unless that heading agrees with it; every
> released version has a note here, even when the note is "nothing to do". If
> you are coming from 0.6.0 or earlier, the 0.7.0 note below — including its
> **mandatory** remediation — is work you still have to do.

### Upgrading past 0.10.0 (unreleased, on `main`): sources with no ingest mode become SRT

> Not yet in a tag — this note is here ahead of the release that carries it,
> for anyone running `main`. It becomes that release's note when it is cut.

**What changed.** The shared SRT port (6000/udp) now admits a publisher only
into a source whose ingest mode is **SRT**. On 0.10.0 it admitted any source,
so an SRT encoder could publish into an RTMP source (two muxers into one
stream) or a pull source. That is fixed, and RTMP and pull sources now refuse
SRT.

**Who it would have hit.** Every source added from the **Sources** page on
0.10.0 or earlier. The create form sends only a name, so those sources were
stored with **no ingest mode** — and SRT was the only ingest that could ever
reach them, so that is what their encoders were using. Without the migration
below, the first boot after upgrading would refuse those encoders with
`srt publish refused: no SRT pipeline for source`, and nothing on screen would
say why. The `Main` source an install gets from its original single-ingest
configuration normally carries that configuration's mode and is left alone.

**What the upgrade does, automatically, on its first boot.** Every source whose
ingest mode is unset is set to `srt`. Nothing else in the source changes — the
token, latency and passphrase are kept, so the encoder needs no change. The
boot logs which sources it changed, once:

```
level=WARN msg="sources with no ingest mode were set to SRT" sources=[...]
```

The migration only ever finds sources with no mode; a source already set to
SRT, RTMP or pull is not touched, and later boots find nothing to do. There is
no schema change (the schema version stays `1`), so a rollback to 0.10.0 reads
these sources as the ordinary SRT sources they now are.

**New sources.** A source created from its name alone — the Sources page's
**Add source** — is now an SRT source with an SRT publish URL from the start.
The API refuses `"ingest": {"mode": ""}` on create, and refuses clearing the
mode of a source that has one, with `choose an ingest mode: srt, rtmp or pull`.

**What you might need to do.** Only if one of the sources named in that log
line was meant for RTMP or pull: change its **Ingest** on the Sources page.
It could not have been receiving RTMP or pulling while it had no mode, so
nothing that worked before stops working.

### Upgrading past 0.10.0 (unreleased, on `main`): config.yaml refuses a key it does not know

> Not yet in a tag — this note is here ahead of the release that carries it.

**What changed.** A key `config.yaml` does not define — at the top level or
inside `ffmpeg:` and `transcription:`, as `tls:` already did — now stops the
server at startup with the key and its line. Keys are case-sensitive. Until now
such a key was dropped silently and its setting stayed at the default: a
misspelled `trustProxyHeaders` left session cookies without `Secure`, a
misspelled `dataDir` put the database in `./data`. The retired `enhancedRtmp`
key is still accepted and ignored.

Also refused: a `tls.hostname` with no `tls.mode`. That meant `off` — plain
HTTP — while it looked like HTTPS was configured. Write the mode you meant, or
`mode: "off"` if something in front terminates TLS.

**What you might need to do.** Before upgrading, check `config.yaml` against
[CONFIGURATION.md](CONFIGURATION.md). If the new binary refuses to start,
`journalctl -u polyemesis` names the key to fix. Files written by
`install.sh` use only known keys and always write a mode.

The same applies to `--log`. A value other than `debug`, `info`, `warn` or
`error` now stops the server instead of meaning `info`. If your unit or
container command passes `--log warning` or similar, change it to `warn`
before upgrading.

### Upgrading past 0.10.0 (unreleased, on `main`): the unit `install.sh` writes no longer passes `--addr`

> Not yet in a tag — this note is here ahead of the release that carries it.

**What changed.** The unit `install.sh` generates used to set the web port
twice. It passed `--addr :<port>` in `ExecStart` and also wrote
`addr: ":<port>"` into `config.yaml`. The flag wins, so editing `addr:` did
nothing. The generated unit now leaves the port to `config.yaml`. An existing
unit keeps its `--addr` until `install.sh` is re-run. The startup warnings now
say when the address came from `--addr`.

**What you might need to do.** Nothing, unless you moved the port by editing
`--addr` in the unit (for example to `:443`). Re-running `install.sh` rewrites
the unit and `config.yaml` with the port you answer, so answer with the port you
use. After that, change the port only in `config.yaml`.

### Upgrading to 0.10.0

**No schema change.** `internal/db` is identical between `v0.9.0` and
`v0.10.0` apart from a test, and the schema version stamped into the database
is still `1`. So a 0.9.0 binary opens a database 0.10.0 has run against, and a
rollback from 0.10.0 to 0.9.0 is the one step on this page where reinstalling
the old binary is enough — though restoring the backup you took is still the
path this page recommends, because it is the one you can check.

Nothing to do beyond the short version above. Read the
[CHANGELOG](../CHANGELOG.md) for what changed in behaviour.

### Upgrading to 0.9.0

Three changes an existing install can notice, none of which touch the data:

- **The default listen address is loopback.** With no `config.yaml` and no
  `--addr`, 0.9.0 binds `127.0.0.1:8080` instead of every interface. The
  shipped systemd unit, the unit `install.sh` writes, every Dockerfile and
  `config.example.yaml` all pass or set an address explicitly and are
  unaffected. A bare binary started with no config, or a `config.yaml` with no
  `addr` key, is now reachable only from the box itself — set `addr` or pass
  `--addr` to widen it. See
  [TLS.md → Binding, and the SSH tunnel](TLS.md#binding-and-the-ssh-tunnel).
- **An explicit `--config` that does not exist refuses to start.** It used to
  boot a second, empty install beside the real one — a new `secret.key`, an
  empty database, and an open `POST /setup` — while looking healthy (#644).
  A unit or launchd job that names a config file must now have one.
- **A container with sources and no running engine fails its `HEALTHCHECK`**
  where it used to pass.

And one that is only good news: **a restored data directory without
`secret.key` is no longer silent.** Boot still mints a fresh key, but it now
logs an `ERROR` naming how many destinations cannot be read, and which.

The generated `update.sh` also changed in 0.9.0 (it stops the service before
copying, verifies the copy with `-verify-backup`, and keeps
`polyemesis.previous`). It is written at install time, so re-run `install.sh`
to get it on an older install.

The schema version stamp is `1` in both 0.8.0 and 0.9.0, so a 0.8.0 binary
does not refuse a database 0.9.0 has opened. That is not the same as the
rollback being safe: 0.9.0 added migrations an 0.8.0 binary knows nothing
about. Restore the backup.

### Upgrading to 0.7.0: sealed stream keys — **breaking to roll back**

0.7.0 encrypts every destination stream key at rest. The key that opens them is
`secret.key`, in `<dataDir>`, and it is generated on first start.

**A restore without `secret.key` looks completely successful and is not.** The
server starts, the database opens, every destination is listed — and each one
comes back **disabled**, because a key that will not decrypt disables its
destination rather than failing open with a wrong key. Nothing is wrong until
you go live, which is the worst moment to find out.

It is easy to get wrong, because `secret.key` is generated when it is absent.
Restore the database without it and the server mints a fresh one — a new key
that cannot open the old rows. Through 0.8.x that happened with no error at
all. From 0.9.0 boot logs an `ERROR` naming the destinations that cannot be
read, but the server still starts and serves, so it is a line in a log, not a
refusal: look for it.

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
- **Every entry plays**, in the order the list gives them, and the list repeats
  from the top when it reaches the end. The lap boundary is not a clean cut —
  see [SCHEDULED-BROADCAST.md](SCHEDULED-BROADCAST.md) for the measured seam.

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

Set `POLYEMESIS_URL` to where this install answers. It is not `localhost:8080`
for most installs: `install.sh` defaults to self-signed TLS on 443 in both
modes. The table in
[INSTALL.md → Verifying the install](INSTALL.md#verifying-the-install) lists
each install shape; in short, `https://localhost` for an `install.sh` install
that took the defaults, and `http://localhost:8080` for `docker compose` from a
clone.

```sh
export POLYEMESIS_URL=https://localhost      # see above
polyemesis -version
curl -fsSk "${POLYEMESIS_URL:?set it first}/api/v1/health"
```

**Check no destination came back disabled.** From 0.7.0 this is the first thing
to look at after an upgrade or a restore, because it is the one failure that
looks like success:

```sh
curl -fsSk -H "Authorization: Bearer $TOKEN" "${POLYEMESIS_URL:?set it first}/api/v1/destinations" \
  | jq '[.[] | select(.destination.keyUnreadable) | .destination.name]'
```

`$TOKEN` is an API token from **Settings → API tokens** (see
[API.md](API.md)); a `read` token is enough.

**`[]` is the all-clear, and it is the only one.** A list of names is the
destinations that could not decrypt their stream key — almost always a restore
that omitted `secret.key`. Re-enter the key on each, or restore the file and
restart. **No output at all means the request failed**, and `curl` has said why
on the line above: the wrong address, a certificate it would not accept, or a
`401` for a missing token.

This used to be `curl -s localhost:8080/… | grep -o keyUnreadable | wc -l`, and
that form is worth recognising if you have it in a runbook. On an install
serving HTTPS on 443 the request fails, `-s` hides the failure, and `wc -l`
prints `0` — the all-clear, for the one failure this check exists to find. The
exploratory test that caught it restored a data directory without `secret.key`
and watched the old command report zero.

Then, in the UI: the ingest goes live, each destination reports running, and the
**Meters** page shows loudness after routing. That last one is the real check —
it is measured from what the platforms actually receive, so if it looks right,
the path is right.

If you keep recordings, confirm a new segment appears and plays.
