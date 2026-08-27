import { useState } from "react";
import { Link } from "react-router";
import {
  AlertTriangle,
  ChevronDown,
  ChevronUp,
  CircleStop,
  Info,
  KeyRound,
  MoreVertical,
  Pencil,
  Play,
  RefreshCw,
  Square,
  Trash2,
} from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { StatusDot } from "@/components/signature/StatusDot";
import { ConfirmDestructive } from "@/components/ConfirmDestructive";
import { ExperimentalBadge } from "@/components/Experimental";
import { FacebookStreamHealthPanel } from "@/components/FacebookStreamHealth";
import { useFacebookStreamHealth } from "@/hooks/useFacebookStreamHealth";
import { TrackSummary } from "@/components/signature/TrackRows";
import { Stat } from "@/components/signature/Stat";
import { duration, kbps } from "@/lib/format";
import { toneBadge, toneForState } from "@/lib/signal";
import { reportingStreamCount } from "@/lib/stream-health";
import type { DestStatus } from "@/lib/types";
import { useT, useStateLabel } from "@/lib/i18n";

const PLATFORM_LABEL: Record<string, string> = {
  youtube: "YouTube",
  twitch: "Twitch",
  kick: "Kick",
  // Facebook was missing, so a Facebook destination's subtitle read a bare
  // lowercase "facebook" beside four properly capitalised siblings — the
  // fallback firing where a label belongs, exactly the failure the i18n drift
  // guard exists to catch one layer up.
  facebook: "Facebook",
  trovo: "Trovo",
  vimeo: "Vimeo",
  custom: "Custom",
};

/** One destination, as shown on the dashboard.
 *
 *  The card answers three questions in reading order: is it up, what is it
 *  sending, and how is it performing. "What is it sending" is one block with
 *  two rows rather than two blocks, because video and audio are a pair — a
 *  platform that will not take the source video is exactly the platform whose
 *  audio mix you are also about to check. That pairing is deliberately above
 *  the stats; it is the thing this product exists to make obvious. */
