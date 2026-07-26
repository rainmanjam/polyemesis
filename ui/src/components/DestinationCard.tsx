import { Link } from "react-router-dom";
import {
  AlertTriangle,
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
import { labelForState, toneBadge, toneForState } from "@/lib/signal";
import type { DestStatus } from "@/lib/types";

const PLATFORM_LABEL: Record<string, string> = {
  youtube: "YouTube",
  twitch: "Twitch",
  kick: "Kick",
  custom: "Custom",
};

/** One destination, as shown on the dashboard.
 *
 *  The card answers three questions in reading order: is it up, what audio is
 *  it sending, and how is it performing. The track summary is deliberately
 *  above the stats — which tracks a platform receives is the thing this whole
 *  product exists to make obvious. */
export function DestinationCard({
  dest,
  onStart,
  onStop,
  onRestart,
  onEdit,
  onDelete,
  onRefreshKey,
  busy,
}: {
  dest: DestStatus;
  onStart: () => void;
  onStop: () => void;
  onRestart: () => void;
  onEdit: () => void;
  onDelete: () => void;
  onRefreshKey: () => void;
  busy?: boolean;
}) {
  const state = dest.process?.state;
  const tone = dest.enabled ? toneForState(state) : "idle";
  const running = state === "running";
  const progress = dest.process?.progress;
  const warnings = dest.warnings ?? [];

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
              {dest.enabled ? labelForState(state) : "Stopped"}
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

        {/* --- what audio does this get? the headline fact --- */}
        <Link
          to={`/routing/${dest.id}`}
          className="group flex items-center justify-between gap-2 rounded-md border border-border bg-background px-2 py-1.5 transition-colors hover:border-border-strong"
        >
          <TrackSummary tracks={dest.tracks} />
          <span className="truncate font-mono text-[10px] text-muted-foreground group-hover:text-foreground">
            {dest.summary || "not configured"}
          </span>
        </Link>

        {/* --- performance --- */}
        <div className="grid grid-cols-4 gap-2">
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

        {/* --- the one action that matters --- */}
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
        </div>
      </CardContent>
    </Card>
  );
}
