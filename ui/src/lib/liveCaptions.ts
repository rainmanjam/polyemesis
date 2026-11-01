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

/** Polls `url` and keeps a captions TextTrack on the video in step with it.
 *
 *  `anchor` is asked for the clock on every poll; until it has one (no
 *  fragment with a program date-time yet) nothing is placed. Each cue is added
 *  once, keyed by its wall-clock start and text, so a poll that re-reads the
 *  whole growing file adds only what is new. */
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

    const poll = async () => {
      let next = POLL_MS;
      try {
        const res = await fetch(url, { cache: "no-store", credentials: "same-origin" });
        if (res.ok) {
          const clock = anchor();
          if (clock) {
            for (const c of parseLiveVTT(await res.text())) {
              const key = `${c.startMs}|${c.text}`;
              if (seen.has(key)) continue;
              seen.add(key);
              track.addCue(new VTTCue(mediaTimeOf(c.startMs, clock), mediaTimeOf(c.endMs, clock), c.text));
            }
          }
        } else {
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
