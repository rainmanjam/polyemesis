import { useCallback, useEffect, useMemo, useState } from "react";
import { toast } from "sonner";
import { ConfirmDestructive } from "@/components/ConfirmDestructive";
import { HooksCard } from "@/components/HooksCard";
import { useConfirm } from "@/hooks/useConfirm";
import {
  AlertTriangle,
  Bell,
  CalendarClock,
  Loader2,
  Webhook,
  Pencil,
  Plus,
  Send,
  Trash2,
} from "lucide-react";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
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
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { PageHeader } from "@/components/AppLayout";
import { Stat } from "@/components/signature/Stat";
import { useLiveData } from "@/hooks/useLiveData";
import { autoApi } from "@/lib/autoApi";
import { timestamp } from "@/lib/format";

export type AlertFormat = "json" | "discord" | "slack";
export type AlertSeverity = "info" | "warning" | "critical";

export interface AlertRule {
  id: number;
  name: string;
  enabled: boolean;
  /** Always masked — the server never sends the real endpoint back. */
  url: string;
  format: AlertFormat;
  events: string[] | null;
  minSeverity: AlertSeverity;
  debounceSeconds: number;
  minIntervalSeconds: number;
  createdAt: string;
  updatedAt: string;
}

interface AlertStats {
  queued: number;
  dropped: number;
  coalesced: number;
  pending: number;
  sent: number;
  failed: number;
  retries: number;
  deferred: number;
  lastSent?: string;
  lastError?: string;
}

interface AlertsMeta {
  events: string[];
  formats: AlertFormat[];
  severities: AlertSeverity[];
  bounds: Record<string, number>;
  stats: AlertStats;
}

type ScheduleAction = "start" | "stop" | "playlist.start" | "playlist.stop";
type ScheduleKind = "once" | "daily" | "weekly";

// "start" names two different things now -- starting destinations and starting
// the failover playlist -- so anything that asks "does this row turn something
// on?" has to ask it of both, and anything that renders a target has to know
// which kind of target the row has. Comparing against the bare string "start"
// is what gave playlist.start the stopped badge and the "every destination"
// subtitle, which is precisely what its validation forbids.
const isPlaylistAction = (a: ScheduleAction) => a.startsWith("playlist.");
const isStartAction = (a: ScheduleAction) => a === "start" || a === "playlist.start";

export interface Schedule {
  id: number;
  name: string;
  enabled: boolean;
  action: ScheduleAction;
  kind: ScheduleKind;
  destinationIds: number[] | null;
  tz: string;
  atMinutes: number;
  days: number[] | null;
  runAt: string;
  graceSeconds: number;
  lastRunAt: string;
  createdAt: string;
  updatedAt: string;
  nextAt?: string;
  localTime: string;
}

interface ScheduleRun {
  id: number;
  name: string;
  action: ScheduleAction;
  fired: boolean;
  skipped: boolean;
  at: string;
  reason: string;
  targets?: number[];
  error?: string;
}

/* ---------------------------------------------------------------- labels */

/** Event types are stored strings, so the catalogue comes from the server and
 *  only the wording lives here. An unknown type still renders — as its own
 *  name — rather than vanishing from the picker. */
const EVENT_LABELS: Record<string, string> = {
  "destination.down": "Destination went down",
  "destination.recovered": "Destination recovered",
  "destination.falling_behind": "Destination falling behind realtime",
  "destination.caught_up": "Destination keeping up again",
  "ingest.lost": "Ingest lost",
  "ingest.recovered": "Ingest recovered",
  "failover.switched": "Failover switched source",
  "audio.clipping": "Audio clipping",
  "disk.low": "Disk running low",
  "disk.recovered": "Disk recovered",
  "loudness.out_of_compliance": "Loudness out of compliance",
  "loudness.recovered": "Loudness back in compliance",
  "auth.login.failed": "Repeated failed sign-ins",
  "auth.login.succeeded": "Signed in",
  "auth.password.changed": "Admin password changed",
  "auth.token.created": "API token created",
  "auth.token.revoked": "API token revoked",
  "settings.changed": "Settings changed",
  "clip.captured": "Clip captured",
};

const eventLabel = (t: string) => EVENT_LABELS[t] ?? t;

const WEEKDAYS = ["Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"];

function hhmm(minutes: number): string {
  const m = Math.max(0, Math.min(1439, Math.round(minutes)));
  return `${String(Math.floor(m / 60)).padStart(2, "0")}:${String(m % 60).padStart(2, "0")}`;
}

