import type { PlatformAccount } from "./types";

/* The "reconnect to enable" banner's rules, as values a test can hold.
 *
 * The server says, per account, the scope version it was granted under
 * (scopeVer), the one this build asks for (currentScopeVersion) and the i18n
 * keys for what the bumps in between unlock (scopeUnlocks); see
 * internal/oauth/scopever.go. An account is behind when its grant is older than
 * the build's AND there is something to say about it. Version 0 rows predate the
 * column and are handled in Settings, which judges them on their scopes. */

const STORAGE_KEY = "polyemesis.reconnectBanner.dismissed";

/** Accounts whose grant is older than the build's and whose bump has a note. */
export function accountsBehind(accounts: readonly PlatformAccount[]): PlatformAccount[] {
  return accounts.filter(
    (a) =>
      a.scopeVer > 0 &&
      (a.currentScopeVersion ?? 0) > a.scopeVer &&
      (a.scopeUnlocks?.length ?? 0) > 0,
  );
}

/** One dismissal: this account, at this required version. A later bump changes
 *  the version and so brings the banner back. */
export function dismissalKey(a: Pick<PlatformAccount, "platform" | "accountRef" | "currentScopeVersion">): string {
  return `${a.platform}:${a.accountRef}:v${a.currentScopeVersion ?? 0}`;
}

export function readDismissed(): Set<string> {
  try {
    const raw = localStorage.getItem(STORAGE_KEY);
    const parsed: unknown = raw ? JSON.parse(raw) : [];
    return new Set(Array.isArray(parsed) ? parsed.filter((x): x is string => typeof x === "string") : []);
  } catch {
    // Storage blocked or the value is junk: show the banner rather than hide it.
    return new Set();
  }
}

export function writeDismissed(set: ReadonlySet<string>): void {
  try {
    localStorage.setItem(STORAGE_KEY, JSON.stringify([...set]));
  } catch {
    // A dismissal that does not persist only costs a second click next load.
  }
}
