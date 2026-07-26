import { useCallback, useEffect, useState } from "react";
import { toast } from "sonner";
import { Download, HardDrive, Loader2, Trash2 } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
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
import { PageHeader } from "@/components/AppLayout";
import { Stat } from "@/components/signature/Stat";
import { useLiveData } from "@/hooks/useLiveData";
import { api } from "@/lib/api";
import { bytes, timestamp } from "@/lib/format";
import type { DiskUsage, Recording, Settings } from "@/lib/types";

export function RecordingsPage() {
  const { recordingsRevision, status } = useLiveData();
  const [recordings, setRecordings] = useState<Recording[]>([]);
  const [usage, setUsage] = useState<DiskUsage | null>(null);
  const [settings, setSettings] = useState<Settings | null>(null);
  const [saving, setSaving] = useState(false);
  const [loading, setLoading] = useState(true);

  const load = useCallback(() => {
    Promise.all([api.listRecordings(), api.recordingUsage(), api.getSettings()])
      .then(([r, u, s]) => {
        setRecordings(r);
        setUsage(u);
        setSettings(s);
      })
      .catch((err) => toast.error(err instanceof Error ? err.message : "Could not load recordings."))
      .finally(() => setLoading(false));
  }, []);

  useEffect(load, [load, recordingsRevision]);

  const remove = async (rec: Recording) => {
    if (!window.confirm(`Delete ${rec.filename}? This permanently removes the file.`)) return;
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
                    <TableHead className="text-right">Size</TableHead>
                    <TableHead className="w-20" />
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {recordings.map((r) => (
                    <TableRow key={r.id}>
                      <TableCell className="font-mono text-[11px]">{r.filename}</TableCell>
                      <TableCell className="tnum font-mono text-[11px] text-muted-foreground">
                        {timestamp(r.startedAt)}
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
                            onClick={() => remove(r)}
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
            </CardContent>
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
    </div>
  );
}
