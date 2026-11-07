# Publishing clips and shorts

A finished clip export, or a vertical short with burned-in captions, can be sent
to **YouTube Shorts** or **TikTok** from the clip editor and from the Jobs page.
You choose the platform and the connected account, review the title,
description and hashtags (filled in from the clip), see the privacy level, and
click **Publish**. The dialog then shows the upload's progress and ends on a
link to the post, or on a plain-words reason it did not complete.

## Nothing posts without your click

There is no auto-publish setting, no schedule and no API shortcut, and that is
enforced in four places rather than promised in one:

1. **Every publish route is session-only.** An API token of any scope, an MCP
   token and the Stream Deck plugin cannot call them, and there is no MCP tool
   for publishing. Only a signed-in console session can.
2. **The Publish button needs a confirmation.** While you edit the form the
   console checks it with `POST /publish/prepare`, which sends nothing to any
   platform and returns a one-time confirmation for exactly that form, for you,
   good for ten minutes. `POST /publish` without a matching, unspent
   confirmation is refused. Change a field and the confirmation is gone until
   the new form has been checked.
3. **The queued job is useless without the click's proof.** The click creates a
   ledger row with a secret nonce, and the job carries the same nonce. The worker
   asks the database to confirm the pair before it reads the file, fetches a
   token or sends a request; a replayed, retried or hand-made job does nothing.
4. **Retrying and cancelling are clicks too.** `POST /jobs/{id}/retry` refuses
   this job kind, cancelling the job revokes the click, and a failed publication
   is re-armed only by a new **Publish**.

## Platforms

| Platform | What is sent | Privacy |
|---|---|---|
| YouTube Shorts | Resumable upload of the MP4 (`videos.insert`) | You choose private, unlisted or public. Default private. |
| TikTok | Content Posting API, Direct Post, file upload | **Always private to you** until TikTok audits the app. Not editable. |
| X | Chunked media upload, then a post | **Off.** See below. |

**YouTube** reuses the OAuth connection you already have. `videos.insert`
accepts the `youtube` scope that polyemesis already asks for (it also accepts
`youtube.upload` and `youtube.force-ssl`), so an account connected before this
feature can publish without reconnecting. If an account's recorded grant
contains none of those, the dialog shows **Reconnect to allow uploads** and sends
you through the ordinary connect flow. An upload costs 1,600 quota units from
the same daily allowance live chat uses, and the dialog says so. Google can hold
uploads from an unaudited API project private whatever you choose; the result
shows the privacy YouTube actually applied.

**TikTok** needs its own developer app (Settings, Platforms, TikTok) with the
Login Kit and Content Posting API products and the `user.info.basic` and
`video.publish` scopes. Until TikTok has audited the app, TikTok holds every post
to private, so polyemesis sends `SELF_ONLY` and nothing else: the privacy field
has no choice, the dialog says why, and if TikTok does not offer that level for
an account the publish fails instead of falling back to another one. Connecting
TikTok here is for posting clips; TikTok LIVE stays a pasted server URL and key.

**X** publishing is built and **switched off**. It runs only when the server's
environment has `POLYEMESIS_X_PUBLISH=1`, a switch separate from the Livestream
Livestream switch (`POLYEMESIS_X_LIVESTREAM`). With it off the API refuses X,
the worker refuses X, and no request is made. X's sign-in asks for the
`tweet.write` and `media.write` scopes (sign-in scope version 2), so an X account
connected before that has to reconnect once before it can publish clips.

## Never twice

Each clip, platform and account has exactly one ledger row (a database unique
key), which records the platform's post id. Click Publish twice, or from two
tabs, or after it has posted: you get the existing post, and no second job.

Every irreversible step is preceded by durable proof of intent, so a retry
resumes or asks the platform instead of starting again:

- **YouTube**: the resumable session URL is stored (sealed) before the first
  byte. A retry sends an empty `Content-Range: bytes */N` request to the session.
  A finished upload answers with the video, which is recorded without sending
  anything; an interrupted one reports how far it got and continues from there.
- **TikTok**: the `publish_id` and upload URL are stored before the first chunk.
  A retry polls the `publish_id`.
- **X**: the post is the one step that cannot be asked about, so intent is stamped
  first and a retry that finds the stamp does not repeat it; it reports that the
  outcome is unknown and tells you to look at the account. The media upload
  remembers how far it got, so a retry redoes an unfinished append or finalize
  rather than posting half-uploaded media.

Session and upload URLs are capabilities, so they are stored sealed, and are kept
out of job parameters, the job log, results and error text; anything that looks
like a link or a token is redacted from stored and displayed failure text as a
second layer.

**One run at a time.** Every write a worker makes to its row carries the nonce of
the click that started it. If you cancel and click Publish again, a worker left
over from the first run finds its writes refused and stops; it cannot disturb the
new run. The one exception is on purpose: if the platform has the post, the ledger
records it even over a local failure, so the next click cannot post it again.

**When the last request may have landed.** If a request that cannot be asked about
afterwards (the final upload byte, the X post) loses its answer, the failure is
`outcome_unknown`, nothing is sent again, and the dialog asks you to look at the
account. Publishing again is only possible after you tick that you checked and the
post is not there; the server refuses the re-arm without it.

## Failure reasons

A failed publish says why in the same shape as a destination's failure reason (a
cause and the line it rests on), in plain words, with what to do next:
`needs_reconnect`, `token_rejected`, `quota_exceeded`, `rate_limited`,
`platform_unavailable`, `network`, `clip_missing`, `clip_rejected`,
`privacy_unavailable`, `platform_refused`, `outcome_unknown`, `gated`,
`not_confirmed`, `cancelled`, `unknown`, `upload_limited` (the channel reached the platform's upload
limit, which is not the API allowance). Temporary causes are retried with the
queue's backoff, up to five attempts.

## Where it runs

Publishing is a job of kind `clip.publish` on the post-production queue, one at a
time. The governor holds it back while a broadcast is live, because an upload
competes with the stream for upstream bandwidth; the dialog shows that reason
while it waits.

## Manual live test

The automated tests use fake platform servers and no credentials. Checking
against the real platforms is a manual step:

1. Connect a YouTube account (Settings, Platforms). Export a short in the clip
   editor. Open **Publish**, pick YouTube Shorts, keep the default privacy
   (private), click **Publish**. Confirm the video appears in YouTube Studio,
   and that the privacy shown in the dialog matches Studio.
2. Click **Publish** again on the same clip and account. Confirm nothing new is
   uploaded and the dialog reports the existing post.
3. Disconnect the network mid-upload (or stop the server and start it again).
   Confirm the upload resumes and YouTube ends up with one video.
4. Create a TikTok developer app, add the account as a target user, save the
   credentials, connect it, and publish. Confirm the post is private on TikTok.
5. Leave `POLYEMESIS_X_PUBLISH` unset and confirm X is greyed out with its reason.
