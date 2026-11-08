/* Who holds a role in a channel, remembered for a minute.

   The viewer card asks "is this person a moderator / VIP?" and the answer is a
   list call per role, so it is cached per channel. EVERY place that changes a
   role goes through api.ts, which calls invalidateRoles -- the Twitch tools
   panel, the X moderator panel and the card itself -- so a change made in one
   place is never hidden by a stale answer in another. */

const TTL_MS = 60_000;
const cache = new Map<string, { at: number; ids: Set<string> }>();

const scope = (platform: string, account: string | undefined) => `${platform}|${account ?? ""}|`;

export function cachedHolders(platform: string, account: string | undefined, role: string): Set<string> | null {
  const hit = cache.get(scope(platform, account) + role);
  return hit && Date.now() - hit.at < TTL_MS ? hit.ids : null;
}

export function storeHolders(platform: string, account: string | undefined, role: string, ids: Set<string>): void {
  cache.set(scope(platform, account) + role, { at: Date.now(), ids });
}

/** Drops every remembered role list for one channel. Called after any role
 *  change, successful or not: a failed call may still have changed something. */
export function invalidateRoles(platform: string, account: string | undefined): void {
  const prefix = scope(platform, account);
  for (const k of cache.keys()) if (k.startsWith(prefix)) cache.delete(k);
}

/** Test seam. */
export function resetRoleCache(): void {
  cache.clear();
}
