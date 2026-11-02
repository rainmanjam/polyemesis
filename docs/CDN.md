# Putting a pull CDN in front of the player

The player origin (`/playout/`) can sit behind a pull CDN such as Cloudflare or
Bunny. The CDN fetches from polyemesis on the first request at each edge and
serves later viewers from its cache. Nothing is pushed to a bucket.

**Only an open stream is cached at the edge.** A stream published with
protection set to *open* answers with `public` caching headers. A token-gated
stream, and anything an operator sees while signed in, answers `private` or
`no-store`, so a CDN passes those requests through to the origin every time. Do
not override that with a "cache everything" rule for a gated stream: most CDNs,
Cloudflare included, ignore `Vary`, and `Cache-Control: private` is then the
only thing keeping one viewer's token-gated segments from being served to the
next.

## What the origin sends

| File | Open stream | Gated stream, or signed in |
|---|---|---|
| `*.m3u8`, `*.mpd` (playlists) | `public, max-age=1` | `no-store` |
| `seg_*.ts` (HLS segments) | `public, max-age=31536000, s-maxage=<window>` | `private, max-age=31536000, immutable` |
| `init-*.m4s`, `chunk-*.m4s`, `*.vtt` | `public, max-age=<segment seconds>` | `private, max-age=<segment seconds>` |
| `/api/v1/playout/poster.jpg` | `public, max-age=10` | `private, max-age=10` |
| `/api/v1/playout/public` (the watch page's view) | `public, max-age=5` | `private, no-store` |
| a refusal (401, 404, 503) | `no-store` | `no-store` |

`<window>` is the seekable window: the DVR window when one is set, otherwise
the live playlist (segment length times playlist segments), and never less than
one segment.

Every file also carries a strong `ETag`, so a CDN or a player can revalidate
with `If-None-Match` and get a `304`. HLS segment names are numbered from the
wall clock, so a name is never reused after the packager restarts and a cached
segment can never be the wrong bytes.

## Unpublishing, and turning protection on

A CDN edge keeps what it already holds until it expires. After you unpublish a
stream or switch it from open to token-gated:

- playlists stop being served from the edge within about a second;
- segments the edge already has can still be served for up to `s-maxage` (the
  seekable window). No playlist lists them any more, so a normal player never
  asks for them; somebody holding an old segment URL could.

If that window matters, purge after unpublishing:

- **Cloudflare:** *Caching → Configuration → Purge Cache → Custom Purge*, and
  purge by prefix `<your-host>/playout/` (no scheme; every plan has prefix purge), or *Purge Everything* for the
  zone. The API equivalent is `POST /zones/<zone>/purge_cache` with
  `{"prefixes": ["<your-host>/playout/"]}`.
- **Bunny:** the pull zone's *Purge* page, purging `https://<zone>.b-cdn.net/playout/*`,
  or `POST https://api.bunny.net/purge?url=<url>` with a wildcard URL.

polyemesis does not call either API: it holds no CDN credentials.

## Cloudflare

1. Put the polyemesis hostname behind Cloudflare (proxied, orange cloud). The
   origin must answer HTTPS on 443; see [TLS.md](TLS.md).
2. Cloudflare caches by file extension, and `.ts`, `.m4s` and `.m3u8` are not
   on its default list. Add one **Cache Rule** for URI path starts with
   `/playout/`: *Eligible for cache*, with the edge TTL set to *Use cache-control
   header if present* (the default "respect origin" behaviour). Do not choose an
   override TTL or *Ignore cache-control*: the origin's `private` and `no-store`
   are what keep a gated stream's responses out, and an override would cache them.
3. Leave Browser Cache TTL at *Respect existing headers*.
4. Keep the rest of the console out of that rule and out of any other cache
   rule; Cloudflare does not cache it by default.
5. In **Network**, keep WebSockets on: the console uses them.

## Bunny

1. Create a pull zone whose origin is `https://<your-host>`.
2. In *Caching*, set *Cache expiration time* to **Respect origin cache-control**
   and leave *Query string sort* and *Vary cache* off: the token travels in the
   query string or a cookie on a gated stream, and the origin's `private` is
   what keeps those responses out.
3. Embed the player with the pull zone's hostname
   (`https://<zone>.b-cdn.net/watch` or your CNAME).

## Checking it

```sh
curl -sI https://<cdn-host>/playout/master.m3u8 | grep -i -E 'cache-control|etag|cf-cache-status|cdn-cache'
curl -sI https://<cdn-host>/playout/source/<segment>.ts | grep -i -E 'cache-control|etag|cf-cache-status|cdn-cache'
```

An open stream's second segment request should report a cache `HIT`; a gated
stream's should always reach the origin (`BYPASS`, `DYNAMIC` or `MISS`).
