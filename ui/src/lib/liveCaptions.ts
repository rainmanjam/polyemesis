/** Live captions on the public player (#8).
 *
 *  The server writes a growing WebVTT file, captions.vtt, beside the HLS window.
 *  It is not an HLS subtitle rendition: it names its epoch in a header line
 *  (X-POLYEMESIS-EPOCH-MS, Unix milliseconds) and places every cue at the moment
 *  its words were spoken, measured from that epoch. The player knows the
 *  wall-clock time of its playhead from the playlist's EXT-X-PROGRAM-DATE-TIME,
 *  so a cue's wall-clock time maps onto the video's own currentTime, live and
 *  after a seek back through the DVR window alike.
 *
 *  Everything here is pure except useLiveCaptionTrack, which polls the file and
 *  adds cues to a captions TextTrack on the video element. */

import { useEffect } from "react";

/** One cue on the wall clock, in Unix milliseconds. */
export interface WallCue {
  startMs: number;
  endMs: number;
  text: string;
}

/** The header line naming the file's epoch. Matches transcribe.LiveVTTEpochHeader. */
export const EPOCH_HEADER = "X-POLYEMESIS-EPOCH-MS";

/** Parses "HH:MM:SS.mmm" or "MM:SS.mmm" into milliseconds, or null. */
export function parseVTTTime(s: string): number | null {
  const m = /^(?:(\d+):)?(\d{2}):(\d{2})\.(\d{3})$/.exec(s.trim());
  if (!m) return null;
  const [, h, mm, ss, ms] = m;
  return ((Number(h ?? 0) * 60 + Number(mm)) * 60 + Number(ss)) * 1000 + Number(ms);
}

/** Reads the sidecar: its epoch and its cues on the wall clock. A file without
 *  an epoch has no wall-clock placement, so it yields no cues rather than cues
 *  in the wrong place. A malformed block is skipped. */
export function parseLiveVTT(body: string): WallCue[] {
  const blocks = body.replace(/\r\n?/g, "\n").split(/\n{2,}/);
  const header = blocks.shift() ?? "";
  if (!header.startsWith("WEBVTT")) return [];
  let epoch: number | null = null;
  for (const line of header.split("\n")) {
    const [k, v] = line.split(":", 2);
    if (k?.trim() === EPOCH_HEADER && v !== undefined && /^\s*\d+\s*$/.test(v)) epoch = Number(v);
  }
  if (epoch === null) return [];
  const cues: WallCue[] = [];
  for (const block of blocks) {
    const lines = block.split("\n").filter((l) => l !== "");
    const at = lines.findIndex((l) => l.includes("-->"));
    if (at < 0) continue;
    const [a, b] = lines[at].split("-->");
    const start = parseVTTTime(a);
    const end = parseVTTTime((b ?? "").trim().split(/\s+/)[0] ?? "");
    const text = lines.slice(at + 1).join("\n");
    if (start === null || end === null || end < start || text === "") continue;
    cues.push({ startMs: epoch + start, endMs: epoch + end, text });
  }
  return cues;
}

/** Where the playhead is on the wall clock: the media time `mediaTime`
 *  (seconds) was shown at `wallMs`. */
export interface ClockAnchor {
  wallMs: number;
  mediaTime: number;
}

/** The video time, in seconds, at which a wall-clock moment is shown. */
export function mediaTimeOf(wallMs: number, anchor: ClockAnchor): number {
  return anchor.mediaTime + (wallMs - anchor.wallMs) / 1000;
}

/** How often the sidecar is fetched while it answers, and while it does not. */
const POLL_MS = 4000;
const POLL_MISSING_MS = 15000;

/** Every this many polls the whole file is read again rather than only what was
 *  appended, so a sidecar that was started again (a new epoch) is picked up even
 *  if it has already grown past the old one's length. */
const FULL_READ_EVERY = 15;

