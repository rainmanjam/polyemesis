import { useEffect, useRef, useState } from "react";
import { useT } from "@/lib/i18n";
import { ChatOverlayView } from "@/components/overlay/ChatOverlayView";
import { PollWidgetView } from "@/components/overlay/PollWidgetView";
import { AlertView } from "@/components/overlay/AlertView";
import {
  advanceAlerts,
  emptyAlertQueue,
  enqueueAlert,
  GAP_MS,
  nextAlertDelay,
  type AlertQueueState,
  type QueuedAlert,
} from "@/lib/activityQueue";
import { playAlertSound } from "@/lib/alertSound";
import {
  alertStyleOf,
  addLine,
  applyPoll,
  expireLines,
  fetchOverlayConfig,
  overlayLocation,
  pollVisible,
  removeLines,
  runOverlayStream,
  type OverlayConfig,
  type OverlayStyle,
  type ShownLine,
  type ShownPoll,
} from "@/lib/overlay";

/** The dock keeps more scrollback than any overlay draws. */
const DOCK_LINES = 200;

/** How long the dock waits before asking again after the server did not
 *  answer. An OBS dock is rarely reloaded by hand, so it has to recover on its
 *  own once the server is back. */
const RETRY_MS = 2000;

/* app-council A99: each phase that is not "ready" is a different fix, and the
 * dock says which. `missing` is a URL with no key in it (copy it again),
 * `denied` a key the server refused (make a new one), `unreachable` a server
 * that did not answer or answered with an error (check the server; the dock
 * retries). They used to share one sentence about an invalid key. */
type Phase =
  | { kind: "loading" }
  | { kind: "missing" }
  | { kind: "denied" }
  | { kind: "unreachable" }
  | { kind: "ready"; config: OverlayConfig };

const DOCK_COPY = {
  loading: "overlay.dockLoading",
  missing: "overlay.dockMissingKey",
  denied: "overlay.dockDenied",
  unreachable: "overlay.dockNetworkError",
} as const;

/** The chat overlay (/overlay) and dock (/overlay/dock), for OBS, and their
 *  path-key forms (/overlay/k/{key}, /overlay/dock/k/{key}).
 *
 *  Like PublicPlayer it runs with NO SESSION and outside the console's shell:
 *  it never calls /auth/me, never opens the telemetry socket, never reads the
 *  CSRF cookie. Its only credential is the overlay key in the URL fragment
 *  or path, sent to exactly two endpoints in a header.
 *
 *  The page background is TRANSPARENT so OBS composites the lines over the
 *  scene. A failure on the overlay draws NOTHING -- an error banner would be
 *  burned into the broadcast; the dock, which only the operator sees, says
 *  what went wrong. */
export function OverlayPage() {
  // Read once, like the key: an OBS source never navigates. Null cannot happen
  // here (App routes only overlay paths to this page), and reads as no key.
  const [loc] = useState(
    () => overlayLocation(window.location.pathname, window.location.hash) ?? { key: "", dock: false, poll: false },
  );
  if (loc.alerts) return <AlertsOverlay overlayKey={loc.key} />;
  if (loc.poll) return <PollOverlay overlayKey={loc.key} />;
  return <ChatOverlay overlayKey={loc.key} dock={loc.dock} />;
}

function useTransparentPage() {
  // Transparent page, for the life of this page. Set on html and body because
  // the console's stylesheet paints both.
  useEffect(() => {
    const els = [document.documentElement, document.body];
    const prev = els.map((e) => e.style.background);
    for (const e of els) e.style.background = "transparent";
    document.documentElement.classList.add("overlay-page");
    return () => {
      els.forEach((e, i) => (e.style.background = prev[i]));
      document.documentElement.classList.remove("overlay-page");
    };
  }, []);
}

/** The poll widget (/overlay/poll, #14). Same key, same style and same display
 *  stream as the chat overlay, but it draws ONLY poll.display: the chat lines
 *  are not handed to it at all. Nothing is drawn while no poll is running, and
 *  a failure draws nothing -- an error on this page would be on stream. */
