import { Activity } from "lucide-react";

import type { HealthState } from "@/hooks/useFacebookStreamHealth";
import {
  formatHealthValue,
  healthRows,
  hasMeasurements,
} from "@/lib/stream-health";
import { useT } from "@/lib/i18n";

/* ===========================================================================
   What Facebook's ingest sees of the encoder feed.

   FACEBOOK-ONLY, AND NOT BECAUSE THE OTHERS ARE BEHIND. Facebook publishes
   bitrates and frame rates from its own ingest; Twitch publishes none — the
   word "bitrate" does not appear once in its entire Helix reference. So the
   absence elsewhere is not a gap in polyemesis waiting to be closed by
   symmetry, and a pane that implied it were would have every operator on
   Twitch hunting for a setting that does not exist. The literal sentence at
   the bottom says so on screen, once, in the only place it can be misread.

   GATED BY PLATFORM, NOT DISABLED. DestinationDialog.tsx:1865 sets the
   precedent with the Twitch multitrack switch: offering a control elsewhere
   would be offering a control that cannot do anything. A greyed-out health
   pane on a YouTube card is exactly that, plus an implication that YouTube is
   failing to answer. The caller renders this only for Facebook.

   NOTHING HERE ANIMATES ITS ARRIVAL. docs/DESIGN-SYSTEM.md:104-108, whose
   named example is the viewer count: a number changes because reality changed,
   and easing it in adds latency to the one figure being watched. No count-up,
   no fade, no shimmer, and no skeleton — a skeleton on a two-second poll is a
   pulsing rectangle where a reading belongs, for ever, on a broadcast that has
   no ingest yet.

   NO SIGNAL COLOUR ANYWHERE BELOW. The five saturated tokens mean the state of
   a destination, and every state this pane can be in is a statement about a
   READING rather than about the stream: no measurement yet, no route, a failed
   poll. Painting any of them amber would say a healthy broadcast is degraded.
   The one place colour would be legitimate is a bitrate judged too low, and
   that judgement needs a threshold nobody published — so it is not made here.
   =========================================================================== */

/** The pane. Purely a renderer; the poll is useFacebookStreamHealth, so the
 *  card can read the same state for its confirmation dialog without asking
 *  Facebook for the same numbers twice. */
export function FacebookStreamHealthPanel({ state }: { state: HealthState }) {
  const t = useT();

  return (
    <div className="rounded-md border border-border bg-background px-2 py-1.5">
      <div className="flex items-center gap-1.5">
        <Activity className="h-3 w-3 shrink-0 text-subtle-foreground" />
        <span className="text-[10px] uppercase tracking-wider text-subtle-foreground">
          {t("dash.streamHealth")}
        </span>
      </div>

      <Body state={state} t={t} />

      {/* Literal English, deliberately, and the same call DestinationDialog.tsx
          :1861-1863 makes for the multitrack explanation beside its switch:
          long explanatory prose next to a per-destination control is left
          untranslated rather than pushed through fifteen catalogues, because a
          paragraph that drifts in fourteen languages is worse than one that is
          only in one. Every label, button and toast around it IS translated;
          this is the sentence that mitigates the choice.

          It is here at all because of what this pane does to the OTHER cards.
          An operator with Facebook, Twitch and YouTube side by side sees
          numbers on one and nothing on two, and the available reading is that
          two platforms are lagging or broken. They are not: Twitch publishes no
          bitrate or frame rate anywhere in its API, so there is nothing to show
          and nothing missing. Saying which is the whole job of this sentence. */}
      <p className="mt-1 text-[10px] leading-snug text-muted-foreground">
        Facebook is the only connected platform that publishes ingest numbers.
        Twitch, YouTube and Kick do not report bitrate or frame rate at all, so
        there is nothing to show on their cards — that is the platform, not this
        destination.
      </p>
    </div>
  );
}

function Body({
  state,
  t,
}: {
  state: HealthState;
  t: ReturnType<typeof useT>;
}) {
  switch (state.kind) {
    case "loading":
      // Words, not a skeleton. See the header note.
      return <Line>{t("dash.streamHealthReading")}</Line>;

    case "unsupported":
      // THE ABSENCE IS LABELLED AS THE PLATFORM DECLINING TO PUBLISH, never as
      // a value that failed to arrive. "—" in a slot marked Bitrate reads as a
      // fetch that broke; this sentence cannot.
      return <Line>{t("dash.streamHealthNotPublished")}</Line>;

    case "unavailable":
      return <Line>{t("dash.streamHealthNoRoute")}</Line>;

    case "error":
      // Muted, not amber. Failing to READ the health of a broadcast is not the
      // broadcast being unhealthy, and spending a signal token here would train
      // an operator to read a perfectly good stream as degraded because a poll
      // missed once.
      return <Line>{state.detail}</Line>;

    case "ok":
      if (state.streams.length === 0) {
        // NOT AN ERROR AND NOT A FAILURE. A scheduled broadcast has no ingest
        // yet, an ended one has none any more, and a live one whose encoder went
        // quiet reports nothing until Facebook's own four-second timeout fires.
        // All three are "nothing to describe", and shouting about the pause
        // between clicking Go Live and the first byte is how a pane teaches an
        // operator to ignore it.
        return <Line>{t("dash.noIngestStream")}</Line>;
      }
      return (
        <div className="mt-1 flex flex-col gap-1">
          {state.streams.map((s, i) => (
            <div key={s.id || `stream-${i}`} className="flex flex-col gap-0.5">
              {/* Named only when there is more than one: a broadcast created
                  without a backup ingest has exactly one, and a lone opaque
                  Facebook id above a single row is noise. */}
              {state.streams.length > 1 && (
                <div className="truncate font-mono text-[9px] text-subtle-foreground">
                  {s.id}
                </div>
              )}
              {hasMeasurements(s) ? (
                healthRows(s).map((row) => (
                  <div
                    key={row.name}
                    className="flex items-baseline justify-between gap-2"
                  >
                    {/* Facebook's own field name, verbatim. See
                        lib/stream-health.ts: mapping these onto labels of ours
                        needs spellings nobody published, and a mapping that
                        misses drops a real measurement off the screen. */}
                    <span className="truncate font-mono text-[10px] text-muted-foreground">
                      {row.name}
                    </span>
                    <span className="tnum shrink-0 font-mono text-[11px]">
                      {formatHealthValue(row.value)}
                    </span>
                  </div>
                ))
              ) : (
                // THE WHOLE POINT OF THE PANE. Facebook described this ingest
                // stream and sent no numbers with it. That renders as WORDS. A 0
                // here would tell a streamer with a healthy 6 Mbps feed that
                // their encoder had stopped, and their next move would be to
                // restart something that was working.
                <span className="text-[10px] text-muted-foreground">
                  {t("dash.notReported")}
                </span>
              )}
              {(s.unparsed ?? []).length > 0 && (
                // Recorded rather than dropped: a field polyemesis could not
                // read looks exactly like a field Facebook did not send, and one
                // of those is a bug on this side. Naming it is what lets somebody
                // tell them apart from a screenshot.
                <div className="text-[10px] text-muted-foreground">
                  {t("dash.streamHealthUnreadable", {
                    fields: (s.unparsed ?? []).join(", "),
                  })}
                </div>
              )}
            </div>
          ))}
        </div>
      );
  }
}

function Line({ children }: { children: React.ReactNode }) {
  return <div className="mt-1 text-[10px] text-muted-foreground">{children}</div>;
}
