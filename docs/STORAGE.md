# Offload storage (S3-compatible)

polyemesis can copy finished recordings to an S3-compatible bucket: AWS S3,
Cloudflare R2, Backblaze B2 (its S3 API), MinIO or Wasabi. Add a target on the
**Recordings** page, under **Offload storage**, or with
`POST /api/v1/storage/targets` (see [API.md](API.md#storage-targets-offload)).

Offload is **mirror mode**: it copies, and it never moves or deletes a local
file. Local retention keeps working exactly as it does without offload, and the
copy in the bucket lives as long as the bucket's own lifecycle rule allows.
Nothing in this release removes a file from local disk on offload's account.

## What a target holds

| Field | Meaning |
|---|---|
| `endpoint` | The service URL, scheme included: `https://s3.us-east-1.amazonaws.com`, `https://<account>.r2.cloudflarestorage.com`, `https://s3.us-west-004.backblazeb2.com`, `http://minio.lan:9000`, `https://s3.us-east-1.wasabisys.com` |
| `bucket`, `region` | The bucket must already exist. R2 uses region `auto` |
| `prefixTemplate` | The key prefix. Tokens: `{kind}` (recordings, stems, transcripts, clips), `{source}` (`s<id>` or `unattributed`), `{yyyy}` `{mm}` `{dd}` (UTC, from the recording's start). Blank means `polyemesis/{kind}/{yyyy}/{mm}/{dd}`. The file's own path follows the prefix, so two files never share a key |
| `pathStyle` | `<endpoint>/<bucket>/<key>` instead of `<bucket>.<host>/<key>`. MinIO and most self-hosted targets need it; a bucket name with a dot uses it automatically over HTTPS |
| `accessKeyId`, `secretAccessKey` | The credential. The secret is write-only (see below) |
| `offloadStems`, `offloadTranscripts`, `offloadClips` | Also copy those |
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
single upload, `md5(part MD5s)-N` for a multipart one; not compared when the
service reports SSE-KMS or a customer key, whose ETags are not MD5s) and the
SHA-256 stored with it as metadata, and with `verifyDownload` the bytes
themselves. Only then is the item `verified`. A file that changes after it was
verified is uploaded again.

A recording's saved chat (`<name>.chat.json` and `.chat.vtt`, see
[CHAT-EXPORT.md](CHAT-EXPORT.md)) is sent with it, under the same `{kind}`
(`recordings`) and with the same checks. There is no separate switch: they are a
few kilobytes beside the recording.

Files above 64 MiB go up in 16 MiB parts. Each part is retried on its own, and
progress is saved after every part, so a retry or a restart sends only what
never arrived.

## Failures, stuck items and Retry now

A failed upload is retried with backoff (30 s, doubling, at most an hour). After
`maxAttempts` failures in a row, or at once on an error that waiting cannot cure
(a refused key, a missing bucket, a key the file cannot have), the item is
`stuck`: it is not retried on its own, and `offload.stuck` is raised as an alert,
as a webhook ([HOOKS.md](HOOKS.md)) and on the events stream
([EVENTS.md](EVENTS.md)), and each target's stuck count is published over MQTT
([MQTT.md](MQTT.md)). **Retry now**, on the target or on the Library item, puts
it back with its attempts reset. A stuck multipart upload is cancelled on the
service.

## Restarts

An upload interrupted by a shutdown or a crash comes back as failed and due at
once, without spending an attempt; a multipart upload resumes from its saved
parts.

## Moving a target

Changing a target's endpoint, bucket, region, access key or addressing sends
every one of its files back to be uploaded at the new address. An upload that
was still running against the old address when the change was saved cannot
record its result: the target's binding generation moved on, and only a write
made at the current generation lands.

## Credentials

The secret access key is sealed with the install's secret key the moment it is
saved and is never returned: responses say `hasSecret`, and a read-scoped token
also gets `accessKeyId` blanked. The secret is not logged and never appears in
an error. A saved secret is used only for the endpoint, bucket, addressing and
private-address setting it was saved with; change any of them and the secret
has to be entered again.

## Recommended bucket settings

- **A lifecycle rule that aborts incomplete multipart uploads** after a few
  days. polyemesis aborts the uploads it gives up on and orphans it finds at
  startup, and tries to abort the uploads of a target you delete or move. When
  that abort fails (revoked keys, a policy that denies it, an endpoint that is
  gone) the target is still deleted, and the response's `warning` and
  `unabortedUploads` name each upload's key and upload id so you can abort it
  with your provider. A process that dies between starting an upload and
  recording it can also leave parts that only a lifecycle rule will clean up. On AWS this is `AbortIncompleteMultipartUpload`;
  R2, B2, MinIO and Wasabi have the same setting.
- A lifecycle rule for how long recordings live in the bucket, if they should
  not live for ever. polyemesis never deletes objects.
- A key scoped to the one bucket, with put, get, delete (for the Test) and
  multipart permissions only.

## Serving recordings through a CDN

Offload is for keeping recordings. Live playback through a CDN pulls from the
player origin instead; see [CDN.md](CDN.md).
