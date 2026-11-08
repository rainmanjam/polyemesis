import { useCallback, useEffect, useState } from "react";
import { toast } from "sonner";
import { Loader2, Plus, X } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { api } from "@/lib/api";
import { WIRE } from "@/lib/contract/wire";
import { useT } from "@/lib/i18n";
import { refreshViewerTags } from "@/hooks/useViewerTags";
import type { ChatPlatform } from "@/lib/types";

/* The operator's private note about this viewer, and a few tags.

   Personal data about someone who chatted: it is stored on this server only,
   readable by admins only, and never shown on stream. Everything is rendered as
   TEXT (React escapes it; there is no innerHTML anywhere near it). Nothing is
   saved until Save is pressed. */

const LIMITS = WIRE.viewerNotes;

type Load = "loading" | "ready" | "failed";

export function ChatUserNotes({
  platform,
  account,
  authorId,
}: Readonly<{ platform: ChatPlatform; account?: string; authorId: string }>) {
  const t = useT();
  const [load, setLoad] = useState<Load>("loading");
  const [note, setNote] = useState("");
  const [tags, setTags] = useState<string[]>([]);
  const [saved, setSaved] = useState({ note: "", tags: [] as string[] });
  const [draftTag, setDraftTag] = useState("");
  const [busy, setBusy] = useState(false);
  const [confirmDelete, setConfirmDelete] = useState(false);

  const key = { platform, account, authorId };

  const read = useCallback(() => {
    if (!authorId) return;
    setLoad("loading");
    api
      .viewerNote({ platform, account, authorId })
      .then((n) => {
        setNote(n.note);
        setTags(n.tags ?? []);
        setSaved({ note: n.note, tags: n.tags ?? [] });
        setLoad("ready");
      })
      .catch(() => setLoad("failed"));
  }, [platform, account, authorId]);

  useEffect(() => {
    setConfirmDelete(false);
    setDraftTag("");
    read();
  }, [read]);

  const dirty = note !== saved.note || tags.join("\u0000") !== saved.tags.join("\u0000");
  const hasRecord = saved.note !== "" || saved.tags.length > 0;

  const addTag = (raw: string) => {
    const tag = raw.trim();
    if (!tag) return;
    if (tags.some((x) => x.toLowerCase() === tag.toLowerCase())) return;
    if (tags.length >= LIMITS.maxTags) {
      toast.error(t("viewerCard.tagLimit", { max: LIMITS.maxTags }));
      return;
    }
    setTags([...tags, tag.slice(0, LIMITS.maxTagLen)]);
    setDraftTag("");
  };

  const save = async () => {
    setBusy(true);
    try {
      const n = await api.saveViewerNote(key, { note: note.trim(), tags });
      setNote(n.note);
      setTags(n.tags ?? []);
      setSaved({ note: n.note, tags: n.tags ?? [] });
      refreshViewerTags();
      toast.success(t("viewerCard.noteSaved"));
    } catch (err) {
      toast.error(err instanceof Error && err.message ? err.message : t("viewerCard.noteSaveFailed"));
    } finally {
      setBusy(false);
    }
  };

  const remove = async () => {
    setBusy(true);
    try {
      await api.deleteViewerNote(key);
      setNote("");
      setTags([]);
      setSaved({ note: "", tags: [] });
      setConfirmDelete(false);
      refreshViewerTags();
      toast.success(t("viewerCard.noteDeleted"));
    } catch (err) {
      toast.error(err instanceof Error && err.message ? err.message : t("viewerCard.noteSaveFailed"));
    } finally {
      setBusy(false);
    }
  };

  if (!authorId) return null;

  if (load === "failed") {
    return (
      <div className="flex items-center gap-2" data-testid="viewer-notes">
        <p className="text-sm text-warn">{t("viewerCard.noteLoadFailed")}</p>
        <Button size="sm" variant="ghost" onClick={read}>
          {t("common.retry")}
        </Button>
      </div>
    );
  }

  const presets = LIMITS.presetTags.filter((p) => !tags.some((x) => x.toLowerCase() === p.toLowerCase()));

  return (
    <div className="flex flex-col gap-2" data-testid="viewer-notes">
      <span className="text-sm uppercase tracking-wider text-muted-foreground">{t("viewerCard.notesTitle")}</span>
      <textarea
        className="min-h-16 w-full rounded border border-border bg-card-raised p-2 text-[12px]"
        value={note}
        maxLength={LIMITS.maxNote}
        disabled={load === "loading" || busy}
        placeholder={t("viewerCard.notePlaceholder")}
        aria-label={t("viewerCard.notesTitle")}
        onChange={(e) => setNote(e.target.value)}
      />
      <div className="flex items-center justify-between text-sm text-muted-foreground">
        <span>{t("viewerCard.noteHelp")}</span>
        <span className="tnum">{t("viewerCard.noteCount", { count: note.length, max: LIMITS.maxNote })}</span>
      </div>

      <div className="flex flex-wrap items-center gap-1.5">
        {tags.map((tag) => (
          <Badge key={tag} variant="outline" className="gap-1">
            {tag}
            <button
              type="button"
              className="text-muted-foreground hover:text-foreground"
              aria-label={t("viewerCard.removeTag", { tag })}
              disabled={busy}
              onClick={() => setTags(tags.filter((x) => x !== tag))}
            >
              <X className="h-2.5 w-2.5" />
            </button>
          </Badge>
        ))}
        {presets.map((p) => (
          <Button
            key={p}
            size="sm"
            variant="ghost"
            disabled={busy || load === "loading"}
            onClick={() => addTag(p)}
          >
            <Plus className="mr-0.5 h-3 w-3" />
            {p}
          </Button>
        ))}
      </div>

      <div className="flex flex-wrap items-center gap-1.5">
        <input
          className="h-8 w-36 rounded border border-border bg-card-raised px-2 text-[12px]"
          value={draftTag}
          maxLength={LIMITS.maxTagLen}
          placeholder={t("viewerCard.tagPlaceholder")}
          aria-label={t("viewerCard.tagPlaceholder")}
          disabled={busy || load === "loading"}
          onChange={(e) => setDraftTag(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter") {
              e.preventDefault();
              addTag(draftTag);
            }
          }}
        />
        <Button size="sm" variant="secondary" disabled={busy || !draftTag.trim()} onClick={() => addTag(draftTag)}>
          {t("viewerCard.addTag")}
        </Button>
        <span className="flex-1" />
        {hasRecord &&
          (confirmDelete ? (
            <>
              <Button size="sm" variant="destructive" disabled={busy} onClick={() => void remove()}>
                {t("viewerCard.deleteConfirm")}
              </Button>
              <Button size="sm" variant="ghost" onClick={() => setConfirmDelete(false)}>
                {t("common.cancel")}
              </Button>
            </>
          ) : (
            <Button size="sm" variant="ghost" disabled={busy} onClick={() => setConfirmDelete(true)}>
              {t("viewerCard.deleteNote")}
            </Button>
          ))}
        <Button size="sm" disabled={busy || !dirty || load !== "ready"} onClick={() => void save()}>
          {busy && <Loader2 className="mr-1 h-3 w-3 animate-spin" />}
          {t("viewerCard.saveNote")}
        </Button>
      </div>
    </div>
  );
}