export function DestinationCard({
  dest,
  onStart,
  onStop,
  onRestart,
  onEdit,
  onDelete,
  onRefreshKey,
  onEndBroadcast,
  onMoveEarlier,
  onMoveLater,
  canMoveEarlier,
  canMoveLater,
  busy,
  domId,
  trackLabels,
}: {
  dest: DestStatus;
  onStart: () => void;
  onStop: () => void;
  onRestart: () => void;
  onEdit: () => void;
  onDelete: () => void;
  onRefreshKey: () => void;
  /** Ends the platform-side broadcast. Optional because only Facebook has one
   *  to end, and a card for a destination that cannot do this is given no
   *  handler rather than a handler it must remember not to call. */
  onEndBroadcast?: () => void | Promise<void>;
  onMoveEarlier: () => void;
  onMoveLater: () => void;
  canMoveEarlier: boolean;
  canMoveLater: boolean;
  busy?: boolean;
  /** An anchor for the dashboard's attention list to send the operator to.
   *
   *  Optional because a card that nothing links to should not be a tab stop:
   *  `tabIndex={-1}` below is what makes focus() land here, and adding it to
   *  every card unconditionally would put a stop with no controls on it between
   *  each pair of real ones. */
  domId?: string;
  /** What the operator calls each ingest track, indexed by track index, for
   *  the tooltips on the audio chips.
   *
   *  Passed in rather than read from a hook here, and that is not ceremony:
   *  useLiveData THROWS outside its provider, and this component is rendered
   *  directly in its own tests. A hook would have made naming a track cost a
   *  provider in every test that touches a destination card.
   *
   *  Optional, and null is a meaningful value: it means "no names are known
   *  for THIS destination's programme", which is the honest answer on a
   *  multi-programme install where the source snapshot describes a different
   *  one. See lib/trackLabels.ts. */
  trackLabels?: (string | undefined)[] | null;
}) {
  const t = useT();
  const stateLabel = useStateLabel();
  const state = dest.process?.state;
  const tone = dest.enabled ? toneForState(state) : "idle";
  const running = state === "running";
  const progress = dest.process?.progress;
  const warnings = dest.warnings ?? [];

  /* --- Facebook broadcast lifecycle -------------------------------------
   *
   *  GATED ON PLATFORM SELECTION, NOT DISABLED, which is the rule this file's
   *  kebab already follows one item up: "Refresh stream key" is HIDDEN for
   *  custom and kick rather than greyed out, because a control that cannot do
   *  anything is not made better by being visible and dead. Same here, plus
   *  the id: without a live video there is no broadcast to end and nothing to
   *  read health from, so the pair appears exactly when both are true. */
  const isFacebook = dest.platform === "facebook";
  const liveVideoId = dest.facebookBroadcastId;
  const hasFacebookBroadcast = isFacebook && !!liveVideoId;
  const canEndBroadcast = hasFacebookBroadcast && !!onEndBroadcast;

  const [endOpen, setEndOpen] = useState(false);

  // One poll per Facebook card with a broadcast, and the card owns it so the
  // pane and the confirmation read the same reading rather than asking twice.
  const health = useFacebookStreamHealth(dest.id, hasFacebookBroadcast);

  /* The consequences panel, and every row in it is a number that was actually
   * measured. THIS IS RULE ONE APPLIED TO A DIALOG: the pane below refuses to
   * render an unreported bitrate as 0, and a confirmation that says
   * "Minutes on air 0" over a stream that has been up for forty seconds is the
   * same false report with a bigger button under it.
   *
   * So each row is conditional on knowing its number, and a dialog with no
   * rows draws no panel at all — ConfirmDestructive already guards on length. */
  const uptimeSec = running ? (dest.process?.uptimeSec ?? 0) : 0;
  const streamCount =
    health.kind === "ok" ? reportingStreamCount(health.streams) : 0;
  const consequences: { label: string; count: number }[] = [];
  // Below a minute this row is ABSENT rather than zero. `Math.floor(40/60)` is
  // 0, and 0 in a panel headed "What this ends" says the broadcast has not
  // started — which is exactly wrong at the moment somebody is ending it.
  if (uptimeSec >= 60) {
    consequences.push({
      label: t("dash.minutesOnAir"),
      count: Math.floor(uptimeSec / 60),
    });
  }
  // Only when Facebook is describing at least one. A "0" here reads as "this
  // does nothing", and that is false: the broadcast node still goes to VOD
  // whether or not an ingest stream is currently attached to it.
  if (streamCount > 0) {
    consequences.push({ label: t("dash.ingestStreamsEnding"), count: streamCount });
  }

  // No rendition id is passthrough. The id is checked rather than the name so a
  // status snapshot that arrives without one cannot make a re-encoded
  // destination read as "source, copied", which is the one thing here that
  // would send someone looking for a fault in the wrong place.
  const video = dest.renditionId
    ? dest.renditionName || `rendition ${dest.renditionId}`
    // One name for the free state, everywhere. It read four different ways
    // across the UI — "passthrough · copy" here, "Passthrough — source, copied"
    // in the dialog, "Ingest (passthrough)" in playout — for one concept, which
    // is how Wowza ended up shipping "Encode", "Preset" and "Stream Name Group"
    // for a single thing.
    : "source, copied";

  return (
    <Card
      id={domId}
      // -1, not 0: reachable by the summary above and by nothing else.
      // `focus:` rather than `focus-visible:` on purpose — this focus arrives
      // from a mouse click on another element, which is exactly the case
      // focus-visible suppresses, and a jump that lands with no ring is a jump
      // that does not say where it landed.
      tabIndex={domId ? -1 : undefined}
      className="overflow-hidden focus:outline-none focus:ring-2 focus:ring-ring"
    >
      <CardContent className="flex flex-col gap-2.5 p-3">
        {/* --- identity + state --- */}
        <div className="flex items-start justify-between gap-2">
          <div className="flex min-w-0 items-center gap-2">
            <StatusDot tone={tone} />
            <div className="min-w-0">
              <div className="truncate text-[13px] font-semibold leading-tight">{dest.name}</div>
              <div className="font-mono text-[10px] leading-tight text-muted-foreground">
                {PLATFORM_LABEL[dest.platform] ?? dest.platform} · {dest.kind.toUpperCase()}
              </div>
            </div>
          </div>

          <div className="flex shrink-0 items-center gap-1">
            <Badge variant={dest.enabled ? toneBadge[tone] : "outline"}>
              {dest.enabled ? stateLabel(state) : t("state.stopped")}
            </Badge>
            <DropdownMenu>
              <DropdownMenuTrigger asChild>
                <Button variant="ghost" size="icon-sm" aria-label={`Actions for ${dest.name}`}>
                  <MoreVertical />
                </Button>
              </DropdownMenuTrigger>
              <DropdownMenuContent align="end">
                <DropdownMenuItem onSelect={onEdit}>
                  <Pencil /> Edit
                </DropdownMenuItem>
                <DropdownMenuItem asChild>
                  <Link to={`/routing/${dest.id}`}>
                    <KeyRound /> Edit routing
                  </Link>
                </DropdownMenuItem>
                {dest.enabled && (
                  <DropdownMenuItem onSelect={onRestart}>
                    <RefreshCw /> Restart
                  </DropdownMenuItem>
                )}
                {dest.platform !== "custom" && dest.platform !== "kick" && (
                  <DropdownMenuItem onSelect={onRefreshKey}>
                    <RefreshCw /> Refresh stream key
                  </DropdownMenuItem>
                )}
                {/* Below the separator, with Delete, because this reaches the
                    platform and the rest of this menu does not. Everything
                    above changes what polyemesis sends; this ends a public
                    broadcast that other people are watching. */}
                {canEndBroadcast && (
                  <>
                    <DropdownMenuSeparator />
                    <DropdownMenuItem destructive onSelect={() => setEndOpen(true)}>
                      <CircleStop /> {t("dash.endBroadcast")}
                    </DropdownMenuItem>
                  </>
                )}
                <DropdownMenuSeparator />
                <DropdownMenuItem destructive onSelect={onDelete}>
                  <Trash2 /> Delete
                </DropdownMenuItem>
              </DropdownMenuContent>
            </DropdownMenu>
          </div>
        </div>

        {/* --- what does this platform get? the headline fact --- */}
        <div className="rounded-md border border-border bg-background">
          {/* Both rows link to the page that edits them, so the fact you are
              reading is the thing you click. */}
          <Link
            to="/renditions"
            className="group flex items-center justify-between gap-2 px-2 py-1.5 transition-colors hover:bg-muted"
          >
            <span className="font-mono text-[10px] uppercase tracking-wide text-subtle-foreground">
              video
            </span>
            <span className="truncate font-mono text-[10px] text-muted-foreground group-hover:text-foreground">
              {video}
            </span>
          </Link>
          <Link
            to={`/routing/${dest.id}`}
            className="group flex items-center justify-between gap-2 border-t border-border px-2 py-1.5 transition-colors hover:bg-muted"
          >
            <div className="flex min-w-0 items-center gap-2">
              <span className="font-mono text-[10px] uppercase tracking-wide text-subtle-foreground">
                audio
              </span>
              <TrackSummary tracks={dest.tracks} labels={trackLabels} />
            </div>
            <span className="truncate font-mono text-[10px] text-muted-foreground group-hover:text-foreground">
              {dest.summary || "not configured"}
            </span>
          </Link>
        </div>

        {/* --- performance --- */}
        <div className="grid grid-cols-5 gap-2">
          <Stat
            label="Bitrate"
            value={running ? kbps(progress?.bitrateKbps ?? 0) : "—"}
            tone={running ? "default" : "muted"}
          />
          <Stat
            label="Uptime"
            value={running ? duration(dest.process?.uptimeSec ?? 0) : "—"}
            tone={running ? "default" : "muted"}
          />
          {/* A DASH IS NOT A ZERO, and these two were the only cells in this
              grid not obeying the rule its own comment states four lines down.

              Restarts: with no process there is no restart count. `?? 0` said
              "this destination has restarted zero times" about a destination
              nobody has reported on — the same reassuring number a healthy
              never-restarted run produces, so the one reading that would send
              somebody looking is indistinguishable from the one that says
              nothing at all.

              Dropped: this is CUMULATIVE OVER A RUN, so a finished run's total
              stays on screen after the encoder is gone and reads as a live
              count. "Dropped 0" beside a stopped destination is the worse half:
              it claims a clean run for a stream nothing is measuring. Gated on
              `running` like Bitrate, Uptime and Speed beside it, so every cell
              in the row now answers the same question the same way. */}
          <Stat
            label="Restarts"
            value={dest.process ? (dest.process.restarts ?? 0) : "—"}
            tone={dest.process && (dest.process.restarts ?? 0) > 0 ? "warn" : "muted"}
          />
          <Stat
            label="Dropped"
            value={running && progress ? (progress.dropFrames ?? 0) : "—"}
            tone={running && (progress?.dropFrames ?? 0) > 0 ? "warn" : "muted"}
          />
          {/* Almost every destination here is a passthrough, so there is barely
              any encoding work to be slow at — a speed under 1 means FFmpeg is
              blocking on the write to the platform. Zero is "no progress block
              yet" rather than stopped, which is why it renders as a dash and
              not as a failure. */}
          <Stat
            label="Speed"
            value={running && (progress?.speed ?? 0) > 0 ? `${(progress?.speed ?? 0).toFixed(2)}x` : "—"}
            tone={
              !running || (progress?.speed ?? 0) === 0
                ? "muted"
                : (progress?.speed ?? 0) < 0.95
                  ? "warn"
                  : "default"
            }
          />
        </div>

        {/* --- problems --- */}
        {dest.error && (
          <div className="flex items-start gap-1.5 rounded border border-down/30 bg-down-dim px-2 py-1 text-[10px] text-down">
            <AlertTriangle className="mt-0.5 h-3 w-3 shrink-0" />
            <span className="break-words">{dest.error}</span>
          </div>
        )}
        {!dest.error && dest.process?.lastError && state !== "running" && (
          <div className="flex items-start gap-1.5 rounded border border-warn/30 bg-warn-dim px-2 py-1 text-[10px] text-warn">
            <AlertTriangle className="mt-0.5 h-3 w-3 shrink-0" />
            <span className="line-clamp-2 break-words">{dest.process.lastError}</span>
          </div>
        )}
        {/* THE STOP THAT WAS NOT CONFIRMED.
            A WARNING, not a control: by the time this arrives the SIGKILL has
            been issued and not waited for, so there is nothing left to prevent
            — only something to say. And it is the one thing `process.state`
            cannot say, because it reads "stopped" on both of Stop's arms
            (engine/status.go:41-50). Without this, a stop that left a child
            possibly still publishing to the platform drew byte-for-byte
            identically to a clean one, and the operator's next move — starting
            it again — is the move that produces two encoders on one stream key.
            Amber rather than red for the same reason the field is not `error`:
            the row is fine and the stop was issued. It is the observation that
            is missing, not the destination. */}
        {dest.stopWarning && (
          <div className="flex items-start gap-1.5 rounded border border-warn/30 bg-warn-dim px-2 py-1 text-[10px] text-warn">
            <AlertTriangle className="mt-0.5 h-3 w-3 shrink-0" />
            <span className="break-words">{dest.stopWarning}</span>
          </div>
        )}
        {warnings.map((w) => (
          <div key={w} className="flex items-start gap-1.5 text-[10px] text-warn">
            <AlertTriangle className="mt-0.5 h-3 w-3 shrink-0" />
            <span>{w}</span>
          </div>
        ))}

        {/* What Enhanced Broadcasting decided for THIS run.
            MUTED, NOT AMBER, and never behind an alert triangle. Twitch grants
            it only to a client with a supported GPU and a rented server has
            none, so on most installs the fallback happens every time, for ever
            — an operator who is shown a warning every broadcast learns to
            ignore warnings. The toggle in the dialog promises this sentence;
            this is where it is kept. */}
        {/* The badge, and NOT the full <Experimental> block. This is the one
            place the operator is sent for the go-live decision — the dialog
            toggle and the routing editor both point here — so it has to carry
            the label, or the caveat stops exactly where the answer starts. But
            it renders on every broadcast, so a paragraph here would be the
            "warning every time" this note's own comment exists to avoid. The
            claim itself is stated in full where the feature is switched on. */}
        {dest.multitrackNote && (
          <div className="flex items-start gap-1.5 text-[10px] text-muted-foreground">
            <Info className="mt-0.5 h-3 w-3 shrink-0" />
            <span className="break-words">
              <ExperimentalBadge className="mr-1 align-[1px]" />
              {dest.multitrackNote}
            </span>
          </div>
        )}
        {/* Advisory only. Twitch agreed and said something about it; the
            destination IS publishing. Rendering these as faults would make an
            optional annotation look like the reason a stream is not working. */}
        {(dest.multitrackDivergences ?? []).map((d) => (
          <div
            key={`${d.field}:${d.detail}`}
            className="flex items-start gap-1.5 pl-4 text-[10px] text-muted-foreground"
          >
            <span className="break-words">{d.detail}</span>
          </div>
        ))}
        {/* One track short of what was configured, and the destination is
            otherwise fine — so this is stated rather than alarmed about. It
            exists because the alternative was silence: two audio tracks pushed
            at a one-track ingest with nothing anywhere saying so. */}
        {dest.vodAudioDropped && (
          <div className="flex items-start gap-1.5 text-[10px] text-muted-foreground">
            <Info className="mt-0.5 h-3 w-3 shrink-0" />
            <span className="break-words">{dest.vodAudioDropped}</span>
          </div>
        )}

        {/* A backup nobody can see is worse than none: the operator believes
            they have redundancy. So its state is rendered beside the primary's
            whether it is healthy or not. */}
        {dest.backupProcess && (
          <div className="flex items-center gap-1.5 text-[10px] text-muted-foreground">
            <span>backup feed:</span>
            {/* Through toneForState, like every other state in the app. The
                two hand-written classes this replaced were `text-ok` and
                `text-warn`: there is no --color-ok token, so Tailwind never
                emitted the first one and a *healthy* backup rendered in plain
                body text while an unhealthy one was amber — the two states
                were told apart only by the presence of colour, and the good
                one was the invisible half. The raw `state` string was also
                escaping the catalogue untranslated. */}
            <StatusDot tone={toneForState(dest.backupProcess.state)} size="sm" />
            <span>{stateLabel(dest.backupProcess.state)}</span>
          </div>
        )}
        {dest.backupError && (
          <div className="flex items-start gap-1.5 text-[10px] text-warn">
            <AlertTriangle className="mt-0.5 h-3 w-3 shrink-0" />
            <span>{dest.backupError}</span>
          </div>
        )}

        {/* A public event page was created on the operator's behalf; giving
            them no way to reach it is half a feature. It also makes a dead
            broadcast legible — when this link 404s, they can see for
            themselves what happened. */}
        {dest.facebookBroadcastId && (
          <a
            href={`https://facebook.com/${dest.facebookBroadcastId}`}
            target="_blank"
            rel="noreferrer"
            className="text-[10px] text-muted-foreground underline underline-offset-2 hover:text-foreground"
          >
            Scheduled Facebook broadcast
          </a>
        )}

        {/* WHERE THE FOOTAGE ACTUALLY WENT.
            A file destination never overwrites an earlier take, so a respawn
            whose configured name already holds bytes is given a timestamped
            sibling. That is correct and is not an error -- which is why this is
            muted text beside the artefact link above rather than a warning tone.
            What it fixes is silence: before it, the name the operator configured
            held a container header and no video, and the only trace of the real
            file was a restart counter moving from 0 to 1.

            Literal English, following the artefact link directly above it. The
            keyed strings in this card are labels and buttons; these two are
            inline artefact names, and the catalogue has fifteen locales that a
            machine translation of this sentence would not serve well. */}
        {dest.rolledOverTo && (
          <p className="text-[10px] text-muted-foreground">
            Recording continued in{" "}
            <span className="font-mono">{dest.rolledOverTo.split("/").pop()}</span>
            {" "}— the configured file already held footage and is never overwritten.
          </p>
        )}


        {/* Facebook's own view of the ingest, under the link to the artefact it
            describes. Rendered only for Facebook, for the reason set out at the
            top of that file: Twitch publishes no bitrate or frame rate at all,
            so a pane on its card would be a control that cannot do anything and
            an implication that Twitch is failing to answer. */}
        {hasFacebookBroadcast && <FacebookStreamHealthPanel state={health} />}

        {/* --- the one action that matters, plus display order --- */}
        <div className="flex gap-1.5">
          {dest.enabled ? (
            <Button variant="outline" size="sm" className="flex-1" onClick={onStop} disabled={busy}>
              <Square /> Stop
            </Button>
          ) : (
            <Button variant="live" size="sm" className="flex-1" onClick={onStart} disabled={busy}>
              <Play /> Start
            </Button>
          )}
          {/* Buttons rather than drag: they work from the keyboard, and moving
              a card is only ever a rearrangement of the dashboard — nothing
              here restarts a stream. */}
          <Button
            variant="ghost"
            size="icon-sm"
            onClick={onMoveEarlier}
            disabled={!canMoveEarlier}
            aria-label={`Move ${dest.name} earlier`}
          >
            <ChevronUp />
          </Button>
          <Button
            variant="ghost"
            size="icon-sm"
            onClick={onMoveLater}
            disabled={!canMoveLater}
            aria-label={`Move ${dest.name} later`}
          >
            <ChevronDown />
          </Button>
        </div>

        {/* =================================================================
            ENDING A BROADCAST: WHY THIS DOES NOT SET requireTyping.

            requireTyping is reserved for things that do not come back, and
            nothing in this tree has ever set it for a non-deletion. Ending a
            live broadcast has an obvious claim on being the first — it is
            public, it is irreversible in the sense that the moment does not
            return, and it disconnects everyone currently watching. The claim
            was taken seriously and rejected, for two reasons that both point
            the same way.

            ONE. ON FACEBOOK, ENDING IS A TRANSITION AND NOT A DELETION. Meta's
            Broadcasting guide, verbatim: "This ends your broadcast and saves
            it as a video on demand (VOD)." Nothing is destroyed. The link four
            blocks above this one keeps resolving, to the same id, now playing
            the recording. Every other requireTyping call site in this tree
            takes something away that no longer exists afterwards — a
            recording, an upload, a source, a clip, a settings reset. This one
            leaves the artefact on the platform and changes its state. Typing
            a destination's name to confirm a state change would say those two
            things are the same weight, and the next time an operator meets a
            real one they will type through it.

            TWO, AND DECISIVE. THE SAME OUTCOME IS ALREADY ONE UNCONFIRMED
            CLICK AWAY, ON THIS CARD. Facebook documents two ways to end a
            broadcast, and the other one is the absence of bytes: "To end a
            broadcast, stop streaming live video data from your encoder to the
            stream URL or send a request to POST
            /<LIVE_VIDEO_ID>?end_live_video=true." The Stop button directly
            above ends this destination's encoder, and after Facebook's own
            four-second timeout the show is over exactly as much as it is via
            this menu item. Putting a typed challenge on the API path while the
            identical consequence sits behind a plain button in the same
            component does not add a control — it adds a ritual, and a ritual
            that guards nothing is precisely how ConfirmDestructive's own note
            says a control decays into a reflex.

            SO THE FRICTION IS THE DIALOG AND THE NUMBERS, WHICH IS THE PART
            THAT ACTUALLY HELPS. What goes wrong here is not "I did not mean to
            end a broadcast", it is "I ended the WRONG destination's broadcast"
            — and the fix for that is showing which one, with how long it has
            been on air and how many ingest streams stop, on the card the
            operator clicked. Confirming a number is a decision. Every number
            in that panel is measured or absent; none is a zero standing in for
            something we did not ask.

            IF THIS EVER STOPS BEING TRUE, REVISIT IT. The argument leans on
            two facts, both of them Facebook's: that the end produces a VOD
            rather than a deletion, and that stopping the encoder ends the show
            anyway. A platform where ending destroys the artefact, or where
            only the API can do it, is a different decision and should get one.
            ================================================================= */}
        {canEndBroadcast && (
          <ConfirmDestructive
            open={endOpen}
            onOpenChange={setEndOpen}
            subject={dest.name}
            title={t("dash.endBroadcastTitle", { name: dest.name })}
            description={t("dash.endBroadcastDescription")}
            consequences={consequences}
            // Not "This also removes": ending removes nothing. See the prop's
            // own comment in ConfirmDestructive.tsx.
            consequencesLabel={t("dash.endBroadcastEnds")}
            confirmLabel={t("dash.endBroadcast")}
            onConfirm={async () => {
              await onEndBroadcast?.();
            }}
          />
        )}
      </CardContent>
    </Card>
  );
}
