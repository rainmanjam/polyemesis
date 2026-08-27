/** Formatters shared by every stat readout. All produce fixed-width-ish
 *  strings so columns of numbers stay aligned under `.tnum`. */

export function bytes(n: number): string {
  if (!n || n < 0) return "0 B";
  const units = ["B", "KB", "MB", "GB", "TB"];
  let i = 0;
  let v = n;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v.toFixed(v >= 100 || i === 0 ? 0 : 1)} ${units[i]}`;
}

export function kbps(n: number): string {
  if (!n || n < 0) return "0 kbps";
  if (n >= 1000) return `${(n / 1000).toFixed(2)} Mbps`;
  return `${n.toFixed(0)} kbps`;
}

/** Uptime as h:mm:ss — the format a broadcaster reads at a glance. */
export function duration(seconds: number): string {
  if (!seconds || seconds < 0) return "--:--:--";
  const s = Math.floor(seconds);
  const h = Math.floor(s / 3600);
  const m = Math.floor((s % 3600) / 60);
  const sec = s % 60;
  return `${String(h).padStart(2, "0")}:${String(m).padStart(2, "0")}:${String(sec).padStart(2, "0")}`;
}

export function shortDuration(ms: number): string {
  return duration(ms / 1000);
}

/* THE INSTALL'S DISPLAY TIME ZONE.
 *
 * Every time in this console was rendered in whatever zone the BROWSER happens
 * to be in, and the server logged in UTC. So an operator in Los Angeles reading
 * a log line beside the screen that produced it was comparing 05:51Z with
 * 22:51, in their head, at the moment something was going wrong -- and two
 * people on the same production, in different places, could not read each
 * other's screenshots at all.
 *
 * Settings.Display.TimeZone names one zone for the whole install. Held in a
 * module variable rather than threaded through every caller because it is a
 * property of the install, not of any component, and it changes about once in
 * the life of a deployment.
 *
 * Empty is UTC, deliberately -- NOT the browser's zone, which is what this
 * replaces. A console whose times move depending on who is looking at it is
 * the thing being fixed; falling back to the browser would leave the default
 * install with exactly the behaviour that made this necessary.
 */
let displayZone = "UTC";

/** Called once when settings load, and again when they are saved. */
export function setDisplayTimeZone(tz: string | null | undefined): void {
  const want = (tz ?? "").trim();
  if (!want) {
    displayZone = "UTC";
    return;
  }
  // A zone the browser cannot resolve must not take the console's clocks with
  // it. The server validates on save, so this only fires when the two disagree
  // -- an old browser against a zone database the server has -- and UTC is the
  // honest answer there rather than a thrown error on every timestamp.
  try {
    new Intl.DateTimeFormat(undefined, { timeZone: want });
    displayZone = want;
  } catch {
    displayZone = "UTC";
  }
}

/** The zone every clock in the console is currently drawn in. */
export function displayTimeZone(): string {
  return displayZone;
}

export function timestamp(iso: string): string {
  if (!iso) return "";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "";
  // A ZERO TIME IS NOT A TIME. Go marshals an unset time.Time as
  // "0001-01-01T00:00:00Z", which parses perfectly and is a non-empty string,
  // so a caller guarding on truthiness sees a value and renders it -- through
  // a local-time offset, which is how the automation page came to report
  // LAST DELIVERY 12/31/1, 16:07:02 beside six counters all reading 0.
  //
  // Fixed at the source too (hooks.Stats.MarshalJSON now omits it), and this
  // stays because that is one field of many: every Go timestamp in this API
  // has the same zero and this function is the single place they all pass
  // through. Returning "" lets callers treat it exactly like a missing value.
  if (d.getUTCFullYear() <= 1) return "";
  return d.toLocaleString(undefined, {
    timeZone: displayZone,
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
    hour12: false,
  });
}

export function clockTime(iso: string): string {
  if (!iso) return "";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "";
  return d.toLocaleTimeString(undefined, { timeZone: displayZone, hour12: false });
}

/** Percent as an integer with a trailing sign. */
export function pct(n: number): string {
  return `${(n ?? 0).toFixed(0)}%`;
}

/** dBFS with one decimal; -100 (our silence floor) reads as a dash. */
export function db(v: number): string {
  if (v <= -100) return "-∞";
  return v.toFixed(1);
}

/** Gain coefficient rendered as the percentage the UI edits. */
export function gainPct(g: number): string {
  return `${Math.round(g * 100)}%`;
}
