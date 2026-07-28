import { Fragment, useCallback, useEffect, useMemo, useState } from "react";
import { toast } from "sonner";
import { ConfirmDestructive, useConfirm } from "@/components/ConfirmDestructive";
import {
  AudioLines,
  ChevronDown,
  ChevronRight,
  Download,
  HardDrive,
  Loader2,
  Trash2,
} from "lucide-react";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
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
import { PageHeader } from "@/components/AppLayout";
import { Stat } from "@/components/signature/Stat";
import { useLiveData } from "@/hooks/useLiveData";
import { api } from "@/lib/api";
import { autoApi } from "@/pages/AutomationPage";
import { bytes, shortDuration, timestamp } from "@/lib/format";
import type { DiskUsage, Recording, Settings } from "@/lib/types";

/** One per-track file the recorder wrote beside a master segment.
 *
 *  `master` is the master's filename without its extension. Stems are joined to
 *  their master on that string rather than on a timestamp: a stem cuts on its
 *  own segment boundary while the master waits for a keyframe, so their stamps
 *  legitimately differ by a fraction of a second. */
interface StemFile {
  name: string;
  master: string;
  track: string;
  bytes: number;
  startedAt: string;
}

const stemDownloadUrl = (name: string) =>
  `/api/v1/recordings/stems/${encodeURIComponent(name)}/download`;

/** The master's join key: its filename with the extension taken off. */
function masterKey(filename: string): string {
  const dot = filename.lastIndexOf(".");
  return dot > 0 ? filename.slice(0, dot) : filename;
}

