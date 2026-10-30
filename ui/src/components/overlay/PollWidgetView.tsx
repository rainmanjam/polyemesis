import type { CSSProperties } from "react";
import { useT } from "@/lib/i18n";
import {
  FONT_STACKS,
  pollPercents,
  pollSecondsLeft,
  withOpacity,
  type OverlayStyle,
  type PollDisplay,
} from "@/lib/overlay";

/** The on-stream poll widget (#14): the question, one bar per option with its
 *  share, the total and the time left.
 *
 *  LOGO-FREE BY CONSTRUCTION. Its input is PollDisplay, which has no platform
 *  field, so there is nothing here that could draw a platform's name or mark --
 *  the widget is the same on a Twitch-only scene as on an all-platforms one.
 *  Drawn with the overlay's own style (font, size, colours) so it matches the
 *  chat lines it sits beside. Display-only: no button, no handler. */
export function PollWidgetView({ poll, style, now }: { poll: PollDisplay; style: OverlayStyle; now: number }) {
  const t = useT();
  const font = FONT_STACKS[style.font] ?? FONT_STACKS.sans;
  const pct = pollPercents(poll);
  const best = Math.max(0, ...poll.options.map((o) => o.votes));
  const ended = poll.state === "ended";
  const bg = withOpacity(style.backgroundColor, Math.max(style.backgroundOpacity, 55));

  const box: CSSProperties = {
    fontFamily: font,
    fontSize: `${style.fontSize}px`,
    color: style.textColor,
    lineHeight: 1.25,
    background: bg,
    borderRadius: "0.5em",
    padding: "0.6em 0.8em",
    boxSizing: "border-box",
    maxWidth: "100%",
    display: "flex",
    flexDirection: "column",
    gap: "0.35em",
    textShadow: "0 1px 2px rgba(0,0,0,0.8)",
  };

  let status: string;
  if (ended) status = t("pollWidget.ended", { count: poll.total });
  else if (poll.state === "closing") status = t("pollWidget.closing", { count: poll.total });
  else status = t("pollWidget.open", { count: poll.total, seconds: pollSecondsLeft(poll, now) });

  return (
    <div style={box} data-poll-id={poll.id} data-poll-state={poll.state} role="status" aria-live="polite">
      <div style={{ fontWeight: 800, overflowWrap: "anywhere" }}>{poll.question}</div>
      {poll.options.map((o, i) => {
        const winning = ended && best > 0 && o.votes === best;
        return (
          <div key={`${i}-${o.label}`} data-option={i + 1} style={{ position: "relative", borderRadius: "0.3em", overflow: "hidden" }}>
            <div
              aria-hidden
              style={{
                position: "absolute",
                inset: 0,
                width: `${pct[i]}%`,
                background: winning ? "rgba(250, 204, 21, 0.55)" : "rgba(255, 255, 255, 0.22)",
                transition: "width 400ms ease-out",
              }}
            />
            <div style={{ position: "relative", display: "flex", justifyContent: "space-between", gap: "0.6em", padding: "0.15em 0.4em" }}>
              <span style={{ overflowWrap: "anywhere", fontWeight: winning ? 800 : 600 }}>
                <span style={{ opacity: 0.8 }}>{`${i + 1}. `}</span>
                {o.label}
              </span>
              <span style={{ fontVariantNumeric: "tabular-nums", whiteSpace: "nowrap" }}>{`${pct[i]}%`}</span>
            </div>
          </div>
        );
      })}
      <div style={{ fontSize: "0.7em", opacity: 0.85 }}>{status}</div>
    </div>
  );
}