function PollOverlay({ overlayKey: key }: Readonly<{ overlayKey: string }>) {
  const [style, setStyle] = useState<OverlayStyle | null>(null);
  const [poll, setPoll] = useState<ShownPoll | null>(null);
  const [now, setNow] = useState(() => Date.now());
  useTransparentPage();

  useEffect(() => {
    if (!key) return;
    const ctl = new AbortController();
    const run = async () => {
      const config = await fetchOverlayConfig(key, ctl.signal);
      if (!config) return;
      setStyle(config.style);
      await runOverlayStream(
        key,
        {
          onPoll: (p) => setPoll((cur) => applyPoll(cur, p, Date.now())),
          onDenied: () => {
            setPoll(null);
            setStyle(null);
          },
          onConnect: async () => {
            const fresh = await fetchOverlayConfig(key, ctl.signal).catch(() => undefined);
            if (fresh) setStyle(fresh.style);
          },
        },
        ctl.signal,
      );
    };
    run().catch(() => {
      if (!ctl.signal.aborted) setStyle(null);
    });
    return () => ctl.abort();
  }, [key]);

  // The countdown and the linger after the end both read the clock.
  useEffect(() => {
    const id = window.setInterval(() => setNow(Date.now()), 500);
    return () => window.clearInterval(id);
  }, []);

  if (!style || !poll || !pollVisible(poll, now)) return null;
  return (
    <div style={{ position: "fixed", inset: 0, background: "transparent", padding: "0.75rem", boxSizing: "border-box" }}>
      <PollWidgetView poll={poll} style={style} now={now} />
    </div>
  );
}

/** The alerts overlay (/overlay/alerts): follows, subs, tips and raids drawn one
 *  at a time. Same key, same style row and same display stream as the chat
 *  overlay, but it draws ONLY activity.display, paced by the queue in
 *  lib/activityQueue. Runs with no session, on a transparent page, and a
 *  failure draws NOTHING -- an error here would be burned into the broadcast.
 *  Unlike the poll widget it retries a failed start: it sits in a scene for
 *  hours, and one blip when OBS launches must not leave it dead. */
function AlertsOverlay({ overlayKey: key }: Readonly<{ overlayKey: string }>) {
  const [style, setStyle] = useState<OverlayStyle | null>(null);
  const [queue, setQueue] = useState<AlertQueueState>(emptyAlertQueue);
  const [shown, setShown] = useState<{ item: QueuedAlert; leaving: boolean } | null>(null);
  useTransparentPage();

  // The stream's handlers are created once; a restyle arrives on a reconnect
  // and is read through this ref.
  const alert = alertStyleOf(style);
  const alertRef = useRef(alert);
  // The ref is set the moment a config arrives, before React renders it:
  // the first frames of the stream can land in between, and would otherwise
  // be filtered by the defaults instead of this overlay's own kinds.
  const take = (s: OverlayStyle) => {
    alertRef.current = alertStyleOf(s);
    setStyle(s);
  };

  useEffect(() => {
    if (!key) return;
    const ctl = new AbortController();
    const run = async () => {
      const config = await fetchOverlayConfig(key, ctl.signal);
      if (!config) return;
      take(config.style);
      let first = true;
      await runOverlayStream(
        key,
        {
          onActivity: (d) => setQueue((q) => enqueueAlert(q, d, alertRef.current, Date.now())),
          onDenied: () => {
            setQueue(emptyAlertQueue());
            setShown(null);
            setStyle(null);
          },
          onConnect: async () => {
            if (first) {
              first = false;
              return;
            }
            const fresh = await fetchOverlayConfig(key, ctl.signal).catch(() => undefined);
            if (fresh) take(fresh.style);
          },
        },
        ctl.signal,
      );
    };
    let retry: number | undefined;
    const attempt = () => {
      run().catch(() => {
        if (ctl.signal.aborted) return;
        retry = window.setTimeout(attempt, RETRY_MS);
      });
    };
    attempt();
    return () => {
      ctl.abort();
      window.clearTimeout(retry);
    };
  }, [key]);

  // The queue's one timer: wakes when the alert on screen is due to go, or the
  // gap before the next has passed.
  const { seconds } = alert;
  useEffect(() => {
    const wait = nextAlertDelay(queue, { seconds }, Date.now());
    if (wait === null) return;
    const id = window.setTimeout(
      () =>
        setQueue((q) => {
          const next = advanceAlerts(q, { seconds }, Date.now());
          // Nothing was due (the wall clock can step back in an OBS source that
          // has run for hours). An unchanged object would make React bail out
          // and this effect never re-arm, leaving the alert up until the next
          // event; a copy re-runs it with a fresh delay.
          return next === q ? { ...q } : next;
        }),
      wait,
    );
    return () => window.clearTimeout(id);
  }, [queue, seconds]);

  // A new alert goes on screen (and plays its sound); when the queue lets go of
  // it, it stays mounted for the exit transition, then leaves.
  const current = queue.current;
  useEffect(() => {
    if (current) {
      setShown({ item: current, leaving: false });
      void playAlertSound(alertRef.current.sound);
      return;
    }
    setShown((s) => (s ? { ...s, leaving: true } : s));
    const id = window.setTimeout(() => setShown(null), GAP_MS);
    return () => window.clearTimeout(id);
  }, [current]);

  if (!style || !shown) return null;
  return (
    <AlertView
      key={shown.item.display.id}
      display={shown.item.display}
      followers={shown.item.followers}
      alert={alert}
      style={style}
      leaving={shown.leaving}
    />
  );
}