/** What a poll has read of the sidecar so far: its header block (the epoch), how
 *  many bytes of it, and any text after the last complete cue. */
interface ReadState {
  header: string;
  bytes: number;
  tail: string;
}

const byteLength = (s: string) => new TextEncoder().encode(s).length;

/** Splits text at its last cue boundary into complete cues and the rest. */
function splitComplete(text: string): [string, string] {
  const at = text.lastIndexOf("\n\n");
  return at < 0 ? ["", text] : [text.slice(0, at + 2), text.slice(at + 2)];
}

/** Reads a whole file into a fresh state. */
function stateOf(body: string): ReadState {
  const norm = body.replace(/\r\n?/g, "\n");
  const end = norm.indexOf("\n\n");
  const header = end < 0 ? norm : norm.slice(0, end);
  const [, tail] = splitComplete(norm);
  return { header, bytes: byteLength(body), tail };
}

/** Polls `url` and keeps a captions TextTrack on the video in step with it.
 *
 *  `anchor` is asked for the clock on every poll; until it has one (no
 *  fragment with a program date-time yet) nothing is placed. Each cue is added
 *  once, keyed by its wall-clock start and text.
 *
 *  After the first read, a poll asks only for the bytes appended since
 *  (HTTP Range, which the playout handler serves), so a twelve-hour show is not
 *  refetched and re-parsed in full by every viewer every four seconds. A 200 is
 *  the whole file again, and a 416 whose size is below what was read means the
 *  file was started again; both start over. */
export function useLiveCaptionTrack(
  video: HTMLVideoElement | null,
  url: string,
  label: string,
  anchor: () => ClockAnchor | null,
): void {
  useEffect(() => {
    if (!video || !url || typeof video.addTextTrack !== "function" || typeof VTTCue === "undefined") return;
    const track = video.addTextTrack("captions", label);
    track.mode = "showing";
    const seen = new Set<string>();
    let timer: number | undefined;
    let stopped = false;
    let read: ReadState | null = null;
    let polls = 0;

    const place = (cues: WallCue[], clock: ClockAnchor) => {
      for (const c of cues) {
        const key = `${c.startMs}|${c.text}`;
        if (seen.has(key)) continue;
        seen.add(key);
        track.addCue(new VTTCue(mediaTimeOf(c.startMs, clock), mediaTimeOf(c.endMs, clock), c.text));
      }
    };

    const poll = async () => {
      let next = POLL_MS;
      try {
        const clock = anchor();
        polls++;
        const partial = clock !== null && read !== null && polls % FULL_READ_EVERY !== 0;
        const headers: Record<string, string> = partial && read ? { Range: `bytes=${read.bytes}-` } : {};
        const res = await fetch(url, { cache: "no-store", credentials: "same-origin", headers });
        if (res.status === 206 && read && clock) {
          const chunk = (await res.text()).replace(/\r\n?/g, "\n");
          read.bytes += byteLength(chunk);
          const [complete, tail] = splitComplete(read.tail + chunk);
          read.tail = tail;
          place(parseLiveVTT(read.header + "\n\n" + complete), clock);
        } else if (res.status === 416) {
          // Nothing appended, or the file is shorter than what was read, which is a new file.
          const total = Number((res.headers?.get?.("Content-Range") ?? "").split("/")[1]);
          if (!read || !Number.isFinite(total) || total < read.bytes) read = null;
        } else if (res.ok) {
          const body = await res.text();
          if (clock) {
            read = stateOf(body);
            place(parseLiveVTT(body), clock);
          }
        } else {
          read = null;
          next = POLL_MISSING_MS;
        }
      } catch {
        next = POLL_MISSING_MS;
      }
      if (!stopped) timer = window.setTimeout(() => void poll(), next);
    };
    void poll();
    return () => {
      stopped = true;
      if (timer) window.clearTimeout(timer);
      track.mode = "disabled";
    };
  }, [video, url, label, anchor]);
}
