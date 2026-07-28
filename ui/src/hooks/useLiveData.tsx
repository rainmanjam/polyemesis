import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
  type ReactNode,
} from "react";
import { api } from "@/lib/api";
import type {
  BitrateSample,
  Levels,
  LogLine,
  SourceInfo,
  SourceTrack,
  Status,
  SystemStats,
  WsEvent,
} from "@/lib/types";

/** How many log lines the client keeps. The server already bounds its own
 *  buffer; this bounds the browser's so a long session cannot grow forever. */
const LOG_LIMIT = 600;

interface LiveData {
  connected: boolean;
  status: Status | null;
  source: SourceInfo | null;
  levels: Levels | null;
  system: SystemStats | null;
  bitrate: BitrateSample[];
  logs: LogLine[];
  /** Bumped whenever the server says the recordings list changed. */
  recordingsRevision: number;
  clearLogs: () => void;
}

const LiveDataContext = createContext<LiveData | null>(null);

export function LiveDataProvider({ children }: { children: ReactNode }) {
  const [connected, setConnected] = useState(false);
  const [status, setStatus] = useState<Status | null>(null);
  const [source, setSource] = useState<SourceInfo | null>(null);
  const [levels, setLevels] = useState<Levels | null>(null);
  const [system, setSystem] = useState<SystemStats | null>(null);
  const [bitrate, setBitrate] = useState<BitrateSample[]>([]);
  const [logs, setLogs] = useState<LogLine[]>([]);
  const [recordingsRevision, setRecordingsRevision] = useState(0);

  const wsRef = useRef<WebSocket | null>(null);
  const retryRef = useRef(0);
  const closedRef = useRef(false);

  const clearLogs = useCallback(() => setLogs([]), []);

  useEffect(() => {
    closedRef.current = false;
    let reconnectTimer: number | undefined;

    const connect = () => {
      if (closedRef.current) return;

      const proto = location.protocol === "https:" ? "wss:" : "ws:";
      const ws = new WebSocket(`${proto}//${location.host}/api/v1/ws`);
      wsRef.current = ws;

      ws.onopen = () => {
        setConnected(true);
        retryRef.current = 0;
      };

      ws.onmessage = (ev) => {
        let msg: WsEvent;
        try {
          msg = JSON.parse(ev.data as string);
        } catch {
          return;
        }
        switch (msg.type) {
          case "status":
            setStatus(msg.data as Status);
            break;
          case "source":
            setSource(msg.data as SourceInfo);
            break;
          case "levels":
            setLevels(msg.data as Levels);
            break;
          case "stats": {
            const d = msg.data as {
              system: SystemStats;
              bitrate: BitrateSample[] | null;
            };
            setSystem(d.system);
            setBitrate(d.bitrate ?? []);
            break;
          }
          case "log":
            setLogs((prev) => {
              const next = [...prev, msg.data as LogLine];
              return next.length > LOG_LIMIT
                ? next.slice(next.length - LOG_LIMIT)
                : next;
            });
            break;
          case "recordings":
            setRecordingsRevision((n) => n + 1);
            break;
        }
      };

      ws.onclose = () => {
        setConnected(false);
        wsRef.current = null;
        if (closedRef.current) return;
        // Back off the same way the server's supervisor does, so a restarting
        // server does not get hammered by every open tab.
        const delay = Math.min(1000 * 2 ** retryRef.current, 15000);
        retryRef.current += 1;
        reconnectTimer = window.setTimeout(connect, delay);
      };

      ws.onerror = () => ws.close();
    };

    connect();

    return () => {
      closedRef.current = true;
      if (reconnectTimer) window.clearTimeout(reconnectTimer);
      wsRef.current?.close();
    };
  }, []);

  // Seed from REST so the first paint is populated even if the socket is slow,
  // and so a browser that cannot open a WebSocket still shows something real.
  useEffect(() => {
    api.status().then(setStatus).catch(() => {});
    api.source().then(setSource).catch(() => {});
    api
      .stats()
      .then((s) => {
        setSystem(s.system);
        setBitrate(s.bitrate ?? []);
      })
      .catch(() => {});
  }, []);

  const value = useMemo<LiveData>(
    () => ({
      connected,
      status,
      source,
      levels,
      system,
      bitrate,
      logs,
      recordingsRevision,
      clearLogs,
    }),
    [connected, status, source, levels, system, bitrate, logs, recordingsRevision, clearLogs],
  );

  return <LiveDataContext.Provider value={value}>{children}</LiveDataContext.Provider>;
}

export function useLiveData(): LiveData {
  const ctx = useContext(LiveDataContext);
  if (!ctx) throw new Error("useLiveData must be used inside <LiveDataProvider>");
  return ctx;
}

/** Whether a broadcast is actually going out right now.
 *
 *  One definition, in one place, because several pages need to ask and the
 *  obvious answer is wrong. `status.ingest.state === "running"` is NOT it: an
 *  SRT listener sits in "running" the whole time it is merely waiting for an
 *  encoder to connect, so a page trusting that would warn about dropping a
 *  stream that does not exist — and an operator who is warned about nothing
 *  stops reading warnings.
 *
 *  The honest signal is bytes arriving: the layout has been probed AND the
 *  recent bitrate is non-zero. Both, because the probe survives the publisher
 *  going away and the bitrate series briefly reads zero between reconnects.
 *
 *  Errs towards saying NOT live. A missed warning costs a stream that was
 *  probably about to be reconfigured anyway; a false one trains the operator to
 *  click through the real thing. */
export function useIngestLive(): boolean {
  const { source, bitrate } = useLiveData();
  if (!source?.probed) return false;
  // The last few seconds only. A stream that stopped a minute ago leaves a
  // long tail of samples behind it.
  const recent = bitrate.slice(-5);
  return recent.some((s) => s.kbps > 0);
}

/** The ingest track layout, falling back to six stereo tracks before the
 *  stream has been probed so the routing editor always has something to draw. */
export function useSourceTracks(): SourceTrack[] {
  const { source } = useLiveData();
  return useMemo<SourceTrack[]>(() => {
    const tracks = source?.tracks;
    if (tracks && tracks.length > 0) return tracks;
    return Array.from({ length: 6 }, (_, i) => ({
      index: i,
      channels: 2,
      codec: "aac",
      layout: "stereo",
    }));
  }, [source]);
}
