import { useCallback, useEffect, useState } from "react";
import { toast } from "sonner";
import {
  AlertTriangle,
  Copy,
  Info,
  KeyRound,
  Loader2,
  Plus,
  RadioTower,
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
import { api } from "@/lib/api";
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

      <Dialog open={deleting !== null} onOpenChange={(o) => !o && setDeleting(null)}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Delete “{deleting?.name}”?</DialogTitle>
            <DialogDescription>
              Its destinations and renditions go with it — they describe where this
              programme goes and mean nothing without it. Recordings are kept: the
              files are still on disk and still playable, they just stop being
              attributed to a source.
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="ghost" onClick={() => setDeleting(null)}>
              Cancel
            </Button>
            <Button variant="destructive" onClick={() => void remove()}>
              Delete
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
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
  const ing = source.ingest;
  const setIngest = (changes: Partial<Source["ingest"]>) =>
    onPatch({ ingest: { ...ing, ...changes } });

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
                onCommit={(n) => setIngest({ srt: { ...ing.srt, port: n } })}
              />
              <NumberField
                label="Latency (ms)"
                value={ing.srt.latencyMs}
                onCommit={(n) => setIngest({ srt: { ...ing.srt, latencyMs: n } })}
              />
              <div className="flex flex-col gap-1">
                <Label>Passphrase</Label>
                <Input
                  className="h-7 text-[11px]"
                  type="password"
                  defaultValue={ing.srt.passphrase}
                  placeholder="10–79 chars"
                  onBlur={(e) =>
                    e.target.value !== ing.srt.passphrase &&
                    setIngest({ srt: { ...ing.srt, passphrase: e.target.value } })
                  }
                />
              </div>
            </>
          )}

          {ing.mode === "rtmp" && (
            <>
              <NumberField
                label="RTMP port"
                value={ing.rtmp.port}
                onCommit={(n) => setIngest({ rtmp: { ...ing.rtmp, port: n } })}
              />
              <div className="flex flex-col gap-1">
                <Label>App</Label>
                <Input
                  className="h-7 text-[11px]"
                  defaultValue={ing.rtmp.app}
                  onBlur={(e) =>
                    e.target.value !== ing.rtmp.app &&
                    setIngest({ rtmp: { ...ing.rtmp, app: e.target.value } })
                  }
                />
              </div>
              <div className="flex flex-col gap-1">
                <Label>Stream key</Label>
                <Input
                  className="h-7 text-[11px]"
                  defaultValue={ing.rtmp.streamKey}
                  onBlur={(e) =>
                    e.target.value !== ing.rtmp.streamKey &&
                    setIngest({ rtmp: { ...ing.rtmp, streamKey: e.target.value } })
                  }
                />
              </div>
            </>
          )}
        </div>

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
          {!source.tokenEnforced && (
            /* Stated plainly rather than omitted. An operator who rotates this
               believing it secures the ingest would be worse off than one who
               knows it currently does nothing. */
            <p className="flex items-start gap-1.5 text-[10px] text-muted-foreground">
              <Info className="mt-0.5 h-3 w-3 shrink-0" />
              This token is stored but <strong className="font-semibold">not yet enforced</strong>.
              Sources are kept apart by port. What actually protects this ingest is the{" "}
              {ing.mode === "rtmp" ? "stream key above" : "SRT passphrase above"}. The token
              becomes the credential when one-port publishing lands.
            </p>
          )}
        </div>
      </CardContent>
    </Card>
  );
}

/** A number input that commits on blur rather than per keystroke.
 *
 *  Per-keystroke would PUT a partial port — typing "6001" sends 6, 60, 600 —
 *  and each of those restarts the ingest. */
function NumberField({
  label,
  value,
  onCommit,
}: {
  label: string;
  value: number;
  onCommit: (n: number) => void;
}) {
  return (
    <div className="flex flex-col gap-1">
      <Label>{label}</Label>
      <Input
        className="tnum h-7 font-mono text-[11px]"
        type="number"
        defaultValue={value}
        onBlur={(e) => {
          const n = Number(e.target.value);
          if (Number.isFinite(n) && n !== value) onCommit(n);
        }}
      />
    </div>
  );
}
