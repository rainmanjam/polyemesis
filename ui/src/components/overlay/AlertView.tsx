import { useEffect, useState, type CSSProperties } from "react";
import { useT } from "@/lib/i18n";
import { alertHeadline } from "@/lib/alertText";
import { FONT_STACKS, withOpacity, type OverlayStyle } from "@/lib/overlay";
import type { ActivityDisplay, AlertStyle } from "@/lib/types";

/** Whether the viewer asked for less motion. Read once per alert; OBS's
 *  browser source honours the OS setting like any browser. */
function reducedMotion(): boolean {
  return typeof window !== "undefined" && typeof window.matchMedia === "function"
    ? window.matchMedia("(prefers-reduced-motion: reduce)").matches
    : false;
}

/** One alert, drawn in one of three layouts. Display-only.
 *
 *  Everything it draws comes from ActivityDisplay, the server's allowlisted
 *  projection: a name that is already blanked where the gate said so, and a
 *  message only if automod released it. The message is a React text child --
 *  escaped, never HTML -- because it is viewer-written and goes on air.
 *
 *  `leaving` starts the exit transition; the page keeps the element mounted for
 *  the gap between alerts so the transition can play. With reduced motion the
 *  transition is opacity only, and instant. */
export function AlertView({
  display,
  followers,
  alert,
  style,
  leaving = false,
  preview = false,
}: Readonly<{
  display: ActivityDisplay;
  followers?: number;
  alert: AlertStyle;
  style: Pick<OverlayStyle, "font" | "fontSize" | "textColor" | "backgroundColor" | "backgroundOpacity">;
  leaving?: boolean;
  /** Drawn in the editor, where the fixed full-screen placement is wrong. */
  preview?: boolean;
}>) {
  const t = useT();
  const [entered, setEntered] = useState(false);
  const calm = reducedMotion();
  // One frame at the start state, then the end state, so the transition runs.
  useEffect(() => {
    const id = requestAnimationFrame(() => setEntered(true));
    return () => cancelAnimationFrame(id);
  }, []);

  const shown = entered && !leaving;
  const font = FONT_STACKS[style.font] ?? FONT_STACKS.sans;
  const bg = withOpacity(style.backgroundColor, Math.max(style.backgroundOpacity, 70));
  const headline = alertHeadline(t, { display, followers }, alert.showAmount);
  const message = alert.showMessage && display.message ? display.message : "";
  const amount = alert.showAmount && display.amount?.display ? display.amount.display : "";

  const base: CSSProperties = {
    fontFamily: font,
    fontSize: `${style.fontSize}px`,
    color: style.textColor,
    lineHeight: 1.25,
    textShadow: "0 1px 2px rgba(0,0,0,0.8)",
    boxSizing: "border-box",
    maxWidth: "100%",
    opacity: shown ? 1 : 0,
    transform: calm || shown ? "none" : "translateY(0.6em)",
    transition: calm ? "none" : "opacity 350ms ease-out, transform 350ms ease-out",
    overflowWrap: "anywhere",
  };

  let box: CSSProperties;
  if (alert.layout === "card") {
    box = {
      ...base,
      background: bg,
      borderTop: `0.3em solid ${alert.accent}`,
      borderRadius: "0.5em",
      padding: "0.8em 1em",
      width: "22em",
      textAlign: "center",
    };
  } else if (alert.layout === "minimal") {
    box = { ...base, padding: "0.2em 0.4em", fontWeight: 800 };
  } else {
    box = {
      ...base,
      background: bg,
      borderLeft: `0.45em solid ${alert.accent}`,
      borderRadius: "0.4em",
      padding: "0.6em 0.9em",
      minWidth: "16em",
    };
  }

  const wrapper: CSSProperties = preview
    ? { display: "flex", justifyContent: "center", padding: "1em" }
    : { position: "fixed", left: 0, right: 0, bottom: "8%", display: "flex", justifyContent: "center", padding: "0.75rem" };

  return (
    <div style={wrapper}>
      <div style={box} data-alert-id={display.id} data-alert-kind={display.kind} data-alert-layout={alert.layout} role="status" aria-live="polite">
        <div style={{ fontWeight: 800, color: alert.layout === "minimal" ? alert.accent : undefined }}>{headline}</div>
        {amount && display.kind !== "tip" && <div style={{ color: alert.accent, fontWeight: 700 }}>{amount}</div>}
        {message && <div data-alert-message style={{ marginTop: "0.25em", fontWeight: 500 }}>{message}</div>}
      </div>
    </div>
  );
}
