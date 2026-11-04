import { useCallback, useEffect, useState } from "react";
import { toast } from "sonner";
import { Check, CloudUpload, Loader2, Pencil, Plus, RotateCcw, Send, Trash2, X } from "lucide-react";
import { ConfirmDestructive } from "@/components/ConfirmDestructive";
import { SecretInput } from "@/components/SecretInput";
import { useConfirm } from "@/hooks/useConfirm";
import { useDismissGuard } from "@/hooks/useDismissGuard";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Checkbox } from "@/components/ui/checkbox";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { api } from "@/lib/api";
import { useT, type TranslationKey } from "@/lib/i18n";
import type {
  StorageProvider,
  StorageTarget,
  StorageTargetBody,
  StorageTestResult,
} from "@/lib/types";

const errText = (err: unknown, fallback: string) =>
  err instanceof Error && err.message ? err.message : fallback;

const PROVIDERS: StorageProvider[] = ["s3", "r2", "b2", "minio", "wasabi"];
const PROVIDER_LABEL: Record<StorageProvider, string> = {
  s3: "AWS S3",
  r2: "Cloudflare R2",
  b2: "Backblaze B2",
  minio: "MinIO",
  wasabi: "Wasabi",
};
/** Example endpoints, shown as placeholders only -- never saved for the user. */
const PROVIDER_ENDPOINT: Record<StorageProvider, string> = {
  s3: "https://s3.us-east-1.amazonaws.com",
  r2: "https://ACCOUNT_ID.r2.cloudflarestorage.com",
  b2: "https://s3.us-west-004.backblazeb2.com",
  minio: "https://minio.lan:9000",
  wasabi: "https://s3.us-east-1.wasabisys.com",
};
const PROVIDER_REGION: Record<StorageProvider, string> = {
  s3: "us-east-1",
  r2: "auto",
  b2: "us-west-004",
  minio: "us-east-1",
  wasabi: "us-east-1",
};
const PREFIX_TOKENS = "{kind} {source} {yyyy} {mm} {dd}";
const STEP_KEY: Record<string, TranslationKey> = {
  put: "storage.stepPut",
  get: "storage.stepGet",
  delete: "storage.stepDelete",
};

/** The form. Everything a target stores, plus the secret field, which is only
 *  ever sent when typed. */
interface Draft {
  id?: number;
  name: string;
  provider: StorageProvider;
  endpoint: string;
  bucket: string;
  region: string;
  prefixTemplate: string;
  pathStyle: boolean;
  accessKeyId: string;
  secret: string;
  offloadStems: boolean;
  offloadTranscripts: boolean;
  offloadClips: boolean;
  verifyDownload: boolean;
  allowPrivateTarget: boolean;
  enabled: boolean;
  maxAttempts: number;
}

const blankDraft = (): Draft => ({
  name: "",
  provider: "s3",
  endpoint: "",
  bucket: "",
  region: "us-east-1",
  prefixTemplate: "",
  pathStyle: false,
  accessKeyId: "",
  secret: "",
  offloadStems: false,
  offloadTranscripts: false,
  offloadClips: false,
  verifyDownload: false,
  allowPrivateTarget: false,
  enabled: true,
  maxAttempts: 8,
});

const draftOf = (t: StorageTarget): Draft => ({ ...t, secret: "" });

const normalEndpoint = (e: string) => e.trim().replace(/\/+$/, "").toLowerCase();

/** The secret is bound to the address it was saved for (the server enforces
 *  it): change the endpoint, bucket or private-address opt-in and it has to be
 *  typed again. Exported for the test. */
export function secretRequired(saved: StorageTarget | undefined, d: Draft): { required: boolean; rebind: boolean } {
  if (!saved) return { required: true, rebind: false };
  const rebind =
    normalEndpoint(saved.endpoint) !== normalEndpoint(d.endpoint) ||
    saved.bucket !== d.bucket.trim() ||
    saved.pathStyle !== d.pathStyle ||
    saved.allowPrivateTarget !== d.allowPrivateTarget;
  return { required: rebind || !saved.hasSecret || !!saved.secretUnreadable, rebind };
}

function bodyOf(d: Draft): StorageTargetBody {
  const body: StorageTargetBody = {
    name: d.name.trim(),
    provider: d.provider,
    endpoint: d.endpoint.trim(),
    bucket: d.bucket.trim(),
    region: d.region.trim(),
    prefixTemplate: d.prefixTemplate.trim(),
    pathStyle: d.pathStyle,
    accessKeyId: d.accessKeyId.trim(),
    offloadStems: d.offloadStems,
    offloadTranscripts: d.offloadTranscripts,
    offloadClips: d.offloadClips,
    verifyDownload: d.verifyDownload,
    allowPrivateTarget: d.allowPrivateTarget,
    enabled: d.enabled,
    maxAttempts: d.maxAttempts,
  };
  if (d.secret.trim()) body.secretAccessKey = d.secret.trim();
  return body;
}

