/** The hostname placeholder the server puts in publishUrls.
 *
 *  internal/api/sources.go builds each publish URL with this literal where the
 *  hostname goes, because the server does not know which of its addresses the
 *  operator can actually reach it on -- it may have several, and the useful one
 *  is whichever they are using right now.
 */
export const SERVER_PLACEHOLDER = "<server>";

/** Fills the server's placeholder with a host an encoder can dial.
 *
 *  The URLs arrive as `rtmp://<server>:1935/live`, and nothing was replacing the
 *  placeholder -- so the Sources page displayed it verbatim and the copy button
 *  put it on the clipboard, where it goes straight into OBS and fails.
 *
 *  THE BROWSER'S OWN HOSTNAME IS THE ANSWER, and it is not a guess. Whatever
 *  address the operator is reading this page on is, by construction, an address
 *  that reaches this server from where they are sitting. A configured DNS name
 *  might read more nicely, but it can also be absent, stale, or resolvable only
 *  from outside -- and an address that is merely prettier is worth nothing to an
 *  encoder that cannot connect to it.
 *
 *  An empty host leaves the placeholder alone. `rtmp://:1935/live` is not an
 *  improvement on a visible `<server>`: one is obviously unfinished, the other
 *  looks like an address and fails later.
 */
export function withServerHost(url: string, host: string): string {
  const h = host.trim();
  if (!url || !h) return url;
  return url.split(SERVER_PLACEHOLDER).join(bracketIfIPv6(h));
}

/** IPv6 literals need brackets inside a URL's authority, or the colons read as
 *  a port separator. window.location.hostname is inconsistent about supplying
 *  them, so this normalises rather than trusting the caller. */
function bracketIfIPv6(host: string): string {
  if (host.startsWith("[")) return host;
  // A bare IPv6 literal is the only host with more than one colon; "host:port"
  // has exactly one and is not something this is ever handed.
  return host.split(":").length > 2 ? `[${host}]` : host;
}

/** The rows the Sources page renders, with the placeholder already filled in.
 *
 *  Extracted from the JSX so the substitution has somewhere to be tested. It was
 *  previously a `withServerHost` call inside a `.map()`, which is logic in a
 *  place no unit test can reach -- and the wiring, not the helper, is where the
 *  bug lived: the helper did not exist at all, and both the displayed URL and
 *  the copied one used the raw string.
 *
 *  Empty entries are dropped here rather than by a `url ? ... : null` at the
 *  call site, so "which rows exist" is one decision in one place.
 */
export function publishRows(
  urls: Record<string, string>,
  host: string,
): { proto: string; url: string }[] {
  return Object.entries(urls ?? {})
    .filter(([, raw]) => Boolean(raw))
    .map(([proto, raw]) => ({ proto, url: withServerHost(raw, host) }));
}
