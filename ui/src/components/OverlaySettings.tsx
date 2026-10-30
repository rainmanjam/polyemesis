import { useEffect, useMemo, useState } from "react";
import { toast } from "sonner";
import { Copy, KeyRound, Loader2, Plus, RefreshCw, Save, Trash2 } from "lucide-react";
import { api } from "@/lib/api";
import { useT, type TranslationKey } from "@/lib/i18n";
import { copyToClipboard } from "@/lib/clipboard";
import { failedRead, mayClaim, okRead, pendingRead, readFailed, type ReadState } from "@/lib/readState";
import {
  OVERLAY_FONTS,
  OVERLAY_LIMITS,
  overlayUrl,
  type ChatOverlay,
  type OverlayFont,
  type OverlayPreset,
  type OverlayStyle,
  type OverlaysResponse,
  type ShownLine,
} from "@/lib/overlay";
import { ChatOverlayView } from "@/components/overlay/ChatOverlayView";
import { ConfirmDestructive } from "@/components/ConfirmDestructive";
import { useConfirm } from "@/hooks/useConfirm";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";

const PRESET_LABEL: Record<OverlayPreset, TranslationKey> = {
  classic: "overlay.presetClassic",
  bubble: "overlay.presetBubble",
  outline: "overlay.presetOutline",
  compact: "overlay.presetCompact",
};

const FONT_LABEL: Record<OverlayFont, TranslationKey> = {
  sans: "overlay.fontSans",
  serif: "overlay.fontSerif",
  mono: "overlay.fontMono",
  rounded: "overlay.fontRounded",
};

const PLATFORM_LABEL: Record<string, string> = {
  twitch: "Twitch",
  youtube: "YouTube",
  kick: "Kick",
  facebook: "Facebook",
  rumble: "Rumble",
};

/** Sample chat for the live preview: one line per platform, so the platform
 *  filter visibly removes the rival lines. Names are names; the words go
 *  through i18n. */
function sampleLines(t: ReturnType<typeof useT>): ShownLine[] {
  const now = Date.now();
  const mk = (
    id: string,
    platform: string,
    name: string,
    color: string,
    text: TranslationKey,
    extra: Partial<ShownLine> = {},
  ): ShownLine => ({ id, platform, name, color, text: t(text), at: "", receivedAt: now, ...extra });
  return [
    mk("s1", "twitch", "pixelpanda", "#ff7f50", "overlay.sample1", { subscriber: true }),
    mk("s2", "youtube", "Mara K.", "#22d3ee", "overlay.sample2"),
    mk("s3", "twitch", "nightowl_tv", "#a78bfa", "overlay.sample3", { moderator: true }),
    mk("s4", "kick", "rivetbeard", "#facc15", "overlay.sample4"),
    mk("s5", "twitch", "host", "#f87171", "overlay.sample5", { broadcaster: true }),
  ];
}

/** Chat overlays: create, style, copy the OBS URL, rotate and revoke keys. */
export function OverlaySettings() {
  const t = useT();
  const [data, setData] = useState<ReadState<OverlaysResponse>>(pendingRead);
  const [selected, setSelected] = useState<number | null>(null);
  const [newName, setNewName] = useState("");
  const [busy, setBusy] = useState(false);

  const load = (select?: number) => {
    api
      .listOverlays()
      .then((r) => {
        setData(okRead(r));
        setSelected((cur) => select ?? cur ?? r.overlays[0]?.id ?? null);
      })
      .catch(() => setData(failedRead()));
  };
  useEffect(() => load(), []);

  const create = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!mayClaim(data)) return;
    setBusy(true);
    try {
      const style = data.value.presets.classic;
      const o = await api.createOverlay({ name: newName.trim(), platforms: [], style });
      setNewName("");
      toast.success(t("overlay.created", { name: o.name }));
      load(o.id);
    } catch (err) {
      toast.error(err instanceof Error ? err.message : t("overlay.saveFailed"));
    } finally {
      setBusy(false);
    }
  };

  const current = mayClaim(data) ? data.value.overlays.find((o) => o.id === selected) : undefined;

  return (
    <Card className="lg:col-span-2">
      <CardHeader>
        <CardTitle>{t("overlay.title")}</CardTitle>
        <CardDescription>{t("overlay.desc")}</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-4">
        <form onSubmit={create} className="flex flex-wrap items-end gap-2">
          <div className="flex min-w-48 flex-1 flex-col gap-1">
            <Label htmlFor="overlay-new-name">{t("overlay.newName")}</Label>
            <Input
              id="overlay-new-name"
              value={newName}
              maxLength={64}
              placeholder={t("overlay.newNamePlaceholder")}
              onChange={(e) => setNewName(e.target.value)}
              required
            />
          </div>
          <Button type="submit" size="sm" disabled={busy || !newName.trim() || !mayClaim(data)}>
            {busy ? <Loader2 className="animate-spin" /> : <Plus />} {t("overlay.create")}
          </Button>
        </form>

        {readFailed(data) ? (
          <span className="text-tiny text-warn">{t("overlay.unread")}</span>
        ) : !mayClaim(data) ? (
          <span className="text-tiny text-muted-foreground">{t("overlay.loading")}</span>
        ) : data.value.overlays.length === 0 ? (
          <span className="text-tiny text-muted-foreground">{t("overlay.none")}</span>
        ) : (
          <>
            <div className="flex flex-wrap gap-1.5" role="tablist" aria-label={t("overlay.title")}>
              {data.value.overlays.map((o) => (
                <Button
                  key={o.id}
                  role="tab"
                  aria-selected={o.id === selected}
                  size="sm"
                  variant={o.id === selected ? "default" : "outline"}
                  onClick={() => setSelected(o.id)}
                >
                  {o.name}
                  {!o.obsKey && <Badge variant="warn">{t("overlay.revokedBadge")}</Badge>}
                </Button>
              ))}
            </div>
            <span className="text-micro text-muted-foreground">
              {t("overlay.stats", {
                shown: data.value.stats.shown,
                blocked: data.value.stats.blocked,
                expired: data.value.stats.expired,
              })}
            </span>
            {current && (
              <OverlayEditor
                key={`${current.id}-${current.obsKey}`}
                overlay={current}
                meta={data.value}
                onChanged={(id) => load(id)}
                onDeleted={() => {
                  setSelected(null);
                  load();
                }}
              />
            )}
          </>
        )}
      </CardContent>
    </Card>
  );
}