export function StorageTargetsCard() {
  const t = useT();
  const [targets, setTargets] = useState<StorageTarget[]>([]);
  const [available, setAvailable] = useState(true);
  const [loadFailed, setLoadFailed] = useState(false);
  const [draft, setDraft] = useState<Draft | null>(null);
  // The editor holds a secret access key: a stray scrim click or Esc must not
  // discard it (poka-yoke audit 2026-11-04, the destination dialog's pattern).
  const dismissGuard = useDismissGuard(() => toast.info(t("save.dirty")));
  const editorOpen = draft !== null;
  const resetDismissGuard = dismissGuard.reset;
  useEffect(() => {
    if (editorOpen) resetDismissGuard();
  }, [editorOpen, resetDismissGuard]);
  const [busy, setBusy] = useState(false);
  const [testing, setTesting] = useState(false);
  const [result, setResult] = useState<StorageTestResult | null>(null);
  const confirmDelete = useConfirm<StorageTarget>();

  const load = useCallback(async () => {
    try {
      const list = await api.storage.list();
      // A response without the list (a proxy page, an older server, a test
      // double that does not know the route) is a failed read, not a crash
      // and not "no targets".
      if (!list || !Array.isArray(list.targets)) throw new Error(t("storage.loadFailed"));
      setTargets(list.targets);
      setAvailable(list.available !== false);
      setLoadFailed(false);
    } catch (err) {
      setLoadFailed(true);
      toast.error(errText(err, t("storage.loadFailed")));
    }
  }, [t]);

  useEffect(() => {
    void load();
  }, [load]);

  const saved = draft?.id ? targets.find((x) => x.id === draft.id) : undefined;
  const secret = draft ? secretRequired(saved, draft) : { required: false, rebind: false };
  const secretMissing = secret.required && !draft?.secret.trim();

  const set = <K extends keyof Draft>(k: K, v: Draft[K]) => {
    setDraft((d) => (d ? { ...d, [k]: v } : d));
    setResult(null);
  };

  const save = async () => {
    if (!draft) return;
    setBusy(true);
    try {
      if (draft.id) await api.storage.update(draft.id, bodyOf(draft));
      else await api.storage.create(bodyOf(draft));
      toast.success(t("storage.saved"));
      setDraft(null);
      await load();
    } catch (err) {
      toast.error(errText(err, t("storage.saveFailed")));
    } finally {
      setBusy(false);
    }
  };

  const test = async () => {
    if (!draft) return;
    setTesting(true);
    setResult(null);
    try {
      setResult(await api.storage.test({ ...bodyOf(draft), ...(draft.id ? { id: draft.id } : {}) }));
    } catch (err) {
      toast.error(errText(err, t("storage.testFailedToRun")));
    } finally {
      setTesting(false);
    }
  };

  const retry = async (target: StorageTarget) => {
    try {
      const { retried } = await api.storage.retry(target.id);
      toast.success(t(retried === 1 ? "storage.retriedOne" : "storage.retried", { n: retried }));
      await load();
    } catch (err) {
      toast.error(errText(err, t("storage.retryFailed")));
    }
  };

  const remove = async (target: StorageTarget) => {
    try {
      await api.storage.remove(target.id);
      toast.success(t("storage.deleted"));
      confirmDelete.close();
      await load();
    } catch (err) {
      toast.error(errText(err, t("storage.deleteFailed")));
    }
  };

  return (
    <Card data-testid="storage-targets">
      <CardHeader className="flex flex-row items-center justify-between gap-2">
        <CardTitle className="flex items-center gap-1.5">
          <CloudUpload className="h-3.5 w-3.5" /> {t("storage.title")}
        </CardTitle>
        <Button size="sm" variant="secondary" onClick={() => setDraft(blankDraft())}>
          <Plus /> {t("storage.add")}
        </Button>
      </CardHeader>
      <CardContent className="flex flex-col gap-2">
        <p className="text-sm text-muted-foreground">{t("storage.intro")}</p>
        {!available && <p className="text-sm text-warn">{t("storage.unavailable")}</p>}
        {loadFailed && <p className="text-sm text-down">{t("storage.loadFailed")}</p>}
        {!loadFailed && targets.length === 0 && (
          <p className="text-sm text-muted-foreground">{t("storage.empty")}</p>
        )}
        {targets.map((x) => (
          <div key={x.id} className="flex flex-wrap items-center gap-1.5 border-t border-border pt-2" data-testid="storage-target">
            <span className="font-mono text-tiny">{x.name}</span>
            <Badge variant="outline">{PROVIDER_LABEL[x.provider] ?? x.provider}</Badge>
            {!x.enabled && <Badge variant="outline">{t("storage.badgeOff")}</Badge>}
            {x.secretUnreadable && <Badge variant="down">{t("storage.secretUnreadable")}</Badge>}
            {(x.counts.verified ?? 0) > 0 && (
              <Badge variant="live">{t((x.counts.verified ?? 0) === 1 ? "storage.countVerifiedOne" : "storage.countVerified", { n: x.counts.verified ?? 0 })}</Badge>
            )}
            {(x.counts.failed ?? 0) > 0 && (
              <Badge variant="warn">{t((x.counts.failed ?? 0) === 1 ? "storage.countFailedOne" : "storage.countFailed", { n: x.counts.failed ?? 0 })}</Badge>
            )}
            {(x.counts.stuck ?? 0) > 0 && (
              <Badge variant="down">{t((x.counts.stuck ?? 0) === 1 ? "storage.countStuckOne" : "storage.countStuck", { n: x.counts.stuck ?? 0 })}</Badge>
            )}
            {(x.counts.pending ?? 0) + (x.counts.uploading ?? 0) > 0 && (
              <Badge variant="outline">
                {t("storage.countPending", { n: (x.counts.pending ?? 0) + (x.counts.uploading ?? 0) })}
              </Badge>
            )}
            <span className="ml-auto flex items-center gap-0.5">
              {(x.counts.failed ?? 0) + (x.counts.stuck ?? 0) > 0 && (
                <Button variant="ghost" size="sm" onClick={() => void retry(x)}>
                  <RotateCcw /> {t("storage.retryNow")}
                </Button>
              )}
              <Button variant="ghost" size="icon-sm" aria-label={t("storage.edit")} onClick={() => { setResult(null); setDraft(draftOf(x)); }}>
                <Pencil />
              </Button>
              <Button variant="ghost" size="icon-sm" aria-label={t("storage.delete")} onClick={() => confirmDelete.ask(x)}>
                <Trash2 />
              </Button>
            </span>
          </div>
        ))}
      </CardContent>

      <Dialog open={draft !== null} onOpenChange={(o) => !o && setDraft(null)}>
        <DialogContent {...dismissGuard.contentProps} className="max-h-[90vh] overflow-y-auto">
          <DialogHeader>
            <DialogTitle>{draft?.id ? t("storage.dialogEdit") : t("storage.dialogNew")}</DialogTitle>
            <DialogDescription>{t("storage.intro")}</DialogDescription>
          </DialogHeader>
          {draft && (
            <div className="grid gap-2">
              <div className="grid grid-cols-2 gap-2">
                <div className="grid gap-1">
                  <Label htmlFor="st-name">{t("storage.name")}</Label>
                  <Input id="st-name" value={draft.name} onChange={(e) => set("name", e.target.value)} />
                </div>
                <div className="grid gap-1">
                  <Label htmlFor="st-provider">{t("storage.provider")}</Label>
                  <Select
                    value={draft.provider}
                    onValueChange={(v) => {
                      const p = v as StorageProvider;
                      setDraft((d) => (d ? { ...d, provider: p, region: PROVIDER_REGION[p], pathStyle: p === "minio" } : d));
                      setResult(null);
                    }}
                  >
                    <SelectTrigger id="st-provider">
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      {PROVIDERS.map((p) => (
                        <SelectItem key={p} value={p}>
                          {PROVIDER_LABEL[p]}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                </div>
              </div>
              <div className="grid gap-1">
                <Label htmlFor="st-endpoint">{t("storage.endpoint")}</Label>
                <Input
                  id="st-endpoint"
                  value={draft.endpoint}
                  placeholder={PROVIDER_ENDPOINT[draft.provider]}
                  onChange={(e) => set("endpoint", e.target.value)}
                />
              </div>
              <div className="grid grid-cols-2 gap-2">
                <div className="grid gap-1">
                  <Label htmlFor="st-bucket">{t("storage.bucket")}</Label>
                  <Input id="st-bucket" value={draft.bucket} onChange={(e) => set("bucket", e.target.value)} />
                </div>
                <div className="grid gap-1">
                  <Label htmlFor="st-region">{t("storage.region")}</Label>
                  <Input id="st-region" value={draft.region} onChange={(e) => set("region", e.target.value)} />
                </div>
              </div>
              <div className="grid gap-1">
                <Label htmlFor="st-prefix">{t("storage.prefix")}</Label>
                <Input
                  id="st-prefix"
                  value={draft.prefixTemplate}
                  placeholder="polyemesis/{kind}/{yyyy}/{mm}/{dd}"
                  onChange={(e) => set("prefixTemplate", e.target.value)}
                />
                <p className="text-tiny text-muted-foreground">
                  {t("storage.prefixTokens")} <code className="font-mono">{PREFIX_TOKENS}</code>
                </p>
              </div>
              <div className="flex items-center gap-2">
                <Switch id="st-path" checked={draft.pathStyle} onCheckedChange={(v) => set("pathStyle", v)} />
                <Label htmlFor="st-path">{t("storage.pathStyle")}</Label>
              </div>
              <div className="grid grid-cols-2 gap-2">
                <div className="grid gap-1">
                  <Label htmlFor="st-key">{t("storage.accessKeyId")}</Label>
                  <Input id="st-key" value={draft.accessKeyId} autoComplete="off" onChange={(e) => set("accessKeyId", e.target.value)} />
                </div>
                <div className="grid gap-1">
                  <Label htmlFor="st-secret">
                    {t("storage.secret")}
                    {secret.required ? " *" : ""}
                  </Label>
                  <SecretInput
                    id="st-secret"
                    value={draft.secret}
                    required={secret.required}
                    aria-invalid={secretMissing || undefined}
                    placeholder={secret.required ? "" : t("storage.secretKeep")}
                    onChange={(e) => set("secret", e.target.value)}
                  />
                </div>
              </div>
              {secret.rebind && (
                <p className="text-tiny text-warn" data-testid="storage-secret-rebind">
                  {t("storage.secretRebind")}
                </p>
              )}
              <fieldset className="flex flex-wrap items-center gap-3">
                <legend className="mb-1 text-tiny text-muted-foreground">{t("storage.extras")}</legend>
                {(
                  [
                    ["offloadStems", "storage.stems"],
                    ["offloadTranscripts", "storage.transcripts"],
                    ["offloadClips", "storage.clips"],
                  ] as const
                ).map(([k, label]) => (
                  <label key={k} className="flex items-center gap-1.5 text-sm">
                    <Checkbox checked={draft[k]} onCheckedChange={(v) => set(k, v === true)} />
                    {t(label)}
                  </label>
                ))}
              </fieldset>
              <label className="flex items-center gap-1.5 text-sm">
                <Checkbox checked={draft.verifyDownload} onCheckedChange={(v) => set("verifyDownload", v === true)} />
                {t("storage.verifyDownload")}
              </label>
              <label className="flex items-center gap-1.5 text-sm">
                <Checkbox checked={draft.allowPrivateTarget} onCheckedChange={(v) => set("allowPrivateTarget", v === true)} />
                {t("storage.allowPrivate")}
              </label>
              <div className="grid gap-1">
                <Label htmlFor="st-attempts">{t("storage.maxAttempts")}</Label>
                <Input
                  id="st-attempts"
                  type="number"
                  min={1}
                  max={100}
                  value={draft.maxAttempts}
                  onChange={(e) => set("maxAttempts", Math.min(100, Math.max(1, Number(e.target.value) || 1)))}
                />
                <p className="text-tiny text-muted-foreground">{t("storage.maxAttemptsHint")}</p>
              </div>
              <div className="flex items-center gap-2">
                <Switch id="st-enabled" checked={draft.enabled} onCheckedChange={(v) => set("enabled", v)} />
                <Label htmlFor="st-enabled">{t("storage.enabled")}</Label>
              </div>

              {result && (
                <div className="grid gap-1 rounded-md border border-border p-2" data-testid="storage-test-result">
                  <p className={result.ok ? "text-sm text-ok" : "text-sm text-down"}>
                    {result.ok
                      ? t("storage.testPassed")
                      : t("storage.testFailed", { step: t(STEP_KEY[result.failedStep ?? "put"]) })}
                  </p>
                  {result.steps.map((s) => (
                    <p key={s.step} className="flex items-start gap-1.5 text-tiny">
                      {s.ok ? <Check className="h-3 w-3 text-ok" /> : <X className="h-3 w-3 text-muted-foreground" />}
                      <span className="font-medium">{t(STEP_KEY[s.step])}</span>
                      <span className="text-muted-foreground">
                        {s.ok ? t("storage.stepOk") : s.skipped ? t("storage.stepSkipped") : s.error}
                      </span>
                    </p>
                  ))}
                </div>
              )}
            </div>
          )}
          <DialogFooter>
            <Button variant="secondary" onClick={() => void test()} disabled={testing || busy || secretMissing}>
              {testing ? <Loader2 className="animate-spin" /> : <Send />} {t("storage.test")}
            </Button>
            <Button onClick={() => void save()} disabled={busy || secretMissing}>
              {busy && <Loader2 className="animate-spin" />} {t("storage.save")}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <ConfirmDestructive
        open={confirmDelete.open}
        onOpenChange={confirmDelete.onOpenChange}
        subject={confirmDelete.target?.name ?? ""}
        title={t("storage.deleteTitle")}
        description={t("storage.deleteDescription")}
        onConfirm={async () => {
          if (confirmDelete.target) await remove(confirmDelete.target);
        }}
      />
    </Card>
  );
}
