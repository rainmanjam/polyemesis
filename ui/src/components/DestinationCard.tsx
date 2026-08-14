import { Link } from "react-router";
import {
  AlertTriangle,
  ChevronDown,
  ChevronUp,
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
import { TrackSummary } from "@/components/signature/TrackRows";
import { Stat } from "@/components/signature/Stat";
import { duration, kbps } from "@/lib/format";
import { toneBadge, toneForState } from "@/lib/signal";
import type { DestStatus } from "@/lib/types";
import { useT, useStateLabel } from "@/lib/i18n";

const PLATFORM_LABEL: Record<string, string> = {
  youtube: "YouTube",
  twitch: "Twitch",
  kick: "Kick",
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
  onMoveEarlier,
  onMoveLater,
  canMoveEarlier,
  canMoveLater,
  busy,
}: {
  dest: DestStatus;
  onStart: () => void;
  onStop: () => void;
  onRestart: () => void;
  onEdit: () => void;
  onDelete: () => void;
  onRefreshKey: () => void;
  onMoveEarlier: () => void;
  onMoveLater: () => void;
  canMoveEarlier: boolean;
  canMoveLater: boolean;
  busy?: boolean;
}) {
  const t = useT();
  const stateLabel = useStateLabel();
  const state = dest.process?.state;
  const tone = dest.enabled ? toneForState(state) : "idle";
  const running = state === "running";
  const progress = dest.process?.progress;
  const warnings = dest.warnings ?? [];

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
    <Card className="overflow-hidden">
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
              <TrackSummary tracks={dest.tracks} />
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
          <Stat
            label="Restarts"
            value={dest.process?.restarts ?? 0}
            tone={(dest.process?.restarts ?? 0) > 0 ? "warn" : "muted"}
          />
          <Stat
            label="Dropped"
            value={progress?.dropFrames ?? 0}
            tone={(progress?.dropFrames ?? 0) > 0 ? "warn" : "muted"}
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
        {dest.multitrackNote && (
          <div className="flex items-start gap-1.5 text-[10px] text-muted-foreground">
            <Info className="mt-0.5 h-3 w-3 shrink-0" />
            <span className="break-words">{dest.multitrackNote}</span>
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
      </CardContent>
    </Card>
  );
}
