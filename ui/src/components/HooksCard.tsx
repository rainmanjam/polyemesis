import { useCallback, useEffect, useState } from "react";
import { toast } from "sonner";
import { AlertTriangle, Loader2, Pencil, Plus, Send, Trash2, Webhook } from "lucide-react";
import { ConfirmDestructive } from "@/components/ConfirmDestructive";
import { useConfirm } from "@/hooks/useConfirm";
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
import { Switch } from "@/components/ui/switch";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { api } from "@/lib/api";
import { useT, type TranslationKey } from "@/lib/i18n";
import type { Hook, HookDelivery, HookMeta, HookTestResult, HookTrigger } from "@/lib/types";

/** Human labels for the wire names. The wire names are stored configuration and
 *  must never change; these are only what an operator reads — so the map holds
 *  a catalogue key and the render does the lookup, which keeps the vocabulary
 *  and its fifteen translations from drifting apart. A trigger a future release
 *  adds and this map does not name still renders: the wire name itself. */
const TRIGGER_LABELS: Record<string, TranslationKey> = {
  "ingest.published": "hooks.triggerIngestPublished",
  "ingest.disconnected": "hooks.triggerIngestDisconnected",
  "destination.up": "hooks.triggerDestinationUp",
  "destination.down": "hooks.triggerDestinationDown",
  "broadcast.fault": "hooks.triggerBroadcastFault",
};

const errText = (err: unknown, fallback: string) =>
  err instanceof Error && err.message ? err.message : fallback;

/** Lifecycle webhooks: one signed POST per transition, for a script rather than
 *  a person.
 *
 *  Deliberately a sibling of the alert rules rather than a mode of them. An
 *  alert coalesces and debounces because a human is reading it; a hook must do
 *  neither, because a script cannot act on the eleven events it was never
 *  given. Same card, same table, same buttons — the difference is in what the
 *  product promises about delivery, not in how it looks. */
