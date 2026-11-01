/* ===========================================================================
   The recurring-broadcast template for one start schedule (#18).

   The template is the title pattern, description, privacy, category, look-ahead
   and thumbnail that every upcoming event for the show is created with. It sits
   in the schedule dialog, and only a start schedule can carry one: the server
   refuses a template on a stop, so the dialog shows it only for a start.

   A platform's refusal of the last look-ahead create is kept on the template and
   shown here as a banner, so the operator sees a limit that is still biting and
   not only a line in the log.
   =========================================================================== */

import { useEffect, useId, useState } from "react";
import { toast } from "sonner";
import { ConfirmDestructive } from "@/components/ConfirmDestructive";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { useT } from "@/lib/i18n";
import {
  deleteTemplate,
  getTemplate,
  saveTemplate,
  uploadThumbnail,
  MAX_THUMBNAIL_BYTES,
  THUMBNAIL_TYPES,
} from "@/lib/recurringApi";
import type { BroadcastTemplate } from "@/lib/types";

/** The server's own bound on look-ahead (scheduler.MaxLookahead). The form enforces it too, so an
 *  out-of-range number is caught before the request. */
const MAX_LOOKAHEAD = 10;

interface TemplateForm {
  titlePattern: string;
  description: string;
  privacy: string;
  category: string;
  lookahead: number;
}

function formFrom(tpl: BroadcastTemplate | null): TemplateForm {
  return {
    titlePattern: tpl?.titlePattern ?? "",
    description: tpl?.description ?? "",
    privacy: tpl?.privacy || "public",
    category: tpl?.category ?? "",
    lookahead: tpl?.lookahead || 2,
  };
}

function messageOf(err: unknown): string {
  return err instanceof Error ? err.message : String(err);
}

