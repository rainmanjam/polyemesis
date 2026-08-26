import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type ReactNode,
} from "react";
import { api } from "@/lib/api";
import { rememberProgramme, rememberedProgramme, resolveProgramme } from "@/lib/currentProgramme";
import { mergeStatusDestinations } from "@/lib/dashboardFacts";
import { LiveDataContext, type LiveData } from "@/hooks/useLiveData";
import type {
  BitrateSample,
  Levels,
  LogLine,
  SourceInfo,
  Status,
  SystemStats,
  WsEvent,
} from "@/lib/types";

/* ===========================================================================
   The one WebSocket. Everything live on every page comes through here.

   Split from hooks/useLiveData.ts so that file can be hot-swapped -- see the
   note there. This half is the part that owns a connection, which is exactly
   the part that SHOULD tear down and rebuild when it is edited.
   =========================================================================== */

/** How many log lines the client keeps. The server already bounds its own
 *  buffer; this bounds the browser's so a long session cannot grow forever. */
const LOG_LIMIT = 600;

export function LiveDataProvider({ children }: { children: ReactNode }) {
  const [connected, setConnected] = useState(false);
  const [status, setStatus] = useState<Status | null>(null);
  const [source, setSource] = useState<SourceInfo | null>(null);
  const [levels, setLevels] = useState<Levels | null>(null);
  const [system, setSystem] = useState<SystemStats | null>(null);
  const [bitrate, setBitrate] = useState<BitrateSample[]>([]);
  const [logs, setLogs] = useState<LogLine[]>([]);
  const [recordingsRevision, setRecordingsRevision] = useState(0);
  const [frameError, setFrameError] = useState(false);
  /* Which programme every request below names. Null until the source list has
     landed, and null forever on an install with none -- see currentProgramme. */
  const [programme, setProgramme] = useState<number | null>(null);
  const [programmeKnown, setProgrammeKnown] = useState(false);

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
      // The socket names a programme for the same reason every poll does: the
      // server refuses an unnamed one on a multi-source install, and this is
      // the connection every screen renders from -- an unnamed one takes the
      // whole console down rather than one panel.
      const q = programme == null ? "" : `?source=${encodeURIComponent(String(programme))}`;
      const ws = new WebSocket(`${proto}//${location.host}/api/v1/ws${q}`);
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
          // A CONTROL FOR THE OPERATOR, NOT JUST A LOG LINE (#13/#21). This
          // frame's status/level/log update is gone and `onclose` will not
          // fire to say so -- the socket stays open and every other value
          // keeps looking current. Silently returning here is how an
          // operator ends up trusting numbers that stopped updating. The
          // chrome renders this the same way it already renders a dropped
          // socket.
          setFrameError(true);
          return;
        }
        switch (msg.type) {
          case "status":
            // FOLDED, not replaced. The socket is install-wide and every engine
            // publishes onto it, so a plain replace made the destination list --
            // which is the whole install on one page -- flip to whichever
            // programme spoke last. See mergeStatusDestinations.
            setStatus((prev) => {
              const incoming = msg.data as Status;
              return {
                ...incoming,
                destinations: mergeStatusDestinations(
                  prev,
                  incoming,
                ) as Status["destinations"],
              };
            });
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
        // THE FROZEN METERS. Every other value here is a description of
        // configuration or of a process, and holding the last one through a
        // reconnect is right: the destinations did not stop existing because a
        // socket did. `levels` is the exception, because it is a MEASUREMENT OF
        // NOW. Left in place it keeps drawing the last frame before the
        // disconnection — bars bouncing at nothing, indistinguishable from live
        // audio — on the page where somebody is deciding which tracks carry
        // sound. A CONTROL: there is no stale frame left to render as current.
        setLevels(null);
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
    // Re-opened when the programme resolves: the first render has none, and a
    // socket opened without one would be answering for a programme the operator
    // is not looking at for the life of the session.
  }, [programmeKnown, programme]);

  // WHICH PROGRAMME, before anything that needs one.
  //
  // The server refuses a programme-shaped route when an install has two or more
  // sources and the request names none, so a poll issued before this resolves
  // would 400 on exactly the installs this exists for. programmeKnown is what
  // holds the polls until there is an answer -- including the answer "none",
  // which is the setup wizard's state and is legitimate.
  useEffect(() => {
    let live = true;
    api
      .listSources()
      .then((rows) => {
        if (!live) return;
        const ids = rows.map((r) => r.id);
        const picked = resolveProgramme(ids, rememberedProgramme());
        setProgramme(picked);
        rememberProgramme(picked);
      })
      .catch(() => {
        // A console that will not render because it could not list sources is
        // worse than one showing the install's only programme. Null is what the
        // routes accept on a single-source install, which is the overwhelming
        // majority and the case where guessing cannot be wrong.
        if (live) setProgramme(null);
      })
      .finally(() => {
        if (live) setProgrammeKnown(true);
      });
    return () => {
      live = false;
    };
  }, []);

  // Seed from REST so the first paint is populated even if the socket is slow,
  // and so a browser that cannot open a WebSocket still shows something real.
  useEffect(() => {
    if (!programmeKnown) return;
    api.status(programme).then(setStatus).catch(() => {});
    api.source(programme).then(setSource).catch(() => {});
    api
      .stats()
      .then((s) => {
        setSystem(s.system);
        setBitrate(s.bitrate ?? []);
      })
      .catch(() => {});
  }, [programmeKnown, programme]);

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
      frameError,
      clearLogs,
    }),
    [
      connected,
      status,
      source,
      levels,
      system,
      bitrate,
      logs,
      recordingsRevision,
      frameError,
      clearLogs,
    ],
  );

  return <LiveDataContext.Provider value={value}>{children}</LiveDataContext.Provider>;
}
