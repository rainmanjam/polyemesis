# Upgrading

## The short version

polyemesis migrates its own database on startup. In the normal case, upgrading
is: stop, replace the binary or pull the image, start.

**Back up `<dataDir>` first.** Migrations run forward only — there is no
downgrade path, and a backup is the only way back.

```sh
# Binary
systemctl stop polyemesis
cp -a /var/lib/polyemesis /var/lib/polyemesis.bak-$(date +%F)
# ... replace the binary ...
systemctl start polyemesis

# Docker
docker compose down
docker run --rm -v polyemesis-data:/data -v "$PWD:/backup" alpine \
  tar czf /backup/polyemesis-$(date +%F).tar.gz -C /data .
docker compose pull && docker compose up -d
```

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

**Off by default.** Nothing changes until you enable it. With it off, each
source keeps its own port exactly as before.

When you turn it on, publish URLs change: the token becomes the SRT streamid.
The Sources page shows the new URL for each source. Update your encoders before
switching, not after.

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

## Verifying an upgrade

```sh
polyemesis -version
curl -s localhost:8080/api/v1/health
```

Then, in the UI: the ingest goes live, each destination reports running, and the
**Meters** page shows loudness after routing. That last one is the real check —
it is measured from what the platforms actually receive, so if it looks right,
the path is right.

If you keep recordings, confirm a new segment appears and plays.
