import { useCallback, useEffect, useMemo, useState } from "react";
import { toast } from "sonner";
import { Loader2, Plus, Save, Trash2 } from "lucide-react";

import { api, type DestinationWithRouting } from "@/lib/api";
import { useT, type TranslationKey } from "@/lib/i18n";
import type {
  Destination,
  ObsConfig,
  ObsConnState,
  ObsStatus,
  Preset,
  SourceView,
} from "@/lib/types";
import { SecretInput } from "@/components/SecretInput";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Checkbox } from "@/components/ui/checkbox";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";

/* THE OBS BRIDGE (#16). Settings for what OBS may drive, and the live state of
 * polyemesis's link to obs-websocket. The server does all of the deciding --
 * the stop acknowledgement, the programme rule, which scene changes can go
 * live -- and this page states its answers rather than repeating its rules.
 * See docs/OBS-BRIDGE.md. */

const STATE_LABEL: Record<ObsConnState, TranslationKey> = {
  off: "obs.state.off",
  connecting: "obs.state.connecting",
  connected: "obs.state.connected",
  unreachable: "obs.state.unreachable",
  refused: "obs.state.refused",
  incompatible: "obs.state.incompatible",
};

const STATE_TONE: Record<ObsConnState, "live" | "warn" | "down" | "outline"> = {
  off: "outline",
  connecting: "warn",
  connected: "live",
  unreachable: "down",
  refused: "down",
  incompatible: "down",
};

/** Status is re-read on this period while the section is open, so the
 *  connection badge follows a link that drops or comes back. */
const STATUS_POLL_MS = 5000;

const EMPTY: ObsConfig = {
  enabled: false,
  connect: false,
  url: "",
  hasPassword: false,
  sourceId: null,
  destinations: [],
  startOnStream: false,
  stopOnStream: false,
  stopAcknowledged: false,
  stopGraceSeconds: 0,
  scenes: [],
};

