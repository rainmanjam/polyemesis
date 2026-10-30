import type { CSSProperties } from "react";
import { useT } from "@/lib/i18n";
import {
  FONT_STACKS,
  PLATFORM_MARK,
  withOpacity,
  type OverlayStyle,
  type ShownLine,
} from "@/lib/overlay";

/** How a set of lines is drawn: on stream ("overlay"), in an OBS dock
 *  ("dock"), or inside Settings as the live preview ("preview", which is the
 *  overlay drawing over a checkerboard so transparency is visible).
 *
 *  ONE component for all three, so the preview cannot drift from what OBS
 *  shows: the editor renders exactly this, with sample lines. Literal colours
 *  rather than the console's design tokens, for the reason PublicPlayer gives:
 *  a browser source has no theme, and its colours are the operator's choice.
 *
 *  Display-only by construction. There is no button, menu or handler here --
 *  an overlay is composited into a broadcast and a dock is for reading. */
export function ChatOverlayView({
  lines,
  style,
  mode,
}: {
  lines: readonly ShownLine[];
  style: OverlayStyle;
  mode: "overlay" | "dock" | "preview";
}) {
  const t = useT();
  const dock = mode === "dock";
  const font = FONT_STACKS[style.font] ?? FONT_STACKS.sans;
  const size = dock ? Math.min(style.fontSize, 16) : style.fontSize;
  const bg = withOpacity(style.backgroundColor, style.backgroundOpacity);
  const preset = dock ? "compact" : style.preset;

  const container: CSSProperties = {
    fontFamily: font,
    fontSize: `${size}px`,
    color: style.textColor,
    lineHeight: 1.3,
    display: "flex",
    flexDirection: "column",
    justifyContent: "flex-end",
    gap: preset === "compact" ? "0.1em" : "0.35em",
    padding: dock ? "0.5rem" : "0.75rem",
    boxSizing: "border-box",
    width: "100%",
    height: "100%",
    overflow: "hidden",
    // The dock is a panel in OBS's own UI, so it gets a solid background to be
    // readable; the overlay is composited and must stay transparent.
    background: dock ? "#0b0d12" : "transparent",
  };
  if (preset === "compact" && !dock) {
    container.background = bg;
    container.borderRadius = "0.4em";
  }

  const lineStyle = (): CSSProperties => {
    switch (preset) {
      case "bubble":
        return {
          background: bg,
          borderRadius: "0.9em",
          padding: "0.35em 0.75em",
          alignSelf: "flex-start",
          maxWidth: "100%",
        };
      case "outline":
        return {
          paintOrder: "stroke fill",
          WebkitTextStroke: `${Math.max(2, Math.round(size / 7))}px #000`,
          fontWeight: 800,
        };
      case "compact":
        return { padding: "0 0.25em" };
      default:
        return {
          textShadow: "0 1px 2px rgba(0,0,0,0.9), 0 0 6px rgba(0,0,0,0.6)",
          background: style.backgroundOpacity > 0 ? bg : undefined,
          padding: style.backgroundOpacity > 0 ? "0.15em 0.5em" : undefined,
          borderRadius: "0.3em",
        };
    }
  };

  return (
    <div
      style={container}
      data-overlay-mode={mode}
      data-overlay-preset={preset}
      role="log"
      aria-live={mode === "preview" ? "off" : "polite"}
      aria-label={t("overlay.ariaLog")}
    >
      {lines.map((l) => {
        const mark = PLATFORM_MARK[l.platform];
        const nameColor = style.authorColors && l.color ? l.color : style.textColor;
        return (
          <div
            key={l.id}
            className="overlay-line"
            data-line-id={l.id}
            data-platform={l.platform}
            style={{ ...lineStyle(), overflowWrap: "anywhere" }}
          >
            {style.showPlatform && mark && (
              <span
                aria-label={l.platform}
                style={{
                  display: "inline-block",
                  fontSize: "0.6em",
                  fontWeight: 700,
                  letterSpacing: "0.04em",
                  padding: "0.1em 0.4em",
                  marginRight: "0.45em",
                  borderRadius: "0.3em",
                  verticalAlign: "0.15em",
                  background: mark.color,
                  color: "#000",
                  WebkitTextStroke: "0",
                  textShadow: "none",
                }}
              >
                {mark.label}
              </span>
            )}
            {style.showBadges && <RoleDots line={l} />}
            <span style={{ color: nameColor, fontWeight: 700 }}>{l.name}</span>
            {l.action ? " " : <span>{": "}</span>}
            <span style={l.action ? { fontStyle: "italic", color: nameColor } : undefined}>{l.text}</span>
          </div>
        );
      })}
    </div>
  );
}

/** The three roles every platform has, as coloured dots. Labelled for a
 *  screen reader in the dock; on stream they are decoration. */
function RoleDots({ line }: { line: ShownLine }) {
  const t = useT();
  const dots: { key: string; color: string; label: string }[] = [];
  if (line.broadcaster) dots.push({ key: "b", color: "#ef4444", label: t("overlay.roleBroadcaster") });
  else if (line.moderator) dots.push({ key: "m", color: "#22c55e", label: t("overlay.roleModerator") });
  if (line.subscriber) dots.push({ key: "s", color: "#a855f7", label: t("overlay.roleSubscriber") });
  if (dots.length === 0) return null;
  return (
    <>
      {dots.map((d) => (
        <span
          key={d.key}
          role="img"
          aria-label={d.label}
          title={d.label}
          style={{
            display: "inline-block",
            width: "0.5em",
            height: "0.5em",
            borderRadius: "999px",
            background: d.color,
            marginRight: "0.3em",
            verticalAlign: "0.12em",
          }}
        />
      ))}
    </>
  );
}