export function RecordingsPage() {
  const { recordingsRevision, status } = useLiveData();
  const [recordings, setRecordings] = useState<Recording[]>([]);
  const [usage, setUsage] = useState<DiskUsage | null>(null);
  const [settings, setSettings] = useState<Settings | null>(null);
  const [stems, setStems] = useState<StemFile[]>([]);
  const [expanded, setExpanded] = useState<number | null>(null);
  const [saving, setSaving] = useState(false);
  const [loading, setLoading] = useState(true);

  const load = useCallback(() => {
    Promise.all([
      api.listRecordings(),
      api.recordingUsage(),
      api.getSettings(),
      // Stems are files on disk rather than index rows, so they are listed
      // separately and joined here. A failure to read them must not cost the
      // recordings list, which is why it resolves to an empty list.
      autoApi.get<StemFile[]>("/recordings/stems").catch(() => [] as StemFile[]),
    ])
      .then(([r, u, s, st]) => {
        setRecordings(r);
        setUsage(u);
        setSettings(s);
        setStems(st ?? []);
      })
      .catch((err) => toast.error(err instanceof Error ? err.message : "Could not load recordings."))
      .finally(() => setLoading(false));
  }, []);

  useEffect(load, [load, recordingsRevision]);

  const stemsByMaster = useMemo(() => {
    const m = new Map<string, StemFile[]>();
    for (const s of stems) {
      const list = m.get(s.master);
      if (list) list.push(s);
      else m.set(s.master, [s]);
    }
    return m;
  }, [stems]);

  const stemBytes = useMemo(() => stems.reduce((n, s) => n + s.bytes, 0), [stems]);

  const confirmDelete = useConfirm<Recording>();

  const remove = async (rec: Recording) => {
    try {
      await api.deleteRecording(rec.id);
      toast.success("Recording deleted.");
      load();
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "Could not delete the recording.");
    }
  };

  const saveRetention = async (next: Settings) => {
    setSaving(true);
    try {
      setSettings(await api.putSettings(next));
      toast.success("Recording settings saved.");
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "Could not save the settings.");
    } finally {
      setSaving(false);
    }
  };

  // The stem fields are real on the wire but are not declared on the shared
  // Settings type yet, so they are read through a narrow view. Nothing is lost
  // in the round trip: the spread below carries keys the type does not name,
  // and the server merges a PUT onto the stored settings anyway.
  const recSettings = (settings?.recording ?? {}) as {
    stems?: boolean;
    stemCodec?: string;
  };

  const saveRecording = (patch: { stems?: boolean; stemCodec?: string }) => {
    if (!settings) return;
    void saveRetention({
      ...settings,
      recording: { ...settings.recording, ...patch },
    } as Settings);
  };

  const recorderState = status?.recorder?.state;

  return (
    <div className="p-3">
      <PageHeader
        title="Recordings"
        subtitle="The full multitrack archive: every ingest audio track preserved, unencoded."
        actions={
          <Badge variant={recorderState === "running" ? "live" : "outline"}>
            {recorderState === "running" ? "recording" : (recorderState ?? "disabled")}
          </Badge>
        }
      />

      <div className="grid gap-3 lg:grid-cols-[minmax(0,1fr)_18rem]">
        {/* ---------- file list ---------- */}
        <Card>
          <CardHeader>
            <CardTitle>Segments</CardTitle>
          </CardHeader>
          <CardContent className="px-0 pb-0">
            {loading ? (
              <div className="flex justify-center py-8">
                <Loader2 className="h-4 w-4 animate-spin text-muted-foreground" />
              </div>
            ) : recordings.length === 0 ? (
              <div className="px-3 py-8 text-center text-[12px] text-muted-foreground">
                No recordings yet. Enable recording on the right and start a stream.
              </div>
            ) : (
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>File</TableHead>
                    <TableHead>Started</TableHead>
                    <TableHead className="text-right">Duration</TableHead>
                    <TableHead className="text-center">Tracks</TableHead>
                    <TableHead className="text-center">Stems</TableHead>
                    <TableHead className="text-right">Size</TableHead>
                    <TableHead className="w-20" />
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {recordings.map((r) => {
                    const mine = stemsByMaster.get(masterKey(r.filename)) ?? [];
                    const open = expanded === r.id;
                    return (
                      <Fragment key={r.id}>
                        <TableRow>
                          <TableCell className="font-mono text-[11px]">
                            <div className="flex items-center gap-1">
                              {/* The disclosure only exists when there is
                                  something behind it; a chevron that opens an
                                  empty drawer is worse than no chevron. */}
                              {mine.length > 0 ? (
                                <button
                                  type="button"
                                  onClick={() => setExpanded(open ? null : r.id)}
                                  aria-expanded={open}
                                  aria-label={open ? "Hide stems" : "Show stems"}
                                  className="text-muted-foreground hover:text-foreground"
                                >
                                  {open ? (
                                    <ChevronDown className="h-3 w-3" />
                                  ) : (
                                    <ChevronRight className="h-3 w-3" />
                                  )}
                                </button>
                              ) : (
                                <span className="w-3" />
                              )}
                              {r.filename}
                            </div>
                          </TableCell>
                          <TableCell className="tnum font-mono text-[11px] text-muted-foreground">
                            {timestamp(r.startedAt)}
                          </TableCell>
                          {/* Both are measured only once the recorder has moved on
                              to the next segment, so the live one reads as dashes. */}
                          <TableCell className="tnum text-right font-mono text-[11px] text-muted-foreground">
                            {r.durationMs > 0 ? shortDuration(r.durationMs) : "—"}
                          </TableCell>
                          <TableCell className="text-center">
                            {r.tracks > 0 ? (
                              <Badge variant="outline" title={`${r.tracks} audio tracks preserved`}>
                                {r.tracks}
                              </Badge>
                            ) : (
                              <span className="text-[11px] text-muted-foreground">—</span>
                            )}
                          </TableCell>
                          <TableCell className="text-center">
                            {mine.length > 0 ? (
                              <Badge
                                variant="armed"
                                title={`${mine.length} per-track stem files, sample-aligned with the master`}
                              >
                                <AudioLines className="h-2.5 w-2.5" />
                                {mine.length}
                              </Badge>
                            ) : (
                              <span className="text-[11px] text-muted-foreground">—</span>
                            )}
                          </TableCell>
                          <TableCell className="tnum text-right font-mono text-[11px]">
                            {bytes(r.bytes)}
                          </TableCell>
                          <TableCell>
                            <div className="flex justify-end gap-0.5">
                              <Button variant="ghost" size="icon-sm" asChild aria-label="Download">
                                <a href={api.downloadUrl(r.id)} download>
                                  <Download />
                                </a>
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

                        {open && (
                          <TableRow>
                            <TableCell colSpan={7} className="bg-card-raised/40">
                              <div className="flex flex-col gap-1 py-1 pl-4">
                                {mine.map((s) => (
                                  <div
                                    key={s.name}
                                    className="flex items-center justify-between gap-2"
                                  >
                                    <span className="truncate font-mono text-[10px] text-muted-foreground">
                                      <span className="text-foreground">{s.track}</span> · {s.name}
                                    </span>
                                    <span className="flex shrink-0 items-center gap-1.5">
                                      <span className="tnum font-mono text-[10px] text-muted-foreground">
                                        {bytes(s.bytes)}
                                      </span>
                                      <Button
                                        variant="ghost"
                                        size="icon-sm"
                                        asChild
                                        aria-label={`Download ${s.name}`}
                                      >
                                        <a href={stemDownloadUrl(s.name)} download>
                                          <Download />
                                        </a>
                                      </Button>
                                    </span>
                                  </div>
                                ))}
                              </div>
                            </TableCell>
                          </TableRow>
                        )}
                      </Fragment>
                    );
                  })}
                </TableBody>
              </Table>
            )}
          </CardContent>
        </Card>

        {/* ---------- usage + retention ---------- */}
        <div className="flex flex-col gap-3">
          <Card>
            <CardHeader>
              <CardTitle className="flex items-center gap-1.5">
                <HardDrive className="h-3.5 w-3.5" /> Disk
              </CardTitle>
            </CardHeader>
            <CardContent className="grid grid-cols-2 gap-2">
              <Stat label="Recordings" value={usage?.count ?? 0} />
              <Stat label="Used" value={bytes(usage?.usedBytes ?? 0)} />
              <Stat
                label="Free"
                value={usage?.freeBytes ? bytes(usage.freeBytes) : "—"}
                tone={
                  usage && usage.freeBytes > 0 && usage.freeBytes < 5 * 1024 ** 3 ? "warn" : "muted"
                }
              />
              <Stat label="Volume" value={usage?.totalBytes ? bytes(usage.totalBytes) : "—"} tone="muted" />
              {/* Stems are not in the recordings index, so their bytes are not
                  in `used` either — showing them separately is the only way the
                  disk figures add up on a machine that writes them. */}
              {stems.length > 0 && (
                <Stat
                  className="col-span-2"
                  label={`Stems (${stems.length} files)`}
                  value={bytes(stemBytes)}
                  tone="muted"
                />
              )}
            </CardContent>
            {usage?.storage.halted && (
              <CardContent className="pt-0">
                <p className="rounded border border-down/50 bg-down/5 p-2 text-[11px] text-down">
                  {usage.storage.reason ?? "Recording is halted: the volume is below the free-space floor."}
                  {" "}The recorder restarts by itself once space is freed.
                </p>
              </CardContent>
            )}
          </Card>

          {settings && (
            <Card>
              <CardHeader>
                <CardTitle>Recording</CardTitle>
              </CardHeader>
              <CardContent className="flex flex-col gap-3">
                <div className="flex items-center justify-between">
                  <Label htmlFor="rec-enabled">Enabled</Label>
                  <Switch
                    id="rec-enabled"
                    checked={settings.recording.enabled}
                    onCheckedChange={(v) =>
                      saveRetention({
                        ...settings,
                        recording: { ...settings.recording, enabled: v },
                      })
                    }
                  />
                </div>

                {/* Stems multiply what a session writes by roughly the track
                    count, so this is off by default and stays that way through
                    an upgrade. */}
                <div className="flex items-center justify-between">
                  <Label htmlFor="rec-stems">Per-track stem files</Label>
                  <Switch
                    id="rec-stems"
                    checked={recSettings.stems ?? false}
                    onCheckedChange={(v) => saveRecording({ stems: v })}
                  />
                </div>

                {recSettings.stems && (
                  <div className="flex flex-col gap-1">
                    <Label>Stem format</Label>
                    <Select
                      value={recSettings.stemCodec || "flac"}
                      onValueChange={(v) => saveRecording({ stemCodec: v })}
                    >
                      <SelectTrigger>
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent>
                        <SelectItem value="flac">FLAC — lossless, about half the size</SelectItem>
                        <SelectItem value="wav">WAV — lossless, opens anywhere</SelectItem>
                      </SelectContent>
                    </Select>
                    <span className="text-[10px] text-muted-foreground">
                      Written from the same process as the master, so every stem stays
                      sample-aligned with the others. Tracks wider than 8 channels fall back to WAV
                      on their own.
                    </span>
                  </div>
                )}

                <div className="flex flex-col gap-1">
                  <Label htmlFor="rec-seg">Segment length (seconds)</Label>
                  <Input
                    id="rec-seg"
                    type="number"
                    min={10}
                    value={settings.recording.segmentSeconds}
                    onChange={(e) =>
                      setSettings({
                        ...settings,
                        recording: {
                          ...settings.recording,
                          segmentSeconds: Number(e.target.value),
                        },
                      })
                    }
                  />
                </div>

                <div className="flex flex-col gap-1">
                  <Label htmlFor="rec-gb">Keep at most (GB)</Label>
                  <Input
                    id="rec-gb"
                    type="number"
                    min={0}
                    step={0.5}
                    value={settings.recording.maxGb}
                    onChange={(e) =>
                      setSettings({
                        ...settings,
                        recording: { ...settings.recording, maxGb: Number(e.target.value) },
                      })
                    }
                  />
                  <span className="text-[10px] text-muted-foreground">0 = no size limit.</span>
                </div>

                <div className="flex flex-col gap-1">
                  <Label htmlFor="rec-age">Delete after (hours)</Label>
                  <Input
                    id="rec-age"
                    type="number"
                    min={0}
                    value={settings.recording.maxAgeHours}
                    onChange={(e) =>
                      setSettings({
                        ...settings,
                        recording: {
                          ...settings.recording,
                          maxAgeHours: Number(e.target.value),
                        },
                      })
                    }
                  />
                  <span className="text-[10px] text-muted-foreground">0 = keep forever.</span>
                </div>

                <div className="flex flex-col gap-1">
                  <Label htmlFor="rec-free">Stop below free space (GB)</Label>
                  <Input
                    id="rec-free"
                    type="number"
                    min={0}
                    step={0.5}
                    value={settings.recording.minFreeGb}
                    onChange={(e) =>
                      setSettings({
                        ...settings,
                        recording: {
                          ...settings.recording,
                          minFreeGb: Number(e.target.value),
                        },
                      })
                    }
                  />
                  <span className="text-[10px] text-muted-foreground">
                    The caps above only bound what polyemesis wrote; anything else on the volume can
                    still fill it. 0 = no floor.
                  </span>
                </div>

                <Button size="sm" onClick={() => saveRetention(settings)} disabled={saving}>
                  {saving && <Loader2 className="animate-spin" />}
                  Save retention policy
                </Button>
                <p className="text-[10px] text-muted-foreground">
                  Retention runs every 30 seconds: oldest first, by age then by total size. The
                  segment currently being written is never deleted.
                </p>
              </CardContent>
            </Card>
          )}
        </div>
      </div>

      <ConfirmDestructive
        open={confirmDelete.open}
        onOpenChange={confirmDelete.onOpenChange}
        subject={confirmDelete.target?.filename ?? ""}
        title="Delete this recording?"
        description="The file is removed from disk, along with its proxies, thumbnails and transcript. This cannot be undone."
        requireTyping
        onConfirm={async () => {
          if (confirmDelete.target) await remove(confirmDelete.target);
        }}
      />
    </div>
  );
}