export function BroadcastTemplateEditor({ scheduleId }: { scheduleId: number }) {
  const t = useT();
  const ids = useId();
  // undefined is "still loading"; null is "this schedule has no template yet".
  const [tpl, setTpl] = useState<BroadcastTemplate | null | undefined>(undefined);
  const [form, setForm] = useState<TemplateForm>(formFrom(null));
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [removeOpen, setRemoveOpen] = useState(false);

  useEffect(() => {
    let live = true;
    getTemplate(scheduleId)
      .then((v) => {
        if (!live) return;
        setTpl(v);
        setForm(formFrom(v));
      })
      .catch((err: unknown) => {
        if (live) setError(messageOf(err));
      });
    return () => {
      live = false;
    };
  }, [scheduleId]);

  const reload = async () => {
    const v = await getTemplate(scheduleId);
    setTpl(v);
    setForm(formFrom(v));
  };

  const save = async () => {
    setBusy(true);
    setError("");
    try {
      const saved = await saveTemplate(scheduleId, form);
      setTpl(saved);
      toast.success(t("wave3.recurring.saved"));
    } catch (err) {
      setError(messageOf(err));
    } finally {
      setBusy(false);
    }
  };

  const remove = async () => {
    setBusy(true);
    setError("");
    try {
      await deleteTemplate(scheduleId);
      setTpl(null);
      setForm(formFrom(null));
    } catch (err) {
      setError(messageOf(err));
    } finally {
      setBusy(false);
    }
  };

  const onPicture = async (file: File | undefined) => {
    if (!file) return;
    if (!(THUMBNAIL_TYPES as readonly string[]).includes(file.type)) {
      setError(t("wave3.recurring.thumbnailType"));
      return;
    }
    if (file.size > MAX_THUMBNAIL_BYTES) {
      setError(t("wave3.recurring.thumbnailTooLarge"));
      return;
    }
    setBusy(true);
    setError("");
    try {
      await uploadThumbnail(scheduleId, file, file.type);
      await reload();
    } catch (err) {
      setError(t("wave3.recurring.thumbnailRefused", { detail: messageOf(err) }));
    } finally {
      setBusy(false);
    }
  };

  if (tpl === undefined && !error) {
    return null;
  }

  const lookaheadOk = form.lookahead >= 1 && form.lookahead <= MAX_LOOKAHEAD;
  const titleOk = form.titlePattern.trim().length > 0 && form.titlePattern.length <= 100;

  return (
    <section className="mt-4 grid gap-3 rounded border border-border p-3" aria-label={t("wave3.recurring.templateTitle")}>
      <div>
        <h3 className="text-sm font-medium">{t("wave3.recurring.templateTitle")}</h3>
        <p className="text-xs text-muted-foreground">{t("wave3.recurring.templateHint")}</p>
      </div>

      {tpl?.lastError ? (
        <div role="alert" className="rounded border border-warn/30 bg-warn-dim px-2 py-1 text-sm text-warn">
          <p className="font-medium">{t("wave3.recurring.lastErrorTitle")}</p>
          <p>{tpl.lastError}</p>
          {tpl.lastErrorAt ? (
            <p className="text-xs">
              {t("wave3.recurring.lastErrorAt", { time: new Date(tpl.lastErrorAt).toLocaleString() })}
            </p>
          ) : null}
        </div>
      ) : null}

      {tpl === null ? <p className="text-sm text-muted-foreground">{t("wave3.recurring.noTemplate")}</p> : null}

      <div className="grid gap-1">
        <Label htmlFor={`${ids}-title`}>{t("wave3.recurring.titlePattern")}</Label>
        <Input
          id={`${ids}-title`}
          value={form.titlePattern}
          maxLength={100}
          onChange={(e) => setForm({ ...form, titlePattern: e.target.value })}
        />
      </div>
      <div className="grid gap-1">
        <Label htmlFor={`${ids}-desc`}>{t("wave3.recurring.description")}</Label>
        <Input
          id={`${ids}-desc`}
          value={form.description}
          onChange={(e) => setForm({ ...form, description: e.target.value })}
        />
      </div>
      <div className="grid gap-1">
        <Label htmlFor={`${ids}-privacy`}>{t("wave3.recurring.privacy")}</Label>
        <select
          id={`${ids}-privacy`}
          className="h-9 rounded border border-input bg-background px-2 text-sm"
          value={form.privacy}
          onChange={(e) => setForm({ ...form, privacy: e.target.value })}
        >
          <option value="public">{t("wave3.recurring.privacyPublic")}</option>
          <option value="unlisted">{t("wave3.recurring.privacyUnlisted")}</option>
          <option value="private">{t("wave3.recurring.privacyPrivate")}</option>
        </select>
      </div>
      <div className="grid gap-1">
        <Label htmlFor={`${ids}-category`}>{t("wave3.recurring.category")}</Label>
        <Input
          id={`${ids}-category`}
          value={form.category}
          onChange={(e) => setForm({ ...form, category: e.target.value })}
        />
      </div>
      <div className="grid gap-1">
        <Label htmlFor={`${ids}-lookahead`}>{t("wave3.recurring.lookahead")}</Label>
        <Input
          id={`${ids}-lookahead`}
          type="number"
          min={1}
          max={MAX_LOOKAHEAD}
          value={form.lookahead}
          onChange={(e) => setForm({ ...form, lookahead: Number(e.target.value) })}
        />
        <p className="text-xs text-muted-foreground">{t("wave3.recurring.lookaheadHint")}</p>
      </div>

      <div className="grid gap-1">
        <Label htmlFor={`${ids}-thumb`}>{t("wave3.recurring.thumbnail")}</Label>
        <p className="text-xs text-muted-foreground">{t("wave3.recurring.thumbnailHint")}</p>
        {tpl?.hasThumbnail ? <p className="text-xs">{t("wave3.recurring.thumbnailStored")}</p> : null}
        <input
          id={`${ids}-thumb`}
          type="file"
          accept="image/png,image/jpeg"
          className="text-sm"
          aria-label={t("wave3.recurring.chooseThumbnail")}
          disabled={busy || tpl === null}
          onChange={(e) => void onPicture(e.target.files?.[0])}
        />
      </div>

      {error ? (
        <p role="alert" className="text-sm text-down">
          {error}
        </p>
      ) : null}

      <div className="flex flex-wrap gap-2">
        <Button size="sm" disabled={busy || !lookaheadOk || !titleOk} onClick={() => void save()}>
          {t("wave3.recurring.save")}
        </Button>
        {tpl ? (
          <Button size="sm" variant="ghost" disabled={busy} onClick={() => setRemoveOpen(true)}>
            {t("wave3.recurring.remove")}
          </Button>
        ) : null}
      </div>

      <ConfirmDestructive
        open={removeOpen}
        onOpenChange={setRemoveOpen}
        subject={tpl?.titlePattern ?? ""}
        title={t("wave3.recurring.remove")}
        description={t("wave3.recurring.removeConfirm")}
        confirmLabel={t("wave3.recurring.remove")}
        onConfirm={remove}
      />
    </section>
  );
}
