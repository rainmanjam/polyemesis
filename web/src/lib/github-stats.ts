/* Repository figures read at BUILD time, so the page can show a real star
 * count and the latest release without a visitor's browser calling GitHub.
 *
 * FAILS CLOSED. Every path that does not produce a clean number returns null,
 * and the page then prints no number. The alternative -- a cached or
 * hard-coded count -- is exactly the stale, decorative figure this site
 * refuses to publish, so there is no fallback value here.
 *
 * `fetchImpl` is injected so the tests can stand in for GitHub; the build uses
 * the global fetch. */

export const REPO = "rainmanjam/polyemesis";

export interface RepoStats {
  stars: number;
  /** The latest published release tag, or null when none exists yet. */
  release: string | null;
}

const TIMEOUT_MS = 4000;

async function getJson(fetchImpl: typeof fetch, url: string): Promise<unknown | null> {
  try {
    const res = await fetchImpl(url, {
      headers: { accept: "application/vnd.github+json", "user-agent": "polyemesis-site-build" },
      signal: AbortSignal.timeout(TIMEOUT_MS),
    });
    if (!res.ok) return null;
    return await res.json();
  } catch {
    return null;
  }
}

export async function fetchRepoStats(fetchImpl: typeof fetch = fetch): Promise<RepoStats | null> {
  const repo = await getJson(fetchImpl, `https://api.github.com/repos/${REPO}`);
  if (!repo || typeof (repo as { stargazers_count?: unknown }).stargazers_count !== "number") {
    return null;
  }
  const stars = (repo as { stargazers_count: number }).stargazers_count;

  // A repository with no release is normal before a first tag: 404, no tag.
  const latest = await getJson(fetchImpl, `https://api.github.com/repos/${REPO}/releases/latest`);
  const tag = (latest as { tag_name?: unknown } | null)?.tag_name;
  return { stars, release: typeof tag === "string" && tag.length > 0 ? tag : null };
}
