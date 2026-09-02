import { Fragment, useCallback, useEffect, useMemo, useState } from "react";
import { toast } from "sonner";
import { ConfirmDestructive } from "@/components/ConfirmDestructive";
import { useConfirm } from "@/hooks/useConfirm";
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
import { useT } from "@/lib/i18n";
import {
  failedRead,
  mayClaim,
  okRead,
  pendingRead,
  readFailed,
  rowsOf,
  type ReadState,
} from "@/lib/readState";
import { InfoHint } from "@/components/InfoHint";
import { autoApi } from "@/lib/autoApi";
import { bytes, shortDuration, timestamp } from "@/lib/format";
import {
  retentionDirty,
  retentionDraft,
  savePolicyBody,
  switchPatchBody,
  type RetentionDraft,
} from "@/lib/retentionDraft";
import type { DiskUsage, Recording, Settings, StemCodec } from "@/lib/types";

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
  const t = useT();
  const { recordingsRevision, status } = useLiveData();
  /* A READ, NOT AN ARRAY. An empty list from a FAILED GET /recordings is not
   * the same fact as an empty list from a successful one, and the empty state
   * below -- "no recordings" -- is a positive claim about a machine's disk.
   * Storing both as `[]` is the bug lib/readState.ts exists to forbid. */
  const [recRead, setRecRead] = useState<ReadState<Recording[]>>(pendingRead);
  const [usage, setUsage] = useState<DiskUsage | null>(null);
  /* The last thing the SERVER said. Nothing on this card writes to it except a
     completed save, which is what makes it safe for a switch to build a PUT
     from. */
  const [settings, setSettings] = useState<Settings | null>(null);
  /* The four retention numbers, held apart. They used to be edited straight
     into `settings`, and every switch on the card spread that same object -- so
     flipping Stems PUT a half-typed segment length with it. */
  const [draft, setDraft] = useState<RetentionDraft | null>(null);
  /* Same again, and this one was written down as deliberate: the stems GET
   * caught to `[]` so that losing it would not cost the recordings list. That
   * half is right and is kept. What it also did was make every row claim "no
   * per-track files" -- an em dash in the Stems column, no disclosure chevron
   * -- for a read that never came back. */
  const [stemRead, setStemRead] = useState<ReadState<StemFile[]>>(pendingRead);
  const [expanded, setExpanded] = useState<number | null>(null);
  const [saving, setSaving] = useState(false);

  const load = useCallback(() => {
    Promise.all([
      api.listRecordings(),
      api.recordingUsage(),
      api.getSettings(),
      // Stems are files on disk rather than index rows, so they are listed
      // separately and joined here. A failure to read them must not cost the
      // recordings list, which is why it resolves rather than rejecting -- but
      // it now resolves to a FAILED READ rather than to an empty list, so the
      // Stems column can say "unknown" instead of "none".
      autoApi
        .get<StemFile[]>("/recordings/stems")
        .then((st) => okRead(st ?? []))
        .catch(() => failedRead<StemFile[]>()),
    ])
      .then(([r, u, s, st]) => {
        setRecRead(okRead(r));
        setUsage(u);
        setSettings(s);
        setDraft(retentionDraft(s));
        setStemRead(st);
      })
      .catch((err) => {
        setRecRead(failedRead());
        setStemRead(failedRead());
        toast.error(err instanceof Error ? err.message : t("rec.loadFailed"));
      });
  }, [t]);

  useEffect(load, [load, recordingsRevision]);

  const recordings = rowsOf(recRead);
  const stems = rowsOf(stemRead);
  // True only when the server actually answered. Where it is false the column
  // says so rather than drawing a dash, which would read as "none".
  const stemsKnown = mayClaim(stemRead);

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
      toast.success(t("rec.deleted"));
      load();
    } catch (err) {
      toast.error(err instanceof Error ? err.message : t("rec.deleteFailed"));
    }
  };

  const saveRetention = async (next: Settings) => {
    setSaving(true);
    try {
      const saved = await api.putSettings(next);
      setSettings(saved);
      // Re-seeded from what came back, so the boxes show what is stored rather
      // than what was asked for -- the server clamps some of these.
      setDraft(retentionDraft(saved));
      toast.success(t("rec.saved"));
    } catch (err) {
      toast.error(err instanceof Error ? err.message : t("rec.saveFailed"));
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

  /* Every switch and select on this card goes through here, and it builds from
     `settings` -- the server's own last answer -- rather than from anything the
     operator is mid-way through typing. */
  const saveRecording = (patch: Partial<Settings["recording"]>) => {
    if (!settings) return;
    void saveRetention(switchPatchBody(settings, patch));
  };

  const dirty = settings ? retentionDirty(settings, draft) : false;

  const recorderState = status?.recorder?.state;

  return (
    <div className="p-3">
      <PageHeader
        title={t("rec.title")}
        subtitle={t("rec.subtitle")}
        actions={
          <Badge variant={recorderState === "running" ? "live" : "outline"}>
            {recorderState === "running" ? t("rec.recording") : (recorderState ?? t("rec.disabled"))}
          </Badge>
        }
      />

      <div className="grid gap-3 lg:grid-cols-[minmax(0,1fr)_18rem]">
        {/* ---------- file list ---------- */}
        <Card>
          <CardHeader>
            <CardTitle>{t("rec.segments")}</CardTitle>
          </CardHeader>
          <CardContent className="px-0 pb-0">
            {readFailed(recRead) ? (
              <div className="px-3 py-8 text-center text-[12px] text-warn">
                {t("rec.listUnread")}
              </div>
            ) : !mayClaim(recRead) ? (
              <div className="flex justify-center py-8">
                <Loader2 className="h-4 w-4 animate-spin text-muted-foreground" />
              </div>
            ) : recordings.length === 0 ? (
              <div className="px-3 py-8 text-center text-[12px] text-muted-foreground">
                {t("rec.empty")}
              </div>
            ) : (
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>{t("rec.file")}</TableHead>
                    <TableHead>{t("rec.started")}</TableHead>
                    <TableHead className="text-right">{t("rec.duration")}</TableHead>
                    <TableHead className="text-center">{t("rec.tracks")}</TableHead>
                    <TableHead className="text-center">{t("rec.stems")}</TableHead>
                    <TableHead className="text-right">{t("rec.size")}</TableHead>
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
                                  aria-label={open ? t("rec.hideStems") : t("rec.showStems")}
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
                              <Badge variant="outline" title={t("rec.tracksTitle", { count: r.tracks })}>
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
                                title={t("rec.stemsTitle", { count: mine.length })}
                              >
                                <AudioLines className="h-2.5 w-2.5" />
                                {mine.length}
                              </Badge>
                            ) : stemsKnown ? (
                              <span className="text-[11px] text-muted-foreground">—</span>
                            ) : (
                              // NOT a dash. The stems read did not come back,
                              // so "this segment has no per-track files" is a
                              // claim nobody is entitled to make here.
                              <span
                                className="text-[11px] text-warn"
                                title={t("rec.stemsUnread")}
                              >
                                ?
                              </span>
                            )}
                          </TableCell>
                          <TableCell className="tnum text-right font-mono text-[11px]">
                            {bytes(r.bytes)}
                          </TableCell>
                          <TableCell>
                            <div className="flex justify-end gap-0.5">
                              <Button variant="ghost" size="icon-sm" asChild aria-label={t("rec.download")}>
                                <a href={api.downloadUrl(r.id)} download>
                                  <Download />
                                </a>
                              </Button>
                              <Button
                                variant="ghost"
                                size="icon-sm"
                                onClick={() => confirmDelete.ask(r)}
                                aria-label={t("rec.delete")}
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
                                        aria-label={t("rec.downloadNamed", { name: s.name })}
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
                <HardDrive className="h-3.5 w-3.5" /> {t("rec.disk")}
              </CardTitle>
            </CardHeader>
            <CardContent className="grid grid-cols-2 gap-2">
              {/* All four follow the same rule. Two of them already did; the
                  other two printed "Recordings 0, Used 0 B" beside "Free —,
                  Volume —" when the usage read had simply failed, which reads
                  as an empty disk rather than as an unanswered question. */}
              <Stat labelKey="rec.diskRecordings" value={usage ? usage.count : "—"} />
              <Stat labelKey="rec.used" value={usage ? bytes(usage.usedBytes) : "—"} />
              <Stat
                labelKey="rec.free"
                value={usage?.freeBytes ? bytes(usage.freeBytes) : "—"}
                tone={
                  usage && usage.freeBytes > 0 && usage.freeBytes < 5 * 1024 ** 3 ? "warn" : "muted"
                }
              />
              <Stat labelKey="rec.volume" value={usage?.totalBytes ? bytes(usage.totalBytes) : "—"} tone="muted" />
              {/* Stems are not in the recordings index, so their bytes are not
                  in `used` either — showing them separately is the only way the
                  disk figures add up on a machine that writes them. */}
              {stemsKnown && stems.length > 0 && (
                <Stat
                  className="col-span-2"
                  labelKey="rec.stemsUsage" labelParams={{ count: stems.length }}
                  value={bytes(stemBytes)}
                  tone="muted"
                />
              )}
            </CardContent>
            {usage?.storage.halted && (
              <CardContent className="pt-0">
                <p className="rounded border border-down/50 bg-down/5 p-2 text-[11px] text-down">
                  {usage.storage.reason ?? t("rec.haltedDefault")}{" "}
                  {t("rec.haltedRestarts")}
                </p>
              </CardContent>
            )}
          </Card>

          {settings && (
            <Card>
              <CardHeader>
                <CardTitle>{t("rec.settingsTitle")}</CardTitle>
              </CardHeader>
              <CardContent className="flex flex-col gap-3">
                <div className="flex items-center justify-between">
                  <Label htmlFor="rec-enabled" className="flex items-center gap-1">
                    {t("rec.enabled")}
                    <InfoHint body="rec.help.enabled" title="rec.enabled" />
                  </Label>
                  <Switch
                    id="rec-enabled"
                    checked={settings.recording.enabled}
                    onCheckedChange={(v) => saveRecording({ enabled: v })}
                  />
                </div>

                {/* Stems multiply what a session writes by roughly the track
                    count, so this is off by default and stays that way through
                    an upgrade. */}
                <div className="flex items-center justify-between">
                  <Label htmlFor="rec-stems" className="flex items-center gap-1">
                    {t("rec.stemFiles")}
                    <InfoHint body="rec.help.stemFiles" title="rec.stemFiles" />
                  </Label>
                  <Switch
                    id="rec-stems"
                    checked={recSettings.stems ?? false}
                    onCheckedChange={(v) => saveRecording({ stems: v })}
                  />
                </div>

                {recSettings.stems && (
                  <div className="flex flex-col gap-1">
                    <Label className="flex items-center gap-1">
                      {t("rec.stemFormat")}
                      <InfoHint body="rec.help.stemFormat" title="rec.stemFormat" />
                    </Label>
                    <Select
                      value={recSettings.stemCodec || "flac"}
                      onValueChange={(v) => saveRecording({ stemCodec: v as StemCodec })}
                    >
                      <SelectTrigger>
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent>
                        <SelectItem value="flac">{t("rec.stemFlac")}</SelectItem>
                        <SelectItem value="wav">{t("rec.stemWav")}</SelectItem>
                      </SelectContent>
                    </Select>
                    <span className="text-[10px] text-muted-foreground">
                      {t("rec.stemNote")}
                    </span>
                  </div>
                )}

                <div className="flex flex-col gap-1">
                  <Label htmlFor="rec-seg" className="flex items-center gap-1">
                    {t("rec.segmentSeconds")}
                    <InfoHint body="rec.help.segmentSeconds" title="rec.segmentSeconds" />
                  </Label>
                  <Input
                    id="rec-seg"
                    type="number"
                    min={10}
                    value={draft?.segmentSeconds ?? settings.recording.segmentSeconds}
                    onChange={(e) =>
                      setDraft((d) => (d ? { ...d, segmentSeconds: Number(e.target.value) } : d))
                    }
                  />
                </div>

                <div className="flex flex-col gap-1">
                  <Label htmlFor="rec-gb" className="flex items-center gap-1">
                    {t("rec.maxGb")}
                    <InfoHint body="rec.help.maxGb" title="rec.maxGb" />
                  </Label>
                  <Input
                    id="rec-gb"
                    type="number"
                    min={0}
                    step={0.5}
                    value={draft?.maxGb ?? settings.recording.maxGb}
                    onChange={(e) =>
                      setDraft((d) => (d ? { ...d, maxGb: Number(e.target.value) } : d))
                    }
                  />
                  <span className="text-[10px] text-muted-foreground">{t("rec.noSizeLimit")}</span>
                </div>

                <div className="flex flex-col gap-1">
                  <Label htmlFor="rec-age" className="flex items-center gap-1">
                    {t("rec.maxAgeHours")}
                    <InfoHint body="rec.help.maxAgeHours" title="rec.maxAgeHours" />
                  </Label>
                  <Input
                    id="rec-age"
                    type="number"
                    min={0}
                    value={draft?.maxAgeHours ?? settings.recording.maxAgeHours}
                    onChange={(e) =>
                      setDraft((d) => (d ? { ...d, maxAgeHours: Number(e.target.value) } : d))
                    }
                  />
                  <span className="text-[10px] text-muted-foreground">{t("rec.keepForever")}</span>
                </div>

                <div className="flex flex-col gap-1">
                  <Label htmlFor="rec-free" className="flex items-center gap-1">
                    {t("rec.minFreeGb")}
                    <InfoHint body="rec.help.minFreeGb" title="rec.minFreeGb" />
                  </Label>
                  <Input
                    id="rec-free"
                    type="number"
                    min={0}
                    step={0.5}
                    value={draft?.minFreeGb ?? settings.recording.minFreeGb}
                    onChange={(e) =>
                      setDraft((d) => (d ? { ...d, minFreeGb: Number(e.target.value) } : d))
                    }
                  />
                  <span className="text-[10px] text-muted-foreground">
                    {t("rec.minFreeNote")}
                  </span>
                </div>

                {dirty && (
                  <span className="text-[10px] text-warn">{t("rec.unsavedRetention")}</span>
                )}
                <Button
                  size="sm"
                  onClick={() => draft && saveRetention(savePolicyBody(settings, draft))}
                  disabled={saving || !draft}
                >
                  {saving && <Loader2 className="animate-spin" />}
                  {t("rec.savePolicy")}
                </Button>
                <p className="text-[10px] text-muted-foreground">
                  {t("rec.retentionNote")}
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
        title={t("rec.deleteTitle")}
        description={t("rec.deleteDescription")}
        requireTyping
        onConfirm={async () => {
          if (confirmDelete.target) await remove(confirmDelete.target);
        }}
      />
    </div>
  );
}