function ChatOverlay({ overlayKey: key, dock }: Readonly<{ overlayKey: string; dock: boolean }>) {
  const t = useT();
  // No key in the URL is decided at once, not in an effect.
  const [phase, setPhase] = useState<Phase>(() => (key ? { kind: "loading" } : { kind: "missing" }));
  const [lines, setLines] = useState<ShownLine[]>([]);
  const [connected, setConnected] = useState(false);

  useTransparentPage();

  // maxLines is read through a ref: the stream's handlers are created once,
  // and a restyle that changes it arrives on a later reconnect.
  const maxRef = useRef(DOCK_LINES);

  useEffect(() => {
    if (!key) return;
    const ctl = new AbortController();
    const apply = (config: OverlayConfig) => {
      maxRef.current = dock ? DOCK_LINES : config.style.maxLines;
      setPhase({ kind: "ready", config });
      setLines((ls) => (ls.length > maxRef.current ? ls.slice(ls.length - maxRef.current) : ls));
    };
    // A named function and an explicit .catch rather than an IIFE whose
    // promise is dropped: every rejection -- the config read, the stream --
    // lands in one handler, and a failure on the overlay draws nothing.
    const run = async () => {
      const config = await fetchOverlayConfig(key, ctl.signal);
      if (!config) {
        setPhase({ kind: "denied" });
        return;
      }
      apply(config);
      let first = true;
      await runOverlayStream(
        key,
        {
          onLine: (l) => setLines((ls) => addLine(ls, { ...l, receivedAt: Date.now() }, maxRef.current)),
          onRemove: (ids) => setLines((ls) => removeLines(ls, ids)),
          onDenied: () => {
            setLines([]);
            setPhase({ kind: "denied" });
          },
          onState: setConnected,
          // Saving in Settings ends open streams on purpose; the reconnect is
          // where a new style is picked up. Not on the first connect, which
          // the read above already covers.
          onConnect: async () => {
            if (first) {
              first = false;
              return;
            }
            const fresh = await fetchOverlayConfig(key, ctl.signal).catch(() => undefined);
            if (fresh) apply(fresh);
          },
        },
        ctl.signal,
      );
    };
    // A failure that is not a 401 -- the network, a 5xx, a stream that
    // threw -- is "unreachable", not "denied", and is tried again. A 401
    // resolves inside run() as denied and is not retried: a refused key stays
    // refused until somebody makes a new one.
    let retry: number | undefined;
    const attempt = () => {
      run().catch(() => {
        if (ctl.signal.aborted) return;
        setPhase({ kind: "unreachable" });
        retry = window.setTimeout(attempt, RETRY_MS);
      });
    };
    attempt();
    return () => {
      ctl.abort();
      window.clearTimeout(retry);
    };
  }, [key, dock]);

  // Fade. The dock keeps its scrollback; an overlay drops lines past fade.
  const fade = phase.kind === "ready" && !dock ? phase.config.style.fadeSeconds : 0;
  useEffect(() => {
    if (fade <= 0) return;
    const id = window.setInterval(() => setLines((ls) => expireLines(ls, fade, Date.now())), 500);
    return () => window.clearInterval(id);
  }, [fade]);

  if (phase.kind !== "ready") {
    // The overlay draws nothing in every phase: an error there is on stream.
    if (!dock) return null;
    return (
      <div
        role={phase.kind === "loading" ? "status" : "alert"}
        style={{ padding: "0.75rem", color: "#e5e7eb", background: "#0b0d12", height: "100vh", font: "14px system-ui, sans-serif" }}
      >
        {t(DOCK_COPY[phase.kind])}
      </div>
    );
  }
  return (
    <div style={{ position: "fixed", inset: 0, background: dock ? "#0b0d12" : "transparent" }}>
      {dock && !connected && (
        <div style={{ position: "absolute", top: 4, right: 8, fontSize: 11, color: "#9ca3af", font: "11px system-ui, sans-serif" }}>
          {t("overlay.dockReconnecting")}
        </div>
      )}
      <ChatOverlayView lines={lines} style={phase.config.style} mode={dock ? "dock" : "overlay"} />
    </div>
  );
}
