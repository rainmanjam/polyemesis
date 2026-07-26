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

export function timestamp(iso: string): string {
  if (!iso) return "";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "";
  return d.toLocaleString(undefined, {
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
  return d.toLocaleTimeString(undefined, { hour12: false });
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
