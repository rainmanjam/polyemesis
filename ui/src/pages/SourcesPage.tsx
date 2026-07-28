import { useCallback, useEffect, useState } from "react";
import { toast } from "sonner";
import {
  Activity,
  AlertTriangle,
  Copy,
  Info,
  KeyRound,
  Loader2,
  Plus,
  RadioTower,
  ShieldCheck,
  Trash2,
} from "lucide-react";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { PageHeader } from "@/components/AppLayout";
import { ConfirmDestructive } from "@/components/ConfirmDestructive";
import { api } from "@/lib/api";
import { LIMITS } from "@/lib/limits";
import { cn } from "@/lib/utils";
import type { Source, SourceView } from "@/lib/types";

/* ===========================================================================
   Sources: one ingested programme each.

   This page exists because a horizontal and a vertical feed out of OBS's
   vertical-canvas plugin are two different compositions, not one cropped from
   the other. Before it, the answer to "I stream both" was "run two containers".

   The honesty problem this page has to solve: every source has a publish
   token, and "token" reads like authentication. It is not, yet. Sources are
   separated by PORT, because the listener is FFmpeg's and an FFmpeg SRT
   listener takes one caller per port and never reads streamid. What actually
   gates an ingest today is the RTMP stream key or the SRT passphrase. The
   server sends tokenEnforced so this page can say that out loud rather than
   let an operator believe rotating a token protected something.
   =========================================================================== */

function copy(text: string, what: string) {
  void navigator.clipboard
    ?.writeText(text)
    .then(() => toast.success(`${what} copied`))
    .catch(() => toast.error(`Could not copy the ${what.toLowerCase()}`));
}

