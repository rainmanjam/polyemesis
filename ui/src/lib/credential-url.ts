/** Does this ingest URL carry a credential inside it?
 *
 *  `system.ingestUrl` is one string that is sometimes an address and sometimes
 *  an address with a secret welded into it — internal/api/handlers.go says so
 *  in as many words: PublicIngestURL renders
 *  `srt://host:port?...&passphrase=<cleartext>` in SRT mode and returns the
 *  pull URL verbatim, userinfo and all, in pull mode. The server masks it for a
 *  read-scope principal; an ADMIN gets the real thing, and an admin is exactly
 *  who is screen-sharing while setting a broadcast up.
 *
 *  So the console cannot mask this field unconditionally without hiding a
 *  plain RTMP address that is not secret and that an operator checks at a
 *  glance. It asks the string instead.
 *
 *  Matches the two shapes the server documents, and nothing else. A URL that
 *  hides a credential some third way reads as safe here — the same known hole
 *  as secret-fields.test.ts, and worth the same note: this covers what the
 *  server actually constructs today. */
export function urlCarriesCredential(url: string): boolean {
  if (!url) return false;
  // SRT: the passphrase rides in the query string.
  if (/[?&]passphrase=[^&]+/i.test(url)) return true;
  // Pull: userinfo before the host, i.e. scheme://user:pass@host. Guarded to
  // the authority section so a '@' anywhere later in the URL does not count.
  const authority = /^[a-z][a-z0-9+.-]*:\/\/([^/?#]*)/i.exec(url)?.[1] ?? "";
  return authority.includes("@");
}