/** The style editor for one overlay: knobs on the left, the live preview on the
 *  right, and the key's controls underneath. Exported for its own tests. */
export function OverlayEditor({
  overlay,
  meta,
  onChanged,
  onDeleted,
}: {
  overlay: ChatOverlay;
  meta: Pick<OverlaysResponse, "presets" | "presetOrder" | "platforms" | "maxHoldMs">;
  onChanged: (id: number) => void;
  onDeleted: () => void;
}) {
  const t = useT();
  const [name, setName] = useState(overlay.name);
  const [platforms, setPlatforms] = useState<string[]>(overlay.platforms);
  const [style, setStyle] = useState<OverlayStyle>(overlay.style);
  const [saving, setSaving] = useState(false);
  const confirmRotate = useConfirm<ChatOverlay>();
  const confirmRevoke = useConfirm<ChatOverlay>();
  const confirmDelete = useConfirm<ChatOverlay>();

  const set = <K extends keyof OverlayStyle>(k: K, v: OverlayStyle[K]) => setStyle((s) => ({ ...s, [k]: v }));
  const num = (k: "fontSize" | "fadeSeconds" | "maxLines" | "backgroundOpacity", raw: string) => {
    const { min, max } = OVERLAY_LIMITS[k];
    const n = Math.round(Number(raw));
    if (Number.isFinite(n)) set(k, Math.max(min, Math.min(max, n)));
  };

  const preview = useMemo(() => {
    const lines = sampleLines(t).filter((l) => platforms.length === 0 || platforms.includes(l.platform));
    return lines.slice(Math.max(0, lines.length - style.maxLines));
  }, [t, platforms, style.maxLines]);

  const dirty =
    name !== overlay.name ||
    JSON.stringify(platforms) !== JSON.stringify(overlay.platforms) ||
    JSON.stringify(style) !== JSON.stringify(overlay.style);

  const save = async () => {
    setSaving(true);
    try {
      await api.updateOverlay(overlay.id, { name: name.trim(), platforms, style });
      toast.success(t("overlay.saved"));
      onChanged(overlay.id);
    } catch (err) {
      toast.error(err instanceof Error ? err.message : t("overlay.saveFailed"));
    } finally {
      setSaving(false);
    }
  };

  const keyAction = async (fn: () => Promise<unknown>, done: TranslationKey) => {
    try {
      await fn();
      toast.success(t(done, { name: overlay.name }));
      onChanged(overlay.id);
    } catch (err) {
      toast.error(err instanceof Error ? err.message : t("overlay.saveFailed"));
    }
  };

  const togglePlatform = (p: string, on: boolean) =>
    setPlatforms((cur) => (on ? [...cur, p].filter((x, i, a) => a.indexOf(x) === i) : cur.filter((x) => x !== p)));

  const origin = window.location.origin;
  const hasKey = overlay.obsKey !== "";

  return (
    <div className="grid gap-4 lg:grid-cols-[minmax(0,22rem)_minmax(0,1fr)]" data-testid="overlay-editor">
      <div className="flex flex-col gap-3">
        <div className="flex flex-col gap-1">
          <Label htmlFor="overlay-name">{t("overlay.name")}</Label>
          <Input id="overlay-name" value={name} maxLength={64} onChange={(e) => setName(e.target.value)} />
        </div>

        <fieldset className="flex flex-col gap-1.5">
          <legend className="text-tiny font-medium">{t("overlay.platforms")}</legend>
          {meta.platforms.map((p) => (
            <label key={p} className="flex items-center gap-2 text-sm">
              <Switch
                checked={platforms.includes(p)}
                onCheckedChange={(on) => togglePlatform(p, on)}
                aria-label={PLATFORM_LABEL[p] ?? p}
              />
              {PLATFORM_LABEL[p] ?? p}
            </label>
          ))}
          <span className="text-micro text-muted-foreground">
            {platforms.length === 0 ? t("overlay.platformsAll") : t("overlay.platformsSome")}
          </span>
        </fieldset>

        <div className="flex flex-col gap-1">
          <Label htmlFor="overlay-preset">{t("overlay.preset")}</Label>
          <Select
            value={style.preset}
            onValueChange={(v) => {
              const p = meta.presets[v as OverlayPreset];
              if (p) setStyle({ ...p });
            }}
          >
            <SelectTrigger id="overlay-preset">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {meta.presetOrder.map((p) => (
                <SelectItem key={p} value={p}>
                  {t(PRESET_LABEL[p])}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          <span className="text-micro text-muted-foreground">{t("overlay.presetHint")}</span>
        </div>

        <div className="grid grid-cols-2 gap-2">
          <div className="flex flex-col gap-1">
            <Label htmlFor="overlay-font">{t("overlay.font")}</Label>
            <Select value={style.font} onValueChange={(v) => set("font", v as OverlayFont)}>
              <SelectTrigger id="overlay-font">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {OVERLAY_FONTS.map((f) => (
                  <SelectItem key={f} value={f}>
                    {t(FONT_LABEL[f])}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
          <NumberField id="overlay-font-size" label={t("overlay.fontSize")} value={style.fontSize}
            limits={OVERLAY_LIMITS.fontSize} onChange={(v) => num("fontSize", v)} />
          <ColorField id="overlay-text-color" label={t("overlay.textColor")} value={style.textColor}
            onChange={(v) => set("textColor", v)} />
          <ColorField id="overlay-bg-color" label={t("overlay.backgroundColor")} value={style.backgroundColor}
            onChange={(v) => set("backgroundColor", v)} />
          <NumberField id="overlay-bg-opacity" label={t("overlay.backgroundOpacity")} value={style.backgroundOpacity}
            limits={OVERLAY_LIMITS.backgroundOpacity} onChange={(v) => num("backgroundOpacity", v)} />
          <NumberField id="overlay-fade" label={t("overlay.fadeSeconds")} value={style.fadeSeconds}
            limits={OVERLAY_LIMITS.fadeSeconds} onChange={(v) => num("fadeSeconds", v)} />
          <NumberField id="overlay-max-lines" label={t("overlay.maxLines")} value={style.maxLines}
            limits={OVERLAY_LIMITS.maxLines} onChange={(v) => num("maxLines", v)} />
        </div>
        <span className="text-micro text-muted-foreground">{t("overlay.fadeHint")}</span>

        <div className="flex flex-col gap-1.5">
          {(
            [
              ["authorColors", "overlay.authorColors"],
              ["showPlatform", "overlay.showPlatform"],
              ["showBadges", "overlay.showBadges"],
            ] as const
          ).map(([k, label]) => (
            <label key={k} className="flex items-center gap-2 text-sm">
              <Switch checked={style[k]} onCheckedChange={(on) => set(k, on)} aria-label={t(label)} />
              {t(label)}
            </label>
          ))}
        </div>

        <div className="flex flex-wrap gap-2">
          <Button size="sm" onClick={save} disabled={saving || !dirty || !name.trim()}>
            {saving ? <Loader2 className="animate-spin" /> : <Save />} {t("overlay.save")}
          </Button>
          <Button size="sm" variant="ghost" onClick={() => confirmDelete.ask(overlay)}>
            <Trash2 /> {t("overlay.delete")}
          </Button>
        </div>
      </div>

      <div className="flex flex-col gap-3">
        <div className="flex flex-col gap-1">
          <span className="text-tiny font-medium">{t("overlay.preview")}</span>
          <div
            data-testid="overlay-preview"
            className="h-72 overflow-hidden rounded border border-border"
            style={{
              // A checkerboard, so transparency reads as transparency.
              backgroundColor: "#1f2937",
              backgroundImage:
                "linear-gradient(45deg,#374151 25%,transparent 25%),linear-gradient(-45deg,#374151 25%,transparent 25%),linear-gradient(45deg,transparent 75%,#374151 75%),linear-gradient(-45deg,transparent 75%,#374151 75%)",
              backgroundSize: "20px 20px",
              backgroundPosition: "0 0,0 10px,10px -10px,-10px 0",
            }}
          >
            <ChatOverlayView lines={preview} style={style} mode="preview" />
          </div>
          {dirty && <span className="text-micro text-warn">{t("overlay.unsaved")}</span>}
        </div>

        <div className="flex flex-col gap-1.5 rounded border border-border p-2">
          <span className="text-tiny font-medium">{t("overlay.obsTitle")}</span>
          {hasKey ? (
            <>
              <div className="flex flex-wrap gap-2">
                <Button size="sm" onClick={() => copyToClipboard(t, overlayUrl(origin, overlay.obsKey), t("overlay.obsUrl"))}>
                  <Copy /> {t("overlay.copyObsUrl")}
                </Button>
                <Button size="sm" variant="outline"
                  onClick={() => copyToClipboard(t, overlayUrl(origin, overlay.obsKey, true), t("overlay.dockUrl"))}>
                  <Copy /> {t("overlay.copyDockUrl")}
                </Button>
              </div>
              <span className="font-mono text-micro text-muted-foreground">
                {overlay.obsKey.slice(0, 12)}…
              </span>
              <span className="text-micro text-muted-foreground">{t("overlay.obsHint")}</span>
              <span className="text-micro text-muted-foreground">
                {t("overlay.holdHint", { seconds: Math.round(meta.maxHoldMs / 1000) })}
              </span>
              <div className="flex flex-wrap gap-2">
                <Button size="sm" variant="outline" onClick={() => confirmRotate.ask(overlay)}>
                  <RefreshCw /> {t("overlay.rotate")}
                </Button>
                <Button size="sm" variant="outline" onClick={() => confirmRevoke.ask(overlay)}>
                  <KeyRound /> {t("overlay.revoke")}
                </Button>
              </div>
            </>
          ) : (
            <>
              <span className="text-micro text-warn">{t("overlay.revokedHint")}</span>
              <Button size="sm" onClick={() => keyAction(() => api.rotateOverlayKey(overlay.id), "overlay.keyIssued")}>
                <KeyRound /> {t("overlay.issueKey")}
              </Button>
            </>
          )}
        </div>
      </div>

      <ConfirmDestructive
        open={confirmRotate.open}
        onOpenChange={confirmRotate.onOpenChange}
        subject={overlay.name}
        title={t("overlay.rotateTitle", { name: overlay.name })}
        description={t("overlay.rotateDesc")}
        confirmLabel={t("overlay.rotate")}
        onConfirm={() => keyAction(() => api.rotateOverlayKey(overlay.id), "overlay.keyRotated")}
      />
      <ConfirmDestructive
        open={confirmRevoke.open}
        onOpenChange={confirmRevoke.onOpenChange}
        subject={overlay.name}
        title={t("overlay.revokeTitle", { name: overlay.name })}
        description={t("overlay.revokeDesc")}
        confirmLabel={t("overlay.revoke")}
        onConfirm={() => keyAction(() => api.revokeOverlayKey(overlay.id), "overlay.keyRevoked")}
      />
      <ConfirmDestructive
        open={confirmDelete.open}
        onOpenChange={confirmDelete.onOpenChange}
        subject={overlay.name}
        title={t("overlay.deleteTitle", { name: overlay.name })}
        description={t("overlay.deleteDesc")}
        requireTyping
        confirmLabel={t("overlay.delete")}
        onConfirm={async () => {
          try {
            await api.deleteOverlay(overlay.id);
            toast.success(t("overlay.deleted", { name: overlay.name }));
            onDeleted();
          } catch (err) {
            toast.error(err instanceof Error ? err.message : t("overlay.saveFailed"));
          }
        }}
      />
    </div>
  );
}

function NumberField({
  id,
  label,
  value,
  limits,
  onChange,
}: {
  id: string;
  label: string;
  value: number;
  limits: { min: number; max: number };
  onChange: (raw: string) => void;
}) {
  return (
    <div className="flex flex-col gap-1">
      <Label htmlFor={id}>{label}</Label>
      <Input id={id} type="number" min={limits.min} max={limits.max} value={value}
        onChange={(e) => onChange(e.target.value)} />
    </div>
  );
}

function ColorField({
  id,
  label,
  value,
  onChange,
}: {
  id: string;
  label: string;
  value: string;
  onChange: (v: string) => void;
}) {
  return (
    <div className="flex flex-col gap-1">
      <Label htmlFor={id}>{label}</Label>
      <Input id={id} type="color" value={value} className="h-8 p-1"
        onChange={(e) => onChange(e.target.value.toLowerCase())} />
    </div>
  );
}
