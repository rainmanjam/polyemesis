# Offload storage (S3-compatible)

polyemesis can copy finished recordings to an S3-compatible bucket: AWS S3,
Cloudflare R2, Backblaze B2 (its S3 API), MinIO or Wasabi. Add a target on the
**Recordings** page, under **Offload storage**, or with
`POST /api/v1/storage/targets` (see [API.md](API.md#storage-targets-offload)).

## What a target holds

| Field | Meaning |
|---|---|
| `endpoint` | The service URL, scheme included: `https://s3.us-east-1.amazonaws.com`, `https://<account>.r2.cloudflarestorage.com`, `https://s3.us-west-004.backblazeb2.com`, `http://minio.lan:9000`, `https://s3.us-east-1.wasabisys.com` |
| `bucket`, `region` | The bucket must already exist. R2 uses region `auto` |
| `prefixTemplate` | The key prefix. Tokens: `{kind}` (recordings, stems, transcripts, clips), `{source}` (`s<id>` or `unattributed`), `{yyyy}` `{mm}` `{dd}` (UTC, from the recording's start). Blank means `polyemesis/{kind}/{yyyy}/{mm}/{dd}`. The file's own path follows the prefix, so two files never share a key |
| `pathStyle` | `<endpoint>/<bucket>/<key>` instead of `<bucket>.<host>/<key>`. MinIO and most self-hosted targets need it; a bucket name with a dot uses it automatically over HTTPS |
| `accessKeyId`, `secretAccessKey` | The credential. The secret is write-only (see below) |
| `mode` | `mirror` or `move` (see below) |
| `keepLocalHours` | Move mode only: how many recent hours stay on local disk |
| `offloadStems`, `offloadTranscripts`, `offloadClips` | Also copy those. They are always mirrored, never moved |
| `verifyDownload` | After each upload, read the object back and hash it. Slower, and most providers charge egress for it |
| `allowPrivateTarget` | Permit a loopback, LAN or other private address (a MinIO on your network). Off by default, like webhooks |
| `maxAttempts` | Consecutive failures before an item is `stuck` (default 8) |

Use **Test** before saving: it writes a small object under the prefix, reads it
back, deletes it, and says which of the three steps failed.

## Only finished files, always verified

A recording segment is sent only once it is finished: it has been measured,
it is not the newest segment or one that started within the last segment
length (the same rule retention uses to protect the file being written), and
it has not been modified for two minutes.

Every upload carries the file's real SHA-256 in the signed request and its MD5
in `Content-MD5`; the service recomputes both and refuses a body that differs.
Afterwards polyemesis checks the stored object's size, its ETag (the MD5 for a
single upload, `md5(part MD5s)-N` for a multipart one) and the SHA-256 stored
with it as metadata, and with `verifyDownload` the bytes themselves. Only then
is the item `verified`.

Files above 64 MiB go up in 16 MiB parts. Each part is retried on its own, and
progress is saved after every part, so a retry or a restart sends only what
never arrived.

## Mirror and move

**Mirror** copies and changes nothing else: local retention deletes local files
exactly as it did before, and the copy in the bucket stays.

**Move** additionally deletes a local recording once **every enabled target**
holds a verified copy and the recording is older than `keepLocalHours`. Just
before the delete, each target is asked again for the object's size and
digest; a missing or different object keeps the file. Stems, transcripts and
clips are never deleted by move mode.

A moved recording stays in the Library, marked **In storage**, and retention no
longer counts it against the local size or age limits. Its life in the bucket
is the bucket's own lifecycle rule.

When local retention (age or size) reaches a recording that every enabled
target holds a verified copy of, with a move-mode target among them, it moves
the recording instead of deleting it: the file goes, the Library row stays and
it can be fetched back. A recording fetched back in the last 24 hours, or one
a download, an export or a queued job is reading, is left on disk by both move
mode and retention. Every enabled target must be usable for anything to be
moved: a target whose secret will not open blocks it.

Changing a target's endpoint, bucket, region, access key or addressing is
refused while it holds moved recordings (fetch them back first); otherwise
every file is uploaded again to the new address. Deleting a recording deletes
its offload state; its objects stay in the bucket for the lifecycle rule.

This release raises the database schema version to 2, so an older polyemesis
refuses to open the database rather than scanning it and dropping the rows of
moved recordings. To go back to an older release, restore the backup taken
before the upgrade.

## Fetch back, and how the clipper waits

The clipper, the post-production jobs and the download route need the file on
disk. For a moved recording they answer `409` with `"code": "offloaded"`. The
clip editor then asks for the fetch (`POST /api/v1/library/recordings/{id}/fetch`,
which fetches every moved segment of the session) and shows *being fetched
back*; it checks every two seconds and opens the editor when the file is local.

A fetch makes up to three attempts, each resuming from the bytes already
received (an HTTP Range request). The whole file must have the uploaded size
and SHA-256 before it is renamed into place; until then it is a hidden
temporary file, so nothing half-written ever appears under the recording's
name. After the third failure the editor shows the error and **Try again**. A
fetched recording stays local for 24 hours before move mode may remove it again.

## Failures, stuck items and Retry now

A failed upload is retried with backoff (30 s, doubling, at most an hour). After
`maxAttempts` failures in a row, or at once on an error that waiting cannot cure
(a refused key, a missing bucket, a key the file cannot have), the item is
`stuck`: it is not retried on its own, and `offload.stuck` is raised as an alert,
as a webhook ([HOOKS.md](HOOKS.md)) and on the events stream
([EVENTS.md](EVENTS.md)). **Retry now**, on the target or on the Library item,
puts it back with its attempts reset. A stuck multipart upload is cancelled on
the service.

## Restarts

An upload interrupted by a shutdown or a crash comes back as failed and due at
once, without spending an attempt; a multipart upload resumes from its saved
parts. A fetch interrupted the same way leaves only its temporary file, which
the next start removes.

## Credentials

The secret access key is sealed with the install's secret key the moment it is
saved and is never returned: responses say `hasSecret`, and a read-scoped token
also gets `accessKeyId` blanked. The secret is not logged and never appears in
an error. A saved secret is used only for the endpoint, bucket and
private-address setting it was saved with; change any of them and the secret
has to be entered again.

## Recommended bucket settings

- **A lifecycle rule that aborts incomplete multipart uploads** after a few
  days. polyemesis aborts the uploads it gives up on, the uploads of a target
  you delete, and orphans it finds at startup, but a process that dies between
  starting an upload and recording it, or a target deleted while unreachable,
  can leave parts that only a lifecycle rule will clean up. On AWS this is
  `AbortIncompleteMultipartUpload`; R2, B2, MinIO and Wasabi have the same
  setting.
- A lifecycle rule for how long recordings live in the bucket, if they should
  not live for ever. polyemesis never deletes objects.
- A key scoped to the one bucket, with put, get, delete (for the Test) and
  multipart permissions only.

## Serving recordings through a CDN

Offload is for keeping recordings. Live playback through a CDN pulls from the
player origin instead; see [CDN.md](CDN.md).
