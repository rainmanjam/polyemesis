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
  /* Which programme every request below names.
   *
   * NULL UNTIL THE SOURCE LIST LANDS, AND NOTHING WAITS FOR IT. An earlier
   * version held the polls and the socket until this resolved, which looked
   * careful and was not: a /sources that is slow, refused or never answered
   * left the console permanently blank -- no status, no socket, no error --
   * and the e2e suite caught it as "the chrome reports a healthy SRT ingest as
   * down".
   *
   * Null is also the RIGHT answer for a single-source install, which the
   * server accepts and which is the overwhelming majority. So the first poll
   * goes out immediately naming nothing, and the effects below re-run when the
   * programme resolves -- one extra round trip on a multi-source install,
   * against never rendering at all. */
  const [programme, setProgramme] = useState<number | null>(null);
  /* Whether the programme question has been ANSWERED -- including the answer
     "there is none", which is legitimate. See the effect below for why this is
     a gate WITH A DEADLINE rather than either extreme. */
  const [programmeKnown, setProgrammeKnown] = useState(false);

  const wsRef = useRef<WebSocket | null>(null);
  const retryRef = useRef(0);

  const clearLogs = useCallback(() => setLogs([]), []);

  useEffect(() => {
    // NOTHING UNNAMED GOES OUT. The REST polls below wait for the programme to
    // resolve; so does this, for the same reason and one sharper. An unnamed
    // socket is refused with a 400 on a multi-source install, and this socket
    // is where every live value on every page comes from -- so the refusal is
    // not one panel going quiet, it is the whole console. The deadline on the
    // resolving effect is what keeps this from being a wait without end.
    if (!programmeKnown) return;

    // TORN DOWN IS A PROPERTY OF *THIS RUN*, NOT OF THE COMPONENT, and it was
    // a ref reset to false at the top of every run -- which cleared the very
    // flag the PREVIOUS run's still-pending `onclose` was about to read. That
    // handler then nulled `wsRef` out from under the socket that had just
    // replaced it and rescheduled `connect` from a closure still holding the
    // old programme, backing off 1s, 2s, 4s, for the life of the page. A local
    // binding is a CONTROL: a later run cannot reach it to clear it.
    let cancelled = false;
    let reconnectTimer: number | undefined;

    const connect = () => {
      if (cancelled) return;

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
        // A RUN THAT HAS BEEN TORN DOWN SAYS NOTHING. Its socket closing is
        // expected and carries no news about the one that replaced it. Marking
        // the console disconnected here, or clearing `wsRef`, describes the
        // live socket using the fate of a dead one.
        if (cancelled) return;
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
      cancelled = true;
      if (reconnectTimer) window.clearTimeout(reconnectTimer);
      wsRef.current?.close();
    };
    // Re-opened when the programme resolves: the first render has none, and a
    // socket opened without one would be answering for a programme the operator
    // is not looking at for the life of the session.
  }, [programmeKnown, programme]);

  // WHICH PROGRAMME, and BOTH extremes were tried and were wrong.
  //
  // Holding every request until this resolved turned a slow or unanswered
  // /sources into a console that rendered nothing at all -- no status, no
  // socket, no error. Issuing them immediately with no programme named went the
  // other way: the server refuses an unnamed programme-shaped route on a
  // multi-source install, so every load logged 400s for /status, /source and
  // /ws before the second attempt succeeded. The browser suite caught each in
  // turn.
  //
  // So: a gate WITH A DEADLINE. Normally this resolves in one round trip and
  // nothing is ever sent unnamed. If it does not resolve at all, the deadline
  // releases the polls with the answer null -- which the server accepts on a
  // single-source install, and which on a multi-source one produces the same
  // 400 as before but only in the case where the alternative was a blank
  // console forever.
  useEffect(() => {
    let live = true;
    let cleanupTimer: number | undefined;
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
        // worse than one showing the install's only programme.
        if (live) setProgramme(null);
      })
      .finally(() => {
        if (live) setProgrammeKnown(true);
      });

    // THE DEADLINE. Two seconds is longer than any healthy /sources and shorter
    // than an operator's patience with a blank screen. Without it a request
    // that never settles holds every poll and the socket forever.
    cleanupTimer = window.setTimeout(() => {
      if (live) setProgrammeKnown(true);
    }, 2000);

    return () => {
      live = false;
      if (cleanupTimer !== undefined) window.clearTimeout(cleanupTimer);
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
