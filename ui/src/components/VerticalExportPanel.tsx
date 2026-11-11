import { useCallback, useRef, useState, type PointerEvent as ReactPointerEvent, type RefObject } from "react";
import { toast } from "sonner";
import { Camera, Save, Trash2 } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Checkbox } from "@/components/ui/checkbox";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { http } from "@/lib/api";
import { useT } from "@/lib/i18n";
import { cn } from "@/lib/utils";
import {
  boxFromDrag,
  lockFaceBox,
  nudgeBox,
  overShortLimit,
  specFromBody,
  verticalBody,
  type CaptionStyle,
  type ClipTemplate,
  type ClipTemplatesView,
  type VerticalLayout,
  type VerticalSpec,
} from "@/lib/vertical";

/* The vertical-short half of the clip editor (Wave 4 #5b).

   It only describes the short: the export itself is the editor's ordinary
   Export (or a highlight's Accept), which carries this panel's spec. The
   face-cam box is drawn on a still of the current frame -- a fixed box, no
   tracking -- and is locked to the face panel's shape so the server's crop
   uses it exactly as drawn. */

interface Props {
  enabled: boolean;
  onEnabledChange: (on: boolean) => void;
  spec: VerticalSpec;
  onChange: (spec: VerticalSpec) => void;
  options: ClipTemplatesView | null;
  onTemplatesChanged: () => void;
  hasTranscript: boolean;
  durationMs: number;
  videoRef: RefObject<HTMLVideoElement | null>;
  poster?: string;
}

const LAYOUTS: VerticalLayout[] = ["center", "stacked", "blur"];