export function ObsBridgeSettings() {
  const t = useT();
  const [draft, setDraft] = useState<ObsConfig | null>(null);
  const [status, setStatus] = useState<ObsStatus | null>(null);
  const [sources, setSources] = useState<SourceView[]>([]);
  const [dests, setDests] = useState<Destination[]>([]);
  const [presets, setPresets] = useState<Preset[]>([]);
  const [password, setPassword] = useState("");
  const [saving, setSaving] = useState(false);
  const [loadError, setLoadError] = useState(false);

  useEffect(() => {
    let live = true;
    Promise.all([api.getObs(), api.listSources(), api.listDestinations(), api.listPresets()])
      .then(([obs, srcs, ds, ps]) => {
        if (!live) return;
        const def = srcs.find((s) => s.isDefault) ?? srcs[0];
        setDraft({ ...EMPTY, ...obs.config, sourceId: obs.config.sourceId ?? (def ? Number(def.id) : null) });
        setStatus(obs.status);
        setSources(srcs);
        setDests(ds.map((d: DestinationWithRouting) => d.destination));
        setPresets(ps.presets);
      })
      .catch(() => {
        if (live) setLoadError(true);
      });
    return () => {
      live = false;
    };
  }, []);

  // Only the status is polled; the draft is the operator's until they save.
  useEffect(() => {
    const id = window.setInterval(() => {
      api
        .getObs()
        .then((r) => setStatus(r.status))
        .catch(() => undefined);
    }, STATUS_POLL_MS);
    return () => window.clearInterval(id);
  }, []);

  const onProgramme = useMemo(
    () => dests.filter((d) => draft !== null && Number(d.sourceId ?? NaN) === draft.sourceId),
    [dests, draft],
  );

  const set = useCallback((patch: Partial<ObsConfig>) => setDraft((d) => (d ? { ...d, ...patch } : d)), []);

  const save = async () => {
    if (!draft) return;
    setSaving(true);
    try {
      if (password !== "") {
        await api.putObsPassword(password);
        setPassword("");
      }
      const r = await api.putObs(draft);
      setDraft({ ...EMPTY, ...r.config });
      setStatus(r.status);
      toast.success(t("obs.saved"));
    } catch (e) {
      toast.error(e instanceof Error ? e.message : String(e));
    } finally {
      setSaving(false);
    }
  };

  const clearPassword = async () => {
    try {
      await api.putObsPassword("");
      set({ hasPassword: false });
    } catch (e) {
      toast.error(e instanceof Error ? e.message : String(e));
    }
  };

  if (loadError) {
    return <p className="text-sm text-down">{t("obs.loadFailed")}</p>;
  }
  if (!draft) {
    return <Loader2 className="animate-spin" aria-label={t("common.loading")} />;
  }

  const toggleDest = (id: number, on: boolean) =>
    set({ destinations: on ? [...draft.destinations, id] : draft.destinations.filter((x) => x !== id) });

  const conn = status?.connection.state ?? "off";

  return (
    <div className="flex flex-col gap-3">
      <Card>
        <CardHeader>
          <CardTitle>{t("obs.title")}</CardTitle>
          <CardDescription>{t("obs.desc")}</CardDescription>
        </CardHeader>
        <CardContent className="flex flex-col gap-4">
          <div className="flex flex-wrap items-center gap-2 text-sm" data-testid="obs-status">
            <span className="font-medium">{t("obs.status")}</span>
            <Badge variant={STATE_TONE[conn]}>{t(STATE_LABEL[conn])}</Badge>
            {status?.connection.detail && <span className="text-muted-foreground">{status.connection.detail}</span>}
            <span className="text-muted-foreground">
              {status?.streaming === true
                ? t("obs.streaming")
                : status?.streaming === false
                  ? t("obs.notStreaming")
                  : t("obs.streamUnknown")}
            </span>
            {status?.scene && <span className="text-muted-foreground">{t("obs.sceneNow", { scene: status.scene })}</span>}
            {status?.stopPendingUntil && (
              <Badge variant="warn">
                {t("obs.stopPending", { time: new Date(status.stopPendingUntil).toLocaleTimeString() })}
              </Badge>
            )}
          </div>

          <div className="flex items-center gap-2">
            <Switch id="obs-enabled" checked={draft.enabled} onCheckedChange={(v) => set({ enabled: v })} />
            <Label htmlFor="obs-enabled">{t("obs.enabled")}</Label>
          </div>

          <div className="flex flex-col gap-2">
            <div className="flex items-center gap-2">
              <Switch id="obs-connect" checked={draft.connect} onCheckedChange={(v) => set({ connect: v })} />
              <Label htmlFor="obs-connect">{t("obs.connect")}</Label>
            </div>
            <span className="text-sm text-muted-foreground">{t("obs.connectHint")}</span>
            {draft.connect && (
              <div className="grid gap-2 sm:grid-cols-2">
                <div className="flex flex-col gap-1">
                  <Label htmlFor="obs-url">{t("obs.url")}</Label>
                  <Input
                    id="obs-url"
                    value={draft.url}
                    placeholder="ws://192.168.1.20:4455"
                    onChange={(e) => set({ url: e.target.value })}
                  />
                </div>
                <div className="flex flex-col gap-1">
                  <Label htmlFor="obs-pw">{t("obs.password")}</Label>
                  <SecretInput
                    id="obs-pw"
                    autoComplete="new-password"
                    value={password}
                    onChange={(e) => setPassword(e.target.value)}
                  />
                  {draft.hasPassword && (
                    <span className="flex items-center gap-2 text-sm text-muted-foreground">
                      {t("obs.passwordSet")}
                      <Button type="button" size="sm" variant="ghost" onClick={() => void clearPassword()}>
                        {t("obs.clearPassword")}
                      </Button>
                    </span>
                  )}
                </div>
              </div>
            )}
          </div>

          {sources.length > 1 && (
            <div className="flex max-w-xs flex-col gap-1">
              <Label htmlFor="obs-source">{t("chrome.programme")}</Label>
              <Select
                value={draft.sourceId === null ? "" : String(draft.sourceId)}
                onValueChange={(v) => set({ sourceId: Number(v), destinations: [], scenes: [] })}
              >
                <SelectTrigger id="obs-source">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {sources.map((s) => (
                    <SelectItem key={s.id} value={String(s.id)}>
                      {s.name}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
          )}

          <fieldset className="flex flex-col gap-1">
            <legend className="text-sm font-medium">{t("obs.destinations")}</legend>
            {onProgramme.length === 0 && <span className="text-sm text-muted-foreground">{t("obs.noDestinations")}</span>}
            {onProgramme.map((d) => {
              const id = Number(d.id);
              return (
                <label key={id} className="flex items-center gap-2 text-sm">
                  <Checkbox
                    checked={draft.destinations.includes(id)}
                    onCheckedChange={(v) => toggleDest(id, v === true)}
                  />
                  {d.name}
                </label>
              );
            })}
          </fieldset>

          <div className="flex items-center gap-2">
            <Switch id="obs-start" checked={draft.startOnStream} onCheckedChange={(v) => set({ startOnStream: v })} />
            <Label htmlFor="obs-start">{t("obs.startOnStream")}</Label>
          </div>
          <div className="flex flex-col gap-2">
            <div className="flex items-center gap-2">
              <Switch
                id="obs-stop"
                checked={draft.stopOnStream}
                onCheckedChange={(v) => set({ stopOnStream: v, stopAcknowledged: v ? draft.stopAcknowledged : false })}
              />
              <Label htmlFor="obs-stop">{t("obs.stopOnStream")}</Label>
            </div>
            {draft.stopOnStream && (
              <div className="flex flex-col gap-2 rounded border border-warn/50 bg-warn/5 p-2">
                <span className="text-sm">{t("obs.stopWarning")}</span>
                <label className="flex items-center gap-2 text-sm">
                  <Checkbox
                    checked={draft.stopAcknowledged}
                    onCheckedChange={(v) => set({ stopAcknowledged: v === true })}
                  />
                  {t("obs.stopAck")}
                </label>
                <div className="flex max-w-xs flex-col gap-1">
                  <Label htmlFor="obs-grace">{t("obs.grace")}</Label>
                  <Input
                    id="obs-grace"
                    type="number"
                    min={0}
                    max={3600}
                    value={draft.stopGraceSeconds}
                    onChange={(e) => set({ stopGraceSeconds: Math.max(0, Math.trunc(Number(e.target.value) || 0)) })}
                  />
                  <span className="text-sm text-muted-foreground">{t("obs.graceHint")}</span>
                </div>
              </div>
            )}
          </div>

          <div className="flex flex-col gap-2">
            <span className="text-sm font-medium">{t("obs.scenes")}</span>
            <span className="text-sm text-muted-foreground">{t("obs.scenesHint")}</span>
            {draft.scenes.map((rule, i) => (
              <div key={i} className="flex flex-wrap items-end gap-2">
                <div className="flex flex-col gap-1">
                  <Label htmlFor={`obs-scene-${i}`}>{t("obs.sceneName")}</Label>
                  <Input
                    id={`obs-scene-${i}`}
                    value={rule.scene}
                    onChange={(e) =>
                      set({ scenes: draft.scenes.map((r, j) => (j === i ? { ...r, scene: e.target.value } : r)) })
                    }
                  />
                </div>
                <div className="flex min-w-48 flex-col gap-1">
                  <Label htmlFor={`obs-preset-${i}`}>{t("obs.preset")}</Label>
                  <Select
                    value={rule.preset}
                    onValueChange={(v) => set({ scenes: draft.scenes.map((r, j) => (j === i ? { ...r, preset: v } : r)) })}
                  >
                    <SelectTrigger id={`obs-preset-${i}`}>
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      {presets.map((p) => (
                        <SelectItem key={p.id} value={p.id}>
                          {p.name}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                </div>
                <Button
                  type="button"
                  size="sm"
                  variant="ghost"
                  aria-label={t("obs.removeRule")}
                  onClick={() => set({ scenes: draft.scenes.filter((_, j) => j !== i) })}
                >
                  <Trash2 />
                </Button>
              </div>
            ))}
            <div>
              <Button
                type="button"
                size="sm"
                variant="outline"
                onClick={() => set({ scenes: [...draft.scenes, { scene: "", preset: presets[0]?.id ?? "" }] })}
              >
                <Plus /> {t("obs.addRule")}
              </Button>
            </div>
          </div>

          <span className="text-sm text-muted-foreground">{t("obs.scriptHint")}</span>

          <div>
            <Button type="button" size="sm" disabled={saving} onClick={() => void save()}>
              {saving ? <Loader2 className="animate-spin" /> : <Save />} {t("common.save")}
            </Button>
          </div>
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>{t("obs.lastActions")}</CardTitle>
        </CardHeader>
        <CardContent className="flex flex-col gap-2 text-sm">
          {(status?.lastActions.length ?? 0) === 0 && <span className="text-muted-foreground">{t("obs.noActions")}</span>}
          {status?.lastActions.map((a) => (
            <div key={a.at + a.action} className="flex flex-col gap-0.5">
              <span className="font-medium">
                {new Date(a.at).toLocaleTimeString()} ·{" "}
                {a.action === "scene"
                  ? t("obs.actionScene", { scene: a.scene ?? "" })
                  : a.action === "start"
                    ? t("obs.actionStart")
                    : t("obs.actionStop")}
              </span>
              {a.results.map((r) => (
                <span key={r.id} className="text-muted-foreground">
                  {r.name || `#${r.id}`}: {r.outcome}
                  {r.message ? ` — ${r.message}` : ""}
                </span>
              ))}
            </div>
          ))}
        </CardContent>
      </Card>
    </div>
  );
}
