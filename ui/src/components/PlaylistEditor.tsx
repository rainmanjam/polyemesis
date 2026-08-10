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
import { useT, type TranslationKey } from "@/lib/i18n";
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

// Catalogue keys rather than English, for the same reason stateLabelKey lives
// in lib/i18n.ts: the vocabulary and its translations cannot drift apart if the
// map holds the key and the render does the lookup.
const STATE_LABEL: Record<PlaylistItemStatus["state"], TranslationKey> = {
  ready: "playlist.stateReady",
  transcoding: "playlist.stateTranscoding",
  attention: "playlist.stateAttention",
};

// Slower than the live panes (Meters 2s, Clips 3s) and faster than the ones
// watching something that changes by the minute (PublicPlayer 15s). What is
// being waited on here is a transcode under the job governor, so seconds of
// latency cost nothing -- but this runs on the settings page, where a save is
// the thing an operator is actually doing, and readiness must not be the
// reason a PUT queues behind a GET.
const POLL_MS = 5000;

export function PlaylistEditor({ items, onChange }: PlaylistEditorProps) {
  const t = useT();
  const [uploads, setUploads] = useState<MediaFile[]>([]);
  const [status, setStatus] = useState<PlaylistStatus | null>(null);
  const [error, setError] = useState("");
  const [picked, setPicked] = useState("");

  const refresh = useCallback(async () => {
    try {
      const [media, playlist] = await Promise.all([api.media(), api.playlistStatus()]);
      setUploads(media);
      setStatus(playlist);
      // Cleared on success, not only set on failure: readiness is polled
      // below, so a single blip -- a reconcile holding the handler, a restart
      // -- would otherwise leave a stale red banner over a pane that has been
      // answering correctly ever since.
      setError("");
    } catch (e) {
      setError(e instanceof ApiError ? e.message : String(e));
    }
  }, []);

  // Keyed on the NAMES, not the array: SettingsPage passes
  // `draft.failover?.playlist?.items ?? []`, and that `?? []` is a fresh array
  // on every render of the page. An effect depending on the array itself would
  // re-run on every unrelated keystroke in the settings form, and while the
  // playlist is enabled but empty it would re-run forever.
  const itemsKey = items.map((it) => it.upload).join("\n");

  // At mount, and again whenever the draft's items change. An edit is the
  // moment the position-matched status below goes stale -- a row inserted at
  // index 1 shifts every status after it -- so the answer is re-asked rather
  // than left to the poll interval.
  useEffect(() => {
    void refresh();
  }, [refresh, itemsKey]);

  // Settled means every row is matched to a status entry AND that entry is
  // ready. Deliberately not PlaylistStatus.ready, which answers about the
  // SAVED playlist: this pane shows a draft, and a row the server has never
  // been told about is exactly the row a poll is waiting to hear about.
  const settled =
    status !== null &&
    status.items.length === items.length &&
    items.every(
      (it, i) => status.items[i]?.upload === it.upload && status.items[i]?.state === "ready",
    );

  // AND ON A TIMER WHILE ANYTHING IS UNSETTLED, because every state this pane
  // shows except "ready" is one the server leaves on its own: a normalisation
  // job finishes, a save lands and the endpoint starts naming the new items, a
  // re-uploaded file makes an attention row playable. Fetched once at mount,
  // "Transcoding" and "Not saved yet" were permanent until the operator
  // reloaded the page -- on the one pane whose entire purpose is to say why an
  // item will not play. Stops once every item is matched and ready, so the
  // steady state of a healthy playlist is no traffic at all.
  useEffect(() => {
    if (settled) return;
    const t = window.setInterval(() => void refresh(), POLL_MS);
    return () => window.clearInterval(t);
  }, [refresh, settled]);

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
  // An upload the server never inspected is not offered either.
  //
  // The server is the gate — playlistUploadProblems refuses an item naming one,
  // with a sentence saying to upload the file again — and this only avoids
  // offering a choice that will be refused on save. The test is "recorded as
  // uninspected", exactly as the server's is: `unverifiedReason` is empty for an
  // upload with no verdict at all, which is every file stored before verdicts
  // existed, and those are still allowed (the normalise worker re-checks them
  // at the moment of use). No new string is needed for this and none is added:
  // the Library row for such a file carries the explanation, on the page where
  // the operator can act on it.
  const available = uploads.filter(
    (u) =>
      !items.some((it) => it.upload === u.name) &&
      (u.verified || !u.unverifiedReason),
  );

  return (
    <div className="flex flex-col gap-2">
      <Label>{t("playlist.title")}</Label>

      {error && (
        <div className="rounded-md border border-destructive/40 bg-destructive/10 p-2 text-xs text-destructive">
          {error}
        </div>
      )}

      {items.length === 0 ? (
        <div className="text-xs text-muted-foreground">{t("playlist.empty")}</div>
      ) : (
        <ol className="flex flex-col gap-1" aria-label={t("playlist.itemsAria")}>
          {items.map((item, index) => {
            const st = statusFor(index, item.upload);
            // Unmatched is deliberately its own case rather than folded into
            // "attention": a freshly added row is not broken, it is just
            // unsaved, and calling it a problem would send an operator hunting
            // for a fault that does not exist yet. It is a TRANSIENT case and
            // the poll above is what makes that true -- the row carries this
            // label until the save lands and the next refresh finds the
            // endpoint naming it, not until the operator reloads the page.
            const tone = st ? STATE_TONE[st.state] : undefined;
            const label = t(st ? STATE_LABEL[st.state] : "playlist.stateUnsaved");
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
                      {st.detail ?? t("playlist.missingUpload")}
                    </div>
                  )}
                </div>
                <div className="flex shrink-0 items-center gap-1">
                  <Button
                    size="icon-sm"
                    variant="ghost"
                    disabled={index === 0}
                    aria-label={t("playlist.moveUp", { name: item.upload })}
                    onClick={() => move(index, -1)}
                  >
                    <ArrowUp className="size-3.5" />
                  </Button>
                  <Button
                    size="icon-sm"
                    variant="ghost"
                    disabled={index === items.length - 1}
                    aria-label={t("playlist.moveDown", { name: item.upload })}
                    onClick={() => move(index, 1)}
                  >
                    <ArrowDown className="size-3.5" />
                  </Button>
                  <Button
                    size="icon-sm"
                    variant="ghost"
                    aria-label={t("playlist.remove", { name: item.upload })}
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
          <SelectTrigger className="flex-1" aria-label={t("playlist.chooseAria")}>
            <SelectValue
              placeholder={t(available.length ? "playlist.choose" : "playlist.noUploads")}
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
          <Plus className="size-3.5" /> {t("common.add")}
        </Button>
      </div>
      {/* One key, not a sentence split around an <em>. The emphasis on "Media"
          is worth less than a sentence a translator can reorder into their own
          grammar -- and "Media" stays untranslated inside it deliberately,
          because it names the card an operator is being sent to look for, which
          still reads "Media" on screen. */}
      <span className="text-[10px] text-muted-foreground">{t("playlist.hint")}</span>
    </div>
  );
}