export function VerticalExportPanel({
  enabled,
  onEnabledChange,
  spec,
  onChange,
  options,
  onTemplatesChanged,
  hasTranscript,
  durationMs,
  videoRef,
  poster,
}: Readonly<Props>) {
  const t = useT();
  const [frame, setFrame] = useState<string | undefined>(undefined);
  const [srcAspect, setSrcAspect] = useState(16 / 9);
  const [templateName, setTemplateName] = useState("");
  const [saving, setSaving] = useState(false);
  const drag = useRef<{ x: number; y: number } | null>(null);
  const picker = useRef<HTMLDivElement>(null);

  const layoutTitle: Record<VerticalLayout, string> = {
    center: t("clipedit.vertical.center"),
    stacked: t("clipedit.vertical.stacked"),
    blur: t("clipedit.vertical.blur"),
  };
  const layoutBody: Record<VerticalLayout, string> = {
    center: t("clipedit.vertical.centerBody"),
    stacked: t("clipedit.vertical.stackedBody"),
    blur: t("clipedit.vertical.blurBody"),
  };

  const style = { ...options?.defaultStyle, ...spec.captions.style };
  const setStyle = (patch: Partial<CaptionStyle>) =>
    onChange({ ...spec, captions: { ...spec.captions, style: { ...spec.captions.style, ...patch } } });

  // A still of the frame under the playhead. The proxy is same-origin, so the
  // canvas is not tainted; anything that fails falls back to the poster.
  const grabFrame = useCallback(() => {
    const v = videoRef.current;
    if (!v?.videoWidth) {
      setFrame(poster);
      return;
    }
    try {
      const c = document.createElement("canvas");
      c.width = v.videoWidth;
      c.height = v.videoHeight;
      c.getContext("2d")?.drawImage(v, 0, 0);
      setFrame(c.toDataURL("image/jpeg", 0.8));
      setSrcAspect(v.videoWidth / v.videoHeight);
    } catch {
      setFrame(poster);
    }
  }, [videoRef, poster]);

  const at = (e: ReactPointerEvent) => {
    const r = picker.current?.getBoundingClientRect();
    if (!r || r.width === 0 || r.height === 0) return { x: 0, y: 0 };
    return { x: (e.clientX - r.left) / r.width, y: (e.clientY - r.top) / r.height };
  };

  const applyTemplate = (tpl: ClipTemplate) => {
    onChange(specFromBody(tpl.vertical, spec));
    onEnabledChange(true);
  };

  const saveTemplate = async () => {
    setSaving(true);
    try {
      await http.post("/clipper/templates", { name: templateName.trim(), vertical: verticalBody(spec) });
      toast.success(t("clipedit.vertical.templateSaved"));
      setTemplateName("");
      onTemplatesChanged();
    } catch (err) {
      toast.error(err instanceof Error && err.message ? err.message : t("clipedit.vertical.templateFailed"));
    } finally {
      setSaving(false);
    }
  };

  const deleteTemplate = async (tpl: ClipTemplate) => {
    try {
      await http.del(`/clipper/templates/${tpl.id}`);
      toast.success(t("clipedit.vertical.templateDeleted"));
      onTemplatesChanged();
    } catch (err) {
      toast.error(err instanceof Error && err.message ? err.message : t("clipedit.vertical.templateFailed"));
    }
  };

  const tooLong = enabled && overShortLimit(durationMs, options?.maxShortSeconds);

  return (
    <Card>
      <CardHeader>
        <CardTitle>{t("clipedit.vertical.title")}</CardTitle>
      </CardHeader>
      <CardContent className="flex flex-col gap-3">
        <Label className="flex items-start gap-2 text-sm">
          <Checkbox checked={enabled} onCheckedChange={(v) => onEnabledChange(v === true)} />
          <span>
            {t("clipedit.vertical.enable")}
            <span className="mt-0.5 block text-tiny text-muted-foreground">{t("clipedit.vertical.enableBody")}</span>
          </span>
        </Label>

        {enabled && (
          <>
            {tooLong && (
              <output className="block text-tiny text-warn">
                {t("clipedit.vertical.tooLong", { seconds: Math.round(durationMs / 100) / 10 })}
              </output>
            )}

            {options && options.templates.length > 0 && (
              <div className="flex flex-col gap-1">
                <Label>
                  {t("clipedit.vertical.templates")}
                </Label>
                {options.templates.map((tpl) => (
                  <div key={tpl.id} className="flex items-center gap-1">
                    <Button variant="secondary" size="sm" className="flex-1 justify-start" onClick={() => applyTemplate(tpl)}
                      aria-label={`${t("clipedit.vertical.applyTemplate")}: ${tpl.name}`}>
                      {tpl.name}
                    </Button>
                    <Button variant="ghost" size="sm" onClick={() => deleteTemplate(tpl)}
                      aria-label={`${t("clipedit.vertical.deleteTemplate")}: ${tpl.name}`}>
                      <Trash2 />
                    </Button>
                  </div>
                ))}
              </div>
            )}

            <div className="flex flex-col gap-1">
              <Label>
                {t("clipedit.vertical.layout")}
              </Label>
              <div className="grid grid-cols-1 gap-1.5">
                {LAYOUTS.filter((l) => !options || options.layouts.includes(l)).map((l) => (
                  <OptionButton key={l} active={spec.layout === l} onClick={() => onChange({ ...spec, layout: l })}
                    title={layoutTitle[l]} body={layoutBody[l]} />
                ))}
              </div>
            </div>

            {spec.layout === "center" && (
              <Label className="flex flex-col gap-1 text-tiny">
                {t("clipedit.vertical.focus")}
                <input type="range" min={0} max={100} step={1} value={Math.round(spec.focus * 100)}
                  onChange={(e) => onChange({ ...spec, focus: Number(e.target.value) / 100 })}
                  className="w-full accent-primary" />
              </Label>
            )}

            {spec.layout === "stacked" && (
              <div className="flex flex-col gap-2">
                <div className="flex items-center justify-between gap-2">
                  <Label>
                    {t("clipedit.vertical.faceBox")}
                  </Label>
                  <Button variant="secondary" size="sm" onClick={grabFrame}>
                    <Camera /> {t("clipedit.vertical.grabFrame")}
                  </Button>
                </div>
                <div
                  ref={picker}
                  data-testid="face-picker"
                  // The picker takes the FRAME's shape, so the image fills it
                  // exactly: a letterboxed portrait or 4:3 frame would make
                  // pointer fractions of the box differ from fractions of the
                  // source, and the server would crop a box nobody drew.
                  style={{ aspectRatio: String(srcAspect) }}
                  className="relative w-full touch-none select-none overflow-hidden rounded-md border border-border bg-black"
                  onPointerDown={(e) => {
                    drag.current = at(e);
                    (e.target as Element).setPointerCapture?.(e.pointerId);
                  }}
                  onPointerMove={(e) => {
                    if (!drag.current) return;
                    onChange({ ...spec, face: boxFromDrag(drag.current, at(e), srcAspect, spec.faceShare) });
                  }}
                  onPointerUp={() => {
                    drag.current = null;
                  }}
                >
                  {(frame ?? poster) && <img src={frame ?? poster} alt="" className="h-full w-full" draggable={false}
                    onLoad={(e) => {
                      const img = e.currentTarget;
                      if (img.naturalWidth > 0 && img.naturalHeight > 0) setSrcAspect(img.naturalWidth / img.naturalHeight);
                    }} />}
                  <div
                    // A slider on the box's left edge, whose value text also gives the
                    // top edge: the arrow keys move it in both axes, and a screen reader
                    // is told where it sits. Not a button, which would promise an
                    // Enter/Space action there is none of.
                    role="slider"
                    tabIndex={0}
                    aria-valuemin={0}
                    aria-valuemax={Math.round((1 - spec.face.w) * 100)}
                    aria-valuenow={Math.round(spec.face.x * 100)}
                    aria-valuetext={t("clipedit.vertical.faceBoxValue", {
                      x: Math.round(spec.face.x * 100),
                      y: Math.round(spec.face.y * 100),
                    })}
                    // Focus on pointerdown: Safari and Firefox on macOS do not focus
                    // a clicked non-text control, so arrow nudging after a drag would
                    // need a Tab first.
                    onPointerDown={(e) => e.currentTarget.focus()}
                    aria-label={t("clipedit.vertical.faceBox")}
                    data-testid="face-box"
                    className="absolute cursor-move border-2 focus-visible:outline focus-visible:outline-2 focus-visible:outline-ring border-primary bg-primary-dim"
                    style={{
                      left: `${spec.face.x * 100}%`,
                      top: `${spec.face.y * 100}%`,
                      width: `${spec.face.w * 100}%`,
                      height: `${spec.face.h * 100}%`,
                    }}
                    onKeyDown={(e) => {
                      const next = nudgeBox(spec.face, e.key);
                      if (next !== spec.face) {
                        e.preventDefault();
                        onChange({ ...spec, face: next });
                      }
                    }}
                  />
                </div>
                <p className="text-tiny text-muted-foreground">{t("clipedit.vertical.faceBoxHint")}</p>
                <Label className="flex flex-col gap-1 text-tiny">
                  {t("clipedit.vertical.faceShare")}
                  <input type="range" min={20} max={60} step={1} value={Math.round(spec.faceShare * 100)}
                    onChange={(e) => {
                      const share = Number(e.target.value) / 100;
                      onChange({ ...spec, faceShare: share, face: lockFaceBox(spec.face, srcAspect, share) });
                    }}
                    className="w-full accent-primary" />
                </Label>
                <Label className="flex items-center gap-2 text-tiny">
                  <Checkbox checked={spec.faceBottom} onCheckedChange={(v) => onChange({ ...spec, faceBottom: v === true })} />
                  {t("clipedit.vertical.faceBottom")}
                </Label>
              </div>
            )}

            <div className="flex flex-col gap-2 border-t border-border pt-2">
              <Label className="flex items-start gap-2 text-sm">
                <Checkbox checked={spec.captions.enabled} disabled={!hasTranscript}
                  onCheckedChange={(v) => onChange({ ...spec, captions: { ...spec.captions, enabled: v === true } })} />
                <span>
                  {t("clipedit.vertical.captions")}
                  <span className="mt-0.5 block text-tiny text-muted-foreground">
                    {hasTranscript ? t("clipedit.vertical.captionsBody") : t("clipedit.vertical.noTranscript")}
                  </span>
                </span>
              </Label>
              {spec.captions.enabled && options && !options.burnCaptions && (
                <output className="block text-tiny text-warn">{t("clipedit.vertical.noLibass")}</output>
              )}
              {spec.captions.enabled && (
                <div className="grid grid-cols-2 gap-2">
                  <Label className="col-span-2 flex flex-col gap-1 text-tiny">
                    {t("clipedit.vertical.font")}
                    <select value={style.font ?? ""} onChange={(e) => setStyle({ font: e.target.value })}
                      className="h-8 rounded-md border border-border bg-card px-2 text-sm">
                      {(options?.fonts ?? []).map((f) => (
                        <option key={f} value={f}>{f}</option>
                      ))}
                    </select>
                  </Label>
                  <Label className="flex flex-col gap-1 text-tiny">
                    {t("clipedit.vertical.size")}
                    <input type="range" min={2} max={8} step={0.2} value={style.sizePct ?? 4.2}
                      onChange={(e) => setStyle({ sizePct: Number(e.target.value) })} className="w-full accent-primary" />
                  </Label>
                  <Label className="flex flex-col gap-1 text-tiny">
                    {t("clipedit.vertical.position")}
                    <input type="range" min={10} max={90} step={1} value={style.positionPct ?? 72}
                      onChange={(e) => setStyle({ positionPct: Number(e.target.value) })} className="w-full accent-primary" />
                  </Label>
                  <Label className="flex flex-col gap-1 text-tiny">
                    {t("clipedit.vertical.color")}
                    <Input type="color" value={style.color ?? "#ffffff"} className="h-8 p-1"
                      onChange={(e) => setStyle({ color: e.target.value })} />
                  </Label>
                  <div className="flex flex-col justify-end gap-1">
                    <Label className="flex items-center gap-2 text-tiny">
                      <Checkbox checked={!!style.uppercase} onCheckedChange={(v) => setStyle({ uppercase: v === true })} />
                      {t("clipedit.vertical.uppercase")}
                    </Label>
                    <Label className="flex items-center gap-2 text-tiny">
                      <Checkbox checked={!!style.box} onCheckedChange={(v) => setStyle({ box: v === true })} />
                      {t("clipedit.vertical.box")}
                    </Label>
                  </div>
                </div>
              )}
            </div>

            <div className="flex items-end gap-1 border-t border-border pt-2">
              <Label className="flex flex-1 flex-col gap-1 text-tiny">
                {t("clipedit.vertical.templateName")}
                <Input value={templateName} maxLength={60} onChange={(e) => setTemplateName(e.target.value)} className="h-8" />
              </Label>
              <Button variant="secondary" size="sm" disabled={saving || templateName.trim() === ""} onClick={saveTemplate}>
                <Save /> {t("clipedit.vertical.saveTemplate")}
              </Button>
            </div>
          </>
        )}
      </CardContent>
    </Card>
  );
}

function OptionButton({ active, onClick, title, body }: Readonly<{ active: boolean; onClick: () => void; title: string; body: string }>) {
  return (
    <button
      type="button"
      onClick={onClick}
      aria-pressed={active}
      className={cn(
        "rounded-md border p-2 text-left transition-colors",
        active ? "border-primary bg-primary-dim text-foreground" : "border-border bg-card hover:bg-card-raised",
      )}
    >
      <div className="text-sm font-semibold">{title}</div>
      <div className="mt-0.5 text-tiny leading-snug text-muted-foreground">{body}</div>
    </button>
  );
}
