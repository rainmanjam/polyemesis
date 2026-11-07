/* Downloading chat: one recording's, and any window of it.
 *
 * The server streams both (internal/api/chat_export.go). A recording's chat is
 * small and can fail in a way the operator needs to read -- "no longer stored" --
 * so it goes through fetch and a toast. A free window can be large and its
 * parameters are validated here first, so it is a plain navigation and the
 * browser saves the stream as it arrives. */

export type RecordingChatFormat = "json" | "vtt";
export type ChatExportFormat = "json" | "csv" | "ndjson";

export const CHAT_EXPORT_FORMATS: readonly ChatExportFormat[] = ["json", "csv", "ndjson"];

/** The name a recording's chat file is saved under: the sidecar's own name. */
export function recordingChatFileName(filename: string, format: RecordingChatFormat): string {
  const dot = filename.lastIndexOf(".");
  const base = dot > 0 ? filename.slice(0, dot) : filename;
  return `${base}.chat.${format}`;
}

/** A `datetime-local` value ("2026-03-04T20:00") read in the browser's time
 *  zone, or null when blank or unparseable. */
export function parseLocalInput(value: string): Date | null {
  if (!value) return null;
  const d = new Date(value);
  return Number.isNaN(d.getTime()) ? null : d;
}

/** The `datetime-local` text for a date, in the browser's time zone. */
export function toLocalInput(d: Date): string {
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

export type ChatExportProblem = "needFrom" | "needOrder";

/** What is wrong with a window, or null. The server refuses the same two cases
 *  with a 400; saying so here keeps a bad window from becoming a downloaded
 *  error file. */
export function chatExportProblem(from: string, to: string): ChatExportProblem | null {
  const f = parseLocalInput(from);
  if (!f) return "needFrom";
  const t = parseLocalInput(to);
  if (t && t.getTime() <= f.getTime()) return "needOrder";
  return null;
}

/** GET /chat/export for a window. A blank `to` means "until now" on the server. */
export function chatExportUrl(
  base: string,
  p: { from: Date; to: Date | null; platform: string; format: ChatExportFormat },
): string {
  const q = new URLSearchParams({ from: p.from.toISOString(), format: p.format });
  if (p.to) q.set("to", p.to.toISOString());
  if (p.platform) q.set("platform", p.platform);
  return `${base}/chat/export?${q.toString()}`;
}

/** Saves a URL the server answers with an attachment, or throws the server's
 *  own sentence when it answers with an error. */
export async function downloadChatFile(url: string, filename: string): Promise<void> {
  const res = await fetch(url, { credentials: "same-origin" });
  if (!res.ok) {
    let reason = `${res.status}`;
    try {
      const body = (await res.json()) as { error?: string };
      if (body.error) reason = body.error;
    } catch {
      /* the status stands */
    }
    throw new Error(reason);
  }
  const href = URL.createObjectURL(await res.blob());
  try {
    const a = document.createElement("a");
    a.href = href;
    a.download = filename;
    document.body.appendChild(a);
    a.click();
    a.remove();
  } finally {
    URL.revokeObjectURL(href);
  }
}
