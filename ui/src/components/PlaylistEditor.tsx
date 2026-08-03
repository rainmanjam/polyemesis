import { useCallback, useEffect, useState } from "react";
import { ArrowDown, ArrowUp, Plus, X } from "lucide-react";
import { api, ApiError } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { toneBadge } from "@/lib/signal";
import type { MediaFile, PlaylistItem, PlaylistItemStatus, PlaylistStatus } from "@/lib/types";

/* ===========================================================================
   The ordered list a failover tier plays when no encoder is delivering.

   Two things this deliberately does that a plain list-of-strings editor would
   not:

   - it shows each item's READINESS, from GET /failover/playlist, not just its
     name. Settings only carries what an operator typed; an upload can be
     deleted, or still transcoding a derivative, entirely independently of the
     playlist that names it, so the settings blob alone cannot say whether an
     item will actually play. A list that only ever showed names would let an
     operator "save" a playlist that fails silently the first time failover
     needs it -- at the exact moment nobody is looking at this page.

   - reordering is a pair of buttons, not drag-and-drop. There is no
     drag-and-drop library in this project yet, and the ordering interaction
     used elsewhere for exactly this reason (MediaUploads' drop zone) already
     doubles a pointer gesture with a keyboard-reachable control because raw
     HTML5 drag is undiscoverable on its own and unreachable by keyboard. Move
     up / move down keeps that property for free.
   =========================================================================== */

interface PlaylistEditorProps {
  items: PlaylistItem[];
  onChange: (items: PlaylistItem[]) => void;
}

const STATE_TONE: Record<PlaylistItemStatus["state"], "live" | "warn" | "down"> = {
  ready: "live",
  transcoding: "warn",
  attention: "down",
};

const STATE_LABEL: Record<PlaylistItemStatus["state"], string> = {
  ready: "Ready",
  transcoding: "Transcoding",
  attention: "Needs attention",
};

export function PlaylistEditor({ items, onChange }: PlaylistEditorProps) {
  const [uploads, setUploads] = useState<MediaFile[]>([]);
  const [status, setStatus] = useState<PlaylistStatus | null>(null);
  const [error, setError] = useState("");
  const [picked, setPicked] = useState("");

  const refresh = useCallback(async () => {
    try {
      const [media, playlist] = await Promise.all([api.media(), api.playlistStatus()]);
      setUploads(media);
      setStatus(playlist);
    } catch (e) {
      setError(e instanceof ApiError ? e.message : String(e));
    }
  }, []);

  useEffect(() => {
    void refresh();
  }, [refresh]);

  // Matched by POSITION, not just name: the status endpoint mirrors the saved
  // settings item-for-item, in the same order (Task 6's contract). Matching
  // by index and then confirming the name keeps a stale response -- one that
  // still reflects the settings from before this draft's last edit -- from
  // being misread as readiness for a different upload that now sits at that
  // index.
  function statusFor(index: number, upload: string): PlaylistItemStatus | undefined {
    // Chained through the index, not just `.items`: status is null whenever
    // the readiness endpoint has not answered yet (including simply not
    // being reachable), and `status?.items[index]` would still evaluate
    // `undefined[index]` in that case and throw -- which crashed the whole
    // Settings page the first time this ran against a backend that had not
    // deployed GET /failover/playlist yet.
    const at = status?.items?.[index];
    return at && at.upload === upload ? at : undefined;
  }

  function add() {
    if (!picked) return;
    onChange([...items, { upload: picked }]);
    setPicked("");
  }

  function remove(index: number) {
    onChange(items.filter((_, i) => i !== index));
  }

  function move(index: number, delta: number) {
    const target = index + delta;
    if (target < 0 || target >= items.length) return;
    const next = items.slice();
    [next[index], next[target]] = [next[target], next[index]];
    onChange(next);
  }

  // An upload already queued is not offered again: the same file twice is a
  // mistake this editor can prevent rather than one an operator finds later
  // by watching it repeat on air.
  const available = uploads.filter((u) => !items.some((it) => it.upload === u.name));

  return (
    <div className="flex flex-col gap-2">
      <Label>Playlist</Label>

      {error && (
        <div className="rounded-md border border-destructive/40 bg-destructive/10 p-2 text-xs text-destructive">
          {error}
        </div>
      )}

      {items.length === 0 ? (
        <div className="text-xs text-muted-foreground">Nothing queued yet.</div>
      ) : (
        <ol className="flex flex-col gap-1" aria-label="Playlist items">
          {items.map((item, index) => {
            const st = statusFor(index, item.upload);
            // Unmatched (not yet saved, or the status endpoint has not caught
            // up) is deliberately its own case rather than folded into
            // "attention": a freshly added row is not broken, it is just
            // unsaved, and calling it a problem would send an operator
            // hunting for a fault that does not exist yet.
            const tone = st ? STATE_TONE[st.state] : undefined;
            const label = st ? STATE_LABEL[st.state] : "Not saved yet";
            return (
              <li
                key={`${item.upload}-${index}`}
                data-testid="playlist-item"
                className="flex items-center justify-between gap-2 rounded-md border p-2"
              >
                <div className="min-w-0">
                  <div className="flex items-center gap-2">
                    <span className="truncate font-mono text-xs">{item.upload}</span>
                    <Badge variant={tone ? toneBadge[tone] : "outline"}>{label}</Badge>
                  </div>
                  {st?.state === "attention" && (
                    <div className="mt-0.5 text-[11px] text-down">
                      {st.detail ?? "This upload can no longer be found."}
                    </div>
                  )}
                </div>
                <div className="flex shrink-0 items-center gap-1">
                  <Button
                    size="icon-sm"
                    variant="ghost"
                    disabled={index === 0}
                    aria-label={`Move ${item.upload} up`}
                    onClick={() => move(index, -1)}
                  >
                    <ArrowUp className="size-3.5" />
                  </Button>
                  <Button
                    size="icon-sm"
                    variant="ghost"
                    disabled={index === items.length - 1}
                    aria-label={`Move ${item.upload} down`}
                    onClick={() => move(index, 1)}
                  >
                    <ArrowDown className="size-3.5" />
                  </Button>
                  <Button
                    size="icon-sm"
                    variant="ghost"
                    aria-label={`Remove ${item.upload}`}
                    onClick={() => remove(index)}
                  >
                    <X className="size-3.5" />
                  </Button>
                </div>
              </li>
            );
          })}
        </ol>
      )}

      <div className="flex items-center gap-2">
        <Select value={picked} onValueChange={setPicked}>
          <SelectTrigger className="flex-1" aria-label="Choose an upload to add">
            <SelectValue
              placeholder={available.length ? "Choose an upload" : "No uploads available"}
            />
          </SelectTrigger>
          <SelectContent>
            {available.map((u) => (
              <SelectItem key={u.name} value={u.name}>
                {u.name}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <Button size="sm" variant="outline" disabled={!picked} onClick={add}>
          <Plus className="size-3.5" /> Add
        </Button>
      </div>
      <span className="text-[10px] text-muted-foreground">
        Uploaded in <em>Media</em>. An item this playlist names that later loses its upload shows
        as needing attention above, rather than failing silently the next time failover reaches
        it.
      </span>
    </div>
  );
}
