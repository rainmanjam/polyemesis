import type { FailoverSource } from "@/lib/types";

/** Whole seconds a failover has held the air, from the server's `switchedAt`.
 *
 *  Null when there is no honest figure: the server sends the zero time until a
 *  switch has happened, and an unparseable stamp is not a duration. A switch
 *  stamped in the future (clock skew between the engine and this browser) is
 *  clamped to zero rather than shown as a negative wait. */
export function failoverElapsedSeconds(switchedAt: string, nowMs: number): number | null {
  if (!switchedAt) return null;
  const at = Date.parse(switchedAt);
  // Year 1 is Go's zero time; anything before 2000 is not a real switch.
  if (Number.isNaN(at) || at < Date.parse("2000-01-01T00:00:00Z")) return null;
  return Math.max(0, Math.floor((nowMs - at) / 1000));
}

/** "45s", "2m 14s", "1h 02m". Seconds are dropped once an hour has passed,
 *  because a banner that ticks every second should not jitter at the end. */
export function formatElapsed(totalSeconds: number): string {
  const s = Math.max(0, Math.floor(totalSeconds));
  if (s < 60) return `${s}s`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}m ${String(s % 60).padStart(2, "0")}s`;
  const h = Math.floor(m / 60);
  return `${h}h ${String(m % 60).padStart(2, "0")}m`;
}

/** The banner's colour is its severity. The backup and the playlist are a
 *  degraded picture the audience still gets; a slate is no picture at all. The
 *  primary draws no banner, so it has no tone. */
export function failoverBannerTone(active: FailoverSource): "warn" | "down" | null {
  if (active === "primary") return null;
  if (active === "slate") return "down";
  return "warn";
}

/** The switch as a clock time for the static "since" line. Null when there is
 *  no real switch to name, so the banner never states a time it does not have.
 *  Deliberately NOT ticking: the ticking figure is the elapsed one, and only it
 *  sits outside the live region. */
export function failoverSinceClock(switchedAt: string): string | null {
  if (failoverElapsedSeconds(switchedAt, Date.now()) === null) return null;
  return new Date(switchedAt).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
}