function minutesOf(value: string): number {
  const [h, m] = value.split(":");
  const mins = Number(h) * 60 + Number(m);
  return Number.isFinite(mins) ? Math.max(0, Math.min(1439, mins)) : 0;
}

/** The browser's own zone, offered as the default so nobody has to know that
 *  an empty zone means UTC on the server. */
function browserZone(): string {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
  } catch {
    return "UTC";
  }
}

/** `runAt` moves over the wire as UTC RFC3339; <input type="datetime-local">
 *  speaks local wall clock with no zone at all. */
function toLocalInput(iso: string): string {
  const d = new Date(iso);
  if (!iso || Number.isNaN(d.getTime())) return "";
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

function fromLocalInput(value: string): string {
  if (!value) return new Date(0).toISOString();
  const d = new Date(value);
  return Number.isNaN(d.getTime()) ? new Date(0).toISOString() : d.toISOString();
}

function describeSchedule(s: Schedule): string {
  const zone = s.tz || "UTC";
  switch (s.kind) {
    case "daily":
      return `Every day at ${s.localTime || hhmm(s.atMinutes)} ${zone}`;
    case "weekly": {
      const days = (s.days ?? []).map((d) => WEEKDAYS[d] ?? d).join(", ");
      return `${days || "no days"} at ${s.localTime || hhmm(s.atMinutes)} ${zone}`;
    }
    default:
      return `Once, ${timestamp(s.runAt)}`;
  }
}

const errText = (err: unknown, fallback: string) =>
  err instanceof Error && err.message ? err.message : fallback;

/* ================================================================== page */

/** Alerting and scheduling: the two things that run when nobody is watching.
 *
 *  Both halves are deliberately on one page — an operator setting up an
 *  unattended stream wants "tell me when it breaks" and "start it at seven"
 *  in the same sitting. */
export function AutomationPage() {
  const [rules, setRules] = useState<AlertRule[]>([]);
  const [meta, setMeta] = useState<AlertsMeta | null>(null);
  const [schedules, setSchedules] = useState<Schedule[]>([]);
  const [runs, setRuns] = useState<ScheduleRun[]>([]);
  const [loading, setLoading] = useState(true);

  const [ruleDraft, setRuleDraft] = useState<AlertRule | null>(null);
  const [scheduleDraft, setScheduleDraft] = useState<Schedule | null>(null);

  const load = useCallback(() => {
    Promise.all([
      autoApi.get<AlertRule[]>("/alerts/rules"),
      autoApi.get<AlertsMeta>("/alerts/meta"),
      autoApi.get<Schedule[]>("/schedules"),
      autoApi.get<ScheduleRun[]>("/schedules/runs"),
    ])
      .then(([r, m, s, runList]) => {
        setRules(r ?? []);
        setMeta(m);
        setSchedules(s ?? []);
        setRuns(runList ?? []);
      })
      .catch((err) => toast.error(errText(err, "Could not load automation settings.")))
      .finally(() => setLoading(false));
  }, []);

  useEffect(load, [load]);

  // The scheduler sweeps every 20 s, so the "next fire" column and the run log
  // go stale on their own. Cheap reads, and the alternative is a page that
  // quietly lies about when the stream starts.
  useEffect(() => {
    const t = window.setInterval(() => {
      autoApi.get<Schedule[]>("/schedules").then((s) => setSchedules(s ?? [])).catch(() => {});
      autoApi.get<ScheduleRun[]>("/schedules/runs").then((r) => setRuns(r ?? [])).catch(() => {});
    }, 15000);
    return () => window.clearInterval(t);
  }, []);

  return (
    <div className="p-3">
      <PageHeader
        title="Automation"
        subtitle="Webhooks that tell you something broke, and schedules that run the show without you."
      />

      {loading ? (
        <div className="flex justify-center py-10">
          <Loader2 className="h-4 w-4 animate-spin text-muted-foreground" />
        </div>
      ) : (
        <Tabs defaultValue="alerts">
          <TabsList>
            <TabsTrigger value="alerts">
              <Bell className="h-3.5 w-3.5" /> Alerts
            </TabsTrigger>
            <TabsTrigger value="schedules">
              <CalendarClock className="h-3.5 w-3.5" /> Schedules
            </TabsTrigger>
            {/* A sibling of Alerts, not a mode of it. An alert is for a person
                and coalesces; a hook is for a script and must not. */}
            <TabsTrigger value="hooks">
              <Webhook className="h-3.5 w-3.5" /> Webhooks
            </TabsTrigger>
          </TabsList>

          <TabsContent value="alerts">
            <AlertRules
              rules={rules}
              meta={meta}
              onReload={load}
              onEdit={setRuleDraft}
              onNew={() =>
                setRuleDraft({
                  id: 0,
                  name: "",
                  enabled: true,
                  url: "",
                  format: "json",
                  events: [],
                  minSeverity: "info",
                  debounceSeconds: 10,
                  minIntervalSeconds: 30,
                  createdAt: "",
                  updatedAt: "",
                })
              }
            />
          </TabsContent>

          <TabsContent value="schedules">
            <Schedules
              schedules={schedules}
              runs={runs}
              onReload={load}
              onEdit={setScheduleDraft}
              onNew={() =>
                setScheduleDraft({
                  id: 0,
                  name: "",
                  enabled: true,
                  action: "start",
                  kind: "daily",
                  destinationIds: [],
                  tz: browserZone(),
                  atMinutes: 19 * 60,
                  days: [1, 2, 3, 4, 5],
                  runAt: new Date(0).toISOString(),
                  graceSeconds: 300,
                  lastRunAt: "",
                  createdAt: "",
                  updatedAt: "",
                  localTime: "19:00",
                })
              }
            />
          </TabsContent>
          <TabsContent value="hooks">
            <HooksCard />
          </TabsContent>
        </Tabs>
      )}

      {ruleDraft && meta && (
        <RuleDialog
          draft={ruleDraft}
          meta={meta}
          onClose={() => setRuleDraft(null)}
          onSaved={() => {
            setRuleDraft(null);
            load();
          }}
        />
      )}

      {scheduleDraft && (
        <ScheduleDialog
          draft={scheduleDraft}
          onClose={() => setScheduleDraft(null)}
          onSaved={() => {
            setScheduleDraft(null);
            load();
          }}
        />
      )}
    </div>
  );
}

/* --------------------------------------------------------------- alerts */

function AlertRules({
  rules,
  meta,
  onReload,
  onEdit,
  onNew,
}: {
  rules: AlertRule[];
  meta: AlertsMeta | null;
  onReload: () => void;
  onEdit: (r: AlertRule) => void;
  onNew: () => void;
}) {
  const [testing, setTesting] = useState<number | null>(null);

  const toggle = async (r: AlertRule, enabled: boolean) => {
    try {
      await autoApi.put<AlertRule>(`/alerts/rules/${r.id}`, { enabled });
      onReload();
    } catch (err) {
      toast.error(errText(err, "Could not change the rule."));
    }
  };

  const confirmDelete = useConfirm<AlertRule>();

  const remove = async (r: AlertRule) => {
    try {
      await autoApi.del(`/alerts/rules/${r.id}`);
      toast.success("Alert rule deleted.");
      onReload();
    } catch (err) {
      toast.error(errText(err, "Could not delete the rule."));
    }
  };

  // The button this whole feature lives or dies by: nobody trusts a webhook
  // they have not seen fire, and a rule that is never tested is a rule that
  // discovers it is misconfigured during the outage it was meant to report.
  const test = async (r: AlertRule) => {
    setTesting(r.id);
    try {
      await autoApi.post(`/alerts/rules/${r.id}/test`, {});
      toast.success(`Test alert delivered to ${r.name}. Check the channel.`);
    } catch (err) {
      toast.error(errText(err, "The endpoint did not accept the test message."));
    } finally {
      setTesting(null);
    }
  };

  const stats = meta?.stats;

  return (
    <div className="grid gap-3 lg:grid-cols-[minmax(0,1fr)_16rem]">
      <Card>
        <CardHeader className="flex-row items-center justify-between">
          <CardTitle>Webhook rules</CardTitle>
          <Button size="sm" onClick={onNew}>
            <Plus /> New rule
          </Button>
        </CardHeader>
        <CardContent className="px-0 pb-0">
          {rules.length === 0 ? (
            <div className="px-3 py-8 text-center text-[12px] text-muted-foreground">
              No alert rules. Add one with a Discord, Slack or plain JSON webhook URL and
              polyemesis will tell you when a destination drops, the ingest goes away, or the disk
              fills.
            </div>
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Rule</TableHead>
                  <TableHead>Endpoint</TableHead>
                  <TableHead>Subscribed to</TableHead>
                  <TableHead className="text-center">On</TableHead>
                  <TableHead className="w-28" />
                </TableRow>
              </TableHeader>
              <TableBody>
                {rules.map((r) => (
                  <TableRow key={r.id}>
                    <TableCell>
                      <div className="text-[12px]">{r.name}</div>
                      <div className="text-[10px] text-muted-foreground">
                        {r.format} · at least {r.minSeverity} · coalesced {r.debounceSeconds}s ·
                        max 1 per {r.minIntervalSeconds}s
                      </div>
                    </TableCell>
                    {/* The stored URL is never sent back; this is the mask, and
                        editing it is how you replace the endpoint. */}
                    <TableCell className="max-w-56 truncate font-mono text-[10px] text-muted-foreground">
                      {r.url}
                    </TableCell>
                    <TableCell>
                      {(r.events ?? []).length === 0 ? (
                        <Badge variant="outline">everything</Badge>
                      ) : (
                        <span className="text-[11px] text-muted-foreground">
                          {(r.events ?? []).length} event
                          {(r.events ?? []).length === 1 ? "" : "s"}
                        </span>
                      )}
                    </TableCell>
                    <TableCell className="text-center">
                      <Switch
                        checked={r.enabled}
                        onCheckedChange={(v) => toggle(r, v)}
                        aria-label={`Enable ${r.name}`}
                      />
                    </TableCell>
                    <TableCell>
                      <div className="flex justify-end gap-0.5">
                        <Button
                          variant="ghost"
                          size="icon-sm"
                          onClick={() => test(r)}
                          disabled={testing === r.id}
                          aria-label="Send test alert"
                          title="Send a test alert now"
                        >
                          {testing === r.id ? <Loader2 className="animate-spin" /> : <Send />}
                        </Button>
                        <Button
                          variant="ghost"
                          size="icon-sm"
                          onClick={() => onEdit(r)}
                          aria-label="Edit"
                        >
                          <Pencil />
                        </Button>
                        <Button
                          variant="ghost"
                          size="icon-sm"
                          onClick={() => confirmDelete.ask(r)}
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

      <Card>
        <CardHeader>
          <CardTitle>Delivery</CardTitle>
        </CardHeader>
        <CardContent className="grid grid-cols-2 gap-2">
          <Stat label="Sent" value={stats?.sent ?? 0} />
          <Stat label="Failed" value={stats?.failed ?? 0} tone={stats?.failed ? "down" : "muted"} />
          <Stat label="Coalesced" value={stats?.coalesced ?? 0} tone="muted" />
          <Stat label="Pending" value={stats?.pending ?? 0} tone="muted" />
          <Stat
            label="Dropped"
            value={stats?.dropped ?? 0}
            tone={stats?.dropped ? "warn" : "muted"}
          />
          <Stat label="Retries" value={stats?.retries ?? 0} tone="muted" />
          {stats?.lastSent && (
            <Stat
              className="col-span-2"
              label="Last delivery"
              value={timestamp(stats.lastSent)}
              tone="muted"
            />
          )}
          {stats?.lastError && (
            <p className="col-span-2 flex items-start gap-1.5 rounded border border-down/50 bg-down/5 p-2 text-[10px] text-down">
              <AlertTriangle className="mt-0.5 h-3 w-3 shrink-0" />
              {stats.lastError}
            </p>
          )}
        </CardContent>
      </Card>
      <ConfirmDestructive
        open={confirmDelete.open}
        onOpenChange={confirmDelete.onOpenChange}
        subject={confirmDelete.target?.name ?? ""}
        title="Delete this alert rule?"
        description="The rule stops firing. Its webhook endpoint is untouched, and you can recreate the rule."
        confirmLabel="Delete rule"
        onConfirm={async () => {
          if (confirmDelete.target) await remove(confirmDelete.target);
        }}
      />
    </div>
  );
}

function RuleDialog({
  draft,
  meta,
  onClose,
  onSaved,
}: {
  draft: AlertRule;
  meta: AlertsMeta;
  onClose: () => void;
  onSaved: () => void;
}) {
  const [form, setForm] = useState<AlertRule>(draft);
  const [saving, setSaving] = useState(false);
  const editing = draft.id > 0;
  const events = form.events ?? [];

  const toggleEvent = (t: string) =>
    setForm((f) => {
      const cur = f.events ?? [];
      return { ...f, events: cur.includes(t) ? cur.filter((x) => x !== t) : [...cur, t] };
    });

  const save = async () => {
    setSaving(true);
    try {
      const body = {
        name: form.name,
        // Left as the mask when the operator did not touch it; the server
        // reads that as "keep the endpoint you already have".
        url: form.url,
        format: form.format,
        events,
        minSeverity: form.minSeverity,
        debounceSeconds: form.debounceSeconds,
        minIntervalSeconds: form.minIntervalSeconds,
        enabled: form.enabled,
      };
      if (editing) await autoApi.put(`/alerts/rules/${form.id}`, body);
      else await autoApi.post("/alerts/rules", body);
      toast.success(editing ? "Alert rule saved." : "Alert rule created. Send it a test.");
      onSaved();
    } catch (err) {
      toast.error(errText(err, "Could not save the rule."));
    } finally {
      setSaving(false);
    }
  };

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{editing ? "Edit alert rule" : "New alert rule"}</DialogTitle>
          <DialogDescription>
            One endpoint and what it wants to hear about. Everything raised about the same subject
            inside the coalescing window arrives as a single message with a count.
          </DialogDescription>
        </DialogHeader>

        <div className="flex max-h-[60vh] flex-col gap-3 overflow-y-auto pr-1">
          <div className="flex flex-col gap-1">
            <Label htmlFor="rule-name">Name</Label>
            <Input
              id="rule-name"
              value={form.name}
              maxLength={meta.bounds.maxNameLen || 120}
              placeholder="Discord — show alerts"
              onChange={(e) => setForm({ ...form, name: e.target.value })}
            />
          </div>

          <div className="flex flex-col gap-1">
            <Label htmlFor="rule-url">Webhook URL</Label>
            <Input
              id="rule-url"
              value={form.url}
              spellCheck={false}
              placeholder="https://discord.com/api/webhooks/…"
              onChange={(e) => setForm({ ...form, url: e.target.value })}
            />
            <span className="text-[10px] text-muted-foreground">
              {editing
                ? "Shown masked because the secret lives in the path. Leave it alone to keep the current endpoint, or paste a new URL to replace it."
                : "Discord and Slack both give you one under channel settings."}
            </span>
          </div>

          <div className="grid grid-cols-2 gap-3">
            <div className="flex flex-col gap-1">
              <Label>Payload format</Label>
              <Select
                value={form.format}
                onValueChange={(v) => setForm({ ...form, format: v as AlertFormat })}
              >
                <SelectTrigger>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {meta.formats.map((f) => (
                    <SelectItem key={f} value={f}>
                      {f === "json" ? "Plain JSON" : f === "discord" ? "Discord" : "Slack"}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
            <div className="flex flex-col gap-1">
              <Label>Minimum severity</Label>
              <Select
                value={form.minSeverity}
                onValueChange={(v) => setForm({ ...form, minSeverity: v as AlertSeverity })}
              >
                <SelectTrigger>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {meta.severities.map((sv) => (
                    <SelectItem key={sv} value={sv}>
                      {sv}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
          </div>

          <div className="flex flex-col gap-1.5">
            <Label>Events</Label>
            <p className="text-[10px] text-muted-foreground">
              None selected means every event, which is the useful default for a first rule.
            </p>
            <div className="grid gap-1.5 sm:grid-cols-2">
              {meta.events.map((t) => (
                <label key={t} className="flex items-center gap-2 text-[11px]">
                  <Checkbox checked={events.includes(t)} onCheckedChange={() => toggleEvent(t)} />
                  {eventLabel(t)}
                </label>
              ))}
            </div>
          </div>

          <div className="grid grid-cols-2 gap-3">
            <div className="flex flex-col gap-1">
              <Label htmlFor="rule-debounce">Coalescing window (seconds)</Label>
              <Input
                id="rule-debounce"
                type="number"
                min={meta.bounds.minDebounceSeconds || 1}
                max={meta.bounds.maxDebounceSeconds || 3600}
                value={form.debounceSeconds}
                onChange={(e) => setForm({ ...form, debounceSeconds: Number(e.target.value) })}
              />
            </div>
            <div className="flex flex-col gap-1">
              <Label htmlFor="rule-interval">Minimum gap between messages (seconds)</Label>
              <Input
                id="rule-interval"
                type="number"
                min={meta.bounds.minIntervalSeconds || 1}
                max={meta.bounds.maxIntervalSeconds || 86400}
                value={form.minIntervalSeconds}
                onChange={(e) => setForm({ ...form, minIntervalSeconds: Number(e.target.value) })}
              />
            </div>
          </div>
        </div>

        <DialogFooter>
          <Button variant="ghost" size="sm" onClick={onClose}>
            Cancel
          </Button>
          <Button size="sm" onClick={save} disabled={saving || !form.name.trim()}>
            {saving && <Loader2 className="animate-spin" />}
            {editing ? "Save rule" : "Create rule"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

/* ------------------------------------------------------------ schedules */

function Schedules({
  schedules,
  runs,
  onReload,
  onEdit,
  onNew,
}: {
  schedules: Schedule[];
  runs: ScheduleRun[];
  onReload: () => void;
  onEdit: (s: Schedule) => void;
  onNew: () => void;
}) {
  const { status } = useLiveData();
  const destNames = useMemo(() => {
    const m = new Map<number, string>();
    for (const d of status?.destinations ?? []) m.set(d.id, d.name);
    return m;
  }, [status]);

  const toggle = async (s: Schedule, enabled: boolean) => {
    try {
      await autoApi.put(`/schedules/${s.id}`, {
        name: s.name,
        enabled,
        action: s.action,
        kind: s.kind,
        destinationIds: s.destinationIds ?? [],
        tz: s.tz,
        atMinutes: s.atMinutes,
        days: s.days ?? [],
        runAt: s.runAt,
        graceSeconds: s.graceSeconds,
      });
      onReload();
    } catch (err) {
      toast.error(errText(err, "Could not change the schedule."));
    }
  };

  const confirmDelete = useConfirm<Schedule>();

  const remove = async (s: Schedule) => {
    try {
      await autoApi.del(`/schedules/${s.id}`);
      toast.success("Schedule deleted.");
      onReload();
    } catch (err) {
      toast.error(errText(err, "Could not delete the schedule."));
    }
  };

  const targets = (s: Schedule) => {
    // A playlist schedule carries NO destinations, by design: the server
    // refuses one that names any, and the dialog clears them. Falling through
    // to the destination wording below would then print "every destination"
    // under every playlist row -- telling the operator this schedule does the
    // one thing its validation exists to forbid.
    if (isPlaylistAction(s.action)) return "the failover playlist";
    const ids = s.destinationIds ?? [];
    if (ids.length === 0) return "every destination";
    return ids.map((id) => destNames.get(id) ?? `#${id}`).join(", ");
  };

  return (
    <div className="grid gap-3 lg:grid-cols-[minmax(0,1fr)_18rem]">
      <Card>
        <CardHeader className="flex-row items-center justify-between">
          <CardTitle>Schedules</CardTitle>
          <Button size="sm" onClick={onNew}>
            <Plus /> New schedule
          </Button>
        </CardHeader>
        <CardContent className="px-0 pb-0">
          {schedules.length === 0 ? (
            <div className="px-3 py-8 text-center text-[12px] text-muted-foreground">
              No schedules. A schedule flips the same enabled switch you would click, so a
              scheduled start is indistinguishable from a manual one.
            </div>
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Schedule</TableHead>
                  <TableHead>When</TableHead>
                  <TableHead>Next</TableHead>
                  <TableHead className="text-center">On</TableHead>
                  <TableHead className="w-20" />
                </TableRow>
              </TableHeader>
              <TableBody>
                {schedules.map((s) => (
                  <TableRow key={s.id}>
                    <TableCell>
                      <div className="flex items-center gap-1.5 text-[12px]">
                        <Badge variant={isStartAction(s.action) ? "live" : "outline"}>
                          {s.action}
                        </Badge>
                        {s.name}
                      </div>
                      <div className="truncate text-[10px] text-muted-foreground">
                        {targets(s)}
                      </div>
                    </TableCell>
                    <TableCell className="text-[11px] text-muted-foreground">
                      {describeSchedule(s)}
                    </TableCell>
                    <TableCell className="tnum font-mono text-[11px]">
                      {s.nextAt ? (
                        timestamp(s.nextAt)
                      ) : (
                        <span className="text-subtle-foreground">never again</span>
                      )}
                    </TableCell>
                    <TableCell className="text-center">
                      <Switch
                        checked={s.enabled}
                        onCheckedChange={(v) => toggle(s, v)}
                        aria-label={`Enable ${s.name}`}
                      />
                    </TableCell>
                    <TableCell>
                      <div className="flex justify-end gap-0.5">
                        <Button
                          variant="ghost"
                          size="icon-sm"
                          onClick={() => onEdit(s)}
                          aria-label="Edit"
                        >
                          <Pencil />
                        </Button>
                        <Button
                          variant="ghost"
                          size="icon-sm"
                          onClick={() => confirmDelete.ask(s)}
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

      <Card>
        <CardHeader>
          <CardTitle>Last sweep</CardTitle>
        </CardHeader>
        <CardContent className="flex flex-col gap-2">
          {runs.length === 0 ? (
            <p className="text-[11px] text-muted-foreground">
              Nothing has fired yet. The scheduler sweeps every 20 seconds.
            </p>
          ) : (
            runs.map((r) => (
              <div key={`${r.id}-${r.at}`} className="flex flex-col gap-0.5 border-b border-border pb-2 last:border-0">
                <div className="flex items-center justify-between gap-2">
                  <span className="truncate text-[11px]">{r.name}</span>
                  <Badge variant={r.error ? "down" : r.fired ? "live" : "outline"}>
                    {r.error ? "error" : r.fired ? "fired" : "skipped"}
                  </Badge>
                </div>
                <span className="tnum font-mono text-[10px] text-muted-foreground">
                  {timestamp(r.at)}
                </span>
                <span className="text-[10px] text-muted-foreground">{r.error ?? r.reason}</span>
              </div>
            ))
          )}
          <p className="text-[10px] text-subtle-foreground">
            An occurrence missed because the server was down is marked handled and skipped rather
            than fired late — a stream that starts itself four hours late is worse than one that
            never started.
          </p>
        </CardContent>
      </Card>
      <ConfirmDestructive
        open={confirmDelete.open}
        onOpenChange={confirmDelete.onOpenChange}
        subject={confirmDelete.target?.name ?? ""}
        title="Delete this schedule?"
        description="The schedule stops running. Anything it already started is unaffected."
        confirmLabel="Delete schedule"
        onConfirm={async () => {
          if (confirmDelete.target) await remove(confirmDelete.target);
        }}
      />
    </div>
  );
}

function ScheduleDialog({
  draft,
  onClose,
  onSaved,
}: {
  draft: Schedule;
  onClose: () => void;
  onSaved: () => void;
}) {
  const { status } = useLiveData();
  const [form, setForm] = useState<Schedule>(draft);
  const [saving, setSaving] = useState(false);
  const editing = draft.id > 0;
  const days = form.days ?? [];
  const dests = form.destinationIds ?? [];

  const toggleDay = (d: number) =>
    setForm((f) => {
      const cur = f.days ?? [];
      return { ...f, days: cur.includes(d) ? cur.filter((x) => x !== d) : [...cur, d] };
    });

  const toggleDest = (id: number) =>
    setForm((f) => {
      const cur = f.destinationIds ?? [];
      return {
        ...f,
        destinationIds: cur.includes(id) ? cur.filter((x) => x !== id) : [...cur, id],
      };
    });

  const save = async () => {
    setSaving(true);
    try {
      const body = {
        name: form.name,
        enabled: form.enabled,
        action: form.action,
        kind: form.kind,
        destinationIds: dests,
        tz: form.tz,
        atMinutes: form.atMinutes,
        days,
        runAt: form.runAt,
        graceSeconds: form.graceSeconds,
      };
      if (editing) await autoApi.put(`/schedules/${form.id}`, body);
      else await autoApi.post("/schedules", body);
      toast.success(editing ? "Schedule saved." : "Schedule created.");
      onSaved();
    } catch (err) {
      toast.error(errText(err, "Could not save the schedule."));
    } finally {
      setSaving(false);
    }
  };

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{editing ? "Edit schedule" : "New schedule"}</DialogTitle>
          <DialogDescription>
            Wall-clock times carry their own zone, so a schedule does not move when the server's
            does.
          </DialogDescription>
        </DialogHeader>

        <div className="flex max-h-[60vh] flex-col gap-3 overflow-y-auto pr-1">
          <div className="flex flex-col gap-1">
            <Label htmlFor="sch-name">Name</Label>
            <Input
              id="sch-name"
              value={form.name}
              placeholder="Weeknight show"
              onChange={(e) => setForm({ ...form, name: e.target.value })}
            />
          </div>

          <div className="grid grid-cols-2 gap-3">
            <div className="flex flex-col gap-1">
              <Label>Action</Label>
              <Select
                value={form.action}
                onValueChange={(v) => {
                  const action = v as ScheduleAction;
                  setForm({
                    ...form,
                    action,
                    // A playlist schedule that also names destinations is
                    // refused by the server, so a stale pick from before the
                    // switch must not ride along invisibly.
                    destinationIds: action.startsWith("playlist.")
                      ? []
                      : form.destinationIds,
                  });
                }}
              >
                <SelectTrigger>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="start">Start destinations</SelectItem>
                  <SelectItem value="stop">Stop destinations</SelectItem>
                  <SelectItem value="playlist.start">Start the playlist</SelectItem>
                  <SelectItem value="playlist.stop">Stop the playlist</SelectItem>
                </SelectContent>
              </Select>
            </div>
            <div className="flex flex-col gap-1">
              <Label>Repeats</Label>
              <Select
                value={form.kind}
                onValueChange={(v) => setForm({ ...form, kind: v as ScheduleKind })}
              >
                <SelectTrigger>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="once">Once</SelectItem>
                  <SelectItem value="daily">Every day</SelectItem>
                  <SelectItem value="weekly">Chosen weekdays</SelectItem>
                </SelectContent>
              </Select>
            </div>
          </div>

          {form.kind === "once" ? (
            <div className="flex flex-col gap-1">
              <Label htmlFor="sch-runat">Fires at</Label>
              <Input
                id="sch-runat"
                type="datetime-local"
                value={toLocalInput(form.runAt)}
                onChange={(e) => setForm({ ...form, runAt: fromLocalInput(e.target.value) })}
              />
              <span className="text-[10px] text-muted-foreground">
                Read in this browser's clock and stored as an instant, so the zone below does not
                apply to a one-shot.
              </span>
            </div>
          ) : (
            <>
              <div className="grid grid-cols-2 gap-3">
                <div className="flex flex-col gap-1">
                  <Label htmlFor="sch-time">Time</Label>
                  <Input
                    id="sch-time"
                    type="time"
                    value={hhmm(form.atMinutes)}
                    onChange={(e) =>
                      setForm({ ...form, atMinutes: minutesOf(e.target.value) })
                    }
                  />
                </div>
                <div className="flex flex-col gap-1">
                  <Label htmlFor="sch-tz">Time zone</Label>
                  <Input
                    id="sch-tz"
                    list="sch-tz-options"
                    value={form.tz}
                    spellCheck={false}
                    placeholder="UTC"
                    onChange={(e) => setForm({ ...form, tz: e.target.value })}
                  />
                  <datalist id="sch-tz-options">
                    <option value="UTC" />
                    <option value={browserZone()} />
                  </datalist>
                  <span className="text-[10px] text-muted-foreground">
                    IANA name. Empty means UTC.
                  </span>
                </div>
              </div>

              {form.kind === "weekly" && (
                <div className="flex flex-col gap-1.5">
                  <Label>Days</Label>
                  <div className="flex flex-wrap gap-3">
                    {WEEKDAYS.map((label, i) => (
                      <label key={label} className="flex items-center gap-1.5 text-[11px]">
                        <Checkbox checked={days.includes(i)} onCheckedChange={() => toggleDay(i)} />
                        {label}
                      </label>
                    ))}
                  </div>
                </div>
              )}
            </>
          )}

          {!form.action.startsWith("playlist.") && (
            <div className="flex flex-col gap-1.5">
              <Label>Destinations</Label>
              <p className="text-[10px] text-muted-foreground">
                None selected means every destination, which is what "start the show" usually
                means.
              </p>
              <div className="grid gap-1.5 sm:grid-cols-2">
                {(status?.destinations ?? []).map((d) => (
                  <label key={d.id} className="flex items-center gap-2 text-[11px]">
                    <Checkbox
                      checked={dests.includes(d.id)}
                      onCheckedChange={() => toggleDest(d.id)}
                    />
                    <span className="truncate">{d.name}</span>
                  </label>
                ))}
              </div>
            </div>
          )}

          <div className="flex flex-col gap-1">
            <Label htmlFor="sch-grace">Lateness allowance (seconds)</Label>
            <Input
              id="sch-grace"
              type="number"
              min={30}
              max={86400}
              value={form.graceSeconds}
              onChange={(e) => setForm({ ...form, graceSeconds: Number(e.target.value) })}
            />
            <span className="text-[10px] text-muted-foreground">
              How late an occurrence may still be acted on after a restart. Past it, the
              occurrence is skipped.
            </span>
          </div>
        </div>

        <DialogFooter>
          <Button variant="ghost" size="sm" onClick={onClose}>
            Cancel
          </Button>
          <Button size="sm" onClick={save} disabled={saving || !form.name.trim()}>
            {saving && <Loader2 className="animate-spin" />}
            {editing ? "Save schedule" : "Create schedule"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
