# Clip publishing: YouTube, TikTok and X upload APIs, checked 2026-11-02

Researched for Wave 4 #5c. Same standard as the other files in this folder: each
claim traces to the platform's own reference, read on the date in the title.
Nothing here was run against a live account; that is the manual step in
`docs/CLIP-PUBLISHING.md`.

## YouTube

* `videos.insert` "requires authorization with at least one of the following
  scopes": `youtube.upload`, `youtube`, `youtubepartner`, `youtube.force-ssl`
  (developers.google.com/youtube/v3/docs/videos/insert). **polyemesis already
  requests `youtube`**, so `youtube.upload` adds nothing a connected account
  lacks; the publisher accepts any of the four.
* Resumable upload: `POST /upload/youtube/v3/videos?uploadType=resumable` returns
  the session URI in `Location`. To check an interrupted upload, "send an empty
  PUT request to the upload URL ... set the `Content-Range` header value to
  `bytes */CONTENT_LENGTH`" with `Content-Length: 0`. To resume, `PUT` the
  remaining bytes with `Content-Range: bytes FIRST_BYTE-LAST_BYTE/TOTAL`, where
  FIRST_BYTE is one more than the last byte of the `Range` header the status
  check returned; "none of the binary content will be uploaded" if the range
  overlaps or skips (using_resumable_upload_protocol).
* The quota cost of `videos.insert` is 1,600 units (already recorded in
  `internal/oauth/quota_meter.go`).
* **Unverified API projects.** The revision history (28 July 2020) says videos
  uploaded through `videos.insert` from unverified API projects created after
  that date "will be restricted to private viewing mode" until the project is
  audited. The `videos.insert` reference's own property table says the opposite
  ("Videos uploaded from unverified API projects are not restricted to private
  viewing mode"). The two disagree, so the publisher takes the privacy YouTube
  reports back (`status.privacyStatus`) and the dialog shows it.
* `status.selfDeclaredMadeForKids` is optional on insert. It is left unset so the
  channel's own default applies; polyemesis does not declare an audience on the
  operator's behalf.

## TikTok (Content Posting API and Login Kit)

* Direct Post flow: `POST /v2/post/publish/creator_info/query/` (returns
  `privacy_level_options`, `max_video_post_duration_sec`), then
  `POST /v2/post/publish/video/init/` with `post_info` (`title`, `privacy_level`,
  `brand_content_toggle` required) and `source_info` (`source: FILE_UPLOAD`,
  `video_size`, `chunk_size`, `total_chunk_count`), which returns `publish_id` and
  `upload_url`; then sequential `PUT` of chunks with `Content-Range`; then
  `POST /v2/post/publish/status/fetch/` with the `publish_id`.
* `privacy_level` "must match one of the `privacy_level_options` returned in the
  `/creator_info/query/` API", and `SELF_ONLY` is in the list for public and
  private creator accounts alike. polyemesis sends `SELF_ONLY` only and fails if
  creator info does not list it.
* Chunk rules: each chunk 5 MB to 64 MB except the final one (up to 128 MB);
  under 5 MB goes whole; over 64 MB must be several chunks; 1 to 1,000 chunks;
  `total_chunk_count` is `video_size / chunk_size` rounded down; chunks are
  uploaded sequentially.
* Status values: `PROCESSING_UPLOAD`, `PROCESSING_DOWNLOAD`,
  `SEND_TO_USER_INBOX`, `PUBLISH_COMPLETE`, `FAILED` (with `fail_reason`);
  `publicaly_available_post_id` (sic) "is returned only if the post is published
  for public viewership and has been approved by the TikTok moderation process",
  so a private post has only its `publish_id`, which is what polyemesis records.
  `uploaded_bytes` is reported for `FILE_UPLOAD`.
* Login Kit: authorize at `https://www.tiktok.com/v2/auth/authorize/` with
  `client_key`, `scope` (comma separated), `redirect_uri`, `state`,
  `response_type=code`; tokens at `POST https://open.tiktokapis.com/v2/oauth/token/`
  (form encoded, `client_key`, `client_secret`); refresh returns a possibly new
  `refresh_token` that must replace the old one.
* Not read: TikTok's UX conformance requirements for audit (consent text,
  creator display). They are an audit matter, not a sandbox one, and are listed
  as an open item in the PR.

## X

* Media upload v2: `POST /2/media/upload/initialize` (`media_type`, `total_bytes`,
  `media_category` such as `tweet_video`), `POST /2/media/upload/{id}/append`
  (`media` base64, `segment_index`), `POST /2/media/upload/{id}/finalize`,
  `GET /2/media/upload?command=STATUS&media_id=` until `processing_info.state` is
  `succeeded`, then `POST /2/tweets` with `media.media_ids`
  (docs.x.com/x-api/media/quickstart/media-upload-chunked). The append request is
  described as JSON with base64 in the reference page and as multipart in a
  quickstart; the adapter uses JSON and is **unverified live**.
* Posting needs `tweet.write` and `media.write`; polyemesis's X sign-in grant has
  neither, and the adapter reports that instead of letting X's 403 say it.
