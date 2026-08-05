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
import type { Hook, HookDelivery, HookMeta, HookTestResult, HookTrigger } from "@/lib/types";

/** Human labels for the wire names. The wire names are stored configuration and
 *  must never change; these are only what an operator reads. */
const TRIGGER_LABELS: Record<string, string> = {
  "ingest.published": "Stream started",
  "ingest.disconnected": "Stream stopped",
  "destination.up": "Destination live",
  "destination.down": "Destination stopped",
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
      toast.error(errText(err, "Could not load webhooks."));
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  const toggle = async (h: Hook, enabled: boolean) => {
    try {
      await api.hooks.update(h.id, { enabled });
      await load();
    } catch (err) {
      toast.error(errText(err, "Could not change the hook."));
    }
  };

  const confirmDelete = useConfirm<Hook>();

  const remove = async (h: Hook) => {
    try {
      await api.hooks.remove(h.id);
      toast.success("Webhook deleted.");
      await load();
    } catch (err) {
      toast.error(errText(err, "Could not delete the hook."));
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
      toast.error(errText(err, "The endpoint did not accept the test delivery."));
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
      toast.error(errText(err, "Could not read recent deliveries."));
    }
  };

  const save = async () => {
    if (!draft || busy) return;
    setBusy(true);
    try {
      if (draft.id) {
        await api.hooks.update(draft.id, draft);
        toast.success("Webhook updated.");
      } else {
        const created = await api.hooks.create({
          ...draft,
          url: draft.url ?? "",
        });
        setNewSecret(created.secret);
        toast.success("Webhook created.");
      }
      setDraft(null);
      await load();
    } catch (err) {
      toast.error(errText(err, "Could not save the hook."));
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
            <CardTitle>Lifecycle webhooks</CardTitle>
            <Button size="sm" onClick={() => setDraft({ enabled: true, triggers: [] })}>
              <Plus /> New webhook
            </Button>
          </CardHeader>
          <CardContent className="px-0 pb-0">
            {hooks.length === 0 ? (
              <div className="px-3 py-8 text-center text-[12px] text-muted-foreground">
                No webhooks. Add one and polyemesis will POST a signed JSON body the moment the
                stream starts or stops, or a destination goes up or down — one delivery per
                transition, in order, for a script to act on.
              </div>
            ) : (
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>Webhook</TableHead>
                    <TableHead>Endpoint</TableHead>
                    <TableHead>Subscribed to</TableHead>
                    <TableHead className="text-center">On</TableHead>
                    <TableHead className="w-36" />
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {hooks.map((h) => (
                    <TableRow key={h.id}>
                      <TableCell>
                        <div className="text-[12px]">{h.name}</div>
                        <div className="text-[10px] text-muted-foreground">
                          {h.hasSecret ? "signed" : "UNSIGNED"} · {h.maxAttempts} attempt
                          {h.maxAttempts === 1 ? "" : "s"} · {h.timeoutSeconds}s timeout
                        </div>
                      </TableCell>
                      {/* The stored URL is never sent back; this is the mask,
                          and editing it is how you replace the endpoint. */}
                      <TableCell className="max-w-56 truncate font-mono text-[10px] text-muted-foreground">
                        {h.url}
                      </TableCell>
                      <TableCell>
                        {(h.triggers ?? []).length === 0 ? (
                          <Badge variant="outline">everything</Badge>
                        ) : (
                          <span className="text-[11px] text-muted-foreground">
                            {(h.triggers ?? []).length} trigger
                            {(h.triggers ?? []).length === 1 ? "" : "s"}
                          </span>
                        )}
                      </TableCell>
                      <TableCell className="text-center">
                        <Switch
                          checked={h.enabled}
                          onCheckedChange={(v) => toggle(h, v)}
                          aria-label={`Enable ${h.name}`}
                        />
                      </TableCell>
                      <TableCell>
                        <div className="flex justify-end gap-0.5">
                          <Button
                            variant="ghost"
                            size="icon-sm"
                            onClick={() => showDeliveries(h)}
                            aria-label="Recent deliveries"
                            title="Recent deliveries"
                          >
                            <Webhook />
                          </Button>
                          <Button
                            variant="ghost"
                            size="icon-sm"
                            onClick={() => test(h)}
                            disabled={testing === h.id}
                            aria-label="Send test delivery"
                            title="Send a test delivery now"
                          >
                            {testing === h.id ? <Loader2 className="animate-spin" /> : <Send />}
                          </Button>
                          <Button
                            variant="ghost"
                            size="icon-sm"
                            onClick={() => setDraft(h)}
                            aria-label="Edit"
                          >
                            <Pencil />
                          </Button>
                          <Button
                            variant="ghost"
                            size="icon-sm"
                            onClick={() => confirmDelete.ask(h)}
                            aria-label="Delete"
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
            <p className="mb-1 font-medium text-warn">
              Signing key — copy it now, it cannot be shown again
            </p>
            <code className="block break-all font-mono text-[11px]">{newSecret}</code>
            <Button
              variant="ghost"
              size="sm"
              className="mt-1"
              onClick={() => setNewSecret(null)}
            >
              I have copied it
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
                HTTP {testResult.status || "no response"}
              </Badge>
              <span className="text-muted-foreground">{testResult.durationMs} ms</span>
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
              <CardTitle>Recent deliveries</CardTitle>
            </CardHeader>
            <CardContent>
              {deliveries.length === 0 ? (
                <p className="text-[11px] text-muted-foreground">
                  Nothing delivered yet. Deliveries appear here as transitions happen.
                </p>
              ) : (
                <ul className="space-y-1 text-[11px]">
                  {deliveries.map((d) => (
                    <li key={d.id} className="flex items-center gap-2">
                      <Badge variant={d.error ? "down" : "outline"}>{d.trigger}</Badge>
                      <span className="text-muted-foreground">#{d.sequence}</span>
                      <span className="text-muted-foreground">{d.status || "—"}</span>
                      <span className="text-muted-foreground">{d.durationMs} ms</span>
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
          <CardTitle>Delivery</CardTitle>
        </CardHeader>
        <CardContent className="grid grid-cols-2 gap-2">
          <Stat label="Sent" value={stats?.sent ?? 0} />
          <Stat label="Failed" value={stats?.failed ?? 0} tone={stats?.failed ? "down" : "muted"} />
          <Stat label="Endpoints" value={stats?.endpoints ?? 0} tone="muted" />
          <Stat label="Queued" value={stats?.queued ?? 0} tone="muted" />
          <Stat
            label="Dropped"
            value={stats?.dropped ?? 0}
            tone={stats?.dropped ? "warn" : "muted"}
          />
          <Stat label="Retries" value={stats?.retries ?? 0} tone="muted" />
          {stats?.lastError && (
            <p className="col-span-2 flex items-start gap-1.5 rounded border border-down/50 bg-down/5 p-2 text-[10px] text-down">
              <AlertTriangle className="mt-0.5 h-3 w-3 shrink-0" />
              {stats.lastError}
            </p>
          )}
          {meta && (
            <p className="col-span-2 text-[10px] text-muted-foreground">
              Payload {meta.specVersion}. Signature in{" "}
              <code className="font-mono">{meta.headers.signature}</code>, over the timestamp and
              body.
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
        title="Delete this webhook?"
        description="It stops delivering immediately. The endpoint is untouched, and you can recreate the hook — but its signing key cannot be recovered."
        confirmLabel="Delete webhook"
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
  if (!draft) return null;
  const editing = Boolean(draft.id);
  const triggers = draft.triggers ?? [];

  const toggleTrigger = (tr: HookTrigger, on: boolean) => {
    onChange({
      ...draft,
      triggers: on ? [...triggers, tr] : triggers.filter((t) => t !== tr),
    });
  };

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{editing ? "Edit webhook" : "New webhook"}</DialogTitle>
          <DialogDescription>
            One signed POST per transition, in order. Unlike an alert, nothing is coalesced or
            debounced — a script gets every edge.
          </DialogDescription>
        </DialogHeader>

        <div className="space-y-3">
          <div className="space-y-1">
            <Label htmlFor="hook-name">Name</Label>
            <Input
              id="hook-name"
              value={draft.name ?? ""}
              onChange={(e) => onChange({ ...draft, name: e.target.value })}
            />
          </div>

          <div className="space-y-1">
            <Label htmlFor="hook-url">Endpoint URL</Label>
            <Input
              id="hook-url"
              value={draft.url ?? ""}
              onChange={(e) => onChange({ ...draft, url: e.target.value })}
              placeholder="https://ci.example.com/hooks/polyemesis"
            />
            {editing && (
              <p className="text-[10px] text-muted-foreground">
                The stored URL is never shown in full. Leave the masked value to keep it, or type a
                new URL to replace it.
              </p>
            )}
          </div>

          <div className="space-y-1">
            <Label>Triggers</Label>
            <div className="grid grid-cols-2 gap-1">
              {(meta?.triggers ?? []).map((tr) => (
                <label key={tr} className="flex items-center gap-1.5 text-[11px]">
                  <Checkbox
                    checked={triggers.includes(tr)}
                    onCheckedChange={(v) => toggleTrigger(tr, Boolean(v))}
                  />
                  {TRIGGER_LABELS[tr] ?? tr}
                </label>
              ))}
            </div>
            <p className="text-[10px] text-muted-foreground">
              None selected means every trigger, including ones added by a future release.
            </p>
          </div>

          <div className="flex gap-3">
            <div className="space-y-1">
              <Label htmlFor="hook-timeout">Timeout (s)</Label>
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
              <Label htmlFor="hook-attempts">Attempts</Label>
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
          <p className="text-[10px] text-muted-foreground">
            Retries block this endpoint&rsquo;s own queue, because ordering is the promise. A 4xx is
            never retried — an endpoint saying the request is wrong will say it again.
          </p>
        </div>

        <DialogFooter>
          <Button variant="ghost" onClick={onClose} disabled={busy}>
            Cancel
          </Button>
          {/* Disabled while the request is in flight, the same shape as
              DestinationDialog's footer. Two fast clicks on Create used to make
              two hooks, and the UI shows one signing key -- the second -- so
              the first receiver was left holding a hook it could never verify
              a signature for, and the server cannot re-issue the key to fix it. */}
          <Button onClick={onSave} disabled={busy}>
            {busy && <Loader2 className="animate-spin" />}
            {editing ? "Save" : "Create"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