export function SourcesPage() {
  const [sources, setSources] = useState<SourceView[]>([]);
  const [loading, setLoading] = useState(true);
  const [creating, setCreating] = useState(false);
  const [newName, setNewName] = useState("");
  const [deleting, setDeleting] = useState<SourceView | null>(null);
  const [busyId, setBusyId] = useState<number | null>(null);

  const load = useCallback(async () => {
    try {
      setSources(await api.listSources());
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Could not load sources");
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  const create = async () => {
    const name = newName.trim();
    if (!name) return;
    try {
      // Name only: the server fills in a default ingest, so adding a programme
      // does not require choosing ports before you have one to configure.
      await api.createSource({ name });
      setCreating(false);
      setNewName("");
      toast.success(`Added “${name}”. Set its ingest ports below.`);
      await load();
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Could not add the source");
    }
  };

  const patch = async (s: SourceView, changes: Partial<Source>) => {
    setBusyId(s.id);
    try {
      // Only the stored fields. Spreading the whole SourceView would send
      // publishUrls, isDefault, tokenEnforced and running back, and the server
      // rejects unknown fields — so the save 400s and the operator watches
      // their edit silently fail to stick.
      const body: Partial<Source> = {
        name: s.name,
        enabled: s.enabled,
        ingest: s.ingest,
        token: s.token,
        position: s.position,
        ...changes,
      };
      await api.updateSource(s.id, body);
      await load();
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Could not save");
    } finally {
      setBusyId(null);
    }
  };

  const rotate = async (s: SourceView) => {
    setBusyId(s.id);
    try {
      await api.rotateSourceToken(s.id);
      toast.success("New token issued");
      await load();
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Could not rotate the token");
    } finally {
      setBusyId(null);
    }
  };

  const remove = async () => {
    if (!deleting) return;
    try {
      await api.deleteSource(deleting.id);
      toast.success(`Deleted “${deleting.name}” and its destinations`);
      setDeleting(null);
      await load();
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Could not delete the source");
    }
  };

  return (
    <div className="p-3">
      <PageHeader
        title="Sources"
        subtitle="One ingested programme each. Destinations and renditions belong to a source."
        actions={
          <Button size="sm" onClick={() => setCreating(true)}>
            <Plus className="h-3.5 w-3.5" /> Add source
          </Button>
        }
      />

      {loading ? (
        <div className="flex items-center gap-2 py-6 text-[12px] text-muted-foreground">
          <Loader2 className="h-3.5 w-3.5 animate-spin" /> Loading sources…
        </div>
      ) : (
        <div className="flex flex-col gap-3">
          {sources.map((s) => (
            <SourceCard
              key={s.id}
              source={s}
              busy={busyId === s.id}
              onlyOne={sources.length === 1}
              onPatch={(c) => patch(s, c)}
              onRotate={() => rotate(s)}
              onDelete={() => setDeleting(s)}
            />
          ))}
        </div>
      )}

      <Dialog open={creating} onOpenChange={setCreating}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Add a source</DialogTitle>
            <DialogDescription>
              A second programme with its own ingest, destinations and renditions —
              a vertical canvas alongside a horizontal one, say. It starts on the
              default ports, which you will want to change so the two do not clash.
            </DialogDescription>
          </DialogHeader>
          <div className="flex flex-col gap-1.5">
            <Label htmlFor="src-name">Name</Label>
            <Input
              id="src-name"
              value={newName}
              placeholder="Vertical"
              onChange={(e) => setNewName(e.target.value)}
              onKeyDown={(e) => e.key === "Enter" && void create()}
            />
          </div>
          <DialogFooter>
            <Button variant="ghost" onClick={() => setCreating(false)}>
              Cancel
            </Button>
            <Button onClick={() => void create()} disabled={!newName.trim()}>
              Add
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <ConfirmDestructive
        open={deleting !== null}
        onOpenChange={(o) => !o && setDeleting(null)}
        subject={deleting?.name ?? ""}
        title={`Delete “${deleting?.name}”?`}
        description={
          <>
            Its destinations and renditions go with it — they describe where this
            programme goes and mean nothing without it. Recordings are kept: the
            files are still on disk and still playable, they just stop being
            attributed to a source.
          </>
        }
        requireTyping
        consequences={[
          { label: "Destinations", count: deleting?.destinations ?? 0 },
          { label: "Renditions", count: deleting?.renditions ?? 0 },
        ]}
        confirmLabel="Delete source"
        onConfirm={remove}
      />
    </div>
  );
}

function SourceCard({
  source,
  busy,
  onlyOne,
  onPatch,
  onRotate,
  onDelete,
}: {
  source: SourceView;
  busy: boolean;
  onlyOne: boolean;
  onPatch: (changes: Partial<Source>) => void;
  onRotate: () => void;
  onDelete: () => void;
}) {
  // A LOCAL DRAFT, not a per-field commit.
  //
  // These fields used to write straight through on blur, and every write
  // restarts this source's ingest. That turned tabbing out of a port field into
  // a dropped broadcast -- an outage as the side effect of a keystroke nobody
  // meant as a decision. Edits now accumulate here and go nowhere until Apply.
  const [draft, setDraft] = useState(source.ingest);
  useEffect(() => setDraft(source.ingest), [source.ingest]);

  const ing = draft;
  const setIngest = (changes: Partial<Source["ingest"]>) =>
    setDraft((d) => ({ ...d, ...changes }));
  const dirty = JSON.stringify(draft) !== JSON.stringify(source.ingest);

  return (
    <Card className={source.running ? undefined : "border-warn/40"}>
      <CardHeader className="flex-row items-start justify-between gap-2">
        <div className="min-w-0">
          <CardTitle className="flex items-center gap-2">
            <RadioTower className="h-3.5 w-3.5 shrink-0" />
            <span className="truncate">{source.name}</span>
            {source.isDefault && <Badge variant="outline">default</Badge>}
            <Badge variant={source.running ? "live" : "warn"}>
              {source.running ? "running" : "not running"}
            </Badge>
          </CardTitle>
          <CardDescription>
            {source.isDefault
              ? "Requests that do not name a source act on this one."
              : "Its own destinations, renditions and recordings."}
          </CardDescription>
        </div>
        <div className="flex shrink-0 items-center gap-2">
          {busy && <Loader2 className="h-3.5 w-3.5 animate-spin text-muted-foreground" />}
          <Switch
            checked={source.enabled}
            onCheckedChange={(v) => onPatch({ enabled: v })}
            aria-label={`Enable ${source.name}`}
          />
          <Button
            size="icon"
            variant="ghost"
            onClick={onDelete}
            disabled={onlyOne}
            title={
              onlyOne
                ? "The last source cannot be deleted — an install needs one ingest"
                : "Delete this source"
            }
            aria-label={`Delete ${source.name}`}
          >
            <Trash2 className="h-3.5 w-3.5" />
          </Button>
        </div>
      </CardHeader>

      <CardContent className="flex flex-col gap-3">
        {!source.running && (
          <p className="flex items-start gap-1.5 text-[11px] text-warn">
            <AlertTriangle className="mt-0.5 h-3 w-3 shrink-0" />
            No engine is running for this source. The usual cause is an ingest port
            already in use by another source or another process.
          </p>
        )}

        <div className="grid gap-2 sm:grid-cols-4">
          <div className="flex flex-col gap-1">
            <Label>Ingest</Label>
            <Select value={ing.mode} onValueChange={(v) => setIngest({ mode: v as typeof ing.mode })}>
              <SelectTrigger className="h-7 text-[11px]">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="srt">SRT</SelectItem>
                <SelectItem value="rtmp">RTMP</SelectItem>
                <SelectItem value="pull">Pull</SelectItem>
              </SelectContent>
            </Select>
          </div>

          {ing.mode === "srt" && (
            <>
              <NumberField
                label="SRT port"
                value={ing.srt.port}
                min={LIMITS.port.min}
                max={LIMITS.port.max}
                onChange={(n) => setIngest({ srt: { ...ing.srt, port: n } })}
              />
              <NumberField
                label="Latency (ms)"
                value={ing.srt.latencyMs}
                min={LIMITS.srtLatencyMs.min}
                max={LIMITS.srtLatencyMs.max}
                onChange={(n) => setIngest({ srt: { ...ing.srt, latencyMs: n } })}
              />
              <div className="flex flex-col gap-1">
                <Label>Passphrase</Label>
                <Input
                  className="h-7 text-[11px]"
                  type="password"
                  value={ing.srt.passphrase}
                  placeholder="10–79 chars"
                  onChange={(e) => setIngest({ srt: { ...ing.srt, passphrase: e.target.value } })}
                />
              </div>
            </>
          )}

          {ing.mode === "rtmp" && (
            <>
              <NumberField
                label="RTMP port"
                value={ing.rtmp.port}
                min={LIMITS.port.min}
                max={LIMITS.port.max}
                onChange={(n) => setIngest({ rtmp: { ...ing.rtmp, port: n } })}
              />
              <div className="flex flex-col gap-1">
                <Label>App</Label>
                <Input
                  className="h-7 text-[11px]"
                  value={ing.rtmp.app}
                  onChange={(e) => setIngest({ rtmp: { ...ing.rtmp, app: e.target.value } })}
                />
              </div>
              <div className="flex flex-col gap-1">
                <Label>Stream key</Label>
                <Input
                  className="h-7 text-[11px]"
                  value={ing.rtmp.streamKey}
                  onChange={(e) => setIngest({ rtmp: { ...ing.rtmp, streamKey: e.target.value } })}
                />
              </div>
            </>
          )}
        </div>

        {dirty && (
          /* The consequence, at the moment of decision. Applying reconciles
             this source's ingest, which restarts its FFmpeg child; if an
             encoder is connected that is a dropped broadcast. Stated on the
             button rather than in a dialog, because a label is read and a
             dialog is dismissed. */
          <div className="flex flex-wrap items-center gap-2 rounded-md border border-warn/40 bg-warn-dim/20 px-2 py-1.5">
            <AlertTriangle className="h-3.5 w-3.5 shrink-0 text-warn" />
            <span className="min-w-0 flex-1 text-[11px]">
              {source.publishing
                ? "An encoder is publishing to this source. Applying restarts its ingest and drops everyone watching."
                : "Unsaved ingest changes. Applying restarts this source's ingest."}
            </span>
            <Button size="sm" variant="ghost" onClick={() => setDraft(source.ingest)} disabled={busy}>
              Discard
            </Button>
            <Button
              size="sm"
              variant={source.publishing ? "destructive" : "default"}
              onClick={() => onPatch({ ingest: draft })}
              disabled={busy}
            >
              {source.publishing ? "Apply and drop the stream" : "Apply"}
            </Button>
          </div>
        )}

        {/* What to paste into the encoder. */}
        {Object.entries(source.publishUrls).map(([proto, url]) =>
          url ? (
            <div key={proto} className="flex items-center gap-2">
              <span className="w-20 shrink-0 text-[10px] uppercase tracking-wider text-subtle-foreground">
                {proto}
              </span>
              <code className="min-w-0 flex-1 truncate rounded bg-muted px-1.5 py-1 font-mono text-[10px]">
                {url}
              </code>
              <Button
                size="icon"
                variant="ghost"
                onClick={() => copy(url, proto.toUpperCase())}
                aria-label={`Copy the ${proto} URL`}
              >
                <Copy className="h-3 w-3" />
              </Button>
            </div>
          ) : null,
        )}

        {source.link && (
          /* Per-source uplink health. With several programmes on one install,
             "why is it breaking up" is a question about one encoder's uplink,
             and answering it per programme is something Restreamer's UI does
             not do. */
          <div className="flex flex-wrap items-center gap-3 rounded-md border border-live/30 bg-live-dim/20 px-2 py-1.5">
            <span className="flex items-center gap-1.5 text-[10px] font-semibold uppercase tracking-wider text-live">
              <Activity className="h-3 w-3" /> publishing
            </span>
            <span className="font-mono text-[10px] text-muted-foreground">{source.link.peer}</span>
            <span className="tnum font-mono text-[10px]">RTT {source.link.rttMs.toFixed(1)} ms</span>
            <span className="tnum font-mono text-[10px]">loss {source.link.lossPackets}</span>
            <span className="tnum font-mono text-[10px]">retrans {source.link.retransPackets}</span>
          </div>
        )}

        {/* The token, and the truth about it. */}
        <div className="flex flex-col gap-1.5 border-t border-border pt-2">
          <div className="flex items-center gap-2">
            <span className="w-20 shrink-0 text-[10px] uppercase tracking-wider text-subtle-foreground">
              token
            </span>
            <code className="min-w-0 flex-1 truncate rounded bg-muted px-1.5 py-1 font-mono text-[10px]">
              {source.token}
            </code>
            <Button
              size="icon"
              variant="ghost"
              onClick={() => copy(source.token, "Token")}
              aria-label="Copy the token"
            >
              <Copy className="h-3 w-3" />
            </Button>
            <Button size="sm" variant="outline" onClick={onRotate} disabled={busy}>
              <KeyRound className="h-3 w-3" /> Rotate
            </Button>
          </div>
          {/* Stated plainly either way. Telling someone a rotated token
              secures an ingest it does not is the worse error, but hiding that
              it now does is also wrong — so the copy follows the server's
              tokenEnforced, which follows the running listener rather than the
              setting. */}
          {source.tokenEnforced ? (
            <p className="flex items-start gap-1.5 text-[10px] text-muted-foreground">
              <ShieldCheck className="mt-0.5 h-3 w-3 shrink-0 text-live" />
              This token <strong className="font-semibold">is the credential</strong>. Put it in
              your encoder’s SRT <code className="font-mono">streamid</code> to publish to this
              source on the shared port. Rotating issues a new one and keeps the old working for
              five minutes, so you can move across without dropping a live stream.
            </p>
          ) : (
            <p className="flex items-start gap-1.5 text-[10px] text-muted-foreground">
              <Info className="mt-0.5 h-3 w-3 shrink-0" />
              This token is stored but <strong className="font-semibold">not enforced</strong> right
              now — sources are kept apart by port. What actually protects this ingest is the{" "}
              {ing.mode === "rtmp" ? "stream key above" : "SRT passphrase above"}. Turn on one-port
              ingest in Settings to make the token the credential.
            </p>
          )}
        </div>
      </CardContent>
    </Card>
  );
}

/** A bounded number input.
 *
 *  min/max/step are on the element itself, so the browser refuses an
 *  out-of-range value before it can be submitted. The server validates the same
 *  ranges -- that is the real guarantee -- but a form that accepts 70000 into a
 *  port field and reports the problem a round trip later has made the operator
 *  do the checking. Refusing the keystroke is the control; validating the
 *  request is the backstop.
 *
 *  Bounds come from lib/limits.ts, which mirrors the Go constants in one place
 *  rather than being retyped per field. */
function NumberField({
  label,
  value,
  min,
  max,
  onChange,
}: {
  label: string;
  value: number;
  min: number;
  max: number;
  onChange: (n: number) => void;
}) {
  const bad = value < min || value > max;
  return (
    <div className="flex flex-col gap-1">
      <Label>
        {label}
        <span className="ml-1 font-normal text-subtle-foreground">
          {min}–{max}
        </span>
      </Label>
      <Input
        className={cn("tnum h-7 font-mono text-[11px]", bad && "border-down")}
        type="number"
        inputMode="numeric"
        min={min}
        max={max}
        step={1}
        value={value}
        onChange={(e) => {
          const n = Number(e.target.value);
          if (Number.isFinite(n)) onChange(n);
        }}
      />
    </div>
  );
}
