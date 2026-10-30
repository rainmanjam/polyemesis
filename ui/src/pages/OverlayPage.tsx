import { useEffect, useRef, useState } from "react";
import { useT } from "@/lib/i18n";
import { ChatOverlayView } from "@/components/overlay/ChatOverlayView";
import {
  addLine,
  expireLines,
  fetchOverlayConfig,
  overlayLocation,
  removeLines,
  runOverlayStream,
  type OverlayConfig,
  type ShownLine,
} from "@/lib/overlay";

/** The dock keeps more scrollback than any overlay draws. */
const DOCK_LINES = 200;

type Phase = { kind: "loading" } | { kind: "denied" } | { kind: "ready"; config: OverlayConfig };

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
  const t = useT();
  // Read once, like the key: an OBS source never navigates. Null cannot happen
  // here (App routes only overlay paths to this page), and reads as no key.
  const [{ key, dock }] = useState(
    () => overlayLocation(window.location.pathname, window.location.hash) ?? { key: "", dock: false },
  );
  // No key in the URL is decided at once, not in an effect.
  const [phase, setPhase] = useState<Phase>(() => (key ? { kind: "loading" } : { kind: "denied" }));
  const [lines, setLines] = useState<ShownLine[]>([]);
  const [connected, setConnected] = useState(false);

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
    run().catch(() => {
      if (!ctl.signal.aborted) setPhase({ kind: "denied" });
    });
    return () => ctl.abort();
  }, [key, dock]);

  // Fade. The dock keeps its scrollback; an overlay drops lines past fade.
  const fade = phase.kind === "ready" && !dock ? phase.config.style.fadeSeconds : 0;
  useEffect(() => {
    if (fade <= 0) return;
    const id = window.setInterval(() => setLines((ls) => expireLines(ls, fade, Date.now())), 500);
    return () => window.clearInterval(id);
  }, [fade]);

  if (phase.kind !== "ready") {
    if (!dock || phase.kind === "loading") return null;
    return (
      <div style={{ padding: "0.75rem", color: "#e5e7eb", background: "#0b0d12", height: "100vh", font: "14px system-ui, sans-serif" }}>
        {t("overlay.dockDenied")}
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