export function HooksCard() {
  const t = useT();
  const [hooks, setHooks] = useState<Hook[]>([]);
  const [meta, setMeta] = useState<HookMeta | null>(null);
  const [loading, setLoading] = useState(true);
  const [draft, setDraft] = useState<Partial<Hook> | null>(null);
  // In flight, same flag every other mutating dialog in this console carries.
  // It matters more here than in most of them: a second create is not a
  // duplicate row an operator can delete and forget, it is a second signing
  // key that overwrites the first one on screen before anybody has copied it.
  const [busy, setBusy] = useState(false);
  const [testing, setTesting] = useState<number | null>(null);
  const [testResult, setTestResult] = useState<HookTestResult | null>(null);
  // Kept outside the dialog: the key must survive closing it, because the
  // server cannot re-issue it.
  const [newSecret, setNewSecret] = useState<string | null>(null);
  const [deliveries, setDeliveries] = useState<HookDelivery[]>([]);
  const [openDeliveries, setOpenDeliveries] = useState<number | null>(null);

  const load = useCallback(async () => {
    try {
      const [rows, m] = await Promise.all([api.hooks.list(), api.hooks.meta()]);
      setHooks(rows);
      setMeta(m);
    } catch (err) {
      toast.error(errText(err, t("hooks.loadFailed")));
    } finally {
      setLoading(false);
    }
  }, [t]);

  useEffect(() => {
    void load();
  }, [load]);

  const toggle = async (h: Hook, enabled: boolean) => {
    try {
      await api.hooks.update(h.id, { enabled });
      await load();
    } catch (err) {
      toast.error(errText(err, t("hooks.toggleFailed")));
    }
  };

  const confirmDelete = useConfirm<Hook>();

  const remove = async (h: Hook) => {
    try {
      await api.hooks.remove(h.id);
      toast.success(t("hooks.deleted"));
      await load();
    } catch (err) {
      toast.error(errText(err, t("hooks.deleteFailed")));
    }
  };

  // The test result is rendered in full rather than reduced to "sent". An
  // operator testing a hook is verifying a machine contract, and the only
  // useful answer is the exact bytes and the exact signature their receiver
  // will have to agree with.
  const test = async (h: Hook) => {
    setTesting(h.id);
    setTestResult(null);
    try {
      setTestResult(await api.hooks.test(h.id));
    } catch (err) {
      toast.error(errText(err, t("hooks.testFailed")));
    } finally {
      setTesting(null);
    }
  };

  const showDeliveries = async (h: Hook) => {
    if (openDeliveries === h.id) {
      setOpenDeliveries(null);
      return;
    }
    try {
      setDeliveries(await api.hooks.deliveries(h.id));
      setOpenDeliveries(h.id);
    } catch (err) {
      toast.error(errText(err, t("hooks.deliveriesFailed")));
    }
  };

  const save = async () => {
    if (!draft || busy) return;
    setBusy(true);
    try {
      if (draft.id) {
        await api.hooks.update(draft.id, draft);
        toast.success(t("hooks.updated"));
      } else {
        const created = await api.hooks.create({
          ...draft,
          url: draft.url ?? "",
        });
        setNewSecret(created.secret);
        toast.success(t("hooks.created"));
      }
      setDraft(null);
      await load();
    } catch (err) {
      toast.error(errText(err, t("hooks.saveFailed")));
    } finally {
      setBusy(false);
    }
  };

  const stats = meta?.stats;

  if (loading) {
    return (
      <div className="flex justify-center py-10">
        <Loader2 className="h-4 w-4 animate-spin text-muted-foreground" />
      </div>
    );
  }

  return (
    <div className="grid gap-3 lg:grid-cols-[minmax(0,1fr)_16rem]">
      <div className="space-y-3">
        <Card>
          <CardHeader className="flex-row items-center justify-between">
            <CardTitle>{t("hooks.title")}</CardTitle>
            <Button size="sm" onClick={() => setDraft({ enabled: true, triggers: [] })}>
              <Plus /> {t("hooks.new")}
            </Button>
          </CardHeader>
          <CardContent className="px-0 pb-0">
            {hooks.length === 0 ? (
              <div className="px-3 py-8 text-center text-[12px] text-muted-foreground">
                {t("hooks.empty")}
              </div>
            ) : (
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>{t("hooks.colWebhook")}</TableHead>
                    <TableHead>{t("hooks.colEndpoint")}</TableHead>
                    <TableHead>{t("hooks.colTriggers")}</TableHead>
                    <TableHead className="text-center">{t("hooks.colEnabled")}</TableHead>
                    <TableHead className="w-36" />
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {hooks.map((h) => (
                    <TableRow key={h.id}>
                      <TableCell>
                        <div className="text-[12px]">{h.name}</div>
                        {/* One key per FORM rather than a stem plus an "s",
                            which is the rule lib/i18n.ts states: substitution
                            is {name} and nothing else, and a language whose
                            plural does not work by suffixing cannot be served
                            by concatenation. */}
                        <div className="text-[10px] text-muted-foreground">
                          {t(h.hasSecret ? "hooks.signed" : "hooks.unsigned")} ·{" "}
                          {t(h.maxAttempts === 1 ? "hooks.attemptsOne" : "hooks.attempts", {
                            count: h.maxAttempts,
                          })}{" "}
                          · {t("hooks.timeout", { seconds: h.timeoutSeconds })}
                        </div>
                      </TableCell>
                      {/* The stored URL is never sent back; this is the mask,
                          and editing it is how you replace the endpoint. */}
                      <TableCell className="max-w-56 truncate font-mono text-[10px] text-muted-foreground">
                        {h.url}
                      </TableCell>
                      <TableCell>
                        {(h.triggers ?? []).length === 0 ? (
                          <Badge variant="outline">{t("hooks.allTriggers")}</Badge>
                        ) : (
                          <span className="text-[11px] text-muted-foreground">
                            {t(
                              (h.triggers ?? []).length === 1
                                ? "hooks.triggerCountOne"
                                : "hooks.triggerCount",
                              { count: (h.triggers ?? []).length },
                            )}
                          </span>
                        )}
                      </TableCell>
                      <TableCell className="text-center">
                        <Switch
                          checked={h.enabled}
                          onCheckedChange={(v) => toggle(h, v)}
                          aria-label={t("hooks.enableAria", { name: h.name })}
                        />
                      </TableCell>
                      <TableCell>
                        <div className="flex justify-end gap-0.5">
                          <Button
                            variant="ghost"
                            size="icon-sm"
                            onClick={() => showDeliveries(h)}
                            aria-label={t("hooks.deliveries")}
                            title={t("hooks.deliveries")}
                          >
                            <Webhook />
                          </Button>
                          <Button
                            variant="ghost"
                            size="icon-sm"
                            onClick={() => test(h)}
                            disabled={testing === h.id}
                            aria-label={t("hooks.sendTest")}
                            title={t("hooks.sendTestTitle")}
                          >
                            {testing === h.id ? <Loader2 className="animate-spin" /> : <Send />}
                          </Button>
                          <Button
                            variant="ghost"
                            size="icon-sm"
                            onClick={() => setDraft(h)}
                            aria-label={t("common.edit")}
                          >
                            <Pencil />
                          </Button>
                          <Button
                            variant="ghost"
                            size="icon-sm"
                            onClick={() => confirmDelete.ask(h)}
                            aria-label={t("common.delete")}
                            className="text-muted-foreground hover:text-down"
                          >
                            <Trash2 />
                          </Button>
                        </div>
                      </TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            )}
          </CardContent>
        </Card>

        {/* Shown once, and only once: the server cannot re-issue it. Rendered as
            a copyable block rather than a toast, because a toast that disappears
            after four seconds is how an operator loses a key they now have to
            regenerate. */}
        {newSecret && (
          <div className="rounded border border-warn/40 bg-warn/10 px-3 py-2 text-xs">
            <p className="mb-1 font-medium text-warn">{t("hooks.secretTitle")}</p>
            <code className="block break-all font-mono text-[11px]">{newSecret}</code>
            <Button
              variant="ghost"
              size="sm"
              className="mt-1"
              onClick={() => setNewSecret(null)}
            >
              {t("hooks.secretCopied")}
            </Button>
          </div>
        )}

        {/* The test result, in full. The operator is verifying a machine
            contract, so the exact bytes and the exact signature are the answer. */}
        {testResult && (
          <div className="space-y-1 rounded border border-border bg-muted/40 px-3 py-2 text-[11px]">
            <div className="flex items-center gap-2">
              <Badge
                variant={
                  testResult.status >= 200 && testResult.status < 300 ? "live" : "down"
                }
              >
                {t("hooks.httpStatus", {
                  status: testResult.status || t("hooks.noResponse"),
                })}
              </Badge>
              <span className="text-muted-foreground">
                {t("hooks.durationMs", { ms: testResult.durationMs })}
              </span>
            </div>
            <pre className="overflow-x-auto whitespace-pre-wrap font-mono">{testResult.body}</pre>
            <p className="break-all font-mono text-muted-foreground">
              {meta?.headers.signature}: {testResult.signature}
            </p>
          </div>
        )}

        {/* Recent deliveries. A hook that fires into a black hole is
            indistinguishable from one that does not fire at all, and this is
            the difference. */}
        {openDeliveries !== null && (
          <Card>
            <CardHeader>
              <CardTitle>{t("hooks.deliveries")}</CardTitle>
            </CardHeader>
            <CardContent>
              {deliveries.length === 0 ? (
                <p className="text-[11px] text-muted-foreground">{t("hooks.noDeliveries")}</p>
              ) : (
                <ul className="space-y-1 text-[11px]">
                  {deliveries.map((d) => (
                    <li key={d.id} className="flex items-center gap-2">
                      <Badge variant={d.error ? "down" : "outline"}>{d.trigger}</Badge>
                      <span className="text-muted-foreground">#{d.sequence}</span>
                      <span className="text-muted-foreground">{d.status || "—"}</span>
                      <span className="text-muted-foreground">
                        {t("hooks.durationMs", { ms: d.durationMs })}
                      </span>
                      {d.error && <span className="truncate text-down">{d.error}</span>}
                    </li>
                  ))}
                </ul>
              )}
            </CardContent>
          </Card>
        )}
      </div>

      <Card>
        <CardHeader>
          <CardTitle>{t("hooks.deliveryTitle")}</CardTitle>
        </CardHeader>
        <CardContent className="grid grid-cols-2 gap-2">
          <Stat label={t("hooks.statSent")} value={stats?.sent ?? 0} />
          <Stat
            label={t("hooks.statFailed")}
            value={stats?.failed ?? 0}
            tone={stats?.failed ? "down" : "muted"}
          />
          <Stat label={t("hooks.statEndpoints")} value={stats?.endpoints ?? 0} tone="muted" />
          <Stat label={t("hooks.statQueued")} value={stats?.queued ?? 0} tone="muted" />
          <Stat
            label={t("hooks.statDropped")}
            value={stats?.dropped ?? 0}
            tone={stats?.dropped ? "warn" : "muted"}
          />
          <Stat label={t("hooks.statRetries")} value={stats?.retries ?? 0} tone="muted" />
          {stats?.lastError && (
            <p className="col-span-2 flex items-start gap-1.5 rounded border border-down/50 bg-down/5 p-2 text-[10px] text-down">
              <AlertTriangle className="mt-0.5 h-3 w-3 shrink-0" />
              {stats.lastError}
            </p>
          )}
          {meta && (
            /* One sentence, one key. It used to wrap the header name in a
               <code>, which meant three fragments a translator cannot reorder --
               and the header name is monospace-looking on its own. */
            <p className="col-span-2 text-[10px] text-muted-foreground">
              {t("hooks.payloadNote", {
                version: meta.specVersion,
                header: meta.headers.signature,
              })}
            </p>
          )}
        </CardContent>
      </Card>

      <HookDialog
        draft={draft}
        meta={meta}
        busy={busy}
        onChange={setDraft}
        onClose={() => setDraft(null)}
        onSave={save}
      />

      <ConfirmDestructive
        open={confirmDelete.open}
        onOpenChange={confirmDelete.onOpenChange}
        subject={confirmDelete.target?.name ?? ""}
        title={t("hooks.deleteTitle")}
        description={t("hooks.deleteBody")}
        confirmLabel={t("hooks.deleteConfirm")}
        onConfirm={async () => {
          if (confirmDelete.target) await remove(confirmDelete.target);
        }}
      />
    </div>
  );
}

function Stat({
  label,
  value,
  tone = "default",
  className,
}: {
  label: string;
  value: number | string;
  tone?: "default" | "muted" | "down" | "warn";
  className?: string;
}) {
  const toneClass =
    tone === "down"
      ? "text-down"
      : tone === "warn"
        ? "text-warn"
        : tone === "muted"
          ? "text-muted-foreground"
          : "";
  return (
    <div className={className}>
      <div className="text-[10px] text-muted-foreground">{label}</div>
      <div className={`text-[13px] tabular-nums ${toneClass}`}>{value}</div>
    </div>
  );
}

function HookDialog({
  draft,
  meta,
  busy,
  onChange,
  onClose,
  onSave,
}: {
  draft: Partial<Hook> | null;
  meta: HookMeta | null;
  busy: boolean;
  onChange: (h: Partial<Hook>) => void;
  onClose: () => void;
  onSave: () => void;
}) {
  const t = useT();
  if (!draft) return null;
  const editing = Boolean(draft.id);
  const triggers = draft.triggers ?? [];

  const toggleTrigger = (tr: HookTrigger, on: boolean) => {
    onChange({
      ...draft,
      triggers: on ? [...triggers, tr] : triggers.filter((have) => have !== tr),
    });
  };

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t(editing ? "hooks.editTitle" : "hooks.newTitle")}</DialogTitle>
          <DialogDescription>{t("hooks.dialogIntro")}</DialogDescription>
        </DialogHeader>

        <div className="space-y-3">
          <div className="space-y-1">
            <Label htmlFor="hook-name">{t("hooks.name")}</Label>
            <Input
              id="hook-name"
              value={draft.name ?? ""}
              onChange={(e) => onChange({ ...draft, name: e.target.value })}
            />
          </div>

          <div className="space-y-1">
            <Label htmlFor="hook-url">{t("hooks.url")}</Label>
            <Input
              id="hook-url"
              value={draft.url ?? ""}
              onChange={(e) => onChange({ ...draft, url: e.target.value })}
              placeholder="https://ci.example.com/hooks/polyemesis"
            />
            {editing && (
              <p className="text-[10px] text-muted-foreground">{t("hooks.urlMasked")}</p>
            )}
          </div>

          <div className="space-y-1">
            <Label>{t("hooks.triggers")}</Label>
            <div className="grid grid-cols-2 gap-1">
              {(meta?.triggers ?? []).map((tr) => (
                <label key={tr} className="flex items-center gap-1.5 text-[11px]">
                  <Checkbox
                    checked={triggers.includes(tr)}
                    onCheckedChange={(v) => toggleTrigger(tr, Boolean(v))}
                  />
                  {TRIGGER_LABELS[tr] ? t(TRIGGER_LABELS[tr]) : tr}
                </label>
              ))}
            </div>
            <p className="text-[10px] text-muted-foreground">{t("hooks.triggersNoneHint")}</p>
          </div>

          <div className="flex gap-3">
            <div className="space-y-1">
              <Label htmlFor="hook-timeout">{t("hooks.timeoutLabel")}</Label>
              <Input
                id="hook-timeout"
                type="number"
                className="w-24"
                min={meta?.bounds.minTimeoutSeconds}
                max={meta?.bounds.maxTimeoutSeconds}
                value={draft.timeoutSeconds ?? 10}
                onChange={(e) => onChange({ ...draft, timeoutSeconds: Number(e.target.value) })}
              />
            </div>
            <div className="space-y-1">
              <Label htmlFor="hook-attempts">{t("hooks.attemptsLabel")}</Label>
              <Input
                id="hook-attempts"
                type="number"
                className="w-24"
                min={meta?.bounds.minAttempts}
                max={meta?.bounds.maxAttempts}
                value={draft.maxAttempts ?? 3}
                onChange={(e) => onChange({ ...draft, maxAttempts: Number(e.target.value) })}
              />
            </div>
          </div>
          <p className="text-[10px] text-muted-foreground">{t("hooks.retriesHint")}</p>
        </div>

        <DialogFooter>
          <Button variant="ghost" onClick={onClose} disabled={busy}>
            {t("common.cancel")}
          </Button>
          {/* Disabled while the request is in flight, the same shape as
              DestinationDialog's footer. Two fast clicks on Create used to make
              two hooks, and the UI shows one signing key -- the second -- so
              the first receiver was left holding a hook it could never verify
              a signature for, and the server cannot re-issue the key to fix it. */}
          <Button onClick={onSave} disabled={busy}>
            {busy && <Loader2 className="animate-spin" />}
            {t(editing ? "common.save" : "hooks.create")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
